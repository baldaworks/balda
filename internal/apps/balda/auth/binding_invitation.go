package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/authpayload"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"github.com/baldaworks/balda/internal/apps/balda/users"
	"github.com/google/uuid"
)

type bindingInvitationStore interface {
	IssueBindingInvitation(ctx context.Context, change usercmd.InvitationIssue) error
	CancelBindingInvitation(ctx context.Context, change usercmd.InvitationCancel) error
	ConsumeBindingInvitation(ctx context.Context, change usercmd.InvitationConsume) (string, error)
	ListBindingInvitations(ctx context.Context, userID string) ([]usercmd.BindingInvitation, error)
}

// BindingInvitations owns Backoffice-issued account proof, independently of owner bootstrap.
type BindingInvitations struct {
	store bindingInvitationStore
	now   func() time.Time
}

// IssuedBindingInvitation discloses the raw credential only at creation.
type IssuedBindingInvitation struct {
	Invitation usercmd.BindingInvitation
	Payload    string
}

// NewBindingInvitations creates the shared invitation use case.
func NewBindingInvitations(store bindingInvitationStore) (*BindingInvitations, error) {
	if store == nil {
		return nil, fmt.Errorf("binding invitation store is required")
	}
	return &BindingInvitations{store: store, now: time.Now}, nil
}

// Issue creates an invitation for the exact active target and configured instance.
func (s *BindingInvitations) Issue(ctx context.Context, actor usercmd.InvitationActor, userID string, version uint64, integration usercmd.BindingIntegration, replace bool) (IssuedBindingInvitation, error) {
	if err := validateBindingIntegration(integration); err != nil {
		return IssuedBindingInvitation{}, err
	}
	secret := make([]byte, 24)
	if _, err := rand.Read(secret); err != nil {
		return IssuedBindingInvitation{}, fmt.Errorf("generate binding invitation: %w", err)
	}
	payload := authpayload.Prefix + base64.RawURLEncoding.EncodeToString(secret)
	digest := sha256.Sum256([]byte(payload))
	now := s.now().UTC()
	invitation := usercmd.BindingInvitation{
		ID: uuid.NewString(), UserID: userID, Integration: integration, TokenDigest: digest[:],
		IssuedBy: actor.UserID, CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour), Version: 1,
	}
	audit := invitationAudit(usercmd.AuditActionInvitationIssued, userID, actor.UserID, now)
	audit.ActorSessionID = actor.SessionID
	if err := s.store.IssueBindingInvitation(ctx, usercmd.InvitationIssue{Invitation: invitation, ExpectedUserVersion: version, Replace: replace, Audit: audit}); err != nil {
		return IssuedBindingInvitation{}, err
	}
	invitation.TokenDigest = nil
	return IssuedBindingInvitation{Invitation: invitation, Payload: payload}, nil
}

// Pending returns safe metadata for current invitations, including expired ones.
func (s *BindingInvitations) Pending(ctx context.Context, userID string) ([]usercmd.BindingInvitation, error) {
	invitations, err := s.store.ListBindingInvitations(ctx, userID)
	if err != nil {
		return nil, err
	}
	for i := range invitations {
		invitations[i].TokenDigest = nil
	}
	return invitations, nil
}

// Cancel revokes a pending invitation without removing confirmed bindings.
func (s *BindingInvitations) Cancel(ctx context.Context, actor usercmd.InvitationActor, userID, id string, version uint64) error {
	now := s.now().UTC()
	audit := invitationAudit(usercmd.AuditActionInvitationRevoked, userID, actor.UserID, now)
	audit.ActorSessionID = actor.SessionID
	return s.store.CancelBindingInvitation(ctx, usercmd.InvitationCancel{
		ID: id, UserID: userID, ActorUserID: actor.UserID, ExpectedVersion: version, CancelledAt: now,
		Audit: audit,
	})
}

// Consume attaches the verified sender while preserving the existing Direct/locator contract.
func (s *BindingInvitations) Consume(ctx context.Context, proof usercmd.BindingProof) (string, error) {
	payload, ok := authpayload.Parse(proof.Payload)
	if !ok || !proof.Direct || proof.Locator.ChannelType != proof.Integration.ChannelType || proof.Locator.AddressKey == "" || proof.Locator.SessionID == "" {
		return "", usercmd.ErrBindingInvitationUnavailable
	}
	if err := validateBindingIntegration(proof.Integration); err != nil {
		return "", err
	}
	principal, err := users.NormalizeBindingPrincipal(proof.Integration.ChannelType, proof.Principal)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte(payload))
	now := s.now().UTC()
	binding := usercmd.Binding{
		ID: uuid.NewString(), ChannelType: proof.Integration.ChannelType, Principal: principal,
		DisplayName: proof.DisplayName, ProviderUsername: proof.ProviderUsername, ProviderFirstName: proof.ProviderFirstName,
		Provenance: "backoffice-invitation", CreatedAt: now, UpdatedAt: now,
	}
	if proof.Provenance != "" {
		binding.Provenance += ";" + proof.Provenance
	}
	audit := invitationAudit(usercmd.AuditActionBindingAttached, binding.ID, "", now)
	audit.TargetType = usercmd.AuditTargetBinding
	return s.store.ConsumeBindingInvitation(ctx, usercmd.InvitationConsume{TokenDigest: digest[:], Integration: proof.Integration, Binding: binding, ConsumedAt: now, Audit: audit})
}

func validateBindingIntegration(integration usercmd.BindingIntegration) error {
	if integration.Key == "" || len(integration.Key) > 512 {
		return usercmd.ErrInvalid
	}
	switch integration.ChannelType {
	case ChannelTelegram, ChannelSlack, ChannelZulip, ChannelMattermost:
		return nil
	default:
		return usercmd.ErrInvalid
	}
}

func invitationAudit(action usercmd.AuditAction, target, actor string, now time.Time) usercmd.AuditEvent {
	return usercmd.AuditEvent{
		ID: uuid.NewString(), Action: action, TargetType: usercmd.AuditTargetUser, TargetID: target,
		ActorUserID: actor, Outcome: usercmd.AuditOutcomeSucceeded, Source: "binding-invitation", OccurredAt: now,
	}
}
