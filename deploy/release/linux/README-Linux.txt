============================================================
  Analy2API v1.0.0 — Linux (amd64) 发行包快速指南
============================================================

1. 零配置一键启动（单二进制 + 内置 SQLite WAL）
   tar -xzf Analy2API-linux-amd64.tar.gz
   cd Analy2API-linux-amd64
   chmod +x ./analy2api ./start.sh ./install-systemd.sh
   ./start.sh              # 前台启动
   ./start.sh -d           # 后台守护运行（日志写入 ./data/analy2api.log）

2. 生产 systemd 安装（推荐）
   sudo ./install-systemd.sh
   journalctl -u analy2api -f

3. 空服务器单账号独立 AntiGravity (agy) 容器
   - 只要宿主机已安装 Docker，录入 AntiGravity 账号时 Analy2API 会利用本二进制与内嵌 Dockerfile
     自动构建 analy2api-agy-cli:latest 镜像，按 1账号:1容器:1IP:1指纹 独立分配，无需宿主机装 agy。
   - 默认策略：
     * AGY_SESSION_RETENTION=stateless   每轮请求结束自动清理 .db / brain 目录，零磁盘膨胀
     * AGY_PROMPT_BLEND_MODE=blend_clean 净化版 AGY 原生提示词 + 后接下游系统提示词

4. 常用命令
   ./analy2api --version     查看版本与构建信息
   ./analy2api               启动完整网关与 Web 控制台（默认 :8080）
   ./analy2api agy-bridge    启动单容器 agy CLI 桥接服务（默认 :8317）

5. 默认管理员
   - 首次直接启动：admin@analy2api.local / admin123456
   - 使用 install-systemd.sh：自动生成随机密码并打印在终端（配置位于 /etc/analy2api/env）
