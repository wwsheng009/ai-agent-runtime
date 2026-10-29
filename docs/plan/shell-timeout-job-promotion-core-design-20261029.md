# Shell 超时提升为后台 Job：Core 能力方案

> 状态：方案定稿；P1/P2 core 已实现，P3 remote 待实现  
> 日期：2026-10-29  
> 版本：v4（single job runtime，confirmed）  
> 适用：`bash` / `shell` / `execute_shell_command`，以及 `aicli-remote-tools` 的 remote shell  
> 参考实现：`E:\projects\ai\deepseek-harness` 的 `dsh-tool-bash`  
> 目标：把“超时命令转为后台 job”做成 **core 能力**，remote-tools 只提供 target-side 适配，不维护第二套语义。

---

## 1. 结论摘要

当前 AAR **没有** shell 超时自动转 job 的能力：

- core `BashTool` 在超时时由 process guard 杀掉命令树，并返回超时错误；
- remote-tools 的 `shell` 调用 `shellexec.Run`，超时同样 kill 整棵进程树并返回 `TIMEOUT`；
- `job_start` 是独立工具，remote-tools 的 job manager 还受 `jobs.root` / `legacyRemoteJobs` 门控；
- remote shell schema 明确不暴露 `detach/job_id/interactive`。

DSH 的关键设计不是“超时后补建 job”，而是：

> **命令启动时就登记为 job；前台调用只是等待 job。等待超时后让 job 继续运行。**

因此，AAR 不能采用“超时后重新启动命令”的实现，否则会产生重复副作用。正确实现必须把 job-first 生命周期下沉到 core shell 层。

### 1.1 方案审查修订（v2）

初版方案虽然提出了共享 `ShellJobRuntime` 接口，但仍允许：

- core 使用 `background.Manager`；
- remote-tools 保留 `remotetools.JobManager` 并另做一个 adapter；
- 两套实现分别维护存储、状态、retention、owner 和输出游标。

这与“不要开发两套作业体系”冲突。v2 明确：

1. **唯一 job runtime 是 core `internal/background.Manager`**，包括 job 生命周期、状态、输出、等待、取消、retention 和 owner 语义。
2. remote-tools 不再新增或长期维护一套 job manager；它只构造/持有 core manager 的 target-side 实例，并把 MCP 调用适配到同一个 runtime。
3. `ShellJobRuntime` 只能是 core manager 的 **薄视图接口**，用于 shell 编排和测试注入；不能成为“第二个后台实现”的抽象。
4. 现有 `remotetools.JobManager` 直接删除；旧数据自动清理，不保留只读迁移层。
5. job 工具族 `job_start/job_output/job_cancel/job_list` 必须指向同一 runtime；remote 的 job 工具只是传输/参数映射，不是独立业务实现。

### 1.2 当前组件契合度矩阵

| 现有组件 | 当前职责 | 方案处置 |
|---|---|---|
| core `background.Manager` | job store、状态、输出、取消、retention、owner/lease | **保留为唯一 job runtime；补 Wait/Forget/统一游标** |
| core `BackgroundSubmitter` | 仅 `detach=true` 的 broker seam | 删除 `detach` 专用 seam，统一到 job runtime |
| core `BashTool` | timeout 由执行器/process guard kill | 改为默认 job-first；无 runtime 时 fail closed |
| broker `job_*` | 现有 core job 控制面 | 保持不变，继续指向 core manager |
| remote `shell` | `shellexec.Run` 同步执行，timeout kill | 改为调用 core `shelljob` + target-side manager |
| remote `JobManager` | 独立 `job.json`/`output.log` 作业体系 | 直接删除；旧目录启动清理 |
| remote `job_*` tools | 独立 remote job 输出/取消/列表 | 改为 core manager 的 MCP 参数/结果映射 |
| remote `jobs.root` | remote job 目录 | 映射为 core `StorePath`/`LogDir`；旧目录自动清理 |
| DSH `tool-bash` | 参考语义：job-first + wait timeout | 只借鉴状态机，不直接复制第二套实现 |

审查结论：**当前可以复用 core 的 manager、工具契约和 broker 控制面；真正需要改的是 shell 编排和 remote transport adapter，而不是再造一个 job 系统。**

### 1.3 v3/v4 决策：直接切换，不做双轨兼容

按“减少垃圾代码、直接切换新功能”的要求，v3/v4 进一步收紧：

1. **删除 remote `JobManager` 及其全部运行时读写路径**，不保留长期只读 reader、双写或 legacy backend。
2. 旧 `jobs/<id>/job.json` / `output.log` 在升级启动时自动清理；不迁移为长期可查询的历史数据。
3. `legacyRemoteJobs` 配置和 `--legacy-jobs` 不再作为功能开关；remote job 能力统一由 core manager 提供。
4. shell **默认 job-first**：不再保留“前台直接执行 shell，超时才处理”的分叉。单条 `command` 走
   `Submit -> Wait`，`job_start` 只是不等待前台结果。
