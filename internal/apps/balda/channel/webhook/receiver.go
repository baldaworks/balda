package webhook

import (
	"context"
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

	"github.com/baldaworks/balda/internal/apps/balda/webhookcmd"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

const (
	statusAccepted  = "accepted"
	statusDelivered = "delivered"
	statusError     = "error"

	codeInvalidMethod       = "invalid_method"
	codeRouteNotFound       = "route_not_found"
	codeUnauthorized        = "unauthorized"
	codeInvalidPayload      = "invalid_payload"
	codeDestinationNotFound = "destination_not_found"
	codeQueueFull           = "queue_full"
	codeDispatchFailed      = "dispatch_failed"

	messageCouldNotAccept  = "could not accept request"
	messageTemporarilyBusy = "temporarily busy"
)

// Ingress is the consuming port for route policy and durable admission.
type Ingress interface {
	PrepareExternal(ctx context.Context, path string, headers map[string]string) (webhookcmd.PreparedRoute, error)
	Admit(ctx context.Context, route webhookcmd.PreparedRoute, inbound webhookcmd.Inbound) (webhookcmd.Result, error)
}

// DeliveryReceipts reads provider receipts from the durable delivery outbox.
type DeliveryReceipts interface {
	SentFinalDelivery(ctx context.Context, jobID string) (string, bool, error)
}

// Receiver receives inbound HTTP webhook events and dispatches normalized input.
type Receiver struct {
	listenAddr       string
	routes           map[string]route
	ingress          Ingress
	deliveryReceipts DeliveryReceipts
	logger           zerolog.Logger

	metrics metrics

	mu       sync.Mutex
	server   *http.Server
	listener net.Listener
	started  bool
}

// SetDeliveryReceipts supplies the outbox used by ack_on_delivery routes.
func (r *Receiver) SetDeliveryReceipts(store DeliveryReceipts) {
	r.deliveryReceipts = store
}

// NewReceiver creates a new inbound webhook HTTP receiver.
func NewReceiver(cfg Config, ingress Ingress, logger zerolog.Logger) (*Receiver, error) {
	normalized, err := normalizeConfig(cfg)
	if err != nil {
		return nil, err
	}

	receiver := &Receiver{
		listenAddr: normalized.ListenAddr,
		routes:     normalized.Routes,
		ingress:    ingress,
		logger:     logger.With().Str("component", "balda.channel.webhook").Logger(),
	}

	if receiver.ingress == nil {
		return nil, fmt.Errorf("webhook application ingress is required")
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
	if r == nil {
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
	if r == nil {
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
	requestID := "inbound-" + uuid.NewString()
	if req.Method != http.MethodPost {
		r.writeError(w, requestID, &httpError{
			status:  http.StatusMethodNotAllowed,
			code:    codeInvalidMethod,
			message: messageCouldNotAccept,
		})
		return
	}

	headers := make(map[string]string, len(req.Header))
	for name, values := range req.Header {
		if len(values) > 0 {
			headers[name] = values[0]
		}
	}
	rt, err := r.ingress.PrepareExternal(req.Context(), req.URL.Path, headers)
	if err != nil {
		r.writeIngressError(w, requestID, err)
		return
	}
	// A route may use X-Request-Id as its credential. Its value must never
	// become a response ID, template input, log field, or durable admission ID.
	if !strings.EqualFold(rt.AuthHeader, "X-Request-Id") {
		if supplied := strings.TrimSpace(req.Header.Get("X-Request-Id")); supplied != "" {
			requestID = supplied
		}
	}
	defer func() { _ = req.Body.Close() }()
	bodyBytes, err := io.ReadAll(io.LimitReader(req.Body, MaxBodyBytes+1))
	if err == nil && len(bodyBytes) > MaxBodyBytes {
		err = fmt.Errorf("request body exceeds %d bytes", MaxBodyBytes)
	}
	if err != nil {
		r.metrics.invalid.Add(1)
		r.writeError(w, requestID, &httpError{status: http.StatusBadRequest,
			code: codeInvalidPayload, message: messageCouldNotAccept, cause: err})
		return
	}
	result, err := r.ingress.Admit(req.Context(), rt, webhookcmd.Inbound{
		RequestID: requestID, Path: req.URL.Path, Method: req.Method,
		RawBody: string(bodyBytes), Headers: headers,
	})
	if err != nil {
		r.writeIngressError(w, requestID, err)
		return
	}
	r.metrics.accepted.Add(1)
	r.logger.Info().
		Str("request_id", requestID).
		Str("route", rt.Name).
		Str("path", rt.Path).
		Str("stream", result.Stream).
		Uint64("sequence", result.Sequence).
		Str("job_id", result.JobID).
		Msg("inbound webhook accepted")

	statusCode := http.StatusAccepted
	status := statusAccepted
	providerMessageID := ""
	if rt.AckOnDelivery {
		if r.deliveryReceipts == nil || result.JobID == "" {
			r.writeError(w, requestID, &httpError{status: http.StatusServiceUnavailable, code: codeDispatchFailed, message: messageTemporarilyBusy,
				cause: fmt.Errorf("delivery receipt lookup unavailable")})
			return
		}
		var sent bool
		providerMessageID, sent, err = r.deliveryReceipts.SentFinalDelivery(req.Context(), result.JobID)
		if err != nil {
			r.writeError(w, requestID, &httpError{status: http.StatusServiceUnavailable, code: codeDispatchFailed, message: messageTemporarilyBusy, cause: err})
			return
		}
		if sent {
			statusCode = http.StatusOK
			status = statusDelivered
		}
	}
	writeJSON(w, statusCode, acceptedResponse{
		Status:             status,
		Accepted:           true,
		RequestID:          requestID,
		MessageID:          result.MessageID,
		Duplicate:          result.Duplicate,
		JobID:              result.JobID,
		ProviderMessageID:  providerMessageID,
		DeliveryAckEnabled: rt.AckOnDelivery,
	})
}

func (r *Receiver) writeIngressError(w http.ResponseWriter, requestID string, err error) {
	var response *httpError
	switch {
	case errors.Is(err, webhookcmd.ErrRouteNotFound):
		r.metrics.notFound.Add(1)
		response = &httpError{status: http.StatusNotFound, code: codeRouteNotFound, message: messageCouldNotAccept, cause: err}
	case errors.Is(err, webhookcmd.ErrUnauthorized):
		r.metrics.unauthorized.Add(1)
		response = &httpError{status: http.StatusUnauthorized, code: codeUnauthorized, message: messageCouldNotAccept, cause: err}
	case webhookcmd.IsInvalidRequest(err):
		r.metrics.invalid.Add(1)
		response = &httpError{status: http.StatusBadRequest, code: codeInvalidPayload, message: messageCouldNotAccept, cause: err}
	case webhookcmd.IsTargetNotFound(err):
		r.metrics.notFound.Add(1)
		response = &httpError{status: http.StatusNotFound, code: codeDestinationNotFound, message: messageCouldNotAccept, cause: err}
	case webhookcmd.IsQueueFull(err):
		r.metrics.queueFull.Add(1)
		response = &httpError{status: http.StatusTooManyRequests, code: codeQueueFull, message: messageTemporarilyBusy, cause: err}
	default:
		r.metrics.dispatchErr.Add(1)
		response = &httpError{status: http.StatusServiceUnavailable, code: codeDispatchFailed, message: messageTemporarilyBusy, cause: err}
	}
	r.writeError(w, requestID, response)
}

type metrics struct {
	accepted     atomic.Uint64
	invalid      atomic.Uint64
	notFound     atomic.Uint64
	unauthorized atomic.Uint64
	queueFull    atomic.Uint64
	dispatchErr  atomic.Uint64
}

type acceptedResponse struct {
	Status             string `json:"status"`
	Accepted           bool   `json:"accepted"`
	RequestID          string `json:"request_id"`
	MessageID          string `json:"message_id"`
	Duplicate          bool   `json:"duplicate,omitempty"`
	JobID              string `json:"job_id,omitempty"`
	ProviderMessageID  string `json:"provider_message_id,omitempty"`
	DeliveryAckEnabled bool   `json:"delivery_ack_enabled,omitempty"`
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
