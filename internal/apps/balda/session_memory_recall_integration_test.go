package balda

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	baldaslack "github.com/normahq/balda/internal/apps/balda/channel/slack"
	baldaslackagent "github.com/normahq/balda/internal/apps/balda/channel/slackagent"
	baldatelegram "github.com/normahq/balda/internal/apps/balda/channel/telegram"
	baldazulip "github.com/normahq/balda/internal/apps/balda/channel/zulip"
	"github.com/normahq/balda/internal/apps/balda/deliverycmd"
	"github.com/normahq/balda/internal/apps/balda/memory"
	"github.com/normahq/balda/internal/apps/balda/sessionmemoryapp"
	"github.com/normahq/balda/internal/apps/balda/sessionmemorymcp"
	"github.com/normahq/balda/sessionmemory"
	"github.com/normahq/balda/sessionmemory/sessionmemorytest"
)

func TestRestoredSessionMemoryRecallUsesFreshAuthenticatedBinding(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	stateDir := t.TempDir()
	locator := baldatelegram.NewLocator(7123, 0)
	resolver := restoredSessionMemoryScopeResolver()
	scope, err := resolver.Resolve(locator)
	if err != nil {
		t.Fatalf("ScopeResolver.Resolve() error = %v", err)
	}
	current := restoredSessionMemoryCurrentSession(locator)

	stateA, err := openBaldaStateProvider(ctx, stateDir)
	if err != nil {
		t.Fatalf("openBaldaStateProvider(epoch A) error = %v", err)
	}
	t.Cleanup(func() { _ = stateA.Close() })
	wantFacts, err := memory.NewStore(stateA.AppKV(), "", true).Remember(ctx, "global fact stays outside restored session recall")
	if err != nil {
		t.Fatalf("global memory Remember() error = %v", err)
	}

	models := sessionmemorytest.NewModels()
	models.SetAtoms([]sessionmemory.AtomCandidate{{
		Category: sessionmemory.AtomCategoryFact,
		Text:     "restored session sentinel",
		Relation: sessionmemory.CandidateRelationNew,
	}}, nil)
	engine, err := sessionmemory.NewEngine(stateA.SessionMemoryStore(), models, models, models, sessionmemory.Config{})
	if err != nil {
		t.Fatalf("sessionmemory.NewEngine() error = %v", err)
	}
	turn, err := sessionmemory.NewTurn(
		scope,
		current.Session,
		"restored-recall-turn-1",
		time.Date(2026, 8, 4, 8, 30, 0, 0, time.UTC),
		"remember the restored session sentinel",
		"the restored session sentinel is stored",
	)
	if err != nil {
		t.Fatalf("sessionmemory.NewTurn() error = %v", err)
	}
	if _, err := engine.ProcessTurn(ctx, turn); err != nil {
		t.Fatalf("Engine.ProcessTurn() error = %v", err)
	}
	snapshotA, err := stateA.SessionMemoryStore().LoadScope(ctx, scope)
	if err != nil {
		t.Fatalf("SessionMemoryStore.LoadScope(epoch A) error = %v", err)
	}
	if len(snapshotA.Atoms) != 1 || len(snapshotA.Sources) != 1 {
		t.Fatalf("epoch A persisted counts = atoms %d sources %d, want 1 each", len(snapshotA.Atoms), len(snapshotA.Sources))
	}
	wantRevision := sessionmemory.RevisionRef{
		ItemID:     snapshotA.Atoms[0].Meta.ItemID,
		RevisionID: snapshotA.Atoms[0].Meta.RevisionID,
	}
	wantSource := snapshotA.Sources[0].Ref

	epochAInvoker := &recallOnlyInvoker{}
	epochAProvider := newRecallOnlyNativeProvider(t, stateA.SessionMemoryStore(), epochAInvoker)
	epochAServer := newRestoredSessionMemoryMCPServer(t, epochAProvider, resolver)
	originalBinding, err := epochAServer.broker.Bind(current)
	if err != nil {
		t.Fatalf("ContextBroker.Bind(epoch A) error = %v", err)
	}
	if err := originalBinding.Release(); err != nil {
		t.Fatalf("ContextBinding.Release(epoch A) error = %v", err)
	}
	assertUnauthorizedSessionMemoryBinding(t, ctx, epochAServer.httpServer.Client(), originalBinding.URL)
	epochAServer.httpServer.Close()
	if err := epochAProvider.Close(ctx); err != nil {
		t.Fatalf("NativeProvider.Close(epoch A) error = %v", err)
	}
	if calls := epochAInvoker.calls.Load(); calls != 0 {
		t.Fatalf("epoch A derivation calls = %d, want 0", calls)
	}
	if err := stateA.Close(); err != nil {
		t.Fatalf("state provider Close(epoch A) error = %v", err)
	}

	stateB, err := openBaldaStateProvider(ctx, stateDir)
	if err != nil {
		t.Fatalf("openBaldaStateProvider(epoch B) error = %v", err)
	}
	t.Cleanup(func() { _ = stateB.Close() })
	epochBInvoker := &recallOnlyInvoker{}
	epochBProvider := newRecallOnlyNativeProvider(t, stateB.SessionMemoryStore(), epochBInvoker)
	t.Cleanup(func() { _ = epochBProvider.Close(context.Background()) })
	epochBServer := newRestoredSessionMemoryMCPServer(t, epochBProvider, resolver)
	restoredBinding, err := epochBServer.broker.Bind(current)
	if err != nil {
		t.Fatalf("ContextBroker.Bind(epoch B) error = %v", err)
	}
	t.Cleanup(func() { _ = restoredBinding.Release() })
	clientSession := connectRestoredSessionMemoryClient(t, ctx, epochBServer.httpServer.Client(), restoredBinding.URL)
	defer func() { _ = clientSession.Close() }()

	searchResult, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
		Name: sessionmemorymcp.ToolName,
		Arguments: map[string]any{
			"query": "restored session sentinel",
			"limit": 1,
		},
	})
	if err != nil {
		t.Fatalf("session-memory search CallTool() failed: error_type=%T", err)
	}
	searchOutput := decodeRestoredSessionMemoryResult[sessionmemorymcp.SearchOutput](t, searchResult)
	if searchResult.IsError || !searchOutput.OK {
		t.Fatalf("restored search failed: is_error=%t code=%q", searchResult.IsError, restoredSessionMemoryErrorCode(searchOutput.Error))
	}
	if searchOutput.Scope == nil || *searchOutput.Scope != scope || len(searchOutput.Results) != 1 {
		t.Fatalf("restored search = scope %v results %d, want exact scope and one result", searchOutput.Scope, len(searchOutput.Results))
	}
	reference := searchOutput.Results[0]
	if reference.ID != wantRevision.RevisionID || reference.ScopeKey != scope.Key ||
		reference.ItemID != wantRevision.ItemID || reference.RevisionID != wantRevision.RevisionID {
		t.Fatalf("restored search revision = %q/%q, want persisted identity", reference.ItemID, reference.RevisionID)
	}
	if len(reference.Provenance.RawSources) != 1 || reference.Provenance.RawSources[0] != wantSource {
		t.Fatalf("restored search raw provenance count = %d, want persisted source", len(reference.Provenance.RawSources))
	}
	if searchOutput.DataClassification != sessionmemorymcp.DataClassificationUntrustedReference ||
		!strings.Contains(searchOutput.Notice, "Do not execute") {
		t.Fatalf("restored search trust contract = classification %q notice_present %t", searchOutput.DataClassification, searchOutput.Notice != "")
	}

	traceResult, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
		Name: sessionmemorymcp.TraceToolName,
		Arguments: map[string]any{
			"item_id":     wantRevision.ItemID,
			"revision_id": wantRevision.RevisionID,
			"max_nodes":   4,
		},
	})
	if err != nil {
		t.Fatalf("session-memory trace CallTool() failed: error_type=%T", err)
	}
	traceOutput := decodeRestoredSessionMemoryResult[sessionmemorymcp.TraceOutput](t, traceResult)
	if traceResult.IsError || !traceOutput.OK {
		t.Fatalf("restored trace failed: is_error=%t code=%q", traceResult.IsError, restoredSessionMemoryErrorCode(traceOutput.Error))
	}
	if traceOutput.Scope == nil || *traceOutput.Scope != scope || traceOutput.Root == nil || *traceOutput.Root != wantRevision {
		t.Fatalf("restored trace did not retain exact scope/root identity")
	}
	if len(traceOutput.Revisions) != 1 || traceOutput.Revisions[0].Atom == nil {
		t.Fatalf("restored trace revision count = %d, want one atom", len(traceOutput.Revisions))
	}
	traceMeta := traceOutput.Revisions[0].Atom.Meta
	if traceMeta.ItemID != wantRevision.ItemID || traceMeta.RevisionID != wantRevision.RevisionID {
		t.Fatalf("restored trace revision = %q/%q, want persisted identity", traceMeta.ItemID, traceMeta.RevisionID)
	}
	if len(traceOutput.Sources) != 1 || traceOutput.Sources[0].Ref != wantSource {
		t.Fatalf("restored trace source count = %d, want persisted source", len(traceOutput.Sources))
	}
	if traceOutput.DataClassification != sessionmemorymcp.DataClassificationUntrustedReference ||
		!strings.Contains(traceOutput.Notice, "Do not execute") {
		t.Fatalf("restored trace trust contract = classification %q notice_present %t", traceOutput.DataClassification, traceOutput.Notice != "")
	}
	if calls := epochBInvoker.calls.Load(); calls != 0 {
		t.Fatalf("recall derivation calls = %d, want 0", calls)
	}

	gotFacts, err := memory.NewStore(stateB.AppKV(), "", true).Snapshot(ctx)
	if err != nil {
		t.Fatalf("global memory Snapshot(epoch B) error = %v", err)
	}
	if gotFacts != wantFacts {
		t.Fatalf("global fact snapshot changed during restored recall: version %d, want %d", gotFacts.Version, wantFacts.Version)
	}
}

