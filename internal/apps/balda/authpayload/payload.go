// Package authpayload recognizes invitation credentials without transport policy.
package authpayload

import "strings"

// Prefix identifies Backoffice account binding credentials.
const Prefix = "bind_"

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
	for _, c := range fields[0][len(Prefix):] {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
		default:
			return "", false
		}
	}
	return fields[0], true
}

// Contains reports invitation-bearing text that must not enter agent context.
func Contains(text string) bool {
	for {
		index := strings.Index(text, Prefix)
		if index < 0 {
			return false
		}
		text = text[index:]
		if len(text) >= len(Prefix)+32 {
			if _, ok := Parse(text[:len(Prefix)+32]); ok {
				return true
			}
		}
		text = text[len(Prefix):]
	}
}
