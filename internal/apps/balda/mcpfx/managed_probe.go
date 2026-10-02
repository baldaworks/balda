package mcpfx

import (
	"context"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
	"github.com/baldaworks/balda/internal/apps/balda/mcpruntime"
)

// ManagedProbe uses the same resolved transport as discovery and closes it
// after the check. It never projects a candidate into a provider registry.
type ManagedProbe struct {
	resolver managedValueResolver
	launcher mcpruntime.Launcher
}

// NewManagedProbe binds trusted credential resolution and SDK discovery.
func NewManagedProbe(resolver managedValueResolver, launcher mcpruntime.Launcher) (*ManagedProbe, error) {
	if resolver == nil || launcher == nil {
		return nil, mcpcmd.ErrInvalid
	}
	return &ManagedProbe{resolver: resolver, launcher: launcher}, nil
}

// ProbeMCP performs bounded discovery and returns only non-secret tool count.
func (p *ManagedProbe) ProbeMCP(ctx context.Context, revision mcpcmd.Revision) (int, error) {
	config, err := ResolveManagedLaunch(revision, p.resolver)
	if err != nil {
		return 0, mcpcmd.ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	key := mcpruntime.InstanceKey{Name: revision.ConnectionID}
	instance, err := p.launcher.Start(ctx, key, config)
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
