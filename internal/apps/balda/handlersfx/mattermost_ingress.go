package handlersfx

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/auth"
	"github.com/baldaworks/balda/internal/apps/balda/channel/mattermost"
	"github.com/baldaworks/balda/internal/apps/balda/chatapp"
	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/rs/zerolog"
	"go.uber.org/fx"
)

const (
	mattermostAccessDeniedText = "You are not allowed to use Balda in this channel."
	mattermostUnknownCommand   = "Unknown command: /%s"
)

// mattermostInboundHandler adapts Mattermost websocket posts and commands to the
// transport-neutral conversational ingress service.
//
// Conversational intake belongs to chatapp. This adapter only preserves the
// transport's access boundary, maps Mattermost thread replies to question
// replies, and publishes slash commands through commandcmd.Ingress.
//
// It is injected as the optional mattermost.InboundProcessor dependency of the
// websocket ingress. Without this provider the ingress accepts every post and
// then silently discards it.
type mattermostInboundHandler struct {
	ownerStore        *auth.OwnerStore
	collaboratorStore *auth.CollaboratorStore
	actorDispatcher   actortransport.Dispatcher
	chat              chatapp.Handler
	commandIngress    commandcmd.Ingress
	logger            zerolog.Logger
	now               func() time.Time
}

type mattermostInboundHandlerParams struct {
	fx.In

	OwnerStore        *auth.OwnerStore          `optional:"true"`
	CollaboratorStore *auth.CollaboratorStore   `optional:"true"`
	Dispatcher        actortransport.Dispatcher `optional:"true"`
	Chat              chatapp.Handler           `optional:"true"`
	CommandIngress    commandcmd.Ingress        `optional:"true"`
	Logger            zerolog.Logger
}

// newMattermostInboundHandler builds the Mattermost inbound processor.
func newMattermostInboundHandler(params mattermostInboundHandlerParams) mattermost.InboundProcessor {
	return &mattermostInboundHandler{
		ownerStore:        params.OwnerStore,
		collaboratorStore: params.CollaboratorStore,
		actorDispatcher:   params.Dispatcher,
		chat:              params.Chat,
		commandIngress:    params.CommandIngress,
		logger:            params.Logger.With().Str("component", "balda.handlersfx.mattermost").Logger(),
	}
}

// ProcessInbound maps one Mattermost post to the canonical chat ingress.
func (h *mattermostInboundHandler) ProcessInbound(ctx context.Context, msg mattermost.InboundMessage) (turncmd.InboundSettlement, error) {
	if h == nil {
		return turncmd.InboundSettlement{}, fmt.Errorf("mattermost inbound handler is required")
	}
	nowFn := time.Now
	if h.now != nil {
		nowFn = h.now
	}
	receivedAt := msg.ReceivedAt
	if receivedAt.IsZero() {
		receivedAt = nowFn()
	}
	allowed, err := h.authorizeMattermostUser(ctx, msg.SenderID)
	if err != nil {
		return turncmd.InboundSettlement{Outcome: turncmd.InboundRetry, Reason: chatapp.ReasonUnauthorized}, err
	}
	if !allowed {
		return turncmd.InboundSettlement{Outcome: turncmd.InboundTerminal, Reason: chatapp.ReasonUnauthorized}, nil
	}
	if h.chat == nil {
		return turncmd.InboundSettlement{Outcome: turncmd.InboundRetry, Reason: chatapp.ReasonDispatchFailed}, actorlayer.TransientError(fmt.Errorf("mattermost chat handler is unavailable"))
	}
	request := mattermostChatRequest(msg, receivedAt)
	result, processErr := h.chat.HandleChat(ctx, request)
	if processErr != nil {
		h.logger.Warn().Err(processErr).
			Str("post_id", msg.PostID).
			Str("settlement", string(result.Settlement.Outcome)).
			Str("reason", result.Settlement.Reason).
			Msg("mattermost inbound did not settle")
		return result.Settlement, processErr
	}
	h.logger.Debug().
		Str("post_id", msg.PostID).
		Str("settlement", string(result.Settlement.Outcome)).
		Msg("mattermost inbound settled")
	return result.Settlement, nil
}

