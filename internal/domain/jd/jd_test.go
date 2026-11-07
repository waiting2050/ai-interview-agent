package jd_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/waiting2050/ai-interview-agent/internal/domain/jd"
	"github.com/waiting2050/ai-interview-agent/internal/domain/skills"
)

const sampleJD = `公司：星海科技
岗位：Go 后端开发工程师
岗位职责
- 负责订单履约系统，使用 Go、MySQL、Redis
- 参与高并发链路优化与消息队列治理
任职要求
1. 3 年以上后端开发经验
2. 熟悉分布式系统与 Kafka
加分项
- 有 Kubernetes、gRPC 经验者优先
`

const sampleResume = `3 年 Go 后端经验，负责订单履约与库存链路，用本地消息表保证最终一致性。
技能：Go、MySQL、Redis、Linux
项目：订单履约系统
- 用 Redis 缓存热点数据，接口 P99 从 120ms 降到 45ms
`

func TestParseStructuredJD(t *testing.T) {
	got := jd.Parse(sampleJD)

	if got.Role != "Go 后端开发工程师" {
		t.Fatalf("role = %q", got.Role)
	}
	if got.Company != "星海科技" {
		t.Fatalf("company = %q", got.Company)
	}
	if got.MinYears != 3 {
		t.Fatalf("min years = %d", got.MinYears)
	}
	for _, skill := range []string{"Go", "MySQL", "Redis", "分布式", "高并发", "Kafka", "消息队列"} {
		if !contains(got.RequiredSkills, skill) {
			t.Fatalf("required skills missing %q: %v", skill, got.RequiredSkills)
		}
	}
	for _, skill := range []string{"Kubernetes", "gRPC"} {
		if !contains(got.NiceToHave, skill) {
			t.Fatalf("nice-to-have missing %q: %v", skill, got.NiceToHave)
		}
		if contains(got.RequiredSkills, skill) {
			t.Fatalf("%q must not be required", skill)
		}
	}
}

func TestAliasMatchingCountsSynonymCoverage(t *testing.T) {
	const aliasJD = `岗位：后端开发
任职要求
1. 熟悉 Kubernetes 与 PostgreSQL`
	resumeText := `技能：k8s 1.27、postgres 15、golang`
	listing := &jd.Listing{ID: "jd-alias", RawText: aliasJD, Structured: jd.Parse(aliasJD)}
	got := jd.AnalyzeListing(listing, resumeText, "", "", 3)

	if !contains(got.Match.MatchedKeywords, "Kubernetes") {
		t.Fatalf("k8s should cover Kubernetes: %v", got.Match.MatchedKeywords)
	}
	if !contains(got.Match.MatchedKeywords, "PostgreSQL") {
		t.Fatalf("postgres should cover PostgreSQL: %v", got.Match.MatchedKeywords)
	}
	// No alias in the resume: Go keeps its old exact-match result.
	if len(got.Match.MissingKeywords) != 0 {
		t.Fatalf("unexpected missing keywords: %v", got.Match.MissingKeywords)
	}
	if len(got.Gaps.MissingSkills) != 0 {
		t.Fatalf("unexpected missing skills: %v", got.Gaps.MissingSkills)
	}

	aliasSource := false
	for _, check := range got.Match.SkillChecks {
		if check.Skill == "Kubernetes" || check.Skill == "PostgreSQL" {
			if check.Source != "alias" || check.Status != "covered" {
				t.Fatalf("check = %+v", check)
			}
			aliasSource = true
		}
	}
	if !aliasSource {
		t.Fatal("expected alias-sourced checks")
	}
}

