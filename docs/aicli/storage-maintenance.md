# aicli 本地存储维护与按龄会话清理

> 适用范围：`~/.aicli/sessions/` 下的会话历史、runtime、usage analytics、artifacts、agent-control、background、team 和 schedule 数据。
>
> 核心原则：**先预演，再删除；删除后必须离线压缩，文件才会真正变小。** 所有 `prune-*` 命令默认都是 dry-run。

## 1. 数据分布

| 库 | 默认位置 | 主要内容 |
|---|---|---|
| 会话历史 | `~/.aicli/sessions/session_history.sqlite` | `sessions`、`session_messages`、`session_prompt_messages` |
| 会话 runtime | `~/.aicli/sessions/runtime/session_runtime.sqlite` | 运行事件、工具回执、缓存请求镜像、runtime state、mailbox、lease |
| 用量分析 | `~/.aicli/sessions/runtime/usage_analytics.sqlite` | `usage_requests`、turns、tool calls、LSP、subagent、route 等 |
| Artifacts | `~/.aicli/sessions/runtime/artifacts.sqlite` | artifacts、checkpoints、checkpoint files、memory entries、blobs |
| Agent control | `~/.aicli/sessions/runtime/agent_control.sqlite` | agent 注册表、wake events、global mailbox |
| Background | `~/.aicli/sessions/runtime/background.sqlite` | 后台 job、job events |
| Team | `~/.aicli/sessions/runtime/team_store.sqlite` | teams、teammates、team events、task/wake/mailbox 历史 |
| Schedule | `~/.aicli/data/schedule/schedule.sqlite` | 自动化任务、pending messages、delivery history/intents |

实际路径由 CLI-local `sessionruntime.ResolvePaths` 与 `aiclipaths` 默认值解析；显式配置 `storePath` 时以配置为准。

## 2. 四个维护动作的区别

| 动作 | 作用 | 是否删除数据 | 是否回收文件空间 |
|---|---|---|---|
| 在线保留策略 | 新写入时按会话限制事件/回执/缓存条数和字节 | 会裁剪旧行 | 否，空闲页留在 freelist |
| `storage prune-runtime` | 清理 runtime 库内部的历史回执、缓存明细、旧事件和旧内联工具面 | 是 | 否 |
| `storage prune-sessions` | 按最后活动时间整场删除会话，并跨关联库级联 | 是 | 否 |
| `storage compact` | 独占访问下 `VACUUM` | 否 | **是** |

因此，“删了数据但文件没变小”通常是正常现象：必须再执行 `storage compact`。

## 3. 在线保留默认值

当前 runtime store 默认按会话保留：

- `session_events`：最近 2,048 条
- 单条事件 `payload_json`：最多 256 KiB（超限写 truncation stub）
- `session_tool_receipts`：最近 2,048 条 / 最多 8 MiB
- `cache_requests`：最近 4,096 条
- `agent_control_mailbox_records`：最近 2,048 条

这些限制只在新写入路径触发，不会自动回溯清理历史行。历史数据请使用下面的离线命令。

## 4. 安全前置

建议顺序：

1. 先备份要处理的数据库及其 `-wal` / `-shm` 文件。
2. 退出所有使用这些库的 aicli / runtime-server / scheduled 进程。
3. 先跑 dry-run，检查候选数、保护数和各表行数。
4. 确认后加 `--apply`。
5. 最后跑 `storage compact --target all --require-exclusive`。

```powershell
$runtime = "$HOME\.aicli\sessions\runtime"
Copy-Item "$HOME\.aicli\sessions\session_history.sqlite" "$HOME\.aicli\sessions\session_history.sqlite.bak"
Copy-Item "$runtime\session_runtime.sqlite" "$runtime\session_runtime.sqlite.bak"
```

> `prune-sessions` 是跨多个 SQLite 文件的级联操作，**不是跨库原子事务**。某个库失败时，已完成的库可能已经删除；命令是幂等的，修复占用/错误后可重新执行。

## 5. `storage prune-runtime`

只处理 `session_runtime.sqlite` 内部的按会话保留，不删除整场会话。

```bash
aicli storage prune-runtime \
  --db "$HOME/.aicli/sessions/runtime/session_runtime.sqlite" \
  --dry-run --json
```

实际执行：

```bash
aicli storage prune-runtime \
  --db "$HOME/.aicli/sessions/runtime/session_runtime.sqlite" \
  --tool-receipt-rows 2048 \
  --tool-receipt-bytes 8388608 \
  --cache-request-rows 4096 \
  --event-rows 2048 \
  --apply
```

参数：

