package execution

import (
	"testing"

	"github.com/baldaworks/go-actorlayer"
)

func TestRuntimeDeliveryUsesTransportRetryPolicy(t *testing.T) {
	t.Parallel()
	delivery := &runtimeDelivery{delivery: testDelivery{
		env:           actorlayer.Envelope{ID: "inbound-turn", Attempt: 99, MaxAttempts: 100},
		numDelivered:  5,
		maxDeliveries: 5,
	}}
	env := delivery.Envelope()
	if env.Attempt != 4 || env.MaxAttempts != 5 || env.ID != "inbound-turn" {
		t.Fatalf("envelope retry metadata = %+v, want transport attempt 4 and max 5", env)
	}
}
