// Package skills provides deterministic, explainable skill-name normalization
// and matching. The vocabulary is a small in-code alias table instead of an
// embedding index: it must be auditable, work without a model or API key, and
// keep every match explainable ("matched k8s -> Kubernetes").
package skills

import (
	"regexp"
	"strings"
	"unicode"
)

// Match statuses exposed by the API and rendered by the demo.
const (
	StatusCovered = "covered"
	StatusWeak    = "weak"
	StatusMissing = "missing"
)

// Match sources explain why a skill was (or was not) counted.
const (
	SourceExact   = "exact"
	SourceAlias   = "alias"
	SourceChild   = "child"
	SourceParent  = "parent"
	SourceSibling = "sibling"
	SourceRelated = "related"
	SourceFuzzy   = "fuzzy"
)

// Result is one requirement-versus-resume verdict.
type Result struct {
	// Skill is the JD requirement as it was written (display name).
	Skill string `json:"skill"`
	// Status is covered (counts), weak (hint only, never counts) or missing.
	Status string `json:"status"`
	// Matched is the canonical skill that actually hit, when any. It lets the
	// UI explain a weak match by name.
	Matched string `json:"matched,omitempty"`
	// Source is exact | alias | child | parent | sibling | related | fuzzy.
	Source string `json:"source,omitempty"`
}

// Covered reports whether this check counts toward keyword coverage.
func (c Result) Covered() bool { return c.Status == StatusCovered }

type entry struct {
	Canonical string
	Aliases   []string
	// Parent names a broader skill. A parent hit never covers a child
	// requirement; a child hit does cover a parent requirement.
	Parent string
	// Related lists skills close enough to hint about but never to count.
	Related []string
}

// table is the single maintainable source of truth. Add an entry here rather
// than teaching the parser a new synonym.
var table = []entry{
	{Canonical: "Go", Aliases: []string{"golang", "go lang", "go语言", "go 语言"}},
	{Canonical: "Java", Aliases: []string{"java se", "java ee"}},
	{Canonical: "Python", Aliases: []string{"python3", "py"}},
	{Canonical: "C++", Aliases: []string{"cpp", "c plus plus"}},
	{Canonical: "Rust"},
	{Canonical: "Node.js", Aliases: []string{"nodejs", "node", "node js"}},
	{Canonical: "TypeScript", Aliases: []string{"ts"}},
	{Canonical: "React", Aliases: []string{"reactjs", "react.js"}},
	{Canonical: "Vue", Aliases: []string{"vuejs", "vue.js"}, Related: []string{"React"}},

	{Canonical: "MySQL", Aliases: []string{"mariadb"}, Parent: "数据库"},
	{Canonical: "PostgreSQL", Aliases: []string{"postgres", "pgsql", "psql", "postgre sql"}, Parent: "数据库"},
	{Canonical: "MongoDB", Aliases: []string{"mongo"}, Parent: "数据库"},
	{Canonical: "Redis", Aliases: []string{"redis cache"}, Parent: "缓存"},
	{Canonical: "Elasticsearch", Aliases: []string{"elastic search"}},
	{Canonical: "Kafka", Aliases: []string{"apache kafka", "kafkajs"}, Parent: "消息队列"},
	{Canonical: "RabbitMQ", Aliases: []string{"rabbit mq"}, Parent: "消息队列"},
	{Canonical: "Nginx"},
	{Canonical: "ZooKeeper", Aliases: []string{"zookeeper"}},

	{Canonical: "Docker", Aliases: []string{"dockerfile", "docker compose", "docker-compose"}, Related: []string{"Kubernetes"}},
	{Canonical: "Kubernetes", Aliases: []string{"k8s", "kube", "k8s.io"}, Related: []string{"Docker"}},
	{Canonical: "Istio", Parent: "微服务"},
	{Canonical: "gRPC"},
	{Canonical: "Linux"},

	{Canonical: "Prometheus", Parent: "可观测性"},
	{Canonical: "Grafana", Parent: "可观测性"},
	{Canonical: "Nacos", Parent: "微服务"},
	{Canonical: "Sentinel", Parent: "微服务"},
	{Canonical: "Seata", Parent: "微服务"},

	{Canonical: "Spring"},
	{Canonical: "Spring Boot", Aliases: []string{"springboot", "spring-boot"}, Parent: "Spring"},
	{Canonical: "Spring Cloud", Aliases: []string{"springcloud"}, Parent: "Spring"},
	{Canonical: "Spring MVC", Aliases: []string{"springmvc"}, Parent: "Spring"},
	{Canonical: "JVM", Aliases: []string{"java virtual machine"}},

	{Canonical: "LLM", Aliases: []string{"large language model", "大语言模型", "大模型"}, Related: []string{"RAG", "Agent"}},
	{Canonical: "RAG", Aliases: []string{"retrieval augmented generation", "检索增强", "检索增强生成"}, Related: []string{"LLM"}},
	{Canonical: "Agent", Aliases: []string{"ai agent", "智能体"}, Related: []string{"LLM"}},
	{Canonical: "LangChain", Aliases: []string{"lang chain"}, Related: []string{"RAG", "LLM"}},
	{Canonical: "向量数据库", Aliases: []string{"vector database", "vector db", "vectordb"}},
	{Canonical: "Milvus", Parent: "向量数据库"},
	{Canonical: "Qdrant", Parent: "向量数据库"},
	{Canonical: "Weaviate", Parent: "向量数据库"},
	{Canonical: "Chroma", Parent: "向量数据库"},
	{Canonical: "PyTorch", Aliases: []string{"torch"}},
	{Canonical: "TensorFlow", Aliases: []string{"tensor flow"}},

	{Canonical: "分布式", Aliases: []string{"distributed", "distributed system", "分布式系统"}},
	{Canonical: "并发", Aliases: []string{"concurrency"}},
	{Canonical: "高并发", Aliases: []string{"high concurrency", "高吞吐", "high throughput"}, Parent: "并发"},
	{Canonical: "微服务", Aliases: []string{"microservice", "microservices"}},
	{Canonical: "消息队列", Aliases: []string{"mq", "message queue", "message queueing", "消息中间件"}},
	{Canonical: "可观测性", Aliases: []string{"observability", "链路追踪", "tracing", "distributed tracing"}},
	{Canonical: "性能优化", Aliases: []string{"性能调优", "performance tuning", "perf tuning"}},
	{Canonical: "CI/CD", Aliases: []string{"cicd", "ci cd", "持续集成", "continuous integration"}},
	{Canonical: "数据库"},
	{Canonical: "缓存"},
}

