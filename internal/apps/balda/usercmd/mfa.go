package usercmd

import (
	"context"
	"time"
)

// MFAProfile is the optional browser factor policy, independent of bot access.
type MFAProfile struct {
	UserID     string
	Enabled    bool
	Version    uint64
	Credential MFACredential
}

// MFACredential stores public WebAuthn material and verified authenticator state.
// Data is the reviewed library's credential encoding; it contains no private key.
type MFACredential struct {
	ID             string
	UserID         string
	RPID           string
	CredentialID   []byte
	PublicKey      []byte
	Data           []byte
	SignCount      uint32
	BackupEligible bool
	BackupState    bool
	CreatedAt      time.Time
	LastUsedAt     time.Time
}

// MFAPurpose binds a ceremony to one server-selected operation.
type MFAPurpose string

const (
	// MFAEnable registers an optional factor after password confirmation.
	MFAEnable MFAPurpose = "enable"
	// MFALogin verifies the second factor after password authentication.
	MFALogin MFAPurpose = "login"
	// MFAReplace registers a replacement for an existing factor.
	MFAReplace MFAPurpose = "replace"
	// MFADisable verifies removal of an optional factor.
	MFADisable MFAPurpose = "disable"
	// MFARecover is the explicitly confirmed offline removal operation.
	MFARecover MFAPurpose = "recover"
)

// Valid reports whether a supported MFA operation was selected.
func (p MFAPurpose) Valid() bool {
	return p == MFAEnable || p == MFALogin || p == MFAReplace || p == MFADisable || p == MFARecover
}

// MFACeremony is expiring library state bound to browser, user, purpose and authority.
type MFACeremony struct {
	TokenDigest       []byte
	BrowserDigest     []byte
	CSRFDigest        []byte
	UserID            string
	Purpose           MFAPurpose
	SessionID         string
	UserVersion       uint64
	CredentialVersion uint64
	MFAVersion        uint64
	Data              []byte
	CreatedAt         time.Time
	ExpiresAt         time.Time
}

// MFAChange atomically changes factor authority, revokes families and records audit.
type MFAChange struct {
	UserID                    string
	ExpectedUserVersion       uint64
	ExpectedCredentialVersion uint64
	ExpectedMFAVersion        uint64
	Purpose                   MFAPurpose
	BoundSessionID            string
	ExpectedSessionVersion    uint64
	Credential                MFACredential
	Session                   *SessionFamily
	ChangedAt                 time.Time
	Audit                     AuditEvent
}

// MFAVerification commits a verified counter update and optional login session.
type MFAVerification struct {
	UserID                    string
	ExpectedUserVersion       uint64
	ExpectedCredentialVersion uint64
	ExpectedMFAVersion        uint64
	ExpectedSignCount         uint32
	Credential                MFACredential
	Session                   *SessionFamily
	VerifiedAt                time.Time
	Audit                     AuditEvent
}

// MFAStore is the persistence contract for optional browser factors.
type MFAStore interface {
	GetMFAProfile(ctx context.Context, userID string) (MFAProfile, error)
	CreateMFACeremony(ctx context.Context, ceremony MFACeremony) error
	ConsumeMFACeremony(ctx context.Context, tokenDigest, browserDigest, csrfDigest []byte, purpose MFAPurpose, now time.Time) (MFACeremony, error)
	DeleteExpiredMFACeremonies(ctx context.Context, before time.Time, limit int) (int, error)
	ApplyMFAChange(ctx context.Context, change MFAChange) error
	VerifyMFACredential(ctx context.Context, verification MFAVerification) error
}
