package sessionturnapp

import (
	"context"
	"fmt"
	"strings"
	"time"

	baldaexecution "github.com/normahq/balda/internal/apps/balda/actorcmd"
	"github.com/normahq/balda/internal/apps/balda/deliverycmd"
	"github.com/normahq/balda/internal/apps/balda/deliveryfmt"
	baldajobs "github.com/normahq/balda/internal/apps/balda/jobs"
	"github.com/normahq/balda/internal/apps/balda/progress"
	baldasession "github.com/normahq/balda/internal/apps/balda/session"
	"github.com/normahq/balda/internal/apps/balda/telegramref"
	"github.com/normahq/balda/internal/apps/balda/usageview"
	"github.com/normahq/balda/pkg/actorlayer"
	actortransport "github.com/normahq/balda/pkg/actorlayer/transport"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/genai"
)

type jobEventAppender interface {
	AppendEvent(ctx context.Context, jobID string, eventType string, actor string, messageID string, payload any) error
}

type TurnExecutionService struct {
	dispatcher actortransport.Dispatcher
	jobEvents  jobEventAppender
	logger     zerolog.Logger
	now        func() time.Time
}

type ExecutionRequest struct {
	Text            string
	Runner          *runner.Runner
	UserID          string
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
}

func NewTurnExecutionService(dispatcher actortransport.Dispatcher, jobEvents *baldajobs.JobEventsService, logger zerolog.Logger) *TurnExecutionService {
	return NewTurnExecutionServiceWithJobEvents(dispatcher, jobEvents, logger)
}

func NewTurnExecutionServiceWithJobEvents(dispatcher actortransport.Dispatcher, jobEvents jobEventAppender, logger zerolog.Logger) *TurnExecutionService {
	return &TurnExecutionService{
		dispatcher: dispatcher,
		jobEvents:  jobEvents,
		logger:     logger.With().Str("component", "balda.turn_execution").Logger(),
		now:        time.Now,
	}
}

func (s *TurnExecutionService) dispatchJobDelivery(
	ctx context.Context,
	jobID string,
	locator baldasession.SessionLocator,
	sessionID string,
	profile deliveryfmt.Profile,
	text string,
	dedupeSuffix string,
) error {
	if s == nil || s.dispatcher == nil {
		return fmt.Errorf("runtime is unavailable")
	}
	env, err := deliverycmd.AgentReplyEnvelopeWithProfileAndSettlement(jobID, actorlayer.ActorAddress{Target: baldaexecution.ActorTypeSession, Key: sessionID}, locator, deliverycmd.Profile{
		Format:         deliverycmd.Format(profile.Format),
		TelegramMode:   profile.TelegramMode,
		FormattingMode: profile.FormattingMode,
	}, deliverycmd.SettlementOutbox, text, dedupeSuffix)
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

	userContent := genai.NewContentFromText(req.Text, genai.RoleUser)
	jobBackedDelivery := req.Deliver && strings.TrimSpace(req.JobID) != "" && s.dispatcher != nil
	req.DeliveryOptions = deliveryfmt.NormalizeOptions(req.DeliveryOptions)
	progressPolicy := req.DeliveryOptions.ProgressPolicy
	deliveryProfile := req.DeliveryOptions.Profile

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
	sawTurnComplete := false
	var terminalFinishReason genai.FinishReason
	terminalErrorCode := ""
	terminalErrorMessage := ""

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
			responseEmitted := false
			responseSource := "none"
			handledEmptyTerminalReason := false
			switch {
			case !req.Deliver:
				responseSource = "fire_and_forget"
			case strings.TrimSpace(responseText) != "":
				if jobBackedDelivery {
					if err := s.dispatchJobDelivery(ctx, req.JobID, req.Locator, req.SessionID, deliveryProfile, responseText, "final"); err != nil {
						return err
					}
					if err := s.appendJobEvent(ctx, req.JobID, baldajobs.JobEventAgentResult, "session.actor", "", map[string]any{
						"text": strings.TrimSpace(responseText),
					}); err != nil {
						return err
					}
					responseEmitted = true
					responseSource = "streamed_text"
				} else if sendErr := sendAgentReplyWithProfile(ctx, s.dispatcher, req.OutboundFrom, req.Locator, deliveryProfile, responseText); sendErr != nil {
					log.Warn().Err(sendErr).Int("topic_id", topicID).Msg("failed to send balda response")
				} else {
					responseEmitted = true
					responseSource = "streamed_text"
				}
			default:
				terminalMessage := terminalErrorTurnMessage(terminalErrorMessage)
				if terminalMessage == "" {
					terminalMessage = terminalTurnMessage(terminalFinishReason)
				}
				if terminalMessage != "" {
					if jobBackedDelivery {
						if err := s.dispatchJobDelivery(ctx, req.JobID, req.Locator, req.SessionID, deliveryProfile, terminalMessage, "terminal"); err != nil {
							return err
						}
						if err := s.appendJobEvent(ctx, req.JobID, baldajobs.JobEventAgentResult, "session.actor", "", map[string]any{
							"text":          strings.TrimSpace(terminalMessage),
							"finish_reason": strings.TrimSpace(string(terminalFinishReason)),
						}); err != nil {
							return err
						}
						responseEmitted = true
						responseSource = "finish_reason"
						handledEmptyTerminalReason = true
					} else if sendErr := sendPlain(ctx, s.dispatcher, req.OutboundFrom, req.Locator, terminalMessage); sendErr != nil {
						log.Warn().Err(sendErr).Int("topic_id", topicID).Msg("failed to send balda terminal finish reason message")
					} else {
						responseEmitted = true
						responseSource = "finish_reason"
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
