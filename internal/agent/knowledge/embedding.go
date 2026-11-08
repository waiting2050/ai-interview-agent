package knowledge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// Embedding errors are typed so the caller can distinguish "no endpoint" from
// "bad response" and expose the real reason in logs and the API.
var (
	ErrEmbeddingUnavailable = errors.New("embedding endpoint unavailable")
	ErrEmbeddingDimension   = errors.New("embedding dimension mismatch")
)

// EmbeddingConfig is the resolved embedding configuration. PricePerMTOK is
// zero for local Ollama; a remote provider sets it so the ingest command can
// report spend.
type EmbeddingConfig struct {
	Provider string
	BaseURL  string
	APIKey   string
	Model    string
	Dim      int
	Batch    int
	Timeout  time.Duration
	// ProbeTimeout covers the first call after a model load. A cold local model
	// takes ~25s, so the startup probe must not reuse the short request timeout.
	ProbeTimeout time.Duration
	PricePerMTOK float64
}

// EmbeddingInfo is safe to log and to expose on /healthz.
type EmbeddingInfo struct {
	Provider     string  `json:"provider"`
	Model        string  `json:"model"`
	BaseURL      string  `json:"base_url"`
	Dim          int     `json:"dim"`
	PricePerMTOK float64 `json:"price_per_mtok"`
}

// Embedder is an OpenAI-compatible embeddings client. Ollama exposes the same
// /v1/embeddings shape, so one code path serves both the local default and an
// optional remote provider.
type Embedder struct {
	cfg  EmbeddingConfig
	http *http.Client
	log  *slog.Logger
}

func NewEmbedder(cfg EmbeddingConfig, log *slog.Logger) *Embedder {
	if log == nil {
		log = slog.Default()
	}
	if cfg.Batch <= 0 {
		cfg.Batch = 16
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 60 * time.Second
	}
	if cfg.ProbeTimeout <= 0 {
		cfg.ProbeTimeout = 45 * time.Second
	}
	return &Embedder{
		cfg:  cfg,
		http: &http.Client{Timeout: cfg.Timeout},
		log:  log,
	}
}

func (e *Embedder) Info() EmbeddingInfo {
	return EmbeddingInfo{
		Provider:     e.cfg.Provider,
		Model:        e.cfg.Model,
		BaseURL:      e.cfg.BaseURL,
		Dim:          e.cfg.Dim,
		PricePerMTOK: e.cfg.PricePerMTOK,
	}
}

// Enabled reports whether a real endpoint is configured.
func (e *Embedder) Enabled() bool {
	return e != nil && e.cfg.Provider != "none" && e.cfg.BaseURL != ""
}

// ProbeTimeout is how long the first embedding call may take. Callers use the
// same value as the background warm-up budget.
func (e *Embedder) ProbeTimeout() time.Duration {
	if e == nil {
		return 45 * time.Second
	}
	return e.cfg.ProbeTimeout
}

// Probe makes one real embedding call. Startup uses it to decide between
// hybrid and BM25: a reachable endpoint means hybrid, any failure means the
// explicit bm25_fallback mode.
func (e *Embedder) Probe(ctx context.Context) error {
	if !e.Enabled() {
		return fmt.Errorf("%w: provider is none", ErrEmbeddingUnavailable)
	}
	probeCtx, cancel := context.WithTimeout(ctx, e.cfg.ProbeTimeout)
	defer cancel()
	_, err := e.Embed(probeCtx, []string{"检索可用性探测"})
	return err
}

// Embed batches the input and returns one vector per text, in input order.
func (e *Embedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	out := make([][]float32, 0, len(texts))
	for start := 0; start < len(texts); start += e.cfg.Batch {
		end := start + e.cfg.Batch
		if end > len(texts) {
			end = len(texts)
		}
		vectors, err := e.embedBatch(ctx, texts[start:end])
		if err != nil {
			return nil, err
		}
		out = append(out, vectors...)
	}
	return out, nil
}

type embedRequest struct {
	Model      string   `json:"model"`
	Input      []string `json:"input"`
	Dimensions int      `json:"dimensions,omitempty"`
}

type embedResponse struct {
	Data []struct {
		Index     int       `json:"index"`
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (e *Embedder) embedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	reqBody := embedRequest{Model: e.cfg.Model, Input: texts}
	if e.cfg.Provider == "openai" && e.cfg.Dim > 0 {
		// OpenAI's text-embedding-3 models accept a shortened dimension; the
		// local model is already 1024 so no parameter is sent to Ollama.
		reqBody.Dimensions = e.cfg.Dim
	}
	payload, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("%w: encode request: %v", ErrEmbeddingUnavailable, err)
	}

	endpoint := strings.TrimRight(e.cfg.BaseURL, "/") + "/embeddings"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("%w: build request: %v", ErrEmbeddingUnavailable, err)
	}
	req.Header.Set("Content-Type", "application/json")
	if e.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+e.cfg.APIKey)
	}

	resp, err := e.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrEmbeddingUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("%w: read response: %v", ErrEmbeddingUnavailable, err)
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("%w: HTTP %d: %s", ErrEmbeddingUnavailable, resp.StatusCode, truncate(string(body), 200))
	}

	var decoded embedResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, fmt.Errorf("%w: decode response: %v", ErrEmbeddingUnavailable, err)
	}
	if decoded.Error != nil && decoded.Error.Message != "" {
		return nil, fmt.Errorf("%w: %s", ErrEmbeddingUnavailable, decoded.Error.Message)
	}
	if len(decoded.Data) != len(texts) {
		return nil, fmt.Errorf("%w: expected %d vectors, got %d", ErrEmbeddingUnavailable, len(texts), len(decoded.Data))
	}

	vectors := make([][]float32, len(texts))
	for _, item := range decoded.Data {
		index := item.Index
		if index < 0 || index >= len(texts) {
			return nil, fmt.Errorf("%w: response index %d out of range", ErrEmbeddingUnavailable, index)
		}
		if e.cfg.Dim > 0 && len(item.Embedding) != e.cfg.Dim {
			return nil, fmt.Errorf("%w: got %d dims, want %d", ErrEmbeddingDimension, len(item.Embedding), e.cfg.Dim)
		}
		vectors[index] = item.Embedding
	}
	return vectors, nil
}

func truncate(s string, max int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= max {
		return string(r)
	}
	return string(r[:max]) + "…"
}

// CostUSD estimates spend for a number of input tokens. Local embeddings cost
// zero by construction.
func (e *Embedder) CostUSD(tokens int) float64 {
	if e == nil || e.cfg.PricePerMTOK <= 0 || tokens <= 0 {
		return 0
	}
	return float64(tokens) * e.cfg.PricePerMTOK / 1_000_000
}
