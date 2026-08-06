package sessionmemoryapp

import (
	"context"
	"testing"
	"time"

	"github.com/normahq/balda/sessionmemory"
)

func TestRecallServiceHydratesAndFailsClosedForStaleAndSensitiveCandidates(t *testing.T) {
	t.Parallel()
	scope := sessionmemory.Scope{Key: "telegram:1:0", Kind: sessionmemory.ScopeKindPersonal}
	now := time.Date(2026, time.August, 6, 10, 0, 0, 0, time.UTC)
	reader := &fakeRecallReader{current: 7, records: []sessionmemory.RecallRecord{
		validRecallRecord(scope, "item-1", "revision-1", "deploy safely", now, 7),
		validRecallRecord(scope, "item-2", "revision-2", "sensitive deploy", now, 7),
	}}
	reader.records[1].Sensitivity = sessionmemory.SensitivitySensitive
	projection := fakeRecallProjection{hits: []sessionmemory.RecallProjectionHit{
		{Scope: scope, RevisionID: "revision-1", Score: 2, ScopeChangeSeq: 7},
		{Scope: scope, RevisionID: "stale-revision", Score: 4, ScopeChangeSeq: 6},
		{Scope: scope, RevisionID: "revision-2", Score: 3, ScopeChangeSeq: 7},
	}}
	service, err := NewRecallService(reader, projection)
	if err != nil {
		t.Fatalf("NewRecallService() error = %v", err)
	}
	service.now = func() time.Time { return now }
	response, err := service.Search(context.Background(), sessionmemory.RecallRequest{Scope: scope, Query: "deploy", Limit: 10, MinScopeChangeSeq: 7})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(response.Results) != 1 || response.Results[0].RevisionID != "revision-1" {
		t.Fatalf("Search() results = %#v, want only active standard canonical record", response.Results)
	}
	if response.Results[0].Explain.Total <= response.Results[0].Explain.Lexical || len(response.Results[0].Evidence) == 0 {
		t.Fatalf("Search() result explain/evidence = %#v", response.Results[0])
	}
}

func TestRecallServiceUsesBoundedCanonicalTailFallbackAndAsOf(t *testing.T) {
	t.Parallel()
	scope := sessionmemory.Scope{Key: "telegram:2:0", Kind: sessionmemory.ScopeKindPersonal}
	now := time.Date(2026, time.August, 6, 10, 0, 0, 0, time.UTC)
	expired := validRecallRecord(scope, "item-old", "revision-old", "old deploy", now.Add(-48*time.Hour), 3)
	expires := now.Add(-time.Hour)
	expired.Temporal.ExpiresAt = &expires
	fresh := validRecallRecord(scope, "item-new", "revision-new", "new deploy decision", now.Add(-time.Minute), 4)
	reader := &fakeRecallReader{current: 4, tail: []sessionmemory.RecallRecord{expired, fresh}}
	service, err := NewRecallService(reader, nil)
	if err != nil {
		t.Fatalf("NewRecallService() error = %v", err)
	}
	service.now = func() time.Time { return now }
	response, err := service.Search(context.Background(), sessionmemory.RecallRequest{Scope: scope, Query: "deploy", Limit: 2, MinScopeChangeSeq: 3})
	if err != nil {
		t.Fatalf("Search(fallback) error = %v", err)
	}
	if len(response.Results) != 1 || response.Results[0].RevisionID != "revision-new" {
		t.Fatalf("Search(fallback) results = %#v", response.Results)
	}
	if reader.tailLimit != 8 {
		t.Fatalf("fallback tail limit = %d, want bounded limit 8", reader.tailLimit)
	}
}

