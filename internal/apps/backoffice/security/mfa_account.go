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
	return s.changeFactor(ctx, v, usercmd.MFAEnable)
}

// BeginReplace requires a new assertion from the current key before registration.
func (s *Service) BeginReplace(ctx context.Context, token string, confirmed bool, browser, csrf string) (CeremonyStart, error) {
	if !confirmed {
		return CeremonyStart{}, usercmd.ErrInvalid
	}
	p, err := s.accountAdministrator(ctx, token)
	if err != nil {
		return CeremonyStart{}, err
	}
	profile, err := s.store.GetMFAProfile(ctx, p.User.ID)
	if err != nil {
		return CeremonyStart{}, err
	}
	if !profile.Enabled {
		return CeremonyStart{}, ErrForbidden
	}
	return s.startMFA(ctx, p.User, profile, usercmd.MFAReplace, p.FamilyID, p.Version, browser, csrf, false)
}

// MFAReplacement is either the next registration or the completed session replacement.
type MFAReplacement struct {
	Next        *CeremonyStart
	Credentials Credentials
}

// FinishReplace verifies the old key, then accepts exactly one verified new registration.
func (s *Service) FinishReplace(ctx context.Context, token, transaction, browser, csrf string, confirmed bool, response []byte) (MFAReplacement, error) {
	if !confirmed {
		return MFAReplacement{}, usercmd.ErrInvalid
	}
	v, err := s.accountCeremony(ctx, token, transaction, browser, csrf, usercmd.MFAReplace, response)
	if err != nil {
		return MFAReplacement{}, err
	}
	if v.state.Registration {
		credentials, err := s.changeFactor(ctx, v, usercmd.MFAReplace)
		return MFAReplacement{Credentials: credentials}, err
	}
	audit := s.factorAudit(v, usercmd.AuditActionMFAVerified, "replacement factor verified")
	if err := s.store.VerifyMFACredential(ctx, v.verification(audit)); err != nil {
		return MFAReplacement{}, ErrUnauthenticated
	}
	profile, err := s.store.GetMFAProfile(ctx, v.user.ID)
	if err != nil {
		return MFAReplacement{}, err
	}
	next, err := s.startMFA(ctx, v.user, profile, usercmd.MFAReplace, v.ceremony.SessionID, v.state.SessionVersion, browser, csrf, true,
		replacementProof{FactorID: v.key.ID, VerifiedAt: v.now})
	return MFAReplacement{Next: &next}, err
}

// BeginDisable requires the current password and explicit opt-out confirmation.
func (s *Service) BeginDisable(ctx context.Context, token string, password []byte, confirmed bool, browser, csrf string) (CeremonyStart, error) {
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
	return s.startMFA(ctx, p.User, profile, usercmd.MFADisable, p.FamilyID, p.Version, browser, csrf, false)
}

// FinishDisable verifies the purpose-bound assertion before disabling and revoking authority.
func (s *Service) FinishDisable(ctx context.Context, token, transaction, browser, csrf string, confirmed bool, response []byte) (Credentials, error) {
	if !confirmed {
		return Credentials{}, usercmd.ErrInvalid
	}
	v, err := s.accountCeremony(ctx, token, transaction, browser, csrf, usercmd.MFADisable, response)
	if err != nil {
		return Credentials{}, err
	}
	if err := s.store.VerifyMFACredential(ctx, v.verification(s.factorAudit(v, usercmd.AuditActionMFAVerified, "disable factor verified"))); err != nil {
		return Credentials{}, ErrUnauthenticated
	}
	return s.changeFactor(ctx, v, usercmd.MFADisable)
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

func (s *Service) changeFactor(ctx context.Context, v verifiedMFA, purpose usercmd.MFAPurpose) (Credentials, error) {
	u := v.user
	u.Version++
	u.Credential.Version++
	credentials, family, err := s.newSession(ctx, u, usercmd.SessionAssuranceNormal, v.now)
	if err != nil {
		return Credentials{}, err
	}
	action := usercmd.AuditActionMFADisabled
	if purpose == usercmd.MFAEnable || purpose == usercmd.MFAReplace {
		family.MFAFactorID, family.WebAuthnVerifiedAt = v.key.ID, v.now
		if purpose == usercmd.MFAEnable {
			action = usercmd.AuditActionMFAEnabled
		} else {
			action = usercmd.AuditActionMFAReplaced
		}
	}
	change := usercmd.MFAChange{UserID: u.ID, ExpectedUserVersion: v.ceremony.UserVersion, ExpectedCredentialVersion: v.ceremony.CredentialVersion,
		ExpectedMFAVersion: v.ceremony.MFAVersion, Purpose: purpose, BoundSessionID: v.ceremony.SessionID,
		ExpectedSessionVersion: v.state.SessionVersion, Credential: v.key, Session: &family, ChangedAt: v.now,
		Audit: s.factorAudit(v, action, "optional factor changed")}
	if err := s.store.ApplyMFAChange(ctx, change); err != nil {
		return Credentials{}, err
	}
	return credentials, nil
}

func (s *Service) factorAudit(v verifiedMFA, action usercmd.AuditAction, reason string) usercmd.AuditEvent {
	audit := s.audit(action, usercmd.AuditTargetUser, v.user.ID, reason, v.now)
	audit.ActorUserID, audit.ActorSessionID = v.user.ID, v.ceremony.SessionID
	return audit
}
