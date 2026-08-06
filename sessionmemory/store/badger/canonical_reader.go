package badger

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/dgraph-io/badger/v4"
	"github.com/normahq/balda/sessionmemory"
)

// CanonicalReader is the bounded application adapter over the canonical
// Badger records. It deliberately implements the storage-neutral recall port
// and keeps Badger keys, envelopes, and payload layout inside state.
type CanonicalReader struct {
	store *BadgerSessionMemoryStore
}

// NewCanonicalReader constructs the canonical read adapter for one Badger
// owner. The reader does not open a second database handle.
func NewCanonicalReader(store *BadgerSessionMemoryStore) (*CanonicalReader, error) {
	if store == nil {
		return nil, sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "canonical reader store is required", nil)
	}
	return &CanonicalReader{store: store}, nil
}

var _ sessionmemory.RecallCanonicalReader = (*CanonicalReader)(nil)

// CurrentScopeChangeSeq returns the canonical watermark for one exact scope.
func (r *CanonicalReader) CurrentScopeChangeSeq(ctx context.Context, scope sessionmemory.Scope) (uint64, error) {
	if err := canonicalReaderContext(ctx); err != nil {
		return 0, err
	}
	if r == nil || r.store == nil {
		return 0, sessionmemory.PermanentError(sessionmemory.CodeDisabled, "canonical reader is unavailable", nil)
	}
	state, err := r.store.LoadScopeState(ctx, scope)
	if err != nil {
		return 0, err
	}
	return state.ChangeSeq, nil
}

// LoadRecallRecords hydrates only the requested revision IDs. Missing,
// inactive, denied, or foreign candidates are skipped so a stale disposable
// projection cannot make an otherwise valid recall request fail.
func (r *CanonicalReader) LoadRecallRecords(ctx context.Context, scope sessionmemory.Scope, revisionIDs []string) ([]sessionmemory.RecallRecord, error) {
	if err := canonicalReaderContext(ctx); err != nil {
		return nil, err
	}
	if r == nil || r.store == nil {
		return nil, sessionmemory.PermanentError(sessionmemory.CodeDisabled, "canonical reader is unavailable", nil)
	}
	request := sessionmemory.CanonicalRevisionReadRequest{Scope: scope, RevisionIDs: revisionIDs}
	if err := request.Validate(); err != nil {
		return nil, err
	}
	result := make([]sessionmemory.RecallRecord, 0, len(revisionIDs))
	for _, revisionID := range revisionIDs {
		record, found, err := r.loadRecallRecord(ctx, scope, revisionID, true)
		if err != nil {
			return nil, err
		}
		if found {
			result = append(result, record)
		}
	}
	return result, nil
}

// SearchRecallTail returns a bounded active-head page. Text matching and
// ranking remain in RecallService; this adapter never scans complete history.
func (r *CanonicalReader) SearchRecallTail(ctx context.Context, request sessionmemory.RecallRequest, limit uint32) ([]sessionmemory.RecallRecord, error) {
	if err := canonicalReaderContext(ctx); err != nil {
		return nil, err
	}
	if r == nil || r.store == nil {
		return nil, sessionmemory.PermanentError(sessionmemory.CodeDisabled, "canonical reader is unavailable", nil)
	}
	normalized, err := sessionmemory.NormalizeRecallRequest(request)
	if err != nil {
		return nil, err
	}
	if limit == 0 || limit > sessionmemory.MaxRecallCandidates {
		return nil, sessionmemory.PermanentError(sessionmemory.CodeLimitExceeded, "canonical recall tail limit is invalid", nil)
	}
	active, err := r.store.ScanActiveMemory(ctx, sessionmemory.ActiveMemoryScanRequest{Scope: normalized.Scope, Limit: limit})
	if err != nil {
		return nil, err
	}
	result := make([]sessionmemory.RecallRecord, 0, len(active))
	for _, memory := range active {
		record, found, loadErr := r.loadRecallRecord(ctx, normalized.Scope, memory.RevisionID, true)
		if loadErr != nil {
			return nil, loadErr
		}
		if found {
			result = append(result, record)
		}
	}
	return result, nil
}

type canonicalRecallView struct {
	item       sessionmemory.MemoryItem
	revision   sessionmemory.MemoryRevision
	state      sessionmemory.RevisionState
	sourceRefs []sessionmemory.SourceRef
	sourceIDs  []string
	sessions   []string
	compat     *sessionmemory.CanonicalCompatibilityPayload
}

