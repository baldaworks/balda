package commandfx

import "testing"

func TestNewModelHandlerRegistersCanonicalName(t *testing.T) {
	handler := newModelHandler(modelParams{})
	if handler == nil || handler.Name() != "model" {
		t.Fatalf("newModelHandler() = %#v", handler)
	}
}
