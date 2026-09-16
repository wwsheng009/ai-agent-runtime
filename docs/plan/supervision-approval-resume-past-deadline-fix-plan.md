# 审批决议复活已终止 Run 缺陷修复方案（迟到 resolution 不得重启已超时子会话）

- 日期：2026-09-16
- 状态：**已实施（2026-09-16）**——P0-1/P0-2/P0-3/P0-4/P1-1 落地并全绿，见 §11 实施记录；
  §8 灰度开关 `supervision.approval_terminal_guard`（默认开、显式 false 回退旧行为）已补实现；
  未实施项（宿主级对等测试、supervision e2e）见 §11.3
- 关联：`docs/plan/supervision-business-supervision-implementation-plan.md` §9（本缺陷的发现记录）、
  `docs/plan/spawn-agent-team-supervision-timeout-recovery-plan.md`（执行期限/取消语义）、
  `backend/internal/supervision/execution_supervisor.go`（期限决策与强制取消）
- 证据来源：本会话实测（子会话 `session_20260916191352_IJOE6as2` + run `run_20260916111352_44736c8d`），
  事件 seq 22–27、supervision lifecycle 行 `n-session_20260916191352_IJOE6as2-bfe709bea8245282`

## 0. 摘要

对**已因执行期限终止**的子会话，事后调用 `resolve_agent_approval`（含 `allow=false`）会把会话
**复活**并继续执行原 turn：`session_start(resume=true)` + `session_compact_skipped(reason=resume_run)`，
越过已触发的 execution deadline。根因不是单点 bug，而是两条既有设计缺少优先级：

1. 运行侧（actor）：run ctx 被取消且存在待审批时，**有意**把审批"脱离"run 并回落
   `Status=SessionWaitingApproval`，让审批跨 run 存活、以便后续决议恢复（`actor.go:3977-3987`）；
2. 监督侧（ExecutionSupervisor）：`waiting_approval` 有自己的审批期限、不被普通进度超时杀死
   （`execution_supervisor.go:494-502`）；但执行期限（`execution_timed_out`）仍会强制取消 run 并
   `InterruptRun` → host `Close`（`execution_supervisor.go:505-507,526-549`；`execution_host_adapters.go:38-51`）。

结果是语义自相矛盾：**run 已终态（timed_out），会话却存活为 waiting_approval 且审批仍可决议**；
决议路径没有任何"run 是否终态"的守卫，决议即恢复执行。

影响：期限/预算的终止语义被绕过（超时 run 可被迟到决议无限续命）；监督面出现
`terminated 行 vs 实际 running` 的矛盾（本会话 workflow：子会话 `status: closed`、
`session_state: active`、`pending_approval: true` 同时成立）；token/成本无上限（本次复活前该 run 已
消耗 4.49M tokens / 69 步，复活后继续计费）。

## 1. 现象与复现（实测时间线）

| 时刻 (UTC) | 事件 | 关键字段 / 说明 |
| --- | --- | --- |
| 11:13:52 | run `run_20260916111352_44736c8d` 启动 | 子会话 `session_20260916191352_IJOE6as2`（spawn_agent child） |
| 11:18:48–11:38:26 | 8 次 `approval_requested` → `approval_resolved(allowed=true)` | seq 3–21，人工逐个批准 shell 调用 |
| 11:38:55 | 第 9 次 `approval_requested`（`call_00_qQD9YnYvRwLyM2fCosCD5415`） | seq 22，此后无人处理 |
| 11:53:18 | `session_end`：`status=stopped`、`cancel_source=execution_context`、`success=false`、`steps=69`、`usage_total_tokens=4489139` | seq 24，执行期限（40m）到期，run 被强制终止 |
| 11:54:32 | 操作侧拒绝该审批：`approval_resolved(allowed=false)` | seq 25，本次处置动作 |
| 11:54:32 | **`session_start(resume=true)`**（同一 `turn_ce2a134c…`）+ `session_compact_skipped(reason=resume_run)` | seq 26/27，**复活**（注：seq 26 的 `resume=true` 是 `session_start` 载荷，非 seq 24 会话结束事件） |
| 11:55+ | 父侧 `close_agent` 停掉复活 run | 复活期间仅只读 grep/glob，无新写入；随后 4 条 lifecycle 行人工收敛 |

最小复现（无 UI 依赖）：run 处于 pending approval → 触发 execution deadline（或直接
`InterruptRun`/`Close`）→ 观察 `session_end(cancel_source=execution_context)` 且会话回落
`waiting_approval` → 调用 `resolve_agent_approval(allow=false)` → 出现 `session_start(resume=true)`。

## 2. 根因链路（逐跳 + 代码定位）

1. **决议入口（宿主）**：`localActorRegistry.ResolveApproval`
   （`cmd/aicli/commands/chat_actor_registry.go:2359-2397`）
   → `resolveLocalAgentTargetSessionID` → `ensureSession`（:2376；仅当会话行缺失才重建，:4073-4112）
   → `SessionHub.GetOrCreate`（:2379）→ `actor.ApproveToolWithArgs`（:2383）；
   决议后回调 `supervision.ResolveApprovalRequest` 收敛监督通知（:1226）。
   API 宿主同构：`internal/api/skills/session_runtime_support.go:1814`（通知收敛 :1197）。
