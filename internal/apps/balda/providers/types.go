// Package providers owns managed provider definitions and exact session bindings.
package providers

import (
	"context"
	"errors"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"github.com/baldaworks/runtime/v2/agentconfig"
)

const StorageFormatVersion uint32 = 1

var (
	ErrInvalid     = errors.New("invalid provider definition")
	ErrConflict    = errors.New("provider state changed")
	ErrUnavailable = errors.New("provider revision unavailable")
)

type Source string

const (
	SourceConfigured Source = "configured"
	SourceManaged    Source = "managed"
)

type StateVersion uint64

type Record struct {
	ID                string
	CurrentRevisionID string
	Enabled           bool
	Deleted           bool
}

// Definition is the persisted public envelope. Config never contains an API
// key or environment value; protected fields only expose presence and names.
type Definition struct {
	Config           agentconfig.Config
	HasAPIKey        bool
	EnvironmentNames []string
	Reproducible     bool
}

type Revision struct {
	ID              string
	ProviderID      string
	DriverType      string
	ContractVersion uint32
	FormatVersion   uint32
	Definition      Definition
	ProtectedValues []byte
	CreatedAt       time.Time
}

type State struct {
	Version   StateVersion
	Records   []Record
	Revisions []Revision
}

// Item is one secret-free provider inventory entry.
type Item struct {
	Source   Source
	Record   Record
	Revision Revision
}

// Inventory is one consistent public projection of configured and managed providers.
type Inventory struct {
	Version StateVersion
	Items   []Item
}

type RevisionRef struct {
	ProviderID string
	RevisionID string
}

type Mutation struct {
	ExpectedVersion StateVersion
	Record          Record
	Revision        *Revision
	Audit           usercmd.AuditEvent
}

// Store is the single persistence port for provider state.
type Store interface {
	Read(ctx context.Context) (State, error)
	Apply(ctx context.Context, mutation Mutation) (State, error)
	Revisions(ctx context.Context, refs []RevisionRef) ([]Revision, error)
}

type ProtectedEdit struct {
	APIKey      *string
	Environment map[string]*string
}

type Change struct {
	ExpectedVersion StateVersion
	ID              string
	Config          agentconfig.Config
	Protected       ProtectedEdit
	Audit           usercmd.AuditEvent
}

type Entry struct {
	ProviderID string
	Source     Source
	RevisionID string
}

type Binding struct {
	RootProviderID string
	Entries        []Entry
	Reproducible   bool
	Warnings       []string
}

type Compiled struct {
	Binding Binding
	Configs map[string]agentconfig.Config
}
