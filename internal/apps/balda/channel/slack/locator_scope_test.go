package slack

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
		{name: "dm", locator: NewDMLocator("T123", "D456"), want: deliverycmd.LocatorScopePersonal},
		{name: "dm thread", locator: NewThreadLocator("T123", "D456", "1712345678.000100"), want: deliverycmd.LocatorScopePersonal},
		{name: "thread", locator: NewThreadLocator("T123", "C456", "1712345678.000100"), want: deliverycmd.LocatorScopeGroup},
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
	if _, err := ClassifyLocatorScope(NewThreadLocator("T123", "unknown", "1712345678.000100")); err == nil {
		t.Fatal("ClassifyLocatorScope(unknown thread) error = nil, want fail-closed error")
	}
}
