package memory

import (
	"context"
	"math"
	"strconv"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/memorycmd"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"github.com/google/uuid"
)

// ManagementPersistence fences global memory writes with current browser authority.
type ManagementPersistence interface {
	CheckAuthority(ctx context.Context, authority memorycmd.Authority) error
	Commit(ctx context.Context, mutation memorycmd.Mutation) error
}

// Management exposes global facts through the exact Store shared with runtime writers.
type Management struct {
	store       *Store
	persistence ManagementPersistence
}

// NewManagement binds management to the existing runtime Store and guarded persistence.
func NewManagement(store *Store, persistence ManagementPersistence) *Management {
	return &Management{store: store, persistence: persistence}
}

func (m *Management) check(ctx context.Context, a memorycmd.Authority) error {
	if m == nil || m.store == nil || m.persistence == nil {
		return memorycmd.ErrUnavailable
	}
	return m.persistence.CheckAuthority(ctx, a)
}

// Inventory returns a consistent, ordered global inventory filtered by fact text.
func (m *Management) Inventory(ctx context.Context, query string, a memorycmd.Authority) (memorycmd.Inventory, error) {
	if err := m.check(ctx, a); err != nil {
		return memorycmd.Inventory{}, err
	}
	if !m.store.MemoryEnabled() {
		return memorycmd.Inventory{}, nil
	}
	m.store.mu.Lock()
	defer m.store.mu.Unlock()
	rec, err := m.store.loadRecordLocked(ctx)
	if err != nil {
		return memorycmd.Inventory{}, memorycmd.ErrUnavailable
	}
	inv := memorycmd.Inventory{Enabled: true, Version: rec.Version, UpdatedAt: rec.UpdatedAt}
	query = strings.ToLower(strings.TrimSpace(query))
	for _, entry := range rec.Entries {
		if strings.Contains(strings.ToLower(entry.Fact), query) {
			inv.Facts = append(inv.Facts, projectFact(entry))
		}
	}
	return inv, nil
}

// Get loads a selected fact and its global optimistic edit version.
func (m *Management) Get(ctx context.Context, id int64, a memorycmd.Authority) (memorycmd.Editor, error) {
	inv, err := m.Inventory(ctx, "", a)
	if err != nil {
		return memorycmd.Editor{}, err
	}
	if !inv.Enabled {
		return memorycmd.Editor{}, memorycmd.ErrDisabled
	}
	if id <= 0 {
		return memorycmd.Editor{}, memorycmd.ErrInvalid
	}
	for _, fact := range inv.Facts {
		if fact.ID == id {
			return memorycmd.Editor{Fact: fact, ExpectedVersion: inv.Version}, nil
		}
	}
	return memorycmd.Editor{}, memorycmd.ErrNotFound
}

// Create appends an explicit fact without losing concurrent runtime appends.
func (m *Management) Create(ctx context.Context, r memorycmd.Create) (memorycmd.Fact, error) {
	return m.mutate(ctx, 0, r.Fact, 0, r.Authority, usercmd.AuditActionMemoryFactCreated)
}

// Edit replaces one fact against its loaded global version, retaining identity.
func (m *Management) Edit(ctx context.Context, r memorycmd.Edit) (memorycmd.Fact, error) {
	if r.ID <= 0 || r.ExpectedVersion <= 0 {
		return memorycmd.Fact{}, memorycmd.ErrInvalid
	}
	return m.mutate(ctx, r.ID, r.Fact, r.ExpectedVersion, r.Authority, usercmd.AuditActionMemoryFactEdited)
}

// Delete removes one fact while retaining the initialized global boundary.
func (m *Management) Delete(ctx context.Context, r memorycmd.Delete) error {
	if r.ID <= 0 || r.ExpectedVersion <= 0 {
		return memorycmd.ErrInvalid
	}
	_, err := m.mutate(ctx, r.ID, "", r.ExpectedVersion, r.Authority, usercmd.AuditActionMemoryFactDeleted)
	return err
}

func (m *Management) mutate(ctx context.Context, id int64, text string, expected int64, a memorycmd.Authority, action usercmd.AuditAction) (memorycmd.Fact, error) {
	if err := m.check(ctx, a); err != nil {
		return memorycmd.Fact{}, err
	}
	if !m.store.MemoryEnabled() {
		return memorycmd.Fact{}, memorycmd.ErrDisabled
	}
	text = strings.TrimSpace(text)
	if action != usercmd.AuditActionMemoryFactDeleted && text == "" {
		return memorycmd.Fact{}, memorycmd.ErrInvalid
	}
	m.store.mu.Lock()
	defer m.store.mu.Unlock()
	rec, err := m.store.loadRecordLocked(ctx)
	if err != nil {
		return memorycmd.Fact{}, memorycmd.ErrUnavailable
	}
	if rec.Version == math.MaxInt64 {
		return memorycmd.Fact{}, memorycmd.ErrUnavailable
	}
	baseline := rec.Version
	var selected memorycmd.Entry
	index := -1
	if action != usercmd.AuditActionMemoryFactCreated {
		for i, entry := range rec.Entries {
			if entry.Version == id {
				index, selected = i, entry
				break
			}
		}
		if index < 0 {
			return memorycmd.Fact{}, memorycmd.ErrNotFound
		}
		if expected != rec.Version {
			return memorycmd.Fact{}, memorycmd.ErrConflict
		}
	}
	rec.Version++
	rec.UpdatedAt = nextUpdatedAt(rec.UpdatedAt)
	switch action {
	case usercmd.AuditActionMemoryFactCreated:
		selected = memorycmd.Entry{Version: rec.Version, CreatedAt: rec.UpdatedAt, Fact: text}
		rec.Entries = append(rec.Entries, selected)
	case usercmd.AuditActionMemoryFactEdited:
		selected.Fact = text
		rec.Entries[index] = selected
	case usercmd.AuditActionMemoryFactDeleted:
		rec.Entries = append(rec.Entries[:index], rec.Entries[index+1:]...)
	}
	audit := usercmd.AuditEvent{ID: uuid.NewString(), Action: action, Outcome: usercmd.AuditOutcomeSucceeded,
		ActorUserID: a.UserID, ActorSessionID: a.SessionID, TargetType: usercmd.AuditTargetMemoryFact,
		TargetID: strconv.FormatInt(selected.Version, 10), Source: "backoffice", OccurredAt: a.At}
	if err := m.persistence.Commit(ctx, memorycmd.Mutation{Record: rec, ExpectedVersion: baseline, Authority: a, Audit: audit}); err != nil {
		return memorycmd.Fact{}, err
	}
	return projectFact(selected), nil
}

func projectFact(entry memorycmd.Entry) memorycmd.Fact {
	return memorycmd.Fact{ID: entry.Version, CreatedAt: entry.CreatedAt, Fact: entry.Fact}
}
