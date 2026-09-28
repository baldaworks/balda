// Package mattermost: HTTP slash-command receiver.
//
// Mattermost does not publish slash commands as posts, so the websocket ingress
// can never see them ("if you type in a slash command it will execute without
// posting a message"). Slash traffic arrives as an HTTP POST to a Request URL
// configured per command in the Mattermost integration settings. This file owns
// that entry point and maps the request onto the same InboundCommand contract the
// websocket path uses, so command policy stays in one place.
//
// This receiver mirrors channel/slackagent/server.go: the HTTP shape, the
// timeouts, the concurrency semaphore and the idempotent invocation id are the
// same. Only the two provider-mandated differences remain — Mattermost
// authenticates with a body "token" instead of a Slack HMAC signature, and it
// answers with its own JSON response shape.
package mattermost

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

const (
	// commandServerMaxBodyBytes bounds a slash-command form body. This matches
	// the Slack receiver so both HTTP entry points share one limit.
	commandServerMaxBodyBytes = 1 << 20
	// commandServerReadHeaderTimeout bounds slow request headers.
	commandServerReadHeaderTimeout = 5 * time.Second
	// commandServerReadTimeout bounds reading the request body.
	commandServerReadTimeout = 10 * time.Second
	// commandServerWriteTimeout bounds writing the response.
	commandServerWriteTimeout = 10 * time.Second
	// commandServerIdleTimeout keeps idle keep-alive connections bounded.
	commandServerIdleTimeout = 30 * time.Second
	// commandServerProcessingTimeout bounds one published command. Mattermost
	// shows the author a timeout when a slash command does not answer quickly,
	// so this stays below its response budget; the command result itself is
	// delivered as a post, exactly like Slack.
	commandServerProcessingTimeout = 2500 * time.Millisecond
	// commandServerMaxConcurrentTasks caps in-flight command handling.
	commandServerMaxConcurrentTasks = 16
	// commandServerShutdownTimeout bounds graceful shutdown.
	commandServerShutdownTimeout = 10 * time.Second

	// responseTypeEphemeral renders the command result only to the author.
	responseTypeEphemeral = "ephemeral"

	commandUsageTemplate = "Usage: /%s"
)

// CommandServerConfig holds the HTTP slash-command receiver settings.
type CommandServerConfig struct {
	// Enabled turns the receiver on. It is independent from the websocket
	// ingress: a deployment may run both, and commands need the HTTP receiver.
	Enabled bool
	// ListenAddr is the local address to serve on.
	ListenAddr string
	// Path is the local HTTP path Mattermost posts slash commands to. It must
	// start with "/" and differ from the websocket endpoint because that one is
	// a websocket upgrade, not an HTTP form receiver.
	Path string
	// Token is the Mattermost slash-command token. Mattermost sends it in the
	// request body as "token" and it is compared exactly; unlike Slack there is
	// no HMAC signature. An empty token rejects every request.
	Token string
}

// CommandServer receives Mattermost slash commands over HTTP and forwards them
// to the shared inbound processor.
//
// It performs transport-level work only: verify the token, resolve which
// Mattermost channel the request targets, then hand an InboundCommand to the
// processor. Authorization, session selection and command policy all stay in
// the shared pipeline, which is why this type holds no policy of its own.
type CommandServer struct {
	processor  InboundProcessor
	commands   IngressCommandSupport
	client     *Client
	config     CommandServerConfig
	logger     zerolog.Logger
	processSem chan struct{}

	server *http.Server
	ln     net.Listener
}

// CommandServerParams are the dependencies of NewCommandServer.
type CommandServerParams struct {
	Processor InboundProcessor
	Commands  IngressCommandSupport
	Client    *Client
	Config    CommandServerConfig
	Logger    zerolog.Logger
}

// NewCommandServer builds the slash-command HTTP receiver.
func NewCommandServer(params CommandServerParams) *CommandServer {
	return &CommandServer{
		processor:  params.Processor,
		commands:   params.Commands,
		client:     params.Client,
		config:     params.Config,
		logger:     params.Logger.With().Str("component", "balda.channel.mattermost.commands").Logger(),
		processSem: make(chan struct{}, commandServerMaxConcurrentTasks),
	}
}

// Start begins serving slash commands.
func (s *CommandServer) Start(ctx context.Context) error { return s.onStart(ctx) }

// Stop shuts the receiver down.
func (s *CommandServer) Stop(ctx context.Context) error { return s.onStop(ctx) }

