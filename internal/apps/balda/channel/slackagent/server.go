package slackagent

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/actorcmd"
	"github.com/baldaworks/balda/internal/apps/balda/ingressapp"
	"github.com/baldaworks/balda/internal/apps/balda/questions"
	baldasession "github.com/baldaworks/balda/internal/apps/balda/session"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/rs/zerolog"
	"go.uber.org/fx"
)

const (
	autoSessionLabel          = "auto"
	webhookMaxBodyBytes       = 1 << 20
	webhookReadHeaderTimeout  = 5 * time.Second
	webhookReadTimeout        = 10 * time.Second
	webhookWriteTimeout       = 10 * time.Second
	webhookIdleTimeout        = 30 * time.Second
	webhookProcessingTimeout  = 5 * time.Minute
	webhookMaxConcurrentTasks = 16
	signatureVersion          = "v0"
)

type Config struct {
	Enabled       bool
	ListenAddr    string
	EventsPath    string
	SigningSecret string
}

type Server struct {
	sessionManager  *baldasession.Manager
	actorDispatcher actortransport.Dispatcher
	questionService *questions.Service
	config          Config
	logger          zerolog.Logger

	server     *http.Server
	ln         net.Listener
	processSem chan struct{}
	processWG  sync.WaitGroup
}

type serverParams struct {
	fx.In

	SessionManager *baldasession.Manager
	Dispatcher     actortransport.Dispatcher
	Question       *questions.Service `optional:"true"`
	Config         Config
	Logger         zerolog.Logger
}

func NewServer(params serverParams) *Server {
	return &Server{
		sessionManager:  params.SessionManager,
		actorDispatcher: params.Dispatcher,
		questionService: params.Question,
		config:          params.Config,
		logger:          params.Logger.With().Str("component", "balda.channel.slackagent").Logger(),
		processSem:      make(chan struct{}, webhookMaxConcurrentTasks),
	}
}

func (h *Server) Start(ctx context.Context) error { return h.onStart(ctx) }
func (h *Server) Stop(ctx context.Context) error  { return h.onStop(ctx) }

func (h *Server) onStart(context.Context) error {
	if !h.config.Enabled {
		h.logger.Debug().Msg("slackagent disabled; skipping server start")
		return nil
	}
	eventsPath, err := normalizePath(h.config.EventsPath, "/slack/agent/events")
	if err != nil {
		return err
	}
	listenAddr := strings.TrimSpace(h.config.ListenAddr)
	if listenAddr == "" {
		listenAddr = ":8092"
	}
	mux := http.NewServeMux()
	mux.HandleFunc(eventsPath, h.handleEvents)
	h.server = &http.Server{
		Addr:              listenAddr,
		Handler:           mux,
		ReadHeaderTimeout: webhookReadHeaderTimeout,
		ReadTimeout:       webhookReadTimeout,
		WriteTimeout:      webhookWriteTimeout,
		IdleTimeout:       webhookIdleTimeout,
	}
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return fmt.Errorf("listen slackagent endpoint on %q: %w", listenAddr, err)
	}
	h.ln = ln
	go func() {
		h.logger.Info().Str("addr", listenAddr).Str("events_path", eventsPath).Msg("slackagent http server starting")
		if err := h.server.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			h.logger.Error().Err(err).Msg("slackagent http server error")
		}
	}()
	return nil
}

func (h *Server) onStop(ctx context.Context) error {
	if h.server == nil {
		return nil
	}
	if err := h.server.Shutdown(ctx); err != nil {
		return fmt.Errorf("shutdown slackagent server: %w", err)
	}
	done := make(chan struct{})
	go func() {
		h.processWG.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("wait for slackagent processing: %w", ctx.Err())
	}
}

func (h *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	body, ok := h.readAndVerifyRequest(w, r)
	if !ok {
		return
	}
	env, err := DecodeIngressEnvelope(body, time.Now())
	if err != nil {
		h.logger.Warn().Err(err).Msg("failed to decode slackagent event payload")
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if env.Type == "url_verification" {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(env.Challenge))
		return
	}
	release, ok := h.acquireProcessSlot()
	if !ok {
		http.Error(w, "busy", http.StatusServiceUnavailable)
		return
	}
	defer release()
	settlement, err := h.processEvent(context.WithoutCancel(r.Context()), env)
	if err != nil && settlement.Outcome == turncmd.InboundRetry {
		http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (h *Server) readAndVerifyRequest(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return nil, false
	}
	defer func() { _ = r.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(r.Body, webhookMaxBodyBytes+1))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return nil, false
	}
	if len(body) > webhookMaxBodyBytes {
		http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
		return nil, false
	}
	if err := verifySignature(strings.TrimSpace(h.config.SigningSecret), r.Header.Get("X-Slack-Request-Timestamp"), r.Header.Get("X-Slack-Signature"), body, time.Now()); err != nil {
		h.logger.Warn().Err(err).Msg("slackagent signature verification failed")
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return nil, false
	}
	return body, true
}

func (h *Server) acquireProcessSlot() (func(), bool) {
	if h.processSem == nil {
		return func() {}, true
	}
	select {
	case h.processSem <- struct{}{}:
		return func() { <-h.processSem }, true
	default:
		return nil, false
	}
}

