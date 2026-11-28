============================================================
  Analy2API v1.2.5 — Windows (amd64) 发行包快速指南
============================================================

1. 零配置一键启动
   - 直接双击或终端运行 start.bat（或 analy2api.exe）。
   - 默认监听：http://127.0.0.1:8080（支持外部访问 0.0.0.0:8080）。
   - 默认数据库：内置零依赖 SQLite（.\data\analy2api.db），自动启用 WAL 模式。
   - 默认管理员：admin@analy2api.local / admin123456（首次登录后请在右上角修改）。

2. 自定义配置
   - 复制 .env.example 为 .env 并修改端口、代理、管理员密码或 PostgreSQL 配置。
   - 如需从旧版 cmdwb2api 平滑迁移：将旧 cmdwb2api.db 放入 .\data\ 目录即可自动识别。

3. 常用命令
   - analy2api.exe --version       查看版本与构建信息
   - analy2api.exe                 启动完整网关与 Web 控制台
   - analy2api.exe agy-bridge      单实例真实 agy CLI 桥接服务（默认 :8317）

4. 下游客户端接入（兼容 OpenAI / Anthropic / Responses 三协议）
   - OpenAI Chat:    http://127.0.0.1:8080/v1/chat/completions
   - Anthropic:      http://127.0.0.1:8080/v1/messages
   - Codex Responses:http://127.0.0.1:8080/v1/responses

5. AntiGravity (AGY) 默认策略（可通过 .env 调节）
   - AGY_SESSION_RETENTION=stateless              每轮请求结束自动清理磁盘会话，零磁盘占用
   - AGY_PROMPT_MODE=default_intercept_continue   默认提示词 -> 双向拦截 -> 下游接续
