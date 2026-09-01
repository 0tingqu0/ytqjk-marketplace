package maintenance

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestBeginDrainSeparatesAdmissionFromWriterDrain(t *testing.T) {
	scope := newTestScope(t)
	permit, err := AcquireShared(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	drainer, err := BeginDrain(context.Background(), scope, exclusiveOptions(operationA))
	if err != nil {
		t.Fatal(err)
	}
	assertRecord(t, scope, func(record Record) bool {
		return record.State == StateDraining && record.Generation == 0
	})
	if _, err := AcquireShared(context.Background(), scope); !IsCode(err, CodeActive) {
		t.Fatalf("new shared permit error = %v", err)
	}
	if err := permit.Commit(func(fence Fence) error {
		if fence.Generation != 0 || fence.OperationID != operationA {
			t.Fatalf("draining fence = %#v", fence)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	lease, err := AwaitExclusive(context.Background(), drainer)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lease.Complete(OutcomeAborted); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentBeginDrainHasSingleOwner(t *testing.T) {
	scope := newTestScope(t)
	start := make(chan struct{})
	results := make(chan struct {
		drainer *Drainer
		err     error
	}, 2)
	var group sync.WaitGroup
	for _, operationID := range []string{operationA, operationB} {
		group.Add(1)
		go func(id string) {
			defer group.Done()
			<-start
			drainer, err := BeginDrain(context.Background(), scope, exclusiveOptions(id))
			results <- struct {
				drainer *Drainer
				err     error
			}{drainer: drainer, err: err}
		}(operationID)
	}
	close(start)
	group.Wait()
	close(results)
	var winner *Drainer
	activeErrors := 0
	for result := range results {
		if result.err == nil {
			winner = result.drainer
		} else if IsCode(result.err, CodeActive) {
			activeErrors++
		} else {
			t.Fatalf("unexpected BeginDrain error = %v", result.err)
		}
	}
	if winner == nil || activeErrors != 1 {
		t.Fatalf("winner=%v active errors=%d", winner != nil, activeErrors)
	}
	if _, err := winner.Abort(); err != nil {
		t.Fatal(err)
	}
}

func TestDrainerAbortIsIdempotentAndPreservesBaseGeneration(t *testing.T) {
	scope := newTestScope(t)
	drainer, err := BeginDrain(context.Background(), scope, exclusiveOptions(operationA))
	if err != nil {
		t.Fatal(err)
	}
	first, err := drainer.Abort()
	if err != nil {
		t.Fatal(err)
	}
	originalResource := first.Resources[0]
	first.Resources[0] = "tampered"
	second, err := drainer.Abort()
	if err != nil {
		t.Fatal(err)
	}
	if first.OperationID != second.OperationID || first.Generation != second.Generation ||
		first.Outcome != second.Outcome || second.Resources[0] != originalResource ||
		!first.FinishedAt.Equal(second.FinishedAt) || first.Generation != 0 || first.Outcome != OutcomeAborted {
		t.Fatalf("abort receipts = %#v %#v", first, second)
	}
	assertRecord(t, scope, func(record Record) bool {
		return record.State == StateOpen && record.Generation == 0
	})
}

func TestAwaitExclusivePrecheckFailureAttemptsExactAbort(t *testing.T) {
	scope := newTestScope(t)
	drainer, err := BeginDrain(context.Background(), scope, exclusiveOptions(operationA))
	if err != nil {
		t.Fatal(err)
	}
	control, err := normalizeScope(scope)
	if err != nil {
		t.Fatal(err)
	}
	guard, err := acquirePlaneLock(
		context.Background(), control, control.guardPath, true, lockDeadline(context.Background()),
	)
	if err != nil {
		t.Fatal(err)
	}
	released := make(chan error, 1)
	go func() {
		time.Sleep(35 * time.Millisecond)
		released <- joinUnlock(nil, guard)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Millisecond)
	defer cancel()
	if _, err := AwaitExclusive(ctx, drainer); err == nil {
		t.Fatal("AwaitExclusive unexpectedly succeeded")
	}
	if err := <-released; err != nil {
		t.Fatal(err)
	}
	assertRecord(t, scope, func(record Record) bool {
		return record.State == StateOpen && record.Generation == 0 &&
			record.Receipt != nil && record.Receipt.Outcome == OutcomeAborted
	})
}

func TestAwaitExclusiveTimeoutAbortsDraining(t *testing.T) {
	scope := newTestScope(t)
	permit, err := AcquireShared(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	defer permit.Release()
	options := exclusiveOptions(operationA)
	options.DrainTimeout = 60 * time.Millisecond
	drainer, err := BeginDrain(context.Background(), scope, options)
	if err != nil {
		t.Fatal(err)
	}
	_, err = AwaitExclusive(context.Background(), drainer)
	assertCode(t, err, CodeWriterDrainTimeout)
	assertRecord(t, scope, func(record Record) bool {
		return record.State == StateOpen && record.Generation == 0 &&
			record.Receipt != nil && record.Receipt.Outcome == OutcomeAborted
	})
}

func TestDrainerRevisionBindingRejectsCopiedStaleHandle(t *testing.T) {
	scope := newTestScope(t)
	drainer, err := BeginDrain(context.Background(), scope, exclusiveOptions(operationA))
	if err != nil {
		t.Fatal(err)
	}
	staleRecord := cloneRecord(drainer.record)
	staleRecord.Revision--
	stale := &Drainer{control: drainer.control, record: staleRecord, owner: drainer.owner}
	stale.self = stale
	if _, err := AwaitExclusive(context.Background(), stale); !IsCode(err, CodeRecoveryRequired) {
		t.Fatalf("stale drainer error = %v", err)
	}
	if _, err := drainer.Abort(); err != nil {
		t.Fatal(err)
	}
}
