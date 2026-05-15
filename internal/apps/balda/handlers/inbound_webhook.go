package handlers

import (
	"bytes"
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
	"text/template"
	"time"

	baldachannel "github.com/normahq/balda/internal/apps/balda/channel"
	baldatelegram "github.com/normahq/balda/internal/apps/balda/channel/telegram"
	baldasession "github.com/normahq/balda/internal/apps/balda/session"
	"github.com/rs/zerolog"
	"go.uber.org/fx"
	"google.golang.org/adk/runner"
)

const (
	defaultInboundWebhookListenAddr = "127.0.0.1:8090"

	inboundWebhookReadHeaderTimeout = 5 * time.Second
	inboundWebhookReadTimeout       = 10 * time.Second
	inboundWebhookWriteTimeout      = 30 * time.Second
	inboundWebhookIdleTimeout       = 60 * time.Second
	inboundWebhookMaxBodyBytes      = 1 << 20
)

const (
	inboundWebhookStatusAccepted = "accepted"
	inboundWebhookStatusError    = "error"

	inboundWebhookCodeInvalidMethod   = "invalid_method"
	inboundWebhookCodeRouteNotFound   = "route_not_found"
	inboundWebhookCodeInvalidPayload  = "invalid_payload"
	inboundWebhookCodeSessionNotFound = "session_not_found"
	inboundWebhookCodeQueueFull       = "queue_full"
	inboundWebhookCodeDispatchFailed  = "dispatch_failed"
)

// WebhookLocatorAlias binds a locator alias name to canonical session locator fields.
type WebhookLocatorAlias struct {
	ChannelType string
	AddressKey  string
	AddressJSON string
	SessionID   string
}

// InboundWebhookRouteConfig configures one inbound webhook route.
type InboundWebhookRouteConfig struct {
	Path           string
	ReportTo       string
	PromptTemplate string
}

// InboundWebhookConfig controls inbound webhook routing and dispatch behavior.
type InboundWebhookConfig struct {
	Enabled        bool
	ListenAddr     string
	LocatorAliases map[string]WebhookLocatorAlias
	Routes         map[string]InboundWebhookRouteConfig
}

type inboundWebhookRoute struct {
	Name           string
	Path           string
	Locator        baldasession.SessionLocator
	PromptTemplate *template.Template
}

type inboundWebhookSessionManager interface {
	GetSession(locator baldasession.SessionLocator) (*baldasession.TopicSession, error)
	GetSessionInfo(ctx context.Context, sessionID string) (baldasession.TopicSessionInfo, error)
	RestoreSession(ctx context.Context, sessionCtx baldasession.SessionContext) (*baldasession.TopicSession, error)
}

type inboundTurnExecutor interface {
	runTurnTask(
		ctx context.Context,
		text string,
		r *runner.Runner,
		userID string,
		sessionID string,
		agentSessionID string,
		locator baldasession.SessionLocator,
		messageID int,
		topicID int,
		progressPolicy baldachannel.ProgressPolicy,
	) error
}

type inboundWebhookParams struct {
	fx.In

	LC         fx.Lifecycle
	Config     InboundWebhookConfig
	Sessions   *baldasession.Manager
	Dispatcher *TurnDispatcher
	Balda      *BaldaHandler
	Logger     zerolog.Logger
}

// InboundWebhookReceiver receives inbound webhook events and dispatches them into bound session turns.
type InboundWebhookReceiver struct {
	enabled    bool
	listenAddr string
	routes     map[string]inboundWebhookRoute
	sessions   inboundWebhookSessionManager
	dispatch   turnQueue
	balda      inboundTurnExecutor
	logger     zerolog.Logger

	metrics inboundWebhookMetrics

	mu       sync.Mutex
	server   *http.Server
	listener net.Listener
	started  bool
}

type inboundWebhookMetrics struct {
	accepted    atomic.Uint64
	invalid     atomic.Uint64
	notFound    atomic.Uint64
	queueFull   atomic.Uint64
	dispatchErr atomic.Uint64
}

type inboundWebhookTemplateData struct {
	RequestID string
	Path      string
	Method    string
	RawBody   string
	Headers   map[string]string
}

type inboundWebhookAcceptedResponse struct {
	Status        string `json:"status"`
	RequestID     string `json:"request_id"`
	SessionID     string `json:"session_id"`
	QueuePosition int    `json:"queue_position"`
}