2. **决议执行（actor）**：`handleApproveTool`（`internal/chat/actor.go:889-977`）
   - :900 `detachForeignSessionRunControl` —— **刻意**剥离调用方的 run token，
     避免"外部审批被本会话活跃 run 判为 superseded"（注释 :897-899）；
   - :907 幂等分支要求 `state.PendingApproval != nil`，否则直接返回（不清不恢复）；
   - :915 过期检查；:943 `resolveApproval`（内存 waiter 存在 → 置 running，由 waiter 续跑）；
   - :961 `resumePendingToolWithResult(ctx, state, nil, "approval_denied")` —— 无 waiter 且拒绝时
     **自愈恢复**；:966 `resumeApprovedPendingTool` 为允许分支；
   - 自愈链：`resumePendingToolWithResult`（:3103-3138）→ `resumePendingBatchAfterCurrentResult`
     （:3336）→ 以 `resume=true` 重新提交 run → `session_start(resume=true)`、
     `maybeAutoCompactSession(..., resume)` 发 `resume_run`（:1845-1865）。**全链无终态守卫。**
3. **审批为何跨 run 存活**：run ctx 结束且审批未决时，审批等待路径
   （`actor.go:3977-3990`）显式 `detach.detached.Store(true)`（:3978-3980）并把
   `state.Status = SessionWaitingApproval`（:3982-3987）——**这是有意设计**（审批跨 run 存活）；
   终态收尾中的 `approvalDetached` 分支（:2526、:2536-2574）因此**跳过**状态清理：
   该分支只清 `PendingTool`（:2570），**不清 `PendingApproval`**（对照另一分支 :3474-3487 会清）。
   持久化落在 `session_runtime_store` 的 `pending_approval_json`
   （`internal/chat/runtime_state.go:84`；`session_runtime_store.go:2134-2145`），
   于是 `list_agents` 仍报告 `pending_approval: true`，决议因此进入 :943/:961 分支而非 :907 幂等分支。
4. **终止侧（监督）**：`execution_supervisor.go:505-507` 判定 `execution_timed_out` →
   :526-549 `RequestExecutionCancel` + `Interrupter.InterruptRun` → `AgentSessionRunInterrupter`
   （`internal/toolbroker/execution_host_adapters.go:38-51`）调 host `Close(sessionID)`；
   run 终态投影 :624-662（`RunStatusTimedOut` → critical）并 `convergeRunAlerts`（:668-673）。
   注意 `waiting_approval/waiting_input` 走独立审批期限、普通进度超时不动它（:494-502）。
5. **自相矛盾的落点**：run 终态 + 会话 `waiting_approval` + 可决议审批三者并存；
   决议一旦到达即恢复执行 → 复活。

## 3. 设计冲突分析（为什么"两边都对"）

| 语义 | 出处 | 意图 | 与期限终止的冲突 |
| --- | --- | --- | --- |
| 审批跨 run 存活（detach） | `actor.go:3977-3987`、:2526 | 让被中断/被接管的审批可在后续被决议并恢复，不丢用户输入 | 未区分"中断原因"：期限终止同样触发 detach |
| waiting_approval 独立期限 | `execution_supervisor.go:494-502` | 审批等待≠卡死，不能被进度超时误杀 | 期限 kill 后仍回落 waiting_approval（:3984），状态自相矛盾 |
| 外部审批不受 run token 约束 | `actor.go:897-900` | 宿主代外部决议时不应被本会话 run 判 superseded | 连"run 已终态"这一层也一并绕过 |
| 终态收尾清理状态 | `actor.go:2536-2574` vs :3474-3487 | 两个收尾分支应对齐 | detached 分支不清 `PendingApproval`，留下可决议的"活"审批 |
| run 终态即终态（业务） | `execution_supervisor.go:505-507`、`run_alerts.go:37-42` | 超时 run 由操作者显式决定 cancel/retry，不允许自动续命 | 决议路径无守卫，迟到决议=自动续命 |

**结论**：需要一条显式优先级规则 —— *"run 因期限/取消而终态"优先于"审批跨 run 存活"*：
终态 run 不得保留可恢复的审批；已在途的迟到决议只允许"记录 + 收敛"，不得触发执行。

## 4. 修复方案

总原则：**在"决议执行"这一决策点加终态守卫（P0-1/P0-2），宿主侧做入口预检（P0-3），
并在中止路径按终止原因分流（P0-4）；可观测性补 `resumed` 语义（P1-1）；状态机对齐（P1-2）。**
不动 `detachForeignSessionRunControl` 的既有语义（合法外部审批仍不受 run token 约束）。

### P0-1 actor：`handleApproveTool` 增加终态守卫（最小充分修复）

位置：`internal/chat/actor.go:938-970`（决议分支前）。
逻辑：取"本会话当前是否存在活跃/可续 run"+"最近一次 run 的终止原因"：

```
terminal := !a.hasLiveSessionRun() && state.CurrentTurnID == "" &&
            state.LastRunTerminalReason ∈ {execution_context, deadline, run_timeout, execution_timed_out}
```

- `terminal == true` 时：清 `state.PendingApproval/PendingTool`（durable），发布
  `approval_resolved{resolution:"run_terminal_no_resume", allowed:false}`，
  **不进入** :943/:961/:966 任一恢复分支，回复 nil。
