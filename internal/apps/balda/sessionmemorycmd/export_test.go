package sessionmemorycmd

import (
	"testing"
	"time"

	"github.com/baldaworks/balda/sessionmemory"
)

func TestExportRoundTrip(t *testing.T) {
	t.Parallel()

	scope := sessionmemory.Scope{Key: "telegram:1:0", Kind: sessionmemory.ScopeKindPersonal}
	session := sessionmemory.SessionRef{SessionID: "tg-1-0", AgentSessionID: "tg-1-0"}
	turn, err := sessionmemory.NewTurn(scope, session, "turn-1", time.Date(2026, 8, 3, 1, 2, 3, 0, time.UTC), "hello", "hi")
	if err != nil {
		t.Fatalf("NewTurn() error = %v", err)
	}
	export, err := NewTurn(turn)
	if err != nil {
		t.Fatalf("NewTurn envelope error = %v", err)
	}
	data, err := Marshal(export)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	decoded, err := Unmarshal(data)
	if err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if decoded.ExportID() != turn.ExportID || decoded.Subject() != SubjectTurn {
		t.Fatalf("decoded identity = %q, subject = %q", decoded.ExportID(), decoded.Subject())
	}
	if decoded.Turn == nil || decoded.Turn.Messages[1].Text != "hi" {
		t.Fatalf("decoded turn = %+v", decoded.Turn)
	}
	if !decoded.Turn.CompletedAt.Equal(turn.CompletedAt) {
		t.Fatalf("decoded completed_at = %s, want %s", decoded.Turn.CompletedAt, turn.CompletedAt)
	}
}

func TestBoundaryExportRoundTrip(t *testing.T) {
	t.Parallel()

	scope := sessionmemory.Scope{Key: "telegram:-100:42", Kind: sessionmemory.ScopeKindGroup}
	session := sessionmemory.SessionRef{SessionID: "tg--100-42", AgentSessionID: "tg--100-42"}
	boundary, err := sessionmemory.NewBoundary(scope, session, "shutdown-1", sessionmemory.BoundaryReasonShutdown, time.Date(2026, 8, 3, 1, 2, 3, 0, time.UTC))
	if err != nil {
		t.Fatalf("NewBoundary() error = %v", err)
	}
	export, err := NewBoundary(boundary)
	if err != nil {
		t.Fatalf("NewBoundary envelope error = %v", err)
	}
	data, err := Marshal(export)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	decoded, err := Unmarshal(data)
	if err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if decoded.ExportID() != boundary.ExportID || decoded.Subject() != SubjectBoundary {
		t.Fatalf("decoded identity = %q, subject = %q", decoded.ExportID(), decoded.Subject())
	}
	if decoded.Boundary == nil || decoded.Boundary.Reason != sessionmemory.BoundaryReasonShutdown ||
		!decoded.Boundary.OccurredAt.Equal(boundary.OccurredAt) {
		t.Fatalf("decoded boundary = %+v", decoded.Boundary)
	}
}

func TestExportValidateRejectsMixedAndUnknownVariants(t *testing.T) {
	t.Parallel()

	scope := sessionmemory.Scope{Key: "telegram:1:0", Kind: sessionmemory.ScopeKindPersonal}
	session := sessionmemory.SessionRef{SessionID: "tg-1-0", AgentSessionID: "tg-1-0"}
	turn, err := sessionmemory.NewTurn(scope, session, "turn-1", time.Now().UTC(), "hello", "hi")
	if err != nil {
		t.Fatalf("NewTurn() error = %v", err)
	}
	boundary, err := sessionmemory.NewBoundary(scope, session, "close-1", sessionmemory.BoundaryReasonClose, time.Now().UTC())
	if err != nil {
		t.Fatalf("NewBoundary() error = %v", err)
	}
	tests := []Export{
		{SchemaVersion: sessionmemory.SchemaVersionV1, Kind: KindTurn, Turn: &turn, Boundary: &boundary},
		{SchemaVersion: sessionmemory.SchemaVersionV1, Kind: "unknown", Turn: &turn},
		{SchemaVersion: "v2", Kind: KindTurn, Turn: &turn},
	}
	for _, export := range tests {
		if err := export.Validate(); err == nil {
			t.Fatalf("Validate(%+v) error = nil, want error", export)
		}
	}
}
