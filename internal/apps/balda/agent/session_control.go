package agent

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/baldaworks/runtime/v2/agentproduct"
	"google.golang.org/adk/v2/runner"
	adksession "google.golang.org/adk/v2/session"
)

// ErrSessionBinding reports that a control operation did not target the exact
// ADK session identity owned by a runtime.
var ErrSessionBinding = errors.New("runtime session binding mismatch")

// SessionIdentity names the existing ADK session a control operation must use.
type SessionIdentity struct {
	SessionService adksession.Service
	AppName        string
	UserID         string
	SessionID      string
}

// VerifiedSessionIdentity records the exact session identity proven by Get.
// SessionService is retained so candidate publication can compare the same
// service instance without recreating or deleting session state.
type VerifiedSessionIdentity struct {
	SessionService adksession.Service
	AppName        string
	UserID         string
	SessionID      string
}

// RuntimeModelChange is a provider-neutral change outcome adapted to a Balda
// runner. Candidate is populated only for a verified replacement.
type RuntimeModelChange struct {
	Status         agentproduct.ModelChangeStatus
	EffectiveModel string
	Reason         agentproduct.Reason
	Candidate      *BuiltRuntime
	Identity       VerifiedSessionIdentity
}

// Abort closes an unpublished replacement candidate without touching the
// incumbent runtime.
func (c RuntimeModelChange) Abort() error {
	if c.Candidate == nil || c.Candidate.Close == nil {
		return nil
	}
	return c.Candidate.Close()
}

// InspectSessionControl verifies identity before inspecting the optional
// product control plane.
func (r *BuiltRuntime) InspectSessionControl(ctx context.Context, identity SessionIdentity) agentproduct.SessionControlSnapshot {
	if r == nil || r.SessionControl == nil {
		return agentproduct.SessionControlSnapshot{Status: agentproduct.ControlUnsupported, Reason: agentproduct.ReasonUnavailable}
	}
	_, sess, err := r.verifySessionIdentity(ctx, identity)
	if err != nil {
		return agentproduct.SessionControlSnapshot{Status: agentproduct.ControlFailed, Reason: agentproduct.ReasonBindingMismatch}
	}
	return r.SessionControl.Inspect(ctx, sess)
}

// ValidateModel verifies identity with Get before invoking non-mutating model
// validation. An absent control is reported without provider I/O.
func (r *BuiltRuntime) ValidateModel(ctx context.Context, identity SessionIdentity, model string) agentproduct.ModelValidation {
	if r == nil || r.SessionControl == nil {
		return agentproduct.ModelValidation{Status: agentproduct.ModelUnsupported, Reason: agentproduct.ReasonUnavailable}
	}
	_, sess, err := r.verifySessionIdentity(ctx, identity)
	if err != nil {
		return agentproduct.ModelValidation{Status: agentproduct.ModelFailed, Reason: agentproduct.ReasonBindingMismatch}
	}
	return r.SessionControl.ValidateModel(ctx, sess, model)
}

// ChangeModel verifies the incumbent identity, invokes the product control,
// and converts a replacement Product into a same-session Runner candidate.
func (r *BuiltRuntime) ChangeModel(ctx context.Context, identity SessionIdentity, model string) RuntimeModelChange {
	if r == nil || r.SessionControl == nil {
		return RuntimeModelChange{Status: agentproduct.ModelChangeUnsupported, Reason: agentproduct.ReasonUnavailable}
	}
	verified, sess, err := r.verifySessionIdentity(ctx, identity)
	if err != nil {
		return RuntimeModelChange{Status: agentproduct.ModelChangeFailed, Reason: agentproduct.ReasonBindingMismatch}
	}
	change := r.SessionControl.ChangeModel(ctx, sess, model)
	result := RuntimeModelChange{
		Status:         change.Status,
		EffectiveModel: change.EffectiveModel,
		Reason:         change.Reason,
		Identity:       verified,
	}
	if change.Status != agentproduct.ModelReplacement || change.Replacement == nil {
		return result
	}

	// The provider mutation may take time. Re-read the incumbent identity before
	// making its independently owned replacement available for publication.
	verified, _, err = r.verifySessionIdentity(ctx, identity)
	if err != nil {
		_ = change.Replacement.Close()
		return RuntimeModelChange{Status: agentproduct.ModelChangeFailed, Reason: agentproduct.ReasonBindingMismatch}
	}
	candidate, err := r.replacementRuntime(change.Replacement)
	if err != nil {
		_ = change.Replacement.Close()
		return RuntimeModelChange{Status: agentproduct.ModelChangeFailed, Reason: agentproduct.ReasonPreDispatch}
	}
	result.Candidate = candidate
	result.Identity = verified
	return result
}

func (r *BuiltRuntime) replacementRuntime(product *agentproduct.Product) (*BuiltRuntime, error) {
	if product == nil || product.Agent == nil {
		return nil, fmt.Errorf("replacement product is required")
	}
	newRunner := r.newRunner
	if newRunner == nil {
		newRunner = func(cfg runner.Config, _ ToolHookScope) (*runner.Runner, error) {
			return runner.New(cfg)
		}
	}
	candidateRunner, err := newRunner(runner.Config{
		AppName:        r.AppName,
		Agent:          product.Agent,
		SessionService: r.SessionSvc,
	}, r.runnerScope)
	if err != nil {
		return nil, err
	}
	candidate := &BuiltRuntime{
		Agent:               product.Agent,
		SessionControl:      product.Control,
		Runner:              candidateRunner,
		SessionSvc:          r.SessionSvc,
		AppName:             r.AppName,
		RuntimeSnapshotID:   r.RuntimeSnapshotID,
		ProviderBindingJSON: r.ProviderBindingJSON,
		productClose:        product.Close,
		runnerScope:         r.runnerScope,
		newRunner:           newRunner,
	}
	candidate.Close = candidate.closeProduct
	return candidate, nil
}

func (r *BuiltRuntime) verifySessionIdentity(ctx context.Context, identity SessionIdentity) (VerifiedSessionIdentity, adksession.Session, error) {
	if r == nil || r.SessionSvc == nil || !sameSessionService(identity.SessionService, r.SessionSvc) ||
		strings.TrimSpace(identity.AppName) == "" || identity.AppName != r.AppName ||
		strings.TrimSpace(identity.UserID) == "" || strings.TrimSpace(identity.SessionID) == "" {
		return VerifiedSessionIdentity{}, nil, ErrSessionBinding
	}
	response, err := r.SessionSvc.Get(ctx, &adksession.GetRequest{
		AppName: identity.AppName, UserID: identity.UserID, SessionID: identity.SessionID,
	})
	if err != nil || response == nil || response.Session == nil {
		return VerifiedSessionIdentity{}, nil, ErrSessionBinding
	}
	sess := response.Session
	if sess.AppName() != identity.AppName || sess.UserID() != identity.UserID || sess.ID() != identity.SessionID {
		return VerifiedSessionIdentity{}, nil, ErrSessionBinding
	}
	verified := VerifiedSessionIdentity{
		SessionService: r.SessionSvc,
		AppName:        identity.AppName,
		UserID:         identity.UserID,
		SessionID:      identity.SessionID,
	}
	return verified, sess, nil
}

func sameSessionService(left, right adksession.Service) bool {
	if left == nil || right == nil {
		return false
	}
	lv := reflect.ValueOf(left)
	rv := reflect.ValueOf(right)
	return lv.Type() == rv.Type() && lv.Comparable() && rv.Comparable() && lv.Interface() == rv.Interface()
}
