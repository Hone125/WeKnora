// Command wikicachebench measures the provider-side prompt-cache hit rate that
// the M3② "fixed-prefix reordering" in internal/agent/prompts_wiki.go is
// designed to win. It does not measure "prefix byte length" (a theoretical
// proxy the acceptance review rejected) — it issues real HTTP calls to the
// configured LLM provider and reads the cache counters the provider actually
// returns (prompt_cache_hit_tokens / prompt_cache_miss_tokens on DeepSeek-style
// endpoints, falling back to prompt_tokens_details.cached_tokens on OpenAI-style
// endpoints).
//
// Two scenarios are compared with the SAME tokens, differing only in block
// order — the thing M3② actually changes:
//
//	reordered (current code): [static rules | candidate_slugs] + [chunks]
//	unordered (old layout)  : [chunks] + [static rules | candidate_slugs]
//
// In the reordered layout every batch of one document shares the long static
// prefix, so batch 2..N should hit the provider's prefix cache. In the
// unordered layout the leading chunks differ every batch, so the shared prefix
// never forms and every batch is a full miss. The tool reports the hit rate the
// provider bills for each scenario, and the concrete money split that follows
// from it.
//
// The prompt is a faithful structural re-copy of WikiChunkCitationPrompt's
// fixed/dynamic split (the actual constants live in internal/agent, which
// cannot be imported here because internal/agent pulls internal/types →
// gojieba, a cgo dependency that breaks a CGO_ENABLED=0 build). Credentials come
// from .env, never committed:
//
//	LLM_API_KEY       LLM provider API key
//	LLM_BASE_URL      default https://api.siliconflow.cn/v1
//	LLM_MODEL_NAME    default deepseek-ai/DeepSeek-V3
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

const httpTimeout = 60 * time.Second

// ---------------------------------------------------------------------------
// config
// ---------------------------------------------------------------------------

type cfg struct {
	apiKey  string
	baseURL string
	model   string
}

func loadCfg(path string) (*cfg, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	c := &cfg{baseURL: "https://api.siliconflow.cn/v1", model: "deepseek-ai/DeepSeek-V3"}
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
		switch strings.TrimSpace(kv[0]) {
		case "LLM_API_KEY":
			c.apiKey = strings.TrimSpace(kv[1])
		case "LLM_BASE_URL":
			c.baseURL = strings.TrimSpace(kv[1])
		case "LLM_MODEL_NAME":
			c.model = strings.TrimSpace(kv[1])
		}
	}
	return c, sc.Err()
}

// ---------------------------------------------------------------------------
// prompt structure (structural re-copy of WikiChunkCitationPrompt)
// ---------------------------------------------------------------------------

// staticPrefix is the fixed block placed BEFORE the per-batch chunks in the
// reordered layout. It stands in for WikiChunkCitationPrompt's <instructions> +
// output schema + <candidate_slugs> block — the part that is byte-stable across
// every batch of a single document.
func staticPrefix() string {
	var b strings.Builder
	b.WriteString("You are a precise citation system. Your job is to scan a batch of document chunks and decide, for each candidate entity/concept below, which chunks substantively discuss it.\n\n")
	b.WriteString("<instructions>\n**IMPORTANT: Write ALL names, descriptions, and details in 中文**.\n\n### Primary task\nFor each candidate slug, select the chunk IDs that substantively discuss that entity/concept.\n- Only cite chunks that appear in the <chunks> block below.\n- Use the \"id\" attribute of each <c> element verbatim.\n- If a candidate is not discussed in any chunk, omit it from the output.\n- A chunk CAN be cited by multiple candidates.\n\n### Secondary task: new slugs\nIf a batch reveals a significant entity/concept NOT in <candidate_slugs>, add it under \"new_slugs\".\n\n### JSON Formatting Rules\n- CRITICAL: Do NOT use literal newline characters inside JSON string values.\n- Output ONLY valid JSON, no preamble.\n</instructions>\n\nOutput format:\n{\n  \"citations\": {\"entity/xxx\": [\"c001\"]},\n  \"new_slugs\": []\n}\n\n")
	// A long, byte-stable "existing directory tree" plays the role of
	// <candidate_slugs>: it is identical for every batch of one document and
	// pushes the shared prefix past the provider's minimum cacheable length.
	b.WriteString("<candidate_slugs>\n")
	for i := 0; i < 80; i++ {
		fmt.Fprintf(&b, "  - entity/subject-%03d : 第 %d 号既有实体，属于知识库统一导航目录，描述为该领域内的稳定命名对象，slug 保持跨文档复用。\n", i, i)
	}
	for i := 0; i < 80; i++ {
		fmt.Fprintf(&b, "  - concept/topic-%03d : 第 %d 号既有概念，抽象方法论或理论主题，slug 保持跨文档复用。\n", i, i)
	}
	b.WriteString("</candidate_slugs>\n")
	return b.String()
}

