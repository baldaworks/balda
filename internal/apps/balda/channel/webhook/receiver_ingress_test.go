package webhook

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"text/template"

	"github.com/baldaworks/balda/internal/apps/balda/webhookcmd"
	"github.com/rs/zerolog"
)

type ingressProbe struct {
	prepareErr error
	prepared   webhookcmd.PreparedRoute
	input      webhookcmd.Inbound
}

func (p *ingressProbe) PrepareExternal(context.Context, string, map[string]string) (webhookcmd.PreparedRoute, error) {
	return p.prepared, p.prepareErr
}

func (p *ingressProbe) Admit(_ context.Context, _ webhookcmd.PreparedRoute, input webhookcmd.Inbound) (webhookcmd.Result, error) {
	p.input = input
	return webhookcmd.Result{RequestID: input.RequestID, MessageID: "msg-1"}, nil
}

func TestReceiver_MapsLookupFailureBeforeBodyRead(t *testing.T) {
	probe := &ingressProbe{prepareErr: &webhookcmd.DispatchFailedError{Cause: errors.New("store unavailable")}}
	r, err := NewReceiver(Config{ListenAddr: "127.0.0.1:0"}, probe, zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/managed", strings.NewReader("body"))
	rec := httptest.NewRecorder()
	r.handleWebhook(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if req.Body == nil {
		t.Fatal("request body disappeared")
	}
}

func TestReceiver_PassesBoundedBodyToIngress(t *testing.T) {
	const body = "body"
	probe := &ingressProbe{prepared: webhookcmd.PreparedRoute{
		Name: "managed", Path: "/managed", PromptTemplate: template.Must(template.New("x").Parse("{{.RawBody}}")),
	}}
	r, err := NewReceiver(Config{ListenAddr: "127.0.0.1:0"}, probe, zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/managed", strings.NewReader(body))
	rec := httptest.NewRecorder()
	r.handleWebhook(rec, req)
	if rec.Code != http.StatusAccepted || probe.input.RawBody != body {
		t.Fatalf("status=%d input=%+v", rec.Code, probe.input)
	}
}
