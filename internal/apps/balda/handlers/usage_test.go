package handlers

import (
	"strings"
	"testing"

	acpagent "github.com/normahq/go-adk-acpagent/v2"
	adksession "google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

func TestUsageSnapshotFromMetadata(t *testing.T) {
	snapshot, ok := usageSnapshotFromMetadata(&genai.GenerateContentResponseUsageMetadata{
		PromptTokenCount:        10,
		CachedContentTokenCount: 2,
		CandidatesTokenCount:    5,
		ToolUsePromptTokenCount: 1,
		ThoughtsTokenCount:      3,
		TotalTokenCount:         18,
		TrafficType:             genai.TrafficTypeOnDemand,
	})
	if !ok {
		t.Fatal("usageSnapshotFromMetadata() ok = false, want true")
	}
	if snapshot.TotalTokenCount != 18 {
		t.Fatalf("TotalTokenCount = %d, want 18", snapshot.TotalTokenCount)
	}
	if snapshot.TrafficType != "ON_DEMAND" {
		t.Fatalf("TrafficType = %q, want ON_DEMAND", snapshot.TrafficType)
	}
}

func TestUsageSnapshotFromMap(t *testing.T) {
	snapshot, ok, err := usageSnapshotFromMap(map[string]any{
		"prompt_token_count": float64(10),
		"total_token_count":  float64(15),
		"traffic_type":       "ON_DEMAND",
	})
	if err != nil {
		t.Fatalf("usageSnapshotFromMap() error = %v", err)
	}
	if !ok {
		t.Fatal("usageSnapshotFromMap() ok = false, want true")
	}
	if snapshot.PromptTokenCount != 10 || snapshot.TotalTokenCount != 15 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
}

func TestUsageSnapshotFromACPEvent(t *testing.T) {
	ev := &adksession.Event{}
	ev.CustomMetadata = map[string]any{
		acpagent.SessionUsageMetadataKey: map[string]any{
			"size": 100,
			"used": 25,
			"cost": map[string]any{"amount": 1.25, "currency": "USD"},
		},
	}
	snapshot, ok := usageSnapshotFromACPEvent(ev)
	if !ok {
		t.Fatal("usageSnapshotFromACPEvent() ok = false, want true")
	}
	if snapshot.ContextWindowSize != 100 || snapshot.ContextUsedTokens != 25 {
		t.Fatalf("session usage snapshot = %+v", snapshot)
	}
	if snapshot.CostAmount != 1.25 || snapshot.CostCurrency != "USD" {
		t.Fatalf("cost snapshot = %+v", snapshot)
	}
}

func TestRenderUsageSnapshotIncludesLimitsOnly(t *testing.T) {
	text := renderUsageSnapshot(usageSnapshot{ContextWindowSize: 100, ContextUsedTokens: 25, CostAmount: 1.25, CostCurrency: "USD"})
	if want := "Token usage: provider did not include token counts in the last response."; !strings.Contains(text, want) {
		t.Fatalf("renderUsageSnapshot() = %q, want %q", text, want)
	}
	if want := "Context usage: 25 / 100 tokens"; !strings.Contains(text, want) {
		t.Fatalf("renderUsageSnapshot() = %q, want %q", text, want)
	}
	if want := "Session cost: 1.2500 USD"; !strings.Contains(text, want) {
		t.Fatalf("renderUsageSnapshot() = %q, want %q", text, want)
	}
}