func (h *Server) processEvent(requestCtx context.Context, env IngressEnvelope) (turncmd.InboundSettlement, error) {
	ctx, cancel := context.WithTimeout(requestCtx, webhookProcessingTimeout)
	defer cancel()
	if env.IgnoreEvent {
		return terminalInbound(), nil
	}
	locator := env.Locator
	if handled, err := h.handleQuestionReply(ctx, env); err != nil {
		h.logger.Warn().Err(err).Str("address_key", locator.AddressKey).Msg("failed to handle slackagent question reply")
		return retryInbound(), err
	} else if handled {
		return terminalInbound(), nil
	}
	service, err := ingressapp.NewWithLogger(
		ingressapp.AuthorizerFunc(func(context.Context, ingressapp.InboundContext) (ingressapp.Authorization, error) {
			return ingressapp.Authorization{Allowed: true}, nil
		}),
		ingressapp.SessionPreparerFunc(h.prepareSession),
		h.actorDispatcher,
		h.logger,
	)
	if err != nil {
		h.logger.Warn().Err(err).Str("address_key", locator.AddressKey).Msg("failed to construct slackagent ingress")
		return retryInbound(), err
	}
	result, err := service.Process(ctx, env.Inbound)
	if err == nil || result.Settlement.Outcome != turncmd.InboundRetry {
		return result.Settlement, err
	}
	if actorcmd.IsCommandQueueFull(err) {
		h.logger.Warn().Err(err).Str("session_id", locator.SessionID).Msg("slackagent session command queue full")
		return result.Settlement, err
	}
	h.logger.Warn().Err(err).Str("session_id", locator.SessionID).Msg("failed to dispatch slackagent session turn")
	return result.Settlement, err
}

func (h *Server) prepareSession(ctx context.Context, inbound ingressapp.InboundContext) (ingressapp.SessionPreparation, error) {
	ts, err := h.getOrCreateSession(ctx, inboundContextLocator(inbound), inbound.UserID)
	if err != nil {
		return ingressapp.SessionPreparation{}, err
	}
	return ingressapp.SessionPreparation{
		Ready:           true,
		UserID:          ts.GetUserID(),
		RequesterUserID: inbound.UserID,
		AgentSessionID:  ts.GetAgentSessionID(),
	}, nil
}

func (h *Server) getOrCreateSession(ctx context.Context, locator baldasession.SessionLocator, subject string) (*baldasession.TopicSession, error) {
	if existing, _ := h.sessionManager.GetSession(locator); existing != nil {
		return existing, nil
	}
	ts, err := h.sessionManager.RestoreSession(ctx, baldasession.SessionContext{Locator: locator, UserID: subject})
	if err == nil && ts != nil {
		return ts, nil
	}
	if err != nil && !errors.Is(err, baldasession.ErrNoPersistedSession) {
		return nil, err
	}
	return h.sessionManager.EnsureSession(ctx, baldasession.SessionContext{Locator: locator, UserID: subject}, autoSessionLabel)
}

func (h *Server) handleQuestionReply(ctx context.Context, env IngressEnvelope) (bool, error) {
	if h == nil || h.questionService == nil || !env.HasReply {
		return false, nil
	}
	result, err := h.questionService.ResolveReplyDetailed(ctx, env.Reply)
	if err != nil || !result.Matched {
		return result.Matched, err
	}
	if !result.Settled {
		return true, nil
	}
	if err := dispatchQuestionContinuation(ctx, h.actorDispatcher, result.Continuation); err != nil {
		return true, err
	}
	return true, nil
}

func normalizePath(path string, fallback string) (string, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return fallback, nil
	}
	if !strings.HasPrefix(trimmed, "/") {
		return "", fmt.Errorf("slack path %q must start with /", path)
	}
	return trimmed, nil
}

func verifySignature(secret, timestamp, signature string, body []byte, now time.Time) error {
	trimmedSecret := strings.TrimSpace(secret)
	if trimmedSecret == "" {
		return fmt.Errorf("slack signing secret is required")
	}
	ts, err := strconv.ParseInt(strings.TrimSpace(timestamp), 10, 64)
	if err != nil {
		return fmt.Errorf("invalid slack request timestamp")
	}
	requestTime := time.Unix(ts, 0)
	if now.Sub(requestTime) > 5*time.Minute || requestTime.Sub(now) > 5*time.Minute {
		return fmt.Errorf("stale slack request timestamp")
	}
	base := signatureVersion + ":" + strings.TrimSpace(timestamp) + ":" + string(body)
	mac := hmac.New(sha256.New, []byte(trimmedSecret))
	_, _ = mac.Write([]byte(base))
	expected := signatureVersion + "=" + hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(expected), []byte(strings.TrimSpace(signature))) {
		return fmt.Errorf("slack request signature mismatch")
	}
	return nil
}

func dispatchQuestionContinuation(ctx context.Context, dispatcher actortransport.Dispatcher, env actorlayer.Envelope) error {
	if dispatcher == nil {
		return actorlayer.TransientError(fmt.Errorf("runtime is unavailable"))
	}
	_, err := dispatcher.Dispatch(ctx, env)
	return err
}

func terminalInbound() turncmd.InboundSettlement {
	return turncmd.InboundSettlement{Outcome: turncmd.InboundTerminal}
}

func retryInbound() turncmd.InboundSettlement {
	return turncmd.InboundSettlement{Outcome: turncmd.InboundRetry}
}

func inboundContextLocator(inbound ingressapp.InboundContext) baldasession.SessionLocator {
	return baldasession.SessionLocator{
		ChannelType: inbound.ChannelType,
		AddressKey:  inbound.AddressKey,
		AddressJSON: inbound.AddressJSON,
		SessionID:   inbound.SessionID,
	}
}
