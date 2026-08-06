package sessionmemoryapp

import (
	"context"
	"sync"
	"time"

	"github.com/normahq/balda/sessionmemory"
)

const migrationBatchRecords = 96

// MigrationCoordinator converts one validated v1 exact-scope snapshot in
// bounded source, atom, and operation batches. The legacy Store is retained
// only as this explicit input; normal provider reads never call it.
type MigrationCoordinator struct {
	legacy      sessionmemory.Store
	canonical   sessionmemory.CanonicalStore
	checkpoints sessionmemory.CanonicalMigrationCheckpointStore
	operations  sessionmemory.LegacyOperationSource

	mu    sync.RWMutex
	ready map[sessionmemory.Scope]uint64
}

func NewMigrationCoordinator(legacy sessionmemory.Store, canonical sessionmemory.CanonicalStore, checkpoints sessionmemory.CanonicalMigrationCheckpointStore) (*MigrationCoordinator, error) {
	if legacy == nil || canonical == nil || checkpoints == nil {
		return nil, sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "migration dependencies are required", nil)
	}
	operations, _ := legacy.(sessionmemory.LegacyOperationSource)
	return &MigrationCoordinator{legacy: legacy, canonical: canonical, checkpoints: checkpoints, operations: operations, ready: make(map[sessionmemory.Scope]uint64)}, nil
}

// MigrateScope resumes until the durable checkpoint reaches the snapshot end.
// Every canonical mutation is independently bounded and idempotent; a caller
// may instead invoke Step for a maintenance tick.
func (m *MigrationCoordinator) MigrateScope(ctx context.Context, scope sessionmemory.Scope) error {
	if m == nil || m.legacy == nil || m.canonical == nil {
		return sessionmemory.PermanentError(sessionmemory.CodeDisabled, "migration coordinator is unavailable", nil)
	}
	if ctx == nil {
		return sessionmemory.PermanentError(sessionmemory.CodeInvalidDerived, "migration context is required", nil)
	}
	if err := scope.Validate(); err != nil {
		return err
	}
	snapshot, err := m.legacy.LoadScope(ctx, scope)
	if err != nil {
		return err
	}
	if err := snapshot.Validate(sessionmemory.MaxSnapshotItems); err != nil {
		return err
	}
	if snapshot.Scope != scope {
		return sessionmemory.PermanentError(sessionmemory.CodeScopeViolation, "migration snapshot scope does not match", nil)
	}
	for {
		checkpoint, found, err := m.checkpoints.LoadCanonicalMigrationCheckpoint(ctx, scope, snapshot.Version)
		if err != nil {
			return err
		}
		if !found {
			operationCount, countErr := m.operationCount(ctx, scope)
			if countErr != nil {
				return countErr
			}
			checkpoint = sessionmemory.CanonicalMigrationCheckpoint{SchemaVersion: "session-memory-migration-checkpoint/v1", Scope: scope, SnapshotVersion: snapshot.Version, SourceCount: uint32(len(snapshot.Sources)), AtomCount: uint32(len(snapshot.Atoms)), ScenarioCount: uint32(len(snapshot.Scenarios)), ProfileCount: uint32(len(snapshot.Profiles)), OperationCount: operationCount, Completed: false}
		}
		if err := checkpoint.Validate(); err != nil {
			return err
		}
		if err := validateMigrationCheckpoint(snapshot, checkpoint); err != nil {
			return err
		}
		if operationCount, countErr := m.operationCount(ctx, scope); countErr != nil {
			return countErr
		} else if checkpoint.OperationCount != operationCount {
			return sessionmemory.PermanentError(sessionmemory.CodeConflict, "canonical migration checkpoint operation count does not match the legacy source", nil)
		}
		if checkpoint.Completed {
			if err := m.preserveForgottenState(ctx, snapshot); err != nil {
				return err
			}
			return m.markReady(ctx, scope, snapshot.Version)
		}
		if checkpoint.SourceCount == 0 && checkpoint.AtomCount == 0 && checkpoint.ScenarioCount == 0 && checkpoint.ProfileCount == 0 && checkpoint.OperationCount == 0 {
			checkpoint.Completed = true
			if err := m.checkpoints.SaveCanonicalMigrationCheckpoint(ctx, checkpoint); err != nil {
				return err
			}
			if err := m.preserveForgottenState(ctx, snapshot); err != nil {
				return err
			}
			return m.markReady(ctx, scope, snapshot.Version)
		}
		if err := m.step(ctx, snapshot, checkpoint); err != nil {
			return err
		}
	}
}

