# 课题三 · 质量评测基线与成本可观测 —— 实施方案

> 腾讯犀牛鸟开源人才培养计划 · WeKnora 开源实战课题
> 选题：**课题三（工程体系，难度中）**
> 目标：**卓越奖**
> 关键节点：**9/4 中期沟通会** · **9/13 00:00（北京时间）最终截止**

---

## 1. 课题定位（一句话）

把 WeKnora「有指标、但不可复现、无门禁、成本盲区」的现状，改造成一套**可复现、可回归、可计价**的工程评测体系——让检索/生成质量的每一次退化在 CI 里现形，让每一个 token 和每一次 embedding 调用都落到结构化账本上。

## 2. 现状盘点（基于代码实读）

| 课题痛点 | 代码证据 | 结论 |
|---|---|---|
| 评测状态存内存、重启即丢 | `internal/application/service/evaluation.go:36` `evaluationMemoryStorage = map[string]*EvaluationDetail`，任务在 `go func()` 后台跑，结果只写内存 | 需落库 |
| 指标齐全（precision/recall/MRR/NDCG/MAP/BLEU/ROUGE） | `internal/application/service/metric/` 全套 + `internal/application/service/metric_hook.go` | 指标**复用**，不重写 |
| 调用用量只写日志 | `internal/models/chat/usage.go:14` `logUsage` 仅 `logger.Infof("[LLM Usage]...")` | 需建结构化账本 |
| 能读厂商原生缓存命中，但无自建缓存层 | `internal/models/chat/prompt_cache.go` 解析 DeepSeek/OpenAI/Anthropic 的 cached_tokens | 命中率可观测，无缓存 |
| embedding 完全无缓存 | `internal/models/embedding/embedder.go` `Embed/BatchEmbed` 直连 provider | 重建索引 = 纯重复计算 |
| 无质量回归门禁 | `.github/workflows/` 仅 lint/build，无评测 | 需新增 CI 门禁 |

**关键判断**：难点不在「算指标」（已有），而在**工程化**——把散落的内存状态、日志、原生缓存信息串成「配置 → 执行 → 落库 → 门禁 → 可视化 → 计价」的完整链路。这正是「工程体系」型题眼。

## 3. 总体技术路线（四大支柱 + 一个选做）

课题 7 项任务收敛为 5 个模块，共享一条数据主线（**评测配置快照 → 一次运行 → 四类结果落库 → 模型调用账本**）：

```
┌────────────── 数据主线 ──────────────┐
配置快照(数据集+模型+分块参数+prompt) ──► 评测执行 ──► 结果落库
                                              │
      ┌───────────────┬───────────────┬───────┴───────┬──────────────┐
     M1 可复现评测    M2 成本可观测    M3 缓存层       M4 CI 质量门禁  M5(选做)
    (持久化+四类结果) (调用账本+页面) (embedding+prompt)(定时+阈值阻断) 解析引擎基线
```

| 模块 | 对应课题任务 | 一句话说明 |
|---|---|---|
| **M1 可复现评测** | 任务 1、2 | 评测任务/配置/结果持久化，单次运行同时产出「检索准确性 + 答案质量 + 成本 + 耗时」四类结果；一条命令复现 |
| **M2 成本可观测** | 任务 4 | 新建模型调用账本表，在 `logUsage` 处旁路写结构化记录；模型管理页新增「调用量 / 缓存命中率 / 费用」按模型+时间区间查询 |
| **M3 缓存层** | 任务 5、6 | ① embedding 缓存（同文本+同模型复用向量）；② 调整 prompt 拼装顺序（固定前置、可变后置），从 Wiki 生成阶段切入提厂商缓存命中率 |
| **M4 CI 质量门禁** | 任务 3 | 定时跑评测，指标较基线退化超阈值 → 阻断合并并报出具体退化指标 |
| **M5 选做** | 任务 7 | 8 个解析引擎横向解析质量基线 |

## 4. 关键设计决策（已拍板）

### 4.1 embedding 缓存载体 → 进程内 LRU + DB 持久化（两级）

**决定**：一级为进程内 LRU（`sync.Mutex` 保护，热命中零开销），二级为数据库持久化（PG 与 SQLite 都建表）。缓存键 = `(model_id, dimension, sha256(text))`；命中直接返回向量，未命中算完回填两级缓存。

