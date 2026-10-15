package state

import "testing"

func TestWebhookTurnClaimMigrationFailsClosedForActiveJobs(t *testing.T) {
	db, migrations := newScheduleRunCloseMigrationDB(t)
	if _, err := migrations.UpTo(t.Context(), 56); err != nil {
		t.Fatal(err)
	}
	store := &sqliteJobStore{db: db}
	for _, jobID := range []string{"webhook-created-old", "webhook-running-old"} {
		status := JobStatusCreated
		if jobID == "webhook-running-old" {
			status = JobStatusRunning
		}
		if created, err := store.CreateJob(t.Context(), JobRecord{
			ID: jobID, SessionID: "wh-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			Objective: "input", Status: status, PrivateRunKind: PrivateRunKindWebhook,
		}); err != nil || !created {
			t.Fatalf("create pre-upgrade job %q = %t, %v", jobID, created, err)
		}
	}
	if _, err := migrations.Up(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, jobID := range []string{"webhook-created-old", "webhook-running-old"} {
		claimed, err := store.ClaimWebhookTurn(t.Context(), jobID)
		if err != nil || claimed {
			t.Fatalf("pre-upgrade job %q claim = %t, %v", jobID, claimed, err)
		}
	}
	if created, err := store.CreateJob(t.Context(), JobRecord{
		ID: "webhook-new", SessionID: "wh-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		Objective: "input", Status: JobStatusCreated, PrivateRunKind: PrivateRunKindWebhook,
	}); err != nil || !created {
		t.Fatalf("create new webhook job = %t, %v", created, err)
	}
	claimed, err := store.ClaimWebhookTurn(t.Context(), "webhook-new")
	if err != nil || !claimed {
		t.Fatalf("new job claim = %t, %v", claimed, err)
	}
}
