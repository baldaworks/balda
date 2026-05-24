package swarm

import "strings"

const ModeMailbox = "mailbox"

type Config struct {
	Enabled bool
	Mode    string
}

func (c Config) MailboxEnabled() bool {
	return c.Enabled && strings.EqualFold(strings.TrimSpace(c.Mode), ModeMailbox)
}
