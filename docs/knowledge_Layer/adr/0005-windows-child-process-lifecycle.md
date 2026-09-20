# ADR-0005: Windows 子进程树生命周期与复用既有 process guard

- **Status**: Proposed
- **Date**: 2026-09-20
- **Deciders**: 项目 owner
- **Gate**: `Phase4-start`
- **Reversibility**: moderate（需接线，但不需要新机制）
- **Supersedes**: `supplement/05` §9.5 的"Job Object 还是 `taskkill /T`"提问
- **Related**: `backend/internal/executor/process_guard_windows.go`、`process_guard_other.go`、`process_guard.go`、`detach.go`、`detach_windows.go`、`detach_other.go`；`backend/internal/mcp/transport/stdio_tree.go`、`stdio_tree_windows_test.go`、`stdio_tree_test.go`；`backend/internal/background/detached.go` L748；`backend/internal/runtimeserver/service_launch_windows.go`、`service_control.go` L294；`backend/scripts/acp_e2e_mcp_parent_kill.go`；`backend/internal/winconsole/`；`supplement/05` §9.5、附录 F

---

## 1. Context

### 1.1 已核实的证据（关键：**本问题在仓库中已被回答**）

`supplement/05` §9.5 问：

> Windows 进程树清理用 Job Object 还是 `taskkill /T`？（影响 `internal/winconsole` 是否被复用）

**证据 1 — 仓库已经用 Job Object 作为主机制，`taskkill /T` 只作降级回退。**

`backend/internal/executor/process_guard_windows.go`：

```go
// platformGuard tracks a Windows command tree with a Job Object. The job is
// ... TerminateJobObject kills every descendant in one call.       // L17–19
job, err := windows.CreateJobObject(nil, nil)                       // L33
windows.SetInformationJobObject(..., windows.JobObjectExtendedLimitInformation, ...) // L44–46
cmd.SysProcAttr.HideWindow = true                                   // L62
cmd.SysProcAttr.CreationFlags |= windows.CREATE_NEW_PROCESS_GROUP   // L63
windows.AssignProcessToJobObject(p.job, handle)                     // L80
windows.TerminateJobObject(p.job, 1)                                // L91

// terminateWindowsFallback kills the tree via taskkill, then the direct child.  // L102
exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid))     // L107
rep.Mode = "taskkill"                                               // L109
```

结论：**两者都有，但职责不同**——Job Object 是正常路径，`taskkill /T` 是绑定失败时的 fallback。

**证据 2 — 这是一套可复用的守卫，不是一次性代码。**

- `process_guard.go` L40：`tree termination on timeout/cancel (Windows Job Object, Unix process group)`
- `process_guard.go` L88：`process-tree tracking (Windows: Job Object; Unix: process group)`
- `process_guard.go` L101：降级时用 `(taskkill / direct kill)` 而不是让命令失败
- `process_guard_other.go` L26：Unix 侧用 `SysProcAttr.Setpgid = true`
- `mcp/transport/stdio_tree.go` L22：`Windows 用 Job Object + JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE：只要本进程...`
- `mcp/transport/stdio_tree.go` L65：ctx 取消 / Close 时按进程组或 `taskkill` 收树，并记录降级原因
- `mcp/transport/stdio_tree.go` L139/L154：`treeGuardReport` 返回终止报告（含 Job Object 绑定失败等降级信息）

**证据 3 — 已有端到端测试证明"父死子亡"成立。**

- `backend/scripts/acp_e2e_mcp_parent_kill.go` L6：`ProcessGuard. On Windows that guard is a Job Object created with ...`
- 同文件 L777：`(Windows Job Object with KILL_ON_JOB_CLOSE) fails to tear it down.`
- 同文件 L800：断言 `MCP child pid=... survived the agent hard kill (Windows Job Object with KILL_ON_JOB_CLOSE must tear the tree down)`
- 同文件 L803：成功时打印 `OK MCP child pid=... reaped by the Job Object after the agent hard kill`
- `mcp/transport/stdio_tree_windows_test.go` L41：用 Toolhelp 快照枚举直接子进程，作为**独立于 Job Object** 的验证手段
- `stdio_tree_windows_test.go` L195 / `stdio_tree_test.go` L97/L103：断言 Job Object 绑定成功

**证据 4 — 存在"故意脱离 Job Object"的既有概念。**

`executor/detach.go` L29：`started outside the per-command Job Object and without inheriting the ...`
`executor/detach_windows.go` L21：`The process is never assigned to the per-command Job Object, so timeouts, ...`
`executor/detach_other.go` L28：`Sys: &syscall.SysProcAttr{Setsid: true}`

