package session

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/chatarchivecmd"
	"github.com/baldaworks/balda/internal/apps/balda/hookcmd"
	"github.com/baldaworks/balda/internal/apps/balda/sessioncmd"
	baldastate "github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/runtime/v2/agentproduct"
	adksession "google.golang.org/adk/v2/session"
)

const (
	freshLifetimeProviderID      = "balda-provider"
	freshLifetimeHostedModel     = "hosted-model"
	freshLifetimeCurrentCatalog  = "catalog-current-B"
	freshLifetimeRecoveryCatalog = "catalog-recovery-B"
	freshLifetimeLaterCatalog    = "catalog-recovery-C"
)

type missingTargetArchive struct {
	baldastate.ChatArchiveStore
	target string
}

func (s missingTargetArchive) Get(ctx context.Context, id string) (chatarchivecmd.Session, error) {
	if id == s.target {
		return chatarchivecmd.Session{}, chatarchivecmd.ErrNotFound
	}
	return s.ChatArchiveStore.Get(ctx, id)
}

func TestFreshLifetimeDoesNotCreateMissingSession(t *testing.T) {
	f := newArchiveFixture(t)
	manager := f.manager()
	manager.runtimeOperations = f.provider.RuntimeOperations()
	request := FreshLifetimeRequest{OperationID: "missing-reset", SessionID: archiveInput().Locator.SessionID, InputHash: strings.Repeat("b", 64)}
	prepared, result, err := manager.PrepareFreshLifetime(t.Context(), request)
	if prepared != nil || result.Status != FreshLifetimeUnavailable || !errors.Is(err, ErrNoPersistedSession) {
		t.Fatalf("missing session prepare = %v, %+v, %v", prepared, result, err)
	}
	if len(manager.sessions) != 0 {
		t.Fatal("reset created an unsolicited session")
	}
}

func TestFreshLifetimeColdRejectsInvalidBindingBeforeSnapshotOrJournal(t *testing.T) {
	f := newArchiveFixture(t)
	f.runtime.ProviderBindingJSON = publicationBinding
	topic, err := f.manager().EnsureSession(t.Context(), archiveInput(), "saved")
	if err != nil {
		t.Fatal(err)
	}
	record, found, err := f.provider.Sessions().GetBySessionID(t.Context(), topic.GetSessionID())
	if err != nil || !found {
		t.Fatalf("record = %+v, %t, %v", record, found, err)
	}
	record.ProviderBindingJSON = `{"RootProviderID":""}`
	if err := f.provider.Sessions().DeleteBySessionID(t.Context(), record.SessionID); err != nil {
		t.Fatal(err)
	}
	if err := f.provider.Sessions().Upsert(t.Context(), record); err != nil {
		t.Fatal(err)
	}

	restarted := f.manager()
	restarted.runtimeOperations = f.provider.RuntimeOperations()
	request := FreshLifetimeRequest{OperationID: "invalid-binding-reset", SessionID: record.SessionID, InputHash: strings.Repeat("b", 64)}
	prepared, result, _ := restarted.PrepareFreshLifetime(t.Context(), request)
	if prepared != nil || result.Status != FreshLifetimeUnavailable {
		t.Fatalf("PrepareFreshLifetime() = %p, %+v", prepared, result)
	}
	runtimeManager := restarted.runtimeManager.(*fakeScopedRuntimeManager)
	if runtimeManager.snapshotCalls != 0 {
		t.Fatalf("snapshot selections = %d, want 0", runtimeManager.snapshotCalls)
	}
	if _, found, err := f.provider.RuntimeOperations().Get(t.Context(), request.OperationID); err != nil || found {
		t.Fatalf("runtime operation found=%t err=%v", found, err)
	}
	if _, err := f.provider.ChatArchive().Pending(t.Context(), record.ChannelType, record.AddressKey); !errors.Is(err, chatarchivecmd.ErrNotFound) {
		t.Fatalf("pending archive error = %v, want not found", err)
	}
}

func TestFreshLifetimeColdRejectsInvalidWorkspaceBeforeSnapshotOrJournal(t *testing.T) {
	tests := []struct {
		name             string
		workspaceEnabled bool
		mutate           func(*testing.T, *baldastate.SessionRecord)
	}{
		{
			name: "missing directory",
			mutate: func(t *testing.T, record *baldastate.SessionRecord) {
				record.WorkspaceDir = t.TempDir() + "/missing"
			},
		},
		{
			name: "not a directory",
			mutate: func(t *testing.T, record *baldastate.SessionRecord) {
				path := t.TempDir() + "/workspace-file"
				if err := os.WriteFile(path, []byte("not a directory"), 0o600); err != nil {
					t.Fatal(err)
				}
				record.WorkspaceDir = path
			},
		},
		{
			name:             "workspace branch missing",
			workspaceEnabled: true,
			mutate: func(_ *testing.T, record *baldastate.SessionRecord) {
				record.BranchName = ""
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := newArchiveFixture(t)
			f.runtime.ProviderBindingJSON = publicationBinding
			topic, err := f.manager().EnsureSession(t.Context(), archiveInput(), "saved")
			if err != nil {
				t.Fatal(err)
			}
			record, found, err := f.provider.Sessions().GetBySessionID(t.Context(), topic.GetSessionID())
			if err != nil || !found {
				t.Fatalf("record = %+v, %t, %v", record, found, err)
			}
			test.mutate(t, &record)
			if err := f.provider.Sessions().DeleteBySessionID(t.Context(), record.SessionID); err != nil {
				t.Fatal(err)
			}
			if err := f.provider.Sessions().Upsert(t.Context(), record); err != nil {
				t.Fatal(err)
			}

			restarted := f.manager()
			restarted.workspaceEnabled = test.workspaceEnabled
			restarted.runtimeOperations = f.provider.RuntimeOperations()
			request := FreshLifetimeRequest{
				OperationID: "invalid-workspace-reset", SessionID: record.SessionID,
				InputHash: strings.Repeat("9", 64),
			}
			prepared, result, _ := restarted.PrepareFreshLifetime(t.Context(), request)
			if prepared != nil || result.Status != FreshLifetimeUnavailable {
				t.Fatalf("PrepareFreshLifetime() = %p, %+v", prepared, result)
			}
			runtimeManager := restarted.runtimeManager.(*fakeScopedRuntimeManager)
			if runtimeManager.snapshotCalls != 0 {
				t.Fatalf("snapshot selections = %d, want 0", runtimeManager.snapshotCalls)
			}
			if _, found, err := f.provider.RuntimeOperations().Get(t.Context(), request.OperationID); err != nil || found {
				t.Fatalf("runtime operation found=%t err=%v", found, err)
			}
			if _, err := f.provider.ChatArchive().Pending(t.Context(), record.ChannelType, record.AddressKey); !errors.Is(err, chatarchivecmd.ErrNotFound) {
				t.Fatalf("pending archive error = %v, want not found", err)
			}
		})
	}
}

