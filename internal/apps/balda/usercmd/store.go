package usercmd

import (
	"context"
	"time"
)

// MaxPageSize bounds user, session, and audit repository reads.
const MaxPageSize = 100

// PageRequest requests a bounded stable page after an exclusive ID cursor.
type PageRequest struct {
	AfterID string
	Limit   int
}

// UserPage contains canonical users ordered by stable ID.
type UserPage struct {
	Users       []User
	NextAfterID string
}

// SessionPage contains session-family summaries ordered by stable ID.
type SessionPage struct {
	Sessions    []SessionSummary
	NextAfterID string
}

// AuditPage contains security events ordered by stable ID.
type AuditPage struct {
	Events      []AuditEvent
	NextAfterID string
}

// AccessSession is the durable state required to authenticate an access credential.
type AccessSession struct {
	User   User
	Family SessionFamily
}

// RefreshSession is the durable state required to verify one refresh credential.
type RefreshSession struct {
	User   User
	Family SessionFamily
	Token  RefreshToken
}

// RefreshRotation contains one atomic access/refresh pair rotation.
type RefreshRotation struct {
	Selector                string
	PresentedVerifierDigest []byte
	Access                  AccessCredential
	Refresh                 RefreshToken
	RotatedAt               time.Time
	SuccessAudit            AuditEvent
	ReplayAudit             AuditEvent
}

// RefreshRotationResult is the non-secret outcome of one verified refresh attempt.
type RefreshRotationResult string

const (
	// RefreshRotationSucceeded means the old generation was consumed and a new pair was stored.
	RefreshRotationSucceeded RefreshRotationResult = "succeeded"
	// RefreshRotationReplayRevoked means a used generation was replayed and its family was revoked.
	RefreshRotationReplayRevoked RefreshRotationResult = "replay_revoked"
	// RefreshRotationUnavailable means the selector, verifier, family, user, or credential is unusable.
	RefreshRotationUnavailable RefreshRotationResult = "unavailable"
)

// MigrationUser is one canonical user and optional binding created by a legacy migration.
type MigrationUser struct {
	User    User
	Secret  CredentialSecret
	Binding *Binding
	Audits  []AuditEvent
}

// UserMigration is one immutable, idempotent legacy-user migration batch.
type UserMigration struct {
	ID                    string
	SourceFingerprint     string
	SourceCountsJSON      string
	PrimaryUserID         string
	CompletedAt           time.Time
	Users                 []MigrationUser
	GeneratedBindingCount int
}

// Store persists canonical users and security state without exposing plaintext credentials.
type Store interface {
	CreateUser(ctx context.Context, user User, secret CredentialSecret, audit AuditEvent) error
	UpdateUser(ctx context.Context, user User, expectedVersion uint64, audit AuditEvent) error
	ChangeCredential(ctx context.Context, userID string, expectedUserVersion, expectedCredentialVersion uint64, credential Credential, secret CredentialSecret, revokedAt time.Time, audit AuditEvent) error
	GetUser(ctx context.Context, userID string) (User, bool, error)
	GetUserByNormalizedUsername(ctx context.Context, normalizedUsername string) (User, bool, error)
	GetUserByBinding(ctx context.Context, channelType, principal string) (User, bool, error)
	GetCredentialSecret(ctx context.Context, userID string) (CredentialSecret, bool, error)
	ListUsers(ctx context.Context, page PageRequest) (UserPage, error)

	CreateBindingClaim(ctx context.Context, claim BindingClaim, createdAt time.Time, audit AuditEvent) error
	AttachBinding(ctx context.Context, claimID string, binding Binding, consumedAt time.Time, audit AuditEvent) error

	CreateSession(ctx context.Context, family SessionFamily, audit AuditEvent) error
	GetSessionByAccessSelector(ctx context.Context, selector string) (AccessSession, bool, error)
	GetSessionByRefreshSelector(ctx context.Context, selector string) (RefreshSession, bool, error)
	ListSessions(ctx context.Context, userID string, page PageRequest) (SessionPage, error)
	RotateRefresh(ctx context.Context, rotation RefreshRotation) (RefreshRotationResult, error)
	RevokeSession(ctx context.Context, sessionID string, expectedVersion uint64, revokedAt time.Time, reason string, audit AuditEvent) error
	RevokeUserSessions(ctx context.Context, userID string, revokedAt time.Time, reason string, audit AuditEvent) error
	DeleteExpiredSessions(ctx context.Context, before time.Time, limit int) (int, error)

	ListAuditEvents(ctx context.Context, page PageRequest) (AuditPage, error)
	UserMigrationApplied(ctx context.Context, sourceFingerprint string) (bool, error)
	ApplyUserMigration(ctx context.Context, migration UserMigration) (bool, error)
}