func (s *CommandServer) onStart(context.Context) error {
	if !s.config.Enabled {
		s.logger.Debug().Msg("mattermost slash commands disabled; skipping command server start")
		return nil
	}
	if s.processor == nil {
		return fmt.Errorf("mattermost slash commands require an inbound processor")
	}
	if s.commands == nil {
		return fmt.Errorf("mattermost slash commands require a command registry")
	}
	path, err := normalizeCommandPath(s.config.Path)
	if err != nil {
		return err
	}
	if strings.TrimSpace(s.config.Token) == "" {
		return fmt.Errorf("mattermost slash command token is required when slash commands are enabled")
	}
	listenAddr := strings.TrimSpace(s.config.ListenAddr)
	if listenAddr == "" {
		listenAddr = ":8093"
	}
	mux := http.NewServeMux()
	mux.HandleFunc(path, s.handleCommand)
	server := &http.Server{
		Addr:              listenAddr,
		Handler:           mux,
		ReadHeaderTimeout: commandServerReadHeaderTimeout,
		ReadTimeout:       commandServerReadTimeout,
		WriteTimeout:      commandServerWriteTimeout,
		IdleTimeout:       commandServerIdleTimeout,
	}
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return fmt.Errorf("listen mattermost command endpoint on %q: %w", listenAddr, err)
	}
	s.server = server
	s.ln = ln
	go func() {
		s.logger.Info().Str("addr", listenAddr).Str("path", path).Msg("mattermost slash command server starting")
		if err := server.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.logger.Error().Err(err).Msg("mattermost slash command server error")
		}
	}()
	return nil
}

func (s *CommandServer) onStop(ctx context.Context) error {
	if s.server == nil {
		return nil
	}
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), commandServerShutdownTimeout)
	defer cancel()
	if err := s.server.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown mattermost command server: %w", err)
	}
	return nil
}

// handleCommand serves one Mattermost slash-command request.
func (s *CommandServer) handleCommand(w http.ResponseWriter, r *http.Request) {
	body, ok := s.readAndVerifyRequest(w, r)
	if !ok {
		return
	}
	form, err := url.ParseQuery(string(body))
	if err != nil {
		s.logger.Warn().Err(err).Msg("failed to decode mattermost slash command form")
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	channelID := strings.TrimSpace(form.Get("channel_id"))
	userID := strings.TrimSpace(form.Get("user_id"))
	if channelID == "" || userID == "" {
		s.logger.Warn().Str("channel_id", channelID).Msg("mattermost slash command missing channel_id or user_id")
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	name, args := commandInvocation(form, func(name string) bool {
		return s.commands != nil && s.commands.Supports(ChannelType, name)
	})
	if name == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if s.processor == nil {
		s.logger.Warn().Msg("mattermost slash command received without an inbound processor")
		writeCommandResponse(w, responseTypeEphemeral, "Balda is temporarily unavailable.")
		return
	}
	if s.commands == nil || !s.commands.Supports(ChannelType, name) {
		writeCommandResponse(w, responseTypeEphemeral, fmt.Sprintf(commandUsageTemplate, name))
		return
	}
	release, ok := s.acquireProcessSlot()
	if !ok {
		http.Error(w, "busy", http.StatusServiceUnavailable)
		return
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), commandServerProcessingTimeout)
	defer cancel()
	locator, direct := s.resolveCommandLocator(ctx, channelID, userID)
	postID := strings.TrimSpace(form.Get("post_id"))
	command := InboundCommand{
		InvocationID: commandInvocationIDForRequest(postID),
		Locator:      locator,
		MessageID:    ParsePostID(postID),
		PostID:       postID,
		SenderID:     userID,
		Command:      name,
		Args:         args,
		Direct:       direct,
	}
	if err := s.processor.HandleCommand(ctx, command); err != nil {
		s.logger.Warn().Err(err).Str("command", name).Msg("failed to handle mattermost slash command")
		http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
		return
	}
	s.logger.Info().
		Str("command", name).
		Str("channel_id", channelID).
		Bool("direct", direct).
		Msg("mattermost slash command accepted")
	w.WriteHeader(http.StatusOK)
}

// readAndVerifyRequest enforces the HTTP boundary and authenticates the caller.
//
// The shape mirrors the Slack receiver. The verification differs on purpose:
// Mattermost sends its slash-command token in the request body and has no HMAC
// signature, so the token is compared in constant time instead.
func (s *CommandServer) readAndVerifyRequest(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return nil, false
	}
	defer func() { _ = r.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(r.Body, commandServerMaxBodyBytes+1))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return nil, false
	}
	if len(body) > commandServerMaxBodyBytes {
		http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
		return nil, false
	}
	form, err := url.ParseQuery(string(body))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return nil, false
	}
	if err := verifyCommandToken(strings.TrimSpace(s.config.Token), form.Get("token")); err != nil {
		// Do not echo the token or the reason: a mismatch is a credential event.
		s.logger.Warn().Err(err).Msg("mattermost slash command token verification failed")
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return nil, false
	}
	return body, true
}

func (s *CommandServer) acquireProcessSlot() (func(), bool) {
	if s.processSem == nil {
		return func() {}, true
	}
	select {
	case s.processSem <- struct{}{}:
		return func() { <-s.processSem }, true
	default:
		return nil, false
	}
}

