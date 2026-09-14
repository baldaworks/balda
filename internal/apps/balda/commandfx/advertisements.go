package commandfx

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
)

// CommandReadiness gates exposure until every pinned runtime dependency is ready.
type CommandReadiness interface {
	PluginCommandReady(ctx context.Context, snapshot runtimecatalogcmd.Snapshot, descriptor runtimecatalogcmd.CommandDescriptor) bool
}

// MCPReadiness reports observed readiness for one exact revision-keyed server.
type MCPReadiness interface {
	MCPServerReady(source runtimecatalogcmd.SourceID, revision runtimecatalogcmd.RevisionID, name string) bool
}

// PinnedCommandReadiness verifies the command's skill and every same-revision MCP dependency.
type PinnedCommandReadiness struct {
	mcp MCPReadiness
}

// NewPinnedCommandReadiness creates the default fail-closed readiness policy.
func NewPinnedCommandReadiness(mcp MCPReadiness) *PinnedCommandReadiness {
	return &PinnedCommandReadiness{mcp: mcp}
}

// PluginCommandReady reports whether all catalog-pinned consumers can resolve one revision.
func (r *PinnedCommandReadiness) PluginCommandReady(_ context.Context, snapshot runtimecatalogcmd.Snapshot, descriptor runtimecatalogcmd.CommandDescriptor) bool {
	if descriptor.Skill == nil || descriptor.Skill.Source != descriptor.ID.Source || descriptor.Skill.Revision != descriptor.Revision {
		return false
	}
	skillID := runtimecatalogcmd.ContributionID{Source: descriptor.Skill.Source, Kind: runtimecatalogcmd.ContributionKindSkill, Name: descriptor.Skill.Name}
	skill, ok := snapshot.Skills[skillID]
	if !ok || skill.Revision != descriptor.Revision {
		return false
	}
	for _, server := range snapshot.MCPServers {
		if server.ID.Source != descriptor.ID.Source {
			continue
		}
		if server.Revision != descriptor.Revision || r == nil || r.mcp == nil || !r.mcp.MCPServerReady(server.ID.Source, server.Revision, server.Name) {
			return false
		}
	}
	return true
}

// AdvertisementTarget owns provider syntax and atomic dynamic registration.
type AdvertisementTarget interface {
	Transport() string
	SupportsCommand(name string) bool
	ReplaceCommands(ctx context.Context, projection commandcmd.AdvertisementProjection) error
}

// AdvertisementProjector derives provider-safe aliases from one immutable snapshot.
type AdvertisementProjector struct {
	readiness CommandReadiness
	targets   []AdvertisementTarget
}

// NewAdvertisementProjector creates a dynamic command projection service.
func NewAdvertisementProjector(readiness CommandReadiness, targets []AdvertisementTarget) (*AdvertisementProjector, error) {
	if readiness == nil {
		return nil, errors.New("plugin command readiness is required")
	}
	cloned := append([]AdvertisementTarget(nil), targets...)
	seen := make(map[string]struct{}, len(cloned))
	for _, target := range cloned {
		if target == nil {
			return nil, errors.New("valid advertisement targets are required")
		}
		transport := target.Transport()
		if transport == "" || transport != strings.ToLower(strings.TrimSpace(transport)) {
			return nil, errors.New("normalized advertisement transport is required")
		}
		if _, exists := seen[transport]; exists {
			return nil, fmt.Errorf("duplicate advertisement transport %q", transport)
		}
		seen[transport] = struct{}{}
	}
	sort.Slice(cloned, func(i, j int) bool { return cloned[i].Transport() < cloned[j].Transport() })
	return &AdvertisementProjector{readiness: readiness, targets: cloned}, nil
}

// Project replaces every provider's dynamic aliases for snapshot.
func (p *AdvertisementProjector) Project(ctx context.Context, snapshot runtimecatalogcmd.Snapshot) error {
	commands := orderedPluginCommands(snapshot)
	var errs []error
	for _, target := range p.targets {
		projection := commandcmd.AdvertisementProjection{SnapshotID: snapshot.ID, Transport: target.Transport()}
		for _, descriptor := range commands {
			if !p.readiness.PluginCommandReady(ctx, snapshot, descriptor) {
				projection.Diagnostics = append(projection.Diagnostics, commandDiagnostic(descriptor, runtimecatalogcmd.DiagnosticCommandRuntimeUnavailable))
				continue
			}
			if !target.SupportsCommand(descriptor.Name) {
				projection.Diagnostics = append(projection.Diagnostics, commandDiagnostic(descriptor, runtimecatalogcmd.DiagnosticCommandTransportIncompatible))
				continue
			}
			projection.Commands = append(projection.Commands, commandcmd.ProjectedCommand{
				Name: descriptor.Name, Description: descriptor.Description, ID: descriptor.ID, Revision: descriptor.Revision,
			})
		}
		if err := target.ReplaceCommands(ctx, projection); err != nil {
			errs = append(errs, fmt.Errorf("replace %s command advertisements: %w", target.Transport(), err))
		}
	}
	return errors.Join(errs...)
}

func orderedPluginCommands(snapshot runtimecatalogcmd.Snapshot) []runtimecatalogcmd.CommandDescriptor {
	commands := make([]runtimecatalogcmd.CommandDescriptor, 0, len(snapshot.Commands))
	for _, descriptor := range snapshot.Commands {
		if descriptor.ID.Source.Kind == runtimecatalogcmd.SourceKindPlugin && descriptor.Advertised && descriptor.Skill != nil {
			commands = append(commands, descriptor)
		}
	}
	sort.Slice(commands, func(i, j int) bool {
		if commands[i].Name != commands[j].Name {
			return commands[i].Name < commands[j].Name
		}
		return commands[i].ID.String() < commands[j].ID.String()
	})
	return commands
}

func commandDiagnostic(descriptor runtimecatalogcmd.CommandDescriptor, code string) runtimecatalogcmd.Diagnostic {
	id := descriptor.ID
	return runtimecatalogcmd.Diagnostic{
		Severity: runtimecatalogcmd.DiagnosticSeverityWarning, Code: code, Source: id.Source, Contribution: &id,
	}
}
