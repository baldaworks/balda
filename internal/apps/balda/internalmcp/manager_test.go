package internalmcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"sync"
	"testing"

	baldaagent "github.com/baldaworks/balda/internal/apps/balda/agent"
	"github.com/baldaworks/balda/internal/apps/balda/appports"
	"github.com/baldaworks/balda/internal/apps/balda/controlcmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
	"github.com/baldaworks/balda/internal/apps/balda/session"
	"github.com/baldaworks/balda/internal/apps/balda/sessionapp"
	"github.com/baldaworks/balda/internal/apps/balda/sessionmemorymcp"
	"github.com/baldaworks/balda/internal/apps/sessionmcp"
	"github.com/baldaworks/balda/sessionmemory"
	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/baldaworks/runtime/v2/mcpregistry"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rs/zerolog"
	"go.uber.org/fx"
)

type recordingDispatcher struct {
	envelopes []actorlayer.Envelope
}

type recordingProgressReporter struct {
	locator         session.SessionLocator
	requesterUserID string
	text            string
	idempotencyKey  string
	calls           int
}

func (r *recordingProgressReporter) Report(
	_ context.Context,
	locator session.SessionLocator,
	requesterUserID string,
	text string,
	idempotencyKey string,
) (sessionapp.ProgressResult, error) {
	r.calls++
	r.locator = locator
	r.requesterUserID = requesterUserID
	r.text = text
	r.idempotencyKey = idempotencyKey
	return sessionapp.ProgressResult{ProgressID: "progress-1", Disposition: "delivered"}, nil
}

type currentTurnResolverStub struct {
	wantSessionID string
	turn          appports.CurrentTurn
	ok            bool
}

func (s currentTurnResolverStub) CurrentTurn(sessionID string) (appports.CurrentTurn, bool) {
	if s.wantSessionID != "" && sessionID != s.wantSessionID {
		return appports.CurrentTurn{}, false
	}
	return s.turn, s.ok
}

func (d *recordingDispatcher) Dispatch(_ context.Context, env actorlayer.Envelope) (*actortransport.DispatchReceipt, error) {
	d.envelopes = append(d.envelopes, env)
	return &actortransport.DispatchReceipt{}, nil
}

type testSessionStore struct {
	mu     sync.RWMutex
	values map[string]any
}

func newTestSessionStore() *testSessionStore {
	return &testSessionStore{values: make(map[string]any)}
}

func (s *testSessionStore) Get(_ context.Context, key string) (string, bool, error) {
	val, ok, err := s.get(key)
	if err != nil {
		return "", false, err
	}
	if !ok {
		return "", false, nil
	}
	str, ok := val.(string)
	if ok {
		return str, true, nil
	}
	data, err := json.Marshal(val)
	if err != nil {
		return "", false, fmt.Errorf("marshal value: %w", err)
	}
	return string(data), true, nil
}

func (s *testSessionStore) Set(_ context.Context, key, value string) error {
	s.set(key, value)
	return nil
}

func (s *testSessionStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.values, strings.TrimSpace(key))
	return nil
}

func (s *testSessionStore) List(_ context.Context, prefix string) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	trimmedPrefix := strings.TrimSpace(prefix)
	keys := make([]string, 0, len(s.values))
	for key := range s.values {
		if trimmedPrefix == "" || strings.HasPrefix(key, trimmedPrefix) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys, nil
}

func (s *testSessionStore) Clear(_ context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values = make(map[string]any)
	return nil
}

func (s *testSessionStore) GetJSON(_ context.Context, key string) (interface{}, bool, error) {
	return s.get(key)
}

func (s *testSessionStore) SetJSON(_ context.Context, key string, value interface{}) error {
	s.set(key, value)
	return nil
}

