package auth

import (
	"errors"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// bcryptCost is the standard work factor. It is deliberately a named constant
// so tests and future tuning do not sprinkle magic numbers.
const bcryptCost = 10

// bcryptMaxBytes is the algorithm's hard input limit.
const bcryptMaxBytes = 72

// HashPassword turns a plaintext password into a bcrypt hash.
func HashPassword(password string) (string, error) {
	if len(password) > bcryptMaxBytes {
		return "", errors.New("password must be at most 72 bytes")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

// VerifyPassword reports whether password matches hash. An empty hash (the
// legacy placeholder owner) never matches.
func VerifyPassword(hash, password string) bool {
	if strings.TrimSpace(hash) == "" {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}
