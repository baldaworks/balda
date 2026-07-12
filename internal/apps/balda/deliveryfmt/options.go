// Package deliveryfmt defines transport-neutral delivery presentation options.
package deliveryfmt

import (
	"strings"
)

type Format string

const (
	FormatAuto               Format = "auto"
	FormatMarkdown           Format = "markdown"
	FormatHTML               Format = "html"
	FormatPlain              Format = "plain"
	TelegramModeRichMarkdown        = "rich_markdown"
	TelegramModeRichHTML            = "rich_html"
	TelegramModeMarkdownV2          = "markdownv2"
	TelegramModeNone                = "none"
)

type ProgressPolicy struct {
	Typing      bool `json:"typing,omitempty"`
	Thinking    bool `json:"thinking,omitempty"`
	PlanUpdates bool `json:"plan_updates,omitempty"`
}

type Profile struct {
	Format         Format `json:"format,omitempty"`
	TelegramMode   string `json:"telegram_mode,omitempty"`
	FormattingMode string `json:"formatting_mode,omitempty"`
}

type Options struct {
	Profile        Profile        `json:"profile,omitempty,omitzero"`
	ProgressPolicy ProgressPolicy `json:"progress_policy,omitempty,omitzero"`
}

func NormalizeOptions(options Options) Options {
	return Options{
		Profile:        NormalizeProfile(options.Profile),
		ProgressPolicy: options.ProgressPolicy,
	}
}

func NormalizeProfile(profile Profile) Profile {
	format := Format(strings.ToLower(strings.TrimSpace(string(profile.Format))))
	telegramMode := strings.ToLower(strings.TrimSpace(profile.TelegramMode))
	legacy := strings.ToLower(strings.TrimSpace(profile.FormattingMode))

	if format == "" && legacy != "" {
		if isTelegramMode(legacy) {
			format = FormatAuto
			telegramMode = legacy
		} else if isDeliveryFormat(legacy) {
			format = Format(legacy)
		}
	}
	if format == "" {
		format = FormatAuto
	}
	if telegramMode == "" && isTelegramMode(legacy) {
		telegramMode = legacy
	}

	return Profile{
		Format:       format,
		TelegramMode: telegramMode,
	}
}

func isDeliveryFormat(value string) bool {
	switch value {
	case string(FormatAuto), string(FormatMarkdown), string(FormatHTML), string(FormatPlain):
		return true
	default:
		return false
	}
}

func isTelegramMode(value string) bool {
	switch value {
	case TelegramModeRichMarkdown, TelegramModeRichHTML, TelegramModeMarkdownV2, string(FormatHTML), TelegramModeNone:
		return true
	default:
		return false
	}
}

func EffectiveTelegramMode(profile Profile, fallback string) string {
	normalized := NormalizeProfile(profile)
	if normalized.Format == FormatPlain {
		return TelegramModeNone
	}
	if normalized.TelegramMode != "" {
		return normalizeTelegramMode(normalized.TelegramMode)
	}
	return normalizeTelegramMode(fallback)
}

func normalizeTelegramMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case TelegramModeRichMarkdown:
		return TelegramModeRichMarkdown
	case TelegramModeRichHTML:
		return TelegramModeRichHTML
	case TelegramModeMarkdownV2:
		return TelegramModeMarkdownV2
	case string(FormatHTML):
		return string(FormatHTML)
	case TelegramModeNone:
		return TelegramModeNone
	default:
		return TelegramModeRichMarkdown
	}
}
