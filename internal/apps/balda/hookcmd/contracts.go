// Package hookcmd defines transport-neutral managed agent hook contracts.
package hookcmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

// Key identifies the application-wide managed hook record.
const Key = "hooks/managed"

// DecisionAllow and DecisionDeny are the supported synchronous verdicts.
const (
	DecisionAllow = "allow"
	DecisionDeny  = "deny"
)

// EventName identifies a supported lifecycle boundary.
type EventName string

const (
	SessionStart     EventName = "SessionStart"
	UserPromptSubmit EventName = "UserPromptSubmit"
	Stop             EventName = "Stop"
	SessionEnd       EventName = "SessionEnd"
	PreToolUse       EventName = "PreToolUse"
	PostToolUse      EventName = "PostToolUse"
)

var (
	ErrInvalid      = errors.New("invalid agent hook")
	ErrForbidden    = errors.New("agent hook management forbidden")
	ErrConflict     = errors.New("agent hooks changed")
	ErrNotFound     = errors.New("agent hook not found")
	ErrUnavailable  = errors.New("agent hooks unavailable")
	ErrDenied       = errors.New("agent hook denied event")
	ErrExecution    = errors.New("agent hook execution failed")
	ErrUnsupported  = errors.New("native tool hooks unavailable for configured provider")
	ErrSessionReset = errors.New("native tool hooks require a session reset")
)

// Events returns a fresh, ordered list for configuration and validation.
func Events() []EventName {
	return []EventName{SessionStart, UserPromptSubmit, Stop, SessionEnd, PreToolUse, PostToolUse}
}

// ToolEvent reports whether an event requires synchronous provider interception.
func (n EventName) ToolEvent() bool { return n == PreToolUse || n == PostToolUse }

// Valid reports whether the event is implemented by the host.
func (n EventName) Valid() bool {
	for _, event := range Events() {
		if n == event {
			return true
		}
	}
	return false
}

// Definition is an administrator-approved executable and its bounded arguments.
// Event payloads are delivered on stdin and never interpolated into arguments.
type Definition struct {
	Name           string    `json:"name"`
	Event          EventName `json:"event"`
	Enabled        bool      `json:"enabled"`
	Command        string    `json:"command"`
	Args           []string  `json:"args,omitempty"`
	Provider       string    `json:"provider,omitempty"`
	Matcher        string    `json:"matcher,omitempty"`
	TimeoutSeconds int       `json:"timeout_seconds"` // Zero waits until the caller cancels.
}

// ValidName reports whether a name is a canonical browser-safe identity.
func ValidName(name string) bool {
	if len(name) == 0 || len(name) > 64 || name[0] < 'a' || name[0] > 'z' {
		return false
	}
	for _, r := range name {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
			return false
		}
	}
	return true
}

// Validate checks executable input bounds and the implemented event contract.
func (d Definition) Validate() error {
	if !ValidName(d.Name) || !d.Event.Valid() || strings.TrimSpace(d.Command) == "" || len(d.Command) > 4096 || strings.ContainsRune(d.Command, 0) || len(d.Args) > 32 || len(d.Provider) > 128 || strings.TrimSpace(d.Provider) != d.Provider || d.TimeoutSeconds < 0 || uint64(d.TimeoutSeconds) > uint64(math.MaxInt64/int64(time.Second)) || len(d.Matcher) > 256 {
		return ErrInvalid
	}
	if _, err := regexp.Compile(d.Matcher); err != nil {
		return ErrInvalid
	}
	for _, arg := range d.Args {
		if len(arg) > 8192 || strings.ContainsRune(arg, 0) {
			return ErrInvalid
		}
	}
	return nil
}

