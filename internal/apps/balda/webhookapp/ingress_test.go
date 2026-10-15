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

func (s *ingressStore) LookupActiveManagedByName(_ context.Context, name string) (webhookroutecmd.Record, bool, error) {
	if s.err != nil {
		return webhookroutecmd.Record{}, false, s.err
	}
	return s.route, s.route.Enabled && !s.route.Deleted && s.route.Source == webhookroutecmd.SourceManaged && s.route.Name == name, nil
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
		Name: "events", Source: webhookroutecmd.SourceManaged,
		PromptTemplate: "{{index .Headers \"X-Event\"}} {{.RawBody}}", Enabled: true,
		AuthType: webhookroutecmd.AuthTypeHeader, AuthHeader: webhookroutecmd.ManagedSecretHeader,
		SecretVerifier: hex.EncodeToString(sum[:]), DedupeSource: webhookroutecmd.DedupeSourceHeader,
		DedupeHeader: "X-Dedupe", ReportToKind: "managed_alias", ReportToKey: "main_chat",
	}}
	acceptor := &ingressAcceptor{}
	ingress, err := NewIngress("", nil, store, acceptor)
	if err != nil {
		t.Fatal(err)
	}
	headers := map[string]string{"X-Balda-Webhook-Secret": secret, "X-Event": "new", "X-Dedupe": "same"}
	prepared, err := ingress.PrepareExternal(t.Context(), "/webhooks/events", headers)
	if err != nil {
		t.Fatal(err)
	}
	store.route.PromptTemplate = "changed"
	store.route.ReportToKey = "other_chat"
	if _, err := ingress.Admit(t.Context(), prepared, webhookcmd.Inbound{
		RequestID: "req-1", Method: "POST", Path: "/webhooks/events", Headers: headers, RawBody: "body",
	}); err != nil {
		t.Fatal(err)
	}
	if got := acceptor.request.Prompt; got != "new body" {
		t.Errorf("prompt = %q, want %q", got, "new body")
	}
	if acceptor.request.RawBody != "body" || acceptor.request.Test {
		t.Errorf("history input = %+v", acceptor.request)
	}
	if got := acceptor.request.DedupeKey; got != "webhook:events:same" {
		t.Errorf("dedupe key = %q", got)
	}
	if acceptor.request.ReportTo == nil || acceptor.request.ReportTo.Key != "main_chat" {
		t.Errorf("report recipient = %+v", acceptor.request.ReportTo)
	}
	store.route.PromptTemplate = "{{index .Headers \"X-Balda-Webhook-Secret\"}}"
	prepared, err = ingress.PrepareExternal(t.Context(), "/webhooks/events", headers)
	if err != nil {
		t.Fatal(err)
	}
	_, err = ingress.Admit(t.Context(), prepared, webhookcmd.Inbound{RequestID: "req-2", Method: "POST", Path: "/webhooks/events", Headers: headers, RawBody: "body"})
	if err == nil || !webhookcmd.IsInvalidRequest(err) {
		t.Fatalf("credential header reached template: %v", err)
	}
}

func TestIngress_DisabledRotatedAndUnavailable(t *testing.T) {
	store := &ingressStore{route: webhookroutecmd.Record{Name: "events", Source: webhookroutecmd.SourceManaged,
		PromptTemplate: "{{.RawBody}}", AuthType: webhookroutecmd.AuthTypeHeader,
		AuthHeader: webhookroutecmd.ManagedSecretHeader, Enabled: true}}
	ingress, err := NewIngress("", nil, store, &ingressAcceptor{})
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("fresh"))
	store.route.SecretVerifier = hex.EncodeToString(sum[:])
	if _, err := ingress.PrepareExternal(t.Context(), "/webhooks/events", map[string]string{"X-Balda-Webhook-Secret": "stale"}); !errors.Is(err, webhookcmd.ErrUnauthorized) {
		t.Fatalf("stale secret: %v", err)
	}
	if _, err := ingress.PrepareExternal(t.Context(), "/webhooks/events", map[string]string{"X-Balda-Webhook-Secret": "fresh"}); err != nil {
		t.Fatal(err)
	}
	store.route.Enabled = false
	if _, err := ingress.PrepareExternal(t.Context(), "/webhooks/events", map[string]string{"X-Balda-Webhook-Secret": "fresh"}); !errors.Is(err, webhookcmd.ErrRouteNotFound) {
		t.Fatalf("disabled route: %v", err)
	}
	store.err = errors.New("database unavailable")
	if _, err := ingress.PrepareExternal(t.Context(), "/webhooks/events", nil); !webhookcmd.IsDispatchFailed(err) {
		t.Fatalf("lookup failure: %v", err)
	}
}