5. core runtime 必须持有**唯一共享的 background manager**；禁止 `toolbroker` 在首次
   `job_start` 时懒创建一个独立 manager（当前 `broker.go:1694`）。
6. 不允许出现第二套 process launcher：shell/job 的进程启动、process tree、输出捕获必须收敛到
   同一个执行实现；否则只是把两套作业体系换成“两套执行体系”。
7. job 必须有 owner；没有稳定 owner 的调用不能创建 job，也不能通过 job tools 读取/取消。
8. remote shell 必须有持久 `jobs.root`；缺省自动使用并创建
   `~/.aicli/remote-tools/jobs`，不使用内存 manager。

---

## 2. 当前实现证据

### 2.1 Core `BashTool`

- `backend/internal/toolkit/tools/bash.go:332-379`：解析 timeout 后直接调用 `executeCommand`。
- `backend/internal/toolkit/tools/bash.go:810-859`：`executeCommand` 建立 `context.WithTimeout`，由命令执行器和 process guard 在到期时终止命令树。
- `backend/internal/toolkit/tools/bash.go` 的 `BackgroundSubmitter` 当前只服务 `detach=true`，不是超时提升。
- `backend/internal/toolkit/tools/background_submitter.go` 只有：
  - `SubmitBackgroundShell`
  - `OpenInteractiveSession`
- core `background.Manager` 已有 `SubmitShell`、`ReadOutput`、`GetJob`、`CancelJob`，但没有一个供 shell 前台等待的 `WaitJob` 原语；broker 的 `task_output wait` 目前是在 broker 层轮询 `ReadOutput`。

### 2.2 Remote tools shell

- `backend/internal/remotetools/shell.go:136-155`：调用 `shellexec.Run(..., Timeout: timeout)`。
- `backend/internal/remotetools/shell.go:198-200`：超时显示 `Command timed out`。
- `backend/internal/remotetools/shell.go:304-316`：超时返回 `CodeTimeout`，不是 job。
- `backend/internal/remotetools/registry.go:92-99`：只有 `LegacyRemoteJobs` 才构造 remote `JobManager`。
- `backend/internal/remotetools/jobs.go:114-186`：`JobManager.Start` 是独立显式后台启动，没有前台等待接口。
- `backend/internal/remotetools/shell_conformance_test.go:14-58`：remote shell 明确禁止 `detach`、`job_id`、`interactive` 等参数。

### 2.3 Remote job 现状

`remotetools.JobManager` 与 core `background.Manager` 是两套生命周期实现：

- remote job 状态：`running` / `exited` / `killed` / `lost`
- core job 状态：`pending` / `running` / `completed` / `failed` / `timed_out` / `cancelled` / ...
- remote 用磁盘 `job.json` + `output.log`
- core 用 background store、ring/outputBuffer、owner lease、wait/notice

这正是要消除的重复体系。方案 v2 不接受“共享一个接口、保留两套存储和生命周期”的折中：

- core `background.Manager` 是唯一 job 实现；
- remote 复用同一个 manager 实现（在 target 进程内实例化）；
- legacy remote `JobManager` 不进入运行时，直接删除；
- 不允许为 promotion 新建第三套 job 生命周期。

---

## 3. DSH 参考实现

### 3.1 行为

`deepseek-harness/packages/shell/tool-bash/src/index.ts`：

- `promoteOnTimeout` 默认 `true`（第 43-52 行）。
- 有 job registry 时：
  - 前台命令也通过 `startJob` 注册（第 262-295 行）；
  - 命令以 `onExpiry: 'none'` 启动，执行器不设置自动 kill deadline（第 508、517 行）；
  - `waitOnJob` 只等待 `timeoutMs`（第 296-367 行）。
- 等待超时且进程仍运行：返回 `kind: 'promoted'`、`jobId` 和当前输出（第 340-353 行）。
- 命令在等待期间完成：删除 job record，返回普通 foreground 结果（第 355-366 行）。
- 没有 job registry、`promoteOnTimeout=false` 或 admission 失败：回退到 executor deadline kill（第 522-530 行）。
- `run_in_background=true` 使用同一 registry，并直接返回 job id（第 498-509 行）。

### 3.2 关键点

- 不是“超时后创建 job”，而是“开始即 job”。
- timeout 只约束 **等待**，不约束已经开始的进程。
- 输出使用 job 的消费游标；promoted 结果已经取走的前段不会被 `job_output` 重复返回。
- 取消调用与 timeout 必须区分：调用取消会 kill job；等待超时不会。

---

## 4. 目标与非目标

### 4.1 目标

1. 同一个 core 语义同时服务本地 `BashTool` 和 remote-tools shell。
2. timeout 后命令继续运行，并返回可操作的 job id / 游标。
3. 快速完成的命令仍返回普通前台结果，不遗留用户不可见的 job。
4. 没有 job runtime 时 shell fail closed；不保留 direct shell/kill fallback。
5. 通过 profile、tool allowlist、background/job 配置控制可见性和启用范围。
6. 不改变命令 policy、workspace jail、process-tree guard、输出折叠、artifact 的既有契约。

### 4.2 非目标

