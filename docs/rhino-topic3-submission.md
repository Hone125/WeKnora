# 课题三 · 最终提交手册

> 依据《WeKnora 腾讯犀牛鸟 课题实战流程指引 2026》整理
> 最终截止：**2026-09-13 00:00（北京时间）** = 9 月 12 日结束那一刻，请至少提前半天

---

## 0. 提交邮箱与标题

- **收件邮箱**：`wxg_prc_cpg@tencent.com`
- **邮件标题**：`【犀牛鸟实战提交】【课题三】【<姓名/GitHubID>】`

---

## 1. 需要你先提供的 4 项信息（唯一卡点）

| 项 | 说明 | 示例 |
|---|---|---|
| 姓名 | 真实姓名 | 张三 |
| GitHubID | GitHub 账号名 | zhangsan |
| 成果链接 | 你 fork 的 WeKnora 仓库地址（GitHub） | `https://github.com/zhangsan/WeKnora` |
| 选题编号确认 | 课题文档写「课题三」，用于 Tag 名与邮件标题 | `3`（如官方另有编号请替换） |

---

## 2. 提交步骤（在最终代码仓库根目录执行）

> 注意：本仓库当前是 zip 解压的源码、**没有 `.git`**。提交前需先在你的 GitHub 上 fork
> `Tencent/WeKnora`，clone 到本地，把本目录的改动覆盖进去（或直接在 clone 出的仓库里重新
> 应用改动），再进行下面的 git 操作。

```bash
# ① 提交所有代码
git status
git add .
git commit -m "final: rhino-bird submission"
git push origin HEAD

# ② 打最终 Tag（Tag 创建后不要删除或覆盖）
git tag -a rhino-2026-final-3 -m "Rhino-bird 2026 final"
git push origin rhino-2026-final-3

# ③ 生成 submission.yaml（官方脚本，从 git 自动读 repo/branch/sha/tag）
#    脚本会交互询问：姓名 / GitHubID / 选题编号 / 选题题目 / 成果链接
python3 - <<'PY'
from pathlib import Path
import json, subprocess
g = lambda *x: subprocess.check_output(["git", *x], text=True).strip()
q = lambda x: json.dumps(x, ensure_ascii=False)
a = lambda x: input(x + "：").strip()
name, gid, tid, title, url = map(a, ["姓名", "GitHubID", "选题编号", "选题题目", "成果链接"])
repo = g("config", "--get", "remote.origin.url")
branch = g("branch", "--show-current")
sha = g("rev-parse", "HEAD")
tag = g("describe", "--tags", "--exact-match", "HEAD")
data = f'''version: 1
student:
  name: {q(name)}
  github_id: {q(gid)}
topic:
  id: {q(tid)}
  title: {q(title)}
result:
  type: code
  url: {q(url)}
repository:
  url: {q(repo)}
  branch: {q(branch)}
  commit: {q(sha)}
  tag: {q(tag)}
'''
Path("submission.yaml").write_text(data, encoding="utf-8")
print("已生成 submission.yaml")
PY

# ④ 检查并提交 submission.yaml
sed -n '1,120p' submission.yaml
git add submission.yaml
git commit -m "docs: add submission metadata"
git push origin HEAD
```

> submission.yaml 记录的是第 ② 步打好 Tag 的最终代码版本；第 ④ 步提交后分支会多一个
> 「材料提交」Commit，这是正常的。不要移动、删除或覆盖最终 Tag。

**选题题目**（脚本交互询问时填写）：`质量评测基线与成本可观测`

---

## 3. 邮件正文模板（填好 4 项信息 + Tag/CommitSHA 后复制到邮件）

```
【犀牛鸟实战提交】【课题三】【<姓名>/<GitHubID>】

姓名：<姓名>
GitHubID：<GitHubID>
选题编号和题目：课题三 · 质量评测基线与成本可观测
成果类型：代码
成果链接：<GitHub 仓库地址>
代码版本：Tag rhino-2026-final-3 + CommitSHA <完整 SHA>

运行或阅读说明：
  - 结题报告：docs/rhino-topic3-final-report.md（验收对照 + 各模块成果 + 复现手册）
  - 一条命令复现评测：make eval（或 bash scripts/eval.sh）
  - 质量门禁：make eval-gate（或 bash scripts/eval-gate.sh）
  - 单元测试：go test ./internal/evalgate/ ./internal/parsequality/
  - 解析引擎基线：go build -o parsebench.exe ./cmd/parsebench && ./parsebench.exe
  - 详细步骤见 docs/rhino-topic3-final-report.md 第 4 节

完成情况：
  - M1 可复现评测：评测落库 + 四类结果（检索/质量/成本/耗时）+ 一条命令复现，基线 6 指标
  - M2 成本可观测：model_usages 账本表 + 模型页调用量/命中率/费用视图（数据来自 DB）
  - M3 缓存层：embedding 两级缓存（进程内 LRU + DB 持久化）+ Wiki prompt 固定前置重排
  - M4 CI 质量门禁：门禁判定核心 + CLI + CI workflow + 降召回 fixture 自证阻断
  - M5（选做）8 解析引擎横向解析质量基线
  - 逐条验收对照见结题报告第 1 节

已知问题：
  - gojieba cgo 运行时 DLL 缺失致 import types 的包测试崩溃（门禁/解析质量两个新包
    刻意不 import types 规避，CI 可干净 go test）
  - 真实端到端评测需带模型 Key 的环境（CI 门禁的 gate-demo 用 fixture 自证，无需 Key）
  - 8 解析引擎中 7 个需外部服务或 Rust 链接，本机仅实测 simple 引擎
  - 详见结题报告第 6 节
```

---

## 4. 提交前自查清单（对照流程指引第五节）

- [ ] 收件邮箱是 `wxg_prc_cpg@tencent.com`
- [ ] 邮件标题含选题编号 + 姓名 / GitHubID
- [ ] 成果链接能打开，附件无损坏
- [ ] 代码类写清 Tag + 完整 Commit SHA，并附 `submission.yaml`
- [ ] 邮件发送时间早于 2026-09-13 00:00（北京时间）
- [ ] 发送后保留已发送邮件，继续留意群消息和邮箱

> 如本指引与群里的最新通知不一致，以群里的最新通知为准。
