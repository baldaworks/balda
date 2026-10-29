package session

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"

	baldastate "github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/rs/zerolog"
	adksession "google.golang.org/adk/v2/session"
)

type lifetimePreferenceCrashService struct {
	adksession.Service
	deleteMode string
	armed      bool
}

func (s *lifetimePreferenceCrashService) Delete(ctx context.Context, request *adksession.DeleteRequest) error {
	if s.armed && s.deleteMode == "before" {
		s.armed = false
		return errors.New("injected delete-before failure")
	}
	err := s.Service.Delete(ctx, request)
	if s.armed && s.deleteMode == "after" {
		s.armed = false
		return errors.New("injected delete-after ambiguity")
	}
	return err
}

type lifetimePreferenceCrashBuilder struct {
	*sqliteAgentBuilder
	failAfterCreate bool
}

func (b *lifetimePreferenceCrashBuilder) CreateFreshRuntimeSession(
	ctx context.Context,
	runtime *BuiltRuntime,
	agentName, userID, sessionID, workspaceDir string,
	sessionCtx RuntimeSessionContext,
) (adksession.Session, error) {
	sess, err := b.sqliteAgentBuilder.CreateFreshRuntimeSession(
		ctx, runtime, agentName, userID, sessionID, workspaceDir, sessionCtx,
	)
	if err == nil && b.failAfterCreate {
		b.failAfterCreate = false
		return nil, errors.New("injected create-after ambiguity")
	}
	return sess, err
}

type lifetimePreferenceCommitErrorStore struct {
	baldastate.RuntimeOperationStore
	fail atomic.Bool
}

type lifetimePreferenceCaptureStore struct {
	baldastate.RuntimeOperationStore
	last baldastate.RuntimeOperationRecord
}

func (s *lifetimePreferenceCaptureStore) Begin(
	ctx context.Context,
	intent baldastate.RuntimeOperationRecord,
) (baldastate.RuntimeOperationRecord, error) {
	record, err := s.RuntimeOperationStore.Begin(ctx, intent)
	if err == nil {
		s.last = record
	}
	return record, err
}

func (s *lifetimePreferenceCommitErrorStore) Finalize(
	ctx context.Context,
	finalization baldastate.RuntimeOperationFinalization,
) (baldastate.RuntimeOperationRecord, error) {
	record, err := s.RuntimeOperationStore.Finalize(ctx, finalization)
	if err == nil && s.fail.Swap(false) {
		return record, baldastate.ErrRuntimeOperationUnavailable
	}
	return record, err
}