- 不在 timeout 之后重新执行命令。
- 不保留 `detach=true` 的旧 fire-and-forget 路径；显式后台统一使用 `job_start`。
- 不保留两套 job 存储；remote 直接复用 core `background.Manager` 的 target-side 实例。
- 不允许 `commands` batch 保留同步直执行旁路；首版可以明确拒绝 batch，后续再实现单 job plan。
- 不让 promotion 绕过 `readOnly`、profile、命令 allowlist 或 workspace 边界。

---

## 5. 核心设计

### 5.1 单一 Seam：`ShellJobRuntime`

在 core 定义生命周期接口，但生产实现只有一个：core `background.Manager`。接口的作用是让 shell 编排层不直接依赖 manager 类型，并便于测试注入，不是允许另写一套 job backend。

```go
type ShellJobRuntime interface {
    Start(ctx context.Context, spec ShellJobSpec) (*ShellJobRef, error)
    Wait(ctx context.Context, jobID string, timeout time.Duration) (ShellJobView, ShellWaitCondition, error)
    Read(ctx context.Context, jobID string, offset int64, limit int) (ShellJobOutput, error)
    Cancel(ctx context.Context, jobID, reason string) (ShellJobView, error)
    Forget(ctx context.Context, jobID string) error
}
```

建议类型：

```go
type ShellJobSpec struct {
    Command       string
    Workdir       string
    SessionID     string
    ToolCallID    string
    TimeoutHint   time.Duration // 写入 job metadata，不是 process deadline
    Metadata      map[string]any
    Origin        string // local | remote
}

type ShellJobRef struct {
    ID       string
    Status   string
    StartedAt time.Time
}

type ShellJobView struct {
    ID       string
    Status   string
    ExitCode *int
    Detail   string
    Finished bool
}

type ShellWaitCondition string

const (
    ShellWaitSettled ShellWaitCondition = "settled"
    ShellWaitTimeout ShellWaitCondition = "timeout"
    ShellWaitAborted ShellWaitCondition = "aborted"
)

type ShellJobOutput struct {
    Text       string
    NextOffset int64
    Truncated  bool
    Lossy      bool
}
```

实现：

- 唯一实现：`backend/internal/background.Manager` 的 core adapter。
- core `BashTool` 和 remote `remoteShellTool` 都调用同一个 `ShellJobRuntime` 实现。
- remote-tools 在 target 进程内构造 core background manager（使用 target-local store/log 配置），不构造第二套 job manager。
- `remotetools.JobManager` 不存在于新架构；旧数据由启动清理删除。
- `BackgroundSubmitter` 不再作为独立 detach 分支；显式后台统一由 `ShellJobRuntime`/`job_start` 承担。

### 5.2 单一编排器：`shelljob.Promote`

新增 core 包（例如 `backend/internal/shelljob/`），负责状态机；`BashTool` 和 `remoteShellTool` 只负责：

- 参数解析；
- policy / workdir / jail；
- 调用 `shelljob.Promote`；
- 将通用结果格式化为各 surface 的 `ToolResult`。

```go
type PromoteOptions struct {
    WaitTimeout   time.Duration
    CaptureLimit  int
    SessionID     string
    ToolCallID    string
}

type PromoteResult struct {
    Kind        PromoteKind // foreground | promoted
    JobID       string
    Output      ShellJobOutput
    ExitCode    *int
    Duration    time.Duration
    TimedOut    bool
    Canceled    bool
}
```

伪代码：

```go
if runtime == nil {
    return jobRuntimeUnavailable()
}

job, err := runtime.Start(ctx, spec)
if err != nil {
    // admission/start failure: 回退现有 kill path
    return jobStartFailed(err)
}

view, condition, err := runtime.Wait(ctx, job.ID, opts.WaitTimeout)
if err != nil {
    _, _ = runtime.Cancel(ctx, job.ID, "wait failed")
    return PromoteResult{Kind: PromoteCanceled}, err
}

switch condition {
case ShellWaitSettled:
    out, _ := runtime.Read(ctx, job.ID, 0, opts.CaptureLimit)
    _ = runtime.Forget(ctx, job.ID)
    return foregroundResult(view, out)

case ShellWaitTimeout:
    out, _ := runtime.Read(ctx, job.ID, 0, opts.CaptureLimit)
    return promotedResult(job.ID, out, opts.WaitTimeout)

case ShellWaitAborted:
    _, _ = runtime.Cancel(ctx, job.ID, "tool call aborted")
    return canceledResult()
}
```

### 5.3 为什么不能“超时后再提升”

超时发生时，现有 `shellexec.Run` 已经通过 process guard 杀掉进程树。此时：

- 进程已经不存在；
- 输出管道已经关闭；
- 重新启动会产生重复副作用（例如重复部署、重复写文件、重复发送请求）。

因此只有两种正确选择：

1. **job-first**：一开始就交给 job runtime；
2. **fail closed**：没有 job runtime 时 shell 不可执行。

不能采用“timeout 后把死进程登记为 job”的做法，也不能保留 timeout-kill 的第二条执行路径。

