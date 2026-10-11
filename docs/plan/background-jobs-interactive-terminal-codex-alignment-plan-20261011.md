# 后台作业交互能力对标 Codex 优化方案（PTY 会话 + write_stdin + 实时输出）

> 状态：提案（已评审修订，待实施）
> 修订记录：2026-10-11 评审后修订——修正「后台作业有沙箱」「可复用 conpty-probe/winconsole/consolehost」两处事实错误；补齐审批缓存、跨实例路由、重启语义、槽位/offset 权威、流式复用 `toolprotocol` 等未决项；`detach` 收敛移出 P0。
> 关联文档：`docs/background-job-lifecycle.md`、`docs/plan/background-jobs-p0-plan-20260928.md`
> 对标实现：`codex-rs/core/src/unified_exec/*`、`codex-rs/core/src/tools/handlers/shell_spec.rs`、`codex-rs/app-server-protocol/src/protocol/v2/thread.rs`
> 涉及面：`backend/internal/background`、`internal/toolbroker`、`internal/toolkit/tools`、`internal/policy`、`internal/api/runtimeapi`、`internal/chat`、`internal/runtimeobserve`、`frontend` jobs 面板
> 难度：expert（跨系统一致性 + 平台差异 + 权限/监督口径 + 前端契约）

---

## 0. 背景与目标

### 0.1 现状（本项目已具备，见 `docs/background-job-lifecycle.md`）

本项目后台作业在「持久化 / 调度 / 容错 / 监督」维度已远超 Codex：

- 托管 shell 作业（`internal/background.Manager.SubmitShell`）+ detached runner（`detached.go`）；
- SQLite store（`store.go`）+ `state_version` CAS + 终态吸收；
- 实例归属 / 租约 / 心跳、队列 `queued_at/deadline_at`、`expired/interrupted/paused/abandoned` 等 11 态；
- launch 重试、恢复退避、watchdog（stalled/zombie/pid_reused/missing）、进程树（Windows Job Object / Unix pgid）、孤儿 reaper；
- 启动验收探针（none/process/tcp/http）；
- 工具面 `background_task / task_output / task_kill / task_monitor / task_control`；
- ⚠️ 现状澄清（评审核验）：后台作业执行**无沙箱**——`manager.go:2958-2965` 仅做 shell 探测（`runtimeexecutor.DefaultUserShell`）+ `exec.CommandContext`，`runtimeexecutor`/`runtimeexecution` 分别只用于 shell 探测/环境注入与 timeout source；
- 终态/巡检投影为 supervision 通知并唤醒空闲会话（`internal/supervision/background_job.go`）。

### 0.2 差距（对标 Codex，2026-10-11 核验）

Codex 的「后台作业」本质是**可交互的 PTY 会话**，本项目本质是**一次性、可持久化的命令作业**。差距集中在交互/实时性：

| 能力 | Codex | 本项目现状 |
|---|---|---|
| 运行中写 stdin | ✅ `write_stdin` | ❌ 无（`TaskOutputArgs` 无输入字段） |
| PTY/TTY 语义 | ✅ PTY（`process.rs`） | ❌ 管道 + 日志文件 |
| 实时输出 | ✅ `ExecCommandOutputDelta` 增量 | 仅 offset 轮询 `task_output`（有阻塞 `wait`） |
| 进程复用 | ✅ 按 `session_id` 复用 | ❌ 每次 `background_task` 新进程 |
| 资源指标 | ✅ `os_pid/cpu_percent/rss_kb` | 仅 pid/heartbeat/watchdog |
| 会话清理 | `thread/backgroundTerminals/{list,clean,terminate}` | REST list/cancel/control，无 input/resize |
| detach 语义 | 与 exec 统一 | `bash detach=true` 为 fire-and-forget、无输出、无监督，与 `background_task` 割裂 |

### 0.3 目标与非目标

**目标**

1. **可交互后台会话**：对运行中的后台作业/会话写 stdin 并读到增量输出（Codex `write_stdin` 等价）。
2. **实时输出流**：输出以增量事件推送（TUI / 前端 / SSE 可见），offset 轮询退化为可重放快照而非唯一通道。
3. **进程复用**：同一 `session_id` 可被多次工具调用驱动，保持 shell 状态（cd/env/变量）。
4. **TTY 语义**：受支持平台提供 PTY；Win7 等降级为管道且功能不崩溃（显式能力声明）。
5. **不回归**：终态 CAS、进程树终止、租约、队列、supervision 语义保持 P0–P3 契约。

**非目标（本期不做，避免范围蔓延）**

- 不引入 Codex 的 JSON-RPC app-server；沿用现有 REST + EventBus。
- 不重写作业生命周期/调度；交互会话作为**并列的一等实体**叠加，而非替换。
- 不做跨实例「交互会话」自动接管（stdin 句柄不可序列化）：会话仅在**拥有实例**存活期内可写 stdin；跨实例只可观测/终止，调用 `write_stdin` 必须路由到 owner 或返回可操作错误（§3.11）。
- 不改变「后台作业当前无沙箱」这一现状的默认语义（是否引入沙箱见 §3.8 决策项），避免把安全面变更混入本方案默认路径。

