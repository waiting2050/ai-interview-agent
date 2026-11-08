package knowledge

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestEmbedderKeepsInputOrderAcrossBatches(t *testing.T) {
	var batches int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req embedRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		batches++
		resp := embedResponse{}
		for i := range req.Input {
			resp.Data = append(resp.Data, struct {
				Index     int       `json:"index"`
				Embedding []float32 `json:"embedding"`
			}{Index: i, Embedding: []float32{float32(batches), float32(i)}})
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	embedder := NewEmbedder(EmbeddingConfig{
		Provider: "ollama", BaseURL: server.URL, Model: "m", Dim: 2, Batch: 2,
		Timeout: time.Second,
	}, nil)
	vectors, err := embedder.Embed(context.Background(), []string{"a", "b", "c"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vectors) != 3 || batches != 2 {
		t.Fatalf("vectors=%d batches=%d", len(vectors), batches)
	}
	if vectors[2][0] != 2 || vectors[2][1] != 0 {
		t.Fatalf("batch order lost: %v", vectors[2])
	}
}

func TestEmbedderRejectsDimensionMismatch(t *testing.T) {
	server := httptest.NewServer(embedHandler(8))
	defer server.Close()

	embedder := NewEmbedder(EmbeddingConfig{
		Provider: "ollama", BaseURL: server.URL, Model: "m", Dim: 1024, Timeout: time.Second,
	}, nil)
	_, err := embedder.Embed(context.Background(), []string{"x"})
	if !errors.Is(err, ErrEmbeddingDimension) {
		t.Fatalf("error = %v, want ErrEmbeddingDimension", err)
	}
}

func TestEmbedderHTTPErrorIsUnavailable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusBadGateway)
	}))
	defer server.Close()

	embedder := NewEmbedder(EmbeddingConfig{
		Provider: "openai", BaseURL: server.URL, APIKey: "k", Model: "m", Dim: 1024, Timeout: time.Second,
	}, nil)
	_, err := embedder.Embed(context.Background(), []string{"x"})
	if !errors.Is(err, ErrEmbeddingUnavailable) {
		t.Fatalf("error = %v, want ErrEmbeddingUnavailable", err)
	}
}

func TestEmbedderDisabledAndCost(t *testing.T) {
	embedder := NewEmbedder(EmbeddingConfig{Provider: "none", Model: "m", Dim: 1024}, nil)
	if embedder.Enabled() {
		t.Fatal("provider none must be disabled")
	}
	if err := embedder.Probe(context.Background()); !errors.Is(err, ErrEmbeddingUnavailable) {
		t.Fatalf("probe error = %v", err)
	}

	paid := NewEmbedder(EmbeddingConfig{Provider: "openai", BaseURL: "http://x", Model: "m", Dim: 1024, PricePerMTOK: 0.02}, nil)
	if got := paid.CostUSD(1_000_000); got != 0.02 {
		t.Fatalf("cost = %v, want 0.02", got)
	}
	if got := embedder.CostUSD(1_000_000); got != 0 {
		t.Fatalf("local embedding must cost zero, got %v", got)
	}
}
