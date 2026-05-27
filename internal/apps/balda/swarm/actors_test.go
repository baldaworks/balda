package swarm

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestMemoryActorRejectsUnsupportedNamespace(t *testing.T) {
	t.Parallel()

	actor := NewMemoryActor()
	err := actor.Handle(context.Background(), memoryEnvelopeForTest(NamespaceHumanInbound, KindMessage, `{"operation":"task_summary"}`))
	if err == nil {
		t.Fatal("Handle() error = nil, want policy error")
	}
	if ClassifyError(err) != ErrorKindPolicy {
		t.Fatalf("ClassifyError() = %q, want %q", ClassifyError(err), ErrorKindPolicy)
	}
}

func TestMemoryActorSupportsV1Operations(t *testing.T) {
	t.Parallel()

	actor := NewMemoryActor()
	for _, op := range []string{memoryOpTaskSummary, memoryOpSessionSummary, memoryOpFactExtract, memoryOpContextPack} {
		t.Run(op, func(t *testing.T) {
			t.Parallel()
			payload := fmt.Sprintf(`{"operation":%q,"scope":"default","task_id":"task-1","session_id":"session-1","content":"done"}`, op)
			if err := actor.Handle(context.Background(), memoryEnvelopeForTest(NamespaceMemorySync, op, payload)); err != nil {
				t.Fatalf("Handle(%s) error = %v, want nil", op, err)
			}
		})
	}
}

func TestMemoryActorUnknownOperationNoops(t *testing.T) {
	t.Parallel()

	actor := NewMemoryActor()
	err := actor.Handle(context.Background(), memoryEnvelopeForTest(NamespaceMemorySync, "memory_tick", `{"operation":"future_op"}`))
	if err != nil {
		t.Fatalf("Handle() error = %v, want nil noop", err)
	}
}

func TestMemoryActorInvalidPayloadIsPermanent(t *testing.T) {
	t.Parallel()

	actor := NewMemoryActor()
	err := actor.Handle(context.Background(), memoryEnvelopeForTest(NamespaceMemorySync, memoryOpTaskSummary, `{`))
	if err == nil {
		t.Fatal("Handle() error = nil, want permanent decode error")
	}
	if ClassifyError(err) != ErrorKindPermanent {
		t.Fatalf("ClassifyError() = %q, want %q", ClassifyError(err), ErrorKindPermanent)
	}
	var actorErr *ActorError
	if !errors.As(err, &actorErr) || actorErr == nil || actorErr.Err == nil {
		t.Fatalf("err = %v, want wrapped actor error", err)
	}
}

func memoryEnvelopeForTest(namespace, kind, payload string) Envelope {
	return Envelope{
		ID:          "mem-1",
		Namespace:   namespace,
		Kind:        kind,
		From:        ActorAddress{Target: ActorTypeTask, Key: "task-1"},
		To:          ActorAddress{Target: ActorTypeMemory, Key: "global"},
		SessionID:   "session-1",
		TaskID:      "task-1",
		PayloadJSON: payload,
	}
}
