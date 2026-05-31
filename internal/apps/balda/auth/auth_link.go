package auth

import (
	"fmt"
	"strings"
)

// BuildOwnerAuthCommand returns the /start command for owner bootstrap.
func BuildOwnerAuthCommand(ownerToken string) string {
	return fmt.Sprintf("/start owner=%s", strings.TrimSpace(ownerToken))
}

// BuildOwnerAuthLink returns the Telegram auth link for owner bootstrap.
func BuildOwnerAuthLink(botUsername, ownerToken string) string {
	username := strings.TrimSpace(botUsername)
	if username == "" {
		username = "<bot_username>"
	}
	return fmt.Sprintf("https://t.me/%s?start=owner_%s", username, strings.TrimSpace(ownerToken))
}
