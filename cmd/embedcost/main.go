// Command embedcost turns the measured embedding-cache hit-rate reductions
// (embedbench: 100% full rebuild / 66.7% incremental; cachebench: 100% across
// restart) into concrete money saved, using the real token count the
// embedding provider bills for and the unit price recorded in
// config/builtin_models.yaml (bge-m3 = 0.07 CNY / 1M tokens).
//
// It calls the provider once with the same 30 chunks embedbench used, reads
// the response's usage.prompt_tokens (the actual billable count), then scales
// the three measured reductions up to realistic knowledge-base sizes.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const pricePerMillion = 0.07 // bge-m3 元/1M token，与 config/builtin_models.yaml 一致

type cfg struct {
	apiKey    string
	baseURL   string
	modelName string
}

func loadCfg(path string) (*cfg, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	c := &cfg{baseURL: "https://api.siliconflow.cn/v1"}
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
		case "EMBEDDING_API_KEY":
			c.apiKey = strings.TrimSpace(kv[1])
		case "EMBEDDING_BASE_URL":
			c.baseURL = strings.TrimSpace(kv[1])
		case "EMBEDDING_MODEL_NAME":
			c.modelName = strings.TrimSpace(kv[1])
		}
	}
	return c, sc.Err()
}

// promptTokens calls the provider once and returns the billable input tokens.
func promptTokens(ctx context.Context, c *cfg, texts []string) (int, error) {
	body, _ := json.Marshal(map[string]interface{}{
		"model":           c.modelName,
		"input":           texts,
		"encoding_format": "float",
	})
	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	resp, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		trunc := raw
		if len(trunc) > 400 {
			trunc = trunc[:400]
		}
		return 0, fmt.Errorf("API %s: %s", resp.Status, string(trunc))
	}
	var out struct {
		Usage struct {
			PromptTokens int `json:"prompt_tokens"`
			TotalTokens  int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return 0, err
	}
	return out.Usage.PromptTokens, nil
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
	if c.apiKey == "" || c.modelName == "" {
		fmt.Println(".env 缺少 EMBEDDING_API_KEY 或 EMBEDDING_MODEL_NAME")
		os.Exit(1)
	}

	// 与 embedbench 完全相同的 30 个 chunk。
	texts := make([]string, 30)
	for i := range texts {
		texts[i] = fmt.Sprintf("第 %d 章：WeKnora 是一个开源的企业级 RAG 知识库系统，支持多租户、多模型与向量检索。本片段用于评测重建索引时 embedding 缓存的真实命中效果，内容主题为机器学习与知识管理。", i+1)
	}

	ctx := context.Background()
	tokens, err := promptTokens(ctx, c, texts)
	if err != nil {
		fmt.Println("调用 embedding 失败:", err)
		os.Exit(1)
	}
	if tokens <= 0 {
		fmt.Println("provider 未返回 usage.prompt_tokens，无法按真实 token 计量")
		os.Exit(1)
	}

	perChunk := float64(tokens) / float64(len(texts))
	// 三个已实测的降幅（embedbench / cachebench）。
	full := 1.0                // 完全重建：30 → 0，100%
	incremental := 20.0 / 30.0 // 增量重建：30 → 10，66.7%
	// 省钱 = 命中省掉的 token × 单价（元/1M）。
	saveFull := float64(tokens) * full * pricePerMillion / 1e6
	saveInc := float64(tokens) * incremental * pricePerMillion / 1e6

	fmt.Println()
	fmt.Println("======== embedding 缓存省钱实测（真实 API token 计量）========")
	fmt.Printf("模型：%s，单价：%.2f 元/1M token（config/builtin_models.yaml）\n", c.modelName, pricePerMillion)
	fmt.Printf("30 个 chunk 真实消耗：%d prompt_tokens（单 chunk 约 %.0f token）\n", tokens, perChunk)
	fmt.Println()
	fmt.Println("【30 chunk 微观测算】")
	fmt.Printf("  完全重建（降幅 100%%，省 %d chunk）：省 %.4f 元\n", 30, saveFull)
	fmt.Printf("  增量重建（降幅 66.7%%，省 %d chunk）：省 %.4f 元\n", 20, saveInc)
	fmt.Println()
	fmt.Println("【放大到真实知识库规模（每次重建的重复 embedding 成本）】")
	for _, n := range []int{1_000, 10_000, 100_000, 1_000_000} {
		cost := float64(n) * perChunk * pricePerMillion / 1e6
		fmt.Printf("  %8d chunk 完全重建：无缓存 %.2f 元 → 有缓存 %.2f 元（省 %.2f 元）\n",
			n, cost, cost*(1-full), cost*full)
	}
}
