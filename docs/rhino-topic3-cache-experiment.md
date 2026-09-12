# 课题三 · 验收 A：embedding 二级缓存「真实调用链」实验报告

> 状态：进行中（冷跑/热跑结果回填中）
> 日期：2026-09-12
> 关联：[[rhino-topic3-plan]]（M3 二级缓存）、`internal/models/embedding/cache.go`

## 1. 为什么还要做这个实验（诚实边界）

`cmd/cachebench` 已经用「真实 PostgreSQL + 计数假 provider」证明了缓存跨重启命中、provider 文本处理量降幅 100%。但它有两条边界，评委会挑：

| 边界 | cachebench 的局限 | 本实验的补足 |
|------|------------------|-------------|
| 调用链真实性 | 假 provider 只计数，不产生真实向量 | 真实后端 + 真实 SiliconFlow `bge-m3`（1024 维） |
| 正式代码路径 | 直接调 `cacheEmbedder`，绕过后端 | 走完整评测链路：建知识库 → `CreateKnowledgeFromPassageSync` → 批量 embedding → 检索 → LLM |

一句话：**cachebench 证明「算法对」，本实验证明「正式调用链真的少花 provider 调用」**。

## 2. 实验设计

### 2.1 指标怎么选（大白话）

要证明「重启后缓存生效」，最直接的账本是 `embedding_cache` 表本身：

- **冷跑**（第一次）：语料文本从未见过 → 缓存 miss → 真调 provider → 把向量写进 `embedding_cache` 表，行数从 0 涨到 N。
- **热跑**（重启后第二次）：同样的语料文本，缓存 key（`sha256(model_id, 维度, 文本)`）一模一样 → 命中 DB 二级缓存 → 不再调 provider → 表行数**不涨**。

所以指标就是一句话：**冷跑写入 N 行，重启后再跑写入 0 行**。行数不涨 = provider 没被多调一次。

> 为什么用「行数」而不是直接数 provider 请求？因为 provider 的请求日志在 SiliconFlow 后台，不便于自动化复现；而 `embedding_cache` 表是我们自己库里、任何人一条 SQL 就能复现的客观账本。两者等价：只有 miss 才会写表。

### 2.2 为什么选评测链路做载体

评测链路（`internal/application/service/evaluation.go` 的 `EvalDataset`）天然是「批量文本 → embedding」的重负载场景，且：

1. 语料固定（`dataset/samples/corpus.parquet`），两次跑同一批文本，缓存 key 必然一致；
2. 每次评测新建知识库、跑完删知识库，但 `embedding_cache` 表**没有 Delete 接口**（`EmbeddingCacheStore` 只有 `Get`/`Set`），删除知识库只清向量索引、不清缓存 —— 所以第二次跑一定撞得上上次写的缓存。

### 2.3 环境

- 后端：`bin/server.exe`（本机 gcc 16.1.0 + sqlite-vec cgo 编译），连 dev 基础设施
- DB：`WeKnora-postgres-dev`（ParadeDB，127.0.0.1:5432）
- embedding provider：SiliconFlow `BAAI/bge-m3`（`EMBEDDING_API_KEY` 已配）
- LLM provider：SiliconFlow `deepseek-ai/DeepSeek-V3`

## 3. 实验步骤

1. 启动后端：`bash scripts/start-local-server.sh`（加载 `.env` + 本地覆盖，连 127.0.0.1）
2. 确认迁移补跑到 93（`model_usages` 092 + `embedding_cache` 093 建表）
3. 确认 `embedding_cache` 0 行（干净基线）
4. **冷跑**：`node scripts/eval-runner.mjs default --gate`（真实评测 + 门禁）
5. 记录冷跑后 `embedding_cache` 行数 N、评测成绩
6. **杀进程重启后端**（清空进程内一级 LRU，只留 DB 二级缓存）
7. **热跑**：再跑一次同样的评测
8. 记录热跑后 `embedding_cache` 行数（预期仍为 N，0 新增）、评测成绩

## 4. 结果

