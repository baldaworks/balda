package webhookapp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/webhookcmd"
	"github.com/baldaworks/balda/internal/apps/balda/webhookroutecmd"
)

type ingressStore struct {
	route webhookroutecmd.Record
	err   error
}

func (s *ingressStore) LookupByPath(_ context.Context, path string) (webhookroutecmd.Record, bool, error) {
	if s.err != nil {
		return webhookroutecmd.Record{}, false, s.err
	}
	return s.route, s.route.Enabled && !s.route.Deleted && s.route.Path == path, nil
}

func (s *ingressStore) Get(_ context.Context, name string) (webhookroutecmd.Record, bool, error) {
	if s.err != nil {
		return webhookroutecmd.Record{}, false, s.err
	}
	return s.route, s.route.Name == name, nil
}

type ingressAcceptor struct{ request webhookcmd.Request }

func (a *ingressAcceptor) Accept(_ context.Context, request webhookcmd.Request) (webhookcmd.Result, error) {
	a.request = request
	return webhookcmd.Result{RequestID: request.RequestID, JobID: "job-1"}, nil
}

func TestIngress_ManagedRouteSnapshotAndScrubbedHeaders(t *testing.T) {
	secret := "test-secret"
	sum := sha256.Sum256([]byte(secret))
	store := &ingressStore{route: webhookroutecmd.Record{
		Name: "events", Source: webhookroutecmd.SourceManaged, Path: "/events",
		PromptTemplate: "{{index .Headers \"X-Event\"}} {{.RawBody}}", Enabled: true,
		AuthType: webhookroutecmd.AuthTypeHeader, AuthHeader: webhookroutecmd.ManagedSecretHeader,
		SecretVerifier: hex.EncodeToString(sum[:]), DedupeSource: webhookroutecmd.DedupeSourceHeader,
		DedupeHeader: "X-Dedupe", ReportToKind: "managed_alias", ReportToKey: "main_chat",
	}}
	acceptor := &ingressAcceptor{}
	ingress, err := NewIngress(nil, store, acceptor)
	if err != nil {
		t.Fatal(err)
	}
	headers := map[string]string{"X-Balda-Webhook-Secret": secret, "X-Event": "new", "X-Dedupe": "same"}
	prepared, err := ingress.PrepareExternal(t.Context(), "/events", headers)
	if err != nil {
		t.Fatal(err)
	}
	store.route.PromptTemplate = "changed"
	store.route.ReportToKey = "other_chat"
	if _, err := ingress.Admit(t.Context(), prepared, webhookcmd.Inbound{
		RequestID: "req-1", Method: "POST", Path: "/events", Headers: headers, RawBody: "body",
	}); err != nil {
		t.Fatal(err)
	}
	if got := acceptor.request.Prompt; got != "new body" {
		t.Errorf("prompt = %q, want %q", got, "new body")
	}
	if got := acceptor.request.DedupeKey; got != "webhook:events:same" {
		t.Errorf("dedupe key = %q", got)
	}
	if acceptor.request.ReportTo == nil || acceptor.request.ReportTo.Key != "main_chat" {
		t.Errorf("report recipient = %+v", acceptor.request.ReportTo)
	}
	store.route.PromptTemplate = "{{index .Headers \"X-Balda-Webhook-Secret\"}}"
	prepared, err = ingress.PrepareExternal(t.Context(), "/events", headers)
	if err != nil {
		t.Fatal(err)
	}
	_, err = ingress.Admit(t.Context(), prepared, webhookcmd.Inbound{RequestID: "req-2", Method: "POST", Path: "/events", Headers: headers, RawBody: "body"})
	if err == nil || !webhookcmd.IsInvalidRequest(err) {
		t.Fatalf("credential header reached template: %v", err)
	}
}

