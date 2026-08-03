package sessionmemoryhttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/normahq/balda/internal/apps/balda/sessionmemory"
)

const (
	defaultRequestTimeout = 10 * time.Second
	defaultMaxResponse    = 1 << 20

	turnPath     = "/v1/turns"
	boundaryPath = "/v1/boundaries"
	searchPath   = "/v1/search"
)

// Config configures the vendor-neutral HTTP/JSON v1 provider.
type Config struct {
	// BaseURL is the provider origin, optionally including a deployment path.
	BaseURL string
	// Token is sent as an optional Bearer credential. It is never logged or
	// included in returned errors.
	Token string
	// Client overrides the HTTP client, which is useful for tests and custom
	// transports. A nil client gets a bounded default client.
	Client *http.Client
	// Timeout bounds each provider request. Zero uses ten seconds.
	Timeout time.Duration
	// MaxResponseBytes bounds successful and error response bodies. Zero uses
	// one mebibyte.
	MaxResponseBytes int64
}

// Client implements sessionmemory.Provider over HTTP/JSON v1.
type Client struct {
	baseURL          *url.URL
	token            string
	httpClient       *http.Client
	requestTimeout   time.Duration
	maxResponseBytes int64
}

// New validates configuration and creates an HTTP provider client.
func New(config Config) (*Client, error) {
	baseURL, err := parseBaseURL(config.BaseURL)
	if err != nil {
		return nil, err
	}
	timeout := config.Timeout
	if timeout <= 0 {
		timeout = defaultRequestTimeout
	}
	maxResponseBytes := config.MaxResponseBytes
	if maxResponseBytes <= 0 {
		maxResponseBytes = defaultMaxResponse
	}
	httpClient := config.Client
	if httpClient == nil {
		httpClient = &http.Client{Timeout: timeout}
	}
	return &Client{
		baseURL:          baseURL,
		token:            strings.TrimSpace(config.Token),
		httpClient:       httpClient,
		requestTimeout:   timeout,
		maxResponseBytes: maxResponseBytes,
	}, nil
}

var _ sessionmemory.Provider = (*Client)(nil)

// SyncTurn submits one completed turn with its stable idempotency key.
func (c *Client) SyncTurn(ctx context.Context, turn sessionmemory.Turn) error {
	if c == nil {
		return sessionmemory.PermanentError(sessionmemory.CodeDisabled, "session-memory provider is unavailable", nil)
	}
	if err := turn.Validate(); err != nil {
		return err
	}
	return c.write(ctx, turnPath, turn.ExportID, turn)
}

// OnSessionBoundary submits one lifecycle boundary with its stable
// idempotency key.
func (c *Client) OnSessionBoundary(ctx context.Context, boundary sessionmemory.Boundary) error {
	if c == nil {
		return sessionmemory.PermanentError(sessionmemory.CodeDisabled, "session-memory provider is unavailable", nil)
	}
	if err := boundary.Validate(); err != nil {
		return err
	}
	return c.write(ctx, boundaryPath, boundary.ExportID, boundary)
}

// Search performs a bounded exact-scope recall request and validates the
// provider's echoed scope and every returned result before returning it.
func (c *Client) Search(ctx context.Context, request sessionmemory.SearchRequest) (sessionmemory.SearchResponse, error) {
	if c == nil {
		return sessionmemory.SearchResponse{}, sessionmemory.PermanentError(sessionmemory.CodeDisabled, "session-memory provider is unavailable", nil)
	}
	normalized, err := sessionmemory.NormalizeSearchRequest(request)
	if err != nil {
		return sessionmemory.SearchResponse{}, err
	}
	var response sessionmemory.SearchResponse
	if err := c.doJSON(ctx, searchPath, "", normalized, &response); err != nil {
		return sessionmemory.SearchResponse{}, err
	}
	if err := sessionmemory.ValidateSearchResponse(normalized, response); err != nil {
		return sessionmemory.SearchResponse{}, err
	}
	return response, nil
}

