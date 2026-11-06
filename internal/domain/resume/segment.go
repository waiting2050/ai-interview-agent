package resume

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// DateSpan is the parsed start/end of one resume entry.
type DateSpan struct {
	StartYear  int
	StartMonth int
	EndYear    int
	EndMonth   int
	Ongoing    bool
	Raw        string
}

func monthIndex(year, month int) int { return year*12 + (month - 1) }

// StartIndex is a monotonic month number used for overlap and gap checks.
func (d DateSpan) StartIndex() int { return monthIndex(d.StartYear, d.StartMonth) }

// EndIndex is inclusive. Ongoing spans use the start month because the
// analysis must be deterministic and cannot depend on today's date.
func (d DateSpan) EndIndex() int {
	if d.Ongoing || d.EndYear == 0 {
		return d.StartIndex()
	}
	return monthIndex(d.EndYear, d.EndMonth)
}

// Months is the inclusive duration. An ongoing entry counts as one month; the
// rules only use it for short-period checks, where a single month is the
// conservative reading.
func (d DateSpan) Months() int {
	if d.Ongoing || d.EndYear == 0 {
		return 1
	}
	diff := d.EndIndex() - d.StartIndex() + 1
	if diff < 1 {
		return 1
	}
	return diff
}

// Segment is one experience block on a resume. Type is assigned by headings and
// keywords; anything ambiguous stays unknown and is returned for confirmation.
type Segment struct {
	Index          int           `json:"index"`
	Title          string        `json:"title"`
	Type           SegmentType   `json:"type"`
	TypeSource     string        `json:"type_source"`
	Raw            string        `json:"raw"`
	Lines          []string      `json:"-"`
	Span           *DateSpan     `json:"-"`
	Months         int           `json:"months"`
	TechStack      []string      `json:"tech_stack"`
	TechCount      int           `json:"tech_count"`
	TeamSize       int           `json:"team_size"`
	Claims         []string      `json:"claims"`
	HonestLabels   []string      `json:"honest_labels"`
	Section        SegmentType   `json:"section,omitempty"`
	SuggestedTypes []SegmentType `json:"suggested_types"`
	NeedsConfirm   bool          `json:"needs_confirm"`
}

var (
	reFullSpan = regexp.MustCompile(`(20\d{2})\s*[.\-/年]\s*(\d{1,2})\s*月?\s*[-–—~至]\s*(20\d{2})\s*[.\-/年]\s*(\d{1,2})\s*月?`)
	reOpenSpan = regexp.MustCompile(`(20\d{2})\s*[.\-/年]\s*(\d{1,2})\s*月?\s*[-–—~至]\s*(?:至今|今|现在|present|Present)`)
	reTeamSize = regexp.MustCompile(`团队\s*(\d+)\s*人|(\d+)\s*人团队|(\d+)\s*名同学`)
)

var educationLineWords = []string{"教育", "大学", "学院", "本科", "硕士", "博士", "专科", "毕业", "就读", "学历"}

var sectionHeadingWords = []string{"项目经历", "工作经历", "实习经历", "教育经历", "校园经历", "荣誉奖项", "技能", "自我评价", "个人信息"}

func parseSpan(line string) (DateSpan, bool) {
	if m := reFullSpan.FindStringSubmatch(line); len(m) == 5 {
		sy, _ := strconv.Atoi(m[1])
		sm, _ := strconv.Atoi(m[2])
		ey, _ := strconv.Atoi(m[3])
		em, _ := strconv.Atoi(m[4])
		if sm < 1 || sm > 12 {
			sm = 1
		}
		if em < 1 || em > 12 {
			em = 12
		}
		return DateSpan{StartYear: sy, StartMonth: sm, EndYear: ey, EndMonth: em, Raw: m[0]}, true
	}
	if m := reOpenSpan.FindStringSubmatch(line); len(m) == 3 {
		sy, _ := strconv.Atoi(m[1])
		sm, _ := strconv.Atoi(m[2])
		if sm < 1 || sm > 12 {
			sm = 1
		}
		return DateSpan{StartYear: sy, StartMonth: sm, Ongoing: true, Raw: m[0]}, true
	}
	return DateSpan{}, false
}

func isEducationLine(line string) bool {
	for _, word := range educationLineWords {
		if strings.Contains(line, word) {
			return true
		}
	}
	return false
}

