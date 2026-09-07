package webhook

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/webhookapp"
	"github.com/rs/zerolog"
)

const (
	statusAccepted = "accepted"
	statusError    = "error"

	codeInvalidMethod   = "invalid_method"
	codeRouteNotFound   = "route_not_found"
	codeUnauthorized    = "unauthorized"
	codeInvalidPayload  = "invalid_payload"
	codeSessionNotFound = "session_not_found"
	codeQueueFull       = "queue_full"
	codeDispatchFailed  = "dispatch_failed"

	messageCouldNotAccept  = "could not accept request"
	messageTemporarilyBusy = "temporarily busy"
)

// Service defines the application service interface required by the webhook receiver.
type Service interface {
	Accept(ctx context.Context, req webhookapp.Request) (webhookapp.Result, error)
}

// Receiver receives inbound HTTP webhook events and dispatches them via webhookapp.Service.
type Receiver struct {
	enabled    bool
	listenAddr string
	routes     map[string]route
	service    Service
	logger     zerolog.Logger

	metrics metrics

	mu       sync.Mutex
	server   *http.Server
	listener net.Listener
	started  bool
}

// NewReceiver creates a new inbound webhook HTTP receiver.
func NewReceiver(cfg Config, svc Service, logger zerolog.Logger) (*Receiver, error) {
	normalized, err := normalizeConfig(cfg)
	if err != nil {
		return nil, err
	}

	receiver := &Receiver{
		enabled:    normalized.Enabled,
		listenAddr: normalized.ListenAddr,
		routes:     normalized.Routes,
		service:    svc,
		logger:     logger.With().Str("component", "balda.channel.webhook").Logger(),
	}

	if !receiver.enabled {
		return receiver, nil
	}
	if receiver.service == nil {
		return nil, fmt.Errorf("webhook application service is required when webhooks are enabled")
	}

	return receiver, nil
}

// Start begins accepting configured inbound webhook requests.
func (r *Receiver) Start(ctx context.Context) error {
	return r.start(ctx)
}

// Stop gracefully shuts down the inbound webhook receiver.
func (r *Receiver) Stop(ctx context.Context) error {
	return r.stop(ctx)
}

