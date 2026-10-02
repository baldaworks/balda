package security

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"github.com/fxamacker/cbor/v2"
	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/protocol/webauthncose"
	libwebauthn "github.com/go-webauthn/webauthn/webauthn"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
	"time"
)

func TestWebAuthnCapabilityUsesExactSecureOrigin(t *testing.T) {
	for _, test := range []struct {
		origin string
		usable bool
	}{
		{"https://admin.example.org", true}, {"http://localhost:8095", true},
		{"http://127.0.0.1:8095", false}, {"https://127.0.0.1", false},
		{"http://admin.example.org", false}, {"https://user@admin.example.org", false},
	} {
		t.Run(test.origin, func(t *testing.T) {
			engine, err := newWebAuthnEngine(test.origin)
			if (err == nil && engine != nil) != test.usable {
				t.Fatalf("capability=%t error=%v", engine != nil, err)
			}
		})
	}
}

func TestWebAuthnRegistrationStandardsVector(t *testing.T) {
	u := usercmd.User{ID: "admin-vector", Username: "admin", DisplayName: "Admin"}
	engine, err := newWebAuthnEngine("https://example.org")
	require.NoError(t, err)
	for _, test := range []struct{ name, origin, flags, rpID, challenge string }{
		{"valid", "https://example.org", "5d", "example.org", ""},
		{"wrong origin", "https://evil.example.org", "5d", "example.org", ""},
		{"missing UV", "https://example.org", "59", "example.org", ""},
		{"wrong RP", "https://example.org", "5d", "evil.example.org", ""},
		{"wrong challenge", "https://example.org", "5d", "example.org", "other-challenge"},
	} {
		t.Run(test.name, func(t *testing.T) {
			body, challenge := registrationVector(t, test.origin, test.flags)
			if test.challenge != "" {
				challenge = test.challenge
			}
			state := libwebauthn.SessionData{Challenge: challenge, RelyingPartyID: test.rpID, Origin: engine.origin,
				UserID: []byte(u.ID), UserVerification: protocol.VerificationRequired,
				CredParams: []protocol.CredentialParameter{{Type: protocol.PublicKeyCredentialType, Algorithm: webauthncose.AlgES256}}}
			key, err := engine.verify(u, usercmd.MFACredential{}, state, body, true)
			if test.name == "valid" {
				require.NoError(t, err)
				require.True(t, key.Flags.UserVerified)
			} else {
				require.ErrorIs(t, err, ErrUnauthenticated)
			}
		})
	}
}

func TestWebAuthnAssertionRealSignatures(t *testing.T) {
	u := usercmd.User{ID: "assertion-admin", Username: "admin", DisplayName: "Admin"}
	e, err := newWebAuthnEngine("https://example.org")
	require.NoError(t, err)
	private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	publicKey, err := cbor.Marshal(map[int]any{1: 2, 3: -7, -1: 1, -2: private.X.FillBytes(make([]byte, 32)), -3: private.Y.FillBytes(make([]byte, 32))})
	require.NoError(t, err)
	for _, test := range []struct {
		name, origin, challenge string
		flags                   byte
		stored, presented       uint32
		tamper                  bool
	}{
		{"valid", e.origin, "challenge", 0x05, 0, 1, false},
		{"synced zero", e.origin, "challenge", 0x05, 0, 0, false},
		{"wrong origin", "https://evil.example.org", "challenge", 0x05, 0, 1, false},
		{"wrong challenge", e.origin, "other", 0x05, 0, 1, false},
		{"missing UV", e.origin, "challenge", 0x01, 0, 1, false},
		{"bad signature", e.origin, "challenge", 0x05, 0, 1, true},
		{"clone counter", e.origin, "challenge", 0x05, 5, 4, false},
		{"counter reset", e.origin, "challenge", 0x05, 5, 0, false},
		{"backup eligibility changed", e.origin, "challenge", 0x0d, 0, 1, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			libraryKey := &libwebauthn.Credential{ID: []byte("credential"), PublicKey: publicKey}
			libraryKey.Authenticator.SignCount = test.stored
			key, err := e.credential(u.ID, "factor", libraryKey, time.Now().UTC(), time.Time{})
			require.NoError(t, err)
			state := libwebauthn.SessionData{Challenge: "challenge", RelyingPartyID: e.rpID, Origin: e.origin,
				UserID: []byte(u.ID), AllowedCredentialIDs: [][]byte{key.CredentialID}, UserVerification: protocol.VerificationRequired}
			client, err := json.Marshal(map[string]any{"type": "webauthn.get", "challenge": test.challenge, "origin": test.origin, "crossOrigin": false})
			require.NoError(t, err)
			rpHash := sha256.Sum256([]byte(e.rpID))
			auth := append(append([]byte(nil), rpHash[:]...), test.flags)
			auth = binary.BigEndian.AppendUint32(auth, test.presented)
			clientHash := sha256.Sum256(client)
			message := sha256.Sum256(append(append([]byte(nil), auth...), clientHash[:]...))
			signature, err := ecdsa.SignASN1(rand.Reader, private, message[:])
			require.NoError(t, err)
			if test.tamper {
				signature[len(signature)-1] ^= 1
			}
			body, err := json.Marshal(map[string]any{"id": base64.RawURLEncoding.EncodeToString(key.CredentialID),
				"rawId": base64.RawURLEncoding.EncodeToString(key.CredentialID), "type": "public-key",
				"response": map[string]any{"clientDataJSON": base64.RawURLEncoding.EncodeToString(client),
					"authenticatorData": base64.RawURLEncoding.EncodeToString(auth), "signature": base64.RawURLEncoding.EncodeToString(signature)}})
			require.NoError(t, err)
			verified, err := e.verify(u, key, state, body, false)
			if test.name == "valid" || test.name == "synced zero" {
				require.NoError(t, err)
				require.Equal(t, test.presented, verified.Authenticator.SignCount)
			} else {
				require.ErrorIs(t, err, ErrUnauthenticated)
			}
		})
	}
}

