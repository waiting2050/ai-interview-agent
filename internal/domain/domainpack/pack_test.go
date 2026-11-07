package domainpack

import (
	"math"
	"strings"
	"testing"
	"testing/fstest"
)

func TestEmbeddedRegistryLoadsThreeCompletePacks(t *testing.T) {
	reg := Default()
	packs := reg.List()
	if len(packs) != 3 {
		t.Fatalf("packs = %d, want 3 (%v)", len(packs), ids(packs))
	}
	want := map[string]bool{"go-backend": false, "java-backend": false, "ai-agent": false}
	for _, pack := range packs {
		if _, ok := want[pack.ID]; !ok {
			t.Fatalf("unexpected pack %q", pack.ID)
		}
		want[pack.ID] = true
		if pack.Name == "" || pack.Version == "" {
			t.Fatalf("%s: name/version required", pack.ID)
		}
		if len(pack.Topics) < 10 {
			t.Fatalf("%s: %d topics, want at least 10", pack.ID, len(pack.Topics))
		}
		var sum float64
		for _, dim := range pack.Rubric.Dimensions {
			if dim.Name == "" || dim.Weight <= 0 {
				t.Fatalf("%s: dimension %q incomplete", pack.ID, dim.Key)
			}
			sum += dim.Weight
		}
		if math.Abs(sum-1) > 0.001 {
			t.Fatalf("%s: weights sum to %.3f", pack.ID, sum)
		}
		for _, topic := range pack.Topics {
			if len(topic.ExpectedPoints) < 3 {
				t.Fatalf("%s/%s: %d expected points, want 3", pack.ID, topic.ID, len(topic.ExpectedPoints))
			}
			if topic.Source == "curated" && topic.URL != "" {
				t.Fatalf("%s/%s: curated topic must not carry a URL", pack.ID, topic.ID)
			}
		}
		if len(pack.Lexicon.Skills)+len(pack.Lexicon.Tools)+len(pack.Lexicon.Keywords) == 0 {
			t.Fatalf("%s: empty lexicon", pack.ID)
		}
	}
	for id, found := range want {
		if !found {
			t.Fatalf("missing pack %q", id)
		}
	}
}

func TestRegistryIgnoresUnregisteredDirectories(t *testing.T) {
	reg := Default()
	for _, id := range []string{"examples", "solution-presales", "ops-growth"} {
		if _, ok := reg.Get(id); ok {
			t.Fatalf("unregistered pack %q was loaded", id)
		}
	}
}

