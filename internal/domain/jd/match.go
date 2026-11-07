package jd

import (
	"fmt"
	"strings"
	"time"

	"github.com/waiting2050/ai-interview-agent/internal/domain/domainpack"
	"github.com/waiting2050/ai-interview-agent/internal/domain/resume"
	"github.com/waiting2050/ai-interview-agent/internal/domain/skills"
)

// Match reuses the M12 screen_score calibration: it is computed by the same
// resume rules engine, not by a second, JD-specific formula.
type Match struct {
	Available       bool                    `json:"available"`
	Score           int                     `json:"score"`
	Components      []resume.ScoreComponent `json:"components"`
	MatchedKeywords []string                `json:"matched_keywords"`
	// WeakKeywords are synonym-family or related hints. They never enter the
	// coverage numerator; they are listed so the UI can show "待确认".
	WeakKeywords    []string        `json:"weak_keywords,omitempty"`
	MissingKeywords []string        `json:"missing_keywords"`
	SkillChecks     []skills.Result `json:"skill_checks,omitempty"`
	Note            string          `json:"note,omitempty"`
}

// Gap is the actionable difference between the JD and this resume.
type Gap struct {
	MissingKeywords    []string        `json:"missing_keywords"`
	WeakSkills         []skills.Result `json:"weak_skills,omitempty"`
	MissingSkills      []string        `json:"missing_skills"`
	MissingExperiences []string        `json:"missing_experiences"`
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

	matched, weak, missing, checks := evaluateKeywords(resumeText, listing.Structured.Keywords)
	return Match{
		Available:       true,
		Score:           diag.ScreenScore.Score,
		Components:      diag.ScreenScore.Components,
		MatchedKeywords: matched,
		WeakKeywords:    weak,
		MissingKeywords: missing,
		SkillChecks:     checks,
		Note:            "匹配度复用简历诊断的 screen_score 口径（JD 覆盖 40 / 技术密度 20 / 岗位匹配 20 / 结构 20）",
	}, buildGap(listing, resumeText, missing, checks)
}

// evaluateKeywords applies the rule ladder (exact, alias, hierarchy, fuzzy)
// per keyword. Weak hints are kept separate and never counted as coverage.
func evaluateKeywords(resumeText string, keywords []string) (matched, weak, missing []string, checks []skills.Result) {
	for _, kw := range keywords {
		check := skills.Check(resumeText, kw)
		checks = append(checks, check)
		switch check.Status {
		case skills.StatusCovered:
			matched = append(matched, kw)
		case skills.StatusWeak:
			weak = append(weak, kw)
		default:
			missing = append(missing, kw)
		}
	}
	return matched, weak, missing, checks
}

func buildGap(listing *Listing, resumeText string, missingKeywords []string, checks []skills.Result) Gap {
	gap := Gap{MissingKeywords: missingKeywords}

	bySkill := make(map[string]skills.Result, len(checks))
	for _, check := range checks {
		bySkill[strings.ToLower(check.Skill)] = check
	}

	for _, skill := range listing.Structured.RequiredSkills {
		check, ok := bySkill[strings.ToLower(skill)]
		if !ok {
			check = skills.Check(resumeText, skill)
			bySkill[strings.ToLower(skill)] = check
		}
		switch check.Status {
		case skills.StatusCovered:
			continue
		case skills.StatusWeak:
			gap.WeakSkills = append(gap.WeakSkills, check)
			gap.MissingExperiences = append(gap.MissingExperiences, weakGapText(check))
		default:
			gap.MissingSkills = append(gap.MissingSkills, skill)
			gap.MissingExperiences = append(gap.MissingExperiences,
				fmt.Sprintf("简历未体现 %s 相关项目经历", skill))
		}
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

// weakGapText explains a hint without concluding that the skill is missing.
func weakGapText(check skills.Result) string {
	relation := map[string]string{
		skills.SourceChild:   "下位技能",
		skills.SourceParent:  "上位技能",
		skills.SourceSibling: "同族技能",
		skills.SourceRelated: "相关技能",
		skills.SourceFuzzy:   "近似写法",
	}[check.Source]
	if relation == "" {
		relation = "相关技能"
	}
	return fmt.Sprintf("JD 要求 %s，简历仅命中%s %s，建议确认是否实际使用过", check.Skill, relation, check.Matched)
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
