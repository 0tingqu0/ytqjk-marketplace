package dashboard

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/0tingqu0/ytqjk-marketplace/internal/document"
	"github.com/0tingqu0/ytqjk-marketplace/internal/peer"
	"github.com/0tingqu0/ytqjk-marketplace/internal/rag"
	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
	"github.com/0tingqu0/ytqjk-marketplace/internal/tree"
)

func (s *Server) ensureStores() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.treeStore != nil && s.peerStore != nil && s.intakeStore != nil {
		return nil
	}
	database := filepath.Join(s.KnowledgeRoot, "service", "knowledge.sqlite3")
	trees, err := tree.OpenStore(database)
	if err != nil {
		return err
	}
	peers, err := peer.OpenStore(database)
	if err != nil {
		trees.Close()
		return err
	}
	intake, err := document.OpenJobStore(database, 2*time.Minute, 3)
	if err != nil {
		peers.Close()
		trees.Close()
		return err
	}
	projects, err := catalogTreeNodes(s.KnowledgeRoot)
	if err != nil {
		intake.Close()
		peers.Close()
		trees.Close()
		return err
	}
	if _, err := trees.BootstrapProjects(context.Background(), projects); err != nil {
		intake.Close()
		peers.Close()
		trees.Close()
		return err
	}
	s.treeStore = trees
	s.peerStore = peers
	s.intakeStore = intake
	return nil
}

func (s *Server) closeStores() {
	s.stopIntakeWorkers()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.intakeStore != nil {
		_ = s.intakeStore.Close()
		s.intakeStore = nil
	}
	if s.peerStore != nil {
		_ = s.peerStore.Close()
		s.peerStore = nil
	}
	if s.treeStore != nil {
		_ = s.treeStore.Close()
		s.treeStore = nil
	}
}

func catalogTreeNodes(root string) ([]tree.Node, error) {
	var catalog rag.Catalog
	err := safeio.ReadJSON(filepath.Join(root, "catalog.json"), &catalog)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, errors.New("knowledge catalog is invalid")
	}
	identifiers := make([]string, 0, len(catalog.Projects))
	for identifier := range catalog.Projects {
		identifiers = append(identifiers, identifier)
	}
	sort.Strings(identifiers)
	result := make([]tree.Node, 0, len(identifiers))
	for _, identifier := range identifiers {
		if !safeIdentifier(identifier) {
			return nil, errors.New("knowledge catalog contains an invalid project id")
		}
		title := catalog.Projects[identifier].Name
		if title == "" {
			title = identifier
		}
		result = append(result, tree.Node{NodeID: identifier, Title: title, Kind: "project"})
	}
	return result, nil
}
