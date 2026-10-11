# 后台作业交互能力实施方案（write_stdin / PTY / 实时输出）

> 状态：待实施（评审已通过设计稿）
> 设计依据：`docs/plan/background-jobs-interactive-terminal-codex-alignment-plan-20261011.md`（已评审修订）
> 关联文档：`docs/background-job-lifecycle.md`、`docs/plan/background-jobs-p0-plan-20260928.md`
> 范围：`backend/internal/background`、`internal/toolbroker`、`internal/toolkit/tools`、`internal/policy`、`internal/api/runtimeapi`、`internal/chat`、`internal/events`、`internal/runtimeobserve`、`internal/toolprotocol`、`internal/pty`（新增）、`frontend` jobs 面板
> 难度：expert（跨系统一致性 + 平台差异 + 权限/审批 + 监督口径 + 前端契约）
> 交付方式：P0（纯增量，可独立上线）→ P1（PTY + 复用）→ P2（对齐与收敛）；每阶段可单独评审、单独回滚。

---

## 0. 前置决策（开工前必须拍板）

| 编号 | 决策项 | 选项 | 默认/建议 |
|---|---|---|---|
| D1 | 交互会话是否引入沙箱 | A 不引入（现状语义 + 会话级授权 + 审计 + 黑名单）／B 对齐 Codex（接沙箱/审批编排，单独立项） | **A**（P0）；B 作为后续独立方案 |
| D2 | Unix PTY 依赖 | `github.com/creack/pty` ／ `github.com/aymanbagabas/go-pty` | 二选一并锁版本，P1 前定 |
| D3 | `MaxInteractiveSessions` 默认值 | 64（对齐 Codex `MAX_UNIFIED_EXEC_PROCESSES`）／按机器资源 | **64** |
| D4 | 审批缓存粒度 | 会话级一次授权（绑定 `job_id` + 会话）／每工具调用 | **会话级**（否则 REPL 不可用） |

> 未拍板前，P0 只实现 D1=A、D4=会话级；D2/D3 属 P1/P0 配置项，可用默认值先行。

---

## 1. 范围与不变量

### 1.1 必须保持的不变量（回归红线）

1. **终态吸收**：任何陈旧写不得覆盖终态（`state_version` CAS + 终态吸收，`store.go`/`manager.go`）。
2. **进程树整树终止**：取消/终态先 `TerminateJobObject`/pgid 整树，再按 pid + identity 校验。
3. **租约与实例归属**：`owner_instance_id`/`lease_expires_at` 语义不变；交互会话不破坏既有接管逻辑。
4. **队列语义**：`queued_at/deadline_at/expired`、`pause/resume/abandon/requeue` 不变。
5. **监督唤醒白名单**：仅终态/巡检到点唤醒；高频 `job_output` 不触发唤醒。
6. **`detach` 在 P0/P1 保持原语义**（收敛放 P2）。

### 1.2 本方案新增面

- 新工具 `write_stdin`（别名 `task_input`）。
- 新作业形态 `interactive=1`（会话），与既有一次性作业并存于 `background_jobs`。
- 新事件 `terminal_opened/terminal_closed/terminal_resized`（+ 复用 `job_output` 增量）。
- 新 REST：`POST .../{id}/input`、`.../{id}/resize`、`POST .../terminals/clean`。
- 新包 `internal/pty`（P1）。
- 新配置：`background.maxInteractiveSessions`、`background.pty.enabled`、`background.stdinApprovalScope`（可选）。

---

## 2. 契约清单（一次性登记 checklist）

> 任一遗漏都会导致「工具不可见 / 审批缺失 / 事件被计 `unknown_events_dropped` / 前端类型漂移」。逐项打勾。

**工具与策略**

- [ ] `internal/toolbroker/broker.go`：`ToolWriteStdin = "write_stdin"` 常量、工具定义（加入 `definitions` 追加列表）、分派 `switch`（broker.go:1522 起同段）新增 `case ToolWriteStdin`、`IsBrokerTool` 名单（broker.go:205）加入。
- [ ] `internal/toolbroker/handle_aliases.go`：`write_stdin` 通过既有 `backgroundJobAliasPrefix`（`job_ref_`）解析 `job_id`；`task_input` 作为工具别名在工具名归一化处登记。
- [ ] `internal/toolbroker/broker_arg_kinds.go`、`broker_arg_audit.go`：登记 `chars/input`、`yield_time_ms`、`close_stdin`。
- [ ] `internal/policy/taxonomy.go`（`knownToolTaxonomy`，第 46-50 行同段）：`"write_stdin"`、`"task_input"` → `{Kind: types.ToolKindControl}`。
- [ ] `internal/policy/capability.go`：新增 `CapWriteStdin`（对齐 `CapBackgroundTask`），映射 `write_stdin`/`task_input`（含 `CapabilityForTool` 分支）。
- [ ] `internal/policy/grants.go`：加入 `shell/bash/aicli_exec/background_task/task_kill/task_monitor/task_control` 同类组（第 180 行同段）。
- [ ] `internal/policy/tool_policy.go`：加入需审批写类组（第 139 行同段）。
- [ ] `internal/toolkit/listable.go`（第 193 行同段）、`internal/agent/doom_loop.go`（第 179 行同段）、`internal/agent/tool_list.go`：可见性 + 非 doom-loop 白名单。

**事件（四处同步）**

- [ ] `internal/chat/events.go`：新增 `EventTerminalOpened/Closed/Resized` 常量。
- [ ] `internal/events/contract.go`（第 216-219 行 `job_*` 同段）：登记三个新类型。
- [ ] `internal/runtimeobserve/known_types.go`（第 156-162 行同段）：登记三个新类型。
- [ ] `cmd/aicli/commands/web_schema.go`（第 178/353 行同段）：SSE 映射 + 分组。

