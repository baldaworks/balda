// Package swarm contains Balda's durable actor runtime primitives.
package swarm

import (
	"github.com/normahq/balda/pkg/actorlayer"
	"github.com/normahq/balda/pkg/actorlayer/dispatch"
)

const (
	ActorTypeSystem     = "system"
	ActorTypeSession    = "session"
	ActorTypeTask       = "task"
	ActorTypeGoalkeeper = "goalkeeper"
	ActorTypeGoal       = ActorTypeGoalkeeper
	ActorTypeDelivery   = "delivery"

	NamespaceHumanInbound      = "human.inbound"
	NamespaceWebhookInbound    = "webhook.inbound"
	NamespaceScheduleInbound   = "schedule.inbound"
	NamespaceAgentResult       = "agent.result"
	NamespaceGoalkeeperCommand = "goalkeeper.command"
	NamespaceGoalCommand       = NamespaceGoalkeeperCommand
	NamespaceTaskControl       = "task.control"
	NamespaceTelemetry         = "telemetry"

	KindMessage       = "message"
	KindWebhookEvent  = "webhook_event"
	KindScheduledTask = "scheduled_task"
	KindGoal          = "goal"
	KindCancel        = "cancel"
)

type ActorAddress = actorlayer.ActorAddress
type Envelope = actorlayer.Envelope
type Actor = dispatch.Actor

func SystemAddress(key string) ActorAddress {
	return actorlayer.SystemAddress(key)
}

func WildcardAddress(target string) string {
	return actorlayer.WildcardAddress(target)
}

func EncodeEnvelope(e Envelope) (string, error) {
	return actorlayer.EncodeEnvelope(e)
}

func DecodeEnvelope(raw string) (Envelope, error) {
	return actorlayer.DecodeEnvelope(raw)
}

func AssertEnvelope(envelope any) (Envelope, error) {
	return actorlayer.AssertEnvelope(envelope)
}

func assertEnvelope(envelope any) (Envelope, error) {
	return actorlayer.AssertEnvelope(envelope)
}
