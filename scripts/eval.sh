#!/usr/bin/env bash
# 统一评测入口；账号通过 EVAL_EMAIL/EVAL_PASSWORD 提供。
set -euo pipefail
SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
exec node "$SCRIPT_DIR/eval-runner.mjs" "${1:-default}"