- `terminal == false` 时：保持现状（合法自愈/内存 waiter 续跑不受影响）。
- 实现要求：终态原因必须来自**显式落盘字段**（新增 `state.LastRunTerminalReason`，
  在 :2581-2592 组装 `session_end` 的同一处写入，与 `cancel_source` 同源），
  禁止用"无活跃 run"单独推断，避免误伤"supersede 后由新 turn 接管"的合法场景。

### P0-2 actor：恢复链入口再加一道断言（纵深防御）

位置：`resumePendingBatchAfterCurrentResult`（:3336）或 `startSessionRun` 的 `resume=true` 入口（:2370 附近）。
逻辑：当 `resume==true` 且 `state.LastRunTerminalReason` 为期限/取消类时，直接返回
`errSessionRunTerminal`（新错误），并发布 diagnostic 事件；
这样即使 P0-1 被未来新分支绕过，也不会复活。

### P0-3 双宿主：决议前终态预检（避免副作用 + 语义一致）

- CLI：`localActorRegistry.ResolveApproval`（`chat_actor_registry.go:2359-2397`）在
  `GetOrCreate`/`ApproveToolWithArgs` 前查询 run/会话终态；终态则跳过 actor 决议，
  仅执行 `supervision.ResolveApprovalRequest`（:1226）收敛通知，返回
  `AgentApprovalResult{Resolved:true, Resumed:false, Resolution:"run_terminal_no_resume"}`。
- API：`sessionAgentController.ResolveApproval`（`internal/api/skills/session_runtime_support.go:1814`）对等实现
  （通知收敛 :1197）。
- 共享语义：把"终态判定 + 返回字段"抽成 `internal/toolbroker`（或 `internal/chat`）的单一 helper，
  两个宿主共用以防再次漂移；`AgentApprovalResult` 增 `Resumed bool`、`Resolution string`
  （`internal/toolbroker/types.go` 的 `AgentApprovalResult` 定义处，:960 附近接口）。

### P0-4 actor：中止路径按终止原因分流（消除"矛盾状态"源头）

位置：`actor.go:3977-3990`（审批等待的 `ctx.Done()` 分支）。
逻辑：`ctx` 结束时读取"run 取消原因"（由 `installSessionRunCancel`/`InterruptRun` 宿主侧传入，
见 :3749、`execution_host_adapters.go:38-51`）：

- 期限/取消类（`execution_timed_out`、`execution_context`、`deadline`、`run_timeout`）：
  **不 detach**、**不回落** `SessionWaitingApproval`；改为清 `PendingApproval`（durable）+
  记录 `LastRunTerminalReason` + 发布 `approval_resolved{resolution:"run_terminated"}`；
- 其余（supersede/移交等）：保持现状 detach + waiting_approval。

### P1-1 可观测性/契约

- `approval_resolved` 载荷补 `resumed`（bool）与 `resolution`（现有字段，扩展取值：
  `allowed|denied|expired|run_terminated|run_terminal_no_resume`）；
- `ResolveAgentApproval` 工具结果文本在 `Resumed=false` 时显式说明"只登记、未恢复执行"，
  防止模型误判子会话仍在跑（对齐 `chat_actor_host.go:1849` 的既有指引）；
- 监督投影：`run_terminal_no_resume` 只落 info（不新增 critical 行），与
  `run_alerts.go` 的终态语义一致。

### P1-2 状态机一致性（run 终态 ⇒ 会话不留 waiting_approval）

`execution_supervisor.go:624-662` 的终态投影旁，补"该 run 的会话不得停留在
`waiting_approval`"的收口（actor 侧 P0-4 已覆盖主路径；此处做可观测断言/兜底：
宿主 `Close` 后若会话仍 waiting_approval 则记录 diagnostic 并在后续 preflight 提示）。

### 4.x 备选方案与取舍

| 方案 | 做法 | 取舍 |
| --- | --- | --- |
| **A（推荐）** | P0-1/P0-2 决策点守卫 + P0-3 宿主预检 + P0-4 原因分流 | 精准、可测、不破坏合法 detach；改动集中在决策点与宿主 |
| B | 只在终态收尾里清 `PendingApproval`（:2562 分支对齐 :3474） | 最小改动，但清掉审批后**丢用户输入**（决议变 404），且与 detach 设计冲突，不采纳 |
| C | 决议即"新建 run 恢复"，但重置 deadline/预算并重新投影 lifecycle | 保留恢复能力，但语义复杂、需预算/通知联动，作为远期可选（当前不需要） |
| D | 禁止外部决议终态会话（宿主直接报错） | 操作者体验差、监督行无法收敛（决议本身也是收敛手段），不采纳 |

**非目标**：不改变 `detachForeignSessionRunControl` 语义；不改变 `waiting_approval` 的独立审批期限；
不引入新的常驻轮询。

## 5. 代码定位清单（实施用）

