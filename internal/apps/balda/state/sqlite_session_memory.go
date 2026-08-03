package state

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/normahq/balda/sessionmemory"
)

// sqliteSessionMemoryStore is the durable implementation of the portable
// sessionmemory.Store contract. The scope snapshot is the canonical document;
// the normalized tables are maintained in the same transaction as a query and
// provenance projection. JetStream delivery state is intentionally absent.
type sqliteSessionMemoryStore struct {
	db *sql.DB
}

var _ sessionmemory.Store = (*sqliteSessionMemoryStore)(nil)

func (s *sqliteSessionMemoryStore) LookupOperation(
	ctx context.Context,
	lookup sessionmemory.OperationLookup,
) (sessionmemory.OperationLookupResult, error) {
	if err := sessionMemoryContextError(ctx); err != nil {
		return sessionmemory.OperationLookupResult{}, err
	}
	if err := lookup.Validate(); err != nil {
		return sessionmemory.OperationLookupResult{}, err
	}
	var requestJSON, outcomeJSON, stage string
	err := s.db.QueryRowContext(ctx, `
		SELECT request_json, outcome_json, stage
		FROM session_memory_operations
		WHERE scope_key = ? AND operation_id = ?`,
		lookup.Scope.Key, lookup.OperationID,
	).Scan(&requestJSON, &outcomeJSON, &stage)
	if err != nil {
		if err == sql.ErrNoRows {
			return sessionmemory.OperationLookupResult{}, nil
		}
		return sessionmemory.OperationLookupResult{}, sessionMemoryStoreError("lookup operation", err)
	}
	if stage != string(lookup.Stage) {
		return sessionmemory.OperationLookupResult{}, sessionmemory.PermanentError(sessionmemory.CodeConflict, "operation identity was reused", nil)
	}
	var outcome sessionmemory.OperationOutcome
	if err := json.Unmarshal([]byte(outcomeJSON), &outcome); err != nil {
		return sessionmemory.OperationLookupResult{}, sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "stored operation outcome is invalid", err)
	}
	if err := outcome.Validate(); err != nil {
		return sessionmemory.OperationLookupResult{}, err
	}
	if outcome.Scope != lookup.Scope || outcome.Stage != lookup.Stage || outcome.OperationID != lookup.OperationID {
		return sessionmemory.OperationLookupResult{}, sessionmemory.PermanentError(sessionmemory.CodeScopeViolation, "stored operation outcome scope does not match lookup", nil)
	}
	_ = requestJSON // retained for commit replay identity checks
	return sessionmemory.OperationLookupResult{Found: true, Outcome: outcome}, nil
}

func (s *sqliteSessionMemoryStore) LookupForget(
	ctx context.Context,
	lookup sessionmemory.ForgetLookup,
) (sessionmemory.ForgetLookupResult, error) {
	if err := sessionMemoryContextError(ctx); err != nil {
		return sessionmemory.ForgetLookupResult{}, err
	}
	if err := lookup.Validate(); err != nil {
		return sessionmemory.ForgetLookupResult{}, err
	}
	var outcomeJSON, kind string
	err := s.db.QueryRowContext(ctx, `
		SELECT outcome_json, kind
		FROM session_memory_forgets
		WHERE scope_key = ? AND operation_id = ?`,
		lookup.Scope.Key, lookup.OperationID,
	).Scan(&outcomeJSON, &kind)
	if err != nil {
		if err == sql.ErrNoRows {
			return sessionmemory.ForgetLookupResult{}, nil
		}
		return sessionmemory.ForgetLookupResult{}, sessionMemoryStoreError("lookup forget", err)
	}
	if kind != string(lookup.Kind) {
		return sessionmemory.ForgetLookupResult{}, sessionmemory.PermanentError(sessionmemory.CodeConflict, "forget identity was reused", nil)
	}
	var outcome sessionmemory.ForgetOutcome
	if err := json.Unmarshal([]byte(outcomeJSON), &outcome); err != nil {
		return sessionmemory.ForgetLookupResult{}, sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "stored forget outcome is invalid", err)
	}
	if err := outcome.Validate(); err != nil {
		return sessionmemory.ForgetLookupResult{}, err
	}
	if outcome.Scope != lookup.Scope || outcome.Kind != lookup.Kind || outcome.OperationID != lookup.OperationID {
		return sessionmemory.ForgetLookupResult{}, sessionmemory.PermanentError(sessionmemory.CodeScopeViolation, "stored forget outcome scope does not match lookup", nil)
	}
	return sessionmemory.ForgetLookupResult{Found: true, Outcome: outcome}, nil
}

func (s *sqliteSessionMemoryStore) LoadScope(ctx context.Context, scope sessionmemory.Scope) (sessionmemory.ScopeSnapshot, error) {
	if err := sessionMemoryContextError(ctx); err != nil {
		return sessionmemory.ScopeSnapshot{}, err
	}
	if err := scope.Validate(); err != nil {
		return sessionmemory.ScopeSnapshot{}, err
	}
	return s.loadScope(ctx, scope)
}

func (s *sqliteSessionMemoryStore) loadScope(ctx context.Context, scope sessionmemory.Scope) (sessionmemory.ScopeSnapshot, error) {
	var kind string
	var version uint64
	var snapshotJSON string
	err := s.db.QueryRowContext(ctx, `
		SELECT scope_kind, version, snapshot_json
		FROM session_memory_scopes WHERE scope_key = ?`, scope.Key).Scan(&kind, &version, &snapshotJSON)
	if err != nil {
		if err == sql.ErrNoRows {
			return emptySessionMemorySnapshot(scope), nil
		}
		return sessionmemory.ScopeSnapshot{}, sessionMemoryStoreError("load session-memory scope", err)
	}
	if kind != string(scope.Kind) {
		return sessionmemory.ScopeSnapshot{}, sessionmemory.PermanentError(sessionmemory.CodeScopeViolation, "stored scope kind does not match locator", nil)
	}
	snapshot, err := decodeSessionMemorySnapshot(snapshotJSON, scope)
	if err != nil {
		return sessionmemory.ScopeSnapshot{}, err
	}
	if snapshot.Version != version {
		return sessionmemory.ScopeSnapshot{}, sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "stored scope version does not match snapshot", nil)
	}
	return snapshot, nil
}

