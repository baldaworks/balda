package command

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/actorcmd"
	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
	"github.com/baldaworks/go-actorlayer"
)

// Actor validates durable ingress and delegates product behavior by command name.
type Actor struct {
	router    *Router
	snapshots SnapshotResolver
	plugins   PluginExecutor
}

// SnapshotResolver reads only the exact retained snapshot named by a durable command.
type SnapshotResolver interface {
	ResolveCommandSnapshot(ctx context.Context, id runtimecatalogcmd.SnapshotID) (runtimecatalogcmd.Snapshot, error)
}

// PluginExecutor publishes one declarative plugin command as a normal pinned turn.
type PluginExecutor interface {
	ExecutePluginCommand(ctx context.Context, env actorlayer.Envelope, payload commandcmd.Payload, descriptor runtimecatalogcmd.CommandDescriptor) error
}

func NewActor(router *Router, snapshots SnapshotResolver, plugins PluginExecutor) *Actor {
	return &Actor{router: router, snapshots: snapshots, plugins: plugins}
}
func (a *Actor) Address() string { return actorlayer.WildcardAddress(actorcmd.ActorTypeCommand) }
func (a *Actor) Handle(ctx context.Context, env actorlayer.Envelope) error {
	payload, err := commandcmd.Decode(env)
	if err != nil {
		return actorlayer.PolicyError(fmt.Errorf("decode command envelope: %w", err))
	}
	handler, builtIn := a.router.Resolve(payload.Name)
	if payload.Version == commandcmd.LegacySchemaVersion {
		if !builtIn {
			return actorlayer.PolicyError(fmt.Errorf("legacy command is not a built-in"))
		}
		return handler.Handle(ctx, env, payload)
	}
	if a.snapshots == nil {
		return actorlayer.PolicyError(runtimecatalogcmd.ErrRevisionUnavailable)
	}
	snapshot, err := a.snapshots.ResolveCommandSnapshot(ctx, payload.SnapshotID)
	if errors.Is(err, runtimecatalogcmd.ErrSnapshotUnavailable) || (err == nil && snapshot.ID != payload.SnapshotID) {
		return actorlayer.PolicyError(runtimecatalogcmd.ErrRevisionUnavailable)
	}
	if err != nil {
		return actorlayer.TransientError(fmt.Errorf("resolve command snapshot: %w", err))
	}
	if builtIn {
		return handler.Handle(ctx, env, payload)
	}
	descriptor, ok := resolvePluginCommand(snapshot, payload.Name)
	if !ok {
		return actorlayer.PolicyError(fmt.Errorf("unsupported command %q", payload.Name))
	}
	if !payload.Access.SessionCommands {
		return actorlayer.PolicyError(fmt.Errorf("plugin command access denied"))
	}
	if a.plugins == nil {
		return actorlayer.TransientError(fmt.Errorf("plugin command runtime is unavailable"))
	}
	return a.plugins.ExecutePluginCommand(ctx, env, payload, descriptor)
}

func resolvePluginCommand(snapshot runtimecatalogcmd.Snapshot, name string) (runtimecatalogcmd.CommandDescriptor, bool) {
	canonical := strings.ToLower(strings.TrimSpace(name))
	var resolved runtimecatalogcmd.CommandDescriptor
	found := false
	for _, descriptor := range snapshot.Commands {
		if descriptor.ID.Source.Kind != runtimecatalogcmd.SourceKindPlugin || !descriptor.Advertised || descriptor.Name != canonical || descriptor.Skill == nil {
			continue
		}
		if descriptor.Revision == "" || descriptor.Skill.Source != descriptor.ID.Source || descriptor.Skill.Revision != descriptor.Revision {
			return runtimecatalogcmd.CommandDescriptor{}, false
		}
		if found {
			return runtimecatalogcmd.CommandDescriptor{}, false
		}
		resolved, found = descriptor, true
	}
	return resolved, found
}
