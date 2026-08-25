package zulip

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	baldaexecution "github.com/baldaworks/balda/internal/apps/balda/actorcmd"
	"github.com/baldaworks/balda/internal/apps/balda/auth"
	"github.com/baldaworks/balda/internal/apps/balda/automode"
		"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/baldaworks/balda/internal/apps/balda/goalkeepercmd"
	"github.com/baldaworks/balda/internal/apps/balda/ingressapp"
	baldajobs "github.com/baldaworks/balda/internal/apps/balda/jobs"
	"github.com/baldaworks/balda/internal/apps/balda/locatorref"
	"github.com/baldaworks/balda/internal/apps/balda/memory"
	baldasession "github.com/baldaworks/balda/internal/apps/balda/session"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"go.uber.org/fx"
)

var zulipHandlerActorAddress = actorlayer.ActorAddress{Target: "channel", Key: "zulip"}

const (
	zulipWebhookMaxBodyBytes       = 1 << 20
	zulipWebhookReadHeaderTimeout  = 5 * time.Second
	zulipWebhookReadTimeout        = 10 * time.Second
	zulipWebhookWriteTimeout       = 10 * time.Second
	zulipWebhookIdleTimeout        = 30 * time.Second
	zulipWebhookProcessingTimeout  = 5 * time.Minute
	zulipWebhookMaxConcurrentTasks = 16
)

// ZulipBaldaHandler handles inbound Zulip webhook messages.
type ZulipBaldaHandler struct {
	ownerStore        *auth.OwnerStore
	inviteStore       *auth.InviteStore
	collaboratorStore *auth.CollaboratorStore
	channelAuth       *auth.ChannelAuthService
	sessionManager    zulipSessionManager
	actorDispatcher   actortransport.Dispatcher
	goalJobs          goalJobService
	memoryStore       *memory.Store
	authToken         string
	baldaProviderName string
	webhookToken      string
	listenAddr        string
	webhookPath       string
	enabled           bool
	goalMaxIterations int
	autoMaxTurns      int
	logger            zerolog.Logger

	mu         sync.RWMutex
	ownerID    int64
	server     *http.Server
	ln         net.Listener
	processSem chan struct{}
	processWG  sync.WaitGroup
}

type zulipSessionManager interface {
	CreateSession(ctx context.Context, sessionCtx baldasession.SessionContext, agentName string) error
	EnsureSession(ctx context.Context, sessionCtx baldasession.SessionContext, agentName string) (*baldasession.TopicSession, error)
	GetAgentMetadata(agentName string) baldasession.AgentMetadata
	GetSession(locator baldasession.SessionLocator) (*baldasession.TopicSession, error)
	GetSessionInfo(ctx context.Context, sessionID string) (baldasession.TopicSessionInfo, error)
	RuntimeStateValue(ctx context.Context, locator baldasession.SessionLocator, key string) (any, bool, error)
	UpdateRuntimeState(ctx context.Context, locator baldasession.SessionLocator, state map[string]any) error
	RestoreSession(ctx context.Context, sessionCtx baldasession.SessionContext) (*baldasession.TopicSession, error)
	BaldaProviderID() string
	ResetSession(ctx context.Context, locator baldasession.SessionLocator) error
	TakeStartupNotice(sessionID string) string
}

type zulipBaldaHandlerParams struct {
	fx.In

	OwnerStore        *auth.OwnerStore
	InviteStore       *auth.InviteStore
	CollaboratorStore *auth.CollaboratorStore
	ChannelAuth       *auth.ChannelAuthService
	SessionManager    *baldasession.Manager
	Dispatcher        actortransport.Dispatcher
	GoalJobs          *baldajobs.JobLifecycleService `optional:"true"`
	MemoryStore       *memory.Store
	AuthToken         string `name:"balda_auth_token"`
	BaldaProviderID   string `name:"balda_provider"`
	ZulipWebhookToken string `name:"balda_zulip_webhook_token"`
	ZulipListenAddr   string `name:"balda_zulip_listen_addr"`
	ZulipWebhookPath  string `name:"balda_zulip_webhook_path"`
	ZulipEnabled      bool   `name:"balda_zulip_webhook_enabled"`
	MaxIterations     int    `name:"balda_goal_max_iterations"`
	AutoMaxTurns      int    `name:"balda_automode_max_turns"`
	Logger            zerolog.Logger
}

