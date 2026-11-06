// Package jobfetch fetches public job pages under a conservative policy:
// robots.txt is obeyed, each host is rate limited to a handful of requests per
// minute, transient failures back off, and non-public hosts are refused.
// It never bypasses CAPTCHAs, simulates logins or spoofs a browser identity.
package jobfetch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	// ErrInvalidURL marks a non-http(s) or private target.
	ErrInvalidURL = errors.New("only public http(s) urls are supported")
	// ErrDisallowed marks a robots.txt refusal or an unreadable robots.txt.
	ErrDisallowed = errors.New("robots.txt disallows this url")
	// ErrUpstream marks a non-2xx response after retries.
	ErrUpstream = errors.New("job page returned an error")
	// ErrTooLarge marks a page beyond the byte cap.
	ErrTooLarge = errors.New("job page exceeds size limit")
)

// Options tune the fetcher. Zero values fall back to the defaults below.
type Options struct {
	Client            *http.Client
	UserAgent         string
	MinHostInterval   time.Duration
	MaxBytes          int64
	Timeout           time.Duration
	MaxAttempts       int
	BackoffBase       time.Duration
	AllowPrivateHosts bool // tests only; production stays false
}

// HTTPFetcher is the public-web implementation of jd.FetchFunc.
type HTTPFetcher struct {
	client       *http.Client
	userAgent    string
	minInterval  time.Duration
	maxBytes     int64
	timeout      time.Duration
	maxAttempts  int
	backoffBase  time.Duration
	allowPrivate bool
	now          func() time.Time

	mu       sync.Mutex
	lastHit  map[string]time.Time
	robots   map[string]robotsRules
	robotsAt map[string]time.Time
}

// NewHTTPFetcher builds a fetcher with production defaults: 6 requests/minute
// per host, 10s timeout, 1 MiB body cap.
func NewHTTPFetcher(opts Options) *HTTPFetcher {
	if opts.Client == nil {
		opts.Client = &http.Client{}
	}
	if opts.UserAgent == "" {
		opts.UserAgent = "ai-interview-agent/0.1 (+https://github.com/waiting2050/ai-interview-agent)"
	}
	if opts.MinHostInterval <= 0 {
		opts.MinHostInterval = 10 * time.Second
	}
	if opts.MaxBytes <= 0 {
		opts.MaxBytes = 1 << 20
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 10 * time.Second
	}
	if opts.MaxAttempts <= 0 {
		opts.MaxAttempts = 3
	}
	if opts.BackoffBase <= 0 {
		opts.BackoffBase = time.Second
	}
	return &HTTPFetcher{
		client:       opts.Client,
		userAgent:    opts.UserAgent,
		minInterval:  opts.MinHostInterval,
		maxBytes:     opts.MaxBytes,
		timeout:      opts.Timeout,
		maxAttempts:  opts.MaxAttempts,
		backoffBase:  opts.BackoffBase,
		allowPrivate: opts.AllowPrivateHosts,
		now:          time.Now,
		lastHit:      map[string]time.Time{},
		robots:       map[string]robotsRules{},
		robotsAt:     map[string]time.Time{},
	}
}

// Result is one successfully fetched page.
type Result struct {
	URL       string
	Text      string
	FetchedAt time.Time
}

