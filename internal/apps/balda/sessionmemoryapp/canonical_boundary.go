package sessionmemoryapp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"time"

	"github.com/normahq/balda/sessionmemory"
)

// CanonicalBoundaryProcessor adapts the memory-owned boundary processor to
// canonical Badger view and mutation ports through the current narrow APIs.
type CanonicalBoundaryProcessor struct {
	processor *sessionmemory.BoundaryProcessor
}

func NewCanonicalBoundaryProcessor(canonical sessionmemory.CanonicalStore, view sessionmemory.CanonicalBoundaryViewReader, scenarios sessionmemory.ScenarioSynthesizer, profiles sessionmemory.ProfileSynthesizer, derivation sessionmemory.DerivationRef) (*CanonicalBoundaryProcessor, error) {
	if canonical == nil || view == nil || scenarios == nil || profiles == nil {
		return nil, sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "canonical boundary dependencies are required", nil)
	}
	if err := derivation.Validate(); err != nil {
		return nil, err
	}
	store := &canonicalBoundaryStore{canonical: canonical, view: view}
	processor, err := sessionmemory.NewBoundaryProcessor(store, scenarios, profiles, derivation)
	if err != nil {
		return nil, err
	}
	return &CanonicalBoundaryProcessor{processor: processor}, nil
}

// ProcessBoundary returns both independently durable stages. A profile/model
// failure leaves the scenario outcome intact, matching the worker retry
// contract.
func (p *CanonicalBoundaryProcessor) ProcessBoundary(ctx context.Context, boundary sessionmemory.Boundary) (sessionmemory.BoundaryOutcome, error) {
	if p == nil || p.processor == nil {
		return sessionmemory.BoundaryOutcome{}, sessionmemory.PermanentError(sessionmemory.CodeDisabled, "canonical boundary processor is unavailable", nil)
	}
	return p.processor.ProcessBoundary(ctx, boundary)
}

type canonicalBoundaryStore struct {
	canonical sessionmemory.CanonicalStore
	view      sessionmemory.CanonicalBoundaryViewReader
}

// canonicalCompatibilityReader is an application-local escape hatch for
// replaying a prior boundary stage after its revision has been superseded.
// The active boundary view intentionally stays bounded; replay may resolve
// only the operation's exact canonical revision IDs through this port.
type canonicalCompatibilityReader interface {
	LoadCanonicalCompatibility(ctx context.Context, scope sessionmemory.Scope, revisionID string) (sessionmemory.CanonicalCompatibilityPayload, bool, error)
}

var _ sessionmemory.BoundaryStore = (*canonicalBoundaryStore)(nil)

