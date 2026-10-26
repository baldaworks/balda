//go:build integration && (sqlite || postgres)

package state

import (
	"database/sql"
	"strings"
	"testing"
	"time"
)

func checkRuntimeOperationStateMigration(t *testing.T, db *sql.DB, bind func(string) string) {
	t.Helper()

	var binding, model, outcome string
	var generation int64
	if err := db.QueryRowContext(t.Context(), bind(`
		SELECT provider_binding_json, runtime_generation, effective_model, runtime_outcome
		FROM balda_session_metadata WHERE session_id = ?`), "legacy-runtime").Scan(
		&binding, &generation, &model, &outcome,
	); err != nil {
		t.Fatal(err)
	}
	legacyBinding := `{"RootProviderID":"legacy-exact","Entries":[{"ProviderID":"legacy-exact","Source":"managed","RevisionID":"revision-legacy"}],"Reproducible":true,"Warnings":null}`
	if binding != legacyBinding || generation != 0 || model != "" || outcome != "" {
		t.Fatalf("legacy runtime metadata = %q/%d/%q/%q", binding, generation, model, outcome)
	}
	now := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	operation := RuntimeOperationRecord{
		OperationID: "operation-1", SessionID: "legacy-runtime", InputHash: strings.Repeat("a", 64),
		Kind: "model", State: RuntimeOperationPrepared, ExpectedArchiveID: "archive-legacy",
		TargetSnapshotID:          "snapshot-next",
		TargetProviderBindingJSON: ` {"Warnings":null,"Entries":[{"RevisionID":"revision-next","Source":"managed","ProviderID":"next"}],"Reproducible":true,"RootProviderID":"next"}`,
		TargetModel:               "model-next", CreatedAt: now, UpdatedAt: now,
	}
	if err := prepareRuntimeOperationRecord(&operation); err != nil {
		t.Fatalf("prepare canonical operation: %v", err)
	}
	for name, raw := range map[string]string{
		"protected": `{"RootProviderID":"next","Entries":[{"ProviderID":"next","Source":"managed","RevisionID":"revision-next"}],"Reproducible":true,"Warnings":null,"api_key":"must-not-land"}`,
		"unknown":   `{"RootProviderID":"next","Entries":[{"ProviderID":"next","Source":"managed","RevisionID":"revision-next","unknown":true}],"Reproducible":true,"Warnings":null}`,
	} {
		t.Run(name+" target binding", func(t *testing.T) {
			invalid := operation
			invalid.TargetProviderBindingJSON = raw
			if err := prepareRuntimeOperationRecord(&invalid); err == nil {
				t.Fatal("prepareRuntimeOperationRecord() accepted non-public binding")
			}
		})
	}

	insertOperation := `INSERT INTO balda_runtime_operations(
		operation_id,session_id,input_hash,kind,state,expected_archive_id,expected_generation,
		target_archive_id,target_snapshot_id,target_provider_binding_json,target_model,created_at,updated_at)
		VALUES(?,?,?,?,'prepared',?,?,?,?,?,?,?,?)`
	if _, err := db.ExecContext(t.Context(), bind(insertOperation),
		operation.OperationID, operation.SessionID, operation.InputHash, operation.Kind, operation.ExpectedArchiveID, 0,
		"", operation.TargetSnapshotID, operation.TargetProviderBindingJSON, operation.TargetModel,
		"2026-10-26T00:00:00Z", "2026-10-26T00:00:00Z",
	); err != nil {
		t.Fatalf("insert valid operation: %v", err)
	}
	if _, err := db.ExecContext(t.Context(), bind(insertOperation),
		"operation-2", "legacy-runtime", strings.Repeat("b", 64), "provider", "archive-legacy", 0,
		"", "", "", "", "2026-10-26T00:00:00Z", "2026-10-26T00:00:00Z",
	); err == nil {
		t.Fatal("second unresolved operation was accepted")
	}
	if _, err := db.ExecContext(t.Context(), bind(`UPDATE balda_runtime_operations SET state = 'unknown' WHERE operation_id = ?`), "operation-1"); err == nil {
		t.Fatal("unknown operation state was accepted")
	}
	if _, err := db.ExecContext(t.Context(), bind(`UPDATE balda_runtime_operations SET input_hash = ? WHERE operation_id = ?`), strings.Repeat("G", 64), "operation-1"); err == nil {
		t.Fatal("non-hex input hash was accepted")
	}
	if _, err := db.ExecContext(t.Context(), bind(`UPDATE balda_runtime_operations SET target_provider_binding_json = ? WHERE operation_id = ?`), `[]`, "operation-1"); err == nil {
		t.Fatal("non-object provider binding was accepted")
	}
	if _, err := db.ExecContext(t.Context(), bind(`UPDATE balda_runtime_operations SET target_provider_binding_json = ? WHERE operation_id = ?`), `{"value":"`+strings.Repeat("x", maxRuntimeProviderBindingJSONBytes)+`"}`, "operation-1"); err == nil {
		t.Fatal("oversized provider binding was accepted")
	}
	if _, err := db.ExecContext(t.Context(), bind(`UPDATE balda_runtime_operations SET target_model = ? WHERE operation_id = ?`), strings.Repeat("m", maxEffectiveModelBytes+1), "operation-1"); err == nil {
		t.Fatal("oversized target model was accepted")
	}
	if _, err := db.ExecContext(t.Context(), bind(`UPDATE balda_runtime_operations SET outcome = ? WHERE operation_id = ?`), strings.Repeat("o", maxRuntimeOutcomeBytes+1), "operation-1"); err == nil {
		t.Fatal("oversized outcome was accepted")
	}
	if _, err := db.ExecContext(t.Context(), bind(`UPDATE balda_runtime_operations SET state = 'terminal' WHERE operation_id = ?`), "operation-1"); err == nil {
		t.Fatal("terminal operation without terminal_at was accepted")
	}
	if _, err := db.ExecContext(t.Context(), bind(`UPDATE balda_runtime_operations SET state = 'applying' WHERE operation_id = ?`), "operation-1"); err != nil {
		t.Fatalf("advance operation to applying: %v", err)
	}

	var targetArchive, operationOutcome, reasonCode string
	var terminalAt sql.NullString
	if err := db.QueryRowContext(t.Context(), bind(`
		SELECT target_archive_id,outcome,reason_code,terminal_at
		FROM balda_runtime_operations WHERE operation_id = ?`), "operation-1").Scan(
		&targetArchive, &operationOutcome, &reasonCode, &terminalAt,
	); err != nil {
		t.Fatal(err)
	}
	if targetArchive != "" || operationOutcome != "" || reasonCode != "" || terminalAt.Valid {
		t.Fatalf("operation defaults = %q/%q/%q/%v", targetArchive, operationOutcome, reasonCode, terminalAt)
	}

	if _, err := db.ExecContext(t.Context(), bind(`DELETE FROM balda_session_metadata WHERE session_id = ?`), "legacy-runtime"); err != nil {
		t.Fatalf("delete session with retained operation: %v", err)
	}
	var retained int
	if err := db.QueryRowContext(t.Context(), bind(`SELECT COUNT(*) FROM balda_runtime_operations WHERE operation_id = ?`), "operation-1").Scan(&retained); err != nil {
		t.Fatal(err)
	}
	if retained != 1 {
		t.Fatalf("retained operation count = %d, want 1", retained)
	}
}

func insertLegacyRuntimeSession(t *testing.T, db *sql.DB, bind func(string) string) {
	t.Helper()
	_, err := db.ExecContext(t.Context(), bind(`INSERT INTO balda_session_metadata(
		session_id,user_id,channel_type,address_key,address_json,agent_name,workspace_dir,branch_name,
		runtime_snapshot_id,provider_binding_json,archive_id,status,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`),
		"legacy-runtime", "user", "telegram", "70:0", `{}`, "agent", "/workspace", "branch",
		"snapshot-exact", `{"RootProviderID":"legacy-exact","Entries":[{"ProviderID":"legacy-exact","Source":"managed","RevisionID":"revision-legacy"}],"Reproducible":true,"Warnings":null}`, "archive-legacy", "active", "2026-10-26T00:00:00Z",
	)
	if err != nil {
		t.Fatal(err)
	}
}