---

## 1. 对标拆解：Codex 后台终端机制（参考锚点）

| 机制 | Codex 实现点 | 要点 |
|---|---|---|
| 进程管理 | `core/src/unified_exec/process_manager.rs` | 进程池、复用、审批/沙箱编排、上限 `MAX_UNIFIED_EXEC_PROCESSES=64` |
| PTY 进程 | `core/src/unified_exec/process.rs` | PTY 生命周期 + 输出缓冲（head/tail cap） |
| 增量输出 | `core/src/unified_exec/async_watcher.rs` | 在 UTF-8 边界产出 `ExecCommandOutputDelta` |
| 超时/让出 | `unified_exec/mod.rs` | `MIN_YIELD_TIME_MS=250`、`MAX_YIELD_TIME_MS=30000`、`DEFAULT_MAX_BACKGROUND_TERMINAL_TIMEOUT_MS=300000` |
| 工具面 | `core/src/tools/handlers/shell_spec.rs` | `exec_command` + `write_stdin`（`yield_time_ms`） |
| 会话信息 | `core/src/codex_thread.rs` | `BackgroundTerminalInfo { item_id, process_id, command, cwd }` |
| 协议 | `app-server-protocol/.../v2/thread.rs` | `thread/backgroundTerminals/{list,clean,terminate}`，`ThreadBackgroundTerminal{ os_pid, cpu_percent, rss_kb }` |

**借鉴结论**：本项目已有事件总线（`internal/chat/events.go` 的 `EventJobOutput`，已贯通 TUI 编码器 `cmd/aicli/ui/render/encoding/encoder.go:894` 与前端 `cmd/aicli/commands/web_schema.go:178`，且被排除在会话持久化之外见 `handler.go:5830`）、`runtimeobserve` 事件目录、REST 面与 supervision 投影，具备「增量输出 + 写 stdin」的落点；缺的是**保持 stdin/stdout 打开的运行中进程原语**与**写 stdin 的工具/API/策略**。

> 现有可复用资产（评审补）：`internal/toolprotocol/terminal_stream.go` 已提供 `NewTerminalStreamWriter`——UTF-8 边界分块、按 `FlushMaxBytes` 强制 flush、`MaxEvents`（默认 64）上限，正是 Codex `ExecCommandOutputDelta` 的等价物。**本方案不重造分块逻辑，直接复用**。
> 现状发射粒度：`emitLogOutput` 按 monitor 周期（`MonitorInterval` 默认 250ms）轮询日志文件发射，属 **tick 驱动伪流式**；交互会话改为**读 goroutine 写驱动**推送（§3.3）。

---

## 2. 目标架构

### 2.1 分层：Job（作业）+ Terminal（会话）

现有 `Job` 是「提交—执行—终态」的一次性实体。新增 `Terminal`（交互会话）作为并列实体，复用 `Job` 的存储/调度/进程树能力：

```
background.Manager
├── Job 运行路径（既有，不动）        SubmitShell → detached/in-process → 终态
└── Terminal 交互路径（新增）        OpenTerminal → 常驻进程（stdin/stdout 打开）
                                      ├── WriteStdin(session_id, chars, yield_ms) → 增量输出
                                      ├── ReadOutput(session_id, offset)          → 快照
                                      ├── Resize(session_id, cols, rows)          → PTY 尺寸
                                      └── CloseTerminal(session_id)               → 整树终止
```

- **Terminal 也写入 `background_jobs` 表**（`kind='terminal'`），复用 `state_version` CAS、`owner_instance_id`/租约、进程树、reaper、supervision 投影；仅新增 `stdin` 句柄的内存态与 `interactive` 元数据。
- 交互句柄（stdin writer）**只在拥有实例内存中存在**，不落库；跨实例只可观测/终止，不可写。
- 数量上限对齐 Codex：新增 `background.MaxInteractiveSessions`（默认 64，参考 `MAX_UNIFIED_EXEC_PROCESSES`）。
- **槽位归属（评审补）**：交互会话**不占用** `MaxConcurrentJobs`（默认 4，那是排队作业的调度槽），由独立的 `MaxInteractiveSessions` 限流。理由：交互会话是用户显式打开的长驻进程，不应与批处理作业争抢队列槽，否则 4 个会话即可饿死所有后台作业。

### 2.2 状态机扩展（仅新增，不改既有）

```
terminal: pending ──launch──▶ running ──(stdin EOF / exit)──▶ {completed, failed, cancelled, timed_out, orphaned}
                                  │
                                  └── idle（有 stdin 打开但当前无输出，仍 running）
```

- 复用既有 `JobStatus`；`interactive=true` 的作业在 `running` 期间允许 `WriteStdin`。
- 关闭语义：`close_stdin` 或显式 `CloseTerminal` → 等进程退出 → 既有终态写入路径（CAS）。
- 交互会话**不参与** `QueueTimeout` 过期（它是显式打开的，不是排队作业）。

