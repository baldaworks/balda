package agent

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
	"github.com/baldaworks/runtime/v2/agentconfig"
	"github.com/baldaworks/runtime/v2/agentfactory"
	"github.com/baldaworks/runtime/v2/agentproduct"
	runtimeconfig "github.com/baldaworks/runtime/v2/appconfig"
	"github.com/baldaworks/runtime/v2/mcpregistry"
	"github.com/rs/zerolog"
	adkagent "google.golang.org/adk/v2/agent"
	adksession "google.golang.org/adk/v2/session"
)

func TestPinnedRuntimeMCPServerIDsPreservesHostConfiguration(t *testing.T) {
	t.Parallel()
	got := pinnedRuntimeMCPServerIDs(
		[]string{" host.one ", "shared", "host.one"},
		[]string{"snapshot.old", "shared"},
	)
	want := []string{"host.one", "shared", "snapshot.old"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("pinnedRuntimeMCPServerIDs() = %#v, want %#v", got, want)
	}
}

func TestRuntimeManagerAllowsPreflightWithoutCapabilityBinder(t *testing.T) {
	t.Parallel()
	field, ok := reflect.TypeOf(RuntimeManagerParams{}).FieldByName("CapabilityBinder")
	if !ok {
		t.Fatal("RuntimeManagerParams.CapabilityBinder field is missing")
	}
	if got := field.Tag.Get("optional"); got != "true" {
		t.Fatalf("CapabilityBinder optional tag = %q, want true", got)
	}
}

func TestRuntimeLifecycleContextSurvivesRequestCancellation(t *testing.T) {
	t.Parallel()

	type contextKey string
	const key contextKey = "runtime"
	requestCtx, cancel := context.WithCancel(context.WithValue(context.Background(), key, "value"))
	cancel()

	lifecycleCtx := runtimeLifecycleContext(requestCtx)
	if err := lifecycleCtx.Err(); err != nil {
		t.Fatalf("runtimeLifecycleContext() error = %v, want nil", err)
	}
	if got := lifecycleCtx.Value(key); got != "value" {
		t.Fatalf("runtimeLifecycleContext() value = %v, want value", got)
	}
}

type recordingCapabilityBinder struct {
	binding  SessionCapabilityBinding
	err      error
	requests []SessionRuntimeRequest
}

type recordingRuntimeFactory struct {
	requests []agentfactory.BuildRequest
}

type orderedProductFactory struct {
	order    *[]string
	closeErr error
}

func (f *orderedProductFactory) BuildScoped(_ context.Context, request agentfactory.BuildRequest, _ map[string][]string) (adkagent.Agent, error) {
	return adkagent.New(adkagent.Config{Name: request.Name})
}

func (f *orderedProductFactory) WithProviders(map[string]agentconfig.Config) ScopedRuntimeFactory {
	return f
}

func (f *orderedProductFactory) BuildSessionProduct(_ context.Context, request agentfactory.BuildRequest, _ map[string][]string, _ string) (*RuntimeProduct, error) {
	ag, err := adkagent.New(adkagent.Config{Name: request.Name})
	if err != nil {
		return nil, err
	}
	return NewRuntimeProduct(ag, nil, func() error {
		*f.order = append(*f.order, "product")
		return f.closeErr
	})
}

type orderedSessionMCPBinder struct {
	order      *[]string
	releaseErr error
}

func (b *orderedSessionMCPBinder) BindSession(context.Context, SessionRuntimeRequest) (ScopedMCPServer, error) {
	return ScopedMCPServer{
		ID: "ordered-session-mcp",
		Config: agentconfig.MCPServerConfig{
			Type: agentconfig.MCPServerTypeHTTP,
			URL:  "http://127.0.0.1:1/mcp/ordered",
		},
		Release: func() error {
			*b.order = append(*b.order, "binding")
			return b.releaseErr
		},
	}, nil
}

type partialSessionMCPBinder struct {
	binding ScopedMCPServer
	err     error
}

func (b *partialSessionMCPBinder) BindSession(context.Context, SessionRuntimeRequest) (ScopedMCPServer, error) {
	return b.binding, b.err
}

type recordingSessionMCPBinder struct {
	requests     []SessionRuntimeRequest
	releaseCalls int
}

func (b *recordingSessionMCPBinder) SessionMCPEnabled(capabilities SessionCapabilityBinding) bool {
	return len(capabilities.Skills.Skills) > 0
}

