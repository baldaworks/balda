//go:build integration && (sqlite || postgres)

package state

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"github.com/baldaworks/balda/internal/apps/balda/webhookcmd"
)

func checkProvider_WebhookTestAudit(t *testing.T, open contractOpener) {
	p, err := open(t.Context(), filepath.Join(t.TempDir(), "webhook-test-audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { closeContractProvider(t, p) }()
	if err := p.WebhookRoutes().ReconcileConfig(t.Context(), []WebhookRouteRecord{{
		Name: "configured", Source: WebhookRouteSourceConfig,
		PromptTemplate: "{{.RawBody}}", DedupeSource: "request_id", Enabled: true,
	}}); err != nil {
		t.Fatal(err)
	}
	base := contractMCPMutation(t, p).Authority
	route, found, err := p.WebhookRoutes().Get(t.Context(), "configured")
	if err != nil || !found {
		t.Fatalf("route lookup: found=%t err=%v", found, err)
	}
	authority := &webhookcmd.TestAuthority{UserID: base.UserID, UserVersion: base.UserVersion,
		CredentialVersion: base.CredentialVersion, MFAVersion: base.MFAVersion,
		SessionID: base.SessionID, SessionVersion: base.SessionVersion, At: base.At,
		RouteVersion: route.Version}
	secretBody := "private-body-must-not-enter-audit"
	admission := webhookcmd.Admission{RouteName: route.Name, DedupeKey: "webhook-test:configured:first",
		RequestID: "first", Prompt: "rendered", RawBody: &secretBody, Source: webhookcmd.SourceTest,
		JobID: "test-job-first", SessionID: "wh-test-first", CreatedAt: time.Now().UTC(),
		TestAuthority: authority}
	if _, created, err := p.WebhookAdmissions().Create(t.Context(), admission); err != nil || !created {
		t.Fatalf("test admission: created=%t err=%v", created, err)
	}
	if _, created, err := p.WebhookAdmissions().Create(t.Context(), admission); err != nil || created {
		t.Fatalf("duplicate admission: created=%t err=%v", created, err)
	}
	auditPage, err := p.Users().ListAuditEvents(t.Context(), usercmd.PageRequest{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	auditCount := 0
	for _, event := range auditPage.Events {
		if event.Action != usercmd.AuditActionWebhookTestRequested {
			continue
		}
		auditCount++
		if event.ActorUserID != authority.UserID || event.ActorSessionID != authority.SessionID ||
			event.TargetID != route.Name || event.TargetType != usercmd.AuditTargetWebhookRoute ||
			strings.Contains(event.Reason, secretBody) || strings.Contains(event.RequestID, secretBody) {
			t.Fatalf("unsafe Test POST audit: %+v", event)
		}
	}
	if auditCount != 1 {
		t.Fatalf("Test POST audit count = %d, want 1", auditCount)
	}
	stale := admission
	stale.DedupeKey, stale.JobID = "webhook-test:configured:stale", "test-job-stale"
	stale.TestAuthority = &webhookcmd.TestAuthority{}
	*stale.TestAuthority = *authority
	stale.TestAuthority.RouteVersion++
	if _, created, err := p.WebhookAdmissions().Create(t.Context(), stale); !errors.Is(err, ErrWebhookRouteConflict) || created {
		t.Fatalf("stale route version: created=%t err=%v", created, err)
	}
	stale.TestAuthority.RouteVersion = route.Version
	stale.TestAuthority.UserVersion++
	if _, created, err := p.WebhookAdmissions().Create(t.Context(), stale); !errors.Is(err, ErrWebhookRouteConflict) || created {
		t.Fatalf("stale administrator: created=%t err=%v", created, err)
	}
	if _, found, err := p.WebhookAdmissions().Get(t.Context(), route.Name, stale.DedupeKey); err != nil || found {
		t.Fatalf("rejected test persisted: found=%t err=%v", found, err)
	}
	if err := p.WebhookRoutes().ReconcileConfig(t.Context(), []WebhookRouteRecord{{
		Name: route.Name, Source: WebhookRouteSourceConfig,
		PromptTemplate: "{{.RawBody}}", DedupeSource: "request_id", Enabled: false,
	}}); err != nil {
		t.Fatal(err)
	}
	route, found, err = p.WebhookRoutes().Get(t.Context(), route.Name)
	if err != nil || !found || route.Enabled {
		t.Fatalf("disabled route lookup: %+v found=%t err=%v", route, found, err)
	}
	disabled := admission
	disabled.DedupeKey, disabled.JobID = "webhook-test:configured:disabled", "test-job-disabled"
	disabled.SessionID = "wh-test-disabled"
	disabled.TestAuthority = &webhookcmd.TestAuthority{}
	*disabled.TestAuthority = *authority
	disabled.TestAuthority.RouteVersion = route.Version
	if _, created, err := p.WebhookAdmissions().Create(t.Context(), disabled); !errors.Is(err, ErrWebhookRouteConflict) || created {
		t.Fatalf("unconfirmed disabled route: created=%t err=%v", created, err)
	}
	disabled.TestAuthority.ConfirmDisabled = true
	if _, created, err := p.WebhookAdmissions().Create(t.Context(), disabled); err != nil || !created {
		t.Fatalf("confirmed disabled route: created=%t err=%v", created, err)
	}
}