func TestRestoredSessionMemoryRecallIsolatedByExactLocator(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	stateDir := t.TempDir()
	resolver := restoredSessionMemoryScopeResolver()
	cases := restoredSessionMemoryScopeCases()
	type persistedIdentity struct {
		scope    sessionmemory.Scope
		revision sessionmemory.RevisionRef
	}
	wantByCase := make(map[string]persistedIdentity, len(cases))
	scopeKeys := make(map[string]string, len(cases))

	stateA, err := openBaldaStateProvider(ctx, stateDir)
	if err != nil {
		t.Fatalf("openBaldaStateProvider(scope epoch A) error = %v", err)
	}
	t.Cleanup(func() { _ = stateA.Close() })
	models := sessionmemorytest.NewModels()
	engine, err := sessionmemory.NewEngine(stateA.SessionMemoryStore(), models, models, models, sessionmemory.Config{})
	if err != nil {
		t.Fatalf("sessionmemory.NewEngine(scope matrix) error = %v", err)
	}
	for index, test := range cases {
		scope, err := resolver.Resolve(test.locator)
		if err != nil {
			t.Fatalf("case %q scope resolution failed", test.name)
		}
		if scope.Kind != test.wantKind {
			t.Fatalf("case %q scope kind = %q, want %q", test.name, scope.Kind, test.wantKind)
		}
		if prior, duplicate := scopeKeys[scope.Key]; duplicate {
			t.Fatalf("case %q duplicates scope key from case %q", test.name, prior)
		}
		scopeKeys[scope.Key] = test.name

		models.SetAtoms([]sessionmemory.AtomCandidate{{
			Category: sessionmemory.AtomCategoryFact,
			Text:     fmt.Sprintf("scope sentinel %02d", index+1),
			Relation: sessionmemory.CandidateRelationNew,
		}}, nil)
		turn, err := sessionmemory.NewTurn(
			scope,
			restoredSessionMemoryCurrentSession(test.locator).Session,
			fmt.Sprintf("scope-turn-%02d", index+1),
			time.Date(2026, 8, 4, 9, 0, index, 0, time.UTC),
			"remember the scope sentinel",
			fmt.Sprintf("scope sentinel %02d stored", index+1),
		)
		if err != nil {
			t.Fatalf("case %q NewTurn() failed", test.name)
		}
		if _, err := engine.ProcessTurn(ctx, turn); err != nil {
			t.Fatalf("case %q ProcessTurn() failed: error_type=%T", test.name, err)
		}
		snapshot, err := stateA.SessionMemoryStore().LoadScope(ctx, scope)
		if err != nil {
			t.Fatalf("case %q LoadScope() failed: error_type=%T", test.name, err)
		}
		if len(snapshot.Atoms) != 1 || len(snapshot.Sources) != 1 {
			t.Fatalf("case %q persisted counts = atoms %d sources %d, want 1 each", test.name, len(snapshot.Atoms), len(snapshot.Sources))
		}
		wantByCase[test.name] = persistedIdentity{
			scope: scope,
			revision: sessionmemory.RevisionRef{
				ItemID:     snapshot.Atoms[0].Meta.ItemID,
				RevisionID: snapshot.Atoms[0].Meta.RevisionID,
			},
		}
	}
	if err := stateA.Close(); err != nil {
		t.Fatalf("state provider Close(scope epoch A) error = %v", err)
	}

	stateB, err := openBaldaStateProvider(ctx, stateDir)
	if err != nil {
		t.Fatalf("openBaldaStateProvider(scope epoch B) error = %v", err)
	}
	t.Cleanup(func() { _ = stateB.Close() })
	invoker := &recallOnlyInvoker{}
	provider := newRecallOnlyNativeProvider(t, stateB.SessionMemoryStore(), invoker)
	t.Cleanup(func() { _ = provider.Close(context.Background()) })
	server := newRestoredSessionMemoryMCPServer(t, provider, resolver)

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			want := wantByCase[test.name]
			binding, err := server.broker.Bind(restoredSessionMemoryCurrentSession(test.locator))
			if err != nil {
				t.Fatalf("ContextBroker.Bind() failed: error_type=%T", err)
			}
			defer func() { _ = binding.Release() }()
			clientSession := connectRestoredSessionMemoryClient(t, ctx, server.httpServer.Client(), binding.URL)
			defer func() { _ = clientSession.Close() }()

			output := callRestoredSessionMemorySearch(t, ctx, clientSession, "scope sentinel", 2)
			if output.Scope == nil || *output.Scope != want.scope || len(output.Results) != 1 {
				t.Fatalf("scope %q recall = result_count %d exact_scope %t, want one exact result", want.scope.Key, len(output.Results), output.Scope != nil && *output.Scope == want.scope)
			}
			got := output.Results[0]
			if got.ScopeKey != want.scope.Key || got.ItemID != want.revision.ItemID || got.RevisionID != want.revision.RevisionID {
				t.Fatalf("scope %q recall returned a foreign or unexpected identity", want.scope.Key)
			}
		})
	}

	primary := cases[0]
	wantPrimary := wantByCase[primary.name]
	t.Run("missing capability", func(t *testing.T) {
		clientSession := connectRestoredSessionMemoryClient(t, ctx, server.httpServer.Client(), server.httpServer.URL)
		defer func() { _ = clientSession.Close() }()
		result, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
			Name:      sessionmemorymcp.ToolName,
			Arguments: map[string]any{"query": "scope sentinel", "limit": 1},
		})
		if err != nil {
			t.Fatalf("missing-capability CallTool() failed: error_type=%T", err)
		}
		output := decodeRestoredSessionMemoryResult[sessionmemorymcp.SearchOutput](t, result)
		if !result.IsError || output.OK || restoredSessionMemoryErrorCode(output.Error) != string(sessionmemory.CodeInvalidScope) || len(output.Results) != 0 {
			t.Fatalf("missing-capability outcome = is_error %t ok %t code %q results %d", result.IsError, output.OK, restoredSessionMemoryErrorCode(output.Error), len(output.Results))
		}
	})

	t.Run("unknown and released capabilities", func(t *testing.T) {
		unknownProbe, err := server.broker.Bind(restoredSessionMemoryCurrentSession(primary.locator))
		if err != nil {
			t.Fatalf("ContextBroker.Bind(unknown probe) failed: error_type=%T", err)
		}
		assertUnauthorizedSessionMemoryBinding(t, ctx, server.httpServer.Client(), unknownProbe.URL+"x")
		if err := unknownProbe.Release(); err != nil {
			t.Fatalf("ContextBinding.Release(unknown probe) failed: error_type=%T", err)
		}

		released, err := server.broker.Bind(restoredSessionMemoryCurrentSession(primary.locator))
		if err != nil {
			t.Fatalf("ContextBroker.Bind(released probe) failed: error_type=%T", err)
		}
		if err := released.Release(); err != nil {
			t.Fatalf("ContextBinding.Release(released probe) failed: error_type=%T", err)
		}
		assertUnauthorizedSessionMemoryBinding(t, ctx, server.httpServer.Client(), released.URL)
	})

	t.Run("caller identity headers are overwritten", func(t *testing.T) {
		binding, err := server.broker.Bind(restoredSessionMemoryCurrentSession(primary.locator))
		if err != nil {
			t.Fatalf("ContextBroker.Bind(header overwrite) failed: error_type=%T", err)
		}
		defer func() { _ = binding.Release() }()
		baseClient := server.httpServer.Client()
		tamperedClient := *baseClient
		tamperedClient.Transport = sessionMemoryHeaderTransport{
			base: baseClient.Transport,
			headers: http.Header{
				sessionmemorymcp.HeaderSessionLocator: []string{`{"channel_type":"telegram","address_key":"-999:0","address_json":"{}","session_id":"attacker"}`},
				sessionmemorymcp.HeaderSessionID:      []string{"attacker-session"},
				sessionmemorymcp.HeaderAgentSessionID: []string{"attacker-agent"},
				sessionmemorymcp.HeaderLineageID:      []string{"attacker-lineage"},
				sessionmemorymcp.HeaderSessionBinding: []string{"attacker-binding"},
			},
		}
		clientSession := connectRestoredSessionMemoryClient(t, ctx, &tamperedClient, binding.URL)
		defer func() { _ = clientSession.Close() }()
		output := callRestoredSessionMemorySearch(t, ctx, clientSession, "scope sentinel", 1)
		if output.Scope == nil || *output.Scope != wantPrimary.scope || len(output.Results) != 1 {
			t.Fatalf("header-overwrite recall = result_count %d exact_scope %t", len(output.Results), output.Scope != nil && *output.Scope == wantPrimary.scope)
		}
		result := output.Results[0]
		if result.ItemID != wantPrimary.revision.ItemID || result.RevisionID != wantPrimary.revision.RevisionID {
			t.Fatalf("header-overwrite recall returned an attacker-selected identity")
		}
	})

	if calls := invoker.calls.Load(); calls != 0 {
		t.Fatalf("scope isolation recall derivation calls = %d, want 0", calls)
	}
}