func (r *CanonicalReader) loadRecallRecord(ctx context.Context, scope sessionmemory.Scope, revisionID string, activeOnly bool) (sessionmemory.RecallRecord, bool, error) {
	view, found, err := r.loadCanonicalView(ctx, scope, revisionID, activeOnly)
	if err != nil || !found {
		return sessionmemory.RecallRecord{}, found, err
	}
	text, err := r.store.LoadPayload(ctx, view.revision.Payload)
	if err != nil {
		if isCanonicalMissing(err) {
			return sessionmemory.RecallRecord{}, false, nil
		}
		return sessionmemory.RecallRecord{}, false, err
	}
	category := recallCategory(view.item.Kind)
	textValue := string(text)
	var legacyKind *sessionmemory.DerivedKind
	var topicKey, title string
	var legacyItemID, legacyRevisionID, legacyOperationID string
	var legacySupersedes *sessionmemory.RevisionRef
	var legacyParents []sessionmemory.RevisionRef
	if compatibility, ok := canonicalCompatibilityPayload(text, scope); ok {
		textValue = compatibility.Text
		kind := compatibility.Kind
		legacyKind = &kind
		legacyItemID = compatibility.LegacyItemID
		legacyRevisionID = compatibility.LegacyRevisionID
		legacyOperationID = compatibility.LegacyOperationID
		if compatibility.Supersedes != nil {
			copyOf := *compatibility.Supersedes
			legacySupersedes = &copyOf
		}
		legacyParents = append([]sessionmemory.RevisionRef(nil), compatibility.LegacyParents...)
		if compatibility.Category != nil {
			category = *compatibility.Category
		}
		topicKey = compatibility.TopicKey
		title = compatibility.Title
	}
	record := sessionmemory.RecallRecord{
		Scope:             scope,
		ItemID:            view.item.ItemID,
		RevisionID:        view.revision.RevisionID,
		Revision:          view.revision.Revision,
		Kind:              view.item.Kind,
		Category:          &category,
		MemoryKey:         view.item.MemoryKey,
		Text:              textValue,
		LegacyKind:        legacyKind,
		LegacyItemID:      legacyItemID,
		LegacyRevisionID:  legacyRevisionID,
		LegacyOperationID: legacyOperationID,
		LegacySupersedes:  legacySupersedes,
		LegacyParents:     legacyParents,
		TopicKey:          topicKey,
		Title:             title,
		State:             sessionmemory.RevisionStateActive,
		CreatedAt:         view.revision.Temporal.ObservedAt,
		Temporal:          view.revision.Temporal,
		Sensitivity:       view.revision.Sensitivity,
		Retention:         view.revision.Retention,
		Evidence:          append([]sessionmemory.EvidenceRef(nil), view.revision.Evidence...),
		ScopeChangeSeq:    0,
	}
	state, err := r.store.LoadScopeState(ctx, scope)
	if err != nil {
		return sessionmemory.RecallRecord{}, false, err
	}
	record.ScopeChangeSeq = state.ChangeSeq
	for _, source := range view.sourceRefs {
		if source.Scope != scope {
			return sessionmemory.RecallRecord{}, false, sessionmemory.PermanentError(sessionmemory.CodeScopeViolation, "canonical recall source scope does not match", nil)
		}
	}
	record.SourceIDs = make([]string, 0, len(view.sourceRefs))
	for _, source := range view.sourceRefs {
		record.SourceIDs = append(record.SourceIDs, source.ExportID)
	}
	// Canonical v2 source IDs are the stable source identities used in
	// EvidenceRef. Keep them as the public source filter keys and retain the
	// session IDs separately for session filtering.
	record.SourceIDs = record.SourceIDs[:0]
	for _, evidence := range view.revision.Evidence {
		record.SourceIDs = appendUnique(record.SourceIDs, evidence.SourceID)
	}
	record.SourceRefs = append([]sessionmemory.SourceRef(nil), view.sourceRefs...)
	record.SessionIDs = append([]string(nil), view.sessions...)
	return record, true, nil
}

func canonicalCompatibilityPayload(payload []byte, scope sessionmemory.Scope) (sessionmemory.CanonicalCompatibilityPayload, bool) {
	var compatibility sessionmemory.CanonicalCompatibilityPayload
	if err := json.Unmarshal(payload, &compatibility); err != nil {
		return sessionmemory.CanonicalCompatibilityPayload{}, false
	}
	if err := compatibility.Validate(scope); err != nil {
		return sessionmemory.CanonicalCompatibilityPayload{}, false
	}
	return compatibility, true
}