func TestLifetimePreferenceFallbackRecoversEveryApplyingCrashBoundary(t *testing.T) {
	for _, test := range []struct {
		name             string
		boundaryFailure  bool
		deleteMode       string
		createAfterError bool
		finalizeAfterErr bool
		terminalAtCrash  bool
	}{
		{name: "after mark applying", boundaryFailure: true},
		{name: "after boundary", deleteMode: "before"},
		{name: "after predecessor delete", deleteMode: "after"},
		{name: "after target create", createAfterError: true},
		{name: "after finalize commit", finalizeAfterErr: true, terminalAtCrash: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			const (
				providerID = "managed-provider"
				recipe     = "recipe-a"
				requested  = "requested-b"
			)
			locator := testTelegramLocator(61, 91)
			dbPath := filepath.Join(t.TempDir(), "lifetime-preference-recovery.db")
			provider, err := baldastate.NewSQLiteProvider(t.Context(), dbPath)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = provider.Close() })
			requestedValue := requested
			record := baldastate.SessionRecord{
				SessionID: locator.SessionID, UserID: ensureTestUserID,
				ChannelType: locator.ChannelType, AddressKey: locator.AddressKey, AddressJSON: locator.AddressJSON,
				AgentName: "display-label", RuntimeSnapshotID: "snapshot-old", ProviderBindingJSON: publicationBinding,
				RuntimeGeneration: 5, RequestedModel: &requestedValue, EffectiveModel: recipe,
				Status: baldastate.SessionStatusActive,
			}
			if err := provider.Sessions().Upsert(t.Context(), record); err != nil {
				t.Fatal(err)
			}
			baseService := provider.RuntimeSessions()
			created, err := baseService.Create(t.Context(), &adksession.CreateRequest{
				AppName: baldaRuntimeAppName, UserID: record.UserID, SessionID: record.SessionID,
			})
			if err != nil {
				t.Fatal(err)
			}
			event := adksession.NewEvent(t.Context(), "predecessor-event")
			event.Author = ensureTestAuthor
			if err := baseService.AppendEvent(t.Context(), created.Session, event); err != nil {
				t.Fatal(err)
			}
			service := &lifetimePreferenceCrashService{Service: baseService, deleteMode: test.deleteMode, armed: test.deleteMode != ""}
			currentRuntime := &BuiltRuntime{
				AppName: baldaRuntimeAppName, SessionSvc: service, RuntimeSnapshotID: currentSnapshotID,
				ProviderBindingJSON: publicationBinding, RecipeModel: requested,
			}
			runtimeManager := &fakeScopedRuntimeManager{
				fakeBaldaRuntimeManager: fakeBaldaRuntimeManager{providerID: providerID},
				runtimeForSession: func(request SessionRuntimeRequest) (*BuiltRuntime, error) {
					if request.ModelOverride == nil || *request.ModelOverride != requested {
						t.Fatalf("runtime override = %v, want %q", request.ModelOverride, requested)
					}
					if request.RuntimeSnapshotID == "snapshot-old" {
						return &BuiltRuntime{
							RuntimeSnapshotID: "snapshot-mismatch", ProviderBindingJSON: publicationBinding, RecipeModel: recipe,
						}, nil
					}
					return currentRuntime, nil
				},
			}
			builder := &lifetimePreferenceCrashBuilder{
				sqliteAgentBuilder: &sqliteAgentBuilder{metadata: AgentMetadata{Model: recipe}},
				failAfterCreate:    test.createAfterError,
			}
			capture := &lifetimePreferenceCaptureStore{RuntimeOperationStore: provider.RuntimeOperations()}
			operationStore := baldastate.RuntimeOperationStore(capture)
			if test.finalizeAfterErr {
				wrapped := &lifetimePreferenceCommitErrorStore{RuntimeOperationStore: operationStore}
				wrapped.fail.Store(true)
				operationStore = wrapped
			}
			observer := &fakeBoundaryObserver{}
			if test.boundaryFailure {
				observer.err = errors.New("injected boundary failure")
			}
			first := &Manager{
				baldaProviderName: providerID, runtimeManager: runtimeManager, agentBuilder: builder,
				workingDir: t.TempDir(), logger: zerolog.Nop(), sessions: make(map[string]*TopicSession),
				sessionStore: provider.Sessions(), runtimeOperations: operationStore, boundaryObserver: observer,
			}
			if _, err := first.RestoreSession(t.Context(), SessionContext{Locator: locator}); err == nil {
				t.Fatal("first restore unexpectedly crossed injected crash boundary")
			}
			operation, found, err := provider.RuntimeOperations().GetUnresolvedBySession(t.Context(), record.SessionID)
			if test.terminalAtCrash {
				if err != nil || found {
					t.Fatalf("terminal commit unresolved = %+v, %t, %v", operation, found, err)
				}
			} else if err != nil || !found || operation.State != baldastate.RuntimeOperationApplying ||
				operation.Kind != string(baldastate.RuntimeOperationKindLifetimePreference) {
				t.Fatalf("applying crash evidence = %+v, %t, %v", operation, found, err)
			}

			if !test.terminalAtCrash {
				blockedObserver := &fakeBoundaryObserver{}
				savedRecipe := currentRuntime.RecipeModel
				savedService := currentRuntime.SessionSvc
				switch {
				case test.boundaryFailure:
					currentRuntime.RecipeModel = "contradictory-recipe"
				case test.deleteMode == "before":
					currentRuntime.SessionSvc = nil
				case test.deleteMode != "":
					service.armed = true
				case test.createAfterError:
					builder.failAfterCreate = true
				}
				blocked := &Manager{
					baldaProviderName: providerID, runtimeManager: runtimeManager, agentBuilder: builder,
					workingDir: t.TempDir(), logger: zerolog.Nop(), sessions: make(map[string]*TopicSession),
					sessionStore: provider.Sessions(), runtimeOperations: provider.RuntimeOperations(),
					boundaryObserver: blockedObserver,
				}
				if _, err := blocked.RestoreSession(t.Context(), SessionContext{Locator: locator}); !errors.Is(err, ErrRuntimeRecoveryRequired) {
					t.Fatalf("blocked fresh Manager recovery error = %v, want recovery required", err)
				}
				operation, found, err = provider.RuntimeOperations().GetUnresolvedBySession(t.Context(), record.SessionID)
				if err != nil || !found || operation.State != baldastate.RuntimeOperationApplying {
					t.Fatalf("blocked recovery lost applying evidence = %+v, %t, %v", operation, found, err)
				}
				currentRuntime.RecipeModel = savedRecipe
				currentRuntime.SessionSvc = savedService
			}

			service.armed = false
			builder.failAfterCreate = false
			recoveryObserver := &fakeBoundaryObserver{}
			restarted := &Manager{
				baldaProviderName: providerID, runtimeManager: runtimeManager, agentBuilder: builder,
				workingDir: t.TempDir(), logger: zerolog.Nop(), sessions: make(map[string]*TopicSession),
				sessionStore: provider.Sessions(), runtimeOperations: provider.RuntimeOperations(),
				boundaryObserver: recoveryObserver,
			}
			restored, err := restarted.RestoreSession(t.Context(), SessionContext{Locator: locator})
			if err != nil {
				t.Fatalf("fresh Manager recovery: %v", err)
			}
			if len(observer.boundaries) != 1 || observer.boundaries[0].TransitionID != capture.last.OperationID {
				t.Fatalf("initial boundary evidence = %+v", observer.boundaries)
			}
			if !test.terminalAtCrash && (len(recoveryObserver.boundaries) != 1 ||
				recoveryObserver.boundaries[0].TransitionID != capture.last.OperationID ||
				!recoveryObserver.boundaries[0].OccurredAt.Equal(capture.last.CreatedAt)) {
				t.Fatalf("recovery boundary evidence = %+v, operation=%+v", recoveryObserver.boundaries, capture.last)
			}
			lease, err := restored.AcquireRuntime()
			if err != nil {
				t.Fatal(err)
			}
			if lease.Generation() != record.RuntimeGeneration+1 || lease.EffectiveModel() != requested {
				t.Fatalf("recovered slot = generation %d model %q", lease.Generation(), lease.EffectiveModel())
			}
			if lease.Session().Events().Len() != 0 {
				t.Fatalf("recovered target retained predecessor events: %d", lease.Session().Events().Len())
			}
			lease.Release()
			committed, found, err := provider.Sessions().GetBySessionID(t.Context(), record.SessionID)
			if err != nil || !found || committed.RequestedModel == nil || *committed.RequestedModel != requested ||
				committed.EffectiveModel != requested || committed.RuntimeGeneration != record.RuntimeGeneration+1 {
				t.Fatalf("recovered metadata = %+v, %t, %v", committed, found, err)
			}
			if operation, found, err := provider.RuntimeOperations().GetUnresolvedBySession(t.Context(), record.SessionID); err != nil || found {
				t.Fatalf("unresolved evidence after recovery = %+v, %t, %v", operation, found, err)
			}
			terminal, found, err := provider.RuntimeOperations().Get(t.Context(), capture.last.OperationID)
			if err != nil || !found || terminal.State != baldastate.RuntimeOperationTerminal ||
				terminal.Kind != string(baldastate.RuntimeOperationKindLifetimePreference) ||
				terminal.Outcome != string(baldastate.RuntimeOperationOutcomeContextReset) {
				t.Fatalf("terminal recovery evidence = %+v, %t, %v", terminal, found, err)
			}
			replayed, err := restarted.RestoreSession(t.Context(), SessionContext{Locator: locator})
			if err != nil || replayed != restored {
				t.Fatalf("terminal replay = %p, %v; want %p", replayed, err, restored)
			}
		})
	}
}