| 参数 | 默认 | 说明 |
|---|---:|---|
| `--tool-receipt-rows` | 2048 | 单会话工具回执条数 |
| `--tool-receipt-bytes` | 8388608 | 单会话工具回执 message_json 总字节 |
| `--cache-request-rows` | 4096 | 单会话缓存请求明细条数 |
| `--event-rows` | 2048 | 单会话事件条数 |
| `--dry-run` | 由 `--apply` 决定 | 显式只统计、不删除；不能与 `--apply` 同时使用 |
| `--apply` | false | 实际执行删除 |
| `--json` | false | JSON 报告 |
| `--require-exclusive` | false | Windows 句柄级独占探测 |

该命令还会：

- 删除与 `UNIQUE(session_id, seq)` 重复的显式事件索引；
- 清理历史内联 `stable_tool_surface_json`，迁移到内容寻址表；
- 不执行 `VACUUM`。

## 6. `storage prune-sessions`

按会话最后活动时间选择候选，并跨库级联删除。

### 6.1 基本命令

```bash
# 默认 dry-run；不会删除
aicli storage prune-sessions --older-than 30d --include-team --json

# 实际删除
aicli storage prune-sessions \
  --older-than 30d \
  --include-team \
  --apply \
  --require-exclusive
```

### 6.2 参数

| 参数 | 默认 | 说明 |
|---|---|---|
| `--older-than` | `30d` | 支持 `30d`、`2w`、`72h`、`90m` |
| `--apply` | false | 缺省只 dry-run |
| `--include-orphans` | true | 包含 history 已不存在、其它库仍有数据的 orphan 会话 |
| `--include-team` | false | 纳入 team store；默认跳过 team |
| `--require-exclusive` | false | 任一库仍被其它进程持有句柄即失败 |
| `--timeout` | 1800 秒 | 总超时 |
| `--json` | false | JSON 报告 |

### 6.3 候选选择

候选分为两类：

1. **history 候选**：`sessions.updated_at < cutoff`，且 `expires_at` 不在未来；
2. **orphan 候选**：history 中没有对应行，但从 runtime、usage analytics、artifacts、agent-control、background、schedule（以及启用时的 team）计算出的最近活动时间仍早于 cutoff。

因此即使历史库已经清理过，runtime/analytics 中残留的孤儿会话仍会被发现。

### 6.4 活跃保护

候选中满足任一条件即跳过：

- runtime 状态为 `running / waiting_approval / waiting_input / rewinding`，且 `updated_at` 在最近 24 小时内；
- `session_actor_leases.expires_at` 未过期；
- background job 状态不是终态；
- agent-control agent 为 `active`，或其父/根会话是候选；
- schedule task 为 `active` / `paused`，或 lease 未过期；
- team 为 `active`（仅 `--include-team` 时检查）；
- `sessions.expires_at` 仍在未来。

注意：陈旧的 `running` 状态不再永久保护。旧库中可能存在崩溃/强退遗留的 running 行；只有最近 24 小时仍有更新，或 lease 未过期，才视为活跃。

### 6.5 删除范围

| 库 | 删除内容 |
|---|---|
| history | `session_messages`、`session_prompt_messages`、`sessions`，以及 `<session-artifacts>/<id>` 目录 |
| runtime | `session_events`、`session_tool_receipts`、`cache_requests`、`session_runtime_state`、`agent_control_mailbox_records`、`session_actor_leases` |
| analytics | `usage_requests`、`usage_sessions`、`usage_turns`、`usage_tool_calls`、`usage_session_turn_keys`、`usage_lsp_requests`、`usage_lsp_first_publish`、`usage_render_fence_drops`、`usage_routes`、`usage_subagents`、legacy 表 |
| artifacts | `artifacts`、`checkpoints`、`checkpoint_files`、`memory_entries`；无引用的 `blobs` 会被删除，仍被其它 checkpoint 引用的 blob 保留 |
| agent-control | `agent_control_agents`、`agent_control_agent_wake_events`、`agent_control_global_mailbox_records` |
| background | `background_jobs`、`background_job_events` |
| schedule | `schedule_tasks`、pending messages、job runs、delivery history/intents |
| team（可选） | teams、teammates、team events/signals、task records、wake/mailbox 历史 |

### 6.6 报告字段

```json
{
  "status": "planned",
  "cutoff": "2026-10-03T22:34:47Z",
  "history_sessions": 143,
  "known_candidates": 0,
  "orphan_candidates": 1485,
  "protected_candidates": 0,
  "candidate_count": 1485,
  "candidate_samples": ["..."],
  "stores": [
    {
      "name": "runtime",
      "status": "planned",
      "tables": [
        {"table": "session_events", "rows": 431403},
        {"table": "session_tool_receipts", "rows": 45021}
      ]
    }
  ]
}
```

- `status=planned` 表示 dry-run；
- `status=pruned` 表示已实际删除；
- `status=partial` 表示至少一个库失败，需修复后重跑；
- `known_candidates` 是 history 命中的会话；
- `orphan_candidates` 是 history 已无对应行的会话；
- `protected_candidates` 是被活跃/lease/job/agent/schedule/team 规则保留的候选；
- 各 `tables[].rows` 是 dry-run 预计删除行数或 apply 实际删除行数。

