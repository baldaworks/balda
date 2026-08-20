package sessionmemoryapp

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/baldaworks/balda/sessionmemory"
	portableapp "github.com/baldaworks/balda/sessionmemory/app"
)

type scriptedStructuredInvoker struct {
	output []byte
	last   portableapp.StructuredInvocation
}

const legacyDerivationVersion = "legacy-v1"

func (s *scriptedStructuredInvoker) Invoke(_ context.Context, request portableapp.StructuredInvocation) ([]byte, error) {
	s.last = request
	return append([]byte(nil), s.output...), nil
}

func (s *scriptedStructuredInvoker) Close(context.Context) error { return nil }

func TestDeriverExtractAtomsUsesTypedEnvelope(t *testing.T) {
	invoker := &scriptedStructuredInvoker{}
	invoker.output = []byte(`{"output":[{"category":"fact","text":"JetStream is durable","relation":"new"}]}`)
	deriver, err := portableapp.NewDeriver(invoker)
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
	if !ok || derivation["pipeline"] != legacyDerivationVersion || derivation["policy"] != legacyDerivationVersion ||
		derivation["prompt"] != legacyDerivationVersion || derivation["model"] != legacyDerivationVersion {
		t.Fatalf("encoded derivation = %#v", encoded["derivation"])
	}
}

func TestDeriverExtractCanonicalSemanticsUsesDistinctOperation(t *testing.T) {
	invoker := &scriptedStructuredInvoker{output: []byte(`{"output":[{"Kind":"state","Subject":"Ada","Predicate":"Lives In","Qualifiers":["Bishkek"],"Memory":{"Kind":"state","Statement":"Ada lives in Bishkek","Temporal":{"ObservedAt":"2026-08-03T16:00:00Z"},"Evidence":[{"SourceID":"source-1","MessageID":"message-1","Role":"user","StartByte":0,"EndByte":3,"AssertionMode":"user"}],"Sensitivity":"standard","Retention":"standard"}}]}`)}
	deriver, err := portableapp.NewDeriver(invoker)
	if err != nil {
		t.Fatalf("NewDeriver() error = %v", err)
	}
	scope := sessionmemory.Scope{Key: "test:personal:topic:1", Kind: sessionmemory.ScopeKindPersonal}
	turn, err := sessionmemory.NewTurn(scope, sessionmemory.SessionRef{SessionID: "session-1", AgentSessionID: "agent-1"}, "turn-1", testDeriverTime(), "remember", "Ada lives in Bishkek")
	if err != nil {
		t.Fatalf("NewTurn() error = %v", err)
	}
	candidates, err := deriver.ExtractCanonicalSemantics(context.Background(), sessionmemory.CanonicalExtractionRequest{SchemaVersion: sessionmemory.CanonicalSchemaVersionV1, Turn: turn})
	if err != nil {
		t.Fatalf("ExtractCanonicalSemantics() error = %v", err)
	}
	if len(candidates) != 1 || candidates[0].Kind != sessionmemory.MemoryKindState {
		t.Fatalf("candidates = %#v", candidates)
	}
	wantOperationID, err := sessionmemory.CanonicalSemanticOperationID(turn.ExportID, sessionmemory.LegacyDerivationRef())
	if err != nil {
		t.Fatalf("CanonicalSemanticOperationID() error = %v", err)
	}
	if invoker.last.Stage != "canonical_semantics" || invoker.last.OperationID != wantOperationID {
		t.Fatalf("invocation = %#v", invoker.last)
	}
}

func TestDeriverRejectsMalformedTypedOutput(t *testing.T) {
	invoker := &scriptedStructuredInvoker{output: []byte(`not-json`)}
	deriver, err := portableapp.NewDeriver(invoker)
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
