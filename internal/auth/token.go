// Package auth implements password hashing and stateless HS256 JWT tokens.
// The signing secret comes from the environment; this package never contains a
// default secret.
package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	// ErrInvalidToken marks a malformed, badly signed or otherwise unusable token.
	ErrInvalidToken = errors.New("invalid access token")
	// ErrExpiredToken marks a token past its exp claim.
	ErrExpiredToken = errors.New("access token expired")
)

// Identity is the stateless view of a signed-in caller. It carries no database
// lookup: every request is verified by signature and expiry alone.
type Identity struct {
	UserID  string `json:"sub"`
	Email   string `json:"email,omitempty"`
	IsDemo  bool   `json:"demo,omitempty"`
	Issued  int64  `json:"iat"`
	Expires int64  `json:"exp"`
}

type tokenHeader struct {
	Alg string `json:"alg"`
	Typ string `json:"typ"`
}

// Signer signs and verifies JWTs with HMAC-SHA256.
type Signer struct {
	secret []byte
	ttl    time.Duration
	now    func() time.Time
}

// NewSigner builds a signer. secret must not be empty; callers resolve it from
// the environment at startup.
func NewSigner(secret []byte, ttl time.Duration) (*Signer, error) {
	if len(secret) == 0 {
		return nil, errors.New("jwt secret must not be empty")
	}
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	return &Signer{secret: secret, ttl: ttl, now: func() time.Time { return time.Now().UTC() }}, nil
}

// SetClock overrides the clock in tests.
func (s *Signer) SetClock(now func() time.Time) {
	if now != nil {
		s.now = now
	}
}

// TTL returns the configured token lifetime.
func (s *Signer) TTL() time.Duration { return s.ttl }

// Sign issues a token for identity. UserID must be non-empty.
func (s *Signer) Sign(id Identity) (string, error) {
	if strings.TrimSpace(id.UserID) == "" {
		return "", fmt.Errorf("%w: user id is required", ErrInvalidToken)
	}
	issued := s.now()
	id.Issued = issued.Unix()
	id.Expires = issued.Add(s.ttl).Unix()

	header := tokenHeader{Alg: "HS256", Typ: "JWT"}
	headerJSON, err := json.Marshal(header)
	if err != nil {
		return "", err
	}
	claimsJSON, err := json.Marshal(id)
	if err != nil {
		return "", err
	}
	unsigned := encodeSegment(headerJSON) + "." + encodeSegment(claimsJSON)
	return unsigned + "." + s.signature(unsigned), nil
}

// Parse verifies the signature and expiry and returns the claims.
func (s *Signer) Parse(token string) (Identity, error) {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) != 3 {
		return Identity{}, ErrInvalidToken
	}
	unsigned := parts[0] + "." + parts[1]
	if !hmac.Equal([]byte(parts[2]), []byte(s.signature(unsigned))) {
		return Identity{}, ErrInvalidToken
	}

	headerJSON, err := decodeSegment(parts[0])
	if err != nil {
		return Identity{}, ErrInvalidToken
	}
	var header tokenHeader
	if err := json.Unmarshal(headerJSON, &header); err != nil || header.Alg != "HS256" {
		return Identity{}, ErrInvalidToken
	}

	claimsJSON, err := decodeSegment(parts[1])
	if err != nil {
		return Identity{}, ErrInvalidToken
	}
	var id Identity
	if err := json.Unmarshal(claimsJSON, &id); err != nil {
		return Identity{}, ErrInvalidToken
	}
	if id.UserID == "" {
		return Identity{}, ErrInvalidToken
	}
	if id.Expires <= 0 || s.now().Unix() >= id.Expires {
		return Identity{}, ErrExpiredToken
	}
	return id, nil
}

func (s *Signer) signature(unsigned string) string {
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(unsigned))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func encodeSegment(v []byte) string {
	return base64.RawURLEncoding.EncodeToString(v)
}

func decodeSegment(v string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(v)
}
