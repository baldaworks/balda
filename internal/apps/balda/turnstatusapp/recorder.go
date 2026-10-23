package turnstatusapp

import (
	"context"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/balda/internal/apps/balda/turnstatuscmd"
	"github.com/google/uuid"
)

// Recorder owns process identity and writes durable turn lifecycle evidence.
type Recorder struct {
	store  state.TurnStatusStore
	bootID string
}

func NewRecorder(provider state.Provider) *Recorder {
	if provider == nil {
		return &Recorder{bootID: uuid.NewString()}
	}
	return &Recorder{store: provider.TurnStatus(), bootID: uuid.NewString()}
}

func (r *Recorder) Begin(ctx context.Context, sessionID, archiveID string, startedAt time.Time) (context.Context, error) {
	identity := turnstatuscmd.Identity{SessionID: sessionID, ArchiveID: archiveID, TurnID: uuid.NewString()}
	ctx = turnstatuscmd.ContextWithIdentity(ctx, identity)
	if r == nil || r.store == nil {
		return ctx, nil
	}
	_, err := r.store.Start(ctx, turnstatuscmd.Start{Identity: identity, StartedAt: startedAt, BootID: r.bootID})
	return ctx, err
}

func (r *Recorder) Observe(ctx context.Context, kind turnstatuscmd.ActivityKind, occurredAt time.Time) error {
	if r == nil || r.store == nil {
		return nil
	}
	identity, ok := turnstatuscmd.IdentityFromContext(ctx)
	if !ok {
		return nil
	}
	_, err := r.store.Observe(ctx, turnstatuscmd.Activity{Identity: identity, Kind: kind, OccurredAt: occurredAt})
	return err
}

func (r *Recorder) Complete(ctx context.Context, outcome turnstatuscmd.Outcome, completedAt time.Time) error {
	if r == nil || r.store == nil {
		return nil
	}
	identity, ok := turnstatuscmd.IdentityFromContext(ctx)
	if !ok {
		return nil
	}
	_, err := r.store.Complete(ctx, turnstatuscmd.Completion{Identity: identity, Outcome: outcome, CompletedAt: completedAt})
	return err
}

func (r *Recorder) BootID() string { return r.bootID }