func TestWeakMatchesAreListedButNeverCounted(t *testing.T) {
	const weakJD = `岗位：后端开发
任职要求
1. 熟悉 Kafka`
	resumeText := `技能：消息队列、MQ 使用经验`
	listing := &jd.Listing{ID: "jd-weak", RawText: weakJD, Structured: jd.Parse(weakJD)}
	got := jd.AnalyzeListing(listing, resumeText, "", "", 3)

	if contains(got.Match.MatchedKeywords, "Kafka") {
		t.Fatalf("weak match must not count as covered: %v", got.Match.MatchedKeywords)
	}
	if !contains(got.Match.WeakKeywords, "Kafka") {
		t.Fatalf("weak keyword missing: %v", got.Match.WeakKeywords)
	}
	var check *skills.Result
	for i := range got.Match.SkillChecks {
		if got.Match.SkillChecks[i].Skill == "Kafka" {
			check = &got.Match.SkillChecks[i]
		}
	}
	if check == nil || check.Status != "weak" || check.Source != "parent" || check.Matched != "消息队列" {
		t.Fatalf("kafka check = %+v", check)
	}
	if contains(got.Gaps.MissingSkills, "Kafka") {
		t.Fatalf("weak match must not appear as a hard gap: %v", got.Gaps.MissingSkills)
	}
	if len(got.Gaps.WeakSkills) != 1 || got.Gaps.WeakSkills[0].Skill != "Kafka" {
		t.Fatalf("weak gap = %+v", got.Gaps.WeakSkills)
	}
	if !containsPrefix(got.Gaps.MissingExperiences, "JD 要求 Kafka") {
		t.Fatalf("experience gap = %v", got.Gaps.MissingExperiences)
	}
	// A weak hint never changes the score: the legacy matcher also missed Kafka.
	if got.Match.Score < 0 || got.Match.Score > 100 {
		t.Fatalf("score = %d", got.Match.Score)
	}
}

func TestNoAliasHitKeepsLegacyKeywordResults(t *testing.T) {
	listing := &jd.Listing{ID: "jd-legacy", RawText: sampleJD, Structured: jd.Parse(sampleJD)}
	got := jd.AnalyzeListing(listing, sampleResume, "", "", 5)

	lower := strings.ToLower(sampleResume)
	var legacyMatched, legacyMissing []string
	for _, kw := range listing.Structured.Keywords {
		if strings.Contains(lower, strings.ToLower(kw)) {
			legacyMatched = append(legacyMatched, kw)
		} else {
			legacyMissing = append(legacyMissing, kw)
		}
	}
	if !sameStrings(got.Match.MatchedKeywords, legacyMatched) {
		t.Fatalf("matched keywords changed:\n got %v\nwant %v", got.Match.MatchedKeywords, legacyMatched)
	}
	if !sameStrings(got.Match.MissingKeywords, legacyMissing) {
		t.Fatalf("missing keywords changed:\n got %v\nwant %v", got.Match.MissingKeywords, legacyMissing)
	}
	if len(got.Match.WeakKeywords) != 0 {
		t.Fatalf("sample should have no weak hits: %v", got.Match.WeakKeywords)
	}
	for _, check := range got.Match.SkillChecks {
		if check.Status == "covered" && check.Source != "exact" {
			t.Fatalf("unexpected non-exact coverage: %+v", check)
		}
	}
}

func TestEditDistanceDoesNotMergeRedisAndRedisson(t *testing.T) {
	const redisJD = `岗位：后端开发
任职要求
1. 熟悉 Redis`
	listing := &jd.Listing{ID: "jd-redis", RawText: redisJD, Structured: jd.Parse(redisJD)}
	got := jd.AnalyzeListing(listing, `技能：Redisson 客户端`, "", "", 3)

	for _, check := range got.Match.SkillChecks {
		if check.Skill == "Redis" && check.Status == "covered" {
			t.Fatalf("Redisson must not cover Redis: %+v", check)
		}
	}
	if !contains(got.Gaps.MissingSkills, "Redis") {
		t.Fatalf("Redis should stay a gap: %v", got.Gaps.MissingSkills)
	}
}

func sameStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for _, item := range got {
		if !contains(want, item) {
			return false
		}
	}
	return true
}