### 2.3 数据模型

`background_jobs` 新增列（自动迁移，遵循 `store.go` 的 `PRAGMA table_info` 加列范式）：

```sql
ALTER TABLE background_jobs ADD COLUMN interactive    INTEGER NOT NULL DEFAULT 0;
ALTER TABLE background_jobs ADD COLUMN tty            INTEGER NOT NULL DEFAULT 0;
ALTER TABLE background_jobs ADD COLUMN stdin_closed   INTEGER NOT NULL DEFAULT 0;
ALTER TABLE background_jobs ADD COLUMN os_pid         INTEGER;
ALTER TABLE background_jobs ADD COLUMN cpu_percent    REAL;
ALTER TABLE background_jobs ADD COLUMN rss_kb         INTEGER;
ALTER TABLE background_jobs ADD COLUMN cols           INTEGER;
ALTER TABLE background_jobs ADD COLUMN rows           INTEGER;
```

- `os_pid/cpu_percent/rss_kb` 对齐 Codex `ThreadBackgroundTerminal`；CPU/RSS 为**采样值**（见 §3.6）。
- 运行期高频字段（stdin 句柄、增量游标）只留内存，不落库，避免写放大。

---

## 3. 详细设计

### 3.1 交互进程原语（P0 核心）

新增 `backend/internal/background/interactive.go`：

```go
// TerminalOpenArgs 打开一个常驻交互进程。
type TerminalOpenArgs struct {
    Command    string
    Cwd        string
    TTY        bool   // 请求 PTY；平台不支持时降级为管道并回填实际能力
    Cols, Rows int
    Env        map[string]string
    YieldMs    int    // 首次让出窗口
}

type terminalSession struct {
    mu       sync.Mutex
    cmd      *exec.Cmd
    stdin    io.WriteCloser      // 管道模式
    ptmx     *os.File            // PTY 模式（可选）
    output   *outputBuffer       // 复用既有 head/tail 缓冲
    offset   int64               // 增量读游标
    closed   bool
    exitCode *int
    tree     *processTree
}
```

- `Manager.OpenTerminal(ctx, sessionID, args) (*Job, error)`：
  1. 校验/登记到 `background_jobs`（`kind='terminal'`, `interactive=1`）；
  2. **沿用现有 shell 构建路径**（`buildShellCommand`，见 `manager.go:2958-2965`：`runtimeexecutor.DefaultUserShell()` + `exec.CommandContext` + rg 路径注入）。
     > ⚠️ 事实澄清（评审修正）：**后台作业当前没有任何沙箱**——`runtimeexecutor` 只用于 shell 探测/环境注入，`runtimeexecution` 只用于 timeout source，`SubmitShell`/`runJob` 不经过 executor 沙箱。交互会话默认继承同一（无沙箱）语义；是否引入沙箱见 §3.8 决策项。
  3. 用 `cmd.StdinPipe()`/`cmd.StdoutPipe()`（或 PTY）启动，**不进入 detached runner**（detached runner 写日志文件、不保持 stdin，无法交互）；
  4. 起 reader goroutine，经 `toolprotocol.NewTerminalStreamWriter` 复用分块，填充 `outputBuffer` 并**写驱动**发射增量事件（§3.3）；
  5. 挂进程树；**在 `acceptStartedProcess` 启动验收通过后才返回** `running` 快照，消除 `background_task interactive=true` 后立即 `write_stdin` 的竞态。
- `Manager.WriteStdin(ctx, sessionID, input string, yieldMs int) (*TerminalReadResult, error)`：
  1. 校验 job 存在且 `running && interactive`（否则返回可修复错误 + `next_action`）；
  2. 写入 stdin（PTY 模式直接写 `ptmx`）；
  3. 按 `yieldMs`（clamp 到 `[250, 30000]`，空输入下限 5000，对齐 Codex `MIN/MAX_YIELD_TIME_MS`、`MIN_EMPTY_YIELD_TIME_MS`）等待新输出或退出；
  4. 返回自上次 offset 起的增量 + `wait_condition`（`immediate|output|exit|timeout`）+ `waited_ms`。
- `Manager.CloseTerminal(ctx, sessionID)`：关 stdin → 等退出（超时则整树终止）→ 既有 CAS 终态。

> **平台降级**：`TTY=true` 但平台无 PTY 时，落管道模式，`job.Metadata["tty_fallback"]="pipe"`，并在工具结果里显式告知模型（能力声明，不静默）。

### 3.2 工具面：`write_stdin`（Codex 等价）

在 `internal/toolbroker/broker.go` 新增（命名对齐 Codex，兼容本项目习惯）：

