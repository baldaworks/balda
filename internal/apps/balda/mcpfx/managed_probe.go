package mcpfx

import (
	"context"
	"crypto/rand"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/mcpbridge"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"

	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
	"github.com/baldaworks/balda/internal/apps/balda/mcpruntime"
)

// ManagedProbe uses the same resolved transport as discovery and closes it
// after the check. It never projects a candidate into a provider registry.
type ManagedProbe struct {
	resolver managedValueResolver
	launcher mcpruntime.Launcher
	bridge   *mcpbridge.Bridge
	timeout  time.Duration
}

// NewManagedProbe binds trusted credential resolution and SDK discovery.
func NewManagedProbe(resolver managedValueResolver, launcher mcpruntime.Launcher, bridge *mcpbridge.Bridge) (*ManagedProbe, error) {
	if resolver == nil || launcher == nil {
		return nil, mcpcmd.ErrInvalid
	}
	return &ManagedProbe{resolver: resolver, launcher: launcher, bridge: bridge, timeout: 10 * time.Second}, nil
}

// ProbeMCP performs bounded discovery and returns only non-secret tool count.
func (p *ManagedProbe) ProbeMCP(ctx context.Context, revision mcpcmd.Revision) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	binding := revision.Definition.AuthBinding
	if revision.Definition.OAuth && (binding == nil || binding.ConnectionID != revision.ConnectionID || binding.Resource != revision.Definition.URL) {
		return 0, mcpcmd.ErrAuthRequired
	}
	if !revision.Definition.OAuth && binding != nil {
		return 0, mcpcmd.ErrInvalid
	}
	config, err := ResolveManagedLaunch(revision, p.resolver)
	if err != nil {
		return 0, mcpcmd.ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	// SDK cancellation notifications may continue on a detached context. The
	// worker retains its transport and route until cleanup completes; the
	// operator waits only for the effective discovery deadline.
	type result struct {
		count int
		err   error
	}
	done := make(chan result)
	go func() {
		count, err := p.discover(ctx, revision, config)
		select {
		case done <- result{count: count, err: err}:
		case <-ctx.Done():
		}
	}()
	select {
	case value := <-done:
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		return value.count, value.err
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

func (p *ManagedProbe) discover(ctx context.Context, revision mcpcmd.Revision, config mcpruntime.LaunchConfig) (int, error) {
	binding := revision.Definition.AuthBinding
	key := mcpruntime.InstanceKey{Source: runtimecatalogcmd.SourceID{Kind: runtimecatalogcmd.SourceKindManagedMCP, Name: revision.ConnectionID}, Name: revision.ConnectionID, Revision: runtimecatalogcmd.RevisionID(rand.Text())}
	launcher := p.launcher
	if p.bridge != nil || revision.Definition.OAuth {
		var err error
		config, err = BridgeLaunch(p.bridge, RegistryID(key), config, binding, revision.Definition.Scopes)
		if err != nil {
			return 0, err
		}
		launcher = BridgeLauncher{Bridge: p.bridge, Launcher: p.launcher}
	}
	instance, err := launcher.Start(ctx, key, config)
	if err != nil {
		return 0, mcpcmd.ErrUnavailable
	}
	closeCtx, closeCancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer closeCancel()
	count := len(instance.Tools())
	if err := instance.Close(closeCtx); err != nil {
		return 0, mcpcmd.ErrUnavailable
	}
	return count, nil
}