func (s *sqliteSessionMemoryStore) Commit(ctx context.Context, request sessionmemory.CommitRequest) (sessionmemory.OperationOutcome, error) {
	if err := sessionMemoryContextError(ctx); err != nil {
		return sessionmemory.OperationOutcome{}, err
	}
	if err := request.Validate(); err != nil {
		return sessionmemory.OperationOutcome{}, err
	}
	requestJSON, err := json.Marshal(request)
	if err != nil {
		return sessionmemory.OperationOutcome{}, sessionmemory.PermanentError(sessionmemory.CodeInvalidDerived, "encode commit request", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return sessionmemory.OperationOutcome{}, sessionMemoryStoreError("begin session-memory commit", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := ensureSessionMemoryScopeTx(ctx, tx, request.Scope); err != nil {
		return sessionmemory.OperationOutcome{}, err
	}
	var priorRequest, priorOutcome string
	err = tx.QueryRowContext(ctx, `
		SELECT request_json, outcome_json
		FROM session_memory_operations
		WHERE scope_key = ? AND operation_id = ?`, request.Scope.Key, request.OperationID).
		Scan(&priorRequest, &priorOutcome)
	if err == nil {
		if !bytes.Equal([]byte(priorRequest), requestJSON) {
			return sessionmemory.OperationOutcome{}, sessionmemory.PermanentError(sessionmemory.CodeConflict, "operation identity was reused with different content", nil)
		}
		var outcome sessionmemory.OperationOutcome
		if err := json.Unmarshal([]byte(priorOutcome), &outcome); err != nil {
			return sessionmemory.OperationOutcome{}, sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "stored operation outcome is invalid", err)
		}
		if err := outcome.Validate(); err != nil {
			return sessionmemory.OperationOutcome{}, err
		}
		if err := tx.Commit(); err != nil {
			return sessionmemory.OperationOutcome{}, sessionMemoryStoreError("commit idempotent session-memory operation", err)
		}
		return outcome, nil
	}
	if err != sql.ErrNoRows {
		return sessionmemory.OperationOutcome{}, sessionMemoryStoreError("lookup session-memory operation", err)
	}
	snapshot, err := loadSessionMemoryScopeTx(ctx, tx, request.Scope)
	if err != nil {
		return sessionmemory.OperationOutcome{}, err
	}
	if snapshot.Version != request.ExpectedScopeVersion {
		return sessionmemory.OperationOutcome{}, sessionmemory.PermanentError(sessionmemory.CodeConflict, "scope version changed", nil)
	}
	if err := validateSessionMemoryCommit(snapshot, request); err != nil {
		return sessionmemory.OperationOutcome{}, err
	}
	if err := applySessionMemoryCommit(&snapshot, request); err != nil {
		return sessionmemory.OperationOutcome{}, err
	}
	snapshot.Version++
	if err := snapshot.Validate(sessionmemory.MaxSnapshotItems); err != nil {
		return sessionmemory.OperationOutcome{}, err
	}
	outcome := sessionMemoryCommitOutcome(request, snapshot.Version)
	if err := outcome.Validate(); err != nil {
		return sessionmemory.OperationOutcome{}, err
	}
	if err := persistSessionMemoryScopeTx(ctx, tx, snapshot); err != nil {
		return sessionmemory.OperationOutcome{}, err
	}
	outcomeJSON, err := json.Marshal(outcome)
	if err != nil {
		return sessionmemory.OperationOutcome{}, sessionmemory.PermanentError(sessionmemory.CodeInvalidDerived, "encode operation outcome", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO session_memory_operations(scope_key, operation_id, stage, request_json, outcome_json, committed_at)
		VALUES (?, ?, ?, ?, ?, ?)`, request.Scope.Key, request.OperationID, request.Stage, string(requestJSON), string(outcomeJSON), utcNow()); err != nil {
		return sessionmemory.OperationOutcome{}, sessionMemoryStoreError("persist session-memory operation", err)
	}
	if err := tx.Commit(); err != nil {
		return sessionmemory.OperationOutcome{}, sessionMemoryStoreError("commit session-memory operation", err)
	}
	return outcome, nil
}

func (s *sqliteSessionMemoryStore) ForgetSource(ctx context.Context, request sessionmemory.ForgetSourceRequest) (sessionmemory.ForgetOutcome, error) {
	if err := sessionMemoryContextError(ctx); err != nil {
		return sessionmemory.ForgetOutcome{}, err
	}
	if err := request.Validate(); err != nil {
		return sessionmemory.ForgetOutcome{}, err
	}
	requestJSON, err := json.Marshal(request)
	if err != nil {
		return sessionmemory.ForgetOutcome{}, sessionmemory.PermanentError(sessionmemory.CodeInvalidDerived, "encode source forget request", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return sessionmemory.ForgetOutcome{}, sessionMemoryStoreError("begin source forget", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := ensureSessionMemoryScopeTx(ctx, tx, request.Scope); err != nil {
		return sessionmemory.ForgetOutcome{}, err
	}
	prior, found, err := loadForgetOutcomeTx(ctx, tx, request.Scope, request.OperationID, requestJSON, sessionmemory.ForgetKindSource)
	if err != nil {
		return sessionmemory.ForgetOutcome{}, err
	}
	if found {
		if err := tx.Commit(); err != nil {
			return sessionmemory.ForgetOutcome{}, sessionMemoryStoreError("commit idempotent source forget", err)
		}
		return prior, nil
	}
	snapshot, err := loadSessionMemoryScopeTx(ctx, tx, request.Scope)
	if err != nil {
		return sessionmemory.ForgetOutcome{}, err
	}
	if snapshot.Version != request.ExpectedScopeVersion {
		return sessionmemory.ForgetOutcome{}, sessionmemory.PermanentError(sessionmemory.CodeConflict, "scope version changed", nil)
	}
	if err := requireActiveSessionMemorySource(snapshot.Sources, request.Source); err != nil {
		return sessionmemory.ForgetOutcome{}, err
	}
	actual, err := dependentSessionMemoryRevisions(snapshot, request.Source)
	if err != nil {
		return sessionmemory.ForgetOutcome{}, err
	}
	if !sameSessionMemoryRevisionSet(actual, request.ExpectedRevisions) {
		return sessionmemory.ForgetOutcome{}, sessionmemory.PermanentError(sessionmemory.CodeConflict, "source forget cascade changed", nil)
	}
	for i := range snapshot.Sources {
		if snapshot.Sources[i].Ref == request.Source {
			snapshot.Sources[i].State = sessionmemory.SourceStateForgotten
			snapshot.Sources[i].Turn = nil
			when := request.ForgottenAt
			snapshot.Sources[i].ForgottenAt = &when
		}
	}
	invalidateSessionMemoryRevisions(&snapshot, actual)
	snapshot.Version++
	if err := snapshot.Validate(sessionmemory.MaxSnapshotItems); err != nil {
		return sessionmemory.ForgetOutcome{}, err
	}
	sortSessionMemoryRevisionRefs(actual)
	outcome := sessionmemory.ForgetOutcome{
		SchemaVersion: sessionmemory.DerivedSchemaVersionV1,
		OperationID:   request.OperationID,
		Kind:          sessionmemory.ForgetKindSource,
		Scope:         request.Scope,
		ScopeVersion:  snapshot.Version,
		Sources:       []sessionmemory.SourceRef{request.Source},
		Revisions:     actual,
	}
	if err := outcome.Validate(); err != nil {
		return sessionmemory.ForgetOutcome{}, err
	}
	if err := persistSessionMemoryScopeTx(ctx, tx, snapshot); err != nil {
		return sessionmemory.ForgetOutcome{}, err
	}
	if err := persistForgetTx(ctx, tx, request.Scope, request.OperationID, sessionmemory.ForgetKindSource, requestJSON, outcome); err != nil {
		return sessionmemory.ForgetOutcome{}, err
	}
	if err := tx.Commit(); err != nil {
		return sessionmemory.ForgetOutcome{}, sessionMemoryStoreError("commit source forget", err)
	}
	return outcome, nil
}

func (s *sqliteSessionMemoryStore) ForgetScope(ctx context.Context, request sessionmemory.ForgetScopeRequest) (sessionmemory.ForgetOutcome, error) {
	if err := sessionMemoryContextError(ctx); err != nil {
		return sessionmemory.ForgetOutcome{}, err
	}
	if err := request.Validate(); err != nil {
		return sessionmemory.ForgetOutcome{}, err
	}
	requestJSON, err := json.Marshal(request)
	if err != nil {
		return sessionmemory.ForgetOutcome{}, sessionmemory.PermanentError(sessionmemory.CodeInvalidDerived, "encode scope forget request", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return sessionmemory.ForgetOutcome{}, sessionMemoryStoreError("begin scope forget", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := ensureSessionMemoryScopeTx(ctx, tx, request.Scope); err != nil {
		return sessionmemory.ForgetOutcome{}, err
	}
	prior, found, err := loadForgetOutcomeTx(ctx, tx, request.Scope, request.OperationID, requestJSON, sessionmemory.ForgetKindScope)
	if err != nil {
		return sessionmemory.ForgetOutcome{}, err
	}
	if found {
		if err := tx.Commit(); err != nil {
			return sessionmemory.ForgetOutcome{}, sessionMemoryStoreError("commit idempotent scope forget", err)
		}
		return prior, nil
	}
	snapshot, err := loadSessionMemoryScopeTx(ctx, tx, request.Scope)
	if err != nil {
		return sessionmemory.ForgetOutcome{}, err
	}
	if snapshot.Version != request.ExpectedScopeVersion {
		return sessionmemory.ForgetOutcome{}, sessionmemory.PermanentError(sessionmemory.CodeConflict, "scope version changed", nil)
	}
	actualSources := activeSessionMemorySources(snapshot.Sources)
	actualRevisions := readableSessionMemoryRevisions(snapshot)
	if !sameSessionMemorySourceSet(actualSources, request.ExpectedSources) || !sameSessionMemoryRevisionSet(actualRevisions, request.ExpectedRevisions) {
		return sessionmemory.ForgetOutcome{}, sessionmemory.PermanentError(sessionmemory.CodeConflict, "scope forget cascade changed", nil)
	}
	active := make(map[sessionmemory.SourceRef]struct{}, len(actualSources))
	for _, source := range actualSources {
		active[source] = struct{}{}
	}
	for i := range snapshot.Sources {
		if _, ok := active[snapshot.Sources[i].Ref]; ok {
			snapshot.Sources[i].State = sessionmemory.SourceStateForgotten
			snapshot.Sources[i].Turn = nil
			when := request.ForgottenAt
			snapshot.Sources[i].ForgottenAt = &when
		}
	}
	invalidateSessionMemoryRevisions(&snapshot, actualRevisions)
	snapshot.Version++
	if err := snapshot.Validate(sessionmemory.MaxSnapshotItems); err != nil {
		return sessionmemory.ForgetOutcome{}, err
	}
	sortSessionMemorySourceRefs(actualSources)
	sortSessionMemoryRevisionRefs(actualRevisions)
	outcome := sessionmemory.ForgetOutcome{
		SchemaVersion: sessionmemory.DerivedSchemaVersionV1,
		OperationID:   request.OperationID,
		Kind:          sessionmemory.ForgetKindScope,
		Scope:         request.Scope,
		ScopeVersion:  snapshot.Version,
		Sources:       actualSources,
		Revisions:     actualRevisions,
	}
	if err := outcome.Validate(); err != nil {
		return sessionmemory.ForgetOutcome{}, err
	}
	if err := persistSessionMemoryScopeTx(ctx, tx, snapshot); err != nil {
		return sessionmemory.ForgetOutcome{}, err
	}
	if err := persistForgetTx(ctx, tx, request.Scope, request.OperationID, sessionmemory.ForgetKindScope, requestJSON, outcome); err != nil {
		return sessionmemory.ForgetOutcome{}, err
	}
	if err := tx.Commit(); err != nil {
		return sessionmemory.ForgetOutcome{}, sessionMemoryStoreError("commit scope forget", err)
	}
	return outcome, nil
}

func (s *sqliteSessionMemoryStore) Search(ctx context.Context, request sessionmemory.DerivedSearchRequest) ([]sessionmemory.SearchHit, error) {
	if err := sessionMemoryContextError(ctx); err != nil {
		return nil, err
	}
	normalized, err := sessionmemory.NormalizeDerivedSearchRequest(request)
	if err != nil {
		return nil, err
	}
	snapshot, err := s.loadScope(ctx, normalized.Scope)
	if err != nil {
		return nil, err
	}
	query := strings.ToLower(normalized.Query)
	hits := make([]sessionmemory.SearchHit, 0)
	for i := range snapshot.Atoms {
		atom := snapshot.Atoms[i]
		if atom.Meta.State != sessionmemory.RevisionStateActive || (normalized.Kind != nil && *normalized.Kind != sessionmemory.DerivedKindAtom) || (normalized.Category != nil && *normalized.Category != atom.Category) || !strings.Contains(strings.ToLower(atom.Text), query) {
			continue
		}
		score := lexicalSessionMemoryScore(atom.Text, query)
		hits = append(hits, sessionmemory.SearchHit{Atom: &atom, Score: &score})
	}
	if normalized.Category == nil {
		for i := range snapshot.Scenarios {
			scenario := snapshot.Scenarios[i]
			if scenario.Meta.State != sessionmemory.RevisionStateActive || (normalized.Kind != nil && *normalized.Kind != sessionmemory.DerivedKindScenario) || !strings.Contains(strings.ToLower(scenario.Title+" "+scenario.Summary), query) {
				continue
			}
			score := lexicalSessionMemoryScore(scenario.Title+" "+scenario.Summary, query)
			hits = append(hits, sessionmemory.SearchHit{Scenario: &scenario, Score: &score})
		}
		for i := range snapshot.Profiles {
			profile := snapshot.Profiles[i]
			if profile.Meta.State != sessionmemory.RevisionStateActive || (normalized.Kind != nil && *normalized.Kind != sessionmemory.DerivedKindProfile) || !strings.Contains(strings.ToLower(profile.Summary), query) {
				continue
			}
			score := lexicalSessionMemoryScore(profile.Summary, query)
			hits = append(hits, sessionmemory.SearchHit{Profile: &profile, Score: &score})
		}
	}
	sort.Slice(hits, func(i, j int) bool {
		left, right := sessionMemoryHitMeta(hits[i]), sessionMemoryHitMeta(hits[j])
		if leftScore, rightScore := hits[i].Score, hits[j].Score; leftScore != nil && rightScore != nil && *leftScore != *rightScore {
			return *leftScore > *rightScore
		}
		if !left.CreatedAt.Equal(right.CreatedAt) {
			return left.CreatedAt.After(right.CreatedAt)
		}
		return left.RevisionID < right.RevisionID
	})
	if len(hits) > normalized.Limit {
		hits = hits[:normalized.Limit]
	}
	return cloneSessionMemoryJSON(hits)
}

func (s *sqliteSessionMemoryStore) Trace(ctx context.Context, request sessionmemory.TraceRequest) (sessionmemory.TraceGraph, error) {
	if err := sessionMemoryContextError(ctx); err != nil {
		return sessionmemory.TraceGraph{}, err
	}
	normalized, err := sessionmemory.NormalizeTraceRequest(request, sessionmemory.MaxTraceNodes)
	if err != nil {
		return sessionmemory.TraceGraph{}, err
	}
	snapshot, err := s.loadScope(ctx, normalized.Scope)
	if err != nil {
		return sessionmemory.TraceGraph{}, err
	}
	hits := sessionMemorySnapshotHits(snapshot)
	byRevision := make(map[sessionmemory.RevisionRef]sessionmemory.SearchHit, len(hits))
	for _, hit := range hits {
		byRevision[sessionMemoryHitRef(hit)] = hit
	}
	sources := make(map[sessionmemory.SourceRef]sessionmemory.SourceRecord, len(snapshot.Sources))
	for _, source := range snapshot.Sources {
		sources[source.Ref] = source
	}
	visitedRevisions := make(map[sessionmemory.RevisionRef]sessionmemory.SearchHit)
	visitedSources := make(map[sessionmemory.SourceRef]sessionmemory.SourceRecord)
	var visit func(sessionmemory.RevisionRef) error
	visit = func(ref sessionmemory.RevisionRef) error {
		if _, ok := visitedRevisions[ref]; ok {
			return nil
		}
		hit, ok := byRevision[ref]
		if !ok {
			return sessionmemory.PermanentError(sessionmemory.CodeNotFound, "trace revision does not exist", nil)
		}
		meta := sessionMemoryHitMeta(hit)
		if meta.State == sessionmemory.RevisionStateInvalidated {
			return sessionmemory.PermanentError(sessionmemory.CodeForgotten, "trace revision was invalidated", nil)
		}
		visitedRevisions[ref] = hit
		if len(visitedRevisions)+len(visitedSources) > normalized.MaxNodes {
			return sessionmemory.PermanentError(sessionmemory.CodeLimitExceeded, "trace exceeds the node limit", nil)
		}
		for _, sourceRef := range meta.Provenance.RawSources {
			source, ok := sources[sourceRef]
			if !ok {
				return sessionmemory.PermanentError(sessionmemory.CodeNotFound, "trace raw source does not exist", nil)
			}
			if source.State == sessionmemory.SourceStateForgotten {
				return sessionmemory.PermanentError(sessionmemory.CodeForgotten, "trace raw source was forgotten", nil)
			}
			visitedSources[sourceRef] = source
		}
		for _, parent := range meta.Provenance.ParentRevisions {
			if err := visit(parent); err != nil {
				return err
			}
		}
		return nil
	}
	if err := visit(normalized.Root); err != nil {
		return sessionmemory.TraceGraph{}, err
	}
	graph := sessionmemory.TraceGraph{SchemaVersion: sessionmemory.DerivedSchemaVersionV1, Scope: normalized.Scope, Root: normalized.Root}
	for _, hit := range visitedRevisions {
		graph.Revisions = append(graph.Revisions, hit)
	}
	for _, source := range visitedSources {
		graph.Sources = append(graph.Sources, source)
	}
	sort.Slice(graph.Revisions, func(i, j int) bool {
		return sessionMemoryHitMeta(graph.Revisions[i]).RevisionID < sessionMemoryHitMeta(graph.Revisions[j]).RevisionID
	})
	sort.Slice(graph.Sources, func(i, j int) bool { return graph.Sources[i].Ref.ExportID < graph.Sources[j].Ref.ExportID })
	return cloneSessionMemoryJSON(graph)
}

func sessionMemoryContextError(ctx context.Context) error {
	if ctx == nil {
		return sessionmemory.PermanentError(sessionmemory.CodeInvalidDerived, "context is required", nil)
	}
	if err := ctx.Err(); err != nil {
		return sessionmemory.RetryableError(sessionmemory.CodeTimeout, "session-memory operation canceled", err)
	}
	return nil
}

func sessionMemoryStoreError(operation string, cause ...error) error {
	var err error
	if len(cause) > 0 {
		err = cause[0]
	}
	return sessionmemory.RetryableError(sessionmemory.CodeStoreFailure, operation, err)
}

func emptySessionMemorySnapshot(scope sessionmemory.Scope) sessionmemory.ScopeSnapshot {
	return sessionmemory.ScopeSnapshot{SchemaVersion: sessionmemory.DerivedSchemaVersionV1, Scope: scope}
}

func decodeSessionMemorySnapshot(raw string, scope sessionmemory.Scope) (sessionmemory.ScopeSnapshot, error) {
	if strings.TrimSpace(raw) == "" {
		return emptySessionMemorySnapshot(scope), nil
	}
	var snapshot sessionmemory.ScopeSnapshot
	if err := json.Unmarshal([]byte(raw), &snapshot); err != nil {
		return sessionmemory.ScopeSnapshot{}, sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "decode session-memory snapshot", err)
	}
	if snapshot.Scope != scope {
		return sessionmemory.ScopeSnapshot{}, sessionmemory.PermanentError(sessionmemory.CodeScopeViolation, "stored snapshot scope does not match locator", nil)
	}
	if err := snapshot.Validate(sessionmemory.MaxSnapshotItems); err != nil {
		return sessionmemory.ScopeSnapshot{}, err
	}
	return snapshot, nil
}

func ensureSessionMemoryScopeTx(ctx context.Context, tx *sql.Tx, scope sessionmemory.Scope) error {
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO session_memory_scopes(scope_key, scope_kind, version, snapshot_json, updated_at)
		VALUES (?, ?, 0, ?, ?)
		ON CONFLICT(scope_key) DO NOTHING`, scope.Key, scope.Kind, `{"schema_version":"session-memory-derived/v1","scope":{"key":"`+escapeJSONString(scope.Key)+`","kind":"`+string(scope.Kind)+`"}}`, utcNow()); err != nil {
		return sessionMemoryStoreError("ensure session-memory scope")
	}
	var kind string
	if err := tx.QueryRowContext(ctx, `SELECT scope_kind FROM session_memory_scopes WHERE scope_key = ?`, scope.Key).Scan(&kind); err != nil {
		return sessionMemoryStoreError("read session-memory scope", err)
	}
	if kind != string(scope.Kind) {
		return sessionmemory.PermanentError(sessionmemory.CodeScopeViolation, "scope kind does not match locator", nil)
	}
	return nil
}

func loadSessionMemoryScopeTx(ctx context.Context, tx *sql.Tx, scope sessionmemory.Scope) (sessionmemory.ScopeSnapshot, error) {
	var version uint64
	var raw string
	if err := tx.QueryRowContext(ctx, `SELECT version, snapshot_json FROM session_memory_scopes WHERE scope_key = ?`, scope.Key).Scan(&version, &raw); err != nil {
		return sessionmemory.ScopeSnapshot{}, sessionMemoryStoreError("load transactional session-memory scope", err)
	}
	snapshot, err := decodeSessionMemorySnapshot(raw, scope)
	if err != nil {
		return sessionmemory.ScopeSnapshot{}, err
	}
	if snapshot.Version != version {
		return sessionmemory.ScopeSnapshot{}, sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "transactional scope version does not match snapshot", nil)
	}
	return snapshot, nil
}

func persistSessionMemoryScopeTx(ctx context.Context, tx *sql.Tx, snapshot sessionmemory.ScopeSnapshot) error {
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return sessionmemory.PermanentError(sessionmemory.CodeInvalidDerived, "encode session-memory snapshot", err)
	}
	result, err := tx.ExecContext(ctx, `UPDATE session_memory_scopes SET scope_kind = ?, version = ?, snapshot_json = ?, updated_at = ? WHERE scope_key = ?`, snapshot.Scope.Kind, snapshot.Version, string(raw), utcNow(), snapshot.Scope.Key)
	if err != nil {
		return sessionMemoryStoreError("persist session-memory snapshot")
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "session-memory scope disappeared during commit", nil)
	}
	return replaceSessionMemoryProjectionTx(ctx, tx, snapshot)
}