var (
	canonicalIndex map[string]*entry
	formIndex      map[string]*entry
	relatedIndex   map[string][]*entry

	parentheticalVersion = regexp.MustCompile(`\s*\([^)]*\d[^)]*\)`)
	trailingVersion      = regexp.MustCompile(`(?i)\s*(?:version|ver|v)?\s*\d+(?:\.\d+)*(?:\.(?:x|\*))?\s*$`)
	trailingTokenVersion = regexp.MustCompile(`^([a-z][a-z0-9+#/]*?)(?:v?\d+)$`)
)

func init() {
	canonicalIndex = make(map[string]*entry, len(table))
	formIndex = make(map[string]*entry, len(table)*3)
	relatedIndex = make(map[string][]*entry)
	for i := range table {
		item := &table[i]
		canonicalIndex[Normalize(item.Canonical)] = item
		formIndex[Normalize(item.Canonical)] = item
		for _, alias := range item.Aliases {
			key := Normalize(alias)
			if key == "" {
				continue
			}
			// First writer wins; a canonical name always keeps its own key.
			if _, exists := formIndex[key]; !exists {
				formIndex[key] = item
			}
		}
	}
	for i := range table {
		item := &table[i]
		for _, name := range item.Related {
			if other := canonicalIndex[Normalize(name)]; other != nil {
				relatedIndex[item.Canonical] = append(relatedIndex[item.Canonical], other)
				relatedIndex[other.Canonical] = append(relatedIndex[other.Canonical], item)
			}
		}
	}
}

// Normalize lowercases a name and removes formatting noise: hyphens,
// underscores, dots and spaces collapse to single spaces, and version suffixes
// such as "Go 1.21" or "Spring Boot 3" are dropped. "+", "#" and "/" survive so
// C++, C# and CI/CD keep their meaning.
func Normalize(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	name = strings.NewReplacer(
		"\u2010", "-", "\u2011", "-", "\u2012", "-", "\u2013", "-", "\u2014", "-",
	).Replace(name)
	name = parentheticalVersion.ReplaceAllString(name, " ")
	name = trailingVersion.ReplaceAllString(name, " ")
	name = strings.NewReplacer("-", " ", "_", " ", ".", " ", "\t", " ").Replace(name)
	return strings.Join(strings.Fields(name), " ")
}

