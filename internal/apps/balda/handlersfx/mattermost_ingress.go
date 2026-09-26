package handlersfx

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/actorcmd"
	"github.com/baldaworks/balda/internal/apps/balda/auth"
	"github.com/baldaworks/balda/internal/apps/balda/channel/mattermost"
	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/baldaworks/balda/internal/apps/balda/ingressapp"
	baldasession "github.com/baldaworks/balda/internal/apps/balda/session"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/rs/zerolog"
	"go.uber.org/fx"
)

const (
	mattermostIngressReasonProviderUnavailable = "provider_unavailable"
	mattermostIngressReasonSessionUnavailable  = "session_unavailable"

	mattermostAccessDeniedText = "You are not allowed to use Balda in this channel."
	mattermostUnknownCommand   = "Unknown command: /%s"
)

// mattermostInboundHandler adapts Mattermost websocket posts and commands to the
// transport-neutral conversational ingress service.
//
// It owns no conversation policy: authorization, session preparation and
// durable acceptance all live in ingressapp, and command execution is published
// through commandcmd.Ingress. This type only maps Mattermost input onto those
// shared contracts, which is why it is far smaller than the Telegram handler
// (no forum topics, no callback buttons, no question replies).
//
// It is injected as the optional mattermost.InboundProcessor dependency of the
// websocket ingress. Without this provider the ingress accepts every post and
// then silently discards it.
type mattermostInboundHandler struct {
	ownerStore        *auth.OwnerStore
	collaboratorStore *auth.CollaboratorStore
	sessionManager    *baldasession.Manager
	actorDispatcher   actortransport.Dispatcher
	commandIngress    commandcmd.Ingress
	authToken         string
	baldaProviderName string
	logger            zerolog.Logger
	now               func() time.Time
}

type mattermostInboundHandlerParams struct {
	fx.In

	OwnerStore        *auth.OwnerStore          `optional:"true"`
	CollaboratorStore *auth.CollaboratorStore   `optional:"true"`
	SessionManager    *baldasession.Manager     `optional:"true"`
	Dispatcher        actortransport.Dispatcher `optional:"true"`
	CommandIngress    commandcmd.Ingress         `optional:"true"`
	AuthToken         string                    `name:"balda_auth_token" optional:"true"`
	BaldaProviderID   string                    `name:"balda_provider" optional:"true"`
	Logger            zerolog.Logger
}

// newMattermostInboundHandler builds the Mattermost inbound processor.
func newMattermostInboundHandler(params mattermostInboundHandlerParams) mattermost.InboundProcessor {
	return &mattermostInboundHandler{
		ownerStore:        params.OwnerStore,
		collaboratorStore: params.CollaboratorStore,
		sessionManager:    params.SessionManager,
		actorDispatcher:   params.Dispatcher,
		commandIngress:    params.CommandIngress,
		authToken:         strings.TrimSpace(params.AuthToken),
		baldaProviderName: strings.TrimSpace(params.BaldaProviderID),
		logger:            params.Logger.With().Str("component", "balda.handlersfx.mattermost").Logger(),
	}
}