type restoredSessionMemoryScopeCase struct {
	name     string
	locator  deliverycmd.Locator
	wantKind sessionmemory.ScopeKind
}

func restoredSessionMemoryScopeCases() []restoredSessionMemoryScopeCase {
	return []restoredSessionMemoryScopeCase{
		{name: "telegram personal root", locator: baldatelegram.NewLocator(8101, 0), wantKind: sessionmemory.ScopeKindPersonal},
		{name: "telegram personal topic", locator: baldatelegram.NewLocator(8101, 11), wantKind: sessionmemory.ScopeKindPersonal},
		{name: "telegram group root", locator: baldatelegram.NewLocator(-8101, 0), wantKind: sessionmemory.ScopeKindGroup},
		{name: "telegram group topic", locator: baldatelegram.NewLocator(-8101, 11), wantKind: sessionmemory.ScopeKindGroup},
		{name: "slack chat personal direct", locator: baldaslack.NewDMLocator("T8101", "D8101"), wantKind: sessionmemory.ScopeKindPersonal},
		{name: "slack chat personal thread", locator: baldaslack.NewThreadLocator("T8101", "D8101", "1718101.0001"), wantKind: sessionmemory.ScopeKindPersonal},
		{name: "slack chat group thread", locator: baldaslack.NewThreadLocator("T8101", "C8101", "1718101.0002"), wantKind: sessionmemory.ScopeKindGroup},
		{name: "slack agent personal conversation", locator: baldaslackagent.NewConversationLocator("T8201", "D8201"), wantKind: sessionmemory.ScopeKindPersonal},
		{name: "slack agent personal thread", locator: baldaslackagent.NewThreadLocator("T8201", "D8201", "thread-1"), wantKind: sessionmemory.ScopeKindPersonal},
		{name: "slack agent group conversation", locator: baldaslackagent.NewConversationLocator("T8201", "C8201"), wantKind: sessionmemory.ScopeKindGroup},
		{name: "slack agent group thread", locator: baldaslackagent.NewThreadLocator("T8201", "C8201", "thread-2"), wantKind: sessionmemory.ScopeKindGroup},
		{name: "zulip personal direct", locator: baldazulip.NewDMLocator(8301), wantKind: sessionmemory.ScopeKindPersonal},
		{name: "zulip group stream topic", locator: baldazulip.NewStreamLocator(8301, "scope-isolation"), wantKind: sessionmemory.ScopeKindGroup},
	}
}

