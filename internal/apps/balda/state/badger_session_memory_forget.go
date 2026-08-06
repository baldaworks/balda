package state

import (
	"context"
	"errors"
	"strings"

	"github.com/dgraph-io/badger/v4"
	"github.com/normahq/balda/sessionmemory"
)

const maxCanonicalScrubPayloads = 4096

type badgerCanonicalForgetOutcome struct {
	Fingerprint string                      `json:"fingerprint"`
	Outcome     sessionmemory.ForgetOutcome `json:"outcome"`
}

var _ sessionmemory.CanonicalForgetEnumerator = (*BadgerSessionMemoryStore)(nil)
var _ sessionmemory.CanonicalSourceIdentityResolver = (*BadgerSessionMemoryStore)(nil)
var _ sessionmemory.CanonicalForgetOutcomeStore = (*BadgerSessionMemoryStore)(nil)

func (s *BadgerSessionMemoryStore) ListCanonicalSourceRefs(ctx context.Context, scope sessionmemory.Scope, afterSourceID string, limit uint32) ([]sessionmemory.SourceRef, string, error) {
	if err := sessionMemoryContextError(ctx); err != nil {
		return nil, "", err
	}
	if err := scope.Validate(); err != nil {
		return nil, "", err
	}
	if limit == 0 || limit > 512 || (afterSourceID != "" && strings.TrimSpace(afterSourceID) != afterSourceID) {
		return nil, "", sessionmemory.PermanentError(sessionmemory.CodeLimitExceeded, "canonical source enumeration limit is invalid", nil)
	}
	if s == nil {
		return nil, "", sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "canonical badger store is closed", nil)
	}
	s.maintenanceMu.RLock()
	defer s.maintenanceMu.RUnlock()
	if s.db == nil {
		return nil, "", sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "canonical badger store is closed", nil)
	}
	prefix, err := badgerSessionMemoryPrefix(scope, badgerRecordSource)
	if err != nil {
		return nil, "", err
	}
	refs := make([]sessionmemory.SourceRef, 0, limit)
	next := ""
	err = s.db.View(func(txn *badger.Txn) error {
		iterator := txn.NewIterator(badger.DefaultIteratorOptions)
		defer iterator.Close()
		for iterator.Seek(prefix); iterator.ValidForPrefix(prefix) && uint32(len(refs)) < limit; iterator.Next() {
			var source sessionmemory.SourceRecordV2
			if err := getBadgerSessionMemoryRecord(txn, iterator.Item().Key(), badgerRecordSource, &source); err != nil {
				return err
			}
			if source.SourceID <= afterSourceID {
				continue
			}
			deniedKey, keyErr := badgerSessionMemoryKey(scope, badgerRecordDeniedSource, source.SourceID)
			if keyErr != nil {
				return keyErr
			}
			var denied badgerDeniedSource
			if deniedErr := getBadgerSessionMemoryRecord(txn, deniedKey, badgerRecordDeniedSource, &denied); deniedErr == nil {
				continue
			} else if !errors.Is(deniedErr, badger.ErrKeyNotFound) {
				return deniedErr
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
				return sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "canonical source payload is invalid", nil)
			}
			ref, _, ok := canonicalSourceRef(payload, scope, source.SourceID)
			if !ok {
				return sessionmemory.PermanentError(sessionmemory.CodeInvalidDerived, "canonical source identity is not representable", nil)
			}
			refs = append(refs, ref)
			next = source.SourceID
		}
		return nil
	})
	if err != nil {
		return nil, "", badgerSessionMemoryError("list canonical source refs", err)
	}
	if len(refs) < int(limit) {
		next = ""
	}
	return refs, next, nil
}

