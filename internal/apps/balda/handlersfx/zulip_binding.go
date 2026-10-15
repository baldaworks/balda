package handlersfx

import (
	"context"
	"strconv"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/auth"
	"github.com/baldaworks/balda/internal/apps/balda/channel/zulip"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

func (h *zulipInboundHandler) consumeBinding(ctx context.Context, locator deliverycmd.Locator, senderID int, senderEmail, botEmail, payload string, direct bool) {
	if !direct {
		return
	}
	response := "Account binding failed. Use the complete current invitation in this bot's direct conversation, or generate a new invitation in Backoffice Access."
	if h.bindings != nil && h.bindingChannels != nil {
		info, ok := h.bindingChannels.Get(zulip.ChannelType)
		if ok && info.Integration.Key == "" {
			var err error
			info, err = h.bindingChannels.Refresh(ctx, zulip.ChannelType)
			ok = err == nil
		}
		if ok && info.Integration.Key != "" && botEmail != "" && strings.EqualFold(botEmail, info.BotUsername) {
			_, err := h.bindings.Consume(ctx, usercmd.BindingProof{Payload: payload, Integration: info.Integration, Principal: strconv.Itoa(senderID), DisplayName: senderEmail, ProviderUsername: senderEmail, Direct: direct, Locator: locator, Provenance: "zulip-verified-webhook"})
			if err == nil {
				response = "Account connected. You can now use Balda from this conversation."
				h.activateZulipBoundPrimary(ctx, senderID)
			}
		}
	}
	_ = h.sendPlain(ctx, locator, response)
}

func (h *zulipInboundHandler) activateZulipBoundPrimary(ctx context.Context, senderID int) {
	if h.ownerStore == nil {
		return
	}
	primary, err := h.ownerStore.IsPrimaryOwnerSubject(ctx, auth.ZulipSubject(senderID))
	if err != nil {
		h.logger.Warn().Err(err).Msg("could not check primary session activation")
		return
	}
	if !primary {
		return
	}
	if h.sessionManager == nil {
		h.setOwnerID(int64(senderID))
		return
	}
	if err := h.ActivateOwner(ctx, senderID); err != nil {
		h.logger.Warn().Err(err).Msg("account connected but primary session activation failed")
	}
}
