package sessionmemoryapp

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/normahq/balda/internal/apps/balda/deliverycmd"
	"github.com/normahq/balda/internal/apps/balda/sessionmemorycmd"
	"github.com/normahq/balda/sessionmemory"
)

type capturePublisher struct {
	exports []sessionmemorycmd.Export
	err     error
}

func (p *capturePublisher) Publish(_ context.Context, export sessionmemorycmd.Export) error {
	p.exports = append(p.exports, export)
	return p.err
}

func testCaptureLocator(t *testing.T, channelType, addressKey, sessionID string) deliverycmd.Locator {
	t.Helper()
	locator, err := deliverycmd.NewLocator(channelType, addressKey, `{"kind":"test"}`, sessionID)
	if err != nil {
		t.Fatalf("NewLocator() error = %v", err)
	}
	return locator
}

func TestTurnCapturePublishesEligibleTurnWithStableIdentity(t *testing.T) {
	t.Parallel()

	publisher := &capturePublisher{}
	resolver := NewScopeResolver(map[string]ScopeClassifier{
		"telegram": func(deliverycmd.Locator) (deliverycmd.LocatorScopeKind, error) {
			return deliverycmd.LocatorScopePersonal, nil
		},
	})
	capture := NewTurnCapture(publisher, resolver)
	capture.now = func() time.Time { return time.Date(2026, 8, 3, 4, 5, 6, 0, time.UTC) }
	locator := testCaptureLocator(t, "telegram", "123:0", "tg-123-0")
	req := CaptureRequest{
		UserText:       " hello ",
		AssistantText:  " world ",
		Locator:        locator,
		AgentSessionID: "adk-42",
		SourceTurnID:   "telegram:message:9",
	}

	result, err := capture.Capture(context.Background(), req)
	if err != nil {
		t.Fatalf("Capture() error = %v", err)
	}
	if !result.Attempted || result.ExportID == "" {
		t.Fatalf("Capture() result = %+v, want attempted export", result)
	}
	if len(publisher.exports) != 1 {
		t.Fatalf("published exports = %d, want 1", len(publisher.exports))
	}
	turn := publisher.exports[0].Turn
	if turn == nil {
		t.Fatal("published turn is nil")
	}
	if turn.Scope.Key != "telegram:123:0" || turn.Scope.Kind != sessionmemory.ScopeKindPersonal {
		t.Fatalf("scope = %+v", turn.Scope)
	}
	if turn.Session.SessionID != "tg-123-0" || turn.Session.AgentSessionID != "adk-42" {
		t.Fatalf("session = %+v", turn.Session)
	}
	if turn.SourceTurnID != req.SourceTurnID || turn.Messages[0].Text != "hello" || turn.Messages[1].Text != "world" {
		t.Fatalf("turn = %+v", *turn)
	}
	if !turn.CompletedAt.Equal(time.Date(2026, 8, 3, 4, 5, 6, 0, time.UTC)) {
		t.Fatalf("completed_at = %s", turn.CompletedAt)
	}

	second, err := capture.Capture(context.Background(), req)
	if err != nil {
		t.Fatalf("second Capture() error = %v", err)
	}
	if second.ExportID != result.ExportID {
		t.Fatalf("second export id = %q, want %q", second.ExportID, result.ExportID)
	}
}

func TestTurnCaptureExcludesIneligibleTextAndAmbiguousScope(t *testing.T) {
	t.Parallel()

	publisher := &capturePublisher{}
	resolver := NewScopeResolver(map[string]ScopeClassifier{
		"telegram": func(deliverycmd.Locator) (deliverycmd.LocatorScopeKind, error) {
			return "", errors.New("ambiguous")
		},
	})
	capture := NewTurnCapture(publisher, resolver)
	locator := testCaptureLocator(t, "telegram", "123:0", "tg-123-0")

	for _, req := range []CaptureRequest{{AssistantText: "answer", Locator: locator, SourceTurnID: "turn-1"}} {
		result, err := capture.Capture(context.Background(), req)
		if err != nil {
			t.Fatalf("ineligible Capture() error = %v", err)
		}
		if result.Attempted {
			t.Fatalf("ineligible result = %+v, want no attempt", result)
		}
	}
	_, err := capture.Capture(context.Background(), CaptureRequest{
		UserText:      "question",
		AssistantText: "answer",
		Locator:       locator,
		SourceTurnID:  "turn-3",
	})
	if err == nil {
		t.Fatal("ambiguous Capture() error = nil")
	}
	if code, _, ok := sessionmemory.ClassifyError(err); !ok || code != sessionmemory.CodeUnsupportedScope {
		t.Fatalf("ambiguous Capture() error = %v, want unsupported_scope", err)
	}
	if len(publisher.exports) != 0 {
		t.Fatalf("published exports = %d, want 0", len(publisher.exports))
	}
}

