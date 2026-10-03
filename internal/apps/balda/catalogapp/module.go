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
	"github.com/baldaworks/balda/internal/apps/balda/mcpbridge"
	"github.com/baldaworks/balda/internal/apps/balda/mcpmanage"
	"github.com/baldaworks/balda/internal/apps/balda/mcpruntime"
	"github.com/baldaworks/balda/internal/apps/balda/pluginapp"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalog"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
	"github.com/baldaworks/balda/internal/apps/balda/sessionturn"
	baldastate "github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/normahq/runtime/v2/agentconfig"
	runtimeconfig "github.com/normahq/runtime/v2/appconfig"
	"github.com/normahq/runtime/v2/mcpregistry"
	"go.uber.org/fx"
)

type sessionCapabilityBinder struct {
	catalog   *Runtime
	skills    *baldaagent.SkillManager
	providers map[string]agentconfig.Config
	extra     []string
}

func (b *sessionCapabilityBinder) BindSessionCapabilities(ctx context.Context, providerID string, request baldaagent.SessionRuntimeRequest) (baldaagent.SessionCapabilityBinding, error) {
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
	var ids []string
	var selections map[string][]string
	var release func()
	if len(b.providers) == 0 && len(snapshot.MCPServers) == 0 {
		ids, release, err = b.catalog.AcquireMCPServerIDs(ctx, snapshot.ID)
	} else {
		var defaults map[string][]string
		defaults, err = providerMCPDefaults(b.providers, providerID, b.extra)
		if err == nil {
			selections, release, err = b.catalog.AcquireProviderMCPServerIDs(ctx, snapshot.ID, defaults)
			ids = selections[providerID]
		}
	}
	if err != nil {
		return baldaagent.SessionCapabilityBinding{}, err
	}
	var once sync.Once
	return baldaagent.SessionCapabilityBinding{
		SnapshotID:           snapshot.ID,
		Skills:               projection,
		MCPServerIDs:         ids,
		ProviderMCPServerIDs: selections,
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
	AgentSkillDir  agentSkillDir
	CodexSkillDir  codexSkillDir
	Provider       baldastate.Provider
	Advertisements []commandcmd.Advertisement `group:"balda_command_advertisements"`
	Norma          runtimeconfig.RuntimeConfig
	Registry       *mcpregistry.MapRegistry
	Commands       *commandcmd.Registry
	Credentials    *mcpmanage.Service
	Bridge         *mcpbridge.Bridge
	ProviderID     string   `name:"balda_provider"`
	MCPServerIDs   []string `name:"balda_mcp_servers"`
}

type agentSkillDir string

type codexSkillDir string

func agentSkillsDir(home string) string {
	home = strings.TrimSpace(home)
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".agents", "skills")
}

func codexSkillsDir(home, codexHome string) string {
	codexHome = strings.TrimSpace(codexHome)
	if codexHome == "" {
		home = strings.TrimSpace(home)
		if home == "" {
			return ""
		}
		codexHome = filepath.Join(home, ".codex")
	}
	return filepath.Join(codexHome, "skills")
}

func provideAgentSkillDir() agentSkillDir {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return agentSkillDir(agentSkillsDir(home))
}

func provideCodexSkillDir() codexSkillDir {
	home, err := os.UserHomeDir()
	if err != nil && strings.TrimSpace(os.Getenv("CODEX_HOME")) == "" {
		return ""
	}
	return codexSkillDir(codexSkillsDir(home, os.Getenv("CODEX_HOME")))
}

func newRuntime(params runtimeParams) (*Runtime, error) {
	runtime, err := NewRuntime(params.StateDir, string(params.AgentSkillDir), string(params.CodexSkillDir), params.Provider, params.Advertisements, params.Norma.MCPServers, params.Registry, params.Commands, params.Credentials, params.Bridge)
	if err != nil {
		return nil, err
	}
	if err := runtime.configureProviderMCP(params.Norma.Providers, params.ProviderID, params.MCPServerIDs); err != nil {
		return nil, err
	}
	return runtime, nil
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
		fx.Annotate(
			commandcmd.NewRegistryWithAdvertisements,
			fx.ParamTags(`group:"balda_command_advertisements"`),
		),
		provideAgentSkillDir,
		provideCodexSkillDir,
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
		fx.Annotate(func(runtime *Runtime, manager *baldaagent.SkillManager, config runtimeconfig.RuntimeConfig, extra []string) baldaagent.SessionCapabilityBinder {
			return &sessionCapabilityBinder{catalog: runtime, skills: manager, providers: config.Providers, extra: append([]string(nil), extra...)}
		}, fx.ParamTags("", "", "", `name:"balda_mcp_servers"`)),
		fx.Annotate(func(manager *baldaagent.SkillManager) sessionturn.SkillLoader { return manager }),
		fx.Annotate(func(manager *baldaagent.SkillManager) chatapp.SkillPinner { return manager }),
		fx.Annotate(func(manager *baldaagent.SkillManager) ingressapp.SkillPinner { return manager }),
		NewLifecycle,
	),
)
