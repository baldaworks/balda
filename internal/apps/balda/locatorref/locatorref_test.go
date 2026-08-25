package locatorref

import (
	"strings"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/telegramref"
)

func TestFormatTelegram(t *testing.T) {
	t.Parallel()

	locator := telegramref.NewLocator(-1002667079342, 8939)
	if got, want := Format(locator), "telegram:-1002667079342:8939"; got != want {
		t.Fatalf("Format() = %q, want %q", got, want)
	}
}

func TestParseTelegram(t *testing.T) {
	t.Parallel()

	got, err := Parse("telegram:-1002667079342:8939")
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	want := telegramref.NewLocator(-1002667079342, 8939)
	if got != want {
		t.Fatalf("Parse() = %+v, want %+v", got, want)
	}
}

func TestParseRejectsMalformedRef(t *testing.T) {
	t.Parallel()

	_, err := Parse("telegram")
	if err == nil {
		t.Fatal("Parse() error = nil, want non-nil")
	}
	if !strings.Contains(err.Error(), "<channel_type>:<address_key>") {
		t.Fatalf("Parse() error = %v", err)
	}
}

func TestParseSlackAgentConversation(t *testing.T) {
	t.Parallel()

	got, err := Parse("slackagent:c:T123:C456")
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	want, err := NewSlackAgentConversationLocator("T123", "C456")
	if err != nil {
		t.Fatalf("NewSlackAgentConversationLocator() error = %v", err)
	}
	if got != want {
		t.Fatalf("Parse() = %+v, want %+v", got, want)
	}
}

func TestParseSlackAgentThread(t *testing.T) {
	t.Parallel()

	got, err := Parse("slackagent:t:T123:C456:thread-789")
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	want, err := NewSlackAgentThreadLocator("T123", "C456", "thread-789")
	if err != nil {
		t.Fatalf("NewSlackAgentThreadLocator() error = %v", err)
	}
	if got != want {
		t.Fatalf("Parse() = %+v, want %+v", got, want)
	}
}

func TestParseRejectsUnknownTransport(t *testing.T) {
	t.Parallel()

	_, err := Parse("matrix:ops:deploy")
	if err == nil {
		t.Fatal("Parse() error = nil, want non-nil")
	}
	if !strings.Contains(err.Error(), `unsupported locator transport "matrix"`) {
		t.Fatalf("Parse() error = %v", err)
	}
}
