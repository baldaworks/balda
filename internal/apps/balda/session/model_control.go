package session

import (
	"context"

	"github.com/baldaworks/runtime/v2/agentproduct"
	adksession "google.golang.org/adk/v2/session"
)

// RuntimeSessionIdentity is the exact ADK identity a model operation targets.
type RuntimeSessionIdentity struct {
	SessionService adksession.Service
	AppName        string
	UserID         string
	SessionID      string
}

// VerifiedRuntimeSessionIdentity records the identity proven by SessionService.Get.
type VerifiedRuntimeSessionIdentity = RuntimeSessionIdentity

// RuntimeModelChange is the bounded model-control result adapted to a Balda runtime.
type RuntimeModelChange struct {
	Status         agentproduct.ModelChangeStatus
	EffectiveModel string
	Reason         agentproduct.Reason
	Candidate      *BuiltRuntime
	Identity       VerifiedRuntimeSessionIdentity
}

// RuntimeModelController keeps provider control adjacent to an immutable runtime.
type RuntimeModelController interface {
	InspectSessionControl(ctx context.Context, identity RuntimeSessionIdentity) agentproduct.SessionControlSnapshot
	ValidateModel(ctx context.Context, identity RuntimeSessionIdentity, model string) agentproduct.ModelValidation
	ChangeModel(ctx context.Context, identity RuntimeSessionIdentity, model string) RuntimeModelChange
}
