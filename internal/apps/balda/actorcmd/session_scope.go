package actorcmd

import (
	"strings"

	"github.com/baldaworks/go-actorlayer"
)

const SessionIDMetaKey = "session_id"

func EnvelopeSessionID(env actorlayer.Envelope) string {
	if env.Meta != nil {
		if sessionID := strings.TrimSpace(env.Meta[SessionIDMetaKey]); sessionID != "" {
			return sessionID
		}
	}
	if strings.EqualFold(strings.TrimSpace(env.To.Target), ActorTypeSession) {
		return strings.TrimSpace(env.To.Key)
	}
	return ""
}

func WithSessionIDMeta(meta map[string]string, sessionID string) map[string]string {
	trimmed := strings.TrimSpace(sessionID)
	if trimmed == "" {
		return meta
	}
	out := make(map[string]string, len(meta)+1)
	for key, value := range meta {
		out[key] = value
	}
	out[SessionIDMetaKey] = trimmed
	return out
}
