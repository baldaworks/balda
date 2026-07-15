// Package permissions implements Balda's generic agent permission review policy.
package permissions

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/google/uuid"
	"github.com/normahq/balda/internal/apps/balda/actorcmd"
	"github.com/normahq/balda/internal/apps/balda/deliverycmd"
	"github.com/normahq/balda/internal/apps/balda/permissioncmd"
	"github.com/normahq/balda/internal/apps/balda/questioncmd"
	"github.com/normahq/balda/internal/apps/balda/questions"
	"github.com/normahq/balda/internal/apps/balda/redaction"
	"github.com/rs/zerolog"
)

const (
	defaultTimeout = 2 * time.Minute
	maxInputLength = 4096
	maxLocations   = 10
)

type Config struct {
	Mode    permissioncmd.Mode
	Timeout time.Duration
}

func ParseConfig(mode, timeout string) (Config, error) {
	parsedMode := permissioncmd.Mode(strings.ToLower(strings.TrimSpace(mode)))
	if parsedMode == "" {
		parsedMode = permissioncmd.ModeAllowAll
	}
	switch parsedMode {
	case permissioncmd.ModeAllowAll, permissioncmd.ModeAsk, permissioncmd.ModeDenyAll:
	default:
		return Config{}, fmt.Errorf("permissions mode %q must be allow_all, ask, or deny_all", mode)
	}
	parsedTimeout := defaultTimeout
	if strings.TrimSpace(timeout) != "" {
		var err error
		parsedTimeout, err = time.ParseDuration(strings.TrimSpace(timeout))
		if err != nil {
			return Config{}, fmt.Errorf("parse permissions timeout: %w", err)
		}
		if parsedTimeout <= 0 {
			return Config{}, fmt.Errorf("permissions timeout must be positive")
		}
	}
	return Config{Mode: parsedMode, Timeout: parsedTimeout}, nil
}

type Service struct {
	config     Config
	questions  *questions.Service
	dispatcher actortransport.Dispatcher
	logger     zerolog.Logger
	now        func() time.Time

	waitMu  sync.Mutex
	waiters map[string]chan permissioncmd.Decision
}

func New(config Config, questionService *questions.Service, dispatcher actortransport.Dispatcher, logger zerolog.Logger) *Service {
	serviceLogger := logger.With().Str("component", "balda.permissions").Logger()
	serviceLogger.Info().
		Str("mode", string(config.Mode)).
		Str("timeout", config.Timeout.String()).
		Bool("questions_available", questionService != nil).
		Bool("dispatcher_available", dispatcher != nil).
		Msg("agent permission policy configured")
	return &Service{
		config:     config,
		questions:  questionService,
		dispatcher: dispatcher,
		logger:     serviceLogger,
		now:        time.Now,
		waiters:    make(map[string]chan permissioncmd.Decision),
	}
}

func (s *Service) Review(ctx context.Context, request permissioncmd.Request) (permissioncmd.Decision, error) {
	switch s.config.Mode {
	case permissioncmd.ModeAllowAll:
		return selectDecision(request.Options, true, "allow_all"), nil
	case permissioncmd.ModeDenyAll:
		return selectDecision(request.Options, false, "deny_all"), nil
	case permissioncmd.ModeAsk:
		return s.ask(ctx, request)
	default:
		return selectDecision(request.Options, false, "invalid_mode"), fmt.Errorf("unsupported permission mode %q", s.config.Mode)
	}
}