func TestIngress_IsActivePathTracksManagedSelection(t *testing.T) {
	store := &ingressStore{route: webhookroutecmd.Record{
		Name: "orders", Source: webhookroutecmd.SourceManaged, Enabled: false,
	}}
	ingress, err := NewIngress("", []ConfiguredRoute{{Name: "config", PromptTemplate: "{{.RawBody}}"}}, store, &ingressAcceptor{})
	if err != nil {
		t.Fatal(err)
	}
	active, err := ingress.IsActivePath(t.Context(), "/webhooks/config")
	if err != nil || !active {
		t.Fatalf("configured route = %t, %v", active, err)
	}
	for _, path := range []string{"/webhooks/orders", "/webhooks/unknown"} {
		active, err := ingress.IsActivePath(t.Context(), path)
		if err != nil || active {
			t.Errorf("inactive %q = %t, %v", path, active, err)
		}
	}
	store.route.Enabled = true
	active, err = ingress.IsActivePath(t.Context(), "/webhooks/orders")
	if err != nil || !active {
		t.Fatalf("enabled managed route = %t, %v", active, err)
	}
	store.route.Source = webhookroutecmd.SourceConfig
	active, err = ingress.IsActivePath(t.Context(), "/webhooks/orders")
	if err != nil || active {
		t.Fatalf("store config route = %t, %v", active, err)
	}
	store.err = errors.New("database unavailable")
	if _, err := ingress.IsActivePath(t.Context(), "/webhooks/orders"); err == nil {
		t.Fatal("store failure hidden")
	}
}

