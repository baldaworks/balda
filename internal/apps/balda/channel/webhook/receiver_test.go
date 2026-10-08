package webhook

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"text/template"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/webhookcmd"
	"github.com/rs/zerolog"
)

type fakeService struct {
	lastReq webhookcmd.Request
	res     webhookcmd.Result
	err     error
}

type fakeDeliveryReceipts struct {
	messageID string
	sent      bool
	err       error
}

func (f *fakeDeliveryReceipts) SentFinalDelivery(_ context.Context, _ string) (string, bool, error) {
	return f.messageID, f.sent, f.err
}

func (f *fakeService) Accept(_ context.Context, req webhookcmd.Request) (webhookcmd.Result, error) {
	f.lastReq = req
	if f.err != nil {
		return webhookcmd.Result{}, f.err
	}
	res := f.res
	if res.MessageID == "" {
		res.MessageID = "msg-" + req.RequestID
	}
	if res.RequestID == "" {
		res.RequestID = req.RequestID
	}
	return res, nil
}

func newTestReceiver(svc Service) *Receiver {
	return &Receiver{
		enabled: true,
		routes: map[string]route{
			"/webhook1": {
				Name:           "webhook1",
				Path:           "/webhook1",
				PromptTemplate: template.Must(template.New("webhook1").Option("missingkey=error").Parse("{{.RawBody}}")),
				Auth:           authPolicy{Type: AuthTypeNone},
				Dedupe:         dedupePolicy{Source: DedupeSourceRequestID},
			},
		},
		service: svc,
		logger:  zerolog.Nop(),
	}
}

func TestNormalizeConfig_CurrentReportDestination(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name     string
		reportTo *RouteTargetConfig
	}{
		{name: "omitted"},
		{name: "managed alias", reportTo: &RouteTargetConfig{Target: "managed_alias", Key: "main_chat"}},
		{name: "literal locator", reportTo: &RouteTargetConfig{Target: "locator", Key: "telegram:-1003953132277:0"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeConfig(Config{Enabled: true, Routes: map[string]RouteConfig{"event": {
				Path: "/event", PromptTemplate: "{{.RawBody}}",
				Envelope: RouteEnvelopeConfig{ReportTo: tt.reportTo, AckOnDelivery: tt.reportTo != nil},
			}}})
			if err != nil {
				t.Fatal(err)
			}
			if got.Routes["/event"].AckOnDelivery != (tt.reportTo != nil) {
				t.Fatalf("ack_on_delivery = %t", got.Routes["/event"].AckOnDelivery)
			}
			svc := &fakeService{}
			receiver, err := NewReceiver(Config{Enabled: true, Routes: map[string]RouteConfig{"event": {
				Path: "/event", PromptTemplate: "{{.RawBody}}", Envelope: RouteEnvelopeConfig{ReportTo: tt.reportTo},
			}}}, svc, zerolog.Nop())
			if err != nil {
				t.Fatal(err)
			}
			response := httptest.NewRecorder()
			receiver.handleWebhook(response, httptest.NewRequest(http.MethodPost, "/event", strings.NewReader("input")))
			if response.Code != http.StatusAccepted {
				t.Fatalf("status = %d", response.Code)
			}
			if tt.reportTo == nil && svc.lastReq.ReportTo != nil || tt.reportTo != nil &&
				(svc.lastReq.ReportTo == nil || svc.lastReq.ReportTo.Target != tt.reportTo.Target || svc.lastReq.ReportTo.Key != tt.reportTo.Key) {
				t.Fatalf("report_to = %+v, want %+v", svc.lastReq.ReportTo, tt.reportTo)
			}
		})
	}
}

