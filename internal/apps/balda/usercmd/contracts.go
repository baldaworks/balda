package usercmd

import (
	"fmt"
	"strings"
	"time"
)

// Role is the system-wide authorization role of a canonical user.
type Role string

const (
	// RoleAdministrator grants Backoffice administration and owner-level bot access when bound.
	RoleAdministrator Role = "administrator"
	// RoleOperator grants ordinary Backoffice access and collaborator-level bot access when bound.
	RoleOperator Role = "operator"
)

// Valid reports whether the role is supported by the canonical user model.
func (r Role) Valid() bool {
	return r == RoleAdministrator || r == RoleOperator
}

// UserStatus determines whether a canonical user may exercise any capability.
type UserStatus string

const (
	// StatusActive allows capabilities appropriate to the user's role and binding.
	StatusActive UserStatus = "active"
	// StatusDisabled denies Backoffice and bot capabilities.
	StatusDisabled UserStatus = "disabled"
)

// Valid reports whether the user status is supported.
func (s UserStatus) Valid() bool {
	return s == StatusActive || s == StatusDisabled
}

// CredentialState is the lifecycle state of a user's local password credential.
type CredentialState string

const (
	// CredentialStateTemporary permits only restricted password-replacement sessions.
	CredentialStateTemporary CredentialState = "temporary"
	// CredentialStateActive permits normal Backoffice authentication.
	CredentialStateActive CredentialState = "active"
	// CredentialStateDisabled denies local authentication.
	CredentialStateDisabled CredentialState = "disabled"
)

// Valid reports whether the credential state is supported.
func (s CredentialState) Valid() bool {
	return s == CredentialStateTemporary || s == CredentialStateActive || s == CredentialStateDisabled
}

// Credential contains safe credential lifecycle metadata. Password hashes are
// stored separately and must never be included in user projections.
type Credential struct {
	State      CredentialState
	MustChange bool
	Version    uint64
}

// CredentialSecret is the storage-only password hash associated with a user.
type CredentialSecret struct {
	UserID       string
	PasswordHash string
}

