package statusfmt

import (
	"fmt"
	"strings"
	"time"
)

type Field struct {
	Label string
	Value string
}

type Model struct {
	Headline    string
	Operational []Field
	Request     string
	Truncated   bool
	Diagnostics []Field
}

// ModelFor produces the shared English semantics used by every transport.
func ModelFor(body Response) Model {
	now := body.ObservedAt.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	state := strings.TrimSpace(body.ConversationState)
	if state == "" {
		if body.TurnActive {
			state = "working"
		} else {
			state = "inactive"
		}
	}
	model := Model{}
	switch state {
	case "working":
		model.Headline = "🟢 Balda is working"
		model.Operational = append(model.Operational,
			Field{"Turn", joinNonEmpty("active", formatDuration(elapsed(body.TurnStartedAt, now)))},
			Field{"Last activity", activityValue(body, now)},
			Field{"Started", formatTime(body.TurnStartedAt)},
			Field{"Queued", fmt.Sprintf("%d", body.PendingTurns)},
		)
	case "waiting":
		model.Headline = "🟡 Balda is waiting for your response"
		model.Operational = append(model.Operational,
			Field{"Waiting", joinNonEmpty(formatDuration(elapsed(waitingSince(body), now)), waitingLabel(body.WaitingKind))},
		)
		if !body.LastActivityAt.IsZero() {
			model.Operational = append(model.Operational, Field{"Last activity", activityValue(body, now)})
		}
	case "unavailable":
		model.Headline = "🟠 Conversation status is temporarily unavailable"
		model.Operational = append(model.Operational, Field{"Turn status", "unavailable"})
	default:
		model.Headline = "⚪ No conversation turn is active"
		if body.TurnState == "terminal" || strings.TrimSpace(body.TurnOutcome) != "" {
			last := strings.TrimSpace(body.TurnOutcome)
			if last == "" {
				last = "unknown"
			}
			if !body.TurnCompletedAt.IsZero() {
				last = joinNonEmpty(last, formatDuration(elapsed(body.TurnCompletedAt, now))+" ago")
			}
			model.Operational = append(model.Operational, Field{"Last turn", last})
			if !body.TurnCompletedAt.IsZero() {
				model.Operational = append(model.Operational,
					Field{"Duration", formatDuration(elapsed(body.TurnStartedAt, body.TurnCompletedAt))},
					Field{"Completed", formatTime(body.TurnCompletedAt)},
				)
			}
		}
	}
	model.Request = firstNonEmpty(body.RequestExcerpt, body.UserText)
	model.Truncated = body.RequestTruncated || body.UserTextTruncated
	if model.Request == "" || state == "inactive" || state == "unavailable" {
		model.Request = ""
	}
	if len(body.DegradedSources) > 0 && state != "unavailable" {
		model.Operational = append(model.Operational, Field{"Status data", "partial"})
	}
	model.Diagnostics = []Field{
		{"Provider", joinNonEmpty(body.ProviderID, body.ProviderType, body.Model, body.ReasoningEffort)},
		{"Session", joinNonEmpty(body.SessionID, body.SessionState)},
		{"Channel", body.ChannelType},
	}
	return model
}

func waitingSince(body Response) time.Time {
	return body.WaitingSince
}

func waitingLabel(kind string) string {
	switch strings.TrimSpace(kind) {
	case "permission":
		return "permission"
	case "auto":
		return "auto mode"
	default:
		return "question"
	}
}

func activityValue(body Response, now time.Time) string {
	if body.LastActivityAt.IsZero() {
		return "unavailable"
	}
	label := strings.ReplaceAll(strings.TrimSpace(body.ActivityKind), "_", " ")
	return joinNonEmpty(formatDuration(elapsed(body.LastActivityAt, now))+" ago", label)
}

func elapsed(start, end time.Time) time.Duration {
	if start.IsZero() || end.Before(start) {
		return 0
	}
	return end.Sub(start)
}

func formatDuration(value time.Duration) string {
	value = value.Truncate(time.Second)
	if value < 0 {
		value = 0
	}
	hours := int(value / time.Hour)
	minutes := int(value%time.Hour) / int(time.Minute)
	seconds := int(value%time.Minute) / int(time.Second)
	switch {
	case hours > 0:
		return fmt.Sprintf("%dh %02dm", hours, minutes)
	case minutes > 0:
		return fmt.Sprintf("%dm %02ds", minutes, seconds)
	default:
		return fmt.Sprintf("%ds", seconds)
	}
}

func formatTime(value time.Time) string {
	if value.IsZero() {
		return "unavailable"
	}
	return value.UTC().Format("15:04:05 UTC")
}

func joinNonEmpty(values ...string) string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" && value != "unavailable" {
			result = append(result, value)
		}
	}
	if len(result) == 0 {
		return "unavailable"
	}
	return strings.Join(result, " · ")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
