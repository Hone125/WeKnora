# 统一评测入口与证据保存

本入口调用正在运行的 WeKnora 后端，不会自动部署 Docker、导入模型配置或注册账号，尚不代表“干净环境一条命令部署”。需要 Node.js 18 及以上；门禁模式还需要 Go。

## Windows PowerShell

在仓库根目录，设置已有评测账号：

```powershell
$env:EVAL_EMAIL = '你的本地评测账号'
$env:EVAL_PASSWORD = '你的本地评测密码'
node scripts/eval-runner.mjs default
```

追加 `--gate` 可在同一结果上运行质量门禁。Git Bash 中原有的 `bash scripts/eval.sh default` 和 `bash scripts/eval-gate.sh default` 也调用该入口。凭据只放本机环境变量，不提交到仓库，也不要粘贴到聊天中。

默认不再使用写死的示例密码。服务端目前只实现 default 数据集，其他名字会被客户端拒绝，避免不同名称实际跑同一份数据。

## 可配置项

| 环境变量 | 默认或说明 |
|---|---|
| BASE_URL | http://localhost:8080 |
| EVAL_EMAIL / EVAL_PASSWORD | 必须提供 |
| EVAL_CHAT_ID / EVAL_RERANK_ID / EVAL_KB_ID | 可指定已有模型、知识库；空值沿用后端选择 |
| EVAL_TIMEOUT_MS | 全流程最长等待 900000 毫秒 |
| EVAL_REQUEST_TIMEOUT_MS | 每次 HTTP 请求最长等待 30000 毫秒 |
| EVAL_POLL_MS | 轮询间隔 2000 毫秒 |
| EVAL_GATE_CONFIG | eval_gate.json |
| EVAL_OUTPUT_DIR | 仓库下 eval-results；每次独立 run-* 子目录 |

## 成功和失败的界限

- 成功：任务成功且完成全部题目，六项检索和六项答案指标有效，用量、耗时字段齐全，Token 总量一致。
- 退出码 0：评测通过；门禁模式下也表示没有超阈值退化。
- 退出码 1：门禁发现质量退化。
- 退出码 2：登录、请求、任务、超时、结果或门禁运行错误。
- HTTP 错误、重定向和无效响应立即失败，不回显可能包含秘密的服务端正文。创建任务的 POST 不自动重试。
- 客户端超时不代表取消服务器任务。已有任务可能继续消耗模型额度，应先检查任务状态，避免直接重复运行。

## 保存的证据

- result.json：成功结果，递归移除明确命名的凭据字段，但仍可能含评测文本，分享前需检查。
- manifest.json：运行时间、退出码、客户端 commit、工作区是否有改动、五个 parquet 文件与本地配置的 SHA-256。
- gate.json：门禁输出（仅门禁模式）。
- failure.json：运行失败摘要。

指纹标记的是客户端文件，不能证明远程服务器加载了相同文件或代码版本，因此 manifest 明确记录 server_revision_verified=false。Token 用量不是货币费用，不把缺失费用称为免费。默认输出目录已加入 gitignore。

## 模型用量日期口径

模型页面按浏览器本地日历日期转换为 UTC 请求；后端查询区间为 **[start, end)**，结束时间不包含在内。选择某一天时，end 是次日零点，避免相邻两天重复记账，也避免漏掉最后一秒的小数部分。API 调用方需遵循该边界。

## 验证命令

```text
node --test scripts/eval-runner.test.mjs
go test ./internal/evalgate ./cmd/evalgate
```

HTTP 测试使用本机模拟服务，证明客户端的异常处理与证据保存逻辑，不替代真实模型评测。真实模型与 PostgreSQL 联调仍需后端可用。
