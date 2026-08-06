package sessionturnapp

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"time"

	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	baldaexecution "github.com/normahq/balda/internal/apps/balda/actorcmd"
	"github.com/normahq/balda/internal/apps/balda/attachment"
	"github.com/normahq/balda/internal/apps/balda/automode"
	"github.com/normahq/balda/internal/apps/balda/automodecmd"
	"github.com/normahq/balda/internal/apps/balda/deliverycmd"
	"github.com/normahq/balda/internal/apps/balda/deliveryfmt"
	baldajobs "github.com/normahq/balda/internal/apps/balda/jobs"
	"github.com/normahq/balda/internal/apps/balda/permissioncmd"
	"github.com/normahq/balda/internal/apps/balda/progress"
	"github.com/normahq/balda/internal/apps/balda/questioncmd"
	baldasession "github.com/normahq/balda/internal/apps/balda/session"
	"github.com/normahq/balda/internal/apps/balda/telegramref"
	"github.com/normahq/balda/internal/apps/balda/turncmd"
	"github.com/normahq/balda/internal/apps/balda/usageview"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/runner"
	adksession "google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

type jobEventAppender interface {
	AppendEvent(ctx context.Context, jobID string, eventType string, actor string, messageID string, payload any) error
}

type runtimeStateReader interface {
	RuntimeStateValue(ctx context.Context, locator baldasession.SessionLocator, key string) (any, bool, error)
}

// CompletedTurn is the transport-neutral input handed to the optional
// session-memory capture hook after ADK reports TurnComplete. The hook owns
// normalization and durable publication; sessionturnapp only owns the seam.
type CompletedTurn struct {
	UserText       string
	AssistantText  string
	Locator        baldasession.SessionLocator
	SessionID      string
	AgentSessionID string
	SourceTurnID   string
	CompletedAt    time.Time
	TerminalStatus TerminalStatus
	TrustedTools   []TrustedToolEvidence
}

// TrustedToolEvidence is a typed provider-tool response explicitly marked as
// eligible for session-memory capture. The downstream capture policy still
// enforces its own tool-name allowlist before durable ingress.
type TrustedToolEvidence struct {
	Name   string
	CallID string
	Text   string
}

// TerminalStatus is the provider-terminal outcome exposed through the capture
// port. The composition adapter translates it to the session-memory contract.
type TerminalStatus string

const (
	TerminalStatusSuccess     TerminalStatus = "success"
	TerminalStatusFailed      TerminalStatus = "failed"
	TerminalStatusInterrupted TerminalStatus = "interrupted"
)

// CompletedTurnCapture is a small consuming-package port. Capture failures
// are reported to the caller but must never change the user-facing delivery
// result of a completed turn.
type CompletedTurnCapture interface {
	CaptureCompletedTurn(ctx context.Context, turn CompletedTurn) error
}

const (
	responseSourceNone            = "none"
	responseSourceAutoDone        = "auto_done"
	responseSourceAutoWaitForUser = "auto_wait_for_user"
)

type TurnExecutionService struct {
	dispatcher     actortransport.Dispatcher
	jobEvents      jobEventAppender
	sessions       runtimeStateReader
	turnCapture    CompletedTurnCapture
	formatComposer *FormatPromptComposer
	logger         zerolog.Logger
	autoMaxTurns   int
	now            func() time.Time
}

func (s *TurnExecutionService) currentTime() time.Time {
	if s == nil || s.now == nil {
		return time.Now()
	}
	return s.now()
}

type ExecutionRequest struct {
	Text            string
	Attachments     []attachment.Descriptor
	Runner          *runner.Runner
	UserID          string
	RequesterUserID string
	SessionID       string
	JobID           string
	AgentSessionID  string
	Locator         baldasession.SessionLocator
	MessageID       int
	DeliveryOptions deliveryfmt.Options
	Deliver         bool
	ProgressEmitter SessionProgressEmitter
	OutboundFrom    actorlayer.ActorAddress
	RunOptions      []runner.RunOption
	TurnSource      string
	DedupeKey       string
}

func NewTurnExecutionService(dispatcher actortransport.Dispatcher, jobEvents *baldajobs.JobEventsService, sessions *baldasession.Manager, logger zerolog.Logger, autoMaxTurns int) *TurnExecutionService {
	return NewTurnExecutionServiceWithJobEvents(dispatcher, jobEvents, sessions, logger, autoMaxTurns)
}

func NewTurnExecutionServiceWithJobEvents(dispatcher actortransport.Dispatcher, jobEvents jobEventAppender, sessions runtimeStateReader, logger zerolog.Logger, autoMaxTurns int) *TurnExecutionService {
	return NewTurnExecutionServiceWithJobEventsAndCapture(dispatcher, jobEvents, sessions, logger, autoMaxTurns, nil)
}

// NewTurnExecutionServiceWithJobEventsAndCapture creates a turn executor with
// an optional completed-turn capture hook. The legacy constructor remains the
// default so existing callers keep the same behavior until composition-root
// wiring opts into session memory.
func NewTurnExecutionServiceWithJobEventsAndCapture(
	dispatcher actortransport.Dispatcher,
	jobEvents jobEventAppender,
	sessions runtimeStateReader,
	logger zerolog.Logger,
	autoMaxTurns int,
	turnCapture CompletedTurnCapture,
) *TurnExecutionService {
	return NewTurnExecutionServiceWithFormats(
		dispatcher,
		jobEvents,
		sessions,
		logger,
		autoMaxTurns,
		turnCapture,
		nil,
	)
}

