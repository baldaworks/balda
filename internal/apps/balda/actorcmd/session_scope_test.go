package actorcmd

import (
	"testing"

	"github.com/baldaworks/go-actorlayer"
)

func TestEnvelopeSessionID(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target string
		key    string
		meta   map[string]string
		want   string
	}{
		{name: "command address", target: ActorTypeCommand, key: "session-1", want: "session-1"},
		{name: "session address", target: ActorTypeSession, key: "session-1", want: "session-1"},
		{name: "metadata takes precedence", target: ActorTypeCommand, key: "other", meta: map[string]string{SessionIDMetaKey: " session-1 "}, want: "session-1"},
		{name: "blank metadata falls back", target: ActorTypeCommand, key: " session-1 ", meta: map[string]string{SessionIDMetaKey: " "}, want: "session-1"},
		{name: "question metadata", target: ActorTypeQuestion, key: "router", meta: map[string]string{SessionIDMetaKey: "session-1"}, want: "session-1"},
		{name: "unscoped address", target: "maintenance", key: "shared"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := actorlayer.Envelope{To: actorlayer.ActorAddress{Target: tc.target, Key: tc.key}, Meta: tc.meta}
			if got := EnvelopeSessionID(env); got != tc.want {
				t.Fatalf("EnvelopeSessionID() = %q, want %q", got, tc.want)
			}
		})
	}
}