func (s *BadgerSessionMemoryStore) ListCanonicalRevisionRefs(ctx context.Context, scope sessionmemory.Scope, afterRevisionID string, limit uint32) ([]sessionmemory.RevisionRef, string, error) {
	if err := sessionMemoryContextError(ctx); err != nil {
		return nil, "", err
	}
	if err := scope.Validate(); err != nil {
		return nil, "", err
	}
	if limit == 0 || limit > 512 || (afterRevisionID != "" && strings.TrimSpace(afterRevisionID) != afterRevisionID) {
		return nil, "", sessionmemory.PermanentError(sessionmemory.CodeLimitExceeded, "canonical revision enumeration limit is invalid", nil)
	}
	if s == nil {
		return nil, "", sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "canonical badger store is closed", nil)
	}
	s.maintenanceMu.RLock()
	defer s.maintenanceMu.RUnlock()
	if s.db == nil {
		return nil, "", sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "canonical badger store is closed", nil)
	}
	prefix, err := badgerSessionMemoryPrefix(scope, badgerRecordRevision)
	if err != nil {
		return nil, "", err
	}
	refs := make([]sessionmemory.RevisionRef, 0, limit)
	next := ""
	err = s.db.View(func(txn *badger.Txn) error {
		iterator := txn.NewIterator(badger.DefaultIteratorOptions)
		defer iterator.Close()
		for iterator.Seek(prefix); iterator.ValidForPrefix(prefix) && uint32(len(refs)) < limit; iterator.Next() {
			var revision sessionmemory.MemoryRevision
			if err := getBadgerSessionMemoryRecord(txn, iterator.Item().Key(), badgerRecordRevision, &revision); err != nil {
				return err
			}
			if revision.RevisionID <= afterRevisionID {
				continue
			}
			deniedKey, keyErr := badgerSessionMemoryKey(scope, badgerRecordDeniedRevision, revision.RevisionID)
			if keyErr != nil {
				return keyErr
			}
			var denied badgerDeniedRevision
			if deniedErr := getBadgerSessionMemoryRecord(txn, deniedKey, badgerRecordDeniedRevision, &denied); deniedErr == nil {
				continue
			} else if !errors.Is(deniedErr, badger.ErrKeyNotFound) {
				return deniedErr
			}
			refs = append(refs, sessionmemory.RevisionRef{ItemID: revision.ItemID, RevisionID: revision.RevisionID})
			next = revision.RevisionID
		}
		return nil
	})
	if err != nil {
		return nil, "", badgerSessionMemoryError("list canonical revision refs", err)
	}
	if len(refs) < int(limit) {
		next = ""
	}
	return refs, next, nil
}

func (s *BadgerSessionMemoryStore) CanonicalSourceID(ctx context.Context, scope sessionmemory.Scope, source sessionmemory.SourceRef) (string, error) {
	if err := source.Validate(); err != nil {
		return "", err
	}
	if source.Scope != scope {
		return "", sessionmemory.PermanentError(sessionmemory.CodeScopeViolation, "canonical source scope does not match", nil)
	}
	if s == nil {
		return "", sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "canonical badger store is closed", nil)
	}
	s.maintenanceMu.RLock()
	defer s.maintenanceMu.RUnlock()
	if s.db == nil {
		return "", sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "canonical badger store is closed", nil)
	}
	// Native turn sources have a deterministic identity. Check it first to
	// avoid scanning payload records in the common path.
	nativeID := sessionmemory.TurnSourceID(source.ExportID)
	key, err := badgerSessionMemoryKey(scope, badgerRecordSource, nativeID)
	if err != nil {
		return "", err
	}
	var native sessionmemory.SourceRecordV2
	if readErr := s.db.View(func(txn *badger.Txn) error {
		return getBadgerSessionMemoryRecord(txn, key, badgerRecordSource, &native)
	}); readErr == nil {
		return nativeID, nil
	} else if !errors.Is(readErr, badger.ErrKeyNotFound) {
		return "", badgerSessionMemoryError("resolve canonical source identity", readErr)
	}
	prefix, err := badgerSessionMemoryPrefix(scope, badgerRecordSource)
	if err != nil {
		return "", err
	}
	var resolved string
	if scanErr := s.db.View(func(txn *badger.Txn) error {
		iterator := txn.NewIterator(badger.DefaultIteratorOptions)
		defer iterator.Close()
		for iterator.Seek(prefix); iterator.ValidForPrefix(prefix); iterator.Next() {
			var record sessionmemory.SourceRecordV2
			if err := getBadgerSessionMemoryRecord(txn, iterator.Item().Key(), badgerRecordSource, &record); err != nil {
				return err
			}
			payloadKey, keyErr := badgerSessionMemoryKey(payloadScope(), badgerRecordPayload, record.Payload.ID)
			if keyErr != nil {
				return keyErr
			}
			var payload []byte
			if err := getBadgerSessionMemoryRecord(txn, payloadKey, badgerRecordPayload, &payload); err != nil {
				return err
			}
			candidate, _, ok := canonicalSourceRef(payload, scope, record.SourceID)
			if ok && candidate == source {
				resolved = record.SourceID
				return nil
			}
		}
		return nil
	}); scanErr != nil {
		return "", badgerSessionMemoryError("resolve canonical source identity", scanErr)
	}
	if resolved != "" {
		return resolved, nil
	}
	return "", sessionmemory.PermanentError(sessionmemory.CodeNotFound, "canonical source does not exist", nil)
}

