package locatorfmt

import (
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
)

func TestResponseDescriptorCarriesTypedLocatorData(t *testing.T) {
	t.Parallel()

	env := deliveryfmt.StructuredEnvelope[Response]{
		Descriptor: ResponseDescriptor,
		Body: Response{
			Transport: "slackagent",
			Locator:   "slackagent:c:T123:C456",
		},
	}
	if got, want := env.Type(), deliveryfmt.MessageType("balda.locator.response.v1"); got != want {
		t.Fatalf("Type() = %q, want %q", got, want)
	}
	if env.Body.Transport != "slackagent" || env.Body.Locator != "slackagent:c:T123:C456" {
		t.Fatalf("Body = %+v", env.Body)
	}
}
