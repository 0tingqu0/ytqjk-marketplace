package dashboard

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/0tingqu0/ytqjk-marketplace/internal/maintenance"
	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
)

func (s *Server) initializeStartup(ctx context.Context) error {
	permit, err := s.acquireSharedPermit(ctx)
	if err != nil {
		return fmt.Errorf("dashboard startup admission: %w", err)
	}
	admittedContext, err := maintenance.WithShared(ctx, permit)
	if err != nil {
		_ = permit.Release()
		return fmt.Errorf("dashboard startup admission context: %w", err)
	}
	actionCommitted := false
	commit := permit.Commit
	if s.startupCommit != nil {
		commit = func(action func(maintenance.Fence) error) error {
			return s.startupCommit(permit, action)
		}
	}
	err = commit(func(maintenance.Fence) error {
		actionErr := s.runStartupActions(admittedContext)
		if actionErr != nil {
			return errors.Join(actionErr, s.cleanupStartupResources())
		}
		actionCommitted = true
		return nil
	})
	if err == nil {
		return nil
	}
	if s.logger != nil {
		s.logger.Printf(
			"dashboard startup maintenance admission finalization failed: action_committed=%t error=%v",
			actionCommitted, err,
		)
	}
	if actionCommitted {
		return fmt.Errorf("dashboard startup result is unknown: %w", err)
	}
	return fmt.Errorf("dashboard startup failed: %w", err)
}

func (s *Server) runStartupActions(ctx context.Context) error {
	if err := s.ensureStores(); err != nil {
		return fmt.Errorf("initialize dashboard stores: %w", err)
	}
	if err := s.startPeerRuntime(); err != nil {
		return fmt.Errorf("start peer runtime: %w", err)
	}
	if err := s.resumeIntakeJobs(ctx); err != nil {
		return fmt.Errorf("resume intake jobs: %w", err)
	}
	pid := []byte(strconv.Itoa(os.Getpid()) + "\n")
	if err := safeio.AtomicWrite(s.pidPath(), pid, 0o600); err != nil {
		return fmt.Errorf("publish dashboard pid: %w", err)
	}
	return nil
}

func (s *Server) cleanupStartupResources() error {
	s.stopPeerRuntime()
	s.closeStores()
	err := os.Remove(s.pidPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (s *Server) shutdown(ctx context.Context) {
	permit, err := s.acquireSharedPermit(ctx)
	if err == nil {
		err = permit.Commit(func(maintenance.Fence) error {
			return s.cleanupStartupResources()
		})
	}
	if err == nil {
		return
	}
	// These operations only stop process-local activity. SQLite close and PID
	// removal remain untouched when durable shutdown admission is unavailable.
	s.stopPeerRuntime()
	s.stopIntakeWorkers()
	if s.logger != nil {
		s.logger.Printf("dashboard persistent shutdown cleanup skipped or uncertain: error=%v", err)
	}
}

func (s *Server) pidPath() string {
	return filepath.Join(s.KnowledgeRoot, "dashboard.pid")
}
