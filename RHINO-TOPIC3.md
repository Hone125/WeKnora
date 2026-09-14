# 课题三 · 质量评测基线与成本可观测

> 腾讯犀牛鸟开源人才培养计划 · WeKnora 开源实战课题（工程体系方向）
> 交付版本：Tag `rhino-2026-final-3`（commit `af5880a8`）｜ 开发分支 `rhino-topic3`
> 规模：**14 commits · 94 files · +21,235 行**（相对上游 `Tencent/WeKnora` 分叉点）

---

## 30 秒版

**要解决的问题**：WeKnora 本来有评测指标，但**跑完就丢、无法复现、没有门禁、成本是盲区**——
换台机器跑出的数不一样；检索质量悄悄退化没人发现；模型花了多少钱只能翻日志。

**做的事**：把它改造成一套**可复现、可回归、可计价**的工程评测体系。

| 模块 | 一句话说明 | 主要落点 |
|---|---|---|
| **M1** 可复现评测 | 评测任务/配置/结果从内存搬到数据库，一条命令跑出检索·答案·成本·耗时四类结果 | `scripts/eval.sh`、`internal/application/service/evaluation.go`、PG `000091` / SQLite `000013` |
| **M2** 成本可观测 | 模型调用旁路写入结构化账本，模型页按模型与时间区间查看调用量·命中率·费用 | `internal/types/model_usage.go`、`internal/handler/model_usage.go`、`frontend/src/views/settings/ModelSettings.vue`、PG `000092` / SQLite `000014` |
| **M3①** embedding 两级缓存 | 进程内 LRU + DB 持久化，重建索引不再重复调 provider | `internal/models/embedding/cache.go`、PG `000093` / SQLite `000015` |
| **M3②** prompt 前缀重排 | Wiki prompt 改为「固定指令前置、动态内容后置」，提升厂商前缀缓存命中率 | `internal/agent/prompts_wiki.go` |
| **M4** CI 质量门禁 | 指标较基线退化超阈值 → CI 自动失败，并指出是哪几个指标退化了 | `internal/evalgate/`、`cmd/evalgate/`、`.github/workflows/eval-gate.yml` |
| **M5** 解析引擎基线（选做） | 引擎无关的解析质量指标库 + 8 引擎横向对比 | `internal/parsequality/`、`cmd/parsebench/`、`cmd/builtinbench/`、`cmd/cloudbench/` |

---

## 关键数字（全部可复现，非估算形容词）

### 检索基线 · 30 问 · `embedding_top_k=5`

| precision | recall | ndcg3 | ndcg10 | mrr | map |
|---|---|---|---|---|---|
| 0.108 | 0.500 | 0.488 | 0.488 | 0.483 | 0.483 |

同次完整 RAG 评测（含 LLM 生成）成本 **48,512 tokens**、耗时 **17,455 ms**；
答案质量 bleu1 0.117 / rouge1 0.251 / rougel 0.231。

### embedding 缓存降幅 · 真实 API 与真实 DB 实测

| 场景 | 无缓存 provider 调用 | 有缓存 | 降幅 |
|---|---:|---:|---:|
| 完全重建（同进程） | 30 | 0 | **100%** |
| 增量重建（2/3 文档未变） | 30 | 10 | **66.7%** |
| 跨重启重建（仅靠 DB 二级缓存） | 30 | 0 | **100%** |

云端 CI 在**正式评测调用链**上的直接计数（独立 workflow `topic3-cache-hit.yml`，同一进程连跑两遍 30 题，
计数来自 `embedding_measurement`，不是缓存表行数、不是字节比例）：

| 场景 | 真实 provider HTTP | memory_hits | database_hits | cache_misses |
|---|---:|---:|---:|---:|
| 冷缓存（第一遍） | 36 | 0 | 0 | 60 |
| 热缓存（第二遍） | **0** | 0 | **60** | **0** |

热缓存那遍 `memory_hits=0`、`database_hits=60`，说明命中**全部来自 DB 持久化二级缓存**
——即在正式调用链上证明了跨重启缓存的真实收益。

### CI 门禁拦截退化 · 真实云端 CI 实验

把检索阈值拉高到合法上限 0.99 制造真实退化后跑完 30 题，门禁报 `passed=false`，
**6 个检索指标全部被判定退化并逐项报出**：recall 0.5→0.367、map/mrr 0.483→0.328、
ndcg3/10 0.488→0.338、precision 0.108→0.08。

门禁不依赖真实模型 Key 的自证路径也已就绪：退化 fixture → exit 1 并报
`recall delta=-0.08 > tolerance 0.05`；基线 fixture → exit 0。

### 厂商 prompt 前缀缓存 · SiliconFlow `deepseek-ai/DeepSeek-V3` 实测

读厂商返回的 `prompt_cache_hit_tokens` / `prompt_cache_miss_tokens`（**不是**前缀字节比例）：

| 场景 | 块顺序 | 命中率 |
|---|---|---|
| 重排后（现状） | 固定 instructions 前置 + chunks 后置 | **98.2%** |
| 重排前（对照） | chunks 前置 + instructions 后置 | **0%** |

