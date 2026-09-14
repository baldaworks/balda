package plugin

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/actors"
	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/plugincmd"
	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/rs/zerolog"
)

type fakeDispatcher struct {
	sentEnvelopes []actorlayer.Envelope
}

func (d *fakeDispatcher) Dispatch(_ context.Context, env actorlayer.Envelope) (*actortransport.DispatchReceipt, error) {
	d.sentEnvelopes = append(d.sentEnvelopes, env)
	return &actortransport.DispatchReceipt{}, nil
}

func (d *fakeDispatcher) lastText(t *testing.T) string {
	t.Helper()
	if len(d.sentEnvelopes) == 0 {
		t.Fatal("no envelopes dispatched")
	}
	last := d.sentEnvelopes[len(d.sentEnvelopes)-1]
	var payload actors.DeliveryPayload
	if err := actorlayer.UnmarshalPayload(last.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	return payload.Text
}

type fakeOwnerStore struct {
	ownerID int64
}

type recordingObserver struct {
	events []OperationEvent
}

func (o *recordingObserver) ObservePluginOperation(_ context.Context, event OperationEvent) {
	o.events = append(o.events, event)
}

func (f *fakeOwnerStore) IsOwner(userID int64) bool {
	return f.ownerID != 0 && f.ownerID == userID
}

func (f *fakeOwnerStore) IsOwnerSubject(_ string) bool {
	return false
}

type fakePluginService struct {
	installed          []plugincmd.PluginSummary
	installedErr       error
	available          []plugincmd.AvailablePlugin
	availableErr       error
	getAvailable       plugincmd.AvailablePlugin
	getAvailableFound  bool
	getAvailableErr    error
	getInstalled       plugincmd.PluginSummary
	getInstalledFound  bool
	getInstalledErr    error
	installErr         error
	installedRef       string
	upgradeErr         error
	upgradedRef        string
	enableErr          error
	enabledPlugin      string
	disableErr         error
	disabledPlugin     string
	rollbackErr        error
	rollbackPlugin     string
	rollbackRevision   string
	removeInstalledErr error
	removedInstalled   string
	purgeErr           error
	purgedPlugin       string
	purgedRevision     string
	purgedData         bool
	status             plugincmd.PluginStatus
	statusFound        bool
	statusErr          error

	marketplaceStatuses   []plugincmd.MarketplaceStatus
	marketplaceListErr    error
	marketplaceStatus     plugincmd.MarketplaceStatus
	marketplaceFound      bool
	marketplaceGetErr     error
	addMarketplaceErr     error
	addedMarketplace      plugincmd.MarketplaceSource
	upgradeResults        []plugincmd.MarketplaceUpgradeResult
	marketplaceUpgradeErr error
	upgradedName          string
	removeMarketErr       error
	removedMarketplace    string
}

func (f *fakePluginService) ListInstalled(_ context.Context) ([]plugincmd.PluginSummary, error) {
	return f.installed, f.installedErr
}

func (f *fakePluginService) ListAvailable(_ context.Context) ([]plugincmd.AvailablePlugin, error) {
	return f.available, f.availableErr
}

func (f *fakePluginService) GetInstalled(_ context.Context, _ string) (plugincmd.PluginSummary, bool, error) {
	return f.getInstalled, f.getInstalledFound, f.getInstalledErr
}

func (f *fakePluginService) GetAvailable(_ context.Context, _ string) (plugincmd.AvailablePlugin, bool, error) {
	return f.getAvailable, f.getAvailableFound, f.getAvailableErr
}

func (f *fakePluginService) Install(_ context.Context, ref string) error {
	f.installedRef = ref
	return f.installErr
}

func (f *fakePluginService) Upgrade(_ context.Context, ref string) error {
	f.upgradedRef = ref
	return f.upgradeErr
}

func (f *fakePluginService) Enable(_ context.Context, name string) error {
	f.enabledPlugin = name
	return f.enableErr
}

func (f *fakePluginService) Disable(_ context.Context, name string) error {
	f.disabledPlugin = name
	return f.disableErr
}

func (f *fakePluginService) Rollback(_ context.Context, name, revision string) error {
	f.rollbackPlugin, f.rollbackRevision = name, revision
	return f.rollbackErr
}

func (f *fakePluginService) RemoveInstalled(_ context.Context, name string) error {
	f.removedInstalled = name
	return f.removeInstalledErr
}

func (f *fakePluginService) Purge(_ context.Context, name, revision string, purgeData bool) error {
	f.purgedPlugin, f.purgedRevision, f.purgedData = name, revision, purgeData
	return f.purgeErr
}

func (f *fakePluginService) Status(_ context.Context, _ string) (plugincmd.PluginStatus, bool, error) {
	return f.status, f.statusFound, f.statusErr
}

func (f *fakePluginService) ListMarketplaceStatuses(_ context.Context) ([]plugincmd.MarketplaceStatus, error) {
	return f.marketplaceStatuses, f.marketplaceListErr
}

func (f *fakePluginService) GetMarketplaceStatus(_ context.Context, _ string) (plugincmd.MarketplaceStatus, bool, error) {
	return f.marketplaceStatus, f.marketplaceFound, f.marketplaceGetErr
}

func (f *fakePluginService) AddMarketplace(_ context.Context, src plugincmd.MarketplaceSource) error {
	f.addedMarketplace = src
	return f.addMarketplaceErr
}

func (f *fakePluginService) UpgradeMarketplaces(_ context.Context, name string) ([]plugincmd.MarketplaceUpgradeResult, error) {
	f.upgradedName = name
	return f.upgradeResults, f.marketplaceUpgradeErr
}

func (f *fakePluginService) RemoveMarketplace(_ context.Context, name string) error {
	f.removedMarketplace = name
	return f.removeMarketErr
}

func newPayload(args string, isOwner bool) (actorlayer.Envelope, commandcmd.Payload) {
	loc, _ := deliverycmd.NewLocator("telegram", "chat-1", "{}", "sess-1")
	p := commandcmd.Payload{
		Version:   commandcmd.SchemaVersion,
		Name:      "plugin",
		Args:      args,
		Locator:   loc,
		Transport: "telegram",
		Principal: "101",
		Access:    commandcmd.Access{Owner: isOwner},
	}
	env := actorlayer.Envelope{ID: "test-env-id"}
	return env, p
}

func TestHandler_NotOwner(t *testing.T) {
	dispatcher := &fakeDispatcher{}
	handler := New(&fakeOwnerStore{ownerID: 999}, &fakePluginService{}, dispatcher, zerolog.Nop())

	env, p := newPayload("list", false)
	if err := handler.Handle(context.Background(), env, p); err != nil {
		t.Fatal(err)
	}

	got := dispatcher.lastText(t)
	if got != "Only the bot owner can use this command." {
		t.Fatalf("expected not owner text, got: %q", got)
	}
}

func TestHandler_Usage(t *testing.T) {
	dispatcher := &fakeDispatcher{}
	handler := New(nil, &fakePluginService{}, dispatcher, zerolog.Nop())

	for _, args := range []string{"", "   ", "unknown"} {
		env, p := newPayload(args, true)
		if err := handler.Handle(context.Background(), env, p); err != nil {
			t.Fatal(err)
		}
		got := dispatcher.lastText(t)
		if !strings.Contains(got, plugincmd.TransportUsageMarkdown()) {
			t.Fatalf("expected usage markdown for %q, got: %q", args, got)
		}
	}
}

func TestHandler_ServiceUnavailable(t *testing.T) {
	dispatcher := &fakeDispatcher{}
	handler := New(nil, nil, dispatcher, zerolog.Nop())

	env, p := newPayload("list", true)
	if err := handler.Handle(context.Background(), env, p); err != nil {
		t.Fatal(err)
	}
	got := dispatcher.lastText(t)
	if got != "Plugin service is unavailable." {
		t.Fatalf("expected service unavailable text, got: %q", got)
	}
}

func TestHandler_List(t *testing.T) {
	t.Run("list installed success", func(t *testing.T) {
		dispatcher := &fakeDispatcher{}
		svc := &fakePluginService{
			installed: []plugincmd.PluginSummary{
				{Name: "my-plugin", Version: "1.0.0", Description: "A plugin"},
			},
		}
		handler := New(nil, svc, dispatcher, zerolog.Nop())

		env, p := newPayload("list", true)
		if err := handler.Handle(context.Background(), env, p); err != nil {
			t.Fatal(err)
		}
		got := dispatcher.lastText(t)
		if !strings.Contains(got, "my-plugin") {
			t.Fatalf("expected installed plugin, got: %q", got)
		}
	})

	t.Run("list installed error", func(t *testing.T) {
		dispatcher := &fakeDispatcher{}
		svc := &fakePluginService{installedErr: errors.New("boom")}
		handler := New(nil, svc, dispatcher, zerolog.Nop())

		env, p := newPayload("list", true)
		if err := handler.Handle(context.Background(), env, p); err != nil {
			t.Fatal(err)
		}
		got := dispatcher.lastText(t)
		if got != "Could not list installed plugins." {
			t.Fatalf("expected error text, got: %q", got)
		}
	})

	t.Run("list available success", func(t *testing.T) {
		dispatcher := &fakeDispatcher{}
		svc := &fakePluginService{
			available: []plugincmd.AvailablePlugin{
				{Name: "avail-plugin", Version: "2.0.0", Description: "Available"},
			},
		}
		handler := New(nil, svc, dispatcher, zerolog.Nop())

		env, p := newPayload("list --available", true)
		if err := handler.Handle(context.Background(), env, p); err != nil {
			t.Fatal(err)
		}
		got := dispatcher.lastText(t)
		if !strings.Contains(got, "avail-plugin") {
			t.Fatalf("expected available plugin, got: %q", got)
		}
	})

	t.Run("list available error", func(t *testing.T) {
		dispatcher := &fakeDispatcher{}
		svc := &fakePluginService{availableErr: errors.New("boom")}
		handler := New(nil, svc, dispatcher, zerolog.Nop())

		env, p := newPayload("list available", true)
		if err := handler.Handle(context.Background(), env, p); err != nil {
			t.Fatal(err)
		}
		got := dispatcher.lastText(t)
		if got != "Could not list available plugins." {
			t.Fatalf("expected error text, got: %q", got)
		}
	})

	t.Run("list invalid args", func(t *testing.T) {
		dispatcher := &fakeDispatcher{}
		svc := &fakePluginService{}
		handler := New(nil, svc, dispatcher, zerolog.Nop())

		env, p := newPayload("list extra arguments", true)
		if err := handler.Handle(context.Background(), env, p); err != nil {
			t.Fatal(err)
		}
		got := dispatcher.lastText(t)
		if !strings.Contains(got, plugincmd.TransportUsage()) {
			t.Fatalf("expected usage text, got: %q", got)
		}
	})
}

func TestHandler_Show(t *testing.T) {
	t.Run("show installed", func(t *testing.T) {
		dispatcher := &fakeDispatcher{}
		svc := &fakePluginService{
			getInstalledFound: true,
			getInstalled:      plugincmd.PluginSummary{Name: "foo", Version: "1.0", Description: "Foo plugin"},
		}
		handler := New(nil, svc, dispatcher, zerolog.Nop())

		env, p := newPayload("show foo", true)
		if err := handler.Handle(context.Background(), env, p); err != nil {
			t.Fatal(err)
		}
		got := dispatcher.lastText(t)
		if !strings.Contains(got, "foo") {
			t.Fatalf("expected plugin show foo, got: %q", got)
		}
	})

	t.Run("show available fallback", func(t *testing.T) {
		dispatcher := &fakeDispatcher{}
		svc := &fakePluginService{
			getInstalledFound: false,
			getAvailableFound: true,
			getAvailable:      plugincmd.AvailablePlugin{Name: "bar", Version: "2.0"},
		}
		handler := New(nil, svc, dispatcher, zerolog.Nop())

		env, p := newPayload("show bar", true)
		if err := handler.Handle(context.Background(), env, p); err != nil {
			t.Fatal(err)
		}
		got := dispatcher.lastText(t)
		if !strings.Contains(got, "bar") {
			t.Fatalf("expected plugin show bar, got: %q", got)
		}
	})

	t.Run("show not found", func(t *testing.T) {
		dispatcher := &fakeDispatcher{}
		svc := &fakePluginService{
			getInstalledFound: false,
			getAvailableFound: false,
		}
		handler := New(nil, svc, dispatcher, zerolog.Nop())

		env, p := newPayload("show missing", true)
		if err := handler.Handle(context.Background(), env, p); err != nil {
			t.Fatal(err)
		}
		got := dispatcher.lastText(t)
		if !strings.Contains(got, "/plugin show missing") {
			t.Fatalf("expected not implemented message, got: %q", got)
		}
	})
}

func TestHandler_InstallAndRemove(t *testing.T) {
	t.Run("install success", func(t *testing.T) {
		dispatcher := &fakeDispatcher{}
		svc := &fakePluginService{}
		handler := New(nil, svc, dispatcher, zerolog.Nop())

		env, p := newPayload("install repo/plug", true)
		if err := handler.Handle(context.Background(), env, p); err != nil {
			t.Fatal(err)
		}
		got := dispatcher.lastText(t)
		if got != "Plugin installed." {
			t.Fatalf("expected 'Plugin installed.', got: %q", got)
		}
		if svc.installedRef != "repo/plug" {
			t.Fatalf("expected ref 'repo/plug', got: %q", svc.installedRef)
		}
	})

	t.Run("install error", func(t *testing.T) {
		dispatcher := &fakeDispatcher{}
		svc := &fakePluginService{installErr: errors.New("fail")}
		handler := New(nil, svc, dispatcher, zerolog.Nop())

		env, p := newPayload("install bad/plug", true)
		if err := handler.Handle(context.Background(), env, p); err != nil {
			t.Fatal(err)
		}
		got := dispatcher.lastText(t)
		if got != "Could not install plugin." {
			t.Fatalf("expected 'Could not install plugin.', got: %q", got)
		}
	})

	t.Run("remove success", func(t *testing.T) {
		dispatcher := &fakeDispatcher{}
		svc := &fakePluginService{}
		handler := New(nil, svc, dispatcher, zerolog.Nop())

		env, p := newPayload("remove my-plug", true)
		if err := handler.Handle(context.Background(), env, p); err != nil {
			t.Fatal(err)
		}
		got := dispatcher.lastText(t)
		if got != "Plugin removed." {
			t.Fatalf("expected 'Plugin removed.', got: %q", got)
		}
		if svc.removedInstalled != "my-plug" {
			t.Fatalf("expected removed 'my-plug', got: %q", svc.removedInstalled)
		}
	})

	t.Run("remove error", func(t *testing.T) {
		dispatcher := &fakeDispatcher{}
		svc := &fakePluginService{removeInstalledErr: errors.New("fail")}
		handler := New(nil, svc, dispatcher, zerolog.Nop())

		env, p := newPayload("remove my-plug", true)
		if err := handler.Handle(context.Background(), env, p); err != nil {
			t.Fatal(err)
		}
		got := dispatcher.lastText(t)
		if got != "Could not remove plugin." {
			t.Fatalf("expected 'Could not remove plugin.', got: %q", got)
		}
	})
}

func TestHandler_ManagedLifecycleAndStatus(t *testing.T) {
	const managedPluginName = "demo"
	tests := []struct {
		args   string
		want   string
		assert func(*testing.T, *fakePluginService)
	}{
		{args: "upgrade " + managedPluginName + "@official", want: "Plugin upgraded.", assert: func(t *testing.T, service *fakePluginService) {
			if service.upgradedRef != managedPluginName+"@official" {
				t.Fatalf("upgraded ref = %q", service.upgradedRef)
			}
		}},
		{args: "enable " + managedPluginName, want: "Plugin enabled.", assert: func(t *testing.T, service *fakePluginService) {
			if service.enabledPlugin != managedPluginName {
				t.Fatalf("enabled plugin = %q", service.enabledPlugin)
			}
		}},
		{args: "disable " + managedPluginName, want: "Plugin disabled.", assert: func(t *testing.T, service *fakePluginService) {
			if service.disabledPlugin != managedPluginName {
				t.Fatalf("disabled plugin = %q", service.disabledPlugin)
			}
		}},
		{args: "rollback " + managedPluginName + " revision-1", want: "Plugin rolled back.", assert: func(t *testing.T, service *fakePluginService) {
			if service.rollbackPlugin != managedPluginName || service.rollbackRevision != "revision-1" {
				t.Fatalf("rollback = %q/%q", service.rollbackPlugin, service.rollbackRevision)
			}
		}},
		{args: "purge " + managedPluginName + " revision-0 --data", want: "Plugin revision purged.", assert: func(t *testing.T, service *fakePluginService) {
			if service.purgedPlugin != managedPluginName || service.purgedRevision != "revision-0" || !service.purgedData {
				t.Fatalf("purge = %q/%q data=%t", service.purgedPlugin, service.purgedRevision, service.purgedData)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.args, func(t *testing.T) {
			dispatcher := &fakeDispatcher{}
			service := &fakePluginService{}
			observer := &recordingObserver{}
			handler := New(nil, service, dispatcher, zerolog.Nop(), observer)
			env, payload := newPayload(test.args, true)
			if err := handler.Handle(context.Background(), env, payload); err != nil {
				t.Fatal(err)
			}
			if got := dispatcher.lastText(t); got != test.want {
				t.Fatalf("response = %q, want %q", got, test.want)
			}
			test.assert(t, service)
			if len(observer.events) != 1 || observer.events[0].Outcome != "success" {
				t.Fatalf("audit/metric events = %+v", observer.events)
			}
		})
	}

	t.Run("status is bounded", func(t *testing.T) {
		dispatcher := &fakeDispatcher{}
		service := &fakePluginService{statusFound: true, status: plugincmd.PluginStatus{
			Name: managedPluginName, Revision: "revision-1", Enabled: true,
			Runtime: plugincmd.RuntimeStatus{SnapshotID: "snapshot-1", DiagnosticCodes: []string{"projection"}},
		}}
		handler := New(nil, service, dispatcher, zerolog.Nop())
		env, payload := newPayload("status "+managedPluginName, true)
		if err := handler.Handle(context.Background(), env, payload); err != nil {
			t.Fatal(err)
		}
		got := dispatcher.lastText(t)
		if !strings.Contains(got, "revision-1") || !strings.Contains(got, "snapshot-1") {
			t.Fatalf("status response = %q", got)
		}
	})

	t.Run("purge failure is reported and audited", func(t *testing.T) {
		dispatcher := &fakeDispatcher{}
		service := &fakePluginService{purgeErr: errors.New("revision still active")}
		observer := &recordingObserver{}
		handler := New(nil, service, dispatcher, zerolog.Nop(), observer)
		env, payload := newPayload("purge "+managedPluginName+" active-revision", true)
		if err := handler.Handle(context.Background(), env, payload); err != nil {
			t.Fatal(err)
		}
		if got := dispatcher.lastText(t); got != "Could not purge plugin revision." {
			t.Fatalf("response = %q", got)
		}
		if len(observer.events) != 1 || observer.events[0].Outcome != "error" {
			t.Fatalf("audit/metric events = %+v", observer.events)
		}
	})
}

func TestHandler_Marketplace(t *testing.T) {
	t.Run("marketplace list", func(t *testing.T) {
		dispatcher := &fakeDispatcher{}
		svc := &fakePluginService{
			marketplaceStatuses: []plugincmd.MarketplaceStatus{
				{Name: "official", Source: "github.com/baldaworks/plugins", Kind: "git"},
			},
		}
		handler := New(nil, svc, dispatcher, zerolog.Nop())

		env, p := newPayload("marketplace list", true)
		if err := handler.Handle(context.Background(), env, p); err != nil {
			t.Fatal(err)
		}
		got := dispatcher.lastText(t)
		if !strings.Contains(got, "official") {
			t.Fatalf("expected marketplace list to contain official, got: %q", got)
		}
	})

	t.Run("marketplace show found", func(t *testing.T) {
		dispatcher := &fakeDispatcher{}
		svc := &fakePluginService{
			marketplaceFound:  true,
			marketplaceStatus: plugincmd.MarketplaceStatus{Name: "official", Source: "github.com/baldaworks/plugins"},
		}
		handler := New(nil, svc, dispatcher, zerolog.Nop())

		env, p := newPayload("marketplace show official", true)
		if err := handler.Handle(context.Background(), env, p); err != nil {
			t.Fatal(err)
		}
		got := dispatcher.lastText(t)
		if !strings.Contains(got, "official") {
			t.Fatalf("expected marketplace show official, got: %q", got)
		}
	})

	t.Run("marketplace show not found", func(t *testing.T) {
		dispatcher := &fakeDispatcher{}
		svc := &fakePluginService{marketplaceFound: false}
		handler := New(nil, svc, dispatcher, zerolog.Nop())

		env, p := newPayload("marketplace show unknown", true)
		if err := handler.Handle(context.Background(), env, p); err != nil {
			t.Fatal(err)
		}
		got := dispatcher.lastText(t)
		if got != "Plugin marketplace not found." {
			t.Fatalf("expected not found message, got: %q", got)
		}
	})

	t.Run("marketplace add", func(t *testing.T) {
		dispatcher := &fakeDispatcher{}
		svc := &fakePluginService{}
		handler := New(nil, svc, dispatcher, zerolog.Nop())

		env, p := newPayload("marketplace add github.com/foo/bar", true)
		if err := handler.Handle(context.Background(), env, p); err != nil {
			t.Fatal(err)
		}
		got := dispatcher.lastText(t)
		if got != "Plugin marketplace added." {
			t.Fatalf("expected 'Plugin marketplace added.', got: %q", got)
		}
		if svc.addedMarketplace.Source != "github.com/foo/bar" {
			t.Fatalf("expected source added, got: %q", svc.addedMarketplace.Source)
		}
	})

	t.Run("marketplace upgrade", func(t *testing.T) {
		dispatcher := &fakeDispatcher{}
		svc := &fakePluginService{
			upgradeResults: []plugincmd.MarketplaceUpgradeResult{
				{Name: "official", Status: plugincmd.MarketplaceStatus{Name: "official"}, Refreshed: true},
			},
		}
		handler := New(nil, svc, dispatcher, zerolog.Nop())

		env, p := newPayload("marketplace upgrade official", true)
		if err := handler.Handle(context.Background(), env, p); err != nil {
			t.Fatal(err)
		}
		got := dispatcher.lastText(t)
		if !strings.Contains(got, "official") {
			t.Fatalf("expected marketplace upgrade official, got: %q", got)
		}
	})

	t.Run("marketplace remove", func(t *testing.T) {
		dispatcher := &fakeDispatcher{}
		svc := &fakePluginService{}
		handler := New(nil, svc, dispatcher, zerolog.Nop())

		env, p := newPayload("marketplace remove official", true)
		if err := handler.Handle(context.Background(), env, p); err != nil {
			t.Fatal(err)
		}
		got := dispatcher.lastText(t)
		if got != "Plugin marketplace removed." {
			t.Fatalf("expected 'Plugin marketplace removed.', got: %q", got)
		}
		if svc.removedMarketplace != "official" {
			t.Fatalf("expected removed official, got: %q", svc.removedMarketplace)
		}
	})
}
