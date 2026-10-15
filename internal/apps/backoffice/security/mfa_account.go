package security

import (
	"context"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"github.com/baldaworks/balda/internal/apps/balda/userpassword"
)

// MFAStatus contains only safe Account status and dates, never verifier material.
type MFAStatus struct {
	Enabled    bool
	Available  bool
	CreatedAt  time.Time
	LastUsedAt time.Time
}

// MFAStatus returns this administrator's optional factor status.
func (s *Service) MFAStatus(ctx context.Context, token string) (MFAStatus, error) {
	p, err := s.accountAdministrator(ctx, token)
	if err != nil {
		return MFAStatus{}, err
	}
	profile, err := s.store.GetMFAProfile(ctx, p.User.ID)
	return MFAStatus{Enabled: profile.Enabled, Available: s.MFAAvailable(), CreatedAt: profile.Credential.CreatedAt, LastUsedAt: profile.Credential.LastUsedAt}, err
}

func (s *Service) accountAdministrator(ctx context.Context, token string) (Principal, error) {
	p, err := s.requireNormal(ctx, token)
	if err != nil {
		return Principal{}, err
	}
	if p.User.Role != usercmd.RoleAdministrator {
		return Principal{}, ErrForbidden
	}
	return p, nil
}

func (s *Service) confirmPassword(ctx context.Context, userID string, password []byte) error {
	provided := append([]byte(nil), password...)
	defer zero(provided)
	secret, found, err := s.store.GetCredentialSecret(ctx, userID)
	if err != nil {
		return err
	}
	if !found || !userpassword.Verify(secret.PasswordHash, provided) {
		return ErrUnauthenticated
	}
	return nil
}

// BeginEnable confirms the current password before issuing registration options.
func (s *Service) BeginEnable(ctx context.Context, token string, password []byte, browser, csrf string) (CeremonyStart, error) {
	p, err := s.accountAdministrator(ctx, token)
	if err != nil {
		return CeremonyStart{}, err
	}
	if err := s.confirmPassword(ctx, p.User.ID, password); err != nil {
		return CeremonyStart{}, err
	}
	profile, err := s.store.GetMFAProfile(ctx, p.User.ID)
	if err != nil {
		return CeremonyStart{}, err
	}
	if profile.Enabled {
		return CeremonyStart{}, ErrForbidden
	}
	return s.startMFA(ctx, p.User, profile, usercmd.MFAEnable, p.FamilyID, p.Version, browser, csrf, true)
}

// FinishEnable installs only a verified registration and one new verified family.
func (s *Service) FinishEnable(ctx context.Context, token, transaction, browser, csrf string, response []byte) (Credentials, error) {
	v, err := s.accountCeremony(ctx, token, transaction, browser, csrf, usercmd.MFAEnable, response)
	if err != nil {
		return Credentials{}, err
	}
	return s.changeFactor(ctx, v.user, v.ceremony.MFAVersion, v.ceremony.SessionID, v.state.SessionVersion, v.key, usercmd.MFAEnable, v.now)
}

// BeginReplace confirms the password and starts new-key registration.
func (s *Service) BeginReplace(ctx context.Context, token string, password []byte, confirmed bool, browser, csrf string) (CeremonyStart, error) {
	if !confirmed {
		return CeremonyStart{}, usercmd.ErrInvalid
	}
	p, err := s.accountAdministrator(ctx, token)
	if err != nil {
		return CeremonyStart{}, err
	}
	if err := s.confirmPassword(ctx, p.User.ID, password); err != nil {
		return CeremonyStart{}, err
	}
	profile, err := s.store.GetMFAProfile(ctx, p.User.ID)
	if err != nil {
		return CeremonyStart{}, err
	}
	if !profile.Enabled {
		return CeremonyStart{}, ErrForbidden
	}
	return s.startMFA(ctx, p.User, profile, usercmd.MFAReplace, p.FamilyID, p.Version, browser, csrf, true)
}

// FinishReplace commits only a verified new-key registration.
func (s *Service) FinishReplace(ctx context.Context, token, transaction, browser, csrf string, response []byte) (Credentials, error) {
	v, err := s.accountCeremony(ctx, token, transaction, browser, csrf, usercmd.MFAReplace, response)
	if err != nil {
		return Credentials{}, err
	}
	return s.changeFactor(ctx, v.user, v.ceremony.MFAVersion, v.ceremony.SessionID, v.state.SessionVersion, v.key, usercmd.MFAReplace, v.now)
}

// Disable removes the factor after password and explicit opt-out confirmation.
func (s *Service) Disable(ctx context.Context, token string, password []byte, confirmed bool) (Credentials, error) {
	if !confirmed {
		return Credentials{}, usercmd.ErrInvalid
	}
	p, err := s.accountAdministrator(ctx, token)
	if err != nil {
		return Credentials{}, err
	}
	if err := s.confirmPassword(ctx, p.User.ID, password); err != nil {
		return Credentials{}, err
	}
	profile, err := s.store.GetMFAProfile(ctx, p.User.ID)
	if err != nil {
		return Credentials{}, err
	}
	if !profile.Enabled {
		return Credentials{}, ErrForbidden
	}
	return s.changeFactor(ctx, p.User, profile.Version, p.FamilyID, p.Version, usercmd.MFACredential{}, usercmd.MFADisable, s.now().UTC())
}

func (s *Service) accountCeremony(ctx context.Context, token, transaction, browser, csrf string, purpose usercmd.MFAPurpose, response []byte) (verifiedMFA, error) {
	p, err := s.accountAdministrator(ctx, token)
	if err != nil {
		return verifiedMFA{}, err
	}
	v, err := s.finishMFA(ctx, transaction, browser, csrf, purpose, response)
	if err != nil || v.ceremony.UserID != p.User.ID || v.ceremony.SessionID != p.FamilyID || v.state.SessionVersion != p.Version {
		return verifiedMFA{}, ErrUnauthenticated
	}
	return v, nil
}

func (s *Service) changeFactor(ctx context.Context, u usercmd.User, mfaVersion uint64, sessionID string, sessionVersion uint64,
	key usercmd.MFACredential, purpose usercmd.MFAPurpose, now time.Time) (Credentials, error) {
	userVersion, credentialVersion := u.Version, u.Credential.Version
	u.Version++
	u.Credential.Version++
	credentials, family, err := s.newSession(ctx, u, usercmd.SessionAssuranceNormal, now)
	if err != nil {
		return Credentials{}, err
	}
	action := usercmd.AuditActionMFADisabled
	if purpose == usercmd.MFAEnable || purpose == usercmd.MFAReplace {
		family.MFAFactorID, family.WebAuthnVerifiedAt = key.ID, now
		if purpose == usercmd.MFAEnable {
			action = usercmd.AuditActionMFAEnabled
		} else {
			action = usercmd.AuditActionMFAReplaced
		}
	}
	audit := s.audit(action, usercmd.AuditTargetUser, u.ID, "optional factor changed", now)
	audit.ActorUserID, audit.ActorSessionID = u.ID, sessionID
	change := usercmd.MFAChange{UserID: u.ID, ExpectedUserVersion: userVersion, ExpectedCredentialVersion: credentialVersion,
		ExpectedMFAVersion: mfaVersion, Purpose: purpose, BoundSessionID: sessionID,
		ExpectedSessionVersion: sessionVersion, Credential: key, Session: &family, ChangedAt: now, Audit: audit}
	if err := s.store.ApplyMFAChange(ctx, change); err != nil {
		return Credentials{}, err
	}
	return credentials, nil
}
