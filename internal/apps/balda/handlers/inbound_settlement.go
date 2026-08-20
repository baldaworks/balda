package handlers

import (
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/ingressapp"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
)

func terminalInbound() turncmd.InboundSettlement {
	return turncmd.InboundSettlement{Outcome: turncmd.InboundTerminal}
}

func retryInbound() turncmd.InboundSettlement {
	return turncmd.InboundSettlement{Outcome: turncmd.InboundRetry}
}

func inboundLocator(inbound ingressapp.InboundContext) deliverycmd.Locator {
	return deliverycmd.Locator{
		ChannelType: inbound.ChannelType,
		AddressKey:  inbound.AddressKey,
		AddressJSON: inbound.AddressJSON,
		SessionID:   inbound.SessionID,
	}
}
