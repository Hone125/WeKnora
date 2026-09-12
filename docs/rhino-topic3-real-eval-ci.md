# 真实检索评测接入 CI（课题三 · 验收 B）

> 本文是「把真实检索评测接进 CI」的方案与操作手册。先讲总思路，再讲为什么这么选，最后给操作步骤。
> 配套文件：`.github/workflows/topic3-eval-real.yml`、`scripts/ci-eval.sh`。

---

## 一、这件事要解决什么（总思路）

验收 B 的原话是「CI 拦截降召回改动」。它问的不是「你有没有门禁代码」，而是：

> 当有人改动了检索逻辑、导致召回率偷偷下降时，CI 能不能在合并之前自动拦住它？

光靠单元测试证明不了这个。单元测试只能证明「代码跑通了」，证明不了「检索质量没变差」。要证明后者，必须**真的跑一次检索**——用真实模型、真实 embedding、真实数据集，把 30 个问题真正地问一遍、检索一遍、生成一遍，算出召回率，再和上次的基线比较。

打个比方：

- **单元测试**像体检时的「量身高、测体重」：快、便宜，但查不出你「检索这条腿是不是瘸了」。
- **真实评测**像「做一次全身 CT + 血液化验」：慢、贵，但能真的查出病。
- **CI 拦截**要的就是这台「能查出病的体检机器」，并且让它**每次体检完自动出报告、指标超标就亮红灯**。

之前的 `eval-gate.yml` 里已经有一个 `gate-demo` job，但它用的是**写死的 fixture**（假数据），证明的是「门禁判定逻辑本身是对的」，不是「真实评测跑通了」。本文要补上的是后者：把「真实评测 → 门禁判定」这一整条链路在 CI 里真正跑起来。

---

## 二、最小闭环（跑一次真实评测到底需要什么）

我把评测链路从头到尾追了一遍，结论是：**评测只走「纯文本 → embedding → 向量检索 → LLM 生成」，完全不碰文件解析**（`internal/application/service/knowledge_create.go` 里的 `createKnowledgeFromPassageInternal` 是纯文本直写，不经过 docreader；docreader 只负责解析 PDF/DOCX 文件）。

所以跑真实评测的最小环境只有四样东西：

```
GitHub Actions (ubuntu-latest)
│
├─ service: postgres（带 pgvector）   ← 存知识库 + 存向量 + 做向量检索
├─ service: redis                      ← asynq 任务队列 + 缓存
│
├─ 编译并后台启动 WeKnora 后端 server  ← go build ./cmd/server
│   ├─ 环境变量注入：数据库/Redis/加密密钥/模型 Key
│   └─ config/builtin_models.yaml 启动时自动注册 embedding + LLM 模型
│
└─ scripts/ci-eval.sh
    ├─ curl /auth/register  注册临时账号
    ├─ node eval-runner.mjs default --gate  跑 30 题真实评测 + 门禁
    └─ 退出码 1 = 召回退化（CI 红）；0 = 通过（CI 绿）
```

一句话总结：**一台带向量能力的数据库 + 一个消息队列 + 一个后端进程 + 一把模型 Key，就能把真实评测跑起来。** 不需要 docker-compose 那一整套（前端、docreader、MinIO、Neo4j 都是评测用不到的）。

---

## 三、四个关键决策（为什么这么选）

### 决策 1：用 GitHub `services:` 起 postgres + redis，而不是 docker-compose 全套

**理由**：docker-compose 会拉起前端、docreader、MinIO、Neo4j 等一大堆评测根本用不到的服务，构建镜像慢、失败面大。评测的最小闭环只需要 postgres（带 pgvector）+ redis + server。

**类比**：你只是想测「这口锅炒菜好不好吃」，没必要把整个厨房的家电——烤箱、洗碗机、咖啡机、微波炉——全搬出来插上电。只搬锅、灶、火就够了。

- PostgreSQL 镜像用 `paradedb/paradedb:v0.22.2-pg17`，和项目 `docker-compose.yml` 里的官方选择**完全一致**，自带 pgvector 扩展（以及 ParadeDB 的全文检索能力，虽然后者评测用不上）。这样迁移（`AUTO_MIGRATE=true` 自动建表 + 建向量列）和向量检索都不会踩「扩展没装」的坑。