func (b *recordingSessionMCPBinder) BindSession(_ context.Context, request SessionRuntimeRequest) (ScopedMCPServer, error) {
	b.requests = append(b.requests, request)
	return ScopedMCPServer{
		ID: "session-skill-mcp",
		Config: agentconfig.MCPServerConfig{
			Type: agentconfig.MCPServerTypeHTTP,
			URL:  "http://127.0.0.1:1/mcp/session-skill-mcp",
		},
		Release: func() error {
			b.releaseCalls++
			return nil
		},
	}, nil
}

func (f *recordingRuntimeFactory) Build(_ context.Context, request agentfactory.BuildRequest) (adkagent.Agent, error) {
	f.requests = append(f.requests, request)
	return adkagent.New(adkagent.Config{Name: request.Name, Description: request.Description})
}

func TestRuntimeForSessionProjectsSkillMetadataAndExtensionInstructionsToProviderRequest(t *testing.T) {
	factory := &recordingRuntimeFactory{}
	var contributorInput SessionInstructionContext
	builder := &Builder{
		factory: factory,
		instructionContributors: []SessionInstructionContributor{
			&testInstructionContributor{id: "example.extension", content: "Extension guidance.", input: &contributorInput},
		},
	}
	binder := &recordingCapabilityBinder{binding: SessionCapabilityBinding{
		SnapshotID: "snapshot-prism",
		Skills: SkillMetadataProjection{
			Snapshot: "snapshot-prism",
			Skills: []SkillPromptMetadata{{
				Source:      runtimecatalogcmd.SourceID{Kind: runtimecatalogcmd.SourceKindPlugin, Name: "prism"},
				Name:        "story",
				Description: "Run a Prism story.",
				Revision:    "revision-prism",
			}},
		},
	}}
	manager := NewRuntimeManager(RuntimeManagerParams{
		Builder:          builder,
		BaldaProviderID:  "alpha",
		WorkingDir:       t.TempDir(),
		StateDir:         t.TempDir(),
		CapabilityBinder: binder,
		MCPRegistry:      mcpregistry.New(nil),
		Logger:           zerolog.Nop(),
	})

	requestedModel := "model-requested"
	runtime, err := manager.RuntimeForSession(context.Background(), SessionRuntimeRequest{
		Locator:       deliverycmd.Locator{SessionID: "session-1", ChannelType: "telegram"},
		ModelOverride: &requestedModel,
	})
	if err != nil {
		t.Fatalf("RuntimeForSession() error = %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	if len(factory.requests) != 1 {
		t.Fatalf("provider build requests = %d, want 1", len(factory.requests))
	}
	if factory.requests[0].ModelOverride == nil || *factory.requests[0].ModelOverride != requestedModel {
		t.Fatalf("provider model override = %v", factory.requests[0].ModelOverride)
	}
	if runtime.RecipeModel != requestedModel {
		t.Fatalf("runtime recipe receipt = %q, want requested override", runtime.RecipeModel)
	}
	instruction := factory.requests[0].Instruction
	for _, want := range []string{
		`source_kind="plugin" source_name="prism" skill_name="story" revision="revision-prism"`,
		"Run a Prism story.",
		"Extension instruction [example.extension]:\nExtension guidance.",
	} {
		if !strings.Contains(instruction, want) {
			t.Fatalf("provider instruction missing %q:\n%s", want, instruction)
		}
	}
	if contributorInput.SnapshotID != "snapshot-prism" || contributorInput.SessionID != "session-1" || contributorInput.ChannelType != "telegram" {
		t.Fatalf("contributor input = %+v, want pinned session context", contributorInput)
	}
}

func TestUnpublishedSessionRuntimeCandidatePromotesExactBaseAtomically(t *testing.T) {
	factory := &recordingRuntimeFactory{}
	builder := &Builder{factory: factory}
	binder := &recordingCapabilityBinder{binding: SessionCapabilityBinding{
		SnapshotID: "snapshot-candidate",
		Skills:     SkillMetadataProjection{Snapshot: "snapshot-candidate"},
	}}
	manager := NewRuntimeManager(RuntimeManagerParams{
		Builder: builder, BaldaProviderID: "alpha", WorkingDir: t.TempDir(), StateDir: t.TempDir(),
		CapabilityBinder: binder, MCPRegistry: mcpregistry.New(nil), Logger: zerolog.Nop(),
	})
	request := SessionRuntimeRequest{
		Locator:           deliverycmd.Locator{SessionID: "session-candidate", ChannelType: "telegram"},
		AgentSessionID:    "runtime-target",
		RuntimeSnapshotID: "snapshot-candidate",
	}
	incumbent, err := manager.RuntimeForSession(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := manager.BuildUnpublishedSessionRuntime(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if candidate.Runtime() == incumbent || candidate.Runtime().RuntimeBaseToken == incumbent.RuntimeBaseToken ||
		candidate.ExpectedBaseToken() != RuntimeBaseToken(incumbent.RuntimeBaseToken) || candidate.Runtime().ProviderID != "alpha" {
		t.Fatalf("candidate identity/token = %p/%d expected=%d, incumbent %p/%d", candidate.Runtime(), candidate.Runtime().RuntimeBaseToken, candidate.ExpectedBaseToken(), incumbent, incumbent.RuntimeBaseToken)
	}
	manager.mu.RLock()
	if got := manager.sessionBases[request.Locator.SessionID].runtime; got != incumbent {
		manager.mu.RUnlock()
		t.Fatalf("base visible during prepare = %p, want incumbent %p", got, incumbent)
	}
	if _, ok := manager.scopedRuntimes[candidate.Runtime().scopedID]; ok {
		manager.mu.RUnlock()
		t.Fatal("unpublished candidate visible in scoped runtimes")
	}
	manager.mu.RUnlock()
	aborted, err := manager.BuildUnpublishedSessionRuntime(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if err := aborted.Abort(t.Context()); err != nil {
		t.Fatal(err)
	}
	manager.mu.RLock()
	if manager.sessionBases[request.Locator.SessionID].runtime != incumbent {
		manager.mu.RUnlock()
		t.Fatal("aborting unpublished candidate replaced incumbent base")
	}
	if _, ok := manager.scopedRuntimes[aborted.Runtime().scopedID]; ok {
		manager.mu.RUnlock()
		t.Fatal("aborted candidate leaked into scoped runtimes")
	}
	manager.mu.RUnlock()
	stale, err := manager.BuildUnpublishedSessionRuntime(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if err := candidate.ClaimForPublication(); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.childRuntimeBase(t.Context(), request.Locator.SessionID); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("child lookup during promotion error = %v", err)
	}
	candidate.MarkPublished()
	if _, err := manager.childRuntimeBase(t.Context(), request.Locator.SessionID); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("premature mark released barrier: %v", err)
	}
	if err := candidate.Promote(); err != nil {
		t.Fatal(err)
	}
	wrongTarget := request
	wrongTarget.AgentSessionID = "another-runtime-target"
	if _, ok := manager.AdoptPromotedSessionRuntime(request.Locator.SessionID, incumbent.RuntimeBaseToken, wrongTarget); ok {
		t.Fatal("promoted candidate adopted for a different runtime target")
	}
	if adopted, ok := manager.AdoptPromotedSessionRuntime(request.Locator.SessionID, incumbent.RuntimeBaseToken, request); !ok || adopted != candidate {
		t.Fatalf("exact promoted candidate adoption = %p, %t", adopted, ok)
	}
	if _, err := manager.childRuntimeBase(t.Context(), request.Locator.SessionID); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("child lookup before slot publication error = %v", err)
	}
	candidate.MarkPublished()
	if _, err := manager.childRuntimeBase(t.Context(), request.Locator.SessionID); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("mark published released journal fence: %v", err)
	}
	if err := candidate.CompletePublication(); err != nil {
		t.Fatal(err)
	}
	if err := stale.ClaimForPublication(); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("stale base claim error = %v", err)
	}
	if err := stale.Abort(t.Context()); err != nil {
		t.Fatal(err)
	}
	base, err := manager.childRuntimeBase(t.Context(), request.Locator.SessionID)
	if err != nil || base.runtime != candidate.Runtime() {
		t.Fatalf("promoted base = %p, %v", base.runtime, err)
	}
	if err := incumbent.Close(); err != nil {
		t.Fatal(err)
	}
	base, err = manager.childRuntimeBase(t.Context(), request.Locator.SessionID)
	if err != nil || base.runtime != candidate.Runtime() {
		t.Fatalf("predecessor close removed candidate = %p, %v", base.runtime, err)
	}
	if err := candidate.Abort(t.Context()); err != nil {
		t.Fatalf("published Abort() = %v", err)
	}
	if err := candidate.Runtime().Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.childRuntimeBase(t.Context(), request.Locator.SessionID); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("missing known session base error = %v", err)
	}
	if err := manager.FenceSessionRuntimeBase(request.Locator.SessionID); err != nil {
		t.Fatal(err)
	}
	recoveryRequest := request
	recoveryRequest.RecoveryFence = true
	recoveryCandidate, err := manager.BuildUnpublishedSessionRuntime(t.Context(), recoveryRequest)
	if err != nil {
		t.Fatal(err)
	}
	if err := recoveryCandidate.Abort(t.Context()); err != nil {
		t.Fatal(err)
	}
	restored, err := manager.RuntimeForSession(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.childRuntimeBase(t.Context(), request.Locator.SessionID); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("cold recovery fence cleared before slot proof: %v", err)
	}
	if err := manager.CompleteSessionRuntimeRecovery(request.Locator.SessionID, restored.RuntimeBaseToken); err != nil {
		t.Fatal(err)
	}
	if base, err := manager.childRuntimeBase(t.Context(), request.Locator.SessionID); err != nil || base.runtime != restored {
		t.Fatalf("cold recovery completed base = %p, %v", base.runtime, err)
	}
	if err := restored.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestHostedReplacementTransfersPublishedBaseBeforePredecessorClose(t *testing.T) {
	service := adksession.InMemoryService()
	replacementAgent := mustProductTestAgent(t, "hosted-replacement")
	replacement, err := agentproduct.New(replacementAgent, nil)
	if err != nil {
		t.Fatal(err)
	}
	control := &productTestControl{replacement: replacement}
	incumbentAgent := mustProductTestAgent(t, "hosted-incumbent")
	product, err := agentproduct.New(incumbentAgent, control)
	if err != nil {
		t.Fatal(err)
	}
	builder := &Builder{factory: &productAwareFactory{product: product}, sessionSvc: service}
	var capabilityCloses int
	binder := &recordingCapabilityBinder{binding: SessionCapabilityBinding{
		SnapshotID: "snapshot-hosted", Skills: SkillMetadataProjection{Snapshot: "snapshot-hosted"},
		Close: func() error { capabilityCloses++; return nil },
	}}
	manager := NewRuntimeManager(RuntimeManagerParams{
		Builder: builder, BaldaProviderID: "provider-hosted", WorkingDir: t.TempDir(), StateDir: t.TempDir(),
		CapabilityBinder: binder, MCPRegistry: mcpregistry.New(nil), Logger: zerolog.Nop(),
	})
	request := SessionRuntimeRequest{Locator: deliverycmd.Locator{SessionID: "session-hosted", ChannelType: "telegram"}}
	incumbent, err := manager.RuntimeForSession(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.Create(t.Context(), &adksession.CreateRequest{AppName: incumbent.AppName, UserID: "user", SessionID: "runtime"})
	if err != nil {
		t.Fatal(err)
	}
	change := incumbent.ChangeModel(t.Context(), SessionIdentity{
		SessionService: service, AppName: incumbent.AppName, UserID: "user", SessionID: created.Session.ID(),
	}, "model-hosted")
	if change.Candidate == nil {
		t.Fatalf("ChangeModel() = %+v", change)
	}
	if change.Candidate.RuntimeBaseToken != incumbent.RuntimeBaseToken || change.Candidate.ProviderID != incumbent.ProviderID {
		t.Fatalf("replacement base/provider = %d/%q, incumbent %d/%q", change.Candidate.RuntimeBaseToken, change.Candidate.ProviderID, incumbent.RuntimeBaseToken, incumbent.ProviderID)
	}
	if change.Candidate.ConfirmedModel != "model-hosted" {
		t.Fatalf("replacement confirmed model = %q, want model-hosted", change.Candidate.ConfirmedModel)
	}
	if err := change.Candidate.PromoteReplacementBase(); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.childRuntimeBase(t.Context(), request.Locator.SessionID); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("child lookup before publication proof = %v, want unavailable", err)
	}
	if err := incumbent.Close(); err != nil {
		t.Fatal(err)
	}
	if capabilityCloses != 0 {
		t.Fatalf("predecessor released transferred capabilities %d times", capabilityCloses)
	}
	if err := change.Candidate.Close(); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("candidate close before publication proof = %v, want unavailable", err)
	}
	if capabilityCloses != 0 {
		t.Fatalf("candidate close before proof released capabilities %d times", capabilityCloses)
	}
	if err := change.Candidate.CompleteReplacementBase(); err != nil {
		t.Fatal(err)
	}
	base, err := manager.childRuntimeBase(t.Context(), request.Locator.SessionID)
	if err != nil || base.runtime != change.Candidate || base.token != RuntimeBaseToken(change.Candidate.RuntimeBaseToken) {
		t.Fatalf("hosted child base = %p/%d, %v", base.runtime, base.token, err)
	}
	fresh, err := manager.BuildUnpublishedSessionRuntime(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.ExpectedBaseToken() != RuntimeBaseToken(change.Candidate.RuntimeBaseToken) {
		t.Fatalf("reset expected base = %d", fresh.ExpectedBaseToken())
	}
	if err := fresh.Abort(t.Context()); err != nil {
		t.Fatal(err)
	}
	if capabilityCloses != 1 {
		t.Fatalf("aborted reset capability closes = %d, want 1", capabilityCloses)
	}
	if err := change.Candidate.Close(); err != nil {
		t.Fatal(err)
	}
	if capabilityCloses != 2 {
		t.Fatalf("replacement capability closes = %d, want 2 total", capabilityCloses)
	}
}

