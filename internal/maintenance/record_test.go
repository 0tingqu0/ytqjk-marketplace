package maintenance

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
)

func TestStrictRecordJSONRejectsDuplicateUnknownAndTrailingData(t *testing.T) {
	tests := []struct {
		name   string
		mutate func([]byte) []byte
	}{
		{
			name: "duplicate",
			mutate: func(data []byte) []byte {
				return bytes.Replace(
					data, []byte("\"schema\":"),
					[]byte("\"schema\":\"ytqjk-maintenance-record/v2\",\"schema\":"), 1,
				)
			},
		},
		{
			name: "unknown",
			mutate: func(data []byte) []byte {
				return bytes.Replace(data, []byte("\"schema\":"), []byte("\"unknown\":true,\"schema\":"), 1)
			},
		},
		{name: "trailing", mutate: func(data []byte) []byte { return append(data, []byte("{}")...) }},
	}
	for _, item := range tests {
		t.Run(item.name, func(t *testing.T) {
			scope := newTestScope(t)
			permit, err := AcquireShared(context.Background(), scope)
			if err != nil {
				t.Fatal(err)
			}
			if err := permit.Release(); err != nil {
				t.Fatal(err)
			}
			control, err := normalizeScope(scope)
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(control.recordPath)
			if err != nil {
				t.Fatal(err)
			}
			mutated := item.mutate(data)
			if bytes.Equal(mutated, data) {
				t.Fatal("test mutation did not change JSON")
			}
			if err := os.WriteFile(control.recordPath, mutated, 0o600); err != nil {
				t.Fatal(err)
			}
			_, err = AcquireShared(context.Background(), scope)
			assertCode(t, err, CodeStateCorrupt)
		})
	}
}

func TestPostCommitErrorLeavesOldOrNewRecordSafeWithoutMarker(t *testing.T) {
	for _, committed := range []bool{false, true} {
		name := "old"
		if committed {
			name = "new"
		}
		t.Run(name, func(t *testing.T) {
			scope := newTestScope(t)
			permit, err := AcquireShared(context.Background(), scope)
			if err != nil {
				t.Fatal(err)
			}
			if err := permit.Release(); err != nil {
				t.Fatal(err)
			}
			original := writeRecordJSON
			writeRecordJSON = func(control controlPlane, value any) error {
				if committed {
					if err := original(control, value); err != nil {
						return err
					}
				}
				return &safeio.PostCommitError{Operation: "test transition", Err: errors.New("sync failed")}
			}
			t.Cleanup(func() { writeRecordJSON = original })
			drainer, err := BeginDrain(context.Background(), scope, exclusiveOptions(operationA))
			writeRecordJSON = original
			if committed {
				if err != nil || drainer == nil {
					t.Fatalf("exact readback did not return drainer: drainer=%v err=%v", drainer, err)
				}
			} else {
				assertCode(t, err, CodeDurabilityUnknown)
			}
			control, err := normalizeScope(scope)
			if err != nil {
				t.Fatal(err)
			}
			record, exists, err := readRecord(control)
			if err != nil || !exists || !validRecord(record) {
				t.Fatalf("safe record: exists=%v err=%v record=%#v", exists, err, record)
			}
			if committed && record.State != StateDraining {
				t.Fatalf("committed state = %s, want DRAINING", record.State)
			}
			if !committed && record.State != StateOpen {
				t.Fatalf("uncommitted state = %s, want OPEN", record.State)
			}
			if committed {
				if _, err := drainer.Abort(); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := os.Stat(filepath.Join(control.directory, "durability.uncertain")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("permanent durability marker exists: %v", err)
			}
		})
	}
}
