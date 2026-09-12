#!/usr/bin/env bash
# 本地启动已编译的后端 server.exe，连本机 dev 基础设施（postgres/redis/docreader/langfuse）。
#
# 用途：验收 A 的「真实调用链」缓存实验 —— 用真实后端 + 真实 embedding provider
# （SiliconFlow bge-m3）跑评测，观察 embedding_cache 表在冷/热/跨重启下的行数变化。
#
# 与 scripts/dev.sh start_app 的区别：dev.sh 用 `go run` 现场编译，本脚本直接跑
# `bin/server.exe`（省去重复编译），便于反复「杀进程 → 重启」做跨重启实验。
#
# 前置：bin/server.exe 已编译；dev 容器已在跑（docker-compose.dev.yml，端口已映射到宿主机）。
set -euo pipefail
cd "$(dirname "$0")/.."   # 仓库根目录（dataset/samples 的相对路径依赖 cwd）

# 1. 加载 .env（容错 CRLF 换行）
set -a
# shellcheck disable=SC1091
source <(sed 's/\r$//' .env)
set +a

# 2. 本地模式覆盖：把 .env 里的容器服务名映射到宿主机回环地址
export DB_HOST=127.0.0.1
export DOCREADER_ADDR=127.0.0.1:50051
export DOCREADER_TRANSPORT=grpc
export MINIO_ENDPOINT=127.0.0.1:9000
export REDIS_ADDR=127.0.0.1:6379
export MILVUS_ADDRESS=127.0.0.1:19530
export NEO4J_URI=bolt://127.0.0.1:7687
export QDRANT_HOST=127.0.0.1
# Langfuse dev 容器在宿主机 3000 端口（.env 里是容器名 langfuse-web:3000）
export LANGFUSE_HOST="${LANGFUSE_HOST:-http://127.0.0.1:3000}"
# 宿主机直跑后端不能用容器内只读路径 /data/files，落盘到仓库本地目录
export LOCAL_STORAGE_BASE_DIR="$(pwd)/.local-data/files"
mkdir -p "$LOCAL_STORAGE_BASE_DIR"

# 3. 跑编译好的后端（前台，由调用方决定后台化/重定向日志）
echo "[start-local-server] 启动后端，DB=$DB_HOST:${DB_PORT:-5432} REDIS=$REDIS_ADDR"
echo "[start-local-server] EMBEDDING=$EMBEDDING_MODEL_NAME ($EMBEDDING_BASE_URL)"
exec ./bin/server.exe