func replaceSessionMemoryProjectionTx(ctx context.Context, tx *sql.Tx, snapshot sessionmemory.ScopeSnapshot) error {
	for _, table := range []string{"session_memory_provenance", "session_memory_revisions", "session_memory_sources"} {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE scope_key = ?`, snapshot.Scope.Key); err != nil {
			return sessionMemoryStoreError("replace session-memory projection")
		}
	}
	for _, source := range snapshot.Sources {
		turnJSON := ""
		if source.Turn != nil {
			encoded, err := json.Marshal(source.Turn)
			if err != nil {
				return sessionmemory.PermanentError(sessionmemory.CodeInvalidDerived, "encode session-memory source", err)
			}
			turnJSON = string(encoded)
		}
		forgottenAt := ""
		if source.ForgottenAt != nil {
			forgottenAt = source.ForgottenAt.UTC().Format(time.RFC3339Nano)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO session_memory_sources(scope_key, export_id, session_id, source_turn_id, state, turn_json, forgotten_at) VALUES (?, ?, ?, ?, ?, ?, ?)`, snapshot.Scope.Key, source.Ref.ExportID, source.Ref.SessionID, source.Ref.SourceTurnID, source.State, turnJSON, forgottenAt); err != nil {
			return sessionMemoryStoreError("persist session-memory source")
		}
	}
	for _, revision := range sessionMemorySnapshotHits(snapshot) {
		meta := sessionMemoryHitMeta(revision)
		payload, err := json.Marshal(revision)
		if err != nil {
			return sessionmemory.PermanentError(sessionmemory.CodeInvalidDerived, "encode session-memory revision", err)
		}
		text := sessionMemoryHitText(revision)
		if _, err := tx.ExecContext(ctx, `INSERT INTO session_memory_revisions(scope_key, item_id, revision_id, kind, state, revision, operation_id, created_at, normalized_text, payload_json) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, snapshot.Scope.Key, meta.ItemID, meta.RevisionID, meta.Kind, meta.State, meta.Revision, meta.OperationID, meta.CreatedAt.UTC().Format(time.RFC3339Nano), text, string(payload)); err != nil {
			return sessionMemoryStoreError("persist session-memory revision")
		}
		for _, source := range meta.Provenance.RawSources {
			if _, err := tx.ExecContext(ctx, `INSERT INTO session_memory_provenance(scope_key, revision_id, source_export_id, source_session_id, source_turn_id) VALUES (?, ?, ?, ?, ?)`, snapshot.Scope.Key, meta.RevisionID, source.ExportID, source.SessionID, source.SourceTurnID); err != nil {
				return sessionMemoryStoreError("persist session-memory source provenance")
			}
		}
		for _, parent := range meta.Provenance.ParentRevisions {
			if _, err := tx.ExecContext(ctx, `INSERT INTO session_memory_provenance(scope_key, revision_id, parent_item_id, parent_revision_id) VALUES (?, ?, ?, ?)`, snapshot.Scope.Key, meta.RevisionID, parent.ItemID, parent.RevisionID); err != nil {
				return sessionMemoryStoreError("persist session-memory parent provenance")
			}
		}
	}
	return nil
}

func persistForgetTx(ctx context.Context, tx *sql.Tx, scope sessionmemory.Scope, operationID string, kind sessionmemory.ForgetKind, requestJSON []byte, outcome sessionmemory.ForgetOutcome) error {
	outcomeJSON, err := json.Marshal(outcome)
	if err != nil {
		return sessionmemory.PermanentError(sessionmemory.CodeInvalidDerived, "encode forget outcome", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO session_memory_forgets(scope_key, operation_id, kind, request_json, outcome_json, committed_at) VALUES (?, ?, ?, ?, ?, ?)`, scope.Key, operationID, kind, string(requestJSON), string(outcomeJSON), utcNow()); err != nil {
		return sessionMemoryStoreError("persist forget outcome")
	}
	return nil
}

