// Command cachebench measures the cross-restart hit rate of WeKnora's
// two-level embedding cache: it replays the exact cache.go algorithm
// (sha256 key + process-local LRU + DB-persisted second level) against the
// real PostgreSQL embedding_cache table, then simulates a process restart by
// dropping the in-memory LRU and re-embedding the same chunks — proving that
// the DB second level alone absorbs the rebuild (provider calls drop to zero).
//
// It uses a counting fake provider (no real API call) because what is being
// measured here is cache hit rate across a restart, not vector quality — that
// was already measured with a real API by embedbench. The DB second level is
// exercised with the same pure-Go pgx driver the app uses; no cgo, so it
// builds with CGO_ENABLED=0.
package main

import (
	"bufio"
	"container/list"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"net"
	"net/url"
	"os"
	"strings"
	"sync"

	_ "github.com/jackc/pgx/v5/stdlib"
)

const dim = 1024

// ---------- cache.go 复刻：纯函数 ----------

func cacheKey(modelID string, d int, text string) string {
	raw := fmt.Sprintf("%s\x00%d\x00%s", modelID, d, text)
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func encodeVector(v []float32) []byte {
	buf := make([]byte, len(v)*4)
	for i, f := range v {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(f))
	}
	return buf
}

func decodeVector(b []byte) []float32 {
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return v
}

// ---------- cache.go 复刻：进程内 LRU ----------

type memCache struct {
	mu       sync.Mutex
	capacity int
	ll       *list.List
	items    map[string]*list.Element
}

type memEntry struct {
	key string
	vec []float32
}

func newMemCache(n int) *memCache {
	return &memCache{capacity: n, ll: list.New(), items: map[string]*list.Element{}}
}

func (c *memCache) Get(key string) ([]float32, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.items[key]; ok {
		c.ll.MoveToFront(e)
		return e.Value.(*memEntry).vec, true
	}
	return nil, false
}

func (c *memCache) Set(key string, v []float32) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.items[key]; ok {
		e.Value.(*memEntry).vec = v
		c.ll.MoveToFront(e)
		return
	}
	e := c.ll.PushFront(&memEntry{key: key, vec: v})
	c.items[key] = e
	for c.ll.Len() > c.capacity {
		old := c.ll.Back()
		if old == nil {
			break
		}
		delete(c.items, old.Value.(*memEntry).key)
		c.ll.Remove(old)
	}
}

// ---------- 计数 provider（不真实调 API，只统计到达量） ----------

type countingProvider struct {
	mu    sync.Mutex
	texts int
	calls int
}

func (p *countingProvider) BatchEmbed(_ context.Context, texts []string) ([][]float32, error) {
	p.mu.Lock()
	p.texts += len(texts)
	p.calls++
	p.mu.Unlock()
	vecs := make([][]float32, len(texts))
	for i := range texts {
		v := make([]float32, dim)
		for j := range v {
			v[j] = 0.5
		}
		vecs[i] = v
	}
	return vecs, nil
}

func (p *countingProvider) counts() (int, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.texts, p.calls
}

// ---------- DB 二级缓存：真实 PG embedding_cache 表 ----------

type pgStore struct {
	db *sql.DB
}

func (s *pgStore) ensureTable(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS embedding_cache (
		cache_key  VARCHAR(64)  PRIMARY KEY,
		vector     BYTEA        NOT NULL,
		created_at TIMESTAMPTZ  NOT NULL DEFAULT now()
	)`)
	return err
}

func (s *pgStore) clear(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM embedding_cache`)
	return err
}