func TestTurnCapturePublishesUserOnlyTerminalTurn(t *testing.T) {
	publisher := &capturePublisher{}
	resolver := NewScopeResolver(map[string]ScopeClassifier{
		"telegram": func(deliverycmd.Locator) (deliverycmd.LocatorScopeKind, error) {
			return deliverycmd.LocatorScopePersonal, nil
		},
	})
	capture := NewTurnCapture(publisher, resolver)
	result, err := capture.Capture(context.Background(), CaptureRequest{
		UserText: "question", Locator: testCaptureLocator(t, "telegram", "123:0", "tg-123-0"), SourceTurnID: "turn-failed", TerminalStatus: sessionmemory.TurnTerminalStatusFailed,
	})
	if err != nil || !result.Attempted || len(publisher.exports) != 1 {
		t.Fatalf("Capture() = %+v, %v; exports = %d", result, err, len(publisher.exports))
	}
	if turn := publisher.exports[0].Turn; turn == nil || len(turn.Messages) != 1 || turn.Messages[0].Role != sessionmemory.MessageRoleUser {
		t.Fatalf("user-only turn = %+v", turn)
	}
}

func TestTurnCapturePersistsOnlyExplicitlyTrustedToolEvidence(t *testing.T) {
	t.Parallel()
	policy, err := NewTrustedToolPolicy([]string{"calendar.lookup"})
	if err != nil {
		t.Fatalf("NewTrustedToolPolicy() error = %v", err)
	}
	publisher := &capturePublisher{}
	resolver := NewScopeResolver(map[string]ScopeClassifier{
		"telegram": func(deliverycmd.Locator) (deliverycmd.LocatorScopeKind, error) {
			return deliverycmd.LocatorScopePersonal, nil
		},
	})
	capture := NewTurnCaptureWithToolPolicy(publisher, resolver, policy)
	_, err = capture.Capture(context.Background(), CaptureRequest{
		UserText: "question", AssistantText: "answer", Locator: testCaptureLocator(t, "telegram", "123:0", "tg-123-0"), SourceTurnID: "turn-tools",
		TrustedTools: []TrustedToolEvidence{{Name: "calendar.lookup", CallID: "call-1", Text: "2026-08-06"}, {Name: "untrusted.tool", CallID: "call-2", Text: "must not persist"}},
	})
	if err != nil {
		t.Fatalf("Capture() error = %v", err)
	}
	if len(publisher.exports) != 1 || publisher.exports[0].Turn == nil {
		t.Fatalf("exports = %#v", publisher.exports)
	}
	messages := publisher.exports[0].Turn.Messages
	if len(messages) != 3 || messages[2].Role != sessionmemory.MessageRoleTool || messages[2].ToolName != "calendar.lookup" || messages[2].Text != "2026-08-06" {
		t.Fatalf("captured messages = %#v", messages)
	}
}

func TestTurnCaptureRedactsCredentialShapedTextBeforePublishing(t *testing.T) {
	t.Parallel()
	publisher := &capturePublisher{}
	resolver := NewScopeResolver(map[string]ScopeClassifier{
		"telegram": func(deliverycmd.Locator) (deliverycmd.LocatorScopeKind, error) {
			return deliverycmd.LocatorScopePersonal, nil
		},
	})
	_, err := NewTurnCapture(publisher, resolver).Capture(context.Background(), CaptureRequest{
		UserText: "token=private-value", AssistantText: "Authorization: Bearer private-reply", Locator: testCaptureLocator(t, "telegram", "123:0", "tg-123-0"), SourceTurnID: "turn-redacted",
	})
	if err != nil || len(publisher.exports) != 1 || publisher.exports[0].Turn == nil {
		t.Fatalf("Capture() error = %v, exports = %#v", err, publisher.exports)
	}
	turn := publisher.exports[0].Turn
	if turn.Messages[0].Text != "token=[REDACTED]" || turn.Messages[1].Text != "Authorization: Bearer [REDACTED]" {
		t.Fatalf("captured redacted messages = %#v", turn.Messages)
	}
}

func TestTurnCapturePublishFailureIsReturnedAfterLocalAttempt(t *testing.T) {
	t.Parallel()

	publisher := &capturePublisher{err: errors.New("puback timeout")}
	resolver := NewScopeResolver(map[string]ScopeClassifier{
		"telegram": func(deliverycmd.Locator) (deliverycmd.LocatorScopeKind, error) {
			return deliverycmd.LocatorScopeGroup, nil
		},
	})
	capture := NewTurnCapture(publisher, resolver)
	result, err := capture.Capture(context.Background(), CaptureRequest{
		UserText:       "question",
		AssistantText:  "answer",
		Locator:        testCaptureLocator(t, "telegram", "-100:77", "tg--100-77"),
		SessionID:      "balda-1",
		AgentSessionID: "adk-1",
		SourceTurnID:   "turn-1",
		CompletedAt:    time.Now().UTC(),
	})
	if err == nil {
		t.Fatal("Capture() error = nil, want publish failure")
	}
	if !result.Attempted || len(publisher.exports) != 1 {
		t.Fatalf("result = %+v, published = %d; want attempted one export", result, len(publisher.exports))
	}
}

