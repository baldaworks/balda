package httpfx

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type deadlineRecorder struct {
	*httptest.ResponseRecorder
	readDeadline  time.Time
	writeDeadline time.Time
}

func (r *deadlineRecorder) SetReadDeadline(deadline time.Time) error {
	r.readDeadline = deadline
	return nil
}

func (r *deadlineRecorder) SetWriteDeadline(deadline time.Time) error {
	r.writeDeadline = deadline
	return nil
}

func TestGatewayCallbackKeepsTransportDeadlines(t *testing.T) {
	registry, err := NewRegistry("/balda")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := registry.AddGatewayCallbacks([]GatewayCallback{{
		Owner: "zulip", Transport: "zulip", Endpoint: "webhook", Handler: http.NotFoundHandler(),
		ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second,
	}}); err != nil {
		t.Fatal(err)
	}
	recorder := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	registry.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/balda/gateway/zulip/webhook", nil))
	if recorder.readDeadline.Before(start.Add(10*time.Second)) || recorder.writeDeadline.Before(start.Add(10*time.Second)) {
		t.Fatalf("callback deadlines = %s, %s", recorder.readDeadline, recorder.writeDeadline)
	}
}

func TestAddGatewayCallbacksRoutesCanonicalPath(t *testing.T) {
	registry, err := NewRegistry("/balda")
	if err != nil {
		t.Fatal(err)
	}
	callback := GatewayCallback{
		Owner: "slack events", Transport: "slack", Endpoint: "events",
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusAccepted) }),
	}
	if err := registry.AddGatewayCallbacks([]GatewayCallback{callback}); err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	registry.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/balda/gateway/slack/events", nil))
	if recorder.Code != http.StatusAccepted {
		t.Errorf("status = %d, want 202", recorder.Code)
	}
}

func TestAddGatewayCallbacksDetectsCanonicalConflicts(t *testing.T) {
	registry, err := NewRegistry("/balda")
	if err != nil {
		t.Fatal(err)
	}
	callbacks := []GatewayCallback{
		{Owner: "slack events", Transport: "slack", Endpoint: "events", Handler: http.NotFoundHandler()},
		{Owner: "other slack events", Transport: "slack", Endpoint: "events", Handler: http.NotFoundHandler()},
	}
	if err := registry.AddGatewayCallbacks(callbacks); err == nil || !strings.Contains(err.Error(), "/balda/gateway/slack/events") || !strings.Contains(err.Error(), "slack events") || !strings.Contains(err.Error(), "other slack events") {
		t.Fatalf("AddGatewayCallbacks() error = %v, want named conflict", err)
	}
}