func (s *canonicalBoundaryStore) LookupOperation(ctx context.Context, lookup sessionmemory.OperationLookup) (sessionmemory.OperationLookupResult, error) {
	if err := lookup.Validate(); err != nil {
		return sessionmemory.OperationLookupResult{}, err
	}
	reader, ok := s.canonical.(sessionmemory.CanonicalOperationReader)
	if !ok {
		return sessionmemory.OperationLookupResult{}, nil
	}
	stored, found, err := reader.LoadCanonicalOperation(ctx, lookup.Scope, lookup.OperationID)
	if err != nil || !found {
		return sessionmemory.OperationLookupResult{}, err
	}
	if err := stored.Validate(); err != nil {
		return sessionmemory.OperationLookupResult{}, err
	}
	view, err := s.view.LoadCanonicalBoundaryView(ctx, lookup.Scope)
	if err != nil {
		return sessionmemory.OperationLookupResult{}, err
	}
	legacyByCanonical := make(map[string]sessionmemory.RevisionRef, len(view.LegacyToCanonical))
	for legacy, canonical := range view.LegacyToCanonical {
		legacyByCanonical[canonical.RevisionID] = legacy
	}
	compatibilityReader, compatibilityReaderOK := s.view.(canonicalCompatibilityReader)
	revisions := make([]sessionmemory.RevisionRef, 0, len(stored.Outcome.RevisionIDs))
	for _, canonicalID := range stored.Outcome.RevisionIDs {
		legacy, exists := legacyByCanonical[canonicalID]
		if !exists {
			if !compatibilityReaderOK {
				return sessionmemory.OperationLookupResult{}, sessionmemory.PermanentError(sessionmemory.CodeConflict, "canonical replay outcome is not representable", nil)
			}
			compatibility, found, compatibilityErr := compatibilityReader.LoadCanonicalCompatibility(ctx, lookup.Scope, canonicalID)
			if compatibilityErr != nil {
				return sessionmemory.OperationLookupResult{}, compatibilityErr
			}
			if !found {
				return sessionmemory.OperationLookupResult{}, sessionmemory.PermanentError(sessionmemory.CodeConflict, "canonical replay outcome is not representable", nil)
			}
			legacy = sessionmemory.RevisionRef{ItemID: compatibility.LegacyItemID, RevisionID: compatibility.LegacyRevisionID}
		}
		revisions = append(revisions, legacy)
	}
	outcome := sessionmemory.OperationOutcome{SchemaVersion: sessionmemory.DerivedSchemaVersionV1, OperationID: lookup.OperationID, Stage: lookup.Stage, Scope: lookup.Scope, ScopeVersion: stored.Outcome.ScopeVersion, Revisions: revisions}
	if err := outcome.Validate(); err != nil {
		return sessionmemory.OperationLookupResult{}, err
	}
	return sessionmemory.OperationLookupResult{Found: true, Outcome: outcome}, nil
}

func (s *canonicalBoundaryStore) LoadScope(ctx context.Context, scope sessionmemory.Scope) (sessionmemory.ScopeSnapshot, error) {
	view, err := s.view.LoadCanonicalBoundaryView(ctx, scope)
	if err != nil {
		return sessionmemory.ScopeSnapshot{}, err
	}
	if err := view.Validate(); err != nil {
		return sessionmemory.ScopeSnapshot{}, err
	}
	return view.Snapshot, nil
}

func (s *canonicalBoundaryStore) Commit(ctx context.Context, request sessionmemory.CommitRequest) (sessionmemory.OperationOutcome, error) {
	if err := request.Validate(); err != nil {
		return sessionmemory.OperationOutcome{}, err
	}
	view, err := s.view.LoadCanonicalBoundaryView(ctx, request.Scope)
	if err != nil {
		return sessionmemory.OperationOutcome{}, err
	}
	mutation, canonicalRefs, err := s.buildMutation(ctx, request, view)
	if err != nil {
		return sessionmemory.OperationOutcome{}, err
	}
	if len(mutation.Items)+len(mutation.Revisions)+len(mutation.Lifecycle)+len(mutation.Payloads) == 0 {
		committer, ok := s.canonical.(sessionmemory.CanonicalOperationCommitter)
		if ok {
			outcome, commitErr := committer.CommitCanonicalOperation(ctx, sessionmemory.CanonicalOperationCommitRequest{Scope: request.Scope, ExpectedScopeVersion: request.ExpectedScopeVersion, OperationID: request.OperationID, Fingerprint: mutation.Operation.Fingerprint, CommittedAt: mutation.Operation.CommittedAt})
			if commitErr != nil {
				return sessionmemory.OperationOutcome{}, commitErr
			}
			return sessionmemory.OperationOutcome{SchemaVersion: sessionmemory.DerivedSchemaVersionV1, OperationID: request.OperationID, Stage: request.Stage, Scope: request.Scope, ScopeVersion: outcome.ScopeVersion}, nil
		}
		state, stateErr := s.canonical.LoadScopeState(ctx, request.Scope)
		if stateErr != nil {
			return sessionmemory.OperationOutcome{}, stateErr
		}
		return sessionmemory.OperationOutcome{SchemaVersion: sessionmemory.DerivedSchemaVersionV1, OperationID: request.OperationID, Stage: request.Stage, Scope: request.Scope, ScopeVersion: state.Version}, nil
	}
	outcome, err := s.canonical.ApplyCanonicalMutation(ctx, mutation)
	if err != nil {
		return sessionmemory.OperationOutcome{}, err
	}
	legacyRefs := make([]sessionmemory.RevisionRef, 0, len(canonicalRefs))
	for _, ref := range canonicalRefs {
		legacyRefs = append(legacyRefs, ref.legacy)
	}
	sort.Slice(legacyRefs, func(i, j int) bool { return legacyRefs[i].RevisionID < legacyRefs[j].RevisionID })
	return sessionmemory.OperationOutcome{SchemaVersion: sessionmemory.DerivedSchemaVersionV1, OperationID: request.OperationID, Stage: request.Stage, Scope: request.Scope, ScopeVersion: outcome.ScopeVersion, Revisions: legacyRefs}, nil
}

