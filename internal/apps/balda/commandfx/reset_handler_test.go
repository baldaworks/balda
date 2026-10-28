package commandfx

import "testing"

func TestNewResetHandlersRegisterCanonicalAliases(t *testing.T) {
	handlers := newResetHandlers(resetParams{})
	if len(handlers) != 2 {
		t.Fatalf("newResetHandlers() count = %d, want 2", len(handlers))
	}
	if handlers[0].Name() != "reset" || handlers[1].Name() != "new" {
		t.Fatalf("newResetHandlers() names = %v, want [reset new]", []string{handlers[0].Name(), handlers[1].Name()})
	}
}
