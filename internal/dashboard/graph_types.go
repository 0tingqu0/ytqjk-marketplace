package dashboard

type graphEvidence struct {
	Path      string `json:"path"`
	Scope     string `json:"scope"`
	LineStart int    `json:"line_start"`
	LineEnd   int    `json:"line_end"`
	Excerpt   string `json:"excerpt"`
}

type graphNode struct {
	ID            string          `json:"id"`
	Label         string          `json:"label"`
	Type          string          `json:"type"`
	Kind          string          `json:"kind"`
	Path          string          `json:"path,omitempty"`
	Scope         string          `json:"scope,omitempty"`
	ProjectID     string          `json:"project_id,omitempty"`
	Snippet       string          `json:"snippet,omitempty"`
	LineStart     int             `json:"line_start,omitempty"`
	LineEnd       int             `json:"line_end,omitempty"`
	Mentions      int             `json:"mentions,omitempty"`
	DocumentCount int             `json:"document_count,omitempty"`
	Evidence      []graphEvidence `json:"evidence,omitempty"`
}

type graphEdge struct {
	ID         string          `json:"id"`
	Source     string          `json:"source"`
	Target     string          `json:"target"`
	Type       string          `json:"type"`
	Label      string          `json:"label"`
	Confidence float64         `json:"confidence"`
	Weight     int             `json:"weight"`
	Evidence   []graphEvidence `json:"evidence"`
}

type graphStats struct {
	Documents    int `json:"documents"`
	Entities     int `json:"entities"`
	Relations    int `json:"relations"`
	SourceChunks int `json:"source_chunks"`
}

type graphCapabilities struct {
	EntityExtraction string `json:"entity_extraction"`
	SemanticSearch   string `json:"semantic_search"`
	Embedding        bool   `json:"embedding"`
	Recommendations  bool   `json:"recommendations"`
	PathExploration  bool   `json:"path_exploration"`
}

type knowledgeGraph struct {
	Schema       int               `json:"schema"`
	Nodes        []graphNode       `json:"nodes"`
	Edges        []graphEdge       `json:"edges"`
	Stats        graphStats        `json:"stats"`
	Capabilities graphCapabilities `json:"capabilities"`
	Warnings     []string          `json:"warnings"`
}

type graphCacheEntry struct {
	Signature   string
	GeneratedAt string
	Graphs      map[int]knowledgeGraph
}

type graphSource struct {
	Scope     string
	ProjectID string
	Path      string
	Start     int
	End       int
	LineStart int
	LineEnd   int
	Content   string
	Digest    string
	IndexedAt string
}

type graphDocument struct {
	ID        string
	Scope     string
	ProjectID string
	Path      string
	IndexedAt string
	LineStart int
	LineEnd   int
	Title     string
	Content   string
	Tokens    map[string]int
}

type graphEntityAggregate struct {
	ID        string
	Label     string
	Kind      string
	Mentions  int
	Documents map[string]struct{}
	Evidence  []graphEvidence
}

type selectedGraphEntity struct {
	Aggregate     *graphEntityAggregate
	Documents     []string
	DocumentCount int
}

type graphExtractedRelation struct {
	Source     string
	Target     string
	Type       string
	Label      string
	Confidence float64
	Evidence   graphEvidence
}
