package command

import (
	"context"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/go-actorlayer"
)

type testHandler string

func (h testHandler) Name() string { return string(h) }
func (h testHandler) Handle(context.Context, actorlayer.Envelope, commandcmd.Payload) error {
	return nil
}

func TestRouterUsesCanonicalCommandName(t *testing.T) {
	r, err := NewRouter([]Handler{testHandler("locator"), testHandler("reset")})
	if err != nil {
		t.Fatal(err)
	}
	if h, ok := r.Resolve(" LOCATOR "); !ok || h.Name() != "locator" {
		t.Fatalf("Resolve() = %v, %v", h, ok)
	}
	if err := r.ValidateAdvertised([]string{"reset", "locator"}); err != nil {
		t.Fatal(err)
	}
	if err := r.ValidateAdvertised([]string{"topic"}); err == nil {
		t.Fatal("missing advertised handler accepted")
	}
}

func TestRouterRejectsDuplicateRegistration(t *testing.T) {
	if _, err := NewRouter([]Handler{testHandler("locator"), testHandler(" LOCATOR ")}); err == nil {
		t.Fatal("duplicate registration accepted")
	}
}
