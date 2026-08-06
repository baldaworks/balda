package state

import (
	"encoding/binary"

	"github.com/normahq/balda/sessionmemory"
)

const (
	badgerSessionMemoryNamespace     byte = 0x53
	badgerSessionMemoryVersion       byte = 1
	badgerRecordScope                     = "scope"
	badgerRecordOperation                 = "operation"
	badgerRecordImportedOperation         = "imported-operation"
	badgerRecordSource                    = "source"
	badgerRecordMessage                   = "message"
	badgerRecordItem                      = "item"
	badgerRecordRevision                  = "revision"
	badgerRecordLifecycle                 = "lifecycle"
	badgerRecordHead                      = "head"
	badgerRecordChange                    = "change"
	badgerRecordDelivery                  = "delivery"
	badgerRecordDeliveryClaim             = "delivery-claim"
	badgerRecordPayload                   = "payload"
	badgerRecordProvenanceForward         = "provenance-forward"
	badgerRecordProvenanceReverse         = "provenance-reverse"
	badgerRecordSourceRevision            = "source-revision"
	badgerRecordDeniedSource              = "denied-source"
	badgerRecordDeniedRevision            = "denied-revision"
	badgerRecordMigrationCheckpoint       = "migration-checkpoint"
	badgerRecordProjectionManifest        = "projection-manifest"
	badgerRecordProjectionActive          = "projection-active"
	badgerRecordProjectionRetention       = "projection-retention"
	badgerRecordProjectionCheckpoint      = "projection-checkpoint"
)

func badgerScopeKey(scope sessionmemory.Scope) ([]byte, error) {
	if err := scope.Validate(); err != nil {
		return nil, err
	}
	return sessionmemory.TupleKey(badgerSessionMemoryNamespace, badgerSessionMemoryVersion, badgerRecordScope, scope.Key, string(scope.Kind))
}

func badgerProvenanceKey(scope sessionmemory.Scope, recordType, revisionID, relatedRevisionID string) ([]byte, error) {
	if err := scope.Validate(); err != nil {
		return nil, err
	}
	return sessionmemory.TupleKey(badgerSessionMemoryNamespace, badgerSessionMemoryVersion, recordType, scope.Key, string(scope.Kind), revisionID, relatedRevisionID)
}

func badgerProvenancePrefix(scope sessionmemory.Scope, recordType, revisionID string) ([]byte, error) {
	if err := scope.Validate(); err != nil {
		return nil, err
	}
	return sessionmemory.TupleKey(badgerSessionMemoryNamespace, badgerSessionMemoryVersion, recordType, scope.Key, string(scope.Kind), revisionID)
}

func badgerOperationKey(scope sessionmemory.Scope, operationID string) ([]byte, error) {
	if err := scope.Validate(); err != nil {
		return nil, err
	}
	return badgerSessionMemoryKey(scope, badgerRecordOperation, operationID)
}

func badgerImportedOperationKey(scope sessionmemory.Scope, operationID string) ([]byte, error) {
	return badgerSessionMemoryKey(scope, badgerRecordImportedOperation, operationID)
}

func badgerMigrationCheckpointKey(scope sessionmemory.Scope, snapshotVersion string) ([]byte, error) {
	return badgerSessionMemoryKey(scope, badgerRecordMigrationCheckpoint, snapshotVersion)
}

func badgerSessionMemoryKey(scope sessionmemory.Scope, recordType string, id string) ([]byte, error) {
	if err := scope.Validate(); err != nil {
		return nil, err
	}
	return sessionmemory.TupleKey(badgerSessionMemoryNamespace, badgerSessionMemoryVersion, recordType, scope.Key, string(scope.Kind), id)
}

func badgerProjectionManifestKey(scope sessionmemory.Scope, projectionID, generationID string) ([]byte, error) {
	if err := scope.Validate(); err != nil {
		return nil, err
	}
	return sessionmemory.TupleKey(badgerSessionMemoryNamespace, badgerSessionMemoryVersion, badgerRecordProjectionManifest, scope.Key, string(scope.Kind), projectionID, generationID)
}

func badgerProjectionRetentionKey(scope sessionmemory.Scope, projectionID string) ([]byte, error) {
	return badgerSessionMemoryKey(scope, badgerRecordProjectionRetention, projectionID)
}

func badgerProjectionCheckpointKey(scope sessionmemory.Scope, kind sessionmemory.ScopeCheckpointKind) ([]byte, error) {
	return badgerSessionMemoryKey(scope, badgerRecordProjectionCheckpoint, string(kind))
}

func badgerSessionMemoryPrefix(scope sessionmemory.Scope, recordType string) ([]byte, error) {
	if err := scope.Validate(); err != nil {
		return nil, err
	}
	return sessionmemory.TupleKey(badgerSessionMemoryNamespace, badgerSessionMemoryVersion, recordType, scope.Key, string(scope.Kind))
}

func badgerScopeChangePrefix(scope sessionmemory.Scope) ([]byte, error) {
	if err := scope.Validate(); err != nil {
		return nil, err
	}
	return sessionmemory.TupleKey(badgerSessionMemoryNamespace, badgerSessionMemoryVersion, badgerRecordChange, scope.Key, string(scope.Kind))
}

func badgerScopeChangeKey(scope sessionmemory.Scope, sequence uint64) ([]byte, error) {
	prefix, err := badgerScopeChangePrefix(scope)
	if err != nil {
		return nil, err
	}
	key := make([]byte, len(prefix)+8)
	copy(key, prefix)
	binary.BigEndian.PutUint64(key[len(prefix):], sequence)
	return key, nil
}
