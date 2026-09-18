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
| `--web-port <port>` | serve/start | 指定 pprof 诊断端点监听端口（1-65535）；等价 `AICLI_PPROF=127.0.0.1:<port>` 且优先级更高，越界直接报错退出；`start` 会随子进程转发 |

---

## 4. 配置文件

默认配置文件名：`runtime.yaml`（Win7 profile 下为 `runtime.win7.yaml`）。

主要配置项（节选）：

```yaml
version: "v1"

sessions:
  backend: sqlite                    # 会话存储后端
  storePath: session_history.sqlite  # 与 aicli 共享的主会话库（相对 sessions.dir）

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
| `/api/runtime/mcps` | MCP 管理（列表/新增/编辑/删除/启停/热重载） |

### MCP 管理接口

与 `aicli mcp ...`、微型 Web 客户端共用 `internal/mcp/admin` 的同一套读写实现，
编辑的始终是配置文件里的 `mcpServers` 段（写操作会落盘并触发热重载）。路径解析优先级：
`./.aicli/mcp.yaml` > `~/.aicli/mcp.yaml` > 显式配置 > 向上搜索 > `configs/mcp.yaml`
（都不存在时落到 `~/.aicli/mcp.yaml` 并自动创建）。服务启动即自动建连（后台并行，不再有
`auto_connect` 开关）；单个 server 是否参与连接由 `enabled` 字段控制，管理接口的写操作 /
热重载会即时生效。

| 方法 | 路径 | 说明 |
|------|------|------|
| `GET` | `/api/runtime/mcps` | 列表：`{"count":N,"mcps":[{"config":{...},"status":{...}}]}` |
| `POST` | `/api/runtime/mcps` | 新增（`UpsertRequest`），201 返回 `{"config":...,"status":...}` |
| `GET/PUT/DELETE` | `/api/runtime/mcps/{name}` | 查看 / 更新 / 删除；删除返回 `{"removed":true}` |
| `POST` | `/api/runtime/mcps/{name}/enable` \| `/disable` | 启停（持久化 `enabled` 并重连） |
| `POST` | `/api/runtime/mcps/reload` | 热重载并重连，返回 `{reloaded,trace_id,catalog,runtime,health}` |

`GET /api/runtime/mcps` 额外返回观测字段（向后兼容，旧字段不变）：

- `config`：实际读写的配置文件与解析来源
  （`path` / `source`（`explicit|project|user|upward|executable|default|user-fallback|session-override`）
  / `exists` / `size_bytes` / `mod_time` / `manager_loaded` / `candidates[]`）。
  `candidates` 按优先级列出全部候选位置及各自 `exists`，用于定位“不同 CWD 启动解析到
  不同 `mcp.yaml`，导致某些 server 没连”。
- `summary`：`{total, enabled, disabled, connected, tools}` 计数。

启动时也会输出一行 `MCP config loaded`（`path` / `source` / `servers` / `enabled`），
后台建连再输出 `MCP manager starting in background`。

`UpsertRequest` 字段：`name`、`type`（`stdio`/`sse`/`websocket`/`streamable`）、
`command`/`args`（stdio）、`url`（其余传输）、`env`、`headers`、`description`、
`enabled`、`trustLevel`、`timeoutSeconds`、`maxParallelCalls`。

字段语义：`env`/`headers` 为**指针语义**——请求中省略（或 JSON 不出现）表示保持原值，
显式传 `{}` 表示清空，传非空 map 表示整体替换；`headers` 最终以 `HEADER_<Name>`
形式落到配置文件的 `env`（stdio 的 `HEADER_*` 只是普通环境变量，不做请求头解释）。
console 与微型 Web 面板的键值行编辑器按传输类型展示：stdio 全部是环境变量行，
URL 传输把 `HEADER_*` 拆成请求头行（去前缀），保存时合并回 `env`（全量替换，
清空行即清空对应配置）。

```bash
# 新增（streamable：粘贴 Chrome MCP 的 /mcp 地址即可）
curl -X POST http://127.0.0.1:8101/api/runtime/mcps -H 'Content-Type: application/json' \
  -d '{"name":"chrome-mcp","type":"streamable","url":"http://127.0.0.1:12306/mcp"}'

# 停用 / 热重载 / 删除
curl -X POST http://127.0.0.1:8101/api/runtime/mcps/chrome-mcp/disable
curl -X POST http://127.0.0.1:8101/api/runtime/mcps/reload
curl -X DELETE http://127.0.0.1:8101/api/runtime/mcps/chrome-mcp
```

鉴权：回环来源免 token；非回环需要 `X-Skills-Admin-Token`（与 skills 管理接口一致）。
校验失败返回 400、目标不存在返回 404，错误体为
`{"error":{"code":"...","message":"..."}}`。

---

## 7. 诊断

- `status` 子命令输出运行状态（PID、监听地址、配置文件）
- `--pprof` 启用后，pprof 端点 URL 打印到 stderr；用 `--web-port 6060`（推荐）或
  `AICLI_PPROF=127.0.0.1:6060` 固定地址（两者都不需要再传 `--pprof`）
- 启动失败时优先检查端口冲突（日志会提示占用 PID）与配置文件路径

---

## 8. 相关文档

- [docs/user-guide/README.md](README.md) — 安装部署、配置与故障排查
- [docs/skill_runtime/runtime_operations_api.md](../skill_runtime/runtime_operations_api.md) — 后台任务 HTTP 操作
- [docs/skill_runtime/session_agent_api.md](../skill_runtime/session_agent_api.md) — 会话/agent API
- [docs/aicli/windows7-build.md](../aicli/windows7-build.md) — Win7 构建
