package handlers

import (
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/ingressapp"
)

func inboundLocator(inbound ingressapp.InboundContext) deliverycmd.Locator {
	return deliverycmd.Locator{
		ChannelType: inbound.ChannelType,
		AddressKey:  inbound.AddressKey,
		AddressJSON: inbound.AddressJSON,
		SessionID:   inbound.SessionID,
	}
}