type inboundWebhookErrorResponse struct {
	Status    string                    `json:"status"`
	RequestID string                    `json:"request_id"`
	Error     inboundWebhookErrorDetail `json:"error"`
}

type inboundWebhookErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type inboundWebhookHTTPError struct {
	status  int
	code    string
	message string
	cause   error
}

func (e *inboundWebhookHTTPError) Error() string {
	if e == nil {
		return ""
	}
	if e.cause != nil {
		return e.cause.Error()
	}
	return e.message
}

func newInboundWebhookHTTPError(status int, code, message string, cause error) *inboundWebhookHTTPError {
	return &inboundWebhookHTTPError{
		status:  status,
		code:    code,
		message: message,
		cause:   cause,
	}
}

func NewInboundWebhookReceiver(params inboundWebhookParams) (*InboundWebhookReceiver, error) {
	normalized, err := normalizeInboundWebhookConfig(params.Config)
	if err != nil {
		return nil, err
	}

	receiver := &InboundWebhookReceiver{
		enabled:    normalized.Enabled,
		listenAddr: normalized.ListenAddr,
		routes:     normalized.Routes,
		sessions:   params.Sessions,
		dispatch:   params.Dispatcher,
		balda:      params.Balda,
		logger:     params.Logger.With().Str("component", "balda.inbound_webhook").Logger(),
	}

	if !receiver.enabled {
		return receiver, nil
	}
	if receiver.sessions == nil {
		return nil, fmt.Errorf("balda session manager is required for inbound webhooks")
	}
	if receiver.dispatch == nil {
		return nil, fmt.Errorf("balda turn dispatcher is required for inbound webhooks")
	}
	if receiver.balda == nil {
		return nil, fmt.Errorf("balda handler is required for inbound webhooks")
	}

	params.LC.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			return receiver.start(ctx)
		},
		OnStop: func(ctx context.Context) error {
			return receiver.stop(ctx)
		},
	})

	return receiver, nil
}

type normalizedInboundWebhookConfig struct {
	Enabled    bool
	ListenAddr string
	Routes     map[string]inboundWebhookRoute
}

