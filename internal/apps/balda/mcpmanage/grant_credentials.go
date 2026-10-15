package mcpmanage

import (
	"crypto/rand"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
)

// GrantSecrets is plaintext for trusted OAuth/MCP adapters only.
type GrantSecrets struct {
	AccessToken  string `json:"-"`
	RefreshToken string `json:"-"`
	ClientSecret string `json:"-"`
	TokenType    string `json:"-"`
}

// ProtectGrant binds encrypted credentials to exact worker identity/generation.
func (s *Service) ProtectGrant(g mcpcmd.Grant, secrets GrantSecrets) ([]byte, error) {
	if !validGrantIdentity(g) || !validGrantSecrets(g, secrets) {
		return nil, mcpcmd.ErrInvalid
	}
	if secrets.AccessToken == "" && secrets.RefreshToken == "" && secrets.ClientSecret == "" {
		return nil, nil
	}
	if s.credentials == nil {
		return nil, mcpcmd.ErrCredentials
	}
	plain, err := json.Marshal(map[string]string{"access_token": secrets.AccessToken, "refresh_token": secrets.RefreshToken, "client_secret": secrets.ClientSecret, "token_type": secrets.TokenType})
	defer clear(plain)
	if err != nil || len(plain) > maxProtectedPayload {
		return nil, mcpcmd.ErrInvalid
	}
	prefix := make([]byte, 1+s.credentials.NonceSize())
	prefix[0] = 1
	if _, err := rand.Read(prefix[1:]); err != nil {
		return nil, mcpcmd.ErrCredentials
	}
	return s.credentials.Seal(prefix, prefix[1:], plain, grantAAD(g)), nil
}

// OpenGrant decrypts solely for trusted OAuth/MCP execution boundaries.
func (s *Service) OpenGrant(g mcpcmd.Grant) (GrantSecrets, error) {
	if !validGrantIdentity(g) {
		return GrantSecrets{}, mcpcmd.ErrCredentials
	}
	var secrets GrantSecrets
	if len(g.ProtectedValues) > 0 {
		if s.credentials == nil || len(g.ProtectedValues) < 1+s.credentials.NonceSize()+s.credentials.Overhead() || len(g.ProtectedValues) > maxProtectedPayload+1+s.credentials.NonceSize()+s.credentials.Overhead() || g.ProtectedValues[0] != 1 {
			return secrets, mcpcmd.ErrCredentials
		}
		end := 1 + s.credentials.NonceSize()
		plain, err := s.credentials.Open(nil, g.ProtectedValues[1:end], g.ProtectedValues[end:], grantAAD(g))
		if err != nil {
			return secrets, mcpcmd.ErrCredentials
		}
		defer clear(plain)
		var values map[string]string
		if err := json.Unmarshal(plain, &values); err != nil {
			return secrets, mcpcmd.ErrCredentials
		}
		secrets = GrantSecrets{AccessToken: values["access_token"], RefreshToken: values["refresh_token"], ClientSecret: values["client_secret"], TokenType: values["token_type"]}
	}
	if !validGrantSecrets(g, secrets) {
		return GrantSecrets{}, mcpcmd.ErrCredentials
	}
	return secrets, nil
}

func grantAAD(g mcpcmd.Grant) []byte {
	b := g.Binding
	aad, _ := json.Marshal([]string{"balda-mcp-grant-v1", g.ID, b.ConnectionID, b.Resource, b.Issuer, b.ClientID, strconv.FormatUint(g.Generation, 10), string(g.Status)})
	return aad
}

func validGrantIdentity(g mcpcmd.Grant) bool {
	b := g.Binding
	return g.ID != "" && b.ConnectionID != "" && b.ClientID != "" && g.Generation > 0 && validRemoteURL(b.Resource) && validRemoteURL(b.Issuer)
}

func validGrantSecrets(g mcpcmd.Grant, secrets GrantSecrets) bool {
	for _, value := range []string{secrets.AccessToken, secrets.RefreshToken, secrets.ClientSecret, secrets.TokenType} {
		if len(value) > 16<<10 || strings.ContainsAny(value, "\x00\r\n") {
			return false
		}
	}
	switch g.Status {
	case mcpcmd.GrantAuthorized:
		return secrets.AccessToken != "" && strings.EqualFold(secrets.TokenType, "Bearer")
	case mcpcmd.GrantAuthRequired, mcpcmd.GrantDisconnected:
		return secrets.AccessToken == "" && secrets.RefreshToken == ""
	default:
		return false
	}
}
