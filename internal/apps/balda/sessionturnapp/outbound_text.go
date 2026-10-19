package sessionturnapp

import (
	"context"
	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	baldasession "github.com/baldaworks/balda/internal/apps/balda/session"
	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/normahq/go-adk-acpagent/v2/acperror"
	"google.golang.org/genai"
)

const (
	genericProviderFailureMessage = "The provider could not complete this turn. Please try again."
	providerOverloadedCode        = "serverOverloaded"
)

func sendPlain(ctx context.Context, dispatcher actortransport.Dispatcher, from actorlayer.ActorAddress, locator baldasession.SessionLocator, text string) error {
	env, err := deliverycmd.PlainEnvelopeWithSettlement("", from, locator, deliverycmd.SettlementBypass, text, "")
	if err != nil {
		return err
	}
	return dispatchOutbound(ctx, dispatcher, env)
}

func sendAgentReplyWithFormat(ctx context.Context, dispatcher actortransport.Dispatcher, from actorlayer.ActorAddress, locator baldasession.SessionLocator, format deliveryfmt.DeliveryFormat, text string) error {
	env, err := deliverycmd.AgentReplyEnvelopeWithFormatAndSettlement("", from, locator, format, deliverycmd.SettlementBypass, text, "")
	if err != nil {
		return err
	}
	return dispatchOutbound(ctx, dispatcher, env)
}

func terminalErrorTurnMessage(code, message string, providerErr *acperror.ProviderError, now time.Time) string {
	original := message
	if providerErr != nil {
		// Metadata-only events print a synthesized error object in ErrorMessage.
		// Its Message field is the actual provider-authored text.
		if strings.HasPrefix(code, "acp_provider_error:") || strings.TrimSpace(original) == "" {
			original = providerErr.Message
		}
	}
	var parts []string
	if strings.TrimSpace(original) != "" {
		parts = append(parts, "Provider error:\n"+original)
	}
	if code == providerOverloadedCode || (providerErr != nil && providerErr.Code == providerOverloadedCode) {
		parts = append(parts, "The selected model is overloaded. Try again later or choose another model.")
	} else if len(parts) == 0 {
		if category := providerErrorTurnMessage(providerErr, now); category != "" {
			parts = append(parts, category)
		} else if code != "" || strings.TrimSpace(message) != "" {
			parts = append(parts, genericProviderFailureMessage)
		}
	}
	return strings.Join(parts, "\n\n")
}

func providerErrorTurnMessage(providerErr *acperror.ProviderError, now time.Time) string {
	if providerErr == nil {
		return ""
	}
	retryHint := providerRetryHint(providerErr.RetryAt, now)
	switch providerErr.Kind {
	case acperror.KindQuotaExceeded:
		return "The provider quota is exhausted." + retryHint
	case acperror.KindRateLimited:
		return "The provider rate limit was reached." + retryHint
	case acperror.KindAuthenticationRequired:
		return "The provider needs authentication. Ask the operator to update its credentials."
	case acperror.KindPaymentRequired:
		return "The provider billing limit was reached. Ask the operator to check the account."
	case acperror.KindUnavailable:
		return "The provider is temporarily unavailable. Please try again."
	case acperror.KindSessionExhausted:
		return "This provider session reached its limit. Start a new session and try again."
	case acperror.KindInvalidRequest:
		switch strings.TrimSpace(providerErr.Code) {
		case "contextWindowExceeded", "input_too_large":
			return "The request is too large for the provider. Shorten it or start a new session."
		case "sandboxError":
			return "The provider could not execute this request in its sandbox. Adjust it and try again."
		default:
			return "The provider rejected this request. Adjust it and try again."
		}
	default:
		return genericProviderFailureMessage
	}
}

func providerRetryHint(retryAt *int64, now time.Time) string {
	if retryAt == nil {
		return " Please try again later."
	}
	reset := time.Unix(*retryAt, 0).UTC()
	if !reset.After(now.UTC()) {
		return " Please try again later."
	}
	return " Try again after " + reset.Format("2006-01-02 15:04 UTC") + "."
}

func interruptedProviderTurnMessage(providerMessage string) string {
	if strings.TrimSpace(providerMessage) == "" {
		providerMessage = genericProviderFailureMessage
	}
	return "Response interrupted. " + providerMessage
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