**REST 与类型**

- [ ] `internal/api/runtimeapi/handler.go`（第 1028-1036 行同段）：新增 `/input`、`/resize`、`/terminals/clean` 路由。
- [ ] `internal/api/runtimeapi/background_handlers.go`：新增 handler；`/output` 增 `wait`/`timeout_ms`。
- [ ] `pkg/skillsapi/client.go`（`BackgroundJob*`，第 2401-2449 行）：结构体扩展。
- [ ] `frontend` jobs 面板 TS 类型 + 动作矩阵 + 实时增量渲染。

---

## 3. P0 实施步骤（纯增量，不改既有行为）

> 目标：管道版交互会话 + `write_stdin` + 会话级审批 + 写驱动流式 + 跨实例路由/重启语义。
> 原则：不触碰 detached runner、队列、CAS 终态路径；新增代码优先，改动既有代码仅限"追加分支/字段"。

### 3.1 步骤 1：类型与字段（`internal/background/types.go`）

- `BackgroundTaskArgs` 增 `Interactive bool`、`TTY bool`、`Cols/Rows int`。
- `Job` 增 `Interactive bool`、`TTY bool`、`OSPID *int`、`CPUPercent *float64`、`RSSKB *int64`、`Cols/Rows int`（P0 先落 `Interactive/TTY/Cols/Rows`；CPU/RSS 属 P2）。
- 新增 `TerminalOpenArgs`、`TerminalReadResult{ Output string; Offset int64; WaitCondition string; WaitedMs int64; Exited bool; ExitCode *int }`。
- `TaskOutputResult` 增 `WaitCondition`、`WaitedMs`、`OSPID/CPUPercent/RSSKB`（P2 填值）。

**验证**：`go build ./internal/background/`。

### 3.2 步骤 2：store 加列（`internal/background/store.go`）

- 在 `init()` 的 `PRAGMA table_info` 迁移段追加：`interactive/tty/stdin_closed/os_pid/cpu_percent/rss_kb/cols/rows`（§2.3 设计）。
- `ListJobs`/`GetJob`/`upsertJob` 同步读写新列；`JobFilter` 增 `Interactive *bool`（可选）。
- 迁移幂等：列已存在则跳过。

**验证**：`go test ./internal/background/ -run 'Store|Migration'`。

### 3.3 步骤 3：交互会话原语（新增 `internal/background/interactive.go`）

- `terminalSession`（§3.1 设计）：`cmd`、`stdin io.WriteCloser`、`output *outputBuffer`（复用 `manager.go:3011` 定义与 `Read(offset,limit)`，`manager.go:3053`）、`offset`、`closed`、`exitCode`、`tree`。
- `Manager.OpenTerminal(ctx, sessionID, args)`：
  1. 校验参数、登记 job（`Kind="terminal"`, `Interactive=true`）；
  2. `buildShellCommand`（`manager.go:2958`）建 `exec.Cmd`；`cmd.Dir = cwd`；`applyProcessGroup(cmd)`；
  3. `cmd.StdinPipe()` + `cmd.Stdout/Stderr` 写 `outputBuffer`（复用 `newJobOutputWriter` 的日志镜像，`manager.go:1178`）；
  4. `cmd.Start()` → `attachProcessTree(pid, true)`（`manager.go:1193` 同款）→ `acceptStartedProcess`（`manager.go:1204`）；
  5. 起 reader goroutine（写驱动，§3.5）；**验收通过后**返回 `running` 快照。
- `Manager.WriteStdin(ctx, sessionID, input, yieldMs)`：校验 `running && interactive` → 写 stdin → 按 clamp 后的 `yieldMs` 等新输出/退出 → 返回 `TerminalReadResult`。
- `Manager.CloseTerminal(ctx, sessionID)`：关 stdin → 等退出（超时整树终止）→ 走既有 CAS 终态写入。
- Manager 增内存 `interactive map[string]*terminalSession`（`mu` 保护）；`Close()` 中随既有取消路径收敛（`manager.go:273-311` 同段）。
- **不占调度槽**：`interactive` job 不进 `dispatchPending`；`MaxInteractiveSessions` 独立限流。

**验证**：`go test ./internal/background/ -run Interactive`。

### 3.4 步骤 4：启动验收与竞态

- `OpenTerminal` 复用 `acceptStartedProcess`（`manager.go:1204`）：验收失败 → `failJobWithErrorCode`；验收超时 → 整树终止。
- 返回前确保状态为 `running`，消除 `background_task interactive=true` 后立即 `write_stdin` 的竞态。

### 3.5 步骤 5：写驱动流式（复用 `toolprotocol`）

- reader goroutine 读到字节 → `outputBuffer.Write` → 经 `internal/toolprotocol/terminal_stream.go` 的 `NewTerminalStreamWriter` 分块 → 复用 `job_output` 事件推送 EventBus。
- **不改** `emitLogOutput` 的 tick 路径（一次性作业维持现状）；交互会话走新写驱动路径。
- 背压：`outputBuffer` 上限 + `NewTerminalStreamWriter.MaxEvents`（默认 64）+ 前端节流。

**验证**：`go test ./internal/toolprotocol/`（既有）+ 新增 `internal/background` 流式用例。

### 3.6 步骤 6：`write_stdin` 工具 + 策略登记

- `broker.go`：常量、工具定义（参数 `job_id|session_id|task_id`、`chars|input`、`yield_time_ms`、`close_stdin`）、分派 `case`、`IsBrokerTool`。
- handler：解析别名（`handle_aliases.go` 的 `backgroundJobAliasPrefix`）→ 调 `Manager.WriteStdin` → 结果走 `toolresult`（`not_interactive`/`approval_required` 为内容结果 + `next_action`）。
- 按 §2 完成策略/可见性/参数审计登记。

