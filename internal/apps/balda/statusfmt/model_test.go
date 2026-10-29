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

func TestModelForRequestedAndActiveModelTruth(t *testing.T) {
	tests := []struct {
		name      string
		body      Response
		requested string
		active    string
	}{
		{name: "requested pending", body: Response{HasRequestedModel: true, RequestedModel: "model-b"}, requested: "model-b (pending)", active: "unknown (not attached)"},
		{name: "requested active", body: Response{HasRequestedModel: true, RequestedModel: "model-b", ActiveModel: "model-b", ActiveModelKind: "provider_observed"}, requested: "model-b (active)", active: "model-b (provider observed)"},
		{name: "no explicit preference", body: Response{}, requested: "none (no explicit preference)", active: "unknown (not attached)"},
		{name: "hosted local selection", body: Response{ActiveModel: "model-a", ActiveModelKind: "locally_executed"}, requested: "none (no explicit preference)", active: "model-a (locally executed)"},
		{name: "unknown evidence kind fails closed", body: Response{ActiveModel: "secret-model", ActiveModelKind: "future"}, requested: "none (no explicit preference)", active: "unknown (not attached)"},
		{name: "matching unknown evidence stays pending", body: Response{HasRequestedModel: true, RequestedModel: "model-b", ActiveModel: "model-b", ActiveModelKind: "future"}, requested: "model-b (pending)", active: "unknown (not attached)"},
		{name: "matching legacy projection stays pending", body: Response{HasRequestedModel: true, RequestedModel: "model-b", Model: "model-b"}, requested: "model-b (pending)", active: "unknown (not attached)"},
		{name: "legacy projection ignored by v3", body: Response{Model: "legacy-projection"}, requested: "none (no explicit preference)", active: "unknown (not attached)"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			model := ModelFor(test.body)
			fields := make(map[string]string, len(model.Diagnostics))
			for _, field := range model.Diagnostics {
				fields[field.Label] = field.Value
			}
			if fields["Requested model"] != test.requested || fields["Active model"] != test.active {
				t.Fatalf("model fields = %v, want requested=%q active=%q", fields, test.requested, test.active)
			}
		})
	}
}

func TestLegacyModelForKeepsProjectionOutOfActiveState(t *testing.T) {
	model := LegacyModelFor(Response{ProviderID: "codex", Model: "legacy-projection"})
	fields := make(map[string]string, len(model.Diagnostics))
	for _, field := range model.Diagnostics {
		fields[field.Label] = field.Value
	}
	if fields["Provider"] != "codex · legacy-projection" {
		t.Fatalf("provider = %q", fields["Provider"])
	}
	if _, ok := fields["Active model"]; ok {
		t.Fatalf("legacy projection rendered as active: %v", fields)
	}
	if _, ok := fields["Requested model"]; ok {
		t.Fatalf("legacy projection rendered as requested: %v", fields)
	}
}
