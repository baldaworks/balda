package mattermostfx

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/auth"
	"github.com/baldaworks/balda/internal/apps/balda/channel/mattermost"
)

func TestMattermostBindingIdentityAndSlashCapability(t *testing.T) {
	for _, commandsEnabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "DM fallback", true: "slash receiver"}[commandsEnabled], func(t *testing.T) {
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v4/users/me" || r.Header.Get("Authorization") != "Bearer synthetic-token" {
					t.Error("unexpected identity request")
				}
				_, _ = io.WriteString(w, `{"id":"bot-1","username":"bindingbot","nickname":"Binding Bot"}`)
			}))
			defer api.Close()
			channels := auth.NewBindingChannels([]string{mattermost.ChannelType})
			p := bindingIdentityParams{Client: mattermost.NewClient(api.URL, "synthetic-token", "bot-1"), Channels: channels, Enabled: true, ServerURL: api.URL, BotUserID: "bot-1", BotUsername: "bindingbot", CommandsEnabled: commandsEnabled}
			if err := registerBindingIdentity(p); err != nil {
				t.Fatal(err)
			}
			info, err := channels.Refresh(t.Context(), mattermost.ChannelType)
			if err != nil || info.Integration.Key != api.URL+"#bot-1" || info.BotUsername != "bindingbot" || info.CommandsEnabled != commandsEnabled {
				t.Fatalf("safe identity %+v: %v", info, err)
			}
			p.BotUserID = "wrong-bot"
			if err := registerBindingIdentity(p); err != nil {
				t.Fatal(err)
			}
			if _, err := channels.Refresh(t.Context(), mattermost.ChannelType); err == nil {
				t.Fatal("configured identity mismatch accepted")
			}
		})
	}
}
