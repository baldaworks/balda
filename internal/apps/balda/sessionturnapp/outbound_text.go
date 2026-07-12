package sessionturnapp

import (
	"context"
	"strings"

	"github.com/normahq/balda/internal/apps/balda/deliverycmd"
	"github.com/normahq/balda/internal/apps/balda/deliveryfmt"
	baldasession "github.com/normahq/balda/internal/apps/balda/session"
	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"google.golang.org/genai"
)

func sendPlain(ctx context.Context, dispatcher actortransport.Dispatcher, from actorlayer.ActorAddress, locator baldasession.SessionLocator, text string) error {
	env, err := deliverycmd.PlainEnvelopeWithSettlement("", from, locator, deliverycmd.SettlementBypass, text, "")
	if err != nil {
		return err
	}
	return dispatchOutbound(ctx, dispatcher, env)
}

func sendAgentReplyWithProfile(ctx context.Context, dispatcher actortransport.Dispatcher, from actorlayer.ActorAddress, locator baldasession.SessionLocator, profile deliveryfmt.Profile, text string) error {
	env, err := deliverycmd.AgentReplyEnvelopeWithProfileAndSettlement("", from, locator, deliverycmd.Profile{
		Format:         deliverycmd.Format(profile.Format),
		TelegramMode:   profile.TelegramMode,
		FormattingMode: profile.FormattingMode,
	}, deliverycmd.SettlementBypass, text, "")
	if err != nil {
		return err
	}
	return dispatchOutbound(ctx, dispatcher, env)
}

func terminalErrorTurnMessage(errorMessage string) string {
	errorMessage = strings.TrimSpace(errorMessage)
	if errorMessage == "" {
		return ""
	}
	return "Provider error: " + errorMessage
}

func terminalTurnMessage(reason genai.FinishReason) string {
	switch reason {
	case genai.FinishReasonMaxTokens:
		return "The provider hit the output limit before producing a visible reply. Ask for a shorter answer or split the request."
	case genai.FinishReasonSafety:
		return "The provider blocked this turn for safety reasons. Please rephrase and try again."
	case genai.FinishReasonRecitation:
		return "The provider blocked this turn because it may reproduce protected source material. Please rephrase and try again."
	case genai.FinishReasonLanguage:
		return "The provider could not answer because the request used an unsupported language. Please rephrase in a supported language and try again."
	case genai.FinishReasonBlocklist:
		return "The provider blocked this turn because it matched restricted terms. Please rephrase and try again."
	case genai.FinishReasonProhibitedContent:
		return "The provider rejected this turn as prohibited content. Please rephrase and try again."
	case genai.FinishReasonSPII:
		return "The provider blocked this turn because it may contain sensitive personal information. Please remove that information and try again."
	case genai.FinishReasonMalformedFunctionCall:
		return "The provider ended the turn with an invalid function call. Please try again."
	case genai.FinishReasonUnexpectedToolCall:
		return "The provider ended the turn with an unexpected tool call. Please try again."
	case genai.FinishReasonImageSafety:
		return "The provider blocked image generation for safety reasons. Please try a different request."
	case genai.FinishReasonImageProhibitedContent:
		return "The provider rejected image generation as prohibited content. Please try a different request."
	case genai.FinishReasonNoImage:
		return "The provider completed the turn without returning an image. Please try a different request."
	case genai.FinishReasonImageRecitation:
		return "The provider blocked image generation because it may reproduce protected source material. Please try a different request."
	case genai.FinishReasonImageOther:
		return "The provider ended image generation without a usable result. Please try again."
	case genai.FinishReasonStop, genai.FinishReasonOther, genai.FinishReasonUnspecified:
		return "The provider ended the turn without a usable reply. Please try again."
	default:
		return "The provider ended the turn without a usable reply. Please try again."
	}
}
