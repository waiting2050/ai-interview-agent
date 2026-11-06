package syllabus

import (
	"strings"
	"testing"
)

func TestBuiltinsHaveExpectedPoints(t *testing.T) {
	for _, direction := range Directions() {
		outline, ok := Builtin(direction)
		if !ok {
			t.Fatalf("missing builtin %s", direction)
		}
		if len(outline.Items) < 5 {
			t.Fatalf("%s has only %d items", direction, len(outline.Items))
		}
		seen := map[string]struct{}{}
		for _, item := range outline.Items {
			if _, dup := seen[item.ID]; dup {
				t.Fatalf("%s duplicate id %s", direction, item.ID)
			}
			seen[item.ID] = struct{}{}
			if item.Topic == "" || item.Objective == "" {
				t.Fatalf("%s item incomplete: %+v", direction, item)
			}
			if len(item.ExpectedPoints) < 2 {
				t.Fatalf("%s item %s needs at least 2 expected points", direction, item.ID)
			}
		}
	}
}

func TestDetectFindsMultipleDirections(t *testing.T) {
	got := Detect("Go 后端开发，熟悉 MySQL 与 Redis")
	want := []string{DirectionGo, DirectionMySQL, DirectionRedis}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("detect = %v, want %v", got, want)
	}
}

func TestPlanMergesDirectionsAndRespectsLimit(t *testing.T) {
	outline := Plan("Go 后端，熟悉 MySQL 和 Redis", "mid", 6)
	if outline.Direction != "go+mysql+redis" {
		t.Fatalf("direction = %s", outline.Direction)
	}
	if len(outline.Items) != 6 {
		t.Fatalf("items = %d, want 6", len(outline.Items))
	}
	topics := map[string]struct{}{}
	for _, item := range outline.Items {
		if _, dup := topics[item.Topic]; dup {
			t.Fatalf("duplicate topic %s", item.Topic)
		}
		topics[item.Topic] = struct{}{}
	}
}

func TestPlanFallsBackToGo(t *testing.T) {
	outline := Plan("", "senior", 3)
	if outline.Direction != DirectionGo {
		t.Fatalf("direction = %s, want go", outline.Direction)
	}
	if len(outline.Items) != 3 {
		t.Fatalf("items = %d", len(outline.Items))
	}
}

func TestPlanSelectsArchitectureItems(t *testing.T) {
	outline := Plan("Go 后端开发工程师", "mid", 3)
	var topics []string
	arch := 0
	for _, item := range outline.Items {
		topics = append(topics, item.Topic)
		if strings.HasPrefix(item.ID, "go-arch-") {
			arch++
		}
	}
	if arch == 0 {
		t.Fatalf("limit=3 plan has no architecture item: %v", topics)
	}
	t.Logf("limit=3 sample: %s", strings.Join(topics, " / "))

	// All five architecture items must be reachable when the plan is large
	// enough, and each must feed the coverage gate through expected points.
	full := Plan("Go 后端开发工程师", "mid", 12)
	seen := map[string]bool{}
	for _, item := range full.Items {
		if !strings.HasPrefix(item.ID, "go-arch-") {
			continue
		}
		seen[item.ID] = true
		if len(item.ExpectedPoints) < 2 {
			t.Fatalf("%s has too few expected points: %v", item.ID, item.ExpectedPoints)
		}
	}
	if len(seen) != 5 {
		t.Fatalf("architecture items selected = %d, want 5 (%v)", len(seen), seen)
	}
}

func TestPlanPrioritizesMatchingDifficulty(t *testing.T) {
	outline := Plan("mysql", "junior", 8)
	for _, item := range outline.Items {
		if item.Difficulty == "senior" {
			continue
		}
		if item.Difficulty != "junior" && item.Difficulty != "mid" {
			t.Fatalf("unexpected difficulty %q", item.Difficulty)
		}
	}
}