- 工具名 `write_stdin`，别名 `task_input`（`handle_aliases.go` 注册）。
- 参数：`job_id|session_id|task_id`（三别名）、`chars|input`（string）、`yield_time_ms`（int）、`close_stdin`（bool，默认 false）。
- 描述明确：写运行中会话的 stdin，返回增量输出与 `wait_condition`；非交互作业调用返回 `not_interactive`（内容结果，附 `next_action`）。
- 与 `task_output` 的分工：`task_output` 读快照/健康；`write_stdin` 写输入并顺带读增量。
- **offset 权威（评审补）**：唯一权威游标是 **`outputBuffer` 的绝对 offset**；`write_stdin` 返回本次读到的新 offset，调用方下一轮把它作为 `task_output` 的 `offset` 继续读，避免「内部游标 vs 调用方 offset」双轨漂移。`write_stdin` 不接受调用方 offset（写语义），只回传 offset。

**策略/权限登记（必须同步，否则工具不可见或审批缺失）**：

- `internal/policy/taxonomy.go`：`"write_stdin": {Kind: ToolKindControl}`（别名 `task_input` 同）；
- `internal/policy/capability.go`：新增 `CapWriteStdin`，映射 `write_stdin`/`task_input`；
- `internal/policy/grants.go`：加入 `shell/bash/aicli_exec/background_task/...` 同类执行授权组；
- `internal/policy/tool_policy.go`：与 `background_task/task_kill/...` 同组（需审批的写类工具）；
- `internal/toolbroker/broker_arg_kinds.go`、`broker_arg_audit.go`：登记 `chars/input`、`yield_time_ms`、`close_stdin`；
- `internal/toolkit/listable.go`、`internal/agent/doom_loop.go`、`internal/agent/tool_list.go`：可见性/防抖白名单同步。

### 3.3 实时输出流（增量事件）

- **复用既有流原语（评审补）**：reader goroutine 使用 `internal/toolprotocol/terminal_stream.go` 的 `NewTerminalStreamWriter`（UTF-8 边界分块、`FlushMaxBytes` 强制 flush、`MaxEvents` 上限），**不重造分块**；事件复用既有 `job_output`（`internal/chat/events.go: EventJobOutput`）推送 EventBus / runtime event bridge。
- **写驱动 vs tick 驱动**：交互会话由 reader goroutine 在**读到字节时**推送（真流式），替代 `emitLogOutput` 的 250ms tick 轮询；`task_output` 仍按需 offset 快照。
- 事件 payload 增补：`job_id`、`session_id`、`seq`、`offset`、`chunk`、`truncated`。
- **背压（评审补）**：三重上限——`outputBuffer`（`MaxOutputBytes` 默认 1 MiB，超限 head/tail + 指针）+ `NewTerminalStreamWriter.MaxEvents`（默认 64，可调，禁止无限事件）+ 前端渲染节流；事件与唤醒白名单解耦（§3.9）。
- 去重/重放：增量事件为「提示」，`task_output` 的 offset 快照为「权威」；断线重连以 offset 续读。
- **事件契约同步（评审补，四处必须齐全，否则计 `unknown_events_dropped`）**：`internal/chat/events.go`（常量）、`internal/events/contract.go`（契约表）、`internal/runtimeobserve/known_types.go`（观测目录）、`cmd/aicli/commands/web_schema.go`（前端 SSE 映射）。
- 前端：`frontend` jobs 面板接入实时增量（SSE/既有事件通道）；`use-session-runtime-stream` 复用既有流。

### 3.4 进程复用与 session 作用域

- `OpenTerminal` 的 `session_id` = 复用键；同一 `session_id` 再次 `OpenTerminal` 返回已有会话（`already_open` 提示 + 现有 `job_id`），对齐 Codex 的按 session 复用。
- `background_task` 新增 `interactive=true`（+可选 `tty=true`）：走 `OpenTerminal` 而非 detached，返回可被 `write_stdin` 驱动的 `job_id`。
- 作用域与会话绑定：`updateSessionActiveJobs`（`internal/api/runtimeapi/handler.go`）已维护 `ActiveJobIDs`，交互会话纳入同一集合，会话关闭时按既有清理路径收敛。

### 3.5 PTY 升级与平台降级（P1）

- **Unix**：`github.com/creack/pty`（或 `github.com/aymanbagabas/go-pty`，二选一并锁版本）打开 `ptmx`，`Setsid + Setctty`；`Resize` 用 `TIOCSWINSZ`。**新增第三方依赖需在 CHANGELOG 与依赖审计中登记。**
- **Windows 10 1809+**：ConPTY（`CreatePseudoConsole`/`ResizePseudoConsole`）。
  > ⚠️ 事实澄清（评审修正）：仓库**没有可复用的 ConPTY 库**——`cmd/conpty-probe/main.go` 是 `package main` 探针（第 3 行注释自述「验证本机 ConPTY 是否可用」，只能抄代码不能 import）；`internal/winconsole` 是控制台 UTF-8 代码页（`console_utf8_*.go`），与 ConPTY 无关；`internal/consolehost` 走 `CREATE_NEW_CONSOLE` 且注释明确 "does not emulate a terminal"。`go.mod` 也无任何 pty/conpty 依赖。
  > 正确做法：**新建 `internal/pty` 包**，把 `conpty-probe` 的 ConPTY 调用提取为可复用库（Windows，复用已有的 `golang.org/x/sys/windows`）。
