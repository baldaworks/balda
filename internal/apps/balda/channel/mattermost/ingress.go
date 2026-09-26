package mattermost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
	"github.com/gorilla/websocket"
	"github.com/rs/zerolog"
)

const (
	// websocketPingInterval is the Mattermost-recommended client ping cadence.
	websocketPingInterval = 30 * time.Second
	// websocketReadLimit caps an inbound frame. Auth challenge frames are small;
	// posted events carry one post, so 1 MiB is generous.
	websocketReadLimit = 1 << 20
	// websocketReconnectBackoff is the delay before re-establishing a dropped
	// connection.
	websocketReconnectBackoff = 5 * time.Second
	// websocketHandshakeTimeout bounds the initial dial.
	websocketHandshakeTimeout = 20 * time.Second
	// websocketMaxConcurrentTasks bounds in-flight inbound processing.
	websocketMaxConcurrentTasks = 16
	// websocketActionPing is the keepalive action Mattermost accepts on a client frame.
	websocketActionPing = "ping"
)

// Ingress streams Mattermost events over the websocket API and dispatches them
// into the transport-neutral inbound processor.
//
// Mattermost has no per-bot outgoing webhook that preserves threads and edit
// events, and bots cannot receive their own posts as ordinary webhooks, so the
// websocket API is the correct ingress for a bot that must see all message
// kinds (including edits and deletes).
type Ingress struct {
	processor InboundProcessor
	client    *Client
	commands  commandSupport
	enabled   bool
	logger    zerolog.Logger

	botUserID   string
	botUsername string

	mu      sync.Mutex
	conn    *websocket.Conn
	cancel  context.CancelFunc
	done    chan struct{}
	stopped bool

	// seq is the monotonic outgoing websocket sequence number Mattermost
	// requires on every client frame. Frames without it are rejected with
	// api.web_socket_router.bad_seq.app_error and the server then stops
	// delivering events on that socket.
	seq int64

	processSem chan struct{}
	processWG  sync.WaitGroup
}

// IngressCommandSupport is the optional shared command registry. It is an
// interface so this transport does not import application policy (see the
// transport presentation boundary contract).
type IngressCommandSupport interface {
	Supports(channelType, command string) bool
}

// commandSupport is the legacy alias retained for internal use.
type commandSupport = IngressCommandSupport

// IngressParams configures the Mattermost websocket ingress.
type IngressParams struct {
	Processor   InboundProcessor
	Client      *Client
	Commands    commandSupport
	Enabled     bool
	BotUserID   string
	BotUsername string
	Logger      zerolog.Logger
}

// NewIngress creates a Mattermost websocket ingress carrier.
func NewIngress(params IngressParams) *Ingress {
	return &Ingress{
		processor:   params.Processor,
		client:      params.Client,
		commands:    params.Commands,
		enabled:     params.Enabled,
		botUserID:   strings.TrimSpace(params.BotUserID),
		botUsername: strings.TrimSpace(params.BotUsername),
		logger:      params.Logger.With().Str("component", "balda.channel.mattermost.ingress").Logger(),
		processSem:  make(chan struct{}, websocketMaxConcurrentTasks),
	}
}

// Start begins streaming events until Stop is called.
func (s *Ingress) Start(ctx context.Context) error { return s.onStart(ctx) }

// Stop closes the websocket and drains in-flight processing.
func (s *Ingress) Stop(ctx context.Context) error { return s.onStop(ctx) }

func (s *Ingress) onStart(_ context.Context) error {
	if !s.enabled {
		s.logger.Info().Msg("mattermost ingress disabled; skipping start")
		return nil
	}
	if s.client == nil {
		return fmt.Errorf("mattermost ingress requires a client")
	}
	if strings.TrimSpace(s.botUserID) == "" {
		return fmt.Errorf("mattermost ingress requires a bot user id")
	}
	if s.processSem == nil {
		s.processSem = make(chan struct{}, websocketMaxConcurrentTasks)
	}

	s.mu.Lock()
	if s.cancel != nil {
		s.mu.Unlock()
		return nil // already running
	}
	runCtx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.done = make(chan struct{})
	s.stopped = false
	done := s.done
	s.mu.Unlock()

	go func() {
		defer close(done)
		s.run(runCtx)
	}()
	return nil
}

