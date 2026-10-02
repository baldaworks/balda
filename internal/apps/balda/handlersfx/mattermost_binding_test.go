package handlersfx

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/auth"
	"github.com/baldaworks/balda/internal/apps/balda/channel/mattermost"
	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"github.com/gorilla/websocket"
	"github.com/rs/zerolog"
)

const bindingSelectedUserID = "selected"

func TestMattermostBackofficeInvitationTrustedIngress(t *testing.T) {
	for _, role := range []usercmd.Role{usercmd.RoleOperator, usercmd.RoleAdministrator} {
		for _, channelType := range []string{"D", "G"} {
			for _, route := range []string{"websocket", "slash"} {
				t.Run(string(role)+"/"+channelType+"/"+route, func(t *testing.T) {
					p, err := state.Open(t.Context(), state.DatabaseConfig{Type: "sqlite", SQLite: state.SQLiteConfig{Path: filepath.Join(t.TempDir(), "state.db")}})
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = p.Close() })
					now := time.Now().UTC()
					for _, id := range []string{bindingPrimaryUserID, bindingSelectedUserID} {
						r := usercmd.RoleAdministrator
						if id == bindingSelectedUserID {
							r = role
						}
						u := usercmd.User{ID: id, Username: id, NormalizedUsername: id, DisplayName: id, Role: r, Status: usercmd.StatusActive, Primary: id == bindingPrimaryUserID, Version: 1, Credential: usercmd.Credential{State: usercmd.CredentialStateActive, Version: 1}, CreatedAt: now, UpdatedAt: now}
						if err := p.Users().CreateUser(t.Context(), u, usercmd.CredentialSecret{UserID: id, PasswordHash: "synthetic"}, usercmd.AuditEvent{ID: "create-" + id, Action: usercmd.AuditActionUserCreated, TargetType: usercmd.AuditTargetUser, TargetID: id, Outcome: usercmd.AuditOutcomeSucceeded, Source: "fixture", OccurredAt: now}); err != nil {
							t.Fatal(err)
						}
					}
					invites, err := auth.NewBindingInvitations(p.Users().(usercmd.InvitationStore))
					if err != nil {
						t.Fatal(err)
					}
					channels := auth.NewBindingChannels([]string{mattermost.ChannelType})
					connected := make(chan *websocket.Conn, 1)
					upgrader := websocket.Upgrader{}
					api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if r.Header.Get("Authorization") != "Bearer synthetic-api-token" {
							t.Error("missing configured Mattermost credential")
						}
						switch r.URL.Path {
						case "/api/v4/users/me":
							_, _ = io.WriteString(w, `{"id":"bot-1","username":"bindingbot","is_bot":true}`)
						case "/api/v4/channels/direct-channel":
							_ = json.NewEncoder(w).Encode(mattermost.Channel{ID: "direct-channel", Type: channelType})
						case "/api/v4/channels/public-channel":
							_ = json.NewEncoder(w).Encode(mattermost.Channel{ID: "public-channel", Type: "O", TeamID: "team-1"})
						case "/api/v4/websocket":
							connection, err := upgrader.Upgrade(w, r, nil)
							if err != nil {
								t.Error(err)
								return
							}
							connected <- connection
						default:
							t.Errorf("unexpected provider API %s", r.URL.Path)
							w.WriteHeader(http.StatusNotFound)
						}
					}))
					defer api.Close()
					client := mattermost.NewClient(api.URL, "synthetic-api-token", "bot-1")
					if err := client.ValidateIdentity(t.Context(), "bot-1", "bindingbot"); err != nil {
						t.Fatal(err)
					}
					info := usercmd.BindingChannel{Integration: usercmd.BindingIntegration{ChannelType: mattermost.ChannelType, Key: api.URL + "#bot-1"}, BotUsername: "bindingbot", Endpoint: api.URL, CommandsEnabled: route == "slash"}
					if err := channels.Register(info); err != nil {
						t.Fatal(err)
					}
					invitation, err := invites.Issue(t.Context(), usercmd.InvitationActor{UserID: bindingPrimaryUserID, SessionID: "browser-fixture"}, bindingSelectedUserID, 1, info.Integration, false)
					if err != nil {
						t.Fatal(err)
					}
					wrongInstance, err := invites.Issue(t.Context(), usercmd.InvitationActor{UserID: bindingPrimaryUserID}, bindingSelectedUserID, 1, usercmd.BindingIntegration{ChannelType: mattermost.ChannelType, Key: api.URL + "#other-bot"}, false)
					if err != nil {
						t.Fatal(err)
					}
					owners, err := auth.NewCanonicalOwnerStore(p.Users())
					if err != nil {
						t.Fatal(err)
					}
					commands := &recordingCommandIngress{}
					delivery := &fakeTurnDispatcher{}
					inbound := newMattermostInboundHandler(mattermostInboundHandlerParams{Bindings: invites, BindingChannels: channels, OwnerStore: owners, CollaboratorStore: auth.NewCanonicalCollaboratorStore(p.Users()), Dispatcher: delivery, CommandIngress: commands, Logger: zerolog.Nop()}).(*mattermostInboundHandler)
					processor := &bindingMattermostProcessor{delegate: inbound, done: make(chan deliverycmd.Locator, 16)}
					registry := commandcmd.NewRegistryWithAdvertisements([]commandcmd.Advertisement{{Transport: mattermost.ChannelType, Enabled: true, Names: []string{commandStart}}})
					var send func(string, string, string) int
					if route == "websocket" {
						ingress := mattermost.NewIngress(mattermost.IngressParams{Processor: processor, Client: client, Commands: registry, Enabled: true, BotUserID: "bot-1", BotUsername: "bindingbot", Logger: zerolog.Nop()})
						if err := ingress.Start(t.Context()); err != nil {
							t.Fatal(err)
						}
						var connection *websocket.Conn
						select {
						case connection = <-connected:
						case <-time.After(3 * time.Second):
							t.Fatal("provider websocket not connected")
						}
						t.Cleanup(func() {
							ctx, cancel := context.WithTimeout(context.Background(), time.Second)
							defer cancel()
							if err := ingress.Stop(ctx); err != nil {
								t.Error(err)
							}
							_ = connection.Close()
						})
						send = func(text, conversation, token string) int {
							t.Helper()
							kind := channelType
							if conversation == "public-channel" {
								kind = "O"
								text = "@bindingbot " + text
							}
							post, err := json.Marshal(mattermost.Post{ID: "post-fixture", UserID: "account-1", ChannelID: conversation, Message: text})
							if err != nil {
								t.Fatal(err)
							}
							data, err := json.Marshal(mattermost.PostedData{ChannelType: kind, Post: string(post), SenderName: "accountname", TeamID: "team-1"})
							if err != nil {
								t.Fatal(err)
							}
							if err := connection.WriteJSON(mattermost.WebSocketEvent{Event: "posted", Data: data}); err != nil {
								t.Fatal(err)
							}
							select {
							case locator := <-processor.done:
								if locator.ChannelType != mattermost.ChannelType || mattermost.ChannelIDOf(locator) != conversation {
									t.Fatalf("changed canonical locator %+v", locator)
								}
							case <-time.After(3 * time.Second):
								t.Fatal("provider event did not settle")
							}
							return http.StatusOK
						}
					} else {
						listener, err := net.Listen("tcp", "127.0.0.1:0")
						if err != nil {
							t.Fatal(err)
						}
						address := listener.Addr().String()
						if err := listener.Close(); err != nil {
							t.Fatal(err)
						}
						receiver := mattermost.NewCommandServer(mattermost.CommandServerParams{Processor: processor, Commands: registry, Client: client, Config: mattermost.CommandServerConfig{Enabled: true, ListenAddr: address, Token: "synthetic-command-token", BotUserID: "bot-1", BotUsername: "bindingbot"}, Logger: zerolog.Nop()})
						if err := receiver.Start(t.Context()); err != nil {
							t.Fatal(err)
						}
						t.Cleanup(func() {
							ctx, cancel := context.WithTimeout(context.Background(), time.Second)
							defer cancel()
							if err := receiver.Stop(ctx); err != nil {
								t.Error(err)
							}
						})
						send = func(text, conversation, token string) int {
							t.Helper()
							body := url.Values{"token": {token}, "command": {"/balda"}, "text": {"start " + text}, "user_id": {"account-1"}, "channel_id": {conversation}}.Encode()
							request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://"+address+"/mattermost/commands", strings.NewReader(body))
							if err != nil {
								t.Fatal(err)
							}
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
								t.Fatal("credential in command HTTP outcome")
							}
							return response.StatusCode
						}
						if status := send(invitation.Payload, "direct-channel", "wrong"); status != http.StatusUnauthorized {
							t.Fatalf("wrong command token status %d", status)
						}
					}
					for _, bad := range []struct{ text, conversation string }{{wrongInstance.Payload, "direct-channel"}, {invitation.Payload, "public-channel"}, {"please " + invitation.Payload, "direct-channel"}, {invitation.Payload[:len(invitation.Payload)-3], "direct-channel"}} {
						if status := send(bad.text, bad.conversation, "synthetic-command-token"); status != http.StatusOK {
							t.Fatalf("safe denial status %d", status)
						}
						if _, found, err := p.Users().GetUserByBinding(t.Context(), mattermost.ChannelType, "account-1"); err != nil || found {
							t.Fatalf("bad proof bound: %t %v", found, err)
						}
					}
					if status := send(invitation.Payload, "direct-channel", "synthetic-command-token"); status != http.StatusOK {
						t.Fatalf("success status %d", status)
					}
					bound, found, err := p.Users().GetUserByBinding(t.Context(), mattermost.ChannelType, "account-1")
					if err != nil || !found || bound.ID != bindingSelectedUserID || bound.Role != role || bound.Primary {
						t.Fatalf("binding %+v: %t %v", bound, found, err)
					}
					if allowed, err := inbound.authorizeMattermostUser(t.Context(), "account-1"); err != nil || !allowed {
						t.Fatalf("canonical authorization %t %v", allowed, err)
					}
					if status := send(invitation.Payload, "direct-channel", "synthetic-command-token"); status != http.StatusOK {
						t.Fatalf("replay status %d", status)
					}
					if status := send("Earlier invitation: "+invitation.Payload, "direct-channel", "synthetic-command-token"); status != http.StatusOK {
						t.Fatalf("quoted invitation status %d", status)
					}
					if len(commands.requests) != 0 {
						t.Fatal("credential entered durable command queue")
					}
					delivery.commandsMu.Lock()
					defer delivery.commandsMu.Unlock()
					for _, envelope := range delivery.commands {
						if strings.Contains(string(envelope.Payload.Data), invitation.Payload) {
							t.Fatal("credential in serialized delivery/model input")
						}
					}
				})
			}
		}
	}
}

type bindingMattermostProcessor struct {
	delegate mattermost.InboundProcessor
	done     chan deliverycmd.Locator
}

func (p *bindingMattermostProcessor) ProcessInbound(ctx context.Context, msg mattermost.InboundMessage) (turncmd.InboundSettlement, error) {
	result, err := p.delegate.ProcessInbound(ctx, msg)
	p.done <- msg.Locator
	return result, err
}
func (p *bindingMattermostProcessor) HandleCommand(ctx context.Context, cmd mattermost.InboundCommand) error {
	err := p.delegate.HandleCommand(ctx, cmd)
	p.done <- cmd.Locator
	return err
}
func (p *bindingMattermostProcessor) HandleUnsupportedCommand(ctx context.Context, cmd mattermost.InboundCommand) error {
	err := p.delegate.HandleUnsupportedCommand(ctx, cmd)
	p.done <- cmd.Locator
	return err
}
