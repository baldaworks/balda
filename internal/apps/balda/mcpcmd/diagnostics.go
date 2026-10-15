package mcpcmd

import "time"

// DiagnosticOutcome is the terminal, secret-free result of one explicit MCP
// connectivity check. It does not represent durable runtime health.
type DiagnosticOutcome string

const (
	// DiagnosticSucceeded means initialization, complete tool discovery, and
	// cleanup all completed within the diagnostic bound.
	DiagnosticSucceeded DiagnosticOutcome = "succeeded"
	// DiagnosticFailed means the check ended without a trustworthy tool count.
	DiagnosticFailed DiagnosticOutcome = "failed"
)

// DiagnosticResult contains only information safe to present to an operator.
// ToolCount is meaningful only for DiagnosticSucceeded.
type DiagnosticResult struct {
	Outcome   DiagnosticOutcome `json:"outcome"`
	ToolCount int               `json:"tool_count,omitempty"`
	CheckedAt time.Time         `json:"checked_at"`
}
