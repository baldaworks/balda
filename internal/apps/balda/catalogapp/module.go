package catalogapp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/baldaworks/balda/internal/apps/balda/actors/command"
	baldaagent "github.com/baldaworks/balda/internal/apps/balda/agent"
	"github.com/baldaworks/balda/internal/apps/balda/chatapp"
	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/commandfx"
	"github.com/baldaworks/balda/internal/apps/balda/ingressapp"
	"github.com/baldaworks/balda/internal/apps/balda/mcpruntime"
	"github.com/baldaworks/balda/internal/apps/balda/pluginapp"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalog"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
	"github.com/baldaworks/balda/internal/apps/balda/sessionturn"
	baldastate "github.com/baldaworks/balda/internal/apps/balda/state"
	runtimeconfig "github.com/normahq/runtime/v2/appconfig"
	"github.com/normahq/runtime/v2/mcpregistry"
	"go.uber.org/fx"
)

type sessionCapabilityBinder struct {
	catalog *Runtime
	skills  *baldaagent.SkillManager
}

func (b *sessionCapabilityBinder) BindSessionCapabilities(ctx context.Context, request baldaagent.SessionRuntimeRequest) (baldaagent.SessionCapabilityBinding, error) {
	if b == nil || b.catalog == nil || b.skills == nil {
		return baldaagent.SessionCapabilityBinding{}, fmt.Errorf("session capability binder is unavailable")
	}
	requestedSnapshotID := runtimecatalogcmd.SnapshotID(strings.TrimSpace(request.RuntimeSnapshotID))
	var (
		snapshot runtimecatalogcmd.Snapshot
		err      error
	)
	if requestedSnapshotID == "" {
		snapshot, err = b.catalog.CurrentSkillSnapshot(ctx, request.WorkspaceDir)
	} else {
		snapshot, err = b.catalog.RetainedSkillSnapshot(ctx, requestedSnapshotID)
	}
	if err != nil {
		return baldaagent.SessionCapabilityBinding{}, err
	}
	if snapshot.ID == "" || (requestedSnapshotID != "" && snapshot.ID != requestedSnapshotID) {
		return baldaagent.SessionCapabilityBinding{}, runtimecatalogcmd.ErrSnapshotUnavailable
	}
	projection, err := b.skills.SkillMetadataForSnapshot(ctx, snapshot.ID)
	if err != nil {
		return baldaagent.SessionCapabilityBinding{}, err
	}
	ids, release, err := b.catalog.AcquireMCPServerIDs(ctx, snapshot.ID)
	if err != nil {
		return baldaagent.SessionCapabilityBinding{}, err
	}
	var once sync.Once
	return baldaagent.SessionCapabilityBinding{
		SnapshotID:   snapshot.ID,
		Skills:       projection,
		MCPServerIDs: ids,
		Close: func() error {
			once.Do(func() {
				if release != nil {
					release()
				}
			})
			return nil
		},
	}, nil
}

type runtimeParams struct {
	fx.In

	StateDir       string `name:"balda_state_dir"`
	GlobalSkillDir globalSkillDir
	Provider       baldastate.Provider
	Advertisements []commandcmd.Advertisement `group:"balda_command_advertisements"`
	Norma          runtimeconfig.RuntimeConfig
	Registry       *mcpregistry.MapRegistry
	Commands       *commandcmd.Registry
}

type globalSkillDir string

func agentSkillsDir(home string) string {
	home = strings.TrimSpace(home)
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".agents", "skills")
}

func provideGlobalSkillDir() globalSkillDir {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return globalSkillDir(agentSkillsDir(home))
}

func newRuntime(params runtimeParams) (*Runtime, error) {
	return NewRuntime(params.StateDir, string(params.GlobalSkillDir), params.Provider, params.Advertisements, params.Norma.MCPServers, params.Registry, params.Commands)
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
		provideGlobalSkillDir,
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
		fx.Annotate(func(runtime *Runtime, manager *baldaagent.SkillManager) baldaagent.SessionCapabilityBinder {
			return &sessionCapabilityBinder{catalog: runtime, skills: manager}
		}),
		fx.Annotate(func(manager *baldaagent.SkillManager) sessionturn.SkillLoader { return manager }),
		fx.Annotate(func(manager *baldaagent.SkillManager) chatapp.SkillPinner { return manager }),
		fx.Annotate(func(manager *baldaagent.SkillManager) ingressapp.SkillPinner { return manager }),
		NewLifecycle,
	),
)
