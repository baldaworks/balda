package execution

import (
	"github.com/normahq/balda/internal/apps/balda/actorcmd"
	"github.com/normahq/balda/pkg/actorlayer"
)

const JobIDMetaKey = actorcmd.JobIDMetaKey

func EnvelopeJobID(env actorlayer.Envelope) string { return actorcmd.EnvelopeJobID(env) }

func WithJobIDMeta(meta map[string]string, jobID string) map[string]string {
	return actorcmd.WithJobIDMeta(meta, jobID)
}
