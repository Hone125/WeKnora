#!/usr/bin/env bash
# 质量门禁：跑一次评测 → 对比基线阈值 → 输出判定报告 → 以退出码表示通过/阻断。
#
# 用法：
#   bash scripts/eval-gate.sh [dataset_id]
#
# 可用环境变量覆盖默认值：
#   BASE_URL         后端地址（默认 http://localhost:8080）
#   EVAL_EMAIL       登录账号（默认 admin@example.com）
#   EVAL_PASSWORD    登录密码（默认 pass123456）
#   EVAL_GATE_CONFIG 门禁配置 JSON 路径（默认 eval_gate.json）
#
# 退出码：0 = 门禁通过，1 = 门禁不通过（有指标退化超阈值），2 = 运行错误。
#
# 前置：后端已在本机运行，且 .env 已配置好模型/embedding 的 API Key。
#       编译 evalgate 需要本机有 Go 工具链。
set -euo pipefail

BASE_URL="${BASE_URL:-http://localhost:8080}"
EVAL_EMAIL="${EVAL_EMAIL:-admin@example.com}"
EVAL_PASSWORD="${EVAL_PASSWORD:-pass123456}"
DATASET_ID="${1:-default}"
GATE_CONFIG="${EVAL_GATE_CONFIG:-eval_gate.json}"

log()  { printf '\033[1;34m[门禁]\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m[门禁]\033[0m %s\n' "$*" >&2; }

# 0. 后端健康检查
if ! curl -sf "$BASE_URL/health" >/dev/null 2>&1; then
    warn "后端未运行：$BASE_URL/health 不可达"
    exit 2
fi

# 0b. 门禁配置存在性检查
if [ ! -f "$GATE_CONFIG" ]; then
    warn "门禁配置不存在：$GATE_CONFIG"
    exit 2
fi

# 1. 登录
log "登录 $EVAL_EMAIL ..."
TOKEN=$(curl -s -X POST "$BASE_URL/api/v1/auth/login" \
    -H 'Content-Type: application/json' \
    -d "{\"email\":\"$EVAL_EMAIL\",\"password\":\"$EVAL_PASSWORD\"}" \
    | node -e 'let s="";process.stdin.on("data",d=>s+=d);process.stdin.on("end",()=>{try{console.log(JSON.parse(s).token)}catch(e){console.log("")}})')
if [ -z "$TOKEN" ]; then
    warn "登录失败，请检查账号密码"
    exit 2
fi

# 2. 触发评测
log "触发评测（dataset=$DATASET_ID）..."
RESP=$(curl -s -X POST "$BASE_URL/api/v1/evaluation" \
    -H "Authorization: Bearer $TOKEN" \
    -H 'Content-Type: application/json' \
    -d "{\"dataset_id\":\"$DATASET_ID\",\"knowledge_base_id\":\"\",\"chat_id\":\"\",\"rerank_id\":\"\"}")
TASK_ID=$(printf '%s' "$RESP" | node -e 'let s="";process.stdin.on("data",d=>s+=d);process.stdin.on("end",()=>{try{console.log(JSON.parse(s).data.task.id)}catch(e){console.log("")}})')
if [ -z "$TASK_ID" ]; then
    warn "触发失败：$RESP"
    exit 2
fi
log "任务 ID：$TASK_ID"

# 3. 轮询直到完成
while :; do
    R=$(curl -s "$BASE_URL/api/v1/evaluation?task_id=$TASK_ID" -H "Authorization: Bearer $TOKEN")
    S=$(printf '%s' "$R" | node -e 'let s="";process.stdin.on("data",d=>s+=d);process.stdin.on("end",()=>{try{console.log((JSON.parse(s).data.task||{}).status??"")}catch(e){console.log("")}})')
    log "进度 status=$S"
    { [ "$S" = "2" ] || [ "$S" = "3" ]; } && break
    sleep 5
done
if [ "$S" = "3" ]; then
    warn "评测任务失败（status=Failed），无法进行门禁判定"
    exit 2
fi

# 4. 保存完整结果 JSON（含 metric），供 evalgate 判定
RESULT_FILE="$(mktemp)"
printf '%s' "$R" > "$RESULT_FILE"
log "结果已保存：$RESULT_FILE"

# 5. 编译 evalgate CLI
EVALGATE_BIN="$(mktemp -d)/evalgate"
log "编译 evalgate ..."
go build -o "$EVALGATE_BIN" ./cmd/evalgate

# 6. 门禁判定
log "门禁判定（config=$GATE_CONFIG）..."
set +e
"$EVALGATE_BIN" -config "$GATE_CONFIG" -result "$RESULT_FILE"
GATE_EXIT=$?
set -e

rm -f "$RESULT_FILE"

if [ "$GATE_EXIT" -eq 0 ]; then
    log "✅ 门禁通过：无指标退化超阈值"
else
    warn "❌ 门禁不通过：存在退化指标（见上方报告 regressions）"
fi
exit "$GATE_EXIT"
