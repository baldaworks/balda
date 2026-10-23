package statusfmt

import (
	"strings"
	"testing"
	"time"
)

func TestModelForConversationStates(t *testing.T) {
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		body       Response
		headline   string
		fieldLabel string
	}{
		{name: "working", body: Response{ConversationState: "working", ObservedAt: now, TurnStartedAt: now.Add(-time.Minute), LastActivityAt: now.Add(-18 * time.Second), ActivityKind: "tool_result"}, headline: "Balda is working", fieldLabel: "Last activity"},
		{name: "waiting", body: Response{ConversationState: "waiting", WaitingKind: "permission", WaitingSince: now.Add(-4 * time.Minute), ObservedAt: now}, headline: "Balda is waiting for your response", fieldLabel: "Waiting"},
		{name: "inactive", body: Response{ConversationState: "inactive", ObservedAt: now, TurnState: "terminal", TurnOutcome: "succeeded", TurnStartedAt: now.Add(-5 * time.Minute), TurnCompletedAt: now.Add(-3 * time.Minute)}, headline: "No conversation turn is active", fieldLabel: "Last turn"},
		{name: "unavailable", body: Response{ConversationState: "unavailable", ObservedAt: now}, headline: "Conversation status is temporarily unavailable", fieldLabel: "Turn status"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			model := ModelFor(test.body)
			if !strings.Contains(model.Headline, test.headline) {
				t.Fatalf("headline = %q", model.Headline)
			}
			found := false
			for _, field := range model.Operational {
				found = found || field.Label == test.fieldLabel
			}
			if !found {
				t.Fatalf("fields = %+v", model.Operational)
			}
			if strings.Contains(strings.ToLower(model.Headline), "idle") || strings.Contains(model.Headline, "GoalKeeper") {
				t.Fatalf("forbidden headline = %q", model.Headline)
			}
		})
	}
}

func TestModelForRequestAndDurations(t *testing.T) {
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	model := ModelFor(Response{ConversationState: "working", ObservedAt: now, TurnStartedAt: now.Add(-time.Minute), LastActivityAt: now.Add(time.Second), RequestExcerpt: "request", RequestTruncated: true})
	if model.Request != "request" || !model.Truncated {
		t.Fatalf("request = %q truncated=%t", model.Request, model.Truncated)
	}
	for _, field := range model.Operational {
		if strings.HasPrefix(field.Value, "-") {
			t.Fatalf("negative duration in %+v", field)
		}
	}
	restarted := ModelFor(Response{ConversationState: "inactive", RequestExcerpt: "must not leak"})
	if restarted.Request != "" {
		t.Fatalf("inactive request leaked: %q", restarted.Request)
	}
}
