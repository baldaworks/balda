package security

import (
	"context"
	"errors"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"github.com/baldaworks/balda/internal/apps/balda/users"
)

// ErrStepUp requires an explicit new assertion before sensitive access.
var ErrStepUp = errors.New("fresh WebAuthn verification required")

type mfaBrowserKey struct{}
type mfaBrowserBinding struct{ browser, csrf string }

func withMFABrowser(ctx context.Context, browser, csrf string) context.Context {
	return context.WithValue(ctx, mfaBrowserKey{}, mfaBrowserBinding{browser: browser, csrf: csrf})
}

// MFAAvailable reports whether this configured public origin supports WebAuthn.
func (s *Service) MFAAvailable() bool { return s.webauthn != nil }

func (s *Service) checkMFAFamily(ctx context.Context, user usercmd.User, family usercmd.SessionFamily) (usercmd.MFAProfile, error) {
	if user.Role != usercmd.RoleAdministrator {
		return usercmd.MFAProfile{}, nil
	}
	p, err := s.store.GetMFAProfile(ctx, user.ID)
	if err != nil {
		return usercmd.MFAProfile{}, err
	}
	if !p.Enabled {
		return p, nil
	}
	if s.webauthn == nil || p.Credential.RPID != s.webauthn.rpID {
		return p, ErrMFAUnavailable
	}
	if family.MFAFactorID != p.Credential.ID || family.WebAuthnVerifiedAt.IsZero() {
		return p, ErrUnauthenticated
	}
	return p, nil
}

// RequireFresh checks optional administrator policy without changing session assurance.
func (s *Service) RequireFresh(ctx context.Context, accessToken string) error {
	p, err := s.ValidateAccess(ctx, accessToken)
	if err != nil {
		return err
	}
	if !p.MFAEnabled {
		return nil
	}
	now := s.now().UTC()
	if now.Before(p.WebAuthnVerifiedAt) || !now.Before(p.WebAuthnVerifiedAt.Add(s.config.StepUpTTL)) {
		return ErrStepUp
	}
	return nil
}

// FinishLogin grants a browser family only after the pending second factor verifies.
func (s *Service) FinishLogin(ctx context.Context, transaction, browser, csrf string, response []byte) (Credentials, error) {
	v, err := s.finishMFA(ctx, transaction, browser, csrf, usercmd.MFALogin, response)
	if err != nil {
		return Credentials{}, err
	}
	assurance, err := users.AuthenticationAssurance(v.user)
	if err != nil {
		return Credentials{}, ErrUnauthenticated
	}
	credentials, family, err := s.newSession(ctx, v.user, assurance, v.now)
	if err != nil {
		return Credentials{}, err
	}
	family.MFAFactorID, family.WebAuthnVerifiedAt = v.key.ID, v.now
	audit := s.audit(usercmd.AuditActionLoginSucceeded, usercmd.AuditTargetSession, family.ID, "password and WebAuthn login", v.now)
	audit.ActorUserID, audit.ActorSessionID = v.user.ID, family.ID
	verification := v.verification(audit)
	verification.Session = &family
	if err := s.store.VerifyMFACredential(ctx, verification); err != nil {
		return Credentials{}, ErrUnauthenticated
	}
	return credentials, nil
}

// BeginStepUp starts an explicit assertion bound to the current normal family.
func (s *Service) BeginStepUp(ctx context.Context, accessToken, browser, csrf string) (CeremonyStart, error) {
	p, err := s.requireNormal(ctx, accessToken)
	if err != nil {
		return CeremonyStart{}, err
	}
	if !p.MFAEnabled {
		return CeremonyStart{}, ErrForbidden
	}
	profile, err := s.store.GetMFAProfile(ctx, p.User.ID)
	if err != nil {
		return CeremonyStart{}, err
	}
	return s.startMFA(ctx, p.User, profile, usercmd.MFAStepUp, p.FamilyID, p.Version, browser, csrf, false)
}

// FinishStepUp rotates only access authority; the refresh lineage and deadline remain unchanged.
func (s *Service) FinishStepUp(ctx context.Context, accessToken, transaction, browser, csrf string, response []byte) (Credentials, error) {
	p, err := s.requireNormal(ctx, accessToken)
	if err != nil {
		return Credentials{}, err
	}
	v, err := s.finishMFA(ctx, transaction, browser, csrf, usercmd.MFAStepUp, response)
	if err != nil || v.ceremony.UserID != p.User.ID || v.ceremony.SessionID != p.FamilyID || v.state.SessionVersion != p.Version {
		return Credentials{}, ErrUnauthenticated
	}
	family, found, err := s.store.GetSession(ctx, p.FamilyID)
	if err != nil || !found {
		return Credentials{}, ErrUnauthenticated
	}
	expires := v.now.Add(s.config.AccessTTL)
	if !expires.Before(family.RefreshExpiresAt) {
		expires = family.RefreshExpiresAt.Add(-time.Nanosecond)
	}
	if !expires.After(v.now) {
		return Credentials{}, ErrUnauthenticated
	}
	raw, access, err := s.newAccess(expires)
	if err != nil {
		return Credentials{}, err
	}
	audit := s.audit(usercmd.AuditActionMFAVerified, usercmd.AuditTargetSession, p.FamilyID, "WebAuthn step-up", v.now)
	audit.ActorUserID, audit.ActorSessionID = p.User.ID, p.FamilyID
	verification := v.verification(audit)
	verification.SessionID, verification.ExpectedSessionVersion, verification.Access = p.FamilyID, p.Version, access
	if err := s.store.VerifyMFACredential(ctx, verification); err != nil {
		return Credentials{}, ErrUnauthenticated
	}
	return Credentials{AccessToken: raw, CSRFToken: csrf, AccessExpiresAt: access.ExpiresAt, RefreshExpiresAt: family.RefreshExpiresAt, Assurance: family.Assurance}, nil
}

func (v verifiedMFA) verification(audit usercmd.AuditEvent) usercmd.MFAVerification {
	return usercmd.MFAVerification{UserID: v.user.ID, ExpectedUserVersion: v.ceremony.UserVersion,
		ExpectedCredentialVersion: v.ceremony.CredentialVersion, ExpectedMFAVersion: v.ceremony.MFAVersion,
		ExpectedSignCount: v.profile.Credential.SignCount, Credential: v.key, VerifiedAt: v.now, Audit: audit}
}
