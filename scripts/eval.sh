#!/usr/bin/env bash
# 评测一条命令复现：登录 → 触发评测 → 轮询 → 输出四类结果报告
#
# 用法：
#   bash scripts/eval.sh [dataset_id]     # 默认 dataset_id=default
#
# 可用环境变量覆盖默认值：
#   BASE_URL      后端地址（默认 http://localhost:8080）
#   EVAL_EMAIL    登录账号（默认 admin@example.com）
#   EVAL_PASSWORD 登录密码（默认 pass123456）
#
# 前置：后端已在本机运行（见 scripts/dev.sh app 或 make dev-app），
#       且 .env 已配置好模型/embedding 的 API Key。
set -euo pipefail

BASE_URL="${BASE_URL:-http://localhost:8080}"
EVAL_EMAIL="${EVAL_EMAIL:-admin@example.com}"
EVAL_PASSWORD="${EVAL_PASSWORD:-pass123456}"
DATASET_ID="${1:-default}"

log()  { printf '\033[1;34m[评测]\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m[评测]\033[0m %s\n' "$*" >&2; }

# 0. 后端健康检查
if ! curl -sf "$BASE_URL/health" >/dev/null 2>&1; then
    warn "后端未运行：$BASE_URL/health 不可达"
    warn "请先启动后端（scripts/dev.sh app 或 make dev-app）"
    exit 1
fi

# 1. 登录
log "登录 $EVAL_EMAIL ..."
TOKEN=$(curl -s -X POST "$BASE_URL/api/v1/auth/login" \
    -H 'Content-Type: application/json' \
    -d "{\"email\":\"$EVAL_EMAIL\",\"password\":\"$EVAL_PASSWORD\"}" \
    | node -e 'let s="";process.stdin.on("data",d=>s+=d);process.stdin.on("end",()=>{try{console.log(JSON.parse(s).token)}catch(e){console.log("")}})')
if [ -z "$TOKEN" ]; then
    warn "登录失败，请检查账号密码"
    exit 1
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
    exit 1
fi
log "任务 ID：$TASK_ID"

# 3. 轮询直到完成（0=Pending 1=Running 2=Success 3=Failed）
# 用 node 容错提取 status/finished，避免 grep 在响应暂缺字段时非零退出触发 set -e
while :; do
    R=$(curl -s "$BASE_URL/api/v1/evaluation?task_id=$TASK_ID" -H "Authorization: Bearer $TOKEN")
    read -r S F <<< "$(printf '%s' "$R" | node -e '
let s="";process.stdin.on("data",d=>s+=d);process.stdin.on("end",()=>{
  try{const t=JSON.parse(s).data.task||{};console.log((t.status??"")+" "+(t.finished??""))}
  catch(e){console.log(" ")}
})')"
    log "进度 finished=$F status=$S"
    { [ "$S" = "2" ] || [ "$S" = "3" ]; } && break
    sleep 5
done

# 4. 输出四类结果报告
export REPORT_TASK_ID="$TASK_ID"
printf '%s' "$R" | node -e '
const chunks = [];
process.stdin.on("data", d => chunks.push(d));
process.stdin.on("end", () => {
    const d = JSON.parse(Buffer.concat(chunks).toString()).data;
    const t = d.task || {};
    const m = (d.metric || {});
    const c = (d.cost || {});
    const r = m.retrieval_metrics || {};
    const g = m.generation_metrics || {};
    const statusNames = ["Pending", "Running", "Success", "Failed"];
    const pad = (s) => String(s).padEnd(18);
    console.log("\n================= 评测报告 =================");
    console.log("任务:", t.id, " 状态:", statusNames[t.status] ?? t.status);
    console.log("规模:", `${t.finished}/${t.total}`, " 数据集:", t.dataset_id, " embedding_top_k:", d.params && d.params.embedding_top_k);
    console.log("\n[1] 检索准确性");
    for (const k of ["precision", "recall", "ndcg3", "ndcg10", "mrr", "map"])
        console.log("  " + pad(k) + ((r[k] ?? 0).toFixed(4)));
    console.log("\n[2] 答案质量");
    for (const k of ["bleu1", "bleu2", "bleu4", "rouge1", "rouge2", "rougel"])
        console.log("  " + pad(k) + ((g[k] ?? 0).toFixed(4)));
    console.log("\n[3] 成本");
    console.log("  " + pad("prompt_tokens") + (c.prompt_tokens ?? 0));
    console.log("  " + pad("completion_tokens") + (c.completion_tokens ?? 0));
    console.log("  " + pad("total_tokens") + (c.total_tokens ?? 0));
    console.log("\n[4] 耗时");
    console.log("  " + pad("latency_ms") + (c.latency_ms ?? 0));
    console.log("=============================================");
});
'
