package agent

import (
	"testing"
	"time"
)

func TestResponseCache_MissOnEmpty(t *testing.T) {
	c := newResponseCache()
	if _, ok := c.Get(1, []float32{1, 0}); ok {
		t.Error("expected miss on empty cache")
	}
}

func TestResponseCache_HitOnIdenticalEmbedding(t *testing.T) {
	c := newResponseCache()
	emb := []float32{1, 0}
	c.Set(1, emb, "hello")
	resp, ok := c.Get(1, emb)
	if !ok {
		t.Fatal("expected hit for identical embedding")
	}
	if resp != "hello" {
		t.Errorf("expected 'hello', got %q", resp)
	}
}

func TestResponseCache_HitOnNearIdenticalEmbedding(t *testing.T) {
	c := newResponseCache()
	c.Set(1, []float32{1, 0}, "cached")
	// Very close but not identical — should still hit (cosine ≈ 0.9999)
	resp, ok := c.Get(1, []float32{0.9999, 0.0141})
	if !ok {
		t.Fatal("expected cache hit for near-identical embedding")
	}
	if resp != "cached" {
		t.Errorf("expected 'cached', got %q", resp)
	}
}

func TestResponseCache_MissOnDissimilarEmbedding(t *testing.T) {
	c := newResponseCache()
	c.Set(1, []float32{1, 0}, "cached")
	// Orthogonal vector — cosine = 0, well below threshold
	if _, ok := c.Get(1, []float32{0, 1}); ok {
		t.Error("expected miss for dissimilar embedding")
	}
}

func TestResponseCache_MissOnDifferentChat(t *testing.T) {
	c := newResponseCache()
	emb := []float32{1, 0}
	c.Set(1, emb, "chat1")
	if _, ok := c.Get(2, emb); ok {
		t.Error("expected miss for different chatID")
	}
}

func TestResponseCache_ExpiredEntryNotReturned(t *testing.T) {
	c := &ResponseCache{ttl: time.Millisecond, maxSize: 10}
	emb := []float32{1, 0}
	c.Set(1, emb, "stale")
	time.Sleep(5 * time.Millisecond)
	if _, ok := c.Get(1, emb); ok {
		t.Error("expected miss for expired entry")
	}
}

func TestResponseCache_EvictsOldestWhenFull(t *testing.T) {
	c := &ResponseCache{ttl: time.Hour, maxSize: 2}
	c.Set(1, []float32{1, 0}, "first")
	c.Set(1, []float32{0, 1}, "second")
	c.Set(1, []float32{0.707, 0.707}, "third") // should evict "first"

	// "first" should be gone
	if _, ok := c.Get(1, []float32{1, 0}); ok {
		t.Error("expected 'first' to be evicted")
	}
	// "second" and "third" should remain
	if _, ok := c.Get(1, []float32{0, 1}); !ok {
		t.Error("expected 'second' to remain")
	}
}

func TestResponseCache_NilEmbeddingIgnored(t *testing.T) {
	c := newResponseCache()
	c.Set(1, nil, "noop")
	if _, ok := c.Get(1, nil); ok {
		t.Error("expected miss for nil embedding")
	}
}
