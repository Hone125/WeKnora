package embedding

import (
	"context"
	"sync"
	"testing"
	"time"
)

// countingEmbedder 记录 Embed/BatchEmbed 调用次数，返回确定性向量（长度=len(text)），
// 用于断言缓存命中确实短路了 provider 往返。
type countingEmbedder struct {
	id     string
	dim    int
	pooler EmbedderPooler

	mu      sync.Mutex
	embeds  int
	batches int
	// lastPoolModel 记录 BatchEmbedWithPool 收到的 model，验证缓存装饰器传自己下去。
	lastPoolModel Embedder
}

func (c *countingEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	c.mu.Lock()
	c.embeds++
	c.mu.Unlock()
	return []float32{float32(len(text))}, nil
}

func (c *countingEmbedder) BatchEmbed(_ context.Context, texts []string) ([][]float32, error) {
	c.mu.Lock()
	c.batches++
	c.mu.Unlock()
	out := make([][]float32, len(texts))
	for i, t := range texts {
		out[i] = []float32{float32(len(t))}
	}
	return out, nil
}

func (c *countingEmbedder) BatchEmbedWithPool(
	ctx context.Context, model Embedder, texts []string,
) ([][]float32, error) {
	c.mu.Lock()
	c.lastPoolModel = model
	c.mu.Unlock()
	if c.pooler != nil {
		return c.pooler.BatchEmbedWithPool(ctx, model, texts)
	}
	return model.BatchEmbed(ctx, texts)
}

func (c *countingEmbedder) GetModelName() string { return c.id }
func (c *countingEmbedder) GetDimensions() int   { return c.dim }
func (c *countingEmbedder) GetModelID() string   { return c.id }

func (c *countingEmbedder) callCounts() (int, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.embeds, c.batches
}

// fakeCacheStore 是内存版二级缓存实现，用于测试两级缓存的读写与跨实例命中。
type fakeCacheStore struct {
	mu sync.Mutex
	m  map[string][]byte
}

func newFakeCacheStore() *fakeCacheStore {
	return &fakeCacheStore{m: make(map[string][]byte)}
}

func (f *fakeCacheStore) Get(_ context.Context, key string) ([]byte, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.m[key]
	return v, ok, nil
}

func (f *fakeCacheStore) Set(_ context.Context, key string, value []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.m[key] = value
	return nil
}

func (f *fakeCacheStore) len() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.m)
}

func TestEncodeDecodeVectorRoundTrip(t *testing.T) {
	orig := []float32{0, 1.5, -2.25, 3.14159, -0.0}
	back := decodeVector(encodeVector(orig))
	if len(orig) != len(back) {
		t.Fatalf("length mismatch: %d != %d", len(orig), len(back))
	}
	for i := range orig {
		if back[i] != orig[i] {
			t.Fatalf("index %d: got %v want %v", i, back[i], orig[i])
		}
	}
}

func TestEncodeDecodeVectorEmpty(t *testing.T) {
	back := decodeVector(encodeVector(nil))
	if len(back) != 0 {
		t.Fatalf("expected empty, got %v", back)
	}
}

func TestMemCacheLRUEvictsOldest(t *testing.T) {
	c := newMemCache(2)
	c.Set("a", []float32{1})
	c.Set("b", []float32{2})
	// 访问 a，把 a 提升为最近使用，b 变为最久未用。
	if _, ok := c.Get("a"); !ok {
		t.Fatal("a should be present")
	}
	c.Set("c", []float32{3}) // 触发淘汰，应淘汰 b
	if _, ok := c.Get("a"); !ok {
		t.Fatal("a should survive (recently used)")
	}
	if _, ok := c.Get("b"); ok {
		t.Fatal("b should have been evicted")
	}
	if _, ok := c.Get("c"); !ok {
		t.Fatal("c should be present")
	}
}

func TestMemCacheGetMiss(t *testing.T) {
	c := newMemCache(4)
	if _, ok := c.Get("nope"); ok {
		t.Fatal("expected miss")
	}
}

// TestCacheEmbedderHitShortCircuitsProvider verifies a repeated Embed of the
// same text hits the in-process LRU and never re-enters the provider.
func TestCacheEmbedderHitShortCircuitsProvider(t *testing.T) {
	inner := &countingEmbedder{id: "emb-1", dim: 4}
	c := wrapEmbeddingCache(inner)

	ctx := context.Background()
	v1, err := c.Embed(ctx, "hello")
	if err != nil {
		t.Fatalf("first embed: %v", err)
	}
	v2, err := c.Embed(ctx, "hello")
	if err != nil {
		t.Fatalf("second embed: %v", err)
	}
	if v1[0] != v2[0] {
		t.Fatalf("cache returned different vector: %v vs %v", v1, v2)
	}
	if embeds, _ := inner.callCounts(); embeds != 1 {
		t.Fatalf("expected 1 provider Embed call, got %d", embeds)
	}
}

