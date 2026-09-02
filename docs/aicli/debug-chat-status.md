# aicli 渲染/显示状态 HTTP 快照端点

```text
GET /debug/chat/status
GET /debug/chat/status?format=text
GET /debug/chat/screen
GET /debug/chat/screen?format=text
GET /debug/endpoints
GET /debug/endpoints?format=text
```

aicli 在 **`--debug`** 或 **`--pprof`** 模式下自动启动 loopback HTTP 服务器，提供以下诊断端点：

| 端点 | 说明 |
|------|------|
| `/debug/pprof/` | 标准 pprof 索引页（pprof 分析） |
| `/debug/chat/status` | 会话渲染/显示状态 JSON 快照（等价于 `/debug display`） |
| `/debug/chat/screen` | 当前屏幕合成帧内容（用户实际看到的文本） |
| `/debug/endpoints` | 全部调试相关端点清单（含 runtime-observe API 端点） |
| `/api/runtime/observe/v1/*` | Runtime Observation Plane（本地 in-process，默认随 `--pprof on` 开启）：`capabilities` / `snapshot` / `sessions/{id}` / `events` |

## `/debug/chat/status` — 渲染器状态快照（面向两种方式显示）

提供当前会话渲染/显示状态的快照，**面向两种方式显示**：

| 方式 | 访问路径 | 适用场景 |
|------|----------|----------|
| **JSON 结构化输出** | `GET /debug/chat/status`（默认） | 自动化轮询采样、工具链集成、程序化分析 |
| **纯文本（人类可读）** | `GET /debug/chat/status?format=text` | 终端直接查看、快速诊断、等价于 `/debug display` 面板 |

`format=text` 返回的文本内容与在会话内手动执行 `/debug display` 看到的完全一致，但**无需进入会话交互**。

主要用途：

- 连续观察 **Unified Render Encoder** 的编码/追加/提交计数器是否停滞；
- 定位 **AppState / History Gates** 哪个门在阻塞提交；
- 监控 **ActiveCell** 的 source ranges（Stable / Enqueued / Acked）是否呈现"enqueued 增长而 acked 不动"的 H1 停滞签名；
- 检查 **Executor** recovery-loop 诊断（backoff_engaged / dead_guard）；
- 查看 **物理终端缓存**（Projection）的 validity 状态。

## 启用方式

端点随 loopback HTTP 服务器自动启动。以下任一方式均可：

| 方式 | 命令 |
|------|------|
| `--pprof` 显式开启 | `aicli resume session_<id> --pprof on` |
| `--debug` 自动开启 | `aicli resume session_<id> --debug on` |
| 环境变量 | `AICLI_PPROF=127.0.0.1:0 aicli resume session_<id>` |

启动后 stderr 会打印：

```
Info: pprof endpoint enabled: http://127.0.0.1:50679/debug/pprof/
Info: chat render status endpoint: http://127.0.0.1:50679/debug/chat/status (JSON; ?format=text for plain text)
Info: chat screen content endpoint: http://127.0.0.1:50679/debug/chat/screen (JSON; ?format=text for plain text)
Info: chat debug endpoints list: http://127.0.0.1:50679/debug/endpoints (JSON; ?format=text for plain text)
Info: runtime observe plane: http://127.0.0.1:50679/api/runtime/observe/v1 (local in-process; capabilities/snapshot/sessions/events)
```

