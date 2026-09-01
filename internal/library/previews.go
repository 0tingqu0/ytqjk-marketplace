package library

// PreviewCreate validates a new node without mutating the tree.
func (t *Tree) PreviewCreate(request CreateRequest) (Preview, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.previewCreate(request)
}

// PreviewAttach plans attaching one root beneath a new parent.
func (t *Tree) PreviewAttach(nodeID, parentID string) (Preview, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.previewAttach(nodeID, parentID)
}

// PreviewDetach plans detaching a node while retaining its descendants.
func (t *Tree) PreviewDetach(nodeID string) (Preview, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.previewDetach(nodeID)
}

// PreviewMove plans atomically replacing a node's parent.
func (t *Tree) PreviewMove(nodeID, parentID string) (Preview, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.previewMove(nodeID, parentID)
}

// PreviewInsertBetween plans replacing one direct edge with two edges.
func (t *Tree) PreviewInsertBetween(
	parentID, nodeID, middleID string,
) (Preview, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.previewInsertBetween(parentID, nodeID, middleID)
}
