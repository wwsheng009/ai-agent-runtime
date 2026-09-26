# wait_agent 等待预算与最大窗口收敛实施记录（2026-09-26）

> 关联设计：`docs/plan/supervised-turn-suspension-and-agent-task-control-plan-20260923.md` §16.2 / §16.3
> （active wait / passive wait、`wait_agent` 四条约束、`next_action=suspend`），
> 本文是该设计"成本口径"从**建议**升级为**运行时判据**的落地记录。

## 1. 根因（现场取证）

用户现场：`wait_agent ids=react-attachments timeout_ms=2.4e+06`（40 分钟窗口）在 TUI 上
持续 51 分钟以上，期间父 turn 无法感知/处理其它事。

代码事实：

1. §16.3 的四条约束（区间钳制、活动驱动、可打断、超时即观测）已落地
   （`chat_actor_registry.go:2796-2883`、`session_runtime_support.go:waitForAgentStatus`）。
2. 但"单次 ≤60s、连续 2 次无进展 ⇒ 改用挂起"只是文档里的**成本建议**：
   - 上界默认 1h（`agentcontrol/wait_timeout.go`），40 分钟是**合法请求**，不触发钳制；
   - 代码中不存在 `next_action=suspend`，也没有"连续无进展"计数；
   - 模型反复开大窗口即可把会话占满（51m 是多次等待段/单元格时长聚合后的表现）。
3. I1 只在**收尾**时兜底（账本非终态 ⇒ 拦截并转挂起），挡不住"收尾之前一直开窗等待"。

结论：缺的不是超时机制，而是**主动等待的预算判据**。

## 2. 改动清单

### 2.1 新增：连续无进展等待预算（§16.2/§16.3 判据化）

| 落点 | 内容 |
| --- | --- |
| `internal/agentcontrol/wait_budget.go`（新增） | `WaitBudget`：按 `sessionID\|turnID` 记录连续无进展等待段；`Observe` / `Exhausted` / `Reset`；零值可用、并发安全、`limit<=0` 关闭 |
| `internal/agentcontrol/wait_budget_test.go`（新增） | 计数/进展重置/键隔离/禁用/空键/nil 语义单测 |
| `internal/toolbroker/types.go` | `AgentWaitResult.WaitBudgetExhausted` 字段；`AgentWaitSuspendNextAction`（`suspend:` 判据文案，指向巡检原语与 I1 出口）；`SuspendAgentWaitResultForBudget`（保留账本视图；`finalize` 优先，不把可收尾的 turn 拖回挂起） |
| CLI 宿主 `cmd/aicli/commands/chat_actor_registry.go` | `localActorRegistry.localWaitBudget`；`Wait` 在开窗前查预算、返回后记进展；`localWaitLedger` 追加预算键（会话+挂起 turn） |
| API 宿主 `internal/api/runtimeapi/{handler.go,session_runtime_support.go}` | `Handler.waitBudget`（宿主长生命周期，controller 是短生命周期）；`Wait` 同口径接线；`waitLedger` 追加预算键 |
| `internal/agentguidance/guidance.go` | `WaitBudgetRule` 从"prefer the longest timeout"改为"有界窗口 + 预算耗尽返回 `next_action=suspend`"，同时进入系统引导与工具描述 |

语义边界（与既有设计一致）：

- **进展** = 本等待段内至少一个 obligation 转终态（`terminal_delta` 非空）；steer/ESC
  提前结束的等待段不计入预算；账本清空即重置。
- 预算耗尽后：宿主**不再打开新的活动等待窗口**，`wait_agent` 立即返回
  `wait_budget_exhausted=true` + `next_action=suspend` + 完整账本视图；**不谎报超时**。
- `wait_agent` 自身仍不挂起 turn：模型可做独立工作，或收尾——收尾由 I1 转挂起
  （`awaiting_obligations`），终态/决策事件以同一 `turn_id` resume。
- 判读失败一律 fail-open：账本读不到 ⇒ 预算键为空 ⇒ 不启用预算（不新增阻塞面）。

### 2.2 默认最大等待窗口：1h → 2m