func (s *testSessionStore) MergeJSON(_ context.Context, key string, fields map[string]interface{}) (map[string]interface{}, error) {
	trimmedKey := strings.TrimSpace(key)
	if trimmedKey == "" {
		return nil, fmt.Errorf("key is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	merged := make(map[string]interface{})
	if current, ok := s.values[trimmedKey].(map[string]interface{}); ok {
		for k, v := range current {
			merged[k] = v
		}
	}
	for k, v := range fields {
		merged[k] = v
	}
	s.values[trimmedKey] = merged
	return merged, nil
}

func (s *testSessionStore) get(key string) (interface{}, bool, error) {
	trimmedKey := strings.TrimSpace(key)
	if trimmedKey == "" {
		return nil, false, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	val, ok := s.values[trimmedKey]
	return val, ok, nil
}

func (s *testSessionStore) set(key string, value any) {
	trimmedKey := strings.TrimSpace(key)
	if trimmedKey == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values[trimmedKey] = value
}

func TestBundledBaldaServerInstructionsReflectWorkspaceMode(t *testing.T) {
	bundledBaldaServerInstructions := func(workspaceEnabled, memoryEnabled bool) string {
		instructions := `Use this bundled balda server for session-local balda tools.

- balda.state stores persistent Balda session and app state in state.db.
- balda.session.send_attachments can send one or more Telegram photos or documents into the active session from local files. Use source.engine=local with source.local_path.
- balda config editing is not exposed through MCP; edit the balda config file directly.
- balda.control.shutdown gracefully stops the whole Balda process; use it only when the user explicitly asks for restart or shutdown. After installing a new override binary, prefer balda.control.shutdown for restart. Use kill -TERM 1 only as a fallback when the in-process shutdown path is unavailable or broken.`
		if memoryEnabled {
			instructions += "\n- balda.memory stores durable facts in Balda state; only call balda.memory.remember when the user explicitly asks you to remember or save a fact."
		}
		if workspaceEnabled {
			instructions += "\n- balda.workspace is available and should be used for workspace import/export instead of manual branch landing."
		} else {
			instructions += "\n- balda.workspace is unavailable because balda workspace mode is disabled for this session."
		}
		return instructions
	}

	enabled := bundledBaldaServerInstructions(true, true)
	if !strings.Contains(enabled, "balda.workspace is available") {
		t.Fatalf("bundledBaldaServerInstructions(true, true) = %q, want workspace-enabled guidance", enabled)
	}
	if !strings.Contains(enabled, "balda.memory") {
		t.Fatalf("bundledBaldaServerInstructions(true, true) = %q, want memory guidance", enabled)
	}
	if !strings.Contains(enabled, "balda.session.send_attachments") {
		t.Fatalf("bundledBaldaServerInstructions(true, true) = %q, want send_attachments guidance", enabled)
	}
	if strings.Contains(enabled, "balda.agents.") {
		t.Fatalf("bundledBaldaServerInstructions(true, true) = %q, want balda.agents absent", enabled)
	}

	disabled := bundledBaldaServerInstructions(false, false)
	if !strings.Contains(disabled, "balda.workspace is unavailable") {
		t.Fatalf("bundledBaldaServerInstructions(false, false) = %q, want workspace-disabled guidance", disabled)
	}
	if strings.Contains(disabled, "balda.memory") {
		t.Fatalf("bundledBaldaServerInstructions(false, false) = %q, want no memory guidance", disabled)
	}
}

func TestCanonicalSessionLocatorReconstructsTelegramAddress(t *testing.T) {
	t.Parallel()

	got, err := canonicalSessionLocator(sessionmcp.SessionLocatorInput{
		SessionID:   "tg-10001-536036",
		ChannelType: "telegram",
		AddressKey:  "10001:536036",
	})
	if err != nil {
		t.Fatalf("canonicalSessionLocator() error = %v", err)
	}
	if got.AddressJSON != `{"chat_id":10001,"topic_id":536036}` {
		t.Fatalf("address_json = %q, want canonical Telegram topic", got.AddressJSON)
	}
}

func TestSessionProgressServiceUsesAuthenticatedCurrentTurn(t *testing.T) {
	t.Parallel()

	locator, err := deliverycmd.NewLocator("telegram", "101:77", `{"chat_id":101,"topic_id":77}`, "tg-101-77")
	if err != nil {
		t.Fatalf("NewLocator() error = %v", err)
	}
	broker := sessionmemorymcp.NewContextBroker()
	request := authenticatedSessionRequest(t, broker, sessionmemorymcp.CurrentSession{
		Locator: locator,
		Session: sessionmemory.SessionRef{SessionID: locator.SessionID, AgentSessionID: "agent-101-77"},
	})
	reporter := &recordingProgressReporter{}
	service := sessionProgressService{
		service:  reporter,
		sessions: sessionmemorymcp.HeaderSessionResolver{Broker: broker},
		currentTurns: currentTurnResolverStub{
			wantSessionID: locator.SessionID,
			turn:          appports.CurrentTurn{SessionID: locator.SessionID, RequesterUserID: "user-42"},
			ok:            true,
		},
	}

	out, err := service.ReportSessionProgress(t.Context(), request, sessionmcp.SessionProgressInput{
		Text: "Working", IdempotencyKey: "step-1",
	})
	if err != nil {
		t.Fatalf("ReportSessionProgress() error = %v", err)
	}
	if !out.Accepted || out.ProgressID != "progress-1" || reporter.calls != 1 {
		t.Fatalf("output = %+v, calls = %d", out, reporter.calls)
	}
	if reporter.locator != locator || reporter.requesterUserID != "user-42" ||
		reporter.text != "Working" || reporter.idempotencyKey != "step-1" {
		t.Fatalf("report = %+v", reporter)
	}
}

func TestSessionProgressServiceRejectsMissingContextOrTurnWithoutReporting(t *testing.T) {
	t.Parallel()

	locator, err := deliverycmd.NewLocator("telegram", "101:77", `{"chat_id":101,"topic_id":77}`, "tg-101-77")
	if err != nil {
		t.Fatalf("NewLocator() error = %v", err)
	}
	broker := sessionmemorymcp.NewContextBroker()
	request := authenticatedSessionRequest(t, broker, sessionmemorymcp.CurrentSession{
		Locator: locator,
		Session: sessionmemory.SessionRef{SessionID: locator.SessionID, AgentSessionID: "agent-101-77"},
	})
	reporter := &recordingProgressReporter{}
	service := sessionProgressService{
		service:      reporter,
		sessions:     sessionmemorymcp.HeaderSessionResolver{Broker: broker},
		currentTurns: currentTurnResolverStub{},
	}
	input := sessionmcp.SessionProgressInput{Text: "Working", IdempotencyKey: "step-1"}

	if _, err := service.ReportSessionProgress(t.Context(), request, input); err == nil {
		t.Fatal("ReportSessionProgress() without running turn error = nil")
	}
	service.currentTurns = currentTurnResolverStub{
		turn: appports.CurrentTurn{SessionID: "other-session", RequesterUserID: "user-42"},
		ok:   true,
	}
	if _, err := service.ReportSessionProgress(t.Context(), request, input); err == nil {
		t.Fatal("ReportSessionProgress() with mismatched running turn error = nil")
	}
	if _, err := service.ReportSessionProgress(t.Context(), nil, input); err == nil {
		t.Fatal("ReportSessionProgress() without authenticated context error = nil")
	}
	tamperedHeaders := request.GetExtra().Header.Clone()
	tamperedHeaders.Set(sessionmemorymcp.HeaderSessionID, "other-session")
	tampered := &mcp.CallToolRequest{Extra: &mcp.RequestExtra{Header: tamperedHeaders}}
	if _, err := service.ReportSessionProgress(t.Context(), tampered, input); err == nil {
		t.Fatal("ReportSessionProgress() with tampered context error = nil")
	}
	if reporter.calls != 0 {
		t.Fatalf("reporter calls = %d, want 0", reporter.calls)
	}
}

func authenticatedSessionRequest(
	t *testing.T,
	broker *sessionmemorymcp.ContextBroker,
	current sessionmemorymcp.CurrentSession,
) *mcp.CallToolRequest {
	t.Helper()

	if err := broker.SetBaseURL("http://127.0.0.1:12345/mcp/balda"); err != nil {
		t.Fatalf("SetBaseURL() error = %v", err)
	}
	binding, err := broker.Bind(current)
	if err != nil {
		t.Fatalf("Bind() error = %v", err)
	}
	t.Cleanup(func() { _ = binding.Release() })

	var headers http.Header
	broker.Wrap(http.HandlerFunc(func(_ http.ResponseWriter, req *http.Request) {
		headers = req.Header.Clone()
	})).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, binding.URL, nil))
	return &mcp.CallToolRequest{Extra: &mcp.RequestExtra{Header: headers}}
}

func TestCanonicalSessionLocatorReconstructsPrivateExecutionAddress(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		channelType string
		sessionID   string
		addressJSON string
	}{
		{channelType: "schedule", sessionID: "sch-any-stable-id", addressJSON: `{"run_id":"sch-any-stable-id"}`},
		{channelType: "webhook", sessionID: "wh-any-stable-id", addressJSON: `{}`},
	} {
		t.Run(test.channelType, func(t *testing.T) {
			got, err := canonicalSessionLocator(sessionmcp.SessionLocatorInput{
				SessionID: test.sessionID, ChannelType: test.channelType,
				AddressKey: test.sessionID, AddressJSON: `{"caller":"ignored"}`,
			})
			if err != nil {
				t.Fatal(err)
			}
			if got.AddressJSON != test.addressJSON || got.SessionID != test.sessionID {
				t.Fatalf("locator = %+v", got)
			}
		})
	}
}

