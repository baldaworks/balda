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
	Kind      sessioncmd.ActivationKind `json:"kind"`
	SessionID string                    `json:"session_id"`
	Label     string                    `json:"label,omitempty"`
	Runtime   sessioncmd.RuntimeSummary `json:"runtime"`
}

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
		Kind:      activation.Kind,
		SessionID: activation.SessionID,
		Label:     activation.Label,
		Runtime:   cloneRuntimeSummary(activation.Runtime),
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
		Kind: n.Kind, SessionID: n.SessionID, Label: n.Label, Runtime: n.Runtime,
	}
	if err := candidate.Validate(); err != nil {
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
	return deliveryfmt.StructuredEnvelope[Notice]{Descriptor: NoticeDescriptor, Body: n}, nil
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
