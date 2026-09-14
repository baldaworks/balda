// Package runtimecatalogcmd defines transport-neutral runtime contribution contracts.
package runtimecatalogcmd

import (
	"errors"
	"fmt"
)

// SourceKind identifies one class of runtime contribution source.
type SourceKind string

const (
	SourceKindBuiltin        SourceKind = "builtin"
	SourceKindUserSkill      SourceKind = "user-skill"
	SourceKindWorkspaceSkill SourceKind = "workspace-skill"
	SourceKindConfiguredMCP  SourceKind = "configured-mcp"
	SourceKindPlugin         SourceKind = "plugin"
)

var (
	// ErrSnapshotUnavailable is the stable cross-layer result for an absent retained snapshot.
	ErrSnapshotUnavailable = errors.New(OutcomeSnapshotUnavailable)
	// ErrRevisionUnavailable is the stable cross-layer result for absent pinned bytes.
	ErrRevisionUnavailable = errors.New(OutcomeRevisionUnavailable)
)

const (
	// OutcomeSnapshotUnavailable is the stable code for a missing retained snapshot.
	OutcomeSnapshotUnavailable = "snapshot_unavailable"
	// OutcomeRevisionUnavailable is the stable code for missing retained source content.
	OutcomeRevisionUnavailable = "revision_unavailable"
)

// ContributionKind identifies one class of runtime contribution.
type ContributionKind string

const (
	ContributionKindCommand   ContributionKind = "command"
	ContributionKindSkill     ContributionKind = "skill"
	ContributionKindMCPServer ContributionKind = "mcp-server"
)

// SnapshotScopeKind identifies the lifetime and visibility of a snapshot.
type SnapshotScopeKind string

const (
	SnapshotScopeApplication SnapshotScopeKind = "application"
	SnapshotScopeWorkspace   SnapshotScopeKind = "workspace"
	SnapshotScopeEffective   SnapshotScopeKind = "effective"
)

// DiagnosticSeverity identifies the operational importance of a diagnostic.
type DiagnosticSeverity string

const (
	DiagnosticSeverityInfo    DiagnosticSeverity = "info"
	DiagnosticSeverityWarning DiagnosticSeverity = "warning"
	DiagnosticSeverityError   DiagnosticSeverity = "error"
)

const (
	// DiagnosticBuiltinCommandReserved reports a contribution hidden by a built-in command.
	DiagnosticBuiltinCommandReserved = "builtin_command_reserved"
	// DiagnosticDuplicateCommandAlias reports ambiguous non-built-in command aliases.
	DiagnosticDuplicateCommandAlias = "duplicate_command_alias"
	// DiagnosticCommandTransportIncompatible reports an alias omitted by provider syntax.
	DiagnosticCommandTransportIncompatible = "command_transport_incompatible"
	// DiagnosticCommandRuntimeUnavailable reports an alias withheld until pinned dependencies are ready.
	DiagnosticCommandRuntimeUnavailable = "command_runtime_unavailable"
	// DiagnosticSkillMetadataOmitted reports metadata omitted by a projection budget.
	DiagnosticSkillMetadataOmitted = "skill_metadata_omitted"
	// DiagnosticManifestFieldIgnored reports an unknown portable manifest field.
	DiagnosticManifestFieldIgnored = "manifest_field_ignored"
	// DiagnosticExtensionInvalid reports a disabled Balda plugin extension.
	DiagnosticExtensionInvalid = "plugin_extension_invalid"
	// DiagnosticSkillInvalid reports one skipped Agent Skill.
	DiagnosticSkillInvalid = "skill_invalid"
	// DiagnosticSkillComponentInvalid reports an unusable skills component root.
	DiagnosticSkillComponentInvalid = "skill_component_invalid"
	// DiagnosticMCPConfigInvalid reports a disabled plugin MCP configuration.
	DiagnosticMCPConfigInvalid = "mcp_config_invalid"
	// DiagnosticMCPServerInvalid reports one skipped MCP server declaration.
	DiagnosticMCPServerInvalid = "mcp_server_invalid"
	// DiagnosticMCPTransportUnsupported reports a valid server using a disabled transport.
	DiagnosticMCPTransportUnsupported = "mcp_transport_unsupported"
	// DiagnosticMarketplaceSourcePathDeprecated reports the transitional marketplace path shape.
	DiagnosticMarketplaceSourcePathDeprecated = "marketplace_source_path_deprecated"
)

// RevisionID identifies validated source content.
type RevisionID string

// SnapshotID identifies compiled contribution content.
type SnapshotID string

// SourceID identifies a source without delimiter-based storage conventions.
type SourceID struct {
	Kind SourceKind `json:"kind"`
	Name string     `json:"name"`
}

// String returns a presentation form of the source identity.
func (id SourceID) String() string {
	return fmt.Sprintf("%s/%s", id.Kind, id.Name)
}

// ContributionID identifies a contribution within its structured source identity.
type ContributionID struct {
	Source SourceID         `json:"source"`
	Kind   ContributionKind `json:"kind"`
	Name   string           `json:"name"`
}

// String returns a presentation form of the contribution identity.
func (id ContributionID) String() string {
	return fmt.Sprintf("%s/%s/%s", id.Source.String(), id.Kind, id.Name)
}

