package peer

import (
	"errors"
	"os"
	"path/filepath"
	"sort"

	"github.com/0tingqu0/ytqjk-marketplace/internal/rag"
	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
	"github.com/0tingqu0/ytqjk-marketplace/internal/tree"
)

type Export struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Type  string `json:"type"`
}

type Library struct {
	NodeID    string
	Kind      string
	Directory string
}

func ExportCatalog(knowledgeRoot, projectID string, exportNodeIDs []string, value *tree.Tree) ([]Export, int, error) {
	if len(exportNodeIDs) < 1 || len(exportNodeIDs) > 64 {
		return nil, 0, errors.New("INVALID_EXPORT_NODE_IDS")
	}
	exports := make([]Export, 0, len(exportNodeIDs))
	libraryIDs := map[string]bool{}
	seen := map[string]bool{}
	for _, nodeID := range exportNodeIDs {
		if seen[nodeID] {
			return nil, 0, errors.New("DUPLICATE_EXPORT_NODE")
		}
		seen[nodeID] = true
		libraries, err := ExportedLibraries(knowledgeRoot, projectID, nodeID, value)
		if err != nil {
			return nil, 0, err
		}
		node, ok := value.Node(nodeID)
		if !ok {
			return nil, 0, errors.New("PEER_EXPORT_NODE_MISSING")
		}
		exports = append(exports, Export{ID: node.NodeID, Title: node.Title, Type: node.Kind})
		for _, library := range libraries {
			libraryIDs[library.NodeID] = true
		}
	}
	return exports, len(libraryIDs), nil
}

func ExportedLibraries(knowledgeRoot, projectID, exportNodeID string, value *tree.Tree) ([]Library, error) {
	if value == nil {
		return nil, errors.New("PEER_TREE_NOT_CONFIGURED")
	}
	if err := requireTrackedProject(knowledgeRoot, projectID); err != nil {
		return nil, err
	}
	project, ok := value.Node(projectID)
	if !ok || project.Kind != "project" {
		return nil, errors.New("PEER_PROJECT_TREE_NODE_MISSING")
	}
	exported, ok := value.Node(exportNodeID)
	if !ok {
		return nil, errors.New("PEER_EXPORT_NODE_MISSING")
	}
	if exported.Kind == "mounted" {
		return nil, errors.New("PEER_EXPORT_MOUNTED_FORBIDDEN")
	}
	inside, err := value.Contains(projectID, exportNodeID)
	if err != nil {
		return nil, errors.New("PEER_EXPORT_NODE_MISSING")
	}
	projectChain, _ := value.Ancestors(projectID)
	exportChain, _ := value.Ancestors(exportNodeID)
	sameLevel := len(projectChain) == len(exportChain)
	if !inside && !sameLevel {
		return nil, errors.New("PEER_EXPORT_OUTSIDE_PROJECT")
	}
	if exported.Kind == "project" && exportNodeID != projectID && !sameLevel {
		return nil, errors.New("PEER_EXPORT_PROJECT_FORBIDDEN")
	}
	nodes := make(map[string]tree.Node)
	children := make(map[string][]string)
	for _, node := range value.Nodes() {
		nodes[node.NodeID] = node
	}
	for _, edge := range value.Edges() {
		children[edge.Parent] = append(children[edge.Parent], edge.Child)
	}
	for parent := range children {
		sort.Strings(children[parent])
	}
	pending := []string{exportNodeID}
	result := make([]Library, 0)
	for len(pending) > 0 {
		current := pending[0]
		pending = pending[1:]
		node := nodes[current]
		if node.Kind == "mounted" || (node.Kind == "project" && current != exportNodeID) {
			continue
		}
		result = append(result, libraryFor(knowledgeRoot, node))
		pending = append(pending, children[current]...)
	}
	return result, nil
}

func RequireExportedLibrary(knowledgeRoot, projectID, exportNodeID, libraryNode string, value *tree.Tree) (Library, error) {
	libraries, err := ExportedLibraries(knowledgeRoot, projectID, exportNodeID, value)
	if err != nil {
		return Library{}, err
	}
	for _, library := range libraries {
		if library.NodeID == libraryNode {
			return library, nil
		}
	}
	return Library{}, errors.New("PEER_LIBRARY_OUTSIDE_EXPORT")
}

func requireTrackedProject(knowledgeRoot, projectID string) error {
	if !validIdentifier(projectID) {
		return errors.New("PEER_PROJECT_NOT_TRACKED")
	}
	var catalog rag.Catalog
	if err := safeio.ReadJSON(filepath.Join(knowledgeRoot, "catalog.json"), &catalog); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return errors.New("PEER_PROJECT_NOT_TRACKED")
		}
		return errors.New("PEER_CATALOG_INVALID")
	}
	if _, ok := catalog.Projects[projectID]; !ok {
		return errors.New("PEER_PROJECT_NOT_TRACKED")
	}
	return nil
}

func libraryFor(knowledgeRoot string, node tree.Node) Library {
	directory := filepath.Join(knowledgeRoot, "libraries", node.NodeID)
	if node.Kind == "project" {
		directory = filepath.Join(knowledgeRoot, "projects", node.NodeID)
	} else if node.Kind == "global" {
		directory = filepath.Join(knowledgeRoot, "global-cache")
	}
	return Library{NodeID: node.NodeID, Kind: node.Kind, Directory: directory}
}
