// Package redaction removes credential-shaped values from user-visible text and logs.
package redaction

import (
	"regexp"
	"strings"
)

var (
	bearerHeaderPattern = regexp.MustCompile(`(?i)(authorization\s*:\s*bearer\s+)([^\s]+)`)
	keyValuePattern     = regexp.MustCompile(`(?i)\b(token|secret|password|api[_-]?key|access[_-]?key|private[_-]?key)\b(\s*[:=]\s*)([^\s,;]+)`)
	pemPattern          = regexp.MustCompile(`(?s)-----BEGIN [^-]+-----.*?-----END [^-]+-----`)
	githubTokenPattern  = regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9_]{20,}\b`)
	telegramToken       = regexp.MustCompile(`\b\d{6,10}:[A-Za-z0-9_-]{20,}\b`)
	slackToken          = regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9-]{10,}\b`)
)

// Secrets trims text and replaces known credential shapes with stable markers.
func Secrets(raw string) string {
	text := strings.TrimSpace(raw)
	if text == "" {
		return text
	}
	text = pemPattern.ReplaceAllString(text, "[REDACTED_PEM]")
	text = bearerHeaderPattern.ReplaceAllString(text, "${1}[REDACTED]")
	text = keyValuePattern.ReplaceAllString(text, "${1}${2}[REDACTED]")
	text = githubTokenPattern.ReplaceAllString(text, "[REDACTED_TOKEN]")
	text = telegramToken.ReplaceAllString(text, "[REDACTED_TOKEN]")
	text = slackToken.ReplaceAllString(text, "[REDACTED_TOKEN]")
	return text
}
