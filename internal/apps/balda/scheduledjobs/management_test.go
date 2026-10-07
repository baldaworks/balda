package scheduledjobs

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/envelopetarget"
	"github.com/baldaworks/balda/internal/apps/balda/schedulecmd"
	"github.com/baldaworks/balda/internal/apps/balda/state"
)

type recordingScheduleManagementStore struct{ jobs state.ScheduledJobStore }

type unavailableScheduleResolver struct{}

func (unavailableScheduleResolver) ResolveAlias(context.Context, string) (envelopetarget.Resolved, error) {
	return envelopetarget.Resolved{}, envelopetarget.ErrResolutionUnavailable
}

func (*recordingScheduleManagementStore) CheckAuthority(context.Context, schedulecmd.Authority) error {
	return nil
}
func (s *recordingScheduleManagementStore) Save(ctx context.Context, m state.ScheduleMutation) error {
	return s.jobs.Upsert(ctx, m.Record)
}

func TestManagementLifecyclePreservesDisabledSelectionAndArchive(t *testing.T) {
	ctx := t.Context()
	jobs := newSchedulerJobStore(t)
	now := time.Date(2026, 10, 8, 8, 0, 0, 0, time.UTC)
	m := NewManagement(jobs, &recordingScheduleManagementStore{jobs: jobs}, newOwnerStoreForTest(t, 101, 9001))
	m.now = func() time.Time { return now }
	authority := schedulecmd.Authority{UserID: "admin", SessionID: "session", At: now}
	definition := schedulecmd.Definition{ID: "daily-review", Cron: "0 9 * * *", Content: "review",
		Target: schedulecmd.Target{Kind: envelopetarget.TargetAlias, Key: envelopetarget.AliasOwner}}
	created, err := m.Create(ctx, schedulecmd.Create{Definition: definition, Authority: authority})
	if err != nil || !created.Enabled || created.Version != 1 || created.Source != state.ScheduledJobSourceManaged ||
		!created.NextRunAt.Equal(time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("created = %+v, %v", created, err)
	}
	inventory, err := m.Inventory(ctx, authority)
	if err != nil || len(inventory) != 1 || inventory[0].Definition.Content != "" {
		t.Fatalf("inventory leaked content = %+v, %v", inventory, err)
	}
	disabled, err := m.SetEnabled(ctx, schedulecmd.ChangeSelection{ID: definition.ID, ExpectedVersion: 1, Enabled: false, Authority: authority})
	if err != nil || disabled.Enabled || disabled.Version != 2 {
		t.Fatalf("disabled = %+v, %v", disabled, err)
	}
	definition.Content = "updated review"
	updated, err := m.Update(ctx, schedulecmd.Update{ID: definition.ID, ExpectedVersion: 2, Definition: definition, Authority: authority})
	if err != nil || updated.Enabled || updated.Version != 3 || updated.Definition.Content != definition.Content {
		t.Fatalf("updated = %+v, %v", updated, err)
	}
	if _, err := m.Update(ctx, schedulecmd.Update{ID: definition.ID, ExpectedVersion: 2, Definition: definition, Authority: authority}); !errors.Is(err, schedulecmd.ErrConflict) {
		t.Fatalf("stale update = %v", err)
	}
	archived, err := m.Delete(ctx, schedulecmd.Delete{ID: definition.ID, ExpectedVersion: 3, Authority: authority})
	if err != nil || !archived.Deleted || archived.Enabled || archived.Version != 4 {
		t.Fatalf("archived = %+v, %v", archived, err)
	}
	inventory, err = m.Inventory(ctx, authority)
	if err != nil || len(inventory) != 0 {
		t.Fatalf("inventory after archive = %+v, %v", inventory, err)
	}
}

func TestManagementMapsDestinationBackendFailureToUnavailable(t *testing.T) {
	jobs := newSchedulerJobStore(t)
	m := NewManagement(jobs, &recordingScheduleManagementStore{jobs: jobs}, unavailableScheduleResolver{})
	definition := schedulecmd.Definition{ID: "daily", Cron: "0 9 * * *", Content: "review",
		Target: schedulecmd.Target{Kind: envelopetarget.TargetAlias, Key: envelopetarget.AliasOwner}}
	_, err := m.Create(t.Context(), schedulecmd.Create{Definition: definition})
	if !errors.Is(err, schedulecmd.ErrUnavailable) {
		t.Fatalf("Create() = %v, want unavailable", err)
	}
}

func TestManagementRejectsInvalidDefinitionAndConfigMutation(t *testing.T) {
	jobs := newSchedulerJobStore(t)
	now := time.Date(2026, 10, 8, 8, 0, 0, 0, time.UTC)
	m := NewManagement(jobs, &recordingScheduleManagementStore{jobs: jobs}, newOwnerStoreForTest(t, 101, 9001))
	m.now = func() time.Time { return now }
	authority := schedulecmd.Authority{At: now}
	definition := schedulecmd.Definition{ID: "bad/id", Cron: "@every 1m", Content: "review",
		Target: schedulecmd.Target{Kind: envelopetarget.TargetAlias, Key: envelopetarget.AliasOwner}}
	if _, err := m.Create(t.Context(), schedulecmd.Create{Definition: definition, Authority: authority}); !errors.Is(err, schedulecmd.ErrInvalid) {
		t.Fatalf("invalid definition = %v", err)
	}
	config := state.ScheduledJobRecord{JobID: "config-daily", Source: state.ScheduledJobSourceConfig,
		Enabled: true, DefinitionVersion: 1, SessionID: "tg-9001-0", ChannelType: state.ChannelTypeTelegram,
		AddressKey: "9001:0", AddressJSON: `{}`, Content: "config", ScheduleSpec: "0 9 * * *",
		NextRunAt: now.Add(time.Hour)}
	if err := jobs.Upsert(t.Context(), config); err != nil {
		t.Fatal(err)
	}
	if _, err := m.SetEnabled(t.Context(), schedulecmd.ChangeSelection{ID: config.JobID, ExpectedVersion: 1, Authority: authority}); !errors.Is(err, schedulecmd.ErrForbidden) {
		t.Fatalf("config mutation = %v", err)
	}
}
