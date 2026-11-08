package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Retrieval modes exposed by RAG_MODE. auto picks hybrid when an embedding
// endpoint answers and bm25 otherwise.
const (
	RAGModeOff    = "off"
	RAGModeBM25   = "bm25"
	RAGModeHybrid = "hybrid"
	RAGModeAuto   = "auto"
)

// Report tool modes. deterministic keeps the fixed evidence plan; autonomous
// lets the model choose read-only tools inside hard budgets.
const (
	ReportToolDeterministic = "deterministic"
	ReportToolAutonomous    = "autonomous"
)

// Memory modes. auto enables the three-layer prompt context; off keeps the
// pre-M20 behaviour.
const (
	MemoryModeAuto = "auto"
	MemoryModeOff  = "off"
)

// M20Config groups the retrieval, tool-autonomy and memory settings. Defaults
// keep the no-key demo path working: auto retrieval degrades to BM25, the
// report stays deterministic, and memory summarisation degrades to a
// deterministic excerpt.
type M20Config struct {
	RAGMode      string
	RAGTopK      int
	RAGCandidate int
	RAGRRFK      int
	// RAGBM25Weight and RAGVectorWeight scale the RRF contributions. The
	// defaults are an empirical preset from the 54-query comparison, not an
	// optimum: a lexical boost recovered A-class MRR without hurting B-class.
	RAGBM25Weight   float64
	RAGVectorWeight float64
	// RAGDocsDir is the markdown corpus root (README.md plus docs/*.md).
	RAGDocsDir string

	EmbeddingProvider string
	EmbeddingBaseURL  string
	EmbeddingAPIKey   string
	EmbeddingModel    string
	EmbeddingDim      int
	EmbeddingBatch    int
	EmbeddingTimeout  time.Duration
	// EmbeddingProbeTimeout bounds the first call after a cold model load.
	// 45s covers the measured 23.9s cold start plus headroom.
	EmbeddingProbeTimeout time.Duration
	EmbeddingPricePerMTOK float64

	ReportToolMode      string
	ReportToolMaxRounds int
	ReportToolMaxCalls  int
	ReportToolTimeout   time.Duration

	MemoryMode         string
	MemoryRecentTurns  int
	MemorySummaryEvery int
}

func defaultM20() M20Config {
	return M20Config{
		RAGMode:         RAGModeAuto,
		RAGTopK:         6,
		RAGCandidate:    20,
		RAGRRFK:         60,
		RAGBM25Weight:   1.2,
		RAGVectorWeight: 1.0,
		RAGDocsDir:      "docs",

		EmbeddingProvider:     "auto",
		EmbeddingBaseURL:      "http://localhost:11434/v1",
		EmbeddingModel:        "bge-m3",
		EmbeddingDim:          1024,
		EmbeddingBatch:        16,
		EmbeddingTimeout:      60 * time.Second,
		EmbeddingProbeTimeout: 45 * time.Second,
		EmbeddingPricePerMTOK: 0,

		ReportToolMode:      ReportToolDeterministic,
		ReportToolMaxRounds: 6,
		ReportToolMaxCalls:  12,
		ReportToolTimeout:   20 * time.Second,

		MemoryMode:         MemoryModeAuto,
		MemoryRecentTurns:  4,
		MemorySummaryEvery: 5,
	}
}

