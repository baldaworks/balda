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
	// CurrentWebAuthSessionID places the active browser session first for WebAuth pages.
	CurrentWebAuthSessionID string
}

// UserPage contains canonical users ordered by stable ID.
type UserPage struct {
	Users       []User
	NextAfterID string
}

// WebAuthSessionPage contains one page of session-family summaries.
type WebAuthSessionPage struct {
	Sessions    []WebAuthSessionSummary
	NextAfterID string
}

// AuditPage contains security events ordered by stable ID.
type AuditPage struct {
	Events      []AuditEvent
	NextAfterID string
}

// WebAuthAccessSession is the durable state required to authenticate an access credential.
type WebAuthAccessSession struct {
	User           User
	WebAuthSession WebAuthSession
}

// WebAuthRefreshSession is the durable state required to verify one refresh credential.
type WebAuthRefreshSession struct {
	User           User
	WebAuthSession WebAuthSession
	Token          WebAuthRefreshToken
}

// WebAuthRefreshRotation contains one atomic access/refresh pair rotation.
type WebAuthRefreshRotation struct {
	Selector                string
	PresentedVerifierDigest []byte
	Access                  WebAuthAccessCredential
	Refresh                 WebAuthRefreshToken
	RotatedAt               time.Time
	SuccessAudit            AuditEvent
	ReplayAudit             AuditEvent
}

// CredentialWebAuthSessionChange atomically replaces one credential, revokes all old
// session families, and creates the replacement normal session family.
type CredentialWebAuthSessionChange struct {
	UserID                    string
	ExpectedUserVersion       uint64
	ExpectedCredentialVersion uint64
	Credential                Credential
	Secret                    CredentialSecret
	RevokedAt                 time.Time
	WebAuthSession            WebAuthSession
	CredentialAudit           AuditEvent
	SessionAudit              AuditEvent
}

// WebAuthRefreshRotationResult is the non-secret outcome of one verified refresh attempt.
type WebAuthRefreshRotationResult string

const (
	// WebAuthRefreshRotationSucceeded means the old generation was consumed and a new pair was stored.
	WebAuthRefreshRotationSucceeded WebAuthRefreshRotationResult = "succeeded"
	// WebAuthRefreshRotationConcurrent means a recently consumed token was presented again.
	WebAuthRefreshRotationConcurrent WebAuthRefreshRotationResult = "concurrent"
	// WebAuthRefreshRotationReplayRevoked means a used generation was replayed and its family was revoked.
	WebAuthRefreshRotationReplayRevoked WebAuthRefreshRotationResult = "replay_revoked"
	// WebAuthRefreshRotationUnavailable means the selector, verifier, family, user, or credential is unusable.
	WebAuthRefreshRotationUnavailable WebAuthRefreshRotationResult = "unavailable"
)

// Store persists canonical users and security state without exposing plaintext credentials.
type Store interface {
	MFAStore
	CreateUser(ctx context.Context, user User, secret CredentialSecret, audit AuditEvent) error
	UpdateUser(ctx context.Context, user User, expectedVersion uint64, audit AuditEvent) error
	ChangeCredential(ctx context.Context, userID string, expectedUserVersion, expectedCredentialVersion uint64, credential Credential, secret CredentialSecret, revokedAt time.Time, audit AuditEvent) error
	ChangeCredentialAndCreateWebAuthSession(ctx context.Context, change CredentialWebAuthSessionChange) error
	GetUser(ctx context.Context, userID string) (User, bool, error)
	GetUserByNormalizedUsername(ctx context.Context, normalizedUsername string) (User, bool, error)
	GetUserByBinding(ctx context.Context, channelType, principal string) (User, bool, error)
	GetCredentialSecret(ctx context.Context, userID string) (CredentialSecret, bool, error)
	ListUsers(ctx context.Context, page PageRequest) (UserPage, error)

	CreateBindingClaim(ctx context.Context, claim BindingClaim, createdAt time.Time, audit AuditEvent) error
	AttachBinding(ctx context.Context, claimID string, binding Binding, consumedAt time.Time, audit AuditEvent) error
	CreateManagedBinding(ctx context.Context, binding Binding, expectedUserVersion uint64, audit AuditEvent) error
	DeleteBinding(ctx context.Context, userID, bindingID string, expectedUserVersion uint64, audit AuditEvent) error
	UpdateTelegramBindingProfile(ctx context.Context, principal, username, firstName string, updatedAt time.Time) (bool, error)

	CreateWebAuthSession(ctx context.Context, session WebAuthSession, audit AuditEvent) error
	GetWebAuthSession(ctx context.Context, webAuthSessionID string) (WebAuthSession, bool, error)
	GetWebAuthSessionByAccessSelector(ctx context.Context, selector string) (WebAuthAccessSession, bool, error)
	GetWebAuthSessionByRefreshSelector(ctx context.Context, selector string) (WebAuthRefreshSession, bool, error)
	ListWebAuthSessions(ctx context.Context, userID string, page PageRequest) (WebAuthSessionPage, error)
	// ListActiveWebAuthSessions excludes revoked and expired families and orders by the
	// recorded sign-in or refresh time, newest first.
	ListActiveWebAuthSessions(ctx context.Context, userID string, page PageRequest, now time.Time) (WebAuthSessionPage, error)
	RotateWebAuthRefresh(ctx context.Context, rotation WebAuthRefreshRotation) (WebAuthRefreshRotationResult, error)
	RevokeWebAuthSession(ctx context.Context, webAuthSessionID string, expectedVersion uint64, revokedAt time.Time, reason string, audit AuditEvent) error
	RevokeUserWebAuthSessions(ctx context.Context, userID string, revokedAt time.Time, reason string, audit AuditEvent) error
	DeleteExpiredWebAuthSessions(ctx context.Context, before time.Time, limit int) (int, error)

	ListAuditEvents(ctx context.Context, page PageRequest) (AuditPage, error)
}
