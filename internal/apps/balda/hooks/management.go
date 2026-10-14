// Package hooks owns managed agent hook validation and execution policy.
package hooks

import (
	"context"
	"math"
	"slices"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/hookcmd"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"github.com/google/uuid"
)

// Persistence is the consuming port for guarded hook configuration storage.
type Persistence interface {
	Load(ctx context.Context) (hookcmd.Record, error)
	CheckAuthority(ctx context.Context, authority hookcmd.Authority) error
	Commit(ctx context.Context, mutation hookcmd.Mutation) error
}

// Management owns per-definition mutations of the shared runtime record.
type Management struct{ store Persistence }

// NewManagement binds the existing application's guarded persistence owner.
func NewManagement(store Persistence) *Management { return &Management{store: store} }

// Inventory returns a configuration only to a currently authorized administrator.
func (m *Management) Inventory(ctx context.Context, a hookcmd.Authority) (hookcmd.Record, error) {
	if m == nil || m.store == nil {
		return hookcmd.Record{}, hookcmd.ErrUnavailable
	}
	if err := m.store.CheckAuthority(ctx, a); err != nil {
		return hookcmd.Record{}, err
	}
	return m.store.Load(ctx)
}

// Save changes one definition without silently overwriting a concurrent edit.
func (m *Management) Save(ctx context.Context, request hookcmd.Save) error {
	if request.Definition.Validate() != nil {
		return hookcmd.ErrInvalid
	}
	return m.mutate(ctx, request.Definition.Name, request.ExpectedVersion, request.Authority, func(record *hookcmd.Record, index int) error {
		if request.Create {
			if index >= 0 {
				return hookcmd.ErrConflict
			}
			record.Definitions = append(record.Definitions, request.Definition)
			return nil
		}
		if index < 0 {
			return hookcmd.ErrNotFound
		}
		record.Definitions[index] = request.Definition
		return nil
	})
}

// Delete removes only the selected definition and retains an initialized record.
func (m *Management) Delete(ctx context.Context, request hookcmd.Delete) error {
	return m.mutate(ctx, request.Name, request.ExpectedVersion, request.Authority, func(record *hookcmd.Record, index int) error {
		if index < 0 {
			return hookcmd.ErrNotFound
		}
		record.Definitions = slices.Delete(record.Definitions, index, index+1)
		return nil
	})
}

func (m *Management) mutate(ctx context.Context, name string, expected int64, authority hookcmd.Authority, change func(*hookcmd.Record, int) error) error {
	if !hookcmd.ValidName(name) || expected < 0 || expected == math.MaxInt64 {
		return hookcmd.ErrInvalid
	}
	record, err := m.Inventory(ctx, authority)
	if err != nil {
		return err
	}
	if record.Version != expected {
		return hookcmd.ErrConflict
	}
	index := slices.IndexFunc(record.Definitions, func(d hookcmd.Definition) bool { return d.Name == name })
	if err := change(&record, index); err != nil {
		return err
	}
	record.Version++
	stamp := time.Now().UTC()
	if previous, err := time.Parse(time.RFC3339Nano, record.UpdatedAt); err == nil && !stamp.After(previous) {
		stamp = previous.Add(time.Nanosecond)
	}
	record.UpdatedAt = stamp.Format(time.RFC3339Nano)
	if record.Validate() != nil {
		return hookcmd.ErrInvalid
	}
	audit := usercmd.AuditEvent{ID: uuid.NewString(), Action: usercmd.AuditActionHookChanged, Outcome: usercmd.AuditOutcomeSucceeded, ActorUserID: authority.UserID, ActorSessionID: authority.SessionID, TargetType: usercmd.AuditTargetHook, TargetID: name, Source: "backoffice", OccurredAt: authority.At}
	return m.store.Commit(ctx, hookcmd.Mutation{Record: record, ExpectedVersion: expected, Authority: authority, Audit: audit})
}