**为什么这么做**：

1. **桌面版无 Redis，DB 是唯一跨重启的通用存储**。仓库里 Redis 基础设施大量存在（asynq、`stream/redis_manager.go`、`sandbox/session_binding_redis.go`），但桌面版走 SQLite + sqlite-vec，明确要能脱离 Redis 运行。用 Redis 会让桌面版缓存功能退化甚至失效。
2. **课题场景要求跨重启复用**。「重建索引」通常发生在服务重启之后，纯进程内内存只能解决单次进程内的重复，解决不了「同一段文本多次重建索引」的跨重启重复计算——而后者才是课题点名的核心场景。
3. **两级取长补短**。进程内 LRU 让同一批重建中大量相同文本近乎零延迟；DB 让跨重启也能命中。缓存未命中才进入 provider 调用，收益远大于一次 DB 查询开销。
4. **纯函数式、无副作用、可安全复用**。键不含租户/会话等易变上下文，只含「文本 + 模型 + 维度」，任何租户、任何路径算同一文本同一模型结果一致，天然可共享。

**实现落点**：新增 `embeddingCache` 装饰器，包在 `concurrencyEmbedder` **外层**（命中直接短路、不占 provider 并发配额），复用 `internal/models/embedding/concurrency_wrapper.go` 的装饰器写法。向量以 float32 二进制序列化存储（PG `bytea` / SQLite `BLOB`），不用 JSON 文本以省体积。

**风险与退路**（效果不好再换）：
- DB 写入放大 / 慢 → 只对「重建索引」类批量路径启用缓存，交互式查询 embedding 不缓存。
- 向量 blob 体积大 → 加容量上限 + TTL + 淘汰策略，或退化为纯进程内 LRU。
- 个别模型 embedding 有随机性 → 缓存键已含 dimension，另提供开关一键关闭。

### 4.2 费用单价来源 → 模型配置扩展价格字段（可空）

**决定**：在模型参数（`ModelParameters`）中新增 `Pricing` 结构，含 `currency`、`input_per_million`、`output_per_million`、`cached_input_per_million` 四档（每 1M token，可空）。账本只存 token 量，费用在读取时按 `tokens × 单价` 计算。

**费用口径**：

```
费用 = (prompt_tokens - cached_tokens)/1M × input单价
     + cached_tokens/1M × cached单价（空则回落 input单价）
     + completion_tokens/1M × output单价
```

**为什么这么做**：

1. **与验收「数据来自数据库」一致**。账本存 token，价格在模型表，费用按需计算——不把「价」和「量」耦合进日志。
2. **不硬编码 27 家厂商全量价目表**。价目表易过期、需持续维护，且大量用户用的是私有 endpoint / 中转，价格本就该用户自己知道。
3. **贴合 WeKnora 定位**：模型由用户自行配置，定价权归用户；`builtin_models.yaml` 可选预填主流模型价格，让默认租户开箱即有费用展示。

**风险与退路**：若评审认为「手填价格」不够自动化 → 追加内置 `provider_price` 价目表，模型未填价时回落厂商默认价（第二优先级，暂不做）。

### 4.3 待细化决策（进入对应模块时拍板）

- 评测结果表结构（双库迁移：versioned 下一个 000091、sqlite 下一个 000013）。
- 「一条命令」形态：`make eval` / 独立 CLI 子命令 / HTTP 接口 + 脚本封装。
- 门禁阈值：相对基线 + 可配置容忍度。
- prompt 拼装顺序调整的具体切入文件（`internal/agent/prompts_wiki.go` 等）。

## 5. 数据模型草案

新增三张表（PG `migrations/versioned/` + SQLite `migrations/sqlite/` 双库各写一套）：

1. **`evaluation_tasks`**：评测任务 + 配置快照（数据集 ID、模型 ID 组合、分块参数、prompt 版本）+ 状态 + 起止时间。
2. **`evaluation_results`**：单次运行的四类结果——检索准确性（precision/recall/NDCG/MRR/MAP）、答案质量（BLEU/ROUGE）、成本（token 与费用）、耗时（每 QA 对 + 汇总）。
3. **`model_usages`**（模型调用账本）：每次模型调用一行——model_id、model_type（chat/embedding/rerank/vlm）、purpose、prompt/completion/cached tokens、cache read/write/miss、duration_ms、归属（session/message/knowledge）、created_at。

