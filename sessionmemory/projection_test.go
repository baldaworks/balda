package sessionmemory

import (
	"testing"
	"time"
)

func TestProjectionManifestValidation(t *testing.T) {
	manifest := ProjectionManifest{
		Scope:        Scope{Key: "telegram:projection", Kind: ScopeKindPersonal},
		ProjectionID: "bleve",
		GenerationID: "generation-1",
		Status:       ProjectionGenerationBuilding,
		UpdatedAt:    time.Date(2026, time.August, 6, 0, 0, 0, 0, time.UTC),
	}
	if err := manifest.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	manifest.Status = "unknown"
	if err := manifest.Validate(); err == nil {
		t.Fatal("Validate() error = nil for unknown status")
	}
}
