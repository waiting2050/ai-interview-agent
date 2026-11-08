package knowledge

import (
	"math"
	"sort"
	"strings"
	"unicode"
)

// BM25 parameters. k1=1.2 and b=0.75 are the standard defaults; the corpus is
// short Chinese sections, so a higher b would over-penalise long design docs.
const (
	bm25K1 = 1.2
	bm25B  = 0.75
)

// BM25Index is an in-process, dependency-free BM25 index. The corpus is small
// (~100 chunks), so scoring in Go keeps Postgres free of a full-text
// configuration that does not tokenise Chinese well.
type BM25Index struct {
	chunks   []Chunk
	termFreq []map[string]int
	lengths  []int
	docFreq  map[string]int
	avgLen   float64
}

// NewBM25Index builds the index. Chunk contents are tokenised once.
func NewBM25Index(chunks []Chunk) *BM25Index {
	idx := &BM25Index{
		chunks:   append([]Chunk(nil), chunks...),
		termFreq: make([]map[string]int, len(chunks)),
		lengths:  make([]int, len(chunks)),
		docFreq:  map[string]int{},
	}
	total := 0
	for i, chunk := range chunks {
		terms := Tokenize(chunk.Title + "\n" + chunk.Content)
		idx.termFreq[i] = terms
		length := 0
		for term, count := range terms {
			idx.docFreq[term]++
			length += count
		}
		idx.lengths[i] = length
		total += length
	}
	if len(chunks) > 0 {
		idx.avgLen = float64(total) / float64(len(chunks))
	}
	return idx
}

// BM25Hit is one scored chunk, rank is 1-based.
type BM25Hit struct {
	ChunkID string
	Score   float64
	Rank    int
}

// Search scores every chunk and returns the top limit. A query with no usable
// token returns no hits instead of a meaningless partial match.
func (idx *BM25Index) Search(query string, limit int) []BM25Hit {
	if limit <= 0 || len(idx.chunks) == 0 {
		return nil
	}
	terms := Tokenize(query)
	if len(terms) == 0 {
		return nil
	}

	type scored struct {
		index int
		score float64
	}
	scores := make([]scored, 0, len(idx.chunks))
	n := float64(len(idx.chunks))
	for i := range idx.chunks {
		var score float64
		length := float64(idx.lengths[i])
		for term, qtf := range terms {
			tf := idx.termFreq[i][term]
			if tf == 0 {
				continue
			}
			df := float64(idx.docFreq[term])
			idf := math.Log(1 + (n-df+0.5)/(df+0.5))
			denom := float64(tf) + bm25K1*(1-bm25B+bm25B*length/idx.avgLen)
			score += idf * (float64(tf) * (bm25K1 + 1)) / denom * float64(qtf)
		}
		if score > 0 {
			scores = append(scores, scored{index: i, score: score})
		}
	}
	sort.SliceStable(scores, func(i, j int) bool {
		if scores[i].score == scores[j].score {
			return idx.chunks[scores[i].index].ID < idx.chunks[scores[j].index].ID
		}
		return scores[i].score > scores[j].score
	})
	if len(scores) > limit {
		scores = scores[:limit]
	}
	out := make([]BM25Hit, 0, len(scores))
	for i, s := range scores {
		out = append(out, BM25Hit{ChunkID: idx.chunks[s.index].ID, Score: s.score, Rank: i + 1})
	}
	return out
}

// Tokenize emits lower-case ASCII words plus CJK bigrams. Bigrams are used
// instead of a dictionary tokenizer so no external dependency and no
// domain-specific dictionary is needed; the trade-off is more index terms and
// an inability to match a single-character query against a longer phrase.
func Tokenize(text string) map[string]int {
	terms := map[string]int{}
	var ascii strings.Builder
	var cjk []rune

	flushASCII := func() {
		if ascii.Len() > 0 {
			terms[strings.ToLower(ascii.String())]++
			ascii.Reset()
		}
	}
	flushCJK := func() {
		if len(cjk) == 0 {
			return
		}
		if len(cjk) == 1 {
			terms[string(cjk)]++
		} else {
			for i := 0; i+1 < len(cjk); i++ {
				terms[string(cjk[i:i+2])]++
			}
		}
		cjk = cjk[:0]
	}

	for _, r := range text {
		switch {
		case unicode.Is(unicode.Han, r):
			flushASCII()
			cjk = append(cjk, r)
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			flushCJK()
			ascii.WriteRune(unicode.ToLower(r))
		default:
			flushASCII()
			flushCJK()
		}
	}
	flushASCII()
	flushCJK()
	return terms
}
