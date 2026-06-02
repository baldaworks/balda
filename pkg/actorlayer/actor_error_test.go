package actorlayer_test

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/normahq/balda/pkg/actorlayer"
	actorengine "github.com/normahq/balda/pkg/actorlayer/engine"
)

func TestClassifyErrorKinds(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		err  error
		want actorlayer.ErrorKind
	}{
		{name: "transient", err: actorlayer.TransientError(errors.New("retry")), want: actorlayer.ErrorKindTransient},
		{name: "permanent", err: actorlayer.PermanentError(errors.New("perm")), want: actorlayer.ErrorKindPermanent},
		{name: "decode", err: actorlayer.DecodeError(errors.New("decode")), want: actorlayer.ErrorKindDecode},
		{name: "external delivery", err: actorlayer.ExternalDeliveryError(errors.New("send failed")), want: actorlayer.ErrorKindExternalDelivery},
		{name: "fallback transient", err: errors.New("plain error"), want: actorlayer.ErrorKindTransient},
		{name: "nil", err: nil, want: ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := actorlayer.ClassifyError(tc.err); got != tc.want {
				t.Fatalf("ClassifyError() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestIsRetryableError(t *testing.T) {
	t.Parallel()

	t.Run("resolve error is non-retryable", func(t *testing.T) {
		t.Parallel()
		err := &actorengine.ResolveError{Address: "session:missing"}
		if got := actorlayer.IsRetryableError(err); got {
			t.Fatalf("IsRetryableError() = true, want false")
		}
	})

	t.Run("wrapped resolve error is non-retryable", func(t *testing.T) {
		t.Parallel()
		err := fmt.Errorf("dispatch failed: %w", &actorengine.ResolveError{Address: "session:wrapped"})
		if got := actorlayer.IsRetryableError(err); got {
			t.Fatalf("IsRetryableError() = true, want false")
		}
	})

	t.Run("wrapped canonical actor not found is non-retryable", func(t *testing.T) {
		t.Parallel()
		err := fmt.Errorf("lookup failed: %w", actorengine.ErrActorNotFound)
		if got := actorlayer.IsRetryableError(err); got {
			t.Fatalf("IsRetryableError() = true, want false")
		}
	})

	t.Run("other errors stay classified by actor errors", func(t *testing.T) {
		t.Parallel()
		err := fmt.Errorf("%w", actorlayer.PermanentError(errors.New("persist failed")))
		if got := actorlayer.IsRetryableError(err); got {
			t.Fatalf("IsRetryableError() = true, want false")
		}
	})
}

func TestRetryHelpers(t *testing.T) {
	t.Parallel()

	if got := actorlayer.RetryExhausted(2, 3); got {
		t.Fatalf("RetryExhausted(2, 3) = %v, want false", got)
	}
	if got := actorlayer.RetryExhausted(3, 3); !got {
		t.Fatalf("RetryExhausted(3, 3) = %v, want true", got)
	}

	low := actorlayer.RetryDelay(0)
	base := time.Second
	if low < base || low > base+(base/4) {
		t.Fatalf("RetryDelay(0) = %s, want in [%s, %s]", low, base, base+(base/4))
	}
	high := actorlayer.RetryDelay(16)
	maxDelay := time.Minute
	if high < maxDelay || high > maxDelay+(maxDelay/4) {
		t.Fatalf("RetryDelay(16) = %s, want in [%s, %s]", high, maxDelay, maxDelay+(maxDelay/4))
	}
}