| 落点 | 内容 |
| --- | --- |
| `internal/agentcontrol/wait_timeout.go` | `MaxWaitTimeoutMs = 120000`（默认 30s / 下界 10s / 上界 2m） |
| `internal/config/manager.go` | 内置 `agents.maxWaitTimeoutMs` 默认 2m；新增 `agents.maxConsecutiveWaitWithoutProgress`（0=默认 2，负值=禁用） |
| `internal/config/agents_normalize.go` | 新字段 0 值归一化；负值"显式禁用"语义写入归一化契约 |
| 测试同步 | `wait_timeout_test.go`、`manager_test.go`、`agentguidance/guidance_test.go`、`broker_team_wait_policy_test.go`、`prompt/environment_context_test.go` |

超 2m 的请求按 `agents.waitTimeoutMode`（默认 clamp）钉到 2m；需要更宽活动窗口的部署
可显式调高 `agents.maxWaitTimeoutMs`，长等待的正确路径仍是挂起（零 goroutine / 零 token）。

### 2.3 等待期间的可感知性

- 预算耗尽/超时的返回契约继续携带 `obligations[] / terminal_delta / pending_count /
  next_action`，`suspend` 文案直接指向 `subagent_status`、`read_agent_events(view=tool_progress)`、
  `subagent_inspect_task` 三个只读巡检面。
- P1-5 的四项（前端下钻、父流节流镜像、`read_agent_events` 过滤视图、inline 审批）已按
  主计划实施（见其 §10 实施记录）；本节表述曾在首版中误写为"仍为后续项"，2026-09-26
  复核后修正。本轮不改变父子事件隔离边界，不动 SSE 面，只复用既有事件/工具面。

## 3. 验收与测试

| 用例 | 内容 |
| --- | --- |
| `internal/agentcontrol`：`TestWaitBudget*` | 连续计数、进展重置、键隔离、禁用与 nil 语义 |
| `internal/toolbroker`：`TestSuspendAgentWaitResultForBudget*` | suspend 判据字段、不覆盖 finalize、账本视图保留 |
| `cmd/aicli/commands`：`TestWaitAgentWaitBudgetEnforcesSuspendAfterNoProgress` | L2：第 1 次开窗→第 2 次带 suspend→第 3 次立即返回（<500ms，不谎报超时） |
| `internal/api/runtimeapi`：`TestSessionAgentControllerWaitBudgetStopsActiveWindows` | L2：API 宿主同判据，账本行不丢 |
| 既有回归 | `go test ./internal/toolbroker/... ./internal/agentcontrol/... ./internal/config/... ./internal/agentguidance/... ./internal/prompt/...` 全绿；`cmd/aicli/commands` 与 `runtimeapi` 全量见 §4 |

## 4. 验证证据（2026-09-26）

- `go build ./...`（backend）exit 0。
- 定向：`agentcontrol` / `agentguidance` / `config` / `prompt` / `toolbroker` 全绿；
  `go test ./cmd/aicli/commands/ -run TestWaitAgent` 与
  `go test ./internal/api/runtimeapi/ -run "TestSessionAgentControllerWait|TestPeekSubagentBatchStore"` 全绿。
- 全量 `internal/api/runtimeapi`（42s）全绿。
- 全量 `cmd/aicli/commands`（177–186s）：本改动相关用例全绿；2 个失败与本改动无关——
  1）`TestChatDebugDisplayShowsStorageSection` 断言 `Maintenance: runs=`，该文案在
  生产代码中已不存在（仅存于测试），是工作树里并行进行中的存储在途改动所致；
  2）`TestAICLIChatActorExecutor_AutoStartTeamMarksBaseSessionRunningUntilSettled`
  单测复跑通过（`ok 0.97s`），属既有满负载时序抖动类。两者均未触及 wait/账本路径。

## 5. 留白与后续

1. `wait_team`（broker 直连路径）尚未接入等待预算——本次只覆盖 `wait_agent`；
   `wait_team` 仍受 `maxWaitTimeoutMs=2m` 约束。接入需要 broker 侧的 caller-turn 键，
   建议与 §16.3"两者共享实现"一并收口。
2. P1-5 四项已实施，残留项是加固稿 H7 的运行期身份缺失与真机 probe（见 §2.3）；
   本轮未触碰。
3. 现场 51m 的具体 run 未逐一回放；按代码路径，其成因是"多次大窗口等待段聚合"，
   现已由 2m 上界 + 预算判据双重兜住。若仍需逐 run 取证，用事件日志核对
   每个 `wait_agent` 调用段的 `waited_ms`/`timed_out`。
