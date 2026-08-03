package slackagent

import (
	"testing"

	"github.com/normahq/balda/internal/apps/balda/deliverycmd"
)

func TestClassifyLocatorScope(t *testing.T) {
	for _, test := range []struct {
		name    string
		locator deliverycmd.Locator
		want    deliverycmd.LocatorScopeKind
	}{
		{name: "group thread", locator: NewThreadLocator("T123", "C456", "thread-789"), want: deliverycmd.LocatorScopeGroup},
		{name: "group conversation", locator: NewConversationLocator("T123", "C456"), want: deliverycmd.LocatorScopeGroup},
		{name: "personal thread", locator: NewThreadLocator("T123", "D456", "thread-789"), want: deliverycmd.LocatorScopePersonal},
		{name: "personal conversation", locator: NewConversationLocator("T123", "D456"), want: deliverycmd.LocatorScopePersonal},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := ClassifyLocatorScope(test.locator)
			if err != nil {
				t.Fatalf("ClassifyLocatorScope() error = %v", err)
			}
			if got != test.want {
				t.Fatalf("ClassifyLocatorScope() = %q, want %q", got, test.want)
			}
		})
	}
	if _, err := ClassifyLocatorScope(NewConversationLocator("T123", "opaque-conversation")); err == nil {
		t.Fatal("ClassifyLocatorScope(opaque conversation) error = nil, want fail-closed error")
	}
}