func TestRecallServiceRejectsForeignProjectionScopeAndConsistencyLag(t *testing.T) {
	t.Parallel()
	scope := sessionmemory.Scope{Key: "telegram:3:0", Kind: sessionmemory.ScopeKindPersonal}
	foreign := sessionmemory.Scope{Key: "telegram:4:0", Kind: sessionmemory.ScopeKindPersonal}
	reader := &fakeRecallReader{current: 2, records: []sessionmemory.RecallRecord{validRecallRecord(scope, "item-1", "revision-1", "safe", time.Now().UTC(), 2)}}
	service, err := NewRecallService(reader, fakeRecallProjection{hits: []sessionmemory.RecallProjectionHit{{Scope: foreign, RevisionID: "revision-1", Score: 1}}})
	if err != nil {
		t.Fatalf("NewRecallService() error = %v", err)
	}
	if _, err := service.Search(context.Background(), sessionmemory.RecallRequest{Scope: scope, Query: "safe", Limit: 1}); !hasRecallCode(err, sessionmemory.CodeScopeViolation) {
		t.Fatalf("foreign scope error = %v, want scope violation", err)
	}
	if _, err := service.Search(context.Background(), sessionmemory.RecallRequest{Scope: scope, Query: "safe", Limit: 1, MinScopeChangeSeq: 3}); !hasRecallCode(err, sessionmemory.CodeConflict) {
		t.Fatalf("consistency lag error = %v, want conflict", err)
	}
}

type fakeRecallProjection struct {
	hits []sessionmemory.RecallProjectionHit
}

func (f fakeRecallProjection) SearchRecall(context.Context, sessionmemory.RecallRequest) ([]sessionmemory.RecallProjectionHit, error) {
	return append([]sessionmemory.RecallProjectionHit(nil), f.hits...), nil
}

type fakeRecallReader struct {
	current   uint64
	records   []sessionmemory.RecallRecord
	tail      []sessionmemory.RecallRecord
	tailLimit uint32
}

func (f *fakeRecallReader) LoadRecallRecords(_ context.Context, scope sessionmemory.Scope, revisionIDs []string) ([]sessionmemory.RecallRecord, error) {
	result := make([]sessionmemory.RecallRecord, 0, len(revisionIDs))
	for _, revisionID := range revisionIDs {
		for _, record := range f.records {
			if record.Scope == scope && record.RevisionID == revisionID {
				result = append(result, record)
			}
		}
	}
	return result, nil
}

func (f *fakeRecallReader) SearchRecallTail(_ context.Context, request sessionmemory.RecallRequest, limit uint32) ([]sessionmemory.RecallRecord, error) {
	f.tailLimit = limit
	return append([]sessionmemory.RecallRecord(nil), f.tail...), nil
}

func (f *fakeRecallReader) CurrentScopeChangeSeq(context.Context, sessionmemory.Scope) (uint64, error) {
	return f.current, nil
}

func validRecallRecord(scope sessionmemory.Scope, itemID, revisionID, text string, createdAt time.Time, changeSeq uint64) sessionmemory.RecallRecord {
	evidence := sessionmemory.EvidenceRef{SourceID: "source-1", MessageID: "message-1", Role: sessionmemory.MessageRoleUser, StartByte: 0, EndByte: 4, AssertionMode: sessionmemory.AssertionModeUser}
	return sessionmemory.RecallRecord{
		Scope: scope, ItemID: itemID, RevisionID: revisionID, Revision: 1, Kind: sessionmemory.MemoryKindState,
		Text: text, State: sessionmemory.RevisionStateActive, CreatedAt: createdAt,
		Temporal: sessionmemory.Temporal{ObservedAt: createdAt}, Sensitivity: sessionmemory.SensitivityStandard,
		Retention: sessionmemory.RetentionClassStandard, Evidence: []sessionmemory.EvidenceRef{evidence},
		SourceIDs: []string{"source-1"}, SessionIDs: []string{"session-1"}, ScopeChangeSeq: changeSeq,
	}
}

func hasRecallCode(err error, want sessionmemory.ErrorCode) bool {
	code, _, ok := sessionmemory.ClassifyError(err)
	return ok && code == want
}
