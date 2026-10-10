package webhookfx

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/channel/webhook"
	"github.com/baldaworks/balda/internal/apps/balda/httpfx"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/balda/internal/apps/balda/webhookapp"
	"github.com/baldaworks/balda/internal/apps/balda/webhookcmd"
	"github.com/baldaworks/balda/internal/apps/balda/webhookroutecmd"
	"github.com/rs/zerolog"
)

type contributionRoutes struct {
	records map[string]state.WebhookRouteRecord
	err     error
}

func (s *contributionRoutes) List(context.Context) ([]state.WebhookRouteRecord, error) {
	if s.err != nil {
		return nil, s.err
	}
	var records []state.WebhookRouteRecord
	for _, record := range s.records {
		records = append(records, record)
	}
	return records, nil
}

func (s *contributionRoutes) LookupActiveManagedByName(_ context.Context, name string) (webhookroutecmd.Record, bool, error) {
	if s.err != nil {
		return webhookroutecmd.Record{}, false, s.err
	}
	r, found := s.records[name]
	return routeRecord(r), found && r.Source == state.WebhookRouteSourceManaged && r.Enabled && !r.Deleted, nil
}

func (s *contributionRoutes) Get(_ context.Context, name string) (webhookroutecmd.Record, bool, error) {
	record, ok := s.records[name]
	return routeRecord(record), ok, nil
}

type contributionAcceptor struct{ count int }

func (a *contributionAcceptor) Accept(_ context.Context, request webhookcmd.Request) (webhookcmd.Result, error) {
	a.count++
	return webhookcmd.Result{RequestID: request.RequestID, MessageID: "accepted"}, nil
}