func TestSessionSendAttachmentServiceDispatchesLocalAttachmentsBatch(t *testing.T) {
	t.Parallel()

	dispatcher := &recordingDispatcher{}
	svc := sessionSendAttachmentService{dispatcher: dispatcher}
	out, err := svc.SendSessionAttachments(context.Background(), sessionmcp.SessionSendAttachmentsInput{
		Locator: sessionmcp.SessionLocatorInput{
			SessionID:   "tg-10001-536036",
			ChannelType: "telegram",
			AddressKey:  "10001:536036",
		},
		Text: "Here you go",
		Attachments: []sessionmcp.SessionAttachmentItemInput{
			{
				Kind: "photo",
				Source: sessionmcp.SessionAttachmentSourceInput{
					Engine:    "local",
					LocalPath: "/state/attachments/a.jpg",
				},
				Caption: "first",
			},
			{
				Kind: "document",
				Source: sessionmcp.SessionAttachmentSourceInput{
					Engine:    "local",
					LocalPath: "/state/output/report.pdf",
				},
				Caption:  "second",
				FileName: "report.pdf",
				MIMEType: "application/pdf",
			},
		},
		RequestedBy: "tg-101",
	})
	if err != nil {
		t.Fatalf("SendSessionAttachments() error = %v", err)
	}
	if !out.Sent || out.Count != 2 || !out.HasText {
		t.Fatalf("SendSessionAttachments() output = %+v", out)
	}
	if len(dispatcher.envelopes) != 3 {
		t.Fatalf("dispatched envelopes = %d, want 3", len(dispatcher.envelopes))
	}
	var textPayload deliverycmd.Payload
	if err := actorlayer.UnmarshalPayload(dispatcher.envelopes[0].Payload, &textPayload); err != nil {
		t.Fatalf("unmarshal text payload: %v", err)
	}
	if textPayload.Mode != deliverycmd.ModeAgentReply || textPayload.Text != "Here you go" {
		t.Fatalf("text payload = %+v", textPayload)
	}
	var photoPayload deliverycmd.Payload
	if err := actorlayer.UnmarshalPayload(dispatcher.envelopes[1].Payload, &photoPayload); err != nil {
		t.Fatalf("unmarshal photo payload: %v", err)
	}
	if photoPayload.Mode != deliverycmd.ModePhoto || photoPayload.Media == nil || photoPayload.Media.LocalPath != "/state/attachments/a.jpg" {
		t.Fatalf("photo payload = %+v", photoPayload)
	}
	var docPayload deliverycmd.Payload
	if err := actorlayer.UnmarshalPayload(dispatcher.envelopes[2].Payload, &docPayload); err != nil {
		t.Fatalf("unmarshal document payload: %v", err)
	}
	if docPayload.Mode != deliverycmd.ModeDocument || docPayload.Media == nil || docPayload.Media.LocalPath != "/state/output/report.pdf" || docPayload.Media.Name != "report.pdf" {
		t.Fatalf("document payload = %+v", docPayload)
	}
}

