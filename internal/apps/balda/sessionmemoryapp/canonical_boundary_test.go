package sessionmemoryapp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/normahq/balda/sessionmemory"
	badgerstore "github.com/normahq/balda/sessionmemory/store/badger"
)

func TestCanonicalBoundaryProcessorPersistsBothStagesAndReplays(t *testing.T) {
	store, err := badgerstore.OpenBadgerSessionMemoryStore(filepath.Join(t.TempDir(), "canonical.badger"))
	if err != nil {
		t.Fatalf("OpenBadgerSessionMemoryStore() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	scope := sessionmemory.Scope{Key: "canonical:boundary", Kind: sessionmemory.ScopeKindPersonal}
	session := sessionmemory.SessionRef{SessionID: "session-boundary", AgentSessionID: "agent-boundary"}
	committedAt := time.Date(2026, time.August, 6, 13, 0, 0, 0, time.UTC)
	turn, err := sessionmemory.NewTurn(scope, session, "source-turn", committedAt, "source statement", "assistant statement")
	if err != nil {
		t.Fatalf("NewTurn() error = %v", err)
	}
	sourceRef := sessionmemory.SourceRef{Scope: scope, ExportID: turn.ExportID, SessionID: session.SessionID, SourceTurnID: turn.SourceTurnID}
	evidence, err := sessionmemory.NewEvidenceRef(turn.SourceID, turn.Messages[0].MessageID, turn.Messages[0].Role, turn.Messages[0].Text, 0, uint32(len(turn.Messages[0].Text)), sessionmemory.AssertionModeUser)
	if err != nil {
		t.Fatalf("NewEvidenceRef() error = %v", err)
	}
	atomOperation, err := sessionmemory.ProcessingOperationID(sessionmemory.OperationStageAtoms, turn.ExportID, sessionmemory.LegacyDerivationRef())
	if err != nil {
		t.Fatalf("ProcessingOperationID() error = %v", err)
	}
	atomItem, err := sessionmemory.AtomItemID(scope, sessionmemory.AtomCategoryFact, "existing fact")
	if err != nil {
		t.Fatalf("AtomItemID() error = %v", err)
	}
	atomProvenance := sessionmemory.Provenance{RawSources: []sessionmemory.SourceRef{sourceRef}}
	atomRevision, err := sessionmemory.DerivedRevisionID(scope, atomItem, atomOperation, []string{string(sessionmemory.AtomCategoryFact), "existing fact", string(sessionmemory.CandidateRelationNew)}, atomProvenance, nil)
	if err != nil {
		t.Fatalf("DerivedRevisionID() error = %v", err)
	}
	seedCompatibility := sessionmemory.CanonicalCompatibilityPayload{
		SchemaVersion: sessionmemory.CanonicalCompatibilitySchemaVersion, Kind: sessionmemory.DerivedKindAtom,
		Category: ptrAtomCategory(sessionmemory.AtomCategoryFact), Text: "existing fact",
		LegacyItemID: atomItem, LegacyRevisionID: atomRevision, LegacyOperationID: atomOperation,
	}
	seedPayload, err := json.Marshal(seedCompatibility)
	if err != nil {
		t.Fatalf("Marshal compatibility payload: %v", err)
	}
	sourceRecord := sessionmemory.SourceRecord{SchemaVersion: sessionmemory.DerivedSchemaVersionV1, Ref: sourceRef, State: sessionmemory.SourceStateActive, Turn: &turn}
	sourcePayload, err := json.Marshal(sourceRecord)
	if err != nil {
		t.Fatalf("Marshal source payload: %v", err)
	}
	sourcePayloadRef := canonicalTestPayloadRef("source", sourcePayload)
	messagePayloadUser := canonicalTestPayloadRef("message-user", []byte(turn.Messages[0].Text))
	messagePayloadAssistant := canonicalTestPayloadRef("message-assistant", []byte(turn.Messages[1].Text))
	revisionPayloadRef := canonicalTestPayloadRef("revision", seedPayload)
	mutation := sessionmemory.CanonicalMutation{
		SchemaVersion: sessionmemory.CanonicalSchemaVersionV1,
		Scope:         scope,
		Operation:     sessionmemory.OperationRecord{OperationID: "seed-boundary", Fingerprint: "seed-boundary-fingerprint", CommittedAt: committedAt},
		Sources:       []sessionmemory.SourceRecordV2{{SourceID: turn.SourceID, Scope: scope, Sensitivity: sessionmemory.SensitivityStandard, Retention: sessionmemory.RetentionClassStandard, Payload: sourcePayloadRef}},
		Messages: []sessionmemory.MessageRecord{
			{MessageID: turn.Messages[0].MessageID, SourceID: turn.SourceID, Role: turn.Messages[0].Role, Payload: messagePayloadUser},
			{MessageID: turn.Messages[1].MessageID, SourceID: turn.SourceID, Role: turn.Messages[1].Role, Payload: messagePayloadAssistant},
		},
		Items:     []sessionmemory.MemoryItem{{ItemID: atomItem, Scope: scope, Kind: sessionmemory.MemoryKindState, MemoryKey: "existing-key"}},
		Revisions: []sessionmemory.MemoryRevision{{SchemaVersion: sessionmemory.MemorySchemaVersionV2, RevisionID: atomRevision, ItemID: atomItem, Revision: 1, Temporal: sessionmemory.Temporal{ObservedAt: committedAt}, Evidence: []sessionmemory.EvidenceRef{evidence}, Sensitivity: sessionmemory.SensitivityStandard, Retention: sessionmemory.RetentionClassStandard, Payload: revisionPayloadRef}},
		Heads:     []sessionmemory.ItemHead{{ItemID: atomItem, RevisionID: atomRevision}},
		Payloads:  []sessionmemory.CanonicalPayload{{Ref: sourcePayloadRef, Data: sourcePayload}, {Ref: messagePayloadUser, Data: []byte(turn.Messages[0].Text)}, {Ref: messagePayloadAssistant, Data: []byte(turn.Messages[1].Text)}, {Ref: revisionPayloadRef, Data: seedPayload}},
	}
	if _, err := store.ApplyCanonicalMutation(context.Background(), mutation); err != nil {
		t.Fatalf("seed ApplyCanonicalMutation() error = %v", err)
	}
	reader, err := badgerstore.NewCanonicalReader(store)
	if err != nil {
		t.Fatalf("NewCanonicalReader() error = %v", err)
	}
	if _, initialErr := reader.LoadCanonicalBoundaryView(context.Background(), scope); initialErr != nil {
		t.Fatalf("initial LoadCanonicalBoundaryView() error = %v (atom operation=%s revision=%s source=%#v)", initialErr, atomOperation, atomRevision, sourceRef)
	}
	scenarios := &boundaryScenarioSynthesizer{}
	profiles := &boundaryProfileSynthesizer{}
	derivation := sessionmemory.DerivationRef{Pipeline: "boundary", Policy: "policy-v1", Prompt: "prompt-v1", Model: "model-v1"}
	processor, err := NewCanonicalBoundaryProcessor(store, reader, scenarios, profiles, derivation)
	if err != nil {
		t.Fatalf("NewCanonicalBoundaryProcessor() error = %v", err)
	}
	exportID, err := sessionmemory.BoundaryExportID(scope, session, "transition-1")
	if err != nil {
		t.Fatalf("BoundaryExportID() error = %v", err)
	}
	boundary := sessionmemory.Boundary{SchemaVersion: sessionmemory.SchemaVersionV1, ExportID: exportID, Scope: scope, Session: session, TransitionID: "transition-1", Reason: sessionmemory.BoundaryReasonReset, OccurredAt: committedAt.Add(time.Minute)}
	first, err := processor.ProcessBoundary(context.Background(), boundary)
	if err != nil {
		t.Fatalf("ProcessBoundary() error = %v (%#v)", err, first)
	}
	if err := first.Validate(); err != nil {
		t.Fatalf("first boundary outcome invalid: %v", err)
	}
	if scenarios.calls != 1 || profiles.calls != 1 {
		t.Fatalf("synthesis calls = scenarios:%d profiles:%d, want one each", scenarios.calls, profiles.calls)
	}
	second, err := processor.ProcessBoundary(context.Background(), boundary)
	if err != nil {
		t.Fatalf("ProcessBoundary(replay) error = %v", err)
	}
	if !reflect.DeepEqual(first, second) || scenarios.calls != 1 || profiles.calls != 1 {
		t.Fatalf("replay outcome/calls = %#v / scenarios:%d profiles:%d, want same durable outcome and no re-synthesis", second, scenarios.calls, profiles.calls)
	}
}

type boundaryScenarioSynthesizer struct{ calls int }

func (s *boundaryScenarioSynthesizer) SynthesizeScenarios(_ context.Context, request sessionmemory.ScenarioSynthesisRequest) ([]sessionmemory.ScenarioCandidate, error) {
	s.calls++
	if len(request.View.Atoms) != 1 {
		return nil, sessionmemory.PermanentError(sessionmemory.CodeInvalidDerived, "boundary fixture atom view is incomplete", nil)
	}
	return []sessionmemory.ScenarioCandidate{{TopicKey: "topic", Title: "Topic", Summary: "Topic summary", Atoms: []sessionmemory.RevisionRef{{ItemID: request.View.Atoms[0].Meta.ItemID, RevisionID: request.View.Atoms[0].Meta.RevisionID}}}}, nil
}

type boundaryProfileSynthesizer struct{ calls int }

func (s *boundaryProfileSynthesizer) SynthesizeProfile(_ context.Context, request sessionmemory.ProfileSynthesisRequest) (*sessionmemory.ProfileCandidate, error) {
	s.calls++
	if len(request.View.Scenarios) != 1 {
		return nil, sessionmemory.PermanentError(sessionmemory.CodeInvalidDerived, "boundary fixture scenario view is incomplete", nil)
	}
	return &sessionmemory.ProfileCandidate{Disposition: sessionmemory.ProfileDispositionUpsert, Summary: "Profile summary", Scenarios: []sessionmemory.RevisionRef{{ItemID: request.View.Scenarios[0].Meta.ItemID, RevisionID: request.View.Scenarios[0].Meta.RevisionID}}}, nil
}

func canonicalTestPayloadRef(id string, data []byte) sessionmemory.PayloadRef {
	digest := sha256.Sum256(data)
	return sessionmemory.PayloadRef{ID: id, Digest: hex.EncodeToString(digest[:]), ByteSize: uint32(len(data))}
}

func ptrAtomCategory(value sessionmemory.AtomCategory) *sessionmemory.AtomCategory { return &value }