func TestHTTPContributionRoutesCanonicalNames(t *testing.T) {
	secret := "orders-secret"
	sum := sha256.Sum256([]byte(secret))
	routes := &contributionRoutes{records: map[string]state.WebhookRouteRecord{
		"orders": {Name: "orders", Source: state.WebhookRouteSourceManaged,
			PromptTemplate: "{{.RawBody}}", AuthType: webhookroutecmd.AuthTypeHeader,
			AuthHeader: webhookroutecmd.ManagedSecretHeader, SecretVerifier: hex.EncodeToString(sum[:]), Enabled: true},
		"later": {Name: "later", Source: state.WebhookRouteSourceManaged,
			PromptTemplate: "{{.RawBody}}", AuthType: webhookroutecmd.AuthTypeHeader,
			AuthHeader: webhookroutecmd.ManagedSecretHeader, SecretVerifier: hex.EncodeToString(sum[:]), Enabled: false},
	}}
	config := webhook.Config{Enabled: true, Routes: map[string]webhook.RouteConfig{
		"configured": {PromptTemplate: "{{.RawBody}}",
			Auth: webhook.RouteAuthConfig{Type: webhook.AuthTypeHeader, Header: "X-Configured-Secret", Value: "config-secret"}},
	}}
	acceptor := &contributionAcceptor{}
	ingress, err := webhookapp.NewIngress("/balda", []webhookapp.ConfiguredRoute{{Name: "configured",
		PromptTemplate: "{{.RawBody}}", AuthType: webhookroutecmd.AuthTypeHeader,
		AuthHeader: "X-Configured-Secret", AuthValue: "config-secret"}}, routes, acceptor)
	if err != nil {
		t.Fatal(err)
	}
	receiver, err := webhook.NewReceiver(config, ingress, zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	register := func() http.Handler {
		t.Helper()
		registry, err := httpfx.NewRegistry("/balda")
		if err != nil {
			t.Fatal(err)
		}
		if err := NewHTTPContribution(receiver, ingress)(t.Context(), registry); err != nil {
			t.Fatal(err)
		}
		return registry.Handler()
	}
	handler := register()
	serve := func(handler http.Handler, path, header, value string) int {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader("order 42"))
		if header != "" {
			request.Header.Set(header, value)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response.Code
	}
	for _, test := range []struct {
		path, header, value string
		want                int
	}{
		{"/balda/webhooks/orders", webhookroutecmd.ManagedSecretHeader, secret, http.StatusAccepted},
		{"/balda/webhooks/orders", webhookroutecmd.ManagedSecretHeader, "bad", http.StatusUnauthorized},
		{"/balda/gateway/webhooks/orders", webhookroutecmd.ManagedSecretHeader, secret, http.StatusNotFound},
		{"/balda/webhooks/unknown", "", "", http.StatusNotFound},
		{"/balda/webhooks/later", webhookroutecmd.ManagedSecretHeader, secret, http.StatusNotFound},
		{"/balda/webhooks/configured", "X-Configured-Secret", "config-secret", http.StatusAccepted},
	} {
		if got := serve(handler, test.path, test.header, test.value); got != test.want {
			t.Errorf("POST %s: status = %d, want %d", test.path, got, test.want)
		}
	}
	// A Backoffice edit takes effect without rebuilding the handler.
	record := routes.records["later"]
	record.Enabled = true
	routes.records["later"] = record
	if got := serve(handler, "/balda/webhooks/later", webhookroutecmd.ManagedSecretHeader, secret); got != http.StatusAccepted {
		t.Errorf("newly enabled route: status = %d, want 202", got)
	}
	// A restarted listener also reads the original stored path.
	if got := serve(register(), "/balda/webhooks/later", webhookroutecmd.ManagedSecretHeader, secret); got != http.StatusAccepted {
		t.Errorf("route after restart: status = %d, want 202", got)
	}
	orders := routes.records["orders"]
	orders.Enabled = false
	routes.records["orders"] = orders
	if got := serve(handler, "/balda/webhooks/orders", webhookroutecmd.ManagedSecretHeader, secret); got != http.StatusNotFound {
		t.Errorf("disabled managed route: status = %d, want 404", got)
	}
	if acceptor.count != 4 {
		t.Errorf("accepted jobs = %d, want 4", acceptor.count)
	}
}

func TestHTTPContributionDerivesPathAcrossProviderRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "webhooks.db")
	provider, err := state.NewSQLiteProvider(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.Close(); err != nil {
		t.Fatal(err)
	}
	fixture, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	secret := "legacy-secret"
	sum := sha256.Sum256([]byte(secret))
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = fixture.ExecContext(t.Context(), `INSERT INTO balda_webhook_routes
		(name, source, prompt_template, auth_type, auth_header, secret_verifier, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, "later", state.WebhookRouteSourceManaged,
		"{{.RawBody}}", webhookroutecmd.AuthTypeHeader, webhookroutecmd.ManagedSecretHeader,
		hex.EncodeToString(sum[:]), 1, now, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.Close(); err != nil {
		t.Fatal(err)
	}
	for restart := range 2 {
		provider, err = state.NewSQLiteProvider(t.Context(), path)
		if err != nil {
			t.Fatal(err)
		}
		store := provider.WebhookRoutes()
		ingress, err := webhookapp.NewIngress("/balda", nil, routeLookup{store: store}, &contributionAcceptor{})
		if err != nil {
			t.Fatal(err)
		}
		receiver, err := webhook.NewReceiver(webhook.Config{}, ingress, zerolog.Nop())
		if err != nil {
			t.Fatal(err)
		}
		registry, err := httpfx.NewRegistry("/balda")
		if err != nil {
			t.Fatal(err)
		}
		if err := NewHTTPContribution(receiver, ingress)(t.Context(), registry); err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodPost, "/balda/webhooks/later", strings.NewReader("order 42"))
		request.Header.Set(webhookroutecmd.ManagedSecretHeader, secret)
		response := httptest.NewRecorder()
		registry.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusAccepted {
			t.Errorf("restart %d: status = %d, want 202", restart, response.Code)
		}
		record, found, err := store.Get(t.Context(), "later")
		if err != nil || !found || record.Name != "later" {
			t.Errorf("restart %d: record = %+v, found = %t, err = %v", restart, record, found, err)
		}
		if err := provider.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