// NewTurnExecutionServiceWithFormats creates a turn executor with optional
// completed-turn capture and turn-aware message-format prompt composition.
func NewTurnExecutionServiceWithFormats(
	dispatcher actortransport.Dispatcher,
	jobEvents jobEventAppender,
	sessions runtimeStateReader,
	logger zerolog.Logger,
	autoMaxTurns int,
	turnCapture CompletedTurnCapture,
	registry messageFormatRegistry,
) *TurnExecutionService {
	var formatComposer *FormatPromptComposer
	if registry != nil {
		state, _ := sessions.(runtimeStateStore)
		formatComposer = NewFormatPromptComposer(registry, state)
	}
	return &TurnExecutionService{
		dispatcher:     dispatcher,
		jobEvents:      jobEvents,
		sessions:       sessions,
		turnCapture:    turnCapture,
		formatComposer: formatComposer,
		logger:         logger.With().Str("component", "balda.turn_execution").Logger(),
		autoMaxTurns:   automode.NormalizeMaxTurns(autoMaxTurns),
		now:            time.Now,
	}
}

// SetCompletedTurnCapture installs the optional capture hook at the
// composition root without changing the established constructor contract.
func (s *TurnExecutionService) SetCompletedTurnCapture(capture CompletedTurnCapture) {
	if s == nil {
		return
	}
	s.turnCapture = capture
}

func (s *TurnExecutionService) dispatchJobDelivery(
	ctx context.Context,
	jobID string,
	locator baldasession.SessionLocator,
	sessionID string,
	format deliveryfmt.DeliveryFormat,
	text string,
	dedupeSuffix string,
) error {
	if s == nil || s.dispatcher == nil {
		return fmt.Errorf("runtime is unavailable")
	}
	env, err := deliverycmd.AgentReplyEnvelopeWithFormatAndSettlement(jobID, actorlayer.ActorAddress{Target: baldaexecution.ActorTypeSession, Key: sessionID}, locator, format, deliverycmd.SettlementOutbox, text, dedupeSuffix)
	if err != nil {
		return err
	}
	_, err = s.dispatcher.Dispatch(ctx, env)
	return err
}

func (s *TurnExecutionService) appendJobEvent(
	ctx context.Context,
	jobID string,
	eventType string,
	actor string,
	messageID string,
	payload any,
) error {
	if s == nil || s.jobEvents == nil || strings.TrimSpace(jobID) == "" {
		return nil
	}
	return s.jobEvents.AppendEvent(ctx, jobID, eventType, actor, messageID, payload)
}

