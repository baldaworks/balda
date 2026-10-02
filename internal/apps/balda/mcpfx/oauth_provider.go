package mcpfx

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
	"github.com/baldaworks/balda/internal/apps/balda/mcpmanage"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"golang.org/x/oauth2"
)

// OAuthProvider adapts supported MCP OAuth protocols to worker grant policy.
type OAuthProvider struct{ client *http.Client }

// DiscoverIssuer reloads endpoints only from the exact registered grant issuer.
func (p *OAuthProvider) DiscoverIssuer(ctx context.Context, binding mcpcmd.AuthBinding) (mcpmanage.OAuthMetadata, error) {
	if !validOAuthURL(binding.Resource) || !validOAuthURL(binding.Issuer) {
		return mcpmanage.OAuthMetadata{}, mcpcmd.ErrUnavailable
	}
	server, err := auth.GetAuthServerMetadata(ctx, binding.Issuer, p.client)
	if err != nil || server == nil || server.Issuer != binding.Issuer || !slices.Contains(server.CodeChallengeMethodsSupported, "S256") || !validOAuthURL(server.TokenEndpoint) {
		return mcpmanage.OAuthMetadata{}, mcpcmd.ErrUnavailable
	}
	methods := server.TokenEndpointAuthMethodsSupported
	if len(methods) == 0 {
		methods = []string{mcpcmd.ClientAuthSecretBasic}
	}
	return mcpmanage.OAuthMetadata{Resource: binding.Resource, Issuer: server.Issuer, AuthorizationEndpoint: server.AuthorizationEndpoint, TokenEndpoint: server.TokenEndpoint, RegistrationEndpoint: server.RegistrationEndpoint, Scopes: server.ScopesSupported, AuthMethods: methods, GrantTypes: server.GrantTypesSupported, ResponseTypes: server.ResponseTypesSupported, PKCEMethods: server.CodeChallengeMethodsSupported, RequireIssuerParameter: server.AuthorizationResponseIssParameterSupported}, nil
}

// NewOAuthProvider owns a bounded non-redirecting protocol client.
func NewOAuthProvider(client *http.Client) *OAuthProvider {
	if client == nil {
		client = http.DefaultClient
	}
	base := client.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	return &OAuthProvider{client: &http.Client{Transport: boundedOAuthTransport{base: base}, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return mcpcmd.ErrUnavailable }}}
}

// Discover validates resource and authorization-server metadata through the SDK.
func (p *OAuthProvider) Discover(ctx context.Context, resource, metadataURL string) (mcpmanage.OAuthMetadata, error) {
	if !validOAuthURL(resource) {
		return mcpmanage.OAuthMetadata{}, mcpcmd.ErrInvalid
	}
	locations := []string{metadataURL}
	if metadataURL == "" {
		u, _ := url.Parse(resource)
		u.RawQuery, u.RawPath = "", ""
		path := u.Path
		u.Path = "/.well-known/oauth-protected-resource/" + strings.TrimLeft(path, "/")
		locations[0] = u.String()
		u.Path = "/.well-known/oauth-protected-resource"
		locations = append(locations, u.String())
	}
	var resourceMetadata *oauthex.ProtectedResourceMetadata
	for _, location := range locations {
		if !validOAuthURL(location) {
			return mcpmanage.OAuthMetadata{}, mcpcmd.ErrInvalid
		}
		// SDK metadata errors deliberately hide their status type. Observe status
		// only to permit the normative root fallback after a genuine 404.
		status := &metadataStatusTransport{base: p.transport()}
		client := *p.client
		client.Transport = status
		var err error
		resourceMetadata, err = oauthex.GetProtectedResourceMetadata(ctx, location, resource, &client)
		if err == nil {
			break
		}
		if status.code != http.StatusNotFound {
			return mcpmanage.OAuthMetadata{}, mcpcmd.ErrUnavailable
		}
	}
	if resourceMetadata == nil || len(resourceMetadata.AuthorizationServers) != 1 || (len(resourceMetadata.BearerMethodsSupported) > 0 && !slices.Contains(resourceMetadata.BearerMethodsSupported, "header")) {
		return mcpmanage.OAuthMetadata{}, mcpcmd.ErrUnavailable
	}
	metadata, err := p.DiscoverIssuer(ctx, mcpcmd.AuthBinding{Resource: resource, Issuer: resourceMetadata.AuthorizationServers[0]})
	if err != nil {
		return mcpmanage.OAuthMetadata{}, err
	}
	scopes := resourceMetadata.ScopesSupported
	if scopes == nil {
		scopes = metadata.Scopes
	} else if metadata.Scopes != nil {
		scopes = make([]string, 0, len(scopes))
		for _, scope := range resourceMetadata.ScopesSupported {
			if slices.Contains(metadata.Scopes, scope) {
				scopes = append(scopes, scope)
			}
		}
	}
	metadata.Scopes = scopes
	return metadata, nil
}

