# runtime-server 使用手册

> 对应程序：`cmd/runtime-server`（`runtime-server` / `runtime-server.exe`）
> 作用：AI Agent 运行时的 HTTP API 服务 + Web 控制台后端，提供 `/api/agent/chat` 与 `/api/runtime/*` 系列接口。

---

## 目录

1. [概述](#1-概述)
2. [子命令](#2-子命令)
3. [常用参数](#3-常用参数)
4. [配置文件](#4-配置文件)
5. [示例](#5-示例)
6. [管理接口](#6-管理接口)
7. [诊断](#7-诊断)
8. [相关文档](#8-相关文档)

---

## 1. 概述

`runtime-server` 以 HTTP/SSE 方式对外提供 Agent 运行时能力：

- `POST /api/agent/chat`：Agent 对话/任务执行（含流式）
- `/api/runtime/*`：会话、技能、团队、agent 控制、日志、后台任务、图片生成、运行时配置等管理接口
- 内嵌 Web 控制台前端（React + Vite 构建产物，编译进二进制）

支持前台运行（`serve`）与后台守护（`start`/`stop`/`status`）。

---

## 2. 子命令

| 子命令 | 说明 |
|--------|------|
| `serve` | 前台启动服务（默认；不带子命令时等价于 `serve`） |
| `start` | 后台启动服务并写入 PID 文件 |
| `stop` | 停止服务（优先按 PID 文件；也可 `--pid` 直接指定） |
| `status` | 查看运行状态 |
| `help` / `-h` | 显示帮助 |

---

## 3. 常用参数

| 参数 | 适用于 | 说明 |
|------|--------|------|
| `-c, --config <path>` | serve/start/status | 配置文件路径；未指定时按 `$HOME/.aicli/` → `./.aicli/` → `./` → `./configs/` 顺序查找（找不到回退 `config.yaml`） |
| `--listen <host:port>` | serve/start/status | 监听地址，优先级高于配置文件，如 `127.0.0.1:8101` |
| `--pid-file <path>` | serve/start/stop/status | PID 文件路径（默认 `./logs/runtime-server.pid`） |
| `--wait <duration>` | start | 等待后台进程完成启动的超时时间（默认 30s） |
| `--wait <duration>` | stop | 等待进程退出的超时时间（默认 10s） |
| `--pid <pid>` | stop | 直接停止指定 PID，跳过 PID 文件 |
| `--pprof` | serve/start | 启用 pprof 诊断端点（127.0.0.1 随机空闲端口，可用 `AICLI_PPROF` 环境变量指定地址） |

---

## 4. 配置文件

默认配置文件名：`runtime.yaml`（Win7 profile 下为 `runtime.win7.yaml`）。

主要配置项（节选）：

```yaml
version: "v1"

sessions:
  backend: sqlite                    # 会话存储后端
  storePath: session_history_replica.sqlite
  replicaSource: session_history.sqlite
  replicaSyncInterval: 30s

agent:
  maxSteps: 0                        # 最大步数（0=无限制）
  enableParallelTools: true          # 并行工具执行
  maxParallelToolCalls: 4

background:
  defaultTimeout: 0s
  heartbeatTimeout: 30s
  launchMaxAttempts: 3

observe:
  enabled: true                      # 运行时观测 API

sessionRuntime:
  defaultPersistence: memory         # memory 或 file
```

> 完整配置说明见 [docs/user-guide/README.md](README.md#3-配置)。

---

## 5. 示例

```bash
# 前台启动（开发调试）
runtime-server serve --listen 127.0.0.1:8101 --config backend/configs/runtime.yaml

# 后台启动并等待就绪
runtime-server start --listen 127.0.0.1:8101 --config backend/configs/runtime.yaml --wait 30s

# 查看状态
runtime-server status

# 停止
runtime-server stop

# 直接停止指定进程
runtime-server stop --pid 12345

# 启用 pprof 诊断
runtime-server serve --pprof
```

---

## 6. 管理接口

服务启动后提供以下 API 前缀（具体以代码为准）：

| 前缀 | 说明 |
|------|------|
| `POST /api/agent/chat` | Agent 对话/任务（支持 SSE 流式） |
| `/api/runtime/sessions` | 会话管理 |
| `/api/runtime/skills` | 技能管理、搜索、用量 |
| `/api/runtime/teams` | team 任务编排 |
| `/api/runtime/agent-control` | 子 agent 控制（mailbox/tasks/events） |
| `/api/runtime/logs` | 日志查询 |
| `/api/runtime/background` | 后台任务 |
| `/api/runtime/images` | 生成图片管理 |
| `/api/runtime/config` | 运行时配置 |

---

## 7. 诊断

- `status` 子命令输出运行状态（PID、监听地址、配置文件）
- `--pprof` 启用后，pprof 端点 URL 打印到 stderr；`AICLI_PPROF=127.0.0.1:6060` 可固定地址
- 启动失败时优先检查端口冲突（日志会提示占用 PID）与配置文件路径

---

## 8. 相关文档

- [docs/user-guide/README.md](README.md) — 安装部署、配置与故障排查
- [docs/skill_runtime/runtime_operations_api.md](../skill_runtime/runtime_operations_api.md) — 后台任务 HTTP 操作
- [docs/skill_runtime/session_agent_api.md](../skill_runtime/session_agent_api.md) — 会话/agent API
- [docs/aicli/windows7-build.md](../aicli/windows7-build.md) — Win7 构建
