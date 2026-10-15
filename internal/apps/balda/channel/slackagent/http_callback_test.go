package slackagent

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHTTPCallbacksReuseSignedHandlers(t *testing.T) {
	server := newTestServer(&recordingInboundProcessor{}, &recordingTurnCanceller{})
	server.config.Enabled = true
	callbacks, err := server.HTTPCallbacks()
	if err != nil {
		t.Fatal(err)
	}
	if callbacks.EventsLegacyPath != "/slack/agent/events" || callbacks.CommandsLegacyPath != "/slack/commands" {
		t.Fatalf("legacy paths = %q, %q", callbacks.EventsLegacyPath, callbacks.CommandsLegacyPath)
	}
	body := []byte(`{"type":"url_verification","challenge":"ready"}`)
	for _, test := range []struct {
		name    string
		handler http.Handler
		path    string
		body    []byte
		want    int
	}{
		{name: "valid events", handler: callbacks.Events, path: "/balda/gateway/slack/events", body: body, want: http.StatusOK},
		{name: "invalid events", handler: callbacks.Events, path: "/balda/gateway/slack/events", body: body, want: http.StatusUnauthorized},
		{name: "invalid commands", handler: callbacks.Commands, path: "/balda/gateway/slack/commands", body: []byte("command=%2Fbalda"), want: http.StatusUnauthorized},
	} {
		t.Run(test.name, func(t *testing.T) {
			secret := "secret"
			if test.name != "valid events" {
				secret = "wrong"
			}
			recorder := httptest.NewRecorder()
			test.handler.ServeHTTP(recorder, signedSlackRequest(t, test.path, secret, test.body))
			if recorder.Code != test.want {
				t.Fatalf("status = %d, want %d", recorder.Code, test.want)
			}
		})
	}
}

func TestHTTPCallbacksDisabled(t *testing.T) {
	server := newTestServer(nil, nil)
	callbacks, err := server.HTTPCallbacks()
	if err != nil || callbacks.Events != nil || callbacks.Commands != nil {
		t.Fatalf("HTTPCallbacks() = %+v, %v, want no routes", callbacks, err)
	}
}
