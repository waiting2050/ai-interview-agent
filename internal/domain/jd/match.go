package jd

import (
	"fmt"
	"strings"
	"time"

	"github.com/waiting2050/ai-interview-agent/internal/domain/domainpack"
	"github.com/waiting2050/ai-interview-agent/internal/domain/resume"
)

// Match reuses the M12 screen_score calibration: it is computed by the same
// resume rules engine, not by a second, JD-specific formula.
type Match struct {
	Available       bool                    `json:"available"`
	Score           int                     `json:"score"`
	Components      []resume.ScoreComponent `json:"components"`
	MatchedKeywords []string                `json:"matched_keywords"`
	MissingKeywords []string                `json:"missing_keywords"`
	Note            string                  `json:"note,omitempty"`
}

// Gap is the actionable difference between the JD and this resume.
type Gap struct {
	MissingKeywords    []string `json:"missing_keywords"`
	MissingSkills      []string `json:"missing_skills"`
	MissingExperiences []string `json:"missing_experiences"`
}

// OutlineItem is one interview topic generated for this JD.
type OutlineItem struct {
	Seq            int      `json:"seq"`
	Topic          string   `json:"topic"`
	Objective      string   `json:"objective"`
	Difficulty     string   `json:"difficulty"`
	ExpectedPoints []string `json:"expected_points"`
	Direction      string   `json:"direction"`
}

// Analysis is the response of POST /v1/jd/analyze.
type Analysis struct {
	JDID        string        `json:"jd_id"`
	SourceKind  string        `json:"source_kind"`
	SourceURL   string        `json:"source_url,omitempty"`
	FetchedAt   time.Time     `json:"fetched_at"`
	Structured  Structured    `json:"structured"`
	Match       Match         `json:"match"`
	Gaps        Gap           `json:"gaps"`
	Outline     []OutlineItem `json:"outline"`
	ModelCalled bool          `json:"model_called"`
}

// AnalyzeListing parses the persisted listing against a resume. Matching is
// skipped (Available=false) when no resume text is supplied, so the endpoint
// still works as a pure JD parser.
func AnalyzeListing(listing *Listing, resumeText, targetRole, difficulty string, limit int) Analysis {
	role := strings.TrimSpace(targetRole)
	if role == "" {
		role = listing.Structured.Role
	}
	if difficulty == "" {
		difficulty = DifficultyFromYears(listing.Structured.MinYears)
	}

	match, gap := matchResume(listing, resumeText)
	outline := Outline(listing.RawText, difficulty, limit)

	return Analysis{
		JDID:        listing.ID,
		SourceKind:  listing.SourceKind,
		SourceURL:   listing.SourceURL,
		FetchedAt:   listing.FetchedAt,
		Structured:  listing.Structured,
		Match:       match,
		Gaps:        gap,
		Outline:     outline,
		ModelCalled: false,
	}
}

func matchResume(listing *Listing, resumeText string) (Match, Gap) {
	resumeText = strings.TrimSpace(resumeText)
	if resumeText == "" {
		skills := append([]string(nil), listing.Structured.RequiredSkills...)
		return Match{
				Available:       false,
				Score:           0,
				MissingKeywords: append([]string(nil), listing.Structured.Keywords...),
				Note:            "未提供简历文本，仅解析 JD；补充简历后可计算匹配度",
			}, Gap{
				MissingKeywords:    append([]string(nil), listing.Structured.Keywords...),
				MissingSkills:      skills,
				MissingExperiences: []string{"未提供简历，无法核对年限与项目经历"},
			}
	}

	diag, err := resume.DefaultAnalyzer().Analyze(resume.AnalyzeOptions{
		Text:       resumeText,
		JDText:     listing.RawText,
		TargetRole: listing.Structured.Role,
	})
	if err != nil {
		return Match{
			Available: false,
			Note:      "简历文本无法解析：" + err.Error(),
		}, Gap{}
	}

	matched, missing := splitKeywords(resumeText, listing.Structured.Keywords)
	return Match{
		Available:       true,
		Score:           diag.ScreenScore.Score,
		Components:      diag.ScreenScore.Components,
		MatchedKeywords: matched,
		MissingKeywords: missing,
		Note:            "匹配度复用简历诊断的 screen_score 口径（JD 覆盖 40 / 技术密度 20 / 岗位匹配 20 / 结构 20）",
	}, buildGap(listing, resumeText, missing)
}

func splitKeywords(resumeText string, keywords []string) (matched, missing []string) {
	lower := strings.ToLower(resumeText)
	for _, kw := range keywords {
		if strings.Contains(lower, strings.ToLower(kw)) {
			matched = append(matched, kw)
			continue
		}
		missing = append(missing, kw)
	}
	return matched, missing
}

func buildGap(listing *Listing, resumeText string, missingKeywords []string) Gap {
	gap := Gap{MissingKeywords: missingKeywords}

	for _, skill := range listing.Structured.RequiredSkills {
		if skillPresent(resumeText, skill) {
			continue
		}
		gap.MissingSkills = append(gap.MissingSkills, skill)
		gap.MissingExperiences = append(gap.MissingExperiences,
			fmt.Sprintf("简历未体现 %s 相关项目经历", skill))
	}

	if listing.Structured.MinYears > 0 {
		profile := resume.Parse(resumeText)
		if profile.Years < listing.Structured.MinYears {
			gap.MissingExperiences = append([]string{
				fmt.Sprintf("JD 要求 %d 年以上经验，简历显示 %d 年", listing.Structured.MinYears, profile.Years),
			}, gap.MissingExperiences...)
		}
	}
	if len(gap.MissingExperiences) > 8 {
		gap.MissingExperiences = gap.MissingExperiences[:8]
	}
	return gap
}

// Outline selects the best matching domain pack and plans its topics for this
// JD. The pack is chosen by direction keyword hits; the legacy Go backend pack
// is the fallback when no direction matches.
func Outline(text, difficulty string, limit int) []OutlineItem {
	pack := bestPack(text)
	if pack == nil {
		return nil
	}
	items := pack.Plan(text, difficulty, limit)
	out := make([]OutlineItem, 0, len(items))
	for i, item := range items {
		out = append(out, OutlineItem{
			Seq:            i + 1,
			Topic:          item.Topic,
			Objective:      item.Objective,
			Difficulty:     item.Difficulty,
			ExpectedPoints: append([]string(nil), item.ExpectedPoints...),
			Direction:      item.Direction,
		})
	}
	return out
}

func bestPack(text string) *domainpack.Pack {
	packs := domainpack.Default().List()
	var best *domainpack.Pack
	bestHits := 0
	for _, pack := range packs {
		hits := len(pack.Detect(text))
		if hits > bestHits {
			best, bestHits = pack, hits
		}
	}
	if best != nil {
		return best
	}
	return domainpack.Default().MustGet(domainpack.LegacyGoBackendID)
}
