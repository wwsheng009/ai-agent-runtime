# Shell 命令执行守卫（进程树 + 有界等待）

> 状态：已实现（backend/internal/executor/process_guard*.go + detach*.go + output_capture.go + toolkit shell/hooks 接入）

## 背景

前台 shell 工具原先走 `exec.CommandContext` + `cmd.Run()`：

- 超时/取消只终止**直接子进程**（pwsh/bash），命令自身拉起、脱离的直接或间接后代（daemon、`Start-Process`、后台服务）会变成孤儿；
- `Wait` 必须等到 stdout/stderr 管道 EOF。若任何后代进程继承了写端（典型：CLI 自动拉起的守护进程），即使 shell 已退出、超时已到，工具调用仍会一直 pending（UI 表现为 `Running … (12m 17s) • esc to interrupt`）；
- 无"静默检测"，模型和用户都无法区分"很慢"与"卡死"。

## 机制

1. **有界等待（WaitDelay）**：`ProcessGuard.Bind` 给命令设置 `cmd.WaitDelay`（默认 5s），cmdCtx 结束或子进程退出后，等待 I/O 的时间有上限，超时返回 `exec.ErrWaitDelay`。
2. **进程树终止**：
   - Windows：每个命令一个 Job Object（`JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE | JOB_OBJECT_LIMIT_BREAKAWAY_OK`），`cmd.Start()` 后把进程加入 job；取消/超时/中断时 `TerminateJobObject` 一次性杀掉整棵树。job 创建或加入失败时自动降级为 `taskkill /T /F`，再降级为直接 kill。`BREAKAWAY_OK` 只放行**显式**声明 `CREATE_BREAKAWAY_FROM_JOB` 的派生（受控逃逸，见下节），其余后代仍全部被捕获。
   - Unix：`Setpgid` 独立进程组，`kill(-pgid, SIGKILL)`。
3. **孤儿可见性**：命令结束后查询 job 内仍存活的进程，写入结果元数据 `leftover_descendant_pids`；默认只报告，设置 `AICLI_SHELL_KILL_ORPHANS_ON_EXIT=1` 时随命令一起清理。
4. **静默提示**：命令仍在运行但持续无输出超过阈值时，向捕获输出（含实时镜像）追加 `[runtime] shell command has been quiet for …` 提示，包含 pid、已运行时长与命令摘要。
5. **结构化诊断**：结果元数据新增 `wait_delay_ms`、`wait_delay_used`、`process_tree_kill`、`killed_pids`、`leftover_descendant_pids`、`termination`（`timeout|cancel|error`）。
   终止方式与降级原因分别记录在 `process_tree_mode`（`job_object|taskkill|direct_kill|process_group`）与 `process_tree_error`。
6. **超时上限**：即使模型显式传入超大 timeout，也被 `AICLI_SHELL_MAX_COMMAND_TIMEOUT`（默认 15m）截断，来源标记为 `runtime_ceiling`。
7. **常驻命令提示**：daemon/dev-server/watch 类命令超时失败时，结果附带 `long_running_command_hint` 与 next_action，建议改用 background_task 或显式 timeout。
8. **管道占用提示**：命令本体已结束但后代仍占用输出管道时（`wait_delay_used=true`），失败结果附带 `wait_delay_note` 与 next_action，说明输出可能不完整以及如何保留/清理该守护进程。

## 环境变量

| 变量 | 默认 | 说明 |
| --- | --- | --- |
| `AICLI_SHELL_WAIT_DELAY` | `5s` | 子进程退出/取消后等待 I/O 的上限；`0` 关闭（不建议） |
| `AICLI_SHELL_MAX_COMMAND_TIMEOUT` | `15m` | 单次 shell 调用的运行时上限（覆盖模型参数） |
| `AICLI_SHELL_QUIET_NOTICE_TIMEOUT` | `2m` | 静默多久后追加提示；`0` 关闭 |
| `AICLI_SHELL_KILL_ORPHANS_ON_EXIT` | `false` | 命令结束时是否连离开的后代进程一起清理 |
| `AICLI_SHELL_ALLOW_DETACH` | `true` | 是否允许 `detach=true` 的独立进程启动；`0/false/off/no/disabled` 时拒绝 |

## 逃逸路径与独立进程（受控 breakaway）

### 事实：隐式逃逸确实存在

守卫的 Job Object 只覆盖"受守卫 shell 的直接子进程及其后代"。以下方式会**绕过**作业对象（Windows + PowerShell 7 实测）：

