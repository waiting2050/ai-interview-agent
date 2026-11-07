package resume

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/waiting2050/ai-interview-agent/internal/domain/skills"
)

var (
	// ErrEmptyResume marks an analyze request with no usable text.
	ErrEmptyResume = errors.New("resume text is empty")
	// ErrInvalidSegmentType marks an unknown experience type override.
	ErrInvalidSegmentType = errors.New("invalid resume segment type")
)

// AnalyzeOptions is the input of one deterministic resume diagnosis.
type AnalyzeOptions struct {
	Text         string
	JDText       string
	TargetRole   string
	Filename     string
	SegmentTypes map[int]SegmentType
}

// RuleEngine applies the YAML ruleset. It never calls a model or the network.
type RuleEngine struct {
	ruleset *Ruleset
}

// NewRuleEngine builds an engine from a validated ruleset.
func NewRuleEngine(ruleset *Ruleset) *RuleEngine {
	if ruleset == nil {
		ruleset = MustDefaultRuleset()
	}
	return &RuleEngine{ruleset: ruleset}
}

var (
	defaultEngineOnce sync.Once
	defaultEngine     *RuleEngine
)

// DefaultAnalyzer returns the shared engine backed by the embedded rules.
func DefaultAnalyzer() *RuleEngine {
	defaultEngineOnce.Do(func() {
		defaultEngine = NewRuleEngine(MustDefaultRuleset())
	})
	return defaultEngine
}

// Ruleset exposes the loaded configuration (tests and docs).
func (e *RuleEngine) Ruleset() *Ruleset { return e.ruleset }

