//go:build integration && (sqlite || postgres)

package state

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/plugincmd"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

func pluginContractAuthority(t *testing.T, p Provider) plugincmd.Authority {
	t.Helper()
	b := contractMCPMutation(t, p).Authority
	return plugincmd.Authority{UserID: b.UserID, UserVersion: b.UserVersion, CredentialVersion: b.CredentialVersion,
		MFAVersion: b.MFAVersion, SessionID: b.SessionID, SessionVersion: b.SessionVersion, At: b.At}
}

func checkPluginManagement(t *testing.T, open contractOpener) {
	p := newContractProvider(t, open)
	defer closeContractProvider(t, p)
	a := pluginContractAuthority(t, p)
	s := p.PluginManagement()
	if err := s.CheckAuthority(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	initial, err := s.ObservePlugin(t.Context(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	now := a.At
	r := PluginRevisionRecord{PluginID: "demo", RevisionID: "r1", Version: "1", RelativeRoot: "plugin-revisions/demo/r1", CapabilityJSON: "{}", CreatedAt: now}
	i := PluginInstallRecord{PluginID: "demo", OriginMarketplace: "market", OriginSource: "/host/private-source-canary", OriginPath: "demo", ActiveRevisionID: "r1", Enabled: true, Version: "1", CapabilityJSON: "{}", DataRelativePath: "plugin-data/demo", UpdatedAt: now}
	intent := PluginActivationIntent{IntentID: "install-demo", PluginID: "demo", ToRevisionID: "r1", Operation: "install", State: PluginActivationIntentPending, CreatedAt: now, UpdatedAt: now}
	m := PluginCommit{Authority: &a, PluginID: "demo", ExpectedState: initial.Token(), Install: &i, Revision: &r, Intent: intent, AuditID: "plugin-install"}
	if err := s.CommitPlugin(t.Context(), m); err != nil {
		t.Fatal(err)
	}
	observed, err := s.ObservePlugin(t.Context(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	if observed.Install == nil || !observed.Install.Enabled || len(observed.Revisions) != 1 || len(observed.Pending) != 1 || observed.Generation == 0 {
		t.Fatalf("incomplete accepted plugin: %+v", observed)
	}
	if err := s.CommitPlugin(t.Context(), m); !errors.Is(err, plugincmd.ErrConflict) {
		t.Fatalf("repeated install=%v", err)
	}
	if err := p.Plugins().CompletePluginActivation(t.Context(), intent.IntentID, now); err != nil {
		t.Fatal(err)
	}
	observed, err = s.ObservePlugin(t.Context(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	// Same revision with opposite enabled state is a material conflict.
	i.Enabled = false
	intent.IntentID, intent.Operation, intent.FromRevisionID = "disable-demo", "disable", "r1"
	m.ExpectedState, m.Revision, m.Intent, m.AuditID = observed.Token(), nil, intent, "plugin-disable"
	if err := s.CommitPlugin(t.Context(), m); err != nil {
		t.Fatal(err)
	}
	if err := s.CommitPlugin(t.Context(), m); !errors.Is(err, plugincmd.ErrConflict) {
		t.Fatalf("stale enabled state=%v", err)
	}
	before, err := s.ObservePlugin(t.Context(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	// A duplicate audit ID rolls back new revision, install, intent and generation.
	r.RevisionID, r.RelativeRoot = "r2", "plugin-revisions/demo/r2"
	i.ActiveRevisionID = "r2"
	intent.IntentID, intent.Operation, intent.ToRevisionID = "upgrade-demo", "upgrade", "r2"
	m.ExpectedState, m.Revision, m.Intent = before.Token(), &r, intent
	if err := s.CommitPlugin(t.Context(), m); !errors.Is(err, plugincmd.ErrUnavailable) {
		t.Fatalf("audit failure=%v", err)
	}
	after, err := s.ObservePlugin(t.Context(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	if after.Token() != before.Token() {
		t.Fatal("audit failure changed logical state")
	}
	page, err := p.Users().ListAuditEvents(t.Context(), usercmd.PageRequest{Limit: usercmd.MaxPageSize})
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range page.Events {
		raw, _ := json.Marshal(event)
		if strings.Contains(string(raw), "private-source-canary") {
			t.Fatal("audit disclosed source")
		}
	}
}

func checkPluginMarketplaceManagement(t *testing.T, open contractOpener) {
	p := newContractProvider(t, open)
	defer closeContractProvider(t, p)
	a := pluginContractAuthority(t, p)
	s := p.PluginManagement()
	before, err := s.ObserveMarketplace(t.Context(), "market")
	if err != nil {
		t.Fatal(err)
	}
	definition := json.RawMessage(`{"Name":"market","Source":"/private/source-canary"}`)
	m := MarketplaceCommit{Authority: &a, Name: "market", ExpectedState: before.Token(), DefinitionJSON: definition, Operation: "add", AuditID: "market-add", At: a.At}
	if err := s.CommitMarketplace(t.Context(), m); err != nil {
		t.Fatal(err)
	}
	if err := s.CommitMarketplace(t.Context(), m); !errors.Is(err, plugincmd.ErrConflict) {
		t.Fatalf("duplicate add=%v", err)
	}
	current, err := s.ObserveMarketplace(t.Context(), "market")
	if err != nil {
		t.Fatal(err)
	}
	m.ExpectedState, m.Operation, m.AuditID = current.Token(), "refresh", "market-refresh"
	m.MetadataJSON = json.RawMessage(`{"LastAttemptStatus":"succeeded","LastSuccessful":{"revision":"r1"}}`)
	if err := s.CommitMarketplace(t.Context(), m); err != nil {
		t.Fatal(err)
	}
	current, err = s.ObserveMarketplace(t.Context(), "market")
	if err != nil {
		t.Fatal(err)
	}
	m.ExpectedState, m.Operation, m.AuditID = current.Token(), "refresh-failed", "market-failed"
	m.MetadataJSON = json.RawMessage(`{"LastAttemptStatus":"failed","LastSuccessful":{"revision":"r1"}}`)
	if err := s.CommitMarketplace(t.Context(), m); err != nil {
		t.Fatal(err)
	}
	failed, err := s.ObserveMarketplace(t.Context(), "market")
	if err != nil {
		t.Fatal(err)
	}
	if failed.Generation <= current.Generation || !strings.Contains(string(failed.MetadataJSON), `"revision":"r1"`) {
		t.Fatal("failure did not preserve success and invalidate generation")
	}
	// Recording a second failed attempt with duplicate audit identity is atomic.
	m.ExpectedState = failed.Token()
	if err := s.CommitMarketplace(t.Context(), m); !errors.Is(err, plugincmd.ErrUnavailable) {
		t.Fatalf("failed-refresh audit failure=%v", err)
	}
	rolledBack, err := s.ObserveMarketplace(t.Context(), "market")
	if err != nil || rolledBack.Token() != failed.Token() {
		t.Fatalf("failed-refresh audit rollback changed observation: %v", err)
	}
	events, err := p.Users().ListAuditEvents(t.Context(), usercmd.PageRequest{Limit: usercmd.MaxPageSize})
	if err != nil {
		t.Fatal(err)
	}
	var foundFailure bool
	for _, event := range events.Events {
		if event.ID == "market-failed" {
			foundFailure = string(event.Action) == "plugin.marketplace.refresh_failed"
		}
	}
	if !foundFailure {
		t.Fatal("accepted discovery failure not identified in audit")
	}
	// Revocation prevents even recording a failed refresh attempt.
	_, err = contractDatabase(p).db.ExecContext(t.Context(), `UPDATE balda_backoffice_sessions SET revoked_at='2000-01-01T00:00:00Z'`)
	if err != nil {
		t.Fatal(err)
	}
	m.ExpectedState, m.AuditID = failed.Token(), "revoked-failure"
	if err := s.CommitMarketplace(t.Context(), m); !errors.Is(err, plugincmd.ErrForbidden) {
		t.Fatalf("revoked refresh=%v", err)
	}
	unchanged, err := s.ObserveMarketplace(t.Context(), "market")
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Token() != failed.Token() {
		t.Fatal("revoked refresh changed state")
	}
	// Trusted existing owner operations preserve definition replacement and ABA fencing.
	m.Authority, m.Operation, m.AuditID = nil, "remove", "market-remove"
	m.DefinitionJSON, m.MetadataJSON = nil, nil
	if err := s.CommitMarketplaceTrusted(t.Context(), m); err != nil {
		t.Fatal(err)
	}
	deleted, err := s.ObserveMarketplace(t.Context(), "market")
	if err != nil {
		t.Fatal(err)
	}
	if deleted.DefinitionJSON != nil || deleted.Generation <= failed.Generation || deleted.Token() == before.Token() {
		t.Fatal("deletion lost tombstone")
	}
	m.ExpectedState, m.Operation, m.AuditID, m.DefinitionJSON = deleted.Token(), "add", "market-readd", definition
	if err := s.CommitMarketplaceTrusted(t.Context(), m); err != nil {
		t.Fatal(err)
	}
	readded, err := s.ObserveMarketplace(t.Context(), "market")
	if err != nil {
		t.Fatal(err)
	}
	m.ExpectedState, m.Operation, m.AuditID = readded.Token(), "upsert", "market-replace"
	m.DefinitionJSON = json.RawMessage(`{"Name":"market","Source":"/other/private-source"}`)
	if err := s.CommitMarketplaceTrusted(t.Context(), m); err != nil {
		t.Fatal(err)
	}
	replaced, err := s.ObserveMarketplace(t.Context(), "market")
	if err != nil || replaced.Generation <= readded.Generation || string(replaced.DefinitionJSON) != string(m.DefinitionJSON) {
		t.Fatalf("trusted replacement not retained: %v", err)
	}
	if err := s.CommitMarketplaceTrusted(t.Context(), MarketplaceCommit{Name: "market", ExpectedState: failed.Token(), Operation: "remove", AuditID: "stale-delete", At: a.At}); !errors.Is(err, plugincmd.ErrConflict) {
		t.Fatalf("ABA stale delete=%v", err)
	}
}

func checkPluginManagementStateFences(t *testing.T, factory func(*testing.T) contractOpener) {
	for _, kind := range []string{"origin", "revision", "pending", "revoked", "ABA", "adopt"} {
		t.Run(kind, func(t *testing.T) {
			p := newContractProvider(t, factory(t))
			defer closeContractProvider(t, p)
			a := pluginContractAuthority(t, p)
			s := p.PluginManagement()
			now := a.At
			before, err := s.ObservePlugin(t.Context(), "demo")
			if err != nil {
				t.Fatal(err)
			}
			r := PluginRevisionRecord{PluginID: "demo", RevisionID: "r1", RelativeRoot: "plugin-revisions/demo/r1", CapabilityJSON: "{}", CreatedAt: now}
			i := PluginInstallRecord{PluginID: "demo", OriginMarketplace: "origin-unknown", OriginSource: "origin-unknown", OriginPath: "origin-unknown", ActiveRevisionID: "r1", Enabled: true, CapabilityJSON: "{}", DataRelativePath: "plugin-data/demo", UpdatedAt: now}
			intent := PluginActivationIntent{IntentID: "initial", PluginID: "demo", ToRevisionID: "r1", Operation: "install", State: PluginActivationIntentPending, CreatedAt: now, UpdatedAt: now}
			m := PluginCommit{Authority: &a, PluginID: "demo", ExpectedState: before.Token(), Revision: &r, Install: &i, Intent: intent, AuditID: "initial-audit"}
			if err := s.CommitPlugin(t.Context(), m); err != nil {
				t.Fatal(err)
			}
			loaded, err := s.ObservePlugin(t.Context(), "demo")
			if err != nil {
				t.Fatal(err)
			}
			i.Enabled = false
			intent.IntentID, intent.FromRevisionID, intent.Operation = "disable", "r1", "disable"
			m.ExpectedState, m.Revision, m.Intent, m.AuditID = loaded.Token(), nil, intent, "disable-audit"
			want := plugincmd.ErrConflict
			switch kind {
			case "origin":
				_, err = contractDatabase(p).db.ExecContext(t.Context(), `UPDATE balda_plugin_installs SET origin_source='changed-origin'`)
			case "revision":
				r.RevisionID, r.RelativeRoot = "retained", "plugin-revisions/demo/retained"
				err = p.Plugins().PutPluginRevision(t.Context(), r)
			case "pending":
				err = p.Plugins().CompletePluginActivation(t.Context(), "initial", now)
			case "revoked":
				_, err = contractDatabase(p).db.ExecContext(t.Context(), `UPDATE balda_backoffice_sessions SET revoked_at='2000-01-01T00:00:00Z'`)
				want = plugincmd.ErrForbidden
			case "ABA":
				remove := m
				remove.Install = nil
				remove.Authority = nil
				remove.Intent.Operation = "remove"
				remove.Intent.IntentID = "remove"
				remove.AuditID = "remove-audit"
				if err := s.CommitPluginTrusted(t.Context(), remove); err != nil {
					t.Fatal(err)
				}
				removed, err := s.ObservePlugin(t.Context(), "demo")
				if err != nil {
					t.Fatal(err)
				}
				i.Enabled = true
				readd := m
				readd.Authority = nil
				readd.ExpectedState = removed.Token()
				readd.Intent.Operation = "install"
				readd.Intent.FromRevisionID = ""
				readd.Intent.IntentID = "readd"
				readd.AuditID = "readd-audit"
				if err := s.CommitPluginTrusted(t.Context(), readd); err != nil {
					t.Fatal(err)
				}
				i.Enabled = false
			case "adopt":
				i.Enabled = true
				i.OriginMarketplace, i.OriginSource, i.OriginPath = "market", "/private/source", "demo"
				adopt := m
				adopt.Authority = nil
				adopt.Intent.Operation = "adopt-origin"
				adopt.Intent.IntentID = "adopt"
				adopt.AuditID = "adopt-audit"
				if err := s.CommitPluginTrusted(t.Context(), adopt); err != nil {
					t.Fatal(err)
				}
				i.Enabled = false
			}
			if err != nil {
				t.Fatal(err)
			}
			state, err := s.ObservePlugin(t.Context(), "demo")
			if err != nil {
				t.Fatal(err)
			}
			if err := s.CommitPlugin(t.Context(), m); !errors.Is(err, want) {
				t.Fatalf("%s stale acceptance=%v want%v", kind, err, want)
			}
			after, err := s.ObservePlugin(t.Context(), "demo")
			if err != nil || after.Token() != state.Token() {
				t.Fatalf("rejected %s changed state: %v", kind, err)
			}
		})
	}
}
