package skillcmd

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
)

// ManagementKey identifies the versioned private app-KV aggregate.
const ManagementKey = "skill_management:v1"

// Origin is protected host metadata, never a browser or catalog projection.
// The application owner validates source/path policy; SQL treats it as data.
type Origin struct {
	Kind   string `json:"kind"`
	Source string `json:"source"`
	Ref    string `json:"ref,omitempty"`
	Path   string `json:"path"`
}

// Installation is private durable metadata, not a browser DTO. Payloads remain
// exclusively in the existing revision archive; removal retains this tombstone.
type Installation struct {
	Descriptor     runtimecatalogcmd.SourceDescriptor `json:"descriptor"`
	Name           string                             `json:"name"`
	Description    string                             `json:"description"`
	Origin         Origin                             `json:"origin"`
	ResolvedCommit string                             `json:"resolved_commit,omitempty"`
	Enabled        bool                               `json:"enabled"`
	Removed        bool                               `json:"removed"`
	CreatedAt      time.Time                          `json:"created_at"`
	UpdatedAt      time.Time                          `json:"updated_at"`
}

// Record is the complete private durable management aggregate.
type Record struct {
	SchemaVersion int            `json:"schema_version"`
	Generation    uint64         `json:"generation"`
	Installations []Installation `json:"installations"`
	Pending       bool           `json:"pending"`
	UpdatedAt     time.Time      `json:"updated_at"`
}

// Validate checks storage shape and bounds, without interpreting source policy.
func (r Record) Validate() error {
	if r.Generation == 0 {
		if r.SchemaVersion != 0 || len(r.Installations) != 0 || r.Pending || !r.UpdatedAt.IsZero() {
			return ErrInvalid
		}
		return nil
	}
	if r.SchemaVersion != 1 || r.Generation > math.MaxInt64 || r.UpdatedAt.IsZero() || len(r.Installations) > 1024 {
		return ErrInvalid
	}
	ids := make(map[runtimecatalogcmd.SourceID]bool, len(r.Installations))
	for _, i := range r.Installations {
		if i.Descriptor.ID.Kind != runtimecatalogcmd.SourceKindUserSkill || !strings.HasPrefix(i.Descriptor.ID.Name, "managed-") || !validManagedID(i.Descriptor.ID.Name) || !bounded(string(i.Descriptor.Revision), 256) || !bounded(i.Name, 128) || len(i.Description) > 16384 || i.CreatedAt.IsZero() || i.UpdatedAt.IsZero() || i.UpdatedAt.Before(i.CreatedAt) || ids[i.Descriptor.ID] || !bounded(i.Origin.Kind, 32) || !bounded(i.Origin.Source, 8192) || !bounded(i.Origin.Path, 4096) || len(i.Origin.Ref) > 1024 || len(i.ResolvedCommit) > 256 {
			return ErrInvalid
		}
		ids[i.Descriptor.ID] = true
	}
	return nil
}

func validManagedID(id string) bool {
	if !strings.HasPrefix(id, "managed-") || len(id) <= len("managed-") || len(id) > 128 {
		return false
	}
	for _, r := range id {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
			return false
		}
	}
	return true
}

func bounded(value string, limit int) bool {
	return value != "" && len(value) <= limit && !strings.ContainsRune(value, 0)
}

// Selection returns only the complete enabled immutable descriptors and generation.
// Private origins and disabled/removed entries never cross the catalog port.
func (r Record) Selection() runtimecatalogcmd.ManagedSkillSelection {
	s := runtimecatalogcmd.ManagedSkillSelection{Generation: r.Generation}
	for _, i := range r.Installations {
		if i.Enabled && !i.Removed {
			s.Sources = append(s.Sources, i.Descriptor)
		}
	}
	return s.Normalized()
}

// Observation carries a private aggregate and its complete comparison token.
type Observation struct{ Record Record }

// Token includes publication state and retained removal identities as well as generation.
func (o Observation) Token() string {
	data, _ := json.Marshal(o.Record)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Commit atomically replaces metadata against current authority and full state.
// Audit fields deliberately exclude arbitrary reasons, bodies and origins.
type Commit struct {
	Authority     Authority
	ExpectedState string
	Record        Record
	Operation     Operation
	TargetID      string
	AuditID       string
}

// Validate checks the storage envelope before opening a transaction.
func (m Commit) Validate() error {
	if !m.Authority.Valid() || len(m.ExpectedState) != 64 || !m.Operation.Valid() || (m.Operation != OperationRefresh && !validManagedID(m.TargetID)) || !bounded(m.AuditID, 256) || m.Record.Generation == 0 || !m.Record.Pending || m.Record.Validate() != nil {
		return ErrInvalid
	}
	return nil
}
