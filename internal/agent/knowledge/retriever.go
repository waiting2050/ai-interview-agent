package knowledge

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"
)

// StoredChunk is a chunk plus whether its vector is already persisted. Ingest
// uses HasEmbedding to skip unchanged rows.
type StoredChunk struct {
	Chunk
	HasEmbedding bool
}

// VectorHit is one approximate-nearest-neighbour result.
type VectorHit struct {
	ChunkID string
	Cosine  float64
	Rank    int
}

// ChunkStore is the persistence seam. The Postgres implementation owns SQL;
// tests use an in-memory double.
type ChunkStore interface {
	LoadChunks(ctx context.Context) ([]StoredChunk, error)
	SearchVectors(ctx context.Context, vector []float32, limit int) ([]VectorHit, error)
}

// Options configure the retriever. Zero values fall back to the documented
// defaults so tests can keep the calls short.
type Options struct {
	Mode      Mode
	TopK      int
	Candidate int
	RRFK      int
	// BM25Weight and VectorWeight scale each arm's RRF contribution. Equal
	// weights preserve textbook RRF; a larger lexical weight is an empirical
	// preset, not a normalisation.
	BM25Weight   float64
	VectorWeight float64
	Logger       *slog.Logger
}

func (o *Options) normalize() {
	if o.TopK <= 0 {
		o.TopK = 6
	}
	if o.Candidate <= 0 {
		o.Candidate = 20
	}
	if o.RRFK <= 0 {
		o.RRFK = 60
	}
	if o.BM25Weight <= 0 {
		o.BM25Weight = 1
	}
	if o.VectorWeight <= 0 {
		o.VectorWeight = 1
	}
	if o.Mode == "" {
		o.Mode = ModeBM25
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
}

// warmState tracks the one-shot embedding probe. It is separate from chunk
// loading: chunks are loaded lazily, the model warms up eagerly.
type warmState int

const (
	warmCold warmState = iota
	warmRunning
	warmReady
	warmFailed
)

// Retriever answers one shipped query from the in-memory BM25 index and, when
// the embedding endpoint is ready, fuses it with pgvector cosine results.
type Retriever struct {
	store    ChunkStore
	embedder *Embedder
	opts     Options

	mu     sync.RWMutex
	chunks []Chunk
	index  *BM25Index
	loaded bool

	state       warmState
	readyReason string
	warmErr     error
	warmDone    chan struct{}
}

func NewRetriever(store ChunkStore, embedder *Embedder, opts Options) *Retriever {
	opts.normalize()
	return &Retriever{store: store, embedder: embedder, opts: opts}
}

// Init starts the embedding warm-up in the background and returns immediately.
// A cold local model can take ~25s to load, so blocking startup would make the
// process look hung; instead the mode is "warming" until the probe answers.
func (r *Retriever) Init(ctx context.Context) {
	switch r.opts.Mode {
	case ModeOff:
		r.setState(warmFailed, "RAG_MODE=off", nil)
		return
	case ModeBM25:
		r.setState(warmFailed, "RAG_MODE=bm25", nil)
		return
	}
	if r.embedder == nil || !r.embedder.Enabled() {
		r.setState(warmFailed, "embedding provider disabled", nil)
		return
	}

	drained := make(chan struct{})
	r.mu.Lock()
	r.state = warmRunning
	r.warmDone = drained
	r.mu.Unlock()

	go func() {
		defer close(drained)
		probeCtx, cancel := context.WithTimeout(context.Background(), r.embedder.ProbeTimeout())
		defer cancel()
		err := r.embedder.Probe(probeCtx)
		if err != nil {
			r.setState(warmFailed, err.Error(), err)
			r.opts.Logger.Warn("embedding warm-up failed, retrieval degrades to bm25",
				"reason", err.Error(), "base_url", r.embedder.cfg.BaseURL,
				"model", r.embedder.cfg.Model, "timeout", r.embedder.ProbeTimeout())
			return
		}
		r.setState(warmReady, "embedding endpoint ready", nil)
		r.opts.Logger.Info("embedding warm-up finished", "model", r.embedder.cfg.Model,
			"dim", r.embedder.cfg.Dim, "base_url", r.embedder.cfg.BaseURL)
	}()
}

func (r *Retriever) setState(state warmState, reason string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.state = state
	r.readyReason = reason
	r.warmErr = err
	r.warmDone = nil
}

// WaitReady blocks until the warm-up finishes. Callers use it to avoid
// reporting a fallback while the model is still loading.
func (r *Retriever) WaitReady(ctx context.Context) error {
	r.mu.RLock()
	done, state, warmErr := r.warmDone, r.state, r.warmErr
	r.mu.RUnlock()
	if done == nil || state == warmReady || state == warmFailed {
		return warmErr
	}
	select {
	case <-done:
		r.mu.RLock()
		defer r.mu.RUnlock()
		return r.warmErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Mode returns the effective mode: hybrid only once the warm-up succeeded.
func (r *Retriever) Mode() Mode {
	switch r.opts.Mode {
	case ModeOff:
		return ModeOff
	case ModeBM25:
		return ModeBM25
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	switch r.state {
	case warmReady:
		return ModeHybrid
	case warmRunning:
		return ModeWarming
	default:
		return ModeBM25Fallback
	}
}

// Reason explains a non-hybrid mode in one human-readable line.
func (r *Retriever) Reason() string {
	if r.opts.Mode == ModeOff || r.opts.Mode == ModeBM25 {
		return r.readyReason
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.state == warmReady {
		return ""
	}
	if r.state == warmRunning {
		return "embedding model warming up"
	}
	if r.readyReason == "" {
		return "embedding warm-up not started"
	}
	return r.readyReason
}

// Status is the payload exposed on /healthz and the retrieval badge.
type Status struct {
	ConfiguredMode string         `json:"configured_mode"`
	Mode           string         `json:"mode"`
	Reason         string         `json:"reason,omitempty"`
	Chunks         int            `json:"chunks"`
	EmbeddingReady bool           `json:"embedding_ready"`
	Embedding      *EmbeddingInfo `json:"embedding,omitempty"`
}

func (r *Retriever) Status() Status {
	r.mu.RLock()
	chunks := len(r.chunks)
	embeddingReady := r.state == warmReady
	r.mu.RUnlock()
	st := Status{
		ConfiguredMode: string(r.opts.Mode),
		Mode:           string(r.Mode()),
		Reason:         r.Reason(),
		Chunks:         chunks,
		EmbeddingReady: embeddingReady,
	}
	if r.embedder != nil {
		info := r.embedder.Info()
		st.Embedding = &info
	}
	return st
}

// Refresh reloads chunks and rebuilds the BM25 index. Ingest calls it after a
// successful apply; the server calls it at startup.
func (r *Retriever) Refresh(ctx context.Context) error {
	if r.store == nil {
		return fmt.Errorf("retriever has no chunk store")
	}
	stored, err := r.store.LoadChunks(ctx)
	if err != nil {
		return fmt.Errorf("load chunks: %w", err)
	}
	chunks := make([]Chunk, 0, len(stored))
	for _, s := range stored {
		chunks = append(chunks, s.Chunk)
	}
	index := NewBM25Index(chunks)
	r.mu.Lock()
	r.chunks = chunks
	r.index = index
	r.loaded = true
	r.mu.Unlock()
	return nil
}

func (r *Retriever) ensureLoaded(ctx context.Context) error {
	r.mu.RLock()
	loaded := r.loaded
	r.mu.RUnlock()
	if loaded {
		return nil
	}
	return r.Refresh(ctx)
}

// Search runs the configured mode. Every degraded path is returned in Result
// so callers can commit it to an event rather than only writing a log line.
func (r *Retriever) Search(ctx context.Context, query string) (result Result, err error) {
	start := time.Now()
	result = Result{Mode: r.Mode(), TopK: r.opts.TopK, Reason: r.Reason()}
	// Named results: a deferred write only reaches the caller when the value is
	// a result parameter, not a local copy.
	defer func() {
		result.Latency = time.Since(start)
		result.LatencyMS = result.Latency.Milliseconds()
	}()

	query = strings.TrimSpace(query)
	if query == "" || result.Mode == ModeOff {
		if result.Mode == ModeOff {
			result.Reason = "RAG_MODE=off"
		} else {
			result.Reason = "empty query"
		}
		return result, nil
	}
	// A query that arrives during warm-up waits for the model instead of
	// silently claiming a fallback. If the caller's context expires first the
	// response keeps mode=warming and says it served BM25 candidates.
	if result.Mode == ModeWarming {
		_ = r.WaitReady(ctx)
		result.Mode = r.Mode()
		result.Reason = r.Reason()
	}
	if err := r.ensureLoaded(ctx); err != nil {
		return result, err
	}

	r.mu.RLock()
	index := r.index
	r.mu.RUnlock()

	bm25Hits := index.Search(query, r.opts.Candidate)
	result.BM25Candidates = len(bm25Hits)

	hits := hitsFromBM25(index, bm25Hits)
	if result.Mode != ModeHybrid {
		result.Hits = finalize(hits, r.opts.TopK, 0)
		return result, nil
	}

	vectors, err := r.embedder.Embed(ctx, []string{query})
	if err != nil {
		// The startup probe passed but the per-query call failed. Degrade this
		// query explicitly instead of returning an empty result.
		result.Mode = ModeBM25Fallback
		result.Reason = err.Error()
		r.opts.Logger.Warn("embedding request failed, query degraded to bm25", "reason", result.Reason)
		result.Hits = finalize(hits, r.opts.TopK, 0)
		return result, nil
	}
	vectorHits, err := r.store.SearchVectors(ctx, vectors[0], r.opts.Candidate)
	if err != nil {
		result.Mode = ModeBM25Fallback
		result.Reason = err.Error()
		r.opts.Logger.Warn("vector search failed, query degraded to bm25", "reason", result.Reason)
		result.Hits = finalize(hits, r.opts.TopK, 0)
		return result, nil
	}
	result.VectorCandidates = len(vectorHits)

	fused := rrfFuse(hits, vectorHits, r.opts.RRFK, r.opts.BM25Weight, r.opts.VectorWeight, r.chunkMap())
	result.Hits = finalize(fused, r.opts.TopK, r.opts.RRFK)
	return result, nil
}

// SearchBM25Only is the evaluation arm. It never touches the embedding
// endpoint so BM25 and hybrid numbers are measured on the same chunks and
// top-k.
func (r *Retriever) SearchBM25Only(ctx context.Context, query string) (result Result, err error) {
	start := time.Now()
	result = Result{Mode: ModeBM25Only, TopK: r.opts.TopK}
	defer func() {
		result.Latency = time.Since(start)
		result.LatencyMS = result.Latency.Milliseconds()
	}()
	if err := r.ensureLoaded(ctx); err != nil {
		return result, err
	}
	r.mu.RLock()
	index := r.index
	r.mu.RUnlock()
	bm25Hits := index.Search(query, r.opts.Candidate)
	result.BM25Candidates = len(bm25Hits)
	result.Hits = finalize(hitsFromBM25(index, bm25Hits), r.opts.TopK, 0)
	return result, nil
}

func hitsFromBM25(index *BM25Index, bm25 []BM25Hit) map[string]*Hit {
	out := make(map[string]*Hit, len(bm25))
	for _, h := range bm25 {
		for i := range index.chunks {
			if index.chunks[i].ID != h.ChunkID {
				continue
			}
			out[h.ChunkID] = hitFromChunk(index.chunks[i])
			out[h.ChunkID].BM25Score = h.Score
			out[h.ChunkID].BM25Rank = h.Rank
			break
		}
	}
	return out
}

func hitFromChunk(chunk Chunk) *Hit {
	return &Hit{
		ChunkID:    chunk.ID,
		SourcePath: chunk.SourcePath,
		SectionKey: chunk.SectionKey,
		SourceKind: chunk.SourceKind,
		Domain:     chunk.Domain,
		TopicID:    chunk.TopicID,
		Title:      chunk.Title,
		Snippet:    truncate(chunk.Content, 320),
	}
}

// rrfFuse applies Reciprocal Rank Fusion: score = Σ weight/(k+rank). RRF is
// used instead of normalised weighted scores because BM25 has no upper bound
// while cosine similarity is bounded; a fixed normalisation would make the
// mixture sensitive to document length and corpus size. The arm weights are
// preset multipliers, not a learned or normalised combination.
func rrfFuse(bm25 map[string]*Hit, vectors []VectorHit, k int, bm25Weight, vectorWeight float64, chunks map[string]Chunk) map[string]*Hit {
	if bm25Weight <= 0 {
		bm25Weight = 1
	}
	if vectorWeight <= 0 {
		vectorWeight = 1
	}
	fused := make(map[string]*Hit, len(bm25)+len(vectors))
	for id, hit := range bm25 {
		copied := *hit
		copied.RRFScore = bm25Weight / float64(k+hit.BM25Rank)
		fused[id] = &copied
	}
	for _, v := range vectors {
		hit, ok := fused[v.ChunkID]
		if !ok {
			chunk, exists := chunks[v.ChunkID]
			if !exists {
				continue
			}
			hit = hitFromChunk(chunk)
			fused[v.ChunkID] = hit
		}
		hit.Cosine = v.Cosine
		hit.VectorRank = v.Rank
		hit.RRFScore += vectorWeight / float64(k+v.Rank)
	}
	return fused
}

func (r *Retriever) chunkMap() map[string]Chunk {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]Chunk, len(r.chunks))
	for _, chunk := range r.chunks {
		out[chunk.ID] = chunk
	}
	return out
}

// finalize orders the fused map and drops candidates whose chunk body was not
// loaded (should not happen, but an orphan vector row must not surface a
// half-empty hit).
func finalize(hits map[string]*Hit, topK int, rrfK int) []Hit {
	if len(hits) == 0 {
		return nil
	}
	out := make([]Hit, 0, len(hits))
	for _, hit := range hits {
		if hit.Title == "" {
			continue
		}
		out = append(out, *hit)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].RRFScore != out[j].RRFScore {
			return out[i].RRFScore > out[j].RRFScore
		}
		if out[i].BM25Score != out[j].BM25Score {
			return out[i].BM25Score > out[j].BM25Score
		}
		if out[i].Cosine != out[j].Cosine {
			return out[i].Cosine > out[j].Cosine
		}
		return out[i].ChunkID < out[j].ChunkID
	})
	if topK > 0 && len(out) > topK {
		out = out[:topK]
	}
	return out
}