即：**"是否纳入 Job Object"是一个已被显式建模的决策维度**，不是隐式行为。

**证据 5 — 其它子进程路径也各自处理了同一问题。**

- `background/detached.go` L748：`exec.Command("taskkill", "/PID", ..., "/T", "/F")`
- `runtimeserver/service_launch_windows.go` L14–16：`HideWindow: true` + `CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP`
- `runtimeserver/service_control.go` L294：`exec.Command("taskkill", "/PID", ..., "/F")`

说明这是**跨子系统反复出现**的需求，各处以不同成熟度实现。

**证据 6 — `winconsole` 与进程树清理是两件不同的事。**

`backend/internal/winconsole/` 只有三个文件：`console_utf8_other.go`、`console_utf8_windows.go`、`console_utf8_test.go`。
其职责是 **UTF-8 控制台代码页**（避免 Windows 上中文输出乱码），
**不是**进程树生命周期。`supplement/05` §9.5 把"是否复用 `winconsole`"与"用哪种收树机制"绑在一个问题里，
**是一个概念混淆**——两者无依赖关系。

### 1.2 问题陈述

Phase 4 要引入 LSP 子进程（`gopls` / `volar` / `typescript-language-server`）与可能的 parser 子进程。
这些进程：

- 会自行 spawn 子进程（`gopls` 会调用 `go list`、`go env`）；
- 若不被正确收树，会在 aicli 崩溃/被强杀后**成为孤儿并继续持有文件锁与内存**；
- 在 Windows 上尤其严重（无进程组信号语义，孤儿进程不会被自动回收）。

真实风险场景（已有先例）：`scripts/acp_e2e_mcp_parent_kill.go` 测试的正是
"agent 被硬杀后 MCP 子进程是否残留"。

### 1.3 风险不对称

| 方向 | 失败形态 | 代价 |
|---|---|---|
| 不复用既有守卫，新写一套 | 重复实现、行为漂移、两套降级语义 | **高**（且违反 `adr/README.md` §7"不复制"原则） |
| 复用既有守卫 | 需理解 `processGuard` 接口与 `detach` 语义 | **低**（一次性阅读成本） |
| 不纳入守卫（裸 `exec.Command`） | 孤儿进程、文件锁泄漏、构建被拖慢 | **高**（需重启机器） |

---

## 2. Decision Drivers

| # | 判据 | 可检验形式 |
|---|---|---|
| D1 | 不得新写第二套进程树机制 | 代码中不存在 knowledge 专属的 Job Object 创建代码 |
| D2 | aicli 崩溃/强杀后不得残留 LSP 孤儿 | 复用 `acp_e2e_mcp_parent_kill.go` 的验证手法，扩展断言 LSP pid |
| D3 | 降级路径必须可观测 | 沿用 `treeGuardReport` / `terminateWindowsFallback` 的 `rep.Mode` |
| D4 | 不得把 `winconsole` 混入进程树问题 | `winconsole` 只负责代码页；本 ADR 不引用它作依据 |
| D5 | 必须能表达"故意脱离"（如未来的守护进程） | 沿用 `detach.go` 的既有语义，不另造 |
| D6 | Unix 与 Windows 行为对称 | 复用 `process_guard_other.go` 的 `Setpgid` 路径 |
| D7 | 不得在 Phase 4 之前引入 | Gate = `Phase4-start` |

---

## 3. Considered Options

| 选项 | 描述 | 优点 | 代价 |
|---|---|---|---|
| **A** | 在 knowledge 内新写一套 Windows Job Object 收树 | 自包含 | 违反 D1；与 `executor`/`mcp` 行为漂移；三处降级语义 |
| **B** | knowledge 的子进程一律用 `taskkill /T` 收 | 简单、无平台 API | 违反 D2：`taskkill` 只在终止时执行，**崩溃/强杀时不会触发**，孤儿仍残留 |
| **C** | 复用 `internal/executor.processGuard`（Job Object + `taskkill` fallback） | 满足 D1/D2/D3/D6 | 需接线；需确认接口对长驻进程友好 |
| **D** | 复用 `internal/mcp/transport` 的 `treeGuard` | 已有 `KILL_ON_JOB_CLOSE` 与诊断报告 | 该守卫面向 stdio 传输，语义偏"随连接生死"，对长驻 LSP 需评估 |
| **E** | 把 LSP 放到独立 runtime-server 服务里托管 | 隔离最好 | 违反 `supplement/05` §8"不新增独立服务"；Phase 4 过重 |
| **F** | 让 LSP 进程 detach（脱离 Job Object）以避免被误杀 | 避免误杀 | 违反 D2：正好制造孤儿 |