- `Start-Process`（走 ShellExecute/Shell 代理创建进程）：新进程既不在 job 内、也不继承工具的输出管道。工具调用会立即成功返回，但该进程**不受超时/Esc/崩溃兜底影响**，也不会出现在 `leftover_descendant_pids`（该字段只枚举 job 成员）。
- 任何显式以 `CREATE_BREAKAWAY_FROM_JOB` 创建的子进程（job 已开启 `BREAKAWAY_OK`，因此受守卫树内也能受控脱离）。

所以"kill 整树 + 崩溃兜底"只对 job 内进程成立。`detach` 与 breakaway 是**有意保留**的逃生门：可控、可观测，而不是默认行为。

### 受控方式一：`detach=true`（推荐）

```json
{ "command": "aicli --yolo", "detach": true }
```

- Windows：`CreateProcessW` + `CREATE_NEW_CONSOLE | CREATE_BREAKAWAY_FROM_JOB`，且**不设置** `STARTF_USESTDHANDLES` → 子进程获得新控制台的真实标准句柄（TUI 可用），不继承工具管道；不加入本次命令的 job。
- Unix：`Setsid` 新会话 + `/dev/null` 作为 stdin/stdout/stderr。
- 不捕获输出、立即返回 PID；元数据：`detached`、`detached_pid`、`detach_mode`（`new_console|setsid`）、`detach_breakaway`、`detach_warning`。
- 外层作业不允许 breakaway 时自动降级（去掉该标志）并在结果里给出 `detach_warning`，此时仍独立于本次调用的 job。
- 仅支持单条 `command`；`commands` 批次明确拒绝（`detach_batch_refused`）。
- 全局开关 `AICLI_SHELL_ALLOW_DETACH=0` 时拒绝执行（`detach_refused`）。

### 受控方式二：job 内的显式 breakaway

守卫 job 已设置 `JOB_OBJECT_LIMIT_BREAKAWAY_OK`：受守卫命令内部若显式以 `CREATE_BREAKAWAY_FROM_JOB` 创建进程，可脱离 job；其余后代仍被捕获。

### 推荐用法与注意事项

| 场景 | 推荐 | 说明 |
| --- | --- | --- |
| 再开一个 aicli TUI / 需要真终端的交互程序 | `detach=true` | 新控制台；窗口可见性取决于默认终端宿主 |
| 守护进程 / 常驻服务 | `detach=true` 或 background_task | background_task 有输出捕获但没有 TTY |
| 只想跑长命令并保留日志 | 普通调用 + `timeout` | 不要 detach，否则拿不到输出 |
| 希望运行时结束时被一起清理 | 不要 detach | 保持默认：随 job 终止/关闭 |

- detach 进程**不会**进入 `leftover_descendant_pids`，也不受 `AICLI_SHELL_KILL_ORPHANS_ON_EXIT` 影响；必须用返回的 PID 显式管理（`Stop-Process -Id <pid> -Force` / `kill <pid>`）。
- 端口/资源冲突、会话存储并发、keyring 访问由被启动程序自行负责（例如第二个 aicli 实例应避开已占用的 `--web-port`/`--pprof` 端口）。
- 无交互桌面（系统服务、计划任务）下新控制台不可见，TUI 类程序无法渲染。

## 适用范围

- `toolkit` shell / bash / execute_shell_command（`internal/toolkit/tools/bash.go`，sandbox 与非 sandbox 两条路径）
- hooks 的 shell executor（`internal/hooks/executor_shell.go`）

其它 exec 站点（gitbrowse、filebrowse、mcp transport、worktree 等）可按同一模式接入：`exec.Command` → `guard.Bind(cmd)` → `guard.Attach(cmd.Process)`（或 `CaptureCombinedOutputGuarded`）→ `guard.Close()`。

## 回归测试

- `internal/executor/process_guard_test.go`：取消时整树终止、孙进程持有管道时 WaitDelay 兜底、子进程追踪、env 解析。
- `internal/executor/detach_test.go`：开关解析、detach 启动返回真实 PID、缺失可执行文件报错。
- `internal/toolkit/tools/bash_process_guard_test.go`：超时命令被终止且返回结构化诊断、运行时上限、常驻命令提示。
- `internal/toolkit/tools/bash_detach_test.go`：detach 启动成功并回收 PID、`AICLI_SHELL_ALLOW_DETACH=0` 拒绝、批次拒绝、detach 进程在兄弟命令超时终止后仍存活。