### 决策 2：模型用 `config/builtin_models.yaml` 声明式自动注册，而不是手动调 API 注册

**理由**：评测服务（`internal/application/service/evaluation.go`）是通过 `modelService.ListModels(ctx)` 从**数据库**里找默认 embedding + KnowledgeQA 模型的。模型必须先存在于数据库里。而项目已经内置了声明式机制：后端启动后会自动把 `config/builtin_models.yaml` 里的条目 UPSERT 进 `models` 表（`internal/types/builtin_models_config.go`），并且 `is_builtin=true` 的模型对所有租户可见。

**类比**：**「交房时物业统一把水电煤气接通」**——住户（评测）进门就能用，不用自己一个一个去报装。YAML 就是那份「统一接通清单」，`${LLM_API_KEY}` 这种占位符会从环境变量读真实值。

CI 里由脚本生成一个临时 YAML，用 `BUILTIN_MODELS_CONFIG` 环境变量指向它，Key 来自 GitHub Secrets，不会写进仓库。

### 决策 3：账号用公开注册接口 `/auth/register`，而不是 lite 版的 `/auth/auto-setup`

**理由**：`/auth/auto-setup` 只在 `Edition == "lite"` 时可用，普通版会返回 403。而 `/auth/register` 默认 `self_serve` 模式开放（只要没设 `DISABLE_REGISTRATION=true`），注册即登录，注册时服务端自动创建个人空间（`create_personal`）。

**类比**：**「自助开户」**——CI 里用 curl 调一下注册接口，当场开一个临时账号，用完就随 CI 实例一起销毁，不污染任何真实数据。账号密码是 CI 临时实例里的，非敏感（真正敏感的只有模型 API Key，走 Secrets）。

### 决策 4：门禁用「相对上次基线的退化幅度」，而不是「绝对分数」

**理由**：评测分数取决于模型、embedding 模型、数据集版本，绝对值每次跑都可能漂移。真正要拦的是「**这次比上次基线掉了多少**」——召回率掉了超过阈值，才说明是代码改坏了，而不是模型本身波动。

**类比**：**体检不看「你血压 120 对不对」，而看「比上次体检血压飙升了多少」**——飙升就是健康告警，平稳就是正常。基线（`eval_gate.json` 里的 `baseline`）就是「上次体检的读数」，阈值（`thresholds`）就是「允许的最大波动」。

现有 `eval_gate.json` 的基线已经是 M1 实测值（`recall: 0.5` 等，embedding_top_k=5 的 30 题默认数据集），无需改动。首次在 CI 跑出真实分数后，若与基线偏差较大，再按实际值校准一次即可（见第五节）。

---

## 四、怎么跑（操作手册）

### 4.1 前提：在 fork 里配好 Secrets

真实评测要花真金白银（模型 API 额度），Key 必须放 GitHub Secrets，不能进仓库。打开 fork 的 **Settings → Secrets and variables → Actions → New repository secret**，配这些：

| Secret 名 | 含义 | 例子 |
|---|---|---|
| `EVAL_LLM_API_KEY` | 对话模型 Key | SiliconFlow 的 sk-xxx |
| `EVAL_LLM_MODEL_NAME` | 对话模型名 | `deepseek-ai/DeepSeek-V3` |
| `EVAL_LLM_BASE_URL` | 对话模型接口 | `https://api.siliconflow.cn/v1` |
| `EVAL_LLM_PROVIDER` | 对话模型协议 | `openai` |
| `EVAL_EMBEDDING_API_KEY` | 向量模型 Key | SiliconFlow 的 sk-xxx |
| `EVAL_EMBEDDING_MODEL_NAME` | 向量模型名 | `BAAI/bge-m3` |
| `EVAL_EMBEDDING_BASE_URL` | 向量模型接口 | `https://api.siliconflow.cn/v1` |
| `EVAL_EMBEDDING_PROVIDER` | 向量模型协议 | `openai` |
| `EVAL_EMBEDDING_DIMENSION` | 向量维度（须匹配模型输出） | `1024`（bge-m3） |
| `EVAL_RERANK_API_KEY` / `..._MODEL_NAME` / `..._BASE_URL` | Rerank 模型（可选，不配则跳过 rerank） | 留空 |