func (s *canonicalBoundaryStore) buildMutation(ctx context.Context, request sessionmemory.CommitRequest, view sessionmemory.CanonicalBoundaryView) (sessionmemory.CanonicalMutation, []canonicalBoundaryRef, error) {
	mutation := sessionmemory.CanonicalMutation{SchemaVersion: sessionmemory.CanonicalSchemaVersionV1, Scope: request.Scope, ExpectedScopeVersion: request.ExpectedScopeVersion, Operation: sessionmemory.OperationRecord{OperationID: request.OperationID, Fingerprint: boundaryFingerprint(request), CommittedAt: requestTime(request)}}
	canonicalRefs := make([]canonicalBoundaryRef, 0, len(request.Scenarios)+len(request.Profiles))
	existing := make(map[sessionmemory.RevisionRef]sessionmemory.RevisionRef, len(view.LegacyToCanonical))
	for legacy, canonical := range view.LegacyToCanonical {
		existing[legacy] = canonical
	}
	for _, scenario := range request.Scenarios {
		ref, err := s.appendScenario(ctx, &mutation, view, scenario, existing)
		if err != nil {
			return sessionmemory.CanonicalMutation{}, nil, err
		}
		canonicalRefs = append(canonicalRefs, ref)
		mutation.Operation.Outcome = append(mutation.Operation.Outcome, ref.canonical.RevisionID)
	}
	for _, profile := range request.Profiles {
		ref, err := s.appendProfile(ctx, &mutation, view, profile, existing)
		if err != nil {
			return sessionmemory.CanonicalMutation{}, nil, err
		}
		canonicalRefs = append(canonicalRefs, ref)
		mutation.Operation.Outcome = append(mutation.Operation.Outcome, ref.canonical.RevisionID)
	}
	for _, transition := range request.Transitions {
		canonical, ok := existing[transition.Ref]
		if !ok {
			return sessionmemory.CanonicalMutation{}, nil, sessionmemory.PermanentError(sessionmemory.CodeConflict, "canonical boundary transition target is unavailable", nil)
		}
		typeName := sessionmemory.LifecycleEventInvalidate
		if transition.To == sessionmemory.RevisionStateSuperseded {
			typeName = sessionmemory.LifecycleEventSupersede
		}
		mutation.Lifecycle = append(mutation.Lifecycle, sessionmemory.LifecycleEvent{EventID: boundaryID("lifecycle", request.OperationID, canonical.RevisionID, string(typeName)), RevisionID: canonical.RevisionID, Type: typeName, OccurredAt: mutation.Operation.CommittedAt})
	}
	if err := mutation.Validate(); err != nil {
		return sessionmemory.CanonicalMutation{}, nil, err
	}
	return mutation, canonicalRefs, nil
}

type canonicalBoundaryRef struct {
	legacy    sessionmemory.RevisionRef
	canonical sessionmemory.RevisionRef
}