func normalizeInboundWebhookConfig(cfg InboundWebhookConfig) (normalizedInboundWebhookConfig, error) {
	normalized := normalizedInboundWebhookConfig{
		Enabled:    cfg.Enabled,
		ListenAddr: normalizeInboundWebhookListenAddr(cfg.ListenAddr),
		Routes:     make(map[string]inboundWebhookRoute),
	}
	if !cfg.Enabled {
		return normalized, nil
	}

	if len(cfg.Routes) == 0 {
		return normalizedInboundWebhookConfig{}, fmt.Errorf("balda.inbound_webhooks.routes is required when inbound webhooks are enabled")
	}

	expectedChannelType := baldatelegram.NewLocator(0, 0).ChannelType
	aliases := make(map[string]baldasession.SessionLocator, len(cfg.LocatorAliases))
	for rawAlias, rawLocator := range cfg.LocatorAliases {
		alias := strings.TrimSpace(rawAlias)
		if alias == "" {
			return normalizedInboundWebhookConfig{}, fmt.Errorf("balda.locators alias is required")
		}
		if _, exists := aliases[alias]; exists {
			return normalizedInboundWebhookConfig{}, fmt.Errorf("duplicate balda.locators alias %q", alias)
		}

		locator, err := baldasession.NewSessionLocator(
			strings.TrimSpace(rawLocator.ChannelType),
			strings.TrimSpace(rawLocator.AddressKey),
			strings.TrimSpace(rawLocator.AddressJSON),
			strings.TrimSpace(rawLocator.SessionID),
		)
		if err != nil {
			return normalizedInboundWebhookConfig{}, fmt.Errorf("invalid balda.locators.%s: %w", alias, err)
		}
		if locator.ChannelType != expectedChannelType {
			return normalizedInboundWebhookConfig{}, fmt.Errorf("balda.locators.%s has unsupported channel_type %q", alias, locator.ChannelType)
		}
		if _, ok, err := baldatelegram.DecodeLocator(locator); err != nil || !ok {
			if err != nil {
				return normalizedInboundWebhookConfig{}, fmt.Errorf("invalid balda.locators.%s: %w", alias, err)
			}
			return normalizedInboundWebhookConfig{}, fmt.Errorf("invalid balda.locators.%s: unsupported channel_type %q", alias, locator.ChannelType)
		}
		aliases[alias] = locator
	}

	seenPaths := make(map[string]string, len(cfg.Routes))
	for rawName, rawRoute := range cfg.Routes {
		routeName := strings.TrimSpace(rawName)
		if routeName == "" {
			return normalizedInboundWebhookConfig{}, fmt.Errorf("balda.inbound_webhooks.routes key is required")
		}

		path, err := normalizeInboundWebhookPath(rawRoute.Path)
		if err != nil {
			return normalizedInboundWebhookConfig{}, fmt.Errorf("balda.inbound_webhooks.routes.%s.path: %w", routeName, err)
		}
		if existingName, exists := seenPaths[path]; exists {
			return normalizedInboundWebhookConfig{}, fmt.Errorf("balda.inbound_webhooks.routes.%s.path duplicates route %q", routeName, existingName)
		}
		seenPaths[path] = routeName

		reportTo := strings.TrimSpace(rawRoute.ReportTo)
		if reportTo == "" {
			return normalizedInboundWebhookConfig{}, fmt.Errorf("balda.inbound_webhooks.routes.%s.report_to is required", routeName)
		}
		locator, ok := aliases[reportTo]
		if !ok {
			return normalizedInboundWebhookConfig{}, fmt.Errorf("balda.inbound_webhooks.routes.%s.report_to %q references undefined balda.locators key", routeName, reportTo)
		}

		templateText := strings.TrimSpace(rawRoute.PromptTemplate)
		if templateText == "" {
			return normalizedInboundWebhookConfig{}, fmt.Errorf("balda.inbound_webhooks.routes.%s.prompt_template is required", routeName)
		}
		tmpl, err := template.New("inbound_webhook." + routeName).Option("missingkey=error").Parse(templateText)
		if err != nil {
			return normalizedInboundWebhookConfig{}, fmt.Errorf("invalid balda.inbound_webhooks.routes.%s.prompt_template: %w", routeName, err)
		}

		normalized.Routes[path] = inboundWebhookRoute{
			Name:           routeName,
			Path:           path,
			Locator:        locator,
			PromptTemplate: tmpl,
		}
	}

	return normalized, nil
}

func normalizeInboundWebhookListenAddr(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return defaultInboundWebhookListenAddr
	}
	return trimmed
}

func normalizeInboundWebhookPath(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("path is required")
	}
	if !strings.HasPrefix(trimmed, "/") {
		trimmed = "/" + trimmed
	}
	return trimmed, nil
}

func (r *InboundWebhookReceiver) start(_ context.Context) error {
	if !r.enabled {
		return nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.started {
		return nil
	}

	listener, err := net.Listen("tcp", r.listenAddr)
	if err != nil {
		return fmt.Errorf("listen inbound webhook on %q: %w", r.listenAddr, err)
	}

	server := &http.Server{
		Handler:           http.HandlerFunc(r.handleInboundWebhook),
		ReadHeaderTimeout: inboundWebhookReadHeaderTimeout,
		ReadTimeout:       inboundWebhookReadTimeout,
		WriteTimeout:      inboundWebhookWriteTimeout,
		IdleTimeout:       inboundWebhookIdleTimeout,
	}

	r.listener = listener
	r.server = server
	r.started = true

	go func() {
		if serveErr := server.Serve(listener); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			r.logger.Error().Err(serveErr).Msg("inbound webhook server failed")
		}
	}()

	r.logger.Info().
		Str("listen_addr", listener.Addr().String()).
		Str("paths", strings.Join(r.routePaths(), ", ")).
		Int("routes", len(r.routes)).
		Msg("inbound webhook server started")
	return nil
}

func (r *InboundWebhookReceiver) stop(ctx context.Context) error {
	r.mu.Lock()
	server := r.server
	r.server = nil
	r.listener = nil
	r.started = false
	r.mu.Unlock()

	if server == nil {
		return nil
	}
	if err := server.Shutdown(ctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("shutdown inbound webhook server: %w", err)
	}
	return nil
}

