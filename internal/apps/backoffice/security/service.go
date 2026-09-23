// Package security owns Backoffice browser authentication and session policy.
package security

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"github.com/baldaworks/balda/internal/apps/balda/userpassword"
	"github.com/baldaworks/balda/internal/apps/balda/users"
	"github.com/google/uuid"
)

const (
	selectorBytes = 18
	verifierBytes = 32
	csrfBytes     = 32
)

var (
	// ErrUnauthenticated is the uniform outward error for unusable browser credentials.
	ErrUnauthenticated = errors.New("authentication failed")
	// ErrForbidden reports valid authentication without sufficient assurance or CSRF proof.
	ErrForbidden = errors.New("request forbidden")
)

// Config controls opaque browser credential lifetimes.
type Config struct {
	AccessTTL  time.Duration
	RefreshTTL time.Duration
}

type store interface {
	GetUserByNormalizedUsername(ctx context.Context, normalizedUsername string) (usercmd.User, bool, error)
	GetCredentialSecret(ctx context.Context, userID string) (usercmd.CredentialSecret, bool, error)
	CreateSession(ctx context.Context, family usercmd.SessionFamily, audit usercmd.AuditEvent) error
	GetSession(ctx context.Context, sessionID string) (usercmd.SessionFamily, bool, error)
	GetSessionByAccessSelector(ctx context.Context, selector string) (usercmd.AccessSession, bool, error)
	GetSessionByRefreshSelector(ctx context.Context, selector string) (usercmd.RefreshSession, bool, error)
	ListSessions(ctx context.Context, userID string, page usercmd.PageRequest) (usercmd.SessionPage, error)
	RotateRefresh(ctx context.Context, rotation usercmd.RefreshRotation) (usercmd.RefreshRotationResult, error)
	RevokeSession(ctx context.Context, sessionID string, expectedVersion uint64, revokedAt time.Time, reason string, audit usercmd.AuditEvent) error
	ChangeCredentialAndCreateSession(ctx context.Context, change usercmd.CredentialSessionChange) error
}

// Credentials contains transient plaintext browser values returned only to cookie writers.
type Credentials struct {
	AccessToken      string
	RefreshToken     string
	CSRFToken        string
	AccessExpiresAt  time.Time
	RefreshExpiresAt time.Time
	Assurance        usercmd.SessionAssurance
}

// Principal is current canonical user and session-family authorization state.
type Principal struct {
	User      usercmd.User
	FamilyID  string
	Version   uint64
	Assurance usercmd.SessionAssurance
}

// Service authenticates passwords and manages opaque access/refresh families.
type Service struct {
	store  store
	config Config
	random io.Reader
	now    func() time.Time
	newID  func() string
}

// NewService creates browser security over the canonical user store.
func NewService(store store, config Config) (*Service, error) {
	if store == nil {
		return nil, fmt.Errorf("security store is required")
	}
	if config.AccessTTL <= 0 || config.RefreshTTL <= config.AccessTTL {
		return nil, fmt.Errorf("access TTL must be positive and shorter than refresh TTL")
	}
	if _, err := dummyPasswordHash(); err != nil {
		return nil, err
	}
	return &Service{store: store, config: config, random: rand.Reader, now: time.Now, newID: uuid.NewString}, nil
}

// Login verifies one bounded local credential with a uniform failure surface.
func (s *Service) Login(ctx context.Context, username string, password []byte) (Credentials, error) {
	provided := append([]byte(nil), password...)
	defer zero(provided)
	normalized := users.NormalizeUsername(username)
	user, found, err := s.store.GetUserByNormalizedUsername(ctx, normalized)
	if err != nil {
		return Credentials{}, fmt.Errorf("load login user: %w", err)
	}
	hash, err := dummyPasswordHash()
	if err != nil {
		return Credentials{}, err
	}
	if found {
		secret, secretFound, secretErr := s.store.GetCredentialSecret(ctx, user.ID)
		if secretErr != nil {
			return Credentials{}, fmt.Errorf("load login credential: %w", secretErr)
		}
		if secretFound {
			hash = secret.PasswordHash
		}
	}
	verified := userpassword.Verify(hash, provided)
	assurance, assuranceErr := users.AuthenticationAssurance(user)
	if !found || !verified || assuranceErr != nil {
		return Credentials{}, ErrUnauthenticated
	}
	return s.issueSession(ctx, user, assurance, s.now().UTC())
}

