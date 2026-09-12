#!/usr/bin/env bash
# CI 编排脚本：等 server 健康 → 注册临时账号 → 跑真实评测 + 门禁。
#
# 前置：server 已由 workflow 后台启动（nohup），环境变量（DB / 模型 Key / BASE_URL /
# EVAL_EMAIL / EVAL_PASSWORD 等）已注入。本脚本只做「等就绪 + 开户 + 跑评测」三步，
# 不负责起服务、不负责部署。
#
# 退出码透传自 eval-runner.mjs：0 通过 / 1 召回退化 / 2 数据或环境错误。
set -euo pipefail

BASE_URL="${BASE_URL:-http://localhost:8080}"
EVAL_EMAIL="${EVAL_EMAIL:-ci-eval@weknora.local}"
EVAL_PASSWORD="${EVAL_PASSWORD:-ci-eval-pass-123}"

log() { printf '[ci-eval] %s\n' "$*"; }

# ---------------------------------------------------------------------------
# 1. 等 server 就绪（AUTO_MIGRATE 首次建表可能要几十秒，这里给足 180 秒）。
# ---------------------------------------------------------------------------
log "等待 server 就绪（${BASE_URL}/health）…"
ready=0
for _ in $(seq 1 90); do
  if curl -fsS --max-time 3 "${BASE_URL}/health" >/dev/null 2>&1; then
    ready=1
    break
  fi
  sleep 2
done
if [ "${ready}" -ne 1 ]; then
  log "server 在 180 秒内未就绪，请查 server-log artifact"
  exit 2
fi
log "server 已就绪"

# ---------------------------------------------------------------------------
# 2. 注册临时账号（self_serve 模式）。重复注册会返回 4xx，可忽略——后续登录用同一账号。
# ---------------------------------------------------------------------------
log "注册临时评测账号 ${EVAL_EMAIL} …"
if curl -fsS --max-time 10 -X POST "${BASE_URL}/api/v1/auth/register" \
  -H 'Content-Type: application/json' \
  -d "{\"username\":\"ci-eval\",\"email\":\"${EVAL_EMAIL}\",\"password\":\"${EVAL_PASSWORD}\"}" \
  >/dev/null 2>&1; then
  log "账号注册成功"
else
  log "账号可能已存在，跳过注册（继续用同一账号登录）"
fi

# ---------------------------------------------------------------------------
# 3. 跑真实评测 + 门禁。eval-runner.mjs 会：登录 → 触发 30 题评测 → 轮询 → 严格校验
#    成绩 → 调 evalgate 判定召回是否退化。退出码透传。
# ---------------------------------------------------------------------------
log "开始真实评测 + 门禁…"
node scripts/eval-runner.mjs default --gate
code=$?
log "评测/门禁结束，退出码 ${code}（0 通过 / 1 召回退化 / 2 数据或环境错误）"
exit "${code}"
