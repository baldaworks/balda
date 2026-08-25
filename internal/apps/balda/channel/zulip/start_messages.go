package zulip

import (
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/auth"
)

type StartArgs struct {
	Mode  string
	Token string
}

func ParseStartArgs(args string) (StartArgs, bool) {
	trimmed := strings.TrimSpace(args)
	if trimmed == "" {
		return StartArgs{}, true
	}
	if token, ok := parseSingleToken(trimmed); ok {
		return StartArgs{Mode: "channel_token", Token: token}, true
	}
	key, value, ok := strings.Cut(trimmed, "=")
	if !ok || strings.TrimSpace(value) == "" {
		return StartArgs{}, false
	}
	mode := strings.TrimSpace(key)
	token := strings.TrimSpace(value)
	switch mode {
	case "owner", "invite":
		return StartArgs{Mode: mode, Token: token}, true
	default:
		return StartArgs{}, false
	}
}

func parseSingleToken(text string) (string, bool) {
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) != 1 {
		return "", false
	}
	token := strings.TrimSpace(fields[0])
	return token, auth.LooksLikeChannelToken(token)
}

func StartWelcomeMessage() string {
	return "Welcome to Balda Bot!\n\nTo authenticate:\n" +
		"• /start owner=<your_owner_token>\n" +
		"• /start invite=<your_invite_token>"
}

func StartInvalidFormatMessage() string {
	return "Invalid /start format. Use one of:\n" +
		"• /start owner=<your_owner_token>\n" +
		"• /start invite=<your_invite_token>"
}

func StartDirectMessageOnly() string {
	return zulipDirectMessageOnlyText
}

func StartOwnerAlreadyRegistered() string {
	return "Bot owner is already registered."
}

func StartOwnerRegistered() string {
	return "You are now registered as the bot owner."
}

func StartOwnerBindUnavailable() string {
	return "Token authentication is unavailable right now."
}

func StartOwnerBindFailed() string {
	return "Failed to process token. Please try again."
}

func StartOwnerBindInvalid() string {
	return "This token is invalid or has expired."
}

func StartOwnerBound() string {
	return "This Zulip account is now connected to the Balda owner."
}

func StartInviteProcessingFailed() string {
	return "Failed to process invite. Ask the operator to check Balda storage configuration."
}

func StartOwnerStoreUnavailable() string {
	return "Could not register owner. Ask the operator to check Balda storage configuration."
}

func StartInvalidAuthToken() string {
	return "Invalid authentication token. Please try again."
}

func StartOwnerAlreadyRegisteredSelfMessage(bundle string) string {
	if strings.TrimSpace(bundle) == "" {
		return "You are already registered as the bot owner."
	}
	return "You are already registered as the bot owner.\n\n" + strings.TrimSpace(bundle)
}

func AccessDeniedMessage() string {
	return "Only the bot owner or collaborators can use this bot."
}