// Event is the JSON document supplied to a trusted hook process.
type Event struct {
	Name           EventName       `json:"hook_event_name"`
	SessionID      string          `json:"session_id"`
	AgentSessionID string          `json:"agent_session_id,omitempty"`
	Provider       string          `json:"provider,omitempty"`
	CWD            string          `json:"cwd"`
	Source         string          `json:"source,omitempty"`
	Prompt         string          `json:"prompt,omitempty"`
	Response       string          `json:"response,omitempty"`
	ToolName       string          `json:"tool_name,omitempty"`
	ToolCallID     string          `json:"tool_call_id,omitempty"`
	ToolInput      json.RawMessage `json:"tool_input,omitempty"`
	ToolResponse   json.RawMessage `json:"tool_response,omitempty"`
}

// Result contains the synchronous hook verdict and optional prompt context.
type Result struct {
	Decision          string          `json:"decision,omitempty"`
	Reason            string          `json:"reason,omitempty"`
	AdditionalContext string          `json:"additional_context,omitempty"`
	UpdatedInput      json.RawMessage `json:"updated_input,omitempty"`
}

// Record is the durable, optimistic configuration boundary.
type Record struct {
	Version     int64        `json:"version"`
	UpdatedAt   string       `json:"updated_at"`
	Definitions []Definition `json:"definitions"`
}

// Validate verifies records read from storage, including disabled definitions.
func (r Record) Validate() error {
	if r.Version < 0 || len(r.Definitions) > 64 {
		return ErrInvalid
	}
	if r.Version == 0 {
		if r.UpdatedAt != "" || len(r.Definitions) != 0 {
			return ErrInvalid
		}
		return nil
	}
	stamp, err := time.Parse(time.RFC3339Nano, r.UpdatedAt)
	if err != nil || stamp.IsZero() {
		return ErrInvalid
	}
	names := make(map[string]bool, len(r.Definitions))
	for _, d := range r.Definitions {
		if d.Validate() != nil || names[d.Name] {
			return ErrInvalid
		}
		names[d.Name] = true
	}
	return nil
}

// Authority binds a mutation to current administrator browser authority.
type Authority struct {
	UserID            string
	UserVersion       uint64
	CredentialVersion uint64
	MFAVersion        uint64
	SessionID         string
	SessionVersion    uint64
	At                time.Time
}

// Save creates or edits one explicitly approved executable definition.
type Save struct {
	Definition      Definition
	Create          bool
	ExpectedVersion int64
	Authority       Authority
}

// Delete removes one definition against its loaded configuration version.
type Delete struct {
	Name            string
	ExpectedVersion int64
	Authority       Authority
}

// Mutation atomically commits a complete record and a body-free audit envelope.
type Mutation struct {
	Record          Record
	ExpectedVersion int64
	Authority       Authority
	Audit           usercmd.AuditEvent
}

// Validate checks the record version and the immutable management audit envelope.
func (m Mutation) Validate() error {
	a := m.Authority
	if a.UserID == "" || a.UserVersion == 0 || a.CredentialVersion == 0 || a.SessionID == "" || a.SessionVersion == 0 || a.At.IsZero() || m.ExpectedVersion < 0 || m.ExpectedVersion == math.MaxInt64 || m.Record.Version != m.ExpectedVersion+1 || m.Record.Validate() != nil {
		return ErrInvalid
	}
	if usercmd.ValidateAuditEvent(m.Audit) != nil || m.Audit.Action != usercmd.AuditActionHookChanged || m.Audit.TargetType != usercmd.AuditTargetHook || !ValidName(m.Audit.TargetID) || m.Audit.ActorUserID != a.UserID || m.Audit.ActorSessionID != a.SessionID || m.Audit.Outcome != usercmd.AuditOutcomeSucceeded || !m.Audit.OccurredAt.Equal(a.At) {
		return ErrInvalid
	}
	return nil
}

// DeniedError retains trusted feedback while supporting errors.Is(ErrDenied).
type DeniedError struct {
	Hook   string
	Reason string
}

func (e *DeniedError) Error() string { return fmt.Sprintf("agent hook %q denied event", e.Hook) }
func (e *DeniedError) Unwrap() error { return ErrDenied }