// ValidateAccess resolves current user policy for one opaque access token.
func (s *Service) ValidateAccess(ctx context.Context, rawToken string) (Principal, error) {
	selector, verifier, err := parseOpaqueToken(rawToken)
	if err != nil {
		return Principal{}, ErrUnauthenticated
	}
	session, found, err := s.store.GetSessionByAccessSelector(ctx, selector)
	if err != nil {
		return Principal{}, fmt.Errorf("load access session: %w", err)
	}
	now := s.now().UTC()
	if !found || subtle.ConstantTimeCompare(session.Family.Access.VerifierDigest, digest(verifier)) != 1 ||
		!now.Before(session.Family.Access.ExpiresAt) || !now.Before(session.Family.RefreshExpiresAt) ||
		!session.Family.RevokedAt.IsZero() || session.User.Status != usercmd.StatusActive ||
		session.User.Credential.State == usercmd.CredentialStateDisabled ||
		session.User.Credential.Version != session.Family.CredentialVersion {
		return Principal{}, ErrUnauthenticated
	}
	wantAssurance, err := users.AuthenticationAssurance(session.User)
	if err != nil || wantAssurance != session.Family.Assurance {
		return Principal{}, ErrUnauthenticated
	}
	return Principal{
		User: session.User, FamilyID: session.Family.ID,
		Version: session.Family.Version, Assurance: session.Family.Assurance,
	}, nil
}

// ValidateCSRF binds an authenticated mutation to the session-family CSRF secret.
func (s *Service) ValidateCSRF(ctx context.Context, rawAccessToken, csrfToken string) error {
	selector, verifier, err := parseOpaqueToken(rawAccessToken)
	if err != nil {
		return ErrUnauthenticated
	}
	session, found, err := s.store.GetSessionByAccessSelector(ctx, selector)
	if err != nil {
		return fmt.Errorf("load access session for CSRF validation: %w", err)
	}
	if !found || subtle.ConstantTimeCompare(session.Family.Access.VerifierDigest, digest(verifier)) != 1 {
		return ErrUnauthenticated
	}
	if csrfToken == "" || subtle.ConstantTimeCompare(session.Family.CSRFVerifierDigest, digest(csrfToken)) != 1 {
		return ErrForbidden
	}
	_, err = s.ValidateAccess(ctx, rawAccessToken)
	return err
}

// Refresh atomically consumes one refresh generation and rotates both opaque credentials.
func (s *Service) Refresh(ctx context.Context, rawToken, csrfToken string) (Credentials, error) {
	selector, verifier, err := parseOpaqueToken(rawToken)
	if err != nil {
		return Credentials{}, ErrUnauthenticated
	}
	session, found, err := s.store.GetSessionByRefreshSelector(ctx, selector)
	if err != nil {
		return Credentials{}, fmt.Errorf("load refresh session: %w", err)
	}
	if !found {
		return Credentials{}, ErrUnauthenticated
	}
	if subtle.ConstantTimeCompare(session.Family.CSRFVerifierDigest, digest(csrfToken)) != 1 {
		return Credentials{}, ErrForbidden
	}
	now := s.now().UTC()
	if !now.Before(session.Family.RefreshExpiresAt) || !now.Add(s.config.AccessTTL).Before(session.Family.RefreshExpiresAt) {
		return Credentials{}, ErrUnauthenticated
	}
	accessRaw, access, err := s.newAccess(now.Add(s.config.AccessTTL))
	if err != nil {
		return Credentials{}, err
	}
	refreshRaw, refresh, err := s.newRefresh(session.Token.Generation+1, now, session.Family.RefreshExpiresAt)
	if err != nil {
		return Credentials{}, err
	}
	rotation := usercmd.RefreshRotation{
		Selector: selector, PresentedVerifierDigest: digest(verifier), Access: access, Refresh: refresh, RotatedAt: now,
		SuccessAudit: s.audit(usercmd.AuditActionRefreshSucceeded, usercmd.AuditTargetSession, session.Family.ID, "refresh rotated", now),
		ReplayAudit:  s.audit(usercmd.AuditActionRefreshReplay, usercmd.AuditTargetSession, session.Family.ID, "refresh replay", now),
	}
	rotation.SuccessAudit.ActorUserID = session.User.ID
	rotation.SuccessAudit.ActorSessionID = session.Family.ID
	rotation.ReplayAudit.ActorUserID = session.User.ID
	rotation.ReplayAudit.ActorSessionID = session.Family.ID
	rotation.ReplayAudit.Outcome = usercmd.AuditOutcomeDenied
	result, err := s.store.RotateRefresh(ctx, rotation)
	if err != nil {
		return Credentials{}, fmt.Errorf("rotate refresh credential: %w", err)
	}
	if result != usercmd.RefreshRotationSucceeded {
		return Credentials{}, ErrUnauthenticated
	}
	return Credentials{
		AccessToken: accessRaw, RefreshToken: refreshRaw, CSRFToken: csrfToken,
		AccessExpiresAt: access.ExpiresAt, RefreshExpiresAt: session.Family.RefreshExpiresAt,
		Assurance: session.Family.Assurance,
	}, nil
}

