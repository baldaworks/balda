package sessionmemory_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/normahq/balda/sessionmemory"
)

func TestPublicPackageBuildsPortableTurn(t *testing.T) {
	t.Parallel()

	scope := sessionmemory.Scope{
		Key:  "telegram:1:0",
		Kind: sessionmemory.ScopeKindPersonal,
	}
	session := sessionmemory.SessionRef{
		SessionID:      "session-1",
		AgentSessionID: "agent-session-1",
	}

	turn, err := sessionmemory.NewTurn(
		scope,
		session,
		"turn-1",
		time.Date(2026, time.August, 3, 0, 0, 0, 0, time.UTC),
		"user message",
		"assistant message",
	)
	if err != nil {
		t.Fatalf("sessionmemory.NewTurn() error = %v", err)
	}
	if err := turn.Validate(); err != nil {
		t.Fatalf("Turn.Validate() error = %v", err)
	}
}

func TestPublicDerivedAtomRoundTrip(t *testing.T) {
	t.Parallel()

	scope := sessionmemory.Scope{Key: "telegram:1:0", Kind: sessionmemory.ScopeKindPersonal}
	source := sessionmemory.SourceRef{
		Scope:        scope,
		ExportID:     "export-1",
		SessionID:    "session-1",
		SourceTurnID: "turn-1",
	}
	provenance := sessionmemory.Provenance{RawSources: []sessionmemory.SourceRef{source}}
	itemID, err := sessionmemory.AtomItemID(scope, sessionmemory.AtomCategoryFact, "The project uses Go")
	if err != nil {
		t.Fatalf("sessionmemory.AtomItemID() error = %v", err)
	}
	revisionID, err := sessionmemory.DerivedRevisionID(
		scope,
		itemID,
		"operation-1",
		[]string{string(sessionmemory.AtomCategoryFact), "The project uses Go"},
		provenance,
		nil,
	)
	if err != nil {
		t.Fatalf("sessionmemory.DerivedRevisionID() error = %v", err)
	}
	want := sessionmemory.Atom{
		Meta: sessionmemory.RevisionMeta{
			SchemaVersion: sessionmemory.DerivedSchemaVersionV1,
			Kind:          sessionmemory.DerivedKindAtom,
			ItemID:        itemID,
			RevisionID:    revisionID,
			Revision:      1,
			OperationID:   "operation-1",
			Scope:         scope,
			State:         sessionmemory.RevisionStateActive,
			Provenance:    provenance,
			CreatedAt:     time.Date(2026, time.August, 3, 0, 0, 0, 0, time.UTC),
		},
		Category: sessionmemory.AtomCategoryFact,
		Text:     "The project uses Go",
	}
	payload, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	var got sessionmemory.Atom
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("round-tripped Atom.Validate() error = %v", err)
	}
}
