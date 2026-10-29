package sessionapp

import (
	"context"
	"errors"

	baldaagent "github.com/baldaworks/balda/internal/apps/balda/agent"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/scheduledjobs"
	baldasession "github.com/baldaworks/balda/internal/apps/balda/session"
	"github.com/baldaworks/balda/internal/apps/balda/sessionmemoryapp"
	"github.com/baldaworks/balda/internal/apps/balda/sessionmemorymcp"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/balda/sessionmemory"
	"github.com/baldaworks/runtime/v2/agentconfig"
	"github.com/baldaworks/runtime/v2/agentproduct"
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
	return a.Builder.CreateRuntimeSession(ctx, agentRuntime(runtime), agentName, userID, sessionID, workspaceDir, agentSessionContext(sessionCtx))
}

func (a SessionAgentBuilderAdapter) OpenExistingRuntimeSession(
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
	return a.Builder.OpenExistingRuntimeSession(ctx, agentRuntime(runtime), agentName, userID, sessionID, workspaceDir, agentSessionContext(sessionCtx))
}

func (a SessionAgentBuilderAdapter) CreateFreshRuntimeSession(
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
	return a.Builder.CreateFreshRuntimeSession(ctx, agentRuntime(runtime), agentName, userID, sessionID, workspaceDir, agentSessionContext(sessionCtx))
}

func agentRuntime(runtime *baldasession.BuiltRuntime) *baldaagent.BuiltRuntime {
	return &baldaagent.BuiltRuntime{
		Agent:               runtime.Agent,
		SessionControl:      runtime.SessionControl,
		Runner:              runtime.Runner,
		SessionSvc:          runtime.SessionSvc,
		AppName:             runtime.AppName,
		RuntimeSnapshotID:   runtime.RuntimeSnapshotID,
		ProviderBindingJSON: runtime.ProviderBindingJSON,
		RecipeModel:         runtime.RecipeModel,
		ConfirmedModel:      runtime.ConfirmedModel,
	}
}

func agentSessionContext(sessionCtx baldasession.RuntimeSessionContext) baldaagent.RuntimeSessionContext {
	return baldaagent.RuntimeSessionContext{
		ArchiveID:      sessionCtx.ArchiveID,
		BaldaSessionID: sessionCtx.BaldaSessionID,
		SessionBranch:  sessionCtx.SessionBranch,
	}
}

func (a SessionAgentBuilderAdapter) GetAgentMetadata(agentName string) baldasession.AgentMetadata {
	meta := a.Builder.GetAgentMetadata(agentName)
	return baldasession.AgentMetadata{
		Type:            meta.Type,
		Model:           meta.Model,
		ReasoningEffort: meta.ReasoningEffort,
		MCPServers:      append([]string(nil), meta.MCPServers...),
	}
}

type SessionRuntimeManagerAdapter struct {
	Manager *baldaagent.RuntimeManager
}

// SessionKindResolverAdapter derives conversation policy from the transport
// classifiers already registered at the composition root.
type SessionKindResolverAdapter struct {
	Resolver sessionmemoryapp.ScopeResolver
}

// ResolveKind implements session.KindResolver.
func (a SessionKindResolverAdapter) ResolveKind(locator baldasession.SessionLocator) (baldasession.Kind, error) {
	scope, err := a.Resolver.Resolve(locator)
	if err != nil {
		return "", err
	}
	switch scope.Kind {
	case sessionmemory.ScopeKindPersonal:
		return baldasession.KindDM, nil
	case sessionmemory.ScopeKindGroup:
		return baldasession.KindChat, nil
	default:
		return "", errors.New("unsupported locator scope kind")
	}
}

func (a SessionRuntimeManagerAdapter) Runtime(ctx context.Context) (*baldasession.BuiltRuntime, error) {
	runtime, err := a.Manager.Runtime(ctx)
	if err != nil {
		return nil, err
	}
	return sessionRuntime(runtime), nil
}

func (a SessionRuntimeManagerAdapter) ProviderID() string {
	return a.Manager.ProviderID()
}

func (a SessionRuntimeManagerAdapter) RuntimeForSession(ctx context.Context, request baldasession.SessionRuntimeRequest) (*baldasession.BuiltRuntime, error) {
	runtime, err := a.Manager.RuntimeForSession(ctx, baldaagent.SessionRuntimeRequest{
		Locator: deliverycmd.Locator{
			ChannelType: request.Locator.ChannelType,
			AddressKey:  request.Locator.AddressKey,
			AddressJSON: request.Locator.AddressJSON,
			SessionID:   request.Locator.SessionID,
		},
		UserID:              request.UserID,
		AgentSessionID:      request.AgentSessionID,
		LineageID:           request.LineageID,
		WorkspaceDir:        request.WorkspaceDir,
		RuntimeSnapshotID:   request.RuntimeSnapshotID,
		ProviderBindingJSON: request.ProviderBindingJSON,
		ModelOverride:       request.ModelOverride,
	})
	if err != nil {
		return nil, err
	}
	return sessionRuntime(runtime), nil
}

