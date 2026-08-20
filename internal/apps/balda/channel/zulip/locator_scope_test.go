package zulip

import (
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
)

func TestClassifyLocatorScope(t *testing.T) {
	for _, test := range []struct {
		name    string
		locator deliverycmd.Locator
		want    deliverycmd.LocatorScopeKind
	}{
		{name: "dm", locator: NewDMLocator(101), want: deliverycmd.LocatorScopePersonal},
		{name: "stream topic", locator: NewStreamLocator(42, "ops"), want: deliverycmd.LocatorScopeGroup},
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
}
