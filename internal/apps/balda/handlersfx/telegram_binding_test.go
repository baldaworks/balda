package handlersfx

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/auth"
	baldatelegram "github.com/baldaworks/balda/internal/apps/balda/channel/telegram"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/balda/internal/apps/balda/telegramref"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"github.com/rs/zerolog"
	"github.com/tgbotkit/client"
	"github.com/tgbotkit/runtime/events"
)

const bindingPrimaryUserID = "primary"

func TestTelegramBackofficeInvitations(t *testing.T) {
	for _, target := range []struct {
		id      string
		role    usercmd.Role
		primary bool
	}{{bindingPrimaryUserID, usercmd.RoleAdministrator, true}, {"secondary", usercmd.RoleAdministrator, false}, {"operator", usercmd.RoleOperator, false}} {
		for _, route := range []string{commandStart, "dm"} {
			t.Run(target.id+"/"+route, func(t *testing.T) {
				p, err := state.Open(t.Context(), state.DatabaseConfig{Type: "sqlite", SQLite: state.SQLiteConfig{Path: filepath.Join(t.TempDir(), "state.db")}})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = p.Close() })
				now := time.Now().UTC()
				ids := []string{bindingPrimaryUserID}
				if target.id != bindingPrimaryUserID {
					ids = append(ids, target.id)
				}
				for _, id := range ids {
					role := usercmd.RoleAdministrator
					if id == target.id {
						role = target.role
					}
					user := usercmd.User{ID: id, DisplayName: id, Username: id, NormalizedUsername: id, Role: role, Status: usercmd.StatusActive, Primary: id == bindingPrimaryUserID, Version: 1, Credential: usercmd.Credential{State: usercmd.CredentialStateActive, Version: 1}, CreatedAt: now, UpdatedAt: now}
					if err := p.Users().CreateUser(t.Context(), user, usercmd.CredentialSecret{UserID: id, PasswordHash: "synthetic-hash"}, usercmd.AuditEvent{ID: "create-" + id, Action: usercmd.AuditActionUserCreated, TargetType: usercmd.AuditTargetUser, TargetID: id, Outcome: usercmd.AuditOutcomeSucceeded, Source: "test", OccurredAt: now}); err != nil {
						t.Fatal(err)
					}
				}
				bindings, err := auth.NewBindingInvitations(p.Users().(usercmd.InvitationStore))
				if err != nil {
					t.Fatal(err)
				}
				channels := auth.NewBindingChannels([]string{"telegram"})
				owners, err := auth.NewCanonicalOwnerStore(p.Users())
				if err != nil {
					t.Fatal(err)
				}
				delivery := &fakeTurnDispatcher{}
				inbound := newTelegramInboundHandler(telegramInboundHandlerParams{Bindings: bindings, BindingChannels: channels, OwnerStore: owners, CollaboratorStore: auth.NewCanonicalCollaboratorStore(p.Users()), Dispatcher: delivery, Logger: zerolog.Nop()})
				if err := inbound.OnBotStarted(t.Context(), 1001, "bindingbot"); err != nil {
					t.Fatal(err)
				}
				info, ok := channels.Get("telegram")
				if !ok || info.Integration.Key != "1001" {
					t.Fatal("verified bot identity unavailable")
				}
				i, err := bindings.Issue(t.Context(), usercmd.InvitationActor{UserID: bindingPrimaryUserID, SessionID: "browser-family"}, target.id, 1, info.Integration, false)
				if err != nil {
					t.Fatal(err)
				}
				commands := &recordingCommandIngress{}
				username := "account_name"
				if route == commandStart {
					h := newTelegramStartHandler(telegramStartHandlerParams{Bindings: bindings, BindingChannels: channels, Inbound: inbound, Dispatcher: delivery, CommandIngress: commands, Logger: zerolog.Nop()})
					event := &events.CommandEvent{Command: commandStart, Args: i.Payload, Message: &client.Message{MessageId: 1, Chat: client.Chat{Id: 101, Type: "private"}, From: &client.User{Id: 101, FirstName: "Account", Username: &username}}}
					if err := h.onCommand(t.Context(), event); err != nil {
						t.Fatal(err)
					}
				} else {
					message := baldatelegram.MessageContext{Text: i.Payload, UserID: 101, ChatID: 101, Username: username, FirstName: "Account", Locator: telegramref.NewLocator(101, 0), IsDM: true}
					group := message
					group.IsDM = false
					if err := inbound.HandleMessage(t.Context(), group); err != nil {
						t.Fatal(err)
					}
					mixed := message
					mixed.Text = "please use " + i.Payload
					if err := inbound.HandleMessage(t.Context(), mixed); err != nil {
						t.Fatal(err)
					}
					if _, found, err := p.Users().GetUserByBinding(t.Context(), "telegram", "101"); err != nil || found {
						t.Fatalf("non-exact/non-DM grants access: %t %v", found, err)
					}
					if err := inbound.HandleMessage(t.Context(), message); err != nil {
						t.Fatal(err)
					}
				}
				bound, found, err := p.Users().GetUserByBinding(t.Context(), "telegram", "101")
				if err != nil || !found || bound.ID != target.id || bound.Role != target.role || bound.Primary != target.primary || bound.Binding.ProviderUsername != username {
					t.Fatalf("verified account = %+v, %t, %v", bound, found, err)
				}
				ownerID, _ := inbound.getOwnerBinding()
				if (ownerID == 101) != target.primary {
					t.Fatalf("primary activation for %s: owner=%d", target.id, ownerID)
				}
				if len(commands.requests) != 0 || strings.Contains(fmt.Sprintf("%+v", delivery.commands), i.Payload) {
					t.Fatal("raw invitation crossed durable command/delivery boundary")
				}
				if allowed, err := inbound.accessCollaboratorScope(t.Context(), 101); err != nil || !allowed {
					t.Fatalf("bound account authorization = %t, %v", allowed, err)
				}
			})
		}
	}
}