func mattermostChatRequest(msg mattermost.InboundMessage, receivedAt time.Time) chatapp.Request {
	postID := strings.TrimSpace(msg.PostID)
	messageID := msg.MessageID
	if messageID == 0 {
		messageID = mattermost.ParsePostID(postID)
	}
	inboundID := turncmd.InboundID("")
	if postID != "" {
		inboundID = turncmd.InboundID("mattermost:" + postID)
	}
	request := chatapp.Request{
		ID:                inboundID,
		Text:              strings.TrimSpace(msg.Text),
		Locator:           msg.Locator,
		ProviderMessageID: postID,
		UserID:            strings.TrimSpace(msg.SenderID),
		MessageID:         messageID,
		ReplyToMessageID:  mattermost.ParsePostID(msg.RootID),
		ReceivedAt:        receivedAt.UTC(),
		DeliveryOptions: deliveryfmt.Options{
			DeliveryFormat: deliveryfmt.DeliveryFormatMarkdown,
			ProgressPolicy: deliveryfmt.ProgressPolicy{Typing: false, PlanUpdates: true},
		},
		Direct: msg.Direct,
		Source: turncmd.SourceMattermost,
	}
	if reply, ok := mattermost.BuildInboundReply(msg.Locator, auth.MattermostSubject(msg.SenderID), msg, receivedAt); ok {
		request.QuestionReply = &reply
	}
	return request
}

// HandleCommand publishes an approved Mattermost command into the shared
// command pipeline.
func (h *mattermostInboundHandler) HandleCommand(ctx context.Context, cmd mattermost.InboundCommand) error {
	if h == nil {
		return nil
	}
	if cmd.Command != commandStart && !h.canAccess(ctx, cmd.SenderID) {
		return h.sendPlain(ctx, cmd.Locator, mattermostAccessDeniedText)
	}
	if h.commandIngress == nil {
		return nil
	}
	invocationID := strings.TrimSpace(cmd.InvocationID)
	if invocationID == "" {
		postID := strings.TrimSpace(cmd.PostID)
		if postID == "" {
			return fmt.Errorf("mattermost command invocation id is required")
		}
		invocationID = fmt.Sprintf("mattermost:command:%s", postID)
	}
	isOwner := h.isOwner(cmd.SenderID)
	return h.commandIngress.PublishCommand(ctx, commandcmd.Request{
		InvocationID: invocationID,
		Payload: commandcmd.Payload{
			Version:   commandcmd.SchemaVersion,
			Name:      cmd.Command,
			Args:      cmd.Args,
			Locator:   cmd.Locator,
			Transport: mattermost.ChannelType,
			Principal: auth.MattermostSubject(cmd.SenderID),
			Access: commandcmd.Access{
				SessionCommands: true,
				Owner:           isOwner,
				Collaborator:    !isOwner,
			},
			Conversation: commandcmd.Conversation{Direct: cmd.Direct},
			Presentation: deliveryfmt.Options{
				DeliveryFormat: deliveryfmt.DeliveryFormatMarkdown,
				ProgressPolicy: deliveryfmt.ProgressPolicy{Typing: false, PlanUpdates: true},
			},
			Invocation: commandcmd.Invocation{Root: "/"},
		},
	})
}

// HandleUnsupportedCommand answers an unknown command instead of ignoring it.
func (h *mattermostInboundHandler) HandleUnsupportedCommand(ctx context.Context, cmd mattermost.InboundCommand) error {
	if h == nil {
		return nil
	}
	return h.sendPlain(ctx, cmd.Locator, fmt.Sprintf(mattermostUnknownCommand, cmd.Command))
}

// authorizeMattermostUser allows the registered owner and verified collaborators.
func (h *mattermostInboundHandler) authorizeMattermostUser(ctx context.Context, userID string) (bool, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return false, nil
	}
	subject := auth.MattermostSubject(userID)
	if h.ownerStore != nil && h.ownerStore.IsOwnerSubject(subject) {
		return true, nil
	}
	if h.collaboratorStore == nil {
		return false, nil
	}
	collaborator, found, err := h.collaboratorStore.GetCollaborator(ctx, subject)
	if err != nil {
		return false, fmt.Errorf("look up mattermost collaborator: %w", err)
	}
	return found && collaborator != nil, nil
}

func (h *mattermostInboundHandler) canAccess(ctx context.Context, userID string) bool {
	allowed, err := h.authorizeMattermostUser(ctx, userID)
	return err == nil && allowed
}

func (h *mattermostInboundHandler) isOwner(userID string) bool {
	if h.ownerStore == nil {
		return false
	}
	return h.ownerStore.IsOwnerSubject(auth.MattermostSubject(userID))
}

func (h *mattermostInboundHandler) sendPlain(ctx context.Context, locator deliverycmd.Locator, text string) error {
	return sendPlain(ctx, h.actorDispatcher, serverActorAddress, locator, text)
}
