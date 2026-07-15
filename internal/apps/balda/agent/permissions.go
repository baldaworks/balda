package agent

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/normahq/balda/internal/apps/balda/permissioncmd"
	"github.com/normahq/balda/internal/apps/balda/redaction"
	acpagent "github.com/normahq/go-adk-acpagent/v2"
	"github.com/rs/zerolog"
)

type PermissionReviewer interface {
	Review(ctx context.Context, request permissioncmd.Request) (permissioncmd.Decision, error)
}

// NewPermissionHandler adapts the ADK-facing permission contract to Balda's
// transport-neutral permission policy.
func NewPermissionHandler(reviewer PermissionReviewer, logger zerolog.Logger) acpagent.PermissionHandler {
	permissionLogger := logger.With().Str("component", "balda.agent.permissions").Logger()
	return func(ctx context.Context, request acpagent.PermissionRequest) (acpagent.PermissionDecision, error) {
		translated := translatePermissionRequest(ctx, request)
		permissionLogger.Debug().
			Str("tool_call_id", translated.ToolCall.ID).
			Str("session_id", translated.Interaction.SessionID).
			Str("channel_type", translated.Interaction.Locator.ChannelType).
			Str("requester_user_id", translated.Interaction.RequestedBy.UserID).
			Int("option_count", len(translated.Options)).
			Msg("reviewing agent permission request")
		decision := denyDecision(translated.Options)
		if reviewer == nil {
			permissionLogger.Warn().Msg("permission reviewer unavailable; denying request")
		} else {
			reviewed, err := reviewer.Review(ctx, translated)
			decision = reviewed
			if err != nil {
				permissionLogger.Warn().Err(err).
					Str("tool_call_id", translated.ToolCall.ID).
					Msg("permission review failed closed")
			}
		}
		permissionLogger.Debug().
			Str("tool_call_id", translated.ToolCall.ID).
			Str("option_id", decision.OptionID).
			Str("source", decision.Source).
			Bool("canceled", decision.Canceled).
			Msg("agent permission review completed")
		if decision.OptionID != "" && hasPermissionOption(request.Options, decision.OptionID) {
			return acpagent.PermissionDecision{OptionID: decision.OptionID}, nil
		}
		return acpagent.PermissionDecision{Canceled: true}, nil
	}
}

func translatePermissionRequest(ctx context.Context, request acpagent.PermissionRequest) permissioncmd.Request {
	interaction, _ := permissioncmd.InteractionFromContext(ctx)
	options := make([]permissioncmd.Option, 0, len(request.Options))
	for _, option := range request.Options {
		options = append(options, permissioncmd.Option{
			ID:   strings.TrimSpace(option.ID),
			Name: strings.TrimSpace(option.Name),
			Kind: string(option.Kind),
		})
	}
	locations := make([]permissioncmd.Location, 0, len(request.ToolCall.Locations))
	for _, location := range request.ToolCall.Locations {
		locations = append(locations, permissioncmd.Location{Path: strings.TrimSpace(location.Path), Line: location.Line})
	}
	content := make([]permissioncmd.Content, 0, len(request.ToolCall.Content))
	for _, item := range request.ToolCall.Content {
		content = append(content, permissioncmd.Content{
			Kind:       permissioncmd.ContentKind(item.Kind),
			Text:       strings.TrimSpace(redaction.Secrets(item.Text)),
			Path:       strings.TrimSpace(redaction.Secrets(item.Path)),
			OldText:    redactedStringPointer(item.OldText),
			NewText:    redaction.Secrets(item.NewText),
			TerminalID: strings.TrimSpace(item.TerminalID),
		})
	}
	rawInput := ""
	if request.ToolCall.RawInput != nil {
		if data, err := json.Marshal(request.ToolCall.RawInput); err == nil {
			rawInput = string(data)
		}
	}
	return permissioncmd.Request{
		Interaction: interaction,
		ToolCall: permissioncmd.ToolCall{
			ID:        strings.TrimSpace(request.ToolCall.ID),
			Title:     strings.TrimSpace(request.ToolCall.Title),
			Kind:      strings.TrimSpace(request.ToolCall.Kind),
			RawInput:  rawInput,
			Locations: locations,
			Content:   content,
		},
		Options: options,
	}
}

func redactedStringPointer(value *string) *string {
	if value == nil {
		return nil
	}
	redacted := redaction.Secrets(*value)
	return &redacted
}

func denyDecision(options []permissioncmd.Option) permissioncmd.Decision {
	for _, kind := range []string{"reject_once", "reject_always"} {
		for _, option := range options {
			if strings.EqualFold(strings.TrimSpace(option.Kind), kind) {
				return permissioncmd.Decision{OptionID: option.ID, Source: "fail_closed"}
			}
		}
	}
	return permissioncmd.Decision{Canceled: true, Source: "fail_closed"}
}

func hasPermissionOption(options []acpagent.PermissionOption, optionID string) bool {
	for _, option := range options {
		if strings.TrimSpace(option.ID) == strings.TrimSpace(optionID) {
			return true
		}
	}
	return false
}