**验证**：`go test ./internal/toolbroker/ -run 'WriteStdin|Alias|Arg'`、`go test ./internal/policy/`。

### 3.7 步骤 7：会话级审批缓存（D4）

- 首次 `write_stdin` 走既有审批路径；授权后按 `job_id`+会话记录 grant，后续写不再逐次审批。
- 未授权返回 `approval_required`（内容结果 + `next_action`）。
- 复用 `internal/policy/grants.go` 的 grant 机制；`close_stdin`/会话结束清理 grant。

**验证**：新增审批缓存用例（首次审批、后续免审批、会话结束失效）。

### 3.8 步骤 8：跨实例路由与重启语义

- `WriteStdin` 前置检查 `owner_instance_id`（§3.11 设计）：
  - 本实例 → 写；活跃 peer → `not_writable_remote`；缺失/过期 → 既有收敛语义。
- `recoverPersistedJobs`：`interactive=1 && running` → CAS `orphaned` + `interactive_not_recoverable` 事件，不重排。

**验证**：跨实例用例 + 重启用例。

### 3.9 步骤 9：事件与 REST（P0 最小面）

- 事件四处登记（§2）。
- REST：`POST .../{id}/input`（写 stdin）；`/output` 增 `wait`/`timeout_ms`（复用 `broker_task_wait.go` 的边界 1s–120s/默认 30s）。
- `/resize`、`/terminals/clean` 属 P1/P2。

### 3.10 P0 验收命令

```
cd backend
go build ./...
go vet ./...
go test ./internal/background/... ./internal/toolbroker/... ./internal/policy/... \
        ./internal/api/runtimeapi/... ./internal/events/... ./internal/runtimeobserve/... ./internal/toolprotocol/...
```

---

## 4. P1 实施步骤（PTY + 进程复用）

### 4.1 步骤 1：新增 `internal/pty` 包

- `pty_windows.go`：从 `backend/cmd/conpty-probe/main.go` 提取 ConPTY 调用（`CreatePseudoConsole`/`ResizePseudoConsole`/`ClosePseudoConsole` + `STARTUPINFOEX` 属性列表），复用 `golang.org/x/sys/windows`；`conpty-probe` 改为调用该包（或保留为 smoke 工具）。
- `pty_unix.go`：按 D2 选定库打开 `ptmx`，`Setsid + Setctty`；`Resize` 用 `TIOCSWINSZ`。
- `pty_other.go`：`//go:build` 降级，返回 `ErrUnsupported`。
- 能力探测：`Supported() bool`（Windows 检查 `CreatePseudoConsole` 是否可用；Unix 恒 true）；进程内缓存。

**验证**：`go test ./internal/pty/`（含 Win7 降级分支）。

### 4.2 步骤 2：接入 Manager（TTY 路径）

- `OpenTerminal` 当 `args.TTY && pty.Supported()`：用 PTY 句柄替换 stdin/stdout 管道；否则降级管道并回填 `Metadata["tty_fallback"]="pipe"`。
- 配置 `background.pty.enabled`（默认 true，探测失败自动关）。
- `Manager.Resize(sessionID, cols, rows)`：PTY 调用 `pty.Resize`；无 PTY 返回 `unsupported`。

### 4.3 步骤 3：进程复用与 `interactive=true`

- `session_id` = 复用键；同 `session_id` 二次 `OpenTerminal` 返回已有 `job_id` + `already_open`。
- `background_task` 工具定义增 `interactive`/`tty` 参数；`interactive=true` 走 `OpenTerminal`（`broker.go:1522` 的 `case ToolBackgroundTask` 分支内分流）。
- 会话作用域：纳入 `updateSessionActiveJobs`（`handler.go:5810` 同段）的 `ActiveJobIDs`；会话关闭按既有清理路径收敛。

### 4.4 步骤 4：沙箱决策落地

- 若 D1=A：文档 + doctor 明确「交互会话无沙箱」，依赖会话级授权 + 审计 + 黑名单。
- 若 D1=B：单独立项，接入沙箱/审批编排（不在本阶段混入）。

### 4.5 P1 验收命令

```
cd backend
go build ./...
go test ./internal/pty/... ./internal/background/...
```
并跑平台矩阵（§7）。

---

## 5. P2 实施步骤（对齐与收敛）

### 5.1 步骤 1：资源指标

- 扩展 `internal/background/process_windows.go`：复用已有 `GetProcessTimes`（`process_windows.go:36-40`）算 CPU%；新增 `GetProcessMemoryInfo` 取 RSS。
- `process_linux.go`/`process_unix.go`：`/proc/<pid>/statm` 或 `ps` 兜底。
- 采样跟随 `MonitorInterval` 或独立 `MetricsInterval`；低频落库；暴露到 `TaskOutputResult` 与 REST。

### 5.2 步骤 2：REST 对齐

- `GET .../{id}` 增 `os_pid/cpu_percent/rss_kb/cols/rows`。
- 新增 `POST .../{id}/resize`、`POST .../terminals/clean`。
- `pkg/skillsapi/client.go` 与 `frontend` TS 同步。

### 5.3 步骤 3：`detach` 收敛（破坏性）

- `internal/toolkit/tools/bash.go` 的 `detach=true` 改为创建受管会话（`OpenTerminal`）并返回 `job_id`。
- 过渡：结果附 `next_action` 指引改用 `background_task`；CHANGELOG 标注；`AICLI_SHELL_ALLOW_DETACH=0` 保留。
- **独立提交、独立回滚**。

### 5.4 步骤 4：监督与前端

- `internal/supervision/background_job.go`：`terminal_opened/closed/resized` 归 `BackgroundJobFamilyMonitor`（不触发终态唤醒）。
- `frontend` jobs 面板：实时增量渲染 + 新字段/动作。

