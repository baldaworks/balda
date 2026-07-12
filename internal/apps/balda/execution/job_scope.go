package execution

import (
	"github.com/normahq/balda/internal/apps/balda/actorcmd"
	"github.com/baldaworks/go-actorlayer"
)

const JobIDMetaKey = actorcmd.JobIDMetaKey
const SessionIDMetaKey = actorcmd.SessionIDMetaKey

func EnvelopeJobID(env actorlayer.Envelope) string { return actorcmd.EnvelopeJobID(env) }
func EnvelopeSessionID(env actorlayer.Envelope) string { return actorcmd.EnvelopeSessionID(env) }

func WithJobIDMeta(meta map[string]string, jobID string) map[string]string {
	return actorcmd.WithJobIDMeta(meta, jobID)
}

func WithSessionIDMeta(meta map[string]string, sessionID string) map[string]string {
	return actorcmd.WithSessionIDMeta(meta, sessionID)
}