// Fetch retrieves one public page. Every hop is checked: URL policy, robots,
// per-host rate limit, then bounded retries with exponential backoff.
func (f *HTTPFetcher) Fetch(ctx context.Context, rawURL string) (Result, error) {
	target, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || target.Scheme == "" || target.Host == "" {
		return Result{}, fmt.Errorf("%w: %v", ErrInvalidURL, err)
	}
	if target.Scheme != "http" && target.Scheme != "https" {
		return Result{}, fmt.Errorf("%w: scheme %q", ErrInvalidURL, target.Scheme)
	}
	if target.User != nil {
		return Result{}, fmt.Errorf("%w: credentials in url are not allowed", ErrInvalidURL)
	}
	if !f.allowPrivate && isPrivateHost(target.Hostname()) {
		return Result{}, fmt.Errorf("%w: private host %q", ErrInvalidURL, target.Hostname())
	}
	host := strings.ToLower(target.Host)

	rules, err := f.robotsFor(ctx, target.Scheme, host)
	if err != nil {
		return Result{}, err
	}
	if !rules.allowed(target.EscapedPath()) {
		return Result{}, fmt.Errorf("%w: %s", ErrDisallowed, target.EscapedPath())
	}
	if err := f.wait(ctx, host, rules.crawlDelay); err != nil {
		return Result{}, err
	}

	var lastErr error
	for attempt := 1; attempt <= f.maxAttempts; attempt++ {
		if attempt > 1 {
			if err := sleep(ctx, f.backoffBase*time.Duration(1<<uint(attempt-2))); err != nil {
				return Result{}, err
			}
			if err := f.wait(ctx, host, rules.crawlDelay); err != nil {
				return Result{}, err
			}
		}
		result, retry, err := f.doFetch(ctx, target.String())
		if err == nil {
			return result, nil
		}
		lastErr = err
		if !retry {
			return Result{}, err
		}
	}
	return Result{}, lastErr
}

func (f *HTTPFetcher) doFetch(ctx context.Context, rawURL string) (Result, bool, error) {
	fetchCtx, cancel := context.WithTimeout(ctx, f.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, rawURL, nil)
	if err != nil {
		return Result{}, false, fmt.Errorf("%w: %v", ErrInvalidURL, err)
	}
	req.Header.Set("User-Agent", f.userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,text/plain,application/json;q=0.8")

	resp, err := f.client.Do(req)
	if err != nil {
		return Result{}, true, fmt.Errorf("%w: %v", ErrUpstream, err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return Result{}, true, fmt.Errorf("%w: status %d", ErrUpstream, resp.StatusCode)
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		return Result{}, false, fmt.Errorf("%w: status %d", ErrUpstream, resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, f.maxBytes+1))
	if err != nil {
		return Result{}, true, fmt.Errorf("%w: read body: %v", ErrUpstream, err)
	}
	if int64(len(body)) > f.maxBytes {
		return Result{}, false, ErrTooLarge
	}

	text := extractText(body, resp.Header.Get("Content-Type"))
	if text == "" {
		return Result{}, false, fmt.Errorf("%w: no readable text", ErrUpstream)
	}
	return Result{URL: rawURL, Text: text, FetchedAt: f.now().UTC()}, false, nil
}

func (f *HTTPFetcher) wait(ctx context.Context, host string, crawlDelay time.Duration) error {
	interval := f.minInterval
	if crawlDelay > interval {
		interval = crawlDelay
	}
	f.mu.Lock()
	last, ok := f.lastHit[host]
	f.lastHit[host] = f.now()
	f.mu.Unlock()
	if !ok {
		return nil
	}
	if wait := interval - f.now().Sub(last); wait > 0 {
		return sleep(ctx, wait)
	}
	return nil
}

func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// robotsRules is the subset of robots.txt this fetcher honours.
type robotsRules struct {
	disallow   []string
	allow      []string
	crawlDelay time.Duration
	denyAll    bool
}

func (r robotsRules) allowed(path string) bool {
	if r.denyAll {
		return false
	}
	if path == "" {
		path = "/"
	}
	allowLen := longestPrefix(r.allow, path)
	disallowLen := longestPrefix(r.disallow, path)
	return allowLen >= disallowLen
}

func longestPrefix(prefixes []string, path string) int {
	best := -1
	for _, prefix := range prefixes {
		if prefix == "/" || prefix == "" {
			if best < 0 {
				best = 0
			}
			continue
		}
		trimmed := strings.TrimSuffix(prefix, "*")
		if strings.HasPrefix(path, trimmed) && len(trimmed) > best {
			best = len(trimmed)
		}
	}
	return best
}

func (f *HTTPFetcher) robotsFor(ctx context.Context, scheme, host string) (robotsRules, error) {
	f.mu.Lock()
	rules, ok := f.robots[host]
	fetchedAt := f.robotsAt[host]
	f.mu.Unlock()
	if ok && f.now().Sub(fetchedAt) < time.Hour {
		return rules, nil
	}

	robotsURL := scheme + "://" + host + "/robots.txt"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, robotsURL, nil)
	if err != nil {
		return robotsRules{}, fmt.Errorf("%w: robots request: %v", ErrInvalidURL, err)
	}
	req.Header.Set("User-Agent", f.userAgent)
	resp, err := f.client.Do(req)
	if err != nil {
		return robotsRules{}, fmt.Errorf("%w: robots.txt unreachable: %v", ErrDisallowed, err)
	}
	defer resp.Body.Close()

	var parsed robotsRules
	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		parsed = robotsRules{}
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		body, err := io.ReadAll(io.LimitReader(resp.Body, 128<<10))
		if err != nil {
			return robotsRules{}, fmt.Errorf("%w: read robots.txt: %v", ErrDisallowed, err)
		}
		parsed = parseRobots(body)
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		parsed = robotsRules{denyAll: true}
	default:
		// Conservative: an unreadable robots.txt means we do not fetch.
		return robotsRules{}, fmt.Errorf("%w: robots.txt status %d", ErrDisallowed, resp.StatusCode)
	}

	f.mu.Lock()
	f.robots[host] = parsed
	f.robotsAt[host] = f.now()
	f.mu.Unlock()
	return parsed, nil
}

