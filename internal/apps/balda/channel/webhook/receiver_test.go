package webhook

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
	"github.com/baldaworks/balda/internal/apps/balda/webhookapp"
	"github.com/baldaworks/balda/internal/apps/balda/webhookcmd"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/rs/zerolog"
)

type fakeAcceptor struct {
	lastReq webhookcmd.Request
	result  webhookcmd.Result
	err     error
}

type historyAcceptor struct {
	store      state.WebhookAdmissionStore
	jobs       state.JobStore
	last       webhookcmd.Request
	created    int
	firstJobID string
}

func (a *historyAcceptor) Accept(ctx context.Context, req webhookcmd.Request) (webhookcmd.Result, error) {
	a.last = req
	admission := webhookcmd.Admission{RouteName: req.RouteName, DedupeKey: req.DedupeKey,
		RequestID: req.RequestID, Prompt: req.Prompt, RawBody: &req.RawBody,
		Source: webhookcmd.SourceExternal, JobID: "webhook-" + req.RequestID,
		SessionID: "wh-" + req.RequestID, CreatedAt: time.Now().UTC()}
	selected, created, err := a.store.Create(ctx, admission)
	if err != nil {
		return webhookcmd.Result{}, err
	}
	if created {
		a.created++
		if a.firstJobID == "" {
			a.firstJobID = selected.JobID
		}
		if _, err := a.jobs.CreateJob(ctx, state.JobRecord{ID: selected.JobID,
			SessionID: selected.SessionID, Objective: selected.Prompt,
			Status: state.JobStatusCreated, PrivateRunKind: state.PrivateRunKindWebhook}); err != nil {
			return webhookcmd.Result{}, err
		}
	}
	return webhookcmd.Result{RequestID: selected.RequestID, JobID: selected.JobID,
		MessageID: "msg-1", Duplicate: !created}, nil
}