// Step performs at most one bounded migration mutation and reports whether the
// durable cursor is complete.
func (m *MigrationCoordinator) Step(ctx context.Context, snapshot sessionmemory.ScopeSnapshot, checkpoint sessionmemory.CanonicalMigrationCheckpoint) (bool, error) {
	if m == nil || m.canonical == nil {
		return false, sessionmemory.PermanentError(sessionmemory.CodeDisabled, "migration coordinator is unavailable", nil)
	}
	if err := snapshot.Validate(sessionmemory.MaxSnapshotItems); err != nil {
		return false, err
	}
	if err := checkpoint.Validate(); err != nil {
		return false, err
	}
	if err := validateMigrationCheckpoint(snapshot, checkpoint); err != nil {
		return false, err
	}
	operationCount, countErr := m.operationCount(ctx, snapshot.Scope)
	if countErr != nil {
		return false, countErr
	}
	if checkpoint.OperationCount != operationCount {
		return false, sessionmemory.PermanentError(sessionmemory.CodeConflict, "canonical migration checkpoint operation count does not match the legacy source", nil)
	}
	if checkpoint.Completed {
		if err := m.preserveForgottenState(ctx, snapshot); err != nil {
			return false, err
		}
		if err := m.markReady(ctx, snapshot.Scope, snapshot.Version); err != nil {
			return false, err
		}
		return true, nil
	}
	if err := m.step(ctx, snapshot, checkpoint); err != nil {
		return false, err
	}
	next, found, err := m.checkpoints.LoadCanonicalMigrationCheckpoint(ctx, snapshot.Scope, snapshot.Version)
	if err != nil {
		return false, err
	}
	if !found {
		return false, sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "migration checkpoint was not persisted", nil)
	}
	if next.Completed {
		if err := m.preserveForgottenState(ctx, snapshot); err != nil {
			return false, err
		}
		if err := m.markReady(ctx, snapshot.Scope, snapshot.Version); err != nil {
			return false, err
		}
	}
	return next.Completed, nil
}

func (m *MigrationCoordinator) step(ctx context.Context, snapshot sessionmemory.ScopeSnapshot, checkpoint sessionmemory.CanonicalMigrationCheckpoint) error {
	config := sessionmemory.CanonicalMigrationConfig{MaxMutationRecords: migrationBatchRecords, SourceOffset: int(checkpoint.NextSourceOffset), AtomOffset: int(checkpoint.NextAtomOffset), ScenarioOffset: int(checkpoint.NextScenarioOffset), ProfileOffset: int(checkpoint.NextProfileOffset), OperationOffset: int(checkpoint.NextOperationOffset), SkipSourceRecords: checkpoint.NextSourceOffset >= checkpoint.SourceCount, SkipAtomRecords: checkpoint.NextAtomOffset >= checkpoint.AtomCount, SkipScenarioRecords: checkpoint.NextScenarioOffset >= checkpoint.ScenarioCount, SkipProfileRecords: checkpoint.NextProfileOffset >= checkpoint.ProfileCount, SkipOperationRecords: checkpoint.NextOperationOffset >= checkpoint.OperationCount}
	switch {
	case !config.SkipSourceRecords:
		config.SourceLimit = migrationBatchRecords
		config.SkipAtomRecords = true
		config.SkipScenarioRecords = true
		config.SkipProfileRecords = true
		config.SkipOperationRecords = true
	case !config.SkipAtomRecords:
		config.AtomLimit = migrationBatchRecords
		config.SkipSourceRecords = true
		config.SkipScenarioRecords = true
		config.SkipProfileRecords = true
		config.SkipOperationRecords = true
	case !config.SkipScenarioRecords:
		config.ScenarioLimit = migrationBatchRecords
		config.SkipSourceRecords = true
		config.SkipAtomRecords = true
		config.SkipProfileRecords = true
		config.SkipOperationRecords = true
	case !config.SkipProfileRecords:
		config.ProfileLimit = migrationBatchRecords
		config.SkipSourceRecords = true
		config.SkipAtomRecords = true
		config.SkipScenarioRecords = true
		config.SkipOperationRecords = true
	case !config.SkipOperationRecords:
		config.OperationLimit = migrationBatchRecords
		config.SkipSourceRecords = true
		config.SkipAtomRecords = true
		config.SkipScenarioRecords = true
		config.SkipProfileRecords = true
	}
	if m.operations != nil {
		operations, err := m.operations.LoadOperationOutcomes(ctx, snapshot.Scope)
		if err != nil {
			return err
		}
		config.LegacyOperations = operations
	}
	_, err := sessionmemory.MigrateV1ScopeSnapshot(ctx, m.canonical, snapshot, config)
	return err
}

func (m *MigrationCoordinator) operationCount(ctx context.Context, scope sessionmemory.Scope) (uint32, error) {
	if m.operations == nil {
		return 0, nil
	}
	operations, err := m.operations.LoadOperationOutcomes(ctx, scope)
	if err != nil {
		return 0, err
	}
	if len(operations) > sessionmemory.MaxSnapshotItems {
		return 0, sessionmemory.PermanentError(sessionmemory.CodeLimitExceeded, "legacy operation migration exceeds the bounded snapshot", nil)
	}
	return uint32(len(operations)), nil
}

