package sessionmemoryapp

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/baldaworks/balda/sessionmemory"
)

const canonicalForgetPage uint32 = 128

// CanonicalForgetService owns logical denial and bounded canonical cascade
// traversal. Physical payload/index scrubbing is deliberately optional and
// runs after the deny markers have made recall fail closed.
type CanonicalForgetService struct {
	store      sessionmemory.CanonicalSourceForgetStore
	canonical  sessionmemory.CanonicalStore
	enumerator sessionmemory.CanonicalForgetEnumerator
	outcomes   sessionmemory.CanonicalForgetOutcomeStore
	scrubber   CanonicalForgetScrubber
	now        func() time.Time
}

// CanonicalForgetScrubber is a bounded maintenance hook. It receives only
// identities; implementations must not make recall depend on scrub success.
type CanonicalForgetScrubber interface {
	ScrubCanonicalForget(ctx context.Context, scope sessionmemory.Scope, sourceIDs []string, revisionIDs []string) error
}

// MultiScrubber combines multiple CanonicalForgetScrubbers into one sequential scrubber.
func MultiScrubber(scrubbers ...CanonicalForgetScrubber) CanonicalForgetScrubber {
	active := make([]CanonicalForgetScrubber, 0, len(scrubbers))
	for _, s := range scrubbers {
		if s != nil {
			active = append(active, s)
		}
	}
	if len(active) == 0 {
		return nil
	}
	if len(active) == 1 {
		return active[0]
	}
	return multiScrubber(active)
}

type multiScrubber []CanonicalForgetScrubber