func TestIngress_ConfigRouteAndTestBypass(t *testing.T) {
	acceptor := &ingressAcceptor{}
	ingress, err := NewIngress("", []ConfiguredRoute{{Name: "configured",
		PromptTemplate: "{{.RawBody}}", AuthType: webhookroutecmd.AuthTypeHeader,
		AuthHeader: "Authorization", AuthValue: "Bearer configured", DedupeSource: webhookroutecmd.DedupeSourceBodySHA}},
		&ingressStore{}, acceptor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ingress.PrepareExternal(t.Context(), "/webhooks/configured", nil); !errors.Is(err, webhookcmd.ErrUnauthorized) {
		t.Fatalf("missing config auth: %v", err)
	}
	prepared, err := ingress.PrepareTest(t.Context(), "configured")
	if err != nil {
		t.Fatal(err)
	}
	_, err = ingress.Admit(t.Context(), prepared, webhookcmd.Inbound{RequestID: "test-1", Method: "POST", Path: "/webhooks/configured", RawBody: "body"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(acceptor.request.DedupeKey, "webhook:configured:") {
		t.Errorf("test dedupe key = %q", acceptor.request.DedupeKey)
	}
}

func TestIngress_CredentialHeaderDedupeDoesNotPersistCredential(t *testing.T) {
	acceptor := &ingressAcceptor{}
	ingress, err := NewIngress("", []ConfiguredRoute{{Name: "configured",
		PromptTemplate: "{{.RawBody}}", AuthType: webhookroutecmd.AuthTypeHeader,
		AuthHeader: "Authorization", AuthValue: "Bearer configured",
		DedupeSource: webhookroutecmd.DedupeSourceHeader, DedupeHeader: "Authorization"}}, nil, acceptor)
	if err != nil {
		t.Fatal(err)
	}
	headers := map[string]string{"Authorization": "Bearer configured"}
	prepared, err := ingress.PrepareExternal(t.Context(), "/webhooks/configured", headers)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ingress.Admit(t.Context(), prepared, webhookcmd.Inbound{
		RequestID: "request-1", Path: "/webhooks/configured", Method: "POST",
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

func TestIngress_DisabledConfiguredRouteTestAdmission(t *testing.T) {
	acceptor := &ingressAcceptor{}
	ingress, err := NewIngress("", []ConfiguredRoute{{Name: "configured",
		PromptTemplate: "event: {{.RawBody}}", Disabled: true}}, nil, acceptor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ingress.PrepareExternal(t.Context(), "/webhooks/configured", nil); !errors.Is(err, webhookcmd.ErrRouteNotFound) {
		t.Fatalf("disabled external route: %v", err)
	}
	prepared, err := ingress.PrepareTest(t.Context(), "configured")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ingress.Admit(t.Context(), prepared, webhookcmd.Inbound{
		RequestID: "test-1", Path: "/webhooks/configured", Method: "POST", RawBody: "hello", Test: true,
	}); err != nil {
		t.Fatal(err)
	}
	if !acceptor.request.Test || acceptor.request.RawBody != "hello" {
		t.Errorf("test history marker and input = %+v", acceptor.request)
	}
	if acceptor.request.Prompt != "event: hello" || acceptor.request.DedupeKey != "webhook-test:configured:test-1" {
		t.Fatalf("test admission = %+v", acceptor.request)
	}
}

func TestIngress_AuthRequestIDIsReplacedBeforeTemplate(t *testing.T) {
	const secret = "credential-used-as-request-id"
	acceptor := &ingressAcceptor{}
	ingress, err := NewIngress("", []ConfiguredRoute{{Name: "events",
		PromptTemplate: "{{.RequestID}}", AuthType: webhookroutecmd.AuthTypeHeader,
		AuthHeader: "X-Request-Id", AuthValue: secret}}, nil, acceptor)
	if err != nil {
		t.Fatal(err)
	}
	headers := map[string]string{"X-Request-Id": secret}
	prepared, err := ingress.PrepareExternal(t.Context(), "/webhooks/events", headers)
	if err != nil {
		t.Fatal(err)
	}
	_, err = ingress.Admit(t.Context(), prepared, webhookcmd.Inbound{
		RequestID: secret, Method: "POST", Path: "/webhooks/events", Headers: headers,
	})
	if err != nil {
		t.Fatal(err)
	}
	if acceptor.request.RequestID == secret || acceptor.request.Prompt == secret ||
		strings.Contains(acceptor.request.DedupeKey, secret) ||
		acceptor.request.Prompt != acceptor.request.RequestID {
		t.Fatalf("credential entered normalized request: %+v", acceptor.request)
	}
	sum := sha256.Sum256([]byte(secret))
	if got, want := acceptor.request.DedupeKey, "webhook:events:"+hex.EncodeToString(sum[:]); got != want {
		t.Fatalf("default dedupe key = %q, want %q", got, want)
	}
	if got, want := acceptor.request.LegacyDedupeKey, "webhook:events:"+secret; got != want {
		t.Fatalf("legacy lookup key = %q, want %q", got, want)
	}
	prepared, err = ingress.PrepareTest(t.Context(), "events")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ingress.Admit(t.Context(), prepared, webhookcmd.Inbound{
		RequestID: "test-1", Method: "POST", Path: "/webhooks/events", Test: true,
	}); err != nil || acceptor.request.DedupeKey != "webhook-test:events:test-1" ||
		acceptor.request.LegacyDedupeKey != "" {
		t.Fatalf("test lookup keys = %+v err=%v", acceptor.request, err)
	}
}

func TestIngress_AuthRequestIDHonorsExplicitDedupeSource(t *testing.T) {
	const secret = "route-secret"
	bodySum := sha256.Sum256([]byte("body"))
	secretSum := sha256.Sum256([]byte(secret))
	for _, tc := range []struct {
		name, source, header, wantBase string
	}{
		{name: "header", source: webhookroutecmd.DedupeSourceHeader, header: "X-Event", wantBase: "event-42"},
		{name: "credential header", source: webhookroutecmd.DedupeSourceHeader,
			header: "X-Request-Id", wantBase: hex.EncodeToString(secretSum[:])},
		{name: "body", source: webhookroutecmd.DedupeSourceBodySHA, wantBase: hex.EncodeToString(bodySum[:])},
	} {
		t.Run(tc.name, func(t *testing.T) {
			acceptor := &ingressAcceptor{}
			ingress, err := NewIngress("", []ConfiguredRoute{{Name: "events",
				PromptTemplate: "constant prompt", AuthType: webhookroutecmd.AuthTypeHeader,
				AuthHeader: "X-Request-Id", AuthValue: secret,
				DedupeSource: tc.source, DedupeHeader: tc.header}}, nil, acceptor)
			if err != nil {
				t.Fatal(err)
			}
			headers := map[string]string{"X-Request-Id": secret, "X-Event": "event-42"}
			prepared, err := ingress.PrepareExternal(t.Context(), "/webhooks/events", headers)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ingress.Admit(t.Context(), prepared, webhookcmd.Inbound{
				RequestID: "safe-public-id", Method: "POST", Path: "/webhooks/events", RawBody: "body", Headers: headers,
			}); err != nil {
				t.Fatal(err)
			}
			if got, want := acceptor.request.DedupeKey, "webhook:events:"+tc.wantBase; got != want {
				t.Fatalf("dedupe key = %q, want %q", got, want)
			}
			if acceptor.request.LegacyDedupeKey != "" {
				t.Fatalf("explicit dedupe looked up legacy credential key: %+v", acceptor.request)
			}
		})
	}
}

func TestIngress_CanonicalPathsUseCurrentBasePath(t *testing.T) {
	for _, basePath := range []string{"", "/balda"} {
		t.Run(basePath, func(t *testing.T) {
			store := &ingressStore{route: webhookroutecmd.Record{Name: "managed", Source: webhookroutecmd.SourceManaged,
				PromptTemplate: "{{.RawBody}}", Enabled: true}}
			ingress, err := NewIngress(basePath, []ConfiguredRoute{{Name: "configured", PromptTemplate: "{{.RawBody}}"}}, store, &ingressAcceptor{})
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"configured", "managed"} {
				want := basePath + "/webhooks/" + name
				if active, err := ingress.IsActivePath(t.Context(), want); err != nil || !active {
					t.Fatalf("canonical route %q: active=%t err=%v", want, active, err)
				}
				prepared, err := ingress.PrepareTest(t.Context(), name)
				if err != nil || prepared.Path != want {
					t.Fatalf("prepared route %q: path=%q want=%q err=%v", name, prepared.Path, want, err)
				}
			}
			for _, path := range []string{basePath + "/webhooks/", basePath + "/webhooks/managed/extra", basePath + "/backoffice/managed", basePath + "/webhooks/missing"} {
				if _, err := ingress.PrepareExternal(t.Context(), path, nil); !errors.Is(err, webhookcmd.ErrRouteNotFound) {
					t.Fatalf("unmatched path %q: err=%v", path, err)
				}
			}
		})
	}
}

func TestIngressRechecksManagedEligibilityBeforeAuthentication(t *testing.T) {
	for _, basePath := range []string{"", "/balda"} {
		for _, archive := range []bool{false, true} {
			store := &ingressStore{route: webhookroutecmd.Record{Name: "orders", Source: webhookroutecmd.SourceManaged, Enabled: true, PromptTemplate: "{{.RawBody}}"}}
			acceptor := &ingressAcceptor{}
			ingress, err := NewIngress(basePath, nil, store, acceptor)
			if err != nil {
				t.Fatal(err)
			}
			path := webhookroutecmd.CanonicalPath(basePath, "orders")
			if active, err := ingress.IsActivePath(t.Context(), path); err != nil || !active {
				t.Fatalf("active=%v err=%v", active, err)
			}
			if archive {
				store.route.Deleted = true
			} else {
				store.route.Enabled = false
			}
			if _, err := ingress.PrepareExternal(t.Context(), path, nil); !errors.Is(err, webhookcmd.ErrRouteNotFound) {
				t.Fatalf("changed eligibility = %v, want not found", err)
			}
			if acceptor.request.RouteName != "" {
				t.Fatal("changed route admitted work")
			}
		}
	}
}