func (s *Ingress) onStop(ctx context.Context) error {
	s.mu.Lock()
	if s.cancel == nil {
		s.mu.Unlock()
		return nil
	}
	cancel := s.cancel
	conn := s.conn
	done := s.done
	s.cancel = nil
	s.stopped = true
	s.mu.Unlock()

	cancel()
	if conn != nil {
		_ = conn.Close()
	}

	waitErr := s.waitForProcessing(ctx)
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			if waitErr == nil {
				waitErr = fmt.Errorf("wait for mattermost ingress shutdown: %w", ctx.Err())
			}
		}
	}
	return waitErr
}

func (s *Ingress) waitForProcessing(ctx context.Context) error {
	waitDone := make(chan struct{})
	go func() {
		s.processWG.Wait()
		close(waitDone)
	}()
	select {
	case <-waitDone:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("wait for mattermost ingress processing: %w", ctx.Err())
	}
}

// run keeps a websocket connection alive across drops until the context ends.
func (s *Ingress) run(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		if err := s.streamOnce(ctx); err != nil && ctx.Err() == nil {
			s.logger.Warn().Err(err).Msg("mattermost websocket stream ended; reconnecting")
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(websocketReconnectBackoff):
		}
	}
}

func (s *Ingress) streamOnce(ctx context.Context) error {
	endpoint, err := s.client.WebSocketURL()
	if err != nil {
		return err
	}
	dialer := &websocket.Dialer{HandshakeTimeout: websocketHandshakeTimeout}
	header := http.Header{}
	header.Set("Authorization", "Bearer "+strings.TrimSpace(tokenOf(s.client)))

	conn, resp, err := dialer.DialContext(ctx, endpoint, header)
	if err != nil {
		status := 0
		if resp != nil {
			status = resp.StatusCode
			_ = resp.Body.Close()
		}
		s.logger.Warn().
			Err(err).
			Str("url", endpoint).
			Int("http_status", status).
			Msg("dial mattermost websocket failed")
		return fmt.Errorf("dial mattermost websocket: %w", err)
	}
	defer func() { _ = conn.Close() }()
	conn.SetReadLimit(websocketReadLimit)

	s.mu.Lock()
	s.conn = conn
	s.seq = 0
	s.mu.Unlock()

	s.logger.Info().Str("url", endpoint).Msg("mattermost websocket connected")

	// Authentication happens via the Authorization: Bearer header on the dial.
	// Sending an authentication_challenge frame on an already-authenticated
	// connection makes Mattermost re-initialise the session for this socket and
	// it silently stops delivering posted events on it, so no challenge is sent.
	return s.readLoop(ctx, conn)
}

