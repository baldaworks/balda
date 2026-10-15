package handlersfx

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/auth"
	"github.com/baldaworks/balda/internal/apps/balda/channel/zulip"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"github.com/rs/zerolog"
)

func TestZulipBackofficeInvitationVerifiedWebhook(t *testing.T) {
	for _, role := range []usercmd.Role{usercmd.RoleOperator, usercmd.RoleAdministrator} {
		for _, route := range []string{"dm", commandStart} {
			t.Run(string(role)+"/"+route, func(t *testing.T) {
				p, err := state.Open(t.Context(), state.DatabaseConfig{Type: "sqlite", SQLite: state.SQLiteConfig{Path: filepath.Join(t.TempDir(), "state.db")}})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = p.Close() })
				now := time.Now().UTC()
				for _, id := range []string{bindingPrimaryUserID, "selected"} {
					r := usercmd.RoleAdministrator
					if id == "selected" {
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
				channels := auth.NewBindingChannels([]string{zulip.ChannelType})
				api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					email, key, ok := r.BasicAuth()
					if !ok || email != "bot@fixture.example" || key != "synthetic-key" || r.URL.Path != "/api/v1/users/me" {
						t.Errorf("unexpected configured identity lookup %s", r.URL.Path)
					}
					_, _ = io.WriteString(w, `{"result":"success","user_id":1001,"email":"bot@fixture.example","full_name":"Binding Bot","is_bot":true,"is_active":true}`)
				}))
				defer api.Close()
				client := zulip.NewClient(api.URL, "bot@fixture.example", "synthetic-key")
				identity, err := client.BindingIdentity(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				info := usercmd.BindingChannel{Integration: usercmd.BindingIntegration{ChannelType: zulip.ChannelType, Key: identity.RealmURL + "#" + strconv.Itoa(identity.UserID)}, BotUsername: identity.Email, Name: identity.FullName}
				if err := channels.Register(info); err != nil {
					t.Fatal(err)
				}
				invitation, err := invites.Issue(t.Context(), usercmd.InvitationActor{UserID: bindingPrimaryUserID, SessionID: "browser-fixture"}, "selected", 1, info.Integration, false)
				if err != nil {
					t.Fatal(err)
				}
				owners, err := auth.NewCanonicalOwnerStore(p.Users())
				if err != nil {
					t.Fatal(err)
				}
				commands := &recordingCommandIngress{}
				delivery := &fakeTurnDispatcher{}
				inbound := newZulipInboundHandler(zulipInboundHandlerParams{Bindings: invites, BindingChannels: channels, OwnerStore: owners, CollaboratorStore: auth.NewCanonicalCollaboratorStore(p.Users()), Dispatcher: delivery, CommandIngress: commands, Logger: zerolog.Nop()}).(*zulipInboundHandler)
				listener, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				address := listener.Addr().String()
				if err := listener.Close(); err != nil {
					t.Fatal(err)
				}
				server := zulip.NewServer(zulip.ServerParams{Processor: inbound, ZulipWebhookToken: "synthetic-webhook", ZulipListenAddr: address, ZulipEnabled: true, Logger: zerolog.Nop()})
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
				send := func(text, botEmail, messageType, token string) int {
					t.Helper()
					if route == commandStart {
						text = "/start " + text
					}
					body, err := json.Marshal(zulip.WebhookPayload{BotEmail: botEmail, Data: text, Token: token, Message: zulip.WebhookMessage{ID: 101, SenderID: 456, SenderEmail: "account@fixture.example", Type: messageType, StreamID: 1, Subject: "fixture", Content: text}})
					if err != nil {
						t.Fatal(err)
					}
					request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://"+address+"/zulip/webhook", strings.NewReader(string(body)))
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
						t.Fatal("secret in webhook outcome")
					}
					return response.StatusCode
				}
				if status := send(invitation.Payload, identity.Email, "private", "wrong"); status != http.StatusUnauthorized {
					t.Fatalf("unverified webhook status %d", status)
				}
				for _, bad := range []struct{ text, botEmail, messageType string }{{invitation.Payload, identity.Email, "stream"}, {invitation.Payload, "other@fixture.example", "private"}, {"please " + invitation.Payload, identity.Email, "private"}, {invitation.Payload[:len(invitation.Payload)-3], identity.Email, "private"}} {
					if status := send(bad.text, bad.botEmail, bad.messageType, "synthetic-webhook"); status != http.StatusOK {
						t.Fatalf("rejection status %d", status)
					}
					if _, found, err := p.Users().GetUserByBinding(t.Context(), zulip.ChannelType, "456"); err != nil || found {
						t.Fatalf("bad proof bound: %t %v", found, err)
					}
				}
				if status := send(invitation.Payload, identity.Email, "private", "synthetic-webhook"); status != http.StatusOK {
					t.Fatalf("success status %d", status)
				}
				bound, found, err := p.Users().GetUserByBinding(t.Context(), zulip.ChannelType, "456")
				if err != nil || !found || bound.ID != "selected" || bound.Role != role || bound.Primary || bound.Binding.ProviderUsername != "account@fixture.example" {
					t.Fatalf("binding %+v: %t %v", bound, found, err)
				}
				if !inbound.canAccessCollaboratorScope(t.Context(), 456) || inbound.getOwnerID() != 0 {
					t.Fatal("canonical access requires primary transport binding or infers owner")
				}
				if status := send(invitation.Payload, identity.Email, "private", "synthetic-webhook"); status != http.StatusOK {
					t.Fatalf("replay status %d", status)
				}
				// Quoted old credentials remain quarantined on subsequent provider messages.
				if status := send("Earlier message: "+invitation.Payload, identity.Email, "private", "synthetic-webhook"); status != http.StatusOK {
					t.Fatalf("quoted credential status %d", status)
				}
				if len(commands.requests) != 0 {
					t.Fatal("credential crossed command/model/delivery boundary")
				}
				for _, envelope := range delivery.commands {
					if strings.Contains(string(envelope.Payload.Data), invitation.Payload) {
						t.Fatal("credential in serialized durable delivery")
					}
				}
			})
		}
	}
}