// verifyCommandToken compares the configured slash-command token with the one in
// the request without leaking a prefix length through timing.
func verifyCommandToken(configured, presented string) error {
	if configured == "" {
		return fmt.Errorf("mattermost slash command token is required")
	}
	presented = strings.TrimSpace(presented)
	if len(presented) != len(configured) {
		// Compare up to the shorter length so the failure still costs a
		// comparable amount of work, then reject.
		var diff byte
		for i := 0; i < len(presented) && i < len(configured); i++ {
			diff |= presented[i] ^ configured[i]
		}
		return fmt.Errorf("mattermost slash command token mismatch")
	}
	var diff byte
	for i := 0; i < len(configured); i++ {
		diff |= presented[i] ^ configured[i]
	}
	if diff != 0 {
		return fmt.Errorf("mattermost slash command token mismatch")
	}
	return nil
}

// commandInvocation reads the command name and arguments from a slash payload.
//
// Mattermost can be configured either as one command per action ("/locator"
// with the arguments in "text") or as a single root command ("/balda locator").
// Both shapes resolve here so the transport does not care which one the operator
// chose.
func commandInvocation(form url.Values, supports func(string) bool) (string, string) {
	configured := strings.TrimPrefix(strings.TrimSpace(form.Get("command")), "/")
	text := strings.TrimSpace(form.Get("text"))
	// A leading slash in the text means the whole invocation is in the text.
	if strings.HasPrefix(text, "/") {
		if name, args, ok := ParseCommand(text); ok {
			return name, args
		}
	}
	// A configured command that is itself a Balda command names the action, and
	// the text carries only its arguments.
	if supports != nil && supports(strings.ToLower(configured)) {
		return strings.ToLower(configured), text
	}
	// Otherwise the configured command is a root wrapper ("/balda") and the
	// action is the first word of the text. An unknown action is still returned
	// so the caller can answer with usage, matching the Slack receiver.
	if configured == "" {
		if name, args, ok := ParseCommand(text); ok {
			return name, args
		}
		return "", ""
	}
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return "", ""
	}
	return strings.ToLower(fields[0]), strings.Join(fields[1:], " ")
}

// commandInvocationID derives a stable id for a slash invocation from its body.
// It remains available for callers that have a provider request identifier or
// explicitly need a stable body fingerprint; ordinary Mattermost slash
// requests use commandInvocationIDForRequest because they have no request ID.
func commandInvocationID(body []byte) string {
	material := append([]byte("mattermost/slash/v1\x00"), body...)
	sum := sha256.Sum256(material)
	return "mattermost:command:" + hex.EncodeToString(sum[:])
}

// commandInvocationIDForRequest keeps retries for the rare post-backed form
// idempotent. Mattermost does not provide a request ID for ordinary slash
// commands, so those requests need a fresh identity; hashing the body would
// incorrectly deduplicate two legitimate identical commands.
func commandInvocationIDForRequest(postID string) string {
	if trimmed := strings.TrimSpace(postID); trimmed != "" {
		return "mattermost:command:post:" + trimmed
	}
	return "mattermost:command:" + uuid.NewString()
}

// resolveCommandLocator resolves the Mattermost channel a slash command targets.
//
// A slash request carries channel_id but not the channel type, and the type
// decides whether the conversation is a direct message (no mention required,
// DM-only commands allowed) or a shared channel. The type is therefore read from
// the API. When the lookup fails the request is treated as a non-direct channel
// so the stricter authorization path applies.
func (s *CommandServer) resolveCommandLocator(ctx context.Context, channelID, userID string) (deliverycmd.Locator, bool) {
	if s.client == nil {
		return NewDMLocator(channelID, userID), true
	}
	channel, err := s.client.GetChannel(ctx, channelID)
	if err != nil {
		s.logger.Warn().Err(err).Str("channel_id", channelID).Msg("failed to resolve mattermost channel for slash command; assuming a shared channel")
		return NewChannelLocator("", channelID, ""), false
	}
	channelType := strings.TrimSpace(channel.Type)
	if IsGroupChannelType(channelType) {
		return NewGroupDMLocator(channelID), true
	}
	if IsDirectChannelType(channelType) {
		return NewDMLocator(channelID, userID), true
	}
	return NewChannelLocator(strings.TrimSpace(channel.TeamID), channelID, ""), false
}

func normalizeCommandPath(path string) (string, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return "/mattermost/commands", nil
	}
	if !strings.HasPrefix(trimmed, "/") {
		return "", fmt.Errorf("mattermost slash command path %q must start with \"/\"", path)
	}
	return trimmed, nil
}

// commandResponse is the JSON body Mattermost renders for a slash command.
type commandResponse struct {
	ResponseType string `json:"response_type,omitempty"`
	Text         string `json:"text,omitempty"`
}

func writeCommandResponse(w http.ResponseWriter, responseType, text string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if strings.TrimSpace(text) == "" {
		return
	}
	_ = json.NewEncoder(w).Encode(commandResponse{ResponseType: responseType, Text: text})
}
