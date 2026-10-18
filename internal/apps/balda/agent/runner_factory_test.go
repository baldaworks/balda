package agent

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/runtime/v2/agentfactory"
	adkagent "google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/plugin"
	"google.golang.org/adk/v2/runner"
	adksession "google.golang.org/adk/v2/session"
)

type recordingHostPluginFactory struct {
	scopes  []ToolHookScope
	plugins []*plugin.Plugin
	err     error
	name    string
}

func (f *recordingHostPluginFactory) NewToolPlugin(scope ToolHookScope) (*plugin.Plugin, error) {
	f.scopes = append(f.scopes, scope)
	if f.err != nil {
		return nil, f.err
	}
	p, err := plugin.New(plugin.Config{Name: f.name})
	if err != nil {
		return nil, err
	}
	f.plugins = append(f.plugins, p)
	return p, nil
}

func mustTestPlugin(t *testing.T, name string) *plugin.Plugin {
	t.Helper()
	p, err := plugin.New(plugin.Config{Name: name})
	if err != nil {
		t.Fatalf("plugin.New() error = %v", err)
	}
	return p
}

func TestRunnerFactoryPreservesCallerConfigAndCreatesFreshPlugins(t *testing.T) {
	t.Parallel()

	callerPlugin := mustTestPlugin(t, "caller")
	callerPlugins := make([]*plugin.Plugin, 1, 2)
	callerPlugins[0] = callerPlugin
	host := &recordingHostPluginFactory{name: "balda.tool-hooks"}
	factory := NewRunnerFactory(RunnerFactoryParams{Host: host})
	cfg := runner.Config{
		AppName:           "test-app",
		Agent:             mustRunnerFactoryAgent(t, "root"),
		SessionService:    adksession.InMemoryService(),
		AutoCreateSession: true,
		PluginConfig: runner.PluginConfig{
			Plugins:      callerPlugins,
			CloseTimeout: 17 * time.Second,
		},
	}
	scope := ToolHookScope{Enabled: true, Provider: "provider", CWD: "/workspace"}

	prepared, err := factory.prepareConfig(cfg, scope)
	if err != nil {
		t.Fatalf("prepareConfig() error = %v", err)
	}
	preparedAgain, err := factory.prepareConfig(cfg, scope)
	if err != nil {
		t.Fatalf("second prepareConfig() error = %v", err)
	}

	if prepared.AppName != cfg.AppName || prepared.Agent != cfg.Agent || prepared.SessionService != cfg.SessionService || prepared.AutoCreateSession != cfg.AutoCreateSession {
		t.Fatalf("prepared config changed caller fields: got %+v, want fields from %+v", prepared, cfg)
	}
	if prepared.PluginConfig.CloseTimeout != cfg.PluginConfig.CloseTimeout {
		t.Fatalf("CloseTimeout = %v, want %v", prepared.PluginConfig.CloseTimeout, cfg.PluginConfig.CloseTimeout)
	}
	if len(prepared.PluginConfig.Plugins) != 2 || prepared.PluginConfig.Plugins[1] != callerPlugin {
		t.Fatalf("prepared plugins = %#v, want host plugin followed by caller plugin", prepared.PluginConfig.Plugins)
	}
	if len(cfg.PluginConfig.Plugins) != 1 || cfg.PluginConfig.Plugins[0] != callerPlugin || callerPlugins[:cap(callerPlugins)][1] != nil {
		t.Fatalf("caller plugins were mutated: %#v", callerPlugins[:cap(callerPlugins)])
	}
	if len(host.plugins) != 2 || host.plugins[0] == host.plugins[1] {
		t.Fatalf("host plugins = %#v, want two fresh instances", host.plugins)
	}
	if prepared.PluginConfig.Plugins[0] != host.plugins[0] || preparedAgain.PluginConfig.Plugins[0] != host.plugins[1] {
		t.Fatalf("prepared configs do not contain their newly-created host plugins")
	}
}

func TestRunnerFactoryDelegatesDuplicatePluginNamesToADK(t *testing.T) {
	t.Parallel()

	host := &recordingHostPluginFactory{name: "balda.tool-hooks"}
	factory := NewRunnerFactory(RunnerFactoryParams{Host: host})
	_, err := factory.New(runner.Config{
		AppName:        "test-app",
		Agent:          mustRunnerFactoryAgent(t, "root"),
		SessionService: adksession.InMemoryService(),
		PluginConfig: runner.PluginConfig{
			Plugins: []*plugin.Plugin{mustTestPlugin(t, "balda.tool-hooks")},
		},
	}, ToolHookScope{Enabled: true})
	if err == nil || !strings.Contains(err.Error(), "plugin with name 'balda.tool-hooks' already registered") {
		t.Fatalf("RunnerFactory.New() error = %v, want duplicate plugin name", err)
	}
}

