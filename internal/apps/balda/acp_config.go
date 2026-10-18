package balda

import (
	"fmt"
	"strings"
	"time"
)

// ResolveACPStartupTimeout returns the finite ACP initialization budget.
// Empty configuration uses 60s; explicit durations must be positive.
func (c BaldaConfig) ResolveACPStartupTimeout() (time.Duration, error) {
	value := strings.TrimSpace(c.ACPStartupTimeout)
	if value == "" {
		return time.Minute, nil
	}
	timeout, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("balda.acp_startup_timeout: %w", err)
	}
	if timeout <= 0 {
		return 0, fmt.Errorf("balda.acp_startup_timeout must be a positive duration")
	}
	return timeout, nil
}