### 5.4 快速完成与 job 清理

- 前台等待期间完成：`Forget(jobID)`，模型只看到普通结果，不看到 job id。
- timeout：job 保留，返回 job id。
- 调用取消：`Cancel(jobID)`，返回取消结果，不留下运行中的 job。
- `Forget` 必须只删除已终态且当前调用拥有的记录；不能删除其他 session 的 job。

### 5.5 输出与游标

promoted 结果必须携带：

```text
job_id
next_offset
timed_out
wait_timeout_ms
partial output
next_action: job_output job_id=<id> offset=<next_offset>
```

要求：

- `Read` 返回的 `next_offset` 是 job 输出文件/ring 的权威游标；
- `job_output` 使用同一游标，不重复返回 promoted 结果已经交给模型的前段；
- 输出折叠、artifact 归档、截断标记继续由 shell surface 负责；
- promoted 结果本身不是 `TIMEOUT` 错误；它是“命令仍在运行”的内容结果；
- 命令随后以非零退出结束时，按现有非零退出内容结果语义处理，不把退出码变成 job 启动错误。

### 5.6 状态机

```text
runtime 不可用 / 初始化失败
  -> shell 不可见或 fail closed；不提供直执行 fallback

runtime 可用
  -> Submit job
     -> admission/start failed -> 返回可修复错误；不重复启动
     -> Wait(wait timeout)
        -> settled -> Forget -> foreground result
        -> timeout -> promoted result (job keeps running)
        -> caller canceled -> Cancel -> canceled result
        -> wait/runtime error -> Cancel/Forget (best effort) -> error
```

### 5.7 Shell 执行模型：默认就是 job

v3 不保留“前台直执行 + 超时才处理”的分支：

```text
shell call
  -> Submit(core job runtime)
  -> Wait(wait timeout)
     -> completed -> foreground result + Forget
     -> timeout   -> promoted result + job_id/next_offset
     -> canceled  -> job_cancel + canceled result
```

- **所有 shell 调用默认走 job**；前台只是调用方等待 job。
- `job_start` 是同一入口的“不等待”形式，不是另一套后台实现。
- `timeout` 只限制 `Wait`，不是进程 deadline；不再生成 timeout-kill 分支。
- 若用户要停止已 promoted 的命令，调用 `job_cancel`。
- shell 工具不再直接调用 `shellexec.Run`；`shellexec` 只能作为 job runtime 的进程执行层。
- `commands` 批量执行不能保留旧的同步旁路：要么实现为同一个 job plan，要么在
  batch job 方案完成前明确拒绝/下线 batch；不能偷偷跑 direct path。

---

## 6. 配置与 Profile 门控

### 6.1 建议配置

v3 不再提供 `promoteOnTimeout` 开关：shell 默认就是 job，不需要一个“是否提升”的分叉。

只保留 job runtime 本身的配置：

```yaml
background:
  storePath: <可选；为空则使用内存 manager>
  logDir: <可选；job 输出日志目录>
  defaultTimeout: 0      # shell job 默认不设进程总时限；需要总时限时单独配置
  retention: 720h
  cleanupInterval: 1h
```

remote-tools 复用 job runtime 配置，但用 `jobs.root` 作为 target 本地根：

```yaml
jobs:
  root: C:\Users\vince\.aicli\remote-tools\jobs  # 缺省值；省略也会自动使用
  retention: 168h
```

- core runtime 必须构造并持有**一个共享** background manager；
- 未配置持久 store 时，manager 使用内存模式；命令仍走 job，但重启后无法恢复；
- remote 必须使用持久 `jobs.root`；省略时自动使用并创建
  `~/.aicli/remote-tools/jobs`，不使用内存 manager；
- `jobs.root` 不存在时启动阶段尝试 `MkdirAll`；显式配置但不可创建/不可写则启动失败；
- shell 的既有 `timeout` 参数保留，含义改为 job 的等待预算，不再是进程 deadline。
- job 自身的 `defaultTimeout` 是独立的执行时限；如果设置，它必须在结果中明确区分于
  shell wait timeout。默认应为 0（不设 job 总时限），避免 promoted job 被后台默认时限再次杀掉。

建议 CLI 不再新增 `--promote-shell-timeout`；只保留 background/job store 配置。

### 6.2 Profile 门控

- `shell` 仍只在 `ops` / `all` 或其他显式允许 shell 的 profile 可用；
- `readonly` / `readOnly=true` 永远不暴露 shell；
- job 控制工具必须可用：
  - core：`job_output` / `job_cancel` / `job_list` 的现有 profile 门控；
  - remote：`jobs.root` 可用时，至少暴露 `job_output`、`job_cancel`、`job_list`；
- `tools:` 仍然能收窄最终工具面；job-first 不等于绕过工具白名单；
- `job_start` 是“提交但不等待”的入口，和 shell 共用 job runtime，不新增第二套后台工具。

### 6.3 remote-tools 配置变化

