package dashboard

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/0tingqu0/ytqjk-marketplace/internal/peer"
)

type PeerRuntimeStatus struct {
	Status   string `json:"status"`
	BindHost string `json:"bind_host,omitempty"`
	Port     int    `json:"port,omitempty"`
}

func (s *Server) startPeerRuntime() error {
	status := PeerRuntimeStatus{Status: "NOT_CONFIGURED"}
	if err := s.ensureStores(); err != nil {
		status.Status = "FAILED"
		s.setPeerRuntime(status)
		return err
	}
	settings, err := s.peerStore.Load(context.Background())
	if errors.Is(err, peer.ErrNotConfigured) {
		s.setPeerRuntime(status)
		return nil
	}
	if err != nil {
		status.Status = "FAILED"
		s.setPeerRuntime(status)
		return err
	}
	status.BindHost, status.Port = settings.BindHost, settings.Port
	if !settings.Enabled {
		status.Status = "DISABLED"
		s.setPeerRuntime(status)
		return nil
	}
	address := net.JoinHostPort(settings.BindHost, strconv.Itoa(settings.Port))
	listener, err := net.Listen("tcp", address)
	if err != nil {
		status.Status = "FAILED"
		s.setPeerRuntime(status)
		if s.logger != nil {
			s.logger.Printf("peer runtime failed to listen: error=%v", err)
		}
		return err
	}
	peerHandler := &peer.Handler{
		KnowledgeRoot: s.KnowledgeRoot, Peers: s.peerStore, Trees: s.treeStore, Logger: s.logger,
	}
	server := &http.Server{
		Addr:              address,
		Handler:           &peerAdmissionHandler{server: s, next: peerHandler},
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       20 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    32 * 1024,
	}
	s.peerRuntimeMu.Lock()
	s.peerServer, s.peerListener = server, listener
	s.peerRuntime = PeerRuntimeStatus{Status: "RUNNING", BindHost: settings.BindHost, Port: settings.Port}
	s.peerRuntimeMu.Unlock()
	go func() {
		err := server.Serve(listener)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.peerRuntimeMu.Lock()
			s.peerRuntime.Status = "FAILED"
			s.peerRuntimeMu.Unlock()
			s.logger.Printf("peer runtime stopped unexpectedly")
		}
	}()
	if s.logger != nil {
		s.logger.Printf("peer runtime listening on %s", fmt.Sprintf("%s:%d", settings.BindHost, settings.Port))
	}
	return nil
}

func (s *Server) stopPeerRuntime() {
	s.peerRuntimeMu.Lock()
	server, listener := s.peerServer, s.peerListener
	s.peerServer, s.peerListener = nil, nil
	s.peerRuntimeMu.Unlock()
	if server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = server.Shutdown(ctx)
		cancel()
	}
	if listener != nil {
		_ = listener.Close()
	}
	s.peerRuntimeMu.Lock()
	if s.peerRuntime.Status == "RUNNING" {
		s.peerRuntime.Status = "STOPPED"
	}
	s.peerRuntimeMu.Unlock()
}

func (s *Server) setPeerRuntime(status PeerRuntimeStatus) {
	s.peerRuntimeMu.Lock()
	s.peerRuntime = status
	s.peerRuntimeMu.Unlock()
}

func (s *Server) peerRuntimeStatus() PeerRuntimeStatus {
	s.peerRuntimeMu.RLock()
	defer s.peerRuntimeMu.RUnlock()
	status := s.peerRuntime
	if status.Status == "" {
		status.Status = "UNKNOWN"
	}
	return status
}