func (s *TurnExecutionService) Execute(ctx context.Context, req ExecutionRequest) error {
	if req.Runner == nil {
		return fmt.Errorf("session turn: no runner in session %s", req.SessionID)
	}
	if strings.TrimSpace(req.AgentSessionID) == "" {
		req.AgentSessionID = req.SessionID
	}

	topicID := 0
	if address, ok, err := telegramref.DecodeLocator(req.Locator); err == nil && ok {
		topicID = address.TopicID
	}

	req.DeliveryOptions = deliveryfmt.NormalizeOptions(req.DeliveryOptions)
	providerText := req.Text
	var (
		formatState *formatStateChange
		err         error
	)
	if s.formatComposer != nil {
		providerText, formatState, err = s.formatComposer.Compose(
			ctx,
			req.Locator,
			req.DeliveryOptions.DeliveryFormat,
			req.Text,
		)
		if err != nil {
			return fmt.Errorf("compose message format prompt: %w", err)
		}
	}
	userContent, err := buildUserContent(providerText, req.Attachments)
	if err != nil {
		return fmt.Errorf("build user content: %w", err)
	}
	jobBackedDelivery := req.Deliver && strings.TrimSpace(req.JobID) != "" && s.dispatcher != nil
	progressPolicy := req.DeliveryOptions.ProgressPolicy
	deliveryFormat := req.DeliveryOptions.DeliveryFormat

	runCtx := zerolog.Ctx(ctx).With().
		Str("channel_type", req.Locator.ChannelType).
		Str("address_key", req.Locator.AddressKey).
		Int("topic_id", topicID).
		Str("session_id", req.SessionID).
		Str("job_id", strings.TrimSpace(req.JobID)).
		Bool("job_backed_delivery", jobBackedDelivery).
		Str("agent_session_id", req.AgentSessionID).
		Str("transport_user_id", req.UserID).
		Logger().
		WithContext(ctx)
	inputPartCount := 0
	inputTextPartCount := 0
	inputInlineDataPartCount := 0
	inputFileDataPartCount := 0
	inputTextCharCount := 0
	inlineMIMETypes := []string{}
	if userContent != nil {
		inlineMIMETypes = make([]string, 0, len(userContent.Parts))
		inputPartCount = len(userContent.Parts)
		for _, part := range userContent.Parts {
			if part == nil {
				continue
			}
			if part.Text != "" {
				inputTextPartCount++
				inputTextCharCount += len(part.Text)
			}
			if part.InlineData != nil {
				inputInlineDataPartCount++
				if mimeType := strings.TrimSpace(part.InlineData.MIMEType); mimeType != "" {
					inlineMIMETypes = append(inlineMIMETypes, mimeType)
				}
			}
			if part.FileData != nil {
				inputFileDataPartCount++
			}
		}
	}
	zerolog.Ctx(runCtx).Info().
		Int("attachments_count", len(req.Attachments)).
		Int("input_part_count", inputPartCount).
		Int("input_text_part_count", inputTextPartCount).
		Int("input_text_char_count", inputTextCharCount).
		Int("input_inline_data_part_count", inputInlineDataPartCount).
		Int("input_file_data_part_count", inputFileDataPartCount).
		Strs("input_inline_data_mime_types", inlineMIMETypes).
		Msg("assembled provider user content")
	if err := s.startAutoCycleIfNeeded(ctx, req); err != nil {
		return err
	}
	requesterUserID := strings.TrimSpace(req.RequesterUserID)
	if requesterUserID == "" {
		requesterUserID = strings.TrimSpace(req.UserID)
	}
	permissionOutcomes := &permissionOutcomeRecorder{}
	runCtx = permissioncmd.WithOutcomeSink(runCtx, permissionOutcomes)
	runCtx = permissioncmd.WithInteraction(runCtx, questioncmd.InteractionContext{
		SessionID:   req.SessionID,
		ChannelKind: req.Locator.ChannelType,
		Locator:     req.Locator,
		RequestedBy: questioncmd.UserRef{UserID: requesterUserID},
		Origin:      questioncmd.InteractionOrigin{RootJobID: strings.TrimSpace(req.JobID)},
	})

	progressEmitter := req.ProgressEmitter
	if progressEmitter == nil && s.dispatcher != nil {
		progressEmitter = NewSessionProgressDispatcher(
			s.dispatcher,
			req.OutboundFrom,
			req.Locator,
			req.JobID,
			topicID,
			progressPolicy,
			jobBackedDelivery,
			zerolog.Ctx(runCtx).With().Logger(),
		)
	}

	var streamedText strings.Builder
	var memoryStreamedText strings.Builder
	var trustedTools []TrustedToolEvidence
	sawTurnComplete := false
	var terminalFinishReason genai.FinishReason
	terminalErrorCode := ""
	terminalErrorMessage := ""
	lastNonRetryErrorMessage := ""

	for ev, err := range req.Runner.Run(runCtx, req.UserID, req.AgentSessionID, userContent, agent.RunConfig{}, req.RunOptions...) {
		if err != nil {
			return fmt.Errorf("agent run: %w", err)
		}
		if ev == nil {
			continue
		}
		if finishReason := strings.TrimSpace(string(ev.FinishReason)); finishReason != "" {
			terminalFinishReason = ev.FinishReason
		}
		if errorCode := strings.TrimSpace(ev.ErrorCode); errorCode != "" {
			terminalErrorCode = errorCode
		}
		if errorMessage := strings.TrimSpace(ev.ErrorMessage); errorMessage != "" {
			terminalErrorMessage = errorMessage
			if !looksLikeRetryOnlyProviderError(errorMessage) {
				lastNonRetryErrorMessage = errorMessage
			}
		}
		if snapshot, ok := usageview.SnapshotFromMetadata(ev.UsageMetadata); ok {
			if ev.Actions.StateDelta == nil {
				ev.Actions.StateDelta = make(map[string]any)
			}
			ev.Actions.StateDelta[usageview.UsageStateKey] = map[string]any{
				"prompt_token_count":          snapshot.PromptTokenCount,
				"cached_content_token_count":  snapshot.CachedContentTokenCount,
				"response_token_count":        snapshot.ResponseTokenCount,
				"tool_use_prompt_token_count": snapshot.ToolUsePromptTokenCount,
				"thoughts_token_count":        snapshot.ThoughtsTokenCount,
				"total_token_count":           snapshot.TotalTokenCount,
				"traffic_type":                snapshot.TrafficType,
			}
		}
		planProgress, planProgressText, hasPlanUpdate := baldaPlanProgress(ev)
		reasoningText, hasThoughtUpdate := progress.ReasoningText(ev)
		hasVisibleResponseText := false
		if ev.Content != nil {
			for _, part := range ev.Content.Parts {
				if part == nil || part.Thought {
					continue
				}
				if strings.TrimSpace(part.Text) != "" {
					hasVisibleResponseText = true
					break
				}
			}
		}
		if hasThoughtUpdate || (reasoningText != "" && !hasThoughtUpdate) {
			zerolog.Ctx(runCtx).Debug().
				Bool("has_thought_update", hasThoughtUpdate).
				Int("reasoning_text_char_count", len(reasoningText)).
				Bool("has_visible_response_text", hasVisibleResponseText).
				Msg("provider reasoning candidate")
		}
		if !ev.TurnComplete && progressEmitter != nil {
			result, err := progressEmitter.HandleNonTerminal(ctx, SessionProgressUpdate{
				Plan:                   planProgress,
				PlanProgressText:       planProgressText,
				HasPlanUpdate:          hasPlanUpdate,
				ReasoningText:          reasoningText,
				HasThoughtUpdate:       hasThoughtUpdate,
				HasVisibleResponseText: hasVisibleResponseText,
				VisibleResponseText:    visibleResponseDelta(ev),
			})
			if err != nil {
				return err
			}
			if jobBackedDelivery && result.DispatchedPlanText != "" {
				if err := s.appendJobEvent(ctx, req.JobID, baldajobs.JobEventAgentProgress, "session.actor", "", map[string]any{
					"kind": "plan",
					"text": result.DispatchedPlanText,
				}); err != nil {
					return err
				}
			}
		}
		contentRole := ""
		partCount := 0
		thoughtPartCount := 0
		textPartCount := 0
		textCharCount := 0
		functionCallPartCount := 0
		functionResponsePartCount := 0
		executableCodePartCount := 0
		codeExecutionResultPartCount := 0
		fileDataPartCount := 0
		inlineDataPartCount := 0
		var eventTextBuilder strings.Builder
		if ev.Content != nil {
			contentRole = ev.Content.Role
			partCount = len(ev.Content.Parts)
			for _, part := range ev.Content.Parts {
				if part == nil {
					continue
				}
				if part.Thought {
					thoughtPartCount++
					continue
				}
				if part.Text != "" {
					textPartCount++
					textCharCount += len(part.Text)
					eventTextBuilder.WriteString(part.Text)
				}
				if part.FunctionCall != nil {
					functionCallPartCount++
				}
				if part.FunctionResponse != nil {
					functionResponsePartCount++
					if evidence, ok := trustedToolEvidenceFromFunctionResponse(part.FunctionResponse); ok && len(trustedTools) < maxTrustedToolEvidence {
						trustedTools = append(trustedTools, evidence)
					}
					if failure, ok := toolFailureFromFunctionResponse(part.FunctionResponse); ok {
						zerolog.Ctx(runCtx).Warn().
							Str("tool_name", failure.ToolName).
							Str("tool_server", failure.Server).
							Str("tool_status", failure.Status).
							Str("tool_error_code", failure.Code).
							Str("tool_error_message", failure.Message).
							Str("function_name", strings.TrimSpace(part.FunctionResponse.Name)).
							Str("tool_call_id", strings.TrimSpace(part.FunctionResponse.ID)).
							Msg("ADK tool call failed")
					}
				}
				if part.ExecutableCode != nil {
					executableCodePartCount++
				}
				if part.CodeExecutionResult != nil {
					codeExecutionResultPartCount++
				}
				if part.FileData != nil {
					fileDataPartCount++
				}
				if part.InlineData != nil {
					inlineDataPartCount++
				}
			}
		}
		eventText := eventTextBuilder.String()
		if eventText != "" && ev.IsFinalResponse() {
			currentText := streamedText.String()
			if eventText != currentText {
				streamedText.WriteString(eventText)
			}
		}
		memoryEventText := visibleMemoryResponseText(ev)
		if memoryEventText != "" {
			currentText := memoryStreamedText.String()
			if memoryEventText != currentText {
				memoryStreamedText.WriteString(memoryEventText)
			}
		}
		zerolog.Ctx(runCtx).Debug().
			Str("event_id", ev.ID).
			Str("event_invocation_id", ev.InvocationID).
			Str("event_author", ev.Author).
			Str("event_branch", ev.Branch).
			Bool("partial", ev.Partial).
			Bool("interrupted", ev.Interrupted).
			Bool("turn_complete", ev.TurnComplete).
			Bool("has_content", ev.Content != nil).
			Str("content_role", contentRole).
			Int("part_count", partCount).
			Int("thought_part_count", thoughtPartCount).
			Int("text_part_count", textPartCount).
			Int("text_char_count", textCharCount).
			Int("function_call_part_count", functionCallPartCount).
			Int("function_response_part_count", functionResponsePartCount).
			Int("executable_code_part_count", executableCodePartCount).
			Int("code_execution_result_part_count", codeExecutionResultPartCount).
			Int("file_data_part_count", fileDataPartCount).
			Int("inline_data_part_count", inlineDataPartCount).
			Str("error_code", strings.TrimSpace(ev.ErrorCode)).
			Bool("has_error_message", strings.TrimSpace(ev.ErrorMessage) != "").
			Interface("finish_reason", ev.FinishReason).
			Int("custom_metadata_count", len(ev.CustomMetadata)).
			Int("long_running_tool_ids_count", len(ev.LongRunningToolIDs)).
			Int("state_delta_count", len(ev.Actions.StateDelta)).
			Bool("has_plan_update", hasPlanUpdate).
			Int("plan_progress_char_count", len(planProgressText)).
			Int("artifact_delta_count", len(ev.Actions.ArtifactDelta)).
			Int("requested_tool_confirmations_count", len(ev.Actions.RequestedToolConfirmations)).
			Bool("skip_summarization", ev.Actions.SkipSummarization).
			Str("transfer_to_agent", strings.TrimSpace(ev.Actions.TransferToAgent)).
			Bool("escalate", ev.Actions.Escalate).
			Bool("final_response", ev.IsFinalResponse()).
			Bool("has_thought_update", hasThoughtUpdate).
			Int("reasoning_text_char_count", len(reasoningText)).
			Bool("has_visible_response_text", hasVisibleResponseText).
			Int("streamed_text_char_count", streamedText.Len()).
			Msg("received provider event")
		if ev.TurnComplete {
			sawTurnComplete = true
			responseText := streamedText.String()
			memoryResponseText := memoryStreamedText.String()
			if formatState != nil && successfulFormatTurn(ev, responseText) {
				if err := s.formatComposer.Commit(ctx, req.Locator, *formatState); err != nil {
					return err
				}
			}
			if s.turnCapture != nil && shouldCaptureTerminalTurn(ev, req.Text, memoryResponseText) {
				if sourceTurnID := completedTurnSourceID(req); sourceTurnID != "" {
					captureErr := s.turnCapture.CaptureCompletedTurn(ctx, CompletedTurn{
						UserText:       req.Text,
						AssistantText:  memoryResponseText,
						Locator:        req.Locator,
						SessionID:      req.SessionID,
						AgentSessionID: req.AgentSessionID,
						SourceTurnID:   sourceTurnID,
						CompletedAt:    s.currentTime().UTC(),
						TerminalStatus: terminalTurnStatus(ev),
						TrustedTools:   append([]TrustedToolEvidence(nil), trustedTools...),
					})
					if captureErr != nil {
						zerolog.Ctx(runCtx).Warn().
							Err(captureErr).
							Str("source_turn_id", sourceTurnID).
							Msg("failed to publish completed turn to session memory")
					}
				}
			}
			responseEmitted := false
			responseSource := responseSourceNone
			handledEmptyTerminalReason := false
			switch {
			case !req.Deliver:
				responseSource = "fire_and_forget"
			case strings.TrimSpace(responseText) != "":
				autoModeStatus := automode.DefaultStatusWithMaxTurns(s.autoMaxTurns)
				if strings.EqualFold(strings.TrimSpace(req.TurnSource), turncmd.SourceAuto) {
					if loadedStatus, err := s.autoStatus(ctx, req.Locator); err == nil {
						autoModeStatus = loadedStatus
					}
				}
				if notification, source, ok := autoDecisionNotification(autoModeStatus, req.TurnSource, responseText, s.currentTime()); ok {
					responseText = notification
					responseSource = source
					switch source {
					case responseSourceAutoDone:
						if err := s.updateAutoState(ctx, req.Locator, map[string]any{
							automode.StateKeyMode:             automode.StateIdle,
							automode.StateKeyConsecutiveTurns: autoModeStatus.ConsecutiveTurns,
							automode.StateKeyLastTurnAt:       s.currentTime().UTC().Format(time.RFC3339),
							automode.StateKeyLastStopReason:   "model_reported_done",
						}); err != nil {
							return err
						}
					case responseSourceAutoWaitForUser:
						if err := s.updateAutoState(ctx, req.Locator, map[string]any{
							automode.StateKeyMode:             automode.StateWaitingForUser,
							automode.StateKeyConsecutiveTurns: autoModeStatus.ConsecutiveTurns,
							automode.StateKeyLastTurnAt:       s.currentTime().UTC().Format(time.RFC3339),
							automode.StateKeyLastStopReason:   "model_waiting_for_user",
						}); err != nil {
							return err
						}
					}
				}
				if jobBackedDelivery {
					if err := s.dispatchJobDelivery(ctx, req.JobID, req.Locator, req.SessionID, deliveryFormat, responseText, "final"); err != nil {
						return err
					}
					if err := s.appendJobEvent(ctx, req.JobID, baldajobs.JobEventAgentResult, "session.actor", "", map[string]any{
						"text": strings.TrimSpace(responseText),
					}); err != nil {
						return err
					}
					responseEmitted = true
					if responseSource == responseSourceNone {
						responseSource = "streamed_text"
					}
				} else if sendErr := sendAgentReplyWithFormat(ctx, s.dispatcher, req.OutboundFrom, req.Locator, deliveryFormat, responseText); sendErr != nil {
					log.Warn().Err(sendErr).Int("topic_id", topicID).Msg("failed to send balda response")
				} else {
					responseEmitted = true
					if responseSource == responseSourceNone {
						responseSource = "streamed_text"
					}
				}
			default:
				if terminalErrorMessage == "" {
					terminalErrorMessage = lastNonRetryErrorMessage
				}
				terminalMessage := terminalErrorTurnMessage(terminalErrorMessage)
				terminalSource := "provider_error"
				if terminalMessage == "" {
					terminalMessage = permissionOutcomeTurnMessage(permissionOutcomes.Latest())
					terminalSource = "permission_outcome"
				}
				if terminalMessage == "" {
					terminalMessage = terminalTurnMessage(terminalFinishReason)
					terminalSource = "finish_reason"
				}
				if terminalMessage != "" {
					if jobBackedDelivery {
						if err := s.dispatchJobDelivery(ctx, req.JobID, req.Locator, req.SessionID, deliveryFormat, terminalMessage, "terminal"); err != nil {
							return err
						}
						if err := s.appendJobEvent(ctx, req.JobID, baldajobs.JobEventAgentResult, "session.actor", "", map[string]any{
							"text":          strings.TrimSpace(terminalMessage),
							"finish_reason": strings.TrimSpace(string(terminalFinishReason)),
						}); err != nil {
							return err
						}
						responseEmitted = true
						responseSource = terminalSource
						handledEmptyTerminalReason = true
					} else if sendErr := sendPlain(ctx, s.dispatcher, req.OutboundFrom, req.Locator, terminalMessage); sendErr != nil {
						log.Warn().Err(sendErr).Int("topic_id", topicID).Msg("failed to send balda terminal finish reason message")
					} else {
						responseEmitted = true
						responseSource = terminalSource
						handledEmptyTerminalReason = true
					}
				}
			}
			zerolog.Ctx(runCtx).Debug().
				Str("response_source", responseSource).
				Bool("response_emitted_on_turn_complete", responseEmitted).
				Interface("terminal_finish_reason", terminalFinishReason).
				Str("terminal_error_code", terminalErrorCode).
				Bool("terminal_has_error_message", terminalErrorMessage != "").
				Bool("handled_empty_terminal_reason", handledEmptyTerminalReason).
				Msg("processed turn complete event")
			if err := s.maybeScheduleAutoTurn(ctx, req, responseSource, strings.TrimSpace(responseText)); err != nil {
				return err
			}
			break
		}
	}
	if !sawTurnComplete {
		zerolog.Ctx(runCtx).Warn().
			Int("streamed_text_char_count", streamedText.Len()).
			Msg("provider event stream ended without turn complete; suppressing balda response")
	}

	return nil
}

