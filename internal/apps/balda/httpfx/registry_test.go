package httpfx

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRegistryDispatchesOnlyOwnedRoutes(t *testing.T) {
	registry, err := NewRegistry("/balda")
	if err != nil {
		t.Fatal(err)
	}
	var browser, gateway, webhook int
	if err := registry.AddBackoffice("backoffice", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		browser++
		w.WriteHeader(http.StatusOK)
	})); err != nil {
		t.Fatal(err)
	}
	if err := registry.AddGateway("slack events", "/balda/gateway/slack/events", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		gateway++
		w.WriteHeader(http.StatusAccepted)
	})); err != nil {
		t.Fatal(err)
	}
	webhookHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		webhook++
		w.WriteHeader(http.StatusAccepted)
	})
	if err := registry.SetWebhookLookup("managed webhooks", webhookHandler, func(_ context.Context, path string) (bool, error) {
		return path == "/balda/webhooks/orders", nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := registry.AddWebhook("legacy orders", "/orders/old", webhookHandler); err != nil {
		t.Fatal(err)
	}
	handler := registry.Handler()
	tests := []struct {
		path string
		want int
	}{
		{"/balda/backoffice/", http.StatusOK},
		{"/balda/backoffice/webhooks", http.StatusOK},
		{"/balda/gateway/slack/events", http.StatusAccepted},
		{"/balda/webhooks/orders", http.StatusAccepted},
		{"/orders/old", http.StatusAccepted},
		{"/balda/webhooks/missing", http.StatusNotFound},
		{"/balda/gateway/webhooks/orders", http.StatusNotFound},
		{"/balda/gateway/slack/missing", http.StatusNotFound},
		{"/balda/backoffice-malformed", http.StatusNotFound},
		{"/other", http.StatusNotFound},
	}
	for _, test := range tests {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, test.path, nil))
		if recorder.Code != test.want {
			t.Errorf("GET %s: status = %d, want %d", test.path, recorder.Code, test.want)
		}
	}
	if browser != 2 || gateway != 1 || webhook != 2 {
		t.Errorf("handler calls = browser %d, gateway %d, webhook %d; want 2, 1, 2", browser, gateway, webhook)
	}
}

func TestRegistryRejectsConflictingRoutes(t *testing.T) {
	tests := []struct {
		name   string
		first  func(*Registry) error
		second func(*Registry) error
		owners []string
		path   string
	}{
		{
			name:  "webhook under browser",
			first: func(r *Registry) error { return r.AddBackoffice("browser", http.NotFoundHandler()) },
			second: func(r *Registry) error {
				return r.AddWebhook("legacy", "/balda/backoffice/custom", http.NotFoundHandler())
			},
			owners: []string{"legacy", "browser"}, path: "/balda/backoffice/custom",
		},
		{
			name: "webhook under gateway",
			first: func(r *Registry) error {
				return r.AddGateway("slack", "/balda/gateway/slack/events", http.NotFoundHandler())
			},
			second: func(r *Registry) error {
				return r.AddWebhook("legacy", "/balda/gateway/slack/events", http.NotFoundHandler())
			},
			owners: []string{"legacy", "slack"}, path: "/balda/gateway/slack/events",
		},
		{
			name:   "duplicate legacy path",
			first:  func(r *Registry) error { return r.AddWebhook("config route", "/orders", http.NotFoundHandler()) },
			second: func(r *Registry) error { return r.AddWebhook("managed route", "/orders", http.NotFoundHandler()) },
			owners: []string{"config route", "managed route"}, path: "/orders",
		},
		{
			name: "gateway under webhooks",
			first: func(r *Registry) error {
				return r.SetWebhookLookup("webhook area", http.NotFoundHandler(), func(context.Context, string) (bool, error) { return false, nil })
			},
			second: func(r *Registry) error { return r.AddGateway("slack", "/balda/webhooks/slack", http.NotFoundHandler()) },
			owners: []string{"slack", "webhook area"}, path: "/balda/webhooks/slack",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			registry, err := NewRegistry("/balda")
			if err != nil {
				t.Fatal(err)
			}
			if err := test.first(registry); err != nil {
				t.Fatal(err)
			}
			err = test.second(registry)
			if err == nil {
				t.Fatal("second route accepted, want conflict")
			}
			for _, want := range append(test.owners, test.path) {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("conflict error %q does not identify %q", err, want)
				}
			}
		})
	}
}

