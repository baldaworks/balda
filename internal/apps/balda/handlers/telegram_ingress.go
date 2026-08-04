package handlers

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	baldaexecution "github.com/normahq/balda/internal/apps/balda/actorcmd"
	"github.com/normahq/balda/internal/apps/balda/ingressapp"
	baldasession "github.com/normahq/balda/internal/apps/balda/session"
	"github.com/normahq/balda/internal/apps/balda/telegramref"
	"github.com/normahq/balda/internal/apps/balda/welcome"
)

const (
	telegramIngressReasonProviderUnavailable = "provider_unavailable"
	telegramIngressReasonSessionUnavailable  = "session_unavailable"
)

func (h *BaldaHandler) authorizeTelegramInbound(
	ctx context.Context,
	inbound ingressapp.InboundContext,
) (ingressapp.Authorization, error) {
	userID, err := telegramref.ParseUserID(inbound.UserID)
	if err != nil {
		return ingressapp.Authorization{Reason: ingressapp.ReasonUnauthorized}, nil
	}
	allowed, err := h.accessCollaboratorScope(ctx, userID)
	if err != nil {
		return ingressapp.Authorization{}, err
	}
	return ingressapp.Authorization{
		Allowed: allowed,
		Reason:  ingressapp.ReasonUnauthorized,
	}, nil
}

func (h *BaldaHandler) prepareTelegramSession(
	ctx context.Context,
	inbound ingressapp.InboundContext,
) (ingressapp.SessionPreparation, error) {
	locator := inboundLocator(inbound)
	transportUserID := strings.TrimSpace(inbound.UserID)

	var ts *baldasession.TopicSession
	var err error
	if inbound.Direct && inbound.TopicID == 0 {
		existingSession, _ := h.sessionManager.GetSession(locator)
		sendOwnerWelcome := existingSession == nil
		baldaProviderName := h.getProviderName()
		if baldaProviderName == "" {
			_ = sendPlain(ctx, h.actorDispatcher, baldaHandlerActorAddress, locator, "Balda is not ready right now. Please close this chat and try again.")
			return rejectedSession(telegramIngressReasonProviderUnavailable), nil
		}
		ts, err = h.sessionManager.EnsureSession(ctx, baldasession.SessionContext{
			Locator: locator,
			UserID:  transportUserID,
		}, ownerSessionLabel)
		if err != nil {
			h.logger.Error().Err(err).Str("agent", baldaProviderName).Msg("failed to ensure main dm session")
			_ = sendPlain(ctx, h.actorDispatcher, baldaHandlerActorAddress, locator, "Could not start this session. Please close this chat and try again.")
			return rejectedSession(telegramIngressReasonSessionUnavailable), nil
		}
		if sendOwnerWelcome {
			metadata := h.sessionManager.GetAgentMetadata(baldaProviderName)
			welcomeMsg := welcome.BuildAgentWelcomeMessage(ownerSessionLabel, ts.GetSessionID(), metadata.Type, metadata.Model, metadata.MCPServers)
			_ = sendMarkdown(ctx, h.actorDispatcher, baldaHandlerActorAddress, locator, welcomeMsg)
			h.sendSessionStartupNotice(ctx, locator, ts.GetSessionID())
		}
	} else {
		ts, err = h.sessionManager.GetSession(locator)
		if err != nil {
			_ = sendPlain(ctx, h.actorDispatcher, baldaHandlerActorAddress, locator, "Restoring agent session...")
			ts, err = h.sessionManager.RestoreSession(ctx, baldasession.SessionContext{
				Locator:                    locator,
				UserID:                     transportUserID,
				AllowBaldaProviderFallback: false,
			})
			if err != nil {
				if errors.Is(err, baldasession.ErrNoPersistedSession) {
					baldaProviderName := h.getProviderName()
					if baldaProviderName == "" {
						_ = sendPlain(ctx, h.actorDispatcher, baldaHandlerActorAddress, locator, "Balda is not ready right now. Please close this chat topic and try again.")
						return rejectedSession(telegramIngressReasonProviderUnavailable), nil
					}
					ts, err = h.sessionManager.EnsureSession(ctx, baldasession.SessionContext{
						Locator: locator,
						UserID:  transportUserID,
					}, autoSessionLabel)
					if err != nil {
						h.logger.Error().Err(err).Str("agent", baldaProviderName).Int("topic_id", inbound.TopicID).Msg("failed to create session")
						_ = sendPlain(ctx, h.actorDispatcher, baldaHandlerActorAddress, locator, "Could not start this session. Please close this chat topic and create a new one.")
						return rejectedSession(telegramIngressReasonSessionUnavailable), nil
					}
				} else {
					h.logger.Warn().Err(err).Int("topic_id", inbound.TopicID).Msg("failed to restore session")
					_ = sendPlain(ctx, h.actorDispatcher, baldaHandlerActorAddress, locator, "Could not restore this session. Please close this chat topic and create a new one.")
					return rejectedSession(telegramIngressReasonSessionUnavailable), nil
				}
			}
			if ts != nil {
				baldaProviderID := h.getProviderName()
				metadata := h.sessionManager.GetAgentMetadata(baldaProviderID)
				welcomeName := ownerSessionLabel
				if inbound.Direct {
					welcomeName = ts.GetAgentName()
				}
				welcomeMsg := welcome.BuildAgentWelcomeMessage(welcomeName, ts.GetSessionID(), metadata.Type, metadata.Model, metadata.MCPServers)
				_ = sendMarkdown(ctx, h.actorDispatcher, baldaHandlerActorAddress, locator, welcomeMsg)
				h.sendSessionStartupNotice(ctx, locator, ts.GetSessionID())
			}
		}
	}

	if ts == nil {
		return rejectedSession(telegramIngressReasonSessionUnavailable), nil
	}
	return ingressapp.SessionPreparation{
		Ready:           true,
		UserID:          ts.GetUserID(),
		RequesterUserID: transportUserID,
		AgentSessionID:  ts.GetAgentSessionID(),
		TopicID:         inbound.TopicID,
	}, nil
}

