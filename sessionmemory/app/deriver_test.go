package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/normahq/balda/sessionmemory"
)

type scriptedInvoker struct {
	output []byte
	last   StructuredInvocation
}

func (s *scriptedInvoker) Invoke(_ context.Context, request StructuredInvocation) ([]byte, error) {
	s.last = request
	return append([]byte(nil), s.output...), nil
}

func (*scriptedInvoker) Close(context.Context) error { return nil }

func TestDeriverExtractCanonicalSemanticsUsesNeutralStructuredBoundary(t *testing.T) {
	t.Parallel()

	invoker := &scriptedInvoker{output: []byte(`{"output":[{"Kind":"state","Subject":"Ada","Predicate":"Lives In","Memory":{"Kind":"state","Statement":"Ada lives in Bishkek","Temporal":{"ObservedAt":"2026-08-03T16:00:00Z"},"Evidence":[{"SourceID":"source-1","MessageID":"message-1","Role":"user","StartByte":0,"EndByte":3,"AssertionMode":"user"}],"Sensitivity":"standard","Retention":"standard"}}]}`)}
	deriver, err := NewDeriver(invoker)
	if err != nil {
		t.Fatalf("NewDeriver() error = %v", err)
	}
	turn, err := sessionmemory.NewTurn(
		sessionmemory.Scope{Key: "telegram:1", Kind: sessionmemory.ScopeKindPersonal},
		sessionmemory.SessionRef{SessionID: "session-1", AgentSessionID: "agent-1"},
		"turn-1", time.Date(2026, time.August, 3, 16, 0, 0, 0, time.UTC), "Ada", "Ada lives in Bishkek",
	)
	if err != nil {
		t.Fatalf("NewTurn() error = %v", err)
	}
	items, err := deriver.ExtractCanonicalSemantics(context.Background(), sessionmemory.CanonicalExtractionRequest{
		SchemaVersion: sessionmemory.CanonicalSchemaVersionV1,
		Derivation:    sessionmemory.LegacyDerivationRef(),
		Turn:          turn,
	})
	if err != nil {
		t.Fatalf("ExtractCanonicalSemantics() error = %v", err)
	}
	if len(items) != 1 || items[0].Memory.Statement != "Ada lives in Bishkek" {
		t.Fatalf("canonical semantics = %#v", items)
	}
	if invoker.last.Stage != "canonical_semantics" || !strings.Contains(invoker.last.Instruction, "session-memory") {
		t.Fatalf("structured invocation = %#v", invoker.last)
	}
}

func TestDeriverRejectsMalformedStructuredOutput(t *testing.T) {
	t.Parallel()

	deriver, err := NewDeriver(&scriptedInvoker{output: []byte("not-json")})
	if err != nil {
		t.Fatalf("NewDeriver() error = %v", err)
	}
	turn, err := sessionmemory.NewTurn(
		sessionmemory.Scope{Key: "telegram:2", Kind: sessionmemory.ScopeKindPersonal},
		sessionmemory.SessionRef{SessionID: "session-2", AgentSessionID: "agent-2"},
		"turn-2", time.Date(2026, time.August, 3, 16, 0, 0, 0, time.UTC), "remember", "stored",
	)
	if err != nil {
		t.Fatalf("NewTurn() error = %v", err)
	}
	_, err = deriver.ExtractCanonicalSemantics(context.Background(), sessionmemory.CanonicalExtractionRequest{
		SchemaVersion: sessionmemory.CanonicalSchemaVersionV1,
		Turn:          turn,
		Derivation:    sessionmemory.LegacyDerivationRef(),
	})
	if code, _, ok := sessionmemory.ClassifyError(err); !ok || code != sessionmemory.CodeInvalidDerived {
		t.Fatalf("error = %v, code = %q, classified = %v", err, code, ok)
	}
}