func (s *Service) ask(ctx context.Context, request permissioncmd.Request) (permissioncmd.Decision, error) {
	fallback := selectDecision(request.Options, false, "fail_closed")
	if s.questions == nil || s.dispatcher == nil {
		return fallback, fmt.Errorf("interactive permission review is unavailable")
	}
	interaction := request.Interaction
	channel := strings.ToLower(strings.TrimSpace(interaction.Locator.ChannelType))
	if channel != "telegram" && channel != "slack_agent" {
		return fallback, fmt.Errorf("interactive permission review is unsupported for channel %q", channel)
	}
	if strings.TrimSpace(interaction.SessionID) == "" || strings.TrimSpace(interaction.RequestedBy.UserID) == "" {
		return fallback, fmt.Errorf("interactive permission review requires session and requester identity")
	}
	if len(request.Options) == 0 {
		return fallback, fmt.Errorf("permission request has no options")
	}

	reviewID := "permission-" + uuid.NewString()
	waiter := make(chan permissioncmd.Decision, 1)
	s.waitMu.Lock()
	s.waiters[reviewID] = waiter
	s.waitMu.Unlock()
	defer func() {
		s.waitMu.Lock()
		delete(s.waiters, reviewID)
		s.waitMu.Unlock()
	}()

	options := make([]questioncmd.Option, 0, len(request.Options))
	for _, option := range request.Options {
		options = append(options, questioncmd.Option{ID: strings.TrimSpace(option.ID), Label: strings.TrimSpace(option.Name)})
	}
	record, err := s.questions.Ask(ctx, interaction, questioncmd.ResumeTarget{
		To:        actorcmd.ActorTypePermission + ":" + reviewID,
		Namespace: actorcmd.NamespacePermissionCommand,
	}, questioncmd.Request{
		Prompt:        renderPrompt(request),
		Options:       options,
		AllowFreeText: false,
		Responder:     questioncmd.ResponderRequester,
		Timeout:       s.config.Timeout,
	})
	if err != nil {
		return fallback, fmt.Errorf("create permission question: %w", err)
	}
	envelope, err := deliverycmd.AgentReplyEnvelopeWithProfileAndSettlementAndRefs(
		"",
		actorlayer.ActorAddress{Target: actorcmd.ActorTypePermission, Key: reviewID},
		interaction.Locator,
		deliverycmd.Profile{Format: deliverycmd.FormatPlain},
		deliverycmd.SettlementOutbox,
		record.Prompt,
		"permission:"+reviewID,
		map[string]string{"question_id": record.QuestionID},
	)
	if err != nil {
		_, _, _ = s.questions.Timeout(context.WithoutCancel(ctx), record.QuestionID, s.now().UTC())
		return fallback, fmt.Errorf("build permission delivery: %w", err)
	}
	if _, err := s.dispatcher.Dispatch(ctx, envelope); err != nil {
		_, _, _ = s.questions.Timeout(context.WithoutCancel(ctx), record.QuestionID, s.now().UTC())
		return fallback, fmt.Errorf("dispatch permission delivery: %w", err)
	}

	timer := time.NewTimer(s.config.Timeout)
	defer timer.Stop()
	select {
	case decision := <-waiter:
		if decision.Canceled {
			return fallback, nil
		}
		if hasOption(request.Options, decision.OptionID) {
			return decision, nil
		}
		return fallback, fmt.Errorf("permission response selected unknown option %q", decision.OptionID)
	case <-timer.C:
		return s.timeoutDecision(record.QuestionID, request.Options, fallback, "timeout")
	case <-ctx.Done():
		decision, timeoutErr := s.timeoutDecision(record.QuestionID, request.Options, fallback, "canceled")
		if timeoutErr != nil {
			return decision, fmt.Errorf("permission request canceled: %w", timeoutErr)
		}
		return decision, ctx.Err()
	}
}

func (s *Service) timeoutDecision(questionID string, options []permissioncmd.Option, fallback permissioncmd.Decision, source string) (permissioncmd.Decision, error) {
	record, settled, err := s.questions.Timeout(context.Background(), questionID, s.now().UTC())
	if err != nil {
		return fallback, err
	}
	if !settled && strings.TrimSpace(record.AnswerJSON) != "" {
		var answer questioncmd.Answer
		if json.Unmarshal([]byte(record.AnswerJSON), &answer) == nil && hasOption(options, answer.SelectedOption) {
			return permissioncmd.Decision{OptionID: answer.SelectedOption, Source: "user"}, nil
		}
	}
	fallback.Source = source
	return fallback, nil
}

func (s *Service) Resolve(reviewID, optionID string) {
	s.waitMu.Lock()
	waiter := s.waiters[strings.TrimSpace(reviewID)]
	s.waitMu.Unlock()
	if waiter == nil {
		return
	}
	trimmedOptionID := strings.TrimSpace(optionID)
	select {
	case waiter <- permissioncmd.Decision{OptionID: trimmedOptionID, Source: "user", Canceled: trimmedOptionID == ""}:
	default:
	}
}

func selectDecision(options []permissioncmd.Option, allow bool, source string) permissioncmd.Decision {
	preferred := []string{"reject_once", "reject_always"}
	if allow {
		preferred = []string{"allow_once", "allow_always"}
	}
	for _, kind := range preferred {
		for _, option := range options {
			if strings.EqualFold(strings.TrimSpace(option.Kind), kind) && strings.TrimSpace(option.ID) != "" {
				return permissioncmd.Decision{OptionID: strings.TrimSpace(option.ID), Source: source}
			}
		}
	}
	return permissioncmd.Decision{Canceled: true, Source: source}
}

func hasOption(options []permissioncmd.Option, optionID string) bool {
	for _, option := range options {
		if strings.TrimSpace(option.ID) == strings.TrimSpace(optionID) && strings.TrimSpace(optionID) != "" {
			return true
		}
	}
	return false
}

func renderPrompt(request permissioncmd.Request) string {
	var out strings.Builder
	out.WriteString("Permission required")
	if title := strings.TrimSpace(request.ToolCall.Title); title != "" {
		out.WriteString("\nTool: ")
		out.WriteString(title)
	}
	if kind := strings.TrimSpace(request.ToolCall.Kind); kind != "" {
		out.WriteString("\nKind: ")
		out.WriteString(kind)
	}
	for index, location := range request.ToolCall.Locations {
		if index >= maxLocations {
			break
		}
		out.WriteString("\nLocation: ")
		out.WriteString(strings.TrimSpace(location.Path))
		if location.Line != nil {
			out.WriteString(":")
			out.WriteString(strconv.Itoa(*location.Line))
		}
	}
	if input := truncate(redaction.Secrets(request.ToolCall.RawInput), maxInputLength); input != "" {
		out.WriteString("\nInput: ")
		out.WriteString(input)
	}
	out.WriteString("\n\nReply with the option number, ID, or name:")
	for index, option := range request.Options {
		fmt.Fprintf(&out, "\n%d. %s [%s]", index+1, strings.TrimSpace(option.Name), strings.TrimSpace(option.ID))
	}
	return out.String()
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "…"
}
