package mcpfx

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
	"github.com/baldaworks/balda/internal/apps/balda/mcpmanage"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"golang.org/x/oauth2"
)

const deviceGrantType = "urn:ietf:params:oauth:grant-type:device_code"

// BeginDevice uses the pinned device authorization primitive.
func (p *OAuthProvider) BeginDevice(ctx context.Context, metadata mcpmanage.OAuthMetadata, grant mcpcmd.Grant, secrets mcpmanage.GrantSecrets) (mcpmanage.OAuthDevice, error) {
	if !validOAuthURL(metadata.DeviceAuthorizationEndpoint) || !slices.Contains(metadata.GrantTypes, deviceGrantType) {
		return mcpmanage.OAuthDevice{}, mcpcmd.ErrUnavailable
	}
	_, config, err := p.tokenConfig(ctx, metadata, grant, secrets)
	if err != nil {
		return mcpmanage.OAuthDevice{}, err
	}
	client := *p.client
	client.Transport = &resourceTokenTransport{base: deviceStartTransport{base: p.transport(), ctx: ctx, clientID: grant.Binding.ClientID, secret: secrets.ClientSecret, basic: grant.TokenEndpointAuthMethod == mcpcmd.ClientAuthSecretBasic}, endpoint: metadata.DeviceAuthorizationEndpoint, resource: grant.Binding.Resource, scopes: grant.Scopes, public: grant.TokenEndpointAuthMethod == mcpcmd.ClientAuthNone}
	ctx = context.WithValue(ctx, oauth2.HTTPClient, &client)
	config.Endpoint.DeviceAuthURL = metadata.DeviceAuthorizationEndpoint
	config.Scopes = grant.Scopes
	var options []oauth2.AuthCodeOption
	if grant.TokenEndpointAuthMethod == mcpcmd.ClientAuthSecretPost {
		options = append(options, oauth2.SetAuthURLParam("client_secret", secrets.ClientSecret))
	}
	device, err := config.DeviceAuth(ctx, options...)
	if err != nil || device == nil {
		return mcpmanage.OAuthDevice{}, mcpcmd.ErrUnavailable
	}
	if device.DeviceCode == "" || len(device.DeviceCode) > 16<<10 || device.UserCode == "" || len(device.UserCode) > 256 || strings.ContainsAny(device.DeviceCode+device.UserCode, "\x00\r\n") || !validOAuthURL(device.VerificationURI) || (device.VerificationURIComplete != "" && !validOAuthURL(device.VerificationURIComplete)) || !device.Expiry.After(time.Now()) || device.Interval < 0 || device.Interval > 600 {
		return mcpmanage.OAuthDevice{}, mcpcmd.ErrUnavailable
	}
	return mcpmanage.OAuthDevice{Code: device.DeviceCode, UserCode: device.UserCode, VerificationURI: device.VerificationURI, VerificationURIComplete: device.VerificationURIComplete, ExpiresAt: device.Expiry, Interval: device.Interval}, nil
}

// PollDevice owns supported interval, pending and slow-down protocol behavior.
func (p *OAuthProvider) PollDevice(ctx context.Context, metadata mcpmanage.OAuthMetadata, grant mcpcmd.Grant, secrets mcpmanage.GrantSecrets, device mcpmanage.OAuthDevice) (mcpmanage.OAuthToken, error) {
	if device.Code == "" || !device.ExpiresAt.After(time.Now()) || device.Interval < 0 || device.Interval > 600 {
		return mcpmanage.OAuthToken{}, mcpcmd.ErrAuthAttempt
	}
	ctx, config, err := p.tokenConfig(ctx, metadata, grant, secrets)
	if err != nil {
		return mcpmanage.OAuthToken{}, err
	}
	config.Scopes = grant.Scopes
	token, err := config.DeviceAccessToken(ctx, &oauth2.DeviceAuthResponse{DeviceCode: device.Code, Expiry: device.ExpiresAt, Interval: device.Interval}, oauth2.SetAuthURLParam("resource", grant.Binding.Resource))
	var rejected *oauth2.RetrieveError
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &rejected) && rejected.ErrorCode == "expired_token") {
		return mcpmanage.OAuthToken{}, mcpcmd.ErrAuthAttempt
	}
	return workerTokenResult(token, err, grant)
}

// The SDK metadata type omits the device endpoint; capture the same bounded
// response without changing its fields or its well-known discovery locations.
type deviceMetadataTransport struct {
	base           http.RoundTripper
	server         *oauthex.AuthServerMeta
	deviceEndpoint string
}

func (t *deviceMetadataTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	response, err := t.base.RoundTrip(r)
	if err != nil || response.StatusCode < 200 || response.StatusCode >= 300 {
		return response, err
	}
	data, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		return nil, mcpcmd.ErrUnavailable
	}
	var metadata struct {
		oauthex.AuthServerMeta
		DeviceEndpoint string `json:"device_authorization_endpoint"`
	}
	if err := json.Unmarshal(data, &metadata); err != nil {
		return nil, mcpcmd.ErrUnavailable
	}
	t.server, t.deviceEndpoint = &metadata.AuthServerMeta, metadata.DeviceEndpoint
	response.Body = io.NopCloser(strings.NewReader(string(data)))
	return response, nil
}
func (t *deviceMetadataTransport) supportsDevice() bool {
	if t.server == nil || !validOAuthURL(t.deviceEndpoint) || !slices.Contains(t.server.GrantTypesSupported, deviceGrantType) || !validOAuthURL(t.server.Issuer) || !validOAuthURL(t.server.TokenEndpoint) {
		return false
	}
	for _, endpoint := range []string{t.server.AuthorizationEndpoint, t.server.RegistrationEndpoint} {
		if endpoint != "" && !validOAuthURL(endpoint) {
			return false
		}
	}
	return true
}

// x/oauth2 DeviceAuth does not close its response body. Close the bounded wire
// body here and pass the SDK a replayable response, also authenticating clients.
type deviceStartTransport struct {
	base             http.RoundTripper
	ctx              context.Context
	clientID, secret string
	basic            bool
}

func (t deviceStartTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	// DeviceAuth uses http.NewRequest without its caller context. Preserve the
	// client's request timeout and bind cancellation to this attempt explicitly.
	if err := t.ctx.Err(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(r.Context())
	stop := context.AfterFunc(t.ctx, cancel)
	defer stop()
	defer cancel()
	r = r.Clone(ctx)
	if t.basic {
		r.SetBasicAuth(url.QueryEscape(t.clientID), url.QueryEscape(t.secret))
	}
	response, err := t.base.RoundTrip(r)
	if err != nil {
		return response, err
	}
	data, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		return nil, mcpcmd.ErrUnavailable
	}
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		// DeviceAuth unmarshals through a pointer and dereferences JSON null.
		var object map[string]json.RawMessage
		if err := json.Unmarshal(data, &object); err != nil || object == nil {
			return nil, mcpcmd.ErrUnavailable
		}
	}
	response.Body = io.NopCloser(strings.NewReader(string(data)))
	return response, nil
}