func (s *Ingress) readLoop(ctx context.Context, conn *websocket.Conn) error {
	pingTicker := time.NewTicker(websocketPingInterval)
	defer pingTicker.Stop()

	type readResult struct {
		messageType int
		payload     []byte
		err         error
	}
	frames := make(chan readResult, 1)

	// Dedicated reader goroutine: a blocking ReadMessage cannot be interrupted
	// by context cancellation, so Stop closes the conn and this goroutine exits
	// with an error.
	go func() {
		for {
			messageType, payload, err := conn.ReadMessage()
			select {
			case frames <- readResult{messageType: messageType, payload: payload, err: err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-pingTicker.C:
			if err := s.writeEvent(conn, websocketActionPing, nil); err != nil {
				return fmt.Errorf("send mattermost websocket ping: %w", err)
			}
		case frame := <-frames:
			if frame.err != nil {
				if errors.Is(frame.err, websocket.ErrCloseSent) || ctx.Err() != nil {
					return nil
				}
				return fmt.Errorf("read mattermost websocket frame: %w", frame.err)
			}
			s.logger.Debug().
				Int("message_type", frame.messageType).
				Int("payload_bytes", len(frame.payload)).
				Str("payload_head", headOf(frame.payload, 160)).
				Msg("mattermost websocket frame received")
			if frame.messageType != websocket.TextMessage {
				continue
			}
			s.handleFrame(ctx, frame.payload)
		}
	}
}

// nextSeq returns the next outgoing websocket sequence number.
//
// Mattermost validates seq on every client frame and rejects an invalid one with
// api.web_socket_router.bad_seq.app_error, after which it silently stops pushing
// events to the socket. The counter therefore has to start above zero and grow
// monotonically for the lifetime of the connection.
func (s *Ingress) nextSeq() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	return s.seq
}

// writeEvent sends a client frame with the required seq field.
func (s *Ingress) writeEvent(conn *websocket.Conn, action string, data any) error {
	frame := map[string]any{"seq": s.nextSeq(), "action": action}
	if data != nil {
		frame["data"] = data
	}
	return conn.WriteJSON(frame)
}

// headOf returns a bounded, single-line preview of a payload for logging.
func headOf(payload []byte, limit int) string {
	if len(payload) > limit {
		payload = payload[:limit]
	}
	return strings.ReplaceAll(string(payload), "\n", " ")
}

func (s *Ingress) handleFrame(ctx context.Context, payload []byte) {
	var event WebSocketEvent
	if err := json.Unmarshal(payload, &event); err != nil {
		s.logger.Debug().Err(err).Msg("ignoring undecodable mattermost websocket frame")
		return
	}
	if event.Status == "FAIL" {
		s.logger.Warn().Str("event", event.Event).Str("status", event.Status).RawJSON("data", event.Data).Msg("mattermost rejected a client websocket frame")
		return
	}
	s.logger.Debug().Str("event", event.Event).Msg("mattermost websocket event decoded")
	switch event.Event {
	case eventPosted:
		s.dispatchPosted(ctx, event)
	case eventPostEdited, eventPostDeleted:
		// Edits and deletes are intentionally ignored: Balda acts on the
		// original instruction and does not retroactively rewrite its answer.
		s.logger.Debug().Str("event", event.Event).Msg("ignoring mattermost post mutation")
	default:
		// hello, status_change, typing, reaction, channel_viewed and the rest
		// are not part of the Balda contract.
	}
}

func (s *Ingress) dispatchPosted(ctx context.Context, event WebSocketEvent) {
	var data PostedData
	if err := json.Unmarshal(event.Data, &data); err != nil {
		s.logger.Debug().Err(err).Msg("ignoring mattermost posted event with undecodable data")
		return
	}
	postID := strings.TrimSpace(data.Post)
	if postID == "" {
		s.logger.Debug().Msg("dropping posted event: empty post id")
		return
	}
	var post Post
	if err := json.Unmarshal([]byte(postID), &post); err != nil {
		s.logger.Debug().Err(err).Msg("ignoring mattermost posted event with undecodable post")
		return
	}
	if strings.TrimSpace(post.ChannelID) == "" {
		post.ChannelID = strings.TrimSpace(event.Broadcast.ChannelID)
	}
	if IsBotEcho(post, s.botUserID) {
		s.logger.Debug().Str("post_id", post.ID).Str("post_user_id", post.UserID).Msg("dropping posted event: bot echo")
		return
	}
	if IsSystemPost(post) {
		s.logger.Debug().Str("post_id", post.ID).Str("post_type", post.Type).Msg("dropping posted event: system post")
		return
	}
	if IsDeletedPost(post) {
		s.logger.Debug().Str("post_id", post.ID).Msg("dropping posted event: deleted post")
		return
	}
	s.logger.Debug().
		Str("post_id", post.ID).
		Str("channel_id", post.ChannelID).
		Str("post_user_id", post.UserID).
		Str("message", post.Message).
		Msg("mattermost posted event accepted")

	release, ok := s.acquireSlot()
	if !ok {
		s.logger.Warn().Str("post_id", post.ID).Msg("mattermost ingress queue full; dropping post")
		return
	}
	s.processWG.Add(1)
	go func() {
		defer func() { release(); s.processWG.Done() }()
		s.processPosted(ctx, data, post)
	}()
}

func (s *Ingress) processPosted(ctx context.Context, data PostedData, post Post) {
	defer func() {
		if recovered := recover(); recovered != nil {
			s.logger.Error().
				Interface("panic", recovered).
				Str("post_id", post.ID).
				Msg("mattermost posted event processing panic recovered")
		}
	}()

	channel := Channel{
		ID:     strings.TrimSpace(post.ChannelID),
		Type:   strings.TrimSpace(data.ChannelType),
		TeamID: strings.TrimSpace(data.TeamID),
	}
	locator := LocatorForPost(channel, post)
	direct := IsDirectChannelType(channel.Type)
	text := strings.TrimSpace(post.Message)
	if !direct {
		// In a shared channel Balda must be addressed explicitly.
		if !MentionsBot(text, s.botUsername) {
			s.logger.Debug().
				Str("post_id", post.ID).
				Str("bot_username", s.botUsername).
				Bool("bot_username_empty", s.botUsername == "").
				Str("message", text).
				Msg("dropping posted event: bot not mentioned")
			return
		}
		text = StripMention(text, s.botUsername)
	}
	if text == "" {
		s.logger.Debug().Str("post_id", post.ID).Msg("dropping posted event: empty text after mention strip")
		return
	}
	s.logger.Info().
		Str("post_id", post.ID).
		Str("channel_id", post.ChannelID).
		Bool("direct", direct).
		Str("text", text).
		Msg("mattermost post accepted for processing")

	if name, args, ok := ParseCommand(text); ok {
		if !commandSupported(name) && (s.commands == nil || !s.commands.Supports(ChannelType, name)) {
			if s.processor != nil {
				_ = s.processor.HandleUnsupportedCommand(ctx, InboundCommand{
					Locator:   locator,
					MessageID: ParsePostID(post.ID),
					PostID:    post.ID,
					SenderID:  post.UserID,
					Command:   name,
					Args:      args,
					Direct:    direct,
				})
			}
			return
		}
		if s.processor != nil {
			_ = s.processor.HandleCommand(ctx, InboundCommand{
				Locator:   locator,
				MessageID: ParsePostID(post.ID),
				PostID:    post.ID,
				SenderID:  post.UserID,
				Command:   name,
				Args:      args,
				Direct:    direct,
			})
		}
		return
	}

	if s.processor == nil {
		return
	}
	if _, err := s.processor.ProcessInbound(ctx, InboundMessage{
		Locator:    locator,
		MessageID:  ParsePostID(post.ID),
		PostID:     post.ID,
		SenderID:   post.UserID,
		SenderName: strings.TrimSpace(data.SenderName),
		Text:       text,
		Direct:     direct,
		ReceivedAt: postTime(post),
	}); err != nil {
		s.logger.Warn().Err(err).Str("post_id", post.ID).Msg("mattermost inbound processing failed")
	}
}

func (s *Ingress) acquireSlot() (func(), bool) {
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

// postTime converts a Mattermost millisecond timestamp to a time. Falls back to
// now when the provider omits it.
func postTime(post Post) time.Time {
	if post.CreateAt > 0 {
		return time.UnixMilli(post.CreateAt).UTC()
	}
	return time.Now().UTC()
}

// tokenOf reaches the configured token for the websocket handshake. The websocket
// endpoint authenticates via a header or an auth-challenge frame, both of which
// need the raw token.
func tokenOf(client *Client) string {
	if client == nil {
		return ""
	}
	return client.token
}

// Ensure the ingress satisfies the lifecycle stage shape used by the
// composition root (Start/Stop with context).
var _ interface {
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
} = (*Ingress)(nil)

// InboundSettlementOutcome exposes the neutral outcome type for callers that
// build settlement values while wiring the processor.
func InboundSettlementOutcome() turncmd.InboundOutcome { return turncmd.InboundTerminal }