func (s *BadgerSessionMemoryStore) LoadCanonicalForgetOutcome(ctx context.Context, scope sessionmemory.Scope, operationID string, kind sessionmemory.ForgetKind) (sessionmemory.ForgetOutcome, bool, error) {
	if err := sessionMemoryContextError(ctx); err != nil {
		return sessionmemory.ForgetOutcome{}, false, err
	}
	if err := scope.Validate(); err != nil {
		return sessionmemory.ForgetOutcome{}, false, err
	}
	if err := kind.Validate(); err != nil || strings.TrimSpace(operationID) == "" || strings.TrimSpace(operationID) != operationID {
		return sessionmemory.ForgetOutcome{}, false, sessionmemory.PermanentError(sessionmemory.CodeInvalidDerived, "canonical forget outcome lookup is invalid", err)
	}
	if s == nil {
		return sessionmemory.ForgetOutcome{}, false, sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "canonical badger store is closed", nil)
	}
	s.maintenanceMu.RLock()
	defer s.maintenanceMu.RUnlock()
	if s.db == nil {
		return sessionmemory.ForgetOutcome{}, false, sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "canonical badger store is closed", nil)
	}
	key, err := badgerForgetOutcomeKey(scope, kind, operationID)
	if err != nil {
		return sessionmemory.ForgetOutcome{}, false, err
	}
	var stored badgerCanonicalForgetOutcome
	err = s.db.View(func(txn *badger.Txn) error {
		return getBadgerSessionMemoryRecord(txn, key, badgerRecordForgetOutcome, &stored)
	})
	if errors.Is(err, badger.ErrKeyNotFound) {
		return sessionmemory.ForgetOutcome{}, false, nil
	}
	if err != nil {
		return sessionmemory.ForgetOutcome{}, false, badgerSessionMemoryError("load canonical forget outcome", err)
	}
	if err := stored.Outcome.Validate(); err != nil || stored.Outcome.Scope != scope || stored.Outcome.OperationID != operationID || stored.Outcome.Kind != kind {
		return sessionmemory.ForgetOutcome{}, false, sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "stored canonical forget outcome is invalid", err)
	}
	return stored.Outcome, true, nil
}