---

## 4. Decision

采纳 **选项 C**，并做四项配套。

### 4.1 统一复用 `internal/executor` 的进程守卫

- knowledge 的 LSP / parser 子进程**必须**通过 `internal/executor` 的 `processGuard` 启动。
- **禁止**在 `backend/internal/knowledge/` 内出现 `windows.CreateJobObject` /
  `AssignProcessToJobObject` / `SysProcAttr` 的直接使用（可用 lint 或 review 检查，D1）。
- Windows：Job Object + `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`
  （沿用 `mcp/transport/stdio_tree.go` L22 已确立的语义）。
- Unix：`SysProcAttr.Setpgid`（沿用 `process_guard_other.go` L26）。
- 终止顺序：先 `TerminateJobObject`（一次收全树），失败再 `taskkill /T /F /PID`，
  最后 fallback 直接 kill 子进程。**沿用既有顺序，不重新设计**（D3）。

### 4.2 崩溃场景的保证

Job Object 用 `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`，
即**只要宿主进程消失（含被强杀），内核回收 job handle 并杀掉整棵树**。
这是 `taskkill` 方案**无法**提供的保证，也是选 C 而非 B 的决定性理由（D2）。

> 注意：这条保证**只在子进程被 `AssignProcessToJobObject` 成功时成立**。
> 因此必须像既有代码一样**检查绑定结果并记录 `rep.Mode`**，不得静默忽略绑定失败。

### 4.3 降级必须可观测

沿用 `treeGuardReport` / `rep.Mode`：

| `Mode` | 含义 | 后续动作 |
|---|---|---|
| `job` | Job Object 绑定成功，正常路径 | 无 |
| `taskkill` | 绑定失败，用 `taskkill /T` 收树 | 记 warning；`/knowledge status` 暴露 |
| `direct` | 两者都失败，只杀直接子进程 | 记 error；视为已知风险状态 |

**不得**在降级时静默继续。`supplement/05` 附录 F 已把 Windows 进程树列为"已知高风险点"，
降级可观测是把它从"未知风险"变为"已知并计量"的必要条件。

### 4.4 明确不纳入的进程

| 进程 | 是否纳入 Job Object | 理由 |
|---|---|---|
| LSP server | **是** | 生命周期与 owner 绑定（`supplement/05` §1.3） |
| parser / tree-sitter 子进程 | **是** | 同上 |
| 未来的 knowledge 守护进程 | **否**，走 `detach.go` 语义 | 需比 owner 活得久（D5） |
| `winconsole` | **不适用** | 它不是进程，是代码页工具（D4） |

### 4.5 与 `supplement/05` §9.5 的表述修正

原问句"用 Job Object 还是 `taskkill /T`"**预设了二选一**，
而仓库的既有答案是"**Job Object 为主，`taskkill /T` 为降级**"。
本 ADR 修正该预设，并**解除**它同"是否复用 `internal/winconsole`"的错误绑定——
后者的答案是"无关，`winconsole` 是代码页工具"。

---

## 5. Rationale

逐条回应 Decision Drivers：

- **D1**：§4.1 明确禁止 knowledge 内自建，并给出可检查的形式。
- **D2**：§4.2 是选 C 的核心理由。选项 B 被否决的**真实缺陷**是：
  `taskkill` 是**终止时**执行的命令，进程被强杀时没有机会执行它。
  而 Job Object 的 `KILL_ON_JOB_CLOSE` 由**内核**在 handle 关闭时执行，
  不依赖任何用户态代码运行。**这是唯一能覆盖"宿主崩溃"场景的机制。**
- **D3**：§4.3 三态可观测。
- **D4**：§4.5 解除概念混淆。这一点值得单独强调：
  `supplement/05` §9.5 把一个**已解决的问题**（收树机制）和一个**无关的问题**（`winconsole` 复用）
  捆在一起问，导致问题看起来悬而未决。澄清本身就是价值。
- **D5**：§4.4 表格明确"故意脱离"仍走 `detach.go`。
- **D6**：§4.1 的 Unix 路径。
- **D7**：Gate 设为 `Phase4-start`，Phase 1–3 不引入子进程，无需提前。

**关于选项 D（复用 mcp transport 的 treeGuard）**：它技术上可行且已有诊断报告，
但它面向的是"stdio 连接的生死"，而 LSP 是**长驻且需要复用**的（见 ADR-0002 §4.4 的锁）。
生命周期语义不同，**强行复用会让 LSP 在连接抖动时被误收**。
因此本 ADR 选择语义更中性的 `executor.processGuard`，并把 `mcp.transport.treeGuard`
的**报告结构**作为设计参考而非直接依赖。