func TestReceiver_AuthRequestIDCannotEnterHistory(t *testing.T) {
	provider, err := state.NewSQLiteProvider(t.Context(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	const secret = "confidential-route-secret"
	acceptor := &historyAcceptor{store: provider.WebhookAdmissions(), jobs: provider.Jobs()}
	configured := webhookapp.ConfiguredRoute{Name: "event",
		PromptTemplate: "request={{.RequestID}}", AuthType: "header",
		AuthHeader: "X-Request-Id", AuthValue: secret}
	ingress, err := webhookapp.NewIngress("", []webhookapp.ConfiguredRoute{configured}, nil, acceptor)
	if err != nil {
		t.Fatal(err)
	}
	receiver, err := NewReceiver(Config{Enabled: true, ListenAddr: "127.0.0.1:0",
		Routes: map[string]RouteConfig{"event": {PromptTemplate: configured.PromptTemplate,
			Auth: RouteAuthConfig{Type: "header", Header: "X-Request-Id", Value: secret}}}},
		ingress, zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/webhooks/event", strings.NewReader("ordinary input"))
	request.Header.Set("X-Request-Id", secret)
	response := httptest.NewRecorder()
	receiver.handleWebhook(response, request)
	if response.Code != http.StatusAccepted || strings.Contains(response.Body.String(), secret) ||
		strings.Contains(acceptor.last.RequestID, secret) || strings.Contains(acceptor.last.Prompt, secret) {
		t.Fatalf("credential entered response/admission: status=%d response=%q input=%+v",
			response.Code, response.Body.String(), acceptor.last)
	}
	firstRequestID := acceptor.last.RequestID
	firstDedupeKey := acceptor.last.DedupeKey
	retry := httptest.NewRequest(http.MethodPost, "/webhooks/event", strings.NewReader("ordinary input"))
	retry.Header.Set("X-Request-Id", secret)
	retryResponse := httptest.NewRecorder()
	receiver.handleWebhook(retryResponse, retry)
	if retryResponse.Code != http.StatusAccepted || !strings.Contains(retryResponse.Body.String(), `"duplicate":true`) ||
		strings.Contains(retryResponse.Body.String(), secret) || acceptor.last.RequestID == firstRequestID ||
		acceptor.last.DedupeKey != firstDedupeKey || strings.Contains(firstDedupeKey, secret) || acceptor.created != 1 {
		t.Fatalf("retry created another job or exposed credential: first=%q retry=%q acceptor=%+v",
			response.Body.String(), retryResponse.Body.String(), acceptor)
	}
	history, found, err := provider.WebhookAdmissions().GetHistory(t.Context(), "event", acceptor.firstJobID)
	if err != nil || !found || history.RawBody == nil || *history.RawBody != "ordinary input" {
		t.Fatalf("history = %+v found=%t err=%v", history, found, err)
	}
	list, err := provider.WebhookAdmissions().ListHistory(t.Context(), "event", time.Time{}, "", 10)
	if err != nil || len(list) != 1 {
		t.Fatalf("retry history = %+v err=%v", list, err)
	}
	if job, found, err := provider.Jobs().GetJob(t.Context(), acceptor.firstJobID); err != nil || !found || job.ID != acceptor.firstJobID {
		t.Fatalf("retry job = %+v found=%t err=%v", job, found, err)
	}
	encoded, err := json.Marshal(history)
	if err != nil || strings.Contains(string(encoded), secret) || strings.Contains(string(encoded), "RequestID") ||
		strings.Contains(string(encoded), "Prompt") {
		t.Fatalf("history exposed credential or legacy fields: %s err=%v", encoded, err)
	}
	denied := httptest.NewRequest(http.MethodPost, "/webhooks/event", strings.NewReader("ordinary input"))
	denied.Header.Set("X-Request-Id", "wrong-credential")
	deniedResponse := httptest.NewRecorder()
	receiver.handleWebhook(deniedResponse, denied)
	if deniedResponse.Code != http.StatusUnauthorized || strings.Contains(deniedResponse.Body.String(), "wrong-credential") {
		t.Fatalf("auth rejection reflected credential: status=%d body=%q", deniedResponse.Code, deniedResponse.Body.String())
	}
}

func TestReceiver_LegacyCredentialDedupePreservesDeliveredAdmission(t *testing.T) {
	provider, err := state.NewSQLiteProvider(t.Context(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	const secret = "pre-upgrade-credential"
	const routeName = "event"
	const jobID = "webhook-legacy"
	legacyKey := "webhook:" + routeName + ":" + secret
	legacy := webhookcmd.Admission{RouteName: routeName, DedupeKey: legacyKey,
		RequestID: secret, Prompt: "legacy request=" + secret, JobID: jobID,
		SessionID: "wh-legacy", CreatedAt: time.Now().UTC()}
	if _, created, err := provider.WebhookAdmissions().Create(t.Context(), legacy); err != nil || !created {
		t.Fatalf("seed admission: created=%t err=%v", created, err)
	}
	if _, err := provider.WebhookAdmissions().RecordReceipt(t.Context(), routeName, legacyKey,
		webhookcmd.Receipt{MessageID: "original-receipt", Stream: "balda.cmd.job", Sequence: 7}); err != nil {
		t.Fatalf("seed receipt: %v", err)
	}
	if created, err := provider.Jobs().CreateJob(t.Context(), state.JobRecord{ID: jobID,
		SessionID: legacy.SessionID, Objective: legacy.Prompt, Status: state.JobStatusCompleted,
		PrivateRunKind: state.PrivateRunKindWebhook}); err != nil || !created {
		t.Fatalf("seed job: created=%t err=%v", created, err)
	}
	if _, created, err := provider.Jobs().ReserveDelivery(t.Context(), state.DeliveryRecord{
		ID: "legacy-delivery", DeliveryKey: jobID + ":delivery:final", JobID: jobID,
		SessionID: legacy.SessionID, Channel: "telegram", AddressKey: "123:0",
		Kind: "delivery", Payload: "report sent", PayloadHash: "report-hash",
	}); err != nil || !created {
		t.Fatalf("seed delivery: created=%t err=%v", created, err)
	}
	published := 0
	publisher := webhookapp.JobPublisherFunc(func(_ context.Context, _ turncmd.SessionTurnPayload, _, _ string) (*actortransport.DispatchReceipt, string, error) {
		published++
		return nil, "", errors.New("legacy admission must not be republished")
	})
	service := webhookapp.NewService(nil, provider.WebhookAdmissions(), publisher)
	configured := webhookapp.ConfiguredRoute{Name: routeName,
		PromptTemplate: "request={{.RequestID}}", AuthType: "header",
		AuthHeader: "X-Request-Id", AuthValue: secret, AckOnDelivery: true,
		ReportToKind: "managed_alias", ReportToKey: "main_chat"}
	ingress, err := webhookapp.NewIngress("", []webhookapp.ConfiguredRoute{configured}, nil, service)
	if err != nil {
		t.Fatal(err)
	}
	receiver, err := NewReceiver(Config{Enabled: true, ListenAddr: "127.0.0.1:0",
		Routes: map[string]RouteConfig{routeName: {PromptTemplate: configured.PromptTemplate,
			Envelope: RouteEnvelopeConfig{AckOnDelivery: true,
				ReportTo: &RouteTargetConfig{Target: "managed_alias", Key: "main_chat"}},
			Auth: RouteAuthConfig{Type: "header", Header: "X-Request-Id", Value: secret}}}},
		ingress, zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	receiver.SetDeliveryReceipts(provider.Jobs())
	previousRequestID := ""
	for attempt := range 2 {
		response := post(receiver, "/webhooks/event", "retry body", map[string]string{"X-Request-Id": secret})
		wantStatus, wantState, wantProviderID := http.StatusAccepted, statusAccepted, ""
		if attempt == 1 {
			wantStatus, wantState, wantProviderID = http.StatusOK, statusDelivered, "provider-message"
		}
		if response.Code != wantStatus {
			t.Fatalf("attempt %d status=%d body=%q", attempt, response.Code, response.Body.String())
		}
		var got acceptedResponse
		if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got.JobID != jobID || got.MessageID != "original-receipt" ||
			got.Status != wantState || got.ProviderMessageID != wantProviderID ||
			!got.Duplicate || got.RequestID == "" || got.RequestID == previousRequestID ||
			strings.Contains(response.Body.String(), secret) {
			t.Fatalf("attempt %d changed admission or disclosed credential: %+v", attempt, got)
		}
		previousRequestID = got.RequestID
		if attempt == 0 {
			if err := provider.Jobs().MarkDeliverySent(t.Context(), jobID+":delivery:final", "provider-message"); err != nil {
				t.Fatalf("mark delivery sent: %v", err)
			}
		}
	}
	serviceResult, err := service.Accept(t.Context(), webhookcmd.Request{RequestID: "safe-direct-id",
		RouteName: routeName, Prompt: "new prompt", DedupeKey: "webhook:" + routeName + ":new",
		LegacyDedupeKey: legacyKey})
	if err != nil || serviceResult.RequestID != "safe-direct-id" || serviceResult.JobID != jobID ||
		serviceResult.MessageID != "original-receipt" || !serviceResult.Duplicate {
		t.Fatalf("legacy service result: %+v err=%v", serviceResult, err)
	}
	if published != 0 {
		t.Fatalf("legacy admission was published %d times", published)
	}
	sum := sha256.Sum256([]byte(secret))
	if _, found, err := provider.WebhookAdmissions().Get(t.Context(), routeName,
		"webhook:"+routeName+":"+hex.EncodeToString(sum[:])); err != nil || found {
		t.Fatalf("new admission: found=%t err=%v", found, err)
	}
	history, err := provider.WebhookAdmissions().ListHistory(t.Context(), routeName, time.Time{}, "", 10)
	if err != nil || len(history) != 1 || history[0].JobID != jobID || history[0].RawBody != nil {
		t.Fatalf("legacy history: %+v err=%v", history, err)
	}
	encoded, err := json.Marshal(history)
	if err != nil || strings.Contains(string(encoded), secret) {
		t.Fatalf("legacy history disclosed credential: %s err=%v", encoded, err)
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
		routes[route.Name] = RouteConfig{PromptTemplate: route.PromptTemplate,
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
	ingress, err := webhookapp.NewIngress("", configured, nil, svc)
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
	route := webhookapp.ConfiguredRoute{Name: "event", PromptTemplate: "{{.RawBody}}",
		AuthType: AuthTypeHeader, AuthHeader: "Authorization", AuthValue: "Bearer secret",
		ReportToKind: "managed_alias", ReportToKey: "main_chat", DedupeSource: DedupeSourceHeader,
		DedupeHeader: "X-Dedupe"}
	r := testReceiver(t, svc, route)
	assertErrorResponse(t, post(r, "/webhooks/event", "body", nil), http.StatusUnauthorized, codeUnauthorized)
	rec := post(r, "/webhooks/event", "body", map[string]string{
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
	route := webhookapp.ConfiguredRoute{Name: "event", PromptTemplate: "{{.RawBody}}"}
	for _, tc := range []struct {
		name       string
		path       string
		body       string
		method     string
		serviceErr error
		wantStatus int
		wantCode   string
	}{
		{name: "method", path: "/webhooks/event", method: http.MethodGet, wantStatus: http.StatusMethodNotAllowed, wantCode: codeInvalidMethod},
		{name: "missing", path: "/webhooks/missing", wantStatus: http.StatusNotFound, wantCode: codeRouteNotFound},
		{name: "oversized", path: "/webhooks/event", body: strings.Repeat("x", MaxBodyBytes+1), wantStatus: http.StatusBadRequest, wantCode: codeInvalidPayload},
		{name: "target", path: "/webhooks/event", body: "body", serviceErr: &webhookcmd.TargetNotFoundError{Cause: errors.New("missing")}, wantStatus: http.StatusNotFound, wantCode: codeDestinationNotFound},
		{name: "queue", path: "/webhooks/event", body: "body", serviceErr: &webhookcmd.QueueFullError{Cause: errors.New("full")}, wantStatus: http.StatusTooManyRequests, wantCode: codeQueueFull},
		{name: "dispatch", path: "/webhooks/event", body: "body", serviceErr: &webhookcmd.DispatchFailedError{Cause: errors.New("down")}, wantStatus: http.StatusServiceUnavailable, wantCode: codeDispatchFailed},
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
		r := testReceiver(t, svc, webhookapp.ConfiguredRoute{Name: "event", PromptTemplate: prompt})
		body := "body"
		if strings.Contains(prompt, "RawBody") {
			body = strings.Repeat("x", MaxBodyBytes/2+1)
		}
		assertErrorResponse(t, post(r, "/webhooks/event", body, nil), http.StatusBadRequest, codeInvalidPayload)
		if svc.lastReq.RequestID != "" {
			t.Fatal("invalid prompt was admitted")
		}
	}
}

func TestReceiver_AckOnDelivery(t *testing.T) {
	svc := &fakeAcceptor{}
	r := testReceiver(t, svc, webhookapp.ConfiguredRoute{Name: "event",
		PromptTemplate: "{{.RawBody}}", ReportToKind: "managed_alias", ReportToKey: "main_chat", AckOnDelivery: true})
	receipts := &fakeDeliveryReceipts{}
	r.SetDeliveryReceipts(receipts)
	if rec := post(r, "/webhooks/event", "body", nil); rec.Code != http.StatusAccepted {
		t.Fatalf("pending status = %d", rec.Code)
	}
	receipts.sent, receipts.messageID = true, "provider-1"
	rec := post(r, "/webhooks/event", "body", nil)
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
			"event": {PromptTemplate: "{{.RawBody}}"},
		}}},
		{name: "ack needs report", cfg: Config{Enabled: true, Routes: map[string]RouteConfig{
			"event": {PromptTemplate: "body", Envelope: RouteEnvelopeConfig{AckOnDelivery: true}},
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
