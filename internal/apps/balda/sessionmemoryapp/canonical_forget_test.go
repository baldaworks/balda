package sessionmemoryapp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/baldaworks/balda/sessionmemory"
	blevestore "github.com/baldaworks/balda/sessionmemory/index/bleve"
	badgerstore "github.com/baldaworks/balda/sessionmemory/store/badger"
)

func TestCanonicalForgetScrubsPayloadMessagesAndBleveAndReplays(t *testing.T) {
	store, err := badgerstore.OpenBadgerSessionMemoryStore(filepath.Join(t.TempDir(), "canonical.badger"))
	if err != nil {
		t.Fatalf("OpenBadgerSessionMemoryStore() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	scope := sessionmemory.Scope{Key: "canonical:forget", Kind: sessionmemory.ScopeKindPersonal}
	completedAt := time.Date(2026, time.August, 6, 14, 0, 0, 0, time.UTC)
	session := sessionmemory.SessionRef{SessionID: "forget-session", AgentSessionID: "forget-agent"}
	turn, err := sessionmemory.NewTurn(scope, session, "forget-turn", completedAt, "forget this source", "forget assistant text")
	if err != nil {
		t.Fatalf("NewTurn() error = %v", err)
	}
	sourceRef := sessionmemory.SourceRef{Scope: scope, ExportID: turn.ExportID, SessionID: session.SessionID, SourceTurnID: turn.SourceTurnID}
	sourceRecord := sessionmemory.SourceRecord{SchemaVersion: sessionmemory.DerivedSchemaVersionV1, Ref: sourceRef, State: sessionmemory.SourceStateActive, Turn: &turn}
	sourcePayload, err := json.Marshal(sourceRecord)
	if err != nil {
		t.Fatalf("marshal source payload: %v", err)
	}
	evidence, err := sessionmemory.NewEvidenceRef(turn.SourceID, turn.Messages[0].MessageID, sessionmemory.MessageRoleUser, turn.Messages[0].Text, 0, uint32(len(turn.Messages[0].Text)), sessionmemory.AssertionModeUser)
	if err != nil {
		t.Fatalf("NewEvidenceRef() error = %v", err)
	}
	item := sessionmemory.MemoryItem{ItemID: "forget-item", Scope: scope, Kind: sessionmemory.MemoryKindState, MemoryKey: "forget-key"}
	revision := sessionmemory.MemoryRevision{
		SchemaVersion: sessionmemory.MemorySchemaVersionV2, RevisionID: "forget-revision", ItemID: item.ItemID, Revision: 1,
		Temporal: sessionmemory.Temporal{ObservedAt: completedAt}, Evidence: []sessionmemory.EvidenceRef{evidence},
		Sensitivity: sessionmemory.SensitivityStandard, Retention: sessionmemory.RetentionClassStandard,
	}
	sourcePayloadRef := forgetPayloadRef("forget-source", sourcePayload)
	userPayloadRef := forgetPayloadRef("forget-user", []byte(turn.Messages[0].Text))
	assistantPayloadRef := forgetPayloadRef("forget-assistant", []byte(turn.Messages[1].Text))
	revisionPayloadRef := forgetPayloadRef("forget-revision-payload", []byte("forgotten canonical text"))
	revision.Payload = revisionPayloadRef
	mutation := sessionmemory.CanonicalMutation{
		SchemaVersion: sessionmemory.CanonicalSchemaVersionV1, Scope: scope,
		Operation: sessionmemory.OperationRecord{OperationID: "forget-seed", Fingerprint: "forget-seed-fingerprint", CommittedAt: completedAt},
		Sources:   []sessionmemory.SourceRecordV2{{SourceID: turn.SourceID, Scope: scope, Sensitivity: sessionmemory.SensitivityStandard, Retention: sessionmemory.RetentionClassStandard, Payload: sourcePayloadRef}},
		Messages: []sessionmemory.MessageRecord{
			{MessageID: turn.Messages[0].MessageID, SourceID: turn.SourceID, Role: turn.Messages[0].Role, Payload: userPayloadRef},
			{MessageID: turn.Messages[1].MessageID, SourceID: turn.SourceID, Role: turn.Messages[1].Role, Payload: assistantPayloadRef},
		},
		Items: []sessionmemory.MemoryItem{item}, Revisions: []sessionmemory.MemoryRevision{revision},
		Heads: []sessionmemory.ItemHead{{ItemID: item.ItemID, RevisionID: revision.RevisionID}},
		Payloads: []sessionmemory.CanonicalPayload{
			{Ref: sourcePayloadRef, Data: sourcePayload}, {Ref: userPayloadRef, Data: []byte(turn.Messages[0].Text)},
			{Ref: assistantPayloadRef, Data: []byte(turn.Messages[1].Text)}, {Ref: revisionPayloadRef, Data: []byte("forgotten canonical text")},
		},
	}
	if _, err := store.ApplyCanonicalMutation(context.Background(), mutation); err != nil {
		t.Fatalf("ApplyCanonicalMutation() error = %v", err)
	}

	reader, err := badgerstore.NewCanonicalReader(store)
	if err != nil {
		t.Fatalf("NewCanonicalReader() error = %v", err)
	}
	projection, err := blevestore.Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatalf("NewBleveRecallProjection() error = %v", err)
	}
	t.Cleanup(func() { _ = projection.Close() })
	generation, err := projection.NewGeneration("forget-seed-generation")
	if err != nil {
		t.Fatalf("NewGeneration() error = %v", err)
	}
	category := sessionmemory.AtomCategoryFact
	if err := generation.Index(context.Background(), sessionmemory.RecallProjectionDocument{
		Scope: scope, ItemID: item.ItemID, RevisionID: revision.RevisionID, Revision: revision.Revision,
		Kind: item.Kind, Category: &category, MemoryKey: item.MemoryKey, Text: "forgotten canonical text", CreatedAt: completedAt,
		Temporal: revision.Temporal, Sensitivity: revision.Sensitivity, Retention: revision.Retention,
		SourceIDs: []string{turn.SourceID}, SessionIDs: []string{session.SessionID}, ScopeChangeSeq: 1,
	}); err != nil {
		t.Fatalf("Index() error = %v", err)
	}
	if err := generation.Commit(context.Background()); err != nil {
		t.Fatalf("generation Commit() error = %v", err)
	}
	if err := generation.Close(); err != nil {
		t.Fatalf("generation Close() error = %v", err)
	}
	if err := projection.ActivateGeneration(context.Background(), "forget-seed-generation"); err != nil {
		t.Fatalf("ActivateGeneration() error = %v", err)
	}
	applier, err := blevestore.NewBleveCanonicalApplier(projection, reader)
	if err != nil {
		t.Fatalf("NewBleveCanonicalApplier() error = %v", err)
	}
	var scrubErr error
	service, err := NewCanonicalForgetService(store, store, store, store, forgetScrubber{canonical: store, projection: applier, err: &scrubErr})
	if err != nil {
		t.Fatalf("NewCanonicalForgetService() error = %v", err)
	}
	command := sessionmemory.ForgetSourceCommand{SchemaVersion: sessionmemory.DerivedSchemaVersionV1, Source: sourceRef, ForgottenAt: completedAt.Add(time.Minute)}
	outcome, err := service.ForgetSource(context.Background(), command)
	if err != nil {
		t.Fatalf("ForgetSource() error = %v", err)
	}
	if err := outcome.Validate(); err != nil {
		t.Fatalf("ForgetSource() outcome invalid: %v", err)
	}
	if records, err := reader.LoadRecallRecords(context.Background(), scope, []string{revision.RevisionID}); err != nil || len(records) != 0 {
		t.Fatalf("LoadRecallRecords(after forget) = %#v, error = %v", records, err)
	}
	for _, ref := range []sessionmemory.PayloadRef{sourcePayloadRef, userPayloadRef, assistantPayloadRef, revisionPayloadRef} {
		if _, err := store.LoadPayload(context.Background(), ref); err == nil {
			t.Fatalf("LoadPayload(%s) succeeded after forget", ref.ID)
		}
	}
	hits, err := projection.SearchRecall(context.Background(), sessionmemory.RecallRequest{Scope: scope, Query: "forgotten", Limit: 10})
	if err != nil || len(hits) != 0 {
		t.Fatalf("SearchRecall(after forget) = %#v, error = %v, scrubErr=%v (source=%s revision=%s)", hits, err, scrubErr, turn.SourceID, revision.RevisionID)
	}
	replayed, err := service.ForgetSource(context.Background(), command)
	if err != nil || !reflect.DeepEqual(outcome, replayed) {
		t.Fatalf("ForgetSource(replay) = %#v, error = %v; want %#v", replayed, err, outcome)
	}
}

type forgetScrubber struct {
	canonical interface {
		ScrubCanonicalForget(ctx context.Context, scope sessionmemory.Scope, sourceIDs, revisionIDs []string) error
	}
	projection interface {
		ScrubCanonicalForget(ctx context.Context, scope sessionmemory.Scope, sourceIDs, revisionIDs []string) error
	}
	err *error
}

func (s forgetScrubber) ScrubCanonicalForget(ctx context.Context, scope sessionmemory.Scope, sourceIDs, revisionIDs []string) error {
	err := errors.Join(s.canonical.ScrubCanonicalForget(ctx, scope, sourceIDs, revisionIDs), s.projection.ScrubCanonicalForget(ctx, scope, sourceIDs, revisionIDs))
	if s.err != nil {
		*s.err = err
	}
	return err
}

func forgetPayloadRef(id string, data []byte) sessionmemory.PayloadRef {
	digest := sha256.Sum256(data)
	return sessionmemory.PayloadRef{ID: id, Digest: hex.EncodeToString(digest[:]), ByteSize: uint32(len(data))}
}
