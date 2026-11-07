// Package jd turns a pasted or fetched job description into a deterministic
// structured listing. It never calls a model: parsing, matching and outline
// generation are rule-based so results are reproducible and auditable.
package jd

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Source kinds recorded with every listing.
const (
	SourcePaste = "paste"
	SourceURL   = "url"
)

// Structured is the machine-readable view of one JD.
type Structured struct {
	Role           string   `json:"role"`
	Company        string   `json:"company,omitempty"`
	MinYears       int      `json:"min_years"`
	MaxYears       int      `json:"max_years,omitempty"`
	RequiredSkills []string `json:"required_skills"`
	NiceToHave     []string `json:"nice_to_have"`
	Keywords       []string `json:"keywords"`
}

// Listing is a parsed JD plus its provenance. It is persisted so GET can serve
// the same result later with the original fetched_at and source URL.
type Listing struct {
	ID         string     `json:"id"`
	UserID     string     `json:"-"`
	SourceKind string     `json:"source_kind"`
	SourceURL  string     `json:"source_url,omitempty"`
	FetchedAt  time.Time  `json:"fetched_at"`
	RawText    string     `json:"raw_text"`
	Structured Structured `json:"structured"`
	CreatedAt  time.Time  `json:"created_at"`
}

type skillRule struct {
	canonical string
	pattern   *regexp.Regexp
}

// skillVocab is deliberately finite and stable. Unknown terms simply do not
// become requirements instead of being guessed.
var skillVocab = []skillRule{
	{"Go", regexp.MustCompile(`(?i)\bgolang\b|\bgo\s*语言|\bgo\b`)},
	{"Java", regexp.MustCompile(`(?i)\bjava\b`)},
	{"Python", regexp.MustCompile(`(?i)\bpython\b`)},
	{"C++", regexp.MustCompile(`(?i)\bc\+\+`)},
	{"Rust", regexp.MustCompile(`(?i)\brust\b`)},
	{"MySQL", regexp.MustCompile(`(?i)\bmysql\b|\bmariadb\b`)},
	{"PostgreSQL", regexp.MustCompile(`(?i)\bpostgres(?:ql)?\b`)},
	{"Redis", regexp.MustCompile(`(?i)\bredis\b`)},
	{"Kafka", regexp.MustCompile(`(?i)\bkafka\b`)},
	{"RabbitMQ", regexp.MustCompile(`(?i)\brabbitmq\b`)},
	{"gRPC", regexp.MustCompile(`(?i)\bgrpc\b`)},
	{"Docker", regexp.MustCompile(`(?i)\bdocker\b`)},
	{"Kubernetes", regexp.MustCompile(`(?i)\bkubernetes\b|\bk8s\b`)},
	{"Linux", regexp.MustCompile(`(?i)\blinux\b`)},
	{"Elasticsearch", regexp.MustCompile(`(?i)\belasticsearch\b`)},
	{"Prometheus", regexp.MustCompile(`(?i)\bprometheus\b`)},
	{"LLM", regexp.MustCompile(`(?i)\bllm\b|大模型`)},
	{"RAG", regexp.MustCompile(`(?i)\brag\b|检索增强`)},
	{"Agent", regexp.MustCompile(`(?i)\bagent\b|智能体`)},
	{"LangChain", regexp.MustCompile(`(?i)\blangchain\b`)},
	{"向量数据库", regexp.MustCompile(`(?i)向量数据库|\bmilvus\b|\bqdrant\b|\bweaviate\b|\bchroma\b`)},
	{"PyTorch", regexp.MustCompile(`(?i)\bpytorch\b`)},
	{"TensorFlow", regexp.MustCompile(`(?i)\btensorflow\b`)},
	{"Spring", regexp.MustCompile(`(?i)\bspring\b`)},
	{"JVM", regexp.MustCompile(`(?i)\bjvm\b`)},
	{"TypeScript", regexp.MustCompile(`(?i)\btypescript\b`)},
	{"React", regexp.MustCompile(`(?i)\breact\b`)},
	{"Vue", regexp.MustCompile(`(?i)\bvue\b`)},
	{"分布式", regexp.MustCompile(`分布式`)},
	{"高并发", regexp.MustCompile(`高并发|高吞吐`)},
	{"微服务", regexp.MustCompile(`微服务`)},
	{"消息队列", regexp.MustCompile(`消息队列|\bmq\b`)},
	{"可观测性", regexp.MustCompile(`可观测性|\bobservability\b|链路追踪`)},
	{"性能优化", regexp.MustCompile(`性能优化|性能调优`)},
	{"CI/CD", regexp.MustCompile(`ci/cd|\bci\b|\bcd\b`)},
}

