package webui

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

func TestBindingProjectionActionsAndMetadata(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	for _, channel := range []string{"telegram", "slackagent", "zulip", "mattermost"} {
		info := usercmd.BindingChannel{Integration: usercmd.BindingIntegration{ChannelType: channel, Key: "verified"}, BotUsername: "verified_bot", Endpoint: "https://chat.example.test", CommandsEnabled: true}
		pending := []usercmd.BindingInvitation{{ID: "pending", Integration: info.Integration, ExpiresAt: now.Add(time.Hour)}, {ID: "expired", Integration: usercmd.BindingIntegration{ChannelType: channel, Key: "prior-bot"}, ExpiresAt: now.Add(-time.Hour)}}
		form := ProjectBindingForm(info, usercmd.User{ID: "target", Status: usercmd.StatusActive, Version: 3}, "csrf", pending, now)
		if !form.Ready || !form.Active || len(form.Pending) != 2 || !form.Pending[1].Expired || form.Pending[1].Current {
			t.Fatalf("%s pending projection = %+v", channel, form)
		}
		reveal := ProjectBindingReveal(form, usercmd.IssuedBindingInvitation{Payload: "secret-value"})
		if !strings.Contains(reveal.Command, "secret-value") {
			t.Fatalf("%s missing command", channel)
		}
		serialized, err := json.Marshal(reveal)
		if err != nil || strings.Contains(string(serialized), "secret-value") {
			t.Fatalf("serialized reveal = %s: %v", serialized, err)
		}
		if channel == "mattermost" {
			form.CommandsEnabled = false
			reveal = ProjectBindingReveal(form, usercmd.IssuedBindingInvitation{Payload: "secret-value"})
			if reveal.Command != "" || reveal.DMCommand != "/msg @verified_bot secret-value" {
				t.Fatalf("DM action = %+v", reveal)
			}
		}
	}
}

func TestBindingActionURLs(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{"javascript:alert(1)", "data:text/html,bad", "//evil.example", "https://user:pass@chat.example", "slack://channel?team=T1&id=U1", "slack://user?team=T1", "slack://user/path?team=T1&id=U1"} {
		if channelURL("slackagent", raw) != "" {
			t.Fatalf("unsafe action allowed: %q", raw)
		}
	}
	for _, raw := range []string{"https://chat.example.test/#narrow/dm/1", "slack://user?team=T1&id=U1"} {
		if channelURL("slackagent", raw) == "" {
			t.Fatalf("verified action suppressed: %q", raw)
		}
	}
}
