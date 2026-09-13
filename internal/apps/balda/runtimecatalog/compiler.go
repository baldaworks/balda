// Package runtimecatalog compiles and retains immutable runtime contribution snapshots.
package runtimecatalog

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
)

const compilationRulesVersion = "runtime-catalog-v1"

// ErrSnapshotUnavailable indicates that a pinned snapshot is not retained.
var ErrSnapshotUnavailable = errors.New("runtime contribution snapshot unavailable")

// Compiler deterministically compiles contribution sources.
type Compiler struct{}

// NewCompiler creates a Compiler using the current compilation rules.
func NewCompiler() *Compiler { return &Compiler{} }

// CompileApplication compiles application-scoped sources.
func (c *Compiler) CompileApplication(sources []runtimecatalogcmd.Source) (runtimecatalogcmd.Snapshot, error) {
	for _, source := range sources {
		if source.Descriptor.ID.Kind == runtimecatalogcmd.SourceKindWorkspaceSkill {
			return runtimecatalogcmd.Snapshot{}, fmt.Errorf("workspace source %q is not application-scoped", source.Descriptor.ID.Name)
		}
	}
	return c.compile(runtimecatalogcmd.SnapshotScope{Kind: runtimecatalogcmd.SnapshotScopeApplication}, nil, sources)
}

// CompileWorkspace compiles one isolated workspace skill overlay.
func (c *Compiler) CompileWorkspace(workspace string, sources []runtimecatalogcmd.Source) (runtimecatalogcmd.Snapshot, error) {
	name := strings.TrimSpace(workspace)
	if name == "" {
		return runtimecatalogcmd.Snapshot{}, errors.New("workspace name is required")
	}
	for _, source := range sources {
		if source.Descriptor.ID.Kind != runtimecatalogcmd.SourceKindWorkspaceSkill {
			return runtimecatalogcmd.Snapshot{}, fmt.Errorf("source %q is not workspace-scoped", source.Descriptor.ID.String())
		}
		if len(source.Commands) != 0 || len(source.MCPServers) != 0 {
			return runtimecatalogcmd.Snapshot{}, fmt.Errorf("workspace source %q may contribute only skills", source.Descriptor.ID.Name)
		}
	}
	return c.compile(runtimecatalogcmd.SnapshotScope{Kind: runtimecatalogcmd.SnapshotScopeWorkspace, Name: name}, nil, sources)
}

// Merge derives an effective snapshot from one application snapshot and one workspace overlay.
func (c *Compiler) Merge(application, workspace runtimecatalogcmd.Snapshot) (runtimecatalogcmd.Snapshot, error) {
	if application.Scope.Kind != runtimecatalogcmd.SnapshotScopeApplication {
		return runtimecatalogcmd.Snapshot{}, errors.New("application snapshot is required")
	}
	if workspace.Scope.Kind != runtimecatalogcmd.SnapshotScopeWorkspace {
		return runtimecatalogcmd.Snapshot{}, errors.New("workspace snapshot is required")
	}
	if strings.TrimSpace(workspace.Scope.Name) == "" || workspace.Scope.Name != strings.TrimSpace(workspace.Scope.Name) {
		return runtimecatalogcmd.Snapshot{}, errors.New("normalized workspace snapshot name is required")
	}
	if err := verifySnapshotID(application); err != nil {
		return runtimecatalogcmd.Snapshot{}, fmt.Errorf("verify application snapshot: %w", err)
	}
	if err := verifySnapshotID(workspace); err != nil {
		return runtimecatalogcmd.Snapshot{}, fmt.Errorf("verify workspace snapshot: %w", err)
	}
	sources, err := mergeSources(application, workspace)
	if err != nil {
		return runtimecatalogcmd.Snapshot{}, err
	}
	parents := []runtimecatalogcmd.SnapshotID{application.ID, workspace.ID}
	return c.compile(runtimecatalogcmd.SnapshotScope{Kind: runtimecatalogcmd.SnapshotScopeEffective, Name: workspace.Scope.Name}, parents, sources)
}