func TestRuntimeForSessionBindsSkillLoaderToResolvedSnapshot(t *testing.T) {
	factory := &recordingRuntimeFactory{}
	builder := &Builder{factory: factory}
	capabilityBinder := &recordingCapabilityBinder{binding: SessionCapabilityBinding{
		SnapshotID: "snapshot-prism",
		Skills: SkillMetadataProjection{
			Snapshot: "snapshot-prism",
			Skills: []SkillPromptMetadata{{
				Source: runtimecatalogcmd.SourceID{Kind: runtimecatalogcmd.SourceKindPlugin, Name: "prism"},
				Name:   "story",
			}},
		},
	}}
	sessionBinder := &recordingSessionMCPBinder{}
	registry := mcpregistry.New(nil)
	manager := NewRuntimeManager(RuntimeManagerParams{
		Builder:          builder,
		BaldaProviderID:  "alpha",
		WorkingDir:       t.TempDir(),
		StateDir:         t.TempDir(),
		SessionMCPBinder: sessionBinder,
		CapabilityBinder: capabilityBinder,
		MCPRegistry:      registry,
		Logger:           zerolog.Nop(),
	})

	runtime, err := manager.RuntimeForSession(context.Background(), SessionRuntimeRequest{
		Locator: deliverycmd.Locator{SessionID: "session-1", ChannelType: "telegram"},
	})
	if err != nil {
		t.Fatalf("RuntimeForSession() error = %v", err)
	}
	if len(sessionBinder.requests) != 1 {
		t.Fatalf("session MCP bind requests = %d, want 1", len(sessionBinder.requests))
	}
	if got := sessionBinder.requests[0].RuntimeSnapshotID; got != "snapshot-prism" {
		t.Fatalf("session MCP snapshot = %q, want snapshot-prism", got)
	}
	if _, ok := registry.Get("session-skill-mcp"); !ok {
		t.Fatal("scoped skill MCP server is not registered")
	}
	if len(factory.requests) != 1 || !slices.Contains(factory.requests[0].MCPServerIDs, "session-skill-mcp") {
		t.Fatalf("provider MCP servers = %+v, want session-skill-mcp", factory.requests)
	}
	if err := runtime.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if _, ok := registry.Get("session-skill-mcp"); ok {
		t.Fatal("scoped skill MCP server remains registered after close")
	}
	if sessionBinder.releaseCalls != 1 {
		t.Fatalf("session MCP release calls = %d, want 1", sessionBinder.releaseCalls)
	}
}

