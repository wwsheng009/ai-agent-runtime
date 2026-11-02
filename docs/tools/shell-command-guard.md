# Shell 命令执行守卫（进程树 + 有界等待）

> 状态：已实现；本地 shell / hooks 使用 ProcessGuard，remote shell 的 core job launcher 也使用 `cmd.WaitDelay`

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
6. **job-first 等待**：单条命令先登记为 core background job，再 `Wait`。`timeout`/`timeout_ms` 只是等待预算；到期返回 promoted result（`job_id` + `next_offset`），不杀 job。需要停止时用 `job_cancel`。
7. **超时上限**：默认**不设上限**（`AICLI_SHELL_MAX_COMMAND_TIMEOUT` 默认 `0`）：自动化场景应当把命令执行到自然结束，唯一的时间上界来自模型显式传入的 `timeout`/`timeout_ms`。只有运维显式配置了该变量时，才会截断超大的模型参数，来源标记为 `runtime_ceiling`。
8. **常驻命令提示**：daemon/dev-server/watch 类命令在等待到期时返回 promoted job，由 `job_output` 继续观察；不再在超时点 kill。
9. **管道占用提示**：命令本体已结束但后代仍占用输出管道时（`wait_delay_used=true`），失败结果附带 `wait_delay_note` 与 next_action，说明输出可能不完整以及如何保留/清理该守护进程。

## 环境变量

| 变量 | 默认 | 说明 |
| --- | --- | --- |
| `AICLI_SHELL_WAIT_DELAY` | `5s` | 子进程退出/取消后等待 I/O 的上限；`0` 关闭（不建议） |
| `AICLI_SHELL_MAX_COMMAND_TIMEOUT` | `0`（不限制） | 单次 shell 调用的运行时上限（截断模型传入的超大 timeout）；`0`/`off`/`disable` 关闭上限，非法值同样按关闭处理 |
| `AICLI_SHELL_QUIET_NOTICE_TIMEOUT` | `2m` | 静默多久后追加提示；`0` 关闭 |
| `AICLI_SHELL_KILL_ORPHANS_ON_EXIT` | `false` | 命令结束时是否连离开的后代进程一起清理 |

## 后台作业与显式后台

- `shell` 不再提供 `detach` 参数，也不允许 `commands` 同步批次；需要后台执行时用 `job_start`（同一 core manager 的“不等待”形式）。
- `job_start` 默认不等待，`job_output` 使用 cursor 读取，`job_cancel` 停止；这些工具与 `shell` 共用 job store、owner、进程树和 retention。
- 需要交互式 stdin/TTY 的会话走 core `OpenTerminal` / `job_input`，不是在 shell 参数里另开 `detach` 分支。
- 最小终端窗口/独立服务需要真正脱离 runtime 时，由目标机自己的 service manager（systemd、Windows 服务、计划任务）或 `setsid nohup` 托管，不再由 shell 工具偷偷创建 fire-and-forget 进程。

## 适用范围

- `toolkit` shell / bash / execute_shell_command（`internal/toolkit/tools/bash.go`，job-first 路径）
- hooks 的 shell executor（`internal/hooks/executor_shell.go`）
- remote tools（`internal/background.Manager` 的进程内 launcher，`DisableDetached: true`）：同一 `WaitDelay` 语义；`wait_delay_used` / `wait_delay_ms` 写入 job 元数据并透传到 `shell` / `job_output`

其它 exec 站点（gitbrowse、filebrowse、mcp transport、worktree 等）可按同一模式接入：`exec.Command` → `guard.Bind(cmd)` → `guard.Attach(cmd.Process)`（或 `CaptureCombinedOutputGuarded`）→ `guard.Close()`。

## 回归测试

- `internal/executor/process_guard_test.go`：取消时整树终止、孙进程持有管道时 WaitDelay 兜底、子进程追踪、env 解析。
- `internal/executor/detach_test.go`：开关解析、detach 启动返回真实 PID、缺失可执行文件报错。
- `internal/toolkit/tools/bash_process_guard_test.go`：超时命令被终止且返回结构化诊断、运行时上限、常驻命令提示。
- `internal/toolkit/tools/bash_job_first_test.go`：快速完成自动 Forget、等待到期 promoted、无 runtime fail closed。
- `backend/internal/shelljob/runtime_test.go`：Submit -> Wait -> Read -> Forget 状态机；取消/超时语义。
- remote-tools：`internal/remotetools/jobs_test.go` 覆盖 core manager、owner、jobs.root、promotion 和旧数据清理。
