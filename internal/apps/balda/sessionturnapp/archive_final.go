package sessionturnapp

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/chatarchivecmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/baldaworks/balda/internal/apps/balda/jobs"
	"github.com/baldaworks/balda/internal/apps/balda/session"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
	"github.com/baldaworks/go-actorlayer"
	"github.com/rs/zerolog"
)

// FinalArchive is a mandatory production port for immutable selection and replay.
type FinalArchive interface {
	CommitFinal(ctx context.Context, message chatarchivecmd.Message, members []string) (chatarchivecmd.Message, error)
	FindFinal(ctx context.Context, epoch, member string) (chatarchivecmd.Message, error)
}

// SetFinalArchive installs the selected database adapter at composition time.
func (s *TurnExecutionService) SetFinalArchive(archive FinalArchive) { s.finalArchive = archive }

// finalReplay contains publication and bookkeeping inputs only, never the prompt,
// runner, tool events or provider error. The delivery envelope is frozen at commit.
type finalReplay struct {
	Envelope        actorlayer.Envelope
	Locator         session.SessionLocator
	FormatAnnounced bool
	FormatName      deliveryfmt.Name
	CommitFormat    bool
	ArchiveID       string
	SessionID       string
	AgentSessionID  string
	UserID          string
	RequesterUserID string
	JobID           string
	TurnSource      string
	DedupeKey       string
	Options         deliveryfmt.Options
}

func finalMembers(req ExecutionRequest) ([]string, error) {
	members := []string{req.DedupeKey}
	if len(req.SteeringMessages) > 0 {
		members = nil
		for _, m := range req.SteeringMessages {
			if m.ArchiveID != req.ArchiveID {
				return nil, chatarchivecmd.ErrConflict
			}
			members = append(members, m.DedupeKey)
		}
	}
	seen := map[string]bool{}
	unique := members[:0]
	for _, m := range members {
		if strings.TrimSpace(m) == "" {
			return nil, chatarchivecmd.ErrInvalid
		}
		if !seen[m] {
			unique = append(unique, m)
			seen[m] = true
		}
	}
	sort.Strings(unique)
	return unique, nil
}

func (s *TurnExecutionService) commitSelectedFinal(ctx context.Context, req ExecutionRequest, text string, change *formatStateChange) (chatarchivecmd.Message, error) {
	if s.finalArchive == nil {
		return chatarchivecmd.Message{}, &turncmd.FinalArchiveError{Cause: chatarchivecmd.ErrUnavailable}
	}
	members, err := finalMembers(req)
	if err != nil {
		return chatarchivecmd.Message{}, &turncmd.FinalArchiveError{Cause: err}
	}
	raw, _ := json.Marshal(struct {
		Epoch   string
		Members []string
	}{req.ArchiveID, members})
	source := fmt.Sprintf("final:%x", sha256.Sum256(raw))
	settlement := deliverycmd.SettlementBypass
	if req.JobID != "" {
		settlement = deliverycmd.SettlementOutbox
	}
	env, err := deliverycmd.AgentReplyEnvelopeWithFormatAndSettlement(req.JobID, req.OutboundFrom, req.Locator, req.DeliveryOptions.DeliveryFormat, settlement, text, "final")
	if err != nil {
		return chatarchivecmd.Message{}, &turncmd.FinalArchiveError{Cause: err}
	}
	// Job-backed finals keep their existing stable delivery key. Conversational
	// bypass delivery gains the persisted logical final key, without an outbox.
	if req.JobID == "" {
		env.ID = source
		env.DedupeKey = source
	}
	continuationKey := req.DedupeKey
	if len(req.SteeringMessages) > 0 {
		continuationKey = source
	}
	replay := finalReplay{Envelope: env, Locator: req.Locator, ArchiveID: req.ArchiveID, SessionID: req.SessionID, AgentSessionID: req.AgentSessionID, UserID: req.UserID, RequesterUserID: req.RequesterUserID, JobID: req.JobID, TurnSource: req.TurnSource, DedupeKey: continuationKey, Options: req.DeliveryOptions}
	if change != nil {
		replay.CommitFormat = true
		replay.FormatAnnounced = change.announced
		replay.FormatName = change.name
	}
	data, err := json.Marshal(replay)
	if err != nil {
		return chatarchivecmd.Message{}, &turncmd.FinalArchiveError{Cause: err}
	}
	candidate := chatarchivecmd.Message{SessionID: req.ArchiveID, Source: source, Role: "agent", Text: text, ObservedAt: s.currentTime().UTC(), Replay: data}
	// A bounded retry reuses the same bytes, timestamp, identity and membership.
	for range 3 {
		committed, err := s.finalArchive.CommitFinal(ctx, candidate, members)
		if err == nil {
			return committed, nil
		}
		if !errors.Is(err, chatarchivecmd.ErrUnavailable) || ctx.Err() != nil {
			return candidate, &turncmd.FinalArchiveError{Cause: err}
		}
	}
	return candidate, &turncmd.FinalArchiveError{Cause: chatarchivecmd.ErrUnavailable}
}

