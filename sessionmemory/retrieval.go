package sessionmemory

import "time"

// ReferenceTrust classifies recalled content at the package boundary.
type ReferenceTrust string

const (
	// ReferenceTrustUntrusted marks content as data, never executable instructions.
	ReferenceTrustUntrusted ReferenceTrust = "untrusted_reference"
)

// DerivedSearchRequest asks for bounded memory in one exact scope.
type DerivedSearchRequest struct {
	SchemaVersion     string        `json:"schema_version"`
	Scope             Scope         `json:"scope"`
	Query             string        `json:"query"`
	Kind              *DerivedKind  `json:"kind,omitempty"`
	Category          *AtomCategory `json:"category,omitempty"`
	Limit             int           `json:"limit"`
	AsOf              *time.Time    `json:"as_of,omitempty"`
	SourceID          string        `json:"source_id,omitempty"`
	SessionID         string        `json:"session_id,omitempty"`
	MemoryKey         string        `json:"memory_key,omitempty"`
	MinScopeChangeSeq uint64        `json:"min_scope_change_seq,omitempty"`
}

// SearchHit is one full Store-owned revision plus an optional relevance score.
// Exactly one of Atom, Scenario, or Profile must be populated.
type SearchHit struct {
	Atom     *Atom     `json:"atom,omitempty"`
	Scenario *Scenario `json:"scenario,omitempty"`
	Profile  *Profile  `json:"profile,omitempty"`
	Score    *float64  `json:"score,omitempty"`
}

// DerivedReference is one structured untrusted recall result.
type DerivedReference struct {
	SchemaVersion string         `json:"schema_version"`
	Trust         ReferenceTrust `json:"trust"`
	Kind          DerivedKind    `json:"kind"`
	Scope         Scope          `json:"scope"`
	ItemID        string         `json:"item_id"`
	RevisionID    string         `json:"revision_id"`
	Revision      uint64         `json:"revision"`
	State         RevisionState  `json:"state"`
	Category      *AtomCategory  `json:"category,omitempty"`
	TopicKey      string         `json:"topic_key,omitempty"`
	Title         string         `json:"title,omitempty"`
	Text          string         `json:"text"`
	CreatedAt     time.Time      `json:"created_at"`
	Score         *float64       `json:"score,omitempty"`
	Provenance    Provenance     `json:"provenance"`
}

// DerivedSearchResponse contains bounded untrusted references for one scope.
type DerivedSearchResponse struct {
	SchemaVersion string             `json:"schema_version"`
	Trust         ReferenceTrust     `json:"trust"`
	Scope         Scope              `json:"scope"`
	Results       []DerivedReference `json:"results"`
}

// TraceRequest asks for one bounded provenance graph in an exact scope.
type TraceRequest struct {
	SchemaVersion string      `json:"schema_version"`
	Scope         Scope       `json:"scope"`
	Root          RevisionRef `json:"root"`
	MaxNodes      int         `json:"max_nodes"`
}

// TraceGraph is the Store-owned full provenance subgraph for one root.
type TraceGraph struct {
	SchemaVersion string         `json:"schema_version"`
	Scope         Scope          `json:"scope"`
	Root          RevisionRef    `json:"root"`
	Revisions     []SearchHit    `json:"revisions"`
	Sources       []SourceRecord `json:"sources"`
}

// TraceResponse is a validated, closed provenance graph marked untrusted.
type TraceResponse struct {
	SchemaVersion string         `json:"schema_version"`
	Trust         ReferenceTrust `json:"trust"`
	Scope         Scope          `json:"scope"`
	Root          RevisionRef    `json:"root"`
	Revisions     []SearchHit    `json:"revisions"`
	Sources       []SourceRecord `json:"sources"`
}