func TestRegistryReportsNonWebhookOwnersForManagement(t *testing.T) {
	registry, err := NewRegistry("/balda")
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.AddBackoffice("browser", http.NotFoundHandler()); err != nil {
		t.Fatal(err)
	}
	if err := registry.AddGateway("slack events", "/balda/gateway/slack/events", http.NotFoundHandler()); err != nil {
		t.Fatal(err)
	}
	if err := registry.AddWebhook("config webhook", "/legacy/orders", http.NotFoundHandler()); err != nil {
		t.Fatal(err)
	}
	if err := registry.AddManagedWebhook("own", "/legacy/own", http.NotFoundHandler()); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ path, owner string }{
		{"/balda/backoffice/webhooks", "browser"},
		{"/balda/gateway/unknown", "gateway"},
		{"/balda/gateway/slack/events", "slack events"},
		{"/legacy/orders", "config webhook"},
		{"/legacy/own", ""},
		{"/balda/webhooks/orders", ""},
	} {
		if got := registry.ConflictingOwner(test.path); got != test.owner {
			t.Errorf("owner of %q = %q, want %q", test.path, got, test.owner)
		}
	}
}

func TestRegistryWebhookLookupFailureDoesNotAdmit(t *testing.T) {
	registry, err := NewRegistry("")
	if err != nil {
		t.Fatal(err)
	}
	called := false
	if err := registry.SetWebhookLookup("webhooks", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	}), func(context.Context, string) (bool, error) { return false, errors.New("storage unavailable") }); err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	registry.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/webhooks/orders", nil))
	if recorder.Code != http.StatusServiceUnavailable || called {
		t.Errorf("lookup failure: status = %d, handler called = %t; want 503 and false", recorder.Code, called)
	}
}

func TestRegistryActivatesLegacyWebhookWithoutRestart(t *testing.T) {
	registry, err := NewRegistry("/balda")
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.AddBackoffice("browser", http.NotFoundHandler()); err != nil {
		t.Fatal(err)
	}
	webhookCalls, lookupCalls := 0, 0
	active := false
	if err := registry.SetWebhookLookup("webhooks", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		webhookCalls++
		w.WriteHeader(http.StatusAccepted)
	}), func(_ context.Context, routePath string) (bool, error) {
		lookupCalls++
		return active && routePath == "/orders/old", nil
	}); err != nil {
		t.Fatal(err)
	}
	handler := registry.Handler()
	request := func(routePath string) int {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, routePath, nil))
		return recorder.Code
	}
	if status := request("/orders/old"); status != http.StatusNotFound {
		t.Fatalf("disabled legacy route status = %d, want 404", status)
	}
	active = true
	if status := request("/orders/old"); status != http.StatusAccepted {
		t.Errorf("enabled legacy route status = %d, want 202", status)
	}
	if status := request("/orders/missing"); status != http.StatusNotFound {
		t.Errorf("unknown legacy route status = %d, want 404", status)
	}
	lookupsBeforeReserved := lookupCalls
	for _, routePath := range []string{"/balda/backoffice/missing", "/balda/gateway/webhooks/orders"} {
		if status := request(routePath); status != http.StatusNotFound {
			t.Errorf("reserved path %s status = %d, want 404", routePath, status)
		}
	}
	if lookupCalls != lookupsBeforeReserved {
		t.Errorf("reserved paths reached webhook lookup: calls = %d, want %d", lookupCalls, lookupsBeforeReserved)
	}
	if webhookCalls != 1 {
		t.Errorf("webhook calls = %d, want 1", webhookCalls)
	}
}

func TestRegistryRejectsInvalidBasePath(t *testing.T) {
	for _, basePath := range []string{"balda", "/balda/", "/balda/../other", "/balda//other"} {
		if _, err := NewRegistry(basePath); err == nil {
			t.Errorf("NewRegistry(%q) accepted invalid base path", basePath)
		}
	}
}