func (s *BadgerSessionMemoryStore) CommitCanonicalForget(ctx context.Context, request sessionmemory.CanonicalForgetCommitRequest) (sessionmemory.ForgetOutcome, error) {
	if err := sessionMemoryContextError(ctx); err != nil {
		return sessionmemory.ForgetOutcome{}, err
	}
	if err := request.Validate(); err != nil {
		return sessionmemory.ForgetOutcome{}, err
	}
	if s == nil {
		return sessionmemory.ForgetOutcome{}, sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "canonical badger store is closed", nil)
	}
	s.maintenanceMu.RLock()
	defer s.maintenanceMu.RUnlock()
	if s.db == nil {
		return sessionmemory.ForgetOutcome{}, sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "canonical badger store is closed", nil)
	}
	key, err := badgerForgetOutcomeKey(request.Scope, request.Kind, request.OperationID)
	if err != nil {
		return sessionmemory.ForgetOutcome{}, err
	}
	scopeKey, err := badgerScopeKey(request.Scope)
	if err != nil {
		return sessionmemory.ForgetOutcome{}, err
	}
	var outcome sessionmemory.ForgetOutcome
	err = s.db.Update(func(txn *badger.Txn) error {
		var existing badgerCanonicalForgetOutcome
		readErr := getBadgerSessionMemoryRecord(txn, key, badgerRecordForgetOutcome, &existing)
		if readErr == nil {
			if existing.Fingerprint != request.Fingerprint {
				return sessionmemory.PermanentError(sessionmemory.CodeConflict, "canonical forget operation identity was reused", nil)
			}
			if err := existing.Outcome.Validate(); err != nil || existing.Outcome.Scope != request.Scope || existing.Outcome.OperationID != request.OperationID || existing.Outcome.Kind != request.Kind {
				return sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "stored canonical forget outcome is invalid", err)
			}
			outcome = existing.Outcome
			return nil
		}
		if !errors.Is(readErr, badger.ErrKeyNotFound) {
			return readErr
		}
		state := sessionmemory.ScopeState{SchemaVersion: sessionmemory.CanonicalSchemaVersionV1, Scope: request.Scope}
		stateErr := getBadgerSessionMemoryRecord(txn, scopeKey, badgerRecordScope, &state)
		if stateErr != nil && !errors.Is(stateErr, badger.ErrKeyNotFound) {
			return stateErr
		}
		if err := state.Validate(); err != nil {
			return err
		}
		if state.Version != request.ExpectedScopeVersion {
			return sessionmemory.RetryableError(sessionmemory.CodeConflict, "canonical forget scope version changed", nil)
		}
		state.Version++
		state.ChangeSeq++
		outcome = sessionmemory.ForgetOutcome{SchemaVersion: sessionmemory.DerivedSchemaVersionV1, OperationID: request.OperationID, Kind: request.Kind, Scope: request.Scope, ScopeVersion: state.Version, Sources: append([]sessionmemory.SourceRef(nil), request.Sources...), Revisions: append([]sessionmemory.RevisionRef(nil), request.Revisions...)}
		if err := outcome.Validate(); err != nil {
			return err
		}
		changeKey, keyErr := badgerScopeChangeKey(request.Scope, state.ChangeSeq)
		if keyErr != nil {
			return keyErr
		}
		change := sessionmemory.ScopeChange{Sequence: state.ChangeSeq, OperationID: request.OperationID, OccurredAt: request.ForgottenAt.UTC()}
		for _, revision := range request.Revisions {
			change.RevisionIDs = append(change.RevisionIDs, revision.RevisionID)
		}
		if err := putBadgerSessionMemoryRecord(txn, changeKey, badgerRecordChange, change); err != nil {
			return err
		}
		if err := putBadgerSessionMemoryRecord(txn, scopeKey, badgerRecordScope, state); err != nil {
			return err
		}
		if err := putBadgerSessionMemoryRecord(txn, key, badgerRecordForgetOutcome, badgerCanonicalForgetOutcome{Fingerprint: request.Fingerprint, Outcome: outcome}); err != nil {
			return err
		}
		if s.beforeCanonicalMutationCommit != nil {
			return s.beforeCanonicalMutationCommit()
		}
		return nil
	})
	if err != nil {
		return sessionmemory.ForgetOutcome{}, badgerSessionMemoryError("commit canonical forget outcome", err)
	}
	return outcome, nil
}

