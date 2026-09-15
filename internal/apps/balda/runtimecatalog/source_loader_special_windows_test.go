//go:build windows

package runtimecatalog

import "testing"

func makeSpecialFile(t *testing.T, _ string) {
	t.Helper()
	t.Skip("portable Windows special-file fixture is unavailable")
}