func successfulFormatTurn(event *adksession.Event, responseText string) bool {
	if event == nil || !event.TurnComplete || event.Interrupted {
		return false
	}
	if strings.TrimSpace(event.ErrorCode) != "" || strings.TrimSpace(event.ErrorMessage) != "" {
		return false
	}
	return strings.TrimSpace(responseText) != ""
}

func shouldCaptureTerminalTurn(event *adksession.Event, userText, assistantText string) bool {
	if event == nil || !event.TurnComplete || strings.TrimSpace(userText) == "" {
		return false
	}
	if strings.TrimSpace(assistantText) != "" {
		return true
	}
	return event.Interrupted || strings.TrimSpace(event.ErrorCode) != "" || strings.TrimSpace(event.ErrorMessage) != ""
}

func terminalTurnStatus(event *adksession.Event) TerminalStatus {
	if event != nil && event.Interrupted {
		return TerminalStatusInterrupted
	}
	if event != nil && (strings.TrimSpace(event.ErrorCode) != "" || strings.TrimSpace(event.ErrorMessage) != "") {
		return TerminalStatusFailed
	}
	return TerminalStatusSuccess
}

func (s *TurnExecutionService) startAutoCycleIfNeeded(ctx context.Context, req ExecutionRequest) error {
	if s == nil || strings.EqualFold(strings.TrimSpace(req.TurnSource), turncmd.SourceAuto) {
		return nil
	}
	status, err := s.autoStatus(ctx, req.Locator)
	if err != nil || !status.Enabled || status.State == automode.StateRunning {
		return err
	}
	now := s.currentTime().UTC().Format(time.RFC3339)
	if err := s.updateAutoState(ctx, req.Locator, map[string]any{
		automode.StateKeyMode:           automode.StateRunning,
		automode.StateKeyLastTurnAt:     now,
		automode.StateKeyMaxTurns:       status.MaxTurns,
		automode.StateKeyLastStopReason: "",
	}); err != nil {
		return err
	}
	return s.notifyAutoStateChange(ctx, req, automode.Status{
		Enabled:          status.Enabled,
		State:            automode.StateRunning,
		ConsecutiveTurns: status.ConsecutiveTurns,
		MaxTurns:         status.MaxTurns,
		LastTurnAt:       now,
		LastStopReason:   "",
	})
}

