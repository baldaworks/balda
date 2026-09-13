package command

import (
	"context"
	"errors"
	"fmt"

	"github.com/baldaworks/balda/internal/apps/balda/actorcmd"
	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
	"github.com/baldaworks/go-actorlayer"
)

// Actor validates durable ingress and delegates product behavior by command name.
type Actor struct {
	router    *Router
	snapshots SnapshotResolver
}

// SnapshotResolver reads only the exact retained snapshot named by a durable command.
type SnapshotResolver interface {
	ResolveCommandSnapshot(ctx context.Context, id runtimecatalogcmd.SnapshotID) (runtimecatalogcmd.Snapshot, error)
}

func NewActor(router *Router, snapshots SnapshotResolver) *Actor {
	return &Actor{router: router, snapshots: snapshots}
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
	if !builtIn {
		return actorlayer.PolicyError(fmt.Errorf("unsupported command %q", payload.Name))
	}
	return handler.Handle(ctx, env, payload)
}