func (r *CanonicalReader) loadCanonicalView(ctx context.Context, scope sessionmemory.Scope, revisionID string, activeOnly bool) (canonicalRecallView, bool, error) {
	if err := scope.Validate(); err != nil {
		return canonicalRecallView{}, false, err
	}
	if strings.TrimSpace(revisionID) == "" {
		return canonicalRecallView{}, false, sessionmemory.PermanentError(sessionmemory.CodeInvalidDerived, "canonical revision id is required", nil)
	}
	if r == nil || r.store == nil || r.store.db == nil {
		return canonicalRecallView{}, false, sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "canonical reader store is closed", nil)
	}
	r.store.maintenanceMu.RLock()
	defer r.store.maintenanceMu.RUnlock()
	var view = canonicalRecallView{state: sessionmemory.RevisionStateActive}
	err := r.store.db.View(func(txn *badger.Txn) error {
		revisionKey, err := badgerSessionMemoryKey(scope, badgerRecordRevision, revisionID)
		if err != nil {
			return err
		}
		if err := getBadgerSessionMemoryRecord(txn, revisionKey, badgerRecordRevision, &view.revision); err != nil {
			if errors.Is(err, badger.ErrKeyNotFound) {
				return err
			}
			return err
		}
		if err := view.revision.Validate(); err != nil {
			return sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "stored canonical revision is invalid", err)
		}
		itemKey, err := badgerSessionMemoryKey(scope, badgerRecordItem, view.revision.ItemID)
		if err != nil {
			return err
		}
		if err := getBadgerSessionMemoryRecord(txn, itemKey, badgerRecordItem, &view.item); err != nil {
			return err
		}
		if err := view.item.Validate(); err != nil || view.item.Scope != scope || view.item.ItemID != view.revision.ItemID {
			return sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "stored canonical item is invalid", err)
		}
		headKey, keyErr := badgerSessionMemoryKey(scope, badgerRecordHead, view.item.ItemID)
		if keyErr != nil {
			return keyErr
		}
		var head sessionmemory.ItemHead
		if err := getBadgerSessionMemoryRecord(txn, headKey, badgerRecordHead, &head); err != nil {
			return err
		}
		if err := head.Validate(); err != nil || head.ItemID != view.item.ItemID {
			return sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "stored canonical item head is invalid", err)
		}
		if head.RevisionID != view.revision.RevisionID {
			view.state = sessionmemory.RevisionStateSuperseded
			if activeOnly {
				return errCanonicalInactive
			}
		}
		deniedKey, err := badgerSessionMemoryKey(scope, badgerRecordDeniedRevision, view.revision.RevisionID)
		if err != nil {
			return err
		}
		var denied badgerDeniedRevision
		if err := getBadgerSessionMemoryRecord(txn, deniedKey, badgerRecordDeniedRevision, &denied); err == nil {
			return errCanonicalDenied
		} else if !errors.Is(err, badger.ErrKeyNotFound) {
			return err
		}
		seenSources := make(map[string]struct{}, len(view.revision.Evidence))
		for _, evidence := range view.revision.Evidence {
			if _, exists := seenSources[evidence.SourceID]; exists {
				continue
			}
			seenSources[evidence.SourceID] = struct{}{}
			deniedSourceKey, keyErr := badgerSessionMemoryKey(scope, badgerRecordDeniedSource, evidence.SourceID)
			if keyErr != nil {
				return keyErr
			}
			var deniedSource badgerDeniedSource
			if err := getBadgerSessionMemoryRecord(txn, deniedSourceKey, badgerRecordDeniedSource, &deniedSource); err == nil {
				return errCanonicalDenied
			} else if !errors.Is(err, badger.ErrKeyNotFound) {
				return err
			}
			sourceKey, keyErr := badgerSessionMemoryKey(scope, badgerRecordSource, evidence.SourceID)
			if keyErr != nil {
				return keyErr
			}
			var source sessionmemory.SourceRecordV2
			if err := getBadgerSessionMemoryRecord(txn, sourceKey, badgerRecordSource, &source); err != nil {
				return err
			}
			if err := source.Validate(); err != nil || source.Scope != scope {
				return sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "stored canonical source is invalid", err)
			}
			payloadKey, keyErr := badgerSessionMemoryKey(payloadScope(), badgerRecordPayload, source.Payload.ID)
			if keyErr != nil {
				return keyErr
			}
			var payload []byte
			if err := getBadgerSessionMemoryRecord(txn, payloadKey, badgerRecordPayload, &payload); err != nil {
				return err
			}
			if !isBadgerPayloadValid(payload, source.Payload) {
				return sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "stored canonical source payload is invalid", nil)
			}
			ref, sessionID, ok := canonicalSourceRef(payload, scope, evidence.SourceID)
			if ok {
				view.sourceRefs = append(view.sourceRefs, ref)
				view.sourceIDs = append(view.sourceIDs, evidence.SourceID)
				if sessionID != "" {
					view.sessions = appendUnique(view.sessions, sessionID)
				}
			}
		}
		return nil
	})
	if errors.Is(err, badger.ErrKeyNotFound) || errors.Is(err, errCanonicalInactive) || errors.Is(err, errCanonicalDenied) {
		return canonicalRecallView{}, false, nil
	}
	if err != nil {
		return canonicalRecallView{}, false, badgerSessionMemoryError("load canonical recall view", err)
	}
	return view, true, nil
}

var (
	errCanonicalInactive = errors.New("canonical revision is inactive")
	errCanonicalDenied   = errors.New("canonical revision is denied")
)