### 5.5 P2 验收命令

```
cd backend
go build ./... && go vet ./...
go test ./internal/background/... ./internal/api/runtimeapi/... ./internal/supervision/... ./internal/toolkit/...
cd ../frontend && npx tsc -b && npm run test && npm run lint:i18n
```

---

## 6. 数据迁移

- 加列迁移幂等（`PRAGMA table_info` 检查后 `ALTER`），旧库无需手工操作，`state_version` 起始不变。
- 存量 job 默认 `interactive=0`，行为不变。
- **升级窗口**：多实例共享 store 时，建议升级后一次性重启所有实例（老进程不识别新列，但不会破坏既有列）。
- 回滚：代码 revert；新列残留不影响旧版本（旧版本 SELECT 显式列，忽略未知列）。

---

## 7. 测试矩阵

| 平台 | 分支 | 必测 |
|---|---|---|
| Win7（无 ConPTY、可能无 PowerShell） | 管道降级 | open→write→read→close；能力声明；不 panic |
| Windows 10 1809+ | ConPTY | TTY 语义、resize、open→write→read→close |
| Unix | ptmx | 同上 |
| 通用 | — | 审批缓存；跨实例 `not_writable_remote`；重启 orphaned；不占 `MaxConcurrentJobs`；`not_interactive`；`approval_required`；offset 续读不丢/不重 |

**门禁**：`go build ./...`、`go vet ./...`、§3.10/§4.5/§5.5 测试集；前端 `tsc -b` + 测试 + `lint:i18n`。

---

## 8. 回滚

| 阶段 | 回滚方式 |
|---|---|
| P0 | 代码 revert；新增列/工具/事件均为 additive，旧客户端不受影响；`write_stdin` 可下线（模型不再可见） |
| P1 | `background.pty.enabled=false` 强制管道回退；`internal/pty` 依赖可移除 |
| P2 | `detach` 收敛为独立提交，单独 revert；资源指标/前端为 additive |

---

## 9. 实施顺序总表（可勾选）

**P0**

- [ ] 步骤 1 类型/字段（`types.go`）
- [ ] 步骤 2 store 加列（`store.go`）
- [ ] 步骤 3 会话原语（`interactive.go`）
- [ ] 步骤 4 启动验收/竞态
- [ ] 步骤 5 写驱动流式（复用 `toolprotocol`）
- [ ] 步骤 6 `write_stdin` 工具 + 策略登记（§2 checklist）
- [ ] 步骤 7 会话级审批缓存
- [ ] 步骤 8 跨实例路由 + 重启语义
- [ ] 步骤 9 事件 + REST（`/input`、`/output` wait）
- [ ] P0 测试与验收命令

**P1**

- [ ] `internal/pty` 包（ConPTY 提取 + Unix ptmx + 探测）
- [ ] Manager TTY 路径 + `Resize`
- [ ] `session_id` 复用 + `background_task interactive=true`
- [ ] 沙箱决策落地
- [ ] 平台矩阵测试

**P2**

- [ ] 资源指标（CPU/RSS）
- [ ] REST 字段/端点 + 客户端/前端类型
- [ ] `detach` 收敛（独立提交）
- [ ] supervision + 前端实时增量
- [ ] P2 测试与验收

---

## 附 A：文件—改动—测试映射

| 文件 | 改动 | 对应测试 |
|---|---|---|
| `internal/background/types.go` | 新增字段/`TerminalOpenArgs`/`TerminalReadResult` | 编译 + store 往返 |
| `internal/background/store.go` | 加列迁移 + 读写新列 | `store_test` |
| `internal/background/interactive.go`（新增） | 会话原语 | `interactive_test` |
| `internal/background/manager.go` | 接入交互路径、`Close`、恢复语义 | `manager_test`、重启用例 |
| `internal/background/process_*.go` | CPU/RSS 采样（P2） | `process_*_test` |
| `internal/pty/*`（新增，P1） | ConPTY/ptmx + 探测 | `pty_*_test` |
| `internal/toolbroker/broker.go` | `write_stdin` 定义/分派 | `broker_*_test` |
| `internal/toolbroker/handle_aliases.go` | job 别名解析 | `broker_handle_alias_test` |
| `internal/toolbroker/broker_arg_kinds.go`/`broker_arg_audit.go` | 参数登记 | `broker_arg_*_test` |
| `internal/policy/{taxonomy,capability,grants,tool_policy}.go` | 策略登记 | `policy/*_test` |
| `internal/toolkit/listable.go`、`internal/agent/{doom_loop,tool_list}.go` | 可见性/防抖 | `listable_test`、`doom_loop_test` |
| `internal/toolprotocol/terminal_stream.go` | 复用（可能加 helper） | `terminal_stream_*_test` |
| `internal/chat/events.go`、`internal/events/contract.go`、`internal/runtimeobserve/known_types.go`、`cmd/aicli/commands/web_schema.go` | 事件四处登记 | `contract`/`known_types`/`web_schema` 测试 |
| `internal/api/runtimeapi/handler.go`、`background_handlers.go` | 路由 + handler | `background_handlers_test` |
| `internal/supervision/background_job.go` | terminal 事件归类 | `background_job_test` |
| `pkg/skillsapi/client.go`、`frontend/.../jobs*` | 类型 + 面板 | 前端测试 |

## 附 B：设计稿锚点

- 设计稿：`docs/plan/background-jobs-interactive-terminal-codex-alignment-plan-20261011.md`（§3.1 原语 / §3.2 工具 / §3.3 流式 / §3.5 PTY / §3.8 审批 / §3.11 路由与重启 / §3.12 工具边界）。
- 既有实施记录：`docs/plan/background-jobs-p0-plan-20260928.md`、`docs/background-job-lifecycle.md`。