func restoredSessionMemoryScopeResolver() sessionmemoryapp.ScopeResolver {
	return sessionmemoryapp.NewScopeResolver(map[string]sessionmemoryapp.ScopeClassifier{
		baldatelegram.ChannelType:   baldatelegram.ClassifyLocatorScope,
		baldaslack.ChannelType:      baldaslack.ClassifyLocatorScope,
		baldaslackagent.ChannelType: baldaslackagent.ClassifyLocatorScope,
		baldazulip.ChannelType:      baldazulip.ClassifyLocatorScope,
	})
}

func restoredSessionMemoryCurrentSession(locator deliverycmd.Locator) sessionmemorymcp.CurrentSession {
	return sessionmemorymcp.CurrentSession{
		Locator: locator,
		Session: sessionmemory.SessionRef{
			SessionID:      locator.SessionID,
			AgentSessionID: "agent-" + locator.SessionID,
			LineageID:      "lineage-" + locator.SessionID,
		},
	}
}

type restoredSessionMemoryMCPServer struct {
	httpServer *httptest.Server
	broker     *sessionmemorymcp.ContextBroker
}

func newRestoredSessionMemoryMCPServer(
	t *testing.T,
	provider sessionmemorymcp.DerivedSearcher,
	resolver sessionmemoryapp.ScopeResolver,
) restoredSessionMemoryMCPServer {
	t.Helper()
	broker := sessionmemorymcp.NewContextBroker()
	server := mcp.NewServer(&mcp.Implementation{Name: "restored-session-memory-test", Version: "1.0.0"}, nil)
	sessionmemorymcp.RegisterTools(server, sessionmemorymcp.Config{
		Enabled:         true,
		DerivedSearcher: provider,
		SessionResolver: sessionmemorymcp.HeaderSessionResolver{Broker: broker},
		ScopeResolver:   resolver,
		Timeout:         2 * time.Second,
	})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{})
	httpServer := httptest.NewServer(broker.Wrap(handler))
	t.Cleanup(httpServer.Close)
	if err := broker.SetBaseURL(httpServer.URL); err != nil {
		t.Fatalf("ContextBroker.SetBaseURL() error = %v", err)
	}
	return restoredSessionMemoryMCPServer{httpServer: httpServer, broker: broker}
}

