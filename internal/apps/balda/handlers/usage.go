package handlers

import (
	"context"
	"fmt"
	"strings"

	acpagent "github.com/normahq/go-adk-acpagent/v2"
	baldasession "github.com/normahq/balda/internal/apps/balda/session"
	adksession "google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

const usageStateKey = "balda:last_usage"

type usageSessionReader interface {
	RuntimeStateValue(ctx context.Context, locator baldasession.SessionLocator, key string) (any, bool, error)
}

type usageSnapshot struct {
	PromptTokenCount        int32   `json:"prompt_token_count,omitempty"`
	CachedContentTokenCount int32   `json:"cached_content_token_count,omitempty"`
	ResponseTokenCount      int32   `json:"response_token_count,omitempty"`
	ToolUsePromptTokenCount int32   `json:"tool_use_prompt_token_count,omitempty"`
	ThoughtsTokenCount      int32   `json:"thoughts_token_count,omitempty"`
	TotalTokenCount         int32   `json:"total_token_count,omitempty"`
	TrafficType             string  `json:"traffic_type,omitempty"`
	ContextWindowSize       int32   `json:"context_window_size,omitempty"`
	ContextUsedTokens       int32   `json:"context_used_tokens,omitempty"`
	CostAmount              float64 `json:"cost_amount,omitempty"`
	CostCurrency            string  `json:"cost_currency,omitempty"`
}

func usageSnapshotFromMetadata(meta *genai.GenerateContentResponseUsageMetadata) (usageSnapshot, bool) {
	if meta == nil {
		return usageSnapshot{}, false
	}
	snapshot := usageSnapshot{
		PromptTokenCount:        meta.PromptTokenCount,
		CachedContentTokenCount: meta.CachedContentTokenCount,
		ResponseTokenCount:      meta.CandidatesTokenCount,
		ToolUsePromptTokenCount: meta.ToolUsePromptTokenCount,
		ThoughtsTokenCount:      meta.ThoughtsTokenCount,
		TotalTokenCount:         meta.TotalTokenCount,
		TrafficType:             strings.TrimSpace(string(meta.TrafficType)),
	}
	if snapshot == (usageSnapshot{}) {
		return usageSnapshot{}, false
	}
	return snapshot, true
}

func usageSnapshotFromACPEvent(ev *adksession.Event) (usageSnapshot, bool) {
	if ev == nil || len(ev.CustomMetadata) == 0 {
		return usageSnapshot{}, false
	}
	raw, ok := ev.CustomMetadata[acpagent.SessionUsageMetadataKey].(map[string]any)
	if !ok {
		return usageSnapshot{}, false
	}
	return usageSnapshotFromACPSessionUsageMap(raw)
}

func usageSnapshotFromACPSessionUsageMap(raw map[string]any) (usageSnapshot, bool) {
	if len(raw) == 0 {
		return usageSnapshot{}, false
	}
	snapshot := usageSnapshot{
		ContextWindowSize: int32FromAny(raw["size"]),
		ContextUsedTokens: int32FromAny(raw["used"]),
	}
	if cost, ok := raw["cost"].(map[string]any); ok {
		snapshot.CostAmount = float64FromAny(cost["amount"])
		snapshot.CostCurrency = strings.TrimSpace(stringFromAny(cost["currency"]))
	}
	if !snapshot.hasSessionUsage() {
		return usageSnapshot{}, false
	}
	return snapshot, true
}

func loadUsageSnapshot(ctx context.Context, sessions usageSessionReader, locator baldasession.SessionLocator) (usageSnapshot, bool, error) {
	if sessions == nil {
		return usageSnapshot{}, false, nil
	}
	value, ok, err := sessions.RuntimeStateValue(ctx, locator, usageStateKey)
	if err != nil || !ok {
		return usageSnapshot{}, false, err
	}
	snapshot, ok := value.(map[string]any)
	if !ok {
		return usageSnapshot{}, false, nil
	}
	return usageSnapshotFromMap(snapshot)
}

func usageSnapshotFromMap(raw map[string]any) (usageSnapshot, bool, error) {
	if len(raw) == 0 {
		return usageSnapshot{}, false, nil
	}
	snapshot := usageSnapshot{
		PromptTokenCount:        int32FromAny(raw["prompt_token_count"]),
		CachedContentTokenCount: int32FromAny(raw["cached_content_token_count"]),
		ResponseTokenCount:      int32FromAny(raw["response_token_count"]),
		ToolUsePromptTokenCount: int32FromAny(raw["tool_use_prompt_token_count"]),
		ThoughtsTokenCount:      int32FromAny(raw["thoughts_token_count"]),
		TotalTokenCount:         int32FromAny(raw["total_token_count"]),
		TrafficType:             strings.TrimSpace(stringFromAny(raw["traffic_type"])),
		ContextWindowSize:       int32FromAny(raw["context_window_size"]),
		ContextUsedTokens:       int32FromAny(raw["context_used_tokens"]),
		CostAmount:              float64FromAny(raw["cost_amount"]),
		CostCurrency:            strings.TrimSpace(stringFromAny(raw["cost_currency"])),
	}
	if !snapshot.hasTokenUsage() && !snapshot.hasSessionUsage() {
		return usageSnapshot{}, false, nil
	}
	return snapshot, true, nil
}

func renderUsageSnapshot(snapshot usageSnapshot) string {
	lines := []string{"Last provider usage:"}
	if snapshot.hasTokenUsage() {
		lines = append(lines,
			fmt.Sprintf("Prompt tokens: %d", snapshot.PromptTokenCount),
			fmt.Sprintf("Cached prompt tokens: %d", snapshot.CachedContentTokenCount),
			fmt.Sprintf("Response tokens: %d", snapshot.ResponseTokenCount),
			fmt.Sprintf("Tool-use prompt tokens: %d", snapshot.ToolUsePromptTokenCount),
			fmt.Sprintf("Reasoning tokens: %d", snapshot.ThoughtsTokenCount),
			fmt.Sprintf("Total tokens: %d", snapshot.TotalTokenCount),
		)
		if snapshot.TrafficType != "" {
			lines = append(lines, fmt.Sprintf("Traffic type: %s", snapshot.TrafficType))
		}
	} else {
		lines = append(lines, "Token usage: provider did not include token counts in the last response.")
	}
	if snapshot.hasSessionUsage() {
		lines = append(lines, fmt.Sprintf("Context usage: %d / %d tokens", snapshot.ContextUsedTokens, snapshot.ContextWindowSize))
		if snapshot.CostCurrency != "" {
			lines = append(lines, fmt.Sprintf("Session cost: %.4f %s", snapshot.CostAmount, snapshot.CostCurrency))
		} else if snapshot.CostAmount != 0 {
			lines = append(lines, fmt.Sprintf("Session cost: %.4f", snapshot.CostAmount))
		}
	} else {
		lines = append(lines, "Limits: provider did not include quota/remaining limits in the last response.")
	}
	return strings.Join(lines, "\n")
}

func (s usageSnapshot) hasTokenUsage() bool {
	return s.PromptTokenCount != 0 || s.CachedContentTokenCount != 0 || s.ResponseTokenCount != 0 || s.ToolUsePromptTokenCount != 0 || s.ThoughtsTokenCount != 0 || s.TotalTokenCount != 0 || s.TrafficType != ""
}

func (s usageSnapshot) hasSessionUsage() bool {
	return s.ContextWindowSize != 0 || s.ContextUsedTokens != 0 || s.CostAmount != 0 || s.CostCurrency != ""
}

func mergeUsageSnapshots(base, incoming usageSnapshot) usageSnapshot {
	merged := base
	if incoming.PromptTokenCount != 0 {
		merged.PromptTokenCount = incoming.PromptTokenCount
	}
	if incoming.CachedContentTokenCount != 0 {
		merged.CachedContentTokenCount = incoming.CachedContentTokenCount
	}
	if incoming.ResponseTokenCount != 0 {
		merged.ResponseTokenCount = incoming.ResponseTokenCount
	}
	if incoming.ToolUsePromptTokenCount != 0 {
		merged.ToolUsePromptTokenCount = incoming.ToolUsePromptTokenCount
	}
	if incoming.ThoughtsTokenCount != 0 {
		merged.ThoughtsTokenCount = incoming.ThoughtsTokenCount
	}
	if incoming.TotalTokenCount != 0 {
		merged.TotalTokenCount = incoming.TotalTokenCount
	}
	if incoming.TrafficType != "" {
		merged.TrafficType = incoming.TrafficType
	}
	if incoming.ContextWindowSize != 0 {
		merged.ContextWindowSize = incoming.ContextWindowSize
	}
	if incoming.ContextUsedTokens != 0 {
		merged.ContextUsedTokens = incoming.ContextUsedTokens
	}
	if incoming.CostAmount != 0 {
		merged.CostAmount = incoming.CostAmount
	}
	if incoming.CostCurrency != "" {
		merged.CostCurrency = incoming.CostCurrency
	}
	return merged
}

func usageSnapshotStateMap(snapshot usageSnapshot) map[string]any {
	return map[string]any{
		"prompt_token_count":          snapshot.PromptTokenCount,
		"cached_content_token_count":  snapshot.CachedContentTokenCount,
		"response_token_count":        snapshot.ResponseTokenCount,
		"tool_use_prompt_token_count": snapshot.ToolUsePromptTokenCount,
		"thoughts_token_count":        snapshot.ThoughtsTokenCount,
		"total_token_count":           snapshot.TotalTokenCount,
		"traffic_type":                snapshot.TrafficType,
		"context_window_size":         snapshot.ContextWindowSize,
		"context_used_tokens":         snapshot.ContextUsedTokens,
		"cost_amount":                 snapshot.CostAmount,
		"cost_currency":               snapshot.CostCurrency,
	}
}

func int32FromAny(value any) int32 {
	switch v := value.(type) {
	case int:
		return int32(v)
	case int32:
		return v
	case int64:
		return int32(v)
	case float64:
		return int32(v)
	case float32:
		return int32(v)
	default:
		return 0
	}
}

func stringFromAny(value any) string {
	switch v := value.(type) {
	case string:
		return v
	default:
		return ""
	}
}

func float64FromAny(value any) float64 {
	switch v := value.(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	case int:
		return float64(v)
	case int32:
		return float64(v)
	case int64:
		return float64(v)
	default:
		return 0
	}
}
