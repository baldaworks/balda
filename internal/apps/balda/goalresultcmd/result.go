package goalresultcmd

import (
	"encoding/json"
	"strings"
)

const (
	StatusDone          = "done"
	StatusNeedUserInput = "need_user_input"
)

type WorkerResult struct {
	Status   string `json:"status"`
	Summary  string `json:"summary,omitempty"`
	Question string `json:"question,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

func ParseWorkerResult(text string) (WorkerResult, bool) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" || !strings.HasPrefix(trimmed, "{") {
		return WorkerResult{}, false
	}
	var result WorkerResult
	if err := json.Unmarshal([]byte(trimmed), &result); err != nil {
		return WorkerResult{}, false
	}
	result.Status = strings.TrimSpace(strings.ToLower(result.Status))
	result.Summary = strings.TrimSpace(result.Summary)
	result.Question = strings.TrimSpace(result.Question)
	result.Reason = strings.TrimSpace(result.Reason)
	switch result.Status {
	case StatusDone:
		return result, result.Summary != ""
	case StatusNeedUserInput:
		return result, result.Question != ""
	default:
		return WorkerResult{}, false
	}
}