- `jobs.root` 是 target 本地 job store/log 根，直接映射 core manager；
- `jobs.root` 省略时使用默认目录 `<user-home>/.aicli/remote-tools/jobs`，并在启动时自动创建；
- 显式配置 `jobs.root` 时也尝试创建；创建或写探针失败则启动失败；
- `legacyRemoteJobs` / `--legacy-jobs` 删除，不再作为开关；
- 旧 `jobs/<id>/job.json` 由启动清理逻辑处理，不参与新 job 读写；
- 不提供内存 manager 作为 remote 长期方案；remote shell/job tools 依赖持久 root；
- job tools 的 expose 条件仍受 profile/tools 收窄。

### 6.4 配置错误策略

建议：

- 无法构造 job runtime：**shell 工具不可见或启动失败**；不提供直执行 fallback；
- 配置了 `background.storePath` 或 remote `jobs.root` 但不可写：启动失败；
- `readOnly=true`：shell 不可见，job runtime 可继续供其他工具使用；
- 不允许“一边 job、一边同步 shell”的半可用状态。

---

## 7. Core 与 Remote 的代码落点

### 7.1 Core `BashTool`

涉及文件：

- `backend/internal/toolkit/tools/bash.go`
- `backend/internal/tools/background_seam.go`
- `backend/internal/background/manager.go`
- `backend/internal/toolbroker/broker.go`
- `backend/internal/toolcore/shellexec` 及其 process guard
- `backend/internal/config/manager.go`（background/job store 配置）

建议改动：

1. **所有 shell 调用走 job**。`BashTool` 不再保留 `executeCommand` 直执行分支；单条命令只做
   policy/workdir 解析后 `Submit -> Wait`。
2. runtime 必须持有唯一共享的 `background.Manager`：
   - 删除 `toolbroker` 在 `job_start` 时懒创建 manager 的分支（当前 `broker.go:1694`）；
   - `buildLocalChatBackgroundManager` 不能因未配置 store 返回 nil；无持久 store 时仍构造内存 manager；
   - 无持久 store 时使用内存 manager，但仍是同一个实现；
   - 不能由每个 tool 或每个请求各自 new 一个 manager。
3. 进程执行层收敛：
   - `background.Manager` 使用与 shell 相同的 `shellexec`/process guard 启动和捕获进程；
   - `BashTool` 不再直接调用 `shellexec.Run`；
   - 删除重复的 shell 启动、process tree、输出捕获代码。
4. `background.Manager` 补齐 `Wait` 和 `Forget`：
   - `Wait` 统一由 manager 提供，不在 broker/shell/remote 各写轮询；
   - `Forget` 删除已终态且当前调用拥有的快速完成 job；
   - `job_start` / `job_output` / `job_cancel` / `job_list` 全部指向同一个 manager。
5. `commands` 批量：
   - 不能保留旧的同步直执行旁路；
   - 必须实现为单个 job plan，或在方案确认前明确拒绝 batch；
   - 不能出现“单条走 job、批量走旧路径”的双执行体系。
6. 删除 `detach=true` 的旧 fire-and-forget 分支；显式后台统一使用 `job_start`。

### 7.2 Remote `remoteShellTool`

涉及文件：

- `backend/internal/remotetools/shell.go`
- `backend/internal/remotetools/jobs.go`
- `backend/internal/remotetools/registry.go`
- `backend/internal/remotetools/config.go`
- `backend/internal/remotetools/jobs_tools.go`

建议改动：

1. `remoteShellTool` 持有 **core `ShellJobRuntime`**（由 target-side `background.Manager` 适配），不再持有 `remotetools.JobManager`。
2. remote-tools 在目标进程内构造 `background.Manager`：
   - `StorePath` 映射到 `jobs.root/background.db`；
   - `LogDir` 映射到 `jobs.root/logs`；
   - `jobs.root` 缺省为 `<user-home>/.aicli/remote-tools/jobs`；启动时 `MkdirAll` 并验证可写；
   - 不使用内存 manager 作为 remote 方案；
   - 使用 job runtime 的 `SubmitShell` / `ReadOutput` / `CancelJob` / `ListJobs`；
   - 不把 core manager 暴露给 MCP 客户端，只通过 remote adapter 映射参数和结果。
   - `internal/background` 当前不导入 `internal/toolkit` 或 `internal/remotetools`，没有包循环；
     remote-tools 可以直接复用其 manager。
3. `remotetools.JobManager` 直接删除：
   - 删除 `jobs.go` 的独立 job store/lifecycle；
   - 删除旧 `job.json` / `output.log` 的运行时读取；
   - 旧目录由启动清理逻辑一次性清空；
   - 不保留 legacy reader、兼容层或双写。
4. `buildRegistry` 在 `Jobs.Root` 可用时构造 core background manager；job tools 的门控同步扩展。
5. `shell` schema 不新增 `detach/job_id` 参数；job-first 是执行语义，不是调用参数。
6. remote shell 输出继续复用现有 folding/artifact 逻辑；promoted 结果单独增加 `job_id`、`next_offset`、`timed_out` 元数据。
7. remote `Server.Close` 必须关闭 core manager 的 dispatch goroutine、heartbeat/reaper 和 store；
   不能像零散的文件 job 一样只删除目录而留下后台 goroutine。
