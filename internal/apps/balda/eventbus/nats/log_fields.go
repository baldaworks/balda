package natsbus

import (
	"strings"

	baldaruntime "github.com/normahq/balda/internal/apps/balda/runtime"
	"github.com/normahq/balda/pkg/actorlayer"
	"github.com/rs/zerolog"
)

func withDeliveryKey(evt *zerolog.Event, env actorlayer.Envelope) *zerolog.Event {
	if evt == nil {
		return nil
	}
	if !strings.EqualFold(strings.TrimSpace(env.To.Target), baldaruntime.ActorTypeDelivery) {
		return evt
	}
	return evt.Str("delivery_key", strings.TrimSpace(env.To.Key))
}