另加 **`embedding_cache`** 表（见 4.1）。

## 6. 实施节奏（对齐两个硬节点）

| 阶段 | 时间 | 目标 | 产出 |
|---|---|---|---|
| P0 | 8/28–8/29 | 环境跑通 + 基线 | 现有评测端到端跑一次，拿到基线指标报告 |
| P1 | 8/29–9/2 | **M1** 核心 | 评测落库 + 四类结果 + 一条命令复现 |
| P2 | 9/2–9/3 | **M3①** + **M4** 骨架 | embedding 缓存前后对比数据（中期杀手锏）；CI 门禁骨架 |
| — | **9/4** | **中期沟通会** | 演示：一条命令出报告 + 缓存降幅实测 + 账本雏形 |
| P3 | 9/4–9/9 | **M2** + **M3②** + **M4** 完整 | 模型页费用视图；prompt 缓存命中率提升；CI 真·阻断合并 |
| P4 | 9/9–9/12 | **M5** + 打磨 | 解析引擎基线；文档/测试/benchmark 报告 |
| 自查 | 9/12 白天 | 提交检查 | Tag + submission.yaml + 邮件 |

## 7. 卓越奖差异化策略

1. **M5 选做做掉**——8 引擎解析质量横向基线，是绝大多数人放弃的部分。
2. **用「门禁阻断」自证**——提交一个故意降召回的反例改动，让 CI 真的报错并指出退化指标，比静态代码更有说服力。
3. **前后对比给「实测数字」而非形容词**——embedding 调用次数降幅、Wiki 缓存命中率提升，全部给出精确百分比 + 复现步骤。
4. **把「可复现」做到极致**——clean 环境一条命令得出一致指标，配置全快照、时间戳可回溯，他人可独立验证。
5. **代码质量对齐上游**——架构干净、双库迁移齐全、单测覆盖、README 写清运行/测试命令，像能提 PR 回主仓的样子。

## 8. 验收对照表

| 课题验收项 | 由哪块达成 |
|---|---|
| clean 环境一条命令得到与报告一致的指标 | M1 |
| 提交降召回改动时 CI 自动报错并指出退化指标 | M4 |
| 模型页面按模型+时间区间查看调用量/命中率/费用，数据来自 DB | M2 |
| 缓存优化前后实测对比（embedding 调用降幅、Wiki 命中率提升） | M3 |

## 9. 风险清单

| 风险 | 影响 | 缓解 |
|---|---|---|
| 桌面版无 Redis 限制缓存选型 | M3 退化 | 已选 DB 持久化，天然规避 |
| 无现成价格表 | M2 费用无法自动计算 | 模型配置手填价格 + builtin 预填 |
| 环境搭建耗时（PG/Redis/模型 Key） | 压缩开发时间 | P0 先跑通，尽早暴露 |
| 评测依赖真实模型调用，CI 成本高 | M4 门禁难落地 | 门禁用固定小数据集 + 定时低频触发 |
| 指标本身已齐全，创新空间小 | 卓越奖竞争力 | 靠 M5 + 门禁自证 + 实测对比拉开差距 |

---

*本文档为实施总纲，每个模块进入开发前会先产出细化设计并同步。*

## 10. 进度记录（持续更新）

### 2026-08-29 · M1 完成（可复现评测）

**已完成 M1 全部四步**，`make eval` / `bash scripts/eval.sh` 一条命令完整复现评测：

1. **数据集扩充**：默认数据集从 1 问扩充到 30 问、30 段语料（含干扰项），见 `internal/application/service/dataset.go`。
2. **持久化**：评测任务/配置/结果全部落库（PG `evaluation_tasks` + `evaluation_results`，SQLite 同步迁移）。已验证**重启后端后从 DB 读回完整四类结果**，持久化闭环成立。
3. **四类结果**：单次运行同时产出检索准确性（precision/recall/ndcg3/ndcg10/mrr/map）、答案质量（bleu/rouge）、成本（prompt/completion/total tokens）、耗时（latency_ms）。
4. **一条命令复现**：`scripts/eval.sh`（登录 → 触发 → 轮询 → 四类报告），Makefile 已加 `eval` 目标。