func TestScopeResolverRejectsUnknownAndNonCanonicalLocators(t *testing.T) {
	t.Parallel()

	resolver := NewScopeResolver(nil)
	unknown := testCaptureLocator(t, "telegram", "123:0", "tg-123-0")
	if _, err := resolver.Resolve(unknown); err == nil {
		t.Fatal("Resolve(unknown) error = nil")
	} else if code, _, ok := sessionmemory.ClassifyError(err); !ok || code != sessionmemory.CodeUnsupportedScope {
		t.Fatalf("Resolve(unknown) error = %v, code = %q", err, code)
	}
	invalid := unknown
	invalid.ChannelType = "Telegram"
	if _, err := NewScopeResolver(map[string]ScopeClassifier{"telegram": func(deliverycmd.Locator) (deliverycmd.LocatorScopeKind, error) {
		return deliverycmd.LocatorScopePersonal, nil
	}}).Resolve(invalid); err == nil {
		t.Fatal("Resolve(non-canonical) error = nil")
	} else if code, _, ok := sessionmemory.ClassifyError(err); !ok || code != sessionmemory.CodeInvalidScope {
		t.Fatalf("Resolve(non-canonical) error = %v, code = %q", err, code)
	}
}

func TestBoundaryCapturePublishesStableBoundary(t *testing.T) {
	t.Parallel()

	publisher := &capturePublisher{}
	resolver := NewScopeResolver(map[string]ScopeClassifier{
		"telegram": func(deliverycmd.Locator) (deliverycmd.LocatorScopeKind, error) {
			return deliverycmd.LocatorScopeGroup, nil
		},
	})
	capture := NewBoundaryCapture(publisher, resolver)
	capture.now = func() time.Time { return time.Date(2026, 8, 3, 4, 5, 6, 0, time.UTC) }
	req := BoundaryCaptureRequest{
		Locator:        testCaptureLocator(t, "telegram", "-100:77", "tg--100-77"),
		AgentSessionID: "adk-42",
		TransitionID:   "shutdown-42",
		Reason:         sessionmemory.BoundaryReasonShutdown,
	}
	result, err := capture.Capture(context.Background(), req)
	if err != nil {
		t.Fatalf("Capture() error = %v", err)
	}
	if !result.Attempted || result.ExportID == "" || len(publisher.exports) != 1 {
		t.Fatalf("result = %+v, exports = %d", result, len(publisher.exports))
	}
	boundary := publisher.exports[0].Boundary
	if boundary == nil {
		t.Fatal("published boundary is nil")
	}
	if boundary.Scope.Key != "telegram:-100:77" || boundary.Scope.Kind != sessionmemory.ScopeKindGroup {
		t.Fatalf("scope = %+v", boundary.Scope)
	}
	if boundary.Session.SessionID != "tg--100-77" || boundary.Session.AgentSessionID != "adk-42" {
		t.Fatalf("session = %+v", boundary.Session)
	}
	if boundary.TransitionID != req.TransitionID || boundary.Reason != req.Reason ||
		!boundary.OccurredAt.Equal(time.Date(2026, 8, 3, 4, 5, 6, 0, time.UTC)) {
		t.Fatalf("boundary = %+v", *boundary)
	}

	second, err := capture.Capture(context.Background(), req)
	if err != nil {
		t.Fatalf("second Capture() error = %v", err)
	}
	if second.ExportID != result.ExportID {
		t.Fatalf("second export id = %q, want %q", second.ExportID, result.ExportID)
	}
}

func TestBoundaryCaptureRejectsInvalidReasonAndReturnsPublishFailure(t *testing.T) {
	t.Parallel()

	resolver := NewScopeResolver(map[string]ScopeClassifier{
		"telegram": func(deliverycmd.Locator) (deliverycmd.LocatorScopeKind, error) {
			return deliverycmd.LocatorScopePersonal, nil
		},
	})
	invalid := NewBoundaryCapture(&capturePublisher{}, resolver)
	_, err := invalid.Capture(context.Background(), BoundaryCaptureRequest{
		Locator:      testCaptureLocator(t, "telegram", "123:0", "tg-123-0"),
		TransitionID: "transition-1",
		Reason:       "unknown",
	})
	if err == nil {
		t.Fatal("invalid reason Capture() error = nil")
	}

	publisher := &capturePublisher{err: errors.New("puback timeout")}
	capture := NewBoundaryCapture(publisher, resolver)
	result, err := capture.Capture(context.Background(), BoundaryCaptureRequest{
		Locator:      testCaptureLocator(t, "telegram", "123:0", "tg-123-0"),
		TransitionID: "transition-1",
		Reason:       sessionmemory.BoundaryReasonClose,
	})
	if err == nil || !result.Attempted || len(publisher.exports) != 1 {
		t.Fatalf("result = %+v, err = %v, exports = %d", result, err, len(publisher.exports))
	}
}
