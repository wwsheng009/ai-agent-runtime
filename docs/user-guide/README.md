# ai-agent-runtime 操作手册

> 面向运维和开发者的本地 AI Agent 运行时操作指南，涵盖安装、配置、启动、构建与故障排查。

---

## 目录

1. [产品概述](#1-产品概述)
2. [安装与部署](#2-安装与部署)
3. [配置](#3-配置)
4. [启动服务](#4-启动服务)
5. [日常操作](#5-日常操作)
6. [构建](#6-构建)
7. [故障排查](#7-故障排查)
8. [升级与卸载](#8-升级与卸载)

> 📖 **各 cmd 程序使用手册索引**见下方 [0. 程序使用手册](#0-程序使用手册)。

---

## 0. 程序使用手册

`backend/cmd/` 下各程序的使用手册（命令、参数、示例）：

| 程序 | 手册 | 说明 |
|------|------|------|
| `aicli` | [aicli.md](aicli.md) | AI CLI：交互式 Chat / Headless Exec / 配置管理 |
| `aicli-console` | [aicli-console.md](aicli-console.md) | aicli 原生 Windows 控制台启动器（仅 Windows） |
| `aicli-render-fixture` | [aicli-render-fixture.md](aicli-render-fixture.md) | 终端渲染 E2E 测试 fixture（仅 Windows） |
| `conpty-probe` | [conpty-probe.md](conpty-probe.md) | ConPTY 可用性探测（仅 Windows） |
| `echo-mcp-server` | [echo-mcp-server.md](echo-mcp-server.md) | MCP WebSocket 演示服务器（echo/add 工具） |
| `runtime-server` | [runtime-server.md](runtime-server.md) | HTTP API 服务 + Web 控制台后端 |
| `sftp-client` | [sftp-client.md](sftp-client.md) | SFTP 客户端（交互/批处理/直传） |
| `skillsapi-demo` | [skillsapi-demo.md](skillsapi-demo.md) | Skills API 演示客户端 |
| `ssh-client` | [ssh-client.md](ssh-client.md) | SSH 客户端（shell/命令/端口转发） |
| `ssh-keygen` | [ssh-keygen.md](ssh-keygen.md) | SSH 密钥生成与证书签发 |
| `toolkit-mcp-server` | [toolkit-mcp-server.md](toolkit-mcp-server.md) | MCP 服务器（暴露 toolkit 工具） |

> SSH/SFTP 客户端完整手册（认证、config、FAQ）另见 [docs/tools/ssh-sftp-clients-usage.md](../tools/ssh-sftp-clients-usage.md)；aicli 详细文档见 [docs/aicli/](../aicli/README.md)。

---

## 1. 产品概述

`ai-agent-runtime` 提供一套**本地优先**的 AI Agent 运行时，包含三个主要组件：

| 组件 | 说明 | 默认入口 |
|------|------|----------|
| **aicli** | CLI 聊天/执行工具，核心用户界面 | `backend/cmd/aicli` |
| **aicli-console** | 原生 Windows 控制台启动器 | `backend/cmd/aicli-console` |
| **runtime-server** | HTTP API 服务 + Web 控制台后端 | `backend/cmd/runtime-server` |
| **前端控制台** | Web 管理界面（React + Vite） | `frontend/` |
| **SSH/SFTP 工具** | SSH 客户端、SFTP 客户端、ssh-keygen | `backend/cmd/ssh-*` |

### 架构简图

```
┌─────────────┐     ┌──────────────────┐     ┌─────────────────┐
│   aicli     │     │  runtime-server  │     │  Web 控制台     │
│ chat/exec   │     │  HTTP / SSE API  │◄────│  React + Vite   │
└──────┬──────┘     └────────┬─────────┘     └─────────────────┘
       │                     │
       └──────────┬──────────┘
                  ▼
        backend/internal/*
        agent · chat · toolkit · policy
        skills · team · provider · storage
```

### 核心能力

- **交互式 Chat**：`aicli` / `aicli chat`，流式对话、slash 命令、session 恢复、模型切换
- **Headless Exec**：`aicli exec`，JSON/JSONL 输出，适合脚本与 CI
- **工具与工作区**：文件读写、搜索、shell、patch；权限审批与策略
- **Provider 接入**：OpenAI 兼容协议、Codex OAuth；`aicli login` 交互/非交互登录
- **Skills / MCP**：将 skills 暴露为可调用能力；接入 MCP server 扩展工具面
- **多 Agent**：portable agent 定义、`spawn_agent` 子会话、team 任务编排
- **HTTP + Web**：runtime-server 提供 `/api/agent/chat`、`/api/runtime/*`；前端工作台管理会话与 team

---

## 2. 安装与部署

### 2.1 安装 aicli

**Linux / macOS**

```bash
curl -fsSL https://raw.githubusercontent.com/wwsheng009/ai-agent-runtime/main/scripts/install-aicli.sh | bash
```

**Windows（PowerShell）**

```powershell
iwr -useb https://raw.githubusercontent.com/wwsheng009/ai-agent-runtime/main/scripts/install-aicli.ps1 | iex
```

安装完成后新开终端，验证：

```bash
aicli version        # 显示版本信息
Get-Command aicli    # Windows 确认 PATH
```

**源码安装**（需要 Go 1.24+）

```bash
git clone https://github.com/wwsheng009/ai-agent-runtime.git
cd ai-agent-runtime
make install-aicli
```

> 详细安装说明见 [docs/aicli/install.md](../aicli/install.md) 和 [docs/aicli/quickstart.md](../aicli/quickstart.md)。

### 2.2 部署 runtime-server

#### 从源码编译

```bash
cd backend
go build -o dist/runtime-server.exe ./cmd/runtime-server
```

#### 使用构建脚本

```powershell
# 主构建（Windows 主线）
pwsh -File ./scripts/build.ps1 -Target windows -Tools runtime-server -Version v1.0.0

# Win7 兼容构建
pwsh -File ./scripts/build.ps1 -Target win7 -Tools runtime-server -Version win7-v1.0.0
```

产物位于 `backend/dist/runtime-server.exe`（主线）或 `backend/dist/runtime-server-win7.exe`（Win7）。

#### 启动 runtime-server

```bash
# 前台启动（开发调试）
./backend/dist/runtime-server.exe serve --listen 127.0.0.1:8101 --config backend/configs/runtime.yaml

# 后台启动（生产）
./backend/dist/runtime-server.exe start --listen 127.0.0.1:8101 --config backend/configs/runtime.yaml --wait 30s
```

### 2.3 部署 Web 控制台

1. 确保 Node.js 和 pnpm 已安装：
   ```bash
   node --version
   pnpm --version
   ```

2. 安装前端依赖并构建：
   ```bash
   cd frontend
   pnpm install --frozen-lockfile
   pnpm build
   ```

3. 启动 runtime-server 时自动嵌入前端产物（`frontend/dist` 中的 `index.html` 等资源会被 stage 到 `backend/internal/webui/dist`，编译进二进制）。

> 构建时可用 `-EmbedPlaceholder` 跳过前端嵌入，仅放入占位符；可用 `-KeepEmbeddedWebUI` 保留现有嵌入资源。

---

## 3. 配置

### 3.1 配置文件体系

| 配置文件 | 用途 | 默认路径 |
|----------|------|----------|
| `config.yaml` | aicli 全局配置 | `~/.aicli/config.yaml` |
| `aicli.yaml` | aicli CLI 配置 | `~/.aicli/aicli.yaml` |
| `runtime.yaml` | runtime-server 配置 | `backend/configs/runtime.yaml` |
| `runtime.win7.yaml` | Win7 构建的 runtime-server 配置 | `backend/configs/runtime.win7.yaml` |

### 3.2 runtime.yaml 配置详解

```yaml
version: "v1"

sessions:
  backend: sqlite                          # 会话存储后端（仅支持 sqlite）
  storePath: session_history_replica.sqlite # 会话数据库路径（相对 aiclipaths 数据目录）
  replicaSource: session_history.sqlite    # 主数据库路径（aicli 写入的活跃库）
  replicaSyncInterval: 30s                 # 从主库同步副本的间隔

agent:
  maxSteps: 0                              # Agent 最大步数（0=无限制）
  maxToolCalls: 0                          # 最大工具调用次数（0=无限制）
  timeout: 0s                              # Agent 超时（0=无限制）
  enableParallelTools: true                # 启用并行工具执行
  maxParallelToolCalls: 4                  # 最大并行工具数

background:
  defaultTimeout: 0s                       # 后台任务默认超时（0=无限制）
  monitorInterval: 250ms                   # 后台任务监控轮询间隔
  heartbeatTimeout: 30s                    # 后台任务心跳超时
  launchMaxAttempts: 3                     # 后台任务启动最大重试次数
  recoveryMaxAttempts: -1                  # 后台任务恢复最大重试次数（-1=无限）

context:
  fallbackMaxPromptTokens: 32000           # 上下文窗口回退 token 上限

images:
  cacheMaxAge: 1h                          # 图片缓存有效期
  generations:
    default_model: gpt-image-2             # 默认图片生成模型
    default_size: "1024x1024"              # 默认图片尺寸
    request_timeout: 5m                    # 图片生成请求超时

observe:
  enabled: true                            # 启用运行时观测 API

sessionRuntime:
  defaultPersistence: memory               # 会话运行时持久化模式（memory 或 file）
  storePath: ../data/runtime/session_runtime.sqlite  # 运行时专用存储路径
```

### 3.3 配置搜索路径

runtime-server 按以下顺序搜索配置文件：

1. `--config` 或 `-c` 参数指定的路径
2. 环境变量（如 `RUNTIME_CONFIG_PATH`）
3. 默认搜索路径中的 `runtime.yaml` 或 `runtime-server.yaml`

### 3.4 会话存储模式

当前默认使用 **Read-Replica 模式**：

- **主数据库**（`session_history.sqlite`）：由 aicli 进程写入，持有 WAL 文件锁
- **副本数据库**（`session_history_replica.sqlite`）：runtime-server 读取，每 `replicaSyncInterval` 全量同步

此模式避免 aicli 与 runtime-server 同时写入同一数据库导致的锁冲突（STORE_TIMEOUT 503）。

> 如需切换为共享直连模式（两进程共用同一数据库），将 `replicaSource` 和 `replicaSyncInterval` 置空即可，但需注意并发写入冲突风险。

---

## 4. 启动服务

### 4.1 启动 runtime-server

```bash
# 查看帮助
runtime-server --help
runtime-server serve --help

# 前台启动（默认监听 :8101）
runtime-server serve

# 指定配置文件和监听地址
runtime-server serve -c backend/configs/runtime.yaml --listen 127.0.0.1:8101

# 启用 pprof 诊断
runtime-server serve --pprof

# 后台启动（带等待确认）
runtime-server start --listen 127.0.0.1:8101 --wait 30s

# 停止服务
runtime-server stop

# 查看状态
runtime-server status
```

### 4.2 启动 Web 控制台

runtime-server 内置嵌入式 Web 前端，启动后访问：

```
http://localhost:8101/
```

前端入口：
- 会话列表：`/workspace/sessions/`
- 运行时监控：`/workspace/runtime/`
- Team 管理：`/workspace/teams/`

### 4.3 健康检查

```bash
# HTTP 健康检查端点
curl http://localhost:8101/api/runtime/health

# 预期响应示例（JSON）
{"status":"ok","version":"v1.0.0","uptime":"1h30m"}
```

### 4.4 日志

runtime-server 日志输出到 stderr，默认采用结构化日志（zap）。可通过环境变量 `AICLI_LOG_LEVEL` 控制日志级别（`debug`、`info`、`warn`、`error`）。

---

## 5. 日常操作

### 5.1 aicli 基本用法

```bash
# 初始化配置
aicli init --global

# 登录 provider
aicli login

# 进入交互式聊天
aicli

# 执行单次对话（headless）
aicli exec "请分析当前目录的代码质量"

# 查看版本
aicli version
```

> 详细用法见 [docs/aicli/install.md](../aicli/install.md) 和 [docs/aicli/exec.md](../aicli/exec.md)。

### 5.2 会话管理

- **Session 恢复**：runtime-server 支持会话断线恢复，通过 `/api/runtime/sessions/{id}/runtime/events` 回放事件
- **Session 历史**：存储在 SQLite 数据库中，默认路径为 `~/.aicli/sessions/session_history.sqlite`
- **Session 自动压缩**：超过阈值后自动压缩历史，减少数据库膨胀

### 5.3 多 Agent 与 Team

- **Portable Agent**：通过 `aicli chat --agent <definition>` 使用自定义 agent 定义
- **Team 编排**：runtime-server 支持 team 任务创建、分派与结果汇总
- **子 Agent**：`spawn_agent` 工具可创建子会话，`spawn_subagents` 并行执行独立子任务

> 详细说明见 [docs/multi-agents/README.md](../multi-agents/README.md)。

### 5.4 Skills / MCP

- **Skills**：注册为可调用能力的工具集合，通过 `aicli chat` 自动暴露
- **MCP Server**：可在 `backend/configs/` 中配置 MCP 服务器端点，扩展工具面

> 详细说明见 [docs/skill_runtime/README.md](../skill_runtime/README.md)。

### 5.5 数据库运维

#### 查看数据库信息

```bash
# 检查 SQLite 数据库大小
python -c "
import os, glob
for f in glob.glob(os.path.expanduser('~/.aicli/sessions/*.sqlite')):
    size = os.path.getsize(f) / 1024 / 1024
    print(f'{f}: {size:.1f} MB')
"
```

#### 手动同步副本

当 replica 落后时，可重启 runtime-server 触发全量同步。重启不会丢失数据，启动时自动从主库同步。

#### 迁移数据库

如果需要迁移会话数据到新位置：

```bash
# 停止 aicli 和 runtime-server
# 复制数据库文件
cp ~/.aicli/sessions/session_history.sqlite /new/path/session_history.sqlite
# 更新 runtime.yaml 中的 storePath 和 replicaSource
```

---

## 6. 构建

### 6.1 统一构建脚本

仓库提供 `scripts/build.ps1` 统一构建所有工具，支持三个目标平台：

| 参数 | 取值 | 说明 |
|------|------|------|
| `-Tools` | `all` 或逗号分隔列表 | 要构建的工具集 |
| `-Target` | `windows`, `win7`, `both` | 构建目标平台 |
| `-Version` | 版本字符串 | 注入到二进制中的版本号 |
| `-OutputDir` | 目录路径 | 产物输出目录（默认 `backend/dist`） |
| `-SkipTests` | 开关 | 跳过测试 |
| `-SkipDependencyCheck` | 开关 | 跳过依赖检查 |

**示例**

```powershell
# 构建所有工具 × Win7
pwsh -File ./scripts/build.ps1 -Target win7 -Tools all -Version win7-v1.0.0

# 构建 runtime-server 主线版
pwsh -File ./scripts/build.ps1 -Target windows -Tools runtime-server -Version v1.0.0

# 构建 SSH 工具族（Win7）
pwsh -File ./scripts/build.ps1 -Target win7 -Tools ssh-client,sftp-client,ssh-keygen

# 同时构建两套目标
pwsh -File ./scripts/build.ps1 -Target both -Tools all -Version v2.0.0
```

### 6.2 向后兼容的旧脚本

旧脚本名保留为薄壳包装器，参数不变：

| 旧脚本 | 等价于 build.ps1 调用 |
|--------|----------------------|
| `build-aicli-win7.ps1` | `-Tools aicli,aicli-console -Target win7` |
| `build-runtime-server-win7.ps1` | `-Tools runtime-server -Target win7` |
| `build-ssh-sftp-clients-win7.ps1` | `-Tools ssh-client,sftp-client -Target win7` |

### 6.3 Win7 构建说明

Win7 构建使用 **Go 1.21.4** 工具链和独立的 `go.win7.mod` 依赖图，确保二进制在 Windows 7 SP1 上可运行。

- 构建标签：`-tags win7compat`
- CGO：`CGO_ENABLED=0`
- 产物后缀：`-win7.exe`

> 详细构建说明见 [docs/aicli/windows7-build.md](../aicli/windows7-build.md)。

### 6.4 Makefile 目标

| 目标 | 说明 |
|------|------|
| `make build` | 构建所有工具 |
| `make aicli` | 构建 aicli |
| `make install-aicli` | 安装 aicli 到 `$GOBIN` |
| `make test` | 运行测试 |
| `make lint` | 代码检查 |
| `make frontend-build` | 构建前端 |

---

## 7. 故障排查

### 7.1 STORE_TIMEOUT / 503

**现象**：Web 页面访问会话列表时返回 "会话存储查询超时（共享数据库被并发写入占用）"（503）。

**原因**：aicli 与 runtime-server 同时操作同一 SQLite 数据库，aicli 写入锁阻塞了 runtime-server 的读取。

**解决**：
1. 确保 `runtime.yaml` 中配置了 `replicaSource`（read-replica 模式），避免直连主库
2. 如已配置 replica，等待约 30s（`replicaSyncInterval`）后重试
3. 检查是否有残留的旧进程（`aicli` 或 `runtime-server`）未正常退出：
   ```bash
   # Windows
   Get-Process | Where-Object { $_.ProcessName -match 'aicli|runtime-server' }
   # Linux
   ps aux | grep -E 'aicli|runtime-server'
   ```
4. 强制结束旧进程后重启 runtime-server

### 7.2 SQLite 文件锁

**现象**：`os.Remove` 无法删除文件，或 `os.Rename` 失败。

**原因**：Windows 上被其他进程打开的文件无法删除/重命名。Read-replica 模式下全量同步时会删除旧副本文件，若 aicli 正在读写该文件则会失败。

**解决**：
- 确保 aicli 进程已退出后再重启 runtime-server
- 避免在 runtime-server 运行时手动操作 SQLite 文件

### 7.3 启动失败：端口被占用

**现象**：`runtime-server serve` 报端口绑定失败。

**解决**：
```bash
# 查找占用端口的进程
# Windows
netstat -ano | findstr :8101
# Linux
ss -tlnp | grep 8101

# 使用 --listen 指定其他端口
runtime-server serve --listen 127.0.0.1:8102
```

### 7.4 Web 前端 404 / 空白页

**现象**：访问 `http://localhost:8101/` 返回 404 或空白页。

**原因**：runtime-server 二进制中未嵌入前端资源（使用了 `-EmbedPlaceholder` 构建）。

**解决**：
1. 重新用前端构建产物编译 runtime-server（去掉 `-EmbedPlaceholder`）
2. 或直接访问 API 端点：`http://localhost:8101/api/runtime/health`

### 7.5 aicli 登录失败

**现象**：`aicli login` 报 401 或 "no providers configured"。

**解决**：
1. 确认已执行 `aicli init --global` 初始化配置
2. 检查网络能否访问 provider 端点
3. 查看日志：`aicli doctor`
4. 详细 FAQ 见 [docs/aicli/faq.md](../aicli/faq.md)

### 7.6 Win7 构建启动崩溃

**现象**：Win7 上启动 exe 即报 `0xc0000005 PC=0x0`。

**原因**：产物不是用 Go 1.21.4 + `win7compat` 构建（例如遗漏了 build tag 或使用了标准 go.mod）。

**验证**：
```bash
go version -m dist/runtime-server-win7.exe | head -1
# 必须输出 "go1.21.4"
```

**解决**：使用 `build.ps1 -Target win7` 重新构建，确保 `-tags win7compat` 和 `-modfile=go.win7.mod` 同时生效。

---

## 8. 升级与卸载

### 8.1 升级二进制

```bash
# 拉取最新代码
git pull

# 重新构建并安装
make install-aicli

# 或使用构建脚本
pwsh -File ./scripts/build.ps1 -Target windows -Tools all -Version v2.0.0
```

### 8.2 升级数据库

数据库 schema 向前兼容，升级二进制后无需迁移。如需迁移历史数据：

```bash
# 停止所有进程 → 备份数据库 → 启动新版本
cp ~/.aicli/sessions/session_history.sqlite ~/.aicli/sessions/session_history.sqlite.bak
```

### 8.3 卸载

```bash
# Linux / macOS
make uninstall-aicli

# Windows
# 删除安装目录（默认 %LOCALAPPDATA%\Programs\aicli）
# 删除配置目录（默认 %USERPROFILE%\.aicli）
```

---

## 附录 A：相关文档索引

| 主题 | 文档 |
|------|------|
| 首次安装与快速上手 | [docs/aicli/quickstart.md](../aicli/quickstart.md) |
| 完整安装与配置 | [docs/aicli/install.md](../aicli/install.md) |
| 常见问题 | [docs/aicli/faq.md](../aicli/faq.md) |
| Headless exec | [docs/aicli/exec.md](../aicli/exec.md) |
| Portable agents | [docs/aicli/agents.md](../aicli/agents.md) |
| 图片生成工具 | [docs/aicli/tool_image_generate.md](../aicli/tool_image_generate.md) |
| Win7 构建 | [docs/aicli/windows7-build.md](../aicli/windows7-build.md) |
| 运行时 / Skills / Multi-agent | [docs/README.md](../README.md) |
| 开发指南 | [docs/development-guidelines.md](../development-guidelines.md) |
| Skills 运行时 | [docs/skill_runtime/README.md](../skill_runtime/README.md) |
| Multi-agent | [docs/multi-agents/README.md](../multi-agents/README.md) |
| SSH/SFTP 客户端 | [docs/tools/ssh-sftp-clients-usage.md](../tools/ssh-sftp-clients-usage.md) |

---

## 附录 B：常用命令速查

```bash
# === aicli ===
aicli --version                        # 查看版本
aicli init --global                    # 初始化配置
aicli login                            # 登录 provider
aicli                                  # 进入聊天
aicli exec "prompt"                    # 执行一次对话
aicli chat --session <id>              # 恢复指定会话
aicli doctor                           # 诊断检查

# === runtime-server ===
runtime-server serve                   # 前台启动
runtime-server start --wait 30s        # 后台启动
runtime-server stop                    # 停止
runtime-server status                  # 查看状态
runtime-server serve --listen :8101    # 指定端口
runtime-server serve --pprof           # 启用诊断

# === 构建 ===
pwsh -File scripts/build.ps1 -Target win7 -Tools runtime-server -Version dev
pwsh -File scripts/build.ps1 -Target both -Tools all -Version v1.0.0

# === 数据库 ===
# 查看 SQLite 文件大小
python -c "import os; print(f'{os.path.getsize(os.path.expanduser(\"~/.aicli/sessions/session_history.sqlite\"))/1024/1024:.1f} MB')"
```