func TestFreshLifetimeReplacesColdPersistedSession(t *testing.T) {
	f := newArchiveFixture(t)
	const (
		retainedProviderID = "retained-provider"
		retainedBinding    = `{"RootProviderID":"retained-provider","Entries":[{"ProviderID":"retained-provider","Source":"managed","RevisionID":"revision"}],"Reproducible":true,"Warnings":null}`
	)
	f.runtime.ProviderID = retainedProviderID
	f.runtime.ProviderBindingJSON = retainedBinding
	f.builder.metadata = AgentMetadata{
		Type: "codex", Model: "configured-model", ReasoningEffort: "high", MCPServers: []string{"know", "grafana"},
	}
	control := &freshLifetimeControlSpy{}
	f.runtime.SessionControl = control
	initial := f.manager()
	topic, err := initial.EnsureSession(t.Context(), archiveInput(), "saved")
	if err != nil {
		t.Fatal(err)
	}
	oldArchive := topic.GetArchiveID()
	record, _, err := f.provider.Sessions().GetBySessionID(t.Context(), topic.GetSessionID())
	if err != nil {
		t.Fatal(err)
	}
	requested := "saved-model"
	record.RequestedModel, record.EffectiveModel = &requested, requested
	record.AgentName = ""
	record.WorkspaceDir, record.BranchName = f.dir, "saved-branch"
	// Seed a saved preference before restart; Upsert preserves runtime-owned
	// fields on an existing row.
	if err := f.provider.Sessions().DeleteBySessionID(t.Context(), record.SessionID); err != nil {
		t.Fatal(err)
	}
	if err := f.provider.Sessions().Upsert(t.Context(), record); err != nil {
		t.Fatal(err)
	}
	restarted := f.manager()
	workspaces := &mutationRecordingWorkspace{}
	restarted.workspaceEnabled, restarted.workspaces = true, workspaces
	hooks := &coldResetHooks{}
	restarted.hooks = hooks
	restarted.runtimeOperations = f.provider.RuntimeOperations()
	runtimeManager := restarted.runtimeManager.(*fakeScopedRuntimeManager)
	runtimeManager.currentSnapshot = freshLifetimeCurrentCatalog
	runtimeManager.runtimeErr = errors.New("predecessor unavailable")
	runtimeManager.runtimeForSession = func(request SessionRuntimeRequest) (*BuiltRuntime, error) {
		runtime := *f.runtime
		runtime.RuntimeSnapshotID = request.RuntimeSnapshotID
		if request.ModelOverride != nil {
			runtime.RecipeModel = *request.ModelOverride
		}
		return &runtime, nil
	}
	request := FreshLifetimeRequest{OperationID: "cold-reset", SessionID: topic.GetSessionID(), InputHash: strings.Repeat("a", 64)}
	prepared, result, err := restarted.PrepareFreshLifetime(t.Context(), request)
	if err != nil || prepared == nil {
		t.Fatalf("cold prepare = %+v, %v; want prepared lifetime", result, err)
	}
	result, err = restarted.PublishFreshLifetime(t.Context(), prepared)
	if err != nil || result.Status != FreshLifetimeReset {
		t.Fatalf("cold publish = %+v, %v", result, err)
	}
	if active := restarted.activeTopicSession(request.SessionID); active != nil {
		t.Fatal("cold reset eagerly published a provider runtime")
	}
	if workspaces.mutations != 0 || runtimeManager.runtimeCalls != 0 || len(runtimeManager.sessionRequests) != 0 {
		t.Fatalf("cold reset restored predecessor: workspace=%d root=%d bindings=%d", workspaces.mutations, runtimeManager.runtimeCalls, len(runtimeManager.sessionRequests))
	}
	saved, _, err := f.provider.Sessions().GetBySessionID(t.Context(), request.SessionID)
	if err != nil || saved.ArchiveID == oldArchive || saved.RuntimeSnapshotID != freshLifetimeCurrentCatalog || saved.ProviderBindingJSON != retainedBinding ||
		saved.WorkspaceDir != record.WorkspaceDir || saved.BranchName != record.BranchName || !sameOptionalModel(saved.RequestedModel, record.RequestedModel) || saved.EffectiveModel != requested {
		t.Fatalf("cold reset metadata = %+v, %v", saved, err)
	}
	created, err := f.runtime.SessionSvc.Get(t.Context(), &adksession.GetRequest{AppName: baldaRuntimeAppName, UserID: saved.UserID, SessionID: saved.ArchiveID})
	if err != nil || created.Session == nil || created.Session.Events().Len() != 0 {
		t.Fatalf("cold reset local lifetime = %#v, %v", created, err)
	}
	if calls := control.calls.Load(); calls != 0 {
		t.Fatalf("cold reset called provider control %d times", calls)
	}
	if result.Activation == nil || result.Activation.Label != defaultSessionLabel || !reflect.DeepEqual(result.Activation.Runtime, sessioncmd.RuntimeSummary{}) {
		t.Fatalf("cold reset activation = %#v, want empty runtime summary", result.Activation)
	}
	wantConfiguration := sessioncmd.ConfigurationSummary{
		ProviderID: retainedProviderID, ProviderType: "codex", ConfiguredModel: "configured-model",
		RequestedModel: &requested, ReasoningEffort: "high", MCPServers: []string{"grafana", "know"},
	}
	if !reflect.DeepEqual(result.Activation.Configuration, wantConfiguration) {
		t.Fatalf("cold reset configuration = %#v, want %#v", result.Activation.Configuration, wantConfiguration)
	}
	if len(hooks.events) != 1 || hooks.events[0].Name != hookcmd.SessionStart || hooks.events[0].Source != "startup" || hooks.events[0].AgentSessionID != saved.ArchiveID {
		t.Fatalf("cold reset hooks = %+v, want successor startup only", hooks.events)
	}
	f.builder.metadata = AgentMetadata{Type: "changed", Model: "changed-model"}
	coldReplay := f.manager()
	coldReplay.runtimeOperations = f.provider.RuntimeOperations()
	_, replayResult, err := coldReplay.PrepareFreshLifetime(t.Context(), request)
	coldRuntime := coldReplay.runtimeManager.(*fakeScopedRuntimeManager)
	if err != nil || !reflect.DeepEqual(replayResult, result) || coldRuntime.snapshotCalls != 0 || len(coldRuntime.unpublishedRequests) != 0 {
		t.Fatalf("cold terminal replay = %+v, selections=%d builds=%d", replayResult, coldRuntime.snapshotCalls, len(coldRuntime.unpublishedRequests))
	}
	brokenReplay := f.manager()
	brokenReplay.runtimeOperations = f.provider.RuntimeOperations()
	brokenReplay.archive = missingTargetArchive{ChatArchiveStore: f.provider.ChatArchive(), target: saved.ArchiveID}
	if _, brokenResult, brokenErr := brokenReplay.PrepareFreshLifetime(t.Context(), request); brokenErr != nil || brokenResult.Status != FreshLifetimeRecoveryRequired || brokenResult.Activation != nil {
		t.Fatalf("missing archive replay = %+v, %v; want recovery required", brokenResult, brokenErr)
	}
	restarted.hooks = nil
	restarted.workspaceEnabled = false
	if err := f.runtime.SessionSvc.Delete(t.Context(), &adksession.DeleteRequest{
		AppName: baldaRuntimeAppName, UserID: saved.UserID, SessionID: saved.ArchiveID,
	}); err != nil {
		t.Fatal(err)
	}
	if next, err := restarted.EnsureSession(t.Context(), archiveInput(), "saved"); err == nil || next != nil {
		t.Fatalf("missing successor history = %v, %v; want bounded restore failure", next, err)
	}
	if _, err := f.provider.ChatArchive().Pending(t.Context(), saved.ChannelType, saved.AddressKey); !errors.Is(err, chatarchivecmd.ErrNotFound) {
		t.Fatalf("failed exact restore pending archive = %v, want not found", err)
	}
	if _, err := f.builder.CreateFreshLocalSession(t.Context(), defaultSessionLabel, saved.UserID, saved.ArchiveID, saved.WorkspaceDir,
		RuntimeSessionContext{ArchiveID: saved.ArchiveID, BaldaSessionID: saved.SessionID, SessionBranch: saved.BranchName}); err != nil {
		t.Fatal(err)
	}
	resumeHooks := &modelStartHooks{}
	restarted.hooks = resumeHooks
	next, err := restarted.EnsureSession(t.Context(), archiveInput(), "saved")
	if err != nil || next == nil || next.GetArchiveID() != saved.ArchiveID {
		t.Fatalf("next ordinary turn preparation = %v, %v; want lazy exact successor", next, err)
	}
	_, nextValue, recoveryRequired := next.runtimeForRecovery()
	if recoveryRequired || nextValue.providerID != retainedProviderID || len(resumeHooks.events) != 1 || resumeHooks.events[0].Provider != retainedProviderID {
		t.Fatalf("retained provider restore = runtime %q, hooks %+v", nextValue.providerID, resumeHooks.events)
	}
	warmPrepared, _, err := restarted.PrepareFreshLifetime(t.Context(), FreshLifetimeRequest{
		OperationID: "retained-provider-warm-reset", SessionID: saved.SessionID, InputHash: strings.Repeat("8", 64),
	})
	if err != nil || warmPrepared == nil {
		t.Fatalf("retained provider warm prepare = %v, %v", warmPrepared, err)
	}
	if warmResult, err := restarted.PublishFreshLifetime(t.Context(), warmPrepared); err != nil || warmResult.Status != FreshLifetimeReset {
		t.Fatalf("retained provider warm publish = %+v, %v", warmResult, err)
	}
}

func TestRecoverApplyingColdFreshLifetimeRejectsMismatchedReceiptBeforeMutation(t *testing.T) {
	requested := "requested"
	other := "other"
	raw, err := encodeConfigurationSummary(sessioncmd.ConfigurationSummary{RequestedModel: &requested})
	if err != nil {
		t.Fatal(err)
	}
	manager := &Manager{}
	err = manager.recoverApplyingColdFreshLifetime(t.Context(), baldastate.RuntimeOperationRecord{
		TargetConfigurationSummaryJSON: raw,
		TargetRequestedModel:           &other,
	})
	if !errors.Is(err, ErrRuntimeRecoveryRequired) {
		t.Fatalf("recoverApplyingColdFreshLifetime() = %v", err)
	}
}

type coldResetHooks struct {
	events     []hookcmd.Event
	startupErr error
	onStartup  func()
}

func (h *coldResetHooks) Run(ctx context.Context, event hookcmd.Event) (hookcmd.Result, error) {
	h.events = append(h.events, event)
	if event.Source == "resume" {
		return hookcmd.Result{}, hookcmd.ErrDenied
	}
	if h.onStartup != nil {
		h.onStartup()
	}
	if h.startupErr != nil {
		return hookcmd.Result{}, h.startupErr
	}
	return hookcmd.Result{}, ctx.Err()
}

func TestFreshLifetimeColdStartupFailurePreservesPredecessor(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(map[bool]string{false: "denied", true: "canceled"}[canceled], func(t *testing.T) {
			f := newArchiveFixture(t)
			f.runtime.ProviderBindingJSON = publicationBinding
			topic, err := f.manager().EnsureSession(t.Context(), archiveInput(), "saved")
			if err != nil {
				t.Fatal(err)
			}
			manager := f.manager()
			manager.runtimeOperations = f.provider.RuntimeOperations()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			hooks := &coldResetHooks{startupErr: hookcmd.ErrDenied}
			if canceled {
				hooks.startupErr, hooks.onStartup = nil, cancel
			}
			manager.hooks = hooks
			request := FreshLifetimeRequest{OperationID: "cold-start-failure", SessionID: topic.GetSessionID(), InputHash: strings.Repeat("f", 64)}
			prepared, result, err := manager.PrepareFreshLifetime(ctx, request)
			if prepared != nil || result.Status != FreshLifetimeUnavailable || err == nil {
				t.Fatalf("startup failure = %v, %+v, %v", prepared, result, err)
			}
			record, _, err := f.provider.Sessions().GetBySessionID(t.Context(), request.SessionID)
			if err != nil || record.ArchiveID != topic.GetArchiveID() || manager.activeTopicSession(request.SessionID) != nil {
				t.Fatalf("startup failure changed predecessor: %+v, %v", record, err)
			}
			operation, _, err := f.provider.RuntimeOperations().Get(t.Context(), request.OperationID)
			if err != nil || operation.State != baldastate.RuntimeOperationTerminal || operation.Outcome != string(baldastate.RuntimeOperationOutcomeAborted) || len(hooks.events) != 1 {
				t.Fatalf("startup failure journal = %+v, hooks=%+v, %v", operation, hooks.events, err)
			}
			_, err = f.runtime.SessionSvc.Get(t.Context(), &adksession.GetRequest{AppName: baldaRuntimeAppName, UserID: record.UserID, SessionID: operation.TargetArchiveID})
			if !errors.Is(err, adksession.ErrNotFound) {
				t.Fatalf("failed successor exists: %v", err)
			}
		})
	}
}

func TestFreshLifetimeColdPublicationRejectsActivatedPredecessorAndShutdown(t *testing.T) {
	for _, stopping := range []bool{false, true} {
		t.Run(map[bool]string{false: "activated", true: "shutdown"}[stopping], func(t *testing.T) {
			f := newArchiveFixture(t)
			f.runtime.ProviderBindingJSON = publicationBinding
			topic, err := f.manager().EnsureSession(t.Context(), archiveInput(), "saved")
			if err != nil {
				t.Fatal(err)
			}
			manager := f.manager()
			manager.runtimeOperations = f.provider.RuntimeOperations()
			request := FreshLifetimeRequest{OperationID: "cold-stale-publication", SessionID: topic.GetSessionID(), InputHash: strings.Repeat("a", 64)}
			prepared, _, err := manager.PrepareFreshLifetime(t.Context(), request)
			if err != nil || prepared == nil {
				t.Fatalf("cold prepare = %v, %v", prepared, err)
			}
			if stopping {
				if err := manager.Stop(t.Context()); err != nil {
					t.Fatal(err)
				}
			} else {
				// A different local lifetime won publication after preparation.
				manager.sessions[request.SessionID] = topic
			}
			result, err := manager.PublishFreshLifetime(t.Context(), prepared)
			if err == nil || result.Status == FreshLifetimeReset {
				t.Fatalf("stale cold publication = %+v, %v", result, err)
			}
			record, _, err := f.provider.Sessions().GetBySessionID(t.Context(), request.SessionID)
			if err != nil || record.ArchiveID != topic.GetArchiveID() {
				t.Fatalf("stale publication changed durable binding: %+v, %v", record, err)
			}
		})
	}
}

