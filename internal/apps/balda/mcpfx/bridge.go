package mcpfx

import (
	"context"

	"github.com/baldaworks/balda/internal/apps/balda/mcpbridge"
	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
	"github.com/baldaworks/balda/internal/apps/balda/mcpmanage"
	"github.com/baldaworks/balda/internal/apps/balda/mcpruntime"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
)

// BridgeResolver wraps a trusted exact resolver before discovery and provider
// projection, so configured remote headers cannot bypass the bridge.
type BridgeResolver struct {
	Resolver mcpruntime.LaunchResolver
	Bridge   *mcpbridge.Bridge
}

// ResolveLaunch resolves private upstream config and installs its local projection.
func (r BridgeResolver) ResolveLaunch(ctx context.Context, descriptor runtimecatalogcmd.MCPServerDescriptor) (mcpruntime.LaunchConfig, error) {
	if r.Resolver == nil {
		return mcpruntime.LaunchConfig{}, runtimecatalogcmd.ErrRevisionUnavailable
	}
	config, err := r.Resolver.ResolveLaunch(ctx, descriptor)
	if err != nil {
		return mcpruntime.LaunchConfig{}, err
	}
	if descriptor.ID.Source.Kind == runtimecatalogcmd.SourceKindConfiguredMCP && config.Transport == transportStdio {
		config.InheritEnvironment = true
	}
	key := mcpruntime.InstanceKey{Source: descriptor.ID.Source, Revision: descriptor.Revision, Name: descriptor.Name}
	return BridgeLaunch(r.Bridge, RegistryID(key), config, nil, nil)
}

// BridgeLauncher ties a resolved bridge endpoint to the reconciler's transport
// lifetime. Failed discovery releases it immediately; retained sessions keep
// the endpoint until the last reference closes the underlying instance.
type BridgeLauncher struct {
	Bridge   *mcpbridge.Bridge
	Launcher mcpruntime.Launcher
}

// BridgeProjector ties private endpoints to exact configuration leases rather
// than discovery success. Resolution installs locally; last drain removes it.
type BridgeProjector struct {
	mcpruntime.Projector
	Bridge *mcpbridge.Bridge
}

func (p BridgeProjector) Remove(ctx context.Context, key mcpruntime.InstanceKey) error {
	if err := p.Projector.Remove(ctx, key); err != nil {
		return err
	}
	if p.Bridge != nil {
		p.Bridge.Remove(RegistryID(key))
	}
	return nil
}

// AuthorizationRequiredLaunch installs a private denied projection for a
// current file definition requiring a fresh trusted OAuth capture.
func AuthorizationRequiredLaunch(bridge *mcpbridge.Bridge, id string, config mcpruntime.LaunchConfig) (mcpruntime.LaunchConfig, error) {
	return bridgeLaunch(bridge, id, config, nil, nil, true)
}

// Start discovers through the resolved projection and owns its eventual removal.
func (l BridgeLauncher) Start(ctx context.Context, key mcpruntime.InstanceKey, config mcpruntime.LaunchConfig) (mcpruntime.Instance, error) {
	if l.Launcher == nil {
		return nil, mcpcmd.ErrUnavailable
	}
	instance, err := l.Launcher.Start(ctx, key, config)
	if err != nil {
		if l.Bridge != nil {
			l.Bridge.Remove(RegistryID(key))
		}
		return nil, err
	}
	return &bridgeInstance{Instance: instance, bridge: l.Bridge, id: RegistryID(key)}, nil
}

type bridgeInstance struct {
	mcpruntime.Instance
	bridge *mcpbridge.Bridge
	id     string
}

func (i *bridgeInstance) Close(ctx context.Context) error {
	defer func() {
		if i.bridge != nil {
			i.bridge.Remove(i.id)
		}
	}()
	return i.Instance.Close(ctx)
}

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
	return bridgeLaunch(bridge, id, config, binding, scopes, binding != nil)
}

func bridgeLaunch(bridge *mcpbridge.Bridge, id string, config mcpruntime.LaunchConfig, binding *mcpcmd.AuthBinding, scopes []string, requireAuthorization bool) (mcpruntime.LaunchConfig, error) {
	if config.Transport == transportStdio {
		if binding != nil || requireAuthorization {
			return mcpruntime.LaunchConfig{}, mcpcmd.ErrInvalid
		}
		return config, nil
	}
	if len(config.Headers) == 0 && binding == nil && !requireAuthorization {
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
	observation := newLaunchObservation(config.URL)
	projection, err := bridge.Install(id, mcpbridge.Endpoint{URL: config.URL, Transport: transport, Headers: config.Headers, Binding: binding, Scopes: scopes, RequireAuthorization: requireAuthorization, Observe: observation.observe})
	if err != nil {
		return mcpruntime.LaunchConfig{}, err
	}
	config.URL, config.Headers, config.EnforceHTTPOrigin = projection.URL, projection.Headers, false
	config.ObservedFailure = observation.failure
	return config, nil
}

var _ mcpbridge.Credentials = GrantCredentials{}
