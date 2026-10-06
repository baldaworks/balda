package mcpfx

import (
	"context"
	"net/url"
	"path"
	"slices"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
	"github.com/baldaworks/balda/internal/apps/balda/mcpmanage"
	"golang.org/x/oauth2"
)

// BeginCode builds a native S256 authorization request for the worker client.
func (*OAuthProvider) BeginCode(metadata mcpmanage.OAuthMetadata, grant mcpcmd.Grant, redirectURI, state string) (string, string, error) {
	if !metadata.RequireIssuerParameter || !validOAuthURL(metadata.AuthorizationEndpoint) || !validOAuthURL(redirectURI) || state == "" || grant.Binding.ClientID == "" || grant.Binding.Resource != metadata.Resource || grant.Binding.Issuer != metadata.Issuer || !slices.Contains(metadata.PKCEMethods, "S256") {
		return "", "", mcpcmd.ErrUnavailable
	}
	verifier := oauth2.GenerateVerifier()
	config := oauth2.Config{ClientID: grant.Binding.ClientID, RedirectURL: redirectURI, Scopes: grant.Scopes, Endpoint: oauth2.Endpoint{AuthURL: metadata.AuthorizationEndpoint}}
	return config.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier), oauth2.SetAuthURLParam("resource", grant.Binding.Resource)), verifier, nil
}

// ExchangeCode exchanges only a validated, consumed browser attempt's code.
func (p *OAuthProvider) ExchangeCode(ctx context.Context, metadata mcpmanage.OAuthMetadata, grant mcpcmd.Grant, secrets mcpmanage.GrantSecrets, redirectURI, code, verifier string) (mcpmanage.OAuthToken, error) {
	if !validOAuthURL(redirectURI) || code == "" || verifier == "" {
		return mcpmanage.OAuthToken{}, mcpcmd.ErrInvalid
	}
	ctx, config, err := p.tokenConfig(ctx, metadata, grant, secrets)
	if err != nil {
		return mcpmanage.OAuthToken{}, err
	}
	config.RedirectURL = redirectURI
	token, err := config.Exchange(ctx, code, oauth2.VerifierOption(verifier), oauth2.SetAuthURLParam("resource", grant.Binding.Resource))
	return workerTokenResult(token, err, grant)
}

// MCPCallbackURL derives the native route from Backoffice's resolved origin/base.
func MCPCallbackURL(publicURL, basePath string) (string, error) {
	u, err := url.Parse(publicURL)
	if err != nil || !validOAuthURL(publicURL) || u.RawQuery != "" || (u.Path != "" && u.Path != "/") {
		return "", mcpcmd.ErrInvalid
	}
	if basePath != "" && (basePath == "/" || !strings.HasPrefix(basePath, "/") || strings.HasSuffix(basePath, "/") || strings.ContainsAny(basePath, "\\?#% \t\r\n") || strings.Contains(basePath, "//") || path.Clean(basePath) != basePath) {
		return "", mcpcmd.ErrInvalid
	}
	u.Path = basePath + "/mcp/oauth/callback"
	u.RawPath = ""
	return u.String(), nil
}