| 文件:行 | 作用 | 改动 |
| --- | --- | --- |
| `internal/chat/actor.go:938-970` | `handleApproveTool` 决议分支 | 加终态守卫（P0-1），新增 `run_terminal_no_resume` 出口 |
| `internal/chat/actor.go:3977-3990` | 审批等待 `ctx.Done()`：detach + 回落 waiting_approval | 按取消原因分流（P0-4）；新增原因入参/字段 |
| `internal/chat/actor.go:2581-2592` | 组装 `session_end`（`cancel_source` 同源） | 同步落盘 `LastRunTerminalReason`（P0-1 依赖） |
| `internal/chat/actor.go:2526,2536-2574` | detached 收尾分支 | 对齐 `PendingApproval/PendingQuestion` 清理（与 :3474-3487 一致或显式保留 detached 语义） |
| `internal/chat/actor.go:3336` / `:2370` | `resumePendingBatchAfterCurrentResult` / `startSessionRun(resume)` | 恢复入口断言 `errSessionRunTerminal`（P0-2） |
| `internal/chat/actor.go:3749` | `installSessionRunCancel` | 传递取消原因（宿主→actor，P0-4） |
| `internal/chat/runtime_state.go:84` | `PendingApproval` 持久化字段 | 新增 `LastRunTerminalReason`（同文件 State 定义） |
| `cmd/aicli/commands/chat_actor_registry.go:2359-2397` | CLI 宿主决议入口 | 终态预检 + `Resumed/Resolution` 返回（P0-3） |
| `cmd/aicli/commands/chat_actor_registry.go:1226` | 决议后收敛监督通知 | 终态路径仍然执行（保监督收敛） |
| `internal/api/skills/session_runtime_support.go:1814` | API 宿主决议入口 | 与 CLI 对等（P0-3）；通知收敛 :1197 |
| `internal/toolbroker/types.go:960` 附近 | `AgentSessionController` 接口/`AgentApprovalResult` | 增 `Resumed bool`、`Resolution string` |
| `internal/toolbroker/execution_host_adapters.go:38-51` | `InterruptRun` → host `Close` | 透传终止原因（P0-4 的输入源） |
| `internal/supervision/execution_supervisor.go:624-673` | run 终态投影 + converge | 断言/兜底会话不留 waiting_approval（P1-2） |

## 6. 测试设计

原则：**先红后绿**——先写能复现"迟到决议复活"的测试（当前代码必红），再实施修复。

### 6.1 单元（actor，`internal/chat/actor_test.go`）

| 用例 | 构造 | 断言 |
| --- | --- | --- |
| `TestSessionActorDeadlineCancelWithPendingApprovalDoesNotDetach` | 运行中发起审批 → 以 `execution_timed_out` 原因取消 run ctx | ①不产生 `waiting_approval` 状态回落；②`PendingApproval` 被清理且落盘；③`approval_resolved` 载荷 `resolution="run_terminated"` |
| `TestSessionActorApproveToolAfterTerminalRunDoesNotResume`（**复现用例**） | 承上：终态后再 `ApproveToolWithArgs(reqID,false,nil)` | ①无 `session_start(resume=true)`；②无新 run/无 turn 续跑；③载荷 `resolution="run_terminal_no_resume"`、`resumed=false`；④状态保持 stopped/failed |
| `..._AllowVariantDoesNotExecuteTool` | 同上，`allow=true` | 审批允许也不执行 pending tool、不恢复 |
| `TestSessionActorApproveToolResumesWithoutInMemoryWaiter`（既有，**回归保护**） | 活跃会话 + 无内存 waiter | 仍自愈恢复（守卫不得误伤） |
| `TestSessionActorApproveToolExpiredStillResolves`（既有语义） | 审批过期 | 仍走 expired 分支 |

### 6.2 终止路径（`internal/chat` 或 store 测试）

| 用例 | 断言 |
| --- | --- |
| `TestSessionRunTerminalFinalizeClearsPendingApproval` | 期限取消 + detached 分支收尾后，`session_runtime_store` 的 `pending_approval_json` 为 NULL/或带 `run_terminated` 标记；`LastRunTerminalReason` 已落盘 |
| `TestSessionRuntimeStateTerminalReasonRoundTrip` | 新字段的序列化/反序列化（`runtime_state_test.go` 风格，含大 payload 场景） |

### 6.3 宿主（双宿主对等）

| 用例 | 位置 | 断言 |
| --- | --- | --- |
| `TestLocalResolveApprovalOnTerminalRunDoesNotReviveSession` | `cmd/aicli/commands/chat_actor_registry_test.go`（或本次新增的 `*_permission_policy_test.go` 同风格） | ①actor 未被恢复（无 `session_start`）；②结果 `Resolved=true, Resumed=false, Resolution=run_terminal_no_resume`；③监督通知行收敛（`ResolveApprovalRequest` 仍执行） |
| `TestSessionAgentControllerResolveApprovalTerminalRunSkipsResume` | `internal/api/skills/`（与 `session_agent_permission_policy_test.go` 风格一致） | 与 CLI 对等断言 |
| `TestAgentApprovalResultContract` | `internal/toolbroker/` | 新字段的 JSON/文本契约（含 tool 结果文案） |

### 6.4 端到端（复用既有 e2e 骨架）

- 扩展 `cmd/aicli/commands/supervision_e2e_three_background_test.go`：
  "超时子会话 + 挂起审批" 场景 → 触发执行期限 → 迟到决议（allow=false/true 两分支）→ 断言：
  ①无 `session_start(resume=true)`；②run 保持终态；③4 类 lifecycle 行收敛
  （run=timed_out/stalled、session=blocked/terminated）且不再回流 critical；
  ④`supervision_descendants` 的该行显示终态与 `Resumed=false` 决议结果。
