package embedding

import (
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"os"
	"strconv"
	"sync"
	"time"
)

// EmbeddingCacheStore 定义 embedding 二级缓存（DB 持久化）的字节级读写。
// embedding 包是叶子依赖，不能反向 import 仓储层，故与 chat.LocalImageResolver /
// chat.UsageRecorder 一样，由 container 在启动时注入实现。未注入（nil）时，
// 缓存退化为仅进程内一级 LRU，不持久化。
type EmbeddingCacheStore interface {
	// Get 返回 key 对应的向量字节；ok=false 且 err=nil 表示未命中。
	Get(ctx context.Context, key string) (value []byte, ok bool, err error)
	// Set 写入一条缓存（异步调用，不阻塞 embedding 主链）。
	Set(ctx context.Context, key string, value []byte) error
}

// CacheStore 由 container 注入的二级缓存实现（见 internal/container）。
var CacheStore EmbeddingCacheStore

// embeddingCacheSize 返回进程内一级 LRU 的容量上限；0 表示禁用缓存。
// 用环境变量 EMBEDDING_CACHE_SIZE 覆盖，默认 4096 条。
func embeddingCacheSize() int {
	if v := os.Getenv("EMBEDDING_CACHE_SIZE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return 4096
}

// cacheKey 计算缓存键 = sha256(model_id \x00 dimension \x00 text) 的十六进制。
// 只含「模型 + 维度 + 文本」，不含租户/会话等易变上下文——同一文本同一模型
// 任何路径、任何租户算出的向量一致，天然可跨租户共享。
func (c *cacheEmbedder) cacheKey(text string) string {
	raw := fmt.Sprintf("%s\x00%d\x00%s", c.inner.GetModelID(), c.inner.GetDimensions(), text)
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// encodeVector 将 float32 向量序列化为小端二进制（每维 4 字节），
// 用于持久化到 PG bytea / SQLite BLOB。
func encodeVector(v []float32) []byte {
	buf := make([]byte, len(v)*4)
	for i, f := range v {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(f))
	}
	return buf
}

// decodeVector 是 encodeVector 的逆操作。
func decodeVector(b []byte) []float32 {
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return v
}

// memCache 是进程内一级缓存：固定容量、线程安全的 LRU。
// 命中不产生任何 provider 往返，热路径近乎零开销。
type memCache struct {
	mu       sync.Mutex
	capacity int
	ll       *list.List // front = 最近使用
	items    map[string]*list.Element
}

type memCacheEntry struct {
	key    string
	vector []float32
}

func newMemCache(capacity int) *memCache {
	return &memCache{
		capacity: capacity,
		ll:       list.New(),
		items:    make(map[string]*list.Element),
	}
}

func (c *memCache) Get(key string) ([]float32, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.items[key]; ok {
		c.ll.MoveToFront(e)
		return e.Value.(*memCacheEntry).vector, true
	}
	return nil, false
}

func (c *memCache) Set(key string, vector []float32) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.items[key]; ok {
		e.Value.(*memCacheEntry).vector = vector
		c.ll.MoveToFront(e)
		return
	}
	e := c.ll.PushFront(&memCacheEntry{key: key, vector: vector})
	c.items[key] = e
	for c.ll.Len() > c.capacity {
		oldest := c.ll.Back()
		if oldest == nil {
			break
		}
		delete(c.items, oldest.Value.(*memCacheEntry).key)
		c.ll.Remove(oldest)
	}
}

// cacheEmbedder 是 embedding 的两级缓存装饰器：一级进程内 LRU + 二级 DB 持久化。
// 它包在 concurrencyEmbedder 之外（见 embedder.go），因此缓存命中直接短路、
// 不占 provider 并发配额；缓存未命中才进入 provider 往返。
type cacheEmbedder struct {
	inner Embedder
	mem   *memCache
	store EmbeddingCacheStore // 可能为 nil
}

// lookup 依次查一级、二级缓存。二级未命中或 DB 出错都返回 ok=false，
// 由调用方走真实 provider 调用（DB 错误降级、不阻断）。
func (c *cacheEmbedder) lookup(ctx context.Context, key string) ([]float32, bool) {
	if v, ok := c.mem.Get(key); ok {
		return v, true
	}
	if c.store == nil {
		return nil, false
	}
	raw, ok, err := c.store.Get(ctx, key)
	if err != nil || !ok {
		return nil, false
	}
	v := decodeVector(raw)
	c.mem.Set(key, v)
	return v, true
}

// backfill 回填两级缓存。一级同步写入立即生效；二级异步写 DB（5 秒超时），
// 掉一条只降级为下次重算，绝不阻塞 embedding 主链。
func (c *cacheEmbedder) backfill(ctx context.Context, key string, v []float32) {
	c.mem.Set(key, v)
	if c.store == nil {
		return
	}
	raw := encodeVector(v)
	go func() {
		recCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = c.store.Set(recCtx, key, raw)
	}()
}

func (c *cacheEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	key := c.cacheKey(text)
	if v, ok := c.lookup(ctx, key); ok {
		return v, nil
	}
	v, err := c.inner.Embed(ctx, text)
	if err != nil {
		return nil, err
	}
	c.backfill(ctx, key, v)
	return v, nil
}

func (c *cacheEmbedder) BatchEmbed(ctx context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	missIdx := make([]int, 0, len(texts))
	missTexts := make([]string, 0, len(texts))

	for i, t := range texts {
		key := c.cacheKey(t)
		if v, ok := c.lookup(ctx, key); ok {
			out[i] = v
		} else {
			missIdx = append(missIdx, i)
			missTexts = append(missTexts, t)
		}
	}
	if len(missIdx) == 0 {
		return out, nil
	}

	vecs, err := c.inner.BatchEmbed(ctx, missTexts)
	if err != nil {
		return nil, err
	}
	if len(vecs) != len(missTexts) {
		return nil, fmt.Errorf("embedding model returned %d embeddings for %d inputs", len(vecs), len(missTexts))
	}
	for j, i := range missIdx {
		out[i] = vecs[j]
		c.backfill(ctx, c.cacheKey(texts[i]), vecs[j])
	}
	return out, nil
}

// BatchEmbedWithPool 把 cacheEmbedder 自身作为 model 传下去，让 pooler 的
// per-sub-batch BatchEmbed 回调落回本装饰器的 BatchEmbed 上，从而命中缓存。
func (c *cacheEmbedder) BatchEmbedWithPool(
	ctx context.Context, model Embedder, texts []string,
) ([][]float32, error) {
	return c.inner.BatchEmbedWithPool(ctx, c, texts)
}

func (c *cacheEmbedder) GetModelName() string { return c.inner.GetModelName() }
func (c *cacheEmbedder) GetDimensions() int   { return c.inner.GetDimensions() }
func (c *cacheEmbedder) GetModelID() string   { return c.inner.GetModelID() }

// wrapEmbeddingCache 在 concurrencyEmbedder 之外再包一层两级缓存。
// 缓存容量 <= 0 时直接透传（等价于禁用缓存）。
func wrapEmbeddingCache(e Embedder) Embedder {
	if e == nil {
		return e
	}
	size := embeddingCacheSize()
	if size <= 0 {
		return e
	}
	return &cacheEmbedder{
		inner: e,
		mem:   newMemCache(size),
		store: CacheStore,
	}
}
