package authpayload

import (
	"strings"
	"testing"
)

func TestInvitationPayload(t *testing.T) {
	t.Parallel()
	payload := Prefix + strings.Repeat("a", 32)
	for _, text := range []string{payload, "/start " + payload, "/start@bot " + payload, "/balda start " + payload} {
		if got, ok := Parse(text); !ok || got != payload {
			t.Errorf("Parse(%q) = %q, %t", text, got, ok)
		}
	}
	for _, text := range []string{"hello", "bind_example", payload + " extra", "/start " + payload + " extra", "/other " + payload} {
		if _, ok := Parse(text); ok {
			t.Errorf("Parse(%q) accepted non-exact input", text)
		}
	}
	if !Contains("previous message: ["+payload+"]") || !Contains(payload+" extra") || Contains("document the bind_ prefix") {
		t.Fatal("credential recognition must filter embedded secrets while preserving ordinary text")
	}
}
