// Package mcpcmd defines transport-neutral MCP management values.
package mcpcmd

import (
	"errors"
	"time"
)

var (
	ErrInvalid      = errors.New("invalid MCP definition")
	ErrConflict     = errors.New("MCP definition conflict")
	ErrNotFound     = errors.New("MCP definition not found")
	ErrForbidden    = errors.New("MCP management forbidden")
	ErrCredentials  = errors.New("MCP protected credentials unavailable")
	ErrUnavailable  = errors.New("MCP operation unavailable")
	ErrAuthRequired = errors.New("MCP worker authorization required")
	ErrDisconnected = errors.New("MCP worker authorization disconnected")
	ErrAuthAttempt  = errors.New("MCP authorization attempt unavailable; start again")
)

type Source string

const (
	SourceConfig  Source = "config"
	SourceManaged Source = "managed"
)

type Transport string

const (
	TransportStdio Transport = "stdio"
	TransportHTTP  Transport = "http"
	TransportSSE   Transport = "sse"
)

type ValueKind string

const (
	ValueLiteral     ValueKind = "literal"
	ValueProtected   ValueKind = "protected"
	ValueEnvironment ValueKind = "environment"
)

// ValueBinding carries only public values or a deployment variable name.
// Protected bindings have no Value; their material belongs to the encrypted payload.
type ValueBinding struct {
	Kind  ValueKind `json:"kind"`
	Value string    `json:"value,omitempty"`
}

type Targets struct {
	All       bool     `json:"all"`
	Providers []string `json:"providers,omitempty"`
}

// Definition is the public, immutable configuration of one revision.
type Definition struct {
	Transport Transport               `json:"transport"`
	Command   string                  `json:"command,omitempty"`
	Args      []string                `json:"args,omitempty"`
	Directory string                  `json:"directory,omitempty"`
	URL       string                  `json:"url,omitempty"`
	Env       map[string]ValueBinding `json:"env,omitempty"`
	Headers   map[string]ValueBinding `json:"headers,omitempty"`
	Targets   Targets                 `json:"targets"`
	OAuth     bool                    `json:"oauth,omitempty"`
	Scopes    []string                `json:"scopes,omitempty"`
}

// Connection is a durable identity and its current selection/recovery state.
type Connection struct {
	ID                string    `json:"id"`
	PublicID          string    `json:"public_id"`
	Source            Source    `json:"source"`
	CurrentRevisionID string    `json:"current_revision_id"`
	Enabled           bool      `json:"enabled"`
	Deleted           bool      `json:"deleted"`
	Version           uint64    `json:"version"`
	PublishedVersion  uint64    `json:"published_version"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// Revision retains exact transport configuration and opaque protected material.
// ProtectedValues is never serialized into public views or catalog snapshots.
type Revision struct {
	ConnectionID    string     `json:"connection_id"`
	ID              string     `json:"id"`
	Definition      Definition `json:"definition"`
	ProtectedValues []byte     `json:"-"`
	CreatedAt       time.Time  `json:"created_at"`
}

// Authority binds a write to the canonical administrator and browser family.
// The caller supplies the server's current time and configured fresh-proof age.
type Authority struct {
	UserID            string
	UserVersion       uint64
	CredentialVersion uint64
	MFAVersion        uint64
	SessionID         string
	SessionVersion    uint64
	At                time.Time
	FreshProofAge     time.Duration
}

type ValueOperation string

const (
	ValueKeep   ValueOperation = "keep"
	ValueSet    ValueOperation = "set"
	ValueRemove ValueOperation = "remove"
)

// ValueEdit is write-only material; public forms expose only its kind/key.
type ValueEdit struct {
	Operation ValueOperation
	Kind      ValueKind
	Value     string `json:"-"`
}

type ValueEdits struct {
	Env     map[string]ValueEdit
	Headers map[string]ValueEdit
}

// LaunchValues contains plaintext exclusively for trusted transport construction.
type LaunchValues struct {
	Env     map[string]string `json:"-"`
	Headers map[string]string `json:"-"`
}