8. 启动时自动清理旧 remote job 数据：
   - 先按旧记录中的 PID/进程树做 best-effort 终止；
   - 再删除 `jobs/<id>` 目录；
   - 清理失败只告警，不回退到旧 `JobManager`。

remote `jobs_tools.go` 的 `job_start/job_output/job_cancel/job_list` 只做 MCP 参数/结果映射：

- `job_start` -> core `SubmitShell`
- `job_output` -> core `ReadOutput`（保留 offset；不再使用 `offset=0` 的 tail 默认）
- `job_cancel` -> core `CancelJob`
- `job_list` -> core `ListJobs`

这保证 MCP 的 job 工具和 core 的 broker job 工具共享同一份 job 状态，而不是各写一套。

### 7.3 共享编排器

新增包建议：

```text
backend/internal/shelljob/
  promote.go
  types.go
  promote_test.go
```

职责：

- 定义 `ShellJobRuntime` / `ShellJobSpec` / `ShellJobView` / `ShellWaitCondition`；
- 实现 job-first + wait-timeout 状态机；
- 不包含任何具体存储、HTTP、本地文件或 MCP 代码；
- 不复制 `toolcore` 工具实现；它是生命周期 helper，不是第二套 shell 工具。

约束：不得在 `toolcore` 新增第二套 shell/job 工具，也不得让 remote-tools
复制一份 promotion 状态机。toolcore 只保留现有工具契约；共享编排逻辑放在
core helper 包中。

Core 与 remote 只提供 **runtime 配置/注入** 和 surface formatter；remote 不再提供第二套 job backend。target-side manager 通过构造参数接入 core `background.Manager`，本地和远端行为由同一实现决定。

---

## 8. 安全与生命周期不变量

### 8.1 启动前

- 命令必须通过现有 `CheckCommand` / allowlist / denied / destructive gate；
- workdir 必须通过 workspace jail / allowed roots；
- `readOnly` 必须在 shell 可见性阶段拒绝；
- job 启动失败不得回退到“无 policy 的直接执行”。

### 8.2 运行中

- job 的 process tree 由 job runtime 管理；
- timeout 只结束 wait，不 kill process；
- 调用取消必须 `Cancel`，并等待 job 进入终态或由 runtime 负责收敛；
- shell 工具的 context 不能直接成为 job 进程的 deadline；job 自身的 `defaultTimeout` 必须独立配置并在结果中区分；
- 必须保留 process guard 的树杀能力；promotion 不能绕过它。

### 8.3 结束后

- 快速完成：`Forget` 后不留 token 不可见的 job；
- promoted：job 必须可由 `job_output` 读取，且 cursor 可续；
- 终态 job 保留策略只由 core `background.Manager` 的 retention 决定；remote target 复用同一配置；
- 旧 `remotetools.JobManager` 的 `job.json` 不进入运行时；启动清理时终止遗留进程并删除目录；
- audit 必须记录：
  - `promotion=shell_timeout`
  - `job_id`
  - `wait_timeout_ms`
  - `session_id` / `tool_call_id`
  - 原命令摘要（遵循现有敏感信息策略）

### 8.4 Ownership

- job 必须绑定 session / owner；
- 不允许其他 session 读取或取消；
- `Forget`/`Cancel` 必须校验 owner；
- remote target 使用 core manager 的 instance/lease/owner 语义；不允许用 remote adapter 自定义一套降级规则。

### 8.5 Owner 模型细节

core `background.Job` 已经提供：

- `SessionID`：逻辑 owner（谁可以读/取消/列表）；
- `OwnerInstanceID`：拥有 job 的 runtime/target 实例；
- `LeaseExpiresAt` + heartbeat：实例失联后由 recovery 处理；
- `StateVersion`：CAS 防止旧写入覆盖终态。

shell job 必须沿用这套模型，而不是新造 `owner` 字段：

1. `ShellJobSpec.SessionID` 由调用上下文注入；core `BashTool` 使用当前 agent/session；
2. remote `RequestInfo.SessionID` / principal 映射为逻辑 owner；
3. `OwnerInstanceID` 由目标 runtime 的 manager 生成并维护 lease；
4. job 的 `job_output` / `job_cancel` / `job_list` 必须按 owner 过滤；
5. remote HTTP 的无 session/stateless 请求不能创建 unowned job，必须 fail closed；
   客户端若要重连续读 job，必须复用稳定 logical session/owner（不能在每次请求生成随机 owner）；
6. 旧 legacy `job.json` 没有可靠 owner，不能导入 core store；启动清理时删除。

这是一个必须确认的架构决策：remote transport 是否有稳定的 session/owner 来源。如果没有，
应先实现稳定的 owner 传递，再开放 remote shell job；不能退化成“所有 token 持有者共享 job”。

---

## 9. 主要失败路径