func (s *TurnExecutionService) resumeFinal(ctx context.Context, final chatarchivecmd.Message) (runErr error) {
	var replay finalReplay
	if err := json.Unmarshal(final.Replay, &replay); err != nil {
		return fmt.Errorf("decode committed final: %w", err)
	}
	if replay.ArchiveID != final.SessionID || replay.Envelope.ID == "" {
		return chatarchivecmd.ErrConflict
	}
	current := false
	var active activeRuntimeState
	if s.sessions != nil {
		var ok bool
		active, ok = s.sessions.(activeRuntimeState)
		if !ok {
			return chatarchivecmd.ErrUnavailable
		}
		if _, err := active.ResumeArchiveRuntime(ctx, replay.Locator, replay.ArchiveID); err != nil {
			return fmt.Errorf("resume final runtime: %w", err)
		}
		epoch, found, err := active.ActiveRuntimeStateValue(ctx, replay.Locator, chatarchivecmd.EpochStateKey)
		if err != nil {
			return fmt.Errorf("read final runtime epoch: %w", err)
		}
		current = found && epoch == replay.ArchiveID
	}
	// Never mutate a replacement runtime during late reply recovery.
	if current && replay.CommitFormat && s.formatComposer != nil {
		if _, err := active.UpdateRuntimeStateForArchive(ctx, replay.Locator, replay.ArchiveID, map[string]any{stateKeyMessageFormatAnnounced: replay.FormatAnnounced, stateKeyMessageFormatName: string(replay.FormatName)}); err != nil {
			return err
		}
	}
	if err := dispatchOutbound(ctx, s.dispatcher, replay.Envelope); err != nil {
		return fmt.Errorf("publish committed final: %w", err)
	}
	defer func() {
		if runErr != nil {
			runErr = &turncmd.FinalOutcomeAcceptedError{Cause: runErr}
		}
	}()
	if replay.JobID != "" {
		if err := s.appendJobEvent(ctx, replay.JobID, jobs.JobEventAgentResult, "session.actor", "", map[string]any{"text": strings.TrimSpace(final.Text)}); err != nil {
			return err
		}
	}
	if current {
		clone := *s
		clone.sessions = activeStateReader{active}
		return clone.maybeScheduleAutoTurn(ctx, ExecutionRequest{ArchiveID: replay.ArchiveID, Locator: replay.Locator, SessionID: replay.SessionID, AgentSessionID: replay.AgentSessionID, UserID: replay.UserID, RequesterUserID: replay.RequesterUserID, JobID: replay.JobID, TurnSource: replay.TurnSource, DedupeKey: replay.DedupeKey, DeliveryOptions: replay.Options, Deliver: true, OutboundFrom: replay.Envelope.From}, "streamed_text", strings.TrimSpace(final.Text))
	}
	return nil
}

// ReplaySessionTurn runs before session restoration and provider preparation.
// Committed constituents resume their frozen delivery; uncommitted constituents
// retain their own original input and form the remaining provider batch.
func (e *ProviderTurnExecutor) ReplaySessionTurn(ctx context.Context, payload turncmd.SessionTurnPayload) (turncmd.SessionTurnPayload, bool, error) {
	if payload.ArchiveID == "" || !payload.Deliver || turncmd.IsPrivateRun(payload.Source, payload.Locator.SessionID) {
		return payload, false, nil
	}
	if e.execution == nil || e.execution.finalArchive == nil {
		return payload, false, chatarchivecmd.ErrUnavailable
	}
	archive := e.execution.finalArchive
	members, err := finalMembers(ExecutionRequest{ArchiveID: payload.ArchiveID, DedupeKey: payload.DedupeKey, SteeringMessages: payload.SteeringMessages})
	if err != nil {
		return payload, false, err
	}
	committed := map[string]bool{}
	outputs := map[string]bool{}
	var acceptedErr error
	for _, member := range members {
		final, err := archive.FindFinal(ctx, payload.ArchiveID, member)
		if errors.Is(err, chatarchivecmd.ErrNotFound) {
			continue
		}
		if err != nil {
			return payload, false, err
		}
		committed[member] = true
		if !outputs[final.Source] {
			if err := e.execution.resumeFinal(ctx, final); err != nil {
				var accepted *turncmd.FinalOutcomeAcceptedError
				if !errors.As(err, &accepted) {
					return payload, true, err
				}
				acceptedErr = err
				zerolog.Ctx(ctx).Warn().Str("source_turn_id", final.Source).Msg("committed replay bookkeeping failed after acceptance")
			}
			outputs[final.Source] = true
		}
	}
	if len(committed) == 0 {
		return payload, false, nil
	}
	if len(committed) == len(members) {
		return payload, true, acceptedErr
	}
	remaining := make([]turncmd.SteeringMessage, 0, len(payload.SteeringMessages))
	for _, m := range payload.SteeringMessages {
		if !committed[m.DedupeKey] {
			remaining = append(remaining, m)
		}
	}
	if len(remaining) == 0 {
		return payload, true, chatarchivecmd.ErrConflict
	}
	payload.SteeringMessages = remaining
	payload.Attachments = nil
	for _, member := range remaining {
		payload.Attachments = append(payload.Attachments, member.Attachments...)
	}
	payload.MessageID = remaining[0].MessageID
	payload.ReplyToMessageID = remaining[0].ReplyToMessageID
	payload.ReceivedAt = remaining[0].ReceivedAt
	payload.RequesterUserID = remaining[0].UserID
	payload.DedupeKey = remaining[0].DedupeKey
	payload.Text = turncmd.RenderSteeringBatchText(remaining)
	return payload, false, nil
}

// activeRuntimeState can resume an exact durable lifetime for ordinary replay;
// active reads never restore and writes fence the lifecycle operation lock.
type activeRuntimeState interface {
	ResumeArchiveRuntime(ctx context.Context, locator session.SessionLocator, epoch string) (bool, error)
	ActiveRuntimeStateValue(ctx context.Context, locator session.SessionLocator, key string) (any, bool, error)
	UpdateRuntimeStateForArchive(ctx context.Context, locator session.SessionLocator, epoch string, values map[string]any) (bool, error)
}

type activeStateReader struct{ activeRuntimeState }

func (r activeStateReader) RuntimeStateValue(ctx context.Context, locator session.SessionLocator, key string) (any, bool, error) {
	return r.ActiveRuntimeStateValue(ctx, locator, key)
}