func (m multiScrubber) ScrubCanonicalForget(ctx context.Context, scope sessionmemory.Scope, sourceIDs []string, revisionIDs []string) error {
	var errs []error
	for _, s := range m {
		if err := s.ScrubCanonicalForget(ctx, scope, sourceIDs, revisionIDs); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func NewCanonicalForgetService(store sessionmemory.CanonicalSourceForgetStore, canonical sessionmemory.CanonicalStore, enumerator sessionmemory.CanonicalForgetEnumerator, outcomes sessionmemory.CanonicalForgetOutcomeStore, scrubber CanonicalForgetScrubber) (*CanonicalForgetService, error) {
	if store == nil || canonical == nil {
		return nil, sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "canonical forget dependencies are required", nil)
	}
	return &CanonicalForgetService{store: store, canonical: canonical, enumerator: enumerator, outcomes: outcomes, scrubber: scrubber, now: time.Now}, nil
}

func (s *CanonicalForgetService) ForgetSource(ctx context.Context, command sessionmemory.ForgetSourceCommand) (sessionmemory.ForgetOutcome, error) {
	if s == nil || s.store == nil || s.canonical == nil {
		return sessionmemory.ForgetOutcome{}, sessionmemory.PermanentError(sessionmemory.CodeDisabled, "canonical forget service is unavailable", nil)
	}
	if ctx == nil {
		return sessionmemory.ForgetOutcome{}, sessionmemory.PermanentError(sessionmemory.CodeInvalidDerived, "canonical forget context is required", nil)
	}
	if err := command.Validate(); err != nil {
		return sessionmemory.ForgetOutcome{}, err
	}
	opID, err := sessionmemory.CanonicalForgetOperationID(sessionmemory.ForgetKindSource, command.Source.Scope, command.Source, "")
	if err != nil {
		return sessionmemory.ForgetOutcome{}, err
	}
	fingerprint := sessionmemory.CanonicalForgetFingerprint(opID, command.Source.Scope, sessionmemory.ForgetKindSource, command.Source, "")
	if prior, found, lookupErr := s.loadOutcome(ctx, command.Source.Scope, opID, sessionmemory.ForgetKindSource); lookupErr != nil {
		return sessionmemory.ForgetOutcome{}, lookupErr
	} else if found {
		return prior, nil
	}
	if s.enumerator != nil {
		found, err := s.sourceExists(ctx, command.Source)
		if err != nil {
			return sessionmemory.ForgetOutcome{}, err
		}
		if !found {
			return sessionmemory.ForgetOutcome{}, sessionmemory.PermanentError(sessionmemory.CodeNotFound, "canonical raw source does not exist", nil)
		}
	}

	deniedAt := command.ForgottenAt.UTC()
	sourceID, err := s.sourceID(ctx, command.Source)
	if err != nil {
		return sessionmemory.ForgetOutcome{}, err
	}
	if err := s.store.DenySource(ctx, command.Source.Scope, sourceID, deniedAt); err != nil {
		return sessionmemory.ForgetOutcome{}, err
	}
	revisionIDs, err := s.denySourceRevisions(ctx, command.Source.Scope, sourceID, deniedAt)
	if err != nil {
		return sessionmemory.ForgetOutcome{}, err
	}
	revisionRefs := make([]sessionmemory.RevisionRef, 0, len(revisionIDs))
	for _, revisionID := range revisionIDs {
		// A canonical revision's item identity is not needed by the denial port;
		// the enumerator supplies it when available for the public outcome.
		revisionRefs = append(revisionRefs, sessionmemory.RevisionRef{ItemID: revisionID, RevisionID: revisionID})
	}
	if s.enumerator != nil {
		revisionRefs, err = s.revisionRefsForIDs(ctx, command.Source.Scope, revisionIDs)
		if err != nil {
			return sessionmemory.ForgetOutcome{}, err
		}
	}
	expectedScopeVersion, err := s.scopeVersion(ctx, command.Source.Scope)
	if err != nil {
		return sessionmemory.ForgetOutcome{}, err
	}
	return s.commit(ctx, sessionmemory.CanonicalForgetCommitRequest{
		Scope: command.Source.Scope, OperationID: opID, Kind: sessionmemory.ForgetKindSource,
		Fingerprint: fingerprint, ExpectedScopeVersion: expectedScopeVersion,
		Sources: []sessionmemory.SourceRef{command.Source}, Revisions: revisionRefs, ForgottenAt: deniedAt,
	}, command.Source.Scope, []string{sourceID}, revisionIDs)
}

func (s *CanonicalForgetService) ForgetScope(ctx context.Context, command sessionmemory.ForgetScopeCommand) (sessionmemory.ForgetOutcome, error) {
	if s == nil || s.store == nil || s.canonical == nil {
		return sessionmemory.ForgetOutcome{}, sessionmemory.PermanentError(sessionmemory.CodeDisabled, "canonical forget service is unavailable", nil)
	}
	if ctx == nil {
		return sessionmemory.ForgetOutcome{}, sessionmemory.PermanentError(sessionmemory.CodeInvalidDerived, "canonical forget context is required", nil)
	}
	if err := command.Validate(); err != nil {
		return sessionmemory.ForgetOutcome{}, err
	}
	opID, err := sessionmemory.CanonicalForgetOperationID(sessionmemory.ForgetKindScope, command.Scope, sessionmemory.SourceRef{}, command.RequestID)
	if err != nil {
		return sessionmemory.ForgetOutcome{}, err
	}
	fingerprint := sessionmemory.CanonicalForgetFingerprint(opID, command.Scope, sessionmemory.ForgetKindScope, sessionmemory.SourceRef{}, command.RequestID)
	if prior, found, lookupErr := s.loadOutcome(ctx, command.Scope, opID, sessionmemory.ForgetKindScope); lookupErr != nil {
		return sessionmemory.ForgetOutcome{}, lookupErr
	} else if found {
		return prior, nil
	}
	if s.enumerator == nil {
		return sessionmemory.ForgetOutcome{}, sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "canonical scope forget enumeration is unavailable", nil)
	}
	sources, err := s.listSources(ctx, command.Scope)
	if err != nil {
		return sessionmemory.ForgetOutcome{}, err
	}
	allRevisionRefs, err := s.listRevisions(ctx, command.Scope)
	if err != nil {
		return sessionmemory.ForgetOutcome{}, err
	}
	if len(sources)+len(allRevisionRefs) > sessionmemory.MaxSnapshotItems {
		return sessionmemory.ForgetOutcome{}, sessionmemory.PermanentError(sessionmemory.CodeLimitExceeded, "canonical scope forget closure exceeds the bounded outcome", nil)
	}
	deniedSources := make([]string, 0, len(sources))
	revisionIDs := make([]string, 0, len(allRevisionRefs))
	for _, source := range sources {
		sourceID, resolveErr := s.sourceID(ctx, source)
		if resolveErr != nil {
			return sessionmemory.ForgetOutcome{}, resolveErr
		}
		if err := s.store.DenySource(ctx, command.Scope, sourceID, command.ForgottenAt.UTC()); err != nil {
			return sessionmemory.ForgetOutcome{}, err
		}
		deniedSources = append(deniedSources, sourceID)
		ids, denyErr := s.denySourceRevisions(ctx, command.Scope, sourceID, command.ForgottenAt.UTC())
		if denyErr != nil {
			return sessionmemory.ForgetOutcome{}, denyErr
		}
		revisionIDs = append(revisionIDs, ids...)
	}
	for _, ref := range allRevisionRefs {
		if err := s.store.DenyRevision(ctx, command.Scope, ref.RevisionID, command.ForgottenAt.UTC()); err != nil {
			return sessionmemory.ForgetOutcome{}, err
		}
		revisionIDs = appendUniqueString(revisionIDs, ref.RevisionID)
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i].ExportID < sources[j].ExportID })
	sort.Slice(allRevisionRefs, func(i, j int) bool { return allRevisionRefs[i].RevisionID < allRevisionRefs[j].RevisionID })
	expectedScopeVersion, err := s.scopeVersion(ctx, command.Scope)
	if err != nil {
		return sessionmemory.ForgetOutcome{}, err
	}
	return s.commit(ctx, sessionmemory.CanonicalForgetCommitRequest{
		Scope: command.Scope, OperationID: opID, Kind: sessionmemory.ForgetKindScope,
		Fingerprint: fingerprint, ExpectedScopeVersion: expectedScopeVersion,
		Sources: sources, Revisions: allRevisionRefs, ForgottenAt: command.ForgottenAt.UTC(),
	}, command.Scope, deniedSources, revisionIDs)
}

