package webhookapp

import (
	"context"
	"errors"
	"fmt"

	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/baldaworks/balda/internal/apps/balda/envelopetarget"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
)

// Inbound webhook execution modes.
const (
	ModeJob     = "job"
	ModeSession = "session"
)

// Request is the normalized, transport-neutral webhook invocation.
type Request struct {
	RequestID string
	RouteName string
	Prompt    string
	Target    envelopetarget.Target
	ReportTo  *envelopetarget.Target
	Mode      string
	DedupeKey string
}

// Result is the normalized acceptance receipt returned by the service.
type Result struct {
	RequestID string
	MessageID string
	Duplicate bool
	JobID     string
	Stream    string
	Sequence  uint64
	Target    envelopetarget.Resolved
}

// TargetResolver resolves an envelope target into a canonical delivery locator and principal.
type TargetResolver interface {
	ResolveTarget(ctx context.Context, target envelopetarget.Target) (envelopetarget.Resolved, error)
}

// TargetResolverFunc adapts a function to TargetResolver.
type TargetResolverFunc func(ctx context.Context, target envelopetarget.Target) (envelopetarget.Resolved, error)

// ResolveTarget implements TargetResolver.
func (f TargetResolverFunc) ResolveTarget(ctx context.Context, target envelopetarget.Target) (envelopetarget.Resolved, error) {
	return f(ctx, target)
}

// NewDestinationTargetResolver creates a TargetResolver that delegates to envelopetarget.Resolve using a DestinationResolver.
func NewDestinationTargetResolver(resolver envelopetarget.DestinationResolver) TargetResolver {
	return TargetResolverFunc(func(ctx context.Context, target envelopetarget.Target) (envelopetarget.Resolved, error) {
		return envelopetarget.Resolve(ctx, resolver, target)
	})
}

// SessionPublisher publishes a session turn directly into the SessionActor.
type SessionPublisher interface {
	PublishSessionTurn(ctx context.Context, payload turncmd.SessionTurnPayload) (*actortransport.DispatchReceipt, error)
}

// SessionPublisherFunc adapts a function to SessionPublisher.
type SessionPublisherFunc func(ctx context.Context, payload turncmd.SessionTurnPayload) (*actortransport.DispatchReceipt, error)

// PublishSessionTurn implements SessionPublisher.
func (f SessionPublisherFunc) PublishSessionTurn(ctx context.Context, payload turncmd.SessionTurnPayload) (*actortransport.DispatchReceipt, error) {
	return f(ctx, payload)
}

// JobPublisher publishes a webhook job into the JobActor.
type JobPublisher interface {
	PublishWebhookJob(ctx context.Context, payload turncmd.SessionTurnPayload, routeName string, requestID string) (*actortransport.DispatchReceipt, string, error)
}

// JobPublisherFunc adapts a function to JobPublisher.
type JobPublisherFunc func(ctx context.Context, payload turncmd.SessionTurnPayload, routeName string, requestID string) (*actortransport.DispatchReceipt, string, error)

// PublishWebhookJob implements JobPublisher.
func (f JobPublisherFunc) PublishWebhookJob(ctx context.Context, payload turncmd.SessionTurnPayload, routeName string, requestID string) (*actortransport.DispatchReceipt, string, error) {
	return f(ctx, payload, routeName, requestID)
}

// TargetNotFoundError indicates that the requested target or report-to destination could not be resolved.
type TargetNotFoundError struct {
	Cause error
}

func (e *TargetNotFoundError) Error() string {
	if e == nil || e.Cause == nil {
		return "target not found"
	}
	return e.Cause.Error()
}

func (e *TargetNotFoundError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// IsTargetNotFound reports whether an error represents a failed target resolution.
func IsTargetNotFound(err error) bool {
	var targetErr *TargetNotFoundError
	return errors.As(err, &targetErr)
}

// QueueFullError indicates that the publication stream is under backpressure.
type QueueFullError struct {
	Cause error
}

func (e *QueueFullError) Error() string {
	if e == nil || e.Cause == nil {
		return "command queue is full"
	}
	return e.Cause.Error()
}

func (e *QueueFullError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// IsQueueFull reports whether an error represents queue backpressure.
func IsQueueFull(err error) bool {
	var queueErr *QueueFullError
	return errors.As(err, &queueErr)
}

// DispatchFailedError indicates that publishing the command failed.
type DispatchFailedError struct {
	Cause error
}

func (e *DispatchFailedError) Error() string {
	if e == nil || e.Cause == nil {
		return "dispatch failed"
	}
	return e.Cause.Error()
}

func (e *DispatchFailedError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// IsDispatchFailed reports whether an error represents a dispatch or runtime failure.
func IsDispatchFailed(err error) bool {
	var dispatchErr *DispatchFailedError
	return errors.As(err, &dispatchErr)
}

// InvalidRequestError indicates that request fields failed basic validation.
type InvalidRequestError struct {
	Field   string
	Message string
}

func (e *InvalidRequestError) Error() string {
	if e == nil {
		return "invalid request"
	}
	if e.Field != "" {
		return fmt.Sprintf("invalid request: %s: %s", e.Field, e.Message)
	}
	return fmt.Sprintf("invalid request: %s", e.Message)
}

// IsInvalidRequest reports whether an error represents an invalid request.
func IsInvalidRequest(err error) bool {
	var invalidErr *InvalidRequestError
	return errors.As(err, &invalidErr)
}