func TestFreshLifetimeColdPreparedAbortEndsExactHook(t *testing.T) {
	f := newArchiveFixture(t)
	f.runtime.ProviderBindingJSON = publicationBinding
	topic, err := f.manager().EnsureSession(t.Context(), archiveInput(), "saved")
	if err != nil {
		t.Fatal(err)
	}
	manager := f.manager()
	manager.runtimeOperations = f.provider.RuntimeOperations()
	hooks := &coldResetHooks{}
	manager.hooks = hooks
	prepared, _, err := manager.PrepareFreshLifetime(t.Context(), FreshLifetimeRequest{
		OperationID: "cold-prepared-abort", SessionID: topic.GetSessionID(), InputHash: strings.Repeat("7", 64),
	})
	if err != nil || prepared == nil {
		t.Fatalf("PrepareFreshLifetime() = %v, %v", prepared, err)
	}
	if _, err := manager.AbortFreshLifetime(t.Context(), prepared); err != nil {
		t.Fatal(err)
	}
	if len(hooks.events) != 2 || hooks.events[1].Name != hookcmd.SessionEnd ||
		hooks.events[1].AgentSessionID != prepared.operation.TargetArchiveID || hooks.events[1].Provider != recoveryProviderID {
		t.Fatalf("cold rollback hooks = %+v", hooks.events)
	}
}

type freshLifetimeControlSpy struct {
	calls atomic.Int32
}

func (s *freshLifetimeControlSpy) Inspect(context.Context, adksession.Session) agentproduct.SessionControlSnapshot {
	s.calls.Add(1)
	return agentproduct.SessionControlSnapshot{}
}

func (s *freshLifetimeControlSpy) ValidateModel(context.Context, adksession.Session, string) agentproduct.ModelValidation {
	s.calls.Add(1)
	return agentproduct.ModelValidation{}
}

func (s *freshLifetimeControlSpy) ChangeModel(context.Context, adksession.Session, string) agentproduct.ModelChange {
	s.calls.Add(1)
	return agentproduct.ModelChange{}
}

func (s *freshLifetimeControlSpy) FastForward(context.Context, adksession.Session, agentproduct.ContextPackage) agentproduct.ContextChange {
	s.calls.Add(1)
	return agentproduct.ContextChange{}
}

func TestFreshLifetimePublishesIndependentPreferenceAwareCandidate(t *testing.T) {
	f := newArchiveFixture(t)
	var closeCalls atomic.Int32
	control := &freshLifetimeControlSpy{}
	f.runtime.ProviderBindingJSON = publicationBinding
	f.runtime.PublicRuntimeMetadata = sessioncmd.PublicRuntimeMetadata{
		ProviderID: freshLifetimeProviderID, ProviderType: "codex_acp", MCPServers: []string{"balda"},
	}
	f.runtime.SessionControl = control
	f.builder.model = publicationOriginalModel
	f.runtime.Close = func() error {
		closeCalls.Add(1)
		return nil
	}
	manager := f.manager()
	manager.runtimeOperations = f.provider.RuntimeOperations()
	runtimeManager := manager.runtimeManager.(*fakeScopedRuntimeManager)

	topic, err := manager.EnsureSession(t.Context(), archiveInput(), "saved")
	if err != nil {
		t.Fatal(err)
	}
	incumbent, err := topic.AcquireRuntime()
	if err != nil {
		t.Fatal(err)
	}
	event := adksession.NewEvent(t.Context(), "old-context")
	event.Author = ensureTestAuthor
	if err := incumbent.SessionService().AppendEvent(t.Context(), incumbent.Session(), event); err != nil {
		t.Fatal(err)
	}
	oldRuntimeID := incumbent.SessionID()
	oldArchiveID := incumbent.ArchiveID()
	oldGeneration := incumbent.Generation()
	oldSnapshot := incumbent.RuntimeSnapshotID()
	oldBinding := incumbent.ProviderBindingJSON()
	oldModel := incumbent.EffectiveModel()
	providerBuilds := len(runtimeManager.sessionRequests)
	initialFreshCreates := f.builder.freshCalls

	request := FreshLifetimeRequest{
		OperationID: "fresh-lifetime-operation",
		SessionID:   topic.GetSessionID(),
		InputHash:   strings.Repeat("a", 64),
	}
	prepared, result, err := manager.PrepareFreshLifetime(t.Context(), request)
	if err != nil || prepared == nil || result.Status != "" {
		t.Fatalf("PrepareFreshLifetime() = %#v, %#v, %v", prepared, result, err)
	}
	targetID := prepared.operation.TargetArchiveID
	if targetID == "" || targetID == oldRuntimeID || targetID != prepared.candidate.value.sessionID {
		t.Fatalf("target runtime identity = %q, old %q, candidate %q", targetID, oldRuntimeID, prepared.candidate.value.sessionID)
	}
	if prepared.candidate.value.archiveID != targetID || prepared.candidate.value.session.Events().Len() != 0 {
		t.Fatalf("candidate archive/events = %q/%d", prepared.candidate.value.archiveID, prepared.candidate.value.session.Events().Len())
	}
	if len(runtimeManager.sessionRequests) != providerBuilds {
		t.Fatalf("provider builds = %d, want unchanged %d", len(runtimeManager.sessionRequests), providerBuilds)
	}
	if len(runtimeManager.unpublishedRequests) != 1 {
		t.Fatalf("unpublished runtime builds = %d, want 1", len(runtimeManager.unpublishedRequests))
	}
	buildRequest := runtimeManager.unpublishedRequests[0]
	if buildRequest.RuntimeSnapshotID != oldSnapshot || buildRequest.ProviderBindingJSON != oldBinding || buildRequest.ModelOverride != nil {
		t.Fatalf("unpublished build request = %+v", buildRequest)
	}
	if prepared.operation.Kind != string(baldastate.RuntimeOperationKindLifetimePreference) || prepared.operation.TargetRequestedModel != nil {
		t.Fatalf("preference-aware operation = %+v", prepared.operation)
	}
	if f.builder.freshCalls != initialFreshCreates+1 {
		t.Fatalf("local session creates = %d, want %d", f.builder.freshCalls, initialFreshCreates+1)
	}
	if got := f.builder.freshProviders[len(f.builder.freshProviders)-1]; got != freshLifetimeProviderID || got == topic.agentName {
		t.Fatalf("fresh local initializer provider = %q, display label = %q", got, topic.agentName)
	}

	result, err = manager.PublishFreshLifetime(t.Context(), prepared)
	if err != nil || result.Status != FreshLifetimeReset || result.Generation != oldGeneration+1 {
		t.Fatalf("PublishFreshLifetime() = %#v, %v", result, err)
	}
	if result.Activation == nil || result.Activation.Kind != sessioncmd.ActivationCreated ||
		result.Activation.SessionID != topic.GetSessionID() || result.Activation.Label != "saved" ||
		result.Activation.Runtime.ProviderID != freshLifetimeProviderID || result.Activation.Runtime.EffectiveModel != oldModel {
		t.Fatalf("fresh lifetime activation = %#v", result.Activation)
	}
	current, err := topic.AcquireRuntime()
	if err != nil {
		t.Fatal(err)
	}
	if topic.GetSessionID() != request.SessionID || topic.GetUserID() != archiveInput().UserID || topic.GetAgentSessionID() != targetID {
		t.Fatalf("stable/runtime identity = %q/%q/%q", topic.GetSessionID(), topic.GetUserID(), topic.GetAgentSessionID())
	}
	if current.SessionID() != targetID || current.ArchiveID() != targetID || current.Generation() != oldGeneration+1 ||
		current.RuntimeSnapshotID() != oldSnapshot || current.ProviderBindingJSON() != oldBinding || current.EffectiveModel() != oldModel {
		t.Fatalf("published runtime = session %q archive %q generation %d snapshot %q binding %q model %q",
			current.SessionID(), current.ArchiveID(), current.Generation(), current.RuntimeSnapshotID(), current.ProviderBindingJSON(), current.EffectiveModel())
	}
	if current.SessionService() != incumbent.SessionService() || current.Session().Events().Len() != 0 {
		t.Fatal("fresh lifetime did not preserve the local service with an empty isolated session")
	}
	if got := archiveStateString(current.Session(), chatarchivecmd.EpochStateKey); got != targetID {
		t.Fatalf("target epoch = %q, want %q", got, targetID)
	}
	if got := archiveStateString(current.Session(), baldaSessionIDStateKey); got != request.SessionID {
		t.Fatalf("stable session state = %q, want %q", got, request.SessionID)
	}
	oldArchive, err := f.provider.ChatArchive().Get(t.Context(), oldArchiveID)
	if err != nil || oldArchive.Status != chatarchivecmd.StatusEnded {
		t.Fatalf("old archive = %#v, %v", oldArchive, err)
	}
	newArchive, err := f.provider.ChatArchive().Get(t.Context(), targetID)
	if err != nil || newArchive.Status != chatarchivecmd.StatusActive || newArchive.RuntimeID != targetID || newArchive.PredecessorID != oldArchiveID {
		t.Fatalf("new archive = %#v, %v", newArchive, err)
	}
	record, found, err := f.provider.Sessions().GetBySessionID(t.Context(), request.SessionID)
	if err != nil || !found || record.ArchiveID != targetID || record.RuntimeGeneration != oldGeneration+1 ||
		record.RuntimeSnapshotID != oldSnapshot || record.ProviderBindingJSON != oldBinding || record.EffectiveModel != oldModel {
		t.Fatalf("durable metadata = %#v, %t, %v", record, found, err)
	}

	replayed, replayResult, err := manager.PrepareFreshLifetime(t.Context(), request)
	if err != nil || replayed != nil || !reflect.DeepEqual(replayResult, result) {
		t.Fatalf("terminal replay = %#v, %#v, %v", replayed, replayResult, err)
	}
	collision := request
	collision.InputHash = strings.Repeat("b", 64)
	if plan, collisionResult, collisionErr := manager.PrepareFreshLifetime(t.Context(), collision); collisionErr != nil || plan != nil || collisionResult.Status != FreshLifetimeConflict {
		t.Fatalf("collision = %#v, %#v, %v", plan, collisionResult, collisionErr)
	}
	if len(runtimeManager.sessionRequests) != providerBuilds || f.builder.freshCalls != initialFreshCreates+1 {
		t.Fatal("replay or collision performed runtime construction")
	}
	if control.calls.Load() != 0 {
		t.Fatalf("SessionControl calls = %d, want 0", control.calls.Load())
	}

	incumbent.Release()
	current.Release()
	if err := topic.DrainRuntime(context.Background()); err != nil {
		t.Fatal(err)
	}
	if closeCalls.Load() != 1 {
		t.Fatalf("incumbent aggregate close calls = %d, want 1", closeCalls.Load())
	}
}

