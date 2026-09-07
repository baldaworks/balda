package plugin

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/actors"
	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/pluginapp"
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

func (f *fakeOwnerStore) IsOwner(userID int64) bool {
	return f.ownerID != 0 && f.ownerID == userID
}

func (f *fakeOwnerStore) IsOwnerSubject(_ string) bool {
	return false
}

type fakePluginService struct {
	installed          []pluginapp.PluginSummary
	installedErr       error
	available          []pluginapp.AvailablePlugin
	availableErr       error
	getAvailable       pluginapp.AvailablePlugin
	getAvailableFound  bool
	getAvailableErr    error
	getInstalled       pluginapp.PluginSummary
	getInstalledFound  bool
	getInstalledErr    error
	installErr         error
	installedRef       string
	removeInstalledErr error
	removedInstalled   string

	marketplaceStatuses []pluginapp.MarketplaceStatus
	marketplaceListErr  error
	marketplaceStatus   pluginapp.MarketplaceStatus
	marketplaceFound    bool
	marketplaceGetErr   error
	addMarketplaceErr   error
	addedMarketplace    pluginapp.MarketplaceSource
	upgradeResults      []pluginapp.MarketplaceUpgradeResult
	upgradeErr          error
	upgradedName        string
	removeMarketErr     error
	removedMarketplace  string
}

func (f *fakePluginService) ListInstalled(_ context.Context) ([]pluginapp.PluginSummary, error) {
	return f.installed, f.installedErr
}

func (f *fakePluginService) ListAvailable(_ context.Context) ([]pluginapp.AvailablePlugin, error) {
	return f.available, f.availableErr
}

func (f *fakePluginService) GetInstalled(_ context.Context, _ string) (pluginapp.PluginSummary, bool, error) {
	return f.getInstalled, f.getInstalledFound, f.getInstalledErr
}

func (f *fakePluginService) GetAvailable(_ context.Context, _ string) (pluginapp.AvailablePlugin, bool, error) {
	return f.getAvailable, f.getAvailableFound, f.getAvailableErr
}

func (f *fakePluginService) Install(_ context.Context, ref string) error {
	f.installedRef = ref
	return f.installErr
}

func (f *fakePluginService) RemoveInstalled(_ context.Context, name string) error {
	f.removedInstalled = name
	return f.removeInstalledErr
}

func (f *fakePluginService) ListMarketplaceStatuses(_ context.Context) ([]pluginapp.MarketplaceStatus, error) {
	return f.marketplaceStatuses, f.marketplaceListErr
}

func (f *fakePluginService) GetMarketplaceStatus(_ context.Context, _ string) (pluginapp.MarketplaceStatus, bool, error) {
	return f.marketplaceStatus, f.marketplaceFound, f.marketplaceGetErr
}

func (f *fakePluginService) AddMarketplace(_ context.Context, src pluginapp.MarketplaceSource) error {
	f.addedMarketplace = src
	return f.addMarketplaceErr
}

func (f *fakePluginService) UpgradeMarketplaces(_ context.Context, name string) ([]pluginapp.MarketplaceUpgradeResult, error) {
	f.upgradedName = name
	return f.upgradeResults, f.upgradeErr
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
			installed: []pluginapp.PluginSummary{
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
			available: []pluginapp.AvailablePlugin{
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
			getInstalled:      pluginapp.PluginSummary{Name: "foo", Version: "1.0", Description: "Foo plugin"},
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
			getAvailable:      pluginapp.AvailablePlugin{Name: "bar", Version: "2.0"},
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

func TestHandler_Marketplace(t *testing.T) {
	t.Run("marketplace list", func(t *testing.T) {
		dispatcher := &fakeDispatcher{}
		svc := &fakePluginService{
			marketplaceStatuses: []pluginapp.MarketplaceStatus{
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
			marketplaceStatus: pluginapp.MarketplaceStatus{Name: "official", Source: "github.com/baldaworks/plugins"},
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
			upgradeResults: []pluginapp.MarketplaceUpgradeResult{
				{Name: "official", Status: pluginapp.MarketplaceStatus{Name: "official"}, Refreshed: true},
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
