package knowledge

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"github.com/waiting2050/ai-interview-agent/internal/domain/domainpack"
)

// CorpusSources describes where chunks come from. Domain topics are taken from
// the embedded packs; documentation is read from disk so ingest always sees
// the current working tree.
type CorpusSources struct {
	Packs      []*domainpack.Pack
	DocsRoot   string
	ExtraFiles []string
}

// maxSectionRunes is the soft cap for one documentation chunk. Oversized
// sections are split at paragraph boundaries, never mid-sentence.
const maxSectionRunes = 1200

// BuildCorpus produces a deterministic, ordered chunk list. Ordering matters
// only for reproducibility of the dry-run report and tests.
func BuildCorpus(src CorpusSources) ([]Chunk, error) {
	var chunks []Chunk

	for _, pack := range src.Packs {
		if pack == nil {
			continue
		}
		for _, topic := range pack.Topics {
			chunks = append(chunks, topicChunk(pack, topic))
		}
	}

	paths, err := markdownPaths(src.DocsRoot, src.ExtraFiles)
	if err != nil {
		return nil, err
	}
	for _, path := range paths {
		body, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read corpus file %s: %w", path, err)
		}
		chunks = append(chunks, docChunks(filepath.ToSlash(path), string(body))...)
	}

	seen := map[string]bool{}
	for _, c := range chunks {
		if seen[c.ID] {
			return nil, fmt.Errorf("duplicate chunk id %s (%s#%s)", c.ID, c.SourcePath, c.SectionKey)
		}
		seen[c.ID] = true
	}
	sort.SliceStable(chunks, func(i, j int) bool {
		if chunks[i].SourcePath == chunks[j].SourcePath {
			return chunks[i].SectionKey < chunks[j].SectionKey
		}
		return chunks[i].SourcePath < chunks[j].SourcePath
	})
	return chunks, nil
}

func topicChunk(pack *domainpack.Pack, topic domainpack.Topic) Chunk {
	var b strings.Builder
	fmt.Fprintf(&b, "考点：%s\n", topic.Topic)
	fmt.Fprintf(&b, "领域：%s（%s）\n", pack.Name, pack.ID)
	fmt.Fprintf(&b, "方向：%s\n", topic.Direction)
	fmt.Fprintf(&b, "难度：%s\n", topic.Difficulty)
	if topic.Objective != "" {
		fmt.Fprintf(&b, "目标：%s\n", topic.Objective)
	}
	if len(topic.ExpectedPoints) > 0 {
		b.WriteString("期望覆盖：\n")
		for _, point := range topic.ExpectedPoints {
			fmt.Fprintf(&b, "- %s\n", point)
		}
	}
	content := strings.TrimSpace(b.String())
	sectionKey := "topic:" + topic.ID
	return Chunk{
		ID:          chunkID("domainpacks/"+pack.ID+"/manifest.yaml", sectionKey),
		SourcePath:  "domainpacks/" + pack.ID + "/manifest.yaml",
		SectionKey:  sectionKey,
		SourceKind:  "domain_topic",
		Domain:      pack.ID,
		TopicID:     topic.ID,
		Title:       topic.Topic,
		Content:     content,
		ContentHash: hashText(content),
	}
}

func markdownPaths(root string, extra []string) ([]string, error) {
	var paths []string
	seen := map[string]bool{}
	for _, path := range extra {
		clean := filepath.Clean(path)
		if _, err := os.Stat(clean); err != nil {
			return nil, fmt.Errorf("corpus file %s: %w", path, err)
		}
		if !seen[clean] {
			seen[clean] = true
			paths = append(paths, clean)
		}
	}
	if root == "" {
		sort.Strings(paths)
		return paths, nil
	}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "eval-results" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(d.Name()), ".md") {
			return nil
		}
		clean := filepath.Clean(path)
		if !seen[clean] {
			seen[clean] = true
			paths = append(paths, clean)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk corpus root %s: %w", root, err)
	}
	sort.Strings(paths)
	return paths, nil
}