func (a SessionRuntimeManagerAdapter) BuildUnpublishedSessionRuntime(ctx context.Context, request baldasession.SessionRuntimeRequest) (baldasession.UnpublishedSessionRuntimeCandidate, error) {
	candidate, err := a.Manager.BuildUnpublishedSessionRuntime(ctx, baldaagent.SessionRuntimeRequest{
		Locator: deliverycmd.Locator{ChannelType: request.Locator.ChannelType, AddressKey: request.Locator.AddressKey, AddressJSON: request.Locator.AddressJSON, SessionID: request.Locator.SessionID},
		UserID:  request.UserID, AgentSessionID: request.AgentSessionID, LineageID: request.LineageID,
		WorkspaceDir: request.WorkspaceDir, RuntimeSnapshotID: request.RuntimeSnapshotID,
		ProviderBindingJSON: request.ProviderBindingJSON, ModelOverride: request.ModelOverride,
		ExpectedRecipeModel: request.ExpectedRecipeModel, RecoveryFence: request.RecoveryFence,
	})
	if err != nil {
		return nil, err
	}
	return &unpublishedRuntimeCandidateAdapter{candidate: candidate, runtime: sessionRuntime(candidate.Runtime())}, nil
}

type unpublishedRuntimeCandidateAdapter struct {
	candidate *baldaagent.UnpublishedSessionRuntimeCandidate
	runtime   *baldasession.BuiltRuntime
}

func (a *unpublishedRuntimeCandidateAdapter) Runtime() *baldasession.BuiltRuntime { return a.runtime }
func (a *unpublishedRuntimeCandidateAdapter) ExpectedBaseToken() uint64 {
	return uint64(a.candidate.ExpectedBaseToken())
}
func (a *unpublishedRuntimeCandidateAdapter) ClaimForPublication() error {
	return a.candidate.ClaimForPublication()
}
func (a *unpublishedRuntimeCandidateAdapter) Promote() error { return a.candidate.Promote() }
func (a *unpublishedRuntimeCandidateAdapter) MarkPublished() { a.candidate.MarkPublished() }
func (a *unpublishedRuntimeCandidateAdapter) CompletePublication() error {
	return a.candidate.CompletePublication()
}
func (a *unpublishedRuntimeCandidateAdapter) Abort(ctx context.Context) error {
	return a.candidate.Abort(ctx)
}

func (a SessionRuntimeManagerAdapter) FenceSessionRuntimeBase(sessionID string) error {
	return a.Manager.FenceSessionRuntimeBase(sessionID)
}

func (a SessionRuntimeManagerAdapter) CompleteSessionRuntimeRecovery(sessionID string, token uint64) error {
	return a.Manager.CompleteSessionRuntimeRecovery(sessionID, token)
}

func (a SessionRuntimeManagerAdapter) AdoptPromotedSessionRuntime(sessionID string, incumbentToken uint64, request baldasession.SessionRuntimeRequest) (baldasession.UnpublishedSessionRuntimeCandidate, bool) {
	candidate, ok := a.Manager.AdoptPromotedSessionRuntime(sessionID, incumbentToken, baldaagent.SessionRuntimeRequest{
		Locator: deliverycmd.Locator{ChannelType: request.Locator.ChannelType, AddressKey: request.Locator.AddressKey, AddressJSON: request.Locator.AddressJSON, SessionID: request.Locator.SessionID},
		UserID:  request.UserID, AgentSessionID: request.AgentSessionID, LineageID: request.LineageID,
		WorkspaceDir: request.WorkspaceDir, RuntimeSnapshotID: request.RuntimeSnapshotID,
		ProviderBindingJSON: request.ProviderBindingJSON, ModelOverride: request.ModelOverride,
		ExpectedRecipeModel: request.ExpectedRecipeModel, RecoveryFence: request.RecoveryFence,
	})
	if !ok {
		return nil, false
	}
	return &unpublishedRuntimeCandidateAdapter{candidate: candidate, runtime: sessionRuntime(candidate.Runtime())}, true
}

