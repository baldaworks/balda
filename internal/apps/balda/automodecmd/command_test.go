package automodecmd

import (
	"testing"

	"github.com/normahq/balda/internal/apps/balda/actorcmd"
	baldasession "github.com/normahq/balda/internal/apps/balda/session"
)

func TestEnvelopeSatisfiesRuntimeContract(t *testing.T) {
	t.Parallel()

	env, err := Envelope(Payload{
		Locator: baldasession.SessionLocator{
			SessionID:   "tg-1-2",
			ChannelType: "telegram",
			AddressKey:  "1:2",
		},
		State: map[string]any{"balda_auto_enabled": true},
	})
	if err != nil {
		t.Fatalf("Envelope() error = %v", err)
	}
	if err := env.Validate(); err != nil {
		t.Fatalf("Envelope().Validate() error = %v", err)
	}
	if env.Namespace != actorcmd.NamespaceAutoModeCommand || env.Kind != actorcmd.KindMessage {
		t.Fatalf("envelope contract = %q/%q", env.Namespace, env.Kind)
	}
	if env.To.Target != actorcmd.ActorTypeSession || env.To.Key != "tg-1-2" {
		t.Fatalf("envelope target = %+v", env.To)
	}
	if got := actorcmd.EnvelopeSessionID(env); got != "tg-1-2" {
		t.Fatalf("session id metadata = %q", got)
	}
}
