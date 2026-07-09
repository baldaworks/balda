package swarm

import (
	"fmt"
	"strings"

	"github.com/normahq/balda/pkg/actorlayer"
)

func runtimeAddressOf(env actorlayer.Envelope) (string, error) {
	to, err := env.To.String()
	if err != nil {
		return "", actorlayer.DecodeError(err)
	}
	if strings.TrimSpace(to) == "" {
		return "", actorlayer.DecodeError(fmt.Errorf("empty actor address"))
	}
	return to, nil
}

func actorLaneKeyFromEnvelope(env actorlayer.Envelope) string {
	namespace := strings.TrimSpace(env.Namespace)
	taskID := strings.TrimSpace(env.TaskID)
	if taskID != "" {
		switch namespace {
		case NamespaceTaskControl,
			NamespaceGoalkeeperCommand,
			NamespaceHumanInbound,
			NamespaceWebhookInbound,
			NamespaceScheduleInbound:
			return "task:" + taskID
		case NamespaceAgentResult:
			if strings.EqualFold(strings.TrimSpace(env.To.Target), ActorTypeDelivery) {
				if address := strings.TrimSpace(env.To.Key); address != "" {
					return "delivery:" + address
				}
			}
			return "task:" + taskID
		}
	}
	switch namespace {
	case NamespaceGoalkeeperCommand:
		if key := strings.TrimSpace(env.To.Key); key != "" {
			return "goalkeeper:" + key
		}
	case NamespaceHumanInbound, NamespaceWebhookInbound, NamespaceScheduleInbound:
		if sessionID := strings.TrimSpace(env.SessionID); sessionID != "" {
			return "session:" + sessionID
		}
	}
	if to, err := env.To.String(); err == nil {
		return to
	}
	return strings.TrimSpace(env.ID)
}
