package commandfx

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
)

// CommandReadiness gates exposure until every pinned runtime dependency is ready.
type CommandReadiness interface {
	PluginCommandReady(ctx context.Context, snapshot runtimecatalogcmd.Snapshot, descriptor runtimecatalogcmd.CommandDescriptor) bool
}

// PinnedCommandReadiness verifies that a command carries executable snapshot data.
type PinnedCommandReadiness struct{}

// NewPinnedCommandReadiness creates the default fail-closed readiness policy.
func NewPinnedCommandReadiness() *PinnedCommandReadiness {
	return &PinnedCommandReadiness{}
}

// PluginCommandReady reports whether the retained descriptor is executable.
func (*PinnedCommandReadiness) PluginCommandReady(_ context.Context, _ runtimecatalogcmd.Snapshot, descriptor runtimecatalogcmd.CommandDescriptor) bool {
	return descriptor.Revision != "" && strings.TrimSpace(descriptor.Instruction) != ""
}

// AdvertisementTarget owns provider syntax and atomic dynamic registration.
type AdvertisementTarget interface {
	Transport() string
	SupportsCommand(name string) bool
	ReplaceCommands(ctx context.Context, projection commandcmd.AdvertisementProjection) error
}

// AdvertisementProjector derives provider-safe aliases from one immutable snapshot.
type AdvertisementProjector struct {
	mu        sync.RWMutex
	readiness CommandReadiness
	targets   []AdvertisementTarget
	sequence  uint64
	projected map[string]advertisementProjectionState
}

type advertisementProjectionState struct {
	sequence   uint64
	projection commandcmd.AdvertisementProjection
}

type advertisementStatus struct {
	Advertisements int
	Lag            uint64
	Omissions      []string
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
	return &AdvertisementProjector{
		readiness: readiness, targets: cloned,
		projected: make(map[string]advertisementProjectionState, len(cloned)),
	}, nil
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
			continue
		}
		p.mu.Lock()
		p.projected[target.Transport()] = advertisementProjectionState{sequence: snapshot.Sequence, projection: cloneAdvertisementProjection(projection)}
		p.mu.Unlock()
	}
	err := errors.Join(errs...)
	if err == nil {
		p.mu.Lock()
		p.sequence = snapshot.Sequence
		p.mu.Unlock()
	}
	return err
}

func (p *AdvertisementProjector) status(pluginName string, revision runtimecatalogcmd.RevisionID, currentSequence uint64) advertisementStatus {
	if p == nil {
		return advertisementStatus{Lag: currentSequence, Omissions: []string{"advertisement_status_unavailable"}}
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	var status advertisementStatus
	for _, target := range p.targets {
		projected, ok := p.projected[target.Transport()]
		if !ok {
			status.Lag = max(status.Lag, currentSequence)
			status.Omissions = append(status.Omissions, target.Transport()+"/projection_unavailable")
			continue
		}
		if projected.sequence < currentSequence {
			status.Lag = max(status.Lag, currentSequence-projected.sequence)
		}
		for _, command := range projected.projection.Commands {
			if pluginSource(command.ID.Source, pluginName) && command.Revision == revision {
				status.Advertisements++
			}
		}
		for _, diagnostic := range projected.projection.Diagnostics {
			if pluginSource(diagnostic.Source, pluginName) {
				status.Omissions = append(status.Omissions, target.Transport()+"/"+diagnostic.Code)
			}
		}
	}
	return status
}

func cloneAdvertisementProjection(projection commandcmd.AdvertisementProjection) commandcmd.AdvertisementProjection {
	out := projection
	out.Commands = append([]commandcmd.ProjectedCommand(nil), projection.Commands...)
	out.Diagnostics = append([]runtimecatalogcmd.Diagnostic(nil), projection.Diagnostics...)
	return out
}

// ProjectionSequence returns the newest fully applied catalog sequence.
func (p *AdvertisementProjector) ProjectionSequence() uint64 {
	if p == nil {
		return 0
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.sequence
}

func orderedPluginCommands(snapshot runtimecatalogcmd.Snapshot) []runtimecatalogcmd.CommandDescriptor {
	commands := make([]runtimecatalogcmd.CommandDescriptor, 0, len(snapshot.Commands))
	for _, descriptor := range snapshot.Commands {
		if descriptor.ID.Source.Kind == runtimecatalogcmd.SourceKindPlugin && descriptor.Advertised && strings.TrimSpace(descriptor.Instruction) != "" {
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