- 手工验证清单（发布前）：真实会话内 `spawn_agent(background)` + shell 审批挂起 → 等期限/或缩短
  `DefaultExecutionTimeout` → `resolve_agent_approval` → 观察 UI 无"复活"（无新 running turn）。

### 6.5 回归面（必须保持全绿）

`go test ./internal/chat/... ./internal/toolbroker/... ./internal/supervision/... ./cmd/aicli/commands/...`
——重点是既有审批自愈/问答恢复**不得被守卫误伤**（`:943` 内存 waiter 路径、:966 允许路径）。

## 7. 验收标准

1. 复现用例在修复前必红（`session_start(resume=true)` 出现），修复后全绿；
2. 迟到决议（允许/拒绝两分支）在终态 run 上**零执行、零恢复**：无新 `session_start(resume=true)`、
   无工具执行、无新 run；决议结果显式 `Resumed=false` + `Resolution=run_terminal_no_resume`；
3. 期限终态路径产出单一一致状态：run 终态、会话非 `waiting_approval`、审批已终态化
   （`pending_approval_json` 已清或标记 `run_terminated`）；
4. 监督面：终态行保持终态、blocked 行收敛为 closed，不出现 `terminated vs running` 矛盾；
5. 合法路径不回退：内存 waiter 恢复、外部审批（非终态）、approval expired 三大既有行为全绿；
6. 双宿主对等：CLI 与 API 的决议行为、返回字段、通知收敛一致（同一 helper/契约测试覆盖）。

## 8. 风险、回滚与提交切分

- **误判终态导致合法审批不可恢复**（最大风险）：终态信号必须是显式落盘的原因字段
  （`LastRunTerminalReason`），禁止"无活跃 run"单独推断；测试用既有自愈用例兜底。
- **detach 语义边界模糊**：本次只把"期限/取消类终止"排除出 detach；其余继续保持。
  若后续有依赖"超时后仍可审批恢复"的场景，应改走方案 C（显式恢复 + deadline/预算重置）。
- **双宿主漂移**：抽共享 helper + 契约测试；API 与 CLI 任一缺失即测试失败。
- **回滚**：改动集中在 `internal/chat`（守卫 + 原因字段）与两个宿主入口；如出现误伤，
  可先只回滚宿主的 `Resumed=false` 出口（保留 actor 守卫），或按开关
  `supervision.approval_terminal_guard`（默认开；**已实现**，键名与 `nil`/`false` 语义见 §11.1 末行与 §11.2 偏差 7/8）灰度。
- **提交切分**：①`internal/chat`（守卫/原因字段/测试）；②`internal/toolbroker`（契约）；
  ③`cmd/aicli/commands` + `internal/api/skills`（双宿主）；④文档；⑤§8 开关（`internal/supervision/config.go`(+测试) + 接线 + 钉子用例）。
  与工作区其它并行改动（frontend/SSE 等）严格分离提交。

## 9. 开放问题（实施前确认）

1. `InterruptRun → Close` 链路目前不携带"终止原因"，P0-4 需要 `AgentSessionController.Close`
   或相邻接口透传 reason（最小侵入：host 侧在 Close 前把 reason 写入会话 runtime state，
   actor 读同一字段）。
2. `LastRunTerminalReason` 的清理时机：新 turn 正常启动时应置空（否则后续合法审批会被误判），
   建议在 `claimSessionRun`/`startSessionRun` 起点重置，并加单元测试。
3. 是否需要在监督侧增加"决议被拒（run_terminal_no_resume）"的 info 行：当前建议只做事件 + 工具结果，
   避免噪声；若操作者需要审计追溯，可在 `run_alerts` 收敛时附带一次投影。

## 10. 附录：证据索引

- 子会话：`session_20260916191352_IJOE6as2`（path `/root/session_20260916191352_IJOE6as2`）
- run：`run_20260916111352_44736c8d`（execution deadline 40m）
- 事件：`approval_requested` seq 22（`call_00_qQD9YnYvRwLyM2fCosCD5415`，11:38:55Z）
  → `session_end` seq 24（11:53:18Z，`cancel_source=execution_context`、`status=stopped`、`success=false`、
  `steps=69`、`usage_total_tokens=4489139`）
  → `approval_resolved(allowed=false)` seq 25（11:54:32Z）
  → `session_start(resume=true)` seq 26 + `session_compact_skipped(reason=resume_run)` seq 27（11:54:32Z）
- 监督通知：`n-run_20260916111352_44736c8d-a26026b5bbb1b1d8`（stalled）、
  `n-run_20260916111352_44736c8d-34876237e62e85a4`（timed_out）、
  `n-session_20260916191352_IJOE6as2-71695686a3740246`（blocked/approval）、
  `n-session_20260916191352_IJOE6as2-bfe709bea8245282`（terminated）
  ——本次处置：`close_agent` 停止复活 run；4 行 resolve（run×2=`failed`、approval 行=`closed`、
  terminated 行=`failed`），与 `runAlertResolution`（`internal/supervision/run_alerts.go:37-42`）语义一致。
