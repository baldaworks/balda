package security

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"github.com/go-webauthn/webauthn/protocol"
	libwebauthn "github.com/go-webauthn/webauthn/webauthn"
)

// ErrMFAUnavailable means this browser origin cannot perform WebAuthn.
var ErrMFAUnavailable = errors.New("WebAuthn requires an HTTPS domain or http://localhost")

const (
	maxWebAuthnBody = 64 << 10
	httpsScheme     = "https"
)

type webAuthnEngine struct {
	library *libwebauthn.WebAuthn
	origin  string
	rpID    string
}

// Adapted from AlaTooGuide's reviewed v0.18.1 adapter. Balda derives the RP
// from its exact configured origin rather than accepting parent-domain aliases.
func newWebAuthnEngine(origin string) (*webAuthnEngine, error) {
	u, err := url.Parse(origin)
	if err != nil || u.Hostname() == "" || net.ParseIP(u.Hostname()) != nil || u.User != nil ||
		u.Path != "" || u.RawQuery != "" || u.Fragment != "" ||
		(u.Scheme != httpsScheme && (u.Scheme != "http" || u.Hostname() != "localhost")) {
		return nil, ErrMFAUnavailable
	}
	library, err := libwebauthn.New(&libwebauthn.Config{RPDisplayName: "Balda", RPID: u.Hostname(), RPOrigins: []string{origin}})
	if err != nil {
		return nil, ErrMFAUnavailable
	}
	return &webAuthnEngine{library: library, origin: origin, rpID: u.Hostname()}, nil
}

type webAuthnUser struct {
	user        usercmd.User
	credentials []libwebauthn.Credential
}

func (u webAuthnUser) WebAuthnID() []byte                            { return []byte(u.user.ID) }
func (u webAuthnUser) WebAuthnName() string                          { return u.user.Username }
func (u webAuthnUser) WebAuthnDisplayName() string                   { return u.user.DisplayName }
func (u webAuthnUser) WebAuthnCredentials() []libwebauthn.Credential { return u.credentials }

func (e *webAuthnEngine) beginRegistration(user usercmd.User) (*protocol.CredentialCreation, *libwebauthn.SessionData, error) {
	return e.library.BeginRegistration(webAuthnUser{user: user}, libwebauthn.WithRegistrationOrigin(e.origin),
		libwebauthn.WithAuthenticatorSelection(protocol.AuthenticatorSelection{UserVerification: protocol.VerificationRequired}))
}

func (e *webAuthnEngine) beginAssertion(user usercmd.User, key usercmd.MFACredential) (*protocol.CredentialAssertion, *libwebauthn.SessionData, error) {
	u, err := e.assertionUser(user, key)
	if err != nil {
		return nil, nil, err
	}
	return e.library.BeginLogin(u, libwebauthn.WithLoginOrigin(e.origin), libwebauthn.WithUserVerification(protocol.VerificationRequired))
}

func (e *webAuthnEngine) assertionUser(user usercmd.User, key usercmd.MFACredential) (webAuthnUser, error) {
	var credential libwebauthn.Credential
	if key.UserID != user.ID || key.RPID != e.rpID || json.Unmarshal(key.Data, &credential) != nil ||
		!bytes.Equal(credential.ID, key.CredentialID) || !bytes.Equal(credential.PublicKey, key.PublicKey) {
		return webAuthnUser{}, ErrUnauthenticated
	}
	credential.Authenticator.SignCount = key.SignCount
	credential.Flags.BackupEligible, credential.Flags.BackupState = key.BackupEligible, key.BackupState
	return webAuthnUser{user: user, credentials: []libwebauthn.Credential{credential}}, nil
}

func (e *webAuthnEngine) verify(user usercmd.User, key usercmd.MFACredential, state libwebauthn.SessionData, body []byte, registration bool) (*libwebauthn.Credential, error) {
	if len(body) == 0 || len(body) > maxWebAuthnBody || state.Origin != e.origin || state.RelyingPartyID != e.rpID ||
		!bytes.Equal(state.UserID, []byte(user.ID)) || state.UserVerification != protocol.VerificationRequired || strings.TrimSpace(state.Challenge) == "" {
		return nil, ErrUnauthenticated
	}
	request := &http.Request{Body: io.NopCloser(bytes.NewReader(body)), Header: http.Header{"Content-Type": []string{"application/json"}}}
	var credential *libwebauthn.Credential
	var err error
	if registration {
		credential, err = e.library.FinishRegistration(webAuthnUser{user: user}, state, request)
	} else {
		u, userErr := e.assertionUser(user, key)
		if userErr != nil {
			return nil, userErr
		}
		credential, err = e.library.FinishLogin(u, state, request)
	}
	if err != nil || credential == nil || !credential.Flags.UserVerified || credential.Authenticator.CloneWarning ||
		(!registration && (credential.Flags.BackupEligible != key.BackupEligible || !bytes.Equal(credential.ID, key.CredentialID))) {
		return nil, ErrUnauthenticated
	}
	return credential, nil
}

func (e *webAuthnEngine) credential(userID, factorID string, key *libwebauthn.Credential, created, used time.Time) (usercmd.MFACredential, error) {
	data, err := json.Marshal(key)
	if err != nil {
		return usercmd.MFACredential{}, err
	}
	return usercmd.MFACredential{ID: factorID, UserID: userID, RPID: e.rpID, CredentialID: key.ID, PublicKey: key.PublicKey, Data: data,
		SignCount: key.Authenticator.SignCount, BackupEligible: key.Flags.BackupEligible, BackupState: key.Flags.BackupState,
		CreatedAt: created, LastUsedAt: used}, nil
}