// Logout revokes the authenticated session family and all refresh generations.
func (s *Service) Logout(ctx context.Context, rawAccessToken string) error {
	principal, err := s.ValidateAccess(ctx, rawAccessToken)
	if err != nil {
		return err
	}
	now := s.now().UTC()
	audit := s.audit(usercmd.AuditActionLogout, usercmd.AuditTargetSession, principal.FamilyID, "browser logout", now)
	audit.ActorUserID = principal.User.ID
	audit.ActorSessionID = principal.FamilyID
	if err := s.store.RevokeSession(ctx, principal.FamilyID, principal.Version, now, "logout", audit); err != nil {
		return fmt.Errorf("revoke logout session: %w", err)
	}
	return nil
}

// ListSessions returns secret-free session summaries for the current user or,
// for administrators, another canonical user.
func (s *Service) ListSessions(ctx context.Context, rawAccessToken, userID string, page usercmd.PageRequest) (usercmd.SessionPage, error) {
	principal, err := s.requireNormal(ctx, rawAccessToken)
	if err != nil {
		return usercmd.SessionPage{}, err
	}
	targetUserID := strings.TrimSpace(userID)
	if targetUserID == "" {
		targetUserID = principal.User.ID
	}
	if targetUserID != principal.User.ID && principal.User.Role != usercmd.RoleAdministrator {
		return usercmd.SessionPage{}, ErrForbidden
	}
	pageResult, err := s.store.ListSessions(ctx, targetUserID, page)
	if err != nil {
		return usercmd.SessionPage{}, fmt.Errorf("list browser sessions: %w", err)
	}
	return pageResult, nil
}

// RevokeSession revokes an owned session or any session when called by an administrator.
func (s *Service) RevokeSession(ctx context.Context, rawAccessToken, targetSessionID string, confirmCurrent bool) error {
	principal, err := s.requireNormal(ctx, rawAccessToken)
	if err != nil {
		return err
	}
	target, found, err := s.store.GetSession(ctx, strings.TrimSpace(targetSessionID))
	if err != nil {
		return fmt.Errorf("load session for revocation: %w", err)
	}
	if !found {
		return usercmd.ErrNotFound
	}
	if target.UserID != principal.User.ID && principal.User.Role != usercmd.RoleAdministrator {
		return ErrForbidden
	}
	if err := users.ValidateSessionRevocation(principal.FamilyID, target.ID, confirmCurrent); err != nil {
		return err
	}
	now := s.now().UTC()
	audit := s.audit(usercmd.AuditActionSessionRevoked, usercmd.AuditTargetSession, target.ID, "browser session revoked", now)
	audit.ActorUserID = principal.User.ID
	audit.ActorSessionID = principal.FamilyID
	if err := s.store.RevokeSession(ctx, target.ID, target.Version, now, "administrative revocation", audit); err != nil {
		return fmt.Errorf("revoke browser session: %w", err)
	}
	return nil
}

