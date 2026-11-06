// Package resume holds the structured candidate profile used by question
// generation and evaluation prompts. M3 deliberately starts with structured
// fields instead of PDF parsing: years, skills and projects.
package resume

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Project is one resume project entry. Highlights are the concrete claims the
// interviewer may ask about.
type Project struct {
	Name       string   `json:"name"`
	Role       string   `json:"role"`
	TechStack  []string `json:"tech_stack"`
	Highlights []string `json:"highlights"`
}

// Profile is the model-facing view of a resume.
type Profile struct {
	Years    int       `json:"years"`
	Skills   []string  `json:"skills"`
	Projects []Project `json:"projects"`
	Summary  string    `json:"summary"`
}

var (
	yearsPattern = regexp.MustCompile(`(?i)(\d{1,2}|[一二三四五六七八九十]{1,3})\s*年`)
	skillAliases = []struct {
		canonical string
		patterns  []*regexp.Regexp
	}{
		{"Go", []*regexp.Regexp{regexp.MustCompile(`(?i)\bgolang\b`), regexp.MustCompile(`(?i)\bgo\b`), regexp.MustCompile(`(?i)go\s*语言`), regexp.MustCompile(`(?i)golang`)}},
		{"MySQL", []*regexp.Regexp{regexp.MustCompile(`(?i)\bmysql\b`), regexp.MustCompile(`(?i)\bmariadb\b`)}},
		{"PostgreSQL", []*regexp.Regexp{regexp.MustCompile(`(?i)\bpostgres(?:ql)?\b`), regexp.MustCompile(`(?i)\bpg\b`)}},
		{"Redis", []*regexp.Regexp{regexp.MustCompile(`(?i)\bredis\b`)}},
		{"Kafka", []*regexp.Regexp{regexp.MustCompile(`(?i)\bkafka\b`)}},
		{"RabbitMQ", []*regexp.Regexp{regexp.MustCompile(`(?i)\brabbitmq\b`)}},
		{"gRPC", []*regexp.Regexp{regexp.MustCompile(`(?i)\bgrpc\b`)}},
		{"Docker", []*regexp.Regexp{regexp.MustCompile(`(?i)\bdocker\b`)}},
		{"Kubernetes", []*regexp.Regexp{regexp.MustCompile(`(?i)\bkubernetes\b`), regexp.MustCompile(`(?i)\bk8s\b`)}},
		{"Java", []*regexp.Regexp{regexp.MustCompile(`(?i)\bjava\b`)}},
		{"Python", []*regexp.Regexp{regexp.MustCompile(`(?i)\bpython\b`)}},
		{"Rust", []*regexp.Regexp{regexp.MustCompile(`(?i)\brust\b`)}},
		{"C++", []*regexp.Regexp{regexp.MustCompile(`(?i)\bc\+\+`)}},
		{"Elasticsearch", []*regexp.Regexp{regexp.MustCompile(`(?i)\belasticsearch\b`), regexp.MustCompile(`(?i)\bes\b`)}},
		{"Prometheus", []*regexp.Regexp{regexp.MustCompile(`(?i)\bprometheus\b`)}},
	}
)

// Normalize trims and caps the profile so it can be persisted and rendered
// safely. It is idempotent.
func (p *Profile) Normalize() {
	if p.Years < 0 {
		p.Years = 0
	}
	if p.Years > 60 {
		p.Years = 60
	}
	p.Summary = strings.TrimSpace(p.Summary)
	if r := []rune(p.Summary); len(r) > 300 {
		p.Summary = string(r[:300])
	}

	p.Skills = dedupeStrings(p.Skills)
	if len(p.Skills) > 30 {
		p.Skills = p.Skills[:30]
	}

	projects := make([]Project, 0, len(p.Projects))
	for _, project := range p.Projects {
		project.Name = strings.TrimSpace(project.Name)
		project.Role = strings.TrimSpace(project.Role)
		project.TechStack = dedupeStrings(project.TechStack)
		highlights := make([]string, 0, len(project.Highlights))
		for _, h := range project.Highlights {
			if h = strings.TrimSpace(h); h != "" {
				highlights = append(highlights, truncate(h, 200))
			}
		}
		if len(highlights) > 6 {
			highlights = highlights[:6]
		}
		project.Highlights = highlights
		if project.Name == "" && len(highlights) == 0 && len(project.TechStack) == 0 {
			continue
		}
		projects = append(projects, project)
	}
	if len(projects) > 10 {
		projects = projects[:10]
	}
	p.Projects = projects
}

