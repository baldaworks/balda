package slackagent

import (
	"testing"

	"github.com/normahq/balda/internal/apps/balda/deliverycmd"
)

func TestClassifyLocatorScope(t *testing.T) {
	thread := NewThreadLocator("T123", "C456", "thread-789")
	got, err := ClassifyLocatorScope(thread)
	if err != nil {
		t.Fatalf("ClassifyLocatorScope(thread) error = %v", err)
	}
	if got != deliverycmd.LocatorScopeGroup {
		t.Fatalf("ClassifyLocatorScope(thread) = %q, want group", got)
	}
	if _, err := ClassifyLocatorScope(NewConversationLocator("T123", "C456")); err == nil {
		t.Fatal("ClassifyLocatorScope(conversation) error = nil, want ambiguous failure")
	}
}
