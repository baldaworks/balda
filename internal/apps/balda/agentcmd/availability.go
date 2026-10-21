// Package agentcmd owns transport-neutral contracts for provider-dependent work.
package agentcmd

import (
	"errors"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/go-actorlayer"
)

// ErrProviderUnavailable identifies work that cannot run with the selected
// agent provider while the rest of the application remains available.
var ErrProviderUnavailable = errors.New("agent provider unavailable")

// ProviderUnavailableMessage is the bounded user-facing presentation for an
// unavailable agent provider. Internal provider details must not be exposed.
const ProviderUnavailableMessage = "Balda has no available agent provider."

// ProviderUnavailableEnvelope builds the transport-neutral, idempotent
// presentation used when conversational work has no usable provider.
func ProviderUnavailableEnvelope(inboundID string, locator deliverycmd.Locator) (actorlayer.Envelope, error) {
	env, err := deliverycmd.PlainEnvelopeWithSettlement(
		"",
		actorlayer.ActorAddress{Target: "chat", Key: "provider-availability"},
		locator,
		deliverycmd.SettlementBypass,
		ProviderUnavailableMessage,
		"provider-unavailable",
	)
	if err != nil {
		return actorlayer.Envelope{}, err
	}
	if inboundID = strings.TrimSpace(inboundID); inboundID != "" {
		key := inboundID + ":provider-unavailable"
		env.ID, env.DedupeKey, env.CorrelationID, env.CausationID = key, key, inboundID, inboundID
	}
	return env, nil
}
