package state

import (
	"context"
	"database/sql"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

// browserAuthority is the shared durable administrator/session fence used by
// unrelated management surfaces without sharing their public contracts.
type browserAuthority struct {
	userID, sessionID                                          string
	userVersion, credentialVersion, mfaVersion, sessionVersion uint64
	at                                                         time.Time
}

type lockedBrowserAuthority struct {
	family     usercmd.SessionFamily
	mfaEnabled bool
}

// Canonical user -> browser family is the same lock order as security writes.
func (s *sqlUserStore) checkBrowserAuthority(ctx context.Context, tx *sql.Tx, a browserAuthority) (lockedBrowserAuthority, error) {
	profile, err := s.mfaAuthority(ctx, tx, a.userID, a.userVersion, a.credentialVersion, a.mfaVersion)
	if err != nil {
		return lockedBrowserAuthority{}, err
	}
	family, err := s.liveMFASession(ctx, tx, a.sessionID, a.userID, a.credentialVersion, a.at)
	if err != nil {
		return lockedBrowserAuthority{}, err
	}
	if family.Assurance != usercmd.SessionAssuranceNormal {
		return lockedBrowserAuthority{}, usercmd.ErrForbidden
	}
	if family.Version != a.sessionVersion {
		return lockedBrowserAuthority{}, usercmd.ErrConflict
	}
	if profile.Enabled && family.MFAFactorID != profile.Credential.ID {
		return lockedBrowserAuthority{}, usercmd.ErrForbidden
	}
	locked := lockedBrowserAuthority{family: family, mfaEnabled: profile.Enabled}
	return locked, locked.checkTime(a)
}

// Recheck after row waits as well as before commit.
func (f lockedBrowserAuthority) checkTime(a browserAuthority) error {
	for _, now := range []time.Time{time.Now(), a.at} {
		if !now.Before(f.family.Access.ExpiresAt) || !now.Before(f.family.RefreshExpiresAt) {
			return usercmd.ErrForbidden
		}
		if f.mfaEnabled && (f.family.WebAuthnVerifiedAt.IsZero() || now.Before(f.family.WebAuthnVerifiedAt)) {
			return usercmd.ErrForbidden
		}
	}
	return nil
}