- 现场快照矛盾：`list_agents` 同一次输出中 `status: closed` + `session_state: active` +
  `pending_approval: true`（本方案 §1 表后注）。

## 11. 实施记录（2026-09-16 完成）

状态：**P0-1 / P0-2 / P0-3 / P0-4 / P1-1 已实施并全绿**；P1-2 由 P0-4 的终态收敛覆盖（见下）。

### 11.1 实施映射（方案条目 → 落地位置）

| 方案条目 | 落地内容 | 位置 |
| --- | --- | --- |
| P0-1 决策点终态守卫 | 新增 `errSessionRunTerminal`、`pendingApprovalRunTerminal()`（判定只读 `LastRunTerminalReason`，**禁止**用"无活跃 run"推断；存在内存 waiter 时否决守卫）；`handleApproveTool` 终态分支：清理 pending 审批/工具、状态落 `stopped`、发 `approval_resolved`（`resolution=run_terminal_no_resume`、`resumed=false`、`run_terminal_reason=<原因>`）、记录 outcome | `internal/chat/actor.go:52, 968-996, 4489-4503` |
| P0-2 恢复入口二道断言 | `resumePendingBatchAfterCurrentResult` 起手读持久原因字段，非空即返回 `errSessionRunTerminal`（改动前后状态一律不变） | `internal/chat/actor.go:3520-3526` |
| P0-3 双宿主决议语义 | **实现方式与方案不同（更省一层）**：不新增"宿主预检"，而由 actor 在两条无审批路径上导出决议结果 —— ①终态 run 上的迟到决议（P0-1 分支）；②重载后仅剩持久标记（幂等分支补齐 resolution，不覆盖已有 outcome）；宿主沿用 `ApprovalOutcome()`，用共享判定 `toolbroker.ApprovalResolutionNotApplied()` 把 `allowed` 归为 false，返回 `Resolved=true, Resumed=false, Resolution=run_terminated\|run_terminal_no_resume` | `internal/chat/actor.go:948-958`；`internal/toolbroker/types.go:715-737`；`cmd/aicli/commands/chat_actor_registry.go:2391-2404`；`internal/api/skills/session_runtime_support.go:1812-1824` |
| P0-4 中止路径分流 | `sessionRunCancelSource()` 归类取消来源（`user_interrupt` / `run_timeout` / `deadline` / `execution_context` / `parent_deadline` / `parent_context`）+ `sessionRunTerminalCancelSource()` 判定终止类；终态收尾时：`approvalDetached && 终止类` ⇒ **不 detach**，清理 `PendingApproval`、落 `LastRunTerminalReason`、发 `approval_resolved`（`resolution=run_terminated`）并记录 outcome；非终止类保持既有跨 run 审批语义 | `internal/chat/actor.go:2625-2710, 3129-3165` |
| P1-1 可观测性/契约 | 常量集中在 toolbroker（事件与工具契约同源，防漂移）：`allowed/denied/expired/run_terminated/run_terminal_no_resume`；`approval_resolved` 统一带 `resolution`/`resumed`；工具结果文案对"未生效决议"显示 `not applied` + 说明句 | `internal/toolbroker/types.go:715-737`；`internal/toolbroker/cache_safe_summary.go:212-232` |
| P1-2 状态机一致性 | run 终态 ⇒ 会话不留 `waiting_approval`：终止类路径清审批且状态落 `stopped`；运行起点（`SubmitPrompt`/`ContinueSession`）置空 `LastRunTerminalReason`，非终止类且未 detach 的收尾同样置空 | `internal/chat/actor.go:873-881, 913-921, 2683-2694` |
| 持久化 | 新字段 `RuntimeState.LastRunTerminalReason` + SQLite 迁移 v21（`ALTER TABLE session_runtime_state ADD COLUMN last_run_terminal_reason TEXT`）+ Save/Load 往返 | `internal/chat/runtime_state.go:86-92`；`internal/chat/session_runtime_store.go:4458-4464, 1934, 2139, 2152` |
| §8 灰度开关 | `supervision.Config.ApprovalTerminalGuard *bool`（键 `approval_terminal_guard`：nil/true=开、显式 false=关）+ `ApprovalTerminalGuardEnabled()`；`chat.SessionActorConfig.ApprovalTerminalGuard` 在构造时解析为 actor 内部 `terminalGuard`，统一闸住四处：决策点守卫（P0-1）、恢复入口断言（P0-2）、幂等分支补齐决议（P0-3）、终态收尾与标记写入（P0-4）；CLI/API 双宿主从 supervision 配置透传 | `internal/supervision/config.go:54-59, 114-117, 128-138`；`internal/chat/actor.go:139-145, 160-162, 244, 953, 2630, 2685-2691, 3524, 4492-4495`；`cmd/aicli/commands/chat_actor_host.go:1198-1200`；`internal/api/skills/session_runtime_support.go:3741-3743` |

### 11.2 与方案的偏差（实测结论，需评审）

