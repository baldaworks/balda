package state

import (
	"path/filepath"
	"testing"
)

func TestScheduledOutputIsDurableAndIdempotent(t *testing.T) {
	provider, err := NewSQLiteProvider(t.Context(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = provider.Close() }()
	const jobID = "scheduled-daily-slot"
	if _, err := provider.Jobs().CreateJob(t.Context(), JobRecord{
		ID: jobID, SessionID: "sch-test", Objective: "input", Status: JobStatusRunning,
	}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := provider.Jobs().RecordScheduledOutput(t.Context(), jobID, "provider output"); err != nil {
			t.Fatalf("record output attempt %d: %v", i, err)
		}
	}
	if err := provider.Jobs().RecordScheduledOutput(t.Context(), jobID, "different output"); err == nil {
		t.Fatal("conflicting provider output replaced the first result")
	}
	if err := provider.Jobs().UpdateJobStatus(t.Context(), jobID, JobStatusCompleted, ""); err != nil {
		t.Fatal(err)
	}
	job, found, err := provider.Jobs().GetJob(t.Context(), jobID)
	if err != nil || !found || job.Result != "provider output" {
		t.Fatalf("durable output = %+v, found=%v, err=%v", job, found, err)
	}
}

func TestWebhookOutputIsDurableAndIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	provider, err := NewSQLiteProvider(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	const jobID = "webhook-main-123"
	if _, err := provider.Jobs().CreateJob(t.Context(), JobRecord{
		ID: jobID, SessionID: "wh-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Objective: "input", Status: JobStatusRunning,
	}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := provider.Jobs().RecordPrivateOutput(t.Context(), jobID, "output", false); err != nil {
			t.Fatal(err)
		}
	}
	if err := provider.Jobs().RecordPrivateOutput(t.Context(), jobID, "changed", false); err == nil {
		t.Fatal("conflicting replay changed the webhook output")
	}
	if err := provider.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewSQLiteProvider(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	job, found, err := reopened.Jobs().GetJob(t.Context(), jobID)
	if err != nil || !found || job.Result != "output" || job.Objective != "input" {
		t.Fatalf("webhook input/output = %+v, found=%t err=%v", job, found, err)
	}
}