// Register uses the SDK's supported dynamic client registration.
func (p *OAuthProvider) Register(ctx context.Context, metadata mcpmanage.OAuthMetadata, redirectURI string, scopes []string) (mcpmanage.OAuthClient, error) {
	if !validOAuthURL(metadata.RegistrationEndpoint) || !validOAuthURL(redirectURI) {
		return mcpmanage.OAuthClient{}, mcpcmd.ErrUnavailable
	}
	method := ""
	for _, supported := range []string{mcpcmd.ClientAuthNone, mcpcmd.ClientAuthSecretBasic, mcpcmd.ClientAuthSecretPost} {
		if slices.Contains(metadata.AuthMethods, supported) {
			method = supported
			break
		}
	}
	if method == "" || (len(metadata.GrantTypes) > 0 && !slices.Contains(metadata.GrantTypes, "authorization_code")) || (len(metadata.ResponseTypes) > 0 && !slices.Contains(metadata.ResponseTypes, "code")) {
		return mcpmanage.OAuthClient{}, mcpcmd.ErrUnavailable
	}
	registration, err := oauthex.RegisterClient(ctx, metadata.RegistrationEndpoint, &oauthex.ClientRegistrationMetadata{RedirectURIs: []string{redirectURI}, TokenEndpointAuthMethod: method, GrantTypes: []string{"authorization_code", "refresh_token"}, ResponseTypes: []string{"code"}, ClientName: "Balda worker", Scope: strings.Join(scopes, " ")}, p.client)
	if err != nil || registration == nil || !slices.Contains(registration.RedirectURIs, redirectURI) {
		return mcpmanage.OAuthClient{}, mcpcmd.ErrUnavailable
	}
	returnedMethod := registration.TokenEndpointAuthMethod
	if returnedMethod == "" {
		returnedMethod = mcpcmd.ClientAuthSecretBasic
	}
	return mcpmanage.OAuthClient{ID: registration.ClientID, Secret: registration.ClientSecret, AuthMethod: returnedMethod, SecretExpiresAt: registration.ClientSecretExpiresAt}, nil
}

// Refresh uses x/oauth2 client authentication and token parsing.
func (p *OAuthProvider) Refresh(ctx context.Context, metadata mcpmanage.OAuthMetadata, grant mcpcmd.Grant, secrets mcpmanage.GrantSecrets) (mcpmanage.OAuthToken, error) {
	if !validOAuthURL(metadata.TokenEndpoint) || metadata.Issuer != grant.Binding.Issuer || metadata.Resource != grant.Binding.Resource || secrets.RefreshToken == "" {
		return mcpmanage.OAuthToken{}, mcpcmd.ErrAuthRequired
	}
	style := oauth2.AuthStyleInParams
	switch grant.TokenEndpointAuthMethod {
	case mcpcmd.ClientAuthNone:
		if secrets.ClientSecret != "" {
			return mcpmanage.OAuthToken{}, mcpcmd.ErrAuthRequired
		}
	case mcpcmd.ClientAuthSecretBasic:
		style = oauth2.AuthStyleInHeader
		if secrets.ClientSecret == "" {
			return mcpmanage.OAuthToken{}, mcpcmd.ErrAuthRequired
		}
	case mcpcmd.ClientAuthSecretPost:
		if secrets.ClientSecret == "" {
			return mcpmanage.OAuthToken{}, mcpcmd.ErrAuthRequired
		}
	default:
		return mcpmanage.OAuthToken{}, mcpcmd.ErrAuthRequired
	}
	client := *p.client
	client.Transport = &resourceTokenTransport{base: p.transport(), endpoint: metadata.TokenEndpoint, resource: grant.Binding.Resource, scopes: grant.Scopes, public: grant.TokenEndpointAuthMethod == mcpcmd.ClientAuthNone}
	ctx = context.WithValue(ctx, oauth2.HTTPClient, &client)
	config := oauth2.Config{ClientID: grant.Binding.ClientID, ClientSecret: secrets.ClientSecret, Endpoint: oauth2.Endpoint{TokenURL: metadata.TokenEndpoint, AuthStyle: style}}
	token, err := config.TokenSource(ctx, &oauth2.Token{RefreshToken: secrets.RefreshToken}).Token()
	if err != nil {
		if errors.Is(err, mcpcmd.ErrAuthRequired) {
			return mcpmanage.OAuthToken{}, mcpcmd.ErrAuthRequired
		}
		var rejected *oauth2.RetrieveError
		if errors.As(err, &rejected) && (rejected.ErrorCode == "invalid_grant" || rejected.ErrorCode == "invalid_client" || rejected.ErrorCode == "unauthorized_client" || rejected.ErrorCode == "invalid_scope" || rejected.ErrorCode == "access_denied") {
			return mcpmanage.OAuthToken{}, mcpcmd.ErrAuthRequired
		}
		return mcpmanage.OAuthToken{}, mcpcmd.ErrUnavailable
	}
	for field, expected := range map[string]string{"resource": grant.Binding.Resource, "iss": grant.Binding.Issuer} {
		if value := token.Extra(field); value != nil {
			got, ok := value.(string)
			if !ok || got != expected {
				return mcpmanage.OAuthToken{}, mcpcmd.ErrAuthRequired
			}
		}
	}
	var scopes []string
	if value := token.Extra("scope"); value != nil {
		scope, ok := value.(string)
		if !ok {
			return mcpmanage.OAuthToken{}, mcpcmd.ErrAuthRequired
		}
		scopes = strings.Fields(scope)
		if scopes == nil {
			scopes = []string{}
		}
	}
	return mcpmanage.OAuthToken{Secrets: mcpmanage.GrantSecrets{AccessToken: token.AccessToken, RefreshToken: token.RefreshToken, TokenType: token.TokenType}, ExpiresAt: token.Expiry, Scopes: scopes}, nil
}

