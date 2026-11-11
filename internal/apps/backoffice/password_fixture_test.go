package backoffice

import (
	"sync"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

const (
	validPassword   = "correct horse battery staple"
	invalidPassword = "incorrect password"
)

var fixturePasswordHash = sync.OnceValues(func() (string, error) {
	// Browser fixtures exercise authentication behavior without repeating
	// production-cost bcrypt work for every independent test user.
	hash, err := bcrypt.GenerateFromPassword([]byte(validPassword), bcrypt.MinCost)
	return string(hash), err
})

func validPasswordHash(t *testing.T) string {
	t.Helper()
	hash, err := fixturePasswordHash()
	if err != nil {
		t.Fatalf("hash test password: %v", err)
	}
	return hash
}
