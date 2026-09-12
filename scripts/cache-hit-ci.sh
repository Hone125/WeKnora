#!/usr/bin/env bash
# 热缓存命中实验的 CI 编排：等 server 健康 → 注册临时账号 → 跑两遍评测对比缓存。
#
# 前置：server 已由 workflow 后台启动（nohup），环境变量已注入。
# 与 ci-eval.sh 的区别：最后一步跑 cache-hit-eval.mjs（同一进程连跑两遍评测，
# 对比冷/热缓存的 embedding 直接计数），而不是跑单遍评测 + 门禁。
set -euo pipefail

BASE_URL="${BASE_URL:-http://localhost:8080}"
EVAL_EMAIL="${EVAL_EMAIL:-ci-eval@weknora.local}"
EVAL_PASSWORD="${EVAL_PASSWORD:-ci-eval-pass-123}"

log() { printf '[cache-hit] %s\n' "$*"; }

# ---------------------------------------------------------------------------
# 1. 等 server 就绪。
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
# 2. 注册临时账号。
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
# 3. 跑两遍评测，对比冷/热缓存 embedding 直接计数。退出码：0 两遍均成功 / 2 错误。
# ---------------------------------------------------------------------------
log "开始热缓存命中实验（同一进程连跑两遍评测）…"
node scripts/cache-hit-eval.mjs
code=$?
log "实验结束，退出码 ${code}（0 = 两遍均成功）"

# ---------------------------------------------------------------------------
# 4. 查模型用量账本（M2 · 成本可观测）：证明真实调用链把 token 量写进了 model_usages
#    表，并能从 /api/v1/models/usage 按模型/时间区间查出来。
# ---------------------------------------------------------------------------
if [ "${code}" -eq 0 ]; then
  log "查模型用量账本（M2）…"
  if node scripts/usage-check.mjs; then
    log "账本验证通过"
  else
    log "账本验证失败（M2 证据缺失）"
    code=2
  fi
fi

exit "${code}"