// docChunks splits one Markdown file by heading. Each heading plus its body is
// one semantic unit; a body longer than maxSectionRunes is split at blank-line
// paragraph boundaries and labelled as a continuation.
func docChunks(path, body string) []Chunk {
	if strings.TrimSpace(body) == "" {
		return nil
	}
	sections := splitMarkdown(body)

	var out []Chunk
	keyCount := map[string]int{}
	for i, sec := range sections {
		heading := strings.TrimSpace(sec.title)
		if heading == "" {
			heading = fmt.Sprintf("第 %d 段", i+1)
		}
		baseKey := slug(heading)
		if baseKey == "" {
			baseKey = fmt.Sprintf("section-%d", i+1)
		}
		keyCount[baseKey]++
		sectionKey := baseKey
		if n := keyCount[baseKey]; n > 1 {
			sectionKey = fmt.Sprintf("%s-%d", baseKey, n)
		}

		parts := splitParagraphs(sec.body, maxSectionRunes)
		for partIndex, part := range parts {
			if !hasMeaningfulText(part) {
				continue
			}
			title := heading
			key := sectionKey
			if partIndex > 0 {
				title = fmt.Sprintf("%s（续 %d）", heading, partIndex+1)
				key = fmt.Sprintf("%s#p%d", sectionKey, partIndex+1)
			}
			content := strings.TrimSpace(title + "\n\n" + part)
			out = append(out, Chunk{
				ID:          chunkID(path, key),
				SourcePath:  path,
				SectionKey:  key,
				SourceKind:  "doc_section",
				Title:       title,
				Content:     content,
				ContentHash: hashText(content),
			})
		}
	}
	return out
}

type mdSection struct {
	title string
	body  string
}

// hasMeaningfulText rejects Markdown scaffolding such as "---" or a lone
// backtick fence: a body of pure punctuation has no words to retrieve and
// would only add noise to BM25 and vector candidates.
func hasMeaningfulText(s string) bool {
	letters := 0
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			letters++
			if letters >= 2 {
				return true
			}
		}
	}
	return false
}

// splitMarkdown returns one section per heading. Heading text includes the
// original line minus leading hashes. Lines inside fenced code blocks never
// start a section.
func splitMarkdown(body string) []mdSection {
	lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	var sections []mdSection
	current := mdSection{title: "开头", body: ""}
	inFence := false
	var buf []string

	flush := func() {
		current.body = strings.TrimSpace(strings.Join(buf, "\n"))
		sections = append(sections, current)
		buf = nil
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
		}
		if !inFence && strings.HasPrefix(trimmed, "#") {
			level := 0
			for level < len(trimmed) && trimmed[level] == '#' {
				level++
			}
			if level <= 6 && level < len(trimmed) && (trimmed[level] == ' ' || trimmed[level] == '\t') {
				flush()
				current = mdSection{title: strings.TrimSpace(trimmed[level:])}
				continue
			}
		}
		buf = append(buf, line)
	}
	flush()

	out := sections[:0]
	for _, sec := range sections {
		if strings.TrimSpace(sec.title) == "开头" && strings.TrimSpace(sec.body) == "" {
			continue
		}
		out = append(out, sec)
	}
	return out
}

// splitParagraphs packs paragraphs into chunks no larger than limit. A single
// oversized paragraph is hard-split only as a last resort.
func splitParagraphs(body string, limit int) []string {
	body = strings.TrimSpace(body)
	if len([]rune(body)) <= limit {
		return []string{body}
	}
	paras := strings.Split(body, "\n\n")
	var parts []string
	var buf strings.Builder
	flush := func() {
		if text := strings.TrimSpace(buf.String()); text != "" {
			parts = append(parts, text)
		}
		buf.Reset()
	}
	for _, para := range paras {
		para = strings.TrimSpace(para)
		if para == "" {
			continue
		}
		if len([]rune(para)) > limit {
			flush()
			runes := []rune(para)
			for start := 0; start < len(runes); start += limit {
				end := start + limit
				if end > len(runes) {
					end = len(runes)
				}
				parts = append(parts, string(runes[start:end]))
			}
			continue
		}
		if buf.Len() > 0 && len([]rune(buf.String()))+len([]rune(para))+2 > limit {
			flush()
		}
		if buf.Len() > 0 {
			buf.WriteString("\n\n")
		}
		buf.WriteString(para)
	}
	flush()
	if len(parts) == 0 {
		return []string{body}
	}
	return parts
}

func slug(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '_' || r == '/' || r == '.':
			b.WriteRune('-')
		default:
			// Han characters and other Unicode letters are kept verbatim so
			// Chinese headings stay stable and readable.
			if r > 127 {
				b.WriteRune(r)
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	for strings.Contains(out, "--") {
		out = strings.ReplaceAll(out, "--", "-")
	}
	return out
}

func chunkID(sourcePath, sectionKey string) string {
	sum := sha256.Sum256([]byte(sourcePath + "#" + sectionKey))
	return hex.EncodeToString(sum[:16])
}

func hashText(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}
