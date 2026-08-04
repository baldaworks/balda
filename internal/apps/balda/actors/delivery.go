package actors

import (
	"github.com/baldaworks/go-actorlayer"
	"github.com/normahq/balda/internal/apps/balda/deliverycmd"
	"github.com/normahq/balda/internal/apps/balda/deliveryfmt"
	"github.com/normahq/balda/internal/apps/balda/progress"
	baldasession "github.com/normahq/balda/internal/apps/balda/session"
)

func DeliveryEnvelope(jobID string, from actorlayer.ActorAddress, locator baldasession.SessionLocator, text string, dedupeSuffix string) (actorlayer.Envelope, error) {
	return AgentReplyDeliveryEnvelope(jobID, from, locator, text, dedupeSuffix)
}

func AgentReplyDeliveryEnvelope(jobID string, from actorlayer.ActorAddress, locator baldasession.SessionLocator, text string, dedupeSuffix string) (actorlayer.Envelope, error) {
	return deliverycmd.AgentReplyEnvelope(jobID, from, locator, text, dedupeSuffix)
}

func AgentReplyDeliveryEnvelopeWithSettlement(jobID string, from actorlayer.ActorAddress, locator baldasession.SessionLocator, settlement deliverycmd.SettlementPolicy, text string, dedupeSuffix string) (actorlayer.Envelope, error) {
	return deliverycmd.AgentReplyEnvelopeWithSettlement(jobID, from, locator, settlement, text, dedupeSuffix)
}

func AgentReplyDeliveryEnvelopeWithFormat(jobID string, from actorlayer.ActorAddress, locator baldasession.SessionLocator, format deliveryfmt.DeliveryFormat, text string, dedupeSuffix string) (actorlayer.Envelope, error) {
	return deliverycmd.AgentReplyEnvelopeWithFormat(jobID, from, locator, format, text, dedupeSuffix)
}

func AgentReplyDeliveryEnvelopeWithFormatAndSettlement(jobID string, from actorlayer.ActorAddress, locator baldasession.SessionLocator, format deliveryfmt.DeliveryFormat, settlement deliverycmd.SettlementPolicy, text string, dedupeSuffix string) (actorlayer.Envelope, error) {
	return deliverycmd.AgentReplyEnvelopeWithFormatAndSettlement(jobID, from, locator, format, settlement, text, dedupeSuffix)
}

func PlainDeliveryEnvelope(jobID string, from actorlayer.ActorAddress, locator baldasession.SessionLocator, text string, dedupeSuffix string) (actorlayer.Envelope, error) {
	return deliverycmd.PlainEnvelope(jobID, from, locator, text, dedupeSuffix)
}

func PlainDeliveryEnvelopeWithSettlement(jobID string, from actorlayer.ActorAddress, locator baldasession.SessionLocator, settlement deliverycmd.SettlementPolicy, text string, dedupeSuffix string) (actorlayer.Envelope, error) {
	return deliverycmd.PlainEnvelopeWithSettlement(jobID, from, locator, settlement, text, dedupeSuffix)
}

func MarkdownDeliveryEnvelope(jobID string, from actorlayer.ActorAddress, locator baldasession.SessionLocator, text string, dedupeSuffix string) (actorlayer.Envelope, error) {
	return deliverycmd.MarkdownEnvelope(jobID, from, locator, text, dedupeSuffix)
}

func MarkdownDeliveryEnvelopeWithSettlement(jobID string, from actorlayer.ActorAddress, locator baldasession.SessionLocator, settlement deliverycmd.SettlementPolicy, text string, dedupeSuffix string) (actorlayer.Envelope, error) {
	return deliverycmd.MarkdownEnvelopeWithSettlement(jobID, from, locator, settlement, text, dedupeSuffix)
}

func DraftPlainDeliveryEnvelope(jobID string, from actorlayer.ActorAddress, locator baldasession.SessionLocator, draftID int, text string) (actorlayer.Envelope, error) {
	return deliverycmd.DraftPlainEnvelope(jobID, from, locator, draftID, text)
}

func ChatActionDeliveryEnvelope(jobID string, from actorlayer.ActorAddress, locator baldasession.SessionLocator, action string) (actorlayer.Envelope, error) {
	return deliverycmd.ChatActionEnvelope(jobID, from, locator, action)
}

func ProgressActivityDeliveryEnvelope(jobID string, from actorlayer.ActorAddress, locator baldasession.SessionLocator, policy deliveryfmt.ProgressPolicy, sequence int, dedupeSuffix string) (actorlayer.Envelope, error) {
	return deliverycmd.ProgressActivityEnvelope(jobID, from, locator, actorProgressPolicy(policy), sequence, dedupeSuffix)
}

func ProgressThinkingDeliveryEnvelope(jobID string, from actorlayer.ActorAddress, locator baldasession.SessionLocator, policy deliveryfmt.ProgressPolicy, visible bool, text string, sequence int, dedupeSuffix string) (actorlayer.Envelope, error) {
	return deliverycmd.ProgressThinkingEnvelope(jobID, from, locator, actorProgressPolicy(policy), visible, text, sequence, dedupeSuffix)
}

func ProgressPlanUpdateDeliveryEnvelope(jobID string, from actorlayer.ActorAddress, locator baldasession.SessionLocator, policy deliveryfmt.ProgressPolicy, visible bool, plan *progress.PlanSnapshot, text string, dedupeSuffix string) (actorlayer.Envelope, error) {
	return deliverycmd.ProgressPlanUpdateEnvelope(jobID, from, locator, actorProgressPolicy(policy), visible, deliveryPlanSnapshot(plan), text, dedupeSuffix)
}

func deliveryPlanSnapshot(plan *progress.PlanSnapshot) *deliverycmd.PlanSnapshot {
	if plan == nil {
		return nil
	}
	out := &deliverycmd.PlanSnapshot{Entries: make([]deliverycmd.PlanEntry, 0, len(plan.Entries))}
	for _, entry := range plan.Entries {
		out.Entries = append(out.Entries, deliverycmd.PlanEntry{
			Content: entry.Content,
			Status:  entry.Status,
		})
	}
	return out
}

func actorProgressPolicy(policy deliveryfmt.ProgressPolicy) deliverycmd.ProgressPolicy {
	return deliverycmd.ProgressPolicy{
		Typing:      policy.Typing,
		Thinking:    policy.Thinking,
		PlanUpdates: policy.PlanUpdates,
	}
}
