package jd

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/waiting2050/ai-interview-agent/internal/domain/user"
	"github.com/waiting2050/ai-interview-agent/internal/platform/idgen"
)

var (
	// ErrInvalidArgument marks unusable input (no URL, no text, bad difficulty).
	ErrInvalidArgument = errors.New("invalid jd input")
	// ErrFetchFailed marks a public-page fetch that was refused or failed.
	ErrFetchFailed = errors.New("fetch jd page failed")
	// ErrNotFound marks a missing cached listing.
	ErrNotFound = errors.New("jd listing not found")
)

// Store persists parsed listings. Implementations live in internal/store.
type Store interface {
	Save(ctx context.Context, listing *Listing) error
	Get(ctx context.Context, id string) (*Listing, error)
}

// Fetched is the result of one public-page fetch.
type Fetched struct {
	URL       string
	Text      string
	FetchedAt time.Time
}

// FetchFunc fetches one public URL. It must refuse non-public pages, obey
// robots.txt and rate limits; those rules live in internal/platform/jobfetch.
type FetchFunc func(ctx context.Context, rawURL string) (Fetched, error)

// AnalyzeInput is the request shape of POST /v1/jd/analyze.
type AnalyzeInput struct {
	URL        string `json:"url"`
	Text       string `json:"text"`
	ResumeText string `json:"resume_text"`
	TargetRole string `json:"target_role"`
	Difficulty string `json:"difficulty"`
	Limit      int    `json:"limit"`
}

// Service parses, matches and caches job descriptions without a model.
type Service struct {
	store Store
	fetch FetchFunc
	now   func() time.Time
}

// New wires the service. fetch may be nil when only pasted text is supported.
func New(store Store, fetch FetchFunc) *Service {
	return &Service{store: store, fetch: fetch, now: func() time.Time { return time.Now().UTC() }}
}

// SetClock overrides the clock in tests.
func (s *Service) SetClock(now func() time.Time) {
	if now != nil {
		s.now = now
	}
}

// Analyze turns a URL or pasted text into a persisted listing plus match,
// gaps and outline. Pasted text wins when both are supplied, so a user can
// label a Boss listing with its source URL without us fetching it. userID is
// stamped on the listing so later reads can enforce ownership.
func (s *Service) Analyze(ctx context.Context, userID string, in AnalyzeInput) (Analysis, error) {
	if userID == "" {
		userID = user.LegacyID
	}
	text := strings.TrimSpace(in.Text)
	sourceURL := strings.TrimSpace(in.URL)
	sourceKind := SourcePaste
	fetchedAt := s.now()

	if text == "" {
		if sourceURL == "" {
			return Analysis{}, fmt.Errorf("%w: url 或 text 至少提供一个", ErrInvalidArgument)
		}
		if s.fetch == nil {
			return Analysis{}, fmt.Errorf("%w: 未启用网页抓取", ErrFetchFailed)
		}
		fetched, err := s.fetch(ctx, sourceURL)
		if err != nil {
			return Analysis{}, fmt.Errorf("%w: %v", ErrFetchFailed, err)
		}
		text = strings.TrimSpace(fetched.Text)
		if text == "" {
			return Analysis{}, fmt.Errorf("%w: 页面没有可解析的文本", ErrFetchFailed)
		}
		sourceKind = SourceURL
		sourceURL = fetched.URL
		fetchedAt = fetched.FetchedAt
	}

	difficulty := strings.TrimSpace(in.Difficulty)
	if difficulty != "" && difficulty != "junior" && difficulty != "mid" && difficulty != "senior" {
		return Analysis{}, fmt.Errorf("%w: difficulty 必须是 junior/mid/senior", ErrInvalidArgument)
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 5
	}
	if limit > 10 {
		limit = 10
	}

	listing := &Listing{
		ID:         idgen.New(),
		UserID:     userID,
		SourceKind: sourceKind,
		SourceURL:  sourceURL,
		FetchedAt:  fetchedAt,
		RawText:    text,
		Structured: Parse(text),
		CreatedAt:  s.now(),
	}
	if s.store != nil {
		if err := s.store.Save(ctx, listing); err != nil {
			return Analysis{}, fmt.Errorf("save jd listing: %w", err)
		}
	}
	return AnalyzeListing(listing, in.ResumeText, in.TargetRole, difficulty, limit), nil
}

// Get returns a cached listing by id. A listing owned by another account is
// reported as missing so the API can answer a uniform 404.
func (s *Service) Get(ctx context.Context, userID, id string) (*Listing, error) {
	if s.store == nil {
		return nil, ErrNotFound
	}
	listing, err := s.store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if listing.UserID != ownerOrLegacy(userID) {
		return nil, ErrNotFound
	}
	return listing, nil
}

func ownerOrLegacy(userID string) string {
	if userID == "" {
		return user.LegacyID
	}
	return userID
}