- **Windows 7 / 受限环境**：无 ConPTY，**降级管道**（§3.1）。与 `docs/aicli/windows7-compat-internals.md` §7 的既有约束一致（该环境连 PowerShell 都可能被裁剪，见 `detached.go: defaultDetachedRunnerHostAvailable`）。
- **能力探测**：启动时探测并缓存 `pty_supported`；配置 `background.pty.enabled`（默认 true，探测失败自动关闭）；工具结果与 doctor 报告显式暴露该能力。
- **Resize**：`POST .../resize` + 工具可选参数；无 PTY 时返回 `unsupported`（内容结果）。

### 3.6 资源指标（对齐 Codex）

- 扩展 `internal/background/process_{windows,linux,unix}.go` 的 `inspectProcess`：采样 `cpu_percent`（增量 CPU 时间 / 间隔）与 `rss_kb`（Windows `GetProcessMemoryInfo`；Unix `/proc/<pid>/statm` 或 `ps` 兜底）。
- 采样频率跟随 `MonitorInterval`（默认 250ms）或独立 `MetricsInterval`，避免高频 syscall；写入 job 内存态 + 低频落库。
- 暴露到 `TaskOutputResult`（`os_pid/cpu_percent/rss_kb`）与 REST/TS 类型，对齐 `ThreadBackgroundTerminal`。

### 3.7 `detach` 路径收敛（消除双入口）

> **阶段归属（评审修订）**：本节为**破坏性行为变更**，已从 P0 移至 **P2**，避免与新增交互原语同批上线；P0 保持完全 additive。

- `internal/toolkit/tools/bash.go` 的 `detach=true` 改为：创建一个**受管交互/常驻会话**（走 `OpenTerminal`，或至少 `SubmitShell` 并捕获输出），返回 `job_id`；保留 `AICLI_SHELL_ALLOW_DETACH=0` 全局禁用语义。
- 过渡期：`detach` 仍可用，但结果附 `next_action` 指引改用 `background_task`；文档标注 deprecated。
- 目的：消除「detach 无输出无监督」与 `background_task` 的语义割裂，向 Codex「单一 exec/write_stdin 对」收敛。

### 3.8 权限 / 沙箱 / 审批

- **沙箱决策（评审补）**：现状**后台作业无沙箱**（§3.1 澄清）。交互会话默认沿用同一无沙箱语义，但增加 stdin 写入会扩大风险面。方案给出两个选项，需评审拍板：
  - **A（默认，最小改动）**：不引入沙箱，依赖「会话级一次授权 + 审计 + 危险命令黑名单」约束；
  - **B（对齐 Codex）**：交互会话接入沙箱/审批编排（Codex `exec_command` 有 sandbox transform + approval）。代价是改动 `manager` 执行路径，建议单独立项。
- **审批缓存（评审补，必须）**：`write_stdin` 逐次审批会让 REPL 类交互**不可用**。必须采用**会话级一次授权**（对齐 Codex `stdin_approval`）：首次写 stdin 时审批，授权绑定 `job_id`+会话，后续写不再逐次弹审批；`close_stdin`/会话结束即失效；未授权会话调用返回 `approval_required`。
- 网络访问：交互会话内的网络请求沿用既有 network approval（`request_permissions`）；deferred 审批语义与后台作业一致。
- 审计：`broker_arg_audit.go` 记录写 stdin 的字符（按上限截断，避免把大段输入写入审计）；`internal/approvalexplain` 增补 `write_stdin` 的人类可读解释。

### 3.9 监督与唤醒

- 交互会话终态复用 `supervision.ProjectBackgroundJobTerminal`（`internal/supervision/background_job.go`）；新增 `terminal_opened/terminal_closed` 归入 `BackgroundJobFamilyMonitor`（不触发终态唤醒）。
- 高频 `job_output` 增量**不**触发唤醒（沿用现有白名单：仅终态/巡检到点唤醒），避免唤醒风暴。

### 3.10 对外 API 对齐

| 现有 REST | 新增/扩展 |
|---|---|
| `GET /api/runtime/background/jobs` | 增 `interactive/tty` 过滤与字段 |
| `GET /api/runtime/background/jobs/{id}` | 增 `os_pid/cpu_percent/rss_kb/cols/rows` |
| `GET .../{id}/output` | 增 `wait=output\|exit`、`timeout_ms`（与工具同口径） |
| — | `POST .../{id}/input`（写 stdin：`chars/yield_ms/close_stdin`） |
| — | `POST .../{id}/resize`（PTY 尺寸） |
| `POST .../{id}/cancel\|pause\|resume\|abandon\|requeue` | 不变 |
| — | `POST /api/runtime/background/terminals/clean`（批量清理会话，对齐 Codex `clean`） |

`pkg/skillsapi/client.go` 的 `BackgroundJob*` 结构体与 `frontend` TS 类型同步扩展（additive）。

### 3.11 跨实例路由与重启语义（评审补）

