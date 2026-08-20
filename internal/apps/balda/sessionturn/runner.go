// Package sessionturn owns restoration and execution orchestration for queued session turns.
package sessionturn

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

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
	baldaMemoryUpdatedAtKey    = "balda_memory_updated_at"
)

// Request contains the resolved session context needed for one provider turn.
type Request struct {
	Payload          turncmd.SessionTurnPayload
	Session          ActiveSession
	UserID           string
	AgentSessionID   string
	DeliveryLocator  SessionLocator
	DeliveryOptions  deliveryfmt.Options
	MemoryRefresh    MemoryRefresh
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
	Content   string
	Version   int64
	UpdatedAt string
}

// MemoryRefresh is the complete memory snapshot to add to one changed turn.
type MemoryRefresh struct {
	Content   string
	UpdatedAt string
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
	preparedMemory, err := prepareMemory(ctx, r.memory, topicSession, payload.Metadata)
	if err != nil {
		return err
	}
	if preparedMemory.updatedAt != "" {
		payload.Metadata = &turncmd.SessionTurnMetadata{LatestMemoryAt: preparedMemory.updatedAt}
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
		MemoryRefresh:    preparedMemory.refresh,
		MemoryRunOptions: preparedMemory.runOptions,
	})
}

var ErrNoPersistedSession = errors.New("no persisted session")

type memoryPreparation struct {
	refresh    MemoryRefresh
	runOptions []adkrunner.RunOption
	updatedAt  string
}

func prepareMemory(
	ctx context.Context,
	store MemoryStateProvider,
	topicSession ActiveSession,
	metadata *turncmd.SessionTurnMetadata,
) (memoryPreparation, error) {
	if store == nil || !store.Enabled() || topicSession == nil {
		return memoryPreparation{}, nil
	}
	snapshot, err := store.Snapshot(ctx)
	if err != nil {
		return memoryPreparation{}, fmt.Errorf("snapshot balda memory: %w", err)
	}
	content := strings.TrimSpace(snapshot.Content)
	if content == "" {
		return memoryPreparation{}, nil
	}
	cursor, err := memoryCursor(ctx, topicSession, metadata)
	if err != nil {
		return memoryPreparation{}, err
	}
	updatedAt, err := parseMemoryTimestamp("snapshot updated_at", snapshot.UpdatedAt)
	if err != nil {
		return memoryPreparation{}, err
	}
	canonicalUpdatedAt := updatedAt.UTC().Format(time.RFC3339Nano)
	prepared := memoryPreparation{updatedAt: canonicalUpdatedAt}
	if cursor != nil && cursor.Equal(updatedAt) {
		return prepared, nil
	}
	prepared.refresh = MemoryRefresh{Content: snapshot.Content, UpdatedAt: canonicalUpdatedAt}
	prepared.runOptions = []adkrunner.RunOption{adkrunner.WithStateDelta(map[string]any{
		baldaMemoryStateKey:        strings.TrimSpace(snapshot.Content),
		baldaMemoryVersionStateKey: versionStateValue(snapshot.Version),
		baldaMemoryUpdatedAtKey:    canonicalUpdatedAt,
	})}
	return prepared, nil
}

func memoryCursor(
	ctx context.Context,
	topicSession ActiveSession,
	metadata *turncmd.SessionTurnMetadata,
) (*time.Time, error) {
	if metadata != nil && strings.TrimSpace(metadata.LatestMemoryAt) != "" {
		cursor, err := parseMemoryTimestamp("turn memory cursor", metadata.LatestMemoryAt)
		if err != nil {
			return nil, err
		}
		return &cursor, nil
	}
	value, ok, err := topicSession.RuntimeStateValue(ctx, baldaMemoryUpdatedAtKey)
	if err != nil {
		return nil, fmt.Errorf("read balda memory updated_at: %w", err)
	}
	if !ok {
		return nil, nil
	}
	text, ok := value.(string)
	if !ok {
		return nil, fmt.Errorf("read balda memory updated_at: expected string, got %T", value)
	}
	if strings.TrimSpace(text) == "" {
		return nil, nil
	}
	cursor, err := parseMemoryTimestamp("session memory cursor", text)
	if err != nil {
		return nil, err
	}
	return &cursor, nil
}

func parseMemoryTimestamp(field, value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	if err != nil {
		return time.Time{}, fmt.Errorf("parse %s: %w", field, err)
	}
	return parsed, nil
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