func (b *recordingCapabilityBinder) BindSessionCapabilities(_ context.Context, _ string, request SessionRuntimeRequest) (SessionCapabilityBinding, error) {
	b.requests = append(b.requests, request)
	return b.binding, b.err
}

func TestRuntimeForSessionOwnsCapabilityBinding(t *testing.T) {
	providers := map[string]agentconfig.Config{"alpha": {
		Type:   agentconfig.AgentTypeOpenAI,
		OpenAI: &agentconfig.LocalAPIConfig{APIKey: "test-key", Model: "gpt-4o-mini"},
	}}
	registry := mcpregistry.New(map[string]agentconfig.MCPServerConfig{
		"balda": {Type: agentconfig.MCPServerTypeHTTP, URL: "http://127.0.0.1:1/mcp"},
	})
	builder := &Builder{
		factory:  agentfactory.New(providers, registry),
		normaCfg: runtimeconfig.RuntimeConfig{Providers: providers},
	}
	closeCalls := 0
	binder := &recordingCapabilityBinder{binding: SessionCapabilityBinding{
		SnapshotID: "snapshot-1",
		Skills:     SkillMetadataProjection{Snapshot: "snapshot-1"},
		Close: func() error {
			closeCalls++
			return nil
		},
	}}
	manager := NewRuntimeManager(RuntimeManagerParams{
		Builder:          builder,
		BaldaProviderID:  "alpha",
		WorkingDir:       t.TempDir(),
		StateDir:         t.TempDir(),
		CapabilityBinder: binder,
		MCPRegistry:      registry,
		Logger:           zerolog.Nop(),
	})

	runtime, err := manager.RuntimeForSession(context.Background(), SessionRuntimeRequest{RuntimeSnapshotID: "snapshot-1"})
	if err != nil {
		t.Fatalf("RuntimeForSession() error = %v", err)
	}
	if runtime.RuntimeSnapshotID != "snapshot-1" {
		t.Fatalf("runtime snapshot = %q, want snapshot-1", runtime.RuntimeSnapshotID)
	}
	if len(binder.requests) != 1 || binder.requests[0].RuntimeSnapshotID != "snapshot-1" {
		t.Fatalf("capability requests = %+v, want persisted snapshot", binder.requests)
	}
	if err := runtime.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := runtime.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	if closeCalls != 1 {
		t.Fatalf("capability close calls = %d, want 1", closeCalls)
	}
}