- **路由**：交互会话的 stdin 句柄只存在于 owner 实例内存。调用方（工具/REST）在 `write_stdin` 前先查 job 的 `owner_instance_id`：
  - owner == 本实例 → 直接写；
  - owner 为活跃 peer → 返回可操作错误 `not_writable_remote` + `next_action`（「该会话由实例 X 持有，交互仅限 owner；如需跨实例请用 `task_output`/`task_kill`」）；
  - owner 缺失/租约过期 → 按既有语义收敛（进程存活则标记 `lease_expired` 可观测；已死则 `orphaned`）。
- **重启语义**：交互会话是 in-process，**不跨重启恢复**。实例启动的 `recoverPersistedJobs` 对 `interactive=1 && running` 一律 CAS → `orphaned`（句柄不可重建），并记 `interactive_not_recoverable` 事件；不重排、不接管。

### 3.12 与既有工具的边界（评审补）

- `write_stdin` 只对 `interactive=1` 的 job 有效；对普通 detached 作业返回 `not_interactive`（内容结果 + `next_action` 指向 `task_output`）。
- `task_kill`/`task_control` 对交互会话照常生效（走既有 CAS + 整树终止）。
- `doom_loop` 防抖：`write_stdin`/`task_output` 属「合法重复轮询」，需加入 `internal/agent/doom_loop.go` 的非 doom-loop 白名单（否则多次驱动会被误判为死循环）。

---

## 4. 改动清单（按包）

| 包 / 文件 | 改动 |
|---|---|
| `internal/background/interactive.go`（新增） | `OpenTerminal/WriteStdin/ReadIncremental/Resize/CloseTerminal` + `terminalSession` |
| `internal/background/manager.go` | 接入 terminal 路径；`interactive` 作业不进 QueueTimeout；复用 CAS/进程树/reaper |
| `internal/background/types.go` | `Job`/`TaskOutputResult`/`BackgroundTaskArgs` 增字段（interactive/tty/os_pid/cpu/rss/cols/rows） |
| `internal/background/store.go` | 加列迁移（§2.3）；`kind='terminal'` 查询过滤 |
| `internal/background/detached.go` | 不改语义（`detach` 收敛移至 P2） |
| `internal/background/process_{windows,linux,unix}.go` | `inspectProcess` 增 CPU/RSS 采样 |
| `internal/pty/`（新增包） | 从 `cmd/conpty-probe` 提取 ConPTY 库（Windows，复用 `golang.org/x/sys/windows`）+ ptmx（Unix）+ 能力探测 + 降级；新增 Unix pty 依赖 |
| `internal/toolprotocol/terminal_stream.go`（复用） | 直接复用 `NewTerminalStreamWriter` 做 UTF-8 分块/`MaxEvents` 上限，不新造 |
| `internal/events/contract.go`、`cmd/aicli/commands/web_schema.go` | 新事件契约与前端 SSE 映射同步（§3.3） |
| `internal/toolbroker/broker.go` | 新增 `write_stdin`（别名 `task_input`）工具定义与 handler |
| `internal/toolbroker/broker_task_wait.go` | 复用 wait 语义；新增增量读取 |
| `internal/toolbroker/handle_aliases.go` | `write_stdin ↔ task_input` 别名 |
| `internal/toolbroker/broker_arg_kinds.go`/`broker_arg_audit.go` | 参数类型与审计登记 |
| `internal/toolkit/tools/bash.go` | `detach` 收敛为受管会话 + `next_action`（**移至 P2**，破坏性变更不随 P0） |
| `internal/policy/{taxonomy,capability,grants,tool_policy}.go` | `write_stdin`/`task_input` 登记（§3.2） |
| `internal/toolkit/listable.go`、`internal/agent/{doom_loop,tool_list}.go` | 可见性/防抖白名单 |
| `internal/chat/events.go`、`internal/runtimeobserve/known_types.go` | `terminal_opened/closed/resized` 事件登记（连同 `events/contract.go`、`web_schema.go` 共四处） |
| `internal/api/runtimeapi/background_handlers.go` | `/input`、`/resize`、`/terminals/clean`；`/output` 增 wait |
| `internal/supervision/background_job.go` | terminal 事件归类（monitor 族） |
| `pkg/skillsapi/client.go` | 客户端结构体扩展 |
| `frontend/.../jobs*` | 实时增量渲染 + 新字段/动作 |

---

## 5. 分阶段落地

| 阶段 | 内容 | 目标 | 工作量 |
|---|---|---|---|
| **P0 交互核心（纯增量）** | 管道版 `OpenTerminal` + `write_stdin` + **会话级审批缓存** + 写驱动 `job_output`（复用 `toolprotocol`）+ 策略/事件契约登记 + 跨实例路由与重启语义 | 关闭最大缺口，全平台可用（含 Win7），**不改既有行为** | 3–5 天 |
| **P1 PTY + 复用** | `internal/pty`（提取 ConPTY + Unix ptmx + 能力探测/降级）+ `Resize` + `session_id` 复用 + `interactive=true` + 沙箱决策落地 | TTY 语义与进程复用 | 4–6 天 |
| **P2 对齐与收敛** | 资源指标、REST 字段/端点、supervision/事件/前端、`terminals/clean`、上限与清理、**`detach` 收敛** | 与 Codex 协议/指标对齐 + 消除双入口 | 3–4 天 |

