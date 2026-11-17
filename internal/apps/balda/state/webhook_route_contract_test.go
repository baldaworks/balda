//go:build integration && (sqlite || postgres)

package state

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"github.com/baldaworks/balda/internal/apps/balda/webhookcmd"
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
		PromptTemplate: "{{.Body}}", DedupeSource: "request_id",
		AuthType: "header", AuthHeader: "X-Config-Token", Enabled: true}
	if err := s.ReconcileConfig(t.Context(), []WebhookRouteRecord{config}); err != nil {
		t.Fatal(err)
	}
	base := contractMCPMutation(t, p)
	a := WebhookRouteAuthority{UserID: base.Authority.UserID, UserVersion: base.Authority.UserVersion,
		CredentialVersion: base.Authority.CredentialVersion, MFAVersion: base.Authority.MFAVersion,
		SessionID: base.Authority.SessionID, SessionVersion: base.Authority.SessionVersion, At: base.Authority.At}
	r := WebhookRouteRecord{Name: "managed", Source: WebhookRouteSourceManaged,
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
	if got, found, err := s.LookupActiveManagedByName(t.Context(), r.Name); err != nil || !found || got.SecretVerifier != r.SecretVerifier {
		t.Fatalf("ingress verifier = %q, found=%t, err=%v", got.SecretVerifier, found, err)
	}
	if _, found, err := s.LookupActiveManagedByName(t.Context(), config.Name); err != nil || found {
		t.Fatalf("config route returned by managed lookup: found=%t err=%v", found, err)
	}
	if routes, err := s.List(t.Context()); err != nil {
		t.Fatal(err)
	} else {
		for _, route := range routes {
			if route.SecretVerifier != "" {
				t.Fatalf("list exposes verifier for %q", route.Name)
			}
		}
	}
	inactive := m
	inactive.Record.Name = "inactive"
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
	if _, found, err := s.LookupActiveManagedByName(t.Context(), inactive.Record.Name); err != nil || found {
		t.Fatalf("disabled managed route active: found=%t err=%v", found, err)
	}
	if err := s.Save(t.Context(), m); !errors.Is(err, ErrWebhookRouteConflict) {
		t.Fatalf("duplicate name = %v, want conflict", err)
	}
	conflict := m
	conflict.Record.Name = config.Name
	conflict.Audit.ID, conflict.Audit.TargetID = "webhook-conflict", config.Name
	if err := s.Save(t.Context(), conflict); !errors.Is(err, ErrWebhookRouteConflict) {
		t.Fatalf("config name collision = %v, want conflict", err)
	}
	m.Kind, m.ExpectedVersion, m.Record.Version, m.Audit.ID = WebhookRouteEdit, 1, 2, "webhook-edit"
	m.Record.PromptTemplate = "edited {{.Body}}"
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
	if got, found, err := s.LookupActiveManagedByName(t.Context(), r.Name); err != nil || !found || got.SecretVerifier != m.Record.SecretVerifier {
		t.Fatalf("rotated verifier = %q, found=%t, err=%v", got.SecretVerifier, found, err)
	}
	priorReport := deliverycmd.Locator{ChannelType: "telegram", AddressKey: "123:0", AddressJSON: `{}`,
		SessionID: "tg-123-0"}
	priorAdmission := webhookcmd.Admission{RouteName: r.Name, DedupeKey: "webhook:managed:prior",
		RequestID: "prior", Prompt: "old generation prompt", JobID: "webhook-managed-prior",
		SessionID: "wh-managed-prior", ReportTo: &priorReport, CreatedAt: a.At}
	if _, created, err := p.WebhookAdmissions().Create(t.Context(), priorAdmission); err != nil || !created {
		t.Fatalf("create prior admission: created=%t err=%v", created, err)
	}
	priorJob := JobRecord{ID: priorAdmission.JobID, SessionID: priorAdmission.SessionID,
		Objective: priorAdmission.Prompt, Status: JobStatusCreated, PrivateRunKind: PrivateRunKindWebhook}
	if created, err := p.Jobs().CreateJob(t.Context(), priorJob); err != nil || !created {
		t.Fatalf("create prior job: created=%t err=%v", created, err)
	}
	conflictingConfig := WebhookRouteRecord{Name: r.Name, Source: WebhookRouteSourceConfig,
		PromptTemplate: "{{.Body}}", DedupeSource: "request_id", Enabled: true}
	if err := s.ReconcileConfig(t.Context(), []WebhookRouteRecord{conflictingConfig}); !errors.Is(err, ErrWebhookRouteConflict) {
		t.Fatalf("config name collision = %v, want conflict", err)
	}
	if got, found, err := s.Get(t.Context(), config.Name); err != nil || !found || got.Name != config.Name {
		t.Fatalf("failed reconciliation changed config route = %+v, found=%t, err=%v", got, found, err)
	}
	if got, found, err := s.LookupActiveManagedByName(t.Context(), r.Name); err != nil || !found || got.Name != r.Name {
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
	if got, found, err := p.WebhookRoutes().Get(t.Context(), r.Name); err != nil || !found || got.PromptTemplate != "edited {{.Body}}" || got.Version != 3 {
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
	if _, found, err := s.LookupActiveManagedByName(t.Context(), r.Name); err != nil || found {
		t.Fatalf("archived route active = %t, err=%v", found, err)
	}
	recreates := make([]WebhookRouteMutation, 2)
	for i, verifier := range []string{strings.Repeat("c", 64), strings.Repeat("d", 64)} {
		recreates[i] = m
		recreates[i].Kind, recreates[i].ExpectedVersion = WebhookRouteRecreate, 4
		recreates[i].Record.Version, recreates[i].Record.Deleted, recreates[i].Record.Enabled = 5, false, true
		recreates[i].Record.PromptTemplate = "recreated {{.Body}}"
		recreates[i].Record.SecretVerifier = verifier
		recreates[i].Audit.ID = "webhook-recreate-" + string(rune('a'+i))
	}
	errCh := make(chan error, len(recreates))
	for i := range recreates {
		go func(mutation WebhookRouteMutation) {
			errCh <- s.Save(t.Context(), mutation)
		}(recreates[i])
	}
	var succeeded, conflicted int
	for range recreates {
		switch err := <-errCh; {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrWebhookRouteConflict):
			conflicted++
		default:
			t.Fatalf("concurrent recreate = %v", err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("concurrent recreate: succeeded=%d conflicted=%d", succeeded, conflicted)
	}
	got, found, err := s.LookupActiveManagedByName(t.Context(), r.Name)
	if err != nil || !found || got.Version != 5 || got.Deleted || !got.Enabled ||
		got.SecretVerifier == strings.Repeat("b", 64) {
		t.Fatalf("recreated managed route = %+v, found=%t, err=%v", got, found, err)
	}
	staleDelete := m
	staleDelete.Audit.ID = "webhook-stale-delete"
	if err := s.Save(t.Context(), staleDelete); !errors.Is(err, ErrWebhookRouteConflict) {
		t.Fatalf("stale pre-recreate delete = %v, want conflict", err)
	}
	if history, found, err := p.WebhookAdmissions().GetHistory(t.Context(), r.Name, priorAdmission.JobID); err != nil || !found || history.ReportTo == nil || *history.ReportTo != priorReport {
		t.Fatalf("prior admission after recreate = %+v, found=%t, err=%v", history, found, err)
	}
	if job, found, err := p.Jobs().GetJob(t.Context(), priorJob.ID); err != nil || !found ||
		job.SessionID != priorJob.SessionID || job.Objective != priorJob.Objective ||
		job.PrivateRunKind != priorJob.PrivateRunKind {
		t.Fatalf("prior job after recreate = %+v, found=%t, err=%v", job, found, err)
	}
	audits, err := p.Users().ListAuditEvents(t.Context(), usercmd.PageRequest{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	var recreateAudits int
	for _, event := range audits.Events {
		if event.TargetType == usercmd.AuditTargetWebhookRoute && event.TargetID == r.Name &&
			event.Reason == "Webhook route recreated" {
			recreateAudits++
			if event.RequestID != "" || event.CorrelationID != "" ||
				strings.Contains(event.Reason, "recreated {{.Body}}") ||
				strings.Contains(event.Reason, got.SecretVerifier) {
				t.Fatalf("recreate audit contains request or definition data: %+v", event)
			}
		}
	}
	if recreateAudits != 1 {
		t.Fatalf("recreate audits = %d, want one committed winner", recreateAudits)
	}
	if err := s.ReconcileConfig(t.Context(), []WebhookRouteRecord{conflictingConfig}); !errors.Is(err, ErrWebhookRouteConflict) {
		t.Fatalf("archived managed name collision = %v, want conflict", err)
	}
	if err := s.ReconcileConfig(t.Context(), []WebhookRouteRecord{config}); err != nil {
		t.Fatal(err)
	}
	if got, found, err := s.Get(t.Context(), config.Name); err != nil || !found || got.Deleted || !got.Enabled {
		t.Fatalf("revived config = %+v found=%t err=%v", got, found, err)
	}
}
