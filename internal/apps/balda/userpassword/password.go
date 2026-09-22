// Package userpassword hashes and verifies bounded local Backoffice passwords.
package userpassword

import (
	"errors"
	"fmt"

	"golang.org/x/crypto/bcrypt"
)

const (
	// MinLength is the shortest accepted local password.
	MinLength = 12
	// MaxLength is bcrypt's maximum password length in bytes.
	MaxLength = 72
	// Cost is the bcrypt work factor used for new credentials.
	Cost = 12
)

// ErrInvalidPassword reports a password outside the supported bounds.
var ErrInvalidPassword = errors.New("invalid password")

// Hash returns an adaptive, salted password hash.
func Hash(password []byte) (string, error) {
	if len(password) < MinLength || len(password) > MaxLength {
		return "", ErrInvalidPassword
	}
	hash, err := bcrypt.GenerateFromPassword(password, Cost)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}
	return string(hash), nil
}

// Verify compares a bounded password with an encoded hash.
func Verify(hash string, password []byte) bool {
	if len(password) < MinLength || len(password) > MaxLength || hash == "" {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), password) == nil
}