// Canonical returns the display name for any known spelling.
func Canonical(name string) (string, bool) {
	if item := lookup(name); item != nil {
		return item.Canonical, true
	}
	return "", false
}

// Covered reports whether text covers requirement under the rule ladder:
// exact wording, then alias, then hierarchy upward motion. Fuzzy matches are
// hints only and never count.
func Covered(text, requirement string) bool {
	return Check(text, requirement).Covered()
}

// Check evaluates one requirement against free-form resume text.
func Check(text, requirement string) Result {
	requirement = strings.TrimSpace(requirement)
	result := Result{Skill: requirement, Status: StatusMissing}
	if requirement == "" || strings.TrimSpace(text) == "" {
		return result
	}
	item := lookup(requirement)
	display := requirement
	if item != nil {
		display = item.Canonical
	}
	result.Matched = display

	// 1. Exact wording. Known ASCII skills match on token boundaries so
	// "redis" no longer matches "redisson"; unknown keywords and CJK names keep
	// the legacy substring rule so their results do not change.
	if item != nil && exactPresent(text, requirement) {
		result.Status = StatusCovered
		result.Source = SourceExact
		return result
	}
	if item == nil {
		if strings.Contains(strings.ToLower(text), strings.ToLower(requirement)) {
			result.Status = StatusCovered
			result.Source = SourceExact
			return result
		}
		result.Matched = ""
		return result
	}

	// 2. Any other spelling of the requirement (canonical or alias), covering
	// both directions: JD writes k8s, resume writes Kubernetes, and vice versa.
	if formsPresent(text, item) {
		result.Status = StatusCovered
		result.Source = SourceAlias
		return result
	}

	// 3. A narrower skill in the resume covers the broader requirement.
	for _, child := range childrenOf(item) {
		if formsPresent(text, child) {
			result.Status = StatusCovered
			result.Source = SourceChild
			result.Matched = child.Canonical
			return result
		}
	}

	// 4. Broader, sibling or related skills are useful hints, never coverage.
	if parent := parentOf(item); parent != nil && formsPresent(text, parent) {
		result.Status = StatusWeak
		result.Source = SourceParent
		result.Matched = parent.Canonical
		return result
	}
	for _, sibling := range siblingsOf(item) {
		if formsPresent(text, sibling) {
			result.Status = StatusWeak
			result.Source = SourceSibling
			result.Matched = sibling.Canonical
			return result
		}
	}
	for _, related := range relatedIndex[item.Canonical] {
		if formsPresent(text, related) {
			result.Status = StatusWeak
			result.Source = SourceRelated
			result.Matched = related.Canonical
			return result
		}
	}

	// 5. Conservative edit-distance fallback for typos. It stays a weak hint:
	// false positives are expensive, so it never adds coverage.
	if typo := fuzzyForm(text, item); typo != "" {
		result.Status = StatusWeak
		result.Source = SourceFuzzy
		result.Matched = typo
		return result
	}
	result.Matched = ""
	return result
}

// PhrasePresent reports whether a normalized phrase appears in text. ASCII
// phrases match on token n-grams (so "go" does not match "django"), while
// phrases containing CJK match on a compacted substring because Chinese text
// has no spaces.
func PhrasePresent(text, phrase string) bool {
	if strings.TrimSpace(phrase) == "" {
		return false
	}
	if containsCJK(phrase) {
		return strings.Contains(compact(text), compact(phrase))
	}
	return ngramPresent(tokens(text), tokens(phrase))
}

func exactPresent(text, requirement string) bool {
	if containsCJK(requirement) {
		return strings.Contains(compact(text), compact(requirement))
	}
	return ngramPresent(tokens(text), tokens(requirement))
}

func ngramPresent(have, want []string) bool {
	if len(want) == 0 {
		return false
	}
	for i := 0; i+len(want) <= len(have); i++ {
		if matchTokens(have[i:i+len(want)], want) {
			return true
		}
	}
	return false
}

func lookup(name string) *entry {
	return formIndex[Normalize(name)]
}

func aliasPresent(text string, item *entry) bool {
	for _, alias := range item.Aliases {
		if PhrasePresent(text, alias) {
			return true
		}
	}
	return false
}

func formsPresent(text string, item *entry) bool {
	if PhrasePresent(text, item.Canonical) {
		return true
	}
	return aliasPresent(text, item)
}

func childrenOf(item *entry) []*entry {
	children := make([]*entry, 0, 4)
	for i := range table {
		if table[i].Parent != "" && Normalize(table[i].Parent) == Normalize(item.Canonical) {
			children = append(children, &table[i])
		}
	}
	return children
}

