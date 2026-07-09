package actors

import (
	"github.com/normahq/balda/internal/apps/balda/deliverycmd"
	"github.com/normahq/balda/internal/apps/balda/deliveryfmt"
	"github.com/normahq/balda/internal/apps/balda/progress"
	baldasession "github.com/normahq/balda/internal/apps/balda/session"
	"github.com/normahq/balda/pkg/actorlayer"
)

func DeliveryEnvelope(taskID string, from actorlayer.ActorAddress, locator baldasession.SessionLocator, text string, dedupeSuffix string) (actorlayer.Envelope, error) {
	return AgentReplyDeliveryEnvelope(taskID, from, locator, text, dedupeSuffix)
}

func AgentReplyDeliveryEnvelope(taskID string, from actorlayer.ActorAddress, locator baldasession.SessionLocator, text string, dedupeSuffix string) (actorlayer.Envelope, error) {
	return deliverycmd.AgentReplyEnvelope(taskID, from, locator, text, dedupeSuffix)
}

func AgentReplyDeliveryEnvelopeWithSettlement(taskID string, from actorlayer.ActorAddress, locator baldasession.SessionLocator, settlement deliverycmd.SettlementPolicy, text string, dedupeSuffix string) (actorlayer.Envelope, error) {
	return deliverycmd.AgentReplyEnvelopeWithSettlement(taskID, from, locator, settlement, text, dedupeSuffix)
}

func AgentReplyDeliveryEnvelopeWithProfile(taskID string, from actorlayer.ActorAddress, locator baldasession.SessionLocator, profile deliveryfmt.Profile, text string, dedupeSuffix string) (actorlayer.Envelope, error) {
	return deliverycmd.AgentReplyEnvelopeWithProfile(taskID, from, locator, profile, text, dedupeSuffix)
}

func AgentReplyDeliveryEnvelopeWithProfileAndSettlement(taskID string, from actorlayer.ActorAddress, locator baldasession.SessionLocator, profile deliveryfmt.Profile, settlement deliverycmd.SettlementPolicy, text string, dedupeSuffix string) (actorlayer.Envelope, error) {
	return deliverycmd.AgentReplyEnvelopeWithProfileAndSettlement(taskID, from, locator, profile, settlement, text, dedupeSuffix)
}

func PlainDeliveryEnvelope(taskID string, from actorlayer.ActorAddress, locator baldasession.SessionLocator, text string, dedupeSuffix string) (actorlayer.Envelope, error) {
	return deliverycmd.PlainEnvelope(taskID, from, locator, text, dedupeSuffix)
}

func PlainDeliveryEnvelopeWithSettlement(taskID string, from actorlayer.ActorAddress, locator baldasession.SessionLocator, settlement deliverycmd.SettlementPolicy, text string, dedupeSuffix string) (actorlayer.Envelope, error) {
	return deliverycmd.PlainEnvelopeWithSettlement(taskID, from, locator, settlement, text, dedupeSuffix)
}

func MarkdownDeliveryEnvelope(taskID string, from actorlayer.ActorAddress, locator baldasession.SessionLocator, text string, dedupeSuffix string) (actorlayer.Envelope, error) {
	return deliverycmd.MarkdownEnvelope(taskID, from, locator, text, dedupeSuffix)
}

func MarkdownDeliveryEnvelopeWithSettlement(taskID string, from actorlayer.ActorAddress, locator baldasession.SessionLocator, settlement deliverycmd.SettlementPolicy, text string, dedupeSuffix string) (actorlayer.Envelope, error) {
	return deliverycmd.MarkdownEnvelopeWithSettlement(taskID, from, locator, settlement, text, dedupeSuffix)
}

func MarkdownDeliveryEnvelopeWithProfile(taskID string, from actorlayer.ActorAddress, locator baldasession.SessionLocator, profile deliveryfmt.Profile, text string, dedupeSuffix string) (actorlayer.Envelope, error) {
	return deliverycmd.MarkdownEnvelopeWithProfile(taskID, from, locator, profile, text, dedupeSuffix)
}

func MarkdownDeliveryEnvelopeWithProfileAndSettlement(taskID string, from actorlayer.ActorAddress, locator baldasession.SessionLocator, profile deliveryfmt.Profile, settlement deliverycmd.SettlementPolicy, text string, dedupeSuffix string) (actorlayer.Envelope, error) {
	return deliverycmd.MarkdownEnvelopeWithProfileAndSettlement(taskID, from, locator, profile, settlement, text, dedupeSuffix)
}

func DraftPlainDeliveryEnvelope(taskID string, from actorlayer.ActorAddress, locator baldasession.SessionLocator, draftID int, text string) (actorlayer.Envelope, error) {
	return deliverycmd.DraftPlainEnvelope(taskID, from, locator, draftID, text)
}

func ChatActionDeliveryEnvelope(taskID string, from actorlayer.ActorAddress, locator baldasession.SessionLocator, action string) (actorlayer.Envelope, error) {
	return deliverycmd.ChatActionEnvelope(taskID, from, locator, action)
}

func ProgressActivityDeliveryEnvelope(taskID string, from actorlayer.ActorAddress, locator baldasession.SessionLocator, policy deliveryfmt.ProgressPolicy, sequence int, dedupeSuffix string) (actorlayer.Envelope, error) {
	return deliverycmd.ProgressActivityEnvelope(taskID, from, locator, policy, sequence, dedupeSuffix)
}

func ProgressThinkingDeliveryEnvelope(taskID string, from actorlayer.ActorAddress, locator baldasession.SessionLocator, policy deliveryfmt.ProgressPolicy, visible bool, draftID int, text string, sequence int, dedupeSuffix string) (actorlayer.Envelope, error) {
	return deliverycmd.ProgressThinkingEnvelope(taskID, from, locator, policy, visible, draftID, text, sequence, dedupeSuffix)
}

func ProgressPlanUpdateDeliveryEnvelope(taskID string, from actorlayer.ActorAddress, locator baldasession.SessionLocator, policy deliveryfmt.ProgressPolicy, visible bool, draftID int, plan *progress.PlanSnapshot, text string, dedupeSuffix string) (actorlayer.Envelope, error) {
	return deliverycmd.ProgressPlanUpdateEnvelope(taskID, from, locator, policy, visible, draftID, plan, text, dedupeSuffix)
}

func validateDeliveryPayload(payload DeliveryPayload) error { return deliverycmd.Validate(payload) }
