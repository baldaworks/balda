package swarm

import (
	"fmt"
	"strings"
)

const (
	ModeLegacy  = "legacy"
	ModeShadow  = "shadow"
	ModeMailbox = "mailbox"
)

type Config struct {
	Enabled       bool
	Mode          string
	WebhookMode   string
	SchedulerMode string
	Shadow        ShadowConfig
}

type ShadowConfig struct {
	Enabled bool
}

func (c Config) MailboxEnabled() bool {
	return c.Enabled && (modeIs(c.Mode, ModeMailbox) || modeIs(c.WebhookMode, ModeMailbox) || modeIs(c.SchedulerMode, ModeMailbox))
}

func (c Config) GlobalMailboxEnabled() bool {
	return c.Enabled && modeIs(c.Mode, ModeMailbox)
}

func (c Config) WebhookMailboxEnabled() bool {
	return c.Enabled && modeIs(c.WebhookMode, ModeMailbox)
}

func (c Config) SchedulerMailboxEnabled() bool {
	return c.Enabled && modeIs(c.SchedulerMode, ModeMailbox)
}

func (c Config) ShadowEnabled() bool {
	return c.Enabled && c.Shadow.Enabled && modeIs(c.Mode, ModeShadow)
}

func (c Config) ShadowRuntimeEnabled() bool {
	return c.Enabled && c.Shadow.Enabled && (modeIs(c.Mode, ModeShadow) || modeIs(c.WebhookMode, ModeShadow) || modeIs(c.SchedulerMode, ModeShadow))
}

func (c Config) WebhookShadowEnabled() bool {
	return c.Enabled && c.Shadow.Enabled && modeIs(c.WebhookMode, ModeShadow)
}

func (c Config) SchedulerShadowEnabled() bool {
	return c.Enabled && c.Shadow.Enabled && modeIs(c.SchedulerMode, ModeShadow)
}

func (c Config) Normalized() (Config, error) {
	var err error
	if c.Mode, err = normalizeMode(c.Mode); err != nil {
		return Config{}, err
	}
	if c.WebhookMode, err = normalizeMode(c.WebhookMode); err != nil {
		return Config{}, err
	}
	if c.SchedulerMode, err = normalizeMode(c.SchedulerMode); err != nil {
		return Config{}, err
	}
	return c, nil
}

func modeIs(raw string, want string) bool {
	return modeOrDefault(raw) == want
}

func normalizeMode(raw string) (string, error) {
	mode := modeOrDefault(raw)
	switch mode {
	case ModeLegacy, ModeShadow, ModeMailbox:
		return mode, nil
	default:
		return "", fmt.Errorf("invalid swarm mode %q: supported values are %q, %q, and %q", mode, ModeLegacy, ModeShadow, ModeMailbox)
	}
}

func modeOrDefault(raw string) string {
	mode := strings.ToLower(strings.TrimSpace(raw))
	if mode == "" {
		return ModeShadow
	}
	return mode
}