func (a SessionRuntimeManagerAdapter) AdoptPromotedModelReplacement(
	sessionID string,
	incumbentToken uint64,
	runtimeSnapshotID string,
	providerBindingJSON string,
	targetModel string,
) (*baldasession.BuiltRuntime, bool) {
	runtime, ok := a.Manager.AdoptPromotedModelReplacement(
		sessionID, incumbentToken, runtimeSnapshotID, providerBindingJSON, targetModel,
	)
	if !ok {
		return nil, false
	}
	return sessionRuntime(runtime), true
}

func sessionRuntime(runtime *baldaagent.BuiltRuntime) *baldasession.BuiltRuntime {
	if runtime == nil {
		return nil
	}
	adapted := &baldasession.BuiltRuntime{
		Agent:               runtime.Agent,
		SessionControl:      runtime.SessionControl,
		Runner:              runtime.Runner,
		SessionSvc:          runtime.SessionSvc,
		AppName:             runtime.AppName,
		RuntimeSnapshotID:   runtime.RuntimeSnapshotID,
		ProviderBindingJSON: runtime.ProviderBindingJSON,
		RecipeModel:         runtime.RecipeModel,
		ConfirmedModel:      runtime.ConfirmedModel,
		ProviderID:          runtime.ProviderID,
		RuntimeBaseToken:    runtime.RuntimeBaseToken,
		PromoteBase:         runtime.PromoteReplacementBase,
		CompleteBase:        runtime.CompleteReplacementBase,
		Close:               runtime.Close,
	}
	adapted.ModelController = modelControllerAdapter{runtime: runtime}
	return adapted
}

type modelControllerAdapter struct{ runtime *baldaagent.BuiltRuntime }

func (a modelControllerAdapter) InspectSessionControl(ctx context.Context, identity baldasession.RuntimeSessionIdentity) agentproduct.SessionControlSnapshot {
	return a.runtime.InspectSessionControl(ctx, baldaagent.SessionIdentity{
		SessionService: identity.SessionService, AppName: identity.AppName, UserID: identity.UserID, SessionID: identity.SessionID,
	})
}

func (a modelControllerAdapter) ValidateModel(ctx context.Context, identity baldasession.RuntimeSessionIdentity, model string) agentproduct.ModelValidation {
	return a.runtime.ValidateModel(ctx, baldaagent.SessionIdentity{
		SessionService: identity.SessionService, AppName: identity.AppName, UserID: identity.UserID, SessionID: identity.SessionID,
	}, model)
}

func (a modelControllerAdapter) ChangeModel(ctx context.Context, identity baldasession.RuntimeSessionIdentity, model string) baldasession.RuntimeModelChange {
	change := a.runtime.ChangeModel(ctx, baldaagent.SessionIdentity{
		SessionService: identity.SessionService, AppName: identity.AppName, UserID: identity.UserID, SessionID: identity.SessionID,
	}, model)
	return baldasession.RuntimeModelChange{
		Status: change.Status, EffectiveModel: change.EffectiveModel, Reason: change.Reason,
		Candidate: sessionRuntime(change.Candidate),
		Identity: baldasession.VerifiedRuntimeSessionIdentity{
			SessionService: change.Identity.SessionService, AppName: change.Identity.AppName,
			UserID: change.Identity.UserID, SessionID: change.Identity.SessionID,
		},
	}
}

// SessionMCPBinderAdapter bridges the MCP context broker to the runtime port.
// It is kept in the composition-root package so the provider runtime does not
// import the concrete MCP surface.
type SessionMCPBinderAdapter struct {
	Broker  *sessionmemorymcp.ContextBroker
	Enabled bool
}

func (a SessionMCPBinderAdapter) SessionMCPEnabled(capabilities baldaagent.SessionCapabilityBinding) bool {
	return a.Enabled || len(capabilities.Skills.Skills) > 0
}

func (a SessionMCPBinderAdapter) BindSession(_ context.Context, request baldaagent.SessionRuntimeRequest) (baldaagent.ScopedMCPServer, error) {
	if a.Broker == nil {
		return baldaagent.ScopedMCPServer{}, errors.New("session MCP context broker is required")
	}
	binding, err := a.Broker.Bind(sessionmemorymcp.CurrentSession{
		Locator:           request.Locator,
		RuntimeSnapshotID: request.RuntimeSnapshotID,
		Session: sessionmemory.SessionRef{
			SessionID:      request.Locator.SessionID,
			AgentSessionID: request.AgentSessionID,
			LineageID:      request.LineageID,
		},
	})
	if err != nil {
		return baldaagent.ScopedMCPServer{}, err
	}
	return baldaagent.ScopedMCPServer{
		ID: binding.ID,
		Config: agentconfig.MCPServerConfig{
			Type: agentconfig.MCPServerTypeHTTP,
			URL:  binding.URL,
			// Same endpoint as the plain bundled server, so both are collapsed
			// to one toolset. This entry wins because its URL carries the
			// context token the broker needs to inject session headers.
			DedupKey:       sessionmemorymcp.BundledDedupKey,
			DedupPreferred: true,
		},
		Release: binding.Release,
	}, nil
}