// SnapshotScope identifies snapshot visibility.
type SnapshotScope struct {
	Kind SnapshotScopeKind `json:"kind"`
	Name string            `json:"name,omitempty"`
}

// SourceDescriptor describes one validated source revision.
type SourceDescriptor struct {
	ID       SourceID   `json:"id"`
	Revision RevisionID `json:"revision"`
}

// SkillRef pins a skill to its source revision.
type SkillRef struct {
	Source   SourceID   `json:"source"`
	Revision RevisionID `json:"revision"`
	Name     string     `json:"name"`
}

// SkillSelection pins one selected skill to the retained snapshot used to resolve it.
type SkillSelection struct {
	Snapshot SnapshotID `json:"snapshot"`
	Ref      SkillRef   `json:"ref"`
}

// SkillReadRequest contains only host-resolved relative resource references.
type SkillReadRequest struct {
	Ref          SkillRef `json:"ref"`
	MainResource string   `json:"main_resource"`
	Resources    []string `json:"resources,omitempty"`
}

// SkillResource is one bounded supporting resource returned without a host path.
type SkillResource struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

// LoadedSkill contains the selected instructions and requested supporting resources.
type LoadedSkill struct {
	Ref          SkillRef        `json:"ref"`
	Instructions string          `json:"instructions"`
	Resources    []SkillResource `json:"resources,omitempty"`
}

// MCPProjectionOutcome describes provider application of desired MCP state.
type MCPProjectionOutcome string

const (
	MCPProjectionApplied         MCPProjectionOutcome = "applied"
	MCPProjectionNewRuntimesOnly MCPProjectionOutcome = "new-runtimes-only"
	MCPProjectionRebuildRequired MCPProjectionOutcome = "rebuild-required"
	MCPProjectionUnsupported     MCPProjectionOutcome = "unsupported"
)

// CommandDescriptor describes a canonical command contribution.
type CommandDescriptor struct {
	ID          ContributionID `json:"id"`
	Revision    RevisionID     `json:"revision"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Skill       *SkillRef      `json:"skill,omitempty"`
	Advertised  bool           `json:"advertised"`
}

// SkillMetadata describes a skill without loading its instruction body.
type SkillMetadata struct {
	ID          ContributionID `json:"id"`
	Revision    RevisionID     `json:"revision"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Resource    string         `json:"resource"`
}

// MCPServerDescriptor describes desired MCP configuration without live or secret values.
type MCPServerDescriptor struct {
	ID        ContributionID `json:"id"`
	Revision  RevisionID     `json:"revision"`
	Name      string         `json:"name"`
	Transport string         `json:"transport"`
	ConfigRef string         `json:"config_ref,omitempty"`
}

// Diagnostic reports a bounded source or contribution condition.
type Diagnostic struct {
	Severity     DiagnosticSeverity `json:"severity"`
	Code         string             `json:"code"`
	Source       SourceID           `json:"source"`
	Contribution *ContributionID    `json:"contribution,omitempty"`
}

// Source contains the descriptors discovered from one source revision.
type Source struct {
	Descriptor  SourceDescriptor      `json:"descriptor"`
	Commands    []CommandDescriptor   `json:"commands,omitempty"`
	Skills      []SkillMetadata       `json:"skills,omitempty"`
	MCPServers  []MCPServerDescriptor `json:"mcp_servers,omitempty"`
	Diagnostics []Diagnostic          `json:"diagnostics,omitempty"`
}

// Snapshot is an immutable-by-contract compiled contribution view.
type Snapshot struct {
	ID          SnapshotID                             `json:"id"`
	Sequence    uint64                                 `json:"sequence"`
	Scope       SnapshotScope                          `json:"scope"`
	Parents     []SnapshotID                           `json:"parents,omitempty"`
	Sources     map[SourceID]SourceDescriptor          `json:"-"`
	Commands    map[ContributionID]CommandDescriptor   `json:"-"`
	Skills      map[ContributionID]SkillMetadata       `json:"-"`
	MCPServers  map[ContributionID]MCPServerDescriptor `json:"-"`
	Diagnostics []Diagnostic                           `json:"diagnostics,omitempty"`
}

// Clone returns a deep copy suitable for crossing an ownership boundary.
func (s Snapshot) Clone() Snapshot {
	out := s
	out.Parents = append([]SnapshotID(nil), s.Parents...)
	out.Sources = cloneMap(s.Sources)
	out.Commands = make(map[ContributionID]CommandDescriptor, len(s.Commands))
	for id, descriptor := range s.Commands {
		if descriptor.Skill != nil {
			skill := *descriptor.Skill
			descriptor.Skill = &skill
		}
		out.Commands[id] = descriptor
	}
	out.Skills = cloneMap(s.Skills)
	out.MCPServers = cloneMap(s.MCPServers)
	out.Diagnostics = make([]Diagnostic, len(s.Diagnostics))
	for i, diagnostic := range s.Diagnostics {
		if diagnostic.Contribution != nil {
			id := *diagnostic.Contribution
			diagnostic.Contribution = &id
		}
		out.Diagnostics[i] = diagnostic
	}
	return out
}

func cloneMap[K comparable, V any](in map[K]V) map[K]V {
	if in == nil {
		return nil
	}
	out := make(map[K]V, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