func (s *CanonicalForgetService) loadOutcome(ctx context.Context, scope sessionmemory.Scope, operationID string, kind sessionmemory.ForgetKind) (sessionmemory.ForgetOutcome, bool, error) {
	if s.outcomes == nil {
		return sessionmemory.ForgetOutcome{}, false, nil
	}
	outcome, found, err := s.outcomes.LoadCanonicalForgetOutcome(ctx, scope, operationID, kind)
	if err != nil {
		return sessionmemory.ForgetOutcome{}, false, err
	}
	if found {
		if err := outcome.Validate(); err != nil {
			return sessionmemory.ForgetOutcome{}, false, err
		}
		if outcome.Scope != scope || outcome.OperationID != operationID || outcome.Kind != kind {
			return sessionmemory.ForgetOutcome{}, false, sessionmemory.PermanentError(sessionmemory.CodeScopeViolation, "canonical forget outcome does not match lookup", nil)
		}
	}
	return outcome, found, nil
}

func (s *CanonicalForgetService) commit(ctx context.Context, request sessionmemory.CanonicalForgetCommitRequest, scope sessionmemory.Scope, sourceIDs, revisionIDs []string) (sessionmemory.ForgetOutcome, error) {
	if s.outcomes != nil {
		outcome, err := s.outcomes.CommitCanonicalForget(ctx, request)
		if err != nil {
			return sessionmemory.ForgetOutcome{}, err
		}
		if err := outcome.Validate(); err != nil {
			return sessionmemory.ForgetOutcome{}, err
		}
		if outcome.Scope != request.Scope || outcome.OperationID != request.OperationID || outcome.Kind != request.Kind {
			return sessionmemory.ForgetOutcome{}, sessionmemory.PermanentError(sessionmemory.CodeScopeViolation, "canonical forget outcome does not match commit", nil)
		}
		if s.scrubber != nil {
			_ = s.scrubber.ScrubCanonicalForget(ctx, scope, sourceIDs, revisionIDs)
		}
		return outcome, nil
	}
	version := request.ExpectedScopeVersion
	if version == 0 {
		version = 1
	}
	outcome := sessionmemory.ForgetOutcome{SchemaVersion: sessionmemory.DerivedSchemaVersionV1, OperationID: request.OperationID, Kind: request.Kind, Scope: request.Scope, ScopeVersion: version, Sources: append([]sessionmemory.SourceRef(nil), request.Sources...), Revisions: append([]sessionmemory.RevisionRef(nil), request.Revisions...)}
	if err := outcome.Validate(); err != nil {
		return sessionmemory.ForgetOutcome{}, err
	}
	if s.scrubber != nil {
		_ = s.scrubber.ScrubCanonicalForget(ctx, scope, sourceIDs, revisionIDs)
	}
	return outcome, nil
}

