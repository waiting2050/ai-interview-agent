package eval

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/waiting2050/ai-interview-agent/internal/domain/evaluation"
)

func TestLoadShippedRegressionSet(t *testing.T) {
	ds, err := LoadDataset(filepath.Join("testdata", "regression.json"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(ds.Cases) < MinCases || len(ds.Cases) > MaxCases {
		t.Fatalf("case count %d outside [%d,%d]", len(ds.Cases), MinCases, MaxCases)
	}

	counts := ds.TypeCounts()
	for _, typ := range []string{
		TypeHigh, TypeMid, TypeLow, TypeEmpty, TypeExaggerated, TypeOffTopic,
		TypeJargon, TypeUnverified, TypeConclusionOnly, TypeVerboseOffTopic,
	} {
		if counts[typ] == 0 {
			t.Fatalf("regression set has no %s case", typ)
		}
	}

	domains := ds.DomainCounts()
	if domains[DomainGoBackend] != 35 || domains[DomainAIAgent] != 25 {
		t.Fatalf("domain distribution = %v, want go-backend=35 ai-agent=25", domains)
	}

	bands := map[int]int{}
	for _, c := range ds.Cases {
		bands[c.ExpectedBand]++
	}
	for _, band := range []int{1, 3, 5} {
		if bands[band] == 0 {
			t.Fatalf("regression set has no expected_band=%d case", band)
		}
	}
}

func TestFileDigest(t *testing.T) {
	path := filepath.Join("testdata", "regression.json")
	digest, err := FileDigest(path)
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	if len(digest) != 64 {
		t.Fatalf("sha256 length = %d, want 64", len(digest))
	}
}

func TestValidateRejectsInconsistentBand(t *testing.T) {
	ds := Dataset{Version: 1, Cases: []Case{{
		CaseID:         "bad-band",
		Domain:         DomainGoBackend,
		Type:           TypeHigh,
		Question:       "q",
		Answer:         "a",
		Notes:          "n",
		EvidenceAnchor: "a",
		ExpectedBand:   5,
		ExpectedScores: map[evaluation.Dimension]int{
			evaluation.DimensionCorrectness:         3,
			evaluation.DimensionDepth:               3,
			evaluation.DimensionEngineeringJudgment: 3,
			evaluation.DimensionStructure:           3,
			evaluation.DimensionEvidenceCredibility: 3,
		},
	}}}
	err := ds.Validate()
	if err == nil || !strings.Contains(err.Error(), "derive band 3") {
		t.Fatalf("expected derived-band error, got %v", err)
	}
}

func TestValidateRejectsBadTypeAndDuplicate(t *testing.T) {
	c := Case{
		CaseID:         "dup",
		Domain:         DomainGoBackend,
		Type:           TypeHigh,
		Question:       "q",
		Answer:         "a",
		Notes:          "n",
		EvidenceAnchor: "a",
		ExpectedBand:   1,
		ExpectedScores: map[evaluation.Dimension]int{
			evaluation.DimensionCorrectness:         1,
			evaluation.DimensionDepth:               1,
			evaluation.DimensionEngineeringJudgment: 1,
			evaluation.DimensionStructure:           1,
			evaluation.DimensionEvidenceCredibility: 1,
		},
	}
	ds := Dataset{Version: 1, Cases: []Case{c, c}}
	if err := ds.Validate(); err == nil || !strings.Contains(err.Error(), "duplicate case_id") {
		t.Fatalf("expected duplicate error, got %v", err)
	}

	ds.Cases[1].CaseID = "other"
	ds.Cases[1].Type = "unknown"
	if err := ds.Validate(); err == nil || !strings.Contains(err.Error(), "type") {
		t.Fatalf("expected type error, got %v", err)
	}
}

func TestValidateSize(t *testing.T) {
	ds := Dataset{Version: 1, Cases: make([]Case, MinCases-1)}
	if err := ds.ValidateSize(); err == nil {
		t.Fatal("expected size error below minimum")
	}
	ds.Cases = make([]Case, MaxCases+1)
	if err := ds.ValidateSize(); err == nil {
		t.Fatal("expected size error above maximum")
	}
}

func TestWriteReportUsesTimestampedName(t *testing.T) {
	dir := t.TempDir()
	report := Report{
		GeneratedAt:   time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC),
		Provider:      "heuristic",
		Model:         "fake/heuristic",
		PromptVersion: "eval-v2",
	}
	path, err := WriteReport(report, dir)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	name := filepath.Base(path)
	if name != "eval-heuristic-fake-heuristic-eval-v2-20261106T120000Z.json" {
		t.Fatalf("unexpected file name %q", name)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("result file missing: %v", err)
	}
}