---

## 实施记录（2026-10-11，P0 核心已落地）

> 范围：P0「纯增量」核心。以下均已 `go build ./...`、`go vet ./...` 通过，且相关包测试全绿。

### 已实现

- **类型**（`internal/background/types.go`）：`BackgroundTaskArgs`/`Job`/`TaskOutputResult` 增 `interactive/tty/cols/rows`（`TaskOutputResult` 另预留 `wait_condition/waited_ms/os_pid/cpu_percent/rss_kb`）；新增 `TerminalOpenArgs`、`TerminalReadResult`。
- **存储**（`internal/background/store.go`）：`background_jobs` 幂等加列 `interactive/tty/cols/rows`；`upsertJob`/`GetJob`/`ListJobs` 同步读写。
- **交互原语**（`internal/background/interactive.go`，新增）：
  - `OpenTerminal/WriteStdin/CloseTerminal/IsInteractive` + `interactiveSession`；
  - 写驱动复用 `newJobOutputWriter`（填充 `outputBuffer` + 发 `output` 事件 + 日志镜像），未重造分块；
  - yield 边界 `250..30000`、空输入下限 `5000`（对齐 Codex）；`WriteStdin` 不接受调用方 offset，只回传 `next_offset`；
  - 启动验收（`acceptStartedProcess`）通过后才返回，消除 `interactive=true` 后立即 `write_stdin` 的竞态；
  - 进程退出经既有 CAS 终态写入；`CloseTerminal` 优雅关 stdin，超时改走 `CancelJob`（CAS + 整树终止）；
  - 句柄仅内存，退出后 60s 宽限再回收。
- **Manager**（`internal/background/manager.go`）：`Config.MaxInteractiveSessions`（默认 64）；内存注册表 `interactive`；`Close` 清理句柄；`recoverPersistedJobs` 对 `interactive && running` → `orphaned` + `interactive_not_recoverable`（不重排）；交互会话**不占用** `MaxConcurrentJobs`。
- **工具面**（`internal/toolbroker/`）：新工具 `write_stdin`（别名 `task_input`）定义/分派/`IsBrokerTool`/`normalizeToolName`；`background_task` 增 `interactive/tty/cols/rows` 并分流 `OpenTerminal`；`chars/input` **原样读取（不 trim，保留换行）**；非交互作业返回 `NOT_INTERACTIVE` 内容结果 + `next_action`；`broker_arg_kinds.go`/`broker_arg_audit.go` 登记。
- **策略**（`internal/policy/`）：`taxonomy.go`/`capability.go`/`grants.go`/`tool_policy.go` 登记 `write_stdin`/`task_input`。
- **事件契约（四处）**：`internal/chat/events.go`、`internal/events/contract.go`、`internal/runtimeobserve/known_types.go`、`cmd/aicli/commands/web_schema.go` 登记 `job_terminal_opened/closed/resized`、`job_interactive_not_recoverable`。
- **可见性/防抖**：`internal/toolkit/listable.go`、`internal/agent/doom_loop.go` 白名单。
- **REST**：`POST /api/runtime/background/jobs/{id}/input`（`internal/api/runtimeapi/{handler.go,background_handlers.go}`）。
- **测试**：`internal/background/interactive_test.go`（往返 / 非交互 / 上限 / 重启 orphan）；`internal/toolbroker/broker_write_stdin_test.go`（broker 往返 / 非交互内容结果）。

### 与设计稿的偏差（已记录）

- **能力口径**：未新增 `CapWriteStdin`，`write_stdin`/`task_input` 复用 `CapBackgroundTask`（与 `task_kill`/`task_monitor`/`task_control` 同组）。理由：能力作用域配置无需新增项即可生效；语义上「驱动一个后台会话」与「启动它」同级。若需更细粒度，可在 P1 引入 `CapWriteStdin` 并同步所有作用域配置。

### 未纳入本次 P0（按计划留给 P1/P2）

- **会话级审批缓存（D4）**：本次 `write_stdin` 复用既有审批/策略路径（`CapBackgroundTask` 组 + read-only 阻断）；一次性授权缓存待与审批面接线后单独实现。
- **跨实例 owner 路由**：`WriteStdin` 在会话不属于本实例（句柄不存在）时返回可操作错误「not available」；显式 owner 查询/路由（`not_writable_remote`）待 P1。
- **PTY**：`internal/pty`（ConPTY 提取 / Unix ptmx）、`resize`、TTY 能力探测/降级 → P1；`tty=true` 目前落管道（未报错）。
- **资源指标**：`os_pid/cpu_percent/rss_kb` 字段已预留但未采样填充 → P2。
- **REST/前端**：`/resize`、`/terminals/clean`、前端实时增量渲染、`pkg/skillsapi` 类型扩展 → P1/P2。
- **`detach` 收敛** → P2（破坏性，独立提交）。

### 验证命令与结果

```
cd backend
go build ./...     # 通过
go vet ./...       # 通过
go test ./internal/background/ ./internal/policy/ ./internal/events/ ./internal/runtimeobserve/   # ok
go test ./internal/toolbroker/                                                                    # ok
go test ./internal/api/runtimeapi/ -run Background                                                # ok
go test ./internal/agent/ -run 'ToolSurface|ReadOnly|Allowlist|DoomLoop|ToolList'                # ok
go test ./cmd/aicli/commands/ -run 'Schema|WebEvent'                                              # ok
```

---

## P1 实施记录（2026-10-11）

### 已实现

