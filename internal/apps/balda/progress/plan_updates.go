package progress

import (
	"fmt"
	"strings"

	adksession "google.golang.org/adk/v2/session"
)

const (
	ACPPlanMetadataKey = "acp_plan"
	ACPUpdateKindKey   = "acp_update_kind"
	ACPUpdateKindPlan  = "plan"
	acpPlanEntriesKey  = "entries"
)

type PlanSnapshot struct {
	Entries []PlanEntry `json:"entries,omitempty"`
}

type PlanEntry struct {
	Content string `json:"content"`
	Status  string `json:"status,omitempty"`
}

func ParsePlanUpdate(ev *adksession.Event) (PlanSnapshot, bool) {
	if ev == nil {
		return PlanSnapshot{}, false
	}
	var snapshot map[string]any
	if len(ev.CustomMetadata) != 0 {
		if rawKind, ok := ev.CustomMetadata[ACPUpdateKindKey]; ok {
			kind := strings.TrimSpace(stringifyPlanValue(rawKind))
			if kind != "" && kind != ACPUpdateKindPlan {
				return PlanSnapshot{}, false
			}
		}
		if candidate, ok := ev.CustomMetadata[ACPPlanMetadataKey].(map[string]any); ok {
			snapshot = candidate
		}
	}
	if snapshot == nil && len(ev.Actions.StateDelta) != 0 {
		if candidate, ok := ev.Actions.StateDelta[ACPPlanMetadataKey].(map[string]any); ok {
			snapshot = candidate
		}
	}
	if snapshot == nil {
		return PlanSnapshot{}, false
	}
	rawEntries, ok := snapshot[acpPlanEntriesKey]
	if !ok {
		return PlanSnapshot{}, false
	}
	entries, ok := parsePlanEntries(rawEntries)
	if !ok || len(entries) == 0 {
		return PlanSnapshot{}, false
	}
	return PlanSnapshot{Entries: entries}, true
}

func PlanUpdateText(ev *adksession.Event) (string, bool) {
	snapshot, ok := ParsePlanUpdate(ev)
	if !ok {
		return "", false
	}
	return snapshot.PlainText(), true
}

func (s PlanSnapshot) Empty() bool {
	return len(s.Entries) == 0
}

func (s PlanSnapshot) PlainText() string {
	if s.Empty() {
		return ""
	}
	lines := make([]string, 0, len(s.Entries)+1)
	lines = append(lines, "Plan update")
	for _, entry := range s.Entries {
		lines = append(lines, fmt.Sprintf("- [%s] %s", normalizedPlanStatus(entry.Status), normalizedPlanContent(entry.Content)))
	}
	return strings.Join(lines, "\n")
}

func parsePlanEntries(rawEntries any) ([]PlanEntry, bool) {
	var rawList []map[string]any
	switch typed := rawEntries.(type) {
	case []map[string]any:
		rawList = typed
	case []any:
		rawList = make([]map[string]any, 0, len(typed))
		for _, rawEntry := range typed {
			entry, ok := rawEntry.(map[string]any)
			if !ok {
				return nil, false
			}
			rawList = append(rawList, entry)
		}
	default:
		return nil, false
	}
	entries := make([]PlanEntry, 0, len(rawList))
	for _, rawEntry := range rawList {
		entries = append(entries, PlanEntry{
			Content: normalizedPlanContent(stringifyPlanValue(rawEntry["content"])),
			Status:  normalizedPlanStatus(stringifyPlanValue(rawEntry["status"])),
		})
	}
	if len(entries) == 0 {
		return nil, false
	}
	return entries, true
}

func stringifyPlanValue(v any) string {
	switch typed := v.(type) {
	case string:
		return typed
	case fmt.Stringer:
		return typed.String()
	default:
		return fmt.Sprintf("%v", v)
	}
}

func normalizedPlanContent(content string) string {
	content = strings.TrimSpace(content)
	if content == "" {
		return "(no description)"
	}
	return content
}

func normalizedPlanStatus(status string) string {
	status = strings.TrimSpace(status)
	if status == "" {
		return "unknown"
	}
	return strings.ReplaceAll(status, "_", " ")
}
