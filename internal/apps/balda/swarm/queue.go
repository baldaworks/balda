package swarm

import "strings"

const queueMetaMode = "queue_mode"

func QueueModeOf(env Envelope) string {
	if env.Meta == nil {
		return ""
	}
	return strings.TrimSpace(env.Meta[queueMetaMode])
}
