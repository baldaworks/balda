package sessionmemory

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"
)

// ReconciliationAction describes the application-owned mutation selected for a candidate.
type ReconciliationAction string

const (
	ReconciliationActionCreate    ReconciliationAction = "create"
	ReconciliationActionSupersede ReconciliationAction = "supersede"
)

// SemanticCandidate is untrusted semantic output. It intentionally carries no
// persistent key, item ID, scope, revision, or lifecycle state.
type SemanticCandidate struct {
	Kind       MemoryKind
	Subject    string
	Predicate  string
	Qualifiers []string
	Memory     MemoryCandidate
}

// PolicyRegistry canonicalizes the finite semantic vocabulary accepted by the
// portable reconciler. It is constructed by the application, not the model.
type PolicyRegistry struct {
	Version string
}

// Reconciliation is the deterministic, application-owned result of one candidate.
type Reconciliation struct {
	Action     ReconciliationAction
	Item       MemoryItem
	Supersedes *MemoryItem
	RevisionID string
	Provenance []EvidenceRef
	Lifecycle  LifecycleEvent
}

// ActiveMemoryItem is the bounded canonical context supplied by the store.
// Evidence is used only to preserve identity through an edit of one event.
type ActiveMemoryItem struct {
	Item     MemoryItem
	Evidence []EvidenceRef
}

// Reconcile validates one exact-scope candidate against a bounded active view.
func (p PolicyRegistry) Reconcile(scope Scope, candidate SemanticCandidate, active []MemoryItem) (Reconciliation, error) {
	view := make([]ActiveMemoryItem, len(active))
	for index, item := range active {
		view[index].Item = item
	}
	return p.ReconcileActive(scope, candidate, view)
}

// ReconcileActive validates one exact-scope candidate against bounded canonical context.
func (p PolicyRegistry) ReconcileActive(scope Scope, candidate SemanticCandidate, active []ActiveMemoryItem) (Reconciliation, error) {
	if err := scope.Validate(); err != nil {
		return Reconciliation{}, err
	}
	if !isCanonicalID(p.Version) {
		return Reconciliation{}, invalidDerived("reconciliation policy version is required")
	}
	if err := candidate.Memory.Validate(); err != nil {
		return Reconciliation{}, err
	}
	if candidate.Kind != candidate.Memory.Kind {
		return Reconciliation{}, invalidDerived("semantic candidate kind does not match memory candidate")
	}
	subject, err := canonicalSemanticPart(candidate.Subject)
	if err != nil {
		return Reconciliation{}, err
	}
	predicate, err := canonicalSemanticPart(candidate.Predicate)
	if err != nil {
		return Reconciliation{}, err
	}
	qualifiers := make([]string, len(candidate.Qualifiers))
	for index, qualifier := range candidate.Qualifiers {
		qualifiers[index], err = canonicalSemanticPart(qualifier)
		if err != nil {
			return Reconciliation{}, err
		}
	}
	if len(active) > MaxSnapshotItems {
		return Reconciliation{}, limitExceeded("reconciliation active view exceeds the limit")
	}
	item := MemoryItem{Scope: scope, Kind: candidate.Kind}
	if candidate.Kind == MemoryKindState {
		item.MemoryKey = MemoryKey(reconciliationID("key", p.Version, scope.Key, subject, predicate, strings.Join(qualifiers, "\x00")))
		item.ItemID = reconciliationID("item", scope.Key, string(item.MemoryKey))
	} else {
		eventAt := ""
		if candidate.Memory.Temporal.EventAt != nil {
			eventAt = candidate.Memory.Temporal.EventAt.UTC().Format(time.RFC3339Nano)
		}
		item.ItemID = reconciliationID("event", scope.Key, candidate.Memory.Evidence[0].SourceID, candidate.Memory.Evidence[0].MessageID, eventAt, subject, predicate, strings.Join(qualifiers, "\x00"))
	}
	if err := item.Validate(); err != nil {
		return Reconciliation{}, err
	}
	var match *MemoryItem
	for _, existing := range active {
		if err := existing.Item.Validate(); err != nil || existing.Item.Scope != scope {
			return Reconciliation{}, PermanentError(CodeScopeViolation, "reconciliation active item is invalid or foreign", nil)
		}
		if candidate.Kind == MemoryKindState && existing.Item.MemoryKey == item.MemoryKey {
			if match != nil {
				return Reconciliation{}, invalidDerived("reconciliation has ambiguous active state matches")
			}
			matched := existing.Item
			match = &matched
		}
		if candidate.Kind == MemoryKindEvent && sameEventEvidence(existing.Evidence, candidate.Memory.Evidence) {
			if match != nil {
				return Reconciliation{}, invalidDerived("reconciliation has ambiguous active event matches")
			}
			item.ItemID = existing.Item.ItemID
			matched := existing.Item
			match = &matched
		}
	}
	revisionID := reconciliationID("revision", item.ItemID, candidate.Memory.Evidence[0].SourceID, candidate.Memory.Evidence[0].MessageID, candidate.Memory.Statement)
	action := ReconciliationActionCreate
	if match != nil {
		action = ReconciliationActionSupersede
	}
	lifecycle := LifecycleEvent{EventID: reconciliationID("lifecycle", revisionID, string(action)), RevisionID: revisionID, OccurredAt: candidate.Memory.Temporal.ObservedAt}
	if action == ReconciliationActionCreate {
		lifecycle.Type = LifecycleEventActivate
	} else {
		lifecycle.Type = LifecycleEventSupersede
	}
	if err := lifecycle.Validate(); err != nil {
		return Reconciliation{}, err
	}
	if match == nil {
		return Reconciliation{Action: action, Item: item, RevisionID: revisionID, Provenance: append([]EvidenceRef(nil), candidate.Memory.Evidence...), Lifecycle: lifecycle}, nil
	}
	return Reconciliation{Action: action, Item: item, Supersedes: match, RevisionID: revisionID, Provenance: append([]EvidenceRef(nil), candidate.Memory.Evidence...), Lifecycle: lifecycle}, nil
}

func sameEventEvidence(left, right []EvidenceRef) bool {
	for _, a := range left {
		for _, b := range right {
			if a.SourceID == b.SourceID && a.MessageID == b.MessageID {
				return true
			}
		}
	}
	return false
}

func canonicalSemanticPart(value string) (string, error) {
	value = strings.ToLower(strings.Join(strings.Fields(value), " "))
	if value == "" || len(value) > maxMemoryTextBytes {
		return "", invalidDerived("semantic candidate part is invalid")
	}
	return value, nil
}

func reconciliationID(kind string, parts ...string) string {
	hash := sha256.New()
	writeHashPart(hash, MemorySchemaVersionV2)
	writeHashPart(hash, kind)
	for _, part := range parts {
		writeHashPart(hash, part)
	}
	return "session-memory:v2:" + kind + ":" + hex.EncodeToString(hash.Sum(nil))
}
