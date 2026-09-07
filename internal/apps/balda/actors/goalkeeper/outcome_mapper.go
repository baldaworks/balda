package goalkeeper

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/goaldelivery"
	"github.com/baldaworks/balda/internal/apps/balda/goalkeepercmd"
	"github.com/baldaworks/balda/internal/apps/balda/redaction"
	baldastate "github.com/baldaworks/balda/internal/apps/balda/state"
)

// GoalOutcomeFromJobRecord maps a storage JobRecord into a transport-neutral GoalOutcome DTO.
func GoalOutcomeFromJobRecord(task baldastate.JobRecord) goalkeepercmd.GoalOutcome {
	var result map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(task.Result)), &result); err != nil {
		result = nil
	}
	parsedOutcome := struct {
		WhatWasDone string
		Validation  string
		Verified    string
		NotVerified string
		NextAction  string
	}{}
	hasOutcome := false
	if len(result) != 0 {
		if outcomeMap, ok := result["reviewable_outcome"].(map[string]any); ok {
			parsedOutcome.WhatWasDone = redaction.Secrets(strings.TrimSpace(fmt.Sprint(outcomeMap["what_was_done"])))
			parsedOutcome.Validation = redaction.Secrets(strings.TrimSpace(fmt.Sprint(outcomeMap["validation_output"])))
			parsedOutcome.Verified = redaction.Secrets(strings.TrimSpace(fmt.Sprint(outcomeMap["what_was_verified"])))
			parsedOutcome.NotVerified = redaction.Secrets(strings.TrimSpace(fmt.Sprint(outcomeMap["what_was_not_verified"])))
			parsedOutcome.NextAction = redaction.Secrets(strings.TrimSpace(fmt.Sprint(outcomeMap["next_action"])))
			hasOutcome = parsedOutcome.WhatWasDone != "" || parsedOutcome.Validation != "" || parsedOutcome.Verified != "" || parsedOutcome.NotVerified != "" || parsedOutcome.NextAction != ""
		}
	}
	goalReached := false
	switch typed := result["goal_reached"].(type) {
	case bool:
		goalReached = typed
	case string:
		goalReached = strings.EqualFold(strings.TrimSpace(typed), "true")
	}
	exportStatus, exportReason, exportError := "", "", ""
	if len(result) != 0 {
		if exportMap, ok := result["export"].(map[string]any); ok {
			exportStatus = redaction.Secrets(strings.TrimSpace(fmt.Sprint(exportMap["status"])))
			exportReason = redaction.Secrets(strings.TrimSpace(fmt.Sprint(exportMap["reason"])))
			exportError = redaction.Secrets(strings.TrimSpace(fmt.Sprint(exportMap["error"])))
		}
	}
	resultText := func(key string) string {
		if len(result) == 0 {
			return ""
		}
		value, ok := result[key]
		if !ok || value == nil {
			return ""
		}
		return strings.TrimSpace(fmt.Sprint(value))
	}
	executorOutput := redaction.Secrets(firstNonEmpty(resultText("executor_output"), resultText("final_text")))
	reviewerOutput := redaction.Secrets(firstNonEmpty(resultText("reviewer_output"), resultText("reviewer_feedback")))
	whatWasDone := firstNonEmpty(executorOutput, task.Objective)
	if hasOutcome {
		whatWasDone = firstNonEmpty(parsedOutcome.WhatWasDone, whatWasDone)
	}
	if !goalReached && task.Status != baldastate.JobStatusCompleted && resultText("final_text") != "" {
		whatWasDone = redaction.Secrets(resultText("final_text"))
	}
	validation := reviewerOutput
	if hasOutcome {
		validation = firstNonEmpty(parsedOutcome.Validation, validation)
	}
	verified := firstNonEmpty(parsedOutcome.Verified, "validator returned feedback")
	notVerified := firstNonEmpty(parsedOutcome.NotVerified, goaldelivery.DefaultNotVerifiedText)
	nextAction := firstNonEmpty(parsedOutcome.NextAction, goaldelivery.DefaultInspectNextAction)

	var iterations int
	if itRaw, ok := result["iterations"].(float64); ok {
		iterations = int(itRaw)
	}

	return goalkeepercmd.GoalOutcome{
		JobID:        strings.TrimSpace(task.ID),
		Objective:    strings.TrimSpace(task.Objective),
		Iterations:   iterations,
		GoalReached:  goalReached,
		WhatWasDone:  whatWasDone,
		Validation:   validation,
		Verified:     verified,
		NotVerified:  notVerified,
		NextAction:   nextAction,
		ExportStatus: exportStatus,
		ExportReason: exportReason,
		ExportError:  exportError,
	}
}
