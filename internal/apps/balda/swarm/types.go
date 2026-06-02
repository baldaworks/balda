// Package swarm contains Balda's durable actor runtime primitives.
package swarm

import (
	actorengine "github.com/normahq/balda/pkg/actorlayer/engine"
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

type ActorAddress = actorengine.ActorAddress
type Envelope = actorengine.Envelope

func SystemAddress(key string) ActorAddress {
	return actorengine.SystemAddress(key)
}

func WildcardAddress(target string) string {
	return actorengine.WildcardAddress(target)
}

func EncodeEnvelope(e Envelope) (string, error) {
	return actorengine.EncodeEnvelope(e)
}

func DecodeEnvelope(raw string) (Envelope, error) {
	return actorengine.DecodeEnvelope(raw)
}
