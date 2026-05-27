package swarm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

type unsupportedActor struct {
	address string
	name    string
}

type memoryActor struct{}

const (
	memoryOpTaskSummary    = "task_summary"
	memoryOpSessionSummary = "session_summary"
	memoryOpFactExtract    = "fact_extract"
	memoryOpContextPack    = "context_pack"
)

type memorySyncPayload struct {
	Operation string `json:"operation,omitempty"`
	Scope     string `json:"scope,omitempty"`
	TaskID    string `json:"task_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	Content   string `json:"content,omitempty"`
}

func NewAgentActor() Actor {
	return unsupportedActor{address: WildcardAddress(ActorTypeAgent), name: ActorTypeAgent}
}

func NewMemoryActor() Actor {
	return memoryActor{}
}

func NewDeliveryActor() Actor {
	return unsupportedActor{address: WildcardAddress(ActorTypeDelivery), name: ActorTypeDelivery}
}

func (a unsupportedActor) Address() string {
	return a.address
}

func (a unsupportedActor) Handle(_ context.Context, env Envelope) error {
	return PolicyError(fmt.Errorf("%s actor does not support %q/%q yet", a.name, env.Namespace, env.Kind))
}

func (memoryActor) Address() string {
	return WildcardAddress(ActorTypeMemory)
}

func (memoryActor) Handle(_ context.Context, env Envelope) error {
	if env.Namespace != NamespaceMemorySync {
		return PolicyError(fmt.Errorf("memory actor does not support %q/%q yet", env.Namespace, env.Kind))
	}
	if strings.TrimSpace(env.PayloadJSON) == "" {
		return nil
	}
	var payload memorySyncPayload
	if err := json.Unmarshal([]byte(env.PayloadJSON), &payload); err != nil {
		return PermanentError(fmt.Errorf("decode memory sync payload: %w", err))
	}
	switch normalizeMemoryOperation(payload.Operation, env.Kind) {
	case "", memoryOpTaskSummary, memoryOpSessionSummary, memoryOpFactExtract, memoryOpContextPack:
		return nil
	default:
		// Unknown operations are treated as noop to keep memory sync idempotent.
		return nil
	}
}

func normalizeMemoryOperation(op string, fallback string) string {
	normalized := strings.ToLower(strings.TrimSpace(op))
	if normalized != "" {
		return normalized
	}
	return strings.ToLower(strings.TrimSpace(fallback))
}
