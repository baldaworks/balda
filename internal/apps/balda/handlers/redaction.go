package handlers

import (
	"regexp"
	"strings"
)

var (
	secretBearerHeaderPattern = regexp.MustCompile(`(?i)(authorization\s*:\s*bearer\s+)([^\s]+)`)
	secretKeyValuePattern     = regexp.MustCompile(`(?i)\b(token|secret|password|api[_-]?key|access[_-]?key|private[_-]?key)\b(\s*[:=]\s*)([^\s,;]+)`)
	secretPEMPattern          = regexp.MustCompile(`(?s)-----BEGIN [^-]+-----.*?-----END [^-]+-----`)
	secretGitHubTokenPattern  = regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9_]{20,}\b`)
	secretTelegramToken       = regexp.MustCompile(`\b\d{6,10}:[A-Za-z0-9_-]{20,}\b`)
)

func redactSecrets(raw string) string {
	text := strings.TrimSpace(raw)
	if text == "" {
		return text
	}
	text = secretPEMPattern.ReplaceAllString(text, "[REDACTED_PEM]")
	text = secretBearerHeaderPattern.ReplaceAllString(text, "${1}[REDACTED]")
	text = secretKeyValuePattern.ReplaceAllString(text, "${1}${2}[REDACTED]")
	text = secretGitHubTokenPattern.ReplaceAllString(text, "[REDACTED_TOKEN]")
	text = secretTelegramToken.ReplaceAllString(text, "[REDACTED_TOKEN]")
	return text
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