func TestCanonicalSessionLocatorRejectsSessionMismatch(t *testing.T) {
	t.Parallel()

	_, err := canonicalSessionLocator(sessionmcp.SessionLocatorInput{
		SessionID:   "tg-10001-1",
		ChannelType: "telegram",
		AddressKey:  "10001:536036",
	})
	if err == nil || !strings.Contains(err.Error(), "locator mismatch") {
		t.Fatalf("canonicalSessionLocator() error = %v, want mismatch", err)
	}
}

func TestScheduleSessionWaitReconstructsMissingAddressJSON(t *testing.T) {
	t.Parallel()

	dispatcher := &recordingDispatcher{}
	service := sessionWaitService{dispatcher: dispatcher}
	err := service.ScheduleSessionWait(context.Background(), sessionmcp.SessionWaitInput{
		Locator: sessionmcp.SessionLocatorInput{
			SessionID:   "tg-10001-536036",
			ChannelType: "telegram",
			AddressKey:  "10001:536036",
		},
		Content:      "wake me",
		DelaySeconds: 60,
	})
	if err != nil {
		t.Fatalf("ScheduleSessionWait() error = %v", err)
	}
	if len(dispatcher.envelopes) != 1 {
		t.Fatalf("dispatched envelopes = %d, want 1", len(dispatcher.envelopes))
	}
	var payload controlcmd.Payload
	if err := actorlayer.UnmarshalPayload(dispatcher.envelopes[0].Payload, &payload); err != nil {
		t.Fatalf("decode control payload: %v", err)
	}
	if got, want := payload.Locator.AddressJSON, `{"chat_id":10001,"topic_id":536036}`; got != want {
		t.Fatalf("address_json = %q, want %q", got, want)
	}
}