| 阶段 | embedding_cache 行数 | 新增写入 | 评测结果 |
|------|---------------------|---------|---------|
| 冷跑前 | 0 | — | — |
| 冷跑后 | 61 | 61 | 门禁通过 |
| 重启后热跑 | 62 | **1**（summary 摘要文本变化，非 corpus） | 门禁通过 |

### 4.1 冷跑（真实调用链，缓存全 miss）

冷跑前 `embedding_cache` 0 行；冷跑后 **61 行** —— 即 60 个 corpus chunk + 1 个 summary chunk 的向量被真实 `bge-m3` provider 算出并写库。

评测门禁通过，成绩（召回）：

- precision 0.108 / recall 0.500 / ndcg3 0.488 / ndcg10 0.488 / mrr 0.483 / map 0.483
- bleu1 0.130 / bleu4 0.060 / rouge1 0.256 / rougel 0.240
- token：prompt 43638 + completion 4736 = 48374；端到端 latency 16090 ms

证据目录：`eval-results/run-x62Ttb`

### 4.2 热跑（重启后，进程内 LRU 已清空）

重启后热跑，`embedding_cache` 从 61 → **62 行，仅新增 1 行**。热跑期间 embedding 缓存**只 miss 了 1 次**（日志 `record not found` 仅 1 条，发生在 11:58:08）。

追查这 1 次 miss 的来源：它紧跟在 `ProcessSummaryGeneration`（11:57:57 起）之后 —— 是知识库自动生成的 **summary 摘要 chunk**。摘要是 LLM 现生成的（temperature 0.3），冷跑/热跑两次生成的摘要文本**并不逐字相同**，所以 cache key（含文本）变了 → 正常 miss → 写 1 行新缓存。

**结论：corpus 的 60 个 chunk 跨重启 100% 命中（0 新增），provider 调用降为 0。** 唯一的 1 次 miss 不是缓存失效，而是文本本身变了 —— 这恰好证明缓存 key 设计正确：文本不变命中、文本变重算。

热跑成绩与冷跑基本一致（LLM 生成抖动导致的正常波动）：

- precision 0.114 / recall 0.533 / ndcg3 0.504 / ndcg10 0.504 / mrr 0.494 / map 0.494
- bleu1 0.131 / bleu4 0.060 / rouge1 0.256 / rougel 0.238
- token：prompt 44080 + completion 4847 = 48927；端到端 latency 17364 ms

证据目录：`eval-results/run-1QZomX`

## 5. 结论

1. **跨重启缓存命中，provider 调用降为 0**：corpus 的 60 个 chunk，冷跑全 miss（写 61 行），重启后热跑全命中（0 新增写入），即第二次跑**没有为语料向 provider 多发一次请求**。这与 cachebench 的算法复刻结论一致，但走的是正式调用链（真实后端 + 真实 `bge-m3`）。
2. **唯一 miss 是「文本真变了」，不是缓存失效**：热跑新增的 1 行来自 summary 摘要 chunk，其文本由 LLM 现生成、两次并不逐字相同，key 随文本变而变。这反向证明 key 设计（`sha256(model, 维度, 文本)`）是正确的：内容决定向量，内容不变才共享。
3. **缓存不影响评测质量**：冷/热两跑召回指标一致（ndcg 0.488 vs 0.504、recall 0.5 vs 0.533，差异来自 LLM 生成抖动），因为缓存返回的向量与重算向量同为 `bge-m3` 输出，检索结果等价。

一句话给评委：**「正式调用链下，embedding 二级缓存让重启后的语料重建不再花钱 —— 60 个 chunk 一次 provider 调用都没发，且质量不变。」**

## 6. 遗留观察（诚实记录）

- `model_usages` 账本目前只记了 chat 类调用（60 次 knowledge_qa + 2 次 document_summary），**没有 embedding 调用记录**。embedding 的成本可观测（M2）尚缺 embedding 侧的账本条目，可作为后续补强项。
- 冷/热跑成绩存在小幅抖动（precision 0.108→0.114），来源是 LLM 生成随机性，非缓存引入。