**实测基线**（`embedding_top_k=5`，30 问）：precision≈0.108、recall≈0.5、ndcg3≈0.488、ndcg10≈0.488、mrr≈0.483、map≈0.483；成本约 prompt 4.4 万 tokens / 耗时约 1.7–2.1 万 ms。

**过程中修复的两个关键 bug**：

1. **检索指标恒满分**：原 `metric_hook.go` 的 `recordFinish` 只用 `qaPair.Passages`（该问题自己的相关段落）做内容匹配，检索到的无关 chunk 被全部丢弃，`retrievalIDs` 退化成只有相关段落 → precision/recall/ndcg/mrr/map 全 1.0。修复：给 `HookMetric` 增加 `allPassages`（全量语料，按 pid 索引），内容匹配遍历全量 corpus 而非单题相关段落。见 `internal/application/service/metric_hook.go:161`。
2. **`embedding_top_k=30` 无区分度**：30 = 语料全量，检索结果恒含所有相关段落，指标无法区分好坏。修复：`config.yaml` 改为 `embedding_top_k: 5`。

**环境结论修正（重要，覆盖早前误判）**：

- **gcc 16.1.0（WinLibs MinGW-W64）编译 gojieba 完全正常**。早前「16.1.0 与 gojieba 不兼容会崩溃」是误判——那是临时测试脚本在 `CGO_CFLAGS="-O0 -g"` 特殊 flag 下的个例。证据：`server.exe` 二进制内嵌 81 处 "16.1.0"、WinLibs 16.1.0 的 c++ include 路径、gojieba 的 cppjieba 源码路径，证明 16.1.0 编译的 gojieba 已正常链接运行。
- **正确构建命令**（Windows 原生）：`CGO_CFLAGS="-Wno-deprecated-declarations -Wno-gnu-folding-constant -I<cygpath -m 的 third_party/sqlite>"`，且 `PATH` 需含 WinLibs `mingw64/bin`（否则 `undefined: pg_query.Parse`）。
- **正确启动方式**：用 `mktemp + sed 's/\r$//' + set -a; source .env; set +a` 加载 `.env`（`source .env` 缺 `set -a` 会导致变量未导出、`unsupported database driver` panic），再覆盖 `DB_HOST=127.0.0.1`、`REDIS_ADDR=127.0.0.1:6379`、`MINIO_ENDPOINT=127.0.0.1:9000` 等 docker 服务名为本机地址。

### 2026-08-29 · M2 完成（成本可观测）

**模型调用账本 + 模型页成本视图** 已全链路打通（后端完整 `go build ./...` 通过）：

**数据层（账本只存 token 量、不存金额）**

- `types/model.go`：新增 `ModelPricing`（每百万 token 单价，三档：input / output / cached_input，cached 空则回落 input）。
- `types/model_usage.go`（新）：`ModelUsageRecord`（每次调用的 token + 缓存命中细分）、`ModelUsageAggregate`（按模型聚合）、`ComputeCost()`（费用 = 未命中输入×input + 命中输入×cached + 输出×output）。
- 迁移：PG `000092_model_usages` + SQLite `000014_model_usages`（`model_usages` 表 + `(tenant_id, model_id, created_at)` 索引）。

**采集链路（零侵入旁路写）**

- 关键设计：chat 包是底层叶子依赖，不能反向 import 仓储层。仿照已有的 `chat.LocalImageResolver` 注入模式，在 `chat/usage.go` 定义包级钩子 `chat.UsageRecorder`，由 container 组装时注入。
- `logUsage`（所有 chat 模型调用的唯一收口点，11 处）签名扩展为接收 `modelID`，在打印日志后旁路调用 `UsageRecorder`，把 token/缓存细分写入账本。**异步 goroutine 写 + 5 秒超时**，掉一行只降级为日志、绝不阻塞对话主链。
- 已覆盖 provider：remote_api / openai_stream / ollama / anthropic（含流式 `processAnthropicStream`）。

**读取链路（聚合 + 费用即时计算）**

