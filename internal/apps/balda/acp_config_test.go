package balda

import (
	"testing"
	"time"
)

func TestZeroConfigACPStartupTimeout(t *testing.T) {
	var cfg Config
	got, err := cfg.Balda.ResolveACPStartupTimeout()
	if err != nil || got != time.Minute {
		t.Fatalf("zero config startup timeout = %s, %v; want 1m", got, err)
	}
}
