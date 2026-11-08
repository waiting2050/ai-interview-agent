package knowledge

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
)

// IngestPlan is the diff between the built corpus and stored chunks.
type IngestPlan struct {
	Changed []Chunk
	Stale   []string
}

// PlanIngest decides what to write. A chunk is changed when it is new, its
// content hash moved, its vector is missing, or force is set. A stored chunk
// absent from the corpus is stale. Pure function so the idempotency rules are
// unit-testable without Postgres.
func PlanIngest(existing []StoredChunk, chunks []Chunk, force bool) IngestPlan {
	known := make(map[string]StoredChunk, len(existing))
	current := make(map[string]bool, len(chunks))
	for _, row := range existing {
		known[row.ID] = row
	}

	var plan IngestPlan
	for _, chunk := range chunks {
		current[chunk.ID] = true
		stored, ok := known[chunk.ID]
		if force || !ok || stored.ContentHash != chunk.ContentHash || !stored.HasEmbedding {
			plan.Changed = append(plan.Changed, chunk)
		}
	}
	for _, row := range existing {
		if !current[row.ID] {
			plan.Stale = append(plan.Stale, row.ID)
		}
	}
	sort.Strings(plan.Stale)
	return plan
}

// CorpusHash is the idempotency key for a full ingest. It covers source path,
// section key and content hash so an unchanged corpus produces the same value
// across rebuilds.
func CorpusHash(chunks []Chunk) string {
	lines := make([]string, 0, len(chunks))
	for _, chunk := range chunks {
		lines = append(lines, chunk.SourcePath+"#"+chunk.SectionKey+"#"+chunk.ContentHash)
	}
	sort.Strings(lines)
	sum := sha256.Sum256([]byte(joinLines(lines)))
	return hex.EncodeToString(sum[:])
}

func joinLines(lines []string) string {
	var out []byte
	for _, line := range lines {
		out = append(out, line...)
		out = append(out, '\n')
	}
	return string(out)
}