> 依赖：P1 依赖 P0 的会话原语；P2 可与 P1 部分并行（资源指标不依赖 PTY）。
> **阶段拆分（评审）**：`detach` 收敛是破坏性行为变更，已从 P0 移到 P2，避免与新增原语同批上线；P0 保持完全 additive。

---

## 6. 测试计划

**单元**

- `interactive_test.go`：open→write→read 增量→close；yield 窗口 clamp（250/30000、空输入 5000）；UTF-8 边界切分；`outputBuffer` 上限截断；`close_stdin` 语义。
- `pty_*_test.go`：PTY 可用/不可用分支；能力探测缓存；Win7 降级管道且不 panic。
- `process_*_test.go`：CPU/RSS 采样在 pid 复用/退出时的稳健性（不 panic、不误报）。
- store 迁移：加列幂等；`kind='terminal'` 过滤与既有查询兼容。

**集成**

- 起一个常驻进程（如 `cat`/简单 REPL），`write_stdin` 多轮驱动，验证：增量输出、进程复用、状态保持（cd/env）。
- 交互会话取消 → 整树终止（回归 P2 契约）+ CAS 终态不被覆盖（回归 P0 契约）。
- 非交互作业调用 `write_stdin` → `not_interactive` 内容结果 + `next_action`（不误判为 hard failure）。
- 上限：超过 `MaxInteractiveSessions` 返回可修复错误；确认交互会话**不占用** `MaxConcurrentJobs`。
- 审批缓存：首次写 stdin 触发审批，同会话后续写不再弹审批；会话结束授权失效；未授权返回 `approval_required`。
- 跨实例：owner 为 peer 时 `write_stdin` 返回 `not_writable_remote` + `next_action`；owner 租约过期/缺失按既有语义收敛。
- 重启：含 `interactive=1 && running` 的库重启后 → `orphaned` + `interactive_not_recoverable`，不重排。

**E2E / 前端**

- TUI/前端长任务实时增量可见（非纯轮询）；断线重连以 offset 续读不丢/不重。
- 事件目录无 `unknown_events_dropped`（新事件已登记）。

**平台矩阵（评审补）**

- Win7（无 ConPTY、可能无 PowerShell）、Windows 10+（ConPTY）、Unix（ptmx）三档必跑；PTY/管道两分支各覆盖 open→write→read→close 与降级声明。

**门禁**

- `go build ./...`、`go vet ./...`；`go test ./internal/background/... ./internal/toolbroker/... ./internal/policy/... ./internal/api/runtimeapi/... ./internal/runtimeobserve/... ./internal/events/...` 全绿；前端 `tsc -b` + 相关测试 + `lint:i18n`。

---

## 7. 兼容与迁移

- 加列迁移幂等（`PRAGMA table_info` 检查后 `ALTER`），旧库无需手工操作。
- 新增字段/端点/工具均为 **additive**；旧客户端不受影响。
- `detach` 行为变化（不再 fire-and-forget）需在 CHANGELOG 显式标注 + 过渡期 `next_action` 指引；`AICLI_SHELL_ALLOW_DETACH=0` 保留。
- PTY 能力按平台探测，`background.pty.enabled=false` 可强制管道回退。
- 交互句柄不落库：跨实例只可观测/终止，**不可写 stdin**；调用返回 `not_writable_remote`（§3.11），文档与错误信息显式说明。
- 重启：交互会话不跨重启恢复，`interactive=1 && running` 重启后 → `orphaned`（§3.11）。
- 沙箱：默认维持「后台作业无沙箱」现状；若采纳 §3.8 选项 B 则需单独标注安全语义变更。

---

## 8. 风险与缓解

| 风险 | 缓解 |
|---|---|
| Win7/受限环境无 ConPTY | 管道降级 + 能力声明 + doctor 暴露；P0 即全平台可用 |
| 保持 stdin/stdout 打开的进程在实例崩溃后残留 | 复用既有进程树 + reaper；交互会话句柄不落库，崩溃后按 pid+identity 校验收敛 |
| 高频增量事件造成唤醒/渲染风暴 | 事件与唤醒白名单解耦（仅终态/巡检唤醒）；输出走缓冲上限 + 指针 |
| 写 stdin 的审批与审计泄漏敏感输入 | 审计截断；默认需审批；与 network approval 一致 |
| 与既有 P0–P3 生命周期语义冲突 | 交互会话复用 CAS/租约/进程树，不新增终态；回归用例覆盖 |
| 工具/策略/事件/前端契约漂移 | 按 §3.2/§3.10 清单一次性登记；事件同步 `events/contract.go` + `known_types.go` + `web_schema.go` 三处 |
| 逐次审批使交互不可用 | 会话级一次授权（§3.8），非逐次 |
| 跨实例调用 `write_stdin` 无路由 | owner 路由 + `not_writable_remote` 可操作错误（§3.11） |
| 无沙箱 + 写 stdin 放大风险 | 会话级授权 + 审计截断 + 黑名单；沙箱对齐作为独立决策项（§3.8 选项 B） |
| 交互会话占用调度槽饿死后台作业 | 独立 `MaxInteractiveSessions`，不占 `MaxConcurrentJobs`（§2.1） |
| 事件/唤醒风暴 | `terminal_stream` `MaxEvents` 上限 + 仅终态/巡检唤醒（§3.3/§3.9） |