func loadForgetOutcomeTx(ctx context.Context, tx *sql.Tx, scope sessionmemory.Scope, operationID string, requestJSON []byte, kind sessionmemory.ForgetKind) (sessionmemory.ForgetOutcome, bool, error) {
	var storedRequest, outcomeJSON, storedKind string
	err := tx.QueryRowContext(ctx, `SELECT request_json, outcome_json, kind FROM session_memory_forgets WHERE scope_key = ? AND operation_id = ?`, scope.Key, operationID).Scan(&storedRequest, &outcomeJSON, &storedKind)
	if err != nil {
		if err == sql.ErrNoRows {
			return sessionmemory.ForgetOutcome{}, false, nil
		}
		return sessionmemory.ForgetOutcome{}, false, sessionMemoryStoreError("lookup forget outcome")
	}
	if storedKind != string(kind) || !bytes.Equal([]byte(storedRequest), requestJSON) {
		return sessionmemory.ForgetOutcome{}, false, sessionmemory.PermanentError(sessionmemory.CodeConflict, "forget identity was reused with different content", nil)
	}
	var outcome sessionmemory.ForgetOutcome
	if err := json.Unmarshal([]byte(outcomeJSON), &outcome); err != nil {
		return sessionmemory.ForgetOutcome{}, false, sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "decode forget outcome", err)
	}
	if err := outcome.Validate(); err != nil {
		return sessionmemory.ForgetOutcome{}, false, err
	}
	return outcome, true, nil
}

