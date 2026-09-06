package handlers

import (
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
)

// commandRequest is the temporary request shape for commands that have not
// yet moved to CommandActor. It stays private to the legacy handler.
type commandRequest struct {
	Locator         deliverycmd.Locator
	DeliveryOptions deliveryfmt.Options
	Transport       string
	Subject         string
	Access          legacyCommandAccess
	Invocation      legacyCommandInvocation
	EnabledCommands []string
	ChatID          int64
	TopicID         int
	UserID          int64
	Command         string
	Args            string
	IsDM            bool
}

type legacyCommandAccess struct{ SessionCommands bool }
type legacyCommandInvocation struct{ Root string }

func (r commandRequest) CommandUsage(command string) string {
	name, root := strings.TrimSpace(command), strings.TrimSpace(r.Invocation.Root)
	if root == "" {
		return "/" + name
	}
	return root + " " + name
}

func (r commandRequest) CommandEnabled(command string) bool {
	if len(r.EnabledCommands) == 0 {
		return true
	}
	for _, enabled := range r.EnabledCommands {
		if strings.TrimSpace(command) == strings.TrimSpace(enabled) {
			return true
		}
	}
	return false
}

func (r commandRequest) EnabledCommandUsage() string {
	commands := make([]string, 0, len(r.EnabledCommands))
	for _, command := range r.EnabledCommands {
		if name := strings.TrimSpace(command); name != "" {
			commands = append(commands, r.CommandUsage(name))
		}
	}
	return strings.Join(commands, ", ")
}
