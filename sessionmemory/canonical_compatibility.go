package sessionmemory

import "context"

// CanonicalCompatibilitySchemaVersion identifies the small rebuildable
// metadata envelope used when v2 canonical records must satisfy the existing
// derived search/trace presentation contract. It is projection metadata, not
// a second source of truth.
const CanonicalCompatibilitySchemaVersion = "session-memory-canonical-compatibility/v1"

type CanonicalCompatibilityPayload struct {
	SchemaVersion     string        `json:"schema_version"`
	Kind              DerivedKind   `json:"kind"`
	Category          *AtomCategory `json:"category,omitempty"`
	TopicKey          string        `json:"topic_key,omitempty"`
	Title             string        `json:"title,omitempty"`
	Text              string        `json:"text"`
	LegacyItemID      string        `json:"legacy_item_id"`
	LegacyRevisionID  string        `json:"legacy_revision_id"`
	LegacyOperationID string        `json:"legacy_operation_id"`
	LegacyParents     []RevisionRef `json:"legacy_parents,omitempty"`
	Supersedes        *RevisionRef  `json:"supersedes,omitempty"`
}

func (p CanonicalCompatibilityPayload) Validate(scope Scope) error {
	if p.SchemaVersion != CanonicalCompatibilitySchemaVersion {
		return invalidDerived("canonical compatibility payload schema version is invalid")
	}
	if err := p.Kind.Validate(); err != nil {
		return err
	}
	if err := scope.Validate(); err != nil {
		return err
	}
	if !isCanonicalID(p.LegacyItemID) || !isCanonicalID(p.LegacyRevisionID) || !isCanonicalID(p.LegacyOperationID) {
		return invalidDerived("canonical compatibility identity is invalid")
	}
	if p.Supersedes != nil {
		if err := p.Supersedes.Validate(); err != nil {
			return err
		}
	}
	if len(p.LegacyParents) > MaxSourcesPerRevision {
		return limitExceeded("canonical compatibility parent bound exceeded")
	}
	for _, parent := range p.LegacyParents {
		if err := parent.Validate(); err != nil {
			return err
		}
	}
	if p.Text == "" {
		return invalidDerived("canonical compatibility text is required")
	}
	switch p.Kind {
	case DerivedKindAtom:
		if p.Category == nil {
			return invalidDerived("canonical atom compatibility category is required")
		}
		return p.Category.Validate()
	case DerivedKindScenario:
		if p.Category != nil || p.TopicKey == "" || p.Title == "" {
			return invalidDerived("canonical scenario compatibility fields are invalid")
		}
	case DerivedKindProfile:
		if p.Category != nil || p.TopicKey != "" || p.Title != "" {
			return invalidDerived("canonical profile compatibility fields are invalid")
		}
	}
	return nil
}

// CanonicalBoundaryView is a bounded legacy-presentation view backed by
// canonical records. LegacyToCanonical is an adapter map only; canonical IDs
// remain authoritative for writes and provenance.
type CanonicalBoundaryView struct {
	Snapshot          ScopeSnapshot
	LegacyToCanonical map[RevisionRef]RevisionRef
}

func (v CanonicalBoundaryView) Validate() error {
	if err := v.Snapshot.Validate(MaxSnapshotItems); err != nil {
		return err
	}
	for legacy, canonical := range v.LegacyToCanonical {
		if err := legacy.Validate(); err != nil {
			return err
		}
		if err := canonical.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// CanonicalBoundaryViewReader is the storage-neutral boundary input port.
type CanonicalBoundaryViewReader interface {
	LoadCanonicalBoundaryView(ctx context.Context, scope Scope) (CanonicalBoundaryView, error)
}