- **会话复用（`session_id`）**：`OpenTerminal` 对同一 session 的**活跃**会话返回既有 job（`Metadata.already_open=true`），不重复起进程；Manager 增 `interactiveBySession` 注册表，退出/关闭时清理。
- **跨实例 owner 路由**：新增 sentinel `ErrNotInteractive` / `ErrSessionNotAvailable` / `ErrSessionNotLocal`；`WriteStdin` 在会话不属于本实例时返回 `ErrSessionNotLocal`；broker 与 REST 用 `errors.Is` 映射为 `NOT_INTERACTIVE` / `SESSION_NOT_AVAILABLE` / `SESSION_NOT_LOCAL` 内容结果（附可操作 `next_action`）。
- **Resize**：`Manager.Resize` + `POST /api/runtime/background/jobs/{id}/resize`；无 PTY 返回 `ErrTTYUnsupported`（HTTP 409），不静默忽略尺寸。
- **`internal/pty` 包**：`Supported()` + `Start(executable, args, opts...)` + `Read/Write/Resize/Wait/Close/Pid`；Linux 走 `/dev/ptmx`（`golang.org/x/sys/unix`，无新增第三方依赖）；其余平台为 stub。
- **TTY 接线**：`OpenTerminal` 在 `TTY && pty.Supported()` 时走 PTY 路径（reader→同一 output writer、`attachProcessTree`、`acceptStartedProcess`、`waitInteractivePTY`）；否则落管道并记 `Metadata.tty_fallback="pipe"`、`Job.TTY=false`（**不谎报 TTY**）。

### 重要发现 / 阻塞（Windows ConPTY）

- 从 `backend/cmd/conpty-probe/main.go` 逐字提取的 ConPTY 实现，在 `internal/pty` 测试中以 **`STATUS_HEAP_CORRUPTION (0xC0000374)`** 崩溃；同一参考探针自身的 read 循环也不返回（会挂起）。判断为 `STARTUPINFOEX` / attribute-list 的 unsafe 处理不可靠。
- 因此 **Windows 上 `pty.Supported()` 返回 false**，`pty_windows.go` 改为文档化 stub；Windows 交互会话一律走已验证的管道路径。ConPTY 需重写并验证后再启用（P1 后续 / 单独立项）。

### 验证

- `go build ./...`、`go vet ./...` 通过。
- Windows：`go test ./internal/background/ ./internal/pty/ ./internal/toolbroker/`、`runtimeapi -run Background` 全绿；`internal/pty` 在 Windows `SKIP`（不支持）。
- Linux：`GOOS=linux go build ./internal/background/ ./internal/pty/ ./internal/toolbroker/` 通过（**仅编译验证**；ptmx 运行期未验证）。
- 新增用例：会话复用、sentinel 错误、TTY 回退、Resize-unsupported。

### 仍待办（P2）

- **ConPTY 重写与验证**（Windows TTY 真正可用）。
- 会话级审批缓存（一次性授权）。
- 资源指标填充（`os_pid/cpu_percent/rss_kb`）、`/terminals/clean`、前端实时增量渲染、`pkg/skillsapi` 类型扩展。
- `detach` 收敛（破坏性，独立提交）。

---

## P2 实施记录（2026-10-11，会话级审批缓存 D4）

### 已实现

- **新增 `job:` grant pattern**（`internal/policy/grant_pattern.go`）：`GrantPatternJob = "job:"`，把一条记住的授权绑定到单个后台作业/会话。
  - `DeriveGrantPattern("write_stdin"|"task_input", args)` → `job:<job_id|task_id|session_id>`；无 id 时返回空。
  - `grantPatternMatches` 新增 `job:` 分支，仅当调用 args 的 job_id 与授权完全一致时命中。
  - 新增 `IsJobScopedGrantTool` / `grantJobID` 辅助函数。
- **引擎授权口径**（`internal/policy/engine.go`）：新增 `grantEligible(toolName) = !IsDangerousTool || IsJobScopedGrantTool`，替换授权查询（step 4）、审批建议模式、remember 写入三处的 `!IsDangerousTool` 判断。危险工具仍不可记住，**唯一例外**是 `write_stdin` 的 job 级授权。
- **存储守卫放宽**（`internal/policy/grants.go` / `file_grants.go`）：`grantRememberAllowed(grant)` —— 危险工具仍拒绝，除非是 job-scoped 工具且 pattern 以 `job:` 开头。因此**工具级**（空 pattern）`write_stdin` 授权仍被拒，杜绝"一次授权放开所有会话"。
- **语义闭环**：首次 `write_stdin` 触发审批；宿主以 `RememberScope=session` 允许后，引擎按 `job:<id>` 存入会话级 `MemoryGrantStore`；同一 job 的后续写入命中 `StageGrants` 自动放行、不再弹审批；**不同 job 或会话结束**（会话级 store 生命周期）后重新审批。

### 安全边界

- `IsDangerousTool` **未改动**：`write_stdin`/`task_input` 仍是危险工具，因此通用「始终允许」API（harness grants）与项目级持久授权**不能**创建工具级 write_stdin 授权。
- 仅 job 级 pattern 可写入；`exact:`/`cmd:` 等其它 pattern 对 write_stdin 不匹配，等同无效授权。

### 验证

- `internal/policy/write_stdin_grant_test.go`：
  - `DeriveGrantPattern` 对 write_stdin/task_input 产出 `job:<id>`；
  - `job:` pattern 仅命中同一 job；
  - 工具级 write_stdin 授权被 `errDangerousGrant` 拒绝、job 级被接受；
  - 引擎级：首次写→审批 1 次；同 job 再写→`StageGrants` 命中、审批仍为 1 次；不同 job→审批增至 2 次。
- `go build ./...` 通过；`go vet ./internal/{policy,background,toolbroker,pty,api/runtimeapi,events,runtimeobserve,chat}` 通过。
- 回归：`go test ./internal/policy/ ./internal/background/ ./internal/toolbroker/` 与 `runtimeapi -run 'Background|Approval'` 全绿。

