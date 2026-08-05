package sessionmemory

import "testing"

func TestNewCanonicalTurnProcessorRequiresAllCutoverPorts(t *testing.T) {
	if _, err := NewCanonicalTurnProcessor(nil, nil, nil, PolicyRegistry{Version: "policy-v1"}); err == nil {
		t.Fatal("NewCanonicalTurnProcessor() accepted missing ports")
	}
	if _, err := NewCanonicalTurnProcessor(testCanonicalStore{}, testCanonicalExtractor{}, testCanonicalSealer{}, PolicyRegistry{}); err == nil {
		t.Fatal("NewCanonicalTurnProcessor() accepted empty policy version")
	}
}

type testCanonicalStore struct{ CanonicalStore }
type testCanonicalExtractor struct{ CanonicalSemanticExtractor }
type testCanonicalSealer struct{ CanonicalPayloadSealer }