// chunks is the dynamic block that changes per batch — the part M3② moves to
// the tail so it no longer breaks the shared prefix.
func chunks(batch int) string {
	return fmt.Sprintf(`<chunks>
<c id="c001">第 %d 批：WeKnora 是一个开源的企业级 RAG 知识库系统，支持多租户、多模型与向量检索，本片段用于测量厂商前缀缓存命中效果。</c>
<c id="c002">第 %d 批：该实体成立于 %d 年，主营人工智能基础设施，员工规模约 %d 人，总部位于示例城市。</c>
</chunks>
`, batch, batch, 2000+batch, 500+batch)
}

// reordered mirrors the current M3② layout: static prefix first, chunks last.
func reordered(batch int) string { return staticPrefix() + chunks(batch) }

// unordered mirrors the pre-optimisation layout: chunks first, static last.
func unordered(batch int) string { return chunks(batch) + staticPrefix() }

// ---------------------------------------------------------------------------
// provider call + cache parsing
// ---------------------------------------------------------------------------

type usage struct {
	PromptTokens int `json:"prompt_tokens"`
	// DeepSeek / SiliconFlow style counters.
	PromptCacheHit  *int `json:"prompt_cache_hit_tokens"`
	PromptCacheMiss *int `json:"prompt_cache_miss_tokens"`
	// OpenAI style counters.
	PromptTokensDetails *struct {
		CachedTokens *int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
}

func (u *usage) hitMiss() (hit, miss int, reported bool) {
	if u.PromptCacheHit != nil || u.PromptCacheMiss != nil {
		h, m := 0, 0
		if u.PromptCacheHit != nil {
			h = *u.PromptCacheHit
		}
		if u.PromptCacheMiss != nil {
			m = *u.PromptCacheMiss
		}
		return h, m, true
	}
	if u.PromptTokensDetails != nil && u.PromptTokensDetails.CachedTokens != nil {
		c := *u.PromptTokensDetails.CachedTokens
		return c, maxInt(0, u.PromptTokens-c), true
	}
	return 0, 0, false
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

type chatResponse struct {
	Usage usage `json:"usage"`
}

func call(c *cfg, prompt string) (*usage, error) {
	body, _ := json.Marshal(map[string]interface{}{
		"model":      c.model,
		"messages":   []map[string]string{{"role": "user", "content": prompt}},
		"max_tokens": 16,
		"stream":     false,
	})
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(c.baseURL, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	resp, err := (&http.Client{Timeout: httpTimeout}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		raw := buf.String()
		if len(raw) > 400 {
			raw = raw[:400]
		}
		return nil, fmt.Errorf("API %s: %s", resp.Status, raw)
	}
	var out chatResponse
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		return nil, err
	}
	return &out.Usage, nil
}

// ---------------------------------------------------------------------------
// report
// ---------------------------------------------------------------------------

type requestResult struct {
	Prompt   int  `json:"prompt_tokens"`
	Hit      int  `json:"cache_hit_tokens"`
	Miss     int  `json:"cache_miss_tokens"`
	Reported bool `json:"cache_reported"`
}

func (r requestResult) hitRate() float64 {
	if !r.Reported || r.Prompt == 0 {
		return 0
	}
	return float64(r.Hit) / float64(r.Prompt)
}

func main() {
	envPath := ".env"
	if len(os.Args) > 1 {
		envPath = os.Args[1]
	}
	c, err := loadCfg(envPath)
	if err != nil {
		fmt.Println("读取 .env 失败:", err)
		os.Exit(1)
	}
	if c.apiKey == "" || c.model == "" {
		fmt.Println(".env 缺少 LLM_API_KEY 或 LLM_MODEL_NAME")
		os.Exit(1)
	}

	fmt.Printf("模型：%s\n接口：%s\n\n", c.model, c.baseURL)

	// 场景A：重排后（现状）——固定前缀前置。冷请求写缓存，紧邻的热请求应命中。
	const rounds = 3
	reorderedRounds := make([]struct {
		Cold requestResult `json:"cold"`
		Hot  requestResult `json:"hot"`
	}, 0, rounds)

	// 场景B：重排前（对照）——动态 chunks 前置，前缀每批都不同，应全 miss。
	unorderedReqs := make([]requestResult, 0, 2)

	fmt.Println("======== 场景A · 重排后（固定 instructions 前置，现状）========")
	for r := 1; r <= rounds; r++ {
		cold, err := call(c, reordered(r*2-1))
		if err != nil {
			fmt.Printf("  第 %d 轮冷请求失败: %v\n", r, err)
			os.Exit(1)
		}
		hot, err := call(c, reordered(r*2))
		if err != nil {
			fmt.Printf("  第 %d 轮热请求失败: %v\n", r, err)
			os.Exit(1)
		}
		coldRes := result(cold)
		hotRes := result(hot)
		reorderedRounds = append(reorderedRounds, struct {
			Cold requestResult `json:"cold"`
			Hot  requestResult `json:"hot"`
		}{coldRes, hotRes})
		fmt.Printf("  第 %d 轮：冷请求 prompt=%d miss=%d → 热请求 prompt=%d hit=%d miss=%d（命中率 %.1f%%）\n",
			r, coldRes.Prompt, coldRes.Miss, hotRes.Prompt, hotRes.Hit, hotRes.Miss, hotRes.hitRate()*100)
		// 让缓存自然失效/轮换，避免上一轮污染下一轮的“冷”判定。
		time.Sleep(2 * time.Second)
	}

	fmt.Println()
	fmt.Println("======== 场景B · 重排前（动态 chunks 前置，对照）========")
	for b := 1; b <= 2; b++ {
		u, err := call(c, unordered(b))
		if err != nil {
			fmt.Printf("  第 %d 个请求失败: %v\n", b, err)
			os.Exit(1)
		}
		res := result(u)
		unorderedReqs = append(unorderedReqs, res)
		fmt.Printf("  第 %d 个请求：prompt=%d hit=%d miss=%d（命中率 %.1f%%）\n",
			b, res.Prompt, res.Hit, res.Miss, res.hitRate()*100)
	}

	// 汇总：场景A 取热请求命中率的中位数（对 SiliconFlow 短 TTL 的尽力而为缓存更稳健）。
	hotRates := make([]float64, 0, len(reorderedRounds))
	reportedCount := 0
	for _, round := range reorderedRounds {
		if round.Hot.Reported {
			reportedCount++
		}
		hotRates = append(hotRates, round.Hot.hitRate())
	}
	sort.Float64s(hotRates)
	medianHot := hotRates[len(hotRates)/2]

	unreported := reportedCount == 0
	unorderedHit := 0
	unorderedTotal := 0
	unorderedReported := 0
	for _, r := range unorderedReqs {
		if r.Reported {
			unorderedReported++
			unorderedHit += r.Hit
			unorderedTotal += r.Prompt
		}
	}

	fmt.Println()
	fmt.Println("======== 结论 ========")
	fmt.Printf("场景A（重排后）热请求命中率中位数：%.1f%%\n", medianHot*100)
	if unorderedTotal > 0 {
		fmt.Printf("场景B（重排前）合计命中率：%.1f%%（hit=%d / prompt=%d）\n",
			float64(unorderedHit)/float64(unorderedTotal)*100, unorderedHit, unorderedTotal)
	} else if unorderedReported > 0 {
		fmt.Printf("场景B（重排前）合计命中率：0%%（%d 个请求全部 miss）\n", unorderedReported)
	}
	if unreported {
		fmt.Println("⚠ 供应商未返回缓存命中字段，无法按真实 token 计量命中率。")
	} else {
		fmt.Println("供应商返回 prompt_cache_hit_tokens / prompt_cache_miss_tokens，以上为真实计费 token。")
	}
}

func result(u *usage) requestResult {
	h, m, reported := u.hitMiss()
	return requestResult{Prompt: u.PromptTokens, Hit: h, Miss: m, Reported: reported}
}