> 说明：`go vet ./...` 期间 `internal/gateway/push/`（**未跟踪目录**，非本方案改动）曾短暂报 `undefined: newGoneServer`，系并发写入中途状态；重跑该包 vet 已通过，与本方案无关。

### 仍待办（P2 剩余）

- ConPTY 重写与验证（Windows TTY）。
- 资源指标填充（`os_pid/cpu_percent/rss_kb`）、`/terminals/clean`、前端实时增量渲染、`pkg/skillsapi` 类型扩展。
- `detach` 收敛（破坏性，独立提交）。

---

## P2 实施记录（2026-10-11，terminals/clean + 资源指标 + 客户端类型）

### 已实现

- **`/terminals/clean`**：
  - `Manager.CleanTerminals(ctx, sessionID)` 关闭活跃交互会话（空 sessionID = 全部；非空 = 仅该会话），返回关闭数。
  - REST `POST /api/runtime/background/terminals/clean?session_id=<id>` → `{ "closed": N }`。
- **资源指标（对齐 Codex `ThreadBackgroundTerminal`）**：
  - 新增平台采样 `sampleProcessUsage(pid) (rss_kb, cpu_seconds, ok)`：Windows（`GetProcessTimes` + `psapi.GetProcessMemoryInfo`）、Linux（`/proc/<pid>/statm` + `/proc/<pid>/stat`）、其它平台 stub。
  - `Manager.processMetrics` 用「上次样本 vs 本次样本」的 CPU 时间差推导 `cpu_percent`；`attachProcessMetrics` / `attachTerminalMetrics` 填充 `os_pid`。
  - 暴露到 `TaskOutputResult`（`task_output`）与 `TerminalReadResult`（`write_stdin`）：`os_pid` / `cpu_percent` / `rss_kb`。
- **客户端类型与方法**（`pkg/skillsapi/client.go`，additive）：
  - `BackgroundJob` 增 `interactive/tty/cols/rows`；`BackgroundJobOutput` 增 `wait_condition/waited_ms/os_pid/cpu_percent/rss_kb`。
  - 新增 `BackgroundJobInputRequest/Response`、`BackgroundJobExchange`、`BackgroundJobResizeRequest/Response`、`BackgroundTerminalsCleanResponse`。
  - 新增方法 `WriteBackgroundJobInput` / `ResizeBackgroundJob` / `CleanBackgroundTerminals`。

### 验证

- 新增用例（`internal/background/interactive_test.go`）：
  - `TestWriteStdinReportsProcessMetrics`：首次交换含 `os_pid`/`rss_kb`；第二次交换含 `cpu_percent`（有前次样本）。
  - `TestCleanTerminals`：全量清理 = 2；按 session 清理 = 1；不匹配 session = 0。
- `go build ./internal/... ./pkg/...` 通过；`go vet ./internal/background/ ./internal/api/runtimeapi/ ./pkg/skillsapi/ ./internal/policy/` 通过。
- 回归：`go test ./internal/background/ ./internal/policy/ ./internal/toolbroker/` 与 `runtimeapi -run Background` 全绿。
- Linux 交叉编译：`GOOS=linux go build ./internal/background/` 通过（采样路径仅编译验证）。

### 并发写入导致的非本方案构建噪音（记录）

- `go build ./...` 期间 `cmd/aicli/commands` 报 `chatSessionID redeclared`（`chat_gateway_attention.go` 为**未跟踪**的并发在写文件）；`go vet ./...` 期间 `internal/gateway/push`（未跟踪）曾报 `undefined: newGoneServer`。二者均非本方案改动，属并发写入中途状态。本方案涉及的所有包 `go build`/`go vet` 均通过。

### 仍待办（P2 剩余）

- ConPTY 重写与验证（Windows TTY）。
- 前端实时增量渲染（TUI/web jobs 面板）、`detach` 收敛（破坏性，独立提交）。

---

## P2 实施记录（2026-10-11，前端交互会话 API/类型/事件契约）

### 已实现（additive，前端）

- **事件契约**（`frontend/src/types/runtime/event-contract.ts`）：登记 `job_interactive_not_recoverable`、`job_terminal_opened/closed/resized`（union + channel map）。
- **类型**（`frontend/src/types/runtime/jobs.ts`）：
  - `RuntimeJob` 增 `interactive?/tty?/cols?/rows?`（**可选**，兼容旧后端/测试夹具）。
  - `RuntimeJobOutput` 增 `waitCondition/waitedMs/osPid/cpuPercent/rssKb`。
  - 新增 `RuntimeJobInputRequest`、`RuntimeJobExchange`、`RuntimeJobInputResponse`、`RuntimeJobResizeRequest/Response`、`RuntimeTerminalsCleanResponse`。
- **REST 客户端**（`frontend/src/api/runtime/jobs.ts`）：`normalizeRuntimeJob`/`normalizeRuntimeJobOutput` 补字段；新增 `normalizeRuntimeJobExchange`、`writeRuntimeJobInput`、`resizeRuntimeJob`、`cleanRuntimeTerminals`。
- **Hook**（`frontend/src/hooks/workspace/use-background-jobs.ts`）：控制器新增 `writeInput` / `resizeTerminal` / `cleanTerminals`（成功后触发刷新、失败写入 `error`）。
- **测试夹具**：`jobs-panel.test.tsx` 控制器 mock 补 3 个新方法；`jobs.test.ts` 期望对象补新字段。

### 验证

- `npx tsc -b`：jobs 相关文件**无错误**；仅剩两处**与本方案无关**的既有错误（`src/api/runtime/lsp.test.ts` 未使用变量、`src/pages/usage-analytics/lsp-observability-panel.tsx` i18n 类型）——二者 `git status` 无改动，非本方案引入。
- `npx vitest run`（jobs 相关 5 个文件）：**55 passed**。