func rejectedSession(reason string) ingressapp.SessionPreparation {
	return ingressapp.SessionPreparation{Reason: reason}
}

func (h *BaldaHandler) dispatchTelegramInbound(
	ctx context.Context,
	envelope actorlayer.Envelope,
) (*actortransport.DispatchReceipt, error) {
	if h.actorDispatcher == nil {
		return nil, actorlayer.TransientError(fmt.Errorf("telegram ingress dispatcher is unavailable"))
	}
	receipt, err := h.actorDispatcher.Dispatch(ctx, envelope)
	if err == nil {
		return receipt, err
	}
	if baldaexecution.IsCommandQueueFull(err) {
		return nil, actorlayer.TransientError(err)
	}
	return receipt, err
}

func (h *BaldaHandler) telegramIngressService() (*ingressapp.Service, error) {
	if h == nil {
		return nil, fmt.Errorf("balda handler is required")
	}
	return ingressapp.NewWithLogger(
		ingressapp.AuthorizerFunc(h.authorizeTelegramInbound),
		ingressapp.SessionPreparerFunc(h.prepareTelegramSession),
		ingressapp.DispatcherFunc(h.dispatchTelegramInbound),
		h.logger,
	)
}

func (h *BaldaHandler) accessCollaboratorScope(ctx context.Context, userID int64) (bool, error) {
	if h.ownerStore != nil && h.ownerStore.IsOwner(userID) {
		return true, nil
	}
	if h.collaboratorStore == nil {
		return false, nil
	}
	collaborator, found, err := h.collaboratorStore.GetCollaborator(ctx, fmt.Sprintf("%d", userID))
	if err != nil {
		return false, fmt.Errorf("look up telegram collaborator: %w", err)
	}
	return found && collaborator != nil, nil
}

func (h *BaldaHandler) canAccessCollaboratorScope(ctx context.Context, userID int64) bool {
	allowed, err := h.accessCollaboratorScope(ctx, userID)
	return err == nil && allowed
}