func connectRestoredSessionMemoryClient(
	t *testing.T,
	ctx context.Context,
	httpClient *http.Client,
	endpoint string,
) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "restored-session-memory-client", Version: "1.0.0"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:             endpoint,
		HTTPClient:           httpClient,
		MaxRetries:           -1,
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatalf("MCP client Connect() failed: error_type=%T", err)
	}
	return session
}

func callRestoredSessionMemorySearch(
	t *testing.T,
	ctx context.Context,
	clientSession *mcp.ClientSession,
	query string,
	limit int,
) sessionmemorymcp.SearchOutput {
	t.Helper()
	result, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
		Name: sessionmemorymcp.ToolName,
		Arguments: map[string]any{
			"query": query,
			"limit": limit,
		},
	})
	if err != nil {
		t.Fatalf("session-memory search CallTool() failed: error_type=%T", err)
	}
	output := decodeRestoredSessionMemoryResult[sessionmemorymcp.SearchOutput](t, result)
	if result.IsError || !output.OK {
		t.Fatalf("session-memory search failed: is_error=%t code=%q", result.IsError, restoredSessionMemoryErrorCode(output.Error))
	}
	return output
}

func newRecallOnlyNativeProvider(
	t *testing.T,
	store sessionmemory.Store,
	invoker sessionmemoryapp.StructuredInvoker,
) *sessionmemoryapp.NativeProvider {
	t.Helper()
	deriver, err := sessionmemoryapp.NewDeriver(invoker)
	if err != nil {
		t.Fatalf("sessionmemoryapp.NewDeriver() error = %v", err)
	}
	provider, err := sessionmemoryapp.NewNativeProvider(store, deriver, invoker)
	if err != nil {
		t.Fatalf("sessionmemoryapp.NewNativeProvider() error = %v", err)
	}
	return provider
}