var (
	yearRangePattern = regexp.MustCompile(`(\d{1,2})\s*(?:-|~|至|到)\s*(\d{1,2})\s*年`)
	yearAbovePattern = regexp.MustCompile(`(\d{1,2})\s*年以上`)
	yearPattern      = regexp.MustCompile(`(\d{1,2})\s*年`)
	roleLinePattern  = regexp.MustCompile(`(?i)(?:岗位名称|职位名称|岗位|职位)\s*[：:]\s*([^，,。;；\n]{2,40})`)
	companyPattern   = regexp.MustCompile(`(?i)(?:公司名称|所属公司|公司)\s*[：:]\s*([^，,。;；\n]{2,40})`)
	roleWords        = []string{"工程师", "开发", "架构师", "研究员", "实习", "专家", "经理", "负责人", "developer", "engineer"}
	niceWords        = []string{"加分", "优先", "更佳", "bonus", "plus", "preferred"}
	niceHeadings     = []string{"加分项", "加分", "优先条件", "nice to have"}
	requiredHeading  = []string{"任职要求", "岗位要求", "任职资格", "职位要求", "要求", "我们希望你", "你将负责", "岗位职责", "工作职责", "职位描述"}
)

// Parse extracts role, years, required/nice-to-have skills and keywords from
// free text. Sections are detected by heading keywords; anything under a
// "加分/优先" heading is classified as nice-to-have.
func Parse(text string) Structured {
	text = normalize(text)
	var out Structured

	if m := roleLinePattern.FindStringSubmatch(text); len(m) == 2 {
		out.Role = strings.TrimSpace(m[1])
	}
	if m := companyPattern.FindStringSubmatch(text); len(m) == 2 {
		out.Company = strings.TrimSpace(m[1])
	}
	if out.Role == "" {
		out.Role = firstRoleLine(text)
	}

	out.MinYears, out.MaxYears = parseYears(text)

	required := map[string]bool{}
	nice := map[string]bool{}
	currentNice := false
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if isHeading(line) {
			currentNice = containsAny(strings.ToLower(line), niceHeadings)
			continue
		}
		lineNice := currentNice || containsAny(strings.ToLower(line), niceWords)
		for _, rule := range skillVocab {
			if !rule.pattern.MatchString(line) {
				continue
			}
			if lineNice {
				nice[rule.canonical] = true
			} else {
				required[rule.canonical] = true
			}
		}
	}
	// A skill listed as required is not repeated under nice-to-have.
	for skill := range required {
		delete(nice, skill)
	}
	out.RequiredSkills = sortedKeys(required)
	out.NiceToHave = sortedKeys(nice)
	out.Keywords = unionSkills(out.RequiredSkills, out.NiceToHave)
	return out
}

func normalize(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	return strings.TrimSpace(text)
}

func parseYears(text string) (int, int) {
	if m := yearRangePattern.FindStringSubmatch(text); len(m) == 3 {
		min, _ := strconv.Atoi(m[1])
		max, _ := strconv.Atoi(m[2])
		if max >= min {
			return min, max
		}
		return min, 0
	}
	if m := yearAbovePattern.FindStringSubmatch(text); len(m) == 2 {
		min, _ := strconv.Atoi(m[1])
		return min, 0
	}
	if m := yearPattern.FindStringSubmatch(text); len(m) == 2 {
		min, _ := strconv.Atoi(m[1])
		return min, 0
	}
	return 0, 0
}

func isHeading(line string) bool {
	if len([]rune(line)) > 24 {
		return false
	}
	lower := strings.ToLower(line)
	for _, word := range requiredHeading {
		if strings.Contains(lower, word) {
			return true
		}
	}
	return containsAny(lower, niceHeadings)
}

func firstRoleLine(text string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		r := []rune(line)
		if len(r) < 2 || len(r) > 40 {
			continue
		}
		lower := strings.ToLower(line)
		for _, word := range roleWords {
			if strings.Contains(lower, strings.ToLower(word)) {
				return line
			}
		}
	}
	return ""
}

// DifficultyFromYears maps an experience requirement onto the pack difficulty
// scale. An explicit user value always wins at the call site.
func DifficultyFromYears(years int) string {
	switch {
	case years <= 1:
		return "junior"
	case years <= 4:
		return "mid"
	default:
		return "senior"
	}
}

func sortedKeys(set map[string]bool) []string {
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func unionSkills(a, b []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(a)+len(b))
	for _, s := range append(append([]string(nil), a...), b...) {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func containsAny(haystack string, needles []string) bool {
	for _, needle := range needles {
		if strings.Contains(haystack, strings.ToLower(needle)) {
			return true
		}
	}
	return false
}
