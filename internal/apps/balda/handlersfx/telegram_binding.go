package handlersfx

import (
	"context"
	"strconv"
)

func (h *telegramInboundHandler) activateBoundPrimary(ctx context.Context, userID, chatID int64) {
	if h.ownerStore == nil {
		return
	}
	primary, err := h.ownerStore.IsPrimaryOwnerSubject(ctx, "telegram:"+strconv.FormatInt(userID, 10))
	if err != nil {
		h.logger.Warn().Err(err).Msg("could not check primary session activation")
		return
	}
	if !primary {
		return
	}
	if h.sessionManager == nil {
		h.setOwner(userID, chatID)
		return
	}
	if err := h.ActivateOwner(ctx, userID, chatID); err != nil {
		h.logger.Warn().Err(err).Msg("account connected but primary session activation failed")
	}
}