// NewZulipBaldaHandler creates a ZulipBaldaHandler.
func NewZulipBaldaHandler(params zulipBaldaHandlerParams) *ZulipBaldaHandler {
	h := &ZulipBaldaHandler{
		ownerStore:        params.OwnerStore,
		inviteStore:       params.InviteStore,
		collaboratorStore: params.CollaboratorStore,
		channelAuth:       params.ChannelAuth,
		sessionManager:    params.SessionManager,
		actorDispatcher:   params.Dispatcher,
		goalJobs:          params.GoalJobs,
		memoryStore:       params.MemoryStore,
		authToken:         strings.TrimSpace(params.AuthToken),
		baldaProviderName: strings.TrimSpace(params.BaldaProviderID),
		webhookToken:      strings.TrimSpace(params.ZulipWebhookToken),
		listenAddr:        strings.TrimSpace(params.ZulipListenAddr),
		webhookPath:       strings.TrimSpace(params.ZulipWebhookPath),
		enabled:           params.ZulipEnabled,
		goalMaxIterations: normalizeGoalMaxIterations(params.MaxIterations),
		autoMaxTurns:      automode.NormalizeMaxTurns(params.AutoMaxTurns),
		logger:            params.Logger.With().Str("component", "balda.handler.zulip").Logger(),
		processSem:        make(chan struct{}, zulipWebhookMaxConcurrentTasks),
	}

	return h
}

// Start begins accepting configured Zulip webhook requests.
func (h *ZulipBaldaHandler) Start(ctx context.Context) error { return h.onStart(ctx) }

// Stop gracefully shuts down the Zulip receiver.
func (h *ZulipBaldaHandler) Stop(ctx context.Context) error { return h.onStop(ctx) }

func (h *ZulipBaldaHandler) onStart(_ context.Context) error {
	if !h.enabled {
		h.logger.Info().Msg("zulip webhook disabled; skipping server start")
		return nil
	}
	if h.processSem == nil {
		h.processSem = make(chan struct{}, zulipWebhookMaxConcurrentTasks)
	}
	h.initOwnerFromStore()

	path, err := normalizeZulipWebhookPath(h.webhookPath)
	if err != nil {
		return err
	}
	listenAddr := h.listenAddr
	if listenAddr == "" {
		listenAddr = ":8090"
	}

	mux := http.NewServeMux()
	mux.HandleFunc(path, h.handleWebhook)
	h.server = &http.Server{
		Addr:              listenAddr,
		Handler:           mux,
		ReadHeaderTimeout: zulipWebhookReadHeaderTimeout,
		ReadTimeout:       zulipWebhookReadTimeout,
		WriteTimeout:      zulipWebhookWriteTimeout,
		IdleTimeout:       zulipWebhookIdleTimeout,
	}
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return fmt.Errorf("listen zulip webhook endpoint on %q: %w", listenAddr, err)
	}
	h.ln = ln

	go func() {
		h.logger.Info().Str("addr", listenAddr).Str("path", path).Msg("zulip webhook server starting")
		if err := h.server.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			h.logger.Error().Err(err).Msg("zulip webhook server error")
		}
	}()

	return nil
}

func normalizeZulipWebhookPath(path string) (string, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return "/zulip/webhook", nil
	}
	if !strings.HasPrefix(trimmed, "/") {
		return "", fmt.Errorf("balda.zulip.webhook.path must start with /")
	}
	return trimmed, nil
}

func (h *ZulipBaldaHandler) onStop(ctx context.Context) error {
	if h.server == nil {
		return nil
	}
	if err := h.server.Shutdown(ctx); err != nil {
		h.logger.Warn().Err(err).Msg("zulip webhook server shutdown error")
		return fmt.Errorf("shutdown zulip webhook server: %w", err)
	}
	if err := h.waitForWebhookProcessing(ctx); err != nil {
		h.logger.Warn().Err(err).Msg("zulip webhook processing shutdown wait error")
		return err
	}
	h.ln = nil
	return nil
}

func (h *ZulipBaldaHandler) waitForWebhookProcessing(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		h.processWG.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("wait for zulip webhook processing: %w", ctx.Err())
	}
}

func (h *ZulipBaldaHandler) initOwnerFromStore() {
	if h.ownerStore == nil {
		h.logger.Warn().Msg("zulip handler owner store is unavailable")
		return
	}
	ownerID, ok := initZulipOwnerID(h.ownerStore)
	if !ok {
		return
	}
	h.mu.Lock()
	h.ownerID = ownerID
	h.mu.Unlock()
	h.logger.Info().Int64("owner_id", ownerID).Msg("zulip handler owner initialized")
}

func (h *ZulipBaldaHandler) getOwnerID() int64 {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.ownerID
}

func (h *ZulipBaldaHandler) setOwnerID(id int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.ownerID = id
}