// Binding identifies the single optional transport principal owned by a user.
type Binding struct {
	ID          string
	UserID      string
	ChannelType string
	Principal   string
	DisplayName string
	Provenance  string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// User is the secret-free canonical identity shared by Backoffice and bot authorization.
type User struct {
	ID                 string
	DisplayName        string
	Username           string
	NormalizedUsername string
	Status             UserStatus
	Role               Role
	Credential         Credential
	Primary            bool
	Version            uint64
	Binding            *Binding
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// BotCapability is the legacy bot capability derived from canonical user state.
type BotCapability string

const (
	// BotCapabilityNone denies bot access.
	BotCapabilityNone BotCapability = ""
	// BotCapabilityOwner grants owner-level bot access.
	BotCapabilityOwner BotCapability = "owner"
	// BotCapabilityCollaborator grants collaborator-level bot access.
	BotCapabilityCollaborator BotCapability = "collaborator"
)

// BackofficeCapabilities is the server-authoritative navigation and action set.
type BackofficeCapabilities struct {
	Overview    bool
	Account     bool
	ManageUsers bool
	ViewAudit   bool
}

// BindingClaim scopes one transport verification to one user and channel.
type BindingClaim struct {
	ID          string
	UserID      string
	ChannelType string
	ExpiresAt   time.Time
	ConsumedAt  time.Time
}

// SessionAssurance distinguishes restricted password-replacement sessions from normal sessions.
type SessionAssurance string

const (
	// SessionAssuranceRestricted permits password replacement, refresh, and logout only.
	SessionAssuranceRestricted SessionAssurance = "restricted"
	// SessionAssuranceNormal permits capabilities derived from the current user.
	SessionAssuranceNormal SessionAssurance = "normal"
)

// Valid reports whether the session assurance is supported.
func (a SessionAssurance) Valid() bool {
	return a == SessionAssuranceRestricted || a == SessionAssuranceNormal
}

// AccessCredential contains only persisted lookup material for the current access token.
type AccessCredential struct {
	Selector       string
	VerifierDigest []byte
	ExpiresAt      time.Time
}

// RefreshTokenState records whether a refresh generation can still be consumed.
type RefreshTokenState string

const (
	// RefreshTokenStateActive identifies the single consumable generation.
	RefreshTokenStateActive RefreshTokenState = "active"
	// RefreshTokenStateUsed retains a consumed generation for replay detection.
	RefreshTokenStateUsed RefreshTokenState = "used"
	// RefreshTokenStateRevoked identifies a generation revoked with its family.
	RefreshTokenStateRevoked RefreshTokenState = "revoked"
)

// Valid reports whether the refresh-token state is supported.
func (s RefreshTokenState) Valid() bool {
	return s == RefreshTokenStateActive || s == RefreshTokenStateUsed || s == RefreshTokenStateRevoked
}

// RefreshToken contains only persisted lookup material for one token generation.
type RefreshToken struct {
	Selector       string
	VerifierDigest []byte
	Generation     uint64
	State          RefreshTokenState
	IssuedAt       time.Time
	UsedAt         time.Time
	ExpiresAt      time.Time
}

// SessionFamily is the durable server-side browser session aggregate.
type SessionFamily struct {
	ID                 string
	UserID             string
	Assurance          SessionAssurance
	CredentialVersion  uint64
	Access             AccessCredential
	CSRFVerifierDigest []byte
	CreatedAt          time.Time
	LastSeenAt         time.Time
	RefreshExpiresAt   time.Time
	RevokedAt          time.Time
	RevocationReason   string
	Version            uint64
	RefreshTokens      []RefreshToken
}

// SessionSummary is the secret-free session-family projection shown to users.
type SessionSummary struct {
	ID         string
	Assurance  SessionAssurance
	CreatedAt  time.Time
	LastSeenAt time.Time
	ExpiresAt  time.Time
	RevokedAt  time.Time
	Version    uint64
}

// AuditAction is a stable security event action.
type AuditAction string

const (
	// AuditActionUserCreated records canonical user creation.
	AuditActionUserCreated AuditAction = "user.created"
	// AuditActionUserUpdated records a profile mutation that does not change role or status.
	AuditActionUserUpdated AuditAction = "user.updated"
	// AuditActionUserAccessChanged records one mutation that changes both role and status.
	AuditActionUserAccessChanged AuditAction = "user.access.changed"
	// AuditActionUserRoleChanged records a system-role mutation.
	AuditActionUserRoleChanged AuditAction = "user.role.changed"
	// AuditActionUserStatusChanged records a user-status mutation.
	AuditActionUserStatusChanged AuditAction = "user.status.changed"
	// AuditActionCredentialChanged records credential setup, reset, or rotation.
	AuditActionCredentialChanged AuditAction = "user.credential.changed"
	// AuditActionSessionRevoked records browser session-family revocation.
	AuditActionSessionRevoked AuditAction = "session.revoked"
	// AuditActionBindingAttached records verified transport-binding attachment.
	AuditActionBindingAttached AuditAction = "user.binding.attached"
	// AuditActionBindingClaimCreated records creation of a scoped onboarding claim.
	AuditActionBindingClaimCreated AuditAction = "user.binding.claim.created"
	// AuditActionUserMigrated records canonical creation from legacy authorization state.
	AuditActionUserMigrated AuditAction = "user.migrated"
	// AuditActionLoginSucceeded records creation of a browser session family.
	AuditActionLoginSucceeded AuditAction = "session.login.succeeded"
	// AuditActionRefreshSucceeded records access/refresh pair rotation.
	AuditActionRefreshSucceeded AuditAction = "session.refresh.succeeded"
	// AuditActionRefreshReplay records verified refresh-token reuse and family revocation.
	AuditActionRefreshReplay AuditAction = "session.refresh.replay"
	// AuditActionLogout records explicit browser-session logout.
	AuditActionLogout AuditAction = "session.logout"
)

// Valid reports whether the action belongs to the bounded security audit vocabulary.
func (a AuditAction) Valid() bool {
	switch a {
	case AuditActionUserCreated, AuditActionUserUpdated, AuditActionUserAccessChanged,
		AuditActionUserRoleChanged, AuditActionUserStatusChanged, AuditActionCredentialChanged,
		AuditActionSessionRevoked, AuditActionBindingAttached, AuditActionBindingClaimCreated,
		AuditActionUserMigrated, AuditActionLoginSucceeded, AuditActionRefreshSucceeded,
		AuditActionRefreshReplay, AuditActionLogout:
		return true
	default:
		return false
	}
}

// AuditOutcome is the security event result.
type AuditOutcome string

const (
	// AuditOutcomeSucceeded records a committed security operation.
	AuditOutcomeSucceeded AuditOutcome = "succeeded"
	// AuditOutcomeDenied records a rejected security operation.
	AuditOutcomeDenied AuditOutcome = "denied"
)

// Valid reports whether the audit outcome is supported.
func (o AuditOutcome) Valid() bool {
	return o == AuditOutcomeSucceeded || o == AuditOutcomeDenied
}

// AuditTargetType identifies the kind of object affected by a security event.
type AuditTargetType string

const (
	// AuditTargetUser identifies a canonical user.
	AuditTargetUser AuditTargetType = "user"
	// AuditTargetSession identifies a browser session family.
	AuditTargetSession AuditTargetType = "session"
	// AuditTargetBinding identifies a transport binding.
	AuditTargetBinding AuditTargetType = "binding"
	// AuditTargetSystem identifies a system-scoped security operation.
	AuditTargetSystem AuditTargetType = "system"
	// AuditTargetMigration identifies a canonical-user migration.
	AuditTargetMigration AuditTargetType = "migration"
)

// Valid reports whether the audit target type is supported.
func (t AuditTargetType) Valid() bool {
	switch t {
	case AuditTargetUser, AuditTargetSession, AuditTargetBinding, AuditTargetSystem, AuditTargetMigration:
		return true
	default:
		return false
	}
}

// AuditEvent is a secret-free immutable security event contract.
type AuditEvent struct {
	ID             string
	Action         AuditAction
	Outcome        AuditOutcome
	ActorUserID    string
	ActorSessionID string
	TargetType     AuditTargetType
	TargetID       string
	Reason         string
	RequestID      string
	Source         string
	CorrelationID  string
	OccurredAt     time.Time
}

// ValidateSessionFamily checks storage-neutral browser-session invariants.
func ValidateSessionFamily(f SessionFamily) error {
	if strings.TrimSpace(f.ID) == "" || strings.TrimSpace(f.UserID) == "" || !f.Assurance.Valid() {
		return fmt.Errorf("%w: session identity and assurance are required", ErrInvalid)
	}
	if f.CredentialVersion == 0 || f.Version == 0 {
		return fmt.Errorf("%w: session versions must be positive", ErrInvalid)
	}
	if strings.TrimSpace(f.Access.Selector) == "" || len(f.Access.VerifierDigest) == 0 || f.Access.ExpiresAt.IsZero() {
		return fmt.Errorf("%w: current access credential is incomplete", ErrInvalid)
	}
	if len(f.CSRFVerifierDigest) == 0 || f.CreatedAt.IsZero() || f.LastSeenAt.IsZero() || f.RefreshExpiresAt.IsZero() {
		return fmt.Errorf("%w: session lifecycle data is incomplete", ErrInvalid)
	}
	if !f.Access.ExpiresAt.Before(f.RefreshExpiresAt) {
		return fmt.Errorf("%w: access expiry must precede refresh expiry", ErrInvalid)
	}

	active := 0
	var previousGeneration uint64
	selectors := make(map[string]struct{}, len(f.RefreshTokens))
	for _, token := range f.RefreshTokens {
		if strings.TrimSpace(token.Selector) == "" || len(token.VerifierDigest) == 0 || token.Generation == 0 || !token.State.Valid() {
			return fmt.Errorf("%w: refresh generation is incomplete", ErrInvalid)
		}
		if token.IssuedAt.IsZero() || !token.ExpiresAt.Equal(f.RefreshExpiresAt) {
			return fmt.Errorf("%w: refresh generation lifetime is invalid", ErrInvalid)
		}
		if token.Generation <= previousGeneration {
			return fmt.Errorf("%w: refresh generations must increase", ErrInvalid)
		}
		previousGeneration = token.Generation
		if _, ok := selectors[token.Selector]; ok {
			return fmt.Errorf("%w: refresh selectors must be unique", ErrInvalid)
		}
		selectors[token.Selector] = struct{}{}
		switch token.State {
		case RefreshTokenStateActive:
			if !token.UsedAt.IsZero() {
				return fmt.Errorf("%w: active refresh generation cannot be used", ErrInvalid)
			}
			active++
		case RefreshTokenStateUsed:
			if token.UsedAt.IsZero() {
				return fmt.Errorf("%w: used refresh generation requires used time", ErrInvalid)
			}
		}
	}
	if f.RevokedAt.IsZero() && active != 1 {
		return fmt.Errorf("%w: active session requires one refresh generation", ErrInvalid)
	}
	if !f.RevokedAt.IsZero() && active != 0 {
		return fmt.Errorf("%w: revoked session cannot retain an active refresh generation", ErrInvalid)
	}
	return nil
}

// ValidateAuditEvent checks the required safe audit envelope fields.
func ValidateAuditEvent(event AuditEvent) error {
	if strings.TrimSpace(event.ID) == "" || !event.Action.Valid() || !event.Outcome.Valid() {
		return fmt.Errorf("%w: audit identity, action, and outcome are required", ErrInvalid)
	}
	if !event.TargetType.Valid() || strings.TrimSpace(event.TargetID) == "" || strings.TrimSpace(event.Source) == "" {
		return fmt.Errorf("%w: audit target and source are required", ErrInvalid)
	}
	if len(event.ID) > 256 || len(event.Action) > 128 || len(event.ActorUserID) > 256 || len(event.ActorSessionID) > 256 ||
		len(event.TargetID) > 256 || len(event.Reason) > 1024 || len(event.RequestID) > 256 ||
		len(event.Source) > 128 || len(event.CorrelationID) > 256 {
		return fmt.Errorf("%w: audit field exceeds its safe bound", ErrInvalid)
	}
	if event.OccurredAt.IsZero() {
		return fmt.Errorf("%w: audit occurrence time is required", ErrInvalid)
	}
	return nil
}