func utcNow() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func escapeJSONString(value string) string {
	encoded, _ := json.Marshal(value)
	if len(encoded) >= 2 {
		return string(encoded[1 : len(encoded)-1])
	}
	return value
}

func cloneSessionMemoryJSON[T any](value T) (T, error) {
	var clone T
	raw, err := json.Marshal(value)
	if err != nil {
		return clone, sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "clone session-memory value", err)
	}
	if err := json.Unmarshal(raw, &clone); err != nil {
		return clone, sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "clone session-memory value", err)
	}
	return clone, nil
}

func validateSessionMemoryCommit(snapshot sessionmemory.ScopeSnapshot, request sessionmemory.CommitRequest) error {
	if len(snapshot.Sources)+len(snapshot.Atoms)+len(snapshot.Scenarios)+len(snapshot.Profiles)+len(request.Sources)+len(request.Atoms)+len(request.Scenarios)+len(request.Profiles) > sessionmemory.MaxSnapshotItems {
		return sessionmemory.PermanentError(sessionmemory.CodeLimitExceeded, "scope exceeds the item limit", nil)
	}
	existingSources := make(map[sessionmemory.SourceRef]sessionmemory.SourceState, len(snapshot.Sources)+len(request.Sources))
	exportIDs := make(map[string]struct{}, len(snapshot.Sources)+len(request.Sources))
	for _, source := range snapshot.Sources {
		existingSources[source.Ref] = source.State
		exportIDs[source.Ref.ExportID] = struct{}{}
	}
	for _, source := range request.Sources {
		if _, exists := exportIDs[source.Ref.ExportID]; exists {
			return sessionmemory.PermanentError(sessionmemory.CodeConflict, "raw source identity already exists", nil)
		}
		exportIDs[source.Ref.ExportID] = struct{}{}
		existingSources[source.Ref] = source.State
	}
	existing := sessionMemorySnapshotMetas(snapshot)
	created := sessionMemoryRequestMetas(request)
	for ref := range created {
		if _, exists := existing[ref]; exists {
			return sessionmemory.PermanentError(sessionmemory.CodeConflict, "derived revision identity already exists", nil)
		}
	}
	createdItems := make(map[string]struct{}, len(created))
	for _, meta := range created {
		if _, exists := createdItems[meta.ItemID]; exists {
			return sessionmemory.PermanentError(sessionmemory.CodeConflict, "commit creates more than one revision of a logical item", nil)
		}
		createdItems[meta.ItemID] = struct{}{}
	}
	all := make(map[sessionmemory.RevisionRef]sessionmemory.RevisionMeta, len(existing)+len(created))
	for ref, meta := range existing {
		all[ref] = meta
	}
	for ref, meta := range created {
		all[ref] = meta
	}
	for _, transition := range request.Transitions {
		meta, ok := existing[transition.Ref]
		if !ok || meta.State != transition.From {
			return sessionmemory.PermanentError(sessionmemory.CodeConflict, "revision transition precondition changed", nil)
		}
	}
	for ref, meta := range created {
		next, ok := sessionMemoryNextRevision(existing, ref.ItemID)
		if !ok || meta.Revision != next {
			return sessionmemory.PermanentError(sessionmemory.CodeConflict, "derived revision number is not the next revision", nil)
		}
		for _, source := range meta.Provenance.RawSources {
			if existingSources[source] != sessionmemory.SourceStateActive {
				return sessionmemory.PermanentError(sessionmemory.CodeConflict, "derived revision references an inactive raw source", nil)
			}
		}
		for _, parent := range meta.Provenance.ParentRevisions {
			parentMeta, ok := all[parent]
			if !ok || parentMeta.State != sessionmemory.RevisionStateActive {
				return sessionmemory.PermanentError(sessionmemory.CodeConflict, "derived revision references an inactive parent", nil)
			}
		}
	}
	return validateSessionMemoryAcyclic(all)
}