func TestRuntimeForSessionClosesLayersInReverseAcquisitionOrder(t *testing.T) {
	var order []string
	factory := &orderedProductFactory{order: &order}
	builder := &Builder{factory: &recordingRuntimeFactory{}, scopedFactory: factory}
	binder := &recordingCapabilityBinder{binding: SessionCapabilityBinding{
		SnapshotID: "snapshot-ordered",
		Skills:     SkillMetadataProjection{Snapshot: "snapshot-ordered"},
		Close: func() error {
			order = append(order, "capabilities")
			return nil
		},
	}}
	manager := NewRuntimeManager(RuntimeManagerParams{
		Builder:          builder,
		BaldaProviderID:  "alpha",
		WorkingDir:       t.TempDir(),
		StateDir:         t.TempDir(),
		SessionMCPBinder: &orderedSessionMCPBinder{order: &order},
		CapabilityBinder: binder,
		MCPRegistry:      mcpregistry.New(nil),
		Logger:           zerolog.Nop(),
	})

	runtime, err := manager.RuntimeForSession(t.Context(), SessionRuntimeRequest{
		Locator: deliverycmd.Locator{SessionID: "session-ordered", ChannelType: "telegram"},
	})
	if err != nil {
		t.Fatalf("RuntimeForSession() error = %v", err)
	}
	if err := runtime.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	want := []string{"product", "binding", "capabilities"}
	if !slices.Equal(order, want) {
		t.Fatalf("close order = %v, want %v", order, want)
	}
}

