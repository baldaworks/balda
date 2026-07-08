package actorlayer_test

import (
	"strings"
	"testing"

	"github.com/normahq/balda/pkg/actorlayer"
)

func TestEnvelopeRoundTrip(t *testing.T) {
	t.Parallel()

	env := actorlayer.Envelope{
		ID:          "env-1",
		Namespace:   "test.command",
		Kind:        "message",
		From:        actorlayer.ActorAddress{Target: "system", Key: "source"},
		To:          actorlayer.ActorAddress{Target: "session", Key: "1"},
		DedupeKey:   "dedupe-1",
		PayloadJSON: `{"ok":true}`,
	}

	raw, err := actorlayer.EncodeEnvelope(env)
	if err != nil {
		t.Fatalf("EncodeEnvelope() error = %v", err)
	}
	got, err := actorlayer.DecodeEnvelope(raw)
	if err != nil {
		t.Fatalf("DecodeEnvelope() error = %v", err)
	}
	if got.ID != env.ID || got.Namespace != env.Namespace || got.Kind != env.Kind || got.From != env.From || got.To != env.To || got.DedupeKey != env.DedupeKey || got.PayloadJSON != env.PayloadJSON {
		t.Fatalf("DecodeEnvelope(EncodeEnvelope()) = %#v, want %#v", got, env)
	}
	if key := actorlayer.DedupeKeyOrID(got); key != env.DedupeKey {
		t.Fatalf("DedupeKeyOrID() = %q, want %q", key, env.DedupeKey)
	}
}

func TestAssertEnvelope(t *testing.T) {
	t.Parallel()

	env := actorlayer.Envelope{
		ID:          "env-1",
		Namespace:   "test.command",
		Kind:        "message",
		From:        actorlayer.ActorAddress{Target: "system", Key: "source"},
		To:          actorlayer.ActorAddress{Target: "session", Key: "1"},
		PayloadJSON: `{"ok":true}`,
	}
	got, err := actorlayer.AssertEnvelope(env)
	if err != nil {
		t.Fatalf("AssertEnvelope() error = %v", err)
	}
	if got.ID != env.ID || got.Namespace != env.Namespace || got.Kind != env.Kind || got.From != env.From || got.To != env.To || got.PayloadJSON != env.PayloadJSON {
		t.Fatalf("AssertEnvelope() = %#v, want %#v", got, env)
	}
	_, err = actorlayer.AssertEnvelope(struct{}{})
	if err == nil || !strings.Contains(err.Error(), "unexpected actor envelope type") {
		t.Fatalf("AssertEnvelope(struct{}{}) error = %v, want unexpected type error", err)
	}
}

func TestEnvelopeValidateRejectsInvalidPayloadJSON(t *testing.T) {
	t.Parallel()

	env := validEnvelopeForTest()
	env.PayloadJSON = `{not-json`
	if err := env.Validate(); err == nil || !strings.Contains(err.Error(), "payload_json must be valid json") {
		t.Fatalf("Validate() error = %v, want invalid payload_json error", err)
	}
}

func TestEnvelopeValidateRejectsInvalidReportTo(t *testing.T) {
	t.Parallel()

	env := validEnvelopeForTest()
	env.ReportTo = &actorlayer.ActorAddress{Target: "session"}
	if err := env.Validate(); err == nil || !strings.Contains(err.Error(), "envelope report_to") {
		t.Fatalf("Validate() error = %v, want invalid report_to error", err)
	}
}

func TestEncodeEnvelopeRejectsInvalidEnvelope(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		env  actorlayer.Envelope
		want string
	}{
		{
			name: "missing id",
			env: func() actorlayer.Envelope {
				env := validEnvelopeForTest()
				env.ID = ""
				return env
			}(),
			want: "envelope id is required",
		},
		{
			name: "invalid payload json",
			env: func() actorlayer.Envelope {
				env := validEnvelopeForTest()
				env.PayloadJSON = "{not-json"
				return env
			}(),
			want: "payload_json must be valid json",
		},
		{
			name: "invalid report to",
			env: func() actorlayer.Envelope {
				env := validEnvelopeForTest()
				env.ReportTo = &actorlayer.ActorAddress{Target: "session"}
				return env
			}(),
			want: "envelope report_to",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if _, err := actorlayer.EncodeEnvelope(tt.env); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("EncodeEnvelope() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func validEnvelopeForTest() actorlayer.Envelope {
	return actorlayer.Envelope{
		ID:          "env-1",
		Namespace:   "test.command",
		Kind:        "message",
		From:        actorlayer.ActorAddress{Target: "system", Key: "source"},
		To:          actorlayer.ActorAddress{Target: "session", Key: "1"},
		PayloadJSON: `{"ok":true}`,
	}
}