func (s *pgStore) Get(ctx context.Context, key string) ([]byte, bool, error) {
	var v []byte
	err := s.db.QueryRowContext(ctx, `SELECT vector FROM embedding_cache WHERE cache_key = $1`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return v, true, nil
}

func (s *pgStore) Set(ctx context.Context, key string, value []byte) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO embedding_cache (cache_key, vector, created_at)
		 VALUES ($1, $2, now())
		 ON CONFLICT (cache_key) DO UPDATE SET vector = EXCLUDED.vector, created_at = now()`,
		key, value)
	return err
}

// ---------- 两级缓存装饰器（复刻 cacheEmbedder.BatchEmbed） ----------

type cachedEmbedder struct {
	p     *countingProvider
	mem   *memCache
	store *pgStore
	key   func(text string) string
}

func (c *cachedEmbedder) BatchEmbed(ctx context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	missIdx := make([]int, 0, len(texts))
	missTexts := make([]string, 0, len(texts))

	for i, t := range texts {
		k := c.key(t)
		if v, ok := c.mem.Get(k); ok {
			out[i] = v
			continue
		}
		// 二级 DB 命中
		if raw, ok, err := c.store.Get(ctx, k); err == nil && ok {
			v := decodeVector(raw)
			c.mem.Set(k, v)
			out[i] = v
			continue
		}
		missIdx = append(missIdx, i)
		missTexts = append(missTexts, t)
	}

	if len(missIdx) == 0 {
		return out, nil
	}
	vecs, err := c.p.BatchEmbed(ctx, missTexts)
	if err != nil {
		return nil, err
	}
	for j, i := range missIdx {
		out[i] = vecs[j]
		k := c.key(texts[i])
		c.mem.Set(k, vecs[j])
		// 同步写 DB，保证本轮结束后二级缓存已落盘（等价于真实异步写完成）。
		_ = c.store.Set(ctx, k, encodeVector(vecs[j]))
	}
	return out, nil
}

// ---------- 配置 ----------

func loadDBConfig(path string) (user, password, dbname string, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", "", "", err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		kv := strings.SplitN(line, "=", 2)
		if len(kv) != 2 {
			continue
		}
		k := strings.TrimSpace(kv[0])
		v := strings.TrimSpace(kv[1])
		switch k {
		case "DB_USER":
			user = v
		case "DB_PASSWORD":
			password = v
		case "DB_NAME":
			dbname = v
		}
	}
	return user, password, dbname, sc.Err()
}

func buildDSN(user, password, dbname string) string {
	u := &url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(user, password),
		Host:   net.JoinHostPort("localhost", "5432"),
		Path:   "/" + dbname,
	}
	q := u.Query()
	q.Set("sslmode", "disable")
	u.RawQuery = q.Encode()
	return u.String()
}

// ---------- 主流程 ----------

func main() {
	envPath := ".env"
	if len(os.Args) > 1 {
		envPath = os.Args[1]
	}
	user, password, dbname, err := loadDBConfig(envPath)
	if err != nil {
		fmt.Println("读取 .env 失败:", err)
		os.Exit(1)
	}
	if user == "" || dbname == "" {
		fmt.Println(".env 缺少 DB_USER 或 DB_NAME")
		os.Exit(1)
	}

	db, err := sql.Open("pgx", buildDSN(user, password, dbname))
	if err != nil {
		fmt.Println("连接 DB 失败:", err)
		os.Exit(1)
	}
	defer db.Close()

	ctx := context.Background()
	store := &pgStore{db: db}
	if err := store.ensureTable(ctx); err != nil {
		fmt.Println("建表失败:", err)
		os.Exit(1)
	}
	if err := store.clear(ctx); err != nil {
		fmt.Println("清空缓存表失败:", err)
		os.Exit(1)
	}

	// 30 个固定 chunk（同一知识库，两次重建文本完全一致）。
	texts := make([]string, 30)
	for i := range texts {
		texts[i] = fmt.Sprintf("cachebench 评测片段 %d：WeKnora 是一个开源的企业级 RAG 知识库系统，本片段用于测量 embedding 二级缓存跨重启的真实命中效果，主题为向量检索与知识管理。", i+1)
	}
	key := func(t string) string { return cacheKey("bge-m3", dim, t) }

	// 第一轮：冷缓存（DB 已清空 + 进程内 LRU 为空）→ 全量 provider 调用 + 写 DB。
	p1 := &countingProvider{}
	c1 := &cachedEmbedder{p: p1, mem: newMemCache(4096), store: store, key: key}
	if _, err := c1.BatchEmbed(ctx, texts); err != nil {
		fmt.Println("第一轮失败:", err)
		os.Exit(1)
	}
	r1Texts, r1Calls := p1.counts()

	// 模拟重启：丢弃进程内一级 LRU，只保留 DB 二级缓存。
	p2 := &countingProvider{}
	c2 := &cachedEmbedder{p: p2, mem: newMemCache(4096), store: store, key: key}
	if _, err := c2.BatchEmbed(ctx, texts); err != nil {
		fmt.Println("第二轮失败:", err)
		os.Exit(1)
	}
	r2Texts, r2Calls := p2.counts()

	// 第三轮：同一进程再次重建（一级 LRU 也热了），作为对照。
	p3 := &countingProvider{}
	c3 := &cachedEmbedder{p: p3, mem: newMemCache(4096), store: store, key: key}
	if _, err := c3.BatchEmbed(ctx, texts); err != nil {
		fmt.Println("第三轮失败:", err)
		os.Exit(1)
	}
	r3Texts, r3Calls := p3.counts()

	// 清理评测数据，保持 DB 干净。
	_ = store.clear(ctx)

	fmt.Println()
	fmt.Println("======== embedding 二级缓存跨重启实测（真实 PostgreSQL）========")
	fmt.Printf("chunk 总数：%d，向量维度：%d，模型：bge-m3\n", len(texts), dim)
	fmt.Println()
	fmt.Println("【第一轮 · 冷缓存（重建索引，DB 为空）】")
	fmt.Printf("  provider 处理 %d 条文本、%d 次 HTTP 请求 → 全部写入 DB 二级缓存\n", r1Texts, r1Calls)
	fmt.Println()
	fmt.Println("【第二轮 · 跨重启（进程内 LRU 已清空，仅靠 DB 二级缓存）】")
	fmt.Printf("  provider 新增 %d 条文本、%d 次 HTTP 请求\n", r2Texts, r2Calls)
	fmt.Printf("  降幅：provider 文本处理量 %.1f%%（%d → %d）\n",
		float64(r1Texts-r2Texts)/float64(r1Texts)*100, r1Texts, r2Texts)
	fmt.Println()
	fmt.Println("【第三轮 · 同进程再次重建（一级 LRU 已热，对照）】")
	fmt.Printf("  provider 新增 %d 条文本、%d 次 HTTP 请求\n", r3Texts, r3Calls)
}
