package webhookroutecmd

import "regexp"

var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// ValidName reports whether name is a stable, single-segment webhook slug.
func ValidName(name string) bool {
	return namePattern.MatchString(name)
}

// CanonicalPath derives a callback path from a validated base path and name.
func CanonicalPath(basePath, name string) string {
	return basePath + "/webhooks/" + name
}
