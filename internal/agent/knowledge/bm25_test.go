package knowledge

import "testing"

func testChunks() []Chunk {
	return []Chunk{
		{ID: "a", Title: "Go 并发模型", Content: "goroutine 调度与 G-M-P 模型，channel 缓冲区满时阻塞。"},
		{ID: "b", Title: "MySQL 索引", Content: "B+ 树索引、最左前缀与回表，覆盖索引可避免回表。"},
		{ID: "c", Title: "分层记忆", Content: "最近四轮问答原文加上更早轮次的压缩摘要。"},
	}
}

func TestBM25ChineseBigramRanksRelevantChunkFirst(t *testing.T) {
	index := NewBM25Index(testChunks())
	hits := index.Search("goroutine 调度", 3)
	if len(hits) == 0 || hits[0].ChunkID != "a" {
		t.Fatalf("expected chunk a first, got %+v", hits)
	}

	hits = index.Search("回表 覆盖索引", 3)
	if len(hits) == 0 || hits[0].ChunkID != "b" {
		t.Fatalf("expected chunk b first, got %+v", hits)
	}
}

func TestBM25EmptyQueryReturnsNoHits(t *testing.T) {
	index := NewBM25Index(testChunks())
	if hits := index.Search("   ", 3); len(hits) != 0 {
		t.Fatalf("empty query produced %d hits", len(hits))
	}
	if hits := index.Search("不存在的词", 3); len(hits) != 0 {
		t.Fatalf("unknown term produced %d hits", len(hits))
	}
}

func TestTokenizeASCIIAndBigrams(t *testing.T) {
	terms := Tokenize("Goroutine-GMP 调度")
	for _, want := range []string{"goroutine", "gmp", "调度"} {
		if terms[want] == 0 {
			t.Fatalf("token %q missing from %v", want, terms)
		}
	}
}

func TestBM25Limit(t *testing.T) {
	index := NewBM25Index(testChunks())
	if hits := index.Search("索引 并发 记忆", 1); len(hits) != 1 {
		t.Fatalf("limit not applied: %d hits", len(hits))
	}
}