func canonicalReaderContext(ctx context.Context) error {
	if ctx == nil {
		return sessionmemory.PermanentError(sessionmemory.CodeInvalidDerived, "canonical reader context is required", nil)
	}
	select {
	case <-ctx.Done():
		return sessionmemory.RetryableError(sessionmemory.CodeTimeout, "canonical reader context ended", ctx.Err())
	default:
		return nil
	}
}

func isCanonicalMissing(err error) bool {
	code, _, ok := sessionmemory.ClassifyError(err)
	return ok && (code == sessionmemory.CodeNotFound || code == sessionmemory.CodeForgotten)
}

func recallCategory(kind sessionmemory.MemoryKind) sessionmemory.AtomCategory {
	if kind == sessionmemory.MemoryKindEvent {
		return sessionmemory.AtomCategoryEvent
	}
	return sessionmemory.AtomCategoryFact
}

func appendUnique(values []string, value string) []string {
	if value == "" {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

type canonicalTurnSourcePayload struct {
	ExportID       string                           `json:"export_id"`
	SessionID      string                           `json:"session_id"`
	AgentSessionID string                           `json:"agent_session_id"`
	SourceTurnID   string                           `json:"source_turn_id"`
	CompletedAt    time.Time                        `json:"completed_at"`
	TerminalStatus sessionmemory.TurnTerminalStatus `json:"terminal_status"`
}

// canonicalSourceRef accepts both native canonical turn payloads and the
// validated v1 SourceRecord payload emitted by the migration adapter.
func canonicalSourceRef(payload []byte, scope sessionmemory.Scope, sourceID string) (sessionmemory.SourceRef, string, bool) {
	var source sessionmemory.SourceRecord
	if err := json.Unmarshal(payload, &source); err == nil && source.Ref.Scope == scope && source.Ref.SessionID != "" {
		return source.Ref, source.Ref.SessionID, true
	}
	var turn canonicalTurnSourcePayload
	if err := json.Unmarshal(payload, &turn); err != nil || turn.ExportID == "" || turn.SessionID == "" || turn.SourceTurnID == "" {
		return sessionmemory.SourceRef{}, "", false
	}
	ref := sessionmemory.SourceRef{Scope: scope, ExportID: turn.ExportID, SessionID: turn.SessionID, SourceTurnID: turn.SourceTurnID}
	if ref.Validate() != nil || sourceID == "" {
		return sessionmemory.SourceRef{}, "", false
	}
	return ref, turn.SessionID, true
}

func payloadScope() sessionmemory.Scope {
	return sessionmemory.Scope{Key: "internal:payload", Kind: sessionmemory.ScopeKindPersonal}
}

var _ sessionmemory.CanonicalDerivedReader = (*CanonicalReader)(nil)

// SearchDerived maps representable v2 revisions to the additive derived
// response contract. v2 state/event records are exposed as atom references;
// category is derived from the durable memory kind and provenance remains
// grounded in canonical evidence.
func (r *CanonicalReader) SearchDerived(ctx context.Context, request sessionmemory.DerivedSearchRequest) (sessionmemory.DerivedSearchResponse, error) {
	if err := canonicalReaderContext(ctx); err != nil {
		return sessionmemory.DerivedSearchResponse{}, err
	}
	normalized, err := sessionmemory.NormalizeDerivedSearchRequest(request)
	if err != nil {
		return sessionmemory.DerivedSearchResponse{}, err
	}
	recallRequest := sessionmemory.RecallRequest{
		Scope: normalized.Scope, Query: normalized.Query, Limit: normalized.Limit,
		AsOf: normalized.AsOf, SourceID: normalized.SourceID, SessionID: normalized.SessionID,
		MinScopeChangeSeq: normalized.MinScopeChangeSeq,
	}
	if normalized.MinScopeChangeSeq != 0 {
		current, seqErr := r.CurrentScopeChangeSeq(ctx, normalized.Scope)
		if seqErr != nil {
			return sessionmemory.DerivedSearchResponse{}, seqErr
		}
		if current < normalized.MinScopeChangeSeq {
			return sessionmemory.DerivedSearchResponse{}, sessionmemory.RetryableError(sessionmemory.CodeConflict, "canonical search watermark has not caught up", nil)
		}
	}
	// The legacy atom kind maps to both canonical state and event records;
	// category filtering below preserves the caller's requested distinction.
	records, err := r.SearchRecallTail(ctx, recallRequest, uint32(sessionmemory.MaxRecallCandidates))
	if err != nil {
		return sessionmemory.DerivedSearchResponse{}, err
	}
	results := make([]sessionmemory.DerivedReference, 0, len(records))
	for _, record := range records {
		if !strings.Contains(strings.ToLower(record.Text), strings.ToLower(normalized.Query)) {
			continue
		}
		if normalized.AsOf != nil && record.CreatedAt.After(normalized.AsOf.UTC()) {
			continue
		}
		if normalized.MemoryKey != "" && record.MemoryKey != sessionmemory.MemoryKey(normalized.MemoryKey) {
			continue
		}
		if normalized.SourceID != "" && !canonicalRecordMatchesSource(record, normalized.SourceID) {
			continue
		}
		if normalized.SessionID != "" && !containsString(record.SessionIDs, normalized.SessionID) {
			continue
		}
		if normalized.MinScopeChangeSeq != 0 && record.ScopeChangeSeq < normalized.MinScopeChangeSeq {
			return sessionmemory.DerivedSearchResponse{}, sessionmemory.RetryableError(sessionmemory.CodeConflict, "canonical search watermark has not caught up", nil)
		}
		legacyKind := sessionmemory.DerivedKindAtom
		if record.LegacyKind != nil {
			legacyKind = *record.LegacyKind
		}
		if normalized.Kind != nil && *normalized.Kind != legacyKind {
			continue
		}
		category := recallCategory(record.Kind)
		if record.Category != nil {
			category = *record.Category
		}
		if normalized.Category != nil && (record.Category == nil || *normalized.Category != category) {
			continue
		}
		provenance := sessionmemory.Provenance{ParentRevisions: append([]sessionmemory.RevisionRef(nil), record.LegacyParents...)}
		if legacyKind == sessionmemory.DerivedKindAtom || record.LegacyKind == nil {
			if len(record.SourceRefs) > 0 {
				provenance.RawSources = append(provenance.RawSources, record.SourceRefs...)
			} else {
				for _, evidence := range record.Evidence {
					provenance.RawSources = append(provenance.RawSources, sessionmemory.SourceRef{Scope: record.Scope, ExportID: evidence.SourceID, SessionID: firstSession(record.SessionIDs), SourceTurnID: evidence.SourceID})
				}
			}
		}
		if err := provenance.Validate(record.Scope); err != nil {
			continue
		}
		itemID, revisionID := record.ItemID, record.RevisionID
		if record.LegacyKind != nil {
			itemID, revisionID = record.LegacyItemID, record.LegacyRevisionID
		}
		ref := sessionmemory.DerivedReference{
			SchemaVersion: sessionmemory.DerivedSchemaVersionV1, Trust: sessionmemory.ReferenceTrustUntrusted,
			Kind: legacyKind, Scope: record.Scope, ItemID: itemID, RevisionID: revisionID,
			Revision: record.Revision, State: sessionmemory.RevisionStateActive, Category: &category,
			TopicKey: record.TopicKey, Title: record.Title, Text: record.Text, CreatedAt: record.CreatedAt, Provenance: provenance,
		}
		if legacyKind != sessionmemory.DerivedKindAtom {
			ref.Category = nil
		}
		if err := ref.Validate(); err != nil {
			continue
		}
		results = append(results, ref)
	}
	sort.SliceStable(results, func(i, j int) bool {
		if !results[i].CreatedAt.Equal(results[j].CreatedAt) {
			return results[i].CreatedAt.After(results[j].CreatedAt)
		}
		return results[i].RevisionID < results[j].RevisionID
	})
	if len(results) > normalized.Limit {
		results = results[:normalized.Limit]
	}
	response := sessionmemory.DerivedSearchResponse{SchemaVersion: sessionmemory.DerivedSchemaVersionV1, Trust: sessionmemory.ReferenceTrustUntrusted, Scope: normalized.Scope, Results: results}
	if err := response.Validate(normalized.Limit); err != nil {
		return sessionmemory.DerivedSearchResponse{}, err
	}
	return response, nil
}

func canonicalRecordMatchesSource(record sessionmemory.RecallRecord, sourceID string) bool {
	if containsString(record.SourceIDs, sourceID) {
		return true
	}
	for _, source := range record.SourceRefs {
		if source.ExportID == sourceID {
			return true
		}
	}
	return false
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func firstSession(values []string) string {
	if len(values) == 0 {
		return "session-unknown"
	}
	return values[0]
}

// Trace returns a bounded parent closure for one exact canonical scope.
func (r *CanonicalReader) Trace(ctx context.Context, request sessionmemory.TraceRequest) (sessionmemory.TraceResponse, error) {
	if err := canonicalReaderContext(ctx); err != nil {
		return sessionmemory.TraceResponse{}, err
	}
	normalized, err := sessionmemory.NormalizeTraceRequest(request, sessionmemory.MaxTraceNodes)
	if err != nil {
		return sessionmemory.TraceResponse{}, err
	}
	queue := []string{normalized.Root.RevisionID}
	seen := make(map[string]struct{}, normalized.MaxNodes)
	type traceNode struct {
		view canonicalRecallView
		text []byte
	}
	nodes := make([]traceNode, 0, normalized.MaxNodes)
	sourcesByRef := make(map[sessionmemory.SourceRef]sessionmemory.SourceRecord)
	for len(queue) > 0 {
		if len(seen) >= normalized.MaxNodes {
			return sessionmemory.TraceResponse{}, sessionmemory.PermanentError(sessionmemory.CodeLimitExceeded, "canonical trace exceeds node bound", nil)
		}
		revisionID := queue[0]
		queue = queue[1:]
		if _, exists := seen[revisionID]; exists {
			continue
		}
		view, found, loadErr := r.loadCanonicalView(ctx, normalized.Scope, revisionID, false)
		if loadErr != nil {
			return sessionmemory.TraceResponse{}, loadErr
		}
		if !found {
			return sessionmemory.TraceResponse{}, sessionmemory.PermanentError(sessionmemory.CodeForgotten, "canonical trace revision is unavailable", nil)
		}
		if revisionID == normalized.Root.RevisionID && view.item.ItemID != normalized.Root.ItemID {
			return sessionmemory.TraceResponse{}, sessionmemory.PermanentError(sessionmemory.CodeScopeViolation, "canonical trace root item does not match", nil)
		}
		seen[revisionID] = struct{}{}
		queue = append(queue, view.revision.Parents...)
		text, payloadErr := r.store.LoadPayload(ctx, view.revision.Payload)
		if payloadErr != nil {
			return sessionmemory.TraceResponse{}, payloadErr
		}
		if compatibility, ok := canonicalCompatibilityPayload(text, normalized.Scope); ok {
			view.compat = &compatibility
		}
		for index, source := range view.sourceRefs {
			if _, exists := sourcesByRef[source]; exists {
				continue
			}
			sourceID := source.ExportID
			if index < len(view.sourceIDs) {
				sourceID = view.sourceIDs[index]
			}
			sourceRecord, sourceErr := r.loadSourceRecord(ctx, normalized.Scope, source, sourceID)
			if sourceErr != nil {
				return sessionmemory.TraceResponse{}, sourceErr
			}
			if sourceRecord.Ref != source {
				return sessionmemory.TraceResponse{}, sessionmemory.PermanentError(sessionmemory.CodeScopeViolation, "canonical trace source identity does not match", nil)
			}
			sourcesByRef[source] = sourceRecord
		}
		if len(view.sourceRefs) == 0 {
			return sessionmemory.TraceResponse{}, sessionmemory.PermanentError(sessionmemory.CodeForgotten, "canonical trace provenance is unavailable", nil)
		}
		if len(nodes)+len(sourcesByRef) > normalized.MaxNodes {
			return sessionmemory.TraceResponse{}, sessionmemory.PermanentError(sessionmemory.CodeLimitExceeded, "canonical trace exceeds node bound", nil)
		}
		nodes = append(nodes, traceNode{view: view, text: text})
	}
	itemByRevision := make(map[string]string, len(nodes))
	for _, node := range nodes {
		itemByRevision[node.view.revision.RevisionID] = node.view.item.ItemID
	}
	records := make([]sessionmemory.SearchHit, 0, len(nodes))
	for _, node := range nodes {
		view := node.view
		provenance := sessionmemory.Provenance{ParentRevisions: make([]sessionmemory.RevisionRef, 0, len(view.revision.Parents))}
		for _, parentID := range view.revision.Parents {
			parentItemID, ok := itemByRevision[parentID]
			if !ok {
				return sessionmemory.TraceResponse{}, sessionmemory.PermanentError(sessionmemory.CodeNotFound, "canonical trace parent revision is missing", nil)
			}
			provenance.ParentRevisions = append(provenance.ParentRevisions, sessionmemory.RevisionRef{ItemID: parentItemID, RevisionID: parentID})
		}
		for _, source := range view.sourceRefs {
			if _, ok := sourcesByRef[source]; ok {
				provenance.RawSources = append(provenance.RawSources, source)
			}
		}
		if err := provenance.Validate(normalized.Scope); err != nil {
			return sessionmemory.TraceResponse{}, err
		}
		hit, err := canonicalTraceHit(normalized.Scope, view, node.text, provenance, categoryForCanonicalView(view))
		if err != nil {
			return sessionmemory.TraceResponse{}, err
		}
		records = append(records, hit)
	}
	root := normalized.Root
	sources := make([]sessionmemory.SourceRecord, 0, len(sourcesByRef))
	for _, source := range sourcesByRef {
		sources = append(sources, source)
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i].Ref.ExportID < sources[j].Ref.ExportID })
	response := sessionmemory.TraceResponse{SchemaVersion: sessionmemory.DerivedSchemaVersionV1, Trust: sessionmemory.ReferenceTrustUntrusted, Scope: normalized.Scope, Root: root, Revisions: records, Sources: sources}
	if err := response.Validate(normalized.MaxNodes); err != nil {
		return sessionmemory.TraceResponse{}, err
	}
	return response, nil
}

func categoryForCanonicalView(view canonicalRecallView) sessionmemory.AtomCategory {
	if view.item.Kind == sessionmemory.MemoryKindEvent {
		return sessionmemory.AtomCategoryEvent
	}
	return sessionmemory.AtomCategoryFact
}

func canonicalTraceHit(scope sessionmemory.Scope, view canonicalRecallView, text []byte, provenance sessionmemory.Provenance, category sessionmemory.AtomCategory) (sessionmemory.SearchHit, error) {
	if view.compat == nil {
		meta := sessionmemory.RevisionMeta{SchemaVersion: sessionmemory.DerivedSchemaVersionV1, Kind: sessionmemory.DerivedKindAtom, ItemID: view.item.ItemID, RevisionID: view.revision.RevisionID, Revision: view.revision.Revision, OperationID: view.revision.RevisionID, Scope: scope, State: view.state, Provenance: provenance, CreatedAt: view.revision.Temporal.ObservedAt}
		atom := sessionmemory.Atom{Meta: meta, Category: category, Text: string(text), Relation: sessionmemory.CandidateRelationNew}
		hit := sessionmemory.SearchHit{Atom: &atom}
		if _, err := hit.Validate(); err != nil {
			return sessionmemory.SearchHit{}, err
		}
		return hit, nil
	}
	compat := *view.compat
	legacyProvenance := provenance
	legacyProvenance.ParentRevisions = append([]sessionmemory.RevisionRef(nil), compat.LegacyParents...)
	if compat.Kind != sessionmemory.DerivedKindAtom {
		legacyProvenance.RawSources = nil
	}
	meta := sessionmemory.RevisionMeta{SchemaVersion: sessionmemory.DerivedSchemaVersionV1, Kind: compat.Kind, ItemID: compat.LegacyItemID, RevisionID: compat.LegacyRevisionID, Revision: view.revision.Revision, OperationID: compat.LegacyOperationID, Scope: scope, State: view.state, Provenance: legacyProvenance, CreatedAt: view.revision.Temporal.ObservedAt}
	if compat.Supersedes != nil {
		copyOf := *compat.Supersedes
		meta.Supersedes = &copyOf
	}
	if meta.OperationID == "" {
		return sessionmemory.SearchHit{}, sessionmemory.PermanentError(sessionmemory.CodeInvalidDerived, "canonical compatibility operation identity is unavailable", nil)
	}
	switch compat.Kind {
	case sessionmemory.DerivedKindScenario:
		hit := sessionmemory.SearchHit{Scenario: &sessionmemory.Scenario{Meta: meta, TopicKey: compat.TopicKey, Title: compat.Title, Summary: compat.Text}}
		if _, err := hit.Validate(); err != nil {
			return sessionmemory.SearchHit{}, err
		}
		return hit, nil
	case sessionmemory.DerivedKindProfile:
		hit := sessionmemory.SearchHit{Profile: &sessionmemory.Profile{Meta: meta, Summary: compat.Text}}
		if _, err := hit.Validate(); err != nil {
			return sessionmemory.SearchHit{}, err
		}
		return hit, nil
	case sessionmemory.DerivedKindAtom:
		hit := sessionmemory.SearchHit{Atom: &sessionmemory.Atom{Meta: meta, Category: *compat.Category, Text: compat.Text, Relation: sessionmemory.CandidateRelationNew}}
		if _, err := hit.Validate(); err != nil {
			return sessionmemory.SearchHit{}, err
		}
		return hit, nil
	default:
		return sessionmemory.SearchHit{}, sessionmemory.PermanentError(sessionmemory.CodeInvalidDerived, "canonical compatibility kind is invalid", nil)
	}
}

func (r *CanonicalReader) loadSourceRecord(ctx context.Context, scope sessionmemory.Scope, ref sessionmemory.SourceRef, sourceID string) (sessionmemory.SourceRecord, error) {
	if r == nil || r.store == nil || r.store.db == nil {
		return sessionmemory.SourceRecord{}, sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "canonical reader store is closed", nil)
	}
	if err := ref.Validate(); err != nil {
		return sessionmemory.SourceRecord{}, err
	}
	var source sessionmemory.SourceRecordV2
	key, err := badgerSessionMemoryKey(scope, badgerRecordSource, sourceID)
	if err != nil {
		return sessionmemory.SourceRecord{}, err
	}
	if err := r.store.db.View(func(txn *badger.Txn) error {
		return getBadgerSessionMemoryRecord(txn, key, badgerRecordSource, &source)
	}); err != nil {
		return sessionmemory.SourceRecord{}, badgerSessionMemoryError("load canonical trace source", err)
	}
	// Evidence keys normally use the canonical SourceID, whereas migrated
	// payloads retain the original export ID. Try both identities so the
	// adapter remains compatible with either representation.
	if source.SourceID != ref.ExportID {
		key, err = badgerSessionMemoryKey(scope, badgerRecordSource, ref.ExportID)
		if err != nil {
			return sessionmemory.SourceRecord{}, err
		}
		if loadErr := r.store.db.View(func(txn *badger.Txn) error {
			return getBadgerSessionMemoryRecord(txn, key, badgerRecordSource, &source)
		}); loadErr != nil {
			return sessionmemory.SourceRecord{}, badgerSessionMemoryError("load canonical trace source alias", loadErr)
		}
	}
	payload, err := r.store.LoadPayload(ctx, source.Payload)
	if err != nil {
		return sessionmemory.SourceRecord{}, err
	}
	var migrated sessionmemory.SourceRecord
	if err := json.Unmarshal(payload, &migrated); err == nil && migrated.Ref.Scope == scope && migrated.Ref.SessionID != "" {
		if migrated.SchemaVersion == sessionmemory.DerivedSchemaVersionV1 && migrated.State == sessionmemory.SourceStateActive && migrated.Turn != nil {
			if err := migrated.Validate(); err != nil {
				return sessionmemory.SourceRecord{}, sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "migrated canonical source is invalid", err)
			}
			return migrated, nil
		}
	}
	var turnPayload canonicalTurnSourcePayload
	if err := json.Unmarshal(payload, &turnPayload); err != nil || turnPayload.ExportID == "" || turnPayload.SessionID == "" || turnPayload.SourceTurnID == "" {
		return sessionmemory.SourceRecord{}, sessionmemory.PermanentError(sessionmemory.CodeInvalidDerived, "canonical trace source payload is not representable", nil)
	}
	messages, err := r.loadSourceMessages(ctx, scope, source.SourceID)
	if err != nil {
		return sessionmemory.SourceRecord{}, err
	}
	if len(messages) == 0 {
		return sessionmemory.SourceRecord{}, sessionmemory.PermanentError(sessionmemory.CodeInvalidDerived, "canonical trace source has no messages", nil)
	}
	turn := sessionmemory.Turn{
		SchemaVersion:  sessionmemory.SchemaVersionV1,
		ExportID:       turnPayload.ExportID,
		Scope:          scope,
		Session:        sessionmemory.SessionRef{SessionID: turnPayload.SessionID, AgentSessionID: turnPayload.AgentSessionID},
		SourceTurnID:   turnPayload.SourceTurnID,
		SourceID:       source.SourceID,
		CompletedAt:    turnPayload.CompletedAt,
		TerminalStatus: turnPayload.TerminalStatus,
		Messages:       messages,
	}
	if err := turn.Validate(); err != nil {
		return sessionmemory.SourceRecord{}, sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "canonical trace source turn is invalid", err)
	}
	record := sessionmemory.SourceRecord{SchemaVersion: sessionmemory.DerivedSchemaVersionV1, Ref: ref, State: sessionmemory.SourceStateActive, Turn: &turn}
	if err := record.Validate(); err != nil {
		return sessionmemory.SourceRecord{}, err
	}
	return record, nil
}

