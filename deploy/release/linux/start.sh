#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")"

chmod +x ./analy2api || true
mkdir -p ./data

if [[ -f ./.env ]]; then
  set -a
  # shellcheck disable=SC1091
  source ./.env
  set +a
fi

export STATIC_DIR="${STATIC_DIR:-./web}"
export DATA_DIR="${DATA_DIR:-./data}"
export GIN_MODE="${GIN_MODE:-release}"
export AGY_SESSION_RETENTION="${AGY_SESSION_RETENTION:-stateless}"
export AGY_PROMPT_MODE="${AGY_PROMPT_MODE:-default_intercept_continue}"

if [[ "${1:-}" == "-d" || "${1:-}" == "--daemon" ]]; then
  nohup ./analy2api > ./data/analy2api.log 2>&1 &
  echo $! > ./data/analy2api.pid
  echo "Analy2API 已在后台启动 (PID: $!), 日志: ./data/analy2api.log"
  exit 0
fi

exec ./analy2api
