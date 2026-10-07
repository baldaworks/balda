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
