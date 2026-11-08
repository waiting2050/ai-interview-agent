package knowledge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/waiting2050/ai-interview-agent/internal/domain/domainpack"
)

func TestBuildCorpusSplitsBySemanticBoundaries(t *testing.T) {
	dir := t.TempDir()
	doc := "# 能力总览\n\n本项目的检索能力说明。\n\n## 混合检索\n\nBM25 与向量用 RRF 融合。\n\n## 分层记忆\n\n最近四轮原文加更早摘要。\n"
	path := filepath.Join(dir, "design.md")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}

	pack := &domainpack.Pack{
		ID:   "go-backend",
		Name: "Go 后端",
		Topics: []domainpack.Topic{
			{
				ID: "go-concurrency", Direction: "go", Topic: "Go 并发模型", Difficulty: "mid",
				Objective:      "说明 goroutine、channel 与 context 的配合",
				ExpectedPoints: []string{"goroutine 调度与 G-M-P 模型", "channel 缓冲设计"},
			},
		},
	}

	chunks, err := BuildCorpus(CorpusSources{Packs: []*domainpack.Pack{pack}, DocsRoot: dir})
	if err != nil {
		t.Fatalf("BuildCorpus: %v", err)
	}

	var foundTopic, foundHybrid, foundMemory bool
	for _, chunk := range chunks {
		switch {
		case chunk.SourceKind == "domain_topic" && chunk.TopicID == "go-concurrency":
			foundTopic = true
			if !strings.Contains(chunk.Content, "goroutine 调度与 G-M-P 模型") {
				t.Fatalf("topic chunk lost expected points: %q", chunk.Content)
			}
		case chunk.Title == "混合检索":
			foundHybrid = true
		case chunk.Title == "分层记忆":
			foundMemory = true
		}
	}
	if !foundTopic || !foundHybrid || !foundMemory {
		t.Fatalf("expected topic + 2 doc sections, got chunks: %+v", chunks)
	}

	// Rebuilding the same corpus must produce identical ids and hashes; the
	// ingest idempotency key depends on it.
	again, err := BuildCorpus(CorpusSources{Packs: []*domainpack.Pack{pack}, DocsRoot: dir})
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if CorpusHash(chunks) != CorpusHash(again) {
		t.Fatalf("corpus hash is unstable")
	}
	for i := range chunks {
		if chunks[i].ID != again[i].ID || chunks[i].ContentHash != again[i].ContentHash {
			t.Fatalf("chunk %d unstable across rebuilds", i)
		}
	}
}

func TestBuildCorpusDropsSeparatorOnlySections(t *testing.T) {
	dir := t.TempDir()
	doc := "# 空段\n\n---\n\n## 有内容\n\n这里有真实文字。\n\n## 只有横线\n\n```\n---\n```\n"
	if err := os.WriteFile(filepath.Join(dir, "empty.md"), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}

	chunks, err := BuildCorpus(CorpusSources{DocsRoot: dir})
	if err != nil {
		t.Fatalf("BuildCorpus: %v", err)
	}
	if len(chunks) != 1 || chunks[0].Title != "有内容" {
		t.Fatalf("separator-only sections must be dropped, got %+v", chunks)
	}
	if !strings.Contains(chunks[0].Content, "真实文字") {
		t.Fatalf("real section lost its body: %q", chunks[0].Content)
	}
}

func TestBuildCorpusSplitsLongSectionsAtParagraphs(t *testing.T) {
	dir := t.TempDir()
	paragraph := strings.Repeat("这是一段用于验证切块边界的测试文字。", 60) // ~1200 runes
	doc := "# 长文\n\n" + paragraph + "\n\n" + paragraph + "\n\n" + paragraph + "\n"
	if err := os.WriteFile(filepath.Join(dir, "long.md"), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}

	chunks, err := BuildCorpus(CorpusSources{DocsRoot: dir})
	if err != nil {
		t.Fatalf("BuildCorpus: %v", err)
	}
	if len(chunks) < 3 {
		t.Fatalf("expected the long section to split, got %d chunk(s)", len(chunks))
	}
	for _, chunk := range chunks {
		if len([]rune(chunk.Content)) > maxSectionRunes+200 {
			t.Fatalf("chunk exceeds soft cap: %d runes", len([]rune(chunk.Content)))
		}
	}
	if !strings.Contains(chunks[1].SectionKey, "#p2") {
		t.Fatalf("continuation key missing: %q", chunks[1].SectionKey)
	}
}
