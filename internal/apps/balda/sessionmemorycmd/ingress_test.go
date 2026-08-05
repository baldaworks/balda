package sessionmemorycmd

import (
	"testing"
	"time"

	"github.com/normahq/balda/sessionmemory"
)

func TestNewIngressRecordUsesExportIdentityAndScope(t *testing.T) {
	t.Parallel()
	turn, err := sessionmemory.NewTurn(
		sessionmemory.Scope{Key: "telegram:1:0", Kind: sessionmemory.ScopeKindPersonal},
		sessionmemory.SessionRef{SessionID: "session-1", AgentSessionID: "session-1"},
		"turn-1", time.Date(2026, time.August, 5, 12, 0, 0, 0, time.UTC), "hello", "hi",
	)
	if err != nil {
		t.Fatalf("NewTurn() error = %v", err)
	}
	export, err := NewTurn(turn)
	if err != nil {
		t.Fatalf("NewTurn export error = %v", err)
	}
	record, err := NewIngressRecord(export, turn.CompletedAt)
	if err != nil {
		t.Fatalf("NewIngressRecord() error = %v", err)
	}
	record.ScopeSequence = 1
	if err := record.Validate(); err != nil {
		t.Fatalf("IngressRecord.Validate() error = %v", err)
	}
	scope, err := record.Scope()
	if err != nil || scope != turn.Scope || record.ExportID() != turn.ExportID {
		t.Fatalf("record identity = scope %#v, export %q, error %v", scope, record.ExportID(), err)
	}
}

func TestIngressRecordValidationRejectsLifecycleLeaks(t *testing.T) {
	t.Parallel()
	turn, err := sessionmemory.NewTurn(
		sessionmemory.Scope{Key: "telegram:1:0", Kind: sessionmemory.ScopeKindPersonal},
		sessionmemory.SessionRef{SessionID: "session-1", AgentSessionID: "session-1"},
		"turn-1", time.Now().UTC(), "hello", "hi",
	)
	if err != nil {
		t.Fatalf("NewTurn() error = %v", err)
	}
	export, err := NewTurn(turn)
	if err != nil {
		t.Fatalf("NewTurn export error = %v", err)
	}
	record, err := NewIngressRecord(export, time.Now().UTC())
	if err != nil {
		t.Fatalf("NewIngressRecord() error = %v", err)
	}
	record.ScopeSequence = 1
	record.LeaseOwner = "worker-1"
	if err := record.Validate(); err == nil {
		t.Fatal("pending record with a lease validated")
	}
}