## 7. `storage compact`

删除完成后，空闲页仍在 freelist 中，需要独占压缩：

```bash
aicli storage compact --target all --require-exclusive
```

只压缩个别库：

```bash
aicli storage compact --target runtime --require-exclusive
aicli storage compact --db "$HOME/.aicli/sessions/runtime/session_runtime.sqlite" --require-exclusive
```

`compact` 不会决定删什么，只执行 `VACUUM`。没有先删除数据时，它不会显著缩小文件。

## 8. 推荐运行手册

```powershell
# 0. 备份（按需）
$runtime = "$HOME\.aicli\sessions\runtime"
Copy-Item "$HOME\.aicli\sessions\session_history.sqlite" "$HOME\.aicli\sessions\session_history.sqlite.bak"
Copy-Item "$runtime\session_runtime.sqlite" "$runtime\session_runtime.sqlite.bak"

# 1. 确认所有 aicli / runtime-server / scheduled 已退出
#    命令不会替你结束其它进程。

# 2. 构建新版本
cd E:\projects\ai\ai-agent-runtime\backend
go build -o ..\dist\aicli.exe .\cmd\aicli

# 3. 预演会话级清理
..\dist\aicli.exe storage prune-sessions `
  --older-than 30d --include-team --json

# 4. 预演 runtime 内部裁剪（可选；用于保留会话内部的旧回执/事件）
..\dist\aicli.exe storage prune-runtime `
  --db "$env:USERPROFILE\.aicli\sessions\runtime\session_runtime.sqlite" `
  --dry-run --json

# 5. 确认后执行
..\dist\aicli.exe storage prune-sessions `
  --older-than 30d --include-team --apply --require-exclusive

..\dist\aicli.exe storage prune-runtime `
  --db "$env:USERPROFILE\.aicli\sessions\runtime\session_runtime.sqlite" `
  --apply --require-exclusive

# 6. 回收文件空间
..\dist\aicli.exe storage compact --target all --require-exclusive
```

建议顺序是 **先 prune-sessions，再 prune-runtime，最后 compact**。两条 prune 命令的删除范围不同，重叠时会按幂等方式处理。

## 9. 常见问题

### 9.1 dry-run 很慢

会扫描 runtime events、tool receipts、analytics、artifacts、agent-control 等库。数 GB 库几分钟属于正常；建议加 `--timeout 1800` 或更高。

### 9.2 为什么 `history_sessions` 有 143，但 `known_candidates=0`

说明当前历史会话都还没有超过阈值；真正可清理的是 runtime/analytics 等库中的 orphan 会话（history 行已被 TTL 清掉）。这是当前版本最常见的形态。

### 9.3 为什么 `protected_candidates=0`，但仍有一些 running 会话

旧的 `running` 状态可能已陈旧：如果 `updated_at` 超过 24 小时且没有未过期 lease，它不再被当作活跃会话保护。真正正在运行的会话应有最近状态更新或 lease。

### 9.4 为什么 team 没被清理

team store 默认跳过；需要显式加 `--include-team`。active team 会自动保护。

### 9.5 删除后磁盘没有变小

正常。`prune-*` 只删行；必须再执行 `storage compact`，且要独占访问。

### 9.6 `require-exclusive` 报占用

仍有 aicli / runtime-server / scheduled 或其它 SQLite 句柄打开目标库。退出这些进程后重试；不要用删除进程以外的方式绕过保护。

### 9.7 想更激进地清理

先用更短阈值 dry-run 对比：

```bash
aicli storage prune-sessions --older-than 7d --include-team --json
aicli storage prune-sessions --older-than 14d --include-team --json
```

确认后再执行 `--apply`。不要直接对生产库跳过 dry-run。

### 9.8 删除是否可恢复

`prune-sessions` / `prune-runtime` 删除的是行，不做回收站。正式执行前必须备份；`compact` 后原页也无法恢复。

## 10. 相关实现与测试

- `backend/cmd/aicli/commands/storage_prune_runtime.go`
- `backend/cmd/aicli/commands/storage_prune_sessions.go`
- `backend/internal/sessionpurge/`
- `backend/internal/chat/runtime_retention.go`
- `backend/internal/chat/session_runtime_store_batch.go`
- `backend/internal/chat/session_runtime_store_cache.go`

测试：

```bash
go test -p 1 ./internal/sessionpurge -count=1
go test -p 1 ./internal/chat -count=1
go test -p 1 ./cmd/aicli/commands -run 'TestStoragePrune|TestParsePruneSessionsAge' -count=1
```

## 11. 相关文档

- [SQLite WAL 损坏根因与离线压缩](../analysis/sqlite-wal-corruption-and-offline-compaction-20260926.md)
- [aicli 会话导出与导入](./session-export-import.md)
- [automation tasks 数据存储设计](../automation-tasks/data-storage-design.md)
