package natsbus

import (
	gnats "github.com/nats-io/nats.go"
	"github.com/normahq/balda/internal/apps/balda/swarm"
	"github.com/normahq/balda/pkg/actorlayer"
)

func messageFromEnvelope(subject string, env actorlayer.Envelope) (*gnats.Msg, error) {
	data, err := actorlayer.EncodeEnvelope(env)
	if err != nil {
		return nil, err
	}
	msg := gnats.NewMsg(subject)
	msg.Data = []byte(data)
	for key, value := range swarm.EnvelopeHeaders(env) {
		msg.Header.Set(key, value)
	}
	return msg, nil
}