func TestMatchReusesScreenScoreAndListsGaps(t *testing.T) {
	listing := &jd.Listing{
		ID:         "jd-1",
		SourceKind: jd.SourcePaste,
		FetchedAt:  time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC),
		RawText:    sampleJD,
		Structured: jd.Parse(sampleJD),
	}
	got := jd.AnalyzeListing(listing, sampleResume, "", "", 5)

	if !got.Match.Available {
		t.Fatalf("match unavailable: %+v", got.Match)
	}
	if got.Match.Score <= 0 {
		t.Fatalf("score = %d", got.Match.Score)
	}
	if len(got.Match.MissingKeywords) == 0 {
		t.Fatal("expected missing keywords")
	}
	if !contains(got.Gaps.MissingSkills, "Kafka") {
		t.Fatalf("gap missing Kafka: %v", got.Gaps.MissingSkills)
	}
	if !containsPrefix(got.Gaps.MissingExperiences, "简历未体现 Kafka") {
		t.Fatalf("experience gap: %v", got.Gaps.MissingExperiences)
	}
	if len(got.Outline) == 0 {
		t.Fatal("outline is empty")
	}
	if got.ModelCalled {
		t.Fatal("rule-based analysis must not claim a model call")
	}
}

func TestAnalyzeWithoutResumeStillParses(t *testing.T) {
	listing := &jd.Listing{ID: "jd-2", RawText: sampleJD, Structured: jd.Parse(sampleJD)}
	got := jd.AnalyzeListing(listing, "", "", "", 3)

	if got.Match.Available {
		t.Fatal("match must be unavailable without a resume")
	}
	if got.Match.Note == "" {
		t.Fatal("expected an explanatory note")
	}
	if len(got.Gaps.MissingSkills) == 0 {
		t.Fatal("all required skills should be listed as gaps")
	}
	if len(got.Outline) != 3 {
		t.Fatalf("outline length = %d", len(got.Outline))
	}
}

type fakeStore struct {
	saved map[string]*jd.Listing
}

func (f *fakeStore) Save(_ context.Context, listing *jd.Listing) error {
	f.saved[listing.ID] = listing
	return nil
}

func (f *fakeStore) Get(_ context.Context, id string) (*jd.Listing, error) {
	listing, ok := f.saved[id]
	if !ok {
		return nil, jd.ErrNotFound
	}
	return listing, nil
}

