package catalogapp

import (
	"context"
	"sync"

	"github.com/baldaworks/balda/internal/apps/balda/actors/command"
	baldaagent "github.com/baldaworks/balda/internal/apps/balda/agent"
	"github.com/baldaworks/balda/internal/apps/balda/chatapp"
	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/commandfx"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/ingressapp"
	"github.com/baldaworks/balda/internal/apps/balda/mcpruntime"
	"github.com/baldaworks/balda/internal/apps/balda/pluginapp"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalog"
	"github.com/baldaworks/balda/internal/apps/balda/sessionturn"
	baldastate "github.com/baldaworks/balda/internal/apps/balda/state"
	runtimeconfig "github.com/normahq/runtime/v2/appconfig"
	"github.com/normahq/runtime/v2/mcpregistry"
	"go.uber.org/fx"
)

type snapshotRuntimeProvider struct {
	catalog *Runtime
	manager *baldaagent.RuntimeManager
}

func (p *snapshotRuntimeProvider) RuntimeForSnapshot(ctx context.Context, request sessionturn.SnapshotRuntimeRequest) (*sessionturn.SnapshotRuntime, error) {
	ids, release, err := p.catalog.AcquireMCPServerIDs(ctx, request.SnapshotID)
	if err != nil {
		return nil, err
	}
	runtime, err := p.manager.RuntimeForSessionWithMCPServerIDs(ctx, baldaagent.SessionRuntimeRequest{
		Locator: deliverycmd.Locator{
			SessionID: request.Locator.SessionID, ChannelType: request.Locator.ChannelType,
			AddressKey: request.Locator.AddressKey, AddressJSON: request.Locator.AddressJSON,
		},
		UserID: request.UserID, AgentSessionID: request.AgentSessionID, WorkspaceDir: request.WorkspaceDir,
	}, ids)
	if err != nil {
		release()
		return nil, err
	}
	var once sync.Once
	return &sessionturn.SnapshotRuntime{Runner: runtime.Runner, Close: func() error {
		var closeErr error
		once.Do(func() {
			if runtime.Close != nil {
				closeErr = runtime.Close()
			}
			release()
		})
		return closeErr
	}}, nil
}

type runtimeParams struct {
	fx.In

	StateDir       string `name:"balda_state_dir"`
	Provider       baldastate.Provider
	Advertisements []commandcmd.Advertisement `group:"balda_command_advertisements"`
	Norma          runtimeconfig.RuntimeConfig
	Registry       *mcpregistry.MapRegistry
	Commands       *commandcmd.Registry
}

func newRuntime(params runtimeParams) (*Runtime, error) {
	return NewRuntime(params.StateDir, params.Provider, params.Advertisements, params.Norma.MCPServers, params.Registry, params.Commands)
}

// Lifecycle reconstructs durable catalog state before dependent ingress.
type Lifecycle struct {
	runtime *Runtime
	plugins *pluginapp.Service
}

// NewLifecycle creates the startup catalog coordinator.
func NewLifecycle(runtime *Runtime, plugins *pluginapp.Service) *Lifecycle {
	return &Lifecycle{runtime: runtime, plugins: plugins}
}

// Start migrates startup-era installs, then reconstructs one complete snapshot.
func (l *Lifecycle) Start(ctx context.Context) error {
	if err := l.plugins.MigrateLegacy(ctx); err != nil {
		return err
	}
	return l.plugins.Recover(ctx)
}

// Stop drains catalog-owned MCP projections in reverse startup order.
func (l *Lifecycle) Stop(ctx context.Context) error { return l.runtime.mcp.Shutdown(ctx) }

var Module = fx.Module("balda_runtime_catalog",
	fx.Provide(
		commandcmd.NewRegistry,
		newRuntime,
		func(runtime *Runtime) *runtimecatalog.Store { return runtime.Store() },
		func(runtime *Runtime) *mcpruntime.Reconciler { return runtime.MCP() },
		func(runtime *Runtime) *commandfx.AdvertisementProjector { return runtime.Advertisements() },
		fx.Annotate(func(runtime *Runtime) pluginapp.CatalogActivator { return runtime }),
		fx.Annotate(func(runtime *Runtime) command.SnapshotResolver { return runtime }),
		fx.Annotate(func(runtime *Runtime) commandfx.EffectiveSnapshotResolver { return runtime }),
		fx.Annotate(func(runtime *Runtime) baldaagent.SkillCatalog { return runtime }),
		fx.Annotate(func(runtime *Runtime) baldaagent.SkillContentReader { return runtime }),
		func(catalog baldaagent.SkillCatalog, reader baldaagent.SkillContentReader) (*baldaagent.SkillManager, error) {
			return baldaagent.NewSkillManager(catalog, reader, baldaagent.SkillMetadataBudget{})
		},
		fx.Annotate(func(manager *baldaagent.SkillManager) baldaagent.SkillMetadataProvider { return manager }),
		fx.Annotate(func(runtime *Runtime) baldaagent.MCPMetadataProvider { return runtime }),
		fx.Annotate(func(manager *baldaagent.SkillManager) sessionturn.SkillLoader { return manager }),
		fx.Annotate(func(runtime *Runtime, manager *baldaagent.RuntimeManager) sessionturn.SnapshotRuntimeProvider {
			return &snapshotRuntimeProvider{catalog: runtime, manager: manager}
		}),
		fx.Annotate(func(manager *baldaagent.SkillManager) chatapp.SkillPinner { return manager }),
		fx.Annotate(func(manager *baldaagent.SkillManager) ingressapp.SkillPinner { return manager }),
		NewLifecycle,
	),
)
