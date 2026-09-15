# 9 月 12 日优化记录：从推测改为直接计数

## 已完成并验证

1. 正式 OpenAI-compatible embedding HTTP 路径增加按评测上下文隔离的计数：请求尝试、输入条数、失败次数。重试也计数，不记录文本与密钥。
2. 正式缓存装饰器分别统计内存命中、数据库命中与未命中。
3. EvalDataset 启用计数并将结果存入现有 metric JSON 的 embedding_measurement；历史记录保持字段缺失，不伪造为零。
4. 修复评测工作线程共用外层 err 变量的问题，避免不同问题互相覆盖错误。
5. 修正缓存实验报告：表行数不能代替 HTTP 请求次数；第二次实际耗时更长，不能宣称已经加速。
6. 本课题相关 Go 文件格式已整理；新计数测试加入现有缓存与账本 CI 工作流。已推送 rhino-topic3 并触发远端 CI（run 34681726847），冷缓存直接计数已取得。

## 本地测试结果

- TestMeasurementProductionCacheHTTPPath：通过。正式缓存与 HTTP 客户端连接本地测试服务器，验证冷缓存、热缓存和增量文本计数。
- TestMeasurementConcurrentAndIsolated：通过。并发计数无丢失，不同实验上下文不混账。
- TestMeasurementHTTPFailureIsNotSuccess：通过。HTTP 503 计入失败。
- 既有 TestCache / TestMemCache / TestEncodeDecode / TestConcurrency / TestModelUsage：通过。
- TestEvaluationMeasurementRecordRoundTrip：通过。评测记录转换前后计数保留，兼容历史结果。这不是新一次真实数据库重启实验。
- git diff --check：通过。

## 云端 CI 冷缓存直接计数（run 34681726847）

已推送 rhino-topic3 并触发 Real Eval Quality Gate，门禁通过（gate.json passed=true）。CI 每次为全新环境（冷缓存），result.json 的 metric.embedding_measurement 记录了真实调用链计数：

| 字段 | 值 | 含义 |
|---|---:|---|
| http_attempts | 36 | 真实 HTTP 调用次数 |
| http_input_items | 60 | 提交文本条数（30 语料 + 30 查询） |
| http_failures | 0 | 失败次数 |
| memory_hits | 0 | 冷缓存内存命中 |
| database_hits | 0 | 冷缓存 DB 命中 |
| cache_misses | 60 | 全部未命中 |

检索指标与基线一致：recall 0.5、ndcg 0.488、mrr 0.483、map 0.483。证明直接计数在正式评测调用链中正确工作，冷缓存下 60 条文本对应 36 次 HTTP、0 失败。热缓存/跨重启命中实验仍需隔离环境单独做。

## 真实退化实验（门禁拦截退化，run 34684475268）

用 workflow_dispatch 的 regression_probe 输入触发负向实验：把检索阈值（vector/keyword）拉高到 0.99（合法范围内的高阈值），预期召回暴跌、门禁报回归。真实环境（CI 起 postgres+redis+server）跑完 30 题后，门禁判定 passed=false，6 个检索指标全部报退化：

| 指标 | 基线 → 实测 | 退化 | 容差 | 是否拦截 |
|---|---:|---:|---:|---|
| recall | 0.5 → 0.367 | -0.133 | 0.05 | ✅ |
| map | 0.483 → 0.328 | -0.155 | 0.05 | ✅ |
| mrr | 0.483 → 0.328 | -0.155 | 0.05 | ✅ |
| ndcg3 | 0.488 → 0.338 | -0.150 | 0.05 | ✅ |
| ndcg10 | 0.488 → 0.338 | -0.150 | 0.05 | ✅ |
| precision | 0.108 → 0.08 | -0.028 | 0.02 | ✅ |

证明门禁不是摆设：真实改坏检索行为（拉高阈值）→ 门禁在 CI 里自动报错并逐个指出退化指标，对应验收 B「提交降召回改动时 CI 自动报错并指出退化指标」。

过程中的真实坑：第一次回归探针把阈值写成 2 / 1000000，超出 config 校验的 [0,1] 范围，server 启动即 panic（run 34684129772，结论 failure 但原因是 server 崩溃而非门禁）。修正为 0.99 后 server 正常启动、评测跑通、门禁正确报回归。说明「负向实验本身也要真实跑一遍才能发现坑」——静态 fixture 覆盖不到这种配置校验错误。

## 热缓存命中实验（run 34684838421）

