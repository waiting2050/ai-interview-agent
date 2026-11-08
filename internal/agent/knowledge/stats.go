package knowledge

import (
	"unicode"
)

// CorpusStats is the dry-run/ingest summary. Tokens is a conservative estimate
// for batching and cost planning, not a billed count.
type CorpusStats struct {
	Chunks   int            `json:"chunks"`
	Runes    int            `json:"runes"`
	Tokens   int            `json:"estimated_tokens"`
	Longest  int            `json:"longest_chunk_runes"`
	Shortest int            `json:"shortest_chunk_runes"`
	Domains  map[string]int `json:"chunks_by_domain"`
	Kinds    map[string]int `json:"chunks_by_kind"`
	Batches  int            `json:"estimated_batches"`
}

// Stats summarises the corpus without touching the network or database.
func Stats(chunks []Chunk, batchSize int) CorpusStats {
	st := CorpusStats{
		Chunks:  len(chunks),
		Domains: map[string]int{},
		Kinds:   map[string]int{},
	}
	if batchSize <= 0 {
		batchSize = 1
	}
	for i, chunk := range chunks {
		runes := len([]rune(chunk.Content))
		st.Runes += runes
		st.Tokens += EstimateTokens(chunk.Content)
		if i == 0 || runes > st.Longest {
			st.Longest = runes
		}
		if i == 0 || runes < st.Shortest {
			st.Shortest = runes
		}
		st.Domains[chunk.Domain]++
		st.Kinds[chunk.SourceKind]++
	}
	if st.Chunks > 0 {
		st.Batches = (st.Chunks + batchSize - 1) / batchSize
	}
	return st
}

// EstimateTokens approximates the token count of mixed Chinese/English text:
// one token per Han character (BGE's tokenizer emits roughly one token per
// character) and one token per four ASCII characters.
func EstimateTokens(text string) int {
	var han, ascii int
	for _, r := range text {
		switch {
		case unicode.Is(unicode.Han, r):
			han++
		case unicode.IsSpace(r):
		default:
			ascii++
		}
	}
	tokens := han + ascii/4
	if ascii%4 != 0 {
		tokens++
	}
	return tokens
}
