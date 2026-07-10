// Package sessionturn owns restoration and execution orchestration for queued session turns.
package sessionturn

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/normahq/balda/internal/apps/balda/actors"
	"github.com/normahq/balda/internal/apps/balda/deliveryfmt"
	"github.com/normahq/balda/internal/apps/balda/memory"
	baldasession "github.com/normahq/balda/internal/apps/balda/session"
	"github.com/rs/zerolog"
	"go.uber.org/fx"
	adkrunner "google.golang.org/adk/v2/runner"
)

const ownerSessionLabel = "balda"

// Request contains the resolved session context needed for one provider turn.
type Request struct {
	Payload          actors.SessionTurnPayload
	Session          *baldasession.TopicSession
	UserID           string
	AgentSessionID   string
	DeliveryLocator  baldasession.SessionLocator
	DeliveryOptions  deliveryfmt.Options
	MemoryRunOptions []adkrunner.RunOption
}

// Executor performs the provider iteration and delivery side effects.
type Executor interface {
	ExecuteSessionTurn(ctx context.Context, request Request) error
}

// Runner restores the target session before delegating provider execution.
type Runner struct {
	sessions *baldasession.Manager
	executor Executor
	memory   *memory.Store
	logger   zerolog.Logger
}

type runnerParams struct {
	fx.In

	Sessions *baldasession.Manager
	Executor Executor
	Memory   *memory.Store
	Logger   zerolog.Logger
}

// NewRunner creates the queued session-turn use case.
func NewRunner(params runnerParams) *Runner {
	return New(params.Sessions, params.Executor, params.Memory, params.Logger)
}

// New creates a Runner from explicit dependencies.
func New(sessions *baldasession.Manager, executor Executor, memoryStore *memory.Store, logger zerolog.Logger) *Runner {
	return &Runner{
		sessions: sessions,
		executor: executor,
		memory:   memoryStore,
		logger:   logger.With().Str("component", "balda.session_turn").Logger(),
	}
}

// RunSessionTurnPayload restores the target session and executes one provider turn.
func (r *Runner) RunSessionTurnPayload(ctx context.Context, payload actors.SessionTurnPayload) error {
	if r.sessions == nil {
		return fmt.Errorf("session turn: session manager is unavailable")
	}
	if r.executor == nil {
		return fmt.Errorf("session turn: executor is unavailable")
	}
	topicSession, err := r.sessions.GetSession(payload.Locator)
	if err != nil {
		userID := strings.TrimSpace(payload.UserID)
		topicSession, err = r.sessions.RestoreSession(ctx, baldasession.SessionContext{
			Locator: payload.Locator,
			UserID:  userID,
		})
		if err != nil {
			if !errors.Is(err, baldasession.ErrNoPersistedSession) {
				return fmt.Errorf("restore session for queued turn: %w", err)
			}
			if userID == "" {
				r.logger.Debug().
					Str("session_id", payload.Locator.SessionID).
					Str("channel_type", payload.Locator.ChannelType).
					Str("address_key", payload.Locator.AddressKey).
					Msg("dropping queued turn for unknown session without transport user")
				return nil
			}
			topicSession, err = r.sessions.EnsureSession(ctx, baldasession.SessionContext{
				Locator: payload.Locator,
				UserID:  userID,
			}, ownerSessionLabel)
			if err != nil {
				return fmt.Errorf("create session for queued turn: %w", err)
			}
		}
	}
	if topicSession == nil {
		return fmt.Errorf("session turn: session %s unavailable after restore", payload.Locator.SessionID)
	}
	userID := strings.TrimSpace(payload.UserID)
	if userID == "" {
		userID = topicSession.GetUserID()
	}
	agentSessionID := strings.TrimSpace(payload.AgentSessionID)
	if agentSessionID == "" {
		agentSessionID = topicSession.GetAgentSessionID()
	}
	deliveryLocator := payload.Locator
	if payload.ReportTo != nil {
		deliveryLocator = *payload.ReportTo
	}
	runOptions, err := prepareMemoryRunOptions(ctx, r.memory, topicSession)
	if err != nil {
		return err
	}
	return r.executor.ExecuteSessionTurn(ctx, Request{
		Payload:          payload,
		Session:          topicSession,
		UserID:           userID,
		AgentSessionID:   agentSessionID,
		DeliveryLocator:  deliveryLocator,
		DeliveryOptions:  actors.NormalizeSessionDeliveryOptions(payload),
		MemoryRunOptions: runOptions,
	})
}

func prepareMemoryRunOptions(
	ctx context.Context,
	store *memory.Store,
	topicSession *baldasession.TopicSession,
) ([]adkrunner.RunOption, error) {
	if store == nil || !store.MemoryEnabled() || topicSession == nil {
		return nil, nil
	}
	snapshot, err := store.Snapshot(ctx)
	if err != nil {
		return nil, fmt.Errorf("snapshot balda memory: %w", err)
	}
	seenVersion := int64(0)
	value, ok, err := topicSession.RuntimeStateValue(ctx, memory.MemoryVersionStateKey)
	if err != nil {
		return nil, fmt.Errorf("read balda memory version: %w", err)
	}
	if ok {
		seenVersion = memory.VersionFromState(value)
	}
	if snapshot.Version <= seenVersion {
		return nil, nil
	}
	return []adkrunner.RunOption{adkrunner.WithStateDelta(map[string]any{
		memory.MemoryStateKey:        strings.TrimSpace(snapshot.Content),
		memory.MemoryVersionStateKey: memory.VersionStateValue(snapshot.Version),
	})}, nil
}
