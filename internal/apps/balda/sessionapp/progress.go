package sessionapp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/actorcmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/jobs"
	"github.com/baldaworks/balda/internal/apps/balda/redaction"
	baldasession "github.com/baldaworks/balda/internal/apps/balda/session"
	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/rs/zerolog"
)

type progressTargetResolver interface {
	ResolveProgressTarget(ctx context.Context, locator baldasession.SessionLocator) (baldasession.ProgressTarget, error)
}

type progressJobEvents interface {
	AppendEvent(ctx context.Context, jobID string, eventType string, actor string, messageID string, payload any) error
}

const (
	maxProgressTextBytes         = 4096
	maxProgressKeyBytes          = 256
	progressDispositionDelivered = "delivered"
	progressDispositionRecorded  = "recorded"
)

// ProgressResult describes the accepted durable route for one progress report.
type ProgressResult struct {
	ProgressID  string
	Disposition string
}

// ProgressService routes explicit session progress through existing delivery
// and job event paths.
type ProgressService struct {
	targets    progressTargetResolver
	dispatcher actortransport.Dispatcher
	jobEvents  progressJobEvents
	logger     zerolog.Logger
}

// NewProgressService constructs the locator-scoped progress application service.
func NewProgressService(
	targets *baldasession.Manager,
	dispatcher actortransport.Dispatcher,
	jobEvents *jobs.JobEventsService,
	logger zerolog.Logger,
) *ProgressService {
	return &ProgressService{
		targets: targets, dispatcher: dispatcher, jobEvents: jobEvents,
		logger: logger.With().Str("component", "balda.session_progress").Logger(),
	}
}

// Report records or delivers one idempotent progress report.
func (s *ProgressService) Report(
	ctx context.Context,
	locator baldasession.SessionLocator,
	requesterUserID string,
	text string,
	idempotencyKey string,
) (ProgressResult, error) {
	if s == nil || s.targets == nil {
		return ProgressResult{}, fmt.Errorf("session progress is unavailable")
	}
	rawText := strings.TrimSpace(text)
	key := strings.TrimSpace(idempotencyKey)
	if rawText == "" {
		return ProgressResult{}, fmt.Errorf("progress text is required")
	}
	if len(rawText) > maxProgressTextBytes {
		return ProgressResult{}, fmt.Errorf("progress text exceeds 4096 bytes")
	}
	if key == "" {
		return ProgressResult{}, fmt.Errorf("progress idempotency key is required")
	}
	if len(key) > maxProgressKeyBytes {
		return ProgressResult{}, fmt.Errorf("progress idempotency key exceeds 256 bytes")
	}
	target, err := s.targets.ResolveProgressTarget(ctx, locator)
	if err != nil {
		return ProgressResult{}, err
	}
	safeText := strings.TrimSpace(redaction.Secrets(rawText))
	if safeText == "" {
		return ProgressResult{}, fmt.Errorf("progress text is required")
	}
	progressID := sessionProgressID(target.SessionID, key)

	result := ProgressResult{ProgressID: progressID}
	switch target.Kind.Family() {
	case baldasession.FamilyConversation:
		requesterUserID = strings.TrimSpace(requesterUserID)
		if requesterUserID == "" {
			return ProgressResult{}, fmt.Errorf("session progress requester is required")
		}
		if s.dispatcher == nil {
			return ProgressResult{}, fmt.Errorf("session progress delivery is unavailable")
		}
		env, err := deliverycmd.ProgressTechnicalEnvelope(
			"",
			actorlayer.ActorAddress{Target: actorcmd.ActorTypeSession, Key: target.SessionID},
			target.Locator,
			deliverycmd.SettlementOutbox,
			safeText,
			deliverycmd.ProgressAudience{UserID: requesterUserID},
			progressID,
		)
		if err != nil {
			return ProgressResult{}, err
		}
		env.ID = progressID
		env.DedupeKey = progressID
		if _, err := s.dispatcher.Dispatch(ctx, env); err != nil {
			return ProgressResult{}, fmt.Errorf("dispatch session progress: %w", err)
		}
		result.Disposition = progressDispositionDelivered
	case baldasession.FamilyService:
		if s.jobEvents == nil {
			return ProgressResult{}, fmt.Errorf("session progress history is unavailable")
		}
		if err := s.jobEvents.AppendEvent(ctx, target.OwnerJobID, jobs.JobEventAgentProgress,
			actorcmd.ActorTypeSession+":"+target.SessionID, progressID, map[string]any{"text": safeText}); err != nil {
			return ProgressResult{}, fmt.Errorf("record session progress: %w", err)
		}
		result.Disposition = progressDispositionRecorded
	default:
		return ProgressResult{}, fmt.Errorf("unsupported session progress kind %q", target.Kind)
	}
	s.logger.Info().
		Str("progress_id", progressID).
		Str("session_kind", string(target.Kind)).
		Str("disposition", result.Disposition).
		Msg("session progress accepted")
	return result, nil
}

func sessionProgressID(sessionID, idempotencyKey string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(sessionID) + "\x00" + strings.TrimSpace(idempotencyKey)))
	return "progress-" + hex.EncodeToString(sum[:])
}