- `model_usage_repo.go`：`Record` + `Aggregate`（按 model_id/model_type 分组，SUM token、算缓存命中率）。
- `model_usage.go` service：`GetOverview(ctx, start, end)` 把聚合结果与模型定价关联，补模型名 + `ComputeCost`。
- `handler/model_usage.go` + `GET /api/v1/models/usage`（Viewer+，默认最近 7 天，支持 RFC3339/Unix 秒时间参数）。
- 前端 `ModelSettings.vue` 新增「模型用量与成本」表格（模型/类型/调用次数/输入输出 tokens/缓存命中率/费用），`api/model/index.ts` 加 `getModelUsage`。

**为什么费用单价放进模型配置（而非硬编码）**：价格是易变的、按厂商/模型区分的业务数据，账本若固化金额，价格一改历史数据就失真。所以账本只存 token 量，查询时用模型当前 `Pricing` 即时计算，价格调整自动生效。

### 2026-08-29 · M3① 完成（embedding 两级缓存）

**embedding 缓存载体最终选型：进程内 LRU + DB 持久化（两级）**，与 4.1 决策一致，全链路打通（`go build ./...` + `go vet` 通过）：

1. **核心装饰器** `internal/models/embedding/cache.go`：`cacheEmbedder` 包在 `concurrencyEmbedder` 外层——缓存命中直接短路、不占 provider 并发配额；未命中才进入 provider，算完回填两级缓存。
2. **缓存键** = `sha256(model_id \x00 dimension \x00 text)` 十六进制，只含「模型+维度+文本」，天然可跨租户共享。向量用 float32 小端二进制序列化（PG `bytea` / SQLite `BLOB`），省体积。
3. **两级结构**：一级进程内 LRU（`container/list` 真 LRU，`EMBEDDING_CACHE_SIZE` 可调，默认 4096）；二级 DB 持久化（异步 goroutine 回填、5 秒超时，掉一条只降级为重算、绝不阻塞主链）。
4. **解耦注入**：embedding 是叶子包，仿 `chat.UsageRecorder` 模式定义 `embedding.CacheStore` 接口，由 container 注入 `EmbeddingCacheRepository`（`embedding_cache` 表）。
5. **双库迁移**：PG `000093_embedding_cache` + SQLite `000015_embedding_cache`。
6. **单测覆盖** 9 个用例（LRU 淘汰、命中短路、批量只补 miss、二级跨实例命中、BatchEmbedWithPool 传自身、跨模型不串扰等），见 `cache_test.go`。

> 注：受 gojieba cgo 在测试二进制崩溃的遗留环境问题影响（任何 import `types` 的包测试都会崩溃），单测暂无法通过 `go test` 运行，但核心算法（LRU + 二进制序列化）已用独立纯 Go 程序验证 ALL OK，`go build ./...`/`go vet` 全通过。

### 2026-08-29 · M3② 完成（prompt 拼装顺序优化 · 固定前置）

**现状**：上游已对两个明确批量复用场景做过前缀缓存优化——`WikiChunkCitationPrompt`（同文档多批次，注释明确 "Block order matters for provider prefix caching"）和 `WikiPageModify*`（同源多页面，共享 source 前置）。但 `WikiSummaryPrompt`、`WikiKnowledgeExtractPrompt`、`WikiCandidateSlugPrompt` 仍是「动态 content 在前、固定 instructions 在后」。

**本次增量**：把这三个「每文档一次、跨文档批量 ingest 复用前缀」的 prompt 重排为「固定 instructions 前置、动态 content/previous_slugs 后置」，并同步把 instructions 里对数据块的 "above" 引用改为 "below"（instructions 内部的 "Extraction Scope rules above" 等相对引用保持不变）。

**为什么只做这三个、不做 TaxonomyPlan / Deduplication**：后两者是「整批一次」调用（输入 items 每次全变），不存在「同前缀、不同后缀」的多次调用模式，前缀缓存无从复用；重排只会引入改措辞风险而无收益。

**验证**（独立纯 Go 程序从源文件提取模板渲染，规避 gojieba 崩溃）：

| Prompt | `<document>` 之前固定前缀字节数 |
|---|---|
| WikiSummaryPrompt | 1861 |
| WikiKnowledgeExtractPrompt | 4494 |
| WikiCandidateSlugPrompt | 4039 |

