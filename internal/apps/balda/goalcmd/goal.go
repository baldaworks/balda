package goalcmd

import (
	"fmt"
	"strings"

	"github.com/baldaworks/go-actorlayer"
	"github.com/google/uuid"
	baldaexecution "github.com/normahq/balda/internal/apps/balda/actorcmd"
	"github.com/normahq/balda/internal/apps/balda/deliveryfmt"
	baldasession "github.com/normahq/balda/internal/apps/balda/session"
)

const (
	PayloadKindGoal      = "goal"
	DefaultMaxIterations = 25
)

type EnvelopePayload struct {
	Kind string      `json:"kind"`
	Goal *JobPayload `json:"goal,omitempty"`
}

type JobPayload struct {
	JobID           string                      `json:"job_id,omitempty"`
	Locator         baldasession.SessionLocator `json:"locator"`
	DeliveryOptions deliveryfmt.Options         `json:"delivery_options,omitempty,omitzero"`
	Objective       string                      `json:"objective"`
	TransportUserID string                      `json:"transport_user_id"`
	MaxIterations   int                         `json:"max_iterations,omitempty"`
}

func JobEnvelope(
	locator baldasession.SessionLocator,
	objective string,
	transportUserID string,
	maxIterations int,
) (actorlayer.Envelope, error) {
	return JobEnvelopeWithOptions(locator, deliveryfmt.Options{}, objective, transportUserID, maxIterations)
}

func JobEnvelopeWithOptions(
	locator baldasession.SessionLocator,
	deliveryOptions deliveryfmt.Options,
	objective string,
	transportUserID string,
	maxIterations int,
) (actorlayer.Envelope, error) {
	jobID := "goal-" + locator.SessionID + "-" + uuid.NewString()
	payload := EnvelopePayload{
		Kind: PayloadKindGoal,
		Goal: &JobPayload{
			JobID:           jobID,
			Locator:         locator,
			DeliveryOptions: deliveryfmt.NormalizeOptions(deliveryOptions),
			Objective:       strings.TrimSpace(objective),
			TransportUserID: strings.TrimSpace(transportUserID),
			MaxIterations:   NormalizeMaxIterations(maxIterations),
		},
	}
	data, err := actorlayer.MarshalPayload(payload)
	if err != nil {
		return actorlayer.Envelope{}, fmt.Errorf("encode goal job payload: %w", err)
	}
	return actorlayer.Envelope{
		ID:        uuid.NewString(),
		Namespace: baldaexecution.NamespaceGoalkeeperCommand,
		Kind:      baldaexecution.KindGoal,
		From:      actorlayer.ActorAddress{Target: "telegram", Key: firstNonEmpty(transportUserID, locator.AddressKey, "unknown")},
		To:        actorlayer.ActorAddress{Target: baldaexecution.ActorTypeGoalkeeper, Key: jobID},
		Meta:      baldaexecution.WithSessionIDMeta(baldaexecution.WithJobIDMeta(nil, jobID), locator.SessionID),
		Priority:  90,
		Payload:   data,
	}, nil
}

func NormalizeMaxIterations(v int) int {
	if v <= 0 {
		return DefaultMaxIterations
	}
	return v
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
