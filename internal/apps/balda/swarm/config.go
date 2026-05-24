package swarm

import "strings"

const (
	ModeShadow  = "shadow"
	ModeMailbox = "mailbox"
)

type Config struct {
	Enabled bool
	Mode    string
	Shadow  ShadowConfig
}

type ShadowConfig struct {
	Enabled bool
}

func (c Config) MailboxEnabled() bool {
	return c.Enabled && strings.EqualFold(strings.TrimSpace(c.Mode), ModeMailbox)
}

func (c Config) ShadowEnabled() bool {
	return c.Enabled && strings.EqualFold(strings.TrimSpace(c.Mode), ModeShadow) && c.Shadow.Enabled
}