// Close releases provider resources. The standard HTTP client owns no
// lifecycle that needs closing; context cancellation is still honored so a
// composition root can use this method as a bounded shutdown hook.
func (c *Client) Close(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func (c *Client) write(ctx context.Context, path, idempotencyKey string, payload any) error {
	return c.doJSON(ctx, path, idempotencyKey, payload, nil)
}

func (c *Client) doJSON(ctx context.Context, path, idempotencyKey string, payload any, response any) error {
	if ctx == nil {
		ctx = context.Background()
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return sessionmemory.PermanentError(sessionmemory.CodePermanent, "encode session-memory request", nil)
	}
	requestCtx, cancel := context.WithTimeout(ctx, c.requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, c.endpoint(path), bytes.NewReader(data))
	if err != nil {
		return sessionmemory.PermanentError(sessionmemory.CodePermanent, "build session-memory request", nil)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if strings.TrimSpace(idempotencyKey) != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return classifyTransportError(requestCtx, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		if resp.StatusCode != http.StatusConflict {
			return classifyHTTPStatus(resp.StatusCode)
		}
	}
	body, err := readBounded(resp.Body, c.maxResponseBytes)
	if err != nil {
		return sessionmemory.PermanentError(sessionmemory.CodePermanent, "read session-memory response", nil)
	}
	if resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices {
		if response == nil || len(bytes.TrimSpace(body)) == 0 {
			return nil
		}
		if err := json.Unmarshal(body, response); err != nil {
			return sessionmemory.PermanentError(sessionmemory.CodePermanent, "decode session-memory response", nil)
		}
		return nil
	}
	if resp.StatusCode == http.StatusConflict && isDuplicateResponse(body) {
		return nil
	}
	return classifyHTTPStatus(resp.StatusCode)
}

func (c *Client) endpoint(path string) string {
	basePath := strings.TrimRight(c.baseURL.Path, "/")
	return c.baseURL.Scheme + "://" + c.baseURL.Host + basePath + path
}

func parseBaseURL(raw string) (*url.URL, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, fmt.Errorf("session-memory provider base URL is required")
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil {
		return nil, fmt.Errorf("session-memory provider base URL must be an HTTP(S) origin")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("session-memory provider base URL must use http or https")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	return parsed, nil
}

func readBounded(reader io.Reader, maxBytes int64) ([]byte, error) {
	limited := io.LimitReader(reader, maxBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > maxBytes {
		return nil, fmt.Errorf("response exceeds configured limit")
	}
	return body, nil
}

func classifyTransportError(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
		return sessionmemory.PermanentError(sessionmemory.CodeShuttingDown, "session-memory request canceled", nil)
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return sessionmemory.RetryableError(sessionmemory.CodeTimeout, "session-memory provider request timed out", nil)
	}
	var networkErr net.Error
	if errors.As(err, &networkErr) && networkErr.Timeout() {
		return sessionmemory.RetryableError(sessionmemory.CodeTimeout, "session-memory provider request timed out", nil)
	}
	return sessionmemory.RetryableError(sessionmemory.CodeUnavailable, "session-memory provider is unavailable", nil)
}

func classifyHTTPStatus(status int) error {
	switch {
	case status == http.StatusRequestTimeout:
		return sessionmemory.RetryableError(sessionmemory.CodeTimeout, "session-memory provider request timed out", nil)
	case status == http.StatusTooManyRequests || status >= http.StatusInternalServerError:
		return sessionmemory.RetryableError(sessionmemory.CodeUnavailable, "session-memory provider is temporarily unavailable", nil)
	default:
		return sessionmemory.PermanentError(sessionmemory.CodePermanent, fmt.Sprintf("session-memory provider rejected request with HTTP %d", status), nil)
	}
}

func isDuplicateResponse(body []byte) bool {
	var response struct {
		Code string `json:"code"`
		Kind string `json:"kind"`
	}
	if json.Unmarshal(body, &response) != nil {
		return false
	}
	for _, value := range []string{response.Code, response.Kind} {
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "duplicate", "already_exists", "already-exists", "idempotent_replay", "idempotent-replay":
			return true
		}
	}
	return false
}