func TestRunnerFactoryPropagatesHostFactoryErrors(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("host unavailable")
	factory := NewRunnerFactory(RunnerFactoryParams{Host: &recordingHostPluginFactory{err: wantErr}})
	_, err := factory.prepareConfig(runner.Config{}, ToolHookScope{Enabled: true})
	if !errors.Is(err, wantErr) {
		t.Fatalf("prepareConfig() error = %v, want %v", err, wantErr)
	}
}

func TestUserFacingRunnerConstructionUsesScopedHostPlugins(t *testing.T) {
	host := &recordingHostPluginFactory{name: "balda.tool-hooks"}
	factory := NewRunnerFactory(RunnerFactoryParams{Host: host})
	provider := "provider-one"
	mainCWD := t.TempDir()
	goalCWD := t.TempDir()
	builder := &Builder{
		factory:       &runnerFactoryAgentFactory{},
		runnerFactory: factory,
	}

	mainRuntime, err := builder.BuildRuntimeWithMCPServerIDs(context.Background(), provider, mainCWD, nil, nil)
	if err != nil {
		t.Fatalf("BuildRuntimeWithMCPServerIDs() error = %v", err)
	}
	child := childRuntimeBase{
		runtime:    mainRuntime,
		builder:    builder,
		providerID: provider,
		workingDir: mainCWD,
	}
	if _, err := child.runner(mustRunnerFactoryAgent(t, "child"), "child", mainCWD); err != nil {
		t.Fatalf("childRuntimeBase.runner() error = %v", err)
	}
	_, workerSessionID, validatorSessionID := newGoalWorkflowTestSessions(
		t,
		context.Background(),
		mainRuntime.SessionSvc,
		mainRuntime.AppName,
	)
	if _, err := builder.BuildGoalWorkflow(context.Background(), GoalBuildConfig{
		BaseAgent:          mustRunnerFactoryAgent(t, "goal-base"),
		ProviderID:         provider,
		SessionID:          "goal-session",
		WorkerSessionID:    workerSessionID,
		ValidatorSessionID: validatorSessionID,
		WorkspaceDir:       goalCWD,
		MaxIterations:      1,
		AppName:            mainRuntime.AppName,
		SessionService:     mainRuntime.SessionSvc,
	}); err != nil {
		t.Fatalf("BuildGoalWorkflow() error = %v", err)
	}

	want := []ToolHookScope{
		{Enabled: true, Provider: provider, CWD: mainCWD},
		{Enabled: true, Provider: provider, CWD: mainCWD},
		{Enabled: true, Provider: provider, CWD: goalCWD},
		{Enabled: true, Provider: provider, CWD: goalCWD},
	}
	if !reflect.DeepEqual(host.scopes, want) {
		t.Fatalf("host scopes = %#v, want %#v", host.scopes, want)
	}
}

func TestBuildDedicatedRuntimeDoesNotEnableHostPlugin(t *testing.T) {
	host := &recordingHostPluginFactory{name: "balda.tool-hooks"}
	builder := &Builder{
		dedicatedFactory: &runnerFactoryAgentFactory{},
		runnerFactory:    NewRunnerFactory(RunnerFactoryParams{Host: host}),
	}
	if _, err := builder.BuildDedicatedRuntime(context.Background(), "memory", t.TempDir(), "derive facts"); err != nil {
		t.Fatalf("BuildDedicatedRuntime() error = %v", err)
	}
	if len(host.scopes) != 0 {
		t.Fatalf("dedicated runtime host scopes = %#v, want none", host.scopes)
	}
}

type runnerFactoryAgentFactory struct{}

func (*runnerFactoryAgentFactory) Build(_ context.Context, req agentfactory.BuildRequest) (adkagent.Agent, error) {
	return adkagent.New(adkagent.Config{Name: req.Name, Description: req.Description})
}

func mustRunnerFactoryAgent(t *testing.T, name string) adkagent.Agent {
	t.Helper()
	agent, err := adkagent.New(adkagent.Config{Name: name})
	if err != nil {
		t.Fatalf("agent.New() error = %v", err)
	}
	return agent
}