### 明确未做（前端）

- **逐任务实时输出流渲染**：面板当前仍为「事件触发刷新 + live 轮询」（`use-background-jobs.ts` 既有语义），本次只补齐了交互会话的**动作 API 与类型/契约**，未实现把 `job_output` 增量直接 append 到面板的流式视图。该视图需要把 runtime 事件 payload 接入 hook（当前 hook 只收到 `lastRuntimeEventType`/`runtimeEventCount`，不接收 payload），属独立改动，建议单列一项。
- 面板未新增「向交互会话发送输入」的 UI 控件（仅暴露了控制器方法，供后续接线）。

### 仍待办

- 逐任务实时输出流视图（需事件 payload 贯通 hook）。
- ConPTY 重写与验证（Windows TTY）。
- `detach` 收敛（破坏性，独立提交）。

---

## P2 实施记录（2026-10-11，`detach` 收敛 — 过渡期）

### 已实现（非破坏性过渡）

- **`bash` 工具 `detach` 参数描述**（`internal/toolkit/tools/bash.go`）：标注 **已弃用**，指引改用 `background_task`（交互会话用 `interactive=true` + `write_stdin`）。
- **detach 结果**：`Content` 追加弃用提示；`Metadata` 增 `deprecated=true`、`deprecated_reason`；`next_action` 改为指引 `background_task`（保留 pid 显式管理提示）。
- 保留 `AICLI_SHELL_ALLOW_DETACH=0` 全局禁用语义与 `detach` 现有行为（未改变启动路径，避免破坏依赖 fire-and-forget 的调用方）。

### 验证

- 新增 `TestBashToolDetachIsMarkedDeprecatedWithGuidance`：断言 `deprecated=true`、`next_action` 含 `background_task`、`Content` 含 `background_task`。
- `go test ./internal/toolkit/... -count=1` 全绿（含既有 5 个 detach 用例 + 新增 1 个）。

### 明确未做（需架构 seam，单独立项）

- **把 `detach` 真正路由到受管作业**：`bash` 工具（`internal/toolkit/tools`）无法访问 toolbroker 的 `Background` manager（两者分属不同层）。要做到「detach → 受管会话/job 并捕获输出」，需要在 toolkit 层引入一个 background-submitter 依赖并在 broker 接线，属独立改动；本次只做弃用与指引（设计稿 §3.7「过渡期」）。
- 因此 `detach` 仍为 fire-and-forget（输出不捕获、无监督），仅通过工具描述 + 结果元数据引导模型改用受管路径。

### CHANGELOG

- 仓库无通用 CHANGELOG（仅 `backend/dist/changelog_*.md` 与 `docs/knowledge_Layer/CHANGELOG.md`，均非本项目变更日志）。弃用说明记录于工具描述、结果元数据与本方案文档；如需发布日志，建议由维护者汇总。

### 仍待办

- 逐任务实时输出流视图（需 runtime 事件 payload 贯通 jobs hook）。
- ConPTY 重写与验证（Windows TTY）。
- `detach` → 受管作业的真正路由（需 toolkit↔broker 的 background-submitter seam）。

---

## P2 实施记录（2026-10-11，逐任务实时输出流视图）

### 已实现（前端，事件驱动增量跟随）

- **新 store**（`frontend/src/lib/job-output-stream/store.ts`）：进程内订阅表，`ingestJobOutputEvent(event)` 仅接受 `type === "job_output"` 的事件，按 `next_offset` 去重（不前进即丢弃），并广播「有新数据」信号；提供 `subscribeJobOutput` / `subscribeAnyJobOutput` / `jobOutputSignal` / `resetJobOutputStreams`。
  - **设计取舍**：store 只广播信号与最新 offset，**不拼接文本**；字节仍由权威 REST `/background/jobs/{id}/output` 按 offset 读取，避免与分页游标出现双份拼接。
- **事件贯通**（`frontend/src/pages/workspace-page.tsx`）：把既有 SSE 回调 `applyPendingInteractionEvent` 与 `ingestJobOutputEvent` 组合为 `handleRuntimeEvent`（`useCallback` 记忆化），同时接入 `useWorkspaceLive` 与 `useWorkspaceMultiSessionRuntime` 两处 `onRuntimeEvent`——即 runtime 事件 payload 现已贯通到 jobs 数据面。
- **面板跟随**（`frontend/src/components/workspace/jobs-panel.tsx`）：展开的 **live** 任务订阅 `job_output` 信号，去抖 250ms 后按当前 `nextOffset` 调用 `loadOutput` 续读（复用既有 REST 分页与渲染）。事件驱动替代纯轮询；终态任务不订阅。

### 验证

- 新增单测：`frontend/src/lib/job-output-stream/store.test.ts`（仅 job_output 广播、去重、跨任务隔离、退订、reset）。
- 新增集成测试：`jobs-panel.test.tsx`「展开的 live 任务跟随 job_output 事件增量（事件驱动）」——注入 `job_output` 后断言面板按 `offset:6` 续读并渲染新字节。
- `npx tsc -b`：`jobs*`/`workspace-page`/`job-output` 相关文件**零错误**（仅剩两处与本方案无关的既有 LSP 错误）。
- `npx vitest run src/components/workspace src/hooks/workspace`：**219 文件 / 1615 用例全绿**。

### 说明（与「纯流式 append」的差异）

- 采用「事件作触发 + REST 作权威读取」而非「前端直接 append chunk」：这样分页游标唯一、断线/重连后可重放，避免双份拼接与 offset 漂移。用户可见效果为**实时增量刷新**（展开的 live 任务随输出增长自动追加）。

### 仍待办

- ConPTY 重写与验证（Windows TTY）。
- `detach` → 受管作业真正路由（需 toolkit↔broker 的 background-submitter seam）。