func (c *Compiler) compile(scope runtimecatalogcmd.SnapshotScope, parents []runtimecatalogcmd.SnapshotID, sources []runtimecatalogcmd.Source) (runtimecatalogcmd.Snapshot, error) {
	_ = c
	snapshot := runtimecatalogcmd.Snapshot{
		Scope:      scope,
		Parents:    append([]runtimecatalogcmd.SnapshotID(nil), parents...),
		Sources:    make(map[runtimecatalogcmd.SourceID]runtimecatalogcmd.SourceDescriptor, len(sources)),
		Commands:   make(map[runtimecatalogcmd.ContributionID]runtimecatalogcmd.CommandDescriptor),
		Skills:     make(map[runtimecatalogcmd.ContributionID]runtimecatalogcmd.SkillMetadata),
		MCPServers: make(map[runtimecatalogcmd.ContributionID]runtimecatalogcmd.MCPServerDescriptor),
	}
	ordered := append([]runtimecatalogcmd.Source(nil), sources...)
	sort.Slice(ordered, func(i, j int) bool { return sourceKey(ordered[i].Descriptor.ID) < sourceKey(ordered[j].Descriptor.ID) })
	for _, source := range ordered {
		if err := addSource(&snapshot, source); err != nil {
			return runtimecatalogcmd.Snapshot{}, err
		}
	}
	if err := validateCommandSkillRefs(snapshot); err != nil {
		return runtimecatalogcmd.Snapshot{}, err
	}
	if err := applyCommandCollisionPolicy(&snapshot); err != nil {
		return runtimecatalogcmd.Snapshot{}, err
	}
	sortDiagnostics(snapshot.Diagnostics)
	id, err := hashSnapshot(snapshot)
	if err != nil {
		return runtimecatalogcmd.Snapshot{}, err
	}
	snapshot.ID = id
	return snapshot, nil
}

func addSource(snapshot *runtimecatalogcmd.Snapshot, source runtimecatalogcmd.Source) error {
	descriptor := source.Descriptor
	if err := validateSourceDescriptor(descriptor); err != nil {
		return err
	}
	if _, exists := snapshot.Sources[descriptor.ID]; exists {
		return fmt.Errorf("duplicate source %q", descriptor.ID.String())
	}
	snapshot.Sources[descriptor.ID] = descriptor
	for _, command := range source.Commands {
		command.Name = canonicalCommandName(command.Name)
		command.Advertised = true
		if err := validateContribution(descriptor, command.ID, command.Revision, runtimecatalogcmd.ContributionKindCommand, command.Name); err != nil {
			return err
		}
		if command.Skill != nil {
			if err := validateSkillRef(*command.Skill); err != nil {
				return fmt.Errorf("command %q: %w", command.ID.String(), err)
			}
		}
		if _, exists := snapshot.Commands[command.ID]; exists {
			return fmt.Errorf("duplicate contribution %q", command.ID.String())
		}
		snapshot.Commands[command.ID] = command
	}
	for _, skill := range source.Skills {
		skill.Name = strings.TrimSpace(skill.Name)
		skill.Resource = filepath.ToSlash(strings.TrimSpace(skill.Resource))
		if err := validateContribution(descriptor, skill.ID, skill.Revision, runtimecatalogcmd.ContributionKindSkill, skill.Name); err != nil {
			return err
		}
		if !validRelativeRef(skill.Resource) {
			return fmt.Errorf("skill %q resource must be a contained relative path", skill.ID.String())
		}
		if _, exists := snapshot.Skills[skill.ID]; exists {
			return fmt.Errorf("duplicate contribution %q", skill.ID.String())
		}
		snapshot.Skills[skill.ID] = skill
	}
	for _, server := range source.MCPServers {
		server.Name = strings.TrimSpace(server.Name)
		server.Transport = strings.TrimSpace(server.Transport)
		server.ConfigRef = filepath.ToSlash(strings.TrimSpace(server.ConfigRef))
		if err := validateContribution(descriptor, server.ID, server.Revision, runtimecatalogcmd.ContributionKindMCPServer, server.Name); err != nil {
			return err
		}
		if server.Transport == "" {
			return fmt.Errorf("MCP server %q transport is required", server.ID.String())
		}
		if server.ConfigRef != "" && !validRelativeRef(server.ConfigRef) {
			return fmt.Errorf("MCP server %q config reference must be relative", server.ID.String())
		}
		if _, exists := snapshot.MCPServers[server.ID]; exists {
			return fmt.Errorf("duplicate contribution %q", server.ID.String())
		}
		snapshot.MCPServers[server.ID] = server
	}
	for _, diagnostic := range source.Diagnostics {
		if diagnostic.Source != descriptor.ID {
			return fmt.Errorf("diagnostic source %q does not match %q", diagnostic.Source.String(), descriptor.ID.String())
		}
		if strings.TrimSpace(diagnostic.Code) == "" {
			return errors.New("diagnostic code is required")
		}
		if !validDiagnosticSeverity(diagnostic.Severity) {
			return fmt.Errorf("diagnostic %q has invalid severity %q", diagnostic.Code, diagnostic.Severity)
		}
		if diagnostic.Contribution != nil && diagnostic.Contribution.Source != descriptor.ID {
			return fmt.Errorf("diagnostic %q contribution does not belong to source %q", diagnostic.Code, descriptor.ID.String())
		}
		snapshot.Diagnostics = append(snapshot.Diagnostics, diagnostic)
	}
	return nil
}

