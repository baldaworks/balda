package usercmd

import (
	"context"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
)

// BindingIntegration identifies one configured transport instance.
type BindingIntegration struct {
	ChannelType string
	Key         string
}

// BindingChannel is safe configured or verified metadata for one binding interface.
type BindingChannel struct {
	Integration     BindingIntegration
	Name            string
	BotUsername     string
	Endpoint        string
	CommandsEnabled bool
}

// InvitationActor carries the authenticated administrator's audit identity.
type InvitationActor struct {
	UserID    string
	SessionID string
}

// BindingInvitation is a digest-only, single-use account onboarding record.
type BindingInvitation struct {
	ID               string
	UserID           string
	Integration      BindingIntegration
	TokenDigest      []byte `json:"-"`
	IssuedBy         string
	CreatedAt        time.Time
	ExpiresAt        time.Time
	ConsumedAt       time.Time
	RevokedAt        time.Time
	RevocationReason string
	Version          uint64
}

// InvitationIssue atomically creates or replaces the target's current invitation.
type InvitationIssue struct {
	Invitation          BindingInvitation
	ExpectedUserVersion uint64
	Replace             bool
	Audit               AuditEvent
}

// InvitationCancel revokes one current invitation with optimistic concurrency.
type InvitationCancel struct {
	ID              string
	UserID          string
	ActorUserID     string
	ExpectedVersion uint64
	CancelledAt     time.Time
	Audit           AuditEvent
}

// InvitationConsume atomically attaches a verified principal and consumes its proof.
type InvitationConsume struct {
	TokenDigest []byte
	Integration BindingIntegration
	Binding     Binding
	ConsumedAt  time.Time
	Audit       AuditEvent
}

// InvitationStore persists invitation lifecycle independently of internal claims.
type InvitationStore interface {
	IssueBindingInvitation(ctx context.Context, change InvitationIssue) error
	CancelBindingInvitation(ctx context.Context, change InvitationCancel) error
	ConsumeBindingInvitation(ctx context.Context, change InvitationConsume) (string, error)
	ListBindingInvitations(ctx context.Context, userID string) ([]BindingInvitation, error)
}

// BindingProof carries sender identity from a verified transport admission boundary.
type BindingProof struct {
	Payload           string
	Integration       BindingIntegration
	Principal         string
	DisplayName       string
	ProviderUsername  string
	ProviderFirstName string
	Provenance        string
	Direct            bool
	Locator           deliverycmd.Locator
}

// IssuedBindingInvitation carries a credential only in the issuance response.
type IssuedBindingInvitation struct {
	Invitation BindingInvitation
	Payload    string `json:"-"`
}
