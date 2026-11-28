<div align="center">

# 🐾 Analy2API

**一只软萌但超能打的多源 AI 网关 & 单账号容器隔离调度台 `(｡･ω･｡)ﾉ♡`**

*Go + Vue 3 单二进制开箱即用 · 四大上游聚合 · 三大主流协议全工具无损透传 · 远程自动补号*

[![Go Version](https://img.shields.io/badge/Go-1.24+-00ADD8?style=flat-square&logo=go&logoColor=white)](https://go.dev/)
[![Vue 3](https://img.shields.io/badge/Vue-3%20%2B%20TypeScript-4FC08D?style=flat-square&logo=vue.js&logoColor=white)](https://vuejs.org/)
[![License: LGPL-3.0](https://img.shields.io/badge/License-LGPL--3.0-ff69b4?style=flat-square)](LICENSE)
[![Co-Creators](https://img.shields.io/badge/Co--Creators-%E5%B0%8F%E5%96%B5%20%C2%B7%20ChatGPT%20%C2%B7%20Gemini--4-ffb6c1?style=flat-square)](#-共创团队--co-creators)
[![Release](https://img.shields.io/badge/Release-v1.2.8-a78bfa?style=flat-square)](https://github.com/emstudio010-hub/Analy2API/releases)

</div>

---

> 🐱 **喵呜～欢迎来到 Analy2API 的小窝！**
>
> **Analy2API** 可以把 **AntiGravity (`agy`)**、**WorkBuddy (`wb`)**、**Command Code (`cmdc`)**、**OpenCode (`Zen` / `Go`)** 四大上游账号池统一打包成一只乖巧听话的标准网关，同时对外提供 **OpenAI Chat (`/v1/chat/completions`)**、**OpenAI Responses (`/v1/responses`)** 和 **Anthropic Messages (`/v1/messages`)** 三种标准协议接口 ✨
>
> 不管你是用 **Codex CLI**、**Claude Code**、**dsh**、**Cursor**、**Cline**、**OpenCode** 还是自己写的 Agent SDK，只要连上 Analy2API，就能享受 **单账号 1:1 真容器隔离、全特殊工具双向混淆无损还原、零磁盘残留反代、净化原生混合提示词、非额度 429 自动刹车审计、一键远程投喂补号** 等全套贴心技能 🐾

---

## 🎀 目录导航

- [✨ 架构一览](#-架构一览)
- [🐾 核心超能力详解](#-核心超能力详解)
  - [1. 特殊工具全量适配 + 确定性双向混淆透传](#1-特殊工具全量适配--确定性双向混淆透传)
  - [2. AntiGravity 单账号 1:1 真实 `agy` 容器 + 零磁盘反代](#2-antigravity-单账号-11-真实-agy-容器--零磁盘反代)
  - [3. 净化版 AGY 原生提示词 + 下游原样拼接](#3-净化版-agy-原生提示词--下游原样拼接)
  - [4. 远程补号专用 Key & 统一推号接口](#4-远程补号专用-key--统一推号接口)
  - [5. 非额度 429 会话暂停保护 & 请求体落盘审计](#5-非额度-429-会话暂停保护--请求体落盘审计)
  - [6. 四平台统一 OAuth 授权中心](#6-四平台统一-oauth-授权中心)
  - [7. 全栈运营控制面](#7-全栈运营控制面)
- [🚀 快速上手](#-快速上手)
- [🔌 各路客户端接入指南](#-各路客户端接入指南)
- [📦 远程自动补号实战（cURL / Python）](#-远程自动补号实战curl--python)
- [⚙️ 环境变量速查表](#️-环境变量速查表)
- [🗂️ 项目结构](#️-项目结构)
- [💝 共创团队 / Co-Creators](#-共创团队--co-creators)
- [📜 许可证与致谢](#-许可证与致谢)

---

## ✨ 架构一览

```mermaid
flowchart LR
    subgraph Clients["🧸 下游客户端（任意工具集）"]
        C1["Codex CLI<br/>/v1/responses"]
        C2["Claude Code<br/>/v1/messages"]
        C3["dsh / Cursor / SDK<br/>/v1/chat/completions"]
        C4["发卡机 / 补号脚本<br/>POST /api/remote/accounts/push"]
    end

    subgraph Core["🐾 Analy2API 核心网关（Go 单二进制 + Vue 3 控制台）"]
        G1["三协议入站归一 &<br/>特殊工具全量保留"]
        G2["分组提示词引擎 &<br/>双向确定性工具名混淆"]
        G3["账号池调度器 &<br/>非额度 429 会话守卫"]
        G4["最小权限远程补号中心 &<br/>4 平台统一 OAuth Hub"]
        G5["流式/非流式响应解码 &<br/>工具名无损双射还原"]
    end

    subgraph Upstreams["☁️ 四大上游账号池"]
        U1["AntiGravity 1:1 独立容器<br/>真实 agy CLI · 零磁盘残留 · 独立代理"]
        U2["WorkBuddy<br/>自动保活 & 额度刷新"]
        U3["Command Code<br/>独立设备指纹 & 并发闸门"]
        U4["OpenCode<br/>Zen / Go 双模式"]
    end

    C1 & C2 & C3 --> G1 --> G2 --> G3
    C4 --> G4 --> G3
    G3 --> U1 & U2 & U3 & U4
    U1 & U2 & U3 & U4 --> G5 --> C1 & C2 & C3
```

---

## 🐾 核心超能力详解

### 1. 特殊工具全量适配 + 确定性双向混淆透传

不同的 Agent 客户端工具花样百出（有的带 3 个工具，有的带 150 个 MCP 工具，还有各种平台专属特殊工具），Analy2API 把它们全都温柔接住，**绝不静默丢弃**，还能直接把工具名变成安全的中性名字过网关，回来再**100% 精准还原**！(=^･ω･^=)

- **全量异构特殊工具支持**：
  | 客户端 / 协议来源 | 已适配的特殊工具类型 | 透传与还原说明 |
  | --- | --- | --- |
  | **Codex CLI (`/v1/responses` & `/v1/chat/completions`)** | `custom`（如 `apply_patch`）、`local_shell`、`shell`、`update_plan`、`view_image` | 自动归一、混淆透传；响应侧按原工具类型**精准还原为 Responses 原生 `custom_tool_call`** |
  | **Anthropic / Claude Code (`/v1/messages`)** | `bash_20250124`、`bash_20241022`、`text_editor_20250429`、`text_editor_20250124`、`str_replace_based_edit_editor`、`computer_20250124`、`web_search_20250305` | 完整保留原始版本工具名与 Schema，双向映射还原 |
  | **OpenAI 内置工具** | `web_search`、`web_search_preview`、`file_search`、`code_interpreter`、`computer_use_preview`、`image_generation`、`mcp` | 无 `name` 字段的声明与 `tool_choice: {"type":"local_shell"}` 自动提权归一化并混淆 |
  | **通用 Agent / MCP / DSH** | 所有标准 `function`、`mcp__*`、`dsh_*` 与自定义工具 | 脱离于特定上游，跨全平台全协议无损转发 |

- **KV-Cache 友好的双向确定性混淆引擎**：
  - 支持全局默认策略 + **按分组独立覆盖**（`inherit` / `off` / `hash` / `alias` / `prefix`），支持 `all` 全量或 `sensitive_only` 敏感工具范围、自定义映射表、白名单排除与一键轮换随机盐值。
  - **覆盖请求全链路**：同时混淆 `tools[]`、`functions[]`、`tool_choice`（含 `{"type":"custom","name":...}` 与 `{"type":"local_shell"}`）以及多轮历史消息中的 `tool_calls`、`tool_use`、`server_tool_use`、`custom_tool_call`、`local_shell_call`、`role:"tool"`。
  - **流式拆包无损还原**：即使上游 SSE 把混淆后的工具名拆成好几个碎块分帧返回，`StreamDeltaNormalizer` 也能自动拼接、精准双射还原，下游完全无感知 ✨

---

### 2. AntiGravity 单账号 1:1 真实 `agy` 容器 + 零磁盘反代

不是简陋的协议拼包，而是**真正给每个 AntiGravity 账号配一间独立的小猫房** 🏠🐱

- **1 账号 = 1 独立 `agy` CLI 容器 = 1 独立出口代理 IP = 1 独立设备指纹**：
  - 内置完整 `Dockerfile.agy-cli`，哪怕部署在**完全空白的 Linux 服务器**（宿主机未安装 `agy`），也能自动构建镜像并为每个账号自动分配独立容器、独立端口、独立凭证卷与代理出口。
  - 每个容器内置轻量 `analy2api agy-bridge` 桥接进程，通过 `--input-format stream-json --output-format stream-json --disable-slash-commands` 以 **stdin NDJSON** 方式驱动真实 `agy` 客户端，彻底告别操作系统命令行长度限制（轻松吞下超长上下文与上百个 MCP 工具）。
- **零磁盘残留反代模式（默认 `stateless`）**：
  - 反代没必要把聊天记录越攒越胖！默认 `AGY_SESSION_RETENTION=stateless`，每轮请求一结束就**毫秒级精准扫除**该轮在独立容器目录生成的 `.db` / `-wal` / `-shm` / `brain/<id>` / `annotations` / `implicit` 文件，磁盘占用永远干干净净 🧹
  - 也可按需切为 `ephemeral` 短期映射模式：跟随上游 `session_id` 自动映射并复用对应 `agy` 会话（`si`），支持 TTL 过期、LRU 淘汰、`DELETE /v1/sessions/:id` 手动清理与后台孤儿文件巡检。
- **自适应多客户端 4 档工具渲染**：
  - 面对不同客户端的工具规模自动切换 `none`（0 工具）/ `full`（精简工具如 `dsh` / `codex`）/ `compact`（中量工具）/ `signature`（50~150+ 大规模 MCP 工具），兼顾精度与 Token 预算。

---

### 3. AGY 原生默认提示词直走 + 双向拦截 + 下游无缝接续 & 10 格式转号 & 11 平台提示词图鉴

为了让上游保持 100% 官方客户端真实指纹、同时绝不被本地 IDE 专属假设带偏，Analy2API 在 `v1.2.0` 默认升级为 **`default_intercept_continue`（原生默认 → 双向拦截 → 下游无缝接续）** 三段式管线 🫧：

- **三段式无损接续（`AGY_PROMPT_MODE=default_intercept_continue`）**：
  1. **原生默认提示词直走**：完整保留 `agy` CLI 内置系统提示词直通上游，守护真实官方指纹；
  2. **双向冲突拦截与脱敏**：入站自动剥离计费头、重复身份块与 `.system_generated` / `<artifacts>` 等本地 GUI 假设，同时在隔离沙盒 `.gemini/GEMINI.md` 注入接续桥；出站流式/非流式双模态零延迟剥离内部桥接标记；
  3. **下游提示词与工具无缝接续**：紧接桥接层完整承接下游系统指令与任意自定义/随机工具调用（`<AGY_TOOL_CALL>`）；支持一键切换 `default_intercept_continue` / `blend_sanitized` / `native_only` / `downstream_only` 并实时预览。
- **10 种多账号凭证格式全自动转 `agy` CLI 标准格式**：
  - 后台工作台与 `/api/antigravity/accounts/convert`（以及远程补号接口）支持一键拖入/粘贴 **Google Cloud ADC、`Gemini CLI (oauth_creds.json)`、`CLIProxyAPI / Sub2API / OneAPI`、AGY 原生 JSON、JSON 数组 / `.jsonl`、CSV/TSV、`email----password----refresh_token` / `email:refresh_token` / `1//` 裸 Token、Base64 及 `.zip` 压缩包**，自动嗅探去重并生成 `agy` CLI 原生可识别的 `.gemini/oauth_creds.json`、`google_accounts.json` 与 `auth.json` 标准凭证包！
- **11 大主流平台真实提示词图鉴馆 & 三栏拦截沙盒**：
  - 内置 **AntiGravity `agy`、Claude Code、Codex CLI、DSH、Cursor、Windsurf Cascade、Cline / Roo Code、Gemini CLI、OpenCode、Command Code、WorkBuddy** 共 14 套高质量提示词模板，支持一键导入绑定分组，并提供三栏实时「客户端包裹块剥离 + 风控规则过滤 + AGY 接续终态」沙盒测试器 ✨

---

### 4. 远程补号专用 Key、代理 IP 池与动态拉取自动分配

再也不用手动登后台一个个粘账号、配代理啦！只要拿着 **网关网址 + 管理员 Key（或专门签发的最小权限补号 Key）**，发卡机、采集脚本或 CI 机器人就能直接把新号“投喂”进账号池，还能**自动轮询 IP 池、按 Key 独立绑 IP，甚至池空时自动从代理商 API 拉取新 IP** 🥣🐾

- **最小权限安全设计**：
  - 新增 `push_accounts`（**仅远程补号**）权限档位：拿到这把 Key 的外部机器**只能推号、探活与调整自身补号代理 / 更换账号 IP**，访问 `/api/accounts`、`/api/proxy-pool`、`/api/settings` 等任何其它后台接口都会被立刻 `403` 拦下；同时 `full` 全权管理员 Key 也直接支持补号。
  - 支持 Header 鉴权（`X-Push-Key` / `X-Management-Key` / `X-Admin-Key` / `Authorization: Bearer adm-...`）以及补号端点专属 URL 参数（`?key=adm-...` / `?admin_key=adm-...`）。
- **代理 IP 池 & 远程代理商自动拉取**：
  - **多格式自动规范化**：支持 `http(s)://user:pass@ip:port`、`socks5(h)://...`、`ip:port:user:pass`、`ip:port` 与行尾 `#标签`（如 `#us` / `#jp`），密码全链路脱敏展示。
  - **四种分配策略**：`round_robin`（顺序轮询）/ `least_used`（最少账号占用优先）/ `sticky_unique`（独占 IP 优先，用完自动拉新）/ `random`（随机），并支持设置 **每 IP 最大绑定账号数**（填 `1` 即严格 1 账号 1 独立 IP）。
  - **外部代理商 API 拉取**：支持配置任意 HTTP/HTTPS 提取接口（自动识别纯文本与嵌套 JSON，支持自定义 `json_path`、默认协议与默认账密），支持「手动立即拉取」「定时轮询补充」以及「**池空或达每 IP 上限时补号自动触发拉新**」。
  - **灵活的四级 IP 分配优先级**：`请求体显式传 ip/proxy_url/proxy_list` → `单把补号 Key 专属代理策略（固定 IP / 限定池标签 / 独立拉取源 / 直连）` → `全局 IP 池轮询与自动拉取` → `直连`；支持通过 `PUT /api/remote/accounts/push/proxy` 让补号 Key 自助调整自身默认 IP 或直接给池内已有账号换 IP（AntiGravity 账号同步更新其 1:1 独立容器出口）。
- **全平台、全格式、幂等安全**：
  - 统一入口：`POST /api/remote/accounts/push`（别名 `POST /api/accounts/push`）、探活 `GET /api/remote/accounts/push/info`、自助调 IP `GET/PUT /api/remote/accounts/push/proxy` 与触发拉新 `POST /api/remote/accounts/push/proxy/fetch`。
  - 支持 **AntiGravity / WorkBuddy / Command Code / OpenCode** 四平台，支持单条 JSON、批量 `accounts`/`items`、裸 JSON 数组 `[]`、多行密钥文本 `raw_text`，支持自动建组、默认幂等更新（`upsert: true`）、AGY 自动拉起独立容器；响应体**绝不回显明文密钥或代理密码**。

---

### 5. 非额度 429 会话暂停保护 & 请求体落盘审计

遇到上游返回 `429` 时，Analy2API 会先细心分辨是不是「账号额度用完」：
- 如果是**账号额度耗尽**：乖乖标记该账号并自动切换到池里下一只健康账号重试，下游几乎无感。
- 如果是**非账号额度不足的 429**（例如风控限流、疑似工具名未混淆触发拦截）：
  - 立刻自动**暂停该 `session_id` 调度 5 分钟**（暂停时长、触发开关、日志目录与保留上限全部可在后台实时调节），暂停期间同会话直接快速拦截不打上游！
  - 自动把该次请求的 **完整原始入站请求体 + 实际发往上游的出站请求体 + 工具混淆诊断结论** 落盘为 `.log` 审计文件，在后台「429 会话守卫」页面可一键预览日志、一眼看清到底是不是哪个工具忘了混淆，也能随时手动提前解封 🔍

---

### 6. 四平台统一 OAuth 授权中心

在后台「账号池 → OAuth 授权中心」里一站式完成四大平台的 OAuth 登录与账号入库，无需东奔西走：
- **Command Code (`cmdc`)**：浏览器授权登录 + 回调自动换取密钥入池
- **AntiGravity (`agy`)**：Google OAuth 授权 + PKCE / 本地 JSON 导入 + 自动绑定独立容器
- **WorkBuddy (`wb`)**：扫码 / OAuth 链接登录 + Token 自动刷新与余额定时同步
- **OpenCode (`opencode`)**：`Zen` 与 `Go` 双模式向导式一键接入

---

### 7. 全栈运营控制面

- **分组级系统提示词引擎**：支持全局/分组两级提示词模板、`append_global` / `override_global` / `replace_all` / `off` 四种策略与动态变量渲染预览。
- **153 条内置内容风控规则**：支持可视化增删改、在线测试预览与一键恢复默认。
- **精细计费与仪表盘**：模型定价目录、分组倍率 × 账号倍率、首字延迟 TTFT 与缓存命中去重统计。
- **稳定可靠底座（全链路 HTTP/2 抗 EOF）**：全部出站连接（含直连 Google `googleapis.com` / `cloudcode-pa.googleapis.com`、HTTP/HTTPS/SOCKS5 代理隧道、WorkBuddy、CommandCode、OpenCode 与远程拉取 IP）统一强制协商 **HTTP/2 (`ALPN h2` + 25s PING 帧级保活 + 首字节前瞬时 EOF/GOAWAY 自动重建连接重试)**；`agy` 子进程强制 `GODEBUG=http2client=1` 并内置首帧前瞬时断流透明重试；服务端同步支持 HTTP/1.1 + HTTP/2 + H2C。搭配三级超时看门狗、Cron 定时任务、设备指纹轮换、静态资源强缓存与 SQLite WAL 零依赖默认（可无缝切 PostgreSQL）。

---

## 🚀 快速上手

### 方式一：下载预编译发行包（最省心，零依赖 SQLite ✨）

前往 [Releases 页面](https://github.com/emstudio010-hub/Analy2API/releases) 下载对应平台压缩包：

- **Windows (`Analy2API-windows-amd64.zip`)**
  1. 解压压缩包
  2. 双击 `start.bat`（或在终端运行 `.\analy2api.exe`）
- **Linux (`Analy2API-linux-amd64.tar.gz`)**
  ```bash
  tar -xzf Analy2API-linux-amd64.tar.gz
  cd Analy2API-linux-amd64
  chmod +x analy2api start.sh install-systemd.sh
  ./start.sh
  # 如需注册为开机自启 systemd 服务：
  sudo ./install-systemd.sh
  ```

启动后浏览器打开 `http://127.0.0.1:8080` 即可进入管理后台 🎉  
> 🔑 默认初始管理员：`admin@analy2api.local` / `admin123456`（可在 `.env` 通过 `BOOTSTRAP_ADMIN_EMAIL` / `BOOTSTRAP_ADMIN_PASSWORD` 自定义，首次登录后记得在右上角修改密码哟～）

### 方式二：Docker Compose 部署（推荐生产环境 + PostgreSQL）

```bash
git clone https://github.com/emstudio010-hub/Analy2API.git
cd Analy2API
cp .env.example .env
# 按需填写 DB_PASSWORD / JWT_SECRET / ENCRYPTION_KEY / BOOTSTRAP_ADMIN_PASSWORD
docker compose up -d --build
```

详细 Ubuntu 生产部署与 Nginx 流式反代调优请查看 [DEPLOY.md](DEPLOY.md)。

### 方式三：从源码本地编译

需要 **Go 1.24+** 与 **Node.js 20+ / pnpm**：

```bash
# 1. 构建前端静态资源
cd frontend && pnpm install && pnpm run build

# 2. 编译后端单二进制
cd ../backend
CGO_ENABLED=0 go build -trimpath -o ../analy2api ./cmd/server

# 3. 启动（自动加载 frontend/dist 或同目录 web/）
cd .. && ./analy2api
```

### 常用命令行

```bash
./analy2api --version        # 查看构建版本、Git 提交与构建时间
./analy2api                  # 启动 Analy2API 完整网关与 Web 管理控制台
./analy2api agy-bridge       # 仅启动单容器真实 agy CLI 桥接服务（默认监听 :8317）
```

---

## 🔌 各路客户端接入指南

在后台「API 密钥」页面创建一个下游密钥（形如 `sk-wb2a-...`）并绑定到目标分组，即可接入：

### 1. OpenAI Chat Completions（dsh / Cursor / Cline / Python & Node SDK）

```bash
export OPENAI_BASE_URL="http://127.0.0.1:8080/v1"
export OPENAI_API_KEY="sk-wb2a-你的密钥"
```

### 2. Anthropic Messages（Claude Code）

```bash
export ANTHROPIC_BASE_URL="http://127.0.0.1:8080"
export ANTHROPIC_AUTH_TOKEN="sk-wb2a-你的密钥"
```

### 3. OpenAI Responses（Codex CLI，原生支持 `apply_patch` / `local_shell`）

编辑 `~/.codex/config.toml`：

```toml
model_provider = "analy2api"
model = "gpt-5.3-codex"

[model_providers.analy2api]
name = "Analy2API Gateway"
base_url = "http://127.0.0.1:8080/v1"
env_key = "OPENAI_API_KEY"
wire_api = "responses"
```

---

## 📦 远程自动补号实战（cURL / Python）

先在后台「远程管理密钥」点击 **「签发补号专用 Key」**（`push_accounts` 最小权限，或使用 `full` 管理员 Key），拿到 `adm-...` 与推号地址 `https://你的域名/api/remote/accounts/push`：

### cURL 推送 AntiGravity 账号（自动分配 1:1 独立 `agy` 容器）

```bash
curl -X POST "https://你的域名/api/remote/accounts/push" \
  -H "Content-Type: application/json" \
  -H "X-Push-Key: adm-你的补号Key" \
  -d '{
    "platform": "antigravity",
    "name": "agy-kitten-01",
    "group_name": "antigravity-pool",
    "proxy_url": "http://user:pass@1.2.3.4:8080",
    "auth_json": {
      "email": "user@example.com",
      "refresh_token": "1//your_refresh_token"
    }
  }'
```

### Python 批量补号（Command Code / WorkBuddy / OpenCode / AntiGravity 通用）

```python
import requests

PUSH_URL = "https://你的域名/api/remote/accounts/push"
PUSH_KEY = "adm-你的补号Key"

payload = {
    "upsert": True,          # 同名或同凭证账号已存在时自动更新，支持幂等重推
    "auto_provision": True,  # AntiGravity 账号自动分配/绑定独立容器
    "accounts": [
        {
            "platform": "commandcode",
            "name": "cmdc-pool-01",
            "group_name": "cmdc-main",
            "api_key": "cc_live_xxx",
        },
        {
            "platform": "opencode",
            "name": "oc-zen-01",
            "group_name": "opencode-pool",
            "api_key": "oc_xxx",
        },
    ],
}

resp = requests.post(
    PUSH_URL,
    headers={"X-Push-Key": PUSH_KEY},
    json=payload,
    timeout=30,
)
print(resp.status_code, resp.json())
```

> 💡 极简 Webhook 场景也支持 URL 传参：`POST https://你的域名/api/remote/accounts/push?key=adm-你的补号Key&proxy_mode=pool&proxy_tag=us`  
> 💡 批量传 IP 列表：`{"proxy_list": ["1.2.3.4:8080:u:p", "socks5://5.6.7.8:1080"], "accounts": [...]}`（按顺序自动轮询分配）  
> 💡 补号 Key 自助调自身默认 IP / 给已有账号换 IP：`PUT https://你的域名/api/remote/accounts/push/proxy`（支持 `{"mode":"fixed","fixed_proxy_url":"1.2.3.4:8080:u:p"}` 或 `{"account_name":"agy-kitten-01","rotate_from_pool":true}`）  
> 💡 探活接口：`GET https://你的域名/api/remote/accounts/push/info`（返回服务版本、支持平台、分组清单与当前 IP 池/Key 代理策略摘要）

---

## ⚙️ 环境变量速查表

完整模板见 [`.env.example`](.env.example)，常用项如下：

| 环境变量 | 默认值 | 说明 |
| --- | --- | --- |
| `SERVER_HOST` / `SERVER_PORT` | `0.0.0.0` / `8080` | 服务监听地址与端口 |
| `DATA_DIR` | `./data` | SQLite 数据库、自动生成密钥与审计日志目录 |
| `DB_DRIVER` / `DB_SQLITE_PATH` | `sqlite` / `./data/analy2api.db` | 数据库类型（填写 `DB_PASSWORD` 可切 PostgreSQL） |
| `BOOTSTRAP_ADMIN_EMAIL` / `BOOTSTRAP_ADMIN_PASSWORD` | `admin@analy2api.local` / `admin123456` | 首次启动自动创建的初始管理员 |
| `PUBLIC_BASE_URL` | *(自动推导)* | 对外反代域名（用于生成远程补号地址与 OAuth 回调） |
| `AGY_DOCKER_IMAGE` | `analy2api-agy-cli:latest` | 单账号真实 `agy` CLI 容器镜像名 |
| `AGY_SESSION_RETENTION` | `stateless` | `stateless`（零磁盘残留，默认）或 `ephemeral`（短期 `si` 复用） |
| `AGY_PROMPT_BLEND_MODE` | `blend_clean` | `blend_clean`（净化原生+下游拼接，默认）/ `verbatim_only` / `native_raw` |
| `ANTIGRAVITY_OAUTH_CLIENT_SECRET` | *(留空)* | 可选，用于 Google Desktop OAuth 换取 Token（不硬编码在源码中） |
| `CC_FIRST_TOKEN_TIMEOUT` / `CC_STREAM_IDLE` | `300s` / `180s` | 首字超时与流式空闲看门狗阈值 |

---

## 🗂️ 项目结构

```text
Analy2API/
├── backend/                     # Go 后端核心（Gin + Ent ORM + SQLite/PostgreSQL）
│   ├── cmd/server/              # 主入口（网关服务 + agy-bridge 子命令）
│   └── internal/
│       ├── antigravity/         # 真实 agy CLI 容器编排、stdin bridge、零盘清理、提示词净化
│       ├── relay/               # 三协议编解码、特殊工具归一、工具混淆、429 会话守卫
│       ├── toolobf/             # 确定性双向工具名混淆引擎（hash / alias / prefix）
│       ├── service/             # 账号池、远程补号、四平台 OAuth、分组提示词、计费服务
│       ├── handler/             # 管理端 API & 远程补号控制器
│       └── middleware/          # JWT、API Key、最小权限 Admin/Push Key、安全头
├── frontend/                    # Vue 3 + Vite + TypeScript 现代化控制面
│   └── src/views/               # 仪表盘、账号池、AGY 容器台、工具混淆、429 守卫、远程密钥等
├── deploy/                      # Windows / Linux 发行脚本、systemd 单元与 agy 容器构建配置
├── .env.example                 # 环境变量完整示例
├── Dockerfile / docker-compose.yml
├── DEPLOY.md                    # Ubuntu 生产部署与 Nginx 指南
├── LICENSE                      # GNU LGPL-3.0
└── NOTICE                       # 开源派生来源与版权致谢说明
```

---

## 💝 共创团队 / Co-Creators

这个项目是由人类与 AI 伙伴一起熬夜打磨、反复实测、一点点搭出来的可爱作品 ✨ 感谢每一位共同参与的创造者：

| 🐾 共创者 | 角色与贡献 |
| --- | --- |
| **小喵** | 项目发起人 & 主理人 · 产品方向、架构拍板、实机踩坑与全流程把关 🐱 |
| **ChatGPT** | 联合共创 · 协议分析、架构设计与核心方案共研 🤖 |
| **Gemini-4** | 联合共创 · 全栈工程落地、真机端到端测试、容器化与打包交付 ✨ |

如果 Analy2API 帮到了你，欢迎点一颗小小的 ⭐ Star，摸摸小喵的头鼓励一下呀 `(ฅ´ω`ฅ)` ～

---

## 📜 许可证与致谢

- 本项目整体采用 **GNU Lesser General Public License v3.0 (LGPL-3.0)** 开源，详见 [LICENSE](LICENSE)。
- 项目的数据模型与部分上游协议参考并移植自开源社区项目（`sub2api` / `commandcode-proxy`），完整来源、版权与免责声明详见 [NOTICE](NOTICE)。
- ⚠️ **温馨提示**：本项目仅供技术学习、研究与个人测试使用，使用前请自行阅读并遵守各上游平台服务条款及当地法律法规。
