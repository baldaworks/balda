package commandapp

import (
	"context"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
)

// Handler owns transport-neutral command policy and execution.
type Handler interface {
	HandleCommand(ctx context.Context, request Request) error
}

// Access records authorization established by authenticated ingress.
type Access struct {
	SessionCommands bool
}

// Invocation describes the external command syntax used by a transport.
type Invocation struct {
	Root string
}

// Request is the transport-neutral command ingress contract.
// Concrete transports map their local command event shape into this request
// before invoking shared command semantics.
type Request struct {
	Locator         deliverycmd.Locator
	DeliveryOptions deliveryfmt.Options
	Transport       string
	Subject         string
	Access          Access
	Invocation      Invocation
	EnabledCommands []string
	// ChatID, TopicID, and UserID are compatibility fields for commands whose
	// Telegram-specific capabilities have not yet moved behind neutral ports.
	ChatID  int64
	TopicID int
	UserID  int64
	Command string
	Args    string
	IsDM    bool
}

// CommandUsage formats one command using the transport's invocation root.
func (r Request) CommandUsage(command string) string {
	name := strings.TrimSpace(command)
	root := strings.TrimSpace(r.Invocation.Root)
	if root == "" {
		return "/" + name
	}
	return root + " " + name
}

// CommandEnabled reports whether ingress enabled a command. An empty set keeps
// compatibility with transports that expose the complete command surface.
func (r Request) CommandEnabled(command string) bool {
	if len(r.EnabledCommands) == 0 {
		return true
	}
	name := strings.TrimSpace(command)
	for _, enabled := range r.EnabledCommands {
		if name == strings.TrimSpace(enabled) {
			return true
		}
	}
	return false
}

// EnabledCommandUsage formats the command surface exposed by ingress.
func (r Request) EnabledCommandUsage() string {
	commands := make([]string, 0, len(r.EnabledCommands))
	for _, command := range r.EnabledCommands {
		if name := strings.TrimSpace(command); name != "" {
			commands = append(commands, r.CommandUsage(name))
		}
	}
	return strings.Join(commands, ", ")
}