func isSectionHeading(line string) bool {
	line = strings.TrimSpace(line)
	if len([]rune(line)) > 12 {
		return false
	}
	for _, word := range sectionHeadingWords {
		if strings.Contains(line, word) {
			return true
		}
	}
	return false
}

// sectionTypeFor turns a section heading into the type of the entries below it.
// Headings are authoritative: a project listed under 项目经历 must not become
// an internship just because the word 实习 appears elsewhere in the resume.
func sectionTypeFor(line string) SegmentType {
	switch {
	case strings.Contains(line, "实习") || strings.Contains(line, "工作"):
		return TypeInternship
	case strings.Contains(line, "项目"):
		return TypePersonalProject
	default:
		return TypeUnknown
	}
}

// parseSegments splits the resume into dated experience entries. When no dated
// entry exists it falls back to blank-line blocks, and finally to the whole
// resume as one unknown block. It never invents a type.
func parseSegments(text string, overrides map[int]SegmentType) []*Segment {
	lines := strings.Split(text, "\n")
	segments := make([]*Segment, 0, 6)
	var current *Segment
	sectionType := TypeUnknown
	pending := make([]string, 0, 4)

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			if current != nil {
				current.Lines = append(current.Lines, "")
			} else {
				pending = append(pending, "")
			}
			continue
		}
		if isSectionHeading(trimmed) {
			sectionType = sectionTypeFor(trimmed)
			current = nil
			pending = append(pending[:0], trimmed)
			continue
		}
		if span, ok := parseSpan(trimmed); ok && !isEducationLine(trimmed) {
			title := trimmed
			dateAt := strings.Index(trimmed, span.Raw)
			if dateAt >= 0 && dateAt < 8 && len(pending) > 0 {
				prev := strings.TrimSpace(pending[len(pending)-1])
				if prev != "" && !isSectionHeading(prev) && len([]rune(prev)) <= 60 {
					title = prev + " " + trimmed
					pending = pending[:len(pending)-1]
				}
			}
			dateCopy := span
			current = &Segment{Title: title, Span: &dateCopy, Type: TypeUnknown, TypeSource: "none", Section: sectionType}
			segments = append(segments, current)
			continue
		}
		if current == nil {
			pending = append(pending, trimmed)
			continue
		}
		current.Lines = append(current.Lines, trimmed)
	}

	if len(segments) == 0 {
		// Fall back to blank-line blocks so pasted resumes without dates are
		// still segmented, but they remain unknown until the user confirms.
		for _, block := range splitBlocks(lines) {
			if len([]rune(block)) < 24 {
				continue
			}
			segments = append(segments, &Segment{
				Title:      firstLine(block),
				Raw:        block,
				Lines:      strings.Split(block, "\n"),
				Type:       TypeUnknown,
				TypeSource: "none",
			})
		}
	}
	if len(segments) == 0 {
		trimmed := strings.TrimSpace(text)
		if trimmed != "" {
			segments = append(segments, &Segment{
				Title:      firstLine(trimmed),
				Raw:        trimmed,
				Lines:      strings.Split(trimmed, "\n"),
				Type:       TypeUnknown,
				TypeSource: "none",
			})
		}
	}

	for i, seg := range segments {
		seg.Index = i
		if seg.Raw == "" {
			seg.Raw = strings.TrimSpace(strings.Join(seg.Lines, "\n"))
		}
		if seg.Title == "" {
			seg.Title = firstLine(seg.Raw)
		}
		if seg.Span == nil {
			// Dates may be inside the body when the title line was not the
			// dated one.
			if span, ok := parseSpan(seg.Raw); ok {
				seg.Span = &span
			}
		}
		if seg.Span != nil {
			seg.Months = seg.Span.Months()
		}
		seg.TechStack = extractTechStack(seg.Raw)
		seg.TechCount = countNonLanguageTech(seg.TechStack)
		seg.TeamSize = parseTeamSize(seg.Raw)
		seg.Claims = detectClaims(seg.Raw)
		seg.HonestLabels = detectHonestLabels(seg.Raw)
		seg.Type, seg.SuggestedTypes = classifySegment(seg.Title, seg.Raw, seg.Section)
		seg.TypeSource = "keyword"
		if seg.Section != TypeUnknown {
			seg.TypeSource = "section"
		}
		if t, ok := overrides[i]; ok {
			seg.Type = t
			seg.TypeSource = "user"
			seg.NeedsConfirm = false
			seg.SuggestedTypes = nil
			continue
		}
		seg.NeedsConfirm = seg.Type == TypeUnknown
	}
	return segments
}

