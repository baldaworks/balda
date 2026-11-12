// Package sessionfmt defines transport-neutral structured presentation
// contracts for ordinary session lifecycle outcomes.
package sessionfmt

import (
	"errors"

	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/baldaworks/balda/internal/apps/balda/sessioncmd"
)

// ErrInvalid reports a malformed session presentation contract.
var ErrInvalid = errors.New("invalid session presentation contract")

// Notice contains the bounded public values needed by transport renderers.
type Notice struct {
	Kind          sessioncmd.ActivationKind       `json:"kind"`
	SessionID     string                          `json:"session_id"`
	Label         string                          `json:"label,omitempty"`
	Runtime       sessioncmd.RuntimeSummary       `json:"runtime"`
	Configuration sessioncmd.ConfigurationSummary `json:"configuration"`
	ModelState    ModelState                      `json:"model_state,omitempty"`
}

// ModelState identifies whether the displayed model is observed or configured.
type ModelState string

const (
	ModelStateActive            ModelState = "active"
	ModelStateRequestedPending  ModelState = "requested_pending"
	ModelStateConfiguredDefault ModelState = "configured_default"
)

const defaultModelLabel = "Model"

// Warning selects a bounded user-presentable warning without arbitrary text.
type Warning struct {
	Code      sessioncmd.WarningCode `json:"code"`
	SessionID string                 `json:"session_id"`
}

var (
	// NoticeDescriptor identifies created and replaced lifecycle notices.
	NoticeDescriptor = deliveryfmt.Descriptor[Notice]{Type: "balda.session.activation.v1"}
	// WarningDescriptor identifies a typed session activation warning.
	WarningDescriptor = deliveryfmt.Descriptor[Warning]{Type: "balda.session.activation-warning.v1"}
)

// NewNotice projects an authoritative activation into presentation data.
func NewNotice(activation sessioncmd.Activation) (Notice, error) {
	if err := activation.Validate(); err != nil || activation.Kind == sessioncmd.ActivationRestored {
		return Notice{}, ErrInvalid
	}
	notice := Notice{
		Kind:          activation.Kind,
		SessionID:     activation.SessionID,
		Label:         activation.Label,
		Runtime:       cloneRuntimeSummary(activation.Runtime),
		Configuration: activation.Configuration.Clone(),
	}
	switch {
	case notice.Runtime.EffectiveModel != "":
		notice.ModelState = ModelStateActive
	case notice.Configuration.RequestedModel != nil:
		notice.ModelState = ModelStateRequestedPending
	case notice.Configuration.ConfiguredModel != "":
		notice.ModelState = ModelStateConfiguredDefault
	}
	if err := notice.Validate(); err != nil {
		return Notice{}, err
	}
	return notice, nil
}

// Validate accepts only created and restore-fallback replacement notices.
func (n Notice) Validate() error {
	if n.Kind != sessioncmd.ActivationCreated && n.Kind != sessioncmd.ActivationReplaced {
		return ErrInvalid
	}
	candidate := sessioncmd.Activation{
		Kind: n.Kind, SessionID: n.SessionID, Label: n.Label, Runtime: n.Runtime, Configuration: n.Configuration,
	}
	if err := candidate.Validate(); err != nil {
		return ErrInvalid
	}
	if n.ModelState != modelState(candidate) {
		return ErrInvalid
	}
	return nil
}

// Envelope binds a validated notice to its versioned descriptor.
func (n Notice) Envelope() (deliveryfmt.StructuredEnvelope[Notice], error) {
	if err := n.Validate(); err != nil {
		return deliveryfmt.StructuredEnvelope[Notice]{}, err
	}
	n.Runtime = cloneRuntimeSummary(n.Runtime)
	n.Configuration = n.Configuration.Clone()
	return deliveryfmt.StructuredEnvelope[Notice]{Descriptor: NoticeDescriptor, Body: n}, nil
}

func modelState(activation sessioncmd.Activation) ModelState {
	switch {
	case activation.Runtime.EffectiveModel != "":
		return ModelStateActive
	case activation.Configuration.RequestedModel != nil:
		return ModelStateRequestedPending
	case activation.Configuration.ConfiguredModel != "":
		return ModelStateConfiguredDefault
	default:
		return ""
	}
}

// ProviderID returns the observed provider or its committed configuration.
func (n Notice) ProviderID() string {
	if n.Runtime.ProviderID != "" {
		return n.Runtime.ProviderID
	}
	return n.Configuration.ProviderID
}

// ReasoningEffort returns observed or committed configured reasoning.
func (n Notice) ReasoningEffort() string {
	if n.Runtime.ReasoningEffort != "" {
		return n.Runtime.ReasoningEffort
	}
	return n.Configuration.ReasoningEffort
}

// MCPServers returns observed or committed configured public MCP names.
func (n Notice) MCPServers() []string {
	if len(n.Runtime.MCPServers) != 0 {
		return n.Runtime.MCPServers
	}
	return n.Configuration.MCPServers
}

// ModelDisplay returns a truthful label and model value for presentation.
func (n Notice) ModelDisplay() (string, string) {
	switch n.ModelState {
	case ModelStateActive:
		return defaultModelLabel, n.Runtime.EffectiveModel
	case ModelStateRequestedPending:
		if n.Configuration.RequestedModel != nil {
			return "Model (requested)", *n.Configuration.RequestedModel
		}
		return defaultModelLabel, ""
	case ModelStateConfiguredDefault:
		return "Model (configured)", n.Configuration.ConfiguredModel
	default:
		return defaultModelLabel, ""
	}
}

// NewWarning builds one validated warning body from a closed code.
func NewWarning(sessionID string, code sessioncmd.WarningCode) (Warning, error) {
	warning := Warning{Code: code, SessionID: sessionID}
	if err := warning.Validate(); err != nil {
		return Warning{}, err
	}
	return warning, nil
}

// Validate rejects unknown warning codes and malformed session identities.
func (w Warning) Validate() error {
	candidate := sessioncmd.Activation{
		Kind: sessioncmd.ActivationCreated, SessionID: w.SessionID,
		Warnings: []sessioncmd.WarningCode{w.Code},
	}
	if err := candidate.Validate(); err != nil {
		return ErrInvalid
	}
	return nil
}

// Envelope binds a validated warning to its versioned descriptor.
func (w Warning) Envelope() (deliveryfmt.StructuredEnvelope[Warning], error) {
	if err := w.Validate(); err != nil {
		return deliveryfmt.StructuredEnvelope[Warning]{}, err
	}
	return deliveryfmt.StructuredEnvelope[Warning]{Descriptor: WarningDescriptor, Body: w}, nil
}

func cloneRuntimeSummary(summary sessioncmd.RuntimeSummary) sessioncmd.RuntimeSummary {
	summary.PublicRuntimeMetadata = summary.Clone()
	return summary
}