| 场景 | 行为 |
|---|---|
| job runtime 不可用 | shell 不可见或 fail closed；不提供 direct shell fallback |
| job start/admission 失败 | 返回可修复错误；不重复启动、不回退直执行 |
| 等待超时 | 返回 promoted + job id + cursor；进程继续运行 |
| job 在等待期间完成 | 快速返回 foreground 结果；Forget job |
| 调用取消 | Cancel job；返回取消结果；不报 timeout |
| job_output 读取失败 | 保留 job id，返回可重试错误；不伪造输出 |
| 输出超过预算 | 沿用现有 folding/artifact；promoted 结果带截断元数据 |
| job store 不可写 | 启动失败；不 fallback 到 legacy；remote 不使用内存作为长期 store |

---

## 10. 兼容与迁移

### 10.1 直接切换

不保留双轨语义：

- shell 默认总是 job；不存在“直执行 / 超时才提升”的开关；
- `timeout` 只作为 job wait 预算；超时返回 promoted，不再返回 timeout-kill；
- shell schema 仍不暴露 `detach/job_id`；
- `job_start/job_output/job_cancel/job_list` 从第一天起使用 core manager；
- remote `legacyRemoteJobs`、`--legacy-jobs` 和旧 `JobManager` 不保留。

### 10.2 API 变化

- `shell` 正常完成仍返回普通结果；
- 等待超时返回 promoted 结果，metadata 包含 `job_id`、`timed_out`、`wait_timeout_ms`、`next_offset`；
- 不新增 shell 参数；
- `job_output` 使用 core `ReadOutput` 的 offset/next_offset 语义读取 promoted job；
- 显式后台使用 `job_start`，和 shell 共用同一个 manager。

### 10.3 文档/提示词

- shell 描述直接说明“所有调用都是 job；timeout 只结束等待”；
- 系统提示词/工具描述增加：
  - `job_output` 使用 offset，不重复轮询；
  - timeout 后用 returned job id 继续读取；
  - 需要立即回到模型时使用 `job_start` 或等待 completion；
- remote-tools 用户手册删除 promotion 开关、legacy job 和双轨说明，改为单一 job 模型。

### 10.4 历史数据与旧代码清理

不保留旧 remote job 运行时：

1. 删除 `backend/internal/remotetools/jobs.go` 的 `JobManager` 和旧 `job.json` 读写；
2. 启动时扫描旧 `jobs/<id>/job.json`：
   - 按记录 PID/进程树 best-effort 终止遗留进程；
   - 删除旧目录和输出文件；
   - 清理失败只告警，不 fallback 到旧 manager；
3. 删除 `legacyRemoteJobs` / `--legacy-jobs` 配置和帮助文本；
4. 新 job id 全部由 core manager 分配，旧 id 不复用；
5. core manager 的 retention/cleanup 负责新 job 的自动清理。

---

## 11. 测试矩阵

### 11.1 Core

- `TestBashTimeoutPromotesAndLeavesJobRunning`
- `TestBashFastCommandForgetsJob`
- `TestBashUsesSingleSharedJobRuntime`
- `TestBashPromotedOutputCursorDoesNotRepeat`
- `TestBashCancelKillsPromotedJob`
- `TestBashBatchUsesOneJobPlanOrIsExplicitlyRejected`
- `TestBashPromotionPreservesSandboxCommandPolicy`
- `TestBashPromotionPreservesWorkdirAndAllowedRoots`
- `TestBashPromotionUsesExistingOutputFoldingAndArtifact`

### 11.2 Remote

- `TestRemoteShellTimeoutPromotesToJob`
- `TestRemoteShellFastCommandLeavesNoJob`
- `TestRemoteShellPromotionRequiresJobsRoot`
- `TestRemoteShellPromotionRespectsReadOnlyAndProfile`
- `TestRemoteShellPromotedJobOutputContinuesFromOffset`
- `TestRemoteShellPromotionCancelKillsJob`
- `TestRemoteShellJobToolsUseCoreManager`
- `TestRemoteShellSchemaDoesNotAdvertiseJobArgs`（保持）
- `TestRemoteShellPromotionUsesWorkspaceJail`
- `TestRemoteJobToolsUseCoreBackgroundManager`
- `TestRemoteLegacyJobsPurgedOnStartup`
- `TestRemoteCreatesOnlyOneJobStore`
- `TestRemoteJobOutputUsesCoreCursor`

### 11.3 回归

- 现有 core bash/shell 测试全量通过；
- remote-tools shell/job/conformance 全量通过；
- `job_start` / `job_output` / `job_cancel` 既有测试不变；
- race 测试覆盖 job wait/cancel/forget 与 supervisor 的并发；
- 至少一次 Windows + 一次 Unix 端到端验证。

---

## 12. 分阶段实施

### P0：本文档

- 确认 shell 默认 job、owner 来源、remote store 要求、batch 策略；
- 不改代码。

### P1：Core job runtime seam

