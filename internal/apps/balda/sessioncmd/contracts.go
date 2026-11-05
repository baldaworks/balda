// Package sessioncmd defines transport-neutral session lifecycle contracts.
package sessioncmd

import (
	"errors"
	"slices"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maxIdentityBytes = 256
	maxLabelBytes    = 512
	maxMCPServers    = 64
	maxWarnings      = 16
)

// ErrInvalid reports a malformed session lifecycle contract.
var ErrInvalid = errors.New("invalid session lifecycle contract")

// ActivationKind identifies one semantic session lifecycle outcome.
type ActivationKind string

const (
	// ActivationCreated identifies a newly created logical session lifetime.
	ActivationCreated ActivationKind = "created"
	// ActivationRestored identifies an exact reopen of the same lifetime.
	ActivationRestored ActivationKind = "restored"
	// ActivationReplaced identifies a fresh lifetime after restore fallback.
	ActivationReplaced ActivationKind = "replaced"
)

// Valid reports whether the activation kind is part of the closed contract.
func (k ActivationKind) Valid() bool {
	switch k {
	case ActivationCreated, ActivationRestored, ActivationReplaced:
		return true
	default:
		return false
	}
}

// WarningCode identifies one bounded user-presentable activation warning.
type WarningCode string

const (
	// WarningWorkspaceSyncSkipped identifies a session whose workspace sync was skipped.
	WarningWorkspaceSyncSkipped WarningCode = "workspace_sync_skipped"
)

// Valid reports whether the warning code is part of the closed contract.
func (c WarningCode) Valid() bool { return c == WarningWorkspaceSyncSkipped }

// PublicRuntimeMetadata is the allowlisted receipt captured from the exact
// provider graph and capability snapshot used to construct a runtime.
type PublicRuntimeMetadata struct {
	ProviderID      string   `json:"provider_id,omitempty"`
	ProviderType    string   `json:"provider_type,omitempty"`
	ReasoningEffort string   `json:"reasoning_effort,omitempty"`
	MCPServers      []string `json:"mcp_servers,omitempty"`
}

// NormalizePublicRuntimeMetadata trims scalar fields and returns deterministic
// sorted MCP identities without empty values or duplicates.
func NormalizePublicRuntimeMetadata(metadata PublicRuntimeMetadata) PublicRuntimeMetadata {
	metadata.ProviderID = strings.TrimSpace(metadata.ProviderID)
	metadata.ProviderType = strings.TrimSpace(metadata.ProviderType)
	metadata.ReasoningEffort = strings.TrimSpace(metadata.ReasoningEffort)
	servers := make([]string, 0, len(metadata.MCPServers))
	seen := make(map[string]struct{}, len(metadata.MCPServers))
	for _, server := range metadata.MCPServers {
		server = strings.TrimSpace(server)
		if server == "" {
			continue
		}
		if _, ok := seen[server]; ok {
			continue
		}
		seen[server] = struct{}{}
		servers = append(servers, server)
	}
	sort.Strings(servers)
	metadata.MCPServers = servers
	return metadata
}

// Clone returns a copy whose MCP identities cannot mutate the source.
func (m PublicRuntimeMetadata) Clone() PublicRuntimeMetadata {
	m.MCPServers = slices.Clone(m.MCPServers)
	return m
}

// Validate checks that metadata is normalized, bounded, and free of control text.
func (m PublicRuntimeMetadata) Validate() error {
	if !validPublicText(m.ProviderID, maxIdentityBytes, true) ||
		!validPublicText(m.ProviderType, maxIdentityBytes, true) ||
		!validPublicText(m.ReasoningEffort, maxIdentityBytes, true) ||
		len(m.MCPServers) > maxMCPServers {
		return ErrInvalid
	}
	for i, server := range m.MCPServers {
		if !validPublicText(server, maxIdentityBytes, false) ||
			(i > 0 && m.MCPServers[i-1] >= server) {
			return ErrInvalid
		}
	}
	return nil
}

// RuntimeSummary combines an exact runtime receipt with the effective model
// from the same published runtime.
type RuntimeSummary struct {
	PublicRuntimeMetadata
	EffectiveModel string `json:"effective_model,omitempty"`
}

// Validate checks the bounded public runtime summary.
func (s RuntimeSummary) Validate() error {
	if s.PublicRuntimeMetadata.Validate() != nil || !validPublicText(s.EffectiveModel, maxIdentityBytes, true) {
		return ErrInvalid
	}
	return nil
}

// Activation is the transport-neutral snapshot of one ordinary lifecycle outcome.
type Activation struct {
	Kind      ActivationKind `json:"kind"`
	SessionID string         `json:"session_id"`
	Label     string         `json:"label,omitempty"`
	Runtime   RuntimeSummary `json:"runtime"`
	Warnings  []WarningCode  `json:"warnings,omitempty"`
}

// Clone returns an immutable copy of the activation snapshot.
func (a *Activation) Clone() *Activation {
	if a == nil {
		return nil
	}
	clone := *a
	clone.Runtime.PublicRuntimeMetadata = a.Runtime.Clone()
	clone.Warnings = slices.Clone(a.Warnings)
	return &clone
}

// Validate checks closed lifecycle values, normalized identities, and warning uniqueness.
func (a Activation) Validate() error {
	if !a.Kind.Valid() ||
		!validPublicText(a.SessionID, maxIdentityBytes, false) ||
		!validPublicText(a.Label, maxLabelBytes, true) ||
		a.Runtime.Validate() != nil || len(a.Warnings) > maxWarnings {
		return ErrInvalid
	}
	seen := make(map[WarningCode]struct{}, len(a.Warnings))
	for _, warning := range a.Warnings {
		if !warning.Valid() {
			return ErrInvalid
		}
		if _, duplicate := seen[warning]; duplicate {
			return ErrInvalid
		}
		seen[warning] = struct{}{}
	}
	return nil
}

func validPublicText(value string, maxBytes int, emptyAllowed bool) bool {
	if value == "" {
		return emptyAllowed
	}
	if len(value) > maxBytes || strings.TrimSpace(value) != value || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