func TestStartBundledMCPHTTPServer_MountsNamedRoutes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mkHandler := func(text string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, text)
		})
	}

	res, err := startBundledMCPHTTPServer(ctx, "127.0.0.1:0", map[string]http.Handler{
		"balda": mkHandler("balda"),
	})
	if err != nil {
		t.Fatalf("startBundledMCPHTTPServer() error = %v", err)
	}
	t.Cleanup(func() {
		_ = res.Close()
	})

	assertBody := func(path, want string) {
		t.Helper()
		resp, err := http.Get("http://" + res.Addr + path)
		if err != nil {
			t.Fatalf("GET %s error = %v", path, err)
		}
		defer func() { _ = resp.Body.Close() }()

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("read body for %s: %v", path, err)
		}
		if got := string(body); got != want {
			t.Fatalf("GET %s body = %q, want %q", path, got, want)
		}
	}

	assertBody("/mcp/balda", "balda")
}

func TestEnsureBundledServers_RegistersSharedListenerURLs(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	manager := &InternalMCPManager{
		workspaceEnabled: true,
		logger:           zerolog.Nop(),
		registry:         mcpregistry.New(nil),
		sessionManager:   &session.Manager{},
		stateStore:       newTestSessionStore(),
		shutdowner:       noopShutdowner{},
	}

	if err := manager.ensureBundledServers(ctx); err != nil {
		t.Fatalf("ensureBundledServers() error = %v", err)
	}
	t.Cleanup(func() {
		for _, cleanup := range manager.cleanups {
			_ = cleanup()
		}
	})

	cfg, ok := manager.registry.Get("balda")
	if !ok {
		t.Fatal("registry missing balda")
	}
	u, err := url.Parse(cfg.URL)
	if err != nil {
		t.Fatalf("parse URL for balda: %v", err)
	}
	if u.Scheme != "http" {
		t.Fatalf("balda scheme = %q, want http", u.Scheme)
	}
	if u.Path != "/mcp/balda" {
		t.Fatalf("balda path = %q, want /mcp/balda", u.Path)
	}
	if strings.TrimSpace(u.Host) == "" {
		t.Fatal("shared host is empty")
	}
}

type retainedSkillFixture struct {
	snapshot runtimecatalogcmd.Snapshot
}

func (f retainedSkillFixture) RetainedSkillSnapshot(_ context.Context, id runtimecatalogcmd.SnapshotID) (runtimecatalogcmd.Snapshot, error) {
	if id != f.snapshot.ID {
		return runtimecatalogcmd.Snapshot{}, runtimecatalogcmd.ErrSnapshotUnavailable
	}
	return f.snapshot, nil
}

func (f retainedSkillFixture) ReadSkill(_ context.Context, request runtimecatalogcmd.SkillReadRequest) (runtimecatalogcmd.LoadedSkill, error) {
	return runtimecatalogcmd.LoadedSkill{Ref: request.Ref, Instructions: "loaded through bundled MCP"}, nil
}

