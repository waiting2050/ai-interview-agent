package jobfetch

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testFetcher(t *testing.T) *HTTPFetcher {
	t.Helper()
	return NewHTTPFetcher(Options{
		MinHostInterval:   time.Millisecond,
		Timeout:           2 * time.Second,
		MaxAttempts:       3,
		BackoffBase:       time.Millisecond,
		AllowPrivateHosts: true,
	})
}

func TestFetchExtractsReadableText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robots.txt":
			fmt.Fprint(w, "User-agent: *\nDisallow: /private\n")
		case "/job":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, `<html><head><style>.x{}</style><script>alert(1)</script></head>
				<body><h1>Go 后端开发工程师</h1><p>要求熟悉 MySQL、Redis</p></body></html>`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	fetcher := testFetcher(t)
	result, err := fetcher.Fetch(context.Background(), srv.URL+"/job")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Text, "Go 后端开发工程师") {
		t.Fatalf("text = %q", result.Text)
	}
	if strings.Contains(result.Text, "alert(1)") || strings.Contains(result.Text, "<p>") {
		t.Fatalf("html was not stripped: %q", result.Text)
	}
	if result.FetchedAt.IsZero() {
		t.Fatal("fetched_at is zero")
	}
}

func TestFetchObeysRobotsTxt(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			fmt.Fprint(w, "User-agent: *\nDisallow: /private\n")
			return
		}
		fmt.Fprint(w, "ok")
	}))
	defer srv.Close()

	fetcher := testFetcher(t)
	if _, err := fetcher.Fetch(context.Background(), srv.URL+"/private"); !errors.Is(err, ErrDisallowed) {
		t.Fatalf("private path err = %v", err)
	}
}

func TestFetchAllowsWhenRobotsMissing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, "Go 后端岗位")
	}))
	defer srv.Close()

	fetcher := testFetcher(t)
	if _, err := fetcher.Fetch(context.Background(), srv.URL+"/job"); err != nil {
		t.Fatal(err)
	}
}

func TestFetchRefusesPrivateHostsByDefault(t *testing.T) {
	fetcher := NewHTTPFetcher(Options{})
	if _, err := fetcher.Fetch(context.Background(), "http://127.0.0.1:1/"); !errors.Is(err, ErrInvalidURL) {
		t.Fatalf("private host err = %v", err)
	}
	if _, err := fetcher.Fetch(context.Background(), "ftp://example.com/job"); !errors.Is(err, ErrInvalidURL) {
		t.Fatalf("scheme err = %v", err)
	}
}

func TestFetchRetriesTransientErrors(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			http.NotFound(w, r)
			return
		}
		calls++
		if calls < 2 {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		fmt.Fprint(w, "Go 后端岗位")
	}))
	defer srv.Close()

	fetcher := testFetcher(t)
	if _, err := fetcher.Fetch(context.Background(), srv.URL+"/job"); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d", calls)
	}
}

func TestFetchExtractsJSONDescription(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"content":"<p>熟悉 Go 与 Redis</p>","title":"Backend"}`)
	}))
	defer srv.Close()

	fetcher := testFetcher(t)
	result, err := fetcher.Fetch(context.Background(), srv.URL+"/api/job")
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "熟悉 Go 与 Redis" {
		t.Fatalf("text = %q", result.Text)
	}
}