### 成本账本穿透

云端 CI（run 34688598889）真实评测跑完后，`scripts/usage-check.mjs` 查
`GET /api/v1/models/usage`，账本按模型聚合查出 **62 次调用 / 103,828 tokens**——
证明「真实调用链 → `model_usages` 表 → 聚合接口」是通的，而不只是代码加单测。

---

## 怎么复现

```bash
# 前置：Docker 基础设施已起（postgres / redis / docreader）；.env 已配模型 Key
#       内置 30 问 default 数据集由后端启动时自动加载，无需手动导入

# 1) 启动后端
./scripts/dev.sh app              # 监听 0.0.0.0:8080

# 2) M1 一条命令复现评测（四类结果：检索 / 答案 / 成本 / 耗时）
make eval                         # 等价于 bash scripts/eval.sh [数据集]

# 3) M4 质量门禁（跑评测 → 对比 eval_gate.json 基线阈值 → 判定 + 退出码）
make eval-gate                    # 等价于 bash scripts/eval-gate.sh [数据集]

# 4) 单元测试（不依赖 cgo，CI 可干净跑）
go test ./internal/evalgate/ ./internal/parsequality/

# 5) M5 解析引擎基线（builtin 引擎，需 DocReader 在跑，gRPC 50051）
go build -o builtinbench.exe ./cmd/builtinbench && ./builtinbench.exe localhost:50051

# 6) M3① 跨重启缓存实验（真实 PostgreSQL embedding_cache 表）
go build -o cachebench.exe ./cmd/cachebench && ./cachebench.exe .env

# 7) M3② 厂商前缀缓存命中率实测
go build -o wikicachebench.exe ./cmd/wikicachebench && ./wikicachebench.exe .env
```

> 注：import `types` 的包测试在本机受 gojieba cgo 运行时 DLL 缺失影响会崩溃，
> 因此门禁与解析质量两个新包**刻意不 import `types`**，保证 CI 能干净 `go test`。

---

## 目录导航

**先读这份** → `docs/rhino-topic3-final-report.md` · 结题报告（验收对照 + 模块详解 + 复现手册）

| 文档 | 内容 |
|---|---|
| `docs/rhino-topic3-plan.md` | 实施总纲 + 逐模块进度记录 |
| `docs/rhino-topic3-baseline.md` | 改造前基线（P0 之前的现状） |
| `docs/rhino-topic3-cache-experiment.md` | 缓存命中实验记录 |
| `docs/rhino-topic3-real-eval-ci.md` | 真实检索评测接入 CI 的编排说明 |
| `docs/rhino-topic3-evaluation-runner.md` | 评测运行器（eval-runner.mjs）操作说明 |
| `docs/rhino-topic3-acceptance-audit.md` | 验收整改记录（含未达标项的如实标注） |
| `docs/rhino-topic3-submission.md` | 提交手册与邮件模板 |

**核心代码**

| 路径 | 说明 |
|---|---|
| `internal/evalgate/` | M4 门禁判定核心 + 10 个单测 |
| `internal/parsequality/` | M5 解析质量指标库（覆盖率 / 相似度 / 结构保真）+ 11 个单测 |
| `internal/models/embedding/cache.go` | M3① 两级缓存（sha256 键 + LRU + 命中短路） |
| `internal/models/chat/usage.go` | M2 调用账本旁路写（`UsageRecorder` 注入） |
| `cmd/evalgate/` `cmd/parsebench/` `cmd/builtinbench/` `cmd/cloudbench/` `cmd/cachebench/` `cmd/embedcost/` `cmd/wikicachebench/` | 七个 CLI 工具 |
| `scripts/eval.sh` `scripts/eval-gate.sh` `scripts/eval-runner.mjs` | 复现 / 门禁脚本 |
| `.github/workflows/` 下 `eval-gate.yml` `topic3-eval-real.yml` `topic3-cache-hit.yml` `topic3-contracts.yml` | CI workflow |

---

## 边界与已知限制（如实标注）

1. **cgo 环境边界**：本机无可用 gcc 工具链，依赖 cgo 的包（gojieba / pg_query / sqlite-vec）编译或
   `go test` 会失败。新加的门禁与解析质量两个包刻意做成纯 Go，CI 可干净跑。
2. **评测依赖真实模型调用**：CI 门禁的 `gate-demo` job 用 fixture 自证，不需要模型 Key；
   真实端到端评测需要配好 `EVAL_*` secret 与模型 Key 的环境。
3. **8 个解析引擎中 4 个需自托管服务或 Rust 链接**：本机实测了 simple / builtin /
   mineru_cloud / paddleocr_vl_cloud 四个；指标库是引擎无关的，接入即可扩展。
4. **口径边界**：Wiki 固定前缀的**字节数**不等于厂商缓存命中率（厂商真实 token 实测见上表
   M3②）；评测结果保存在 `evaluation_tasks` 表，未另建 `evaluation_results` 表；
   账本只存 token 量、不存金额（费用按模型当前定价即时计算，避免历史账单失真）。

---

*过程与结论的完整证据链见 `docs/rhino-topic3-final-report.md`。*