> 说明：对话模型和向量模型可以共用同一个 SiliconFlow Key（填两遍同一个值即可）。Rerank 不配也能跑，评测里的检索指标（precision/recall/mrr/map…）不依赖 rerank。

### 4.2 三种触发方式

| 触发 | 场景 | 说明 |
|---|---|---|
| `workflow_dispatch` | 手动 | 想立刻验证时点 **Actions → Real Eval Quality Gate → Run workflow** |
| `schedule`（周一） | 定期体检 | 每周自动跑一次，作为长期质量基线 |
| `pull_request`（改到关键路径） | 拦截降召回 | 改到 `internal/**`、`cmd/**`、`dataset/**`、`config/**` 时自动跑 |

真实评测消耗模型额度，所以**不是每次 push 都跑**，只在上面三种时机跑。这也符合「贵、慢、但能查出病」的定位。

### 4.3 结果怎么看

- **门禁通过**：CI 绿，`eval-runner.mjs` 退出码 0。
- **门禁拦截**：CI 红，退出码 1，日志和 `eval-results/` 里能看到具体哪个指标退化了多少（如 `recall` 掉了 0.08 超过阈值 0.05）。
- **数据/环境错误**：CI 红，退出码 2（登录失败、模型 Key 错、任务失败等），不等于质量退化。
- **证据**：`eval-results/` 作为 Artifact 上传，含 `result.json`（脱敏后的评测结果）、`manifest.json`（客户端指纹）、`gate.json`（门禁输出）。

---

## 五、诚实边界（哪些验证了、哪些还没）

这是给评委看的诚实交代，不夸大：

| 项 | 状态 |
|---|---|
| 评测链路理解（纯文本直写、不依赖 docreader） | ✅ 已从代码逐层确认 |
| 最小拓扑（postgres+redis+server+key） | ✅ 已确认，环境变量名与官方 docker-compose 一一对应 |
| 模型注册机制（builtin_models.yaml 自动 UPSERT） | ✅ 已确认，含单元测试覆盖 |
| 注册/登录/评测/门禁的接口与退出码 | ✅ 已确认，含 10 项 HTTP 客户端测试 |
| **CI 里端到端真实跑通一次** | ⚠️ 未验证——需要在有真实 fork + 配好 Secrets 的 GitHub 环境里触发一次；本机无 Docker/PostgreSQL 运行环境，无法离线复现远端 workflow |

**首次落地要做的三件事**（按顺序）：

1. 配好 4.1 的 Secrets；
2. `workflow_dispatch` 手动触发一次，观察 server 日志（`/tmp/server.log` Artifact）确认「迁移成功 → 模型注册成功 → 评测跑通」；
3. 跑通后，把这次的真实分数和 `eval_gate.json` 的基线比对：若偏差超过阈值，说明基线需要按当前模型/数据集校准一次，更新 `baseline` 再重跑确认绿。

**一个已知风险**：`embedding dimension` 必须和向量模型实际输出一致（bge-m3 是 1024），否则向量写库会报维度不符。已把它做成 Secret 可配，首跑时若报维度错误，改 `EVAL_EMBEDDING_DIMENSION` 即可。

---

## 六、文件清单

| 文件 | 作用 |
|---|---|
| `.github/workflows/topic3-eval-real.yml` | 真实评测 CI：起服务、编译启动 server、注入 Secrets、跑评测、上传证据 |
| `scripts/ci-eval.sh` | CI 内编排：等 server 健康 → 注册临时账号 → 跑评测 + 门禁 |
| `eval_gate.json` | 门禁基线 + 阈值（沿用 M1 实测值，首次 CI 跑通后按需校准） |
| `scripts/eval-runner.mjs` | 评测 HTTP 客户端（已有，本文复用，不重造） |
| `cmd/evalgate` | 门禁判定器（已有，本文复用，不重造） |

复用大于重造：评测客户端、门禁判定器、数据集、门禁配置都是现成的，本文只新增「CI 编排」这一层，把它们串进 GitHub Actions。