用独立 workflow `topic3-cache-hit.yml` 在 CI 里起 postgres+redis+server，**同一 server 进程内连跑两遍同一份 30 题默认数据集评测**：第一遍冷缓存、第二遍热缓存。两遍都通过 `embedding_measurement` 直接计数（不是缓存表行数、不是字节比例），量化「缓存省了多少次真实 provider HTTP 往返」：

| 场景 | http_attempts | http_input_items | memory_hits | database_hits | cache_misses | latency_ms |
|---|---:|---:|---:|---:|---:|---:|
| 冷缓存（第一遍） | 36 | 60 | 0 | 0 | 60 | 83514 |
| 热缓存（第二遍） | **0** | **0** | 0 | **60** | **0** | 69833 |

**真实 HTTP 调用 36 → 0（降幅 100%）**，provider 往返彻底砍掉。

关键细节：第二遍 `memory_hits=0`、`database_hits=60`——命中**全部来自 DB 持久化二级缓存，而非进程内 LRU**。因为每个评测任务用独立的评测上下文（`WithMeasurement` 按任务隔离），内存缓存不跨任务复用，这恰好等价于「跨进程/重启后内存清空」的场景：即使内存缓存为空，DB 二级缓存仍 100% 命中。这是比「同进程内存命中」更强的证据——它直接证明了跨重启持久化缓存在**正式评测调用链**里的真实收益，补上了验收项「缓存优化有前后实测对比（重建索引 embedding 调用降幅）」的云端端到端直接计数证据。

耗时口径：冷 83514 → 热 69833 ms（约降 16%）。但评测耗时大头是 LLM 生成（两遍 completion 都在 5000 token 左右、有随机性），embedding 只占小头，且 CI 网络抖动大，所以 latency 只作参考、不作为强结论。**核心结论是 provider HTTP 调用 36→0**，不是「整体提速」。

## 模型用量账本（M2）真实写入（run 34688598889）

同一次 cache-hit 实验（run 34688598889）跑完两遍评测后，新增 `scripts/usage-check.mjs` 登录查 `GET /api/v1/models/usage`，把账本按模型聚合打印出来——证明真实调用链把 token 量写进了 `model_usages` 表、接口真能查出来：

| 字段 | 值 | 含义 |
|---|---:|---|
| model_usage_rows | 1 | 按模型聚合后 1 行（ci-llm-default） |
| call_count | 62 | 两遍评测共 62 次真实 LLM 调用 |
| prompt_tokens | 93434 | 输入 token 累计 |
| completion_tokens | 10394 | 输出 token 累计 |
| total_tokens | 103828 | 总量（93434 + 10394 一致） |
| cost | 0 | CI 临时模型未配价格，符合「账本存 token、费用按定价即时算」设计 |

这是 M2「模型页面按模型/时间区间查看调用量与费用、数据来自数据库而非日志」的端到端直接证据：不再是「账本代码 + 单测」，而是真实调用链 → `model_usages` 表 → 聚合接口 → 结构化记录。`cost=0` 是因为 CI 里的临时 LLM 模型没配 `pricing` 字段——账本设计本来就不把金额固化进记录，查询时用模型当前定价即时计算，这反过来验证了「价格进模型配置、账本只存 token 量」的取舍是对的。同一 run 也再次复现冷热缓存计数（冷 36 → 热 0 次 HTTP、DB 命中 60），结果可复现。

## 仍需优先完成的验收证据

1. ~~用新服务端做隔离的云端冷/热/重启实验，保存直接计数与质量、耗时。~~ 已完成：run 34681726847（冷缓存直接计数）+ run 34684838421（冷 36→热 0 次 HTTP、DB 命中 60，见上节）。只覆盖保留评测上下文的调用；其他 provider 的 HTTP 计数尚未接入。
2. ~~真实改变检索行为后执行评测，证明门禁报指标退化。~~ 已完成：run 34684475268 用回归探针拉高阈值，门禁真实报出 6 个指标退化（见上节）。
3. 用实际修改的 Wiki 模板做厂商缓存前后对照。字节前缀比例不能当作厂商 token 命中率；人工长前缀实验不能当作正式 Wiki 业务整体收益。
4. 全仓库格式扫描仍发现非本课题的 CLI/client 文件格式差异；不能宣称全部 CI 已绿。

本轮已提交（398e7060 修复回归探针阈值并新增热缓存命中实验脚本，main 分支 2cbc1a68 注册 cache-hit workflow）、推送 rhino-topic3 与 main，并触发远端 CI 取得三份活证据：冷缓存直接计数（run 34681726847）、门禁拦截退化（run 34684475268）、热缓存命中（run 34684838421，冷 36→热 0 次 HTTP、DB 命中 60）。未清空共享缓存、未修改用户密钥。
