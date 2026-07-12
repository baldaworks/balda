package sessionapp

import (
	"context"
	"errors"

	baldaagent "github.com/normahq/balda/internal/apps/balda/agent"
	baldasession "github.com/normahq/balda/internal/apps/balda/session"
	"go.uber.org/fx"
	adksession "google.golang.org/adk/v2/session"
)

type SessionAgentBuilderAdapter struct {
	Builder *baldaagent.Builder
}

func (a SessionAgentBuilderAdapter) CreateRuntimeSession(
	ctx context.Context,
	runtime *baldasession.BuiltRuntime,
	agentName string,
	userID string,
	sessionID string,
	workspaceDir string,
	sessionCtx baldasession.RuntimeSessionContext,
) (adksession.Session, error) {
	if a.Builder == nil {
		return nil, errors.New("agent builder is required")
	}
	return a.Builder.CreateRuntimeSession(ctx, &baldaagent.BuiltRuntime{
		Agent:      runtime.Agent,
		Runner:     runtime.Runner,
		SessionSvc: runtime.SessionSvc,
		AppName:    runtime.AppName,
	}, agentName, userID, sessionID, workspaceDir, baldaagent.RuntimeSessionContext{
		BaldaSessionID: sessionCtx.BaldaSessionID,
		SessionBranch:  sessionCtx.SessionBranch,
	})
}

func (a SessionAgentBuilderAdapter) GetAgentMetadata(agentName string) baldasession.AgentMetadata {
	meta := a.Builder.GetAgentMetadata(agentName)
	return baldasession.AgentMetadata{
		Type:       meta.Type,
		Model:      meta.Model,
		MCPServers: append([]string(nil), meta.MCPServers...),
	}
}

type SessionRuntimeManagerAdapter struct {
	Manager *baldaagent.RuntimeManager
}

func (a SessionRuntimeManagerAdapter) Runtime(ctx context.Context) (*baldasession.BuiltRuntime, error) {
	runtime, err := a.Manager.Runtime(ctx)
	if err != nil {
		return nil, err
	}
	return &baldasession.BuiltRuntime{
		Agent:      runtime.Agent,
		Runner:     runtime.Runner,
		SessionSvc: runtime.SessionSvc,
		AppName:    runtime.AppName,
	}, nil
}

func (a SessionRuntimeManagerAdapter) ProviderID() string {
	return a.Manager.ProviderID()
}

type SessionWorkspaceManagerAdapter struct {
	Manager *baldaagent.WorkspaceManager
}

func (a SessionWorkspaceManagerAdapter) CanonicalWorkspaceDir(key string) string {
	return a.Manager.CanonicalWorkspaceDir(key)
}

func (a SessionWorkspaceManagerAdapter) ForceRemountCanonicalWorkspace(ctx context.Context, key, branchName string) (baldasession.EnsureWorkspaceResult, error) {
	result, err := a.Manager.ForceRemountCanonicalWorkspace(ctx, key, branchName)
	return baldasession.EnsureWorkspaceResult{Dir: result.Dir, SyncSkipped: result.SyncSkipped}, translateWorkspaceError(err)
}

func (a SessionWorkspaceManagerAdapter) EnsureWorkspace(ctx context.Context, key, branchName, existingPath string) (baldasession.EnsureWorkspaceResult, error) {
	result, err := a.Manager.EnsureWorkspace(ctx, key, branchName, existingPath)
	return baldasession.EnsureWorkspaceResult{Dir: result.Dir, SyncSkipped: result.SyncSkipped}, translateWorkspaceError(err)
}

func (a SessionWorkspaceManagerAdapter) Import(ctx context.Context, workspaceDir string) error {
	return a.Manager.Import(ctx, workspaceDir)
}

func (a SessionWorkspaceManagerAdapter) Export(ctx context.Context, workspaceDir, branchName, commitMessage string) error {
	return a.Manager.Export(ctx, workspaceDir, branchName, commitMessage)
}

func (a SessionWorkspaceManagerAdapter) CleanupWorkspace(ctx context.Context, workspaceDir string) error {
	return a.Manager.CleanupWorkspace(ctx, workspaceDir)
}

func translateWorkspaceError(err error) error {
	if errors.Is(err, baldaagent.ErrWorkspaceCollision) {
		return errors.Join(baldasession.ErrWorkspaceCollision, err)
	}
	return err
}

var Module = fx.Module("balda_sessionapp",
	fx.Provide(
		baldaagent.NewBuilder,
		baldaagent.NewRuntimeManager,
		func(builder *baldaagent.Builder) baldasession.AgentBuilder {
			return SessionAgentBuilderAdapter{Builder: builder}
		},
		func(manager *baldaagent.RuntimeManager) baldasession.RuntimeManager {
			return SessionRuntimeManagerAdapter{Manager: manager}
		},
		fx.Annotate(
			func(workingDir string, stateDir string, baseBranch string, sessionsDir string) baldasession.WorkspaceManager {
				return SessionWorkspaceManagerAdapter{
					Manager: baldaagent.NewWorkspaceManagerWithSessionsDir(workingDir, stateDir, baseBranch, sessionsDir),
				}
			},
			fx.ParamTags(``, `name:"balda_state_dir"`, `name:"balda_workspace_base_branch"`, `name:"balda_workspace_sessions_dir"`),
		),
		baldasession.NewManager,
	),
)
