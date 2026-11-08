package knowledge

import (
	"path/filepath"
	"testing"
)

func TestLoadCommittedQuerySet(t *testing.T) {
	set, err := LoadQuerySet(filepath.Join("testdata", "retrieval_queries.json"))
	if err != nil {
		t.Fatalf("LoadQuerySet: %v", err)
	}
	if len(set.Queries) != 54 {
		t.Fatalf("queries = %d, want 54", len(set.Queries))
	}
	counts := map[string]int{}
	for _, q := range set.Queries {
		counts[q.Group]++
		if q.Group == "B" && q.Rewrite == "" {
			t.Fatalf("B query %s has no rewrite intent", q.ID)
		}
	}
	if counts["A"] != 25 || counts["B"] != 25 || counts["no_answer"] != 4 {
		t.Fatalf("group counts = %+v, want A=25 B=25 no_answer=4", counts)
	}
}

func TestLoadQuerySetRejectsWrongSplit(t *testing.T) {
	set := QuerySet{Version: "test"}
	expected := []Expectation{{TopicID: "go-concurrency"}}
	for i := 0; i < 25; i++ {
		set.Queries = append(set.Queries, QuerySpec{
			ID: "a" + string(rune('a'+i)), Group: "A", Query: "q", Answerable: true, Expected: expected,
		})
	}
	for i := 0; i < 24; i++ {
		set.Queries = append(set.Queries, QuerySpec{
			ID: "b" + string(rune('a'+i)), Group: "B", Rewrite: "synonym", Query: "q", Answerable: true, Expected: expected,
		})
	}
	for i := 0; i < 4; i++ {
		set.Queries = append(set.Queries, QuerySpec{
			ID: "n" + string(rune('a'+i)), Group: "no_answer", Query: "q", Answerable: false,
		})
	}
	if err := validateSet(&set); err == nil {
		t.Fatal("expected group split validation to fail")
	}
}

func TestCalibrateFindsCleanThreshold(t *testing.T) {
	outcomes := []Outcome{
		{Answerable: true, Top1Score: 0.80, Top: []Hit{{}}},
		{Answerable: true, Top1Score: 0.90, Top: []Hit{{}}},
		{Answerable: false, Top1Score: 0.20, Top: []Hit{{}}},
		{Answerable: false, Top1Score: 0.30, Top: []Hit{{}}},
	}
	cal := Calibrate(outcomes)
	if cal.RefusalRate != 1 || cal.FalseRefusalRate != 0 {
		t.Fatalf("clean split: refusal=%v false=%v", cal.RefusalRate, cal.FalseRefusalRate)
	}
	if cal.Overlap {
		t.Fatal("clean split must not report overlap")
	}
}

func TestCalibrateReportsOverlap(t *testing.T) {
	outcomes := []Outcome{
		{Answerable: true, Top1Score: 0.50, Top: []Hit{{}}},
		{Answerable: true, Top1Score: 0.70, Top: []Hit{{}}},
		{Answerable: false, Top1Score: 0.60, Top: []Hit{{}}},
		{Answerable: false, Top1Score: 0.30, Top: []Hit{{}}},
	}
	cal := Calibrate(outcomes)
	if !cal.Overlap {
		t.Fatalf("overlapping scores must be reported: %+v", cal)
	}
	if cal.RefusalRate == 0 || cal.FalseRefusalRate == 0 {
		t.Fatalf("an overlapping tradeoff must show both rates: %+v", cal)
	}
}

func TestMatchesExpectationRequiresRealLabel(t *testing.T) {
	chunk := Chunk{SourcePath: "docs/a.md", SectionKey: "5-report", SourceKind: "doc_section"}
	if MatchesExpectation(chunk, Expectation{}) {
		t.Fatal("empty label must not match")
	}
	if !MatchesExpectation(chunk, Expectation{SourcePath: "docs/a.md", SectionPrefix: "5-"}) {
		t.Fatal("path plus section prefix should match")
	}
	if MatchesExpectation(chunk, Expectation{TopicID: "go-concurrency"}) {
		t.Fatal("doc chunk must not match a topic id")
	}
}
