// Package modelapp queues command-neutral model changes through the existing
// per-session turn admission path.
package modelapp

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/appports"
	baldasession "github.com/baldaworks/balda/internal/apps/balda/session"
)

var (
	ErrInvalidRequest = errors.New("invalid model change request")
	ErrConflict       = errors.New("conflict")
	ErrUnavailable    = errors.New("unavailable")
)

const modelInputHashVersion = "balda:model-change:v1"

type Request struct {
	OperationID string
	SessionID   string
	Model       string
}

type Completion struct {
	Result baldasession.ModelChangeResult
	Err    error
}

type modelChanger interface {
	ChangeModel(ctx context.Context, request baldasession.ModelChangeRequest) (baldasession.ModelChangeResult, error)
}

type Service struct {
	queue   appports.TurnQueue
	changer modelChanger
}

func New(queue appports.TurnQueue, changer *baldasession.Manager) *Service {
	return newService(queue, changer)
}

func newService(queue appports.TurnQueue, changer modelChanger) *Service {
	return &Service{queue: queue, changer: changer}
}

// Submit queues one model operation behind any active turn. SessionTurn is
// deliberately nil so the dispatcher cannot steering-merge it with a prompt.
func (s *Service) Submit(ctx context.Context, request Request) (<-chan Completion, int, error) {
	request.OperationID = strings.TrimSpace(request.OperationID)
	request.SessionID = strings.TrimSpace(request.SessionID)
	request.Model = strings.TrimSpace(request.Model)
	if s == nil || s.queue == nil || s.changer == nil || request.OperationID == "" || request.SessionID == "" || request.Model == "" {
		return nil, 0, ErrInvalidRequest
	}

	completion := make(chan Completion, 1)
	runResult := make(chan Completion, 1)
	inputHash := modelInputHash(request)
	queued, position, err := s.queue.Enqueue(ctx, appports.TurnTask{
		SessionID: request.SessionID,
		Run: func(runCtx context.Context) error {
			result, changeErr := s.changer.ChangeModel(runCtx, baldasession.ModelChangeRequest{
				OperationID: request.OperationID,
				SessionID:   request.SessionID,
				InputHash:   inputHash,
				TargetModel: request.Model,
			})
			runResult <- Completion{Result: result, Err: changeErr}
			return changeErr
		},
	})
	if err != nil {
		if errors.Is(err, appports.ErrTurnQueueFull) {
			return nil, 0, ErrConflict
		}
		return nil, 0, ErrUnavailable
	}

	go func() {
		defer close(completion)
		queueErr := <-queued
		select {
		case result := <-runResult:
			completion <- result
		default:
			completion <- Completion{Err: queueErr}
		}
	}()
	return completion, position, nil
}

func modelInputHash(request Request) string {
	request.OperationID = strings.TrimSpace(request.OperationID)
	request.SessionID = strings.TrimSpace(request.SessionID)
	request.Model = strings.TrimSpace(request.Model)
	hash := sha256.New()
	_, _ = hash.Write([]byte(modelInputHashVersion))
	for _, field := range []string{request.OperationID, request.SessionID, request.Model} {
		var size [4]byte
		binary.BigEndian.PutUint32(size[:], uint32(len(field)))
		_, _ = hash.Write(size[:])
		_, _ = hash.Write([]byte(field))
	}
	return hex.EncodeToString(hash.Sum(nil))
}
