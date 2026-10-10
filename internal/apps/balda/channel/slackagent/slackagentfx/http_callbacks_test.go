package slackagentfx

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/channel/slackagent"
	"github.com/baldaworks/balda/internal/apps/balda/httpfx"
	"github.com/rs/zerolog"
)

func TestGatewayCallbacksRouteSignedEventsAndCommands(t *testing.T) {
	server := slackagent.NewServer(nil, nil, nil, nil, slackagent.Config{Enabled: true, SigningSecret: "secret"}, zerolog.Nop())
	callbacks, err := NewGatewayCallbackProvider(server)(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	registry, err := httpfx.NewRegistry("/balda")
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.AddGatewayCallbacks(callbacks); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		path, body, secret string
		want               int
	}{
		{path: "/balda/gateway/slack/events", body: `{"type":"url_verification","challenge":"ok"}`, secret: "secret", want: http.StatusOK},
		{path: "/balda/gateway/slack/events", body: `{"type":"url_verification","challenge":"ok"}`, secret: "wrong", want: http.StatusUnauthorized},
		{path: "/balda/gateway/slack/commands", body: "command=%2Fbalda&text=unknown&team_id=T123&channel_id=C456&user_id=U789", secret: "secret", want: http.StatusOK},
		{path: "/balda/gateway/slack/commands", body: "command=%2Fbalda&text=unknown&team_id=T123&channel_id=C456&user_id=U789", secret: "wrong", want: http.StatusUnauthorized},
	} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(test.body))
		timestamp := strconv.FormatInt(time.Now().Unix(), 10)
		mac := hmac.New(sha256.New, []byte(test.secret))
		_, _ = mac.Write([]byte("v0:" + timestamp + ":" + test.body))
		request.Header.Set("X-Slack-Request-Timestamp", timestamp)
		request.Header.Set("X-Slack-Signature", "v0="+hex.EncodeToString(mac.Sum(nil)))
		registry.Handler().ServeHTTP(recorder, request)
		if recorder.Code != test.want {
			t.Errorf("%s: status = %d, want %d", test.path, recorder.Code, test.want)
		}
	}
}