func (h *ZulipBaldaHandler) handleWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	defer func() { _ = r.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(r.Body, zulipWebhookMaxBodyBytes+1))
	if err != nil {
		h.logger.Warn().Err(err).Msg("failed to read zulip webhook body")
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if len(body) > zulipWebhookMaxBodyBytes {
		h.logger.Warn().Int("bytes", len(body)).Msg("zulip webhook body too large")
		http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
		return
	}

	var payload zulipWebhookPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		h.logger.Warn().Err(err).Msg("failed to decode zulip webhook payload")
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	if h.webhookToken == "" {
		h.logger.Error().Msg("zulip webhook token is not configured")
		http.Error(w, "service unavailable", http.StatusServiceUnavailable)
		return
	}
	if !verifyZulipWebhookToken(payload, h.webhookToken) {
		h.logger.Warn().Str("sender", payload.Message.SenderEmail).Msg("zulip webhook token mismatch")
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if err := validateZulipWebhookPayload(payload); err != nil {
		h.logger.Warn().Err(err).Msg("invalid zulip webhook payload")
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if isZulipBotEcho(payload) {
		h.logger.Debug().Str("sender", payload.Message.SenderEmail).Msg("ignoring zulip bot echo")
		writeZulipWebhookNoResponse(w)
		return
	}
	release, ok := h.acquireWebhookProcessSlot()
	if !ok {
		h.logger.Warn().Msg("zulip webhook processing queue full")
		http.Error(w, "busy", http.StatusServiceUnavailable)
		return
	}

	defer release()
	settlement, err := h.processWebhookPayload(context.WithoutCancel(r.Context()), payload)
	if err != nil && settlement.Outcome == turncmd.InboundRetry {
		http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
		return
	}
	writeZulipWebhookNoResponse(w)
}

func (h *ZulipBaldaHandler) processWebhookPayload(requestCtx context.Context, payload zulipWebhookPayload) (settlement turncmd.InboundSettlement, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			h.logger.Error().
				Interface("panic", recovered).
				Int("sender_id", payload.Message.SenderID).
				Str("session_id", h.locatorFromPayload(payload).SessionID).
				Msg("zulip webhook processing panic recovered")
			settlement = retryInbound()
			err = fmt.Errorf("zulip webhook processing panic: %v", recovered)
		}
	}()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(requestCtx), zulipWebhookProcessingTimeout)
	defer cancel()
	return h.processMessage(ctx, payload)
}

