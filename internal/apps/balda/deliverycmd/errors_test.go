package deliverycmd

import (
	"errors"
	"fmt"
	"testing"
)

func TestClassifyError(t *testing.T) {
	t.Parallel()

	cause := errors.New("send failed")
	tests := []struct {
		name     string
		err      error
		wantKind ErrorKind
		wantOK   bool
	}{
		{name: "retryable", err: RetryableError(cause), wantKind: ErrorKindRetryable, wantOK: true},
		{name: "permanent wrapped", err: fmt.Errorf("deliver: %w", PermanentError(cause)), wantKind: ErrorKindPermanent, wantOK: true},
		{name: "unclassified", err: cause},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kind, ok := ClassifyError(tt.err)
			if kind != tt.wantKind || ok != tt.wantOK {
				t.Fatalf("ClassifyError() = (%q, %v), want (%q, %v)", kind, ok, tt.wantKind, tt.wantOK)
			}
			if tt.wantOK && !errors.Is(tt.err, cause) {
				t.Fatal("classified error does not preserve its cause")
			}
		})
	}
}
