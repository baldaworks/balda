package sessionmemorymcp

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

const contextQueryParameter = "balda_context"

// ContextBinding is an opaque, authenticated internal MCP endpoint binding.
// The URL contains only a random capability; the locator and session identity
// remain server-side and are injected by ContextBroker before dispatch.
type ContextBinding struct {
	ID      string
	URL     string
	Release func() error
}

type boundContext struct {
	token        string
	locatorJSON  string
	sessionID    string
	agentSession string
	lineageID    string
}

// ContextBroker binds one internal MCP client endpoint to one authenticated
// Balda session. It is deliberately separate from the public tool schema and
// only accepts bindings created by the composition root.
type ContextBroker struct {
	mu      sync.RWMutex
	baseURL string
	bound   map[string]boundContext
}

// NewContextBroker creates an empty broker. SetBaseURL must be called after
// the bundled MCP listener has started and before a runtime binds a session.
func NewContextBroker() *ContextBroker {
	return &ContextBroker{bound: make(map[string]boundContext)}
}

// SetBaseURL sets the internal MCP endpoint used for new bindings.
func (b *ContextBroker) SetBaseURL(raw string) error {
	if b == nil {
		return fmt.Errorf("context broker is required")
	}
	trimmed := strings.TrimSpace(raw)
	u, err := url.Parse(trimmed)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("context broker base URL must be absolute")
	}
	b.mu.Lock()
	b.baseURL = strings.TrimRight(trimmed, "?")
	b.mu.Unlock()
	return nil
}

// Bind creates an opaque endpoint for one validated current session.
func (b *ContextBroker) Bind(current CurrentSession) (ContextBinding, error) {
	if b == nil {
		return ContextBinding{}, fmt.Errorf("context broker is required")
	}
	if err := validateCurrentSession(current); err != nil {
		return ContextBinding{}, err
	}
	locatorJSON, err := json.Marshal(current.Locator)
	if err != nil {
		return ContextBinding{}, fmt.Errorf("encode current locator: %w", err)
	}
	b.mu.RLock()
	baseURL := b.baseURL
	b.mu.RUnlock()
	if baseURL == "" {
		return ContextBinding{}, fmt.Errorf("context broker listener is not ready")
	}

	token, err := randomContextToken()
	if err != nil {
		return ContextBinding{}, err
	}
	bound := boundContext{
		token:        token,
		locatorJSON:  string(locatorJSON),
		sessionID:    strings.TrimSpace(current.Session.SessionID),
		agentSession: strings.TrimSpace(current.Session.AgentSessionID),
		lineageID:    strings.TrimSpace(current.Session.LineageID),
	}
	b.mu.Lock()
	if b.bound == nil {
		b.bound = make(map[string]boundContext)
	}
	b.bound[token] = bound
	b.mu.Unlock()

	endpoint, err := contextURL(baseURL, token)
	if err != nil {
		b.release(token)
		return ContextBinding{}, err
	}
	var once sync.Once
	release := func() error {
		once.Do(func() { b.release(token) })
		return nil
	}
	return ContextBinding{ID: "balda-session-memory-" + token, URL: endpoint, Release: release}, nil
}

// Wrap injects the trusted headers for a bound request and rejects unknown
// capabilities. Requests without the internal query parameter continue to
// the wrapped handler for non-session-bound bundled tools; session-memory
// calls still fail closed in HeaderSessionResolver.
func (b *ContextBroker) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if b == nil || next == nil {
			http.Error(w, "internal MCP context unavailable", http.StatusServiceUnavailable)
			return
		}
		token := strings.TrimSpace(r.URL.Query().Get(contextQueryParameter))
		if token == "" {
			next.ServeHTTP(w, r)
			return
		}
		b.mu.RLock()
		bound, ok := b.bound[token]
		b.mu.RUnlock()
		if !ok {
			http.Error(w, "invalid internal MCP context", http.StatusUnauthorized)
			return
		}
		request := r.Clone(r.Context())
		request.Header = r.Header.Clone()
		request.URL = cloneURL(r.URL)
		query := request.URL.Query()
		query.Del(contextQueryParameter)
		request.URL.RawQuery = query.Encode()
		request.Header.Set(HeaderSessionLocator, bound.locatorJSON)
		request.Header.Set(HeaderSessionID, bound.sessionID)
		request.Header.Set(HeaderAgentSessionID, bound.agentSession)
		request.Header.Set(HeaderSessionBinding, bound.token)
		if bound.lineageID == "" {
			request.Header.Del(HeaderLineageID)
		} else {
			request.Header.Set(HeaderLineageID, bound.lineageID)
		}
		next.ServeHTTP(w, request)
	})
}

// Verify authenticates the headers injected by an active binding. It compares
// every identity field so a caller cannot reuse a capability with a modified
// locator or session header.
func (b *ContextBroker) Verify(headers http.Header) bool {
	if b == nil || headers == nil {
		return false
	}
	token := strings.TrimSpace(headers.Get(HeaderSessionBinding))
	if token == "" {
		return false
	}
	b.mu.RLock()
	bound, ok := b.bound[token]
	b.mu.RUnlock()
	if !ok {
		return false
	}
	return headers.Get(HeaderSessionLocator) == bound.locatorJSON &&
		headers.Get(HeaderSessionID) == bound.sessionID &&
		headers.Get(HeaderAgentSessionID) == bound.agentSession &&
		headers.Get(HeaderLineageID) == bound.lineageID
}

func cloneURL(raw *url.URL) *url.URL {
	if raw == nil {
		return &url.URL{}
	}
	copyOfURL := *raw
	return &copyOfURL
}

func (b *ContextBroker) release(token string) {
	b.mu.Lock()
	delete(b.bound, token)
	b.mu.Unlock()
}

func validateCurrentSession(current CurrentSession) error {
	if strings.TrimSpace(current.Locator.ChannelType) == "" ||
		strings.TrimSpace(current.Locator.AddressKey) == "" ||
		strings.TrimSpace(current.Locator.AddressJSON) == "" ||
		strings.TrimSpace(current.Locator.SessionID) == "" {
		return fmt.Errorf("current locator is incomplete")
	}
	if current.Session.SessionID != current.Locator.SessionID {
		return fmt.Errorf("current session does not match locator")
	}
	if err := current.Session.Validate(); err != nil {
		return fmt.Errorf("current session is invalid: %w", err)
	}
	return nil
}

func randomContextToken() (string, error) {
	var raw [24]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate internal MCP context: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

func contextURL(baseURL, token string) (string, error) {
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", fmt.Errorf("context broker base URL must be absolute")
	}
	query := u.Query()
	query.Set(contextQueryParameter, token)
	u.RawQuery = query.Encode()
	return u.String(), nil
}