func TestReceiver_RoutesAuthenticatedReportReference(t *testing.T) {
	svc := &fakeService{}
	cfg := Config{Enabled: true, Routes: map[string]RouteConfig{"execution": {
		Path: "/execution", PromptTemplate: "event={{.RawBody}}",
		Envelope: RouteEnvelopeConfig{ReportTo: &RouteTargetConfig{Target: "managed_alias", Key: "main_chat"}},
		Auth:     RouteAuthConfig{Type: AuthTypeHeader, Header: "Authorization", Value: "Bearer test"},
	}}}
	receiver, err := NewReceiver(cfg, svc, zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/execution", strings.NewReader(`{"event":"update"}`))
	req.Header.Set("Authorization", "Bearer test")
	rec := httptest.NewRecorder()
	receiver.handleWebhook(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if svc.lastReq.Prompt != `event={"event":"update"}` || svc.lastReq.ReportTo == nil || svc.lastReq.ReportTo.Key != "main_chat" {
		t.Fatalf("normalized request = %+v", svc.lastReq)
	}
}

func TestReceiver_AckOnDelivery(t *testing.T) {
	service := &fakeService{res: webhookcmd.Result{JobID: "job-1"}}
	receiver := newTestReceiver(service)
	route := receiver.routes["/webhook1"]
	route.AckOnDelivery = true
	receiver.routes["/webhook1"] = route
	receipts := &fakeDeliveryReceipts{}
	receiver.SetDeliveryReceipts(receipts)
	post := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		receiver.handleWebhook(rec, httptest.NewRequest(http.MethodPost, "/webhook1", strings.NewReader("event")))
		return rec
	}
	if rec := post(); rec.Code != http.StatusAccepted {
		t.Fatalf("pending status = %d: %s", rec.Code, rec.Body.String())
	}
	receipts.sent = true
	receipts.messageID = "mm-post-1"
	rec := post()
	if rec.Code != http.StatusOK {
		t.Fatalf("delivered status = %d: %s", rec.Code, rec.Body.String())
	}
	var response acceptedResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Status != statusDelivered || response.JobID != "job-1" || response.ProviderMessageID != "mm-post-1" {
		t.Fatalf("response = %+v", response)
	}
}

func TestNormalizeConfig_AckRequiresReportTo(t *testing.T) {
	_, err := normalizeConfig(Config{Enabled: true, Routes: map[string]RouteConfig{"r": {
		Path: "/r", PromptTemplate: "{{.RawBody}}", Envelope: RouteEnvelopeConfig{AckOnDelivery: true},
	}}})
	if err == nil || !strings.Contains(err.Error(), "ack_on_delivery requires report_to") {
		t.Fatalf("expected ack validation, got %v", err)
	}
}

func assertErrorResponse(t *testing.T, rec *httptest.ResponseRecorder, wantStatus int, wantCode, wantMessage string) {
	t.Helper()

	if got := rec.Code; got != wantStatus {
		t.Fatalf("status = %d, want %d", got, wantStatus)
	}
	var payload errorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if got := payload.Status; got != statusError {
		t.Fatalf("status body = %q, want %q", got, statusError)
	}
	if got := payload.Error.Code; got != wantCode {
		t.Fatalf("error.code = %q, want %q", got, wantCode)
	}
	if got := payload.Error.Message; got != wantMessage {
		t.Fatalf("error.message = %q, want %q", got, wantMessage)
	}
}

func TestNormalizeConfig(t *testing.T) {
	t.Parallel()

	t.Run("requires_routes_when_enabled", func(t *testing.T) {
		_, err := normalizeConfig(Config{Enabled: true})
		if err == nil || !strings.Contains(err.Error(), "balda.webhooks.routes is required") {
			t.Fatalf("expected error about missing routes, got %v", err)
		}
	})

	t.Run("allows_route_without_report_to", func(t *testing.T) {
		got, err := normalizeConfig(Config{
			Enabled: true,
			Routes: map[string]RouteConfig{
				"w1": {
					Path:           "/w1",
					PromptTemplate: "{{.RawBody}}",
				},
			},
		})
		if err != nil {
			t.Fatalf("normalizeConfig error = %v", err)
		}
		rt, ok := got.Routes["/w1"]
		if !ok {
			t.Fatal("route /w1 missing")
		}
		if rt.ReportTo != nil {
			t.Fatalf("unexpected report destination %+v", rt.ReportTo)
		}
		if rt.Auth.Type != AuthTypeNone {
			t.Fatalf("auth type = %q, want none", rt.Auth.Type)
		}
		if rt.Dedupe.Source != DedupeSourceRequestID {
			t.Fatalf("dedupe source = %q, want request_id", rt.Dedupe.Source)
		}
	})

	t.Run("allows_locator_report", func(t *testing.T) {
		got, err := normalizeConfig(Config{
			Enabled: true,
			Routes: map[string]RouteConfig{
				"w1": {
					Path:           "/w1",
					PromptTemplate: "{{.RawBody}}",
					Envelope: RouteEnvelopeConfig{
						ReportTo: &RouteTargetConfig{
							Target: "locator",
							Key:    "telegram:300:400",
						},
					},
				},
			},
		})
		if err != nil {
			t.Fatalf("normalizeConfig error = %v", err)
		}
		rt := got.Routes["/w1"]
		if rt.ReportTo == nil || rt.ReportTo.Key != "telegram:300:400" {
			t.Fatalf("report_to = %+v", rt.ReportTo)
		}
	})

	t.Run("rejects_duplicate_paths", func(t *testing.T) {
		_, err := normalizeConfig(Config{
			Enabled: true,
			Routes: map[string]RouteConfig{
				"w1": {Path: "/dup", PromptTemplate: "a"},
				"w2": {Path: "/dup", PromptTemplate: "b"},
			},
		})
		if err == nil || !strings.Contains(err.Error(), "duplicates route") {
			t.Fatalf("expected duplicate path error, got %v", err)
		}
	})

	t.Run("rejects_invalid_auth", func(t *testing.T) {
		_, err := normalizeConfig(Config{
			Enabled: true,
			Routes: map[string]RouteConfig{
				"w1": {
					Path:           "/w1",
					PromptTemplate: "a",
					Auth:           RouteAuthConfig{Type: AuthTypeHeader},
				},
			},
		})
		if err == nil || !strings.Contains(err.Error(), "header is required") {
			t.Fatalf("expected header required error, got %v", err)
		}
	})

	t.Run("rejects_invalid_dedupe", func(t *testing.T) {
		_, err := normalizeConfig(Config{
			Enabled: true,
			Routes: map[string]RouteConfig{
				"w1": {
					Path:           "/w1",
					PromptTemplate: "a",
					Dedupe:         RouteDedupeConfig{Source: DedupeSourceHeader},
				},
			},
		})
		if err == nil || !strings.Contains(err.Error(), "header is required") {
			t.Fatalf("expected dedupe header required error, got %v", err)
		}
	})
}

func TestReceiver_HTTPHandling(t *testing.T) {
	t.Parallel()

	t.Run("invalid_method", func(t *testing.T) {
		r := newTestReceiver(&fakeService{})
		req := httptest.NewRequest(http.MethodGet, "/webhook1", nil)
		rec := httptest.NewRecorder()

		r.handleWebhook(rec, req)

		assertErrorResponse(t, rec, http.StatusMethodNotAllowed, codeInvalidMethod, messageCouldNotAccept)
	})

	t.Run("route_not_found", func(t *testing.T) {
		r := newTestReceiver(&fakeService{})
		req := httptest.NewRequest(http.MethodPost, "/missing", bytes.NewBufferString("payload"))
		rec := httptest.NewRecorder()

		r.handleWebhook(rec, req)

		assertErrorResponse(t, rec, http.StatusNotFound, codeRouteNotFound, messageCouldNotAccept)
	})

	t.Run("unauthorized", func(t *testing.T) {
		r := newTestReceiver(&fakeService{})
		r.routes["/webhook1"] = route{
			Name:           "webhook1",
			Path:           "/webhook1",
			PromptTemplate: template.Must(template.New("w").Parse("{{.RawBody}}")),
			Auth: authPolicy{
				Type:   AuthTypeHeader,
				Header: "X-Secret",
				Value:  "expected-token",
			},
		}

		req := httptest.NewRequest(http.MethodPost, "/webhook1", bytes.NewBufferString("body"))
		rec := httptest.NewRecorder()

		r.handleWebhook(rec, req)

		assertErrorResponse(t, rec, http.StatusUnauthorized, codeUnauthorized, messageCouldNotAccept)

		// With valid auth
		reqValid := httptest.NewRequest(http.MethodPost, "/webhook1", bytes.NewBufferString("body"))
		reqValid.Header.Set("X-Secret", "expected-token")
		recValid := httptest.NewRecorder()

		r.handleWebhook(recValid, reqValid)
		if recValid.Code != http.StatusAccepted {
			t.Fatalf("valid auth status = %d, want %d", recValid.Code, http.StatusAccepted)
		}
	})

	t.Run("template_render_error", func(t *testing.T) {
		r := newTestReceiver(&fakeService{})
		r.routes["/webhook1"] = route{
			Name:           "webhook1",
			Path:           "/webhook1",
			PromptTemplate: template.Must(template.New("w").Option("missingkey=error").Parse("{{.MissingField}}")),
		}

		req := httptest.NewRequest(http.MethodPost, "/webhook1", bytes.NewBufferString("body"))
		rec := httptest.NewRecorder()

		r.handleWebhook(rec, req)

		assertErrorResponse(t, rec, http.StatusBadRequest, codeInvalidPayload, messageCouldNotAccept)
	})

	t.Run("empty_prompt", func(t *testing.T) {
		r := newTestReceiver(&fakeService{})
		r.routes["/webhook1"] = route{
			Name:           "webhook1",
			Path:           "/webhook1",
			PromptTemplate: template.Must(template.New("w").Parse("   ")),
		}

		req := httptest.NewRequest(http.MethodPost, "/webhook1", bytes.NewBufferString("body"))
		rec := httptest.NewRecorder()

		r.handleWebhook(rec, req)

		assertErrorResponse(t, rec, http.StatusBadRequest, codeInvalidPayload, messageCouldNotAccept)
	})

	t.Run("body_exceeds_max_bytes", func(t *testing.T) {
		r := newTestReceiver(&fakeService{})
		bigBody := strings.Repeat("x", MaxBodyBytes+10)
		req := httptest.NewRequest(http.MethodPost, "/webhook1", strings.NewReader(bigBody))
		rec := httptest.NewRecorder()

		r.handleWebhook(rec, req)

		assertErrorResponse(t, rec, http.StatusBadRequest, codeInvalidPayload, messageCouldNotAccept)
	})

	t.Run("rendered_prompt_exceeds_max_bytes", func(t *testing.T) {
		svc := &fakeService{}
		r := newTestReceiver(svc)
		r.routes["/webhook1"] = route{
			Name: "webhook1", Path: "/webhook1",
			PromptTemplate: template.Must(template.New("w").Parse("{{.RawBody}}{{.RawBody}}")),
		}
		req := httptest.NewRequest(http.MethodPost, "/webhook1", strings.NewReader(strings.Repeat("x", MaxBodyBytes/2+1)))
		rec := httptest.NewRecorder()
		r.handleWebhook(rec, req)
		assertErrorResponse(t, rec, http.StatusBadRequest, codeInvalidPayload, messageCouldNotAccept)
		if svc.lastReq.RequestID != "" {
			t.Fatal("oversized rendered prompt reached application service")
		}
	})

	t.Run("destination_not_found_mapped_to_404", func(t *testing.T) {
		svc := &fakeService{err: &webhookcmd.TargetNotFoundError{Cause: errors.New("target not found")}}
		r := newTestReceiver(svc)

		req := httptest.NewRequest(http.MethodPost, "/webhook1", bytes.NewBufferString("body"))
		rec := httptest.NewRecorder()

		r.handleWebhook(rec, req)

		assertErrorResponse(t, rec, http.StatusNotFound, codeDestinationNotFound, messageCouldNotAccept)
	})

	t.Run("queue_full_mapped_to_429", func(t *testing.T) {
		svc := &fakeService{err: &webhookcmd.QueueFullError{Cause: errors.New("command queue is full")}}
		r := newTestReceiver(svc)

		req := httptest.NewRequest(http.MethodPost, "/webhook1", bytes.NewBufferString("body"))
		rec := httptest.NewRecorder()

		r.handleWebhook(rec, req)

		assertErrorResponse(t, rec, http.StatusTooManyRequests, codeQueueFull, messageTemporarilyBusy)
	})

	t.Run("dispatch_failed_mapped_to_503", func(t *testing.T) {
		svc := &fakeService{err: &webhookcmd.DispatchFailedError{Cause: errors.New("dispatch failure")}}
		r := newTestReceiver(svc)

		req := httptest.NewRequest(http.MethodPost, "/webhook1", bytes.NewBufferString("body"))
		rec := httptest.NewRecorder()

		r.handleWebhook(rec, req)

		assertErrorResponse(t, rec, http.StatusServiceUnavailable, codeDispatchFailed, messageTemporarilyBusy)
	})

	t.Run("accepted_success", func(t *testing.T) {
		svc := &fakeService{}
		r := newTestReceiver(svc)

		req := httptest.NewRequest(http.MethodPost, "/webhook1", bytes.NewBufferString(`{"event":"test"}`))
		req.Header.Set("X-Request-Id", "custom-req-id")
		rec := httptest.NewRecorder()

		r.handleWebhook(rec, req)

		if rec.Code != http.StatusAccepted {
			t.Fatalf("code = %d, want 202", rec.Code)
		}
		var resp acceptedResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("json unmarshal failed: %v", err)
		}
		if resp.Status != statusAccepted || !resp.Accepted || resp.RequestID != "custom-req-id" {
			t.Fatalf("unexpected response %+v", resp)
		}
		if svc.lastReq.RequestID != "custom-req-id" || svc.lastReq.Prompt != `{"event":"test"}` {
			t.Fatalf("unexpected svc request: %+v", svc.lastReq)
		}
	})

	t.Run("dedupe_header_and_sha", func(t *testing.T) {
		svc := &fakeService{}
		r := newTestReceiver(svc)
		r.routes["/header"] = route{
			Name:           "hr",
			Path:           "/header",
			PromptTemplate: template.Must(template.New("h").Parse("{{.RawBody}}")),
			Dedupe:         dedupePolicy{Source: DedupeSourceHeader, Header: "X-Custom-Dedupe"},
		}
		r.routes["/sha"] = route{
			Name:           "sr",
			Path:           "/sha",
			PromptTemplate: template.Must(template.New("s").Parse("{{.RawBody}}")),
			Dedupe:         dedupePolicy{Source: DedupeSourceBodySHA},
		}

		// Header dedupe
		reqHeader := httptest.NewRequest(http.MethodPost, "/header", bytes.NewBufferString("hi"))
		reqHeader.Header.Set("X-Custom-Dedupe", "dedupe-val")
		r.handleWebhook(httptest.NewRecorder(), reqHeader)
		if svc.lastReq.DedupeKey != "webhook:hr:dedupe-val" {
			t.Errorf("dedupeKey = %q, want webhook:hr:dedupe-val", svc.lastReq.DedupeKey)
		}

		// SHA dedupe
		reqSHA := httptest.NewRequest(http.MethodPost, "/sha", bytes.NewBufferString("hello"))
		r.handleWebhook(httptest.NewRecorder(), reqSHA)
		if !strings.HasPrefix(svc.lastReq.DedupeKey, "webhook:sr:") {
			t.Errorf("dedupeKey = %q, want webhook:sr:<sha>", svc.lastReq.DedupeKey)
		}
	})
}

func TestReceiver_Lifecycle(t *testing.T) {
	t.Parallel()

	cfg := Config{
		Enabled:    true,
		ListenAddr: "127.0.0.1:0",
		Routes: map[string]RouteConfig{
			"w1": {Path: "/w1", PromptTemplate: "body"},
		},
	}
	r, err := NewReceiver(cfg, &fakeService{}, zerolog.Nop())
	if err != nil {
		t.Fatalf("NewReceiver failed: %v", err)
	}

	if err := r.Start(context.Background()); err != nil {
		t.Fatalf("Start() failed: %v", err)
	}

	routes := r.RoutePaths()
	if len(routes) != 1 || routes[0] != "/w1" {
		t.Fatalf("RoutePaths() = %v, want [/w1]", routes)
	}

	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := r.Stop(stopCtx); err != nil {
		t.Fatalf("Stop() failed: %v", err)
	}
}
