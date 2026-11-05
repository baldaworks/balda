package deliveryfmt

import (
	"context"
	"errors"
	"testing"
)

type structuredBody struct {
	Text string
}

type structuredRendererFunc func(context.Context, StructuredEnvelope[structuredBody]) (StructuredPresentation, error)

func (f structuredRendererFunc) RenderStructured(ctx context.Context, env StructuredEnvelope[structuredBody]) (StructuredPresentation, error) {
	return f(ctx, env)
}

func TestStructuredRegistryRoundTrip(t *testing.T) {
	t.Parallel()

	reg := NewStructuredRegistry()
	desc := Descriptor[structuredBody]{Type: "balda.test.message.v1"}
	if err := RegisterStructuredRenderer(reg, TransportTelegram, desc, structuredRendererFunc(func(_ context.Context, env StructuredEnvelope[structuredBody]) (StructuredPresentation, error) {
		return StructuredPresentation{
			Text:           env.Body.Text,
			DeliveryFormat: DeliveryFormatRichMarkdown,
		}, nil
	})); err != nil {
		t.Fatalf("RegisterStructuredRenderer() error = %v", err)
	}

	got, err := RenderStructured(context.Background(), reg, TransportTelegram, StructuredEnvelope[structuredBody]{
		Descriptor: desc,
		Body:       structuredBody{Text: "hello"},
	})
	if err != nil {
		t.Fatalf("RenderStructured() error = %v", err)
	}
	if got.Text != "hello" {
		t.Fatalf("RenderStructured() text = %q, want hello", got.Text)
	}
	if got.DeliveryFormat != DeliveryFormatRichMarkdown {
		t.Fatalf("RenderStructured() format = %q", got.DeliveryFormat)
	}
}

func TestStructuredRegistryMissingRenderer(t *testing.T) {
	t.Parallel()

	_, err := RenderStructured(context.Background(), NewStructuredRegistry(), TransportTelegram, StructuredEnvelope[structuredBody]{
		Descriptor: Descriptor[structuredBody]{Type: "balda.test.message.v1"},
		Body:       structuredBody{Text: "hello"},
	})
	if !errors.Is(err, ErrStructuredRendererNotFound) {
		t.Fatalf("RenderStructured() error = %v, want ErrStructuredRendererNotFound", err)
	}
}

func TestRequireStructuredRenderer(t *testing.T) {
	t.Parallel()

	desc := Descriptor[structuredBody]{Type: "balda.test.message.v1"}
	reg := NewStructuredRegistry()
	if err := RequireStructuredRenderer(reg, TransportTelegram, desc.Type); !errors.Is(err, ErrStructuredRendererNotFound) {
		t.Fatalf("missing requirement error = %v", err)
	}
	if err := RegisterStructuredRenderer(reg, TransportTelegram, desc, structuredRendererFunc(func(context.Context, StructuredEnvelope[structuredBody]) (StructuredPresentation, error) {
		return StructuredPresentation{}, nil
	})); err != nil {
		t.Fatal(err)
	}
	if err := RequireStructuredRenderer(reg, TransportTelegram, desc.Type); err != nil {
		t.Fatalf("registered requirement error = %v", err)
	}
	for _, test := range []struct {
		transport string
		typ       MessageType
	}{
		{transport: " Telegram ", typ: desc.Type},
		{transport: TransportTelegram, typ: "invalid"},
	} {
		if err := RequireStructuredRenderer(reg, test.transport, test.typ); err == nil {
			t.Fatalf("RequireStructuredRenderer(%q, %q) succeeded", test.transport, test.typ)
		}
	}
}
