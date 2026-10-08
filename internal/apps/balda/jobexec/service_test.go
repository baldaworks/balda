package jobexec

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/actorcmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
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

type webhookLifecycleFixture struct {
	mu      sync.Mutex
	jobs    map[string]state.JobRecord
	creates int
	marks   int
}

func (f *webhookLifecycleFixture) Create(_ context.Context, record state.JobRecord, _ string, _ any) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, exists := f.jobs[record.ID]; exists {
		return false, nil
	}
	f.jobs[record.ID] = record
	f.creates++
	return true, nil
}

func (f *webhookLifecycleFixture) Get(_ context.Context, id string) (state.JobRecord, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	record, found := f.jobs[id]
	return record, found, nil
}

func (f *webhookLifecycleFixture) MarkStatus(_ context.Context, id, status, _, _, _ string, _ any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	record := f.jobs[id]
	if terminalJobExecution(record.Status) && record.Status != status {
		return errors.New("invalid runtime job transition: terminal status")
	}
	record.Status = status
	f.jobs[id] = record
	f.marks++
	return nil
}

func (f *webhookLifecycleFixture) RebindScheduledSession(context.Context, string, string, string) (bool, error) {
	return false, nil
}

func (f *webhookLifecycleFixture) setStatus(id, status string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	record := f.jobs[id]
	record.Status = status
	f.jobs[id] = record
}

type webhookDispatchFixture struct {
	mu        sync.Mutex
	attempts  []actorlayer.Envelope
	accepted  map[string]bool
	failNext  error
	onSuccess func()
}

func (f *webhookDispatchFixture) Dispatch(_ context.Context, env actorlayer.Envelope) (*actortransport.DispatchReceipt, error) {
	f.mu.Lock()
	f.attempts = append(f.attempts, env)
	if f.failNext != nil {
		err := f.failNext
		f.failNext = nil
		f.mu.Unlock()
		return nil, err
	}
	if f.accepted == nil {
		f.accepted = make(map[string]bool)
	}
	duplicate := f.accepted[env.DedupeKey]
	f.accepted[env.DedupeKey] = true
	callback := f.onSuccess
	f.mu.Unlock()
	if callback != nil {
		callback()
	}
	return &actortransport.DispatchReceipt{MsgID: env.ID, Duplicate: duplicate}, nil
}

func webhookJobFixture(t *testing.T) (actorlayer.Envelope, turncmd.SessionTurnPayload, string) {
	t.Helper()
	payload := turncmd.SessionTurnPayload{Text: "webhook input", Source: turncmd.SourceWebhook,
		DedupeKey: "webhook:events:req-1",
		Locator:   deliverycmd.Locator{ChannelType: "webhook", AddressKey: "wh-test", AddressJSON: `{}`, SessionID: "wh-test"}}
	env, jobID, err := turncmd.WebhookJobEnvelope(payload, "events", "req-1")
	if err != nil {
		t.Fatal(err)
	}
	payload.JobID = jobID
	payload.DedupeKey += ":session"
	return env, payload, jobID
}

func TestWebhookJobReplaysAfterDispatchFailureAndRestart(t *testing.T) {
	env, payload, jobID := webhookJobFixture(t)
	tasks := &webhookLifecycleFixture{jobs: make(map[string]state.JobRecord)}
	dispatcher := &webhookDispatchFixture{failNext: errors.New("publication failed")}
	firstProcess := New(tasks, dispatcher)
	if err := firstProcess.DispatchWebhookSessionTurn(t.Context(), env, payload); err == nil {
		t.Fatal("failed publication was accepted")
	}
	stored, _, _ := tasks.Get(t.Context(), jobID)
	if stored.Status != state.JobStatusCreated || tasks.marks != 0 {
		t.Fatalf("job before retry = %+v, marks=%d", stored, tasks.marks)
	}
	restarted := New(tasks, dispatcher)
	if err := restarted.DispatchWebhookSessionTurn(t.Context(), env, payload); err != nil {
		t.Fatal(err)
	}
	stored, _, _ = tasks.Get(t.Context(), jobID)
	if stored.Status != state.JobStatusRunning || tasks.creates != 1 || tasks.marks != 1 ||
		len(dispatcher.attempts) != 2 || len(dispatcher.accepted) != 1 {
		t.Fatalf("replay: job=%+v, creates=%d, marks=%d, dispatches=%d, accepted=%d",
			stored, tasks.creates, tasks.marks, len(dispatcher.attempts), len(dispatcher.accepted))
	}
	first, second := dispatcher.attempts[0], dispatcher.attempts[1]
	if first.ID == "" || first.ID != second.ID || first.DedupeKey != second.DedupeKey ||
		first.To != second.To || !bytes.Equal(first.Payload.Data, second.Payload.Data) {
		t.Fatalf("replayed session envelope changed: first=%+v second=%+v", first, second)
	}
}

func TestWebhookJobConcurrentRedeliveryUsesOneSessionTurnIdentity(t *testing.T) {
	env, payload, jobID := webhookJobFixture(t)
	tasks := &webhookLifecycleFixture{jobs: make(map[string]state.JobRecord)}
	dispatcher := &webhookDispatchFixture{}
	service := New(tasks, dispatcher)
	const deliveries = 12
	var group sync.WaitGroup
	errs := make([]error, deliveries)
	for i := range errs {
		group.Add(1)
		go func() {
			defer group.Done()
			errs[i] = service.DispatchWebhookSessionTurn(t.Context(), env, payload)
		}()
	}
	group.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("delivery %d: %v", i, err)
		}
	}
	stored, _, _ := tasks.Get(t.Context(), jobID)
	if stored.Status != state.JobStatusRunning || tasks.creates != 1 || len(dispatcher.accepted) != 1 {
		t.Fatalf("concurrent replay: job=%+v, creates=%d, accepted=%d", stored, tasks.creates, len(dispatcher.accepted))
	}
	before := len(dispatcher.attempts)
	if err := service.DispatchWebhookSessionTurn(t.Context(), env, payload); err != nil || len(dispatcher.attempts) != before {
		t.Fatalf("running job redelivery republished: error=%v, attempts=%d, want %d", err, len(dispatcher.attempts), before)
	}
	for _, attempt := range dispatcher.attempts {
		if attempt.ID != dispatcher.attempts[0].ID || attempt.DedupeKey != dispatcher.attempts[0].DedupeKey {
			t.Fatalf("concurrent replay changed identity: %+v", dispatcher.attempts)
		}
	}
	tasks.setStatus(jobID, state.JobStatusCompleted)
	if err := service.DispatchWebhookSessionTurn(t.Context(), env, payload); err != nil || len(dispatcher.attempts) != before {
		t.Fatalf("terminal replay error=%v, attempts=%d, want %d", err, len(dispatcher.attempts), before)
	}
}

func TestWebhookJobCompletionBeforeRunningMarkSettles(t *testing.T) {
	env, payload, jobID := webhookJobFixture(t)
	tasks := &webhookLifecycleFixture{jobs: make(map[string]state.JobRecord)}
	dispatcher := &webhookDispatchFixture{onSuccess: func() { tasks.setStatus(jobID, state.JobStatusCompleted) }}
	if err := New(tasks, dispatcher).DispatchWebhookSessionTurn(t.Context(), env, payload); err != nil {
		t.Fatalf("fast completed turn should settle: %v", err)
	}
	stored, _, _ := tasks.Get(t.Context(), jobID)
	if stored.Status != state.JobStatusCompleted {
		t.Fatalf("terminal status changed: %+v", stored)
	}
}