// ReplacePassword rotates a normal password or replaces a verified temporary credential.
func (s *Service) ReplacePassword(ctx context.Context, rawAccessToken string, currentPassword, nextPassword []byte) (Credentials, error) {
	current := append([]byte(nil), currentPassword...)
	next := append([]byte(nil), nextPassword...)
	defer zero(current)
	defer zero(next)
	principal, err := s.ValidateAccess(ctx, rawAccessToken)
	if err != nil {
		return Credentials{}, err
	}
	secret, found, err := s.store.GetCredentialSecret(ctx, principal.User.ID)
	if err != nil {
		return Credentials{}, fmt.Errorf("load current credential: %w", err)
	}
	if !found || !userpassword.Verify(secret.PasswordHash, current) {
		return Credentials{}, ErrUnauthenticated
	}
	hash, err := userpassword.Hash(next)
	if err != nil {
		if errors.Is(err, userpassword.ErrInvalidPassword) {
			return Credentials{}, fmt.Errorf("%w: replacement password length is invalid", usercmd.ErrInvalid)
		}
		return Credentials{}, err
	}
	now := s.now().UTC()
	credential := usercmd.Credential{State: usercmd.CredentialStateActive, Version: principal.User.Credential.Version + 1}
	updatedUser := principal.User
	updatedUser.Credential = credential
	updatedUser.Version++
	updatedUser.UpdatedAt = now
	credentials, family, err := s.newSession(updatedUser, usercmd.SessionAssuranceNormal, now)
	if err != nil {
		return Credentials{}, err
	}
	credentialAudit := s.audit(usercmd.AuditActionCredentialChanged, usercmd.AuditTargetUser, principal.User.ID, "password replaced", now)
	credentialAudit.ActorUserID = principal.User.ID
	credentialAudit.ActorSessionID = principal.FamilyID
	sessionAudit := s.audit(usercmd.AuditActionLoginSucceeded, usercmd.AuditTargetSession, family.ID, "password replacement session", now)
	sessionAudit.ActorUserID = principal.User.ID
	sessionAudit.ActorSessionID = family.ID
	change := usercmd.CredentialSessionChange{
		UserID: principal.User.ID, ExpectedUserVersion: principal.User.Version,
		ExpectedCredentialVersion: principal.User.Credential.Version,
		Credential:                credential, Secret: usercmd.CredentialSecret{UserID: principal.User.ID, PasswordHash: hash},
		RevokedAt: now, Session: family, CredentialAudit: credentialAudit, SessionAudit: sessionAudit,
	}
	if err := s.store.ChangeCredentialAndCreateSession(ctx, change); err != nil {
		return Credentials{}, fmt.Errorf("replace password: %w", err)
	}
	return credentials, nil
}

func (s *Service) requireNormal(ctx context.Context, rawAccessToken string) (Principal, error) {
	principal, err := s.ValidateAccess(ctx, rawAccessToken)
	if err != nil {
		return Principal{}, err
	}
	if principal.Assurance != usercmd.SessionAssuranceNormal {
		return Principal{}, ErrForbidden
	}
	return principal, nil
}

func (s *Service) issueSession(ctx context.Context, user usercmd.User, assurance usercmd.SessionAssurance, now time.Time) (Credentials, error) {
	credentials, family, err := s.newSession(user, assurance, now)
	if err != nil {
		return Credentials{}, err
	}
	audit := s.audit(usercmd.AuditActionLoginSucceeded, usercmd.AuditTargetSession, family.ID, "password login", now)
	audit.ActorUserID = user.ID
	audit.ActorSessionID = family.ID
	if err := s.store.CreateSession(ctx, family, audit); err != nil {
		return Credentials{}, fmt.Errorf("create browser session: %w", err)
	}
	return credentials, nil
}

