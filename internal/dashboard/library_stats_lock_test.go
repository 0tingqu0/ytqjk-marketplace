package dashboard

import (
	"testing"
	"time"

	"github.com/0tingqu0/ytqjk-marketplace/internal/library"
)

func TestGroupLibraryStatisticsReadLockExcludesGenerationSwitch(t *testing.T) {
	server := &Server{}
	readerEntered := make(chan struct{})
	releaseReader := make(chan struct{})
	readerDone := make(chan error, 1)
	go func() {
		_, err := server.withGroupIndexReadLock(func() (library.Statistics, error) {
			close(readerEntered)
			<-releaseReader
			return library.Statistics{}, nil
		})
		readerDone <- err
	}()

	waitForDashboardSignal(t, readerEntered, "group statistics reader")
	if server.groupIndexMu.TryLock() {
		server.groupIndexMu.Unlock()
		close(releaseReader)
		<-readerDone
		t.Fatal("generation writer acquired the lock while group statistics were being read")
	}

	close(releaseReader)
	if err := <-readerDone; err != nil {
		t.Fatalf("group statistics read failed: %v", err)
	}
	if !server.groupIndexMu.TryLock() {
		t.Fatal("generation writer remained blocked after group statistics read completed")
	}
	server.groupIndexMu.Unlock()
}

func waitForDashboardSignal(t *testing.T, signal <-chan struct{}, operation string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", operation)
	}
}