func TestEnsureBundledServers_LoadsSkillFromAuthenticatedRuntimeSnapshot(t *testing.T) {
	source := runtimecatalogcmd.SourceID{Kind: runtimecatalogcmd.SourceKindPlugin, Name: "prism"}
	id := runtimecatalogcmd.ContributionID{Source: source, Kind: runtimecatalogcmd.ContributionKindSkill, Name: "story"}
	fixture := retainedSkillFixture{snapshot: runtimecatalogcmd.Snapshot{
		ID: "snapshot-prism",
		Skills: map[runtimecatalogcmd.ContributionID]runtimecatalogcmd.SkillMetadata{
			id: {ID: id, Name: "story", Revision: "revision-prism", Resource: "skills/story/SKILL.md"},
		},
	}}
	skills, err := baldaagent.NewSkillManager(fixture, fixture, baldaagent.SkillMetadataBudget{})
	if err != nil {
		t.Fatalf("NewSkillManager() error = %v", err)
	}
	broker := sessionmemorymcp.NewContextBroker()
	manager := &InternalMCPManager{
		logger:         zerolog.Nop(),
		registry:       mcpregistry.New(nil),
		sessionManager: &session.Manager{},
		stateStore:     newTestSessionStore(),
		shutdowner:     noopShutdowner{},
		contextBroker:  broker,
		skills:         skills,
	}
	if err := manager.ensureBundledServers(t.Context()); err != nil {
		t.Fatalf("ensureBundledServers() error = %v", err)
	}
	t.Cleanup(func() {
		for i := len(manager.cleanups) - 1; i >= 0; i-- {
			_ = manager.cleanups[i]()
		}
	})
	locator, err := deliverycmd.NewLocator("telegram", "101:77", `{"chat_id":101,"topic_id":77}`, "tg-101-77")
	if err != nil {
		t.Fatalf("NewLocator() error = %v", err)
	}
	binding, err := broker.Bind(sessionmemorymcp.CurrentSession{
		Locator:           locator,
		Session:           sessionmemory.SessionRef{SessionID: locator.SessionID, AgentSessionID: "agent-101-77"},
		RuntimeSnapshotID: "snapshot-prism",
	})
	if err != nil {
		t.Fatalf("Bind() error = %v", err)
	}
	t.Cleanup(func() { _ = binding.Release() })
	client := mcp.NewClient(&mcp.Implementation{Name: "bundled-skill-test", Version: "1"}, nil)
	connected, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: binding.URL}, nil)
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	t.Cleanup(func() { _ = connected.Close() })
	result, err := connected.CallTool(t.Context(), &mcp.CallToolParams{Name: "balda.skill.load", Arguments: map[string]any{
		"source_kind": "plugin", "source_name": "prism", "skill_name": "story",
	}})
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if result.IsError || !strings.Contains(string(encoded), "loaded through bundled MCP") || !strings.Contains(string(encoded), "revision-prism") {
		t.Fatalf("CallTool() result = %s, IsError = %v", encoded, result.IsError)
	}
}

func TestInternalMCPManagerEnsureStarted_IsIdempotent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	manager := &InternalMCPManager{
		workspaceEnabled: false,
		logger:           zerolog.Nop(),
		registry:         mcpregistry.New(nil),
		sessionManager:   &session.Manager{},
		stateStore:       newTestSessionStore(),
		shutdowner:       noopShutdowner{},
	}

	if err := manager.EnsureStarted(ctx); err != nil {
		t.Fatalf("EnsureStarted() first call error = %v", err)
	}
	firstCleanupCount := len(manager.cleanups)
	if firstCleanupCount == 0 {
		t.Fatal("EnsureStarted() did not register cleanup handlers")
	}

	if err := manager.EnsureStarted(ctx); err != nil {
		t.Fatalf("EnsureStarted() second call error = %v", err)
	}
	if got := len(manager.cleanups); got != firstCleanupCount {
		t.Fatalf("EnsureStarted() cleanup count = %d, want %d", got, firstCleanupCount)
	}
	manager.mu.RLock()
	started := manager.started
	manager.mu.RUnlock()
	if !started {
		t.Fatal("started = false, want true")
	}

	t.Cleanup(func() {
		for i := len(manager.cleanups) - 1; i >= 0; i-- {
			_ = manager.cleanups[i]()
		}
	})
}

type noopShutdowner struct{}

func (noopShutdowner) Shutdown(...fx.ShutdownOption) error {
	return nil
}
