package sessionmemory

import (
	"testing"
	"time"
)

func TestPolicyRegistryReconcilesStateParaphrases(t *testing.T) {
	t.Parallel()

	scope := Scope{Key: "telegram:1:0", Kind: ScopeKindPersonal}
	policy := PolicyRegistry{Version: "policy-v1"}
	first, err := policy.Reconcile(scope, reconciliationCandidate(MemoryKindState, "Ada", "Lives In", []string{"Bishkek"}), nil)
	if err != nil {
		t.Fatalf("first Reconcile() error = %v", err)
	}
	second, err := policy.Reconcile(scope, reconciliationCandidate(MemoryKindState, " ada ", "lives   in", []string{"bishkek"}), []MemoryItem{first.Item})
	if err != nil {
		t.Fatalf("second Reconcile() error = %v", err)
	}
	if second.Action != ReconciliationActionSupersede || second.Item.ItemID != first.Item.ItemID || second.Lifecycle.Type != LifecycleEventSupersede {
		t.Fatalf("state reconciliation = %+v", second)
	}
}

func TestPolicyRegistryRetainsEventIdentityAcrossEdit(t *testing.T) {
	t.Parallel()

	scope := Scope{Key: "telegram:1:0", Kind: ScopeKindPersonal}
	policy := PolicyRegistry{Version: "policy-v1"}
	candidate := reconciliationCandidate(MemoryKindEvent, "Ada", "Visited", []string{"Bishkek"})
	first, err := policy.Reconcile(scope, candidate, nil)
	if err != nil {
		t.Fatalf("first Reconcile() error = %v", err)
	}
	edited := candidate
	edited.Predicate = "Visited Again"
	second, err := policy.ReconcileActive(scope, edited, []ActiveMemoryItem{{Item: first.Item, Evidence: candidate.Memory.Evidence}})
	if err != nil {
		t.Fatalf("edited ReconcileActive() error = %v", err)
	}
	if second.Action != ReconciliationActionSupersede || second.Item.ItemID != first.Item.ItemID {
		t.Fatalf("event edit reconciliation = %+v", second)
	}
}

func TestPolicyRegistryRejectsForeignAndAmbiguousActiveItems(t *testing.T) {
	t.Parallel()

	policy := PolicyRegistry{Version: "policy-v1"}
	candidate := reconciliationCandidate(MemoryKindState, "Ada", "Lives In", []string{"Bishkek"})
	scope := Scope{Key: "telegram:1:0", Kind: ScopeKindPersonal}
	foreign := MemoryItem{ItemID: "item-1", Scope: Scope{Key: "telegram:2:0", Kind: ScopeKindPersonal}, Kind: MemoryKindState, MemoryKey: "key-1"}
	if _, err := policy.Reconcile(scope, candidate, []MemoryItem{foreign}); err == nil {
		t.Fatal("foreign active item was accepted")
	}
	first, err := policy.Reconcile(scope, candidate, nil)
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if _, err := policy.Reconcile(scope, candidate, []MemoryItem{first.Item, first.Item}); err == nil {
		t.Fatal("ambiguous active state matches were accepted")
	}
}

func TestPolicyRegistryRejectsInvalidEvidenceAndTime(t *testing.T) {
	t.Parallel()

	policy := PolicyRegistry{Version: "policy-v1"}
	scope := Scope{Key: "telegram:1:0", Kind: ScopeKindPersonal}
	candidate := reconciliationCandidate(MemoryKindState, "Ada", "Lives In", []string{"Bishkek"})
	candidate.Memory.Evidence[0].Role = MessageRoleAssistant
	if _, err := policy.Reconcile(scope, candidate, nil); err == nil {
		t.Fatal("assistant-only evidence was accepted")
	}
	candidate = reconciliationCandidate(MemoryKindState, "Ada", "Lives In", []string{"Bishkek"})
	from := candidate.Memory.Temporal.ObservedAt.Add(time.Hour)
	until := candidate.Memory.Temporal.ObservedAt
	candidate.Memory.Temporal.ValidFrom = &from
	candidate.Memory.Temporal.ValidUntil = &until
	if _, err := policy.Reconcile(scope, candidate, nil); err == nil {
		t.Fatal("inverted temporal interval was accepted")
	}
}

func TestPolicyRegistryIsRepeatable(t *testing.T) {
	t.Parallel()

	policy := PolicyRegistry{Version: "policy-v1"}
	scope := Scope{Key: "telegram:1:0", Kind: ScopeKindPersonal}
	candidate := reconciliationCandidate(MemoryKindState, "Ada", "Lives In", []string{"Bishkek"})
	first, err := policy.Reconcile(scope, candidate, nil)
	if err != nil {
		t.Fatalf("first Reconcile() error = %v", err)
	}
	second, err := policy.Reconcile(scope, candidate, nil)
	if err != nil {
		t.Fatalf("second Reconcile() error = %v", err)
	}
	if first.Item != second.Item || first.RevisionID != second.RevisionID || first.Lifecycle != second.Lifecycle {
		t.Fatalf("reconciliation is not repeatable: %+v != %+v", first, second)
	}
}

func reconciliationCandidate(kind MemoryKind, subject, predicate string, qualifiers []string) SemanticCandidate {
	return SemanticCandidate{
		Kind: kind, Subject: subject, Predicate: predicate, Qualifiers: qualifiers,
		Memory: MemoryCandidate{
			Kind: kind, Statement: "candidate", Temporal: Temporal{ObservedAt: time.Date(2026, time.August, 5, 0, 0, 0, 0, time.UTC)},
			Evidence:    []EvidenceRef{{SourceID: "source-1", MessageID: "message-1", Role: MessageRoleUser, StartByte: 1, EndByte: 2, AssertionMode: AssertionModeUser}},
			Sensitivity: SensitivityStandard, Retention: RetentionClassStandard,
		},
	}
}