// SDK registration uses io.ReadAll; cap every protocol body before SDK parsing.
type boundedOAuthTransport struct{ base http.RoundTripper }

func (t boundedOAuthTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	response, err := t.base.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	response.Body = http.MaxBytesReader(nil, response.Body, 1<<20)
	return response, nil
}

func (p *OAuthProvider) transport() http.RoundTripper {
	if p.client.Transport != nil {
		return p.client.Transport
	}
	return http.DefaultTransport
}

type metadataStatusTransport struct {
	base http.RoundTripper
	code int
}

func (t *metadataStatusTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	response, err := t.base.RoundTrip(r)
	if response != nil {
		t.code = response.StatusCode
	}
	return response, err
}

// resourceTokenTransport adds the mandatory resource indicator to x/oauth2's
// refresh request, which otherwise provides no refresh AuthCodeOption hook.
type resourceTokenTransport struct {
	base               http.RoundTripper
	endpoint, resource string
	scopes             []string
	public             bool
}

func (t *resourceTokenTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Method != http.MethodPost || r.URL.String() != t.endpoint {
		return nil, mcpcmd.ErrUnavailable
	}
	defer func() { _ = r.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<10+1))
	if err != nil || len(body) > 64<<10 {
		return nil, mcpcmd.ErrUnavailable
	}
	values, err := url.ParseQuery(string(body))
	if err != nil {
		return nil, mcpcmd.ErrUnavailable
	}
	values.Set("resource", t.resource)
	if len(t.scopes) > 0 {
		values.Set("scope", strings.Join(t.scopes, " "))
	}
	if t.public {
		values.Del("client_secret")
	}
	request := r.Clone(r.Context())
	encoded := values.Encode()
	request.Body = io.NopCloser(strings.NewReader(encoded))
	request.ContentLength = int64(len(encoded))
	request.GetBody = nil
	response, err := t.base.RoundTrip(request)
	if err != nil {
		return nil, err
	}
	contentType := response.Header.Get("Content-Type")
	if response.StatusCode < 200 || response.StatusCode >= 300 || strings.HasPrefix(contentType, "application/x-www-form-urlencoded") || strings.HasPrefix(contentType, "text/plain") {
		return response, nil
	}
	// Extra cannot distinguish a missing JSON claim from explicit null. Check
	// only claim shape on the bounded response; x/oauth2 still parses tokens.
	data, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		return nil, mcpcmd.ErrUnavailable
	}
	var claims map[string]json.RawMessage
	if err := json.Unmarshal(data, &claims); err != nil {
		return nil, mcpcmd.ErrAuthRequired
	}
	for _, name := range []string{"scope", "resource", "iss"} {
		if raw, exists := claims[name]; exists {
			var value *string
			if err := json.Unmarshal(raw, &value); err != nil || value == nil {
				return nil, mcpcmd.ErrAuthRequired
			}
		}
	}
	response.Body = io.NopCloser(strings.NewReader(string(data)))
	return response, nil
}

func validOAuthURL(value string) bool {
	u, err := url.Parse(value)
	if err != nil || u.User != nil || u.Fragment != "" || u.Hostname() == "" {
		return false
	}
	if u.Scheme == "https" {
		return true
	}
	if u.Scheme != "http" {
		return false
	}
	if u.Hostname() == "localhost" {
		return true
	}
	ip := net.ParseIP(u.Hostname())
	return ip != nil && ip.IsLoopback()
}