func validateCommandSkillRefs(snapshot runtimecatalogcmd.Snapshot) error {
	for _, command := range snapshot.Commands {
		if command.Skill == nil {
			continue
		}
		ref := *command.Skill
		if command.ID.Source.Kind == runtimecatalogcmd.SourceKindPlugin && ref.Source != command.ID.Source {
			return fmt.Errorf("plugin command %q must reference a plugin-local skill", command.ID.String())
		}
		id := runtimecatalogcmd.ContributionID{Source: ref.Source, Kind: runtimecatalogcmd.ContributionKindSkill, Name: ref.Name}
		skill, ok := snapshot.Skills[id]
		if !ok || skill.Revision != ref.Revision {
			return fmt.Errorf("command %q references unavailable skill %q", command.ID.String(), id.String())
		}
	}
	return nil
}

func validateSourceDescriptor(descriptor runtimecatalogcmd.SourceDescriptor) error {
	if !validSourceKind(descriptor.ID.Kind) {
		return fmt.Errorf("invalid source kind %q", descriptor.ID.Kind)
	}
	if strings.TrimSpace(descriptor.ID.Name) == "" {
		return errors.New("source name is required")
	}
	if strings.TrimSpace(descriptor.ID.Name) != descriptor.ID.Name {
		return fmt.Errorf("source name %q is not normalized", descriptor.ID.Name)
	}
	if strings.TrimSpace(string(descriptor.Revision)) == "" {
		return fmt.Errorf("source %q revision is required", descriptor.ID.String())
	}
	if strings.TrimSpace(string(descriptor.Revision)) != string(descriptor.Revision) {
		return fmt.Errorf("source %q revision is not normalized", descriptor.ID.String())
	}
	return nil
}

func validateContribution(source runtimecatalogcmd.SourceDescriptor, id runtimecatalogcmd.ContributionID, revision runtimecatalogcmd.RevisionID, kind runtimecatalogcmd.ContributionKind, name string) error {
	if id.Source != source.ID {
		return fmt.Errorf("contribution %q does not belong to source %q", id.String(), source.ID.String())
	}
	if id.Kind != kind {
		return fmt.Errorf("contribution %q has kind %q, want %q", id.String(), id.Kind, kind)
	}
	if strings.TrimSpace(id.Name) == "" || strings.TrimSpace(name) == "" {
		return fmt.Errorf("contribution %q name is required", id.String())
	}
	if id.Name != name {
		return fmt.Errorf("contribution identity name %q does not match descriptor name %q", id.Name, name)
	}
	if revision != source.Revision {
		return fmt.Errorf("contribution %q revision does not match its source", id.String())
	}
	return nil
}

func validateSkillRef(ref runtimecatalogcmd.SkillRef) error {
	if !validSourceKind(ref.Source.Kind) || strings.TrimSpace(ref.Source.Name) == "" {
		return errors.New("skill source is required")
	}
	if strings.TrimSpace(string(ref.Revision)) == "" || strings.TrimSpace(ref.Name) == "" {
		return errors.New("skill revision and name are required")
	}
	return nil
}

func validSourceKind(kind runtimecatalogcmd.SourceKind) bool {
	switch kind {
	case runtimecatalogcmd.SourceKindBuiltin,
		runtimecatalogcmd.SourceKindUserSkill,
		runtimecatalogcmd.SourceKindWorkspaceSkill,
		runtimecatalogcmd.SourceKindConfiguredMCP,
		runtimecatalogcmd.SourceKindPlugin:
		return true
	default:
		return false
	}
}