func (s *TurnExecutionService) autoStatus(ctx context.Context, locator baldasession.SessionLocator) (automode.Status, error) {
	defaultMaxTurns := automode.DefaultMaxTurns
	if s != nil {
		defaultMaxTurns = automode.NormalizeMaxTurns(s.autoMaxTurns)
	}
	status := automode.DefaultStatusWithMaxTurns(defaultMaxTurns)
	if s == nil || s.sessions == nil {
		return status, nil
	}
	if value, ok, err := s.sessions.RuntimeStateValue(ctx, locator, automode.StateKeyEnabled); err != nil {
		return status, err
	} else if ok {
		status.Enabled = automode.ParseBool(value)
	}
	if value, ok, err := s.sessions.RuntimeStateValue(ctx, locator, automode.StateKeyMode); err != nil {
		return status, err
	} else if ok {
		if text, ok := value.(string); ok {
			status.State = strings.TrimSpace(text)
		}
	}
	if value, ok, err := s.sessions.RuntimeStateValue(ctx, locator, automode.StateKeyConsecutiveTurns); err != nil {
		return status, err
	} else if ok {
		status.ConsecutiveTurns = automode.ParseInt(value, 0)
	}
	if value, ok, err := s.sessions.RuntimeStateValue(ctx, locator, automode.StateKeyMaxTurns); err != nil {
		return status, err
	} else if ok {
		status.MaxTurns = automode.ParseInt(value, defaultMaxTurns)
	}
	if value, ok, err := s.sessions.RuntimeStateValue(ctx, locator, automode.StateKeyLastTurnAt); err != nil {
		return status, err
	} else if ok {
		if text, ok := value.(string); ok {
			status.LastTurnAt = strings.TrimSpace(text)
		}
	}
	if value, ok, err := s.sessions.RuntimeStateValue(ctx, locator, automode.StateKeyLastStopReason); err != nil {
		return status, err
	} else if ok {
		if text, ok := value.(string); ok {
			status.LastStopReason = strings.TrimSpace(text)
		}
	}
	return automode.NormalizeWithDefault(status, defaultMaxTurns), nil
}