1. **P0-3 不做宿主预检**：方案 §4/P0-3 设想"宿主入口先读终态再决定是否调用 actor"。实施后确认不需要：actor 的两条无审批路径已能给出决议语义，宿主只做一行共享判定。这样避免新增一条"宿主读 runtime state"的耦合，也避免 actor 状态与宿主读到的快照发生竞态（读到的可能是旧状态）。代价：宿主返回的 `resolution` 语义由 actor 决定（`run_terminated` vs `run_terminal_no_resume`，两者都表示"已记录未执行"）。
2. **§9 开放问题 1（Close 透传 reason）已闭合，但不需要透传**：终止原因由 run 自身的 ctx/错误/`result.LimitReason` 推导（`sessionRunCancelSource`），覆盖 `execution_context`、`deadline`、`run_timeout`、`parent_*` 五类；宿主不必在 `Close` 前写会话状态。
3. **§9 开放问题 2 已实施**：`LastRunTerminalReason` 在运行起点置空（两处），并有测试覆盖（终态决议用例 + 既有自愈用例）。
4. **§9 开放问题 3 按建议未做**：不为 `run_terminal_no_resume` 增加监督 info 行，只落事件 + 工具结果，避免噪声。
5. **测试落点调整**：新建 `internal/chat/actor_approval_terminal_test.go` 与 `internal/toolbroker/approval_resolution_test.go`，未改 `actor_test.go`（降低与工作区并行改动/未来 rebase 的冲突面）。
6. **实现中发现的第二个洞（方案未覆盖，已修）**：终态 run 的审批被 P0-4 收尾清理后，迟到决议会命中"幂等分支"直接返回 nil，宿主拿不到任何 resolution（`ApprovalOutcome` 为空），会误报 `allowed=true`。修法：P0-4 收尾时记录 outcome；幂等分支在持久标记存在且尚无 outcome 时补齐 `run_terminal_no_resume`（不覆盖已有记录，避免把 `run_terminated` 覆盖成语义更弱的结论）。
7. **开关键名采用 snake_case（与方案建议不同）**：方案 §8 建议 `supervision.approvalTerminalGuard`。本仓 `supervision` 配置块既有键（`execution_deadline`、`snapshot_max_items`、`wake_budget_mode`…）全部是 snake_case，且 `supervision.Config` 的 yaml/json tag 逐字段同名。为保持该块一致，实现键为 **`supervision.approval_terminal_guard`**（`approvalTerminalGuard` 仍可作为文档/伪代码里的称呼）。
8. **开关默认开、且开关本身有钉子测试**：`nil` 与 `true` 均视为启用（零值/未接线配置不会静默关闭守卫）；显式 `false` 时三处闸门一起回退到引入守卫前的行为，并由 `TestSessionActorApprovalTerminalGuardDisabledRestoresLegacyResume` 断言"关掉即旧行为"（迟到决议仍恢复 run、模型被再次调用、`resolution=allowed`），避免开关退化为空操作。

### 11.3 未实施（明确标注，需决策）

| 方案条目 | 状态 | 原因 |
| --- | --- | --- |
| §6.3 双宿主对等测试（CLI/API 宿主级） | **未实施** | 宿主 `ResolveApproval` 现已纯粹透传 actor outcome，其行为已由 toolbroker 契约测试（`TestApprovalResolutionNotApplied`、`TestAgentApprovalCacheSafeSummaryReportsUnappliedDecisions`）与 chat 端到端测试覆盖；宿主级测试需装配 Host/SessionHub，收益低。若评审要求，可后续补。 |
| §6.4 supervision e2e（超时子会话 + 挂起审批 → 迟到决议 → 断言行收敛） | **未实施** | 需要真实 run 期限与监督通知装配；本轮以「live 取消路径 + 终态决议 + 持久往返」三段测试覆盖了同一断言的机制层。**建议**：作为独立的 e2e 任务跟进，不在本提交范围。 |
| §8 灰度开关 `supervision.approval_terminal_guard` | **已实施（2026-09-16 追加）** | 见 §11.1 末行与 §11.2 偏差 7/8：默认开，仅显式 `false` 关闭；关闭后决策点/恢复入口/终态收尾三处一并回退旧行为，宿主无需回滚二进制即可灰度或止损。 |

### 11.4 验证证据

**红→绿**（先证明测试能抓住缺陷，再恢复实现）：

- 临时把两处守卫改为恒不触发（`return "", false` / `return nil`），复现用例立刻变红，症状与线上一致：
  `PendingApproval` 未被清理、`Status=waiting_approval`、`Resolution=allowed`、pending tool 被真实执行
  （`ResultMessageJSON` 含 `approved_resume:true`）。恢复守卫后全绿。

**新增用例（全绿）**：