func validDiagnosticSeverity(severity runtimecatalogcmd.DiagnosticSeverity) bool {
	switch severity {
	case runtimecatalogcmd.DiagnosticSeverityInfo,
		runtimecatalogcmd.DiagnosticSeverityWarning,
		runtimecatalogcmd.DiagnosticSeverityError:
		return true
	default:
		return false
	}
}

func validRelativeRef(ref string) bool {
	if ref == "" || filepath.IsAbs(ref) {
		return false
	}
	clean := filepath.ToSlash(filepath.Clean(ref))
	return clean != "." && clean != ".." && !strings.HasPrefix(clean, "../")
}

func canonicalCommandName(name string) string { return strings.ToLower(strings.TrimSpace(name)) }

func applyCommandCollisionPolicy(snapshot *runtimecatalogcmd.Snapshot) error {
	aliases := make(map[string][]runtimecatalogcmd.ContributionID)
	for id, command := range snapshot.Commands {
		aliases[command.Name] = append(aliases[command.Name], id)
	}
	for _, ids := range aliases {
		sort.Slice(ids, func(i, j int) bool { return contributionKey(ids[i]) < contributionKey(ids[j]) })
		builtinCount := 0
		for _, id := range ids {
			if id.Source.Kind == runtimecatalogcmd.SourceKindBuiltin {
				builtinCount++
			}
		}
		if builtinCount > 1 {
			return fmt.Errorf("duplicate built-in command alias %q", snapshot.Commands[ids[0]].Name)
		}
		if builtinCount == 1 {
			for _, id := range ids {
				if id.Source.Kind == runtimecatalogcmd.SourceKindBuiltin {
					continue
				}
				markCommandOmitted(snapshot, id, runtimecatalogcmd.DiagnosticBuiltinCommandReserved)
			}
			continue
		}
		if len(ids) > 1 {
			for _, id := range ids {
				markCommandOmitted(snapshot, id, runtimecatalogcmd.DiagnosticDuplicateCommandAlias)
			}
		}
	}
	return nil
}

func markCommandOmitted(snapshot *runtimecatalogcmd.Snapshot, id runtimecatalogcmd.ContributionID, code string) {
	command := snapshot.Commands[id]
	command.Advertised = false
	snapshot.Commands[id] = command
	contribution := id
	snapshot.Diagnostics = append(snapshot.Diagnostics, runtimecatalogcmd.Diagnostic{
		Severity: runtimecatalogcmd.DiagnosticSeverityWarning, Code: code, Source: id.Source, Contribution: &contribution,
	})
}

func mergeSources(application, workspace runtimecatalogcmd.Snapshot) ([]runtimecatalogcmd.Source, error) {
	grouped := make(map[runtimecatalogcmd.SourceID]*runtimecatalogcmd.Source)
	for _, snapshot := range []runtimecatalogcmd.Snapshot{application, workspace} {
		for id, descriptor := range snapshot.Sources {
			if _, exists := grouped[id]; exists {
				return nil, fmt.Errorf("duplicate source %q while merging snapshots", id.String())
			}
			grouped[id] = &runtimecatalogcmd.Source{Descriptor: descriptor}
		}
		for _, command := range snapshot.Commands {
			source, ok := grouped[command.ID.Source]
			if !ok {
				return nil, fmt.Errorf("command %q has no source descriptor", command.ID.String())
			}
			source.Commands = append(source.Commands, command)
		}
		for _, skill := range snapshot.Skills {
			source, ok := grouped[skill.ID.Source]
			if !ok {
				return nil, fmt.Errorf("skill %q has no source descriptor", skill.ID.String())
			}
			source.Skills = append(source.Skills, skill)
		}
		for _, server := range snapshot.MCPServers {
			source, ok := grouped[server.ID.Source]
			if !ok {
				return nil, fmt.Errorf("MCP server %q has no source descriptor", server.ID.String())
			}
			source.MCPServers = append(source.MCPServers, server)
		}
		for _, diagnostic := range snapshot.Diagnostics {
			if diagnostic.Code == runtimecatalogcmd.DiagnosticBuiltinCommandReserved || diagnostic.Code == runtimecatalogcmd.DiagnosticDuplicateCommandAlias {
				continue
			}
			source, ok := grouped[diagnostic.Source]
			if !ok {
				return nil, fmt.Errorf("diagnostic %q has no source descriptor", diagnostic.Code)
			}
			source.Diagnostics = append(source.Diagnostics, diagnostic)
		}
	}
	out := make([]runtimecatalogcmd.Source, 0, len(grouped))
	for _, source := range grouped {
		out = append(out, *source)
	}
	return out, nil
}