func TestMFACeremonyBrowserBindingAndReplay(t *testing.T) {
	p, s, _ := newSecurityTestService(t)
	now := time.Now().UTC()
	s.now = func() time.Time { return now }
	u := createSecurityTestUser(t, p.Users(), "ceremony-admin", "ceremony-admin", usercmd.CredentialStateActive, usercmd.RoleAdministrator, true, now)
	credentials, err := s.Login(t.Context(), u.Username, []byte(testPassword))
	require.NoError(t, err)
	principal, err := s.ValidateAccess(t.Context(), credentials.AccessToken)
	require.NoError(t, err)
	s.webauthn, err = newWebAuthnEngine("https://example.org")
	require.NoError(t, err)
	profile, err := s.store.GetMFAProfile(t.Context(), u.ID)
	require.NoError(t, err)
	start, err := s.startMFA(t.Context(), u, profile, usercmd.MFAEnable, principal.FamilyID, principal.Version, "browser", "csrf", true)
	require.NoError(t, err)
	// A mismatched browser cannot consume the legitimate registration.
	_, err = s.finishMFA(t.Context(), start.Transaction, "different", "csrf", usercmd.MFAEnable, []byte(`{}`))
	require.ErrorIs(t, err, ErrUnauthenticated)
	c, err := s.store.ConsumeMFACeremony(t.Context(), digest(start.Transaction), digest("browser"), digest("csrf"), usercmd.MFAEnable, now)
	require.NoError(t, err)
	require.False(t, bytes.Contains(c.Data, []byte(start.Transaction)))
	_, err = s.store.ConsumeMFACeremony(t.Context(), digest(start.Transaction), digest("browser"), digest("csrf"), usercmd.MFAEnable, now)
	require.Error(t, err)

	start, err = s.startMFA(t.Context(), u, profile, usercmd.MFAEnable, principal.FamilyID, principal.Version, "browser", "csrf", true)
	require.NoError(t, err)
	body := registrationResponseForStart(t, start)
	for _, binding := range []struct {
		csrf    string
		purpose usercmd.MFAPurpose
	}{{"other", usercmd.MFAEnable}, {"csrf", usercmd.MFADisable}} {
		_, err := s.finishMFA(t.Context(), start.Transaction, "browser", binding.csrf, binding.purpose, body)
		require.ErrorIs(t, err, ErrUnauthenticated)
	}
	verified, err := s.finishMFA(t.Context(), start.Transaction, "browser", "csrf", usercmd.MFAEnable, body)
	require.NoError(t, err)
	require.Equal(t, u.ID, verified.key.UserID)
	require.True(t, verified.state.Registration)
	_, err = s.finishMFA(t.Context(), start.Transaction, "browser", "csrf", usercmd.MFAEnable, body)
	require.ErrorIs(t, err, ErrUnauthenticated)

	start, err = s.startMFA(t.Context(), u, profile, usercmd.MFAEnable, principal.FamilyID, principal.Version, "browser", "csrf", true)
	require.NoError(t, err)
	now = now.Add(6 * time.Minute)
	_, err = s.finishMFA(t.Context(), start.Transaction, "browser", "csrf", usercmd.MFAEnable, registrationResponseForStart(t, start))
	require.ErrorIs(t, err, ErrUnauthenticated)
	now = now.Add(-6 * time.Minute)
	start, err = s.startMFA(t.Context(), u, profile, usercmd.MFAEnable, principal.FamilyID, principal.Version, "browser", "csrf", true)
	require.NoError(t, err)
	u.Version++
	u.DisplayName = "Changed"
	err = s.store.(usercmd.Store).UpdateUser(t.Context(), u, u.Version-1, s.audit(usercmd.AuditActionUserUpdated, usercmd.AuditTargetUser, u.ID, "authority test", now))
	require.NoError(t, err)
	_, err = s.finishMFA(t.Context(), start.Transaction, "browser", "csrf", usercmd.MFAEnable, registrationResponseForStart(t, start))
	require.ErrorIs(t, err, ErrUnauthenticated)
	_, err = s.startMFA(t.Context(), u, profile, usercmd.MFADisable, principal.FamilyID, principal.Version, "browser", "csrf", true)
	require.ErrorIs(t, err, ErrForbidden)
}