func (r *Receiver) start(_ context.Context) error {
	if r == nil || !r.enabled {
		return nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.started {
		return nil
	}

	listener, err := net.Listen("tcp", r.listenAddr)
	if err != nil {
		return fmt.Errorf("inbound webhook listen on %q: %w", r.listenAddr, err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", r.handleWebhook)

	server := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: ReadHeaderTimeout,
		ReadTimeout:       ReadTimeout,
		WriteTimeout:      WriteTimeout,
		IdleTimeout:       IdleTimeout,
	}

	r.listener = listener
	r.server = server
	r.started = true

	go func() {
		if serveErr := server.Serve(listener); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			r.logger.Error().Err(serveErr).Msg("inbound webhook server terminated with error")
		}
	}()

	r.logger.Info().
		Str("listen_addr", listener.Addr().String()).
		Strs("routes", r.RoutePaths()).
		Msg("inbound webhook server started")
	return nil
}

func (r *Receiver) stop(ctx context.Context) error {
	if r == nil || !r.enabled {
		return nil
	}

	r.mu.Lock()
	if !r.started || r.server == nil {
		r.mu.Unlock()
		return nil
	}
	server := r.server
	r.started = false
	r.server = nil
	r.listener = nil
	r.mu.Unlock()

	shutdownCtx, cancel := context.WithTimeout(ctx, WriteTimeout)
	defer cancel()

	err := server.Shutdown(shutdownCtx)
	if err != nil {
		r.logger.Warn().Err(err).Msg("inbound webhook server graceful shutdown failed")
		return fmt.Errorf("inbound webhook shutdown: %w", err)
	}
	r.logger.Info().Msg("inbound webhook server stopped")
	return nil
}

// RoutePaths returns sorted registered paths for this receiver.
func (r *Receiver) RoutePaths() []string {
	if r == nil || len(r.routes) == 0 {
		return nil
	}
	paths := make([]string, 0, len(r.routes))
	for path := range r.routes {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

func (r *Receiver) handleWebhook(w http.ResponseWriter, req *http.Request) {
	requestID := strings.TrimSpace(req.Header.Get("X-Request-Id"))
	if requestID == "" {
		requestID = fmt.Sprintf("inbound-%d", time.Now().UnixNano())
	}
	if req.Method != http.MethodPost {
		r.writeError(w, requestID, &httpError{
			status:  http.StatusMethodNotAllowed,
			code:    codeInvalidMethod,
			message: messageCouldNotAccept,
		})
		return
	}

	rt, ok := r.routes[req.URL.Path]
	if !ok {
		r.metrics.notFound.Add(1)
		r.writeError(w, requestID, &httpError{
			status:  http.StatusNotFound,
			code:    codeRouteNotFound,
			message: messageCouldNotAccept,
		})
		return
	}
	if authErr := authorizeRequest(req, rt.Auth); authErr != nil {
		r.metrics.unauthorized.Add(1)
		r.writeError(w, requestID, &httpError{
			status:  http.StatusUnauthorized,
			code:    codeUnauthorized,
			message: messageCouldNotAccept,
			cause:   authErr,
		})
		return
	}

	defer func() { _ = req.Body.Close() }()
	bodyBytes, readErr := io.ReadAll(io.LimitReader(req.Body, MaxBodyBytes+1))
	if readErr != nil {
		r.metrics.invalid.Add(1)
		r.writeError(w, requestID, &httpError{
			status:  http.StatusBadRequest,
			code:    codeInvalidPayload,
			message: messageCouldNotAccept,
			cause:   readErr,
		})
		return
	}
	if len(bodyBytes) > MaxBodyBytes {
		r.metrics.invalid.Add(1)
		r.writeError(w, requestID, &httpError{
			status:  http.StatusBadRequest,
			code:    codeInvalidPayload,
			message: messageCouldNotAccept,
			cause:   fmt.Errorf("request body exceeds %d bytes", MaxBodyBytes),
		})
		return
	}
	rawBody := string(bodyBytes)

	headers := make(map[string]string, len(req.Header))
	for name, values := range req.Header {
		if len(values) == 0 {
			headers[name] = ""
			continue
		}
		headers[name] = values[0]
	}

	var promptBuf bytes.Buffer
	renderErr := rt.PromptTemplate.Execute(&promptBuf, templateData{
		RequestID: requestID,
		Path:      req.URL.Path,
		Method:    req.Method,
		RawBody:   rawBody,
		Headers:   headers,
	})
	if renderErr != nil {
		r.metrics.invalid.Add(1)
		r.writeError(w, requestID, &httpError{
			status:  http.StatusBadRequest,
			code:    codeInvalidPayload,
			message: messageCouldNotAccept,
			cause:   renderErr,
		})
		return
	}
	prompt := strings.TrimSpace(promptBuf.String())
	if prompt == "" {
		r.metrics.invalid.Add(1)
		r.writeError(w, requestID, &httpError{
			status:  http.StatusBadRequest,
			code:    codeInvalidPayload,
			message: messageCouldNotAccept,
			cause:   fmt.Errorf("rendered prompt is empty"),
		})
		return
	}

	dedupeBase := strings.TrimSpace(requestID)
	switch rt.Dedupe.Source {
	case DedupeSourceHeader:
		if header := strings.TrimSpace(req.Header.Get(rt.Dedupe.Header)); header != "" {
			dedupeBase = header
		}
	case DedupeSourceBodySHA:
		sum := sha256.Sum256([]byte(rawBody))
		dedupeBase = fmt.Sprintf("%x", sum[:])
	}
	dedupeKey := strings.Join([]string{"webhook", strings.TrimSpace(rt.Name), dedupeBase}, ":")

	result, err := r.service.Accept(req.Context(), webhookapp.Request{
		RequestID: requestID,
		RouteName: rt.Name,
		Prompt:    prompt,
		Target:    rt.Target,
		ReportTo:  rt.ReportTo,
		Mode:      rt.Mode,
		DedupeKey: dedupeKey,
	})
	if err != nil {
		if webhookapp.IsTargetNotFound(err) {
			r.metrics.notFound.Add(1)
			r.writeError(w, requestID, &httpError{
				status:  http.StatusNotFound,
				code:    codeSessionNotFound,
				message: messageCouldNotAccept,
				cause:   err,
			})
			return
		}
		if webhookapp.IsQueueFull(err) {
			r.metrics.queueFull.Add(1)
			r.writeError(w, requestID, &httpError{
				status:  http.StatusTooManyRequests,
				code:    codeQueueFull,
				message: messageTemporarilyBusy,
				cause:   err,
			})
			return
		}
		r.metrics.dispatchErr.Add(1)
		r.writeError(w, requestID, &httpError{
			status:  http.StatusServiceUnavailable,
			code:    codeDispatchFailed,
			message: messageTemporarilyBusy,
			cause:   err,
		})
		return
	}

	r.metrics.accepted.Add(1)
	r.logger.Info().
		Str("request_id", requestID).
		Str("route", rt.Name).
		Str("path", rt.Path).
		Str("session_id", result.Target.Locator.SessionID).
		Str("channel_type", result.Target.Locator.ChannelType).
		Str("address_key", result.Target.Locator.AddressKey).
		Str("mode", rt.Mode).
		Str("dedupe_key", dedupeKey).
		Str("stream", result.Stream).
		Uint64("sequence", result.Sequence).
		Str("job_id", result.JobID).
		Msg("inbound webhook accepted")

	writeJSON(w, http.StatusAccepted, acceptedResponse{
		Status:    statusAccepted,
		Accepted:  true,
		RequestID: requestID,
		MessageID: result.MessageID,
		Duplicate: result.Duplicate,
	})
}

func authorizeRequest(req *http.Request, policy authPolicy) error {
	switch policy.Type {
	case "", AuthTypeNone:
		return nil
	case AuthTypeHeader:
		provided := req.Header.Get(policy.Header)
		if provided == "" {
			return fmt.Errorf("missing authorization header %q", policy.Header)
		}
		if subtle.ConstantTimeCompare([]byte(provided), []byte(policy.Value)) != 1 {
			return fmt.Errorf("invalid authorization header value for %q", policy.Header)
		}
		return nil
	default:
		return fmt.Errorf("unsupported auth type %q", policy.Type)
	}
}

type metrics struct {
	accepted     atomic.Uint64
	invalid      atomic.Uint64
	notFound     atomic.Uint64
	unauthorized atomic.Uint64
	queueFull    atomic.Uint64
	dispatchErr  atomic.Uint64
}

type templateData struct {
	RequestID string
	Path      string
	Method    string
	RawBody   string
	Headers   map[string]string
}

type acceptedResponse struct {
	Status    string `json:"status"`
	Accepted  bool   `json:"accepted"`
	RequestID string `json:"request_id"`
	MessageID string `json:"message_id"`
	Duplicate bool   `json:"duplicate,omitempty"`
}

type errorResponse struct {
	Status    string      `json:"status"`
	RequestID string      `json:"request_id"`
	Error     errorDetail `json:"error"`
}

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type httpError struct {
	status  int
	code    string
	message string
	cause   error
}

func (e *httpError) Error() string {
	if e == nil {
		return ""
	}
	if e.cause != nil {
		return e.cause.Error()
	}
	return e.message
}

func (r *Receiver) writeError(w http.ResponseWriter, requestID string, httpErr *httpError) {
	if httpErr == nil {
		httpErr = &httpError{
			status:  http.StatusInternalServerError,
			code:    codeDispatchFailed,
			message: messageTemporarilyBusy,
		}
	}
	if httpErr.status >= http.StatusInternalServerError {
		r.logger.Error().
			Str("request_id", requestID).
			Str("code", httpErr.code).
			Err(httpErr.cause).
			Msg("inbound webhook request failed")
	} else if httpErr.status >= http.StatusBadRequest {
		r.logger.Warn().
			Str("request_id", requestID).
			Str("code", httpErr.code).
			Err(httpErr.cause).
			Msg("inbound webhook request rejected")
	}

	writeJSON(w, httpErr.status, errorResponse{
		Status:    statusError,
		RequestID: requestID,
		Error: errorDetail{
			Code:    httpErr.code,
			Message: httpErr.message,
		},
	})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
