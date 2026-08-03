package sessionmemoryhttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/normahq/balda/internal/apps/balda/sessionmemory"
)

func TestClientImplementsV1ProtocolWithAuthAndIdempotency(t *testing.T) {
	var turns, boundaries, searches atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer secret-token" {
			t.Errorf("Authorization = %q, want bearer token", got)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q", got)
		}
		if got := r.Header.Get("Accept"); got != "application/json" {
			t.Errorf("Accept = %q", got)
		}
		switch r.URL.Path {
		case turnPath:
			var turn sessionmemory.Turn
			if err := json.NewDecoder(r.Body).Decode(&turn); err != nil {
				t.Errorf("decode turn: %v", err)
			}
			if turn.ExportID == "" || r.Header.Get("Idempotency-Key") != turn.ExportID {
				t.Errorf("turn idempotency key = %q, export = %q", r.Header.Get("Idempotency-Key"), turn.ExportID)
			}
			turns.Add(1)
			w.WriteHeader(http.StatusAccepted)
		case boundaryPath:
			var boundary sessionmemory.Boundary
			if err := json.NewDecoder(r.Body).Decode(&boundary); err != nil {
				t.Errorf("decode boundary: %v", err)
			}
			if boundary.ExportID == "" || r.Header.Get("Idempotency-Key") != boundary.ExportID {
				t.Errorf("boundary idempotency key = %q, export = %q", r.Header.Get("Idempotency-Key"), boundary.ExportID)
			}
			boundaries.Add(1)
			w.WriteHeader(http.StatusNoContent)
		case searchPath:
			var request sessionmemory.SearchRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode search: %v", err)
			}
			if request.Scope.Key != "telegram:123:0" || request.Limit != 10 {
				t.Errorf("search request = %+v", request)
			}
			searches.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(sessionmemory.SearchResponse{
				SchemaVersion: sessionmemory.SchemaVersionV1,
				Scope:         request.Scope,
				Results: []sessionmemory.SearchResult{{
					ID:        "memory-1",
					ScopeKey:  request.Scope.Key,
					SessionID: request.Session.SessionID,
					Text:      "reference text",
				}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := New(Config{BaseURL: server.URL, Token: "secret-token"})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	scope := sessionmemory.Scope{Key: "telegram:123:0", Kind: sessionmemory.ScopeKindPersonal}
	session := sessionmemory.SessionRef{SessionID: "tg-123-0", AgentSessionID: "adk-1"}
	turn, err := sessionmemory.NewTurn(scope, session, "turn-1", time.Now().UTC(), "hello", "hi")
	if err != nil {
		t.Fatalf("NewTurn() error = %v", err)
	}
	if err := client.SyncTurn(context.Background(), turn); err != nil {
		t.Fatalf("SyncTurn() error = %v", err)
	}
	boundary, err := sessionmemory.NewBoundary(scope, session, "close-1", sessionmemory.BoundaryReasonClose, time.Now().UTC())
	if err != nil {
		t.Fatalf("NewBoundary() error = %v", err)
	}
	if err := client.OnSessionBoundary(context.Background(), boundary); err != nil {
		t.Fatalf("OnSessionBoundary() error = %v", err)
	}
	response, err := client.Search(context.Background(), sessionmemory.SearchRequest{
		Scope:   scope,
		Session: session,
		Query:   "hello",
	})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(response.Results) != 1 || response.Results[0].Text != "reference text" {
		t.Fatalf("Search() response = %+v", response)
	}
	if turns.Load() != 1 || boundaries.Load() != 1 || searches.Load() != 1 {
		t.Fatalf("request counts = turns:%d boundaries:%d searches:%d", turns.Load(), boundaries.Load(), searches.Load())
	}
}

func TestClientAcceptsDocumentedDuplicateAndClassifiesStatuses(t *testing.T) {
	status := atomic.Int32{}
	duplicate := atomic.Bool{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if duplicate.Load() {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"code":"duplicate"}`))
			return
		}
		w.WriteHeader(int(status.Load()))
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	scope := sessionmemory.Scope{Key: "telegram:123:0", Kind: sessionmemory.ScopeKindPersonal}
	session := sessionmemory.SessionRef{SessionID: "tg-123-0", AgentSessionID: "adk-1"}
	turn, err := sessionmemory.NewTurn(scope, session, "turn-1", time.Now().UTC(), "hello", "hi")
	if err != nil {
		t.Fatalf("NewTurn() error = %v", err)
	}

	duplicate.Store(true)
	if err := client.SyncTurn(context.Background(), turn); err != nil {
		t.Fatalf("duplicate SyncTurn() error = %v", err)
	}
	for _, test := range []struct {
		name  string
		code  int32
		want  sessionmemory.ErrorCode
		class sessionmemory.ErrorClass
	}{
		{name: "request timeout", code: http.StatusRequestTimeout, want: sessionmemory.CodeTimeout, class: sessionmemory.ErrorClassRetryable},
		{name: "rate limit", code: http.StatusTooManyRequests, want: sessionmemory.CodeUnavailable, class: sessionmemory.ErrorClassRetryable},
		{name: "server failure", code: http.StatusBadGateway, want: sessionmemory.CodeUnavailable, class: sessionmemory.ErrorClassRetryable},
		{name: "unauthorized", code: http.StatusUnauthorized, want: sessionmemory.CodePermanent, class: sessionmemory.ErrorClassPermanent},
	} {
		t.Run(test.name, func(t *testing.T) {
			duplicate.Store(false)
			status.Store(test.code)
			err := client.SyncTurn(context.Background(), turn)
			if err == nil {
				t.Fatal("SyncTurn() error = nil")
			}
			code, class, ok := sessionmemory.ClassifyError(err)
			if !ok || code != test.want || class != test.class {
				t.Fatalf("error = %v, code = %q, class = %q", err, code, class)
			}
		})
	}
}

func TestClientRejectsForeignSearchScopeWithoutLeakingResponseBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(sessionmemory.SearchResponse{
			SchemaVersion: sessionmemory.SchemaVersionV1,
			Scope:         sessionmemory.Scope{Key: "telegram:999:0", Kind: sessionmemory.ScopeKindPersonal},
			Results: []sessionmemory.SearchResult{{
				ID:        "secret-result",
				ScopeKey:  "telegram:999:0",
				SessionID: "foreign-session",
				Text:      "provider secret should stay out of diagnostics",
			}},
		})
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	_, err = client.Search(context.Background(), sessionmemory.SearchRequest{
		Scope:   sessionmemory.Scope{Key: "telegram:123:0", Kind: sessionmemory.ScopeKindPersonal},
		Session: sessionmemory.SessionRef{SessionID: "tg-123-0", AgentSessionID: "adk-1"},
		Query:   "hello",
	})
	if err == nil {
		t.Fatal("Search() error = nil")
	}
	if code, _, ok := sessionmemory.ClassifyError(err); !ok || code != sessionmemory.CodeScopeViolation {
		t.Fatalf("Search() error = %v, code = %q", err, code)
	}
	if strings.Contains(err.Error(), "provider secret") || strings.Contains(err.Error(), "secret-result") {
		t.Fatalf("Search() error leaked response body: %v", err)
	}
}

func TestClientRejectsMalformedSearchResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"not":"a search response"}`))
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	_, err = client.Search(context.Background(), sessionmemory.SearchRequest{
		Scope:   sessionmemory.Scope{Key: "telegram:123:0", Kind: sessionmemory.ScopeKindPersonal},
		Session: sessionmemory.SessionRef{SessionID: "tg-123-0", AgentSessionID: "adk-1"},
		Query:   "hello",
	})
	if err == nil {
		t.Fatal("Search() error = nil")
	}
	if code, _, ok := sessionmemory.ClassifyError(err); !ok || code != sessionmemory.CodePermanent {
		t.Fatalf("Search() error = %v, code = %q", err, code)
	}
}

func TestClientMapsTransportTimeoutAndValidatesConfiguration(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(100 * time.Millisecond):
			w.WriteHeader(http.StatusNoContent)
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL, Timeout: 10 * time.Millisecond})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	scope := sessionmemory.Scope{Key: "telegram:123:0", Kind: sessionmemory.ScopeKindPersonal}
	turn, err := sessionmemory.NewTurn(scope, sessionmemory.SessionRef{SessionID: "tg-123-0", AgentSessionID: "adk-1"}, "turn-1", time.Now().UTC(), "hello", "hi")
	if err != nil {
		t.Fatalf("NewTurn() error = %v", err)
	}
	err = client.SyncTurn(context.Background(), turn)
	if err == nil {
		t.Fatal("SyncTurn() error = nil")
	}
	if code, class, ok := sessionmemory.ClassifyError(err); !ok || code != sessionmemory.CodeTimeout || class != sessionmemory.ErrorClassRetryable {
		t.Fatalf("timeout error = %v, code = %q, class = %q", err, code, class)
	}
	for _, raw := range []string{"", "ftp://memory.example", "https://memory.example/path?token=secret", "https://user:pass@memory.example"} {
		if _, err := New(Config{BaseURL: raw}); err == nil {
			t.Fatalf("New(%q) error = nil", raw)
		}
	}
	if err := client.Close(context.Background()); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := client.Close(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Close(canceled) error = %v, want context.Canceled", err)
	}
}