func registrationResponseForStart(t *testing.T, start CeremonyStart) []byte {
	t.Helper()
	body, _ := registrationVector(t, "https://example.org", "5d")
	var response map[string]any
	require.NoError(t, json.Unmarshal(body, &response))
	fields := response["response"].(map[string]any)
	clientBytes, err := base64.RawURLEncoding.DecodeString(fields["clientDataJSON"].(string))
	require.NoError(t, err)
	var client map[string]any
	require.NoError(t, json.Unmarshal(clientBytes, &client))
	client["challenge"] = base64.RawURLEncoding.EncodeToString(start.Options.(*protocol.CredentialCreation).Response.Challenge)
	clientBytes, err = json.Marshal(client)
	require.NoError(t, err)
	fields["clientDataJSON"] = base64.RawURLEncoding.EncodeToString(clientBytes)
	body, err = json.Marshal(response)
	require.NoError(t, err)
	return body
}

// registrationVector is the W3C WebAuthn L3 none/ES256 test vector with its
// unsigned authenticator flags varied to exercise mandatory UV.
func registrationVector(t *testing.T, origin, flagsHex string) ([]byte, string) {
	t.Helper()
	const (
		attestationHex = "a363666d74646e6f6e656761747453746d74a068617574684461746158a4bfabc37432958b063360d3ad6461c9c4735ae7f8edd46592a5e0f01452b2e4b559000000008446ccb9ab1db374750b2367ff6f3a1f0020f91f391db4c9b2fde0ea70189cba3fb63f579ba6122b33ad94ff3ec330084be4a5010203262001215820afefa16f97ca9b2d23eb86ccb64098d20db90856062eb249c33a9b672f26df61225820930a56b87a2fca66334b03458abf879717c12cc68ed73290af2e2664796b9220"
		clientHex      = "7b2274797065223a22776562617574686e2e637265617465222c226368616c6c656e6765223a22414d4d507434557878475453746e63647134313759447742466938767049612d7077386f4f755657345441222c226f726967696e223a2268747470733a2f2f6578616d706c652e6f7267222c2263726f73734f726967696e223a66616c73652c22657874726144617461223a22636c69656e74446174614a534f4e206d617920626520657874656e6465642077697468206164646974696f6e616c206669656c647320696e20746865206675747572652c207375636820617320746869733a20426b5165446a646354427258426941774a544c453551227d"
		credentialHex  = "f91f391db4c9b2fde0ea70189cba3fb63f579ba6122b33ad94ff3ec330084be4"
		challengeHex   = "00c30fb78531c464d2b6771dab8d7b603c01162f2fa486bea70f283ae556e130"
		rpHashHex      = "bfabc37432958b063360d3ad6461c9c4735ae7f8edd46592a5e0f01452b2e4b5"
	)
	attestation := strings.Replace(attestationHex, rpHashHex+"59", rpHashHex+flagsHex, 1)
	clientData := decodeHex(t, clientHex)
	var client map[string]any
	require.NoError(t, json.Unmarshal(clientData, &client))
	client["origin"] = origin
	clientData, err := json.Marshal(client)
	require.NoError(t, err)
	credentialID := decodeHex(t, credentialHex)
	response := map[string]any{
		"id": base64.RawURLEncoding.EncodeToString(credentialID), "rawId": base64.RawURLEncoding.EncodeToString(credentialID), "type": "public-key",
		"response": map[string]any{
			"attestationObject": base64.RawURLEncoding.EncodeToString(decodeHex(t, attestation)),
			"clientDataJSON":    base64.RawURLEncoding.EncodeToString(clientData),
		},
	}
	body, err := json.Marshal(response)
	require.NoError(t, err)
	return body, base64.RawURLEncoding.EncodeToString(decodeHex(t, challengeHex))
}

func decodeHex(t *testing.T, value string) []byte {
	t.Helper()
	decoded, err := hex.DecodeString(value)
	require.NoError(t, err)
	return decoded
}
