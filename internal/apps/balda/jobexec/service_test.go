package jobexec

import (
	"context"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/actorcmd"
	"github.com/baldaworks/balda/internal/apps/balda/locatorref"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
)

type scheduleJobLifecycleFixture struct{ jobs map[string]state.JobRecord }

func (f *scheduleJobLifecycleFixture) Create(_ context.Context, record state.JobRecord, _ string, _ any) (bool, error) {
	if _, exists := f.jobs[record.ID]; exists {
		return false, nil
	}
	f.jobs[record.ID] = record
	return true, nil
}

func (f *scheduleJobLifecycleFixture) Get(_ context.Context, id string) (state.JobRecord, bool, error) {
	record, found := f.jobs[id]
	return record, found, nil
}

func (f *scheduleJobLifecycleFixture) MarkStatus(_ context.Context, id, status, _, _, _ string, _ any) error {
	record := f.jobs[id]
	record.Status = status
	f.jobs[id] = record
	return nil
}

func (f *scheduleJobLifecycleFixture) RebindScheduledSession(_ context.Context, id, oldID, newID string) (bool, error) {
	record := f.jobs[id]
	if record.SessionID != oldID {
		return false, nil
	}
	record.SessionID = newID
	record.AssignedActor = "session:" + newID
	f.jobs[id] = record
	return true, nil
}

type scheduleDispatchFixture struct{ envelopes []actorlayer.Envelope }

type scheduleModeFixture struct{ oneShot bool }

func (f scheduleModeFixture) IsOneShot(context.Context, string) (bool, error) { return f.oneShot, nil }

func (f *scheduleDispatchFixture) Dispatch(_ context.Context, env actorlayer.Envelope) (*actortransport.DispatchReceipt, error) {
	f.envelopes = append(f.envelopes, env)
	return &actortransport.DispatchReceipt{}, nil
}

func TestStartScheduledJobIsolatesRunsAndReplaysOldWire(t *testing.T) {
	recipient, err := locatorref.Parse("telegram:9001:0")
	if err != nil {
		t.Fatal(err)
	}
	tasks := &scheduleJobLifecycleFixture{jobs: make(map[string]state.JobRecord)}
	dispatcher := &scheduleDispatchFixture{}
	service := NewWithScheduleModes(tasks, dispatcher, scheduleModeFixture{})
	request := ScheduledJobRequest{JobID: "daily", Content: "review", Locator: recipient,
		ReportTo: &recipient, UserID: "tg-101"}
	for _, id := range []string{"execution-1", "execution-1", "execution-2"} {
		env := actorlayer.Envelope{ID: id, Meta: actorcmd.WithJobIDMeta(nil, id), DedupeKey: id}
		if err := service.StartScheduledJob(t.Context(), env, request); err != nil {
			t.Fatal(err)
		}
	}
	if len(tasks.jobs) != 2 || len(dispatcher.envelopes) != 3 {
		t.Fatalf("jobs=%d, dispatches=%d", len(tasks.jobs), len(dispatcher.envelopes))
	}
	first := tasks.jobs["execution-1"].SessionID
	second := tasks.jobs["execution-2"].SessionID
	if first == "" || first == recipient.SessionID || first == second ||
		tasks.jobs["execution-1"].AssignedActor != "session:"+first {
		t.Fatalf("execution identities: first=%+v, second=%+v", tasks.jobs["execution-1"], tasks.jobs["execution-2"])
	}
	for i, want := range []string{first, first, second} {
		var payload turncmd.SessionTurnPayload
		if err := actorlayer.UnmarshalPayload(dispatcher.envelopes[i].Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Locator.SessionID != want || payload.ReportTo == nil ||
			payload.ReportTo.SessionID != recipient.SessionID || !payload.Deliver {
			t.Fatalf("turn %d = %+v", i, payload)
		}
	}
}

func TestStartScheduledJobWithoutLocatorUsesPrivateSessionAndNoDelivery(t *testing.T) {
	dispatcher := &scheduleDispatchFixture{}
	service := NewWithScheduleModes(nil, dispatcher, scheduleModeFixture{})
	const executionJobID = "scheduled-daily-slot"
	env := actorlayer.Envelope{ID: executionJobID,
		Meta: actorcmd.WithJobIDMeta(nil, executionJobID), DedupeKey: executionJobID}
	if err := service.StartScheduledJob(t.Context(), env, ScheduledJobRequest{
		JobID: "daily", Content: "review",
	}); err != nil {
		t.Fatal(err)
	}
	if len(dispatcher.envelopes) != 1 {
		t.Fatalf("published turns = %d", len(dispatcher.envelopes))
	}
	var payload turncmd.SessionTurnPayload
	if err := actorlayer.UnmarshalPayload(dispatcher.envelopes[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Locator.SessionID != turncmd.ScheduledExecutionSessionID(executionJobID) ||
		payload.Locator.ChannelType != turncmd.SourceSchedule || payload.ReportTo != nil ||
		payload.Deliver || payload.UserID == "" {
		t.Fatalf("no-report private turn = %+v", payload)
	}
}

func TestStartInternalOneShotKeepsOrdinarySessionAndOptionalDelivery(t *testing.T) {
	locator, err := locatorref.Parse("telegram:9001:0")
	if err != nil {
		t.Fatal(err)
	}
	dispatcher := &scheduleDispatchFixture{}
	service := NewWithScheduleModes(nil, dispatcher, scheduleModeFixture{oneShot: true})
	env := actorlayer.Envelope{ID: "one-shot", Meta: actorcmd.WithJobIDMeta(nil, "one-shot")}
	if err := service.StartScheduledJob(t.Context(), env, ScheduledJobRequest{
		JobID: "wait-1", Content: "wake", Locator: locator, UserID: "tg-101",
	}); err != nil {
		t.Fatal(err)
	}
	var payload turncmd.SessionTurnPayload
	if err := actorlayer.UnmarshalPayload(dispatcher.envelopes[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Locator.SessionID != locator.SessionID || payload.ReportTo != nil || payload.Deliver ||
		payload.ScheduleOneShot == nil || !*payload.ScheduleOneShot {
		t.Fatalf("one-shot turn = %+v", payload)
	}
}

func TestStartScheduledJobRebindsPreUpgradeRecipientScopedJob(t *testing.T) {
	recipient, err := locatorref.Parse("telegram:9001:0")
	if err != nil {
		t.Fatal(err)
	}
	tasks := &scheduleJobLifecycleFixture{jobs: map[string]state.JobRecord{
		"execution-old": {ID: "execution-old", SessionID: recipient.SessionID, Status: state.JobStatusRunning},
	}}
	dispatcher := &scheduleDispatchFixture{}
	service := NewWithScheduleModes(tasks, dispatcher, scheduleModeFixture{})
	env := actorlayer.Envelope{ID: "replay", Meta: actorcmd.WithJobIDMeta(nil, "execution-old")}
	if err := service.StartScheduledJob(t.Context(), env, ScheduledJobRequest{
		JobID: "daily", Content: "review", Locator: recipient, UserID: "tg-101",
	}); err != nil {
		t.Fatal(err)
	}
	privateID := turncmd.ScheduledExecutionSessionID("execution-old")
	if got := tasks.jobs["execution-old"]; got.SessionID != privateID || got.AssignedActor != "session:"+privateID {
		t.Fatalf("pre-upgrade job scope = %+v", got)
	}
	if len(dispatcher.envelopes) != 1 || dispatcher.envelopes[0].To.Key != privateID {
		t.Fatalf("replay dispatch = %+v", dispatcher.envelopes)
	}
}
