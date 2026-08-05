package sessionmemoryapp

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/normahq/balda/sessionmemory"
)

type scriptedStructuredInvoker struct {
	output []byte
	last   StructuredInvocation
}

func (s *scriptedStructuredInvoker) Invoke(_ context.Context, request StructuredInvocation) ([]byte, error) {
	s.last = request
	return append([]byte(nil), s.output...), nil
}

func (s *scriptedStructuredInvoker) Close(context.Context) error { return nil }

func TestDeriverExtractAtomsUsesTypedEnvelope(t *testing.T) {
	invoker := &scriptedStructuredInvoker{}
	invoker.output = []byte(`{"output":[{"category":"fact","text":"JetStream is durable","relation":"new"}]}`)
	deriver, err := NewDeriver(invoker)
	if err != nil {
		t.Fatalf("NewDeriver() error = %v", err)
	}
	scope := sessionmemory.Scope{Key: "test:personal:topic:1", Kind: sessionmemory.ScopeKindPersonal}
	turn, err := sessionmemory.NewTurn(scope, sessionmemory.SessionRef{SessionID: "session-1", AgentSessionID: "agent-1"}, "turn-1", testDeriverTime(), "remember", "JetStream is durable")
	if err != nil {
		t.Fatalf("NewTurn() error = %v", err)
	}
	atoms, err := deriver.ExtractAtoms(context.Background(), sessionmemory.AtomExtractionRequest{SchemaVersion: sessionmemory.DerivedSchemaVersionV1, Turn: turn, View: sessionmemory.ScopeView{SchemaVersion: sessionmemory.DerivedSchemaVersionV1, Scope: scope}})
	if err != nil {
		t.Fatalf("ExtractAtoms() error = %v", err)
	}
	if len(atoms) != 1 || atoms[0].Category != sessionmemory.AtomCategoryFact || atoms[0].Text != "JetStream is durable" {
		t.Fatalf("atoms = %#v", atoms)
	}
	wantOperationID, err := sessionmemory.ProcessingOperationID(sessionmemory.OperationStageAtoms, turn.ExportID)
	if err != nil {
		t.Fatalf("ProcessingOperationID() error = %v", err)
	}
	if invoker.last.Stage != string(sessionmemory.OperationStageAtoms) || invoker.last.OperationID != wantOperationID || len(invoker.last.InputJSON) == 0 {
		t.Fatalf("invocation = %#v", invoker.last)
	}
	var encoded map[string]any
	if err := json.Unmarshal(invoker.last.InputJSON, &encoded); err != nil {
		t.Fatalf("input JSON error = %v", err)
	}
	derivation, ok := encoded["derivation"].(map[string]any)
	if !ok || derivation["pipeline"] != "legacy-v1" || derivation["policy"] != "legacy-v1" ||
		derivation["prompt"] != "legacy-v1" || derivation["model"] != "legacy-v1" {
		t.Fatalf("encoded derivation = %#v", encoded["derivation"])
	}
}

func TestDeriverRejectsMalformedTypedOutput(t *testing.T) {
	invoker := &scriptedStructuredInvoker{output: []byte(`not-json`)}
	deriver, err := NewDeriver(invoker)
	if err != nil {
		t.Fatalf("NewDeriver() error = %v", err)
	}
	_, err = deriver.ExtractAtoms(context.Background(), sessionmemory.AtomExtractionRequest{SchemaVersion: sessionmemory.DerivedSchemaVersionV1})
	if err == nil {
		t.Fatal("ExtractAtoms() error = nil, want malformed request/output failure")
	}
	if code, _, ok := sessionmemory.ClassifyError(err); !ok || code != sessionmemory.CodeInvalidDerived {
		t.Fatalf("error = %v, code = %q, classified = %v", err, code, ok)
	}
}

func TestMemoryProcessorSessionIDIsStableAndStageSpecific(t *testing.T) {
	atomsA := memoryProcessorSessionID("operation-1", "atoms")
	atomsB := memoryProcessorSessionID("operation-1", "atoms")
	profile := memoryProcessorSessionID("operation-1", "profile")
	if atomsA != atomsB || atomsA == profile || atomsA == "" {
		t.Fatalf("session IDs = %q, %q, %q", atomsA, atomsB, profile)
	}
}

func testDeriverTime() time.Time {
	return time.Date(2026, 8, 3, 16, 0, 0, 0, time.UTC)
}
