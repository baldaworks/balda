package jobexec

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/balda/internal/apps/balda/webhookcmd"
	"github.com/baldaworks/go-actorlayer"
	"github.com/rs/zerolog"
)

type webhookRunCloser struct {
	calls    []string
	failOnce bool
}

func (c *webhookRunCloser) CloseRunSession(_ context.Context, sessionID, userID string) error {
	if c.failOnce {
		c.failOnce = false
		return errors.New("interrupted cleanup")
	}
	c.calls = append(c.calls, sessionID+":"+userID)
	return nil
}

func createWebhookFinalizerFixture(t *testing.T, report bool, output string) (state.Provider, string, string) {
	t.Helper()
	provider, err := state.NewSQLiteProvider(t.Context(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	const jobID = "webhook-events-123"
	const sessionID = "wh-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	admission := webhookcmd.Admission{
		RouteName: "events", DedupeKey: "event-123", RequestID: "request-123",
		Prompt: "private input", JobID: jobID, SessionID: sessionID, CreatedAt: time.Now().UTC(),
	}
	if report {
		admission.ReportTo = &deliverycmd.Locator{SessionID: "tg-report", ChannelType: "telegram",
			AddressKey: "10:42", AddressJSON: `{"chat_id":10,"topic_id":42}`}
	}
	if _, created, err := provider.WebhookAdmissions().Create(t.Context(), admission); err != nil || !created {
		t.Fatalf("create admission: created=%t err=%v", created, err)
	}
	if _, err := provider.Jobs().CreateJob(t.Context(), state.JobRecord{
		ID: jobID, SessionID: sessionID, Objective: admission.Prompt, Result: output,
		Status: state.JobStatusCompleted, CreatedBy: sessionID,
		PrivateRunKind: state.PrivateRunKindWebhook,
	}); err != nil {
		t.Fatal(err)
	}
	return provider, jobID, sessionID
}

func TestWebhookFinalizerClosesNoReportRunAndRetainsInputOutput(t *testing.T) {
	provider, jobID, sessionID := createWebhookFinalizerFixture(t, false, "final output")
	closer := &webhookRunCloser{}
	finalizer := NewWebhookRunFinalizer(provider.Jobs(), provider.WebhookAdmissions(),
		provider.Jobs(), closer, &webhookDispatchFixture{}, zerolog.Nop())
	if err := finalizer.Settle(t.Context(), 10); err != nil {
		t.Fatal(err)
	}
	if len(closer.calls) != 1 || closer.calls[0] != sessionID+":"+sessionID {
		t.Fatalf("cleanup calls = %v", closer.calls)
	}
	if err := finalizer.Settle(t.Context(), 10); err != nil {
		t.Fatal(err)
	}
	if len(closer.calls) != 1 {
		t.Fatalf("duplicate cleanup = %v", closer.calls)
	}
	job, found, err := provider.Jobs().GetJob(t.Context(), jobID)
	if err != nil || !found || job.PrivateRunClosedAt == "" ||
		job.Objective != "private input" || job.Result != "final output" {
		t.Fatalf("retained job = %+v, found=%t err=%v", job, found, err)
	}
}

func TestWebhookFinalizerWaitsForReportAndRetriesCleanupAfterRestart(t *testing.T) {
	provider, jobID, sessionID := createWebhookFinalizerFixture(t, true, "final output")
	closer := &webhookRunCloser{failOnce: true}
	finalizer := NewWebhookRunFinalizer(provider.Jobs(), provider.WebhookAdmissions(),
		provider.Jobs(), closer, &webhookDispatchFixture{}, zerolog.Nop())
	if err := finalizer.Settle(t.Context(), 10); err != nil || len(closer.calls) != 0 {
		t.Fatalf("cleanup before report: calls=%v err=%v", closer.calls, err)
	}
	key := jobID + ":delivery:final"
	if _, _, err := provider.Jobs().ReserveDelivery(t.Context(), state.DeliveryRecord{
		ID: "final-delivery", DeliveryKey: key, JobID: jobID, SessionID: sessionID,
		Channel: "telegram", AddressKey: "10:42", Kind: "delivery",
		Payload: `{"text":"final output"}`, PayloadHash: "final-hash",
	}); err != nil {
		t.Fatal(err)
	}
	if err := finalizer.Settle(t.Context(), 10); err != nil || len(closer.calls) != 0 {
		t.Fatalf("cleanup before sent receipt: calls=%v err=%v", closer.calls, err)
	}
	if err := provider.Jobs().MarkDeliverySent(t.Context(), key, "provider-message"); err != nil {
		t.Fatal(err)
	}
	if err := finalizer.Settle(t.Context(), 10); err == nil {
		t.Fatal("interrupted cleanup marked run closed")
	}
	restarted := NewWebhookRunFinalizer(provider.Jobs(), provider.WebhookAdmissions(),
		provider.Jobs(), closer, &webhookDispatchFixture{}, zerolog.Nop())
	if err := restarted.Settle(t.Context(), 10); err != nil {
		t.Fatal(err)
	}
	if len(closer.calls) != 1 {
		t.Fatalf("restart cleanup calls = %v", closer.calls)
	}
	job, _, err := provider.Jobs().GetJob(t.Context(), jobID)
	if err != nil || job.PrivateRunClosedAt == "" || job.Result != "final output" {
		t.Fatalf("job after restart cleanup = %+v, err=%v", job, err)
	}
}

func TestWebhookFinalizerWaitsForTransientFailureAndClosesPermanentFailure(t *testing.T) {
	provider, jobID, sessionID := createWebhookFinalizerFixture(t, true, "final output")
	closer := &webhookRunCloser{}
	finalizer := NewWebhookRunFinalizer(provider.Jobs(), provider.WebhookAdmissions(),
		provider.Jobs(), closer, &webhookDispatchFixture{}, zerolog.Nop())
	key := jobID + ":delivery:final"
	if _, _, err := provider.Jobs().ReserveDelivery(t.Context(), state.DeliveryRecord{
		ID: "failed-delivery", DeliveryKey: key, JobID: jobID, SessionID: sessionID,
		Channel: "telegram", AddressKey: "10:42", Kind: "delivery",
		Payload: `{"text":"final output"}`, PayloadHash: "final-hash",
	}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Jobs().MarkDeliveryFailed(t.Context(), key, "temporary"); err != nil {
		t.Fatal(err)
	}
	if err := finalizer.Settle(t.Context(), 10); err != nil || len(closer.calls) != 0 {
		t.Fatalf("transient failure closed run: calls=%v err=%v", closer.calls, err)
	}
	if err := provider.Jobs().MarkDeliveryFailed(t.Context(), key, "permanent"); err != nil {
		t.Fatal(err)
	}
	if err := finalizer.Settle(t.Context(), 10); err != nil || len(closer.calls) != 1 {
		t.Fatalf("permanent failure cleanup: calls=%v err=%v", closer.calls, err)
	}
}

func TestWebhookFinalizerRecordsTerminalFailureBeforeReporting(t *testing.T) {
	provider, jobID, sessionID := createWebhookFinalizerFixture(t, true, "")
	closer := &webhookRunCloser{}
	dispatcher := &webhookDispatchFixture{}
	finalizer := NewWebhookRunFinalizer(provider.Jobs(), provider.WebhookAdmissions(),
		provider.Jobs(), closer, dispatcher, zerolog.Nop())
	if err := finalizer.Settle(t.Context(), 10); err != nil {
		t.Fatal(err)
	}
	if len(closer.calls) != 0 || len(dispatcher.attempts) != 1 {
		t.Fatalf("terminal failure: cleanup=%v reports=%d", closer.calls, len(dispatcher.attempts))
	}
	job, _, err := provider.Jobs().GetJob(t.Context(), jobID)
	if err != nil || job.Result == "" || job.PrivateRunClosedAt != "" {
		t.Fatalf("terminal failure output = %+v, err=%v", job, err)
	}
	var report deliverycmd.Payload
	if err := actorlayer.UnmarshalPayload(dispatcher.attempts[0].Payload, &report); err != nil {
		t.Fatal(err)
	}
	if report.Text != job.Result || report.DeliveryFormat != deliveryfmt.DeliveryFormatNone {
		t.Fatalf("terminal failure report = %+v", report)
	}
	key := jobID + ":delivery:final"
	if _, _, err := provider.Jobs().ReserveDelivery(t.Context(), state.DeliveryRecord{
		ID: "terminal-failure-delivery", DeliveryKey: key, JobID: jobID, SessionID: sessionID,
		Channel: "telegram", AddressKey: "10:42", Kind: "delivery",
		Payload: `{"text":"failure"}`, PayloadHash: "failure-hash",
	}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Jobs().MarkDeliverySent(t.Context(), key, "provider-message"); err != nil {
		t.Fatal(err)
	}
	if err := finalizer.Settle(t.Context(), 10); err != nil || len(closer.calls) != 1 {
		t.Fatalf("terminal failure cleanup: calls=%v err=%v", closer.calls, err)
	}
}

func TestWebhookFinalizerRevisitsFailedCleanupDespiteNewerJobs(t *testing.T) {
	provider, firstID, _ := createWebhookFinalizerFixture(t, false, "first output")
	first, _, err := provider.Jobs().GetJob(t.Context(), firstID)
	if err != nil {
		t.Fatal(err)
	}
	cutoff := first.CreatedAt.Add(time.Minute)
	createRun := func(jobID, sessionID string, createdAt time.Time) {
		t.Helper()
		admission := webhookcmd.Admission{
			RouteName: "events", DedupeKey: jobID, RequestID: jobID,
			Prompt: "input", JobID: jobID, SessionID: sessionID, CreatedAt: createdAt,
		}
		if _, created, err := provider.WebhookAdmissions().Create(t.Context(), admission); err != nil || !created {
			t.Fatalf("create admission %q: created=%t err=%v", jobID, created, err)
		}
		if _, err := provider.Jobs().CreateJob(t.Context(), state.JobRecord{
			ID: jobID, SessionID: sessionID, Objective: admission.Prompt, Result: "output",
			Status: state.JobStatusCompleted, CreatedBy: sessionID,
			PrivateRunKind: state.PrivateRunKindWebhook, CreatedAt: createdAt,
		}); err != nil {
			t.Fatal(err)
		}
	}
	createRun("webhook-events-124", "wh-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", first.CreatedAt)
	closer := &webhookRunCloser{failOnce: true}
	finalizer := NewWebhookRunFinalizer(provider.Jobs(), provider.WebhookAdmissions(),
		provider.Jobs(), closer, &webhookDispatchFixture{}, zerolog.Nop())
	finalizer.now = func() time.Time { return cutoff }
	if err := finalizer.Settle(t.Context(), 1); err == nil {
		t.Fatal("first cleanup did not fail")
	}
	if err := finalizer.Settle(t.Context(), 1); err != nil {
		t.Fatal(err)
	}
	createRun("webhook-events-125", "wh-cccccccccccccccccccccccccccccccc", cutoff.Add(time.Minute))
	if err := finalizer.Settle(t.Context(), 1); err != nil {
		t.Fatal(err)
	}
	first, _, err = provider.Jobs().GetJob(t.Context(), firstID)
	if err != nil || first.PrivateRunClosedAt == "" {
		t.Fatalf("older failed cleanup was starved: job=%+v err=%v", first, err)
	}
}
