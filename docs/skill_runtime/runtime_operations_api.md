# Runtime Operations API

更新时间：2026-05-09

本文记录当前独立 `runtime-server` 的运维与运行期 HTTP surface。当前源码路由来源为：

- `backend/cmd/runtime-server/main.go`
- `backend/internal/api/skills/handler.go`
- `backend/internal/api/skills/*_handlers.go`

## 启动与进程管理

`runtime-server` 支持以下子命令：

- `serve [--config PATH] [--listen HOST:PORT] [--pid-file PATH]`
- `start [--config PATH] [--listen HOST:PORT] [--pid-file PATH] [--wait 30s]`
- `stop [--pid-file PATH] [--pid PID] [--wait 10s]`
- `status [--config PATH] [--listen HOST:PORT] [--pid-file PATH]`

推荐本地开发启动方式：

```bash
cd E:\projects\ai\ai-agent-runtime\backend
go run ./cmd/runtime-server serve --listen 127.0.0.1:8081
```

兼容说明：

- 不带子命令时等价于 `serve`，旧的 `go run ./cmd/runtime-server --listen ...` 写法仍可用。
- 默认 PID 文件为 `./logs/runtime-server.pid`。
- 未指定 `--config` 时，配置搜索顺序为 `$HOME/.aicli/config.yaml -> ./.aicli/config.yaml -> ./config.yaml -> ./configs/config.yaml`。
- 当前 runtime config snapshot 已收敛为 base config only；旧的 `config.runtime.snapshot.yaml` 语义应视为历史说明。

## 主入口

- `POST /api/agent/chat`
  - 当前 agent chat 主入口。
- `/api/runtime/*`
  - skills、sessions、teams、AgentControl、观测、配置、日志、文件与后台任务等运行期 API。

旧文档中的 `/api/skills/runtime/*`、`/api/skills/teams/*`、`/api/skills/agent/chat` 不应作为当前仓库的 live route，除非外层代理自行映射。

## Service Control

源码：

- `backend/internal/api/skills/service_control_handlers.go`
- `backend/internal/runtimeserver/service_api.go`

Endpoints:

- `GET /api/runtime/service`
- `POST /api/runtime/service/restart`

`GET /api/runtime/service` 返回 `service` 对象，主要字段包括：

- `running`
- `pid`
- `pid_file`
- `listen_addr`
- `config_path`
- `cwd`
- `executable`
- `started_at`
- `restart_supported`
- `note`

`POST /api/runtime/service/restart` 返回 `202 Accepted`，响应体包含 `restart.accepted`、`message`、`requested_at`。当前实现通过 runtime-server service control 层执行重启；如果未来重启脚本继续增长，应优先改成临时 `.ps1` 文件，而不是继续扩展内联 `powershell -Command`。

## Runtime Logs

源码：

- `backend/internal/api/skills/log_viewer_handlers.go`

Endpoints:

- `GET /api/runtime/logs`
- `GET /api/runtime/logs/stream`

这两个入口需要 usage/admin 授权。

`GET /api/runtime/logs` query:

- `limit`
  - 默认 `200`，最大 `500`。
- `level`
  - 可选，按日志级别过滤。
- `query`
  - 可选，按文本过滤。

响应包含：

- `entries`
- `count`
- `exists`
- `file_path`
- `next_cursor`
- `filters`

`GET /api/runtime/logs/stream` query:

- `after`
  - 可选，日志 cursor。
- `poll_ms`
  - 可选，默认约 `750ms`，最大 `5s`。
- `level`
- `query`

SSE event:

- `ready`
- `log`
- `reset`
- `error`

## Config Document

源码：

- `backend/internal/api/skills/config_document_handlers.go`
- `backend/internal/runtimeserver/config_document_runtime.go`

Endpoints:

- `GET /api/runtime/config/document`
- `PUT /api/runtime/config/document`
- `POST /api/runtime/config/document/preview`
- `POST /api/runtime/skills/config/write`

保存与预览 request 可包含：

- `raw`
- `parsed`
- `mode`
- `changed_by`

响应中的 `document` 主要字段：

- `path`
- `format`
- `raw`
- `parsed`
- `sections`
- `size_bytes`
- `updated_at`
- `warnings`
- `restart_required`
- `supports_structured_save`
- `runtime_impact`

`runtime_impact` 会标出 `changed_paths`、`hot_reload_paths`、`restart_required_paths`、`inactive_paths`、`applied_paths` 等信息。

## Models

源码：

- `backend/internal/api/skills/handler.go`

Endpoint:

- `GET /api/runtime/models`

用于列出前端可用的聊天 provider / model 目录。

## File Transfer

源码：

- `backend/internal/api/skills/file_transfer_handlers.go`

Endpoints:

- `POST /api/runtime/fs/read-file`
- `POST /api/runtime/fs/write-file`
- `POST /api/runtime/fs/append-file`

读文件 request:

```json
{
  "path": "relative/or/absolute/path"
}
```

写入与追加 request:

```json
{
  "path": "relative/or/absolute/path",
  "data_base64": "..."
}
```

`read-file` 响应中的 `file.data_base64` 为 base64 内容；`write-file` 返回 `200 OK`，`append-file` 返回 `202 Accepted`。

## Background Jobs

源码：

- `backend/internal/api/skills/background_handlers.go`
- `backend/internal/background/*`

Endpoints:

- `GET /api/runtime/background/jobs`
- `GET /api/runtime/background/jobs/{id}`
- `POST /api/runtime/background/jobs/{id}/cancel`
- `GET /api/runtime/background/jobs/{id}/events`
- `GET /api/runtime/background/jobs/{id}/output`

`GET /api/runtime/background/jobs` query:

- `session_id`
- `status`
  - 逗号分隔，支持 `pending`、`running`、`completed`、`failed`、`cancelled`。
- `limit`
- `offset`

`events` 支持 `after` 与 `limit`；`output` 支持 `offset` 与 `limit`。

## Sessions, Checkpoints, Images

常用 route group:

- `GET/POST /api/runtime/sessions`
- `GET/PATCH/DELETE /api/runtime/sessions/{id}`
- `GET/DELETE /api/runtime/sessions/{id}/history`
- `GET /api/runtime/sessions/{id}/runtime`
- `GET /api/runtime/sessions/{id}/runtime/events`
- `GET /api/runtime/sessions/{id}/runtime/stream`
- `POST /api/runtime/sessions/{id}/runtime/commands`
- `GET /api/runtime/sessions/{id}/runtime/tool-receipts`
- `GET /api/runtime/sessions/{id}/generated-images/{name}`
- `GET /api/runtime/sessions/{id}/checkpoints`
- `GET /api/runtime/sessions/{id}/checkpoints/{checkpoint_id}/files`
- `POST /api/runtime/sessions/{id}/checkpoints/{checkpoint_id}/preview`
- `POST /api/runtime/sessions/{id}/checkpoints/{checkpoint_id}/restore`

Child-agent 控制面详见 [Session Agent HTTP API](./session_agent_api.md)。

## Teams And AgentControl

常用 route group:

- `/api/runtime/teams/*`
- `/api/runtime/agent-control/*`

Team task outcome 当前 canonical HTTP 入口为：

- `POST /api/runtime/teams/{id}/tasks/{task_id}/outcome`

`/complete`、`/fail`、`/block` 是历史 HTTP alias 说明，不是当前 live route。Go typed client 中 `CompleteTask`、`FailTask`、`BlockTask` 只是围绕 canonical `/outcome` 的 convenience wrappers。