- 定义 `ShellJobRuntime` / `ShellJobSpec` / `ShellJobView`；
- 为 `background.Manager` 实现唯一 adapter（不是第二套 backend）；
- 增加 `Wait`（推荐）或复用 `ReadOutput` 长轮询；
- 增加 `Forget`；
- job tools 仍指向同一个 manager；
- 去掉 broker 懒创建 manager，统一由 runtime 注入；
- 补单元测试和 race 测试。

### P2：Core shell promotion

- `BashTool` 默认 job-first，不再保留 direct shell 分支；
- 单条命令 Submit -> Wait；timeout 返回 promoted；
- `background.Manager` 使用统一进程执行层；
- 删除 `detach=true` 旧分支；
- 补 core 测试和文档。

### P3：Remote tools adapter

- `remoteShellTool` 复用 `shelljob.Promote`；
- remote-tools 在 target 进程构造 core `background.Manager`；
- `jobs.root` 缺省自动使用并创建 `~/.aicli/remote-tools/jobs`；
- `job_*` tools 只做 MCP 参数/结果映射到 core manager；
- 删除 `remotetools.JobManager` 及其旧 job 读写；
- 启动时自动清理旧 `jobs/<id>` 目录和遗留进程；
- 删除 `legacyRemoteJobs` / `--legacy-jobs`；
- 扩展 registry/config/profile/job tools；
- 补 remote 测试和用户手册。

### P4：发布与默认值评估

- 收集 job-first shell 的耗时、promoted 比例、job_output 使用和残留 job 数据；
- 校准 wait timeout 默认值和 job retention；
- 明确升级迁移、legacy 数据清理和回滚策略（直接回到旧版本二进制，不做运行时双轨开关）。

---

## 13. 已确认决策

以下按“直接切换、不保留双轨”确认：

1. **shell 默认 job**：取消 direct shell 路径，所有 shell 调用都经过 core manager。
2. **batch**：`commands` batch 实现为单个 job plan；不保留同步旁路。
3. **无 job runtime**：shell 直接不可见/启动失败；fail closed，不做 kill fallback。
4. **快速完成 job**：使用 `Forget` 删除，不出现在普通 `job_list`。
5. **owner 来源**：稳定 MCP logical session id + target instance id；
   HTTP stateless/无 session 时 fail closed，不允许 unowned job。
6. **remote 持久化**：remote shell 必须有 `jobs.root`；缺省自动创建
   `<user-home>/.aicli/remote-tools/jobs`，不使用内存 manager。
7. **单进程执行层**：`shellexec`/process guard 是 manager 的唯一 launcher；
   删除 manager 自带的第二套 `exec.Command` 路径。
8. **历史数据清理**：启动时自动终止遗留 legacy jobs 并删除 `jobs/<id>` 目录；
   接受这是破坏性操作。

---

## 14. 验收标准

实现完成后必须满足：

- [ ] 所有 shell 调用默认走 job；不存在 direct shell / timeout-kill 旁路；
- [ ] timeout 命令不会在 timeout 时被 kill，而是继续作为 job 运行；
- [ ] promoted 结果包含 job id 和可续读 cursor；
- [ ] 快速完成命令不留下模型不可见的 job；
- [ ] 调用取消会终止 job，并区别于 timeout；
- [ ] 无 job runtime 时 shell fail closed，不存在 direct shell fallback；
- [ ] core 与 remote 共用 promotion 状态机，remote 不复制语义；
- [ ] 只存在一个 job runtime/store；remote `JobManager` 不再承接新 job；
- [ ] remote job tools 与 core broker job tools 读取同一份 job 状态；
- [ ] shell/job 只有一个进程 launcher（shellexec/process guard）；
- [ ] 每个 job 都有可验证的 owner；无 owner 时创建/读取/取消失败；
- [ ] 旧 remote job 数据在启动后自动清理，不保留 legacy reader；
- [ ] remote `jobs.root` 缺失时自动使用并创建 `~/.aicli/remote-tools/jobs`；
- [ ] remote 没有持久 job store 时不暴露 shell/job，不使用内存 manager；
- [ ] profile / tools / readOnly / command policy / workspace jail 全部保持；
- [ ] 文档、工具描述、测试和迁移说明同步完成。

---

## 15. 代码索引

| 区域 | 文件 |
|---|---|
| core shell | `backend/internal/toolkit/tools/bash.go` |
| core background seam | `backend/internal/toolkit/tools/background_submitter.go` |
| core background adapter | `backend/internal/tools/background_seam.go` |
| core background manager | `backend/internal/background/manager.go` |
| broker wait semantics | `backend/internal/toolbroker/broker_task_wait.go` |
| remote shell | `backend/internal/remotetools/shell.go` |
| remote legacy jobs（删除） | `backend/internal/remotetools/jobs.go` |
| remote registry | `backend/internal/remotetools/registry.go` |
| remote config | `backend/internal/remotetools/config.go` |
| remote shell contract test | `backend/internal/remotetools/shell_conformance_test.go` |
| DSH reference | `E:\projects\ai\deepseek-harness\packages\shell\tool-bash\src\index.ts` |
| DSH shell contract | `E:\projects\ai\deepseek-harness\packages\shell\shell\src\types.ts` |
