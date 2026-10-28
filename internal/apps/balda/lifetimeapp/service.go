// Package lifetimeapp queues command-neutral local lifetime replacements
// through the existing per-session replacement boundary.
package lifetimeapp

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/appports"
	baldasession "github.com/baldaworks/balda/internal/apps/balda/session"
	baldastate "github.com/baldaworks/balda/internal/apps/balda/state"
)

const (
	lifetimeInputHashVersion = "balda:fresh-lifetime:v1"
	settlementTimeout        = 10 * time.Second
)

// ErrInvalidRequest reports missing or unsupported public request fields.
var ErrInvalidRequest = errors.New("invalid fresh lifetime request")

// Status is bounded application evidence for one fresh lifetime request.
type Status string

const (
	StatusReset            Status = "reset"
	StatusConflict         Status = "conflict"
	StatusUnavailable      Status = "unavailable"
	StatusInterrupted      Status = "interrupted"
	StatusRecoveryRequired Status = "recovery_required"
)

// Request identifies one command envelope and its canonical lifecycle alias.
type Request struct {
	OperationID string
	SessionID   string
	Command     string
}

// Result contains no provider, archive, or runtime identity details.
type Result struct {
	Status     Status
	Generation uint64
}

// Completion reports the bounded terminal result of one queued request.
type Completion struct {
	Result Result
}

type freshLifetimeManager interface {
	PrepareFreshLifetime(ctx context.Context, request baldasession.FreshLifetimeRequest) (*baldasession.PreparedFreshLifetime, baldasession.FreshLifetimeResult, error)
	PublishFreshLifetime(ctx context.Context, prepared *baldasession.PreparedFreshLifetime) (baldasession.FreshLifetimeResult, error)
	AbortFreshLifetime(ctx context.Context, prepared *baldasession.PreparedFreshLifetime) (baldasession.FreshLifetimeResult, error)
}

// Service prepares local lifetime state before reserving the existing FIFO
// replacement boundary.
type Service struct {
	queue   appports.ReplacementQueue
	manager freshLifetimeManager
}

// New constructs the fresh lifetime application service.
func New(queue appports.ReplacementQueue, manager *baldasession.Manager) *Service {
	return newService(queue, manager)
}

func newService(queue appports.ReplacementQueue, manager freshLifetimeManager) *Service {
	return &Service{queue: queue, manager: manager}
}

// Submit prepares one exact empty local lifetime, then queues its publication
// behind work already admitted to the session FIFO.
func (s *Service) Submit(ctx context.Context, request Request) (<-chan Completion, int, error) {
	request = normalizeRequest(request)
	if s == nil || s.queue == nil || s.manager == nil || !validRequest(request) {
		return nil, 0, ErrInvalidRequest
	}

	inputHash := lifetimeInputHash(request)
	prepared, preparedResult, prepareErr := s.manager.PrepareFreshLifetime(ctx, baldasession.FreshLifetimeRequest{
		OperationID: request.OperationID,
		SessionID:   request.SessionID,
		InputHash:   inputHash,
	})
	if prepared == nil {
		return completed(mapManagerResult(preparedResult, prepareErr)), 0, nil
	}

	runResult := make(chan Result, 1)
	queued, position, err := s.queue.EnqueueReplacement(ctx, appports.ReplacementTask{
		SessionID:   request.SessionID,
		OperationID: request.OperationID,
		InputHash:   inputHash,
		Run: func(runCtx context.Context) error {
			result, publishErr := s.manager.PublishFreshLifetime(runCtx, prepared)
			mapped := mapManagerResult(result, publishErr)
			runResult <- mapped
			return replacementError(mapped)
		},
	})
	if err != nil || queued == nil {
		result := s.abortAfterAdmission(ctx, prepared)
		if result.Status != StatusRecoveryRequired && result.Status != StatusReset {
			result = mapAdmissionError(err)
		}
		return completed(result), 0, nil
	}

	completion := make(chan Completion, 1)
	go func() {
		defer close(completion)
		queueErr := <-queued
		select {
		case result := <-runResult:
			completion <- Completion{Result: result}
		default:
			result := s.abortAfterAdmission(ctx, prepared)
			if result.Status != StatusRecoveryRequired && result.Status != StatusReset {
				result = mapAdmissionError(queueErr)
			}
			completion <- Completion{Result: result}
		}
	}()
	return completion, position, nil
}

func (s *Service) abortAfterAdmission(ctx context.Context, prepared *baldasession.PreparedFreshLifetime) Result {
	settleCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), settlementTimeout)
	defer cancel()
	result, err := s.manager.AbortFreshLifetime(settleCtx, prepared)
	return mapManagerResult(result, err)
}

func normalizeRequest(request Request) Request {
	request.OperationID = strings.TrimSpace(request.OperationID)
	request.SessionID = strings.TrimSpace(request.SessionID)
	request.Command = strings.ToLower(strings.TrimSpace(request.Command))
	return request
}

func validRequest(request Request) bool {
	return request.OperationID != "" && request.SessionID != "" &&
		(request.Command == "reset" || request.Command == "new")
}

func lifetimeInputHash(request Request) string {
	request = normalizeRequest(request)
	hash := sha256.New()
	_, _ = hash.Write([]byte(lifetimeInputHashVersion))
	for _, field := range []string{request.OperationID, request.SessionID, request.Command} {
		var size [4]byte
		binary.BigEndian.PutUint32(size[:], uint32(len(field)))
		_, _ = hash.Write(size[:])
		_, _ = hash.Write([]byte(field))
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func mapManagerResult(result baldasession.FreshLifetimeResult, err error) Result {
	switch result.Status {
	case baldasession.FreshLifetimeReset:
		return Result{Status: StatusReset, Generation: result.Generation}
	case baldasession.FreshLifetimeConflict:
		return Result{Status: StatusConflict}
	case baldasession.FreshLifetimeRecoveryRequired:
		return Result{Status: StatusRecoveryRequired}
	case baldasession.FreshLifetimeUnavailable:
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
			result.Reason == baldastate.RuntimeOperationReasonCanceled ||
			result.Reason == baldastate.RuntimeOperationReasonDeadlineExceeded {
			return Result{Status: StatusInterrupted}
		}
		return Result{Status: StatusUnavailable}
	}
	if errors.Is(err, baldasession.ErrRuntimeRecoveryRequired) {
		return Result{Status: StatusRecoveryRequired}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return Result{Status: StatusInterrupted}
	}
	return Result{Status: StatusUnavailable}
}

func mapAdmissionError(err error) Result {
	switch {
	case errors.Is(err, appports.ErrTurnReplacementConflict),
		errors.Is(err, appports.ErrTurnReplacementPending),
		errors.Is(err, appports.ErrTurnQueueFull):
		return Result{Status: StatusConflict}
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return Result{Status: StatusInterrupted}
	default:
		return Result{Status: StatusUnavailable}
	}
}

func replacementError(result Result) error {
	switch result.Status {
	case StatusReset:
		return nil
	case StatusConflict:
		return appports.ErrTurnReplacementConflict
	case StatusInterrupted:
		return context.Canceled
	default:
		return appports.ErrTurnReplacementFailed
	}
}

func completed(result Result) <-chan Completion {
	completion := make(chan Completion, 1)
	completion <- Completion{Result: result}
	close(completion)
	return completion
}