// loadM20 reads the M20 settings. Invalid values fail startup instead of
// silently changing retrieval behaviour.
func loadM20() (M20Config, error) {
	cfg := defaultM20()
	cfg.RAGMode = strings.ToLower(env("RAG_MODE", cfg.RAGMode))
	switch cfg.RAGMode {
	case RAGModeOff, RAGModeBM25, RAGModeHybrid, RAGModeAuto:
	default:
		return M20Config{}, fmt.Errorf("RAG_MODE must be off, bm25, hybrid or auto, got %q", cfg.RAGMode)
	}

	var err error
	if cfg.RAGTopK, err = intEnv("RAG_TOP_K", cfg.RAGTopK); err != nil {
		return M20Config{}, err
	}
	if cfg.RAGCandidate, err = intEnv("RAG_CANDIDATE", cfg.RAGCandidate); err != nil {
		return M20Config{}, err
	}
	if cfg.RAGRRFK, err = intEnv("RAG_RRF_K", cfg.RAGRRFK); err != nil {
		return M20Config{}, err
	}
	if cfg.RAGTopK <= 0 || cfg.RAGCandidate <= 0 || cfg.RAGRRFK <= 0 {
		return M20Config{}, fmt.Errorf("RAG_TOP_K, RAG_CANDIDATE and RAG_RRF_K must be positive")
	}
	if cfg.RAGBM25Weight, err = floatEnv("RAG_BM25_WEIGHT", cfg.RAGBM25Weight); err != nil {
		return M20Config{}, err
	}
	if cfg.RAGVectorWeight, err = floatEnv("RAG_VECTOR_WEIGHT", cfg.RAGVectorWeight); err != nil {
		return M20Config{}, err
	}
	if cfg.RAGBM25Weight <= 0 || cfg.RAGVectorWeight <= 0 {
		return M20Config{}, fmt.Errorf("RAG_BM25_WEIGHT and RAG_VECTOR_WEIGHT must be positive")
	}
	if cfg.RAGCandidate < cfg.RAGTopK {
		return M20Config{}, fmt.Errorf("RAG_CANDIDATE (%d) must be >= RAG_TOP_K (%d)", cfg.RAGCandidate, cfg.RAGTopK)
	}
	cfg.RAGDocsDir = env("RAG_DOCS_DIR", cfg.RAGDocsDir)

	cfg.EmbeddingProvider = strings.ToLower(env("EMBEDDING_PROVIDER", cfg.EmbeddingProvider))
	switch cfg.EmbeddingProvider {
	case "auto", "ollama", "openai", "none":
	default:
		return M20Config{}, fmt.Errorf("EMBEDDING_PROVIDER must be auto, ollama, openai or none, got %q", cfg.EmbeddingProvider)
	}
	cfg.EmbeddingBaseURL = strings.TrimRight(env("EMBEDDING_BASE_URL", cfg.EmbeddingBaseURL), "/")
	cfg.EmbeddingAPIKey = strings.TrimSpace(os.Getenv("EMBEDDING_API_KEY"))
	cfg.EmbeddingModel = env("EMBEDDING_MODEL", cfg.EmbeddingModel)
	if cfg.EmbeddingDim, err = intEnv("EMBEDDING_DIM", cfg.EmbeddingDim); err != nil {
		return M20Config{}, err
	}
	if cfg.EmbeddingBatch, err = intEnv("EMBEDDING_BATCH", cfg.EmbeddingBatch); err != nil {
		return M20Config{}, err
	}
	if cfg.EmbeddingTimeout, err = durationEnv("EMBEDDING_TIMEOUT", cfg.EmbeddingTimeout); err != nil {
		return M20Config{}, err
	}
	if cfg.EmbeddingProbeTimeout, err = durationEnv("EMBEDDING_PROBE_TIMEOUT", cfg.EmbeddingProbeTimeout); err != nil {
		return M20Config{}, err
	}
	if cfg.EmbeddingProbeTimeout <= 0 {
		return M20Config{}, fmt.Errorf("EMBEDDING_PROBE_TIMEOUT must be positive")
	}
	cfg.EmbeddingPricePerMTOK, err = floatEnv("EMBEDDING_PRICE_PER_MTOK", cfg.EmbeddingPricePerMTOK)
	if err != nil {
		return M20Config{}, err
	}
	if cfg.EmbeddingDim != 1024 {
		return M20Config{}, fmt.Errorf("EMBEDDING_DIM must be 1024 (vector(1024) column), got %d", cfg.EmbeddingDim)
	}

	cfg.ReportToolMode = strings.ToLower(env("REPORT_TOOL_MODE", cfg.ReportToolMode))
	switch cfg.ReportToolMode {
	case ReportToolDeterministic, ReportToolAutonomous:
	default:
		return M20Config{}, fmt.Errorf("REPORT_TOOL_MODE must be deterministic or autonomous, got %q", cfg.ReportToolMode)
	}
	if cfg.ReportToolMaxRounds, err = intEnv("REPORT_TOOL_MAX_ROUNDS", cfg.ReportToolMaxRounds); err != nil {
		return M20Config{}, err
	}
	if cfg.ReportToolMaxCalls, err = intEnv("REPORT_TOOL_MAX_CALLS", cfg.ReportToolMaxCalls); err != nil {
		return M20Config{}, err
	}
	if cfg.ReportToolTimeout, err = durationEnv("REPORT_TOOL_TIMEOUT", cfg.ReportToolTimeout); err != nil {
		return M20Config{}, err
	}
	if cfg.ReportToolMaxRounds <= 0 || cfg.ReportToolMaxCalls <= 0 {
		return M20Config{}, fmt.Errorf("REPORT_TOOL_MAX_ROUNDS and REPORT_TOOL_MAX_CALLS must be positive")
	}

	cfg.MemoryMode = strings.ToLower(env("MEMORY_MODE", cfg.MemoryMode))
	switch cfg.MemoryMode {
	case MemoryModeAuto, MemoryModeOff:
	default:
		return M20Config{}, fmt.Errorf("MEMORY_MODE must be auto or off, got %q", cfg.MemoryMode)
	}
	if cfg.MemoryRecentTurns, err = intEnv("MEMORY_RECENT_TURNS", cfg.MemoryRecentTurns); err != nil {
		return M20Config{}, err
	}
	if cfg.MemorySummaryEvery, err = intEnv("MEMORY_SUMMARY_EVERY", cfg.MemorySummaryEvery); err != nil {
		return M20Config{}, err
	}
	if cfg.MemoryRecentTurns <= 0 || cfg.MemorySummaryEvery <= 0 {
		return M20Config{}, fmt.Errorf("MEMORY_RECENT_TURNS and MEMORY_SUMMARY_EVERY must be positive")
	}
	return cfg, nil
}

func floatEnv(key string, fallback float64) (float64, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", key, err)
	}
	if v < 0 {
		return 0, fmt.Errorf("%s must not be negative, got %s", key, raw)
	}
	return v, nil
}
