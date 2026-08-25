package zulip

import (
	"fmt"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/auth"
)

func UserUsageMessage() string {
	return "Usage:\n" +
		"• /user add - Generate invite token\n" +
		"• /user list - Show collaborators and active invites\n" +
		"• /user remove <user_id> - Remove collaborator by ID\n"
}

func UserInviteMessage(token string) string {
	return fmt.Sprintf("Invite token created:\n%s\n\nHave the collaborator send:\n/start invite=%s", token, token)
}

func UserRemovedMessage(userID string) string {
	return fmt.Sprintf("Collaborator removed: %s", strings.TrimSpace(userID))
}

func UserListMessage(collaborators []auth.Collaborator, invites []auth.Invite) string {
	var lines []string
	if len(collaborators) > 0 {
		lines = append(lines, "Collaborators:")
		for _, c := range collaborators {
			name := "unknown"
			if strings.TrimSpace(c.Username) != "" {
				name = c.Username
			} else if strings.TrimSpace(c.FirstName) != "" {
				name = c.FirstName
			}
			lines = append(lines, fmt.Sprintf("• %s (%s) - added %s",
				c.UserID, name, c.AddedAt.Format("2006-01-02 15:04")))
		}
	} else {
		lines = append(lines, "No collaborators")
	}
	if len(invites) > 0 {
		lines = append(lines, "", "Active Invites:")
		for _, inv := range invites {
			lines = append(lines, fmt.Sprintf("expires %s", inv.ExpiresAt.Format("2006-01-02 15:04")))
		}
	}
	return strings.Join(lines, "\n")
}
