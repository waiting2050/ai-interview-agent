// Package knowledge owns the retrieval corpus: semantic chunking, a
// dependency-free BM25 index and the hybrid (BM25 + pgvector) retriever.
package knowledge

import (
	"time"
)

// Chunk is one retrievable unit. Chunks follow semantic boundaries: one
// interview topic, or one Markdown section (split further only at paragraph
// boundaries when a section is too long).
type Chunk struct {
	ID          string
	SourcePath  string
	SectionKey  string
	SourceKind  string
	Domain      string
	TopicID     string
	Title       string
	Content     string
	ContentHash string
}

// Hit is one retrieval result. BM25Rank / VectorRank are 1-based and zero
// when the candidate did not come from that arm; RRF is the fused score.
type Hit struct {
	ChunkID    string  `json:"chunk_id"`
	SourcePath string  `json:"source_path"`
	SectionKey string  `json:"section_key"`
	SourceKind string  `json:"source_kind"`
	Domain     string  `json:"domain"`
	TopicID    string  `json:"topic_id"`
	Title      string  `json:"title"`
	Snippet    string  `json:"snippet"`
	BM25Score  float64 `json:"bm25_score,omitempty"`
	Cosine     float64 `json:"cosine,omitempty"`
	BM25Rank   int     `json:"bm25_rank,omitempty"`
	VectorRank int     `json:"vector_rank,omitempty"`
	RRFScore   float64 `json:"rrf_score"`
}

// Mode is the retrieval mode actually used for a query. It is surfaced in the
// API payload, the UI badge and the logs so a fallback is never silent.
type Mode string

const (
	ModeOff    Mode = "off"
	ModeBM25   Mode = "bm25"
	ModeHybrid Mode = "hybrid"
	// ModeWarming means the embedding model is still loading. Queries are
	// served by BM25 but the response says so instead of claiming a fallback.
	ModeWarming      Mode = "warming"
	ModeBM25Fallback Mode = "bm25_fallback"
	// ModeBM25Only is the retrieval-quality evaluation label for an explicit
	// BM25 run; it is never produced by the production retriever.
	ModeBM25Only Mode = "bm25_only"
)

// Result is one query result with the evidence needed to explain the mode.
type Result struct {
	Mode             Mode          `json:"mode"`
	Reason           string        `json:"reason,omitempty"`
	Hits             []Hit         `json:"hits"`
	Latency          time.Duration `json:"-"`
	LatencyMS        int64         `json:"latency_ms"`
	BM25Candidates   int           `json:"bm25_candidates"`
	VectorCandidates int           `json:"vector_candidates"`
	TopK             int           `json:"top_k"`
}

// Empty reports whether the result carries no usable hits.
func (r Result) Empty() bool { return len(r.Hits) == 0 }