func TestIngress_DisabledRotatedAndUnavailable(t *testing.T) {
	store := &ingressStore{route: webhookroutecmd.Record{Name: "events", Source: webhookroutecmd.SourceManaged,
		Path: "/events", PromptTemplate: "{{.RawBody}}", AuthType: webhookroutecmd.AuthTypeHeader,
		AuthHeader: webhookroutecmd.ManagedSecretHeader, Enabled: true}}
	ingress, err := NewIngress(nil, store, &ingressAcceptor{})
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("fresh"))
	store.route.SecretVerifier = hex.EncodeToString(sum[:])
	if _, err := ingress.PrepareExternal(t.Context(), "/events", map[string]string{"X-Balda-Webhook-Secret": "stale"}); !errors.Is(err, webhookcmd.ErrUnauthorized) {
		t.Fatalf("stale secret: %v", err)
	}
	if _, err := ingress.PrepareExternal(t.Context(), "/events", map[string]string{"X-Balda-Webhook-Secret": "fresh"}); err != nil {
		t.Fatal(err)
	}
	store.route.Enabled = false
	if _, err := ingress.PrepareExternal(t.Context(), "/events", map[string]string{"X-Balda-Webhook-Secret": "fresh"}); !errors.Is(err, webhookcmd.ErrRouteNotFound) {
		t.Fatalf("disabled route: %v", err)
	}
	store.err = errors.New("database unavailable")
	if _, err := ingress.PrepareExternal(t.Context(), "/events", nil); !webhookcmd.IsDispatchFailed(err) {
		t.Fatalf("lookup failure: %v", err)
	}
}

func TestIngress_ConfigRouteAndTestBypass(t *testing.T) {
	acceptor := &ingressAcceptor{}
	ingress, err := NewIngress([]ConfiguredRoute{{Name: "configured", Path: "/configured",
		PromptTemplate: "{{.RawBody}}", AuthType: webhookroutecmd.AuthTypeHeader,
		AuthHeader: "Authorization", AuthValue: "Bearer configured", DedupeSource: webhookroutecmd.DedupeSourceBodySHA}},
		&ingressStore{}, acceptor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ingress.PrepareExternal(t.Context(), "/configured", nil); !errors.Is(err, webhookcmd.ErrUnauthorized) {
		t.Fatalf("missing config auth: %v", err)
	}
	prepared, err := ingress.PrepareTest(t.Context(), "configured")
	if err != nil {
		t.Fatal(err)
	}
	_, err = ingress.Admit(t.Context(), prepared, webhookcmd.Inbound{RequestID: "test-1", Method: "POST", Path: "/configured", RawBody: "body"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(acceptor.request.DedupeKey, "webhook:configured:") {
		t.Errorf("test dedupe key = %q", acceptor.request.DedupeKey)
	}
}

func TestIngress_CredentialHeaderDedupeDoesNotPersistCredential(t *testing.T) {
	acceptor := &ingressAcceptor{}
	ingress, err := NewIngress([]ConfiguredRoute{{Name: "configured", Path: "/configured",
		PromptTemplate: "{{.RawBody}}", AuthType: webhookroutecmd.AuthTypeHeader,
		AuthHeader: "Authorization", AuthValue: "Bearer configured",
		DedupeSource: webhookroutecmd.DedupeSourceHeader, DedupeHeader: "Authorization"}}, nil, acceptor)
	if err != nil {
		t.Fatal(err)
	}
	headers := map[string]string{"Authorization": "Bearer configured"}
	prepared, err := ingress.PrepareExternal(t.Context(), "/configured", headers)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ingress.Admit(t.Context(), prepared, webhookcmd.Inbound{
		RequestID: "request-1", Path: "/configured", Method: "POST",
		RawBody: "body", Headers: headers,
	}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(acceptor.request.DedupeKey, "Bearer configured") {
		t.Fatalf("dedupe key contains credential: %q", acceptor.request.DedupeKey)
	}
	sum := sha256.Sum256([]byte("Bearer configured"))
	if got, want := acceptor.request.DedupeKey, "webhook:configured:"+hex.EncodeToString(sum[:]); got != want {
		t.Fatalf("dedupe key = %q, want %q", got, want)
	}
}
