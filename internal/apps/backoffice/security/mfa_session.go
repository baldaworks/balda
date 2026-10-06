package security

import (
	"context"

	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"github.com/baldaworks/balda/internal/apps/balda/users"
)

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

func (v verifiedMFA) verification(audit usercmd.AuditEvent) usercmd.MFAVerification {
	return usercmd.MFAVerification{UserID: v.user.ID, ExpectedUserVersion: v.ceremony.UserVersion,
		ExpectedCredentialVersion: v.ceremony.CredentialVersion, ExpectedMFAVersion: v.ceremony.MFAVersion,
		ExpectedSignCount: v.profile.Credential.SignCount, Credential: v.key, VerifiedAt: v.now, Audit: audit}
}
