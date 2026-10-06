package security

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	libwebauthn "github.com/go-webauthn/webauthn/webauthn"
)

// CeremonyStart exposes public browser options and an opaque single-use handle.
type CeremonyStart struct {
	Transaction string `json:"transaction"`
	Options     any    `json:"options"`
}

type ceremonyState struct {
	Library        libwebauthn.SessionData
	SessionVersion uint64
	Registration   bool
}

func (s *Service) startMFA(ctx context.Context, user usercmd.User, profile usercmd.MFAProfile, purpose usercmd.MFAPurpose,
	sessionID string, sessionVersion uint64, browser, csrf string, registration bool) (CeremonyStart, error) {
	if s.webauthn == nil {
		return CeremonyStart{}, ErrMFAUnavailable
	}
	if browser == "" || csrf == "" {
		return CeremonyStart{}, ErrForbidden
	}
	if !validCeremonyKind(purpose, registration) {
		return CeremonyStart{}, ErrForbidden
	}
	var options any
	var state *libwebauthn.SessionData
	var err error
	if registration {
		options, state, err = s.webauthn.beginRegistration(user)
	} else {
		options, state, err = s.webauthn.beginAssertion(user, profile.Credential)
	}
	if err != nil {
		return CeremonyStart{}, ErrUnauthenticated
	}
	token, err := randomValue(s.random, 32)
	if err != nil {
		return CeremonyStart{}, err
	}
	now := s.now().UTC()
	state.Expires = now.Add(s.config.CeremonyTTL)
	data, err := json.Marshal(ceremonyState{Library: *state, SessionVersion: sessionVersion, Registration: registration})
	if err != nil {
		return CeremonyStart{}, err
	}
	c := usercmd.MFACeremony{TokenDigest: digest(token), BrowserDigest: digest(browser), CSRFDigest: digest(csrf),
		UserID: user.ID, Purpose: purpose, SessionID: sessionID, UserVersion: user.Version, CredentialVersion: user.Credential.Version,
		MFAVersion: profile.Version, Data: data, CreatedAt: now, ExpiresAt: state.Expires}
	if err := s.store.CreateMFACeremony(ctx, c); err != nil {
		return CeremonyStart{}, err
	}
	// Cleanup is bounded; failure to clean old proofs cannot grant authorization.
	_, _ = s.store.DeleteExpiredMFACeremonies(ctx, now, 100)
	return CeremonyStart{Transaction: token, Options: options}, nil
}

type verifiedMFA struct {
	ceremony usercmd.MFACeremony
	state    ceremonyState
	user     usercmd.User
	profile  usercmd.MFAProfile
	key      usercmd.MFACredential
	now      time.Time
}

func (s *Service) finishMFA(ctx context.Context, transaction, browser, csrf string, purpose usercmd.MFAPurpose, response []byte) (verifiedMFA, error) {
	var v verifiedMFA
	raw, err := base64.RawURLEncoding.DecodeString(transaction)
	if err != nil || len(raw) != 32 || browser == "" || csrf == "" || s.webauthn == nil {
		return v, ErrUnauthenticated
	}
	v.now = s.now().UTC()
	v.ceremony, err = s.store.ConsumeMFACeremony(ctx, digest(transaction), digest(browser), digest(csrf), purpose, v.now)
	if err != nil || json.Unmarshal(v.ceremony.Data, &v.state) != nil || !validCeremonyKind(purpose, v.state.Registration) {
		return v, ErrUnauthenticated
	}
	v.user, _, err = s.store.GetUser(ctx, v.ceremony.UserID)
	if err != nil || v.user.Version != v.ceremony.UserVersion || v.user.Credential.Version != v.ceremony.CredentialVersion {
		return v, ErrUnauthenticated
	}
	v.profile, err = s.store.GetMFAProfile(ctx, v.user.ID)
	if err != nil || v.profile.Version != v.ceremony.MFAVersion {
		return v, ErrUnauthenticated
	}
	key, err := s.webauthn.verify(v.user, v.profile.Credential, v.state.Library, response, v.state.Registration)
	if err != nil {
		return v, ErrUnauthenticated
	}
	factorID, created := v.profile.Credential.ID, v.profile.Credential.CreatedAt
	if v.state.Registration {
		factorID, created = s.newID(), v.now
	}
	v.key, err = s.webauthn.credential(v.user.ID, factorID, key, created, v.now)
	return v, err
}

func validCeremonyKind(purpose usercmd.MFAPurpose, registration bool) bool {
	if registration {
		return purpose == usercmd.MFAEnable || purpose == usercmd.MFAReplace
	}
	return purpose == usercmd.MFALogin
}
