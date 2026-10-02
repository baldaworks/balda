package slackagentfx

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/auth"
	"github.com/baldaworks/balda/internal/apps/balda/channel/slackagent"
	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"github.com/rs/zerolog"
)

func TestSlackBackofficeInvitationSignedAdmission(t *testing.T) {
	for _, role := range []usercmd.Role{usercmd.RoleOperator, usercmd.RoleAdministrator} {
		for _, route := range []string{"dm", "start"} {
			t.Run(string(role)+"/"+route, func(t *testing.T) {
				p, err := state.Open(t.Context(), state.DatabaseConfig{Type: "sqlite", SQLite: state.SQLiteConfig{Path: filepath.Join(t.TempDir(), "state.db")}})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = p.Close() })
				now := time.Now().UTC()
				for _, id := range []string{"primary", "selected"} {
					r := usercmd.RoleAdministrator
					if id == "selected" {
						r = role
					}
					u := usercmd.User{ID: id, Username: id, NormalizedUsername: id, DisplayName: id, Role: r, Status: usercmd.StatusActive, Primary: id == "primary", Version: 1, Credential: usercmd.Credential{State: usercmd.CredentialStateActive, Version: 1}, CreatedAt: now, UpdatedAt: now}
					if err := p.Users().CreateUser(t.Context(), u, usercmd.CredentialSecret{UserID: id, PasswordHash: "synthetic"}, usercmd.AuditEvent{ID: "create-" + id, Action: usercmd.AuditActionUserCreated, TargetType: usercmd.AuditTargetUser, TargetID: id, Outcome: usercmd.AuditOutcomeSucceeded, Source: "fixture", OccurredAt: now}); err != nil {
						t.Fatal(err)
					}
				}
				invites, err := auth.NewBindingInvitations(p.Users().(usercmd.InvitationStore))
				if err != nil {
					t.Fatal(err)
				}
				channels := auth.NewBindingChannels([]string{slackagent.ChannelType})
				var identityCalls atomic.Int32
				var replies atomic.Int32
				api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Header.Get("Authorization") != "Bearer fixture-bot-token" {
						t.Error("missing configured bot credential")
					}
					switch r.URL.Path {
					case "/auth.test":
						identityCalls.Add(1)
						_, _ = io.WriteString(w, `{"ok":true,"team_id":"T123","user_id":"UBOT","bot_id":"B123","team":"Fixture workspace","user":"bindingbot"}`)
					case "/chat.postMessage":
						var body struct {
							Text    string `json:"text"`
							Channel string `json:"channel"`
						}
						if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
							t.Error(err)
						}
						if strings.Contains(body.Text, "bind_") || body.Channel != "D789" {
							t.Errorf("unsafe outcome %+v", body)
						}
						replies.Add(1)
						_, _ = io.WriteString(w, `{"ok":true,"ts":"1.1"}`)
					default:
						t.Errorf("unexpected API %s", r.URL.Path)
						w.WriteHeader(http.StatusNotFound)
					}
				}))
				defer api.Close()
				address := slackFixtureAddress(t)
				commands := &bindingCommandRecorder{}
				processor := &bindingChatRecorder{}
				server, err := newBindingServer(bindingServerParams{Processor: processor, Commands: commands, Config: slackagent.Config{Enabled: true, ListenAddr: address, SigningSecret: "fixture-signing-secret"}, Logger: zerolog.Nop(), Client: slackagent.NewClientWithBaseURL(api.URL, "fixture-bot-token"), Invitations: invites, Channels: channels})
				if err != nil {
					t.Fatal(err)
				}
				if err := server.Start(t.Context()); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					ctx, cancel := context.WithTimeout(context.Background(), time.Second)
					defer cancel()
					if err := server.Stop(ctx); err != nil {
						t.Error(err)
					}
				})
				info, err := channels.Refresh(t.Context(), slackagent.ChannelType)
				if err != nil || info.Integration.Key != "T123:UBOT" || !strings.Contains(info.Endpoint, "UBOT") {
					t.Fatalf("verified identity %+v: %v", info, err)
				}
				invitation, err := invites.Issue(t.Context(), usercmd.InvitationActor{UserID: "primary", SessionID: "browser-fixture"}, "selected", 1, info.Integration, false)
				if err != nil {
					t.Fatal(err)
				}
				send := func(text, team, conversation, secret string) int {
					t.Helper()
					var path string
					var body []byte
					if route == "start" {
						path = "/slack/commands"
						body = []byte(url.Values{"command": {"/balda"}, "text": {"start " + text}, "team_id": {team}, "channel_id": {conversation}, "user_id": {"U456"}}.Encode())
					} else {
						path = "/slack/agent/events"
						eventType, channelType := "message", "im"
						if conversation[0] != 'D' {
							eventType, channelType = "app_mention", "channel"
						}
						body = []byte(fmt.Sprintf(`{"type":"event_callback","event_id":"EvFixture","team_id":%q,"event":{"type":%q,"user":"U456","channel":%q,"text":%q,"ts":"1782234671.392669","channel_type":%q}}`, team, eventType, conversation, text, channelType))
					}
					request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://"+address+path, strings.NewReader(string(body)))
					if err != nil {
						t.Fatal(err)
					}
					timestamp := fmt.Sprint(time.Now().Unix())
					mac := hmac.New(sha256.New, []byte(secret))
					_, _ = mac.Write([]byte("v0:" + timestamp + ":" + string(body)))
					request.Header.Set("X-Slack-Request-Timestamp", timestamp)
					request.Header.Set("X-Slack-Signature", "v0="+hex.EncodeToString(mac.Sum(nil)))
					response, err := http.DefaultClient.Do(request)
					if err != nil {
						t.Fatal(err)
					}
					defer func() { _ = response.Body.Close() }()
					result, err := io.ReadAll(response.Body)
					if err != nil {
						t.Fatal(err)
					}
					if strings.Contains(string(result), invitation.Payload) {
						t.Fatal("secret in HTTP outcome")
					}
					return response.StatusCode
				}
				if status := send(invitation.Payload, "T123", "D789", "wrong-signature"); status != http.StatusUnauthorized {
					t.Fatalf("signature status %d", status)
				}
				for _, bad := range []struct{ text, team, conversation string }{{invitation.Payload, "T999", "D789"}, {invitation.Payload, "T123", "C789"}, {"please " + invitation.Payload, "T123", "D789"}, {invitation.Payload[:len(invitation.Payload)-4], "T123", "D789"}} {
					if status := send(bad.text, bad.team, bad.conversation, "fixture-signing-secret"); status != http.StatusOK {
						t.Fatalf("safe rejection status %d", status)
					}
					if _, found, err := p.Users().GetUserByBinding(t.Context(), slackagent.ChannelType, "T123:U456"); err != nil || found {
						t.Fatalf("bad proof bound account: %t %v", found, err)
					}
				}
				if status := send(invitation.Payload, "T123", "D789", "fixture-signing-secret"); status != http.StatusOK {
					t.Fatalf("success status %d", status)
				}
				bound, found, err := p.Users().GetUserByBinding(t.Context(), slackagent.ChannelType, "T123:U456")
				if err != nil || !found || bound.ID != "selected" || bound.Role != role || bound.Primary {
					t.Fatalf("selected binding %+v: %t %v", bound, found, err)
				}
				if status := send(invitation.Payload, "T123", "D789", "fixture-signing-secret"); status != http.StatusOK {
					t.Fatalf("replay status %d", status)
				}
				if commands.calls.Load() != 0 || processor.calls.Load() != 0 {
					t.Fatal("credential entered durable command or chat routing")
				}
				if identityCalls.Load() != 1 {
					t.Fatalf("identity calls %d", identityCalls.Load())
				}
				if route == "dm" && replies.Load() == 0 {
					t.Fatal("missing safe DM outcome")
				}
			})
		}
	}
}

func slackFixtureAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}

type bindingCommandRecorder struct{ calls atomic.Int32 }

func (r *bindingCommandRecorder) PublishCommand(context.Context, commandcmd.Request) error {
	r.calls.Add(1)
	return nil
}

type bindingChatRecorder struct{ calls atomic.Int32 }

func (r *bindingChatRecorder) ProcessInbound(context.Context, slackagent.IngressEnvelope) (turncmd.InboundSettlement, error) {
	r.calls.Add(1)
	return turncmd.InboundSettlement{Outcome: turncmd.InboundAccepted}, nil
}

func TestSlackInvitationRemovedBeforeHistoricalHydrationAndDispatch(t *testing.T) {
	envelope := contextualMentionEnvelope(t, "1.1", "2.0")
	manager := existingSessionManager(t, envelope.Locator, envelope.Subject)
	dispatcher := &dispatcherRecorder{}
	historyFiles := &historicalContextHydratorStub{}
	payload := "bind_" + strings.Repeat("x", 32)
	history := &threadHistoryStub{snapshot: slackagent.ThreadSnapshot{RootTS: "1.1", CutoffTS: "2.0", Available: true, Messages: []slackagent.ThreadMessage{{TS: "1.1", Text: payload}, {TS: "1.2", Text: "Useful prior discussion"}}}}
	processor := newTestInboundProcessorWithHistoricalFiles(t, manager, dispatcher, &sessionLifecycleStub{}, history, historyFiles)
	if _, err := processor.ProcessInbound(t.Context(), envelope); err != nil {
		t.Fatal(err)
	}
	if len(historyFiles.snapshot.Messages) != 1 || historyFiles.snapshot.Messages[0].Text != "Useful prior discussion" {
		t.Fatalf("credentials reached history hydration %+v", historyFiles.snapshot)
	}
	if len(dispatcher.commands) != 1 || strings.Contains(fmt.Sprintf("%+v", dispatcher.commands), payload) {
		t.Fatal("missing safe durable turn")
	}
}
