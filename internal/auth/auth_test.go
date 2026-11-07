package auth_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/waiting2050/ai-interview-agent/internal/auth"
	"github.com/waiting2050/ai-interview-agent/internal/domain/user"
	"github.com/waiting2050/ai-interview-agent/internal/store/memory"
)

func TestPasswordHashRoundTrip(t *testing.T) {
	hash, err := auth.HashPassword("correct-horse-battery")
	if err != nil {
		t.Fatal(err)
	}
	if hash == "correct-horse-battery" || len(hash) < 20 {
		t.Fatalf("hash looks wrong: %q", hash)
	}
	if !auth.VerifyPassword(hash, "correct-horse-battery") {
		t.Fatal("valid password rejected")
	}
	if auth.VerifyPassword(hash, "wrong-password") {
		t.Fatal("wrong password accepted")
	}
	if auth.VerifyPassword("", "anything") {
		t.Fatal("empty hash must never verify")
	}
}

func TestSignerRejectsTamperedAndExpiredTokens(t *testing.T) {
	signer, err := auth.NewSigner([]byte("unit-test-secret"), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)
	signer.SetClock(func() time.Time { return now })

	token, err := signer.Sign(auth.Identity{UserID: "usr_1", Email: "a@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := signer.Parse(token)
	if err != nil {
		t.Fatal(err)
	}
	if identity.UserID != "usr_1" {
		t.Fatalf("user id = %q", identity.UserID)
	}

	parts := strings.Split(token, ".")
	tampered := parts[0] + "." + parts[1] + "." + "AAAA"
	if _, err := signer.Parse(tampered); err == nil {
		t.Fatal("tampered signature accepted")
	}

	signer.SetClock(func() time.Time { return now.Add(2 * time.Hour) })
	if _, err := signer.Parse(token); err == nil {
		t.Fatal("expired token accepted")
	}
}

func TestServiceRegisterLoginAndDemoIsolation(t *testing.T) {
	store := memory.New()
	signer, err := auth.NewSigner([]byte("unit-test-secret"), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	svc := auth.New(store.Repos().Users, signer)
	ctx := context.Background()

	first, err := svc.Register(ctx, "  Alice@Example.COM ", "correct-horse-battery")
	if err != nil {
		t.Fatal(err)
	}
	if first.User.Email != "alice@example.com" {
		t.Fatalf("email not normalized: %q", first.User.Email)
	}
	if _, err := svc.Register(ctx, "alice@example.com", "correct-horse-battery"); err == nil {
		t.Fatal("duplicate email accepted")
	}

	login, err := svc.Login(ctx, "alice@example.com", "correct-horse-battery")
	if err != nil {
		t.Fatal(err)
	}
	if login.User.ID != first.User.ID {
		t.Fatalf("login user = %q want %q", login.User.ID, first.User.ID)
	}
	if _, err := svc.Login(ctx, "alice@example.com", "wrong"); err == nil {
		t.Fatal("wrong password accepted")
	}

	// The legacy placeholder can never be authenticated even if someone tries
	// to reuse its email.
	if _, err := svc.Login(ctx, "legacy@placeholder.local", ""); err != nil {
		if err != user.ErrInvalidCredentials {
			t.Fatalf("legacy login error = %v", err)
		}
	} else {
		t.Fatal("legacy placeholder logged in")
	}

	demoA, err := svc.Demo(ctx)
	if err != nil {
		t.Fatal(err)
	}
	demoB, err := svc.Demo(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if demoA.User.ID == demoB.User.ID || !demoA.User.IsDemo {
		t.Fatalf("demo users not isolated: %+v %+v", demoA.User, demoB.User)
	}
}