func TestFreshLifetimePreparedAbortDeletesOnlyTarget(t *testing.T) {
	f := newArchiveFixture(t)
	f.runtime.ProviderBindingJSON = publicationBinding
	manager := f.manager()
	manager.runtimeOperations = f.provider.RuntimeOperations()
	topic, err := manager.EnsureSession(t.Context(), archiveInput(), "saved")
	if err != nil {
		t.Fatal(err)
	}
	request := FreshLifetimeRequest{OperationID: "fresh-abort", SessionID: topic.GetSessionID(), InputHash: strings.Repeat("c", 64)}
	prepared, prepareResult, err := manager.PrepareFreshLifetime(t.Context(), request)
	if err != nil || prepared == nil {
		t.Fatalf("PrepareFreshLifetime() = %#v, %#v, %v", prepared, prepareResult, err)
	}
	targetID := prepared.operation.TargetArchiveID
	result, err := manager.AbortFreshLifetime(t.Context(), prepared)
	if err != nil || result.Status != FreshLifetimeUnavailable {
		t.Fatalf("AbortFreshLifetime() = %#v, %v", result, err)
	}
	if _, err := f.runtime.SessionSvc.Get(t.Context(), &adksession.GetRequest{AppName: f.runtime.AppName, UserID: archiveInput().UserID, SessionID: targetID}); !errors.Is(err, adksession.ErrNotFound) {
		t.Fatalf("target Get() error = %v, want not found", err)
	}
	lease, err := topic.AcquireRuntime()
	if err != nil {
		t.Fatalf("incumbent admission after prepared abort = %v", err)
	}
	lease.Release()
	operation, found, err := f.provider.RuntimeOperations().Get(t.Context(), request.OperationID)
	if err != nil || !found || operation.State != baldastate.RuntimeOperationTerminal || operation.Outcome != string(baldastate.RuntimeOperationOutcomeAborted) {
		t.Fatalf("aborted operation = %#v, %t, %v", operation, found, err)
	}
}

func TestFreshLifetimeLegacyPreparedReplayKeepsDeliveredSharedPath(t *testing.T) {
	f := newArchiveFixture(t)
	f.runtime.ProviderBindingJSON = publicationBinding
	manager := f.manager()
	manager.runtimeOperations = f.provider.RuntimeOperations()
	topic, err := manager.EnsureSession(t.Context(), archiveInput(), "saved")
	if err != nil {
		t.Fatal(err)
	}
	value, ok := topic.runtimeValue()
	if !ok {
		t.Fatal("missing incumbent runtime")
	}
	request := FreshLifetimeRequest{OperationID: "legacy-prepared-reset", SessionID: topic.GetSessionID(), InputHash: strings.Repeat("f", 64)}
	legacy := freshLifetimeIntent(request, value, nil, value.effectiveModel, manager.currentTime())
	legacy.Kind = string(baldastate.RuntimeOperationKindLifetime)
	legacy.TargetRequestedModel = nil
	if _, err := manager.runtimeOperations.Begin(t.Context(), legacy); err != nil {
		t.Fatal(err)
	}
	prepared, result, err := manager.PrepareFreshLifetime(t.Context(), request)
	if err != nil || prepared == nil || result.Status != "" || prepared.runtimeCandidate != nil {
		t.Fatalf("legacy PrepareFreshLifetime() = %#v, %#v, %v", prepared, result, err)
	}
	if got := len(manager.runtimeManager.(*fakeScopedRuntimeManager).unpublishedRequests); got != 0 {
		t.Fatalf("legacy replay built unpublished runtime %d times", got)
	}
	result, err = manager.PublishFreshLifetime(t.Context(), prepared)
	if err != nil || result.Status != FreshLifetimeReset {
		t.Fatalf("legacy publish = %#v, %v", result, err)
	}
	if err := topic.DrainRuntime(t.Context()); err != nil {
		t.Fatal(err)
	}
}

type freshLifetimeFinalizeObserver struct {
	baldastate.RuntimeOperationStore
	before func()
}

func (s freshLifetimeFinalizeObserver) Finalize(
	ctx context.Context,
	finalization baldastate.RuntimeOperationFinalization,
) (baldastate.RuntimeOperationRecord, error) {
	if s.before != nil {
		s.before()
	}
	return s.RuntimeOperationStore.Finalize(ctx, finalization)
}