func splitBlocks(lines []string) []string {
	blocks := make([]string, 0, 6)
	var b strings.Builder
	flush := func() {
		block := strings.TrimSpace(b.String())
		if block != "" {
			blocks = append(blocks, block)
		}
		b.Reset()
	}
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			flush()
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(strings.TrimSpace(line))
	}
	flush()
	return blocks
}

func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return truncate(trimmed, 80)
		}
	}
	return ""
}

type typeKeyword struct {
	t    SegmentType
	word string
}

// Order matters only for tie-breaking suggestions; scores decide the type.
var segmentKeywords = []typeKeyword{
	{TypeInternship, "实习"},
	{TypeInternship, "入职"},
	{TypeInternship, "在职"},
	{TypeInternship, "转正"},
	{TypeInternship, "有限公司"},
	{TypeInternship, "科技有限公司"},
	{TypeInternship, "集团"},
	{TypeInternship, "公司"},
	{TypeInternship, "部门"},
	{TypeInternship, "工作经历"},
	{TypePersonalProject, "个人项目"},
	{TypePersonalProject, "个人作品"},
	{TypePersonalProject, "独立开发"},
	{TypePersonalProject, "side project"},
	{TypePersonalProject, "自研"},
	{TypePersonalProject, "个人博客"},
	{TypeOpenSource, "开源"},
	{TypeOpenSource, "贡献"},
	{TypeOpenSource, "pull request"},
	{TypeOpenSource, "社区"},
	{TypeOpenSource, "issue"},
	{TypeOpenSource, "commit"},
	{TypeCompetition, "竞赛"},
	{TypeCompetition, "比赛"},
	{TypeCompetition, "大赛"},
	{TypeCompetition, "参赛"},
	{TypeCompetition, "获奖"},
	{TypeCompetition, "冠军"},
	{TypeCompetition, "亚军"},
	{TypeCompetition, "acm"},
	{TypeCompetition, "kaggle"},
	{TypeCoursework, "课程设计"},
	{TypeCoursework, "课程项目"},
	{TypeCoursework, "课设"},
	{TypeCoursework, "大作业"},
	{TypeCoursework, "毕设"},
	{TypeCoursework, "毕业设计"},
	{TypeCoursework, "课程"},
	{TypeCoursework, "作业"},
	{TypeCoursework, "实验课"},
}

// classifySegment scores keyword evidence separately for the title and the
// body. A tie means the text is ambiguous, so it stays unknown rather than
// silently picking one rubric.
func classifySegment(title, body string, section SegmentType) (SegmentType, []SegmentType) {
	lowerTitle := strings.ToLower(title)
	lowerBody := strings.ToLower(body)
	scores := map[SegmentType]int{}
	for _, kw := range segmentKeywords {
		if strings.Contains(lowerTitle, kw.word) {
			scores[kw.t] += 2
		}
		if strings.Contains(lowerBody, kw.word) {
			scores[kw.t]++
		}
	}
	if strings.Contains(lowerTitle, "项目") && !strings.Contains(lowerTitle, "项目经历") {
		scores[TypePersonalProject]++
	}

	best, bestScore, second, secondScore := TypeUnknown, 0, TypeUnknown, 0
	for _, t := range segmentTypeOrder {
		if t == TypeUnknown {
			continue
		}
		score := scores[t]
		if score > bestScore {
			best, second, secondScore = t, best, bestScore
			bestScore = score
			continue
		}
		if score > secondScore {
			second, secondScore = t, score
		}
	}
	if section == TypeInternship {
		return TypeInternship, nil
	}
	if section == TypePersonalProject {
		// A course/competition/open-source entry may sit under 项目经历; the
		// specific label wins when it is named at least in the title.
		switch best {
		case TypeOpenSource, TypeCompetition, TypeCoursework:
			if bestScore >= 2 {
				return best, nil
			}
		}
		return TypePersonalProject, nil
	}
	if bestScore == 0 {
		return TypeUnknown, nil
	}
	if secondScore == bestScore {
		return TypeUnknown, []SegmentType{best, second}
	}
	return best, nil
}