func validateMigrationCheckpoint(snapshot sessionmemory.ScopeSnapshot, checkpoint sessionmemory.CanonicalMigrationCheckpoint) error {
	if checkpoint.Scope != snapshot.Scope || checkpoint.SnapshotVersion != snapshot.Version || checkpoint.SourceCount != uint32(len(snapshot.Sources)) || checkpoint.AtomCount != uint32(len(snapshot.Atoms)) || checkpoint.ScenarioCount != uint32(len(snapshot.Scenarios)) || checkpoint.ProfileCount != uint32(len(snapshot.Profiles)) {
		return sessionmemory.PermanentError(sessionmemory.CodeConflict, "canonical migration checkpoint does not match the legacy snapshot", nil)
	}
	return nil
}

// preserveForgottenState translates v1 identity-only tombstones into durable
// canonical denial markers after all records are safely imported. Denial is
// idempotent and deliberately precedes the readiness record, so a crash never
// advertises a scope whose forgotten source could still be recalled.
func (m *MigrationCoordinator) preserveForgottenState(ctx context.Context, snapshot sessionmemory.ScopeSnapshot) error {
	forgotten := make([]sessionmemory.SourceRecord, 0)
	for _, source := range snapshot.Sources {
		if source.State == sessionmemory.SourceStateForgotten {
			forgotten = append(forgotten, source)
		}
	}
	if len(forgotten) == 0 {
		return nil
	}
	denier, denierOK := m.canonical.(sessionmemory.CanonicalSourceForgetStore)
	resolver, resolverOK := m.canonical.(sessionmemory.CanonicalSourceIdentityResolver)
	if !denierOK || !resolverOK {
		return sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "canonical migration cannot preserve forgotten source state", nil)
	}
	for _, source := range forgotten {
		sourceID, err := resolver.CanonicalSourceID(ctx, snapshot.Scope, source.Ref)
		if err != nil {
			return err
		}
		forgottenAt := time.Now().UTC()
		if source.ForgottenAt != nil && !source.ForgottenAt.IsZero() {
			forgottenAt = source.ForgottenAt.UTC()
		}
		if err := denier.DenySource(ctx, snapshot.Scope, sourceID, forgottenAt); err != nil {
			return err
		}
		after := ""
		for {
			revisionIDs, next, err := denier.SourceRevisionBatch(ctx, snapshot.Scope, sourceID, after, 512)
			if err != nil {
				return err
			}
			for _, revisionID := range revisionIDs {
				if err := denier.DenyRevision(ctx, snapshot.Scope, revisionID, forgottenAt); err != nil {
					return err
				}
			}
			if next == "" || next == after {
				break
			}
			after = next
		}
	}
	return nil
}

func (m *MigrationCoordinator) markReady(ctx context.Context, scope sessionmemory.Scope, version uint64) error {
	if durable, ok := m.checkpoints.(sessionmemory.CanonicalMigrationReadinessStore); ok {
		if err := durable.SaveCanonicalMigrationReadiness(ctx, sessionmemory.CanonicalMigrationReadiness{SchemaVersion: sessionmemory.CanonicalMigrationReadinessSchemaVersion, Scope: scope, SnapshotVersion: version, ReadyAt: time.Now().UTC()}); err != nil {
			return err
		}
	}
	m.mu.Lock()
	m.ready[scope] = version
	m.mu.Unlock()
	return nil
}

// IsReady checks durable readiness first, then the in-process fast path.
func (m *MigrationCoordinator) IsReady(ctx context.Context, scope sessionmemory.Scope) (bool, error) {
	if m == nil || m.checkpoints == nil || m.legacy == nil {
		return false, sessionmemory.PermanentError(sessionmemory.CodeDisabled, "migration coordinator is unavailable", nil)
	}
	if err := scope.Validate(); err != nil {
		return false, err
	}
	if durable, ok := m.checkpoints.(sessionmemory.CanonicalMigrationReadinessStore); ok {
		readiness, found, err := durable.LoadCanonicalMigrationReadiness(ctx, scope)
		if err != nil {
			return false, err
		}
		if found {
			if err := readiness.Validate(); err != nil || readiness.Scope != scope {
				return false, sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "canonical migration readiness is invalid", err)
			}
			snapshot, snapshotErr := m.legacy.LoadScope(ctx, scope)
			if snapshotErr != nil {
				return false, snapshotErr
			}
			if snapshotErr := snapshot.Validate(sessionmemory.MaxSnapshotItems); snapshotErr != nil {
				return false, snapshotErr
			}
			return readiness.SnapshotVersion == snapshot.Version, nil
		}
	}
	state, err := m.canonical.LoadScopeState(ctx, scope)
	if err != nil {
		return false, err
	}
	m.mu.RLock()
	_, cached := m.ready[scope]
	m.mu.RUnlock()
	if cached && state.Version > 0 {
		return true, nil
	}
	// A snapshot version is not known from canonical state alone. The durable
	// checkpoint is therefore checked for the latest version only by callers
	// that supply the v1 snapshot; absent that proof, fail closed.
	return false, nil
}