// ScrubCanonicalForget removes only payload blobs already protected by the
// logical deny markers. Structural records, identities, and tombstone markers
// remain available for audit and replay-safe rejection.
func (s *BadgerSessionMemoryStore) ScrubCanonicalForget(ctx context.Context, scope sessionmemory.Scope, sourceIDs []string, revisionIDs []string) error {
	if err := sessionMemoryContextError(ctx); err != nil {
		return err
	}
	if err := scope.Validate(); err != nil {
		return err
	}
	if len(sourceIDs) > 4096 || len(revisionIDs) > 4096 {
		return sessionmemory.PermanentError(sessionmemory.CodeLimitExceeded, "canonical forget scrub bound exceeded", nil)
	}
	if s == nil {
		return sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "canonical badger store is closed", nil)
	}
	deniedSources := make(map[string]struct{}, len(sourceIDs))
	for _, id := range sourceIDs {
		if strings.TrimSpace(id) != id || id == "" {
			return sessionmemory.PermanentError(sessionmemory.CodeInvalidDerived, "canonical forget source identity is invalid", nil)
		}
		deniedSources[id] = struct{}{}
	}
	deniedRevisions := make(map[string]struct{}, len(revisionIDs))
	for _, id := range revisionIDs {
		if strings.TrimSpace(id) != id || id == "" {
			return sessionmemory.PermanentError(sessionmemory.CodeInvalidDerived, "canonical forget revision identity is invalid", nil)
		}
		deniedRevisions[id] = struct{}{}
	}
	s.maintenanceMu.RLock()
	if s.db == nil {
		s.maintenanceMu.RUnlock()
		return sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "canonical badger store is closed", nil)
	}
	refs := make([]sessionmemory.PayloadRef, 0, len(sourceIDs)+len(revisionIDs))
	seenPayloads := make(map[string]struct{}, len(sourceIDs)+len(revisionIDs))
	scrubOverflow := false
	appendPayload := func(ref sessionmemory.PayloadRef) {
		if ref.ID == "" {
			return
		}
		if _, exists := seenPayloads[ref.ID]; exists {
			return
		}
		if len(refs) >= maxCanonicalScrubPayloads {
			scrubOverflow = true
			return
		}
		seenPayloads[ref.ID] = struct{}{}
		refs = append(refs, ref)
	}
	if err := s.db.View(func(txn *badger.Txn) error {
		for recordType, wanted := range map[string]map[string]struct{}{badgerRecordSource: deniedSources, badgerRecordRevision: deniedRevisions} {
			prefix, err := badgerSessionMemoryPrefix(scope, recordType)
			if err != nil {
				return err
			}
			iterator := txn.NewIterator(badger.DefaultIteratorOptions)
			for iterator.Seek(prefix); iterator.ValidForPrefix(prefix); iterator.Next() {
				if recordType == badgerRecordSource {
					var source sessionmemory.SourceRecordV2
					if err := getBadgerSessionMemoryRecord(txn, iterator.Item().Key(), recordType, &source); err != nil {
						iterator.Close()
						return err
					}
					if _, ok := wanted[source.SourceID]; ok {
						appendPayload(source.Payload)
					}
				} else {
					var revision sessionmemory.MemoryRevision
					if err := getBadgerSessionMemoryRecord(txn, iterator.Item().Key(), recordType, &revision); err != nil {
						iterator.Close()
						return err
					}
					if _, ok := wanted[revision.RevisionID]; ok {
						appendPayload(revision.Payload)
					}
				}
			}
			iterator.Close()
		}
		// Message payloads are independent immutable blobs.  Removing only
		// source/revision payloads would leave forgotten turn text reachable by
		// a trace hydrator, so scan the bounded exact scope for denied sources.
		if len(deniedSources) > 0 {
			prefix, err := badgerSessionMemoryPrefix(scope, badgerRecordMessage)
			if err != nil {
				return err
			}
			iterator := txn.NewIterator(badger.DefaultIteratorOptions)
			for iterator.Seek(prefix); iterator.ValidForPrefix(prefix); iterator.Next() {
				var message sessionmemory.MessageRecord
				if err := getBadgerSessionMemoryRecord(txn, iterator.Item().Key(), badgerRecordMessage, &message); err != nil {
					iterator.Close()
					return err
				}
				if _, ok := deniedSources[message.SourceID]; ok {
					appendPayload(message.Payload)
				}
			}
			iterator.Close()
		}
		return nil
	}); err != nil {
		s.maintenanceMu.RUnlock()
		return badgerSessionMemoryError("scan canonical forget payloads", err)
	}
	s.maintenanceMu.RUnlock()
	if scrubOverflow {
		return sessionmemory.PermanentError(sessionmemory.CodeLimitExceeded, "canonical forget scrub payload bound exceeded", nil)
	}
	for _, ref := range refs {
		if err := s.DeletePayload(ctx, ref); err != nil {
			return err
		}
	}
	return nil
}
