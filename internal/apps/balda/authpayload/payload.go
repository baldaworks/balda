// Package authpayload recognizes invitation credentials without transport policy.
package authpayload

import (
	"regexp"
	"strings"
)

// Prefix identifies Backoffice account binding credentials.
const Prefix = "bind_"

var (
	payloadPattern = regexp.MustCompile(`^` + Prefix + `[A-Za-z0-9_-]{32}$`)
	// Damaged or truncated credentials still must not enter logs or agent context.
	credentialPattern = regexp.MustCompile(Prefix + `[A-Za-z0-9_-]{16,}`)
)

// Parse accepts an exact payload or a supported start command argument.
func Parse(text string) (string, bool) {
	fields := strings.Fields(text)
	if len(fields) == 2 && (fields[0] == "/start" || strings.HasPrefix(fields[0], "/start@")) {
		fields = fields[1:]
	} else if len(fields) == 3 && fields[0] == "/balda" && fields[1] == "start" {
		fields = fields[2:]
	}
	if len(fields) != 1 || len(fields[0]) != len(Prefix)+32 || !strings.HasPrefix(fields[0], Prefix) {
		return "", false
	}
	if !payloadPattern.MatchString(fields[0]) {
		return "", false
	}
	return fields[0], true
}

// Contains reports invitation-bearing text that must not enter agent context.
func Contains(text string) bool { return credentialPattern.MatchString(text) }

// Redact replaces invitation credentials before crossing a logging boundary.
func Redact(text string) string {
	return credentialPattern.ReplaceAllString(text, "[REDACTED_INVITATION]")
}
