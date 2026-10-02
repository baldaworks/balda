package mcpfx

import (
	"context"

	"github.com/baldaworks/balda/internal/apps/balda/mcpbridge"
	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
	"github.com/baldaworks/balda/internal/apps/balda/mcpmanage"
	"github.com/baldaworks/balda/internal/apps/balda/mcpruntime"
)

// GrantCredentials exposes current worker access tokens solely to the bridge.
type GrantCredentials struct{ Grants *mcpmanage.Grants }

// AccessToken keeps rotation/persistence policy in the worker grant owner.
func (a GrantCredentials) AccessToken(ctx context.Context, binding mcpcmd.AuthBinding, scopes []string) (string, error) {
	if a.Grants == nil {
		return "", mcpcmd.ErrUnavailable
	}
	secrets, err := a.Grants.RequestCredentials(ctx, binding, scopes)
	if err != nil {
		return "", err
	}
	return secrets.AccessToken, nil
}

// BridgeLaunch projects resolved remote credentials onto one private endpoint.
// Callers retain the original definition in public snapshots and read models.
func BridgeLaunch(bridge *mcpbridge.Bridge, id string, config mcpruntime.LaunchConfig, binding *mcpcmd.AuthBinding, scopes []string) (mcpruntime.LaunchConfig, error) {
	if config.Transport == transportStdio {
		if binding != nil {
			return mcpruntime.LaunchConfig{}, mcpcmd.ErrInvalid
		}
		return config, nil
	}
	if len(config.Headers) == 0 && binding == nil {
		config.EnforceHTTPOrigin = false
		return config, nil
	}
	if bridge == nil {
		return mcpruntime.LaunchConfig{}, mcpcmd.ErrUnavailable
	}
	transport := mcpcmd.Transport(config.Transport)
	if config.Transport == transportStreamableHTTP {
		transport = mcpcmd.TransportHTTP
	}
	projection, err := bridge.Install(id, mcpbridge.Endpoint{URL: config.URL, Transport: transport, Headers: config.Headers, Binding: binding, Scopes: scopes})
	if err != nil {
		return mcpruntime.LaunchConfig{}, err
	}
	config.URL, config.Headers, config.EnforceHTTPOrigin = projection.URL, projection.Headers, false
	return config, nil
}

var _ mcpbridge.Credentials = GrantCredentials{}
