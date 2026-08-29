# 课题三 · P0 基线报告（改造前）

> 腾讯犀牛鸟开源人才培养计划 · WeKnora 课题三
> 记录日期：2026-08-28
> 目的：在实施 M1–M5 改造**之前**，把「现有评测」端到端跑通并固化一份基线，作为后续每个模块前后对比的参照系。

---

## 1. 环境快照

| 项 | 值 |
|---|---|
| 后端 | Go 1.26，`go run ./cmd/server`，监听 `0.0.0.0:8080` |
| 数据库 | PostgreSQL（pgvector），`127.0.0.1:5432`，迁移 0→90 |
| 编译链 | MinGW GCC 16.1.0（WinLibs POSIX UCRT），CGO 开启 |
| 基础设施 | Docker：postgres(paradedb) / redis / docreader / langfuse |
| chat 模型 | `deepseek-ai/DeepSeek-V3`（SiliconFlow，openai 兼容） |
| embedding 模型 | `BAAI/bge-m3`（SiliconFlow，1024 维） |
| 评测数据集 | `./dataset/samples/*.parquet`（MS MARCO 精简样本） |

> **Windows 编译踩坑（已修复并落库）**：`sqlite-vec-go-bindings/cgo` 需要 `sqlite3.h`，
> Windows 无系统 `libsqlite3-dev`。方案：`third_party/sqlite/sqlite3.h`（复制自
> mattn/go-sqlite3 的 amalgamation 头文件，版本与运行时链接的 SQLite 一致），并在
> `scripts/dev.sh` 用 `cygpath -m` 注入 `CGO_CFLAGS -I`。详见 `third_party/sqlite/README.md`。

---

## 2. 基线指标（现有评测，1 个 QA pair）

任务 ID：`evaluation_10000_1787929663344_a801b5f8_default`
状态：`Success`（枚举 `EvaluationStatueSuccess=2`），finished/total = 1/1。

### 检索准确性

| 指标 | 值 |
|---|---|
| Precision | **1.0000** |
| Recall | **0.7500** |
| NDCG@3 | **1.0000** |
| NDCG@10 | **1.0000** |
| MRR | **1.0000** |
| MAP | **1.0000** |

### 答案质量

| 指标 | 值 |
|---|---|
| BLEU-1 | 0.0404 |
| BLEU-2 | 0.0191 |
| BLEU-4 | 0.0093 |
| ROUGE-1 | 0.1616 |
| ROUGE-2 | 0.0143 |
| ROUGE-L | 0.1010 |

---

## 3. 关键发现（直接决定后续设计）

1. **默认数据集太小，不足以做可信基线**：`dataset/samples` 仅 **1 个 QA pair、5 个 passage**。
   现有 `precision/recall/map` 全是基于单个 query 算出来的，方差极大、不具备统计意义。
   → M1 必须**扩充/替换为更大、更规范的数据集**（沿用 parquet 格式，扩到几十~上百个 QA pair），
   否则「可复现评测」与「CI 门禁」都站不住。

2. **评测状态存内存、重启即丢**：`evaluationMemoryStorage`（`internal/application/service/evaluation.go:36`）
   是进程内 `map`，任务在 `go func()` 后台跑。基线结果是靠「服务不重启 + 轮询」才拿到的。
   → 印证 M1「评测任务/配置/结果落库」的必要性。

3. **模型用量只写日志、无结构化账本**：本次评测实际调用了 embedding（5 段 corpus 向量化）+ chat（1 次问答），
   但只能从日志里翻 `[LLM Usage]`，没有可查询的 token/费用记录。
   → 印证 M2「模型调用账本」的必要性。

4. **状态枚举易误读**：`EvaluationStatue` 是 `iota`（Pending=0/Running=1/Success=2/Failed=3），
   GET 返回的 `status:2` 实为「成功」，不是「运行中」。后续 M1 做持久化与前端展示时要显式映射。

5. **评测自动建临时 KB 并事后删除**：`EvalDataset` 里 `CreateKnowledgeFromPassageSync` 后
   `defer DeleteKnowledge + DeleteKnowledgeBase`，评测资源不残留，但**无任何落库痕迹**，
   无法回溯「这次跑的是什么配置」。→ M1 的「配置快照」正好补这个洞。

---

## 4. 复现步骤（可独立验证）

```bash
# 0) 前置：Docker 基础设施已起（postgres/redis/docreader/langfuse）；.env 已配置模型 Key

# 1) 启动后端（Windows 原生，MinGW 已装）
export CC='<mingw64>/bin/gcc.exe'
export CXX='<mingw64>/bin/g++.exe'
export CGO_ENABLED=1
./scripts/dev.sh app            # 监听 0.0.0.0:8080

# 2) 注册 + 登录拿 token
curl -s -X POST http://localhost:8080/api/v1/auth/register \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","email":"admin@example.com","password":"pass123456"}'
TOKEN=$(curl -s -X POST http://localhost:8080/api/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"email":"admin@example.com","password":"pass123456"}' | jq -r '.token')

# 3) 触发评测（注意：路由是 /evaluation，无尾斜杠；带尾斜杠会 307）
curl -s -X POST http://localhost:8080/api/v1/evaluation \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d '{}'
#   → 返回 {"data":{"task":{"id":"evaluation_..."}}}，记下 id

# 4) 轮询结果（status=2 即 Success）
curl -s "http://localhost:8080/api/v1/evaluation?task_id=<id>" \
  -H "Authorization: Bearer $TOKEN"
#   → data.metric.retrieval_metrics / data.metric.generation_metrics
```

---

## 5. 下一步

进入 **M1 可复现评测**：评测任务/配置/结果落库 + 四类结果（检索/质量/成本/耗时）+
一条命令复现。基线报告中的「1 个 QA pair」问题将在 M1 一并解决（扩充数据集）。
