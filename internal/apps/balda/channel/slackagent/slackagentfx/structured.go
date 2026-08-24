package slackagentfx

import (
	"github.com/baldaworks/balda/internal/apps/balda/channel/slackagent/presentation"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
)

type structuredRegistryRegistrar func(*deliveryfmt.StructuredRegistry) error

func registerStructuredRenderers(reg *deliveryfmt.StructuredRegistry) error {
	for _, register := range []structuredRegistryRegistrar{
		presentation.RegisterQuestionRenderer,
		presentation.RegisterPermissionRenderer,
		presentation.RegisterProgressRenderer,
	} {
		if err := register(reg); err != nil {
			return err
		}
	}
	return nil
}