func applySessionMemoryCommit(snapshot *sessionmemory.ScopeSnapshot, request sessionmemory.CommitRequest) error {
	for _, transition := range request.Transitions {
		if !applySessionMemoryTransition(snapshot, transition) {
			return sessionmemory.PermanentError(sessionmemory.CodeConflict, "revision transition precondition changed", nil)
		}
	}
	snapshot.Sources = append(snapshot.Sources, request.Sources...)
	snapshot.Atoms = append(snapshot.Atoms, request.Atoms...)
	snapshot.Scenarios = append(snapshot.Scenarios, request.Scenarios...)
	snapshot.Profiles = append(snapshot.Profiles, request.Profiles...)
	return nil
}

func applySessionMemoryTransition(snapshot *sessionmemory.ScopeSnapshot, transition sessionmemory.RevisionTransition) bool {
	for i := range snapshot.Atoms {
		if sessionMemoryRevisionRef(snapshot.Atoms[i].Meta) == transition.Ref && snapshot.Atoms[i].Meta.State == transition.From {
			snapshot.Atoms[i].Meta.State = transition.To
			return true
		}
	}
	for i := range snapshot.Scenarios {
		if sessionMemoryRevisionRef(snapshot.Scenarios[i].Meta) == transition.Ref && snapshot.Scenarios[i].Meta.State == transition.From {
			snapshot.Scenarios[i].Meta.State = transition.To
			return true
		}
	}
	for i := range snapshot.Profiles {
		if sessionMemoryRevisionRef(snapshot.Profiles[i].Meta) == transition.Ref && snapshot.Profiles[i].Meta.State == transition.From {
			snapshot.Profiles[i].Meta.State = transition.To
			return true
		}
	}
	return false
}