func TestRuntimeManagerCloseReturnsStableFailureForConcurrentCallers(t *testing.T) {
	t.Run("app runtime", func(t *testing.T) {
		privateErr := errors.New("private provider close detail")
		capabilityDetail := errors.New("private capability cleanup detail")
		capabilityCalls := 0
		ag := mustProductTestAgent(t, "app-close")
		ag.closeErr = privateErr
		product, err := agentproduct.New(ag, nil)
		if err != nil {
			t.Fatalf("agentproduct.New() error = %v", err)
		}
		manager := NewRuntimeManager(RuntimeManagerParams{
			Builder:         &Builder{factory: &productAwareFactory{product: product}},
			BaldaProviderID: "alpha",
			WorkingDir:      t.TempDir(),
			StateDir:        t.TempDir(),
			CapabilityBinder: &recordingCapabilityBinder{binding: SessionCapabilityBinding{
				SnapshotID: "snapshot-app-close",
				Skills:     SkillMetadataProjection{Snapshot: "snapshot-app-close"},
				Close: func() error {
					capabilityCalls++
					return capabilityDetail
				},
			}},
			MCPRegistry: mcpregistry.New(nil),
			Logger:      zerolog.Nop(),
		})
		runtime, err := manager.Runtime(t.Context())
		if err != nil {
			t.Fatalf("Runtime() error = %v", err)
		}
		assertConcurrentCloseResult(t, runtime,
			[]error{agentproduct.ErrCloseFailed, ErrSessionCapabilityCleanup},
			[]string{privateErr.Error(), capabilityDetail.Error()},
		)
		if ag.closeCalls != 1 {
			t.Fatalf("product close calls = %d, want 1", ag.closeCalls)
		}
		if capabilityCalls != 1 {
			t.Fatalf("capability close calls = %d, want 1", capabilityCalls)
		}
	})

	t.Run("session runtime", func(t *testing.T) {
		var order []string
		bindingDetail := errors.New("private binding cleanup detail")
		capabilityDetail := errors.New("private capability cleanup detail")
		factory := &orderedProductFactory{order: &order, closeErr: ErrRuntimeProductClose}
		binder := &recordingCapabilityBinder{binding: SessionCapabilityBinding{
			SnapshotID: "snapshot-close",
			Skills:     SkillMetadataProjection{Snapshot: "snapshot-close"},
			Close: func() error {
				order = append(order, "capabilities")
				return capabilityDetail
			},
		}}
		manager := NewRuntimeManager(RuntimeManagerParams{
			Builder:          &Builder{factory: &recordingRuntimeFactory{}, scopedFactory: factory},
			BaldaProviderID:  "alpha",
			WorkingDir:       t.TempDir(),
			StateDir:         t.TempDir(),
			SessionMCPBinder: &orderedSessionMCPBinder{order: &order, releaseErr: bindingDetail},
			CapabilityBinder: binder,
			MCPRegistry:      mcpregistry.New(nil),
			Logger:           zerolog.Nop(),
		})
		runtime, err := manager.RuntimeForSession(t.Context(), SessionRuntimeRequest{
			Locator: deliverycmd.Locator{SessionID: "session-close", ChannelType: "telegram"},
		})
		if err != nil {
			t.Fatalf("RuntimeForSession() error = %v", err)
		}
		assertConcurrentCloseResult(t, runtime,
			[]error{ErrRuntimeProductClose, ErrSessionBindingCleanup, ErrSessionCapabilityCleanup},
			[]string{bindingDetail.Error(), capabilityDetail.Error()},
		)
		wantOrder := []string{"product", "binding", "capabilities"}
		if !slices.Equal(order, wantOrder) {
			t.Fatalf("close order = %v, want %v", order, wantOrder)
		}
	})
}

