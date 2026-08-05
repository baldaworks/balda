package sessionmemory

import (
	"context"
	"time"
)

// ProjectionGenerationStatus controls whether a rebuild generation is safe to
// advertise. Only Active may serve indexed results; Dirty and Building must be
// rebuilt or resumed from canonical changes.
type ProjectionGenerationStatus string

const (
	ProjectionGenerationBuilding ProjectionGenerationStatus = "building"
	ProjectionGenerationDirty    ProjectionGenerationStatus = "dirty"
	ProjectionGenerationActive   ProjectionGenerationStatus = "active"
)

// ProjectionManifest is mutable metadata for one projection generation. The
// canonical change log remains authoritative; Watermark is only the highest
// sequence known to have been applied to this generation.
type ProjectionManifest struct {
	Scope        Scope                      `json:"scope"`
	ProjectionID string                     `json:"projection_id"`
	GenerationID string                     `json:"generation_id"`
	Status       ProjectionGenerationStatus `json:"status"`
	Watermark    uint64                     `json:"watermark"`
	UpdatedAt    time.Time                  `json:"updated_at"`
}

// ProjectionCheckpointStore persists generation manifests independently from
// any concrete projection engine. A coordinator marks a generation Dirty
// before applying a batch, advances the watermark only after the engine commit,
// then atomically activates the clean generation.
type ProjectionCheckpointStore interface {
	LoadProjectionManifest(ctx context.Context, scope Scope, projectionID string) (ProjectionManifest, bool, error)
	MarkProjectionDirty(ctx context.Context, manifest ProjectionManifest) error
	AdvanceProjectionWatermark(ctx context.Context, manifest ProjectionManifest, watermark uint64, updatedAt time.Time) error
	ActivateProjectionGeneration(ctx context.Context, manifest ProjectionManifest, updatedAt time.Time) error
}

// Validate verifies a projection manifest does not expose an ambiguous
// generation or claim an uninitialized state.
func (m ProjectionManifest) Validate() error {
	if err := m.Scope.Validate(); err != nil {
		return err
	}
	if !isCanonicalID(m.ProjectionID) || !isCanonicalID(m.GenerationID) || m.UpdatedAt.IsZero() {
		return invalidDerived("projection manifest identity is invalid")
	}
	switch m.Status {
	case ProjectionGenerationBuilding, ProjectionGenerationDirty, ProjectionGenerationActive:
		return nil
	default:
		return invalidDerived("projection manifest status is invalid")
	}
}
