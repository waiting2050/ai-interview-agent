package knowledge

import "testing"

func TestPlanIngestIsIdempotent(t *testing.T) {
	chunks := []Chunk{
		{ID: "a", ContentHash: "h1", SourcePath: "p", SectionKey: "s1"},
		{ID: "b", ContentHash: "h2", SourcePath: "p", SectionKey: "s2"},
	}

	first := PlanIngest(nil, chunks, false)
	if len(first.Changed) != 2 || len(first.Stale) != 0 {
		t.Fatalf("first run: %+v", first)
	}

	stored := []StoredChunk{
		{Chunk: chunks[0], HasEmbedding: true},
		{Chunk: chunks[1], HasEmbedding: true},
	}
	second := PlanIngest(stored, chunks, false)
	if len(second.Changed) != 0 || len(second.Stale) != 0 {
		t.Fatalf("second run must be a no-op: %+v", second)
	}

	// A rebuilt corpus with the same content hashes must also be a no-op.
	if CorpusHash(chunks) != CorpusHash([]Chunk{chunks[1], chunks[0]}) {
		t.Fatal("corpus hash must not depend on ordering")
	}
}

func TestPlanIngestDetectsChangesStaleAndMissingVectors(t *testing.T) {
	base := Chunk{ID: "a", ContentHash: "h1", SourcePath: "p", SectionKey: "s1"}
	stored := []StoredChunk{{Chunk: base, HasEmbedding: true}}

	changed := base
	changed.ContentHash = "h2"
	plan := PlanIngest(stored, []Chunk{changed}, false)
	if len(plan.Changed) != 1 || len(plan.Stale) != 0 {
		t.Fatalf("content change: %+v", plan)
	}

	plan = PlanIngest(stored, []Chunk{base}, true)
	if len(plan.Changed) != 1 {
		t.Fatalf("force must re-embed: %+v", plan)
	}

	plan = PlanIngest([]StoredChunk{{Chunk: base, HasEmbedding: false}}, []Chunk{base}, false)
	if len(plan.Changed) != 1 {
		t.Fatalf("missing vector must be re-embedded: %+v", plan)
	}

	plan = PlanIngest(stored, nil, false)
	if len(plan.Changed) != 0 || len(plan.Stale) != 1 || plan.Stale[0] != "a" {
		t.Fatalf("stale detection: %+v", plan)
	}
}

func TestStatsEstimatesBatches(t *testing.T) {
	chunks := []Chunk{
		{Content: "goroutine 调度与 G-M-P 模型", SourceKind: "domain_topic", Domain: "go-backend"},
		{Content: "MySQL 索引与回表", SourceKind: "doc_section"},
	}
	st := Stats(chunks, 16)
	if st.Chunks != 2 || st.Runes == 0 || st.Tokens == 0 {
		t.Fatalf("stats: %+v", st)
	}
	if st.Batches != 1 {
		t.Fatalf("batches = %d", st.Batches)
	}
	if st.Domains["go-backend"] != 1 || st.Kinds["doc_section"] != 1 {
		t.Fatalf("grouping: %+v", st)
	}
}
