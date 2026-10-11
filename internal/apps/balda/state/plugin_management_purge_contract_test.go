//go:build integration && (sqlite || postgres)

package state

import (
	"errors"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/plugincmd"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

func checkPluginManagementPurge(t *testing.T, open contractOpener) {
	p := newContractProvider(t, open)
	defer closeContractProvider(t, p)
	now := time.Now().UTC()
	for _, id := range []string{"retained", "unretired"} {
		r := PluginRevisionRecord{PluginID: "demo", RevisionID: id, RelativeRoot: "plugin-revisions/demo/" + id, CapabilityJSON: "{}", CreatedAt: now}
		if id == "retained" {
			r.RetiredAt = now
		}
		if err := p.Plugins().PutPluginRevision(t.Context(), r); err != nil {
			t.Fatal(err)
		}
	}
	// An active revision and a pending transition remain protected even when
	// retirement metadata exists (for example during incomplete recovery).
	active := PluginRevisionRecord{PluginID: "busy", RevisionID: "current", RelativeRoot: "plugin-revisions/busy/current", CapabilityJSON: "{}", CreatedAt: now}
	prior := PluginRevisionRecord{PluginID: "busy", RevisionID: "prior", RelativeRoot: "plugin-revisions/busy/prior", CapabilityJSON: "{}", CreatedAt: now, RetiredAt: now}
	for _, r := range []PluginRevisionRecord{active, prior} {
		if err := p.Plugins().PutPluginRevision(t.Context(), r); err != nil {
			t.Fatal(err)
		}
	}
	install := PluginInstallRecord{PluginID: "busy", OriginMarketplace: "market", OriginSource: "source", OriginPath: "busy", ActiveRevisionID: "current", Enabled: true, CapabilityJSON: "{}", DataRelativePath: "plugin-data/busy", UpdatedAt: now}
	install.ActiveRevisionID = "prior"
	initialIntent := PluginActivationIntent{IntentID: "busy-initial", PluginID: "busy", ToRevisionID: "prior", Operation: "install", State: PluginActivationIntentPending, CreatedAt: now, UpdatedAt: now}
	if err := p.Plugins().ActivatePlugin(t.Context(), initialIntent, install); err != nil {
		t.Fatal(err)
	}
	if err := p.Plugins().CompletePluginActivation(t.Context(), initialIntent.IntentID, now); err != nil {
		t.Fatal(err)
	}
	install.ActiveRevisionID = "current"
	intent := PluginActivationIntent{IntentID: "busy-intent", PluginID: "busy", FromRevisionID: "prior", ToRevisionID: "current", Operation: "upgrade", State: PluginActivationIntentPending, CreatedAt: now, UpdatedAt: now}
	if err := p.Plugins().ActivatePlugin(t.Context(), intent, install); err != nil {
		t.Fatal(err)
	}
	if err := p.Plugins().RetirePluginRevision(t.Context(), "busy", "current", now); err != nil {
		t.Fatal(err)
	}
	s := p.PluginManagement()
	busy, err := s.ObservePlugin(t.Context(), "busy")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"current", "prior"} {
		if err := s.PurgePluginRevisionTrusted(t.Context(), "busy", id, busy.Token(), "purge-"+id, now); !errors.Is(err, plugincmd.ErrConflict) {
			t.Fatalf("referenced %s purge=%v", id, err)
		}
	}
	if err := p.Plugins().CompletePluginActivation(t.Context(), "busy-intent", now); err != nil {
		t.Fatal(err)
	}
	busy, err = s.ObservePlugin(t.Context(), "busy")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PurgePluginRevisionTrusted(t.Context(), "busy", "current", busy.Token(), "purge-active", now); !errors.Is(err, plugincmd.ErrConflict) {
		t.Fatalf("active purge=%v", err)
	}
	before, err := s.ObservePlugin(t.Context(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PurgePluginRevisionTrusted(t.Context(), "demo", "unretired", before.Token(), "purge-unretired", now); !errors.Is(err, plugincmd.ErrConflict) {
		t.Fatalf("unretired purge=%v", err)
	}
	if err := s.PurgePluginRevisionTrusted(t.Context(), "demo", "retained", before.Token(), "purge-retained", now); err != nil {
		t.Fatal(err)
	}
	after, err := s.ObservePlugin(t.Context(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	if after.Generation != before.Generation+1 || len(after.Revisions) != 1 || after.Revisions[0].RevisionID != "unretired" {
		t.Fatalf("purge observation=%+v", after)
	}
	if err := s.PurgePluginRevisionTrusted(t.Context(), "demo", "unretired", before.Token(), "purge-stale", now); !errors.Is(err, plugincmd.ErrConflict) {
		t.Fatalf("stale purge=%v", err)
	}
	// Recreate a retired revision, then duplicate the accepted audit ID: deletion
	// and generation must roll back together.
	r := PluginRevisionRecord{PluginID: "demo", RevisionID: "second", RelativeRoot: "plugin-revisions/demo/second", CapabilityJSON: "{}", CreatedAt: now, RetiredAt: now}
	if err := p.Plugins().PutPluginRevision(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	before, err = s.ObservePlugin(t.Context(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PurgePluginRevisionTrusted(t.Context(), "demo", "second", before.Token(), "purge-retained", now); !errors.Is(err, plugincmd.ErrUnavailable) {
		t.Fatalf("audit failure purge=%v", err)
	}
	after, err = s.ObservePlugin(t.Context(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	if after.Token() != before.Token() {
		t.Fatal("audit failure changed purge observation")
	}
	page, err := p.Users().ListAuditEvents(t.Context(), usercmd.PageRequest{Limit: usercmd.MaxPageSize})
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, event := range page.Events {
		if event.Action == usercmd.AuditActionPluginRevisionPurged {
			count++
			if event.TargetID != "demo" || event.Source != "chat" {
				t.Fatalf("purge audit=%+v", event)
			}
		}
	}
	if count != 1 {
		t.Fatalf("purge audit count=%d", count)
	}
}