func TestServiceFetchesURLAndPersists(t *testing.T) {
	store := &fakeStore{saved: map[string]*jd.Listing{}}
	now := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	svc := jd.New(store, func(_ context.Context, rawURL string) (jd.Fetched, error) {
		return jd.Fetched{URL: rawURL, Text: sampleJD, FetchedAt: now}, nil
	})
	svc.SetClock(func() time.Time { return now })

	got, err := svc.Analyze(context.Background(), "", jd.AnalyzeInput{
		URL:        "https://jobs.example.com/123",
		ResumeText: sampleResume,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.SourceKind != jd.SourceURL {
		t.Fatalf("source kind = %q", got.SourceKind)
	}
	if got.SourceURL != "https://jobs.example.com/123" {
		t.Fatalf("source url = %q", got.SourceURL)
	}
	if !got.FetchedAt.Equal(now) {
		t.Fatalf("fetched at = %v", got.FetchedAt)
	}
	if store.saved[got.JDID] == nil {
		t.Fatal("listing was not persisted")
	}

	cached, err := svc.Get(context.Background(), "", got.JDID)
	if err != nil {
		t.Fatal(err)
	}
	if cached.RawText != strings.TrimSpace(sampleJD) {
		t.Fatal("cached raw text differs")
	}
}

func TestServicePrefersPastedTextAndSkipsFetch(t *testing.T) {
	store := &fakeStore{saved: map[string]*jd.Listing{}}
	fetched := false
	svc := jd.New(store, func(context.Context, string) (jd.Fetched, error) {
		fetched = true
		return jd.Fetched{}, errors.New("should not be called")
	})

	got, err := svc.Analyze(context.Background(), "", jd.AnalyzeInput{
		URL:  "https://www.zhipin.com/job_detail/abc.html",
		Text: sampleJD,
	})
	if err != nil {
		t.Fatal(err)
	}
	if fetched {
		t.Fatal("pasted text must not trigger a fetch")
	}
	if got.SourceKind != jd.SourcePaste {
		t.Fatalf("source kind = %q", got.SourceKind)
	}
	if got.SourceURL == "" {
		t.Fatal("declared source url should be kept for provenance")
	}
}

func TestServiceErrors(t *testing.T) {
	store := &fakeStore{saved: map[string]*jd.Listing{}}
	svc := jd.New(store, nil)

	if _, err := svc.Analyze(context.Background(), "", jd.AnalyzeInput{}); !errors.Is(err, jd.ErrInvalidArgument) {
		t.Fatalf("empty input err = %v", err)
	}
	if _, err := svc.Analyze(context.Background(), "", jd.AnalyzeInput{
		Text: sampleJD, Difficulty: "expert",
	}); !errors.Is(err, jd.ErrInvalidArgument) {
		t.Fatalf("bad difficulty err = %v", err)
	}
	if _, err := svc.Analyze(context.Background(), "", jd.AnalyzeInput{URL: "https://jobs.example.com/1"}); !errors.Is(err, jd.ErrFetchFailed) {
		t.Fatalf("fetch nil err = %v", err)
	}
	if _, err := svc.Get(context.Background(), "", "missing"); !errors.Is(err, jd.ErrNotFound) {
		t.Fatalf("get err = %v", err)
	}
}

func TestBuildKitKeepsEveryFactTraceable(t *testing.T) {
	listing := &jd.Listing{
		ID:         "jd-kit",
		SourceKind: jd.SourceURL,
		SourceURL:  "https://jobs.example.com/123",
		FetchedAt:  time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC),
		RawText:    sampleJD,
		Structured: jd.Parse(sampleJD),
	}
	kit, err := jd.BuildKit(listing, jd.KitInput{
		ResumeText:    sampleResume,
		CandidateName: "张三",
	}, time.Date(2026, 11, 6, 11, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}

	if kit.ModelCalled {
		t.Fatal("kit must be rule-generated")
	}
	if !strings.Contains(kit.Summary, "订单履约系统") {
		t.Fatalf("summary should cite the real project: %s", kit.Summary)
	}
	if !strings.Contains(kit.Summary, "3 年") {
		t.Fatalf("summary should cite the real years: %s", kit.Summary)
	}
	if strings.Contains(kit.Summary, "50%") || strings.Contains(kit.Summary, "提升") {
		t.Fatalf("summary invented a metric: %s", kit.Summary)
	}
	if len(kit.Facts) == 0 {
		t.Fatal("facts are empty")
	}
	for _, fact := range kit.Facts {
		if !strings.HasPrefix(fact.Source, "简历") {
			t.Fatalf("fact %q does not trace back to the resume", fact.Text)
		}
	}
	if len(kit.RehearsalQuestions) == 0 {
		t.Fatal("rehearsal questions are empty")
	}
	if !strings.Contains(kit.Markdown, "不提供自动投递") {
		t.Fatal("markdown must state no auto-apply")
	}
	if !strings.Contains(kit.Markdown, "事实来源") {
		t.Fatal("markdown must list fact provenance")
	}

	if _, err := jd.BuildKit(listing, jd.KitInput{}, time.Now()); !errors.Is(err, jd.ErrEmptyResume) {
		t.Fatalf("empty resume err = %v", err)
	}
}

func contains(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func containsPrefix(items []string, prefix string) bool {
	for _, item := range items {
		if strings.HasPrefix(item, prefix) {
			return true
		}
	}
	return false
}