func sessionMemoryCommitOutcome(request sessionmemory.CommitRequest, version uint64) sessionmemory.OperationOutcome {
	refs := make([]sessionmemory.RevisionRef, 0, len(request.Atoms)+len(request.Scenarios)+len(request.Profiles))
	for _, atom := range request.Atoms {
		refs = append(refs, sessionMemoryRevisionRef(atom.Meta))
	}
	for _, scenario := range request.Scenarios {
		refs = append(refs, sessionMemoryRevisionRef(scenario.Meta))
	}
	for _, profile := range request.Profiles {
		refs = append(refs, sessionMemoryRevisionRef(profile.Meta))
	}
	return sessionmemory.OperationOutcome{SchemaVersion: sessionmemory.DerivedSchemaVersionV1, OperationID: request.OperationID, Stage: request.Stage, Scope: request.Scope, ScopeVersion: version, Revisions: refs}
}

func sessionMemorySnapshotMetas(snapshot sessionmemory.ScopeSnapshot) map[sessionmemory.RevisionRef]sessionmemory.RevisionMeta {
	metas := make(map[sessionmemory.RevisionRef]sessionmemory.RevisionMeta, len(snapshot.Atoms)+len(snapshot.Scenarios)+len(snapshot.Profiles))
	for _, atom := range snapshot.Atoms {
		metas[sessionMemoryRevisionRef(atom.Meta)] = atom.Meta
	}
	for _, scenario := range snapshot.Scenarios {
		metas[sessionMemoryRevisionRef(scenario.Meta)] = scenario.Meta
	}
	for _, profile := range snapshot.Profiles {
		metas[sessionMemoryRevisionRef(profile.Meta)] = profile.Meta
	}
	return metas
}

func sessionMemoryRequestMetas(request sessionmemory.CommitRequest) map[sessionmemory.RevisionRef]sessionmemory.RevisionMeta {
	metas := make(map[sessionmemory.RevisionRef]sessionmemory.RevisionMeta, len(request.Atoms)+len(request.Scenarios)+len(request.Profiles))
	for _, atom := range request.Atoms {
		metas[sessionMemoryRevisionRef(atom.Meta)] = atom.Meta
	}
	for _, scenario := range request.Scenarios {
		metas[sessionMemoryRevisionRef(scenario.Meta)] = scenario.Meta
	}
	for _, profile := range request.Profiles {
		metas[sessionMemoryRevisionRef(profile.Meta)] = profile.Meta
	}
	return metas
}

func sessionMemoryNextRevision(existing map[sessionmemory.RevisionRef]sessionmemory.RevisionMeta, itemID string) (uint64, bool) {
	var maximum uint64
	for _, meta := range existing {
		if meta.ItemID == itemID && meta.Revision > maximum {
			maximum = meta.Revision
		}
	}
	if maximum == math.MaxUint64 {
		return 0, false
	}
	return maximum + 1, true
}

func validateSessionMemoryAcyclic(metas map[sessionmemory.RevisionRef]sessionmemory.RevisionMeta) error {
	colors := make(map[sessionmemory.RevisionRef]uint8, len(metas))
	var visit func(sessionmemory.RevisionRef) error
	visit = func(ref sessionmemory.RevisionRef) error {
		switch colors[ref] {
		case 1:
			return sessionmemory.PermanentError(sessionmemory.CodeInvalidDerived, "provenance cycle", nil)
		case 2:
			return nil
		}
		meta, ok := metas[ref]
		if !ok {
			return sessionmemory.PermanentError(sessionmemory.CodeInvalidDerived, "missing provenance parent", nil)
		}
		colors[ref] = 1
		for _, parent := range meta.Provenance.ParentRevisions {
			if err := visit(parent); err != nil {
				return err
			}
		}
		colors[ref] = 2
		return nil
	}
	for ref := range metas {
		if err := visit(ref); err != nil {
			return err
		}
	}
	return nil
}

func requireActiveSessionMemorySource(sources []sessionmemory.SourceRecord, want sessionmemory.SourceRef) error {
	for _, source := range sources {
		if source.Ref.ExportID != want.ExportID {
			continue
		}
		if source.Ref != want {
			return sessionmemory.PermanentError(sessionmemory.CodeNotFound, "raw source does not exist", nil)
		}
		if source.State == sessionmemory.SourceStateForgotten {
			return sessionmemory.PermanentError(sessionmemory.CodeForgotten, "raw source was already forgotten", nil)
		}
		return nil
	}
	return sessionmemory.PermanentError(sessionmemory.CodeNotFound, "raw source does not exist", nil)
}