// ProcessInbound normalizes one Mattermost post and hands it to the shared
// ingress service.
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
	post := mattermost.Post{
		ID:        strings.TrimSpace(msg.PostID),
		Message:   strings.TrimSpace(msg.Text),
		UserID:    strings.TrimSpace(msg.SenderID),
		ChannelID: mattermost.ChannelIDOf(msg.Locator),
	}
	normalized := mattermost.NormalizeInbound(msg.Locator, post, msg.SenderName, msg.Direct, receivedAt)

	service, err := h.ingressService()
	if err != nil {
		return turncmd.InboundSettlement{}, err
	}
	result, processErr := service.Process(ctx, normalized)
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
	isOwner := h.isOwner(cmd.SenderID)
	return h.commandIngress.PublishCommand(ctx, commandcmd.Request{
		InvocationID: fmt.Sprintf("mattermost:command:%s", cmd.PostID),
		Payload: commandcmd.Payload{
			Version: commandcmd.SchemaVersion,
			Name:    cmd.Command,
			Args:    cmd.Args,
			Locator: cmd.Locator,
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

func (h *mattermostInboundHandler) ingressService() (*ingressapp.Service, error) {
	if h == nil {
		return nil, fmt.Errorf("mattermost inbound handler is required")
	}
	return ingressapp.NewWithLogger(
		ingressapp.AuthorizerFunc(h.authorizeInbound),
		ingressapp.SessionPreparerFunc(h.prepareSession),
		ingressapp.DispatcherFunc(h.dispatchInbound),
		h.logger,
	)
}

// authorizeInbound allows the registered owner and any verified collaborator.
func (h *mattermostInboundHandler) authorizeInbound(ctx context.Context, inbound ingressapp.InboundContext) (ingressapp.Authorization, error) {
	userID := strings.TrimSpace(inbound.UserID)
	if userID == "" {
		return ingressapp.Authorization{Reason: ingressapp.ReasonUnauthorized}, nil
	}
	subject := auth.MattermostSubject(userID)
	if h.ownerStore != nil && h.ownerStore.IsOwnerSubject(subject) {
		return ingressapp.Authorization{Allowed: true}, nil
	}
	if h.collaboratorStore == nil {
		return ingressapp.Authorization{Reason: ingressapp.ReasonUnauthorized}, nil
	}
	collaborator, found, err := h.collaboratorStore.GetCollaborator(ctx, subject)
	if err != nil {
		return ingressapp.Authorization{}, fmt.Errorf("look up mattermost collaborator: %w", err)
	}
	return ingressapp.Authorization{Allowed: found && collaborator != nil, Reason: ingressapp.ReasonUnauthorized}, nil
}

// prepareSession ensures a session exists for the locator before durable
// acceptance, mirroring the Telegram flow minus the DM-welcome choreography.
func (h *mattermostInboundHandler) prepareSession(ctx context.Context, inbound ingressapp.InboundContext) (ingressapp.SessionPreparation, error) {
	if h.sessionManager == nil {
		return ingressapp.SessionPreparation{Reason: mattermostIngressReasonSessionUnavailable}, nil
	}
	locator := baldasession.SessionLocator{
		ChannelType: inbound.ChannelType,
		AddressKey:  inbound.AddressKey,
		AddressJSON: inbound.AddressJSON,
		SessionID:   inbound.SessionID,
	}
	transportUserID := strings.TrimSpace(inbound.UserID)
	providerName := h.providerName()

	session, err := h.sessionManager.GetSession(locator)
	if err != nil || session == nil {
		if err != nil && !errors.Is(err, baldasession.ErrNoPersistedSession) {
			h.logger.Warn().Err(err).Msg("failed to look up mattermost session; attempting restore")
		}
		restored, restoreErr := h.sessionManager.RestoreSession(ctx, baldasession.SessionContext{
			Locator:                    locator,
			UserID:                     transportUserID,
			AllowBaldaProviderFallback: false,
		})
		if restoreErr != nil {
			if !errors.Is(restoreErr, baldasession.ErrNoPersistedSession) {
				h.logger.Warn().Err(restoreErr).Msg("failed to restore mattermost session; creating a new one")
			}
			if providerName == "" {
				return ingressapp.SessionPreparation{Reason: mattermostIngressReasonProviderUnavailable}, nil
			}
			created, createErr := h.sessionManager.EnsureSession(ctx, baldasession.SessionContext{
				Locator: locator,
				UserID:  transportUserID,
			}, autoSessionLabel)
			if createErr != nil {
				h.logger.Error().Err(createErr).Str("agent", providerName).Msg("failed to create mattermost session")
				return ingressapp.SessionPreparation{Reason: mattermostIngressReasonSessionUnavailable}, nil
			}
			session = created
		} else {
			session = restored
		}
	}
	if session == nil {
		return ingressapp.SessionPreparation{Reason: mattermostIngressReasonSessionUnavailable}, nil
	}
	return ingressapp.SessionPreparation{
		Ready:             true,
		UserID:            session.GetUserID(),
		RequesterUserID:   transportUserID,
		AgentSessionID:    session.GetAgentSessionID(),
		TopicID:           inbound.TopicID,
		WorkspaceDir:      session.GetWorkspaceDir(),
		RuntimeSnapshotID: session.GetRuntimeSnapshotID(),
	}, nil
}

func (h *mattermostInboundHandler) dispatchInbound(ctx context.Context, envelope actorlayer.Envelope) (*actortransport.DispatchReceipt, error) {
	if h.actorDispatcher == nil {
		return nil, actorlayer.TransientError(errors.New("mattermost ingress dispatcher is unavailable"))
	}
	receipt, err := h.actorDispatcher.Dispatch(ctx, envelope)
	if err == nil {
		return receipt, nil
	}
	if actorcmd.IsCommandQueueFull(err) {
		return nil, actorlayer.TransientError(err)
	}
	return receipt, err
}

func (h *mattermostInboundHandler) canAccess(ctx context.Context, userID string) bool {
	subject := auth.MattermostSubject(userID)
	if h.ownerStore != nil && h.ownerStore.IsOwnerSubject(subject) {
		return true
	}
	if h.collaboratorStore == nil {
		return false
	}
	collaborator, found, err := h.collaboratorStore.GetCollaborator(ctx, subject)
	return err == nil && found && collaborator != nil
}

func (h *mattermostInboundHandler) isOwner(userID string) bool {
	if h.ownerStore == nil {
		return false
	}
	return h.ownerStore.IsOwnerSubject(auth.MattermostSubject(userID))
}

func (h *mattermostInboundHandler) providerName() string {
	if h.sessionManager != nil {
		if name := strings.TrimSpace(h.sessionManager.BaldaProviderID()); name != "" {
			return name
		}
	}
	return h.baldaProviderName
}

func (h *mattermostInboundHandler) sendPlain(ctx context.Context, locator deliverycmd.Locator, text string) error {
	return sendPlain(ctx, h.actorDispatcher, serverActorAddress, locator, text)
}