同一知识库设置（语言/粒度相同）下，两个不同文档渲染出的前缀字节完全一致——批量 ingest 时第 2 个及以后的文档可复用这段长指令前缀、免重复计费。配套 `prompts_wiki_test.go` 新增 `TestWikiPrompt_StableInstructionPrefixAcrossDocuments`（跨文档前缀稳定）+ `TestWikiPrompt_PreservesPlaceholders`（防字段丢失）。

### 2026-08-29 · M4 完成（CI 质量门禁）

**质量门禁 = 门禁判定核心（可单测）+ CLI + 脚本 + CI workflow + 降召回自证 fixture**，全链路打通（`go build ./...` + `go vet` 通过，门禁单测可正常 `go test` 运行）：

1. **门禁判定核心** `internal/evalgate/`（新包）：`JudgeGate(current, cfg)` 把评测指标与基线对比，规则是「current < baseline - tolerance 即退化」。**刻意不 import `types`、用 `map[string]float64` 表示指标**——这绕开了「任何 import types 的包测试都被 gojieba cgo 崩溃拖垮」的遗留问题，让门禁逻辑的 10 个单测在 CI 里能干净跑。`FlattenEvaluationResponse` 从评测 API 原始响应提取 retrieval/generation 指标。
2. **CLI** `cmd/evalgate/`：读门禁配置 + 评测结果 JSON → 输出判定报告 → 退出码 0/1/2（通过/退化/错误）。
3. **脚本 + Makefile**：`scripts/eval-gate.sh`（登录 → 触发评测 → 轮询 → 存结果 → `go build` evalgate → 判定）+ Makefile `eval-gate` 目标，与 M1 的 `make eval` 对称。
4. **门禁配置** `eval_gate.json`：baseline 用 M1 实测基线（recall=0.5 等），thresholds 只对**确定性检索指标**设门禁（precision/recall/ndcg/mrr/map），生成质量指标（bleu/rouge）因受 LLM 随机性影响默认不设阈值、仅展示不阻断。
5. **CI workflow** `.github/workflows/eval-gate.yml`：`unit`（跑门禁单测）+ `gate-demo`（用降召回 fixture 自证门禁会 exit 1 并报出 recall 退化）两个 job；触发为 `schedule`（每周错峰）+ `workflow_dispatch` + `pull_request`（仅门禁相关文件变更时）。

**端到端自证（无需真实模型 Key）**：

| 输入 | 结果 |
|---|---|
| `docs/eval_gate_regression_fixture.json`（recall 0.5→0.42） | `passed:false`，精确报出 `recall delta=-0.08 > tolerance 0.05`，退出码 1 |
| 正常结果（recall=0.55） | `passed:true`，退出码 0 |

门禁单测 10 例全过：`TestJudgeGate_Pass/Regression/ImprovementNeverFails/SkipsMissing…/DeterministicOrder`、`TestLoadGateConfig*`、`TestFlattenEvaluationResponse*`。

**真实评测接入方式**（评审环境有模型 Key 时）：`bash scripts/eval-gate.sh default` 即跑真实评测再判定；CI 里把 gate-demo 的 fixture 换成该脚本并配 `EVAL_*` secret 即可。

### 2026-08-29 · M5 完成（8 解析引擎横向解析质量基线）

**解析质量基线 = 指标库（可单测）+ 8 引擎能力矩阵 + simple 引擎实测 + CLI**，全链路打通：

1. **指标库** `internal/parsequality/`（新包，刻意不 import `types`、纯字符串函数，CI 可干净 `go test`）：
   - `TextCoverage`：set-based 字符覆盖率——抓「内容被丢弃/截断」。
   - `TextSimilarity`：归一化 Levenshtein 相似度——抓「乱序/重复/乱码」。
   - `StructureFidelity`：Markdown 结构元素计数保真（标题/列表/表格/引用/代码块）——抓「结构拍平」，这是对下游分块和检索最致命的退化。
   - 单测 11 例全过（`go test ./internal/parsequality/`）。

2. **CLI** `cmd/parsebench/`：一条命令输出两部分——`engines`（8 引擎能力矩阵）+ `simple_engine_quality`（simple 引擎实测质量）。

3. **8 引擎能力矩阵**（`docparser.ListAllEngines` 实测）：

