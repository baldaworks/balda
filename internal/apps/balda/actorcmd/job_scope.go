package actorcmd

import (
	"strings"

	"github.com/normahq/balda/pkg/actorlayer"
)

const JobIDMetaKey = "job_id"

func EnvelopeJobID(env actorlayer.Envelope) string {
	if env.Meta == nil {
		return ""
	}
	return strings.TrimSpace(env.Meta[JobIDMetaKey])
}

func WithJobIDMeta(meta map[string]string, jobID string) map[string]string {
	trimmed := strings.TrimSpace(jobID)
	if trimmed == "" {
		return meta
	}
	out := make(map[string]string, len(meta)+1)
	for key, value := range meta {
		out[key] = value
	}
	out[JobIDMetaKey] = trimmed
	return out
}