---

## 9. 验收标准

1. 能对运行中的后台会话写 stdin，并读到自上次 offset 起的增量输出（Codex `write_stdin` 等价）。
2. 输出以增量事件推送，TUI/前端无需轮询即可看到长任务进展；`task_output` 仍可 offset 重放。
3. 同一 `session_id` 可被多次调用驱动（进程复用，shell 状态保持）。
4. 支持平台提供 PTY/TTY 与 `resize`；Win7 降级管道，功能不崩溃且有显式能力声明。
5. 作业快照/REST 暴露 `os_pid/cpu_percent/rss_kb`（对齐 `ThreadBackgroundTerminal`）。
6. 终态 CAS、进程树终止、租约、队列、supervision 语义**不回归**（P0–P3 回归用例全绿）。
7. 会话级审批缓存生效：同会话多次写 stdin 不逐次弹审批；未授权返回 `approval_required`。
8. 跨实例 `write_stdin` 返回 `not_writable_remote` + `next_action`；重启后交互会话收敛为 `orphaned`。
9. 交互会话不占用 `MaxConcurrentJobs`（4 个会话不会饿死后台作业）。
10. `go build ./...`、`go vet`、§6 门禁测试全绿（含 Win7/Win10+/Unix 矩阵）；前端类型/文案/事件契约同步。

---

## 附 A：本项目代码锚点

- 作业管理：`backend/internal/background/manager.go`（`SubmitShell`/`dispatchPending`/CAS 写路径）
- detached 运行与监控：`backend/internal/background/detached.go`（`runDetachedJob`/`monitorDetachedJob`/`recoverDetachedRunningJob`）
- 进程探活/终止：`backend/internal/background/process_{windows,linux,unix}.go`、`proc_tree_{windows,unix}.go`
- 类型/输出：`backend/internal/background/types.go`（`Job`/`TaskOutputArgs`/`TaskOutputResult`）
- 存储：`backend/internal/background/store.go`（建表/加列迁移/CAS）
- 工具面：`backend/internal/toolbroker/broker.go`、`broker_task_wait.go`、`broker_task_monitor.go`、`handle_aliases.go`
- 流式原语（复用）：`backend/internal/toolprotocol/terminal_stream.go`（`NewTerminalStreamWriter`）
- ConPTY 探针（待提取为 `internal/pty`）：`backend/cmd/conpty-probe/main.go`
- 事件契约（四处同步）：`backend/internal/chat/events.go`、`internal/events/contract.go`、`internal/runtimeobserve/known_types.go`、`cmd/aicli/commands/web_schema.go`
- 无沙箱执行路径（澄清锚点）：`backend/internal/background/manager.go:2958-2965 buildShellCommand`
- shell/detach：`backend/internal/toolkit/tools/bash.go`（`detach` 分支）
- 策略：`backend/internal/policy/{taxonomy,capability,grants,tool_policy}.go`
- 事件：`backend/internal/chat/events.go`（`EventJobOutput`）、`internal/runtimeobserve/known_types.go`
- REST：`backend/internal/api/runtimeapi/background_handlers.go`
- 监督：`backend/internal/supervision/background_job.go`
- 既有方案：`docs/background-job-lifecycle.md`、`docs/plan/background-jobs-p0-plan-20260928.md`

## 附 B：Codex 参考锚点

- `codex-rs/core/src/unified_exec/mod.rs`（`MIN/MAX_YIELD_TIME_MS`、`DEFAULT_MAX_BACKGROUND_TERMINAL_TIMEOUT_MS`、`MAX_UNIFIED_EXEC_PROCESSES`）
- `codex-rs/core/src/unified_exec/process_manager.rs`（进程池/复用/`list_processes`/`terminate_process`）
- `codex-rs/core/src/unified_exec/process.rs`、`async_watcher.rs`（PTY 生命周期 + `ExecCommandOutputDelta`）
- `codex-rs/core/src/tools/handlers/shell_spec.rs`（`exec_command` + `write_stdin`）
- `codex-rs/core/src/codex_thread.rs`（`BackgroundTerminalInfo`）
- `codex-rs/app-server-protocol/src/protocol/v2/thread.rs`（`ThreadBackgroundTerminal{ os_pid, cpu_percent, rss_kb }` 与 `backgroundTerminals/{list,clean,terminate}`）