func (r *InboundWebhookReceiver) routePaths() []string {
	paths := make([]string, 0, len(r.routes))
	for path := range r.routes {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

func (r *InboundWebhookReceiver) handleInboundWebhook(w http.ResponseWriter, req *http.Request) {
	requestID := requestIDFromInboundWebhookRequest(req)
	if req.Method != http.MethodPost {
		r.writeInboundWebhookError(w, requestID, newInboundWebhookHTTPError(
			http.StatusMethodNotAllowed,
			inboundWebhookCodeInvalidMethod,
			"method must be POST",
			nil,
		))
		return
	}

	route, ok := r.routes[req.URL.Path]
	if !ok {
		r.metrics.notFound.Add(1)
		r.writeInboundWebhookError(w, requestID, newInboundWebhookHTTPError(
			http.StatusNotFound,
			inboundWebhookCodeRouteNotFound,
			fmt.Sprintf("no inbound webhook route for path %q", req.URL.Path),
			nil,
		))
		return
	}

	rawBody, readErr := readInboundWebhookBody(req.Body)
	if readErr != nil {
		r.metrics.invalid.Add(1)
		r.writeInboundWebhookError(w, requestID, newInboundWebhookHTTPError(
			http.StatusBadRequest,
			inboundWebhookCodeInvalidPayload,
			"invalid request body",
			readErr,
		))
		return
	}

	prompt, renderErr := renderInboundWebhookPrompt(route, inboundWebhookTemplateData{
		RequestID: requestID,
		Path:      req.URL.Path,
		Method:    req.Method,
		RawBody:   rawBody,
		Headers:   inboundWebhookHeaders(req.Header),
	})
	if renderErr != nil {
		r.metrics.invalid.Add(1)
		r.writeInboundWebhookError(w, requestID, newInboundWebhookHTTPError(
			http.StatusBadRequest,
			inboundWebhookCodeInvalidPayload,
			"failed to render prompt template",
			renderErr,
		))
		return
	}

	topicID, ts, resolveErr := r.resolveInboundWebhookSession(req.Context(), route.Locator)
	if resolveErr != nil {
		r.metrics.notFound.Add(1)
		r.writeInboundWebhookError(w, requestID, resolveErr)
		return
	}

	position, enqueueErr := r.dispatch.Enqueue(TurnTask{
		SessionID: ts.GetSessionID(),
		Run: func(runCtx context.Context) error {
			if _, getErr := r.sessions.GetSession(route.Locator); getErr != nil {
				r.logger.Debug().
					Str("request_id", requestID).
					Str("session_id", route.Locator.SessionID).
					Msg("dropping inbound webhook turn for inactive session")
				return nil
			}

			return r.balda.runTurnTask(
				runCtx,
				prompt,
				ts.GetRunner(),
				ts.GetUserID(),
				ts.GetSessionID(),
				ts.GetAgentSessionID(),
				route.Locator,
				0,
				topicID,
				inboundWebhookProgressPolicy(),
			)
		},
	})
	if enqueueErr != nil {
		if errors.Is(enqueueErr, ErrTurnQueueFull) {
			r.metrics.queueFull.Add(1)
			r.writeInboundWebhookError(w, requestID, newInboundWebhookHTTPError(
				http.StatusTooManyRequests,
				inboundWebhookCodeQueueFull,
				"turn queue is full",
				enqueueErr,
			))
			return
		}

		r.metrics.dispatchErr.Add(1)
		r.writeInboundWebhookError(w, requestID, newInboundWebhookHTTPError(
			http.StatusInternalServerError,
			inboundWebhookCodeDispatchFailed,
			"failed to dispatch inbound turn",
			enqueueErr,
		))
		return
	}

	r.metrics.accepted.Add(1)
	r.logger.Info().
		Str("request_id", requestID).
		Str("route", route.Name).
		Str("path", route.Path).
		Str("session_id", route.Locator.SessionID).
		Str("channel_type", route.Locator.ChannelType).
		Str("address_key", route.Locator.AddressKey).
		Int("queue_position", position).
		Msg("inbound webhook accepted")

	writeInboundWebhookJSON(w, http.StatusAccepted, inboundWebhookAcceptedResponse{
		Status:        inboundWebhookStatusAccepted,
		RequestID:     requestID,
		SessionID:     route.Locator.SessionID,
		QueuePosition: position,
	})
}

func requestIDFromInboundWebhookRequest(req *http.Request) string {
	requestID := strings.TrimSpace(req.Header.Get("X-Request-Id"))
	if requestID != "" {
		return requestID
	}
	return fmt.Sprintf("inbound-%d", time.Now().UnixNano())
}

func readInboundWebhookBody(body io.ReadCloser) (string, error) {
	defer func() { _ = body.Close() }()

	payload, err := io.ReadAll(io.LimitReader(body, inboundWebhookMaxBodyBytes+1))
	if err != nil {
		return "", err
	}
	if len(payload) > inboundWebhookMaxBodyBytes {
		return "", fmt.Errorf("request body exceeds %d bytes", inboundWebhookMaxBodyBytes)
	}
	return string(payload), nil
}

func inboundWebhookHeaders(header http.Header) map[string]string {
	out := make(map[string]string, len(header))
	for name, values := range header {
		if len(values) == 0 {
			out[name] = ""
			continue
		}
		out[name] = values[0]
	}
	return out
}

func renderInboundWebhookPrompt(route inboundWebhookRoute, data inboundWebhookTemplateData) (string, error) {
	var buf bytes.Buffer
	if err := route.PromptTemplate.Execute(&buf, data); err != nil {
		return "", err
	}
	prompt := strings.TrimSpace(buf.String())
	if prompt == "" {
		return "", fmt.Errorf("rendered prompt is empty")
	}
	return prompt, nil
}

func (r *InboundWebhookReceiver) resolveInboundWebhookSession(
	ctx context.Context,
	locator baldasession.SessionLocator,
) (int, *baldasession.TopicSession, *inboundWebhookHTTPError) {
	ts, err := r.sessions.GetSession(locator)
	if err != nil {
		info, infoErr := r.sessions.GetSessionInfo(ctx, locator.SessionID)
		if infoErr != nil {
			return 0, nil, newInboundWebhookHTTPError(
				http.StatusNotFound,
				inboundWebhookCodeSessionNotFound,
				fmt.Sprintf("session %q not found", locator.SessionID),
				infoErr,
			)
		}
		userID := strings.TrimSpace(info.UserID)
		if userID == "" {
			return 0, nil, newInboundWebhookHTTPError(
				http.StatusNotFound,
				inboundWebhookCodeSessionNotFound,
				fmt.Sprintf("session %q has no user id for restore", locator.SessionID),
				nil,
			)
		}
		ts, err = r.sessions.RestoreSession(ctx, baldasession.SessionContext{
			Locator: locator,
			UserID:  userID,
		})
		if err != nil {
			return 0, nil, newInboundWebhookHTTPError(
				http.StatusNotFound,
				inboundWebhookCodeSessionNotFound,
				fmt.Sprintf("session %q restore failed", locator.SessionID),
				err,
			)
		}
	}

	address, ok, decodeErr := baldatelegram.DecodeLocator(locator)
	if decodeErr != nil || !ok {
		if decodeErr == nil {
			decodeErr = fmt.Errorf("unsupported channel type %q", locator.ChannelType)
		}
		return 0, nil, newInboundWebhookHTTPError(
			http.StatusBadRequest,
			inboundWebhookCodeInvalidPayload,
			"invalid locator address payload",
			decodeErr,
		)
	}

	return address.TopicID, ts, nil
}

func (r *InboundWebhookReceiver) writeInboundWebhookError(
	w http.ResponseWriter,
	requestID string,
	handlerErr *inboundWebhookHTTPError,
) {
	if handlerErr == nil {
		handlerErr = newInboundWebhookHTTPError(
			http.StatusInternalServerError,
			inboundWebhookCodeDispatchFailed,
			"internal error",
			nil,
		)
	}

	evt := r.logger.Warn().
		Str("request_id", requestID).
		Str("error_code", handlerErr.code).
		Int("status_code", handlerErr.status)
	if handlerErr.cause != nil {
		evt = evt.Err(handlerErr.cause)
	}
	evt.Msg("inbound webhook rejected")

	writeInboundWebhookJSON(w, handlerErr.status, inboundWebhookErrorResponse{
		Status:    inboundWebhookStatusError,
		RequestID: requestID,
		Error: inboundWebhookErrorDetail{
			Code:    handlerErr.code,
			Message: handlerErr.message,
		},
	})
}

func writeInboundWebhookJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		return
	}
}

func inboundWebhookProgressPolicy() baldachannel.ProgressPolicy {
	return baldachannel.ProgressPolicy{
		Typing:   false,
		Thinking: false,
	}
}
