// Package sessionturn owns restoration and execution orchestration for queued session turns.
package sessionturn

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/normahq/balda/internal/apps/balda/deliveryfmt"
	"github.com/normahq/balda/internal/apps/balda/turncmd"
	"github.com/rs/zerolog"
	"go.uber.org/fx"
	adkrunner "google.golang.org/adk/v2/runner"
)

const ownerSessionLabel = "balda"
const (
	baldaMemoryStateKey        = "balda_memory"
	baldaMemoryVersionStateKey = "balda_memory_version"
)

// Request contains the resolved session context needed for one provider turn.
type Request struct {
	Payload          turncmd.SessionTurnPayload
	Session          ActiveSession
	UserID           string
	AgentSessionID   string
	DeliveryLocator  SessionLocator
	DeliveryOptions  deliveryfmt.Options
	MemoryRunOptions []adkrunner.RunOption
}

// Executor performs the provider iteration and delivery side effects.
type Executor interface {
	ExecuteSessionTurn(ctx context.Context, request Request) error
}

type SessionLocator struct {
	SessionID   string
	ChannelType string
	AddressKey  string
	AddressJSON string
}

type SessionContext struct {
	Locator SessionLocator
	UserID  string
}

type ActiveSession interface {
	GetRunner() *adkrunner.Runner
	GetSessionID() string
	GetAgentSessionID() string
	GetUserID() string
	RuntimeStateValue(ctx context.Context, key string) (any, bool, error)
}

type SessionAccessor interface {
	GetSession(locator SessionLocator) (ActiveSession, error)
	RestoreSession(ctx context.Context, sessionCtx SessionContext) (ActiveSession, error)
	EnsureSession(ctx context.Context, sessionCtx SessionContext, agentName string) (ActiveSession, error)
}

type MemorySnapshot struct {
	Content string
	Version int64
}

type MemoryStateProvider interface {
	Enabled() bool
	Snapshot(ctx context.Context) (MemorySnapshot, error)
}

// Runner restores the target session before delegating provider execution.
type Runner struct {
	sessions SessionAccessor
	executor Executor
	memory   MemoryStateProvider
	logger   zerolog.Logger
}

type runnerParams struct {
	fx.In

	Sessions SessionAccessor
	Executor Executor
	Memory   MemoryStateProvider
	Logger   zerolog.Logger
}

// NewRunner creates the queued session-turn use case.
func NewRunner(params runnerParams) *Runner {
	return New(params.Sessions, params.Executor, params.Memory, params.Logger)
}

// New creates a Runner from explicit dependencies.
func New(sessions SessionAccessor, executor Executor, memoryStore MemoryStateProvider, logger zerolog.Logger) *Runner {
	return &Runner{
		sessions: sessions,
		executor: executor,
		memory:   memoryStore,
		logger:   logger.With().Str("component", "balda.session_turn").Logger(),
	}
}

// RunSessionTurnPayload restores the target session and executes one provider turn.
func (r *Runner) RunSessionTurnPayload(ctx context.Context, payload turncmd.SessionTurnPayload) error {
	if r.sessions == nil {
		return fmt.Errorf("session turn: session manager is unavailable")
	}
	if r.executor == nil {
		return fmt.Errorf("session turn: executor is unavailable")
	}
	locator := sessionLocatorFromPayload(payload)
	topicSession, err := r.sessions.GetSession(locator)
	if err != nil {
		userID := strings.TrimSpace(payload.UserID)
		topicSession, err = r.sessions.RestoreSession(ctx, SessionContext{
			Locator: locator,
			UserID:  userID,
		})
		if err != nil {
			if !errors.Is(err, ErrNoPersistedSession) {
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
			topicSession, err = r.sessions.EnsureSession(ctx, SessionContext{
				Locator: locator,
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
		Payload:        payload,
		Session:        topicSession,
		UserID:         userID,
		AgentSessionID: agentSessionID,
		DeliveryLocator: SessionLocator{
			SessionID:   deliveryLocator.SessionID,
			ChannelType: deliveryLocator.ChannelType,
			AddressKey:  deliveryLocator.AddressKey,
			AddressJSON: deliveryLocator.AddressJSON,
		},
		DeliveryOptions:  turncmd.NormalizeSessionDeliveryOptions(payload),
		MemoryRunOptions: runOptions,
	})
}

var ErrNoPersistedSession = errors.New("no persisted session")

func prepareMemoryRunOptions(
	ctx context.Context,
	store MemoryStateProvider,
	topicSession ActiveSession,
) ([]adkrunner.RunOption, error) {
	if store == nil || !store.Enabled() || topicSession == nil {
		return nil, nil
	}
	snapshot, err := store.Snapshot(ctx)
	if err != nil {
		return nil, fmt.Errorf("snapshot balda memory: %w", err)
	}
	seenVersion := int64(0)
	value, ok, err := topicSession.RuntimeStateValue(ctx, baldaMemoryVersionStateKey)
	if err != nil {
		return nil, fmt.Errorf("read balda memory version: %w", err)
	}
	if ok {
		seenVersion = versionFromState(value)
	}
	if snapshot.Version <= seenVersion {
		return nil, nil
	}
	return []adkrunner.RunOption{adkrunner.WithStateDelta(map[string]any{
		baldaMemoryStateKey:        strings.TrimSpace(snapshot.Content),
		baldaMemoryVersionStateKey: versionStateValue(snapshot.Version),
	})}, nil
}

func versionFromState(value any) int64 {
	switch raw := value.(type) {
	case int64:
		return raw
	case int:
		return int64(raw)
	case int32:
		return int64(raw)
	case float64:
		return int64(raw)
	case string:
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			return 0
		}
		var parsed int64
		if _, err := fmt.Sscan(trimmed, &parsed); err != nil {
			return 0
		}
		return parsed
	default:
		return 0
	}
}

func versionStateValue(version int64) string {
	return fmt.Sprintf("%d", version)
}

func sessionLocatorFromPayload(payload turncmd.SessionTurnPayload) SessionLocator {
	return SessionLocator{
		SessionID:   payload.Locator.SessionID,
		ChannelType: payload.Locator.ChannelType,
		AddressKey:  payload.Locator.AddressKey,
		AddressJSON: payload.Locator.AddressJSON,
	}
}