func (s *canonicalBoundaryStore) appendScenario(ctx context.Context, mutation *sessionmemory.CanonicalMutation, view sessionmemory.CanonicalBoundaryView, scenario sessionmemory.Scenario, existing map[sessionmemory.RevisionRef]sessionmemory.RevisionRef) (canonicalBoundaryRef, error) {
	if err := scenario.Validate(); err != nil {
		return canonicalBoundaryRef{}, err
	}
	parents, evidence, err := s.parentsAndEvidence(ctx, view, scenario.Meta.Provenance.ParentRevisions, existing)
	if err != nil {
		return canonicalBoundaryRef{}, err
	}
	return s.appendAggregate(mutation, scenario.Meta, sessionmemory.DerivedKindScenario, scenario.TopicKey, scenario.Title, scenario.Summary, parents, evidence, existing)
}

func (s *canonicalBoundaryStore) appendProfile(ctx context.Context, mutation *sessionmemory.CanonicalMutation, view sessionmemory.CanonicalBoundaryView, profile sessionmemory.Profile, existing map[sessionmemory.RevisionRef]sessionmemory.RevisionRef) (canonicalBoundaryRef, error) {
	if err := profile.Validate(); err != nil {
		return canonicalBoundaryRef{}, err
	}
	parents, evidence, err := s.parentsAndEvidence(ctx, view, profile.Meta.Provenance.ParentRevisions, existing)
	if err != nil {
		return canonicalBoundaryRef{}, err
	}
	return s.appendAggregate(mutation, profile.Meta, sessionmemory.DerivedKindProfile, "", "", profile.Summary, parents, evidence, existing)
}

func (s *canonicalBoundaryStore) parentsAndEvidence(ctx context.Context, view sessionmemory.CanonicalBoundaryView, parents []sessionmemory.RevisionRef, existing map[sessionmemory.RevisionRef]sessionmemory.RevisionRef) ([]string, []sessionmemory.EvidenceRef, error) {
	if len(parents) == 0 {
		return nil, nil, sessionmemory.PermanentError(sessionmemory.CodeInvalidDerived, "canonical boundary provenance is required", nil)
	}
	canonicalIDs := make([]string, 0, len(parents))
	for _, parent := range parents {
		canonical, ok := existing[parent]
		if !ok {
			return nil, nil, sessionmemory.PermanentError(sessionmemory.CodeConflict, "canonical boundary parent is unavailable", nil)
		}
		canonicalIDs = append(canonicalIDs, canonical.RevisionID)
	}
	revisions, err := s.canonical.LoadCanonicalRevisions(ctx, sessionmemory.CanonicalRevisionReadRequest{Scope: view.Snapshot.Scope, RevisionIDs: canonicalIDs})
	if err != nil {
		return nil, nil, err
	}
	seen := make(map[string]struct{})
	evidence := make([]sessionmemory.EvidenceRef, 0, sessionmemory.MaxRecallEvidence)
	for _, revision := range revisions {
		for _, item := range revision.Evidence {
			if _, ok := seen[item.SourceID+"\x00"+item.MessageID]; ok {
				continue
			}
			seen[item.SourceID+"\x00"+item.MessageID] = struct{}{}
			evidence = append(evidence, item)
			if len(evidence) == sessionmemory.MaxRecallEvidence {
				return canonicalIDs, evidence, nil
			}
		}
	}
	if len(evidence) == 0 {
		return nil, nil, sessionmemory.PermanentError(sessionmemory.CodeInvalidDerived, "canonical boundary parent evidence is unavailable", nil)
	}
	return canonicalIDs, evidence, nil
}

