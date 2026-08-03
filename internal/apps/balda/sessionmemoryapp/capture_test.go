package sessionmemoryapp

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/normahq/balda/internal/apps/balda/deliverycmd"
	"github.com/normahq/balda/internal/apps/balda/sessionmemory"
	"github.com/normahq/balda/internal/apps/balda/sessionmemorycmd"
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

	for _, req := range []CaptureRequest{
		{AssistantText: "answer", Locator: locator, SourceTurnID: "turn-1"},
		{UserText: "question", Locator: locator, SourceTurnID: "turn-2"},
	} {
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
