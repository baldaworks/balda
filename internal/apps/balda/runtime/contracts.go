// Package runtime contains Balda's actor runtime host and transport-facing contracts.
package runtime

const (
	ActorTypeSystem     = "system"
	ActorTypeSession    = "session"
	ActorTypeTask       = "task"
	ActorTypeGoalkeeper = "goalkeeper"
	ActorTypeGoal       = ActorTypeGoalkeeper
	ActorTypeDelivery   = "delivery"
	ActorTypeMemory     = "memory"

	NamespaceHumanInbound      = "human.inbound"
	NamespaceWebhookInbound    = "webhook.inbound"
	NamespaceScheduleInbound   = "schedule.inbound"
	NamespaceAgentResult       = "agent.result"
	NamespaceGoalkeeperCommand = "goalkeeper.command"
	NamespaceGoalCommand       = NamespaceGoalkeeperCommand
	NamespaceTaskControl       = "task.control"
	NamespaceMemoryCommand     = "memory.command"
	NamespaceTelemetry         = "telemetry"

	KindMessage        = "message"
	KindWebhookEvent   = "webhook_event"
	KindScheduledTask  = "scheduled_task"
	KindGoal           = "goal"
	KindCancel         = "cancel"
	KindMemoryRemember = "memory_remember"
)