func autoDecisionNotification(status automode.Status, turnSource, responseText string, now time.Time) (string, string, bool) {
	if !strings.EqualFold(strings.TrimSpace(turnSource), turncmd.SourceAuto) {
		return "", "", false
	}
	status = automode.NormalizeWithDefault(status, automode.DefaultMaxTurns)
	lastTurnAt := now.UTC().Format(time.RFC3339)
	switch strings.TrimSpace(responseText) {
	case automode.DoneSentinel:
		return automode.RenderCompactStatusMarkdown(automode.Status{
			Enabled:          true,
			State:            automode.StateIdle,
			ConsecutiveTurns: status.ConsecutiveTurns,
			MaxTurns:         status.MaxTurns,
			LastTurnAt:       lastTurnAt,
			LastStopReason:   "model_reported_done",
		}), responseSourceAutoDone, true
	case automode.WaitSentinel:
		return automode.RenderCompactStatusMarkdown(automode.Status{
			Enabled:          true,
			State:            automode.StateWaitingForUser,
			ConsecutiveTurns: status.ConsecutiveTurns,
			MaxTurns:         status.MaxTurns,
			LastTurnAt:       lastTurnAt,
			LastStopReason:   "model_waiting_for_user",
		}), responseSourceAutoWaitForUser, true
	default:
		return "", "", false
	}
}

