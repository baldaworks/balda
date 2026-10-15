//go:build integration && (sqlite || postgres)

package state

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/auth"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"github.com/google/uuid"
)

func checkBindingInvitations(t *testing.T, open contractOpener) {
	fixture := func(t *testing.T) (Provider, *auth.BindingInvitations, usercmd.User) {
		t.Helper()
		p := newContractProvider(t, open)
		t.Cleanup(func() { closeContractProvider(t, p) })
		now := time.Now().UTC()
		targetID := uuid.NewString()
		admin := contractUser(targetID+"-issuer", targetID+"-issuer", false, now)
		target := contractUser(targetID, targetID, false, now)
		target.Role = usercmd.RoleOperator
		for _, user := range []usercmd.User{admin, target} {
			if err := p.Users().CreateUser(t.Context(), user, contractSecret(user.ID), contractAudit("create-"+user.ID, usercmd.AuditActionUserCreated, user.ID, now)); err != nil {
				t.Fatal(err)
			}
		}
		service, err := auth.NewBindingInvitations(p.Users().(usercmd.InvitationStore))
		if err != nil {
			t.Fatal(err)
		}
		return p, service, target
	}
	issue := func(t *testing.T, s *auth.BindingInvitations, target usercmd.User, integration usercmd.BindingIntegration, replace bool) usercmd.IssuedBindingInvitation {
		t.Helper()
		got, err := s.Issue(t.Context(), usercmd.InvitationActor{UserID: target.ID + "-issuer", SessionID: "browser-family"}, target.ID, target.Version, integration, replace)
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Invitation.TokenDigest) != 0 || got.Invitation.ExpiresAt.Sub(got.Invitation.CreatedAt) != 24*time.Hour {
			t.Fatal("issuance must disclose only payload with fixed lifetime")
		}
		return got
	}
	proof := func(i usercmd.IssuedBindingInvitation, principal string) usercmd.BindingProof {
		return usercmd.BindingProof{Payload: i.Payload, Integration: i.Invitation.Integration, Principal: principal, Direct: true,
			Locator: deliverycmd.Locator{ChannelType: i.Invitation.Integration.ChannelType, AddressKey: "direct-address", SessionID: "session"}}
	}
	t.Run("selected operator and four integrations", func(t *testing.T) {
		p, s, target := fixture(t)
		for _, channel := range []struct{ name, principal string }{{"telegram", "101"}, {"slackagent", "T1:U1"}, {"zulip", "202"}, {"mattermost", "opaque-user"}} {
			i := issue(t, s, target, usercmd.BindingIntegration{ChannelType: channel.name, Key: "verified-instance"}, false)
			pending, err := s.Pending(t.Context(), target.ID)
			if err != nil || len(pending) != 1 || len(pending[0].TokenDigest) != 0 {
				t.Fatalf("safe pending metadata = %+v, %v", pending, err)
			}
			got, err := s.Consume(t.Context(), proof(i, channel.principal))
			if err != nil || got != target.ID {
				t.Fatalf("consume %s = %q, %v", channel.name, got, err)
			}
			target, _, err = p.Users().GetUser(t.Context(), target.ID)
			if err != nil || target.Role != usercmd.RoleOperator || target.Primary || target.Status != usercmd.StatusActive {
				t.Fatalf("bound target = %+v, %v", target, err)
			}
			if _, err := s.Consume(t.Context(), proof(i, channel.principal)); !errors.Is(err, usercmd.ErrBindingInvitationUnavailable) {
				t.Fatalf("replay error = %v", err)
			}
		}
		if len(target.Bindings) != 4 {
			t.Fatalf("confirmed bindings = %d", len(target.Bindings))
		}
		audits, err := p.Users().ListAuditEvents(t.Context(), usercmd.PageRequest{Limit: usercmd.MaxPageSize})
		if err != nil {
			t.Fatal(err)
		}
		for _, event := range audits.Events {
			if event.Action == usercmd.AuditActionInvitationIssued && event.ActorSessionID != "browser-family" {
				t.Fatal("issuance audit lost browser family")
			}
			if strings.Contains(fmt.Sprintf("%+v", event), "bind_") {
				t.Fatal("audit contains raw credential")
			}
		}
	})
	t.Run("authorization scope and conflict preserve proof", func(t *testing.T) {
		p, s, target := fixture(t)
		integration := usercmd.BindingIntegration{ChannelType: "telegram", Key: "bot-one"}
		if _, err := s.Issue(t.Context(), usercmd.InvitationActor{UserID: target.ID}, target.ID, target.Version, integration, false); !errors.Is(err, usercmd.ErrForbidden) {
			t.Fatalf("operator issuance = %v", err)
		}
		if _, err := s.Issue(t.Context(), usercmd.InvitationActor{UserID: target.ID + "-issuer", SessionID: "browser-family"}, target.ID, 99, integration, false); !errors.Is(err, usercmd.ErrConflict) {
			t.Fatalf("stale issuance = %v", err)
		}
		i := issue(t, s, target, integration, false)
		wrong := proof(i, "301")
		wrong.Integration.Key = "bot-two"
		if _, err := s.Consume(t.Context(), wrong); !errors.Is(err, usercmd.ErrBindingInvitationScope) {
			t.Fatalf("wrong instance = %v", err)
		}
		wrong = proof(i, "T1:U1")
		wrong.Integration.ChannelType, wrong.Locator.ChannelType = "slackagent", "slackagent"
		if _, err := s.Consume(t.Context(), wrong); !errors.Is(err, usercmd.ErrBindingInvitationScope) {
			t.Fatalf("wrong transport = %v", err)
		}
		wrong = proof(i, "301")
		wrong.Direct = false
		if _, err := s.Consume(t.Context(), wrong); !errors.Is(err, usercmd.ErrBindingInvitationUnavailable) {
			t.Fatalf("non-direct = %v", err)
		}
		now := time.Now().UTC()
		binding := usercmd.Binding{ID: "existing", UserID: target.ID + "-issuer", ChannelType: "telegram", Principal: "301", CreatedAt: now, UpdatedAt: now}
		if err := p.Users().CreateManagedBinding(t.Context(), binding, 1, contractAudit("existing-binding", usercmd.AuditActionBindingAttached, binding.ID, now)); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Consume(t.Context(), proof(i, "301")); !errors.Is(err, usercmd.ErrBindingPrincipalInUse) {
			t.Fatalf("principal conflict = %v", err)
		}
		if got, err := s.Consume(t.Context(), proof(i, "302")); err != nil || got != target.ID {
			t.Fatalf("valid proof after failures = %q, %v", got, err)
		}
	})
	t.Run("replace cancel and disable", func(t *testing.T) {
		p, s, target := fixture(t)
		integration := usercmd.BindingIntegration{ChannelType: "telegram", Key: "bot"}
		first := issue(t, s, target, integration, false)
		second := issue(t, s, target, integration, true)
		if _, err := s.Consume(t.Context(), proof(first, "101")); !errors.Is(err, usercmd.ErrBindingInvitationUnavailable) {
			t.Fatalf("replaced proof = %v", err)
		}
		if err := s.Cancel(t.Context(), usercmd.InvitationActor{UserID: target.ID + "-issuer", SessionID: "browser-family"}, target.ID, second.Invitation.ID, 99); !errors.Is(err, usercmd.ErrConflict) {
			t.Fatalf("stale cancel = %v", err)
		}
		if err := s.Cancel(t.Context(), usercmd.InvitationActor{UserID: target.ID + "-issuer", SessionID: "browser-family"}, target.ID, second.Invitation.ID, second.Invitation.Version); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Consume(t.Context(), proof(second, "101")); !errors.Is(err, usercmd.ErrBindingInvitationUnavailable) {
			t.Fatalf("cancelled proof = %v", err)
		}
		third := issue(t, s, target, integration, false)
		now := time.Now().UTC()
		target.Status, target.Version, target.UpdatedAt = usercmd.StatusDisabled, 2, now
		if err := p.Users().UpdateUser(t.Context(), target, 1, contractAudit("disable-target", usercmd.AuditActionUserStatusChanged, target.ID, now)); err != nil {
			t.Fatal(err)
		}
		target.Status, target.Version = usercmd.StatusActive, 3
		if err := p.Users().UpdateUser(t.Context(), target, 2, contractAudit("enable-target", usercmd.AuditActionUserStatusChanged, target.ID, now)); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Consume(t.Context(), proof(third, "101")); !errors.Is(err, usercmd.ErrBindingInvitationUnavailable) {
			t.Fatalf("disabled/re-enabled proof = %v", err)
		}
	})
	t.Run("transaction rollback expiry and concurrency", func(t *testing.T) {
		p, s, target := fixture(t)
		integration := usercmd.BindingIntegration{ChannelType: "telegram", Key: "bot"}
		i := issue(t, s, target, integration, false)
		store := p.Users().(usercmd.InvitationStore)
		digest := sha256.Sum256([]byte(i.Payload))
		audits, err := p.Users().ListAuditEvents(t.Context(), usercmd.PageRequest{Limit: usercmd.MaxPageSize})
		if err != nil {
			t.Fatal(err)
		}
		var issuedAudit usercmd.AuditEvent
		for _, a := range audits.Events {
			if a.Action == usercmd.AuditActionInvitationIssued && a.TargetID == target.ID {
				issuedAudit = a
			}
		}
		pending, err := store.ListBindingInvitations(t.Context(), target.ID)
		if err != nil || len(pending) != 1 {
			t.Fatalf("stored invitations = %+v, %v", pending, err)
		}
		replacement := pending[0]
		replacement.ID = "failed-replacement"
		replacement.TokenDigest = make([]byte, 32)
		if err := store.IssueBindingInvitation(t.Context(), usercmd.InvitationIssue{Invitation: replacement, ExpectedUserVersion: target.Version, Replace: true, Audit: issuedAudit}); !errors.Is(err, usercmd.ErrConflict) {
			t.Fatalf("failed replacement = %v", err)
		}
		now := time.Now().UTC()
		binding := usercmd.Binding{ID: "rolled-back-binding", ChannelType: "telegram", Principal: "501"}
		audit := contractAudit(issuedAudit.ID, usercmd.AuditActionBindingAttached, binding.ID, now)
		consume := usercmd.InvitationConsume{TokenDigest: digest[:], Integration: integration, Binding: binding, ConsumedAt: now, Audit: audit}
		if _, err := store.ConsumeBindingInvitation(t.Context(), consume); !errors.Is(err, usercmd.ErrConflict) {
			t.Fatalf("failed binding/audit transaction = %v", err)
		}
		if _, found, err := p.Users().GetUserByBinding(t.Context(), "telegram", "501"); err != nil || found {
			t.Fatalf("rolled back binding found = %t, %v", found, err)
		}
		consume.ConsumedAt = i.Invitation.ExpiresAt
		consume.Audit.ID = "expired-consume"
		if _, err := store.ConsumeBindingInvitation(t.Context(), consume); !errors.Is(err, usercmd.ErrBindingInvitationUnavailable) {
			t.Fatalf("absolute expiry = %v", err)
		}
		var wg sync.WaitGroup
		outcomes := make(chan error, 2)
		for n := range 2 {
			wg.Go(func() {
				_, err := s.Consume(t.Context(), proof(i, fmt.Sprint(501+n)))
				outcomes <- err
			})
		}
		wg.Wait()
		close(outcomes)
		wins := 0
		for err := range outcomes {
			if err == nil {
				wins++
			} else if !errors.Is(err, usercmd.ErrBindingInvitationUnavailable) {
				t.Fatalf("concurrent consume = %v", err)
			}
		}
		if wins != 1 {
			t.Fatalf("concurrent winners = %d", wins)
		}
	})
	t.Run("non-primary administrator and deleted target", func(t *testing.T) {
		p, s, target := fixture(t)
		now := time.Now().UTC()
		target.Role, target.Version = usercmd.RoleAdministrator, 2
		if err := p.Users().UpdateUser(t.Context(), target, 1, contractAudit("promote-"+target.ID, usercmd.AuditActionUserRoleChanged, target.ID, now)); err != nil {
			t.Fatal(err)
		}
		i := issue(t, s, target, usercmd.BindingIntegration{ChannelType: "mattermost", Key: "server-bot"}, false)
		if got, err := s.Consume(t.Context(), proof(i, "non-primary-admin")); err != nil || got != target.ID {
			t.Fatalf("non-primary administrator = %q, %v", got, err)
		}
		bound, _, err := p.Users().GetUser(t.Context(), target.ID)
		if err != nil || bound.Primary || bound.Role != usercmd.RoleAdministrator {
			t.Fatalf("administrator role/primary = %+v, %v", bound, err)
		}
		i = issue(t, s, bound, usercmd.BindingIntegration{ChannelType: "telegram", Key: "bot"}, false)
		db := contractDatabase(p).db
		query := `DELETE FROM balda_users WHERE user_id = ?`
		if _, ok := p.(*postgresProvider); ok {
			query = postgresBind(query)
		}
		if _, err := db.ExecContext(t.Context(), query, target.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Consume(t.Context(), proof(i, "701")); !errors.Is(err, usercmd.ErrBindingInvitationUnavailable) {
			t.Fatalf("deleted target proof = %v", err)
		}
	})
}
