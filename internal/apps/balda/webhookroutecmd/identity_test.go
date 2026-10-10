package webhookroutecmd

import (
	"strings"
	"testing"
)

func TestValidName(t *testing.T) {
	for _, tt := range []struct {
		name string
		want bool
	}{{"orders", true}, {"0_a-b", true}, {"", false}, {"Orders", false}, {"a/b", false}, {" a", false}, {strings.Repeat("a", 64), true}, {strings.Repeat("a", 65), false}} {
		if got := ValidName(tt.name); got != tt.want {
			t.Errorf("ValidName(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestCanonicalPath(t *testing.T) {
	for _, tt := range []struct{ base, want string }{{"", "/webhooks/orders"}, {"/balda", "/balda/webhooks/orders"}} {
		if got := CanonicalPath(tt.base, "orders"); got != tt.want {
			t.Errorf("CanonicalPath(%q, orders) = %q, want %q", tt.base, got, tt.want)
		}
	}
}
