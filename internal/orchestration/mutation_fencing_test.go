package orchestration

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestMutationTransitionRejectsInFlightAcrossConnections(t *testing.T) {
	for _, target := range []string{"PAUSED", "STOPPED", "DONE", "BLOCKED"} {
		t.Run(target, func(t *testing.T) {
			ledger, session, token := newTestAttestation(t, true)
			if err := ledger.Grant(Grant{
				RunID: token.RunID, SessionKey: session, Role: "director",
				Capabilities: []string{"run:lifecycle"},
			}, session); err != nil {
				t.Fatal(err)
			}
			peer := openPeerTestLedger(t, ledger)
			operationStarted := make(chan struct{})
			releaseOperation := make(chan struct{})
			mutationDone := make(chan error, 1)
			go func() {
				mutationDone <- ledger.ExecuteMutation(token, session, func() error {
					close(operationStarted)
					<-releaseOperation
					return nil
				})
			}()
			<-operationStarted

			startedAt := time.Now()
			_, transitionErr := peer.Transition(token.RunID, session, target, 0)
			elapsed := time.Since(startedAt)
			if transitionErr == nil || transitionErr.Error() != mutationInFlightMessage {
				close(releaseOperation)
				<-mutationDone
				t.Fatalf("Transition() error = %v, want %q", transitionErr, mutationInFlightMessage)
			}
			if elapsed >= 2*time.Second {
				close(releaseOperation)
				<-mutationDone
				t.Fatalf("Transition() took %v while callback was in flight", elapsed)
			}
			blocked, err := peer.Run(token.RunID)
			if err != nil || blocked.State != "RUNNING" || blocked.InFlightMutations != 1 {
				close(releaseOperation)
				<-mutationDone
				t.Fatalf("blocked run = %#v, %v", blocked, err)
			}

			close(releaseOperation)
			mutationErr := <-mutationDone
			if mutationErr != nil {
				t.Fatalf("ExecuteMutation() error = %v", mutationErr)
			}
			transitioned, err := peer.Transition(token.RunID, session, target, 0)
			if err != nil || transitioned.State != target || transitioned.InFlightMutations != 0 {
				t.Fatalf("retried Transition() = %#v, %v", transitioned, err)
			}
		})
	}
}

func TestMutationTransitionBeforeClaimPreventsCallback(t *testing.T) {
	ledger, session, token := newTestAttestation(t, true)
	if err := ledger.Grant(Grant{
		RunID: token.RunID, SessionKey: session, Role: "director",
		Capabilities: []string{"run:lifecycle"},
	}, session); err != nil {
		t.Fatal(err)
	}
	peer := openPeerTestLedger(t, ledger)
	if _, err := peer.Transition(token.RunID, session, "STOPPED", 0); err != nil {
		t.Fatal(err)
	}
	operations := 0
	if err := ledger.ExecuteMutation(token, session, func() error {
		operations++
		return nil
	}); err == nil {
		t.Fatal("ExecuteMutation() accepted a stopped run")
	}
	if operations != 0 {
		t.Fatalf("operation count = %d, want 0", operations)
	}
}

func TestMutationFailureAndInterruptedOutcomeAreNotReplayable(t *testing.T) {
	t.Run("callback failure", func(t *testing.T) {
		ledger, session, token := newTestAttestation(t, true)
		operationErr := errors.New("operation failed")
		operations := 0
		err := ledger.ExecuteMutation(token, session, func() error {
			operations++
			return operationErr
		})
		if !errors.Is(err, operationErr) {
			t.Fatalf("ExecuteMutation() error = %v", err)
		}
		assertMutationCannotReplay(t, ledger, token, session, &operations)
		run, err := ledger.Run(token.RunID)
		if err != nil || run.InFlightMutations != 0 {
			t.Fatalf("failed mutation run = %#v, %v", run, err)
		}
	})

	t.Run("outcome audit failure", func(t *testing.T) {
		ledger, session, token := newTestAttestation(t, true)
		if _, err := ledger.database.Exec(`CREATE TRIGGER reject_mutation_outcome
BEFORE INSERT ON audit_events WHEN NEW.kind IN ('mutation_completed','mutation_failed')
BEGIN SELECT RAISE(ABORT,'outcome unavailable'); END`); err != nil {
			t.Fatal(err)
		}
		operations := 0
		err := ledger.ExecuteMutation(token, session, func() error {
			operations++
			return nil
		})
		if err == nil || !strings.Contains(err.Error(), "persist mutation outcome") {
			t.Fatalf("ExecuteMutation() error = %v", err)
		}
		assertMutationCannotReplay(t, ledger, token, session, &operations)
		run, err := ledger.Run(token.RunID)
		if err != nil || run.InFlightMutations != 1 {
			t.Fatalf("unknown mutation run = %#v, %v", run, err)
		}
		assertLifecycleBlockedByMutation(t, ledger, token, session)
	})

	t.Run("crash after durable fence", func(t *testing.T) {
		ledger, session, token := newTestAttestation(t, true)
		bindingHash, err := ledger.validateAttestation(token, session)
		if err != nil {
			t.Fatal(err)
		}
		if err := ledger.beginMutation(token, bindingHash); err != nil {
			t.Fatal(err)
		}
		peer := openPeerTestLedger(t, ledger)
		operations := 0
		assertMutationCannotReplay(t, peer, token, session, &operations)
		run, err := peer.Run(token.RunID)
		if err != nil || run.InFlightMutations != 1 {
			t.Fatalf("interrupted mutation run = %#v, %v", run, err)
		}
		assertLifecycleBlockedByMutation(t, peer, token, session)
	})
}

func assertLifecycleBlockedByMutation(t *testing.T, ledger *Ledger, token Attestation, session string) {
	t.Helper()
	if err := ledger.Grant(Grant{
		RunID: token.RunID, SessionKey: session, Role: "director",
		Capabilities: []string{"run:lifecycle"},
	}, session); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Transition(token.RunID, session, "PAUSED", 0); err == nil || err.Error() != mutationInFlightMessage {
		t.Fatalf("Transition() error = %v, want %q", err, mutationInFlightMessage)
	}
	run, err := ledger.Run(token.RunID)
	if err != nil || run.State != "RUNNING" || run.InFlightMutations != 1 {
		t.Fatalf("blocked lifecycle run = %#v, %v", run, err)
	}
}

func assertMutationCannotReplay(
	t *testing.T,
	ledger *Ledger,
	token Attestation,
	session string,
	operations *int,
) {
	t.Helper()
	before := *operations
	err := ledger.ExecuteMutation(token, session, func() error {
		(*operations)++
		return nil
	})
	if err == nil || err.Error() != inactiveLeaseMessage {
		t.Fatalf("replayed ExecuteMutation() error = %v", err)
	}
	if *operations != before {
		t.Fatalf("replayed operation count = %d, want %d", *operations, before)
	}
}

func openPeerTestLedger(t *testing.T, ledger *Ledger) *Ledger {
	t.Helper()
	var sequence int
	var name, databasePath string
	if err := ledger.database.QueryRow("PRAGMA database_list").Scan(&sequence, &name, &databasePath); err != nil {
		t.Fatal(err)
	}
	peer, _, err := Open(databasePath, ledger.keyPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := peer.Close(); err != nil {
			t.Errorf("peer Close() error = %v", err)
		}
	})
	return peer
}
