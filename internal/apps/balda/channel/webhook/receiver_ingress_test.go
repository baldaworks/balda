package webhook

import (
	"context"
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

type ingressProbe struct {
	admissions int
	prepareErr error
	prepared   webhookcmd.PreparedRoute
	input      webhookcmd.Inbound
}

type deadlineRecorder struct {
	*httptest.ResponseRecorder
	readDeadline time.Time
}

func (r *deadlineRecorder) SetReadDeadline(deadline time.Time) error {
	r.readDeadline = deadline
	return nil
}

func (p *ingressProbe) PrepareExternal(context.Context, string, map[string]string) (webhookcmd.PreparedRoute, error) {
	return p.prepared, p.prepareErr
}

func (p *ingressProbe) Admit(_ context.Context, _ webhookcmd.PreparedRoute, input webhookcmd.Inbound) (webhookcmd.Result, error) {
	p.admissions++
	p.input = input
	return webhookcmd.Result{RequestID: input.RequestID, MessageID: "msg-1"}, nil
}

type countedBody struct{ reads int }

func (b *countedBody) Read([]byte) (int, error) {
	b.reads++
	return 0, errors.New("body read before route authorization")
}
func (b *countedBody) Close() error { return nil }

func TestReceiver_AuthorizesBeforeReadingBody(t *testing.T) {
	for _, tt := range []struct {
		name   string
		err    error
		status int
	}{
		{"unauthorized", webhookcmd.ErrUnauthorized, http.StatusUnauthorized},
		{"unavailable", &webhookcmd.DispatchFailedError{Cause: errors.New("store unavailable")}, http.StatusServiceUnavailable},
		{"unknown", webhookcmd.ErrRouteNotFound, http.StatusNotFound},
	} {
		t.Run(tt.name, func(t *testing.T) {
			probe := &ingressProbe{prepareErr: tt.err}
			receiver, err := NewReceiver(Config{}, probe, zerolog.Nop())
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodPost, "/webhooks/managed", nil)
			body := &countedBody{}
			req.Body = body
			response := httptest.NewRecorder()
			receiver.handleWebhook(response, req)
			if response.Code != tt.status || body.reads != 0 || probe.admissions != 0 {
				t.Fatalf("status=%d reads=%d admissions=%d; want %d,0,0", response.Code, body.reads, probe.admissions, tt.status)
			}
		})
	}
}

func TestReceiver_PassesBoundedBodyToIngress(t *testing.T) {
	const body = "body"
	probe := &ingressProbe{prepared: webhookcmd.PreparedRoute{
		Name: "managed", Path: "/webhooks/managed", PromptTemplate: template.Must(template.New("x").Parse("{{.RawBody}}")),
	}}
	r, err := NewReceiver(Config{ListenAddr: "127.0.0.1:0"}, probe, zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/webhooks/managed", strings.NewReader(body))
	rec := httptest.NewRecorder()
	r.handleWebhook(rec, req)
	if rec.Code != http.StatusAccepted || probe.input.RawBody != body {
		t.Fatalf("status=%d input=%+v", rec.Code, probe.input)
	}
}

func TestReceiver_HandlerServesWithoutBinding(t *testing.T) {
	probe := &ingressProbe{prepared: webhookcmd.PreparedRoute{
		Name: "orders", Path: "/balda/webhooks/orders", PromptTemplate: template.Must(template.New("orders").Parse("{{.RawBody}}")),
	}}
	receiver, err := NewReceiver(Config{BasePath: "/balda", ListenAddr: "127.0.0.1:0"}, probe, zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/balda/webhooks/orders", strings.NewReader("order 42"))
	response := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	before := time.Now()
	receiver.Handler().ServeHTTP(response, request)
	after := time.Now()
	if response.Code != http.StatusAccepted || probe.input.Path != "/balda/webhooks/orders" || probe.input.RawBody != "order 42" {
		t.Errorf("response status = %d, admitted input = %+v", response.Code, probe.input)
	}
	if response.readDeadline.Before(before.Add(ReadTimeout)) || response.readDeadline.After(after.Add(ReadTimeout)) {
		t.Errorf("shared handler read deadline = %s, want %s from request start", response.readDeadline, ReadTimeout)
	}
	if receiver.listener != nil {
		t.Error("handler unexpectedly bound a listener")
	}
}