| 用例 | 覆盖 |
| --- | --- |
| `TestSessionActorApproveToolAfterTerminalRunDoesNotResume`（approved/denied 两分支） | P0-1：终态后迟到决议零恢复、零模型调用、零消息追加、事件载荷正确、落盘清理 |
| `TestPendingApprovalRunTerminalGuardRequiresDurableMarkerAndNoWaiter` | P0-1 守卫的两个条件（缺标记不推断；有 waiter 不误伤） |
| `TestResumePendingBatchAfterCurrentResultRefusesTerminalRun` | P0-2：直达恢复入口也拒绝且状态不变 |
| `TestSessionActorTerminalRunCancelRetiresPendingApproval` | P0-4：运行中取消 → 审批随 run 终态化（无 `waiting_approval` 回落）、事件 `resolution=run_terminated`、落盘一致、迟到决议不复活 |
| `TestSessionActorLateApprovalAfterRestartReportsNotApplied` | P0-3：重启后仅凭持久标记也能报 `run_terminal_no_resume` 且不复活 |
| `TestSQLiteRuntimeStorePersistsLastRunTerminalReason` | 迁移 v21 列存在 + 落盘/清空往返 |
| `TestSessionRunCancelSourceClassifiesTerminalClasses` | 终止类判定边界（`user_interrupt` 不属终止类） |
| `TestApprovalResolutionNotApplied` / `TestAgentApprovalCacheSafeSummaryReportsUnappliedDecisions` | 契约：哪些决议算"未生效"，工具文案不得显示 approved |
| `TestSessionActorApprovalTerminalGuardDisabledRestoresLegacyResume` | §8 开关关闭 ⇒ 迟到决议走旧恢复路径（模型再次被调用、`resolution=allowed`、`resumed=true`），即"关掉即旧行为" |
| `TestApprovalTerminalGuardDefaultsEnabled` / `TestApprovalTerminalGuardYAMLBinding` | §8 配置层：零值 / `DefaultConfig` 默认开；显式 false 经 `WithDefaults` 与 YAML 绑定后仍关闭；无关 supervision 块不会误关 |

**回归命令与结果**（`backend/`，最终一轮，含 supervision 包）：

```
go build ./...                                                       # BUILD_EXIT=0
go vet ./internal/chat/ ./internal/toolbroker/ ./internal/supervision/ ./internal/api/skills/ ./cmd/aicli/commands/   # VET_EXIT=0
go test ./internal/chat/ ./internal/toolbroker/ ./internal/supervision/ ./internal/api/skills/ ./cmd/aicli/commands/ -count=1
# ok chat 18.5s / toolbroker 11.5s / supervision 1.9s / api/skills 20.7s / aicli:commands 78.9s  → GO_TEST_EXIT=0
```

**§8 灰度开关（追加）**：`go test ./internal/supervision/ -run TestApprovalTerminalGuard -count=1` 全绿（默认开 / 显式关 / YAML 绑定）；
chat 侧同批 6 个守卫用例（含开关关闭的钉子用例）`-count=1` 全绿，随后全量 5 包回归仍为 `GO_TEST_EXIT=0`。

重点回归面（§6.5）：既有审批自愈 / 外部审批 / 过期审批路径未受影响
（`TestSessionActorApproveToolResumesWithoutInMemoryWaiter` 等全部保持全绿）。

**过程中的两次“波动”与真实根因**（均与本次改动无关，已复跑全绿）：

- `cmd/aicli/commands` 出现 `[build failed]`（0.001s）——根因是 **C 盘写满**（剩余 0.06 GB），
  编译现场报 `compile: writing output: write $WORK\...\_pkg_.a: There is not enough space on the disk`。
  处置：`go clean -cache`（GOCACHE 8.33 GB → 0.06 GB，C 盘剩余 0.06 GB → 8.37 GB），复跑全绿。
- `internal/chat` 出现一次 `--- FAIL: TestSessionActorApproveToolResumesWithoutInMemoryWaiter (2.04s)`，
  失败点正是该用例内置的 **2s `select` 看门狗**（"original actor submit did not exit after turn cancellation"），
  发生在冷缓存重建、多包并行编译导致机器高负载的那一轮；隔离复跑 0.43s、`-count=3`、整包复跑与最终全量复跑
  均全绿，判定为**环境负载下的时限抖动**。
  建议（未实施，避免与并行改动冲突）：该看门狗由 2s 放宽至 5–10s 或改为轮询等待。

### 11.5 §7 验收标准对照

| 标准 | 结论 | 依据 |
| --- | --- | --- |
| 1 复现用例修复前必红 | ✅ | §11.4 红→绿记录 |
| 2 迟到决议零执行、零恢复、显式 `Resumed=false` + `Resolution=run_terminal_no_resume` | ✅ | `..._AfterTerminalRunDoesNotResume`、`..._LateApprovalAfterRestartReportsNotApplied` |
| 3 期限终态路径单一一致状态（非 `waiting_approval`、审批终态化、原因落盘） | ✅ | `..._TerminalRunCancelRetiresPendingApproval`、`..._PersistsLastRunTerminalReason` |
| 4 监督面终态行不回流 critical | ⚠️ 机制层已满足，e2e 未做 | §11.3（建议独立 e2e 跟进） |
| 5 合法路径不回退 | ✅ | §11.4 回归结果 |
| 6 双宿主对等 | ✅（入口 + 共享判定 + 契约测试） | §11.1 P0-3 行 |

### 11.6 提交切分（建议）

1. `internal/chat`（守卫、取消来源分类、终态收尾、持久字段 + chat 测试）
2. `internal/toolbroker`（常量/共享判定/文案 + 契约测试）
3. `cmd/aicli/commands` + `internal/api/skills`（双宿主出口）
4. 文档（本方案 §11 + implementation-plan §9 状态更新）
5. §8 灰度开关（追加）：`internal/supervision/config.go`(+`config_test.go`)、`internal/chat/actor.go` 的开关接线 + 钉子用例、双宿主 `ApprovalTerminalGuard` 透传；若评审要求严格按包切分，可并入 ①/③。

与工作区其它并行改动（frontend/SSE、supervision 等）严格分离提交。