func parseRobots(body []byte) robotsRules {
	var rules robotsRules
	applies := false
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if i := strings.Index(line, "#"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		if line == "" {
			continue
		}
		key, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		switch key {
		case "user-agent":
			applies = value == "*"
		case "disallow":
			if applies && value != "" {
				rules.disallow = append(rules.disallow, value)
			}
		case "allow":
			if applies && value != "" {
				rules.allow = append(rules.allow, value)
			}
		case "crawl-delay":
			if applies {
				if seconds, err := strconv.ParseFloat(value, 64); err == nil && seconds > 0 {
					rules.crawlDelay = time.Duration(seconds * float64(time.Second))
				}
			}
		}
	}
	return rules
}

func isPrivateHost(host string) bool {
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified()
}

var (
	scriptBlock = regexp.MustCompile(`(?is)<script.*?</script>`)
	styleBlock  = regexp.MustCompile(`(?is)<style.*?</style>`)
	comment     = regexp.MustCompile(`(?s)<!--.*?-->`)
	blockBreak  = regexp.MustCompile(`(?i)<(br|/p|/div|/li|/h[1-6]|/tr|/section)[^>]*>`)
	tag         = regexp.MustCompile(`<[^>]+>`)
	blankLine   = regexp.MustCompile(`\n{3,}`)
	spaces      = regexp.MustCompile(`[ \t\f\v]+`)
)

func extractText(body []byte, contentType string) string {
	if strings.Contains(strings.ToLower(contentType), "json") {
		if text := extractJSONText(body); text != "" {
			return cleanText(text)
		}
	}
	return cleanText(string(body))
}

// cleanText strips markup and collapses whitespace. It is shared by HTML pages
// and ATS JSON payloads, whose content fields are usually HTML fragments.
func cleanText(raw string) string {
	raw = scriptBlock.ReplaceAllString(raw, " ")
	raw = styleBlock.ReplaceAllString(raw, " ")
	raw = comment.ReplaceAllString(raw, " ")
	raw = blockBreak.ReplaceAllString(raw, "\n")
	raw = tag.ReplaceAllString(raw, " ")
	raw = html.UnescapeString(raw)
	raw = strings.ReplaceAll(raw, "\u00a0", " ")
	raw = spaces.ReplaceAllString(raw, " ")
	lines := make([]string, 0, 64)
	for _, line := range strings.Split(raw, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	return blankLine.ReplaceAllString(strings.Join(lines, "\n"), "\n")
}

func extractJSONText(body []byte) string {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return ""
	}
	for _, key := range []string{"content", "description", "descriptionPlain", "text", "job_description"} {
		if value, ok := payload[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
