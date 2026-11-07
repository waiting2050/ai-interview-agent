// Package user defines the minimal account entity used by the HTTP auth layer.
// Passwords are opaque hashes: the domain never sees plaintext after hashing.
package user

import (
	"errors"
	"time"
)

const (
	// LegacyID owns every row created before the auth migration. Its password
	// hash is empty, so no login path can ever authenticate as it.
	LegacyID = "usr_legacy"
	// DemoPrefix marks throwaway anonymous demo accounts.
	DemoPrefix = "usr_demo_"
)

var (
	ErrNotFound           = errors.New("user not found")
	ErrEmailTaken         = errors.New("email already registered")
	ErrInvalidCredentials = errors.New("invalid email or password")
	ErrInvalidArgument    = errors.New("invalid user input")
)

// User is one account. IsDemo is true for anonymous demo accounts created by
// POST /v1/auth/demo.
type User struct {
	ID           string
	Email        string
	PasswordHash string
	IsDemo       bool
	CreatedAt    time.Time
}