func (s *TurnExecutionService) updateAutoState(ctx context.Context, locator baldasession.SessionLocator, state map[string]any) error {
	if s == nil || s.dispatcher == nil || len(state) == 0 {
		return nil
	}
	env, err := automodecmd.Envelope(automodecmd.Payload{
		Locator: locator,
		State:   state,
	})
	if err != nil {
		return err
	}
	_, err = s.dispatcher.Dispatch(ctx, env)
	return err
}

func (s *TurnExecutionService) notifyAutoStateChange(ctx context.Context, req ExecutionRequest, status automode.Status) error {
	status = automode.NormalizeWithDefault(status, s.autoMaxTurns)
	if status.State == automode.StateRunning {
		return nil
	}
	text := automode.RenderCompactStatusMarkdown(status)
	if req.JobID != "" {
		return s.dispatchJobDelivery(ctx, req.JobID, req.Locator, req.SessionID, req.DeliveryOptions.DeliveryFormat, text, "auto-status")
	}
	return sendAgentReplyWithFormat(ctx, s.dispatcher, req.OutboundFrom, req.Locator, req.DeliveryOptions.DeliveryFormat, text)
}

func (s *TurnExecutionService) maybeScheduleAutoTurn(ctx context.Context, req ExecutionRequest, responseSource string, visibleOutput string) error {
	if s == nil || s.dispatcher == nil || s.sessions == nil {
		return nil
	}
	if responseSource == responseSourceAutoDone || responseSource == responseSourceAutoWaitForUser {
		return nil
	}
	status, err := s.autoStatus(ctx, req.Locator)
	if err != nil || !status.Enabled {
		return err
	}
	if strings.EqualFold(strings.TrimSpace(req.TurnSource), turncmd.SourceAuto) {
		if lastOutput, ok, err := s.sessions.RuntimeStateValue(ctx, req.Locator, automode.StateKeyLastOutput); err == nil && ok {
			currentOutput := strings.TrimSpace(visibleOutput)
			if currentOutput == "" {
				currentOutput = strings.TrimSpace(responseSource)
			}
			if lastText, ok := lastOutput.(string); ok && strings.TrimSpace(lastText) != "" && strings.TrimSpace(lastText) == currentOutput {
				if err := s.updateAutoState(ctx, req.Locator, map[string]any{
					automode.StateKeyMode:             automode.StateNoProgress,
					automode.StateKeyConsecutiveTurns: status.ConsecutiveTurns,
					automode.StateKeyLastTurnAt:       s.currentTime().UTC().Format(time.RFC3339),
					automode.StateKeyLastStopReason:   "repeated_visible_output",
				}); err != nil {
					return err
				}
				if status.State != automode.StateNoProgress {
					return s.notifyAutoStateChange(ctx, req, automode.Status{
						Enabled:          status.Enabled,
						State:            automode.StateNoProgress,
						ConsecutiveTurns: status.ConsecutiveTurns,
						MaxTurns:         status.MaxTurns,
						LastTurnAt:       s.currentTime().UTC().Format(time.RFC3339),
						LastStopReason:   "repeated_visible_output",
					})
				}
				return nil
			}
		}
	}
	nextCount := 1
	if strings.EqualFold(strings.TrimSpace(req.TurnSource), turncmd.SourceAuto) {
		nextCount = status.ConsecutiveTurns + 1
	}
	if nextCount > status.MaxTurns {
		if err := s.updateAutoState(ctx, req.Locator, map[string]any{
			automode.StateKeyMode:             automode.StateLimitReached,
			automode.StateKeyConsecutiveTurns: status.MaxTurns,
			automode.StateKeyLastTurnAt:       s.currentTime().UTC().Format(time.RFC3339),
			automode.StateKeyLastStopReason:   "max_auto_turns_reached",
		}); err != nil {
			return err
		}
		if status.State != automode.StateLimitReached {
			return s.notifyAutoStateChange(ctx, req, automode.Status{
				Enabled:          status.Enabled,
				State:            automode.StateLimitReached,
				ConsecutiveTurns: status.MaxTurns,
				MaxTurns:         status.MaxTurns,
				LastTurnAt:       s.currentTime().UTC().Format(time.RFC3339),
				LastStopReason:   "max_auto_turns_reached",
			})
		}
		return nil
	}
	lastOutput := strings.TrimSpace(visibleOutput)
	if lastOutput == "" {
		lastOutput = strings.TrimSpace(responseSource)
	}
	if err := s.updateAutoState(ctx, req.Locator, map[string]any{
		automode.StateKeyMode:             automode.StateRunning,
		automode.StateKeyConsecutiveTurns: nextCount,
		automode.StateKeyLastTurnAt:       s.currentTime().UTC().Format(time.RFC3339),
		automode.StateKeyMaxTurns:         status.MaxTurns,
		automode.StateKeyLastOutput:       lastOutput,
		automode.StateKeyLastStopReason:   "",
	}); err != nil {
		return err
	}
	if status.State != automode.StateRunning {
		if err := s.notifyAutoStateChange(ctx, req, automode.Status{
			Enabled:          status.Enabled,
			State:            automode.StateRunning,
			ConsecutiveTurns: nextCount,
			MaxTurns:         status.MaxTurns,
			LastTurnAt:       s.currentTime().UTC().Format(time.RFC3339),
			LastStopReason:   "",
		}); err != nil {
			return err
		}
	}
	env, err := turncmd.SessionTurnEnvelope(turncmd.SessionTurnPayload{
		Text:            automode.InternalPrompt(status.MaxTurns),
		Locator:         req.Locator,
		UserID:          req.UserID,
		RequesterUserID: req.RequesterUserID,
		AgentSessionID:  req.AgentSessionID,
		Deliver:         true,
		Source:          turncmd.SourceAuto,
		DedupeKey:       autoTurnDedupeKey(req.Locator.SessionID, req.DedupeKey, nextCount),
		DeliveryFormat:  req.DeliveryOptions.DeliveryFormat,
		ProgressPolicy:  req.DeliveryOptions.ProgressPolicy,
	})
	if err != nil {
		return err
	}
	_, err = s.dispatcher.Dispatch(ctx, env)
	return err
}

