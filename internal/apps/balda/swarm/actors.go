package swarm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/normahq/balda/internal/apps/balda/memory"
)

type unsupportedActor struct {
	address string
	name    string
}

type memoryActor struct {
	memoryStore *memory.Store
}

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

func NewMemoryActor() Actor {
	return memoryActor{}
}

func NewMemoryActorWithStore(memoryStore *memory.Store) Actor {
	return memoryActor{memoryStore: memoryStore}
}

func NewDeliveryActor() Actor {
	return unsupportedActor{address: WildcardAddress(ActorTypeDelivery), name: ActorTypeDelivery}
}

func (a unsupportedActor) Address() string {
	return a.address
}

func (a unsupportedActor) Handle(_ context.Context, envelope any) error {
	env, err := assertEnvelope(envelope)
	if err != nil {
		return err
	}
	return PolicyError(fmt.Errorf("%s actor does not support %q/%q yet", a.name, env.Namespace, env.Kind))
}

func (memoryActor) Address() string {
	return WildcardAddress(ActorTypeMemory)
}

func (a memoryActor) Handle(ctx context.Context, envelope any) error {
	env, err := assertEnvelope(envelope)
	if err != nil {
		return err
	}
	if env.Namespace != NamespaceMemorySync {
		return PolicyError(fmt.Errorf("memory actor does not support %q/%q yet", env.Namespace, env.Kind))
	}
	if strings.TrimSpace(env.PayloadJSON) == "" {
		return nil
	}
	var payload memorySyncPayload
	if err := json.Unmarshal([]byte(env.PayloadJSON), &payload); err != nil {
		return DecodeError(fmt.Errorf("decode memory sync payload: %w", err))
	}
	switch normalizeMemoryOperation(payload.Operation, env.Kind) {
	case "", memoryOpTaskSummary, memoryOpSessionSummary, memoryOpFactExtract, memoryOpContextPack:
		return a.handleOperation(ctx, normalizeMemoryOperation(payload.Operation, env.Kind), payload)
	default:
		// Unknown operations are treated as noop to keep memory sync idempotent.
		return nil
	}
}

func (a memoryActor) handleOperation(ctx context.Context, operation string, payload memorySyncPayload) error {
	switch operation {
	case memoryOpTaskSummary, memoryOpSessionSummary, memoryOpContextPack:
		return a.handleSummaryOperation(ctx, operation, payload)
	case memoryOpFactExtract:
		return a.handleFactExtract(ctx, payload)
	default:
		return nil
	}
}

func (a memoryActor) handleSummaryOperation(ctx context.Context, operation string, payload memorySyncPayload) error {
	if a.memoryStore == nil || !a.memoryStore.MemoryEnabled() {
		return nil
	}
	content := strings.TrimSpace(payload.Content)
	if content == "" {
		return nil
	}
	entry := formatMemorySummaryEntry(operation, payload, content)
	if err := a.memoryStore.Remember(ctx, entry); err != nil {
		return TransientError(fmt.Errorf("remember %s entry: %w", operation, err))
	}
	return nil
}

func (a memoryActor) handleFactExtract(ctx context.Context, payload memorySyncPayload) error {
	if a.memoryStore == nil || !a.memoryStore.MemoryEnabled() {
		return nil
	}
	facts := extractMemoryFacts(payload.Content)
	for _, fact := range facts {
		if err := a.memoryStore.Remember(ctx, fact); err != nil {
			return TransientError(fmt.Errorf("remember extracted fact: %w", err))
		}
	}
	return nil
}

func formatMemorySummaryEntry(operation string, payload memorySyncPayload, content string) string {
	meta := make([]string, 0, 3)
	if value := strings.TrimSpace(payload.Scope); value != "" {
		meta = append(meta, "scope="+value)
	}
	if value := strings.TrimSpace(payload.TaskID); value != "" {
		meta = append(meta, "task_id="+value)
	}
	if value := strings.TrimSpace(payload.SessionID); value != "" {
		meta = append(meta, "session_id="+value)
	}
	prefix := strings.TrimSpace(operation)
	if prefix == "" {
		prefix = "memory"
	}
	if len(meta) == 0 {
		return prefix + ": " + content
	}
	return prefix + " (" + strings.Join(meta, ", ") + "): " + content
}

func extractMemoryFacts(content string) []string {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return nil
	}
	lines := strings.Split(trimmed, "\n")
	out := make([]string, 0, len(lines))
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		line = strings.TrimLeft(line, "-*• \t")
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		lower := strings.ToLower(line)
		if strings.HasPrefix(lower, "fact:") {
			line = strings.TrimSpace(line[len("fact:"):])
		}
		if line == "" {
			continue
		}
		out = append(out, line)
	}
	if len(out) == 0 {
		return []string{trimmed}
	}
	return out
}

func normalizeMemoryOperation(op string, fallback string) string {
	normalized := strings.ToLower(strings.TrimSpace(op))
	if normalized != "" {
		return normalized
	}
	return strings.ToLower(strings.TrimSpace(fallback))
}