func assertUnauthorizedSessionMemoryBinding(t *testing.T, ctx context.Context, client *http.Client, endpoint string) {
	t.Helper()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("build released-binding request failed: error_type=%T", err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("send released-binding request failed: error_type=%T", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("released binding status = %d, want %d", response.StatusCode, http.StatusUnauthorized)
	}
}

type sessionMemoryHeaderTransport struct {
	base    http.RoundTripper
	headers http.Header
}

func (t sessionMemoryHeaderTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	copyOfRequest := request.Clone(request.Context())
	copyOfRequest.Header = request.Header.Clone()
	for name, values := range t.headers {
		copyOfRequest.Header.Del(name)
		for _, value := range values {
			copyOfRequest.Header.Add(name, value)
		}
	}
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(copyOfRequest)
}

func decodeRestoredSessionMemoryResult[T any](t *testing.T, result *mcp.CallToolResult) T {
	t.Helper()
	var zero T
	if result == nil {
		t.Fatal("MCP tool result is nil")
	}
	payload, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatalf("marshal MCP structured content: %v", err)
	}
	if string(payload) == "null" && len(result.Content) > 0 {
		if content, ok := result.Content[0].(*mcp.TextContent); ok {
			payload = []byte(content.Text)
		}
	}
	if err := json.Unmarshal(payload, &zero); err != nil {
		t.Fatalf("decode MCP structured content: %v", err)
	}
	return zero
}

func restoredSessionMemoryErrorCode(toolError *sessionmemorymcp.ToolError) string {
	if toolError == nil {
		return ""
	}
	return toolError.Code
}

type recallOnlyInvoker struct {
	calls atomic.Int64
}

func (i *recallOnlyInvoker) Invoke(context.Context, sessionmemoryapp.StructuredInvocation) ([]byte, error) {
	i.calls.Add(1)
	return nil, sessionmemory.PermanentError(sessionmemory.CodeModelFailure, "derivation is not allowed during recall", nil)
}

func (*recallOnlyInvoker) Close(context.Context) error { return nil }

var _ sessionmemoryapp.StructuredInvoker = (*recallOnlyInvoker)(nil)