// Band is one of the five resume-quality bands.
type Band struct {
	Level       int    `json:"level"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// Rewrite is the "差 → 好" example attached to every issue.
type Rewrite struct {
	Before string `json:"before"`
	After  string `json:"after"`
}

// Issue is one rule hit with the original quote and why it lost points.
type Issue struct {
	RuleID        string      `json:"rule_id"`
	Title         string      `json:"title"`
	Dimension     string      `json:"dimension"`
	DimensionName string      `json:"dimension_name"`
	Severity      string      `json:"severity"`
	Evidence      string      `json:"evidence"`
	Mode          string      `json:"mode"`
	Deducted      int         `json:"deducted"`
	Quote         string      `json:"quote"`
	Segment       int         `json:"segment"`
	SegmentType   SegmentType `json:"segment_type,omitempty"`
	Rewrite       Rewrite     `json:"rewrite"`
	Note          string      `json:"note,omitempty"`
}

// DimensionScore is one research dimension after applying its cap.
type DimensionScore struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Weight   int    `json:"weight"`
	Deducted int    `json:"deducted"`
	Score    int    `json:"score"`
}

// SegmentScore is the typed score of one experience block.
type SegmentScore struct {
	Index         int              `json:"index"`
	Title         string           `json:"title"`
	Type          SegmentType      `json:"type"`
	TypeSource    string           `json:"type_source"`
	Months        int              `json:"months"`
	WeightPercent int              `json:"weight_percent"`
	Score         int              `json:"score"`
	Dimensions    []DimensionScore `json:"dimensions"`
	Followups     []string         `json:"followups"`
}

// ManualCheck is a rule that cannot be decided from plain text.
type ManualCheck struct {
	RuleID    string `json:"rule_id"`
	Title     string `json:"title"`
	Dimension string `json:"dimension"`
	Severity  string `json:"severity"`
	Note      string `json:"note"`
}

// ConfirmItem asks the user to pick the type of an ambiguous entry.
type ConfirmItem struct {
	Index          int           `json:"index"`
	Title          string        `json:"title"`
	Snippet        string        `json:"snippet"`
	SuggestedTypes []SegmentType `json:"suggested_types"`
}

// ScoreComponent explains one part of screen_score / tech_score.
type ScoreComponent struct {
	Name   string `json:"name"`
	Score  int    `json:"score"`
	Weight int    `json:"weight"`
	Note   string `json:"note"`
}

// ScoreBreakdown keeps a total plus its visible components.
type ScoreBreakdown struct {
	Score      int              `json:"score"`
	Components []ScoreComponent `json:"components"`
}

// Followup is the type-specific interview probe set.
type Followup struct {
	Segment int         `json:"segment"`
	Type    SegmentType `json:"type"`
	Title   string      `json:"title"`
	Prompts []string    `json:"prompts"`
}

// Diagnosis is the full response of POST /v1/resume/analyze.
type Diagnosis struct {
	RulesVersion      string           `json:"rules_version"`
	RulesSummary      string           `json:"rules_summary"`
	OverallScore      int              `json:"overall_score"`
	Band              Band             `json:"band"`
	DocumentScore     int              `json:"document_score"`
	Weighted          bool             `json:"weighted"`
	ScreenScore       ScoreBreakdown   `json:"screen_score"`
	TechScore         ScoreBreakdown   `json:"tech_score"`
	ConflictAdvice    string           `json:"conflict_advice"`
	Redline           bool             `json:"redline"`
	NeedsConfirm      bool             `json:"needs_confirm"`
	Dimensions        []DimensionScore `json:"dimensions"`
	Segments          []SegmentScore   `json:"segments"`
	Issues            []Issue          `json:"issues"`
	TopIssues         []string         `json:"top_issues"`
	ManualChecks      []ManualCheck    `json:"manual_checks"`
	NeedsConfirmation []ConfirmItem    `json:"needs_confirmation"`
	Followups         []Followup       `json:"followups"`
	ModelCalled       bool             `json:"model_called"`
	RuleHits          int              `json:"rule_hits"`
}

// Analyze runs all machine-checkable rules and computes every score.
func (e *RuleEngine) Analyze(opts AnalyzeOptions) (Diagnosis, error) {
	text := strings.TrimSpace(strings.ReplaceAll(opts.Text, "\r\n", "\n"))
	if text == "" {
		return Diagnosis{}, ErrEmptyResume
	}
	for index, t := range opts.SegmentTypes {
		if !IsValidSegmentType(string(t)) {
			return Diagnosis{}, fmt.Errorf("%w: index %d type %q", ErrInvalidSegmentType, index, t)
		}
	}

	segments := parseSegments(text, opts.SegmentTypes)
	ctx := buildAnalysisContext(text, segments, opts)

	var (
		issues []Issue
		manual []ManualCheck
	)
	manualSeen := map[string]bool{}
	redline := false

	for _, check := range e.ruleset.Checks {
		if check.Mode == "manual" || !hasDetector(check.ID) {
			note := check.Note
			if note == "" {
				note = "语义/版式判断，需人工确认"
			}
			manual = append(manual, ManualCheck{
				RuleID: check.ID, Title: check.Title, Dimension: check.Dimension,
				Severity: check.Severity, Note: note,
			})
			manualSeen[check.ID] = true
			continue
		}
		hits := detectors[check.ID](ctx)
		for _, h := range hits {
			segmentType := SegmentType("")
			if h.Segment >= 0 {
				if h.Segment >= len(segments) {
					continue
				}
				seg := segments[h.Segment]
				if seg.Type == TypeUnknown || !check.AppliesToType(seg.Type) {
					continue
				}
				segmentType = seg.Type
			}
			base := check.DeductionFor(segmentType)
			if base == 0 {
				continue
			}
			deducted := int(math.Round(float64(base) * check.autoScoreRatio()))
			if deducted <= 0 {
				continue
			}
			dimension, ok := e.ruleset.DimensionByID(check.Dimension)
			if !ok {
				continue
			}
			issue := Issue{
				RuleID: check.ID, Title: check.Title, Dimension: check.Dimension,
				DimensionName: dimension.Name, Severity: check.Severity, Evidence: check.Evidence,
				Mode: check.Mode, Deducted: deducted, Quote: h.Quote, Segment: h.Segment,
				SegmentType: segmentType,
				Rewrite:     Rewrite{Before: check.RewriteBefore, After: check.RewriteAfter},
				Note:        h.Note,
			}
			if h.Segment >= 0 {
				issue.SegmentType = segments[h.Segment].Type
			}
			issues = append(issues, issue)
			if check.Redline {
				redline = true
			}
			if check.Mode == "auto_manual" {
				remaining := base - deducted
				if remaining > 0 && !manualSeen[check.ID] {
					manual = append(manual, ManualCheck{
						RuleID: check.ID, Title: check.Title, Dimension: check.Dimension,
						Severity: check.Severity,
						Note:     fmt.Sprintf("自动判定覆盖 %d 分，剩余 %d 分需人工确认（机器无法判断语义边界）", deducted, remaining),
					})
					manualSeen[check.ID] = true
				}
			}
		}
	}

	dims, documentScore := e.scoreDimensions(issues)
	if redline {
		// Research rule: a confirmed red line zeroes A and caps the total at 69.
		for i := range dims {
			if dims[i].ID == "A" {
				dims[i].Deducted = dims[i].Weight
				dims[i].Score = 0
			}
		}
		documentScore = 100
		for _, d := range dims {
			documentScore -= d.Deducted
		}
		if documentScore > 69 {
			documentScore = 69
		}
	}
	if documentScore < 0 {
		documentScore = 0
	}

	segmentsOut, overall, weighted := e.scoreSegments(segments, issues)
	if !weighted {
		overall = documentScore
	}
	// A red line means "cannot pass screening", so the weighted overall score
	// must not land in a higher band than the capped document score.
	if redline && overall > 69 {
		overall = 69
	}

	needsConfirm := make([]ConfirmItem, 0, len(segments))
	for _, seg := range segments {
		if seg.NeedsConfirm {
			needsConfirm = append(needsConfirm, ConfirmItem{
				Index: seg.Index, Title: seg.Title, Snippet: truncate(seg.Raw, 160),
				SuggestedTypes: seg.SuggestedTypes,
			})
		}
	}

	screen := screenScore(text, opts.TargetRole, opts.JDText, ctx)
	tech := techScore(segments, ctx)
	advice := conflictAdvice(screen.Score, tech.Score)

	issues = sortIssues(issues)
	top := topIssues(issues)

	diag := Diagnosis{
		RulesVersion: e.ruleset.Version, RulesSummary: e.ruleset.Summary(),
		OverallScore: overall, Band: bandFor(overall), DocumentScore: documentScore,
		Weighted: weighted, ScreenScore: screen, TechScore: tech, ConflictAdvice: advice,
		Redline: redline, NeedsConfirm: len(needsConfirm) > 0,
		Dimensions: dims, Segments: segmentsOut, Issues: issues, TopIssues: top,
		ManualChecks: manual, NeedsConfirmation: needsConfirm, Followups: followupsFor(segments),
		ModelCalled: false, RuleHits: len(issues),
	}
	return diag, nil
}

func (e *RuleEngine) scoreDimensions(issues []Issue) ([]DimensionScore, int) {
	raw := map[string]int{}
	for _, issue := range issues {
		raw[issue.Dimension] += issue.Deducted
	}
	out := make([]DimensionScore, 0, len(e.ruleset.Dimensions))
	total := 0
	for _, d := range e.ruleset.Dimensions {
		deducted := raw[d.ID]
		if deducted > d.Weight {
			deducted = d.Weight
		}
		out = append(out, DimensionScore{
			ID: d.ID, Name: d.Name, Weight: d.Weight, Deducted: deducted, Score: d.Weight - deducted,
		})
		total += deducted
	}
	return out, 100 - total
}

func (e *RuleEngine) scoreSegments(segments []*Segment, issues []Issue) ([]SegmentScore, int, bool) {
	known := make([]*Segment, 0, len(segments))
	totalWeight := 0
	for _, seg := range segments {
		if seg.Type == TypeUnknown {
			continue
		}
		if _, ok := e.ruleset.ProfileFor(seg.Type); !ok {
			continue
		}
		known = append(known, seg)
		totalWeight += segmentWeight(seg)
	}

	out := make([]SegmentScore, 0, len(segments))
	for _, seg := range segments {
		item := SegmentScore{
			Index: seg.Index, Title: truncate(seg.Title, 80), Type: seg.Type,
			TypeSource: seg.TypeSource, Months: seg.Months,
			Followups: followupPrompts(seg.Type),
		}
		if seg.Type == TypeUnknown {
			item.Score = -1
			out = append(out, item)
			continue
		}
		profile, ok := e.ruleset.ProfileFor(seg.Type)
		if !ok {
			item.Score = -1
			out = append(out, item)
			continue
		}
		deducted := map[string]int{}
		for _, issue := range issues {
			if issue.Segment != seg.Index {
				continue
			}
			typedID := profile.Map[issue.Dimension]
			if typedID == "" {
				continue
			}
			deducted[typedID] += issue.Deducted
		}
		dims := make([]DimensionScore, 0, len(profile.Dimensions))
		score := 100
		for _, d := range profile.Dimensions {
			take := deducted[d.ID]
			if take > d.Weight {
				take = d.Weight
			}
			score -= take
			dims = append(dims, DimensionScore{ID: d.ID, Name: d.Name, Weight: d.Weight, Deducted: take, Score: d.Weight - take})
		}
		if score < 0 {
			score = 0
		}
		if totalWeight > 0 {
			item.WeightPercent = int(math.Round(float64(segmentWeight(seg)) / float64(totalWeight) * 100))
		}
		item.Score = score
		item.Dimensions = dims
		out = append(out, item)
	}

	if len(known) == 0 || totalWeight == 0 {
		return out, 0, false
	}
	weighted := 0.0
	for _, item := range out {
		if item.Score < 0 {
			continue
		}
		seg := segments[item.Index]
		weighted += float64(item.Score) * float64(segmentWeight(seg))
	}
	return out, int(math.Round(weighted / float64(totalWeight))), true
}

func segmentWeight(seg *Segment) int {
	n := len([]rune(seg.Raw))
	if n < 40 {
		return 40
	}
	return n
}

func bandFor(score int) Band {
	switch {
	case score <= 39:
		return Band{1, "1 档 · 无法通过初筛", "存在造假/职级错位，或全篇无结果、无数据、无边界"}
	case score <= 54:
		return Band{2, "2 档 · 表述空泛", "有项目但只有负责/参与，缺 before→after，岗位匹配弱"}
	case score <= 69:
		return Band{3, "3 档 · 基本完整", "项目真实、结构基本完整，但量化口径不严、分工略模糊"}
	case score <= 84:
		return Band{4, "4 档 · 可信清晰", "有可信指标与清晰边界，项目与 JD 对齐，仅有少量细节扣分"}
	default:
		return Band{5, "5 档 · 可直接投递", "每条经历都能展开讲 5 分钟，指标有分母/环境/来源，有取舍与复盘"}
	}
}

func sortIssues(in []Issue) []Issue {
	out := append([]Issue(nil), in...)
	rank := map[string]int{"high": 3, "medium": 2, "low": 1}
	sort.SliceStable(out, func(i, j int) bool {
		if rank[out[i].Severity] != rank[out[j].Severity] {
			return rank[out[i].Severity] > rank[out[j].Severity]
		}
		if out[i].Deducted != out[j].Deducted {
			return out[i].Deducted > out[j].Deducted
		}
		return out[i].RuleID < out[j].RuleID
	})
	return out
}

func topIssues(issues []Issue) []string {
	out := make([]string, 0, 3)
	for _, issue := range issues {
		if len(out) == 3 {
			break
		}
		out = append(out, fmt.Sprintf("%s：%s（扣 %d 分）", issue.RuleID, issue.Title, issue.Deducted))
	}
	if len(out) == 0 {
		out = append(out, "未发现自动可判定的扣分项；版式与语义项仍需人工复核")
	}
	return out
}

func followupPrompts(t SegmentType) []string {
	switch t {
	case TypeInternship:
		return []string{
			"这段改动具体落在哪几个文件、哪几行？你个人写了哪些？",
			"上线后哪个指标动了？前后各是多少，谁统计的？",
			"谁 review 了你的代码？提了什么意见？",
			"出过什么事故？你怎么发现、怎么处理的？",
		}
	case TypePersonalProject:
		return []string{
			"如果用户量 ×100，这套系统最先崩在哪？",
			"做过压测吗？数据量、并发和机器规格是多少？",
			"为什么选 X 而不是 Y？取舍是什么？",
			"哪些边界或功能是你明确没做的？",
		}
	case TypeOpenSource:
		return []string{
			"你提交的 PR 解决了什么问题？维护者提了什么修改意见？",
			"这个模块原本的边界是什么，你改动了哪一部分？",
			"你如何验证改动没有破坏兼容性？",
			"社区最后合入了吗？没合入的原因是什么？",
		}
	case TypeCompetition:
		return []string{
			"比赛中你负责哪一部分？团队怎么分工？",
			"方案在哪个环节被卡住？你们怎么取舍？",
			"最终名次与参赛队伍数量是多少？",
			"如果重来一次，你会改哪一步？",
		}
	case TypeCoursework:
		return []string{
			"这是课程要求还是你主动扩展？扩展了什么？",
			"代码量大概多少？哪些是课程框架提供的？",
			"如果让你独立重写，你会先改哪里？",
			"这套东西离生产系统还差什么？",
		}
	default:
		return []string{
			"这段经历是你独立完成的，还是团队合作？你负责哪一块？",
			"能给出可验证的产出吗（仓库、文档、指标）？",
		}
	}
}

func followupsFor(segments []*Segment) []Followup {
	out := make([]Followup, 0, len(segments))
	for _, seg := range segments {
		out = append(out, Followup{
			Segment: seg.Index, Type: seg.Type, Title: truncate(seg.Title, 80),
			Prompts: followupPrompts(seg.Type),
		})
	}
	return out
}

func buildAnalysisContext(text string, segments []*Segment, opts AnalyzeOptions) *analysisContext {
	ctx := &analysisContext{text: text, segments: segments}
	ctx.lines = strings.Split(text, "\n")
	for _, seg := range segments {
		for _, line := range seg.Lines {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" {
				continue
			}
			if strings.HasPrefix(trimmed, "-") || strings.HasPrefix(trimmed, "•") ||
				strings.HasPrefix(trimmed, "·") || strings.HasPrefix(trimmed, "*") ||
				regexp.MustCompile(`^\d+[.、)]`).MatchString(trimmed) {
				ctx.bullets = append(ctx.bullets, bullet{Segment: seg.Index, Text: strings.TrimLeft(trimmed, "-•·* ")})
			}
			if ctx.skillsLine == "" && containsAny(trimmed, "技能", "技术栈") {
				ctx.skillsLine = truncate(trimmed, 200)
			}
		}
	}
	if ctx.skillsLine == "" {
		for _, line := range ctx.lines {
			if containsAny(line, "技能：", "技能:", "技术栈：", "技术栈:") {
				ctx.skillsLine = truncate(strings.TrimSpace(line), 200)
				break
			}
		}
	}
	ctx.keywords = selectKeywords(opts.JDText, opts.TargetRole)
	for _, kw := range ctx.keywords {
		if skills.Covered(text, kw) {
			ctx.matched = append(ctx.matched, kw)
		} else {
			ctx.missing = append(ctx.missing, kw)
		}
	}
	if len(ctx.keywords) > 0 {
		ctx.coverage = float64(len(ctx.matched)) / float64(len(ctx.keywords))
	}
	return ctx
}

var defaultGoKeywords = []string{"goroutine", "channel", "context", "MySQL", "Redis", "Linux", "HTTP"}

func selectKeywords(jd, role string) []string {
	jd = strings.TrimSpace(jd)
	seen := map[string]bool{}
	out := make([]string, 0, 8)
	add := func(s string) {
		key := strings.ToLower(s)
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, s)
	}
	if jd != "" {
		for _, item := range techVocab {
			if item.pattern.MatchString(jd) {
				add(item.canonical)
			}
		}
		extra := []string{"goroutine", "channel", "context", "Linux", "HTTP", "并发", "数据库", "缓存", "算法", "数据结构"}
		for _, kw := range extra {
			if strings.Contains(strings.ToLower(jd), strings.ToLower(kw)) {
				add(kw)
			}
		}
		if len(out) > 12 {
			out = out[:12]
		}
		return out
	}
	lowerRole := strings.ToLower(role)
	if role == "" || strings.Contains(lowerRole, "go") || strings.Contains(role, "后端") || strings.Contains(lowerRole, "backend") {
		for _, kw := range defaultGoKeywords {
			add(kw)
		}
	}
	return out
}

func screenScore(text, role, jd string, ctx *analysisContext) ScoreBreakdown {
	coverage := 100
	coverageNote := "无目标 JD/岗位关键词，按满分处理"
	if len(ctx.keywords) > 0 {
		coverage = int(math.Round(ctx.coverage * 100))
		coverageNote = fmt.Sprintf("命中 %d/%d：%s", len(ctx.matched), len(ctx.keywords), strings.Join(ctx.matched, "、"))
	}

	skills := extractTechStack(text)
	density := len(skills) * 15
	if density > 100 {
		density = 100
	}

	roleScore := 0
	roleNote := "未提供目标岗位"
	if strings.TrimSpace(role) != "" {
		roleNote = "目标岗位：" + role
		first := truncate(text, 300)
		if containsAny(first, role) || containsAny(first, "Go", "Java", "后端", "前端", "算法", "测试") {
			roleScore = 100
		} else {
			roleScore = 50
			roleNote += "（仅正文间接体现）"
		}
	}

	structure := 40
	headings := 0
	for _, word := range []string{"教育", "项目", "技能", "实习", "工作经历"} {
		if strings.Contains(text, word) {
			headings++
		}
	}
	if headings >= 3 {
		structure = 100
	} else if headings == 2 {
		structure = 70
	}
	runes := len([]rune(text))
	if runes < 400 {
		structure -= 30
	} else if runes > 6000 {
		structure -= 20
	}
	if structure < 0 {
		structure = 0
	}

	total := int(math.Round(0.4*float64(coverage) + 0.2*float64(density) + 0.2*float64(roleScore) + 0.2*float64(structure)))
	return ScoreBreakdown{Score: total, Components: []ScoreComponent{
		{Name: "JD 关键词覆盖", Score: coverage, Weight: 40, Note: coverageNote},
		{Name: "技术词密度", Score: density, Weight: 20, Note: fmt.Sprintf("识别到 %d 个技术词", len(skills))},
		{Name: "岗位匹配", Score: roleScore, Weight: 20, Note: roleNote},
		{Name: "篇幅与结构", Score: structure, Weight: 20, Note: fmt.Sprintf("%d 个常见小节，正文 %d 字", headings, runes)},
	}}
}

var (
	reVerifiable = regexp.MustCompile(`[0-9]+\s*(?:ms|毫秒|s|秒|%|QPS|条|万|倍|人|台|个)|从\s*\d|→|->|http|github|压测|埋点|测试|监控`)
	reTradeoff   = regexp.MustCompile(`取舍|权衡|降级|失败|限制|未做|未落地|只做|单机|内测|为什么|代价|兜底`)
)

func techScore(segments []*Segment, ctx *analysisContext) ScoreBreakdown {
	selfProof := 50
	if len(ctx.bullets) > 0 {
		verifiable := 0
		for _, b := range ctx.bullets {
			if reVerifiable.MatchString(b.Text) {
				verifiable++
			}
		}
		selfProof = int(math.Round(float64(verifiable) / float64(len(ctx.bullets)) * 100))
	}

	metricTotal, metricVerified := 0, 0
	for _, seg := range segments {
		for _, line := range seg.Lines {
			if rePerfToken.MatchString(line) && rePerfChange.MatchString(line) && regexp.MustCompile(`[0-9]`).MatchString(line) {
				metricTotal++
				if reBaseline.MatchString(line) && reEnv.MatchString(line) {
					metricVerified++
				}
			}
		}
	}
	metricScore := 50
	if metricTotal > 0 {
		metricScore = int(math.Round(float64(metricVerified) / float64(metricTotal) * 100))
	}

	known := 0
	boundary := 0
	tradeoff := 0
	for _, seg := range segments {
		if seg.Type == TypeUnknown {
			continue
		}
		known++
		if reBoundary.MatchString(seg.Raw) || seg.TeamSize > 0 {
			boundary++
		}
		if reTradeoff.MatchString(seg.Raw) {
			tradeoff++
		}
	}
	boundaryScore, tradeoffScore := 50, 50
	if known > 0 {
		boundaryScore = int(math.Round(float64(boundary) / float64(known) * 100))
		tradeoffScore = int(math.Round(float64(tradeoff) / float64(known) * 100))
	}

	total := int(math.Round(0.4*float64(selfProof) + 0.25*float64(metricScore) + 0.2*float64(boundaryScore) + 0.15*float64(tradeoffScore)))
	return ScoreBreakdown{Score: total, Components: []ScoreComponent{
		{Name: "主张可自证", Score: selfProof, Weight: 40, Note: fmt.Sprintf("%d 条量化/可核验描述 / %d 条目", boundaryCount(ctx), len(ctx.bullets))},
		{Name: "指标有基线", Score: metricScore, Weight: 25, Note: fmt.Sprintf("%d/%d 条性能指标同时给出基线与环境", metricVerified, metricTotal)},
		{Name: "职责边界", Score: boundaryScore, Weight: 20, Note: fmt.Sprintf("%d/%d 段写明团队或模块边界", boundary, known)},
		{Name: "三层追问韧性", Score: tradeoffScore, Weight: 15, Note: fmt.Sprintf("%d/%d 段提到取舍、限制或未落地", tradeoff, known)},
	}}
}

func boundaryCount(ctx *analysisContext) int {
	n := 0
	for _, b := range ctx.bullets {
		if reVerifiable.MatchString(b.Text) {
			n++
		}
	}
	return n
}

func conflictAdvice(screen, tech int) string {
	if screen-tech >= 15 {
		return "初筛友好但证据偏弱：关键词齐，二面追问容易被打穿；优先补基线、环境与个人边界。"
	}
	if tech-screen >= 15 {
		return "证据扎实但关键词覆盖不足：可能被 HR 初筛过滤；按 JD 前三关键词补技能栏与项目描述。"
	}
	return "两项分数接近，未发现明显的「初筛友好 / 面试脆弱」错位。"
}
