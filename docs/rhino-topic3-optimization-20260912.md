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

## 仍需优先完成的验收证据

1. 用新服务端做隔离的云端冷/热/重启实验，保存直接计数与质量、耗时。冷缓存部分已由本次 CI 完成；热缓存与跨重启命中仍需隔离环境单独做。只覆盖保留评测上下文的调用；其他 provider 的 HTTP 计数尚未接入。
2. 真实改变检索行为后执行评测，证明门禁报指标退化。已有静态失败 fixture 不能替代该证据。
3. 用实际修改的 Wiki 模板做厂商缓存前后对照。字节前缀比例不能当作厂商 token 命中率；人工长前缀实验不能当作正式 Wiki 业务整体收益。
4. 全仓库格式扫描仍发现非本课题的 CLI/client 文件格式差异；不能宣称全部 CI 已绿。

本轮已提交（9e2d9b3b）、推送 rhino-topic3、移动 tag 至 rhino-2026-final-7，并触发远端 CI 取得冷缓存直接计数。未清空共享缓存、未修改用户密钥。
