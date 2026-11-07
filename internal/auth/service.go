package auth

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/waiting2050/ai-interview-agent/internal/domain/user"
	"github.com/waiting2050/ai-interview-agent/internal/platform/idgen"
	"github.com/waiting2050/ai-interview-agent/internal/ports"
)

// Account is the public view of a user. Password hashes never leave the server.
type Account struct {
	ID     string `json:"id"`
	Email  string `json:"email"`
	IsDemo bool   `json:"is_demo"`
}

// Token is the response of register / login / demo.
type Token struct {
	AccessToken string  `json:"access_token"`
	TokenType   string  `json:"token_type"`
	ExpiresIn   int64   `json:"expires_in"`
	User        Account `json:"user"`
}

// Service owns account creation, credential checks and token issuance.
type Service struct {
	users  ports.UserRepository
	signer *Signer
	now    func() time.Time
}

// New wires the service. A nil repository makes every account operation fail
// with ErrNotFound, which is only useful in tests.
func New(users ports.UserRepository, signer *Signer) *Service {
	return &Service{
		users:  users,
		signer: signer,
		now:    func() time.Time { return time.Now().UTC() },
	}
}

// SetClock overrides the clock in tests.
func (s *Service) SetClock(now func() time.Time) {
	if now != nil {
		s.now = now
	}
}

// Parse verifies a token without touching storage.
func (s *Service) Parse(token string) (Identity, error) {
	return s.signer.Parse(token)
}

// Register creates a password account and returns a signed token.
func (s *Service) Register(ctx context.Context, email, password string) (*Token, error) {
	email, password, err := validateCredentials(email, password)
	if err != nil {
		return nil, err
	}
	if _, err := s.users.GetByEmail(ctx, email); err == nil {
		return nil, user.ErrEmailTaken
	} else if !isNotFound(err) {
		return nil, fmt.Errorf("lookup user: %w", err)
	}

	hash, err := HashPassword(password)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", user.ErrInvalidArgument, err)
	}
	account := &user.User{
		ID:           idgen.New(),
		Email:        email,
		PasswordHash: hash,
		CreatedAt:    s.now(),
	}
	if err := s.users.Create(ctx, account); err != nil {
		return nil, err
	}
	return s.issue(account)
}

// Login verifies credentials and returns a signed token. Unknown emails and
// wrong passwords are indistinguishable.
func (s *Service) Login(ctx context.Context, email, password string) (*Token, error) {
	email = normalizeEmail(email)
	if email == "" || password == "" {
		return nil, user.ErrInvalidCredentials
	}
	account, err := s.users.GetByEmail(ctx, email)
	if err != nil {
		if isNotFound(err) {
			return nil, user.ErrInvalidCredentials
		}
		return nil, fmt.Errorf("lookup user: %w", err)
	}
	if !VerifyPassword(account.PasswordHash, password) {
		return nil, user.ErrInvalidCredentials
	}
	return s.issue(account)
}

// Demo creates a fresh throwaway account for anonymous demo use. Every call
// yields a new user id, so anonymous visitors can never see each other's data.
func (s *Service) Demo(ctx context.Context) (*Token, error) {
	id := user.DemoPrefix + idgen.New()
	account := &user.User{
		ID:        id,
		Email:     id + "@demo.local",
		IsDemo:    true,
		CreatedAt: s.now(),
	}
	if err := s.users.Create(ctx, account); err != nil {
		return nil, err
	}
	return s.issue(account)
}

func (s *Service) issue(account *user.User) (*Token, error) {
	raw, err := s.signer.Sign(Identity{
		UserID: account.ID,
		Email:  account.Email,
		IsDemo: account.IsDemo,
	})
	if err != nil {
		return nil, fmt.Errorf("sign access token: %w", err)
	}
	return &Token{
		AccessToken: raw,
		TokenType:   "Bearer",
		ExpiresIn:   int64(s.signer.TTL().Seconds()),
		User: Account{
			ID:     account.ID,
			Email:  account.Email,
			IsDemo: account.IsDemo,
		},
	}, nil
}

func validateCredentials(email, password string) (string, string, error) {
	email = normalizeEmail(email)
	switch {
	case email == "":
		return "", "", fmt.Errorf("%w: email is required", user.ErrInvalidArgument)
	case !strings.Contains(email, "@") || len(email) > 254:
		return "", "", fmt.Errorf("%w: email is invalid", user.ErrInvalidArgument)
	}
	switch {
	case len(password) < 8:
		return "", "", fmt.Errorf("%w: password must be at least 8 characters", user.ErrInvalidArgument)
	case len(password) > bcryptMaxBytes:
		return "", "", fmt.Errorf("%w: password must be at most 72 bytes", user.ErrInvalidArgument)
	}
	return email, password, nil
}

func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func isNotFound(err error) bool {
	return err == user.ErrNotFound || err == ports.ErrNotFound
}