func (s *canonicalBoundaryStore) appendAggregate(mutation *sessionmemory.CanonicalMutation, meta sessionmemory.RevisionMeta, kind sessionmemory.DerivedKind, topicKey, title, text string, parents []string, evidence []sessionmemory.EvidenceRef, existing map[sessionmemory.RevisionRef]sessionmemory.RevisionRef) (canonicalBoundaryRef, error) {
	item := sessionmemory.MemoryItem{ItemID: meta.ItemID, Scope: meta.Scope, Kind: sessionmemory.MemoryKindState, MemoryKey: sessionmemory.MemoryKey(meta.ItemID)}
	if err := item.Validate(); err != nil {
		return canonicalBoundaryRef{}, err
	}
	compat := sessionmemory.CanonicalCompatibilityPayload{SchemaVersion: sessionmemory.CanonicalCompatibilitySchemaVersion, Kind: kind, TopicKey: topicKey, Title: title, Text: text, LegacyItemID: meta.ItemID, LegacyRevisionID: meta.RevisionID, LegacyOperationID: meta.OperationID, LegacyParents: append([]sessionmemory.RevisionRef(nil), meta.Provenance.ParentRevisions...)}
	if meta.Supersedes != nil {
		copyOf := *meta.Supersedes
		compat.Supersedes = &copyOf
	}
	payloadBytes, err := json.Marshal(compat)
	if err != nil {
		return canonicalBoundaryRef{}, err
	}
	payload, payloadRef := canonicalBoundaryPayload("compatibility", meta.RevisionID, payloadBytes)
	revision := sessionmemory.MemoryRevision{SchemaVersion: sessionmemory.MemorySchemaVersionV2, RevisionID: meta.RevisionID, ItemID: item.ItemID, Revision: meta.Revision, Parents: parents, Temporal: sessionmemory.Temporal{ObservedAt: meta.CreatedAt}, Evidence: evidence, Sensitivity: sessionmemory.SensitivityStandard, Retention: sessionmemory.RetentionClassStandard, Payload: payloadRef}
	if err := revision.Validate(); err != nil {
		return canonicalBoundaryRef{}, err
	}
	itemExists := false
	for legacy := range existing {
		if legacy.ItemID == meta.ItemID {
			itemExists = true
			break
		}
	}
	if !itemExists {
		mutation.Items = append(mutation.Items, item)
	}
	mutation.Revisions = append(mutation.Revisions, revision)
	lifecycleType := sessionmemory.LifecycleEventActivate
	if meta.Supersedes != nil {
		lifecycleType = sessionmemory.LifecycleEventSupersede
	}
	mutation.Lifecycle = append(mutation.Lifecycle, sessionmemory.LifecycleEvent{EventID: boundaryID("lifecycle", meta.OperationID, meta.RevisionID, string(lifecycleType)), RevisionID: meta.RevisionID, Type: lifecycleType, OccurredAt: meta.CreatedAt})
	mutation.Heads = append(mutation.Heads, sessionmemory.ItemHead{ItemID: item.ItemID, RevisionID: meta.RevisionID})
	mutation.Payloads = append(mutation.Payloads, payload)
	return canonicalBoundaryRef{legacy: sessionmemory.RevisionRef{ItemID: meta.ItemID, RevisionID: meta.RevisionID}, canonical: sessionmemory.RevisionRef{ItemID: item.ItemID, RevisionID: meta.RevisionID}}, nil
}

func boundaryFingerprint(request sessionmemory.CommitRequest) string {
	stable := request
	stable.ExpectedScopeVersion = 0
	encoded, _ := json.Marshal(stable)
	hash := sha256.Sum256(encoded)
	return "session-memory:v2:boundary-fingerprint:" + hex.EncodeToString(hash[:])
}

func boundaryID(kind string, values ...string) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte(kind))
	for _, value := range values {
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(value))
	}
	return "session-memory:v2:boundary:" + hex.EncodeToString(hash.Sum(nil))
}

func canonicalBoundaryPayload(kind, identity string, data []byte) (sessionmemory.CanonicalPayload, sessionmemory.PayloadRef) {
	digest := sha256.Sum256(data)
	ref := sessionmemory.PayloadRef{ID: boundaryID("payload", kind, identity), Digest: hex.EncodeToString(digest[:]), ByteSize: uint32(len(data))}
	return sessionmemory.CanonicalPayload{Ref: ref, Data: append([]byte(nil), data...)}, ref
}

func requestTime(request sessionmemory.CommitRequest) time.Time {
	for _, scenario := range request.Scenarios {
		return scenario.Meta.CreatedAt
	}
	for _, profile := range request.Profiles {
		return profile.Meta.CreatedAt
	}
	return time.Now().UTC()
}