func TestLoadFSRejectsInvalidPacks(t *testing.T) {
	const validTopic = `
topics:
  - id: t1
    direction: d1
    topic: T
    objective: O
    difficulty: mid
    expected_points: [a, b, c]
    source: curated
`
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "id mismatch",
			body: "id: other\nname: N\nversion: 1\nrubric:\n  dimensions:\n    - key: correctness\n      name: C\n      weight: 1\ndirections:\n  - id: d1\n    keywords: [k]\n" + validTopic + "lexicon:\n  skills: [s]\n",
			want: "must match directory",
		},
		{
			name: "weights do not sum to one",
			body: "id: bad\nname: N\nversion: 1\nrubric:\n  dimensions:\n    - key: correctness\n      name: C\n      weight: 0.5\ndirections:\n  - id: d1\n    keywords: [k]\n" + validTopic + "lexicon:\n  skills: [s]\n",
			want: "weights must sum to 1",
		},
		{
			name: "duplicate dimension",
			body: "id: bad\nname: N\nversion: 1\nrubric:\n  dimensions:\n    - key: correctness\n      name: C\n      weight: 0.5\n    - key: correctness\n      name: C2\n      weight: 0.5\ndirections:\n  - id: d1\n    keywords: [k]\n" + validTopic + "lexicon:\n  skills: [s]\n",
			want: "duplicate dimension",
		},
		{
			name: "curated topic with url",
			body: "id: bad\nname: N\nversion: 1\nrubric:\n  dimensions:\n    - key: correctness\n      name: C\n      weight: 1\ndirections:\n  - id: d1\n    keywords: [k]\n" + strings.Replace(validTopic, "source: curated", "source: curated\n    url: https://example.com", 1) + "lexicon:\n  skills: [s]\n",
			want: "curated topics must not carry a URL",
		},
		{
			name: "retrieved topic without url",
			body: "id: bad\nname: N\nversion: 1\nrubric:\n  dimensions:\n    - key: correctness\n      name: C\n      weight: 1\ndirections:\n  - id: d1\n    keywords: [k]\n" + strings.Replace(validTopic, "source: curated", "source: retrieved", 1) + "lexicon:\n  skills: [s]\n",
			want: "retrieved topics must carry the source URL",
		},
		{
			name: "unknown direction",
			body: "id: bad\nname: N\nversion: 1\nrubric:\n  dimensions:\n    - key: correctness\n      name: C\n      weight: 1\ndirections:\n  - id: d1\n    keywords: [k]\n" + strings.Replace(validTopic, "direction: d1", "direction: d2", 1) + "lexicon:\n  skills: [s]\n",
			want: "unknown direction",
		},
		{
			name: "bad difficulty",
			body: "id: bad\nname: N\nversion: 1\nrubric:\n  dimensions:\n    - key: correctness\n      name: C\n      weight: 1\ndirections:\n  - id: d1\n    keywords: [k]\n" + strings.Replace(validTopic, "difficulty: mid", "difficulty: expert", 1) + "lexicon:\n  skills: [s]\n",
			want: "difficulty must be junior/mid/senior",
		},
		{
			name: "too few expected points",
			body: "id: bad\nname: N\nversion: 1\nrubric:\n  dimensions:\n    - key: correctness\n      name: C\n      weight: 1\ndirections:\n  - id: d1\n    keywords: [k]\n" + strings.Replace(validTopic, "expected_points: [a, b, c]", "expected_points: [a]", 1) + "lexicon:\n  skills: [s]\n",
			want: "expected_points",
		},
		{
			name: "empty lexicon",
			body: "id: bad\nname: N\nversion: 1\nrubric:\n  dimensions:\n    - key: correctness\n      name: C\n      weight: 1\ndirections:\n  - id: d1\n    keywords: [k]\n" + validTopic,
			want: "lexicon",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fsys := fstest.MapFS{
				"bad/manifest.yaml": &fstest.MapFile{Data: []byte(tc.body)},
			}
			_, err := LoadFS(fsys)
			if err == nil {
				t.Fatalf("expected error containing %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestLoadFSSkipsDirectoriesWithoutManifest(t *testing.T) {
	fsys := fstest.MapFS{
		"docs/readme.md":     &fstest.MapFile{Data: []byte("not a pack")},
		"good/manifest.yaml": &fstest.MapFile{Data: []byte(validManifest("good"))},
		"examples/sample":    &fstest.MapFile{Data: []byte(validManifest("examples"))},
	}
	reg, err := LoadFS(fsys)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if _, ok := reg.Get("good"); !ok {
		t.Fatalf("good pack missing: %v", ids(reg.List()))
	}
	if _, ok := reg.Get("examples"); ok {
		t.Fatalf("examples directory must not load as a pack")
	}
}

func TestPlanIsDeterministicAndDeduplicated(t *testing.T) {
	pack, ok := Default().Get("go-backend")
	if !ok {
		t.Fatal("go-backend pack missing")
	}
	first := pack.Plan("Go 后端，熟悉 MySQL 和 Redis", "mid", 6)
	second := pack.Plan("Go 后端，熟悉 MySQL 和 Redis", "mid", 6)
	if len(first) != 6 || len(second) != 6 {
		t.Fatalf("plan sizes = %d/%d, want 6", len(first), len(second))
	}
	for i := range first {
		if first[i].Topic != second[i].Topic {
			t.Fatalf("plan not deterministic at %d: %q vs %q", i, first[i].Topic, second[i].Topic)
		}
	}
	seen := map[string]struct{}{}
	for _, item := range first {
		if _, dup := seen[item.Topic]; dup {
			t.Fatalf("duplicate topic %q", item.Topic)
		}
		seen[item.Topic] = struct{}{}
	}
}

func TestPlanGoldenGoBackend(t *testing.T) {
	pack := Default().MustGet("go-backend")
	got := pack.Plan("Go 后端开发工程师", "mid", 3)
	var topics []string
	for _, item := range got {
		topics = append(topics, item.Topic)
	}
	want := []string{"Go 并发模型", "服务分层与依赖边界", "内存管理与 GC"}
	if strings.Join(topics, "|") != strings.Join(want, "|") {
		t.Fatalf("golden plan changed:\n got: %v\nwant: %v", topics, want)
	}
}

func TestPacksMarkHighFrequencyTopics(t *testing.T) {
	for _, pack := range Default().List() {
		marked := 0
		for _, topic := range pack.Topics {
			if topic.Focus {
				marked++
			}
		}
		if marked == 0 {
			t.Fatalf("pack %s has no focus topic", pack.ID)
		}
	}
}

func TestDetectAndRubric(t *testing.T) {
	pack := Default().MustGet("ai-agent")
	if got := pack.Detect("做过 RAG 检索和 LLM 提示词优化"); len(got) != 2 || got[0] != "llm" || got[1] != "rag" {
		t.Fatalf("detect = %v", got)
	}
	rubric := pack.EvaluationRubric()
	if rubric.IsZero() {
		t.Fatal("empty rubric")
	}
	if rubric.Name("system_design") != "系统设计" {
		t.Fatalf("system_design label = %q", rubric.Name("system_design"))
	}
	if rubric.Weight("system_design") != 0.20 {
		t.Fatalf("system_design weight = %v", rubric.Weight("system_design"))
	}
}

func ids(packs []*Pack) []string {
	out := make([]string, 0, len(packs))
	for _, pack := range packs {
		out = append(out, pack.ID)
	}
	return out
}

func validManifest(id string) string {
	return "id: " + id + "\nname: N\nversion: 1\nrubric:\n  dimensions:\n    - key: correctness\n      name: C\n      weight: 1\ndirections:\n  - id: d1\n    keywords: [k]\ntopics:\n  - id: t1\n    direction: d1\n    topic: T\n    objective: O\n    difficulty: mid\n    expected_points: [a, b, c]\n    source: curated\nlexicon:\n  skills: [s]\n"
}
