package state

import (
	"path/filepath"
	"testing"
)

func TestScheduledJobRebindRemovesOldChatCancellationScope(t *testing.T) {
	provider, err := NewSQLiteProvider(t.Context(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = provider.Close() }()
	jobs := provider.Jobs()
	if created, err := jobs.CreateJob(t.Context(), JobRecord{ID: "scheduled-old", SessionID: "tg-9001-0",
		Objective: "review", Status: JobStatusRunning, AssignedActor: "session:tg-9001-0"}); err != nil || !created {
		t.Fatalf("create = %t, %v", created, err)
	}
	if updated, err := jobs.RebindScheduledJobSession(t.Context(), "scheduled-old", "tg-9001-0", "sch-private", "session:sch-private"); err != nil || !updated {
		t.Fatalf("rebind = %t, %v", updated, err)
	}
	if old, err := jobs.ListActiveJobsBySession(t.Context(), "tg-9001-0"); err != nil || len(old) != 0 {
		t.Fatalf("recipient chat jobs = %+v, %v", old, err)
	}
	if newJobs, err := jobs.ListActiveJobsBySession(t.Context(), "sch-private"); err != nil ||
		len(newJobs) != 1 || newJobs[0].AssignedActor != "session:sch-private" {
		t.Fatalf("private jobs = %+v, %v", newJobs, err)
	}
}