var techVocab = []struct {
	canonical string
	pattern   *regexp.Regexp
	language  bool
}{
	{"Kubernetes", regexp.MustCompile(`(?i)\bkubernetes\b|\bk8s\b`), false},
	{"Istio", regexp.MustCompile(`(?i)\bistio\b`), false},
	{"Kafka", regexp.MustCompile(`(?i)\bkafka\b`), false},
	{"RabbitMQ", regexp.MustCompile(`(?i)\brabbitmq\b`), false},
	{"Elasticsearch", regexp.MustCompile(`(?i)\belasticsearch\b|\bes\b`), false},
	{"MongoDB", regexp.MustCompile(`(?i)\bmongo(db)?\b`), false},
	{"Redis", regexp.MustCompile(`(?i)\bredis\b`), false},
	{"MySQL", regexp.MustCompile(`(?i)\bmysql\b|\bmariadb\b`), false},
	{"PostgreSQL", regexp.MustCompile(`(?i)\bpostgres(?:ql)?\b`), false},
	{"Nginx", regexp.MustCompile(`(?i)\bnginx\b`), false},
	{"ZooKeeper", regexp.MustCompile(`(?i)\bzookeeper\b`), false},
	{"Docker", regexp.MustCompile(`(?i)\bdocker\b`), false},
	{"gRPC", regexp.MustCompile(`(?i)\bgrpc\b`), false},
	{"Prometheus", regexp.MustCompile(`(?i)\bprometheus\b`), false},
	{"Grafana", regexp.MustCompile(`(?i)\bgrafana\b`), false},
	{"Spring", regexp.MustCompile(`(?i)\bspring(?:\s*cloud|\s*boot)?\b`), false},
	{"Nacos", regexp.MustCompile(`(?i)\bnacos\b`), false},
	{"Sentinel", regexp.MustCompile(`(?i)\bsentinel\b`), false},
	{"Seata", regexp.MustCompile(`(?i)\bseata\b`), false},
	{"Go", regexp.MustCompile(`(?i)\bgolang\b|\bgo\s*语言\b`), true},
	{"Java", regexp.MustCompile(`(?i)\bjava\b`), true},
	{"Python", regexp.MustCompile(`(?i)\bpython\b`), true},
	{"Rust", regexp.MustCompile(`(?i)\brust\b`), true},
	{"C++", regexp.MustCompile(`(?i)\bc\+\+`), true},
}

func extractTechStack(text string) []string {
	out := make([]string, 0, 8)
	for _, item := range techVocab {
		if item.pattern.MatchString(text) {
			out = append(out, item.canonical)
		}
	}
	return out
}

func countNonLanguageTech(stack []string) int {
	count := 0
	for _, name := range stack {
		if isLanguage(name) {
			continue
		}
		count++
	}
	return count
}

func isLanguage(name string) bool {
	for _, item := range techVocab {
		if item.canonical == name {
			return item.language
		}
	}
	return false
}

func parseTeamSize(text string) int {
	m := reTeamSize.FindStringSubmatch(text)
	if len(m) == 0 {
		return 0
	}
	for i := 1; i < len(m); i++ {
		if m[i] == "" {
			continue
		}
		if n, err := strconv.Atoi(m[i]); err == nil {
			return n
		}
	}
	return 0
}

var claimWords = []string{
	"分布式", "高并发", "微服务", "多租户", "容灾", "集群", "高可用",
	"99.99", "百万用户", "千万用户", "日均千万", "日均百万", "十万 QPS",
	"从0到1", "从 0 到 1", "主导", "架构师", "技术负责人",
}

func detectClaims(text string) []string {
	out := make([]string, 0, 4)
	for _, word := range claimWords {
		if strings.Contains(text, word) {
			out = append(out, word)
		}
	}
	return out
}

var honestLabelWords = []string{
	"单机部署", "单机", "小规模内测", "内测", "方案设计未落地", "未落地",
	"仅方案", "未对外", "未公网", "本地压测", "未统计", "未做多副本",
	"测试机", "未上线", "课程了解", "规划中",
}

func detectHonestLabels(text string) []string {
	out := make([]string, 0, 4)
	for _, word := range honestLabelWords {
		if strings.Contains(text, word) {
			out = append(out, word)
		}
	}
	return out
}

func sortedSegmentsByStart(segments []*Segment) []*Segment {
	out := make([]*Segment, 0, len(segments))
	for _, seg := range segments {
		if seg.Span != nil {
			out = append(out, seg)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		si, sj := out[i].Span.StartIndex(), out[j].Span.StartIndex()
		if si == sj {
			return out[i].Index < out[j].Index
		}
		return si < sj
	})
	return out
}