type canonicalSnapshot struct {
	Rules       string                                  `json:"rules"`
	Scope       runtimecatalogcmd.SnapshotScope         `json:"scope"`
	Parents     []runtimecatalogcmd.SnapshotID          `json:"parents,omitempty"`
	Sources     []runtimecatalogcmd.SourceDescriptor    `json:"sources"`
	Commands    []runtimecatalogcmd.CommandDescriptor   `json:"commands"`
	Skills      []runtimecatalogcmd.SkillMetadata       `json:"skills"`
	MCPServers  []runtimecatalogcmd.MCPServerDescriptor `json:"mcp_servers"`
	Diagnostics []runtimecatalogcmd.Diagnostic          `json:"diagnostics"`
}

func hashSnapshot(snapshot runtimecatalogcmd.Snapshot) (runtimecatalogcmd.SnapshotID, error) {
	canonical := canonicalSnapshot{Rules: compilationRulesVersion, Scope: snapshot.Scope, Parents: append([]runtimecatalogcmd.SnapshotID(nil), snapshot.Parents...)}
	for _, descriptor := range snapshot.Sources {
		canonical.Sources = append(canonical.Sources, descriptor)
	}
	for _, descriptor := range snapshot.Commands {
		canonical.Commands = append(canonical.Commands, descriptor)
	}
	for _, descriptor := range snapshot.Skills {
		canonical.Skills = append(canonical.Skills, descriptor)
	}
	for _, descriptor := range snapshot.MCPServers {
		canonical.MCPServers = append(canonical.MCPServers, descriptor)
	}
	canonical.Diagnostics = append(canonical.Diagnostics, snapshot.Diagnostics...)
	sort.Slice(canonical.Sources, func(i, j int) bool { return sourceKey(canonical.Sources[i].ID) < sourceKey(canonical.Sources[j].ID) })
	sort.Slice(canonical.Commands, func(i, j int) bool {
		return contributionKey(canonical.Commands[i].ID) < contributionKey(canonical.Commands[j].ID)
	})
	sort.Slice(canonical.Skills, func(i, j int) bool {
		return contributionKey(canonical.Skills[i].ID) < contributionKey(canonical.Skills[j].ID)
	})
	sort.Slice(canonical.MCPServers, func(i, j int) bool {
		return contributionKey(canonical.MCPServers[i].ID) < contributionKey(canonical.MCPServers[j].ID)
	})
	sortDiagnostics(canonical.Diagnostics)
	data, err := json.Marshal(canonical)
	if err != nil {
		return "", fmt.Errorf("marshal canonical snapshot: %w", err)
	}
	digest := sha256.Sum256(data)
	return runtimecatalogcmd.SnapshotID(hex.EncodeToString(digest[:])), nil
}

func verifySnapshotID(snapshot runtimecatalogcmd.Snapshot) error {
	if snapshot.ID == "" {
		return errors.New("snapshot ID is required")
	}
	want, err := hashSnapshot(snapshot)
	if err != nil {
		return err
	}
	if snapshot.ID != want {
		return errors.New("snapshot ID does not match content")
	}
	return nil
}

func sourceKey(id runtimecatalogcmd.SourceID) string {
	return string(id.Kind) + "\x00" + id.Name
}

func contributionKey(id runtimecatalogcmd.ContributionID) string {
	return sourceKey(id.Source) + "\x00" + string(id.Kind) + "\x00" + id.Name
}

func sortDiagnostics(diagnostics []runtimecatalogcmd.Diagnostic) {
	sort.Slice(diagnostics, func(i, j int) bool {
		left := sourceKey(diagnostics[i].Source) + "\x00" + diagnostics[i].Code
		right := sourceKey(diagnostics[j].Source) + "\x00" + diagnostics[j].Code
		if diagnostics[i].Contribution != nil {
			left += "\x00" + contributionKey(*diagnostics[i].Contribution)
		}
		if diagnostics[j].Contribution != nil {
			right += "\x00" + contributionKey(*diagnostics[j].Contribution)
		}
		left += "\x00" + string(diagnostics[i].Severity)
		right += "\x00" + string(diagnostics[j].Severity)
		return left < right
	})
}
