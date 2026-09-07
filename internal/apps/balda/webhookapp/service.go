package webhookapp

import (
	"context"
	"errors"
	"strings"

	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/baldaworks/balda/internal/apps/balda/actorcmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
)

// Service orchestrates inbound webhook requests across target resolution and command publication.
type Service struct {
	targetResolver   TargetResolver
	sessionPublisher SessionPublisher
	jobPublisher     JobPublisher
}

// NewService creates a webhook application Service.
func NewService(targetResolver TargetResolver, sessionPub SessionPublisher, jobPub JobPublisher) *Service {
	return &Service{
		targetResolver:   targetResolver,
		sessionPublisher: sessionPub,
		jobPublisher:     jobPub,
	}
}

// Accept validates and dispatches a normalized webhook request.
func (s *Service) Accept(ctx context.Context, req Request) (Result, error) {
	reqID := strings.TrimSpace(req.RequestID)
	if reqID == "" {
		return Result{}, &InvalidRequestError{Field: "RequestID", Message: "cannot be empty"}
	}
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		return Result{}, &InvalidRequestError{Field: "Prompt", Message: "cannot be empty"}
	}
	if s.targetResolver == nil {
		return Result{}, &DispatchFailedError{Cause: errors.New("target resolver is unavailable")}
	}

	target, err := s.targetResolver.ResolveTarget(ctx, req.Target)
	if err != nil {
		return Result{}, &TargetNotFoundError{Cause: err}
	}

	var reportTo *deliverycmd.Locator
	if req.ReportTo != nil {
		resolvedReportTo, reportErr := s.targetResolver.ResolveTarget(ctx, *req.ReportTo)
		if reportErr != nil {
			return Result{}, &TargetNotFoundError{Cause: reportErr}
		}
		reportTo = &resolvedReportTo.Locator
	}

	payload := turncmd.SessionTurnPayload{
		Text:           prompt,
		Locator:        target.Locator,
		ReportTo:       reportTo,
		UserID:         target.UserID(),
		TopicID:        0,
		DeliveryFormat: "",
		ProgressPolicy: deliveryfmt.ProgressPolicy{
			Typing:      false,
			Thinking:    false,
			PlanUpdates: true,
		},
		Deliver:   reportTo != nil,
		Source:    "webhook",
		DedupeKey: req.DedupeKey,
	}

	var (
		receipt     *actortransport.DispatchReceipt
		jobID       string
		dispatchErr error
	)

	if req.Mode == ModeSession {
		if s.sessionPublisher == nil {
			return Result{}, &DispatchFailedError{Cause: errors.New("session publisher is unavailable")}
		}
		receipt, dispatchErr = s.sessionPublisher.PublishSessionTurn(ctx, payload)
	} else {
		if s.jobPublisher == nil {
			return Result{}, &DispatchFailedError{Cause: errors.New("job publisher is unavailable")}
		}
		receipt, jobID, dispatchErr = s.jobPublisher.PublishWebhookJob(ctx, payload, req.RouteName, reqID)
	}

	if dispatchErr != nil {
		if actorcmd.IsCommandQueueFull(dispatchErr) {
			return Result{}, &QueueFullError{Cause: dispatchErr}
		}
		return Result{}, &DispatchFailedError{Cause: dispatchErr}
	}

	if receipt == nil {
		return Result{}, &DispatchFailedError{Cause: errors.New("nil dispatch receipt returned")}
	}

	return Result{
		RequestID: reqID,
		MessageID: receipt.MsgID,
		Duplicate: receipt.Duplicate,
		JobID:     jobID,
		Stream:    receipt.Stream,
		Sequence:  receipt.Sequence,
		Target:    target,
	}, nil
}
