package telegram

import "github.com/baldaworks/balda/internal/apps/balda/telegramfmt"

const (
	FormattingModeRichMarkdown = telegramfmt.ModeRichMarkdown
	FormattingModeRichHTML     = telegramfmt.ModeRichHTML
	FormattingModeNone         = telegramfmt.ModeNone
	RichMessagesDocsURL        = telegramfmt.RichMessagesDocsURL
)

func NormalizeFormattingMode(raw string) string {
	return telegramfmt.NormalizeMode(raw)
}

func ValidateFormattingMode(raw string) (string, error) {
	return telegramfmt.ValidateMode(raw)
}

func FormattingPromptRuleAndExample(mode string) (string, string) {
	return telegramfmt.PromptRuleAndExample(mode)
}

func EscapeHTML(text string) string {
	return telegramfmt.HTML(text)
}

func HTMLPlainText(text string) string {
	return telegramfmt.HTMLPlainText(text)
}