func assertConcurrentCloseResult(t *testing.T, runtime *BuiltRuntime, wants []error, privateDetails []string) {
	t.Helper()
	const callers = 16
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- runtime.Close()
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		for _, want := range wants {
			if !errors.Is(err, want) {
				t.Errorf("Close() error = %v, want errors.Is(%v)", err, want)
			}
		}
		for _, privateDetail := range privateDetails {
			if strings.Contains(err.Error(), privateDetail) {
				t.Errorf("Close() leaked private cleanup detail: %v", err)
			}
		}
	}
	repeatedErr := runtime.Close()
	for _, want := range wants {
		if !errors.Is(repeatedErr, want) {
			t.Errorf("repeated Close() error = %v, want errors.Is(%v)", repeatedErr, want)
		}
	}
}

func TestRuntimeManagerConstructionFailureBoundsOuterCleanup(t *testing.T) {
	t.Run("partial capability binding", func(t *testing.T) {
		primaryErr := errors.New("capability bind failed")
		privateDetail := errors.New("private capability cleanup detail")
		closeCalls := 0
		binder := &recordingCapabilityBinder{
			err: primaryErr,
			binding: SessionCapabilityBinding{Close: func() error {
				closeCalls++
				return privateDetail
			}},
		}
		manager := NewRuntimeManager(RuntimeManagerParams{
			Builder:          &Builder{factory: &recordingRuntimeFactory{}},
			BaldaProviderID:  "alpha",
			WorkingDir:       t.TempDir(),
			StateDir:         t.TempDir(),
			CapabilityBinder: binder,
			MCPRegistry:      mcpregistry.New(nil),
			Logger:           zerolog.Nop(),
		})
		_, err := manager.Runtime(t.Context())
		if !errors.Is(err, primaryErr) || !errors.Is(err, ErrSessionCapabilityCleanup) {
			t.Fatalf("Runtime() error = %v, want primary and bounded capability cleanup", err)
		}
		if strings.Contains(err.Error(), privateDetail.Error()) {
			t.Fatalf("Runtime() leaked capability cleanup detail: %v", err)
		}
		if closeCalls != 1 {
			t.Fatalf("capability close calls = %d, want 1", closeCalls)
		}
	})

	t.Run("partial session binding", func(t *testing.T) {
		primaryErr := errors.New("session binding failed")
		bindingDetail := errors.New("private binding cleanup detail")
		capabilityDetail := errors.New("private capability cleanup detail")
		var order []string
		capabilities := &recordingCapabilityBinder{binding: SessionCapabilityBinding{
			SnapshotID: "snapshot-partial",
			Skills:     SkillMetadataProjection{Snapshot: "snapshot-partial"},
			Close: func() error {
				order = append(order, "capabilities")
				return capabilityDetail
			},
		}}
		binding := ScopedMCPServer{
			ID: "partial-binding",
			Config: agentconfig.MCPServerConfig{
				Type: agentconfig.MCPServerTypeHTTP,
				URL:  "http://127.0.0.1:1/mcp/partial",
			},
			Release: func() error {
				order = append(order, "binding")
				return bindingDetail
			},
		}
		manager := NewRuntimeManager(RuntimeManagerParams{
			Builder:          &Builder{factory: &recordingRuntimeFactory{}},
			BaldaProviderID:  "alpha",
			WorkingDir:       t.TempDir(),
			StateDir:         t.TempDir(),
			SessionMCPBinder: &partialSessionMCPBinder{binding: binding, err: primaryErr},
			CapabilityBinder: capabilities,
			MCPRegistry:      mcpregistry.New(nil),
			Logger:           zerolog.Nop(),
		})
		_, err := manager.RuntimeForSession(t.Context(), SessionRuntimeRequest{
			Locator: deliverycmd.Locator{SessionID: "session-partial", ChannelType: "telegram"},
		})
		for _, want := range []error{primaryErr, ErrSessionBindingCleanup, ErrSessionCapabilityCleanup} {
			if !errors.Is(err, want) {
				t.Fatalf("RuntimeForSession() error = %v, want errors.Is(%v)", err, want)
			}
		}
		for _, privateDetail := range []error{bindingDetail, capabilityDetail} {
			if strings.Contains(err.Error(), privateDetail.Error()) {
				t.Fatalf("RuntimeForSession() leaked cleanup detail %q: %v", privateDetail, err)
			}
		}
		wantOrder := []string{"binding", "capabilities"}
		if !slices.Equal(order, wantOrder) {
			t.Fatalf("cleanup order = %v, want %v", order, wantOrder)
		}
	})
}