func (s *CanonicalForgetService) denySourceRevisions(ctx context.Context, scope sessionmemory.Scope, sourceID string, deniedAt time.Time) ([]string, error) {
	var after string
	var all []string
	for {
		batch, err := sessionmemory.DenyCanonicalSourceBatch(ctx, s.store, sessionmemory.CanonicalSourceForgetBatchRequest{Scope: scope, SourceID: sourceID, AfterRevisionID: after, Limit: canonicalForgetPage, DeniedAt: deniedAt})
		if err != nil {
			return nil, err
		}
		for _, revisionID := range batch.RevisionIDs {
			all = appendUniqueString(all, revisionID)
		}
		if batch.NextCursor == "" || batch.NextCursor == after {
			return all, nil
		}
		after = batch.NextCursor
	}
}

func (s *CanonicalForgetService) sourceExists(ctx context.Context, want sessionmemory.SourceRef) (bool, error) {
	sources, err := s.listSources(ctx, want.Scope)
	if err != nil {
		return false, err
	}
	for _, source := range sources {
		if source == want {
			return true, nil
		}
	}
	return false, nil
}

func (s *CanonicalForgetService) listSources(ctx context.Context, scope sessionmemory.Scope) ([]sessionmemory.SourceRef, error) {
	var after string
	var result []sessionmemory.SourceRef
	for {
		page, next, err := s.enumerator.ListCanonicalSourceRefs(ctx, scope, after, canonicalForgetPage)
		if err != nil {
			return nil, err
		}
		if len(result)+len(page) > sessionmemory.MaxSnapshotItems {
			return nil, sessionmemory.PermanentError(sessionmemory.CodeLimitExceeded, "canonical source enumeration exceeds the bounded outcome", nil)
		}
		result = append(result, page...)
		if next == "" || next == after {
			return result, nil
		}
		after = next
	}
}

func (s *CanonicalForgetService) listRevisions(ctx context.Context, scope sessionmemory.Scope) ([]sessionmemory.RevisionRef, error) {
	var after string
	var result []sessionmemory.RevisionRef
	for {
		page, next, err := s.enumerator.ListCanonicalRevisionRefs(ctx, scope, after, canonicalForgetPage)
		if err != nil {
			return nil, err
		}
		if len(result)+len(page) > sessionmemory.MaxSnapshotItems {
			return nil, sessionmemory.PermanentError(sessionmemory.CodeLimitExceeded, "canonical revision enumeration exceeds the bounded outcome", nil)
		}
		result = append(result, page...)
		if next == "" || next == after {
			return result, nil
		}
		after = next
	}
}

func (s *CanonicalForgetService) revisionRefsForIDs(ctx context.Context, scope sessionmemory.Scope, ids []string) ([]sessionmemory.RevisionRef, error) {
	all, err := s.listRevisions(ctx, scope)
	if err != nil {
		return nil, err
	}
	want := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		want[id] = struct{}{}
	}
	refs := make([]sessionmemory.RevisionRef, 0, len(ids))
	for _, ref := range all {
		if _, ok := want[ref.RevisionID]; ok {
			refs = append(refs, ref)
		}
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].RevisionID < refs[j].RevisionID })
	return refs, nil
}

func (s *CanonicalForgetService) scopeVersion(ctx context.Context, scope sessionmemory.Scope) (uint64, error) {
	state, err := s.canonical.LoadScopeState(ctx, scope)
	if err != nil {
		return 0, err
	}
	return state.Version, nil
}

func (s *CanonicalForgetService) sourceID(ctx context.Context, source sessionmemory.SourceRef) (string, error) {
	if resolver, ok := s.enumerator.(sessionmemory.CanonicalSourceIdentityResolver); ok {
		return resolver.CanonicalSourceID(ctx, source.Scope, source)
	}
	return sessionmemory.TurnSourceID(source.ExportID), nil
}

func appendUniqueString(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}
