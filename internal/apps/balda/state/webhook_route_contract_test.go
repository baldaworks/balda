//go:build integration && (sqlite || postgres)

package state

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

func checkProvider_WebhookRouteManagement(t *testing.T, open contractOpener) {
	path := filepath.Join(t.TempDir(), "webhook-routes.db")
	p, err := open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()
	s := p.WebhookRoutes()
	config := WebhookRouteRecord{Name: "configured", Source: WebhookRouteSourceConfig,
		Path: "/configured", PromptTemplate: "{{.Body}}", DedupeSource: "request_id",
		AuthType: "header", AuthHeader: "X-Config-Token", Enabled: true}
	if err := s.ReconcileConfig(t.Context(), []WebhookRouteRecord{config}); err != nil {
		t.Fatal(err)
	}
	base := contractMCPMutation(t, p)
	a := WebhookRouteAuthority{UserID: base.Authority.UserID, UserVersion: base.Authority.UserVersion,
		CredentialVersion: base.Authority.CredentialVersion, MFAVersion: base.Authority.MFAVersion,
		SessionID: base.Authority.SessionID, SessionVersion: base.Authority.SessionVersion, At: base.Authority.At}
	r := WebhookRouteRecord{Name: "managed", Source: WebhookRouteSourceManaged, Path: "/managed",
		PromptTemplate: "{{.Body}}", DedupeSource: "body_sha256", AuthType: "header",
		AuthHeader: "X-Balda-Webhook-Secret", SecretVerifier: strings.Repeat("a", 64),
		Enabled: true, Version: 1}
	audit := usercmd.AuditEvent{ID: "webhook-create", Action: usercmd.AuditActionWebhookRouteChanged,
		Outcome: usercmd.AuditOutcomeSucceeded, ActorUserID: a.UserID, ActorSessionID: a.SessionID,
		TargetType: usercmd.AuditTargetWebhookRoute, TargetID: r.Name,
		Source: "provider-contract", OccurredAt: a.At}
	m := WebhookRouteMutation{Kind: WebhookRouteCreate, Record: r, Authority: a, Audit: audit}
	if err := s.Save(t.Context(), m); err != nil {
		t.Fatal(err)
	}
	if got, found, err := s.Get(t.Context(), r.Name); err != nil || !found || got.SecretVerifier != "" {
		t.Fatalf("read model secret = %q, found=%t, err=%v", got.SecretVerifier, found, err)
	}
	if got, found, err := s.LookupByPath(t.Context(), r.Path); err != nil || !found || got.SecretVerifier != r.SecretVerifier {
		t.Fatalf("ingress verifier = %q, found=%t, err=%v", got.SecretVerifier, found, err)
	}
	inactive := m
	inactive.Record.Name, inactive.Record.Path = "inactive", "/old/inactive"
	inactive.Audit.ID, inactive.Audit.TargetID = "webhook-inactive-create", inactive.Record.Name
	if err := s.Save(t.Context(), inactive); err != nil {
		t.Fatal(err)
	}
	inactive.Kind, inactive.ExpectedVersion = WebhookRouteSelection, 1
	inactive.Record.Enabled, inactive.Record.Version = false, 2
	inactive.Audit.ID = "webhook-inactive-disable"
	if err := s.Save(t.Context(), inactive); err != nil {
		t.Fatal(err)
	}
	inactive.Kind, inactive.ExpectedVersion = WebhookRouteEdit, 2
	inactive.Record.Path, inactive.Record.Version = r.Path, 3
	inactive.Audit.ID = "webhook-inactive-edit"
	if err := s.Save(t.Context(), inactive); !errors.Is(err, ErrWebhookRouteConflict) ||
		!strings.Contains(err.Error(), r.Path) || !strings.Contains(err.Error(), r.Name) {
		t.Fatalf("disabled route edit over active managed path = %v, want named conflict", err)
	}
	if got, found, err := s.Get(t.Context(), "inactive"); err != nil || !found || got.Path != "/old/inactive" || got.Version != 2 {
		t.Fatalf("conflicting edit changed inactive row = %+v, found=%t, err=%v", got, found, err)
	}
	if err := s.Save(t.Context(), m); !errors.Is(err, ErrWebhookRouteConflict) {
		t.Fatalf("duplicate name = %v, want conflict", err)
	}
	conflict := m
	conflict.Record.Name, conflict.Record.Path = "other", config.Path
	conflict.Audit.ID, conflict.Audit.TargetID = "webhook-conflict", conflict.Record.Name
	if err := s.Save(t.Context(), conflict); !errors.Is(err, ErrWebhookRouteConflict) {
		t.Fatalf("duplicate path = %v, want conflict", err)
	}
	conflict.Record.Name, conflict.Record.Path = config.Name, "/other"
	conflict.Audit.TargetID = config.Name
	if err := s.Save(t.Context(), conflict); !errors.Is(err, ErrWebhookRouteConflict) {
		t.Fatalf("config name collision = %v, want conflict", err)
	}
	m.Kind, m.ExpectedVersion, m.Record.Version, m.Audit.ID = WebhookRouteEdit, 1, 2, "webhook-edit"
	m.Record.Path = "/edited"
	stale := m
	stale.Authority.UserVersion++
	if err := s.Save(t.Context(), stale); !errors.Is(err, ErrWebhookRouteConflict) {
		t.Fatalf("stale authority = %v, want conflict", err)
	}
	if err := s.Save(t.Context(), m); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(t.Context(), m); !errors.Is(err, ErrWebhookRouteConflict) {
		t.Fatalf("stale definition = %v, want conflict", err)
	}
	m.Kind, m.ExpectedVersion, m.Record.Version, m.Audit.ID = WebhookRouteRotate, 2, 3, "webhook-rotate"
	m.Record.SecretVerifier = strings.Repeat("b", 64)
	if err := s.Save(t.Context(), m); err != nil {
		t.Fatal(err)
	}
	if got, found, err := s.LookupByPath(t.Context(), "/edited"); err != nil || !found || got.SecretVerifier != m.Record.SecretVerifier {
		t.Fatalf("rotated verifier = %q, found=%t, err=%v", got.SecretVerifier, found, err)
	}
	conflictingConfig := WebhookRouteRecord{Name: "second-config", Source: WebhookRouteSourceConfig,
		Path: "/edited", PromptTemplate: "{{.Body}}", DedupeSource: "request_id", Enabled: true}
	if err := s.ReconcileConfig(t.Context(), []WebhookRouteRecord{conflictingConfig}); !errors.Is(err, ErrWebhookRouteConflict) {
		t.Fatalf("config path collision = %v, want conflict", err)
	}
	if got, found, err := s.LookupByPath(t.Context(), config.Path); err != nil || !found || got.Name != config.Name {
		t.Fatalf("failed reconciliation changed config route = %+v, found=%t, err=%v", got, found, err)
	}
	if got, found, err := s.LookupByPath(t.Context(), "/edited"); err != nil || !found || got.Name != r.Name {
		t.Fatalf("failed reconciliation changed managed route = %+v, found=%t, err=%v", got, found, err)
	}
	if err := s.ReconcileConfig(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if got, found, err := s.Get(t.Context(), config.Name); err != nil || !found || !got.Deleted {
		t.Fatalf("archived config = %+v, found=%t, err=%v", got, found, err)
	}
	if got, found, err := s.Get(t.Context(), r.Name); err != nil || !found || got.Deleted {
		t.Fatalf("managed after config reconciliation = %+v, found=%t, err=%v", got, found, err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	p, err = open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if got, found, err := p.WebhookRoutes().Get(t.Context(), r.Name); err != nil || !found || got.Path != "/edited" || got.Version != 3 {
		t.Fatalf("managed after restart = %+v, found=%t, err=%v", got, found, err)
	}
	s = p.WebhookRoutes()
	m.Kind, m.ExpectedVersion, m.Record.Version, m.Audit.ID = WebhookRouteDelete, 3, 4, "webhook-delete"
	m.Record.Deleted = true
	if err := s.Save(t.Context(), m); err != nil {
		t.Fatal(err)
	}
	if got, found, err := s.Get(t.Context(), r.Name); err != nil || !found || !got.Deleted || got.Enabled {
		t.Fatalf("managed archive = %+v, found=%t, err=%v", got, found, err)
	}
	if _, found, err := s.LookupByPath(t.Context(), "/edited"); err != nil || found {
		t.Fatalf("archived path active = %t, err=%v", found, err)
	}
	newRoute := m
	newRoute.Kind, newRoute.ExpectedVersion = WebhookRouteCreate, 0
	newRoute.Record.Name, newRoute.Record.Path = "replacement", "/edited"
	newRoute.Record.Version, newRoute.Record.Deleted, newRoute.Record.Enabled = 1, false, true
	newRoute.Audit.ID, newRoute.Audit.TargetID = "webhook-replacement", newRoute.Record.Name
	if err := s.Save(t.Context(), newRoute); err != nil {
		t.Fatalf("reuse archived path: %v", err)
	}
	newRoute.Record.Path = "/other"
	newRoute.Audit.ID = "webhook-reuse-name"
	newRoute.Record.Name, newRoute.Audit.TargetID = r.Name, r.Name
	if err := s.Save(t.Context(), newRoute); !errors.Is(err, ErrWebhookRouteConflict) {
		t.Fatalf("reuse archived name = %v, want conflict", err)
	}
}