func dependentSessionMemoryRevisions(snapshot sessionmemory.ScopeSnapshot, source sessionmemory.SourceRef) ([]sessionmemory.RevisionRef, error) {
	metas := sessionMemorySnapshotMetas(snapshot)
	reverse := make(map[sessionmemory.RevisionRef][]sessionmemory.RevisionRef, len(metas))
	queue := make([]sessionmemory.RevisionRef, 0)
	for ref, meta := range metas {
		for _, raw := range meta.Provenance.RawSources {
			if raw == source {
				queue = append(queue, ref)
			}
		}
		for _, parent := range meta.Provenance.ParentRevisions {
			if _, ok := metas[parent]; !ok {
				return nil, sessionmemory.PermanentError(sessionmemory.CodeInvalidDerived, "missing provenance parent", nil)
			}
			reverse[parent] = append(reverse[parent], ref)
		}
	}
	seen := make(map[sessionmemory.RevisionRef]struct{}, len(queue))
	for len(queue) > 0 {
		ref := queue[0]
		queue = queue[1:]
		if _, ok := seen[ref]; ok {
			continue
		}
		seen[ref] = struct{}{}
		queue = append(queue, reverse[ref]...)
	}
	refs := make([]sessionmemory.RevisionRef, 0, len(seen))
	for ref := range seen {
		state := metas[ref].State
		if state == sessionmemory.RevisionStateActive || state == sessionmemory.RevisionStateSuperseded {
			refs = append(refs, ref)
		}
	}
	return refs, nil
}

func activeSessionMemorySources(sources []sessionmemory.SourceRecord) []sessionmemory.SourceRef {
	refs := make([]sessionmemory.SourceRef, 0, len(sources))
	for _, source := range sources {
		if source.State == sessionmemory.SourceStateActive {
			refs = append(refs, source.Ref)
		}
	}
	return refs
}

func readableSessionMemoryRevisions(snapshot sessionmemory.ScopeSnapshot) []sessionmemory.RevisionRef {
	refs := make([]sessionmemory.RevisionRef, 0, len(snapshot.Atoms)+len(snapshot.Scenarios)+len(snapshot.Profiles))
	for ref, meta := range sessionMemorySnapshotMetas(snapshot) {
		if meta.State == sessionmemory.RevisionStateActive || meta.State == sessionmemory.RevisionStateSuperseded {
			refs = append(refs, ref)
		}
	}
	return refs
}

func invalidateSessionMemoryRevisions(snapshot *sessionmemory.ScopeSnapshot, refs []sessionmemory.RevisionRef) {
	set := make(map[sessionmemory.RevisionRef]struct{}, len(refs))
	for _, ref := range refs {
		set[ref] = struct{}{}
	}
	for i := range snapshot.Atoms {
		if _, ok := set[sessionMemoryRevisionRef(snapshot.Atoms[i].Meta)]; ok {
			snapshot.Atoms[i].Meta.State = sessionmemory.RevisionStateInvalidated
		}
	}
	for i := range snapshot.Scenarios {
		if _, ok := set[sessionMemoryRevisionRef(snapshot.Scenarios[i].Meta)]; ok {
			snapshot.Scenarios[i].Meta.State = sessionmemory.RevisionStateInvalidated
		}
	}
	for i := range snapshot.Profiles {
		if _, ok := set[sessionMemoryRevisionRef(snapshot.Profiles[i].Meta)]; ok {
			snapshot.Profiles[i].Meta.State = sessionmemory.RevisionStateInvalidated
		}
	}
}

func sameSessionMemorySourceSet(left, right []sessionmemory.SourceRef) bool {
	if len(left) != len(right) {
		return false
	}
	set := make(map[sessionmemory.SourceRef]struct{}, len(left))
	for _, ref := range left {
		set[ref] = struct{}{}
	}
	for _, ref := range right {
		if _, ok := set[ref]; !ok {
			return false
		}
	}
	return true
}

func sameSessionMemoryRevisionSet(left, right []sessionmemory.RevisionRef) bool {
	if len(left) != len(right) {
		return false
	}
	set := make(map[sessionmemory.RevisionRef]struct{}, len(left))
	for _, ref := range left {
		set[ref] = struct{}{}
	}
	for _, ref := range right {
		if _, ok := set[ref]; !ok {
			return false
		}
	}
	return true
}

func sortSessionMemorySourceRefs(refs []sessionmemory.SourceRef) {
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].ExportID != refs[j].ExportID {
			return refs[i].ExportID < refs[j].ExportID
		}
		return refs[i].SourceTurnID < refs[j].SourceTurnID
	})
}

func sortSessionMemoryRevisionRefs(refs []sessionmemory.RevisionRef) {
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].ItemID != refs[j].ItemID {
			return refs[i].ItemID < refs[j].ItemID
		}
		return refs[i].RevisionID < refs[j].RevisionID
	})
}

func sessionMemoryRevisionRef(meta sessionmemory.RevisionMeta) sessionmemory.RevisionRef {
	return sessionmemory.RevisionRef{ItemID: meta.ItemID, RevisionID: meta.RevisionID}
}

func sessionMemorySnapshotHits(snapshot sessionmemory.ScopeSnapshot) []sessionmemory.SearchHit {
	hits := make([]sessionmemory.SearchHit, 0, len(snapshot.Atoms)+len(snapshot.Scenarios)+len(snapshot.Profiles))
	for i := range snapshot.Atoms {
		hit := snapshot.Atoms[i]
		hits = append(hits, sessionmemory.SearchHit{Atom: &hit})
	}
	for i := range snapshot.Scenarios {
		hit := snapshot.Scenarios[i]
		hits = append(hits, sessionmemory.SearchHit{Scenario: &hit})
	}
	for i := range snapshot.Profiles {
		hit := snapshot.Profiles[i]
		hits = append(hits, sessionmemory.SearchHit{Profile: &hit})
	}
	return hits
}

func sessionMemoryHitMeta(hit sessionmemory.SearchHit) sessionmemory.RevisionMeta {
	if hit.Atom != nil {
		return hit.Atom.Meta
	}
	if hit.Scenario != nil {
		return hit.Scenario.Meta
	}
	return hit.Profile.Meta
}

func sessionMemoryHitRef(hit sessionmemory.SearchHit) sessionmemory.RevisionRef {
	return sessionMemoryRevisionRef(sessionMemoryHitMeta(hit))
}

func sessionMemoryHitText(hit sessionmemory.SearchHit) string {
	if hit.Atom != nil {
		return hit.Atom.Text
	}
	if hit.Scenario != nil {
		return hit.Scenario.Title + " " + hit.Scenario.Summary
	}
	return hit.Profile.Summary
}

func lexicalSessionMemoryScore(text, query string) float64 {
	if query == "" {
		return 0
	}
	text = strings.ToLower(text)
	terms := strings.Fields(query)
	matched := 0
	for _, term := range terms {
		if strings.Contains(text, term) {
			matched++
		}
	}
	if len(terms) == 0 {
		return 0
	}
	return float64(matched) / float64(len(terms))
}
