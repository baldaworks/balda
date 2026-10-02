package handlersfx

import (
	"context"

	"github.com/baldaworks/balda/internal/apps/balda/channel/mattermost"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

func (h *mattermostInboundHandler) consumeBinding(ctx context.Context, proof usercmd.BindingProof) error {
	if !proof.Direct {
		return nil
	}
	response := "Account binding failed. Use the complete current invitation in this bot's direct conversation, or generate a new invitation in Backoffice Access."
	if h.bindings != nil && h.bindingChannels != nil {
		info, ok := h.bindingChannels.Get(mattermost.ChannelType)
		if ok && info.Integration.Key == "" {
			var err error
			info, err = h.bindingChannels.Refresh(ctx, mattermost.ChannelType)
			ok = err == nil
		}
		if ok && info.Integration.Key != "" {
			proof.Integration = info.Integration
			proof.Provenance = "mattermost-verified-ingress"
			if _, err := h.bindings.Consume(ctx, proof); err == nil {
				response = "Account connected. You can now use Balda from this conversation."
			}
		}
	}
	return h.sendPlain(ctx, proof.Locator, response)
}
