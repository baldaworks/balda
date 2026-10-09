package webhook

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/balda/internal/apps/balda/webhookapp"
	"github.com/baldaworks/balda/internal/apps/balda/webhookcmd"
	"github.com/rs/zerolog"
)

type fakeAcceptor struct {
	lastReq webhookcmd.Request
	result  webhookcmd.Result
	err     error
}

type historyAcceptor struct {
	store state.WebhookAdmissionStore
	last  webhookcmd.Request
}

func (a *historyAcceptor) Accept(ctx context.Context, req webhookcmd.Request) (webhookcmd.Result, error) {
	a.last = req
	admission := webhookcmd.Admission{RouteName: req.RouteName, DedupeKey: req.DedupeKey,
		RequestID: req.RequestID, Prompt: req.Prompt, RawBody: &req.RawBody,
		Source: webhookcmd.SourceExternal, JobID: "webhook-history-secret",
		SessionID: "wh-history-secret", CreatedAt: time.Now().UTC()}
	if _, _, err := a.store.Create(ctx, admission); err != nil {
		return webhookcmd.Result{}, err
	}
	return webhookcmd.Result{RequestID: req.RequestID, JobID: admission.JobID, MessageID: "msg-1"}, nil
}

func TestReceiver_AuthRequestIDCannotEnterHistory(t *testing.T) {
	provider, err := state.NewSQLiteProvider(t.Context(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	const secret = "confidential-route-secret"
	acceptor := &historyAcceptor{store: provider.WebhookAdmissions()}
	configured := webhookapp.ConfiguredRoute{Name: "event", Path: "/event",
		PromptTemplate: "request={{.RequestID}}", AuthType: "header",
		AuthHeader: "X-Request-Id", AuthValue: secret}
	ingress, err := webhookapp.NewIngress([]webhookapp.ConfiguredRoute{configured}, nil, acceptor)
	if err != nil {
		t.Fatal(err)
	}
	receiver, err := NewReceiver(Config{Enabled: true, ListenAddr: "127.0.0.1:0",
		Routes: map[string]RouteConfig{"event": {Path: "/event", PromptTemplate: configured.PromptTemplate,
			Auth: RouteAuthConfig{Type: "header", Header: "X-Request-Id", Value: secret}}}},
		ingress, zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/event", strings.NewReader("ordinary input"))
	request.Header.Set("X-Request-Id", secret)
	response := httptest.NewRecorder()
	receiver.handleWebhook(response, request)
	if response.Code != http.StatusAccepted || strings.Contains(response.Body.String(), secret) ||
		strings.Contains(acceptor.last.RequestID, secret) || strings.Contains(acceptor.last.Prompt, secret) {
		t.Fatalf("credential entered response/admission: status=%d response=%q input=%+v",
			response.Code, response.Body.String(), acceptor.last)
	}
	history, found, err := provider.WebhookAdmissions().GetHistory(t.Context(), "event", "webhook-history-secret")
	if err != nil || !found || history.RawBody == nil || *history.RawBody != "ordinary input" {
		t.Fatalf("history = %+v found=%t err=%v", history, found, err)
	}
	encoded, err := json.Marshal(history)
	if err != nil || strings.Contains(string(encoded), secret) || strings.Contains(string(encoded), "RequestID") ||
		strings.Contains(string(encoded), "Prompt") {
		t.Fatalf("history exposed credential or legacy fields: %s err=%v", encoded, err)
	}
	denied := httptest.NewRequest(http.MethodPost, "/event", strings.NewReader("ordinary input"))
	denied.Header.Set("X-Request-Id", "wrong-credential")
	deniedResponse := httptest.NewRecorder()
	receiver.handleWebhook(deniedResponse, denied)
	if deniedResponse.Code != http.StatusUnauthorized || strings.Contains(deniedResponse.Body.String(), "wrong-credential") {
		t.Fatalf("auth rejection reflected credential: status=%d body=%q", deniedResponse.Code, deniedResponse.Body.String())
	}
}

func (f *fakeAcceptor) Accept(_ context.Context, req webhookcmd.Request) (webhookcmd.Result, error) {
	f.lastReq = req
	if f.err != nil {
		return webhookcmd.Result{}, f.err
	}
	if f.result.JobID == "" {
		f.result.JobID = "job-1"
	}
	if f.result.MessageID == "" {
		f.result.MessageID = "msg-1"
	}
	return f.result, nil
}

type fakeDeliveryReceipts struct {
	messageID string
	sent      bool
	err       error
}

func (f *fakeDeliveryReceipts) SentFinalDelivery(_ context.Context, _ string) (string, bool, error) {
	return f.messageID, f.sent, f.err
}

func testReceiver(t *testing.T, svc *fakeAcceptor, route webhookapp.ConfiguredRoute) *Receiver {
	t.Helper()
	routes := map[string]RouteConfig{}
	configured := []webhookapp.ConfiguredRoute{}
	if route.Name != "" {
		routes[route.Name] = RouteConfig{Path: route.Path, PromptTemplate: route.PromptTemplate,
			Envelope: RouteEnvelopeConfig{AckOnDelivery: route.AckOnDelivery},
			Auth:     RouteAuthConfig{Type: route.AuthType, Header: route.AuthHeader, Value: route.AuthValue},
			Dedupe:   RouteDedupeConfig{Source: route.DedupeSource, Header: route.DedupeHeader}}
		if route.ReportToKey != "" {
			cfg := routes[route.Name]
			cfg.Envelope.ReportTo = &RouteTargetConfig{Target: route.ReportToKind, Key: route.ReportToKey}
			routes[route.Name] = cfg
		}
		configured = append(configured, route)
	}
	ingress, err := webhookapp.NewIngress(configured, nil, svc)
	if err != nil {
		t.Fatal(err)
	}
	receiver, err := NewReceiver(Config{Enabled: true, ListenAddr: "127.0.0.1:0", Routes: routes}, ingress, zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	return receiver
}

func post(r *Receiver, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	rec := httptest.NewRecorder()
	r.handleWebhook(rec, req)
	return rec
}

func assertErrorResponse(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status = %d, want %d: %s", rec.Code, status, rec.Body.String())
	}
	var payload errorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Error.Code != code {
		t.Fatalf("error code = %q, want %q", payload.Error.Code, code)
	}
}

func TestReceiver_ConfigAuthenticationAndAdmission(t *testing.T) {
	svc := &fakeAcceptor{}
	route := webhookapp.ConfiguredRoute{Name: "event", Path: "/event", PromptTemplate: "{{.RawBody}}",
		AuthType: AuthTypeHeader, AuthHeader: "Authorization", AuthValue: "Bearer secret",
		ReportToKind: "managed_alias", ReportToKey: "main_chat", DedupeSource: DedupeSourceHeader,
		DedupeHeader: "X-Dedupe"}
	r := testReceiver(t, svc, route)
	assertErrorResponse(t, post(r, "/event", "body", nil), http.StatusUnauthorized, codeUnauthorized)
	rec := post(r, "/event", "body", map[string]string{
		"Authorization": "Bearer secret", "X-Dedupe": "same", "X-Request-Id": "req-1"})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if svc.lastReq.Prompt != "body" || svc.lastReq.DedupeKey != "webhook:event:same" ||
		svc.lastReq.ReportTo == nil || svc.lastReq.ReportTo.Key != "main_chat" {
		t.Fatalf("admission = %+v", svc.lastReq)
	}
}

func TestReceiver_HTTPErrorMapping(t *testing.T) {
	route := webhookapp.ConfiguredRoute{Name: "event", Path: "/event", PromptTemplate: "{{.RawBody}}"}
	for _, tc := range []struct {
		name       string
		path       string
		body       string
		method     string
		serviceErr error
		wantStatus int
		wantCode   string
	}{
		{name: "method", path: "/event", method: http.MethodGet, wantStatus: http.StatusMethodNotAllowed, wantCode: codeInvalidMethod},
		{name: "missing", path: "/missing", wantStatus: http.StatusNotFound, wantCode: codeRouteNotFound},
		{name: "oversized", path: "/event", body: strings.Repeat("x", MaxBodyBytes+1), wantStatus: http.StatusBadRequest, wantCode: codeInvalidPayload},
		{name: "target", path: "/event", body: "body", serviceErr: &webhookcmd.TargetNotFoundError{Cause: errors.New("missing")}, wantStatus: http.StatusNotFound, wantCode: codeDestinationNotFound},
		{name: "queue", path: "/event", body: "body", serviceErr: &webhookcmd.QueueFullError{Cause: errors.New("full")}, wantStatus: http.StatusTooManyRequests, wantCode: codeQueueFull},
		{name: "dispatch", path: "/event", body: "body", serviceErr: &webhookcmd.DispatchFailedError{Cause: errors.New("down")}, wantStatus: http.StatusServiceUnavailable, wantCode: codeDispatchFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := &fakeAcceptor{err: tc.serviceErr}
			r := testReceiver(t, svc, route)
			method := tc.method
			if method == "" {
				method = http.MethodPost
			}
			req := httptest.NewRequest(method, tc.path, strings.NewReader(tc.body))
			rec := httptest.NewRecorder()
			r.handleWebhook(rec, req)
			assertErrorResponse(t, rec, tc.wantStatus, tc.wantCode)
		})
	}
}

func TestReceiver_TemplateErrorAndPromptLimit(t *testing.T) {
	for _, prompt := range []string{"{{.Missing}}", "{{.RawBody}}{{.RawBody}}"} {
		svc := &fakeAcceptor{}
		r := testReceiver(t, svc, webhookapp.ConfiguredRoute{Name: "event", Path: "/event", PromptTemplate: prompt})
		body := "body"
		if strings.Contains(prompt, "RawBody") {
			body = strings.Repeat("x", MaxBodyBytes/2+1)
		}
		assertErrorResponse(t, post(r, "/event", body, nil), http.StatusBadRequest, codeInvalidPayload)
		if svc.lastReq.RequestID != "" {
			t.Fatal("invalid prompt was admitted")
		}
	}
}

func TestReceiver_AckOnDelivery(t *testing.T) {
	svc := &fakeAcceptor{}
	r := testReceiver(t, svc, webhookapp.ConfiguredRoute{Name: "event", Path: "/event",
		PromptTemplate: "{{.RawBody}}", ReportToKind: "managed_alias", ReportToKey: "main_chat", AckOnDelivery: true})
	receipts := &fakeDeliveryReceipts{}
	r.SetDeliveryReceipts(receipts)
	if rec := post(r, "/event", "body", nil); rec.Code != http.StatusAccepted {
		t.Fatalf("pending status = %d", rec.Code)
	}
	receipts.sent, receipts.messageID = true, "provider-1"
	rec := post(r, "/event", "body", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("delivered status = %d", rec.Code)
	}
	var response acceptedResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Status != statusDelivered || response.ProviderMessageID != "provider-1" {
		t.Fatalf("response = %+v", response)
	}
}

func TestNormalizeConfig_CurrentRouteContract(t *testing.T) {
	for _, tc := range []struct {
		name      string
		cfg       Config
		wantError bool
	}{
		{name: "no active routes", cfg: Config{Enabled: true}},
		{name: "optional report", cfg: Config{Enabled: true, Routes: map[string]RouteConfig{
			"event": {Path: "/event", PromptTemplate: "{{.RawBody}}"},
		}}},
		{name: "ack needs report", cfg: Config{Enabled: true, Routes: map[string]RouteConfig{
			"event": {Path: "/event", PromptTemplate: "body", Envelope: RouteEnvelopeConfig{AckOnDelivery: true}},
		}}, wantError: true},
		{name: "duplicate path", cfg: Config{Enabled: true, Routes: map[string]RouteConfig{
			"first": {Path: "/event", PromptTemplate: "a"}, "second": {Path: "/event", PromptTemplate: "b"},
		}}, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := normalizeConfig(tc.cfg)
			if (err != nil) != tc.wantError {
				t.Fatalf("normalizeConfig error = %v", err)
			}
		})
	}
}

func TestReceiver_StartBindsWithNoActiveRoutes(t *testing.T) {
	r := testReceiver(t, &fakeAcceptor{}, webhookapp.ConfiguredRoute{})
	if err := r.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Stop(context.Background()) })
	if r.listener == nil {
		t.Fatal("listener was not bound")
	}
}
