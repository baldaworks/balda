package natsbus

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	baldatelegram "github.com/normahq/balda/internal/apps/balda/channel/telegram"
	"github.com/normahq/balda/internal/apps/balda/sessionmemory"
	"github.com/normahq/balda/internal/apps/balda/sessionmemoryapp"
	"github.com/normahq/balda/internal/apps/balda/sessionmemoryhttp"
	"github.com/rs/zerolog"
)

func TestSessionMemoryEndToEndHTTPProviderScopeAndBoundary(t *testing.T) {
	t.Parallel()

	type providerState struct {
		mu            sync.Mutex
		turns         []sessionmemory.Turn
		boundaries    []sessionmemory.Boundary
		turnCalls     int
		writeKeys     []string
		injectForeign bool
	}
	state := &providerState{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		state.mu.Lock()
		defer state.mu.Unlock()
		switch r.URL.Path {
		case "/v1/turns":
			state.turnCalls++
			state.writeKeys = append(state.writeKeys, r.Header.Get("Idempotency-Key"))
			if state.turnCalls == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			var turn sessionmemory.Turn
			if err := json.NewDecoder(r.Body).Decode(&turn); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			state.turns = append(state.turns, turn)
		case "/v1/boundaries":
			state.writeKeys = append(state.writeKeys, r.Header.Get("Idempotency-Key"))
			var boundary sessionmemory.Boundary
			if err := json.NewDecoder(r.Body).Decode(&boundary); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			state.boundaries = append(state.boundaries, boundary)
		case "/v1/search":
			var request sessionmemory.SearchRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			response := sessionmemory.SearchResponse{
				SchemaVersion: sessionmemory.SchemaVersionV1,
				Scope:         request.Scope,
				Results:       nil,
			}
			if state.injectForeign {
				response.Results = []sessionmemory.SearchResult{{
					ID:        "foreign",
					ScopeKey:  "telegram:123:0",
					SessionID: "tg-123-0",
					Text:      "must be rejected",
				}}
			} else {
				for _, turn := range state.turns {
					if turn.Scope.Key != request.Scope.Key {
						continue
					}
					response.Results = append(response.Results, sessionmemory.SearchResult{
						ID:        turn.ExportID,
						ScopeKey:  turn.Scope.Key,
						SessionID: turn.Session.SessionID,
						Text:      turn.Messages[1].Text,
						CreatedAt: turn.CompletedAt,
					})
				}
			}
			_ = json.NewEncoder(w).Encode(response)
			return
		default:
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	bus := StartTestRuntime(t, enabledSessionMemoryExecutionConfig()).Bus
	provider, err := sessionmemoryhttp.New(sessionmemoryhttp.Config{
		BaseURL: server.URL,
		Timeout: time.Second,
	})
	if err != nil {
		t.Fatalf("sessionmemoryhttp.New() error = %v", err)
	}
	resolver := sessionmemoryapp.NewScopeResolver(map[string]sessionmemoryapp.ScopeClassifier{
		baldatelegram.ChannelType: baldatelegram.ClassifyLocatorScope,
	})
	capture := sessionmemoryapp.NewTurnCapture(bus.SessionMemoryExportPublisher(), resolver)
	boundaryCapture := sessionmemoryapp.NewBoundaryCapture(bus.SessionMemoryExportPublisher(), resolver)
	locator := baldatelegram.NewLocator(123, 0)
	completedAt := time.Date(2026, 8, 3, 5, 6, 7, 0, time.UTC)
	turnResult, err := capture.Capture(context.Background(), sessionmemoryapp.CaptureRequest{
		UserText:       "remember the release checklist",
		AssistantText:  "the release checklist is ready",
		Locator:        locator,
		SessionID:      locator.SessionID,
		AgentSessionID: "adk-7",
		SourceTurnID:   "telegram:message:9",
		CompletedAt:    completedAt,
	})
	if err != nil {
		t.Fatalf("TurnCapture.Capture() error = %v", err)
	}
	boundaryResult, err := boundaryCapture.Capture(context.Background(), sessionmemoryapp.BoundaryCaptureRequest{
		Locator:        locator,
		SessionID:      locator.SessionID,
		AgentSessionID: "adk-7",
		TransitionID:   "rotation-1",
		Reason:         sessionmemory.BoundaryReasonRotation,
		OccurredAt:     completedAt.Add(time.Second),
	})
	if err != nil {
		t.Fatalf("BoundaryCapture.Capture() error = %v", err)
	}

	worker, err := sessionmemoryapp.NewWorker(bus.SessionMemoryTransport(), provider, sessionmemoryapp.Config{
		Enabled:          true,
		MaxAttempts:      2,
		RetryBaseDelay:   5 * time.Millisecond,
		RetryMaxDelay:    5 * time.Millisecond,
		ProgressInterval: 5 * time.Millisecond,
	}, zerolog.Nop())
	if err != nil {
		t.Fatalf("NewWorker() error = %v", err)
	}
	if err := worker.Start(context.Background()); err != nil {
		t.Fatalf("worker.Start() error = %v", err)
	}
	waitForSessionMemoryWorker(t, 3*time.Second, func() bool {
		stats, statsErr := bus.SessionMemoryStats(context.Background())
		state.mu.Lock()
		defer state.mu.Unlock()
		return statsErr == nil && stats.Messages == 0 && stats.Pending == 0 && stats.Acknowledging == 0 &&
			len(state.turns) == 1 && len(state.boundaries) == 1
	})
	if err := worker.Stop(context.Background()); err != nil {
		t.Fatalf("worker.Stop() error = %v", err)
	}

	state.mu.Lock()
	if len(state.writeKeys) != 3 || state.writeKeys[0] != turnResult.ExportID || state.writeKeys[1] != turnResult.ExportID || state.writeKeys[2] != boundaryResult.ExportID {
		t.Fatalf("provider idempotency keys = %v, want turn retry followed by boundary", state.writeKeys)
	}
	state.mu.Unlock()

	scope, err := resolver.Resolve(locator)
	if err != nil {
		t.Fatalf("ScopeResolver.Resolve(personal) error = %v", err)
	}
	personal, err := provider.Search(context.Background(), sessionmemory.SearchRequest{
		Scope:   scope,
		Session: sessionmemory.SessionRef{SessionID: locator.SessionID, AgentSessionID: "adk-7"},
		Query:   "release",
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("provider.Search(personal) error = %v", err)
	}
	if len(personal.Results) != 1 || personal.Results[0].ScopeKey != scope.Key {
		t.Fatalf("personal search results = %+v, want one exact-scope result", personal.Results)
	}

	groupLocator := baldatelegram.NewLocator(-100, 42)
	groupScope, err := resolver.Resolve(groupLocator)
	if err != nil {
		t.Fatalf("ScopeResolver.Resolve(group) error = %v", err)
	}
	group, err := provider.Search(context.Background(), sessionmemory.SearchRequest{
		Scope:   groupScope,
		Session: sessionmemory.SessionRef{SessionID: groupLocator.SessionID, AgentSessionID: "adk-group"},
		Query:   "release",
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("provider.Search(group) error = %v", err)
	}
	if len(group.Results) != 0 {
		t.Fatalf("group search leaked personal results = %+v", group.Results)
	}

	state.mu.Lock()
	state.injectForeign = true
	state.mu.Unlock()
	_, err = provider.Search(context.Background(), sessionmemory.SearchRequest{
		Scope:   groupScope,
		Session: sessionmemory.SessionRef{SessionID: groupLocator.SessionID, AgentSessionID: "adk-group"},
		Query:   "release",
		Limit:   10,
	})
	if code, _, ok := sessionmemory.ClassifyError(err); !ok || code != sessionmemory.CodeScopeViolation {
		t.Fatalf("foreign search error = %v (%q), want scope violation", err, code)
	}
}
