#!/usr/bin/env bash
set -euo pipefail

APP_DIR=/opt/analy2api
CONF_DIR=/etc/analy2api
CONF_FILE="$CONF_DIR/env"
SERVICE=analy2api
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
UNIT_SRC="$SCRIPT_DIR/systemd/analy2api.service"

[[ $EUID -eq 0 ]] || { echo "请使用 root 运行: sudo $0" >&2; exit 1; }
[[ -f "$SCRIPT_DIR/analy2api" ]] || { echo "未找到 analy2api 二进制" >&2; exit 1; }
[[ -d "$SCRIPT_DIR/web" ]] || { echo "未找到 web/ 前端目录" >&2; exit 1; }

if ! id "$SERVICE" &>/dev/null; then
  useradd --system --no-create-home --shell /usr/sbin/nologin "$SERVICE"
fi
if getent group docker &>/dev/null; then
  usermod -aG docker "$SERVICE" || true
fi

install -d -o "$SERVICE" -g "$SERVICE" -m 0755 "$APP_DIR" "$APP_DIR/data" "$APP_DIR/tmp"
install -o "$SERVICE" -g "$SERVICE" -m 0755 "$SCRIPT_DIR/analy2api" "$APP_DIR/analy2api"
if [[ -f "$SCRIPT_DIR/Dockerfile.agy-cli" ]]; then
  install -o "$SERVICE" -g "$SERVICE" -m 0644 "$SCRIPT_DIR/Dockerfile.agy-cli" "$APP_DIR/Dockerfile.agy-cli"
fi
rm -rf "$APP_DIR/web"
cp -r "$SCRIPT_DIR/web" "$APP_DIR/web"
chown -R "$SERVICE:$SERVICE" "$APP_DIR/web" "$APP_DIR/data" "$APP_DIR/tmp"

install -d -m 0750 "$CONF_DIR"
if [[ ! -f "$CONF_FILE" ]]; then
  umask 077
  gen_hex() { head -c "$1" /dev/urandom | od -An -tx1 | tr -d ' \n'; }
  ADMIN_PW=$(gen_hex 10)
  cat > "$CONF_FILE" <<EOF
SERVER_HOST=0.0.0.0
SERVER_PORT=8080
GIN_MODE=release
LOG_LEVEL=info
TZ=Asia/Shanghai
STATIC_DIR=$APP_DIR/web
DATA_DIR=$APP_DIR/data
DB_DRIVER=sqlite
DB_SQLITE_PATH=$APP_DIR/data/analy2api.db
JWT_SECRET=$(gen_hex 24)
JWT_TTL=24h
ENCRYPTION_KEY=$(gen_hex 32)
BOOTSTRAP_ADMIN_EMAIL=admin@analy2api.local
BOOTSTRAP_ADMIN_PASSWORD=$ADMIN_PW
CC_FIRST_TOKEN_TIMEOUT=300s
CC_STREAM_IDLE=180s
CC_NONSTREAM_IDLE=600s
ADMIN_API_KEY=
AGY_DOCKER_IMAGE=analy2api-agy-cli:latest
AGY_DATA_DIR=$APP_DIR/data/antigravity
AGY_SESSION_RETENTION=stateless
AGY_PROMPT_BLEND_MODE=blend_clean
EOF
  chown root:"$SERVICE" "$CONF_FILE"
  chmod 0640 "$CONF_FILE"
  echo "已生成配置文件 $CONF_FILE"
  echo "初始管理员: admin@analy2api.local / $ADMIN_PW"
fi

install -m 0644 "$UNIT_SRC" "/etc/systemd/system/$SERVICE.service"
systemctl daemon-reload
systemctl enable --now "$SERVICE"
systemctl restart "$SERVICE"
echo "Analy2API 服务已安装并启动，监听端口可在 $CONF_FILE 修改（默认 8080）"