type SessionWorkspaceManagerAdapter struct {
	Manager *baldaagent.WorkspaceManager
}

func (a SessionWorkspaceManagerAdapter) CanonicalWorkspaceDir(key string) string {
	return a.Manager.CanonicalWorkspaceDir(key)
}

func (a SessionWorkspaceManagerAdapter) ForceRemountCanonicalWorkspace(ctx context.Context, key, branchName string) (baldasession.EnsureWorkspaceResult, error) {
	result, err := a.Manager.ForceRemountCanonicalWorkspace(ctx, key, branchName)
	return baldasession.EnsureWorkspaceResult{Dir: result.Dir, SyncSkipped: result.SyncSkipped, Created: result.Created}, translateWorkspaceError(err)
}

func (a SessionWorkspaceManagerAdapter) EnsureWorkspace(ctx context.Context, key, branchName, existingPath string) (baldasession.EnsureWorkspaceResult, error) {
	result, err := a.Manager.EnsureWorkspace(ctx, key, branchName, existingPath)
	return baldasession.EnsureWorkspaceResult{Dir: result.Dir, SyncSkipped: result.SyncSkipped, Created: result.Created}, translateWorkspaceError(err)
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

func (a SessionWorkspaceManagerAdapter) DeleteBranch(ctx context.Context, branchName string) error {
	return a.Manager.DeleteBranch(ctx, branchName)
}

func translateWorkspaceError(err error) error {
	if errors.Is(err, baldaagent.ErrWorkspaceCollision) {
		return errors.Join(baldasession.ErrWorkspaceCollision, err)
	}
	return err
}

var Module = fx.Module("balda_sessionapp",
	fx.Provide(
		baldaagent.NewRunnerFactory,
		baldaagent.NewBuilder,
		baldaagent.NewRuntimeManager,
		NewProgressService,
		fx.Annotate(
			NewBoundaryObserverComposite,
			fx.ParamTags(`group:"balda_session_boundary_observer"`),
		),
		func(builder *baldaagent.Builder) baldasession.AgentBuilder {
			return SessionAgentBuilderAdapter{Builder: builder}
		},
		func(manager *baldaagent.RuntimeManager) baldasession.RuntimeManager {
			return SessionRuntimeManagerAdapter{Manager: manager}
		},
		func(resolver sessionmemoryapp.ScopeResolver) baldasession.KindResolver {
			return SessionKindResolverAdapter{Resolver: resolver}
		},
		fx.Annotate(
			func(broker *sessionmemorymcp.ContextBroker, enabled bool) baldaagent.SessionMCPBinder {
				return SessionMCPBinderAdapter{Broker: broker, Enabled: enabled}
			},
			fx.ParamTags(``, `name:"balda_session_memory_enabled"`),
		),
		fx.Annotate(
			func(workingDir string, stateDir string, baseBranch string, sessionsDir string) baldasession.WorkspaceManager {
				return SessionWorkspaceManagerAdapter{
					Manager: baldaagent.NewWorkspaceManagerWithSessionsDir(workingDir, stateDir, baseBranch, sessionsDir),
				}
			},
			fx.ParamTags(``, `name:"balda_state_dir"`, `name:"balda_workspace_base_branch"`, `name:"balda_workspace_sessions_dir"`),
		),
		baldasession.NewManager,
		func(manager *baldasession.Manager) scheduledjobs.RunSessionCloser { return manager },
		fx.Annotate(
			NewTopicService,
			fx.ParamTags(`optional:"true"`),
		),
		fx.Annotate(
			NewBootstrapService,
			fx.ParamTags(`optional:"true"`, `optional:"true"`),
		),
		fx.Annotate(
			func(capture *sessionmemoryapp.BoundaryCapture) baldasession.BoundaryObserver {
				return SessionBoundaryObserverAdapter{Capture: capture}
			},
			fx.As(new(baldasession.BoundaryObserver)),
			fx.ResultTags(`group:"balda_session_boundary_observer"`),
		),
		fx.Annotate(
			func(provider state.Provider) baldasession.BoundaryObserver {
				return TurnStatusBoundaryObserver{Store: provider.TurnStatus()}
			},
			fx.As(new(baldasession.BoundaryObserver)),
			fx.ResultTags(`group:"balda_session_boundary_observer"`),
		),
	),
)