**关于选项 E（独立服务）**：`supplement/05` §8 已明确"不新增独立 knowledge 服务 / 端口"。
把 LSP 放进独立服务会引入跨进程 IPC、端口管理与新的失败模式，
而它解决的问题（隔离）可以用 Job Object 的边界直接达成，不必付出服务化的代价。

---

## 6. Consequences

### 6.1 Positive

- 零新机制；LSP 与现有 MCP/命令执行共享同一套经过 e2e 验证的收树保证。
- "宿主崩溃不留孤儿"这条**最难自测的保证**直接继承自既有测试。
- 降级路径可观测，与 `supplement/05` 附录 F 的"已知高风险"定位一致。
- 澄清了 `winconsole` 的职责，避免未来把代码页问题误当进程树问题。

### 6.2 Negative / Accepted trade-offs

- **knowledge 依赖 `internal/executor`**，增加一条包依赖。
  主动接受：这比复制一套 Job Object 代码便宜得多，且 `executor` 本就是通用进程设施。
- **LSP 被硬杀时不会有优雅关闭（`shutdown`/`exit` LSP 请求）。**
  主动接受：`gopls` 崩溃后被 Job Object 收树是安全的；优雅关闭只在 owner 正常退出时走，
  那条路径本来就会先发 `shutdown`。**不得**为了优雅关闭而放弃 Job Object（那会重新引入孤儿风险）。
- **`winconsole` 的 UTF-8 问题仍未在本 ADR 解决。** 主动接受：它是独立议题，
  应在 LSP stdio 实现时按需处理（`internal/consolehost/consolehost_windows.go` 已有相关线索）。

---

## 7. Reversal Plan

- 想换收树机制：只改 §4.1 的接线点，知识层的 LSP 生命周期代码不变。
- 想让某类子进程脱离：按 §4.4 走 `detach.go` 语义，加一行配置。
- 想撤销 LSP：`knowledge.lsp.enabled=false`（见 ADR-0002）。
- **无数据迁移**：进程生命周期不持久化。

---

## 8. Validation

| 检查 | 形式 | 门槛 |
|---|---|---|
| 无孤儿（正常退出） | 启动 LSP → 正常关闭 aicli → 枚举进程 | 无残留 |
| 无孤儿（硬杀） | 复用 `acp_e2e_mcp_parent_kill.go` 手法，`taskkill /F` 杀 aicli，断言 LSP pid 消失 | 必须通过 |
| 无孤儿（崩溃） | 令 aicli panic/`os.Exit(1)`，断言 LSP pid 消失 | 必须通过 |
| 降级可观测 | 模拟 `AssignProcessToJobObject` 失败，断言 `rep.Mode == "taskkill"` 且被记录 | 必须通过 |
| 无自建 | grep 确认 `internal/knowledge/` 内无 `CreateJobObject` / `SysProcAttr` | 必须通过 |
| Unix 对称 | 在 Unix 上跑同一测试，确认进程组回收 | 必须通过 |
| 不误收 | 未来 detach 的守护进程在 aicli 退出后仍存活 | 必须通过 |
| 不拖慢 | 收树操作不影响 turn P95 | P95 无变化 |

---

## 9. Alternatives Rejected (and why)

| 选项 | 否决理由（一句话） |
|---|---|
| A（knowledge 自建 Job Object） | 违反 D1，制造第三套降级语义 |
| B（只用 `taskkill /T`） | 进程被强杀时无机会执行，孤儿仍残留 |
| D（直接用 mcp transport 的 treeGuard） | 其生命周期语义是"随连接生死"，会误收需复用的长驻 LSP |
| E（LSP 放进独立服务） | 违反 `supplement/05` §8"不新增独立服务"，且代价远超收益 |
| F（detach 以避免误杀） | 正好制造它要避免的孤儿 |
| 忽略绑定失败 | 会让"父死子亡"保证静默失效，是最危险的失败模式 |

---

## 10. Open Follow-ups

| 项 | Gate |
|---|---|
| `executor.processGuard` 对长驻（非一次性命令）进程的接口是否够用 | `Phase4-start` |
| LSP 的 `shutdown`/`exit` 优雅关闭与 Job Object 的配合细节 | `Phase4-start` |
| LSP stdio 的 UTF-8 代码页处理（是否复用 `internal/consolehost`） | `Phase4-start` |
| 是否把 `background` / `runtimeserver` 的两处 `taskkill` 也收敛到统一守卫 | `Phase5-start` |
| `supplement/05` 附录 F 中"Windows 进程树"高风险条目的状态更新 | `Phase4-start` |