func (s *Service) newSession(user usercmd.User, assurance usercmd.SessionAssurance, now time.Time) (Credentials, usercmd.SessionFamily, error) {
	accessRaw, access, err := s.newAccess(now.Add(s.config.AccessTTL))
	if err != nil {
		return Credentials{}, usercmd.SessionFamily{}, err
	}
	refreshExpiresAt := now.Add(s.config.RefreshTTL)
	refreshRaw, refresh, err := s.newRefresh(1, now, refreshExpiresAt)
	if err != nil {
		return Credentials{}, usercmd.SessionFamily{}, err
	}
	csrfRaw, err := randomValue(s.random, csrfBytes)
	if err != nil {
		return Credentials{}, usercmd.SessionFamily{}, fmt.Errorf("generate CSRF credential: %w", err)
	}
	family := usercmd.SessionFamily{
		ID: s.newID(), UserID: user.ID, Assurance: assurance, CredentialVersion: user.Credential.Version,
		Access: access, CSRFVerifierDigest: digest(csrfRaw), CreatedAt: now, LastSeenAt: now,
		RefreshExpiresAt: refreshExpiresAt, Version: 1, RefreshTokens: []usercmd.RefreshToken{refresh},
	}
	credentials := Credentials{
		AccessToken: accessRaw, RefreshToken: refreshRaw, CSRFToken: csrfRaw,
		AccessExpiresAt: access.ExpiresAt, RefreshExpiresAt: refreshExpiresAt, Assurance: assurance,
	}
	return credentials, family, nil
}

func (s *Service) newAccess(expiresAt time.Time) (string, usercmd.AccessCredential, error) {
	raw, selector, verifier, err := newOpaqueToken(s.random)
	if err != nil {
		return "", usercmd.AccessCredential{}, fmt.Errorf("generate access credential: %w", err)
	}
	return raw, usercmd.AccessCredential{Selector: selector, VerifierDigest: digest(verifier), ExpiresAt: expiresAt}, nil
}

func (s *Service) newRefresh(generation uint64, issuedAt, expiresAt time.Time) (string, usercmd.RefreshToken, error) {
	raw, selector, verifier, err := newOpaqueToken(s.random)
	if err != nil {
		return "", usercmd.RefreshToken{}, fmt.Errorf("generate refresh credential: %w", err)
	}
	return raw, usercmd.RefreshToken{
		Selector: selector, VerifierDigest: digest(verifier), Generation: generation,
		State: usercmd.RefreshTokenStateActive, IssuedAt: issuedAt, ExpiresAt: expiresAt,
	}, nil
}

func (s *Service) audit(action usercmd.AuditAction, targetType usercmd.AuditTargetType, targetID, reason string, now time.Time) usercmd.AuditEvent {
	return usercmd.AuditEvent{
		ID: s.newID(), Action: action, Outcome: usercmd.AuditOutcomeSucceeded,
		TargetType: targetType, TargetID: targetID, Reason: reason, Source: "backoffice-security", OccurredAt: now,
	}
}

func newOpaqueToken(random io.Reader) (raw, selector, verifier string, err error) {
	selector, err = randomValue(random, selectorBytes)
	if err != nil {
		return "", "", "", err
	}
	verifier, err = randomValue(random, verifierBytes)
	if err != nil {
		return "", "", "", err
	}
	return selector + "." + verifier, selector, verifier, nil
}

func parseOpaqueToken(raw string) (selector, verifier string, err error) {
	trimmed := strings.TrimSpace(raw)
	selector, verifier, found := strings.Cut(trimmed, ".")
	if !found || selector == "" || verifier == "" || strings.Contains(verifier, ".") {
		return "", "", ErrUnauthenticated
	}
	if len(selector) != base64.RawURLEncoding.EncodedLen(selectorBytes) || len(verifier) != base64.RawURLEncoding.EncodedLen(verifierBytes) {
		return "", "", ErrUnauthenticated
	}
	selectorValue, err := base64.RawURLEncoding.DecodeString(selector)
	if err != nil || len(selectorValue) != selectorBytes {
		return "", "", ErrUnauthenticated
	}
	verifierValue, err := base64.RawURLEncoding.DecodeString(verifier)
	if err != nil || len(verifierValue) != verifierBytes {
		return "", "", ErrUnauthenticated
	}
	return selector, verifier, nil
}

func randomValue(random io.Reader, size int) (string, error) {
	value := make([]byte, size)
	if _, err := io.ReadFull(random, value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func digest(value string) []byte {
	sum := sha256.Sum256([]byte(value))
	return sum[:]
}

func zero(value []byte) {
	for i := range value {
		value[i] = 0
	}
}

var (
	dummyHashOnce sync.Once
	dummyHash     string
	dummyHashErr  error
)

func dummyPasswordHash() (string, error) {
	dummyHashOnce.Do(func() {
		dummyHash, dummyHashErr = userpassword.Hash([]byte("uniform authentication dummy password"))
	})
	return dummyHash, dummyHashErr
}