| 引擎 | 说明 | 文件类型数 | 本环境可用 | 不可用原因 |
|---|---|---|---|---|
| builtin | DocReader 内置解析（Python 服务） | 22 | ❌ | DocReader 服务未连接 |
| simple | Go 原生轻量解析（无外部依赖） | 17 | ✅ | — |
| anydoc | 进程内办公文档转换（Rust） | 16 | ❌ | 未以 `-tags anydoc` 编译进二进制 |
| weknoracloud | WeKnoraCloud 云解析 | 9 | ❌ | 未配置云凭证 |
| mineru | MinerU 自托管服务 | 10 | ❌ | 服务未配置 |
| mineru_cloud | MinerU 云 API | 10 | ❌ | API Key 未配置 |
| paddleocr_vl | PaddleOCR-VL 自托管 | 6 | ❌ | 服务未配置 |
| paddleocr_vl_cloud | PaddleOCR-VL 云 | 6 | ❌ | Token 未配置 |

4. **simple 引擎实测质量基线**（唯一无外部依赖、本机可测的引擎）：

| 文件类型 | coverage | similarity | structure_fidelity |
|---|---|---|---|
| md（无损透传） | 1.0 | 1.0 | 1.0 |
| txt（无损透传） | 1.0 | 1.0 | 1.0 |
| csv（→ Markdown 表格） | 1.0 | 1.0 | 1.0 |

**为什么只有 simple 能实测、为什么这样设计**：其余 7 个引擎各需外部服务（DocReader/MinerU/PaddleOCR-VL）或 Rust 链接（anydoc），本机无法拉起。**指标库是引擎无关的**——任何引擎的输出套同一套 coverage/similarity/structure 指标，即可与 simple 基线直接横向对比；这是「基线」而非「一次性 benchmark」：以后接入任一引擎，喂同一份 golden 文档，三个分数直接可比。

**过程中修复**：`parsebench.exe` 运行时 0xC0000135（STATUS_DLL_NOT_FOUND）——cgo 二进制运行时缺 `libgcc_s_seh-1.dll` 等，根因是只设了 `CC`/`CXX` 绝对路径、没把 WinLibs `mingw64/bin` 加进 `PATH`。修复：运行前 `PATH` 前置该目录。

### 2026-09-12 · 验收 A（真实调用链缓存）+ 验收 B（CI 真·门禁）闭环

**验收 A —— embedding 二级缓存「真实调用链」实测**（详见 `docs/rhino-topic3-cache-experiment.md`）：

- 用真实后端（`bin/server.exe`，本机 gcc 16.1.0 + sqlite-vec cgo 编译）+ 真实 SiliconFlow `bge-m3` 跑完整评测链路，补足 cachebench 只有「算法复刻」的缺口。
- 冷跑：`embedding_cache` 0 → 61 行（60 corpus chunk + 1 summary chunk）。
- 重启后端（清空进程内一级 LRU）后热跑：仅新增 1 行，且来自 LLM 现生成的 summary 摘要（文本逐字不同 → key 变 → 正常 miss）；**corpus 60 chunk 跨重启 100% 命中、provider 调用降为 0**。
- 冷/热召回指标一致（ndcg 0.488 vs 0.504，抖动来自 LLM 生成），缓存命中不影响质量。
- 附带落地：启动后端 AUTO_MIGRATE 把迁移从 91 补跑到 93，`model_usages`(092)、`embedding_cache`(093) 建表，解决「账本表名错配」。
- 新增 `scripts/start-local-server.sh`：本地启动编译好的后端连 dev 基础设施，支持反复「杀进程→重启」做跨重启实验。

**验收 B —— GitHub Actions 真实评测门禁通过**：

- `Real Eval Quality Gate`（`.github/workflows/topic3-eval-real.yml`）workflow_dispatch 触发，conclusion **success**，约 6.7 分钟。
- CI 成绩 precision 0.111 / recall 0.5 / ndcg 0.488 / mrr 0.483，与本地冷跑基线（0.108 / 0.5 / 0.488 / 0.483）**一致** → 干净容器环境一条命令复现。
- 门禁 `eval_gate.json` 基线 = M1 实测 + 紧阈值；CI 跑出 ndcg=基线、precision 略高于基线，正确判定 passed（exit 0），无需再校准。

### 待办（按 9/4 中期、9/13 截止倒排）

- **最终提交**：Tag + `submission.yaml` + 文档 + 邮件。
