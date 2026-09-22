//go:build integration && sqlite

package state

import "testing"

func TestSQLiteProviderContract(t *testing.T) {
	runProviderContract(t, func(*testing.T) contractOpener { return NewSQLiteProvider })
}