> **Runtime Observation Plane（本地模式）默认随 `--pprof on` 开启。**
> 只要 loopback HTTP 服务器启动（`--pprof` / `--debug` / `AICLI_PPROF`），本地 in-process 会话就会自动构建
> observe 服务，把 `/api/runtime/observe/v1/*` 端点挂到同一个 loopback 服务器上
> （见下文 [Runtime Observation Plane（本地模式）](#runtime-observation-plane本地模式)）。
> 与 runtime-server 模式不同，这里**不需要**任何远端服务，也**不会** fallback 到
> `http://127.0.0.1:8101` 这类并不存在的地址——observe base 直接指向本机 loopback 地址。

## 使用示例

### JSON 快照（默认）

```powershell
curl http://127.0.0.1:50679/debug/chat/status | python -m json.tool
```

### 纯文本摘要（人类可读，无需解析 JSON）

```powershell
curl http://127.0.0.1:50679/debug/chat/status?format=text
```

### 无活动会话时

```json
{
  "available": false,
  "reason": "no active chat session",
  "captured_at": "2026-09-01T12:00:00.123456+08:00"
}
```

### 连续轮询采样

```powershell
while ($true) {
  curl -s http://127.0.0.1:50679/debug/chat/status | python -c "import sys,json; d=json.load(sys.stdin); print(d.get('captured_at',''), d.get('app_state',{}).get('active_cell',{}).get('phase',''), d.get('render_output',{}).get('state',''), d.get('executor',{}).get('diagnosis',''))"
  Start-Sleep -Seconds 1
}
```

## 响应结构

快照包含以下顶级字段（按 `/debug display` 面板区块顺序）：

| 字段 | 类型 | 说明 |
|------|------|------|
| `available` | bool | 是否有活动会话 |
| `reason` | string | 不可用时原因 |
| `captured_at` | RFC3339 | 快照生成时间戳 |
| `session` | object | 会话基础信息（含 provider/model/endpoint/profile 等，与面板顶部 Session Info 一致） |
| `files` | object | 会话文件与目录（session store、日志、产物目录、last HTTP/Shell 等） |
| `runtime` | object | 运行时调试配置（config 路径、permission、queued input、surface 等） |
| `routing` | object | Subagent / Team Routing 配置摘要 |
| `components` | object | 运行时组件状态（核心组件 + 观察平面组件） |
| `agents` | object | AgentControl Registry / Agent Graph / Mailbox Pending |
| `render_encoder` | object | Unified Render Encoder 统计 |
| `scene` | object | Scene 层状态 |
| `render_output` | object | 输出管道（Gateway）统计 |
| `app_state` | object | **Presenter 调度器状态**（核心诊断块） |
| `executor` | object | **Executor recovery-loop 诊断** |
| `projection` | object | 物理终端缓存摘要 |
| `paint_trace` | string | 表面绘制追踪（调试用） |
| `pprof_url` | string | pprof 索引页 URL |

所有区块都是只读快照：只读取 session/provider 上的配置与状态，不做变更。每个区块既可通过 `/debug/chat/status` 以 JSON 方式访问，也可在 `/debug display` 面板中以纯文本方式查看（`?format=text` 返回 `/debug display` 面板文本）。

### session

```json
{
  "session_id": "session_abc123",
  "debug_mode": true,
  "transport": "openai",
  "runtime_core": "openai",
  "surface_enabled": true,
  "interaction": "Ready",
  "provider": "openai",
  "protocol": "openai",
  "model": "gpt-4o",
  "endpoint_url": "https://api.openai.com/v1",
  "host": "api.openai.com",
  "key_count": 1,
  "timeout": "30s",
  "stream": true,
  "supports_fast": true,
  "fast_mode": false,
  "reasoning_enabled": true,
  "profile": "default (agent=myagent)",
  "agent_source": "profile:default",
  "reasoning_effort": "medium"
}
```

对应 `/debug display` 面板顶部的 Session Info 区块（`ui.SessionInfoDocument`）+ 会话详情（`appendChatDebugSessionDetails`）。完整镜像了启动时展示的 provider/model/endpoint 信息，以及当前会话的 profile、agent source、reasoning effort 等。

### files

```json
{
  "session": "session_abc123",
  "session_store": "file://E:/repo/sessions",
  "session_file": "E:/repo/sessions/session_abc123.json",
  "chat_log_file": "E:/repo/sessions/chat-log-abc123.jsonl",
  "debug_log_file": "E:/repo/sessions/debug-log-abc123.jsonl",
  "http_artifact_dir": "E:/repo/sessions/http-artifacts-abc123",
  "shell_artifact_dir": "E:/repo/sessions/shell-artifacts-abc123",
  "generated_image_artifact_dir": "E:/repo/sessions/generated-images-abc123",
  "last_http_request": "E:/repo/sessions/http-artifacts-abc123/req-20260901T120000.json",
  "last_http_response": "E:/repo/sessions/http-artifacts-abc123/resp-20260901T120000.json",
  "last_shell_out": "E:/repo/sessions/shell-artifacts-abc123/out-20260901T120000.txt",
  "title": "My Chat Session",
  "summary": "Discussed project architecture",
  "history_messages": 42
}
```

对应 `/debug display` 面板的"会话文件与目录:"区块。所有路径均为绝对路径，不存在的路径显示 `<none>`。

### runtime

```json
{
  "aicli_config_path": "E:/repo/.aicli.yaml",
  "profile_root": "E:/repo/profiles",
  "agent_source": "profile:default",
  "runtime_config_path": "E:/repo/runtime.yaml",
  "mcp_config_path": "E:/repo/mcp.yaml",
  "skill_dirs": ["E:/repo/skills"],
  "output_format": "interactive",
  "no_interactive": false,
  "json_output": false,
  "json_envelope": false,
  "mcp_enabled": true,
  "debug_mode": true,
  "skills_debug": false,
  "permission_mode": "default",
  "approval_reuse": "none",
  "queued_input": 3,
  "queued_draining": true,
  "agent_target": "",
  "surface_enabled": true,
  "row_plan": "..."
}
```

对应 `/debug display` 面板的"运行时调试:"区块。包含 aicli config 路径、profile、permission、queued input 等运行时配置。

### routing

```json
{
  "subagent": {
    "source": "subagent",
    "enabled": true,
    "compatibility_mode": "permissive",
    "default_difficulty": "normal",
    "inherit_parent": true,
    "validate_models": true,
    "reasoning_policy": "ignore",
    "allow_provider_override": false,
    "allow_model_override": false,
    "allow_reasoning_override": false,
    "expert_limit": 0,
    "allowed_providers": ["openai"],
    "allowed_models": ["gpt-4o"],
    "levels": ["easy", "normal", "hard", "expert"],
    "roles": ["default", "code_review"]
  },
  "team": {
    "source": "subagent_inherited",
    "enabled": false,
    ...
  }
}
```

对应 `/debug display` 面板的"Subagent Routing:"与"Team Routing:"区块。当无 routing 配置时，`enabled=false` 且大多数字段为默认值。

### components

```json
{
  "runtime_core": "session-actor v1 transport=in-process",
  "actor_registry": true,
  "session_hub": true,
  "session_hub_active": 1,
  "event_bus": true,
  "event_store": true,
  "supervision": true,
  "team_store": true,
  "agent_control": true,
  "skills_mcp_surface": true,
  "background": true,
  "observe": {
    "enabled": true,
    "status": "ready",
    "retention": "retention=4096 events / 16777216 bytes / 10m0s",
    "limits": "event_max=65536 bytes snapshot_max=262144 bytes query=50..200",
    "redactor": "profile=safe_default key_ref=runtime-observe-fingerprint-v1",
    "ingress": "1024 events / 4194304 bytes"
  }
}
```

对应 `/debug display` 面板的"运行时组件:"区块。`runtime_core` 格式为 `"{core名字} v{contract_version} transport={transport}"`。

### agents

```json
{
  "registry": "AgentControl Registry: 3 active, 1 pending approval",
  "consistency": "checked 12 records, 0 issues | unavailable",
  "graph": [
    {
      "path": "/root/worker-1",
      "status": "running",
      "session_id": "session_worker1",
      "session_state": "running",
      "parent": "session_abc123",
      "depth": 1,
      "agent_type": "subagent",
      "team_id": "",
      "pending_approval": false,
      "pending_question": false,
      "pending_tool": ""
    }
  ],
  "mailbox": "Mailbox: 0 pending, 0 in-flight"
}
```

对应 `/debug display` 面板的"AgentControl Registry:"、"Agent Graph:"与"Mailbox Pending:"三个区块。`graph` 数组列出所有活跃 agent 层级关系。

## 面板区块 ↔ HTTP 端点对照表

以下对照表列出 `/debug display` 面板的每个信息区块，以及对应的 HTTP 端点访问方式。所有区块均可通过两种方式访问：

- **JSON 化访问**：`GET /debug/chat/status`（各区块对应 JSON 顶级字段）
- **纯文本访问**：`GET /debug/chat/status?format=text`（等价于 `/debug display` 面板文本）

| `/debug display` 面板区块 | 对应 JSON 字段 | HTTP 端点路径 |
|---------------------------|---------------|---------------|
| Session Info（顶部） | `session` | `GET /debug/chat/status` → `session` |
| 会话文件与目录 | `files` | `GET /debug/chat/status` → `files` |
| 运行时调试 | `runtime` | `GET /debug/chat/status` → `runtime` |
| Subagent Routing | `routing.subagent` | `GET /debug/chat/status` → `routing.subagent` |
| Team Routing | `routing.team` | `GET /debug/chat/status` → `routing.team` |
| 运行时组件 | `components` | `GET /debug/chat/status` → `components` |
| AgentControl Registry | `agents.registry` | `GET /debug/chat/status` → `agents.registry` |
| Agent Graph | `agents.graph` | `GET /debug/chat/status` → `agents.graph` |
| Mailbox Pending | `agents.mailbox` | `GET /debug/chat/status` → `agents.mailbox` |
| Unified Render Encoder | `render_encoder` | `GET /debug/chat/status` → `render_encoder` |
| Scene 层 | `scene` | `GET /debug/chat/status` → `scene` |
| 输出管道（Gateway） | `render_output` | `GET /debug/chat/status` → `render_output` |
| Presenter 调度器状态 | `app_state` | `GET /debug/chat/status` → `app_state` |
| Executor recovery-loop | `executor` | `GET /debug/chat/status` → `executor` |
| 物理终端缓存 | `projection` | `GET /debug/chat/status` → `projection` |
| 屏幕合成帧摘要 |（面板内嵌预览） | `GET /debug/chat/screen`（JSON 或 `?format=text`） |
| HTTP 调试端点清单 | `endpoints` | `GET /debug/endpoints`（JSON 或 `?format=text`） |
| pprof 性能分析 | — | `GET /debug/pprof/` 及子路径 `/debug/pprof/{heap,goroutine,executor…}` |
| Runtime Observation Plane | — | `GET /api/runtime/observe/v1/{capabilities,snapshot,sessions,events}` |

> **注意**：`render_encoder`、`scene`、`render_output`、`app_state`、`executor`、`projection` 是渲染器/调度器核心诊断区块，除 `/debug/chat/status` 外没有独立的 HTTP 端点。`screen` 区块有独立端点 `/debug/chat/screen`。pprof 和 observe 端点有独立路径。
>
> 所有区块都通过同一个 `/debug/chat/status` 端点返回（JSON 顶层字段或 `?format=text` 纯文本），无需为每个区块单独请求。

### render_encoder

```json
{
  "encode_count": 42,
  "append_count": 10,
  "upsert_count": 0,
  "remove_count": 0,
  "out_of_order_count": 0,
  "duplicate_count": 0,
  "unknown_count": 0,
  "tail": { "item_id": "item-7", "seq": 7 },
  "interaction_anchor": {
    "tail": { "item_id": "item-7", "seq": 7 },
    "at": "14:30:01",
    "source": "assistant_delta",
    "count": 3
  },
  "event_log": {
    "path": "E:\\repo\\sessions\\event-journal.jsonl",
    "recorded": 42,
    "replayed": 0,
    "failures": 0
  },
  "model_items_count": 7,
  "model_items_tail": [
    { "seq": 5, "id": "item-5", "kind": "user", "head": "hello" },
    { "seq": 6, "id": "item-6", "kind": "assistant", "head": "Hi! How can I help you today?" }
  ]
}
```

**诊断价值**：`encode_count` 连续采样期间不增长 → Unified Render Encoder 未收到新事件。`append_count` 不增长但 `encode_count` 增长 → 事件被追加到已有 item，Scene 未新建 cell。

### scene

```json
{
  "cells": 12,
  "revision": 5,
  "apply_failures": 0,
  "last_error": "",
  "layout_rows": 120,
  "layout_gaps": 0,
  "text_rows": 118,
  "text_parity": { "blocks": 12, "matched": 12, "missed": 0, "last_error": "" },
  "cells_tail": [
    { "id": 10, "kind": "user", "source": "hello" },
    { "id": 11, "kind": "assistant", "source": "Hi! How can I help..." }
  ]
}
```

**诊断价值**：`apply_failures` 非零 → Scene 层有应用失败。`text_parity.missed` 非零 → 渲染层双跑对不齐。

### render_output

```json
{
  "state": "idle",
  "primary_committed": 15,
  "primary_deferred": 0,
  "primary_rejected": 0,
  "primary_in_flight": 0,
  "admission_accepted": 15,
  "admission_deferred": 0,
  "admission_rejected": 0,
  "mirrors_applied": 45,
  "mirrors_failed": 0,
  "mirrors_skipped": 0,
  "mirrors_timed_out": 0,
  "mirrors_late": 0,
  "mirror_schedule_drops": 0,
  "mirror_pending": 0,
  "mirror_in_flight": 0,
  "observer_drops": 0,
  "event_journal_drops": 0,
  "delivery_records_sealed": 15,
  "delivery_records_unsealed": 0,
  "entry_seal_count": 15,
  "last_sequence": 15,
  "abandoned": 0,
  "abandoned_reason": "",
  "last_primary_duration_ns": 123456789
}
```

**诊断价值**：`primary_committed` 停滞 + `primary_in_flight` > 0 → 输出管道有未密封的 in-flight 记录，提交受阻。`mirror_failed` / `mirror_schedule_drops` 非零 → 镜像调度异常。

### app_state（核心诊断块）

```json
{
  "revision": 7,
  "layout_generation": 3,
  "width": 120,
  "height": 42,
  "primary_lease": "active",
  "history_effects": "frozen=false pending=0 active",
  "history_gates": {
    "frozen": false,
    "projection_unknown": false,
    "reconciliation_required": false,
    "recovery_actionable": false,
    "pending_count": 0,
    "oldest_pending_token": 0,
    "oldest_pending_generation": 0
  },
  "active_cell": {
    "id": 41,
    "kind": "assistant",
    "phase": "mutable",
    "revision": 7,
    "source": "partial streamed body text…",
    "commit_blocked": false,
    "stable_start": 0,
    "stable_end": 22,
    "enqueued_end": 22,
    "acked_end": 8
  }
}
```

#### history_gates 字段

| 字段 | 类型 | 含义 |
|------|------|------|
| `frozen` | bool | **冻结**：HistoryEffects 被冻结，拒绝任何新提交 |
| `projection_unknown` | bool | **投影未知**：物理终端缓存状态不可信，需先重建投影 |
| `reconciliation_required` | bool | **需协调**：滚动缓存需要显式协调后才能提交 |
| `recovery_actionable` | bool | **可恢复**：综合以上条件判断为需要执行恢复操作 |
| `pending_count` | int | 待处理（已提交但未确认）的 HistoryCommit 数量 |
| `oldest_pending_token` | uint64 | 最旧待处理提交的 token（可关联到 layout_generation） |
| `oldest_pending_generation` | uint64 | 最旧待处理提交对应的 layout_generation |

**诊断价值**：这四个门控直接回答"哪个条件在阻塞提交"。例如 `projection_unknown=true` 且 `recovery_actionable=true` → 需要先恢复投影，提交才能继续。

#### active_cell 字段

| 字段 | 类型 | 含义 |
|------|------|------|
| `id` | uint64 | 活动单元格 ID |
| `kind` | string | 单元格类型（assistant / reasoning / tool_chain） |
| `phase` | string | **mutable** / **finalizing** / **inactive** |
| `revision` | uint64 | 单元格修订号 |
| `source` | string | 语义源内容（截断至 48 字节） |
| `commit_blocked` | bool | HistoryCommit 是否被阻塞 |
| `stable_start` / `stable_end` | int | **稳定渲染范围**：已确认渲染到终端的字节偏移 |
| `enqueued_end` | int | **已入队范围**：已入队调度但尚未确认的字节偏移 |
| `acked_end` | int | **已确认范围**：终端已确认的字节偏移 |

**诊断价值**：H1 停滞的经典签名——**`enqueued_end` 持续增长，`acked_end` 保持不动，`phase` 始终为 "mutable"**。这意味着 active band 的流更新持续渲染（enqueued 增长），但确认前缀（acked）从不推进，因此 HistoryCommit 无法完成。`commit_blocked=true` 提供额外确认。

### executor

```json
{
  "diagnosis": "healthy",
  "total_recoveries": 12,
  "backoff_engaged": 0,
  "armed_backoff": 1,
  "flushes_while_backoff": 0,
  "handoffs_while_backoff": 0,
  "generated_at_unix_ms": 1723045678000,
  "window_recoveries_per_sec": 0.5,
  "generation_advances_in_window": 2,
  "frame_errors_in_window": 0,
  "scrollback_resets_in_window": 0,
  "last_generation": 3,
  "last_entry": {
    "seq": 1,
    "branch": "scheduled",
    "generation": 3,
    "revision": 5,
    "revision_after": 6,
    "terminal_epoch": 1,
    "projection_unknown": false,
    "reconciliation_required": false,
    "backoff_engaged": false,
    "armed_backoff": false,
    "full_repaint": false,
    "scrollback_reset": false,
    "frame_error": "",
    "flushed_while_backoff": false,
    "handoff_while_backoff": false,
    "continued": false
  }
}
```

**诊断价值**：`diagnosis` 字段给出执行器恢复循环的健康状态：

| 诊断值 | 含义 |
|--------|------|
| `idle` | 无待恢复项，正常 |
| `healthy` | 有恢复但进度正常 |
| `backoff_engaged` | **恢复回退已启动**：executor 正在以 backoff 间隔重试 recovery flush |
| `dead_guard` | **死循环保护**：recovery 在不变 generation 上反复失败，可能已死锁 |

`frame_errors_in_window` 非零 → 布局帧持续出错。`backoff_engaged` > 0 且 `last_generation` 不变 → 执行器在相同 generation 上反复重试而未推进。

### projection

```json
{
  "history_rows": 120,
  "history_known": true,
  "layout_generation": 3,
  "terminal_epoch": 1,
  "frame": 128,
  "scrollback_reset_count": 0,
  "last_scrollback_reset_reason": "",
  "validity": "known",
  "output_bottom_row": 40
}
```

**诊断价值**：`history_known=false` 或 `validity != "known"` 是**物理终端缓存不可信**的直接证据——这正是 executor 拒绝提交的条件。`validity` 取值：

| 值 | 含义 |
|-----|------|
| `known` | 投影有效，终端缓存可信 |
| `unknown` | 投影未知，需重建 |
| `stale` | 投影已过期，需刷新 |
| `invalid` | 投影无效，需完全重建 |

## 五信号诊断法

连续采样 `/debug/chat/status` 时，关注以下 5 个信号即可精确定位"统一渲染器只更新 active band 而不提交"的根因：

| # | 信号 | 位置 | 停滞模式 |
|---|------|------|---------|
| 1 | **active_cell.acked_end 停滞 + enqueued_end 增长** | `app_state.active_cell` | 流更新渲染但确认前缀不推进 |
| 2 | **history_gates 任一 true** | `app_state.history_gates` | 提交被阻塞的确切门控 |
| 3 | **executor.diagnosis ≠ "healthy"/"idle"** | `executor.diagnosis` | 执行器陷入恢复循环 |
| 4 | **render_output.primary_committed 停滞 + primary_in_flight > 0** | `render_output` | 输出管道有未密封的 in-flight |
| 5 | **projection.history_known=false 或 validity≠"known"** | `projection` | 物理终端缓存不可信 |

### 典型诊断流程

1. 检查 `available` → 有会话；
2. 检查 `app_state.active_cell.phase` → 若为 "mutable" 且持续不动 → 活动单元格未完成；
3. 检查 `app_state.active_cell.acked_end` 和 `enqueued_end` → **enqueued 增长而 acked 不变** → 确认 H1 签名；
4. 检查 `app_state.history_gates` → 哪个门为 true → 确定阻塞原因；
5. 检查 `executor.diagnosis` → 是否陷入 backoff 或 dead_guard；
6. 检查 `projection.history_known` 和 `validity` → 终端缓存是否可信；
7. 综合以上 5 个信号得出根因。

## `/debug/chat/screen` — 当前屏幕内容快照

提供**用户当前实际看到的屏幕合成帧**（终端最终显示内容），与 `/debug/chat/status` 互补：

| 端点 | 观察对象 |
|------|----------|
| `/debug/chat/status` | 渲染器**内部状态**（encoder / scene / output / app_state） |
| `/debug/chat/screen` | 合成帧**最终文本**（等价于"截图"） |

### 纯文本（推荐，人类可读）

```powershell
curl http://127.0.0.1:50679/debug/chat/screen?format=text
```

返回完整屏幕文本（每行以 `\n` 分隔，末尾有 `\n`），可直接重定向保存为终端截图：

```powershell
curl -s http://127.0.0.1:50679/debug/chat/screen?format=text -o screen.txt
```

### JSON 快照（默认）

```powershell
curl http://127.0.0.1:50679/debug/chat/screen | python -m json.tool
```

```json
{
  "available": true,
  "width": 80,
  "height": 24,
  "lines": ["session_abc123", "user> hello", "assistant> Hi!"],
  "text": "session_abc123\nuser> hello\nassistant> Hi!"
}
```

### 无活动会话时

```json
{
  "available": false,
  "reason": "no active chat session"
}
```

**诊断价值**：周期性请求 `/debug/chat/screen?format=text` 可直接观察"用户看到的内容是否在变化"。若 `/debug/chat/status` 显示 `render_encoder.encode_count` 在增长，但 `/debug/chat/screen` 文本连续多帧不变 → 渲染器在编码/提交，但合成帧未更新（输出管道被 gate 阻塞的直接证据）。该端点也在 `/debug display` 内以"屏幕合成帧"摘要块展示（尺寸 + 前 5 行预览）。

## 补充端点

### `/debug/endpoints` — 调试端点清单

返回当前环境**全部调试相关 HTTP 端点**的统一清单，便于脚本/工具一次性发现所有调试入口。默认返回 JSON；`?format=text` 返回纯文本。

端点按 **loopback**（本机 `--pprof` HTTP 服务器）与 **runtime-observe**（Runtime Observation Plane）两个分组展示，每个端点带 `[enabled]` / `[disabled]` 标记与用途说明。

```powershell
# JSON 格式（默认）
curl http://127.0.0.1:50679/debug/endpoints

# 纯文本清单
curl 'http://127.0.0.1:50679/debug/endpoints?format=text'
```

纯文本输出示例（`?format=text`，本地模式 + `--pprof` 开启时）：

```text
loopback  (aicli --pprof 本机调试服务器)
  Base: http://127.0.0.1:50679
  GET http://127.0.0.1:50679/debug/pprof/  [enabled]  pprof 性能分析索引（含 heap/allocs/goroutine/block/mutex/trace 等）
  GET http://127.0.0.1:50679/debug/pprof/executor  [enabled]  executor 恢复循环逐次诊断
  GET http://127.0.0.1:50679/debug/chat/status  [enabled]  渲染/显示状态快照（JSON / ?format=text）
  GET http://127.0.0.1:50679/debug/chat/screen  [enabled]  当前屏幕合成帧（JSON / ?format=text）
  GET http://127.0.0.1:50679/debug/endpoints  [enabled]  调试端点清单（本端点）
runtime-observe  (Runtime Observation Plane)
  Base: http://127.0.0.1:50679/api/runtime/observe/v1
  GET http://127.0.0.1:50679/api/runtime/observe/v1/capabilities  [enabled]  观察平面能力声明
  GET http://127.0.0.1:50679/api/runtime/observe/v1/snapshot  [enabled]  当前会话运行快照
  GET http://127.0.0.1:50679/api/runtime/observe/v1/sessions/{session_id}  [enabled]  指定会话详情
  GET http://127.0.0.1:50679/api/runtime/observe/v1/events  [enabled]  事件流（轮询观测）
```

本地模式下 observe 组 base 就是 loopback 基础地址；只有既未启动本地 observe 服务、也未连接 runtime-server 时才会显示 `<route-only>`（仅相对路径）。

JSON 响应包含 `available`、`base_url`（loopback 基础地址，向后兼容）、`loopback_base_url`、`observe_base_url` 与 `endpoints` 数组，每个端点含 `method`、`path`、`scheme`（`loopback` / `runtime-observe`）、`enabled`、`url`（base 已知时）与 `note` 字段。

在 `/debug display` 面板中，同一清单以 **"HTTP 调试端点:"** 区块展示，按 loopback / runtime-observe 分组，每组带基础地址与端点说明，与 `/debug/endpoints?format=text` 输出一致。

## Runtime Observation Plane（本地模式）

aicli 本地 in-process 模式（无 runtime-server）下，观察平面由进程内服务提供，挂载在
loopback HTTP 服务器上，前缀为 `/api/runtime/observe/v1`。**默认随 `--pprof on` 开启**：
只要 loopback 服务器启动，本地会话即自动启用观察平面（无需额外配置）；此外
`RuntimeConfig.Observe.Enabled=true` 也会单独启用它。

启用条件（二者满足其一即可）：

- pprof loopback HTTP 服务器已开启（`--pprof` / `--debug` / `AICLI_PPROF`）→ 默认开启；
- `observe.enabled = true`（显式配置）。

服务为**惰性构建**：首次被 HTTP 请求或 `/debug display` 触发时创建一次并缓存，
会话关闭（`host.Close()`）时释放 collector 的 bus 订阅。数据管道与 runtime-server 相同：
订阅白名单事件 → redactor 脱敏 → projector 投影 → retention ring，因此
`/debug/chat/status` 的 renderer 硬编码不可用状态（`renderer_observation_not_implemented`）
同样适用于本地模式。

四个端点与 runtime-server 行为一致（同一 envelope 契约）：

```powershell
# 能力声明
curl http://127.0.0.1:50679/api/runtime/observe/v1/capabilities

# 复合快照（默认含 sessions；?include_sessions=0 可省略）
curl http://127.0.0.1:50679/api/runtime/observe/v1/snapshot

# 指定会话详情（未知 session → 404 observe_session_not_found）
curl http://127.0.0.1:50679/api/runtime/observe/v1/sessions/session_abc123

# 事件流（轮询观测；支持 after_seq / limit / event_types 等查询参数）
curl 'http://127.0.0.1:50679/api/runtime/observe/v1/events?after_seq=0&limit=20'
```

响应统一使用 envelope（`ok` / `schema_version` / `request_id` / `data` / `warnings` /
`redaction`），错误码与 HTTP 状态映射同 runtime-server：disabled → 403、session
not found → 404、cursor expired → 410、invalid → 400、resource exceeded → 429。
端点只监听 127.0.0.1 loopback，无需额外鉴权 token。

### `/debug/pprof/executor`

提供 executor recovery-loop 的完整环形缓冲诊断（原始数据源），包含每次 recovery flush 的 revision 前后值、generation、epoch、ProjectionUnknown / ReconciliationRequired 标志、FullRepaint / ScrollbackReset 事件、frame 错误、backoff 是否 arm/触发，以及派生的循环健康诊断。

```powershell
# JSON 格式（默认）
curl http://127.0.0.1:50679/debug/pprof/executor

# 纯文本摘要
curl 'http://127.0.0.1:50679/debug/pprof/executor?format=text'
```

### 标准 pprof 端点

```powershell
curl http://127.0.0.1:50679/debug/pprof/
curl http://127.0.0.1:50679/debug/pprof/heap
curl http://127.0.0.1:50679/debug/pprof/goroutine
```

## 实现说明

- 端点使用 **独立路径 `/debug/chat/status` 与 `/debug/chat/screen`**，不混入标准 pprof 命名空间 `/debug/pprof/`；
- 无活动会话时两者均返回 `available=false`（HTTP 200），便于轮询探测；
- 不依赖 Observe API（`service.go:126-130` 写死 `renderer_observation_not_implemented`），直接读取内存中的 Presenter / Executor / TerminalSession 内部状态；
- 快照在 HTTP 请求处理函数中同步构建，**不是定期缓存**，确保每次请求都是当前时刻的最新状态；
- 使用 `--pprof` 或 `--debug` 启动的 HTTP 服务器默认监听 `127.0.0.1:0`（随机空闲端口），仅本机可访问。