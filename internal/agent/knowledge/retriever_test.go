package knowledge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

type fakeStore struct {
	chunks    []StoredChunk
	vectors   []VectorHit
	loadErr   error
	vectorErr error
}

func (f *fakeStore) LoadChunks(context.Context) ([]StoredChunk, error) {
	return f.chunks, f.loadErr
}

func (f *fakeStore) SearchVectors(context.Context, []float32, int) ([]VectorHit, error) {
	if f.vectorErr != nil {
		return nil, f.vectorErr
	}
	return f.vectors, nil
}

func embedHandler(dim int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req embedRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		resp := embedResponse{}
		for i := range req.Input {
			vec := make([]float32, dim)
			vec[0] = 1
			resp.Data = append(resp.Data, struct {
				Index     int       `json:"index"`
				Embedding []float32 `json:"embedding"`
			}{Index: i, Embedding: vec})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}
}

func storedTestChunks() []StoredChunk {
	chunks := testChunks()
	out := make([]StoredChunk, 0, len(chunks))
	for _, c := range chunks {
		out = append(out, StoredChunk{Chunk: c, HasEmbedding: true})
	}
	return out
}

func newTestRetriever(t *testing.T, mode Mode, store ChunkStore, baseURL string) *Retriever {
	t.Helper()
	embedder := NewEmbedder(EmbeddingConfig{
		Provider: "ollama", BaseURL: baseURL, Model: "test", Dim: 1024, Batch: 2,
		Timeout: 2 * time.Second, ProbeTimeout: 2 * time.Second,
	}, nil)
	r := NewRetriever(store, embedder, Options{Mode: mode, TopK: 3, Candidate: 10, RRFK: 60})
	r.Init(context.Background())
	// Init warms up in the background; tests that assert the final mode must
	// wait for the probe to settle. Failure is expected in fallback tests.
	_ = r.WaitReady(context.Background())
	return r
}

func TestRetrieverWarmingIsExplicitAndBlocksSearch(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		embedHandler(1024)(w, r)
	}))
	defer server.Close()

	embedder := NewEmbedder(EmbeddingConfig{
		Provider: "ollama", BaseURL: server.URL, Model: "test", Dim: 1024, Batch: 2,
		Timeout: 2 * time.Second, ProbeTimeout: 5 * time.Second,
	}, nil)
	r := NewRetriever(&fakeStore{chunks: storedTestChunks()}, embedder,
		Options{Mode: ModeHybrid, TopK: 3, Candidate: 10, RRFK: 60})
	r.Init(context.Background())

	if r.Mode() != ModeWarming {
		t.Fatalf("mode = %s, want warming while the model loads", r.Mode())
	}
	if r.Status().Mode != string(ModeWarming) || r.Reason() == "" {
		t.Fatalf("status must expose warming: %+v", r.Status())
	}

	// A query during warm-up waits for the probe instead of silently degrading.
	done := make(chan closeReason, 1)
	go func() {
		res, err := r.Search(context.Background(), "goroutine 调度")
		done <- closeReason{result: res, err: err}
	}()
	time.Sleep(50 * time.Millisecond)
	close(release)
	got := <-done
	if got.err != nil {
		t.Fatalf("search: %v", got.err)
	}
	if got.result.Mode != ModeHybrid {
		t.Fatalf("first query mode = %s, want hybrid after warm-up", got.result.Mode)
	}
}

func TestRetrieverWarmingSurvivesCallerDeadline(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		embedHandler(1024)(w, r)
	}))
	defer server.Close()

	embedder := NewEmbedder(EmbeddingConfig{
		Provider: "ollama", BaseURL: server.URL, Model: "test", Dim: 1024, Batch: 2,
		Timeout: 2 * time.Second, ProbeTimeout: 5 * time.Second,
	}, nil)
	r := NewRetriever(&fakeStore{chunks: storedTestChunks()}, embedder,
		Options{Mode: ModeHybrid, TopK: 3, Candidate: 10, RRFK: 60})
	r.Init(context.Background())

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	res, err := r.Search(ctx, "goroutine 调度")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if res.Mode != ModeWarming {
		t.Fatalf("mode = %s, want explicit warming (never a silent fallback)", res.Mode)
	}
	if res.Reason == "" || len(res.Hits) == 0 {
		t.Fatalf("warming response must keep bm25 hits and a reason: %+v", res)
	}

	close(release)
	_ = r.WaitReady(context.Background())
}

// closeReason is a small struct so the goroutine can return both values.
type closeReason struct {
	result Result
	err    error
}

func TestRetrieverBM25ModeNeverCallsEmbedding(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("bm25 mode must not call embedding")
	}))
	defer server.Close()

	r := newTestRetriever(t, ModeBM25, &fakeStore{chunks: storedTestChunks()}, server.URL)
	if r.Mode() != ModeBM25 {
		t.Fatalf("mode = %s, want bm25", r.Mode())
	}
	res, err := r.Search(context.Background(), "goroutine 调度")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if res.Mode != ModeBM25 || len(res.Hits) == 0 || res.Hits[0].ChunkID != "a" {
		t.Fatalf("unexpected result: %+v", res)
	}
}

func TestRetrieverHybridFusesVectorOnlyChunk(t *testing.T) {
	server := httptest.NewServer(embedHandler(1024))
	defer server.Close()

	chunks := storedTestChunks()
	chunks = append(chunks, StoredChunk{Chunk: Chunk{ID: "d", Title: "向量召回", Content: "向量检索补充的片段"}, HasEmbedding: true})
	store := &fakeStore{chunks: chunks, vectors: []VectorHit{{ChunkID: "d", Cosine: 0.9, Rank: 1}, {ChunkID: "a", Cosine: 0.5, Rank: 2}}}

	r := newTestRetriever(t, ModeHybrid, store, server.URL)
	if r.Mode() != ModeHybrid {
		t.Fatalf("mode = %s, want hybrid (reason %s)", r.Mode(), r.Reason())
	}
	res, err := r.Search(context.Background(), "goroutine 调度")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if res.Mode != ModeHybrid {
		t.Fatalf("result mode = %s", res.Mode)
	}
	if len(res.Hits) == 0 {
		t.Fatal("no hits")
	}
	seen := map[string]Hit{}
	for _, hit := range res.Hits {
		seen[hit.ChunkID] = hit
	}
	a, ok := seen["a"]
	if !ok || a.BM25Rank == 0 {
		t.Fatalf("bm25 arm missing: %+v", seen)
	}
	d, ok := seen["d"]
	if !ok || d.VectorRank == 0 || d.Title != "向量召回" {
		t.Fatalf("vector-only candidate not enriched: %+v", seen)
	}
	// Rank 1 in both arms must have the largest RRF score (2/(60+1)).
	if a.RRFScore <= d.RRFScore {
		t.Fatalf("rrf ordering unexpected: a=%f d=%f", a.RRFScore, d.RRFScore)
	}
}

func TestRetrieverFallsBackWhenProbeFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "model not loaded", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	r := newTestRetriever(t, ModeHybrid, &fakeStore{chunks: storedTestChunks()}, server.URL)
	if r.Mode() != ModeBM25Fallback {
		t.Fatalf("mode = %s, want bm25_fallback", r.Mode())
	}
	res, err := r.Search(context.Background(), "goroutine 调度")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if res.Mode != ModeBM25Fallback || len(res.Hits) == 0 {
		t.Fatalf("fallback must still answer with bm25: %+v", res)
	}
	if res.Reason == "" {
		t.Fatal("fallback reason must be visible")
	}
	if !res.Empty() && res.Hits[0].BM25Rank == 0 {
		t.Fatal("fallback hits must carry bm25 ranks")
	}
}

func TestRetrieverFallsBackOnQueryEmbeddingFailure(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			embedHandler(1024)(w, r)
			return
		}
		http.Error(w, "temporary failure", http.StatusInternalServerError)
	}))
	defer server.Close()

	r := newTestRetriever(t, ModeHybrid, &fakeStore{chunks: storedTestChunks()}, server.URL)
	if r.Mode() != ModeHybrid {
		t.Fatalf("probe should have succeeded, mode = %s", r.Mode())
	}
	res, err := r.Search(context.Background(), "MySQL 索引")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if res.Mode != ModeBM25Fallback {
		t.Fatalf("query-time failure must degrade explicitly, mode = %s", res.Mode)
	}
	if len(res.Hits) == 0 || res.Hits[0].ChunkID != "b" {
		t.Fatalf("degraded query lost bm25 results: %+v", res)
	}
	if res.Reason == "" {
		t.Fatal("degraded query must expose a reason")
	}
}

func TestRetrieverFallsBackWhenVectorSearchFails(t *testing.T) {
	server := httptest.NewServer(embedHandler(1024))
	defer server.Close()

	store := &fakeStore{chunks: storedTestChunks(), vectorErr: context.DeadlineExceeded}
	r := newTestRetriever(t, ModeHybrid, store, server.URL)
	res, err := r.Search(context.Background(), "goroutine 调度")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if res.Mode != ModeBM25Fallback || len(res.Hits) == 0 {
		t.Fatalf("vector failure must fall back to bm25: %+v", res)
	}
}

func TestRetrieverOffAndNoHit(t *testing.T) {
	r := newTestRetriever(t, ModeOff, &fakeStore{chunks: storedTestChunks()}, "")
	if r.Mode() != ModeOff {
		t.Fatalf("mode = %s", r.Mode())
	}
	res, err := r.Search(context.Background(), "goroutine")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(res.Hits) != 0 || res.Mode != ModeOff {
		t.Fatalf("off mode returned hits: %+v", res)
	}

	r = newTestRetriever(t, ModeBM25, &fakeStore{chunks: storedTestChunks()}, "")
	res, err = r.Search(context.Background(), "完全不相关的检索词")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(res.Hits) != 0 {
		t.Fatalf("no-hit query returned %d hits", len(res.Hits))
	}
}
