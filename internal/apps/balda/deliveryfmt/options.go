// Package deliveryfmt defines transport-neutral delivery presentation options.
package deliveryfmt

import (
	"strings"
)

type ProgressPolicy struct {
	Typing      bool `json:"typing,omitempty"`
	Thinking    bool `json:"thinking,omitempty"`
	PlanUpdates bool `json:"plan_updates,omitempty"`
}

type Options struct {
	DeliveryFormat DeliveryFormat `json:"delivery_format,omitempty"`
	ProgressPolicy ProgressPolicy `json:"progress_policy,omitempty,omitzero"`
}

func NormalizeOptions(options Options) Options {
	return Options{
		DeliveryFormat: NormalizeDeliveryFormat(options.DeliveryFormat),
		ProgressPolicy: options.ProgressPolicy,
	}
}

// NormalizeDeliveryFormat normalizes an opaque delivery capability without
// interpreting or defaulting it.
func NormalizeDeliveryFormat(format DeliveryFormat) DeliveryFormat {
	return DeliveryFormat(strings.ToLower(strings.TrimSpace(string(format))))
}