// IsZero reports whether the profile carries no usable information.
func (p Profile) IsZero() bool {
	return p.Years == 0 && len(p.Skills) == 0 && len(p.Projects) == 0 && strings.TrimSpace(p.Summary) == ""
}

// Text renders the profile for prompts. It is compact and stable so the same
// session always produces the same prompt prefix.
func (p Profile) Text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "工作年限：%d 年\n", p.Years)
	if len(p.Skills) > 0 {
		fmt.Fprintf(&b, "技能：%s\n", strings.Join(p.Skills, ", "))
	}
	if p.Summary != "" {
		fmt.Fprintf(&b, "简介：%s\n", p.Summary)
	}
	if len(p.Projects) > 0 {
		b.WriteString("项目：\n")
		for i, project := range p.Projects {
			name := project.Name
			if name == "" {
				name = fmt.Sprintf("项目 %d", i+1)
			}
			line := "- " + name
			if project.Role != "" {
				line += "（" + project.Role + "）"
			}
			if len(project.TechStack) > 0 {
				line += " 技术栈：" + strings.Join(project.TechStack, "/")
			}
			b.WriteString(line + "\n")
			for _, h := range project.Highlights {
				b.WriteString("  - " + h + "\n")
			}
		}
	}
	return strings.TrimSpace(b.String())
}

// Parse derives a structured profile from free-form resume text. It recognises
// "N 年" years, a fixed skill vocabulary and lines that look like projects. It
// is intentionally conservative: unknown formats simply yield fewer fields.
func Parse(text string) Profile {
	text = strings.TrimSpace(text)
	var profile Profile
	if text == "" {
		return profile
	}

	if matches := yearsPattern.FindStringSubmatch(text); len(matches) == 2 {
		profile.Years = parseYears(matches[1])
	}

	for _, alias := range skillAliases {
		for _, pattern := range alias.patterns {
			if pattern.MatchString(text) {
				profile.Skills = append(profile.Skills, alias.canonical)
				break
			}
		}
	}
	if len(profile.Skills) == 0 {
		for _, line := range strings.Split(text, "\n") {
			if strings.Contains(strings.ToLower(line), "技能") {
				for _, part := range splitList(line) {
					if len([]rune(part)) <= 24 {
						profile.Skills = append(profile.Skills, part)
					}
				}
			}
		}
	}

	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.Contains(line, "项目") {
			continue
		}
		project := Project{Name: truncate(strings.Trim(line, "-•· "), 60)}
		for _, alias := range skillAliases {
			for _, pattern := range alias.patterns {
				if pattern.MatchString(line) {
					project.TechStack = append(project.TechStack, alias.canonical)
					break
				}
			}
		}
		project.Highlights = []string{truncate(line, 200)}
		profile.Projects = append(profile.Projects, project)
	}

	firstLine := strings.Split(strings.TrimSpace(text), "\n")[0]
	profile.Summary = truncate(strings.TrimSpace(firstLine), 150)
	profile.Normalize()
	return profile
}

func dedupeStrings(in []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		key := strings.ToLower(s)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, s)
	}
	return out
}

func splitList(line string) []string {
	line = strings.ReplaceAll(line, "：", ":")
	line = strings.ReplaceAll(line, "、", ",")
	line = strings.ReplaceAll(line, "；", ",")
	line = strings.ReplaceAll(line, ";", ",")
	if i := strings.Index(line, ":"); i >= 0 {
		line = line[i+1:]
	}
	parts := strings.Split(line, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}

func parseYears(raw string) int {
	if n, err := strconv.Atoi(raw); err == nil {
		return n
	}
	digits := map[rune]int{'一': 1, '二': 2, '三': 3, '四': 4, '五': 5, '六': 6, '七': 7, '八': 8, '九': 9}
	runes := []rune(raw)
	switch {
	case len(runes) == 1 && runes[0] == '十':
		return 10
	case len(runes) == 2 && runes[0] == '十':
		if n, ok := digits[runes[1]]; ok {
			return 10 + n
		}
	case len(runes) == 2 && runes[1] == '十':
		if n, ok := digits[runes[0]]; ok {
			return n * 10
		}
	case len(runes) == 3 && runes[1] == '十':
		first, ok1 := digits[runes[0]]
		second, ok2 := digits[runes[2]]
		if ok1 && ok2 {
			return first*10 + second
		}
	case len(runes) == 1:
		if n, ok := digits[runes[0]]; ok {
			return n
		}
	}
	return 0
}