// TestCacheEmbedderBatchEmbedsOnlyMisses verifies BatchEmbed fans out to the
// provider only for the not-yet-cached texts.
func TestCacheEmbedderBatchEmbedsOnlyMisses(t *testing.T) {
	inner := &countingEmbedder{id: "emb-2", dim: 4}
	c := wrapEmbeddingCache(inner)

	ctx := context.Background()
	// 先缓存 "a"。
	if _, err := c.Embed(ctx, "a"); err != nil {
		t.Fatalf("prime: %v", err)
	}
	if _, err := c.BatchEmbed(ctx, []string{"a", "b", "a", "c"}); err != nil {
		t.Fatalf("batch: %v", err)
	}
	// "a" 命中缓存；"b"、"c" 未命中 → 一次 BatchEmbed 调用，输入应只有 2 个。
	_, batches := inner.callCounts()
	if batches != 1 {
		t.Fatalf("expected 1 provider BatchEmbed call, got %d", batches)
	}
}

// TestCacheEmbedderSecondLevelPersists verifies that after the in-process LRU
// is dropped (simulating a restart), a fresh cacheEmbedder still hits the
// second-level store and avoids the provider round-trip.
func TestCacheEmbedderSecondLevelPersists(t *testing.T) {
	store := newFakeCacheStore()
	CacheStore = store
	t.Cleanup(func() { CacheStore = nil })

	inner := &countingEmbedder{id: "emb-3", dim: 4}
	c := wrapEmbeddingCache(inner)
	ctx := context.Background()

	if _, err := c.Embed(ctx, "persist-me"); err != nil {
		t.Fatalf("prime: %v", err)
	}
	// 二级缓存是异步回填，轮询等待落库。
	deadline := time.Now().Add(2 * time.Second)
	for store.len() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if store.len() == 0 {
		t.Fatal("second-level store was never written")
	}

	// 新实例：进程内 LRU 为空，只能靠二级缓存命中。
	freshInner := &countingEmbedder{id: "emb-3", dim: 4}
	fresh := wrapEmbeddingCache(freshInner)
	if _, err := fresh.Embed(ctx, "persist-me"); err != nil {
		t.Fatalf("second instance: %v", err)
	}
	if embeds, _ := freshInner.callCounts(); embeds != 0 {
		t.Fatalf("fresh instance should hit second-level cache, got %d provider calls", embeds)
	}
}

// TestCacheEmbedderBatchEmbedWithPoolPropagatesSelf verifies the decorator
// passes itself down so per-sub-batch callbacks re-enter the cache.
func TestCacheEmbedderBatchEmbedWithPoolPropagatesSelf(t *testing.T) {
	inner := &countingEmbedder{id: "emb-4", dim: 4}
	c := wrapEmbeddingCache(inner)

	ctx := context.Background()
	if _, err := c.BatchEmbedWithPool(ctx, c, []string{"x", "y"}); err != nil {
		t.Fatalf("pool embed: %v", err)
	}
	inner.mu.Lock()
	got := inner.lastPoolModel
	inner.mu.Unlock()
	if got == nil {
		t.Fatal("BatchEmbedWithPool did not record a model")
	}
	// 传下去的 model 必须是 cacheEmbedder 自身（而非被包装的内层），
	// 这样 pooler 的 per-sub-batch 回调才会命中缓存。
	if _, ok := got.(*cacheEmbedder); !ok {
		t.Fatalf("expected *cacheEmbedder, got %T", got)
	}
}

// TestCacheEmbedderDifferentModelsDoNotCollide verifies the cache key includes
// the model ID so two models embedding identical text stay isolated.
func TestCacheEmbedderDifferentModelsDoNotCollide(t *testing.T) {
	a := wrapEmbeddingCache(&countingEmbedder{id: "model-a", dim: 4})
	b := wrapEmbeddingCache(&countingEmbedder{id: "model-b", dim: 4})

	ctx := context.Background()
	// 两边都算一次 "same"。
	if _, err := a.Embed(ctx, "same"); err != nil {
		t.Fatalf("a: %v", err)
	}
	if _, err := b.Embed(ctx, "same"); err != nil {
		t.Fatalf("b: %v", err)
	}
	// b 不应命中 a 的缓存：再次调用 b 应再触发一次 provider。
	if _, err := b.Embed(ctx, "same"); err != nil {
		t.Fatalf("b again: %v", err)
	}
	innerA := a.(*cacheEmbedder).inner.(*countingEmbedder)
	innerB := b.(*cacheEmbedder).inner.(*countingEmbedder)
	if ea, _ := innerA.callCounts(); ea != 1 {
		t.Fatalf("model-a should have 1 provider call, got %d", ea)
	}
	if eb, _ := innerB.callCounts(); eb != 2 {
		t.Fatalf("model-b should have 2 provider calls (no cross-model hit), got %d", eb)
	}
}
