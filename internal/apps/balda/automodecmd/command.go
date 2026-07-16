package automodecmd

import (
	"fmt"
	"strings"

	"github.com/baldaworks/go-actorlayer"
	"github.com/google/uuid"
	"github.com/normahq/balda/internal/apps/balda/actorcmd"
	baldasession "github.com/normahq/balda/internal/apps/balda/session"
)

type Payload struct {
	Locator baldasession.SessionLocator `json:"locator"`
	State   map[string]any              `json:"state,omitempty"`
}

func Envelope(payload Payload) (actorlayer.Envelope, error) {
	if strings.TrimSpace(payload.Locator.SessionID) == "" {
		return actorlayer.Envelope{}, fmt.Errorf("session id is required")
	}
	raw, err := actorlayer.MarshalPayload(payload)
	if err != nil {
		return actorlayer.Envelope{}, fmt.Errorf("encode auto mode payload: %w", err)
	}
	id := uuid.NewString()
	return actorlayer.Envelope{
		ID:        id,
		Namespace: actorcmd.NamespaceAutoModeCommand,
		Kind:      actorcmd.KindMessage,
		From:      actorlayer.SystemAddress("auto-mode"),
		To: actorlayer.ActorAddress{
			Target: actorcmd.ActorTypeSession,
			Key:    payload.Locator.SessionID,
		},
		Meta:     actorcmd.WithSessionIDMeta(nil, payload.Locator.SessionID),
		Priority: 100,
		Payload:  raw,
	}, nil
}
