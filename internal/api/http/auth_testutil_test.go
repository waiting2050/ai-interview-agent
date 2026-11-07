package httpapi

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/waiting2050/ai-interview-agent/internal/auth"
	"github.com/waiting2050/ai-interview-agent/internal/store/memory"
)

// testAuthFor builds a real auth service over the memory store and issues one
// demo token. Tests use the returned user id when they seed rows directly.
func testAuthFor(t *testing.T, store *memory.Store) (*auth.Service, string, string) {
	t.Helper()
	signer, err := auth.NewSigner([]byte("test-secret-0123456789abcdef0123456789"), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	svc := auth.New(store.Repos().Users, signer)
	token, err := svc.Demo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return svc, token.AccessToken, token.User.ID
}

// withTestAuthorization lets existing tests keep using http.Get/Post without
// touching every call site: requests that carry no Authorization header are
// signed in as the test account.
func withTestAuthorization(next http.Handler, token string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		next.ServeHTTP(w, r)
	})
}