func parentOf(item *entry) *entry {
	if item.Parent == "" {
		return nil
	}
	return canonicalIndex[Normalize(item.Parent)]
}

func siblingsOf(item *entry) []*entry {
	parent := parentOf(item)
	if parent == nil {
		return nil
	}
	all := childrenOf(parent)
	out := make([]*entry, 0, len(all))
	for _, sibling := range all {
		if sibling.Canonical != item.Canonical {
			out = append(out, sibling)
		}
	}
	return out
}

// tokens splits normalized text into skill-shaped tokens while keeping C++
// intact and separating ASCII from CJK runs ("Redis缓存" -> redis, 缓存).
// Hyphens, dots, underscores and slashes are separators, so "HTTP/2" still
// matches "HTTP" and "ci/cd" matches a two-token phrase.
func tokens(text string) []string {
	norm := Normalize(text)
	var out []string
	var current strings.Builder
	cjk := false
	flush := func() {
		if current.Len() > 0 {
			out = append(out, strings.ToLower(current.String()))
			current.Reset()
		}
	}
	for _, r := range norm {
		word := unicode.IsLetter(r) || unicode.IsDigit(r) || r == '+' || r == '#'
		isCJK := unicode.IsLetter(r) && r > unicode.MaxASCII
		if !word {
			flush()
			cjk = false
			continue
		}
		if current.Len() > 0 && isCJK != cjk {
			flush()
		}
		cjk = isCJK
		current.WriteRune(r)
	}
	flush()
	return out
}

func compact(text string) string {
	lower := strings.ToLower(text)
	var b strings.Builder
	for _, r := range lower {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r), r == '+', r == '#':
			b.WriteRune(r)
		}
	}
	return b.String()
}

func matchTokens(have, want []string) bool {
	for i := range want {
		if !tokenEqual(have[i], want[i]) {
			return false
		}
	}
	return true
}

func tokenEqual(have, want string) bool {
	if have == want {
		return true
	}
	for _, variant := range tokenVariants(have) {
		if variant == want {
			return true
		}
	}
	return false
}

// tokenVariants strips an attached version from a token, so "go1" also
// matches "go" without turning "k8s" into anything else.
func tokenVariants(token string) []string {
	if m := trailingTokenVersion.FindStringSubmatch(token); len(m) == 2 && len(m[1]) >= 2 {
		return []string{m[1]}
	}
	return nil
}

// fuzzyForm returns the canonical name a token is one edit away from, or "".
// Guards: at least six characters, same first letter, length difference <= 1,
// and the token must not already be a known spelling of another skill.
func fuzzyForm(text string, item *entry) string {
	names := append([]string{item.Canonical}, item.Aliases...)
	for _, token := range tokens(text) {
		if !isASCII(token) || len(token) < 6 || formIndex[token] != nil {
			continue
		}
		for _, name := range names {
			for _, candidate := range tokens(name) {
				if !isASCII(candidate) || len(candidate) < 6 {
					continue
				}
				if abs(len(token)-len(candidate)) > 1 || token[0] != candidate[0] {
					continue
				}
				if levenshtein(token, candidate) > 1 {
					continue
				}
				// Never fuzzy-match a spelling that already belongs to another
				// canonical skill (for example mysql versus mssql).
				if other := formIndex[candidate]; other != nil && other.Canonical != item.Canonical {
					continue
				}
				return item.Canonical
			}
		}
	}
	return ""
}

func levenshtein(a, b string) int {
	if a == b {
		return 0
	}
	rows := make([]int, len(b)+1)
	for j := range rows {
		rows[j] = j
	}
	for i := 1; i <= len(a); i++ {
		previous := rows[0]
		rows[0] = i
		for j := 1; j <= len(b); j++ {
			current := rows[j]
			cost := 0
			if a[i-1] != b[j-1] {
				cost = 1
			}
			rows[j] = min3(rows[j]+1, rows[j-1]+1, previous+cost)
			previous = current
		}
	}
	return rows[len(b)]
}

func min3(a, b, c int) int {
	if b < a {
		a = b
	}
	if c < a {
		a = c
	}
	return a
}

func abs(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

func containsCJK(text string) bool {
	for _, r := range text {
		if r > unicode.MaxASCII {
			return true
		}
	}
	return false
}

func isASCII(text string) bool {
	for _, r := range text {
		if r > unicode.MaxASCII {
			return false
		}
	}
	return true
}
