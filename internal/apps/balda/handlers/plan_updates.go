package handlers

import (
	"fmt"
	"strings"

	adksession "google.golang.org/adk/session"
)

const (
	acpPlanMetadataKey = "acp_plan"
	acpUpdateKindKey   = "acp_update_kind"
	acpUpdateKindPlan  = "plan"
	acpPlanEntriesKey  = "entries"
)

func baldaPlanProgressText(ev *adksession.Event) (string, bool) {
	if ev == nil {
		return "", false
	}
	var snapshot map[string]any
	if len(ev.CustomMetadata) != 0 {
		if rawKind, ok := ev.CustomMetadata[acpUpdateKindKey]; ok {
			if kind := strings.TrimSpace(stringValue(rawKind)); kind != "" && kind != acpUpdateKindPlan {
				return "", false
			}
		}
		if candidate, ok := ev.CustomMetadata[acpPlanMetadataKey].(map[string]any); ok {
			snapshot = candidate
		}
	}
	if snapshot == nil && len(ev.Actions.StateDelta) != 0 {
		if candidate, ok := ev.Actions.StateDelta[acpPlanMetadataKey].(map[string]any); ok {
			snapshot = candidate
		}
	}
	if snapshot == nil {
		return "", false
	}
	entries, ok := baldaPlanEntries(snapshot)
	if !ok || len(entries) == 0 {
		return "", false
	}

	lines := make([]string, 0, len(entries)+1)
	lines = append(lines, "Plan update")
	for _, entry := range entries {
		content := strings.TrimSpace(stringValue(entry["content"]))
		if content == "" {
			content = "(no description)"
		}
		status := strings.TrimSpace(stringValue(entry["status"]))
		if status == "" {
			status = "unknown"
		}
		status = strings.ReplaceAll(status, "_", " ")
		lines = append(lines, fmt.Sprintf("- [%s] %s", status, content))
	}
	return strings.Join(lines, "\n"), true
}

func baldaPlanEntries(snapshot map[string]any) ([]map[string]any, bool) {
	rawEntries, ok := snapshot[acpPlanEntriesKey]
	if !ok {
		return nil, false
	}
	switch entries := rawEntries.(type) {
	case []map[string]any:
		if len(entries) == 0 {
			return nil, false
		}
		return entries, true
	case []any:
		normalized := make([]map[string]any, 0, len(entries))
		for _, rawEntry := range entries {
			entry, ok := rawEntry.(map[string]any)
			if !ok {
				return nil, false
			}
			normalized = append(normalized, entry)
		}
		if len(normalized) == 0 {
			return nil, false
		}
		return normalized, true
	default:
		return nil, false
	}
}

func stringValue(raw any) string {
	switch v := raw.(type) {
	case string:
		return v
	case fmt.Stringer:
		return v.String()
	default:
		return fmt.Sprintf("%v", raw)
	}
}