func TestRuntimeForSessionRejectsIncompleteCapabilityBinding(t *testing.T) {
	closeCalls := 0
	binder := &recordingCapabilityBinder{binding: SessionCapabilityBinding{
		SnapshotID: "snapshot-1",
		Skills:     SkillMetadataProjection{Snapshot: runtimecatalogcmd.SnapshotID("snapshot-other")},
		Close: func() error {
			closeCalls++
			return nil
		},
	}}
	manager := &RuntimeManager{capabilityBinder: binder}

	_, err := manager.RuntimeForSession(context.Background(), SessionRuntimeRequest{})
	if err == nil || !strings.Contains(err.Error(), "binding is incomplete") {
		t.Fatalf("RuntimeForSession() error = %v, want incomplete binding", err)
	}
	if closeCalls != 1 {
		t.Fatalf("capability close calls = %d, want 1", closeCalls)
	}
}

func TestRuntimeForSessionReleasesCapabilitiesOnConstructionFailure(t *testing.T) {
	closeCalls := 0
	binder := &recordingCapabilityBinder{binding: SessionCapabilityBinding{
		SnapshotID: "snapshot-1",
		Skills:     SkillMetadataProjection{Snapshot: "snapshot-1"},
		Close: func() error {
			closeCalls++
			return nil
		},
	}}
	manager := &RuntimeManager{capabilityBinder: binder}

	_, err := manager.RuntimeForSession(context.Background(), SessionRuntimeRequest{})
	if err == nil || !strings.Contains(err.Error(), "agent builder is required") {
		t.Fatalf("RuntimeForSession() error = %v, want missing builder", err)
	}
	if closeCalls != 1 {
		t.Fatalf("capability close calls = %d, want 1", closeCalls)
	}
}

type closeableRuntimeAgent struct {
	closeErr error
}

func (a *closeableRuntimeAgent) Close() error {
	return a.closeErr
}

func TestCloseRuntimeAgent_IgnoresExpectedShutdownError(t *testing.T) {
	agent := &closeableRuntimeAgent{closeErr: fmt.Errorf("close acp client: acp client close: context canceled")}

	if err := closeRuntimeAgent(agent); err != nil {
		t.Fatalf("closeRuntimeAgent() error = %v, want nil", err)
	}
}

func TestCloseRuntimeAgent_ReturnsUnexpectedCloseError(t *testing.T) {
	agent := &closeableRuntimeAgent{closeErr: fmt.Errorf("close failed")}

	err := closeRuntimeAgent(agent)
	if err == nil {
		t.Fatal("closeRuntimeAgent() error = nil, want non-nil")
	}
	if !strings.Contains(err.Error(), "close balda runtime agent: close failed") {
		t.Fatalf("closeRuntimeAgent() error = %v, want wrapped close failure", err)
	}
}

func TestCloseRuntimeAgent_IgnoresWrappedContextCanceled(t *testing.T) {
	agent := &closeableRuntimeAgent{closeErr: fmt.Errorf("close failed: %w", context.Canceled)}

	if err := closeRuntimeAgent(agent); err != nil {
		t.Fatalf("closeRuntimeAgent() error = %v, want nil", err)
	}
}
