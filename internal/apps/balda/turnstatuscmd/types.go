// Package turnstatuscmd defines the durable conversation-turn status contract.
package turnstatuscmd

import "time"

type State string

const (
	StateActive   State = "active"
	StateTerminal State = "terminal"
)

type ActivityKind string

const (
	ActivityStart      ActivityKind = "start"
	ActivityProvider   ActivityKind = "provider"
	ActivityToolCall   ActivityKind = "tool_call"
	ActivityToolResult ActivityKind = "tool_result"
	ActivityProgress   ActivityKind = "progress"
	ActivityTerminal   ActivityKind = "terminal"
)

type Outcome string

const (
	OutcomeSucceeded   Outcome = "succeeded"
	OutcomeFailed      Outcome = "failed"
	OutcomeCanceled    Outcome = "canceled"
	OutcomeInterrupted Outcome = "interrupted"
	OutcomeUnknown     Outcome = "unknown"
)

type Identity struct {
	SessionID string
	ArchiveID string
	TurnID    string
}

type Start struct {
	Identity
	StartedAt time.Time
	BootID    string
}

type Activity struct {
	Identity
	Kind       ActivityKind
	OccurredAt time.Time
}

type Completion struct {
	Identity
	Outcome     Outcome
	CompletedAt time.Time
}

type Snapshot struct {
	Identity
	State          State
	StartedAt      time.Time
	LastActivityAt time.Time
	ActivityKind   ActivityKind
	CompletedAt    time.Time
	Outcome        Outcome
	BootID         string
	Version        uint64
}