func writeZulipWebhookNoResponse(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"response_not_required": true}`))
}

func (h *ZulipBaldaHandler) acquireWebhookProcessSlot() (func(), bool) {
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

func (h *ZulipBaldaHandler) processMessage(ctx context.Context, payload zulipWebhookPayload) (turncmd.InboundSettlement, error) {
	locator := h.locatorFromPayload(payload)
	senderID := payload.Message.SenderID
	text := normalizeZulipMessageText(payload)
	isDM := payload.Message.Type == chatTypePrivate

	h.logger.Debug().
		Str("trigger", payload.Trigger).
		Str("type", payload.Message.Type).
		Int("sender_id", senderID).
		Msg("processing zulip message")

	if strings.HasPrefix(text, "/") {
		h.handleCommand(ctx, locator, senderID, text, isDM)
		return terminalInbound(), nil
	}
	if isDM {
		if token, ok := firstFieldToken(text); ok {
			h.handleOwnerBindToken(ctx, locator, senderID, token)
			return terminalInbound(), nil
		}
	}

	return h.handleMessage(ctx, locator, senderID, payload.Message.ID, text, isDM)
}

func (h *ZulipBaldaHandler) handleCommand(
	ctx context.Context,
	locator baldasession.SessionLocator,
	senderID int,
	text string,
	isDM bool,
) {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return
	}
	cmd := strings.ToLower(strings.TrimPrefix(fields[0], "/"))
	args := ""
	if len(fields) > 1 {
		args = strings.Join(fields[1:], " ")
	}

	transportUserID := int64(senderID)
	if cmd != commandStart && !h.canAccessCollaboratorScope(ctx, transportUserID) {
		_ = h.sendPlain(ctx, locator, zulipAccessDeniedMessage())
		return
	}

	switch cmd {
	case commandStart:
		h.handleStartCommand(ctx, locator, senderID, args, isDM)
	case commandReset, commandRestart:
		h.handleResetCommand(ctx, locator, senderID, cmd, args, isDM)
	case commandCancel:
		h.handleCancelCommand(ctx, locator, senderID, args)
	case commandLocator:
		h.handleLocatorCommand(ctx, locator, args)
	case commandTopic:
		h.handleTopicCommand(ctx, locator, senderID, args, isDM)
	case commandGoal:
		h.handleGoalCommand(ctx, locator, senderID, args)
	case commandUsage:
		h.handleUsageCommand(ctx, locator, args)
	case commandAuto:
		h.handleAutoCommand(ctx, locator, args)
	case commandClose:
		h.handleCloseCommand(ctx, locator, senderID, args, isDM)
	case commandUser:
		h.handleUserCommand(ctx, locator, senderID, args)
	default:
		_ = h.sendPlain(ctx, locator, fmt.Sprintf("Unknown command: /%s", cmd))
	}
}

func (h *ZulipBaldaHandler) handleAutoCommand(
	ctx context.Context,
	locator baldasession.SessionLocator,
	args string,
) {
	_ = h.sendPlain(ctx, locator, PlainAutoCommandReply(ctx, h.sessionManager, h.actorDispatcher, locator, args, "Usage: /auto [on|off]", time.Now(), h.autoMaxTurns))
}

func (h *ZulipBaldaHandler) handleStartCommand(
	ctx context.Context,
	locator baldasession.SessionLocator,
	senderID int,
	args string,
	isDM bool,
) {
	if !isDM {
		_ = h.sendPlain(ctx, locator, startDirectMessageOnly())
		return
	}
	if strings.TrimSpace(args) == "" {
		ownerID := h.getOwnerID()
		if ownerID != 0 {
			if h.ownerStore != nil && h.ownerStore.IsOwnerSubject(auth.ZulipSubject(senderID)) {
				msg := ownerAlreadyRegisteredMessage
				if bundle, ok := ownerBindTokenBundleMessage(ctx, h.channelAuth, auth.ZulipSubject(senderID)); ok {
					msg = startOwnerAlreadyRegisteredSelfMessage(bundle)
				}
				_ = h.sendPlain(ctx, locator, msg)
			} else {
				_ = h.sendPlain(ctx, locator, startOwnerAlreadyRegistered())
			}
			return
		}
		_ = h.sendPlain(ctx, locator, startWelcomeMessage())
		return
	}
	parsed, ok := parseZulipStartArgs(args)
	if !ok {
		_ = h.sendPlain(ctx, locator, startInvalidFormatMessage())
		return
	}
	if parsed.Mode == "channel_token" {
		h.handleOwnerBindToken(ctx, locator, senderID, parsed.Token)
		return
	}

	if parsed.Mode == userActionInvite {
		h.handleInviteStart(ctx, locator, senderID, parsed.Token)
		return
	}

	ownerID := h.getOwnerID()
	if ownerID != 0 {
		if ownerID == int64(senderID) {
			_ = h.sendPlain(ctx, locator, ownerAlreadyRegisteredMessage)
		} else {
			_ = h.sendPlain(ctx, locator, startOwnerAlreadyRegistered())
		}
		return
	}

	registered, err := registerZulipOwner(h.ownerStore, senderID, h.authToken, parsed.Token)
	if err != nil {
		if err.Error() == "invalid authentication token" {
			_ = h.sendPlain(ctx, locator, startInvalidAuthToken())
			return
		}
		h.logger.Error().Int("sender_id", senderID).Msg("zulip: owner store is unavailable during owner registration")
		_ = h.sendPlain(ctx, locator, startOwnerStoreUnavailable())
		return
	}
	newOwnerID := int64(senderID)
	if !registered {
		_ = h.sendPlain(ctx, locator, "Owner is already registered.")
		return
	}
	h.setOwnerID(newOwnerID)
	log.Info().Int64("owner_id", newOwnerID).Msg("zulip: owner registered")
	_ = h.sendPlain(ctx, locator, startOwnerRegistered())
}

func (h *ZulipBaldaHandler) handleOwnerBindToken(ctx context.Context, locator baldasession.SessionLocator, senderID int, token string) {
	if h.channelAuth == nil {
		_ = h.sendPlain(ctx, locator, startOwnerBindUnavailable())
		return
	}
	consumed, err := consumeZulipOwnerBindToken(ctx, h.channelAuth, senderID, token)
	if err != nil {
		h.logger.Warn().Err(err).Int("sender_id", senderID).Msg("zulip: failed to consume owner bind token")
		_ = h.sendPlain(ctx, locator, startOwnerBindFailed())
		return
	}
	if !consumed {
		_ = h.sendPlain(ctx, locator, startOwnerBindInvalid())
		return
	}
	h.setOwnerID(int64(senderID))
	_ = h.sendPlain(ctx, locator, startOwnerBound())
}

func (h *ZulipBaldaHandler) handleInviteStart(
	ctx context.Context,
	locator baldasession.SessionLocator,
	senderID int,
	token string,
) {
	msg, err := consumeZulipInvite(ctx, h.ownerStore, h.inviteStore, h.collaboratorStore, senderID, token)
	if err != nil {
		userIDStr := fmt.Sprintf("%d", senderID)
		h.logger.Error().Str("user_id", userIDStr).Err(err).Msg("zulip: failed to process invite")
		_ = h.sendPlain(ctx, locator, startInviteProcessingFailed())
		return
	}
	if msg != "" {
		_ = h.sendPlain(ctx, locator, msg)
	}
}

func (h *ZulipBaldaHandler) handleResetCommand(
	ctx context.Context,
	locator baldasession.SessionLocator,
	senderID int,
	cmd string,
	args string,
	isDM bool,
) {
	if strings.TrimSpace(args) != "" {
		_ = h.sendPlain(ctx, locator, resetUsageMessage(cmd))
		return
	}
	if h.sessionManager == nil {
		_ = h.sendPlain(ctx, locator, resetNotReadyMessage())
		return
	}
	info, infoErr := h.sessionManager.GetSessionInfo(ctx, locator.SessionID)
	if infoErr != nil {
		h.logger.Debug().Err(infoErr).Str("session_id", locator.SessionID).Str("cmd", cmd).Msg("zulip: session info unavailable before restart")
	}
	transportUserID := zulipUserID(senderID)
	reason := fmt.Sprintf("session canceled by %s command", cmd)
	if submitErr := SubmitSessionCancelControl(
		ctx, h.actorDispatcher, locator, transportUserID, reason, false,
	); submitErr != nil {
		h.logger.Warn().Err(submitErr).Str("session_id", locator.SessionID).Str("cmd", cmd).Msg("failed to submit cancel control")
	}
	if err := h.sessionManager.ResetSession(ctx, locator); err != nil {
		h.logger.Warn().Err(err).Str("session_id", locator.SessionID).Msg("failed to reset session")
		_ = h.sendPlain(ctx, locator, resetFailedMessage())
		return
	}
	label := restartZulipSessionLabel(isDM, info, ownerSessionLabel, autoSessionLabel)
	userID := restartZulipSessionUserID(senderID, info)
	if err := h.sessionManager.CreateSession(ctx, baldasession.SessionContext{
		Locator: locator,
		UserID:  userID,
	}, label); err != nil {
		h.logger.Warn().Err(err).Str("session_id", locator.SessionID).Str("cmd", cmd).Msg("zulip: failed to recreate session during restart command")
		_ = h.sendPlain(ctx, locator, restartFailedMessage())
		return
	}

	providerName := strings.TrimSpace(h.sessionManager.BaldaProviderID())
	welcomeMsg := buildZulipRestartWelcome(h.sessionManager, providerName, isDM, label, locator.SessionID, ownerSessionLabel)
	if err := h.sendMarkdown(ctx, locator, welcomeMsg); err != nil {
		h.logger.Warn().Err(err).Str("session_id", locator.SessionID).Str("cmd", cmd).Msg("zulip: failed to send restart welcome")
	}
	h.sendSessionStartupNotice(ctx, locator, locator.SessionID)
}

func (h *ZulipBaldaHandler) handleCancelCommand(
	ctx context.Context,
	locator baldasession.SessionLocator,
	senderID int,
	args string,
) {
	if strings.TrimSpace(args) != "" {
		_ = h.sendPlain(ctx, locator, cancelUsageMessage())
		return
	}
	if h.actorDispatcher == nil {
		_ = h.sendPlain(ctx, locator, cancelUnavailableMessage())
		return
	}
	transportUserID := zulipUserID(senderID)
	if err := SubmitSessionTurnCancelControl(
		ctx, h.actorDispatcher, locator, transportUserID, "session turn canceled by user", true,
	); err != nil {
		h.logger.Warn().Err(err).Str("session_id", locator.SessionID).Msg("failed to submit turn cancel control")
		_ = h.sendPlain(ctx, locator, cancelFailedMessage())
		return
	}
	_ = h.sendPlain(ctx, locator, cancelRequestedMessage())
}

func (h *ZulipBaldaHandler) handleLocatorCommand(
	ctx context.Context,
	locator baldasession.SessionLocator,
	args string,
) {
	if strings.TrimSpace(args) != "" {
		_ = h.sendPlain(ctx, locator, locatorUsageMessage())
		return
	}
	_ = h.sendPlain(ctx, locator, buildZulipLocatorMessage(locator))
}

func (h *ZulipBaldaHandler) handleUsageCommand(
	ctx context.Context,
	locator baldasession.SessionLocator,
	args string,
) {
	if strings.TrimSpace(args) != "" {
		_ = h.sendPlain(ctx, locator, usageUsageMessage())
		return
	}
	snapshot, ok, err := LoadUsageSnapshot(ctx, h.sessionManager, locator)
	if err != nil {
		h.logger.Warn().Err(err).Str("session_id", locator.SessionID).Msg("failed to load usage snapshot")
	}
	if err != nil || !ok {
		_ = h.sendPlain(ctx, locator, usageEmptyMessage())
		return
	}
	_ = h.sendPlain(ctx, locator, RenderUsageSnapshot(snapshot))
}

func (h *ZulipBaldaHandler) handleCloseCommand(
	ctx context.Context,
	locator baldasession.SessionLocator,
	senderID int,
	args string,
	isDM bool,
) {
	if !isDM {
		_ = h.sendPlain(ctx, locator, closeDirectMessageOnly())
		return
	}
	if strings.TrimSpace(args) != "" {
		_ = h.sendPlain(ctx, locator, closeUsageMessage())
		return
	}
	if h.sessionManager == nil {
		_ = h.sendPlain(ctx, locator, resetNotReadyMessage())
		return
	}
	transportUserID := zulipUserID(senderID)
	if submitErr := SubmitSessionCancelControl(
		ctx, h.actorDispatcher, locator, transportUserID, "session canceled by close command", false,
	); submitErr != nil {
		h.logger.Warn().Err(submitErr).Str("session_id", locator.SessionID).Msg("failed to submit cancel control for /close")
	}
	if err := ResetSessionWithReason(ctx, h.sessionManager, locator, baldasession.BoundaryReasonClose); err != nil {
		h.logger.Warn().Err(err).Str("session_id", locator.SessionID).Msg("failed to reset session for /close")
		_ = h.sendPlain(ctx, locator, closeFailedMessage())
		return
	}
	_ = h.sendPlain(ctx, locator, closeResetMessage())
}

func (h *ZulipBaldaHandler) handleUserCommand(
	ctx context.Context,
	locator baldasession.SessionLocator,
	senderID int,
	args string,
) {
	ownerID := h.getOwnerID()
	if ownerID == 0 || int64(senderID) != ownerID {
		_ = h.sendPlain(ctx, locator, "This command is only for the owner.")
		return
	}
	fields := strings.Fields(args)
	if len(fields) == 0 {
		h.sendUserUsage(ctx, locator)
		return
	}
	switch fields[0] {
	case userActionAdd, userActionInvite:
		h.handleUserInvite(ctx, locator, senderID)
	case userActionList:
		h.handleUserList(ctx, locator)
	case userActionRemove:
		if len(fields) < 2 {
			_ = h.sendPlain(ctx, locator, "Usage: /user remove <user_id>")
			return
		}
		h.handleUserRemove(ctx, locator, fields[1])
	default:
		h.sendUserUsage(ctx, locator)
	}
}

func (h *ZulipBaldaHandler) sendUserUsage(ctx context.Context, locator baldasession.SessionLocator) {
	_ = h.sendPlain(ctx, locator, userUsageMessage())
}

func (h *ZulipBaldaHandler) handleUserInvite(
	ctx context.Context,
	locator baldasession.SessionLocator,
	senderID int,
) {
	token, err := createZulipInviteToken(ctx, h.inviteStore, senderID)
	if err != nil {
		if err.Error() == "invite store is unavailable" {
		_ = h.sendPlain(ctx, locator, "Invite store is unavailable.")
		} else {
		_ = h.sendPlain(ctx, locator, "Failed to create invite. Please try again.")
		}
		return
	}
	_ = h.sendPlain(ctx, locator, userInviteMessage(token))
}

func (h *ZulipBaldaHandler) handleUserList(
	ctx context.Context,
	locator baldasession.SessionLocator,
) {
	collaborators, invites, err := loadZulipUserListView(ctx, h.collaboratorStore, h.inviteStore)
	if err != nil {
		if err.Error() == "collaborator store is unavailable" {
		_ = h.sendPlain(ctx, locator, "Collaborator store is unavailable.")
		} else {
		_ = h.sendPlain(ctx, locator, "Failed to list collaborators. Please try again.")
		}
		return
	}
	_ = h.sendPlain(ctx, locator, userListMessage(collaborators, invites))
}

func (h *ZulipBaldaHandler) handleUserRemove(
	ctx context.Context,
	locator baldasession.SessionLocator,
	userID string,
) {
	err := removeZulipCollaborator(ctx, h.collaboratorStore, userID)
	if err != nil {
		switch err.Error() {
		case "collaborator store is unavailable":
		_ = h.sendPlain(ctx, locator, "Collaborator store is unavailable.")
		case "user id is required":
		_ = h.sendPlain(ctx, locator, "User ID required.")
		default:
		_ = h.sendPlain(ctx, locator, "Could not remove collaborator. Please try again.")
		}
		return
	}
	_ = h.sendPlain(ctx, locator, userRemovedMessage(userID))
}

func (h *ZulipBaldaHandler) handleGoalCommand(
	ctx context.Context,
	locator baldasession.SessionLocator,
	senderID int,
	args string,
) {
	objective := strings.TrimSpace(args)
	if objective == "" {
		_ = h.sendPlain(ctx, locator, goalUsageMessage())
		return
	}
	if strings.EqualFold(objective, "clear") {
		if h.actorDispatcher == nil {
			_ = h.sendPlain(ctx, locator, goalUnavailableMessage())
			return
		}
		if err := SubmitGoalClearControl(
			ctx, h.actorDispatcher, locator, zulipUserID(senderID), "goal cleared by user", true,
		); err != nil {
			h.logger.Warn().Err(err).Str("session_id", locator.SessionID).Msg("failed to submit goal clear control")
			_ = h.sendPlain(ctx, locator, goalClearFailedMessage())
		}
		return
	}
	started, err := h.submitGoalJob(ctx, locator, objective, zulipUserID(senderID))
	if err != nil {
		h.logger.Warn().Err(err).Str("session_id", locator.SessionID).Msg("failed to start /goalkeeper run")
		_ = h.sendPlain(ctx, locator, goalStartFailedMessage())
		return
	}
	if !started {
		_ = h.sendPlain(ctx, locator, goalAlreadyActiveMessage())
	}
}

func (h *ZulipBaldaHandler) submitGoalJob(
	ctx context.Context,
	locator baldasession.SessionLocator,
	objective string,
	transportUserID string,
) (bool, error) {
	if h.goalJobs != nil {
		active, err := h.goalJobs.HasActiveGoalJob(ctx, locator.SessionID)
		if err != nil {
			return false, fmt.Errorf("list active goal jobs: %w", err)
		}
		if active {
			return false, nil
		}
	}
	maxIterations := normalizeGoalMaxIterations(h.goalMaxIterations)
	env, err := goalkeepercmd.JobEnvelopeWithOptions(locator, deliveryfmt.Options{DeliveryFormat: deliveryfmt.DeliveryFormatMarkdown, ProgressPolicy: deliveryfmt.ProgressPolicy{Typing: true, Thinking: false, PlanUpdates: true}}, objective, transportUserID, maxIterations)
	if err != nil {
		return false, err
	}
	if h.actorDispatcher == nil {
		return false, fmt.Errorf("runtime is unavailable")
	}
	if _, err = h.actorDispatcher.Dispatch(ctx, env); err != nil {
		return false, err
	}
	return true, nil
}

func (h *ZulipBaldaHandler) handleTopicCommand(
	ctx context.Context,
	locator baldasession.SessionLocator,
	senderID int,
	args string,
	isDM bool,
) {
	if isDM {
		_ = h.sendPlain(ctx, locator, topicDirectMessageOnly())
		return
	}
	topicName := strings.TrimSpace(args)
	if topicName == "" {
		_ = h.sendPlain(ctx, locator, topicUsageMessage())
		return
	}
	if h.sessionManager == nil {
		_ = h.sendPlain(ctx, locator, topicNotReadyMessage())
		return
	}
	baldaProviderID := strings.TrimSpace(h.sessionManager.BaldaProviderID())
	if baldaProviderID == "" {
		_ = h.sendPlain(ctx, locator, topicNotReadyMessage())
		return
	}

	streamID, ok := locatorref.ZulipStreamID(locator)
	if !ok {
		_ = h.sendPlain(ctx, locator, topicStreamContextMissingMessage())
		return
	}

	h.logger.Info().
		Int("sender_id", senderID).
		Int("stream_id", streamID).
		Str("topic_name", topicName).
		Msg("creating zulip topic session")

	topicLocator := newZulipStreamLocator(streamID, topicName)
	transportUserID := zulipUserID(senderID)
	if err := h.sessionManager.CreateSession(ctx, baldasession.SessionContext{
		Locator: topicLocator,
		UserID:  transportUserID,
	}, topicName); err != nil {
		h.logger.Error().Err(err).Str("topic_name", topicName).Msg("failed to create zulip topic session")
		_ = h.sendPlain(ctx, locator, topicCreateFailedMessage())
		return
	}
	welcomeMsg := buildZulipTopicWelcome(h.sessionManager, baldaProviderID, topicName, topicLocator.SessionID)
	if err := h.sendZulipAgentReply(ctx, topicLocator, welcomeMsg); err != nil {
		h.logger.Warn().Err(err).Str("topic_name", topicName).Msg("failed to send welcome to new topic")
		_ = h.sendPlain(ctx, locator, topicCreatedFallbackMessage(topicName))
		return
	}
	_ = h.sendPlain(ctx, locator, topicCreatedMessage(topicName))
}

func (h *ZulipBaldaHandler) handleMessage(
	ctx context.Context,
	locator baldasession.SessionLocator,
	senderID int,
	messageID int,
	text string,
	isDM bool,
) (turncmd.InboundSettlement, error) {
	if h.getOwnerID() == 0 {
		return terminalInbound(), nil
	}
	if strings.TrimSpace(text) == "" {
		return terminalInbound(), nil
	}

	transportUserID := zulipUserID(senderID)
	inbound := normalizeZulipInbound(zulipInboundMessage{
		Locator:    locator,
		MessageID:  messageID,
		UserID:     transportUserID,
		Text:       text,
		Direct:     isDM,
		ReceivedAt: time.Now(),
	})
	service, err := ingressapp.NewWithLogger(
		ingressapp.AuthorizerFunc(h.authorizeZulipInbound),
		ingressapp.SessionPreparerFunc(h.prepareZulipSession),
		h.actorDispatcher,
		h.logger,
	)
	if err != nil {
		return retryInbound(), err
	}
	result, err := service.Process(ctx, inbound)
	if err != nil && result.Settlement.Outcome == turncmd.InboundRetry {
		if baldaexecution.IsCommandQueueFull(err) {
			_ = h.sendPlain(ctx, locator, "Session command queue is full. Please wait or use /cancel.")
			return result.Settlement, err
		}
		h.logger.Error().Err(err).Str("session_id", locator.SessionID).Msg("zulip: failed to enqueue turn")
		if result.Settlement.Reason != ingressapp.ReasonSessionRejected {
			_ = h.sendPlain(ctx, locator, "Failed to publish your message for processing. Please try again.")
		}
	}
	return result.Settlement, err
}

func (h *ZulipBaldaHandler) authorizeZulipInbound(ctx context.Context, inbound ingressapp.InboundContext) (ingressapp.Authorization, error) {
	userID, err := parseZulipUserID(inbound.UserID)
	if err != nil {
		return ingressapp.Authorization{Reason: ingressapp.ReasonUnauthorized}, nil
	}
	allowed, err := h.accessCollaboratorScope(ctx, int64(userID))
	return ingressapp.Authorization{Allowed: allowed, Reason: ingressapp.ReasonUnauthorized}, err
}

func (h *ZulipBaldaHandler) prepareZulipSession(ctx context.Context, inbound ingressapp.InboundContext) (ingressapp.SessionPreparation, error) {
	ts, err := h.getOrCreateSession(ctx, inboundLocator(inbound), inbound.UserID, h.getProviderName(), inbound.Direct)
	if err != nil {
		return ingressapp.SessionPreparation{}, err
	}
	return buildZulipSessionPreparation(ts, inbound.UserID), nil
}

func (h *ZulipBaldaHandler) getOrCreateSession(
	ctx context.Context,
	locator baldasession.SessionLocator,
	transportUserID string,
	providerName string,
	isDM bool,
) (*baldasession.TopicSession, error) {
	if h.sessionManager == nil {
		_ = h.sendPlain(ctx, locator, "Balda is not ready right now. Please try again.")
		return nil, fmt.Errorf("session manager is unavailable")
	}
	if strings.TrimSpace(providerName) == "" {
		_ = h.sendPlain(ctx, locator, "Balda is not ready right now. Please try again.")
		return nil, fmt.Errorf("no provider configured")
	}
	ts, welcomed, err := ensureZulipSession(
		ctx,
		h.sessionManager,
		locator,
		transportUserID,
		providerName,
		isDM,
		ownerSessionLabel,
		autoSessionLabel,
	)
	if err != nil {
		h.logger.Warn().Err(err).Str("session_id", locator.SessionID).Msg("zulip: failed to restore session")
		_ = h.sendPlain(ctx, locator, "Could not restore this session. Please try again.")
		return nil, err
	}
	if welcomed {
		h.sendSessionWelcome(ctx, locator, ts, providerName, isDM)
	}
	return ts, nil
}

func (h *ZulipBaldaHandler) sendSessionWelcome(
	ctx context.Context,
	locator baldasession.SessionLocator,
	ts *baldasession.TopicSession,
	providerName string,
	isDM bool,
) {
	welcomeMsg := buildZulipSessionWelcome(h.sessionManager, providerName, isDM, ts.GetSessionID(), ownerSessionLabel, autoSessionLabel)
	_ = h.sendPlain(ctx, locator, welcomeMsg)
}

func (h *ZulipBaldaHandler) sendZulipAgentReply(
	ctx context.Context,
	locator baldasession.SessionLocator,
	text string,
) error {
	return SendAgentReply(ctx, h.actorDispatcher, zulipHandlerActorAddress, locator, text)
}

func (h *ZulipBaldaHandler) canAccessCollaboratorScope(ctx context.Context, userID int64) bool {
	allowed, err := h.accessCollaboratorScope(ctx, userID)
	return err == nil && allowed
}

func (h *ZulipBaldaHandler) accessCollaboratorScope(ctx context.Context, userID int64) (bool, error) {
	return canAccessZulipCollaboratorScope(ctx, h.ownerStore, h.collaboratorStore, userID)
}

func (h *ZulipBaldaHandler) getProviderName() string {
	if h.sessionManager == nil {
		return strings.TrimSpace(h.baldaProviderName)
	}
	providerName := strings.TrimSpace(h.sessionManager.BaldaProviderID())
	if providerName == "" {
		providerName = strings.TrimSpace(h.baldaProviderName)
	}
	return providerName
}

func (h *ZulipBaldaHandler) sendPlain(
	ctx context.Context,
	locator baldasession.SessionLocator,
	text string,
) error {
	return SendPlain(ctx, h.actorDispatcher, zulipHandlerActorAddress, locator, text)
}

func (h *ZulipBaldaHandler) sendMarkdown(
	ctx context.Context,
	locator baldasession.SessionLocator,
	text string,
) error {
	return SendMarkdown(ctx, h.actorDispatcher, zulipHandlerActorAddress, locator, text)
}

func (h *ZulipBaldaHandler) sendSessionStartupNotice(ctx context.Context, locator baldasession.SessionLocator, sessionID string) {
	if h.sessionManager == nil {
		return
	}
	notice := strings.TrimSpace(h.sessionManager.TakeStartupNotice(sessionID))
	if notice == "" {
		return
	}
	if err := h.sendPlain(ctx, locator, notice); err != nil {
		h.logger.Warn().Err(err).Str("session_id", sessionID).Msg("zulip: failed to send restart startup notice")
	}
}

func (h *ZulipBaldaHandler) locatorFromPayload(payload zulipWebhookPayload) baldasession.SessionLocator {
	return locatorFromZulipWebhookPayload(payload)
}