func autoTurnDedupeKey(sessionID string, parentDedupeKey string, turn int) string {
	parentSum := sha256.Sum256([]byte(strings.TrimSpace(parentDedupeKey)))
	return fmt.Sprintf("auto:%s:%d:%x", strings.TrimSpace(sessionID), turn, parentSum[:16])
}

func completedTurnSourceID(req ExecutionRequest) string {
	if dedupeKey := strings.TrimSpace(req.DedupeKey); dedupeKey != "" {
		return dedupeKey
	}
	if jobID := strings.TrimSpace(req.JobID); jobID != "" {
		return "job:" + jobID
	}
	if req.MessageID > 0 {
		sessionID := strings.TrimSpace(req.SessionID)
		if sessionID == "" {
			sessionID = strings.TrimSpace(req.Locator.SessionID)
		}
		if sessionID != "" {
			return fmt.Sprintf("message:%s:%d", sessionID, req.MessageID)
		}
	}
	return ""
}

func visibleResponseDelta(ev *adksession.Event) string {
	if ev == nil || !ev.Partial || ev.Content == nil {
		return ""
	}
	var b strings.Builder
	for _, part := range ev.Content.Parts {
		if part == nil || part.Thought {
			continue
		}
		if strings.TrimSpace(part.Text) != "" {
			b.WriteString(part.Text)
		}
	}
	return strings.TrimSpace(b.String())
}

func visibleMemoryResponseText(ev *adksession.Event) string {
	if ev == nil || ev.Partial || !ev.IsFinalResponse() || ev.Content == nil {
		return ""
	}
	var b strings.Builder
	for _, part := range ev.Content.Parts {
		if part == nil || part.Thought || strings.TrimSpace(part.Text) == "" {
			continue
		}
		if part.FunctionCall != nil || part.FunctionResponse != nil ||
			part.ExecutableCode != nil || part.CodeExecutionResult != nil ||
			part.FileData != nil || part.InlineData != nil {
			continue
		}
		b.WriteString(part.Text)
	}
	return b.String()
}

func looksLikeRetryOnlyProviderError(message string) bool {
	trimmed := strings.ToLower(strings.TrimSpace(message))
	if trimmed == "" {
		return false
	}
	return strings.Contains(trimmed, "reconnecting") || strings.Contains(trimmed, "will retry")
}
