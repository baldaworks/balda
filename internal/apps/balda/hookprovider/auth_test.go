package hookprovider

import (
	"context"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/hookcmd"
)

func TestAuthenticateRejectsMalformedUnknownAndIncorrectCapabilities(t *testing.T) {
	service, err := New(runnerFunc(func(context.Context, hookcmd.Event) (hookcmd.Result, error) {
		return hookcmd.Result{}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close() })
	binding, err := service.Bind(t.Context(), BindingScope{SessionID: "session", CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if _, authenticated := service.authenticate(binding.Token); authenticated == nil {
		t.Fatal("valid capability was rejected")
	}
	token, err := hex.DecodeString(binding.Token)
	if err != nil {
		t.Fatal(err)
	}

	wrongSecret := append([]byte(nil), token...)
	wrongSecret[len(wrongSecret)-1] ^= 0xff
	unknownID := append([]byte(nil), token...)
	unknownID[0] ^= 0xff
	copy(unknownID[16:], service.authDummy[:])
	for _, candidate := range []string{
		hex.EncodeToString(wrongSecret),
		hex.EncodeToString(unknownID),
		strings.Repeat("0", 62),
		strings.Repeat("z", 64),
	} {
		if _, authenticated := service.authenticate(candidate); authenticated != nil {
			t.Fatalf("invalid capability authenticated: %q", candidate)
		}
	}
}