func TestFreshLifetimeLegacyFinalizesBeforeSlotSwap(t *testing.T) {
	f := newArchiveFixture(t)
	f.runtime.ProviderBindingJSON = publicationBinding
	manager := f.manager()
	manager.runtimeOperations = f.provider.RuntimeOperations()
	topic, err := manager.EnsureSession(t.Context(), archiveInput(), "saved")
	if err != nil {
		t.Fatal(err)
	}
	value, ok := topic.runtimeValue()
	if !ok {
		t.Fatal("missing incumbent runtime")
	}
	request := FreshLifetimeRequest{
		OperationID: "legacy-publication-order",
		SessionID:   topic.GetSessionID(),
		InputHash:   strings.Repeat("9", 64),
	}
	legacy := freshLifetimeIntent(request, value, nil, value.effectiveModel, manager.currentTime())
	legacy.Kind = string(baldastate.RuntimeOperationKindLifetime)
	if _, err := manager.runtimeOperations.Begin(t.Context(), legacy); err != nil {
		t.Fatal(err)
	}
	prepared, _, err := manager.PrepareFreshLifetime(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	manager.runtimeOperations = freshLifetimeFinalizeObserver{
		RuntimeOperationStore: f.provider.RuntimeOperations(),
		before: func() {
			current, present := topic.runtimeValue()
			if !present || current.archiveID != value.archiveID || current.generation != value.generation {
				t.Fatalf("legacy slot changed before finalize: %+v", current)
			}
		},
	}
	result, err := manager.PublishFreshLifetime(t.Context(), prepared)
	if err != nil || result.Status != FreshLifetimeReset {
		t.Fatalf("PublishFreshLifetime() = %+v, %v", result, err)
	}
}

func TestFreshLifetimePreferenceAdoptsPromotedCandidateAfterFinalizeUncertainty(t *testing.T) {
	for _, test := range []struct {
		name   string
		commit bool
	}{
		{name: "error before commit"},
		{name: "error after commit", commit: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newArchiveFixture(t)
			f.runtime.ProviderBindingJSON = publicationBinding
			manager := f.manager()
			operations := f.provider.RuntimeOperations()
			manager.runtimeOperations = operations
			topic, err := manager.EnsureSession(t.Context(), archiveInput(), "saved")
			if err != nil {
				t.Fatal(err)
			}
			request := FreshLifetimeRequest{
				OperationID: "preference-finalize-" + strings.ReplaceAll(test.name, " ", "-"),
				SessionID:   topic.GetSessionID(),
				InputHash:   strings.Repeat("8", 64),
			}
			prepared, _, err := manager.PrepareFreshLifetime(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			runtimeManager := manager.runtimeManager.(*fakeScopedRuntimeManager)
			runtimeManager.adoptedCandidate = prepared.runtimeCandidate
			candidate := prepared.runtimeCandidate.(*fakeUnpublishedRuntimeCandidate)
			manager.runtimeOperations = uncertainFinalizeStore{RuntimeOperationStore: operations, commit: test.commit}
			result, err := manager.PublishFreshLifetime(t.Context(), prepared)
			if test.commit {
				if err != nil || result.Status != FreshLifetimeReset || candidate.completeCalls != 1 {
					t.Fatalf("committed finalize result = %+v, %v; complete=%d", result, err, candidate.completeCalls)
				}
				return
			}
			if !errors.Is(err, ErrRuntimeRecoveryRequired) || candidate.markCalls != 1 || candidate.completeCalls != 0 {
				t.Fatalf("uncertain finalize = %+v, %v; mark=%d complete=%d", result, err, candidate.markCalls, candidate.completeCalls)
			}
			if _, err := topic.AcquireRuntime(); !errors.Is(err, ErrRuntimeRecoveryRequired) {
				t.Fatalf("AcquireRuntime() error = %v, want recovery required", err)
			}
			operation, found, err := operations.Get(t.Context(), request.OperationID)
			if err != nil || !found || operation.State != baldastate.RuntimeOperationApplying {
				t.Fatalf("applying operation = %+v, %t, %v", operation, found, err)
			}
			manager.runtimeOperations = operations
			if err := manager.recoverApplyingFreshLifetimeOperation(t.Context(), operation); err != nil {
				t.Fatal(err)
			}
			if candidate.markCalls != 2 || candidate.completeCalls != 1 || len(runtimeManager.unpublishedRequests) != 1 {
				t.Fatalf("adopted candidate calls mark=%d complete=%d builds=%d", candidate.markCalls, candidate.completeCalls, len(runtimeManager.unpublishedRequests))
			}
			lease, err := topic.AcquireRuntime()
			if err != nil {
				t.Fatal(err)
			}
			lease.Release()
		})
	}
}

func TestFreshLifetimeApplyingRecoveryPublishesPinnedTarget(t *testing.T) {
	f := newArchiveFixture(t)
	f.runtime.ProviderBindingJSON = publicationBinding
	manager := f.manager()
	manager.runtimeOperations = f.provider.RuntimeOperations()
	topic, err := manager.EnsureSession(t.Context(), archiveInput(), "saved")
	if err != nil {
		t.Fatal(err)
	}
	runtimeManagerBefore := manager.runtimeManager.(*fakeScopedRuntimeManager)
	runtimeManagerBefore.currentSnapshot = freshLifetimeRecoveryCatalog
	runtimeManagerBefore.runtimeForSession = func(request SessionRuntimeRequest) (*BuiltRuntime, error) {
		runtime := *f.runtime
		runtime.RuntimeSnapshotID = request.RuntimeSnapshotID
		return &runtime, nil
	}
	request := FreshLifetimeRequest{OperationID: "fresh-applying", SessionID: topic.GetSessionID(), InputHash: strings.Repeat("d", 64)}
	prepared, _, err := manager.PrepareFreshLifetime(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	runtimeManagerBefore.currentSnapshot = freshLifetimeLaterCatalog
	if err := f.provider.RuntimeOperations().MarkApplying(t.Context(), request.OperationID, request.InputHash); err != nil {
		t.Fatal(err)
	}
	live := manager.sessionRecordFromValue(topic, baldastate.SessionStatusActive, prepared.candidate.value)
	if _, err := manager.archive.Activate(t.Context(), prepared.archiveToken, &live); err != nil {
		t.Fatal(err)
	}
	if err := prepared.runtimeCandidate.Abort(t.Context()); err != nil {
		t.Fatal(err)
	}
	operation, found, err := f.provider.RuntimeOperations().Get(t.Context(), request.OperationID)
	if err != nil || !found {
		t.Fatalf("applying operation = %#v, %t, %v", operation, found, err)
	}
	if err := manager.recoverApplyingFreshLifetimeOperation(t.Context(), operation); err != nil {
		t.Fatal(err)
	}
	lease, err := topic.AcquireRuntime()
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	if lease.RuntimeSnapshotID() != freshLifetimeRecoveryCatalog {
		t.Fatalf("recovery snapshot=%q", lease.RuntimeSnapshotID())
	}

	t.Cleanup(func() {
		_ = topic.DrainRuntime(context.Background())
	})
	if lease.SessionID() != prepared.operation.TargetArchiveID || lease.ArchiveID() != prepared.operation.TargetArchiveID ||
		lease.Generation() != prepared.operation.ExpectedGeneration+1 || lease.Session().Events().Len() != 0 {
		t.Fatalf("recovered runtime = session %q archive %q generation %d events %d",
			lease.SessionID(), lease.ArchiveID(), lease.Generation(), lease.Session().Events().Len())
	}
	terminal, found, err := f.provider.RuntimeOperations().Get(t.Context(), request.OperationID)
	if err != nil || !found || terminal.State != baldastate.RuntimeOperationTerminal ||
		terminal.Outcome != string(baldastate.RuntimeOperationOutcomeContextReset) {
		t.Fatalf("terminal operation = %#v, %t, %v", terminal, found, err)
	}
}

func TestFreshLifetimePreferenceAdoptsEveryPromotedSameProcessState(t *testing.T) {
	for _, stage := range []string{"promoted", "slot-swapped", "slot-marked"} {
		t.Run(stage, func(t *testing.T) {
			f := newArchiveFixture(t)
			f.runtime.ProviderBindingJSON = publicationBinding
			manager := f.manager()
			manager.runtimeOperations = f.provider.RuntimeOperations()
			topic, err := manager.EnsureSession(t.Context(), archiveInput(), "saved")
			if err != nil {
				t.Fatal(err)
			}
			request := FreshLifetimeRequest{
				OperationID: "promoted-state-" + stage,
				SessionID:   topic.GetSessionID(),
				InputHash:   strings.Repeat("7", 64),
			}
			prepared, _, err := manager.PrepareFreshLifetime(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			if err := manager.runtimeOperations.MarkApplying(t.Context(), request.OperationID, request.InputHash); err != nil {
				t.Fatal(err)
			}
			if err := prepared.runtimeCandidate.ClaimForPublication(); err != nil || !prepared.candidate.claimPublication() {
				t.Fatalf("publication claims = %v", err)
			}
			live := manager.sessionRecordFromValue(topic, baldastate.SessionStatusActive, prepared.candidate.value)
			if _, err := manager.archive.Activate(t.Context(), prepared.archiveToken, &live); err != nil {
				t.Fatal(err)
			}
			if err := prepared.runtimeCandidate.Promote(); err != nil {
				t.Fatal(err)
			}
			runtimeManager := manager.runtimeManager.(*fakeScopedRuntimeManager)
			runtimeManager.adoptedCandidate = prepared.runtimeCandidate
			candidate := prepared.runtimeCandidate.(*fakeUnpublishedRuntimeCandidate)
			swapped := stage != "promoted"
			if swapped && !topic.swapRuntime(
				prepared.incumbent,
				prepared.candidate,
				prepared.operation.ExpectedArchiveID,
				prepared.operation.ExpectedGeneration,
			) {
				t.Fatal("failed to install exact target fixture")
			}
			prepared.candidate.releasePublicationClaim(true)
			if stage == "slot-marked" {
				prepared.runtimeCandidate.MarkPublished()
			}
			if swapped {
				prepared.incumbent.beginDrain()
			}
			topic.fenceRuntimeRecovery()
			operation, found, err := manager.runtimeOperations.Get(t.Context(), request.OperationID)
			if err != nil || !found || operation.State != baldastate.RuntimeOperationApplying {
				t.Fatalf("applying operation = %+v, %t, %v", operation, found, err)
			}
			if err := manager.recoverApplyingFreshLifetimeOperation(t.Context(), operation); err != nil {
				t.Fatal(err)
			}
			if candidate.completeCalls != 1 || len(runtimeManager.unpublishedRequests) != 1 {
				t.Fatalf("adoption completion/builds = %d/%d", candidate.completeCalls, len(runtimeManager.unpublishedRequests))
			}
			lease, err := topic.AcquireRuntime()
			if err != nil {
				t.Fatal(err)
			}
			if lease.SessionID() != prepared.operation.TargetArchiveID || lease.Generation() != prepared.operation.ExpectedGeneration+1 {
				t.Fatalf("recovered target = %q/%d", lease.SessionID(), lease.Generation())
			}
			lease.Release()
		})
	}
}

func TestFreshLifetimeColdRecoveryUsesPinnedLocalIdentity(t *testing.T) {
	f := newArchiveFixture(t)
	f.runtime.ProviderBindingJSON = publicationBinding
	manager := f.manager()
	manager.runtimeOperations = f.provider.RuntimeOperations()
	topic, err := manager.EnsureSession(t.Context(), archiveInput(), "saved")
	if err != nil {
		t.Fatal(err)
	}
	oldLease, err := topic.AcquireRuntime()
	if err != nil {
		t.Fatal(err)
	}
	oldEvent := adksession.NewEvent(t.Context(), "old-context")
	oldEvent.Author = ensureTestAuthor
	if err := oldLease.SessionService().AppendEvent(t.Context(), oldLease.Session(), oldEvent); err != nil {
		t.Fatal(err)
	}
	oldLease.Release()
	manager = f.manager()
	manager.runtimeOperations = f.provider.RuntimeOperations()
	hooks := &coldResetHooks{}
	manager.hooks = hooks
	runtimeManagerBefore := manager.runtimeManager.(*fakeScopedRuntimeManager)
	runtimeManagerBefore.currentSnapshot = freshLifetimeRecoveryCatalog
	runtimeManagerBefore.runtimeForSession = func(request SessionRuntimeRequest) (*BuiltRuntime, error) {
		runtime := *f.runtime
		runtime.RuntimeSnapshotID = request.RuntimeSnapshotID
		return &runtime, nil
	}
	request := FreshLifetimeRequest{OperationID: "fresh-cold-recovery", SessionID: topic.GetSessionID(), InputHash: strings.Repeat("e", 64)}
	prepared, _, err := manager.PrepareFreshLifetime(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	runtimeManagerBefore.currentSnapshot = freshLifetimeLaterCatalog
	if err := f.provider.RuntimeOperations().MarkApplying(t.Context(), request.OperationID, request.InputHash); err != nil {
		t.Fatal(err)
	}
	live := freshLifetimeTargetRecord(*prepared.predecessor, prepared.operation)
	if _, err := manager.archive.Activate(t.Context(), prepared.archiveToken, &live); err != nil {
		t.Fatal(err)
	}
	operation, found, err := f.provider.RuntimeOperations().Get(t.Context(), request.OperationID)
	if err != nil || !found {
		t.Fatalf("applying operation = %#v, %t, %v", operation, found, err)
	}

	restarted := f.manager()
	restarted.runtimeOperations = f.provider.RuntimeOperations()
	restarted.hooks = hooks
	runtimeManager := restarted.runtimeManager.(*fakeScopedRuntimeManager)
	runtimeManager.currentSnapshot = freshLifetimeLaterCatalog
	runtimeManager.runtimeForSession = runtimeManagerBefore.runtimeForSession

	if err := restarted.recoverApplyingFreshLifetimeOperation(t.Context(), operation); err != nil {
		t.Fatal(err)
	}
	if len(runtimeManager.sessionRequests) != 0 {
		t.Fatalf("cold recovery built provider runtime %d times", len(runtimeManager.sessionRequests))
	}
	if len(runtimeManager.unpublishedRequests) != 0 {
		t.Fatalf("cold recovery unpublished runtime builds = %d, want 0", len(runtimeManager.unpublishedRequests))
	}
	if runtimeManager.fenceCalls != 0 || runtimeManager.buildBeforeFence {
		t.Fatalf("cold recovery fence/build order = calls %d, build-before-fence %t", runtimeManager.fenceCalls, runtimeManager.buildBeforeFence)
	}
	record, found, err := f.provider.Sessions().GetBySessionID(t.Context(), request.SessionID)
	if err != nil || !found || record.ArchiveID != prepared.operation.TargetArchiveID ||
		record.RuntimeGeneration != prepared.operation.ExpectedGeneration+1 {
		t.Fatalf("cold recovered metadata = %#v, %t, %v", record, found, err)
	}
	if len(hooks.events) != 1 || hooks.events[0].Source != "startup" {
		t.Fatalf("cold recovery repeated lifecycle hooks: %+v", hooks.events)
	}
	restarted.hooks = nil
	restored, err := restarted.RestoreSession(t.Context(), SessionContext{Locator: archiveInput().Locator})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := restored.AcquireRuntime()
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	if lease.RuntimeSnapshotID() != freshLifetimeRecoveryCatalog {
		t.Fatalf("recovery snapshot=%q", lease.RuntimeSnapshotID())
	}

	if lease.SessionID() != prepared.operation.TargetArchiveID {
		t.Fatalf("restored target = %q, want %q", lease.SessionID(), prepared.operation.TargetArchiveID)
	}
	for event := range lease.Session().Events().All() {
		if event.ID == "old-context" {
			t.Fatal("cold recovery restored predecessor context")
		}
	}
}

func TestFreshLifetimeRecoveryFencesContradictoryTarget(t *testing.T) {
	f := newArchiveFixture(t)
	f.runtime.ProviderBindingJSON = publicationBinding
	manager := f.manager()
	manager.runtimeOperations = f.provider.RuntimeOperations()
	topic, err := manager.EnsureSession(t.Context(), archiveInput(), "saved")
	if err != nil {
		t.Fatal(err)
	}
	request := FreshLifetimeRequest{OperationID: "fresh-contradiction", SessionID: topic.GetSessionID(), InputHash: strings.Repeat("f", 64)}
	prepared, _, err := manager.PrepareFreshLifetime(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	event := adksession.NewEvent(t.Context(), "contradictory-context")
	event.Author = ensureTestAuthor
	if err := f.runtime.SessionSvc.AppendEvent(t.Context(), prepared.candidate.value.session, event); err != nil {
		t.Fatal(err)
	}
	if err := f.provider.RuntimeOperations().MarkApplying(t.Context(), request.OperationID, request.InputHash); err != nil {
		t.Fatal(err)
	}
	operation, _, err := f.provider.RuntimeOperations().Get(t.Context(), request.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.recoverApplyingFreshLifetimeOperation(t.Context(), operation); !errors.Is(err, ErrRuntimeRecoveryRequired) {
		t.Fatalf("recovery error = %v, want recovery required", err)
	}
	if _, err := topic.AcquireRuntime(); !errors.Is(err, ErrRuntimeRecoveryRequired) {
		t.Fatalf("AcquireRuntime() = %v, want recovery fence", err)
	}
	unresolved, _, err := f.provider.RuntimeOperations().Get(t.Context(), request.OperationID)
	if err != nil || unresolved.State != baldastate.RuntimeOperationApplying {
		t.Fatalf("unresolved operation = %#v, %v", unresolved, err)
	}
}

func TestFreshLifetimeConcurrentRedeliveryPublishesOnce(t *testing.T) {
	f := newArchiveFixture(t)
	f.runtime.ProviderBindingJSON = publicationBinding
	manager := f.manager()
	manager.runtimeOperations = f.provider.RuntimeOperations()
	topic, err := manager.EnsureSession(t.Context(), archiveInput(), "saved")
	if err != nil {
		t.Fatal(err)
	}
	initialCreates := f.builder.freshCalls
	request := FreshLifetimeRequest{OperationID: "fresh-redelivery", SessionID: topic.GetSessionID(), InputHash: strings.Repeat("1", 64)}

	type preparedResult struct {
		plan   *PreparedFreshLifetime
		result FreshLifetimeResult
		err    error
	}
	start := make(chan struct{})
	preparedResults := make(chan preparedResult, 2)
	for range 2 {
		go func() {
			<-start
			plan, result, err := manager.PrepareFreshLifetime(t.Context(), request)
			preparedResults <- preparedResult{plan: plan, result: result, err: err}
		}()
	}
	close(start)
	first := <-preparedResults
	second := <-preparedResults
	for i, got := range []preparedResult{first, second} {
		if got.err != nil || got.plan == nil || got.result.Status != "" {
			t.Fatalf("prepare %d = %#v, %#v, %v", i, got.plan, got.result, got.err)
		}
	}
	if f.builder.freshCalls != initialCreates+1 {
		t.Fatalf("redelivery local session creates = %d, want %d", f.builder.freshCalls, initialCreates+1)
	}

	results := make(chan preparedResult, 2)
	for _, plan := range []*PreparedFreshLifetime{first.plan, second.plan} {
		go func(plan *PreparedFreshLifetime) {
			result, err := manager.PublishFreshLifetime(t.Context(), plan)
			results <- preparedResult{result: result, err: err}
		}(plan)
	}
	for i := range 2 {
		got := <-results
		if got.err != nil || got.result.Status != FreshLifetimeReset || got.result.Generation != 1 {
			t.Fatalf("publish %d = %#v, %v", i, got.result, got.err)
		}
	}
	lease, err := topic.AcquireRuntime()
	if err != nil {
		t.Fatal(err)
	}
	if lease.Generation() != 1 || lease.SessionID() != deterministicFreshLifetimeID(request) {
		t.Fatalf("published runtime = generation %d session %q", lease.Generation(), lease.SessionID())
	}
	lease.Release()
	if err := topic.DrainRuntime(context.Background()); err != nil {
		t.Fatal(err)
	}
	operation, found, err := f.provider.RuntimeOperations().Get(t.Context(), request.OperationID)
	if err != nil || !found || operation.State != baldastate.RuntimeOperationTerminal ||
		operation.Outcome != string(baldastate.RuntimeOperationOutcomeContextReset) {
		t.Fatalf("operation = %#v, %t, %v", operation, found, err)
	}
}

func TestFreshLifetimePreparationFailureRollsBackAuthoritatively(t *testing.T) {
	for _, test := range []struct {
		name      string
		configure func(*archiveFixture, *Manager)
	}{
		{
			name: "archive begin rejected",
			configure: func(f *archiveFixture, manager *Manager) {
				manager.archive = &archiveFault{ArchiveLifecycle: f.provider.ChatArchive(), beginErr: chatarchivecmd.ErrUnavailable}
			},
		},
		{
			name: "local create rejected",
			configure: func(f *archiveFixture, _ *Manager) {
				f.builder.fail = true
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newArchiveFixture(t)
			f.runtime.ProviderBindingJSON = publicationBinding
			manager := f.manager()
			manager.runtimeOperations = f.provider.RuntimeOperations()
			topic, err := manager.EnsureSession(t.Context(), archiveInput(), "display-label")
			if err != nil {
				t.Fatal(err)
			}
			test.configure(f, manager)
			request := FreshLifetimeRequest{
				OperationID: "rollback-" + strings.ReplaceAll(test.name, " ", "-"),
				SessionID:   topic.GetSessionID(),
				InputHash:   strings.Repeat("2", 64),
			}
			plan, result, err := manager.PrepareFreshLifetime(t.Context(), request)
			if err != nil || plan != nil || result.Status != FreshLifetimeUnavailable {
				t.Fatalf("PrepareFreshLifetime() = %#v, %#v, %v", plan, result, err)
			}
			operation, found, err := f.provider.RuntimeOperations().Get(t.Context(), request.OperationID)
			if err != nil || !found || operation.State != baldastate.RuntimeOperationTerminal ||
				operation.Outcome != string(baldastate.RuntimeOperationOutcomeAborted) {
				t.Fatalf("rolled back operation = %#v, %t, %v", operation, found, err)
			}
			lease, err := topic.AcquireRuntime()
			if err != nil {
				t.Fatalf("incumbent fenced after definitive preparation failure: %v", err)
			}
			lease.Release()
			if _, err := f.provider.ChatArchive().Pending(t.Context(), topic.locator.ChannelType, topic.locator.AddressKey); !errors.Is(err, chatarchivecmd.ErrNotFound) {
				t.Fatalf("pending archive after rollback = %v", err)
			}
		})
	}
}

type uncertainBeginRuntimeStore struct {
	baldastate.RuntimeOperationStore
}

func (s uncertainBeginRuntimeStore) Begin(ctx context.Context, operation baldastate.RuntimeOperationRecord) (baldastate.RuntimeOperationRecord, error) {
	committed, err := s.RuntimeOperationStore.Begin(context.WithoutCancel(ctx), operation)
	if err != nil {
		return committed, err
	}
	return baldastate.RuntimeOperationRecord{}, context.Canceled
}

type freshLifetimeGetFaultService struct {
	adksession.Service
	fail atomic.Bool
}

func (s *freshLifetimeGetFaultService) Get(ctx context.Context, request *adksession.GetRequest) (*adksession.GetResponse, error) {
	if s.fail.Load() {
		return nil, errors.New("private-get-secret")
	}
	return s.Service.Get(ctx, request)
}

type freshLifetimeCreateCommitErrorService struct {
	adksession.Service
	fail atomic.Bool
}

func (s *freshLifetimeCreateCommitErrorService) Create(ctx context.Context, request *adksession.CreateRequest) (*adksession.CreateResponse, error) {
	response, err := s.Service.Create(ctx, request)
	if err != nil {
		return response, err
	}
	if !s.fail.Load() {
		return response, nil
	}
	return nil, errors.New("private-create-secret")
}

func TestFreshLifetimePreparationUncertaintyUsesStoreTruth(t *testing.T) {
	t.Run("begin committed then cancellation rolls back", func(t *testing.T) {
		f := newArchiveFixture(t)
		f.runtime.ProviderBindingJSON = publicationBinding
		manager := f.manager()
		manager.runtimeOperations = uncertainBeginRuntimeStore{RuntimeOperationStore: f.provider.RuntimeOperations()}
		topic, err := manager.EnsureSession(t.Context(), archiveInput(), "display-label")
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		request := FreshLifetimeRequest{OperationID: "uncertain-begin", SessionID: topic.GetSessionID(), InputHash: strings.Repeat("3", 64)}
		plan, result, err := manager.PrepareFreshLifetime(ctx, request)
		if err != nil || plan != nil || result.Status != FreshLifetimeUnavailable || result.Reason != baldastate.RuntimeOperationReasonCanceled {
			t.Fatalf("PrepareFreshLifetime() = %#v, %#v, %v", plan, result, err)
		}
		operation, found, err := f.provider.RuntimeOperations().Get(t.Context(), request.OperationID)
		if err != nil || !found || operation.State != baldastate.RuntimeOperationTerminal ||
			operation.ReasonCode != string(baldastate.RuntimeOperationReasonCanceled) {
			t.Fatalf("resolved begin = %#v, %t, %v", operation, found, err)
		}
	})

	t.Run("create committed then error is adopted by Get", func(t *testing.T) {
		f := newArchiveFixture(t)
		f.runtime.ProviderBindingJSON = publicationBinding
		service := &freshLifetimeCreateCommitErrorService{Service: f.runtime.SessionSvc}
		f.runtime.SessionSvc = service
		f.builder.service = service
		manager := f.manager()
		manager.runtimeOperations = f.provider.RuntimeOperations()
		topic, err := manager.EnsureSession(t.Context(), archiveInput(), "display-label")
		if err != nil {
			t.Fatal(err)
		}
		service.fail.Store(true)
		request := FreshLifetimeRequest{OperationID: "uncertain-create", SessionID: topic.GetSessionID(), InputHash: strings.Repeat("4", 64)}
		plan, _, err := manager.PrepareFreshLifetime(t.Context(), request)
		if err != nil || plan == nil {
			t.Fatalf("PrepareFreshLifetime() = %#v, %v", plan, err)
		}
		if _, err := manager.AbortFreshLifetime(t.Context(), plan); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("unreadable target remains prepared and fenced", func(t *testing.T) {
		f := newArchiveFixture(t)
		f.runtime.ProviderBindingJSON = publicationBinding
		service := &freshLifetimeGetFaultService{Service: f.runtime.SessionSvc}
		f.runtime.SessionSvc = service
		f.builder.service = service
		manager := f.manager()
		manager.runtimeOperations = f.provider.RuntimeOperations()
		topic, err := manager.EnsureSession(t.Context(), archiveInput(), "display-label")
		if err != nil {
			t.Fatal(err)
		}
		service.fail.Store(true)
		request := FreshLifetimeRequest{OperationID: "uncertain-get", SessionID: topic.GetSessionID(), InputHash: strings.Repeat("5", 64)}
		plan, result, err := manager.PrepareFreshLifetime(t.Context(), request)
		if plan != nil || result.Status != FreshLifetimeRecoveryRequired || !errors.Is(err, ErrRuntimeRecoveryRequired) {
			t.Fatalf("PrepareFreshLifetime() = %#v, %#v, %v", plan, result, err)
		}
		operation, found, getErr := f.provider.RuntimeOperations().Get(t.Context(), request.OperationID)
		if getErr != nil || !found || operation.State != baldastate.RuntimeOperationPrepared {
			t.Fatalf("uncertain operation = %#v, %t, %v", operation, found, getErr)
		}
		if _, acquireErr := topic.AcquireRuntime(); !errors.Is(acquireErr, ErrRuntimeRecoveryRequired) {
			t.Fatalf("AcquireRuntime() = %v, want recovery fence", acquireErr)
		}
	})
}

func TestFreshLifetimePublishAbortRaceSettlesOnce(t *testing.T) {
	f := newArchiveFixture(t)
	var closeCalls atomic.Int32
	f.runtime.ProviderBindingJSON = publicationBinding
	f.runtime.Close = func() error {
		closeCalls.Add(1)
		return nil
	}
	manager := f.manager()
	manager.runtimeOperations = f.provider.RuntimeOperations()
	topic, err := manager.EnsureSession(t.Context(), archiveInput(), "display-label")
	if err != nil {
		t.Fatal(err)
	}
	request := FreshLifetimeRequest{OperationID: "publish-abort-race", SessionID: topic.GetSessionID(), InputHash: strings.Repeat("6", 64)}
	prepared, _, err := manager.PrepareFreshLifetime(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	type settled struct {
		result FreshLifetimeResult
		err    error
	}
	start := make(chan struct{})
	results := make(chan settled, 2)
	go func() {
		<-start
		result, err := manager.PublishFreshLifetime(t.Context(), prepared)
		results <- settled{result: result, err: err}
	}()
	go func() {
		<-start
		result, err := manager.AbortFreshLifetime(t.Context(), prepared)
		results <- settled{result: result, err: err}
	}()
	close(start)
	first, second := <-results, <-results
	if first.err != nil || second.err != nil || !reflect.DeepEqual(first.result, second.result) {
		t.Fatalf("settlements = %#v/%v and %#v/%v", first.result, first.err, second.result, second.err)
	}
	if first.result.Status != FreshLifetimeReset && first.result.Status != FreshLifetimeUnavailable {
		t.Fatalf("settlement status = %q", first.result.Status)
	}
	lease, err := topic.AcquireRuntime()
	if err != nil {
		t.Fatalf("terminal settlement fenced incumbent: %v", err)
	}
	lease.Release()
	if err := topic.DrainRuntime(context.Background()); err != nil {
		t.Fatal(err)
	}
	if closeCalls.Load() != 1 {
		t.Fatalf("incumbent aggregate close calls = %d, want 1", closeCalls.Load())
	}
}

func TestFreshLifetimeRedeliveredAbortReplaysPublishedTerminal(t *testing.T) {
	f := newArchiveFixture(t)
	f.runtime.ProviderBindingJSON = publicationBinding
	manager := f.manager()
	manager.runtimeOperations = f.provider.RuntimeOperations()
	topic, err := manager.EnsureSession(t.Context(), archiveInput(), "display-label")
	if err != nil {
		t.Fatal(err)
	}
	request := FreshLifetimeRequest{OperationID: "publish-then-abort", SessionID: topic.GetSessionID(), InputHash: strings.Repeat("8", 64)}
	first, _, err := manager.PrepareFreshLifetime(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	redelivered, _, err := manager.PrepareFreshLifetime(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	published, err := manager.PublishFreshLifetime(t.Context(), first)
	if err != nil || published.Status != FreshLifetimeReset {
		t.Fatalf("PublishFreshLifetime() = %#v, %v", published, err)
	}
	replayed, err := manager.AbortFreshLifetime(t.Context(), redelivered)
	if err != nil || !reflect.DeepEqual(replayed, published) {
		t.Fatalf("AbortFreshLifetime() replay = %#v, %v; want %#v", replayed, err, published)
	}
	lease, err := topic.AcquireRuntime()
	if err != nil {
		t.Fatalf("terminal replay fenced runtime: %v", err)
	}
	lease.Release()
	if err := topic.DrainRuntime(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestFreshLifetimePreparationLeaseBlocksDrainUntilRollback(t *testing.T) {
	f := newArchiveFixture(t)
	var closeCalls atomic.Int32
	f.runtime.ProviderBindingJSON = publicationBinding
	f.runtime.Close = func() error {
		closeCalls.Add(1)
		return nil
	}
	manager := f.manager()
	manager.runtimeOperations = f.provider.RuntimeOperations()
	topic, err := manager.EnsureSession(t.Context(), archiveInput(), "display-label")
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	f.builder.beforeFresh = func() {
		close(entered)
		<-release
	}
	request := FreshLifetimeRequest{OperationID: "prepare-drain", SessionID: topic.GetSessionID(), InputHash: strings.Repeat("7", 64)}
	type preparation struct {
		plan   *PreparedFreshLifetime
		result FreshLifetimeResult
		err    error
	}
	prepared := make(chan preparation, 1)
	go func() {
		plan, result, err := manager.PrepareFreshLifetime(t.Context(), request)
		prepared <- preparation{plan: plan, result: result, err: err}
	}()
	<-entered
	drained := make(chan error, 1)
	go func() {
		drained <- topic.DrainRuntime(context.Background())
	}()
	select {
	case err := <-drained:
		t.Fatalf("drain completed while preparation held a lease: %v", err)
	default:
	}
	if closeCalls.Load() != 0 {
		t.Fatalf("aggregate closed during preparation: %d", closeCalls.Load())
	}
	close(release)
	got := <-prepared
	if got.err != nil || got.plan == nil || got.result.Status != "" {
		t.Fatalf("PrepareFreshLifetime() = %#v, %#v, %v", got.plan, got.result, got.err)
	}
	if err := <-drained; err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AbortFreshLifetime(t.Context(), got.plan); err != nil {
		t.Fatal(err)
	}
	if closeCalls.Load() != 1 {
		t.Fatalf("incumbent aggregate close calls = %d, want 1", closeCalls.Load())
	}
}

func TestFreshLifetimeAfterHostedReplacementPreservesProviderIdentity(t *testing.T) {
	f := newArchiveFixture(t)
	f.runtime.ProviderBindingJSON = publicationBinding
	f.runtime.RecipeModel = freshLifetimeHostedModel
	manager := f.manager()
	manager.runtimeOperations = f.provider.RuntimeOperations()
	topic, err := manager.EnsureSession(t.Context(), archiveInput(), "display-label")
	if err != nil {
		t.Fatal(err)
	}
	incumbent := topic.runtime
	replacementRuntime := incumbent.value.runtime
	replacementRuntime.RecipeModel = freshLifetimeHostedModel
	replacementRuntime.ConfirmedModel = freshLifetimeHostedModel
	replacement, err := incumbent.independentModelSuccessor(
		&replacementRuntime,
		incumbent.value.session,
		freshLifetimeHostedModel,
		string(baldastate.RuntimeOperationOutcomeContextPreserved),
	)
	if err != nil {
		t.Fatal(err)
	}
	// This fixture exercises provider identity only; keep its durable guard
	// aligned without synthesizing a separate model-operation journal.
	replacement.value.generation = incumbent.value.generation
	if replacement.value.providerID != freshLifetimeProviderID {
		t.Fatalf("hosted successor provider = %q, want %q", replacement.value.providerID, freshLifetimeProviderID)
	}
	if !replacement.claimPublication() ||
		!topic.swapRuntime(incumbent, replacement, incumbent.value.archiveID, incumbent.value.generation) {
		t.Fatal("failed to publish hosted replacement fixture")
	}
	replacement.releasePublicationClaim(true)
	incumbent.beginDrain()
	if err := manager.sessionStore.Upsert(t.Context(), manager.sessionRecordFromValue(topic, baldastate.SessionStatusActive, replacement.value)); err != nil {
		t.Fatal(err)
	}

	request := FreshLifetimeRequest{OperationID: "hosted-then-reset", SessionID: topic.GetSessionID(), InputHash: strings.Repeat("9", 64)}
	prepared, prepareResult, err := manager.PrepareFreshLifetime(t.Context(), request)
	if err != nil || prepared == nil {
		t.Fatalf("PrepareFreshLifetime() = %#v, %#v, %v", prepared, prepareResult, err)
	}
	if got := f.builder.freshProviders[len(f.builder.freshProviders)-1]; got != freshLifetimeProviderID || got == topic.agentName {
		t.Fatalf("reset initializer provider = %q, display label = %q", got, topic.agentName)
	}
	if _, err := manager.AbortFreshLifetime(t.Context(), prepared); err != nil {
		t.Fatal(err)
	}
	if err := topic.DrainRuntime(context.Background()); err != nil {
		t.Fatal(err)
	}
}

type freshLifetimeOperationGetErrorStore struct {
	baldastate.RuntimeOperationStore
}

func (s freshLifetimeOperationGetErrorStore) Get(context.Context, string) (baldastate.RuntimeOperationRecord, bool, error) {
	return baldastate.RuntimeOperationRecord{}, false, errors.New("private-operation-secret")
}

func TestFreshLifetimeAbortGetAmbiguityReleasesCandidateOwner(t *testing.T) {
	f := newArchiveFixture(t)
	var closeCalls atomic.Int32
	f.runtime.ProviderBindingJSON = publicationBinding
	f.runtime.Close = func() error {
		closeCalls.Add(1)
		return nil
	}
	manager := f.manager()
	manager.runtimeOperations = f.provider.RuntimeOperations()
	topic, err := manager.EnsureSession(t.Context(), archiveInput(), "display-label")
	if err != nil {
		t.Fatal(err)
	}
	request := FreshLifetimeRequest{OperationID: "abort-get-ambiguity", SessionID: topic.GetSessionID(), InputHash: strings.Repeat("a", 64)}
	prepared, _, err := manager.PrepareFreshLifetime(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	manager.runtimeOperations = freshLifetimeOperationGetErrorStore{RuntimeOperationStore: f.provider.RuntimeOperations()}
	result, err := manager.AbortFreshLifetime(t.Context(), prepared)
	if result.Status != FreshLifetimeRecoveryRequired || !errors.Is(err, ErrRuntimeRecoveryRequired) {
		t.Fatalf("AbortFreshLifetime() = %#v, %v", result, err)
	}
	if err := topic.DrainRuntime(context.Background()); err != nil {
		t.Fatal(err)
	}
	if closeCalls.Load() != 1 {
		t.Fatalf("incumbent aggregate close calls = %d, want 1", closeCalls.Load())
	}
}

type freshLifetimeCloseAwareService struct {
	adksession.Service
	closed       *atomic.Bool
	violation    *atomic.Bool
	deleteEnter  chan struct{}
	deleteResume chan struct{}
}

func (s *freshLifetimeCloseAwareService) Delete(ctx context.Context, request *adksession.DeleteRequest) error {
	if s.closed.Load() {
		s.violation.Store(true)
	}
	close(s.deleteEnter)
	<-s.deleteResume
	if s.closed.Load() {
		s.violation.Store(true)
	}
	return s.Service.Delete(ctx, request)
}

type freshLifetimeFinalizeBarrierStore struct {
	baldastate.RuntimeOperationStore
	closed         *atomic.Bool
	violation      *atomic.Bool
	finalizeEnter  chan struct{}
	finalizeResume chan struct{}
}

func (s *freshLifetimeFinalizeBarrierStore) Finalize(ctx context.Context, final baldastate.RuntimeOperationFinalization) (baldastate.RuntimeOperationRecord, error) {
	if final.Outcome == baldastate.RuntimeOperationOutcomeAborted {
		if s.closed.Load() {
			s.violation.Store(true)
		}
		close(s.finalizeEnter)
		<-s.finalizeResume
		if s.closed.Load() {
			s.violation.Store(true)
		}
	}
	return s.RuntimeOperationStore.Finalize(ctx, final)
}

func TestFreshLifetimeAbortRetainsOwnerThroughDeleteAndFinalize(t *testing.T) {
	f := newArchiveFixture(t)
	var closed, violation atomic.Bool
	var closeCalls atomic.Int32
	service := &freshLifetimeCloseAwareService{
		Service: f.runtime.SessionSvc, closed: &closed, violation: &violation,
		deleteEnter: make(chan struct{}), deleteResume: make(chan struct{}),
	}
	f.runtime.SessionSvc = service
	f.builder.service = service
	f.runtime.ProviderBindingJSON = publicationBinding
	f.runtime.Close = func() error {
		closed.Store(true)
		closeCalls.Add(1)
		return nil
	}
	operations := &freshLifetimeFinalizeBarrierStore{
		RuntimeOperationStore: f.provider.RuntimeOperations(),
		closed:                &closed,
		violation:             &violation,
		finalizeEnter:         make(chan struct{}),
		finalizeResume:        make(chan struct{}),
	}
	manager := f.manager()
	manager.runtimeOperations = operations
	topic, err := manager.EnsureSession(t.Context(), archiveInput(), "display-label")
	if err != nil {
		t.Fatal(err)
	}
	request := FreshLifetimeRequest{OperationID: "abort-owner-barrier", SessionID: topic.GetSessionID(), InputHash: strings.Repeat("b", 64)}
	prepared, _, err := manager.PrepareFreshLifetime(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	type abortResult struct {
		result FreshLifetimeResult
		err    error
	}
	aborted := make(chan abortResult, 1)
	go func() {
		result, err := manager.AbortFreshLifetime(t.Context(), prepared)
		aborted <- abortResult{result: result, err: err}
	}()
	<-service.deleteEnter
	drained := make(chan error, 1)
	go func() {
		drained <- topic.DrainRuntime(context.Background())
	}()
	close(service.deleteResume)
	<-operations.finalizeEnter
	close(operations.finalizeResume)
	got := <-aborted
	if got.err != nil || got.result.Status != FreshLifetimeUnavailable {
		t.Fatalf("AbortFreshLifetime() = %#v, %v", got.result, got.err)
	}
	if err := <-drained; err != nil {
		t.Fatal(err)
	}
	if closeCalls.Load() != 1 {
		t.Fatalf("incumbent close calls=%d", closeCalls.Load())
	}
}

func TestFreshLifetimeFreezesCurrentCatalogInsteadOfIncumbent(t *testing.T) {
	f := newArchiveFixture(t)
	f.runtime.ProviderBindingJSON = publicationBinding
	manager := f.manager()
	manager.runtimeOperations = f.provider.RuntimeOperations()
	topic, err := manager.EnsureSession(t.Context(), archiveInput(), "saved")
	if err != nil {
		t.Fatal(err)
	}
	runtimeManager := manager.runtimeManager.(*fakeScopedRuntimeManager)
	oldSnapshot := topic.runtime.value.runtime.RuntimeSnapshotID
	runtimeManager.currentSnapshot = freshLifetimeCurrentCatalog
	runtimeManager.runtimeForSession = func(request SessionRuntimeRequest) (*BuiltRuntime, error) {
		runtime := *f.runtime
		runtime.RuntimeSnapshotID = request.RuntimeSnapshotID
		return &runtime, nil
	}
	request := FreshLifetimeRequest{OperationID: "current-catalog", SessionID: topic.GetSessionID(), InputHash: strings.Repeat("c", 64)}
	prepared, result, err := manager.PrepareFreshLifetime(t.Context(), request)
	if err != nil || prepared == nil {
		t.Fatalf("prepare = %#v, %v", result, err)
	}
	if prepared.operation.TargetSnapshotID != freshLifetimeCurrentCatalog || oldSnapshot == prepared.operation.TargetSnapshotID {
		t.Errorf("frozen snapshot=%q, incumbent=%q", prepared.operation.TargetSnapshotID, oldSnapshot)
	}
	runtimeManager.currentSnapshot = "catalog-current-C"
	duplicate, _, err := manager.PrepareFreshLifetime(t.Context(), request)
	if err != nil || duplicate == nil {
		t.Fatalf("retry prepare=%v", err)
	}
	result, err = manager.PublishFreshLifetime(t.Context(), prepared)
	if err != nil || result.Status != FreshLifetimeReset {
		t.Fatalf("publish=%#v, %v", result, err)
	}
	if _, err := manager.AbortFreshLifetime(t.Context(), duplicate); err != nil {
		t.Fatal(err)
	}
	replay, found, err := manager.ResolveFreshLifetimeResult(t.Context(), request)
	if err != nil || !found || !reflect.DeepEqual(replay, result) {
		t.Fatalf("receipt=%+v found=%v err=%v, want %+v", replay, found, err, result)
	}
	collision := request
	collision.InputHash = strings.Repeat("d", 64)
	if got, found, err := manager.ResolveFreshLifetimeResult(t.Context(), collision); err != nil || !found || got.Status != FreshLifetimeConflict {
		t.Fatalf("collision=%+v,%v,%v", got, found, err)
	}
	missing := request
	missing.OperationID = "never-admitted"
	if _, found, err := manager.ResolveFreshLifetimeResult(t.Context(), missing); err != nil || found {
		t.Fatalf("missing found=%v err=%v", found, err)
	}
	if topic.runtime.value.runtime.RuntimeSnapshotID != freshLifetimeCurrentCatalog || runtimeManager.snapshotCalls != 1 {
		t.Errorf("published snapshot=%q selections=%d", topic.runtime.value.runtime.RuntimeSnapshotID, runtimeManager.snapshotCalls)
	}
}

func TestFreshLifetimeSelectionFailureLeavesPredecessorAndNoOperation(t *testing.T) {
	f := newArchiveFixture(t)
	manager := f.manager()
	manager.runtimeOperations = f.provider.RuntimeOperations()
	topic, err := manager.EnsureSession(t.Context(), archiveInput(), "saved")
	if err != nil {
		t.Fatal(err)
	}
	runtimeManager := manager.runtimeManager.(*fakeScopedRuntimeManager)
	runtimeManager.snapshotErr = errors.New("synthetic catalog selection failure")
	before := topic.GetArchiveID()
	request := FreshLifetimeRequest{OperationID: "selection-failure", SessionID: topic.GetSessionID(), InputHash: strings.Repeat("e", 64)}
	plan, result, err := manager.PrepareFreshLifetime(t.Context(), request)
	if plan != nil || result.Status != FreshLifetimeUnavailable || err == nil {
		t.Fatalf("prepare=%v,%+v,%v", plan, result, err)
	}
	if _, found, err := f.provider.RuntimeOperations().Get(t.Context(), request.OperationID); err != nil || found {
		t.Fatalf("operation found=%v err=%v", found, err)
	}
	if topic.GetArchiveID() != before || len(runtimeManager.unpublishedRequests) != 0 {
		t.Fatalf("archive=%q builds=%d", topic.GetArchiveID(), len(runtimeManager.unpublishedRequests))
	}
	if _, err := manager.archive.Pending(t.Context(), topic.locator.ChannelType, topic.locator.AddressKey); !errors.Is(err, chatarchivecmd.ErrNotFound) {
		t.Fatalf("pending archive=%v", err)
	}
}
