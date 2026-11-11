//go:build integration && (sqlite || postgres)

package state

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/plugincmd"
)

// This exercises the actual acceptance transaction after a database/record wait,
// rather than injecting a future request timestamp into its authority.
func checkPluginManagementAuthorityWhileWaiting(t *testing.T, open contractOpener) {
	p := newContractProvider(t, open)
	defer closeContractProvider(t, p)
	a := pluginContractAuthority(t, p)
	store := p.PluginManagement().(*sqlPluginManagementStore)
	before, err := store.ObserveMarketplace(t.Context(), "market")
	if err != nil {
		t.Fatal(err)
	}
	m := MarketplaceCommit{Name: "market", ExpectedState: before.Token(), DefinitionJSON: json.RawMessage(`{"Name":"market","Source":"local"}`), Operation: "add", AuditID: "wait-seed", At: a.At}
	if err := store.CommitMarketplaceTrusted(t.Context(), m); err != nil {
		t.Fatal(err)
	}
	before, err = store.ObserveMarketplace(t.Context(), "market")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().UTC().Add(500 * time.Millisecond)
	_, err = store.users.db.ExecContext(t.Context(), store.users.bind(`UPDATE balda_backoffice_sessions SET access_expires_at=? WHERE session_id=?`), formatUserTime(deadline), a.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	a.At = time.Now().UTC()
	if err := store.CheckAuthority(t.Context(), a); err != nil {
		t.Fatalf("authority before wait: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	blocker, err := store.users.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Rollback() }()
	var raw string
	if err := blocker.QueryRowContext(ctx, store.users.bind(`SELECT value_json FROM balda_app_kv WHERE namespace=? AND key=?`)+store.users.forUpdate, NamespaceApp, marketplaceGenerationPrefix+"market").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var pid int
	if store.users.forUpdate != "" {
		if err := blocker.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
			t.Fatal(err)
		}
	}
	waits := store.users.db.Stats().WaitCount
	m.Authority = &a
	m.At = a.At
	m.ExpectedState = before.Token()
	m.Operation = "refresh"
	m.MetadataJSON = json.RawMessage(`{"status":"succeeded"}`)
	m.AuditID = "wait-expired"
	done := make(chan error, 1)
	go func() { done <- store.CommitMarketplace(ctx, m) }()
	observeMCPAuthorityWait(t, ctx, store.users.db, waits, pid, done)
	timer := time.NewTimer(time.Until(deadline.Add(20 * time.Millisecond)))
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := blocker.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, plugincmd.ErrForbidden) {
			t.Fatalf("acceptance after expiry: %v", err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	after, err := store.ObserveMarketplace(t.Context(), "market")
	if err != nil {
		t.Fatal(err)
	}
	if after.Token() != before.Token() {
		t.Fatal("expired queued authority changed accepted discovery/generation")
	}
}