func (r *CanonicalReader) loadSourceMessages(ctx context.Context, scope sessionmemory.Scope, sourceID string) ([]sessionmemory.Message, error) {
	if err := canonicalReaderContext(ctx); err != nil {
		return nil, err
	}
	prefix, err := badgerSessionMemoryPrefix(scope, badgerRecordMessage)
	if err != nil {
		return nil, err
	}
	var records []sessionmemory.MessageRecord
	if err := r.store.db.View(func(txn *badger.Txn) error {
		options := badger.DefaultIteratorOptions
		options.Prefix = prefix
		iterator := txn.NewIterator(options)
		defer iterator.Close()
		for iterator.Seek(prefix); iterator.ValidForPrefix(prefix); iterator.Next() {
			var record sessionmemory.MessageRecord
			if err := getBadgerSessionMemoryRecord(txn, iterator.Item().Key(), badgerRecordMessage, &record); err != nil {
				return err
			}
			if record.SourceID == sourceID {
				if len(records) >= 32 {
					return sessionmemory.PermanentError(sessionmemory.CodeLimitExceeded, "canonical trace source message bound exceeded", nil)
				}
				if err := record.Validate(); err != nil {
					return err
				}
				records = append(records, record)
			}
		}
		return nil
	}); err != nil {
		return nil, badgerSessionMemoryError("scan canonical trace messages", err)
	}
	sort.Slice(records, func(i, j int) bool {
		return messageRoleOrder(records[i].Role) < messageRoleOrder(records[j].Role) ||
			(messageRoleOrder(records[i].Role) == messageRoleOrder(records[j].Role) && records[i].MessageID < records[j].MessageID)
	})
	messages := make([]sessionmemory.Message, 0, len(records))
	for _, record := range records {
		payload, err := r.store.LoadPayload(ctx, record.Payload)
		if err != nil {
			return nil, err
		}
		messages = append(messages, sessionmemory.Message{MessageID: record.MessageID, Role: record.Role, Text: string(payload)})
	}
	return messages, nil
}

func messageRoleOrder(role sessionmemory.MessageRole) int {
	switch role {
	case sessionmemory.MessageRoleUser:
		return 0
	case sessionmemory.MessageRoleAssistant:
		return 1
	default:
		return 2
	}
}
