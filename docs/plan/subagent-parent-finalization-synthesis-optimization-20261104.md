# 主 Agent / 子 Agent 收尾机制优化建议（2026-11-04）

- 状态：`P0/P1 implemented; P2 peek implemented, metrics/UI pending`
- 日期：2026-11-04
- 范围：父 turn 的 obligation / finalization、子 Agent 批次完成后的 resume、结果包、收尾阶段、可观测性
- 关联文档：
  - `docs/plan/parent-turn-finalization-and-late-result-hardening-plan-20261103.md`
  - `docs/plan/implementation-execution-plan-20261103.md`
- 关联代码：
  - `backend/cmd/aicli/commands/chat_actor_agent_obligations.go`
  - `backend/cmd/aicli/commands/chat_actor_progress_check.go`
  - `backend/cmd/aicli/commands/chat_actor_host.go`
  - `backend/internal/chat/actor_finalization_sequence.go`
  - `backend/internal/subagentbatch/turn_suspension.go`
  - `backend/internal/subagentbatch/sqlite_store.go`
  - `backend/internal/agent/loop.go`
  - `backend/cmd/aicli/commands/exec_finalization.go`
  - `backend/internal/supervision/`

## 1. 结论摘要

本次用 `magpie` + `workbuddy-ai/deepseek-v4.1-flash` 做了真实多 Agent 时序测试。结论分两部分：

1. **安全门有效**：子 Agent 未全部 terminal 前，父 turn 不会 finalize；派发时写入 durable obligation，并产生 `turn.suspended`，父 turn 进入 `awaiting_obligations`。
2. **收尾活性不足**：子 Agent 全部返回后，父模型没有及时进入“汇总并收尾”阶段。目标模型在收到 batch completion 和 `subagent_wait` 结果后，仍然继续执行 shell / ls / 数据库查询，最终触发 3 分钟超时。

因此，当前机制解决的是“不能提前结束”，还没有完整解决“子 Agent 返回后必须及时结束”。

建议的核心方向：

```text
保留现有 obligation / finalization gate
+ 增加有界的 SYNTHESIZING 阶段
+ 把子 Agent 结果包直接注入 resume 上下文
+ 用 exploration stall 触发收尾
+ 对 unsupported reasoning effort 和路由降级做显式反馈
```

## 2. 实测场景

### 2.1 模型与路由

请求参数：

```text
provider = magpie
model    = workbuddy-ai/deepseek-v4.1-flash
effort   = max
```

实际子 Agent 路由：

```text
route_provider = magpie
route_model    = workbuddy-ai/deepseek-v4.1-flash
route_warnings = [
  explicit_provider_override_denied,
  explicit_reasoning_override_denied,
  reasoning_effort_unsupported_ignored
]
```

结论：

- 模型路由确实落在 `magpie` + `workbuddy-ai/deepseek-v4.1-flash`；
- `reasoning_effort=max` 没有生效；
- 原因不是调用失败，而是当前 magpie 的该模型能力声明没有 `reasoning_efforts`；
- 这个降级目前只出现在 debug log，没有进入工具返回值，父 Agent 无法感知。

### 2.2 场景 A：3 个子 Agent 错峰返回

使用 `subagent_spawn_batch` 派发 A/B/C 三个子 Agent，分别 sleep 15s / 30s / 45s，然后分析同一句话并返回 JSON。

| 子 Agent | 开始 UTC | 结束 UTC | 实际耗时 | 状态 |
|---|---|---|---|---|
| A | `10:19:23.475` | `10:19:38.495` | 15.019s | succeeded |
| B | `10:19:22.000` | `10:19:52.000` | 30.000s | succeeded |
| C | `10:19:23.819` | `10:20:08.846` | 45.030s | succeeded |

完成顺序：**A -> B -> C**。

父 Agent 的 `subagent_wait` 结果：

```text
ready_count = 3
waited_ms = 43518
obligations[0].state = completed
next_action = finalize
```

这说明三个子 Agent 全部 terminal 之前，父 Agent 没有拿到 `finalize` 许可；全部完成后才允许收尾。

### 2.3 场景 B：显式观察 pending 收尾门

再派发 D/E 两个子 Agent，分别 sleep 12s / 24s，并在派发后立即调用短窗口 `subagent_wait`。

第一次等待结果：

```text
timed_out = true
pending_count = 2
obligations[0].state = running
next_action = continue_independent_work_before_waiting_again
```

子 Agent 完成后：

```text
ready_count = 2
obligations[0].state = completed
next_action = finalize
```

这证明父 turn 在义务未结清时不能收尾。

### 2.4 场景 C：目标模型直接作为主 Agent

使用目标模型执行一次 aicli 父 Agent 测试：

```text
provider = magpie
model    = workbuddy-ai/deepseek-v4.1-flash
effort   = max
```

实际事件：

```text
10:30:36.793  turn.suspended       obligation_count=3
10:30:36.794  subagent.batch.created  batch_88c0710f35a51529
10:30:36.823  D/E 子任务开始
10:31:00.310  D/E 子任务完成
10:31:00.345  subagent.batch.completed
10:31:00.343  mailbox_received     "Subagent batch ... completed with status completed."
10:31:00.997  subagent_wait 完成
10:31:05.123  父模型开始 shell 排查
10:32:58.073  tool_loop.exploration_stall_observed  consecutive_readonly_steps=12
10:33:09.986  session_interrupted  context deadline exceeded
```

关键事实：

- 父 turn 在派发时确实挂起，没有提前 finalize；
- 子 Agent 在约 23.5s 后完成；
- 父模型在 `10:31:00.997` 已经拿到 `subagent_wait` 结果；
- 但它随后仍然执行了 20 次工具调用，其中 12 次是只读探索；
- 最终因 3 分钟超时被中断，没有输出最终 JSON。

这不是“父 turn 提前结束”，而是“父 turn 在义务结清后没有及时进入收尾”。## 3. 机制缺口

### G1：缺少显式的 `SYNTHESIZING` 阶段

当前状态机可以概括为：

```text
RUNNING
  -> AWAITING_OBLIGATIONS
  -> 子 Agent 全部 terminal
  -> 继续 RUNNING
```

义务结清后，父 Agent 仍然可以自由调用 shell、ls、数据库查询、重新派发子 Agent。运行时没有把“现在应该汇总并收尾”表达成一个受约束的阶段。

### G2：batch completion mailbox 缺少结果包

父会话收到的 mailbox 只有：

```text
Subagent batch batch_88c0710f35a51529 completed with status completed.
```

没有子 Agent 的实际输出、耗时、完成顺序、失败项或 artifact 引用。

虽然 `subagent_wait` 结果里有 `agents[].output`，但续跑 mailbox 没有同样的结果包。两处契约不一致，模型容易误判为“还需要自己去查”。

### G3：`subagent_status` / digest 历史噪声过大

本次 `subagent_status(include_digest=true)` 返回了约 200 行，其中 768 条历史 unresolved / action_required。大量后台作业与当前测试无关，但会进入父模型上下文，增加误判和探索概率。

### G4：exploration stall 只观测，不触发收尾

运行时已经能识别：

```text
tool_loop.exploration_stall_observed
consecutive_readonly_steps = 12
```

但当前只是记录事件，没有把它转化为“义务已结清，请立即汇总”的强制信号。

### G5：路由/能力降级没有反馈给调用方

`reasoning_effort=max` 被忽略，但 `subagent_spawn_batch` 返回值没有携带：

- `reasoning_effort_requested`
- `reasoning_effort_applied`
- `route_warnings`

调用方无法判断“max 是否真的生效”。

### G6：超时路径丢失子 Agent 结果

目标模型父进程最终返回：

```text
context deadline exceeded
status = failed
```

但子 Agent 结果已经完整返回。超时路径没有把子 Agent 结果和已完成义务写入结构化收尾。

### G7：等待窗口下限不利于观测

`subagent_wait(timeout_ms=5000)` 被 clamp 到 10000ms。对于测试和轻量观测，缺少非阻塞 peek 或更短窗口。

## 4. P0 优化建议

### P0-1：增加有界的 `SYNTHESIZING` 阶段

建议状态机：

```text
RUNNING
  -> AWAITING_OBLIGATIONS
  -> SYNTHESIZING
  -> FINALIZED
  -> FINALIZATION_INCOMPLETE
```

进入条件（满足任一）：

- 当前 turn 的所有 obligation 进入 terminal；
- `subagent_wait` 返回全部 ready；
- batch completion 已投递且没有新的 pending child。

进入 `SYNTHESIZING` 后：

- 禁止 `subagent_spawn` / `team_spawn`；
- 禁止 shell、ls、数据库查询、网络探索；
- 允许直接输出最终答案；
- 允许最多一次 `subagent_wait` / `subagent_status` 读取结果；
- 设置小预算，例如 `max_steps=2` 或 `30s`。

预算耗尽时自动生成：

```json
{
  "status": "finalization_incomplete",
  "reason": "synthesis_budget_exhausted",
  "batch_id": "batch_...",
  "child_results": []
}
```

设计要点：

- 不能因为进入 `SYNTHESIZING` 就丢失用户的新消息；新用户输入应进入新 episode；
- 不能把“预算耗尽”伪装成成功 finalize，应保留 `finalization_incomplete` 审计状态；
- 子 Agent 结果必须随收尾一起保存。

### P0-2：定义 `SynthesisPacket` 并注入 resume 上下文

建议 `subagent_wait` 和 batch completion mailbox 使用同一结构：

```json
{
  "batch_id": "batch_...",
  "status": "completed",
  "task_count": 2,
  "completed_count": 2,
  "failed_count": 0,
  "timed_out_count": 0,
  "elapsed_ms": 23519,
  "completion_order": ["D", "E"],
  "children": [
    {
      "task_id": "D",
      "status": "succeeded",
      "duration_ms": 19470,
      "summary": "...",
      "findings": [],
      "artifacts": [],
      "error": ""
    }
  ],
  "next_action": "synthesize_and_finalize"
}
```

要求：

- mailbox、resume context、`subagent_wait` 三处使用同一 schema；
- 默认包含每个 child 的 `summary`，而不是只给 task id；
- 失败/超时 child 必须包含 `error`、`failure_category`、`partial_results`；
- 结果包应有大小上限，超出时给 artifact 引用。

### P0-3：把 exploration stall 作为收尾触发器

当以下条件同时满足：

- 当前 turn 所有 obligation 已 terminal；
- 检测到连续只读探索；
- 没有新的用户输入；

注入系统级提示：

```text
All required child obligations are terminal.
Do not inspect internal storage or run more shell commands.
Use the provided SynthesisPacket and produce the final answer now.
```

如果下一步仍然不是最终答案：

- 进入 `SYNTHESIZING`；或
- 直接按 `finalization_incomplete` 收尾并保留结果包。

这样可以避免本次测试中的 20 次工具调用和 2 分钟空转。

### P0-4：`subagent_status` / digest 默认按当前 turn/batch 收敛

建议默认：

```text
scope = current_turn
include_resolved = false
include_historical = false
```

增加显式参数：

```text
scope=current_turn
scope=current_batch
batch_id=batch_...
turn_id=turn_...
include_resolved=true
```

目标：

- 当前测试只返回 2-3 行，而不是 200 行；
- 历史后台作业默认不进入父模型上下文；
- 全量 supervision 账本只在运维排查时显式读取。

### P0-5：把 route/capability warning 放进工具结果

`subagent_spawn_batch` / `subagent_spawn` 返回值建议包含：

```json
{
  "route_provider": "magpie",
  "route_model": "workbuddy-ai/deepseek-v4.1-flash",
  "reasoning_effort_requested": "max",
  "reasoning_effort_applied": false,
  "route_warnings": [
    "reasoning_effort_unsupported_ignored"
  ]
}
```

策略选项：

- 显式 `reasoning_effort` 不受支持时返回 warning；
- 或在严格模式下 fail fast，要求调用方确认降级；
- 不允许静默忽略。

如果 magpie 的该模型实际支持 `max`，应同步补模型能力声明：

```yaml
reasoning_model: true
reasoning_efforts:
  - high
  - max
```## 5. P1 优化建议

### P1-1：增加显式 `turn.resumed` 事件

建议事件：

```text
turn.resumed
  reason=obligations_settled
  turn_id=...
  episode_id=...
  batch_id=...
  child_results_available=true
  next_action=synthesize_and_finalize
```

用途：

- 明确告诉模型和 UI：这是同一个 turn 的 continuation，不是新的用户请求；
- 让 resume 路径可以携带结果包摘要；
- 便于统计“子 Agent 完成到父 turn finalize”的延迟。

### P1-2：区分 `obligations_settled` 与 `results_consumed`

建议在 turn suspension / obligation 记录中增加：

```text
obligations_settled = true
results_consumed    = true/false
```

父 turn finalize 前可以选择：

- 要求 `results_consumed=true`；或
- 自动把 `SynthesisPacket` 注入 resume context，视为已消费。

这样可以避免两种极端：

- 子 Agent 没返回就收尾；
- 子 Agent 返回了但父 Agent 没读结果就收尾。

### P1-3：超时/中断路径输出结构化收尾

当 exec / headless 的 deadline 到达时：

- 如果所有子 Agent 已 terminal，输出：

```json
{
  "status": "finalization_incomplete",
  "reason": "parent_exec_timeout",
  "child_results": [],
  "completed_obligations": 3
}
```

- 如果仍有子 Agent pending，输出：

```json
{
  "status": "incomplete",
  "reason": "parent_exec_timeout",
  "pending_children": [],
  "partial_results": []
}
```

不要只返回裸 `context deadline exceeded`。

### P1-4：子 Agent 结果使用稳定 schema

建议约定：

```json
{
  "summary": "...",
  "findings": [],
  "artifacts": [],
  "status": "succeeded",
  "error": ""
}
```

可选支持 `output_schema` / `result_schema`，让父 Agent 可以直接合并，不需要理解每个子 Agent 的自定义 JSON。

### P1-5：终态 batch 自动 close

子 Agent terminal 后自动收敛 batch / child session：

- 释放 live slot；
- 减少历史 supervision 噪声；
- 避免父 Agent 再次看到“可关闭”的终态行。

可以保留显式 `subagent_close` 作为兜底。

## 6. P2 优化建议

1. **增加非阻塞 `subagent_wait` peek**
   - 允许 `timeout_ms=0` 或单独的 `peek=true`，不必等待 10s。

2. **增加收尾指标**
   - `time_from_batch_terminal_to_parent_finalize`
   - `post_settlement_tool_calls`
   - `post_settlement_shell_calls`
   - `route_warning_count`
   - `reasoning_effort_applied`
   - `finalization_timeout_count`
   - `results_consumed_before_finalize`

3. **UI/TUI 显示 turn 阶段**
   - `awaiting_obligations`
   - `synthesizing`
   - `finalization_incomplete`
   - 当前 batch 的子 Agent 完成顺序和耗时。

4. **内部存储查询提示**
   - 父 Agent 查询 `~/.aicli/data/subagent-batches` 等内部路径时，工具结果可以提示：
     `Use subagent_wait / SynthesisPacket instead of querying internal storage.`

## 7. 实现映射建议

| 能力 | 建议落点 |
|---|---|
| `SYNTHESIZING` 阶段 | `backend/internal/chat/actor.go`、`backend/internal/agent/loop.go` |
| finalization budget / incomplete | `backend/internal/chat/actor_finalization_sequence.go` |
| obligation 状态与 `results_consumed` | `backend/internal/subagentbatch/turn_suspension.go` |
| batch 结果包 | `backend/internal/subagentbatch/sqlite_store.go`、batch coordinator |
| mailbox 注入结果包 | `backend/cmd/aicli/commands/chat_actor_host.go`、`chat_actor_agent_obligations.go` |
| stall 触发收尾 | `backend/internal/agent/loop.go`、`backend/cmd/aicli/commands/chat_actor_progress_check.go` |
| status/digest 收敛 | `backend/internal/supervision/`、`subagent_status` 工具实现 |
| route warning 透传 | `backend/internal/agent/child_factory.go`、`subagent_spawn_batch` 结果 |
| exec 超时收尾 | `backend/cmd/aicli/commands` exec 路径 |

## 8. 验收标准

使用同一组场景回归：

```text
3 个子 Agent：15s / 30s / 45s
```

必须满足：

1. 子 Agent 未全部 terminal 前，父 turn 不能 finalize；
2. 第一次 `subagent_wait` 返回 `pending_count=3`；
3. 子 Agent 全部完成后，父 turn 在最多 2 个 step 内输出最终结果；
4. 父 Agent 不再调用 shell/ls 查询内部 batch 存储；
5. 最终输出包含 `completion_order`、每个子 Agent 的 `duration`、`status`；
6. 如果父模型超时，输出 `finalization_incomplete` + 子 Agent 结果，而不是裸超时；
7. `reasoning_effort=max` 要么实际生效，要么在工具返回中明确标记未生效；
8. `subagent_status(scope=current_turn)` 只返回当前 turn 的义务，不返回历史 200 行。

补充回归：

```text
TestParentTurnSuspendsUntilBatchTerminal
TestParentTurnSynthesisPhaseForbidsNewSpawns
TestBatchCompletionMailboxCarriesSynthesisPacket
TestExplorationStallTriggersSynthesis
TestFinalizationTimeoutKeepsChildResults
TestSubagentStatusScopesToCurrentTurn
TestReasoningEffortUnsupportedIsVisible
```

## 9. 非目标 / 不要改的地方

- 不要取消 `turn.suspended` 或 obligation gate；
- 不要在子 Agent 未完成时自动 finalize；
- 不要默认关闭 auto wake；
- 不要只靠提示词让模型等待；
- 不要把“收尾预算耗尽”伪装成成功 finalize；
- 不要让父 Agent 为了拿子 Agent 结果去查询内部 SQLite。

## 10. 证据路径

本次测试证据：

```text
direct batch A/B/C:
  batch_f1c69cb6675b5cca

direct guard probe D/E:
  batch_6e2cf93262c3f8f3

target-model parent run:
  batch_88c0710f35a51529
  E:\temp\magpie-parent-child-guard\2026\11\04\session_20261104182937_4R8gO7tX\events\runtime-events.jsonl

child route/duration:
  C:\Users\vince\.aicli\chat-logs\2026\11\03\session_20261103225441_3w7BFl6B\debug\debug.log
```

关键事件：

```text
10:30:36.793  turn.suspended       obligation_count=3
10:31:00.345  subagent.batch.completed
10:31:00.997  subagent_wait 完成
10:31:05.123  父模型开始 shell 排查
10:32:58.073  tool_loop.exploration_stall_observed
10:33:09.986  session_interrupted  context deadline exceeded
```

## 11. 推荐落地顺序

```text
Phase 1（P0）:
  1. SynthesisPacket 统一契约
  2. SYNTHESIZING 阶段 + 小预算
  3. stall -> synthesis 触发
  4. subagent_status 默认 current_turn/batch

Phase 2（P1）:
  5. turn.resumed + resume_reason
  6. results_consumed
  7. 超时结构化收尾
  8. route warning 透传

Phase 3（P2）:
  9. 指标与 UI
  10. 非阻塞 peek
  11. 自动 close / 存储提示
```

最小可落地版本：

```text
batch completion mailbox 携带 SynthesisPacket
+ 父 turn 收到结果后进入 SYNTHESIZING
+ SYNTHESIZING 禁止 shell/新 spawn，最多 2 step
+ 超时输出 finalization_incomplete
```

这组最小改动即可显著降低“子 Agent 已返回但父 Agent 继续空转”的概率。

## 12. 实施状态（2026-11-04）

### 已实现（P0 + P1 + P2 peek）

1. **`SynthesisPacket` 统一契约**
   - `toolbroker.AgentWaitResult` 新增 `synthesis` 与 `finalization_required`；
   - `subagent_wait` 返回时自动附加 compact child result packet；
   - batch terminal payload / mailbox metadata 同步携带 `synthesis_packet`；
   - mailbox body 明确追加 `SynthesisPacket attached; synthesize and finalize.`。

2. **batch terminal 结果包**
   - `SubagentBatchCoordinator` 从 durable task ledger 构建 `SynthesisPacket`；
   - 包含 task id、child session、status、duration、summary、error、artifact ref、route warnings；
   - 覆盖实时 terminal 与 replay delivery 两条路径。

3. **有界 `SYNTHESIZING` 门**
   - `ReActLoop` 在 `subagent_wait` 返回 `finalization_required=true` 后进入 synthesis 阶段；
   - 后续工具批次被拒绝，返回 `synthesis_required`；
   - 连续 2 个被拒绝的工具批次后，下一次迭代输出 `synthesis_budget_exhausted` 的确定性收尾包；
   - 不再允许父模型在义务结清后继续 shell / ls / 数据库探索。

4. **route warning 透传**
   - `subagent_spawn_batch` 回执中的 task rows 增加 `route_warnings`、provider、model、reasoning effort、route source；
   - `TaskResult` 持久化 child route warnings，供 batch synthesis packet 使用；
   - batch schema 的 `reasoning_effort` 枚举接受 `max`，不支持时由 route warning 显式反馈。

5. **`turn.resumed` 载荷增强**
   - `trigger=settled` 的 `turn.resumed` 增加 `resume_reason=obligations_settled`；
   - 增加 `child_results_available`，标识该挂起 turn 是否包含 batch / agent_session 结果。

6. **`subagent_status` 的 `scope=current_turn` / `current_batch`**
   - 新增 `scope` 参数：`auto`（默认）/ `all` / `current_turn` / `current_batch`；
   - `auto` 在调用方拥有当前 turn 的 obligation subjects 时收敛到 `current_turn`，否则回退 `all`；
   - `current_turn` 从当前挂起/运行 turn 的 obligation ids 解析 subject ids，只保留当前 turn 的矩阵行；
   - `current_batch` 用 `batch_id` 保留该 batch 及其 child sessions，并校验 batch 属于调用方会话；
   - 过滤后重新计算 summary，保留 pending wake 等 scope 级队列指标；
   - 宿主把实际生效的 scope 写入 `filter_scope`；非 `all` 下不附带历史 lifecycle digest，返回 `digest_skipped` 提示；需要历史 digest 时显式使用 `scope=all`。

7. **`results_consumed` 与 finalization gate 联动**
   - `TurnSuspension` 新增 durable `results_consumed`；
   - SQLite schema v5 新增 `results_consumed` 列，旧记录迁移时置为已消费以保持升级前行为；
   - `subagent_wait` 返回 `finalization_required=true` 时写入已消费；
   - batch-only 终态 resume prompt 已内联结果摘要/artifact，投递被接受后写入已消费；
   - `agent_session` 结果未被 resume prompt 内联，仍要求父 Agent 通过 `subagent_wait` 读取后才写入已消费；
   - `settleParkedTurnOnRunEnd` 仅在该 epoch 的全部义务终态且 `results_consumed=true` 时清除挂起记录；
   - CLI/API 的孤儿清扫使用同一门禁：未消费结果的记录保留并继续排结算 wake；
   - 新追加的子 Agent 义务会把同一 epoch 重置为未消费。

8. **exec/headless 超时的结构化收尾**
   - deadline 路径不再只返回裸 `context deadline exceeded`；
   - 输出 `status=finalization_incomplete`、`reason=parent_exec_timeout`、`child_results`、`completed_obligations`、`pending_obligations`、`batch_ids`；
   - 子结果来自 durable batch/task ledger 与 park 记录，超时后仍可读取；
   - `turn.failed` / `ExecFinalResult` / `--output-last-message` 共用同一结构化载荷；
   - 退出码仍为 `124`，并保留 `errors.Is(err, context.DeadlineExceeded)`。

9. **非阻塞 `subagent_wait` peek**
   - `subagent_wait(peek=true)` 只做一次状态/邮箱快照，立即返回；
   - 不打开活动等待窗口、不消耗 active-wait 预算、不触发等待超时；
   - `peek` 仍复用 obligation ledger 与 `SynthesisPacket` 判定，因此终态结果可直接进入收尾；
   - pending 快照返回 `peek_pending` 指引，空快照返回 `peek_no_changes`，避免把 peek 当成轮询；
   - CLI local runtime 与 API runtime host 同步实现。

10. **测试**
   - `TestAttachSynthesisPacketBuildsTerminalPacket`
   - `TestAttachSynthesisPacketStaysOpenWhilePending`
   - `TestBuildSubagentBatchTerminalMailboxCopiesSynthesisPacket`
   - `TestTerminalPayloadCarriesSynthesisPacket`
   - `TestObserveSynthesisWaitResultsEntersSynthesis`
   - `TestObserveSynthesisWaitResultsIgnoresPending`
   - `TestDenySynthesisToolCallsDeniesAndExhaustsBudget`
   - `TestSynthesisFallbackOutputIncludesPacket`
   - `TestSynthesisPacketBoundsChildren`
   - `TestFilterSnapshotToSubjectIDsKeepsOnlyCurrentSubjects`
   - `TestSubjectIDFromObligation`
   - `TestParseSubagentStatusArgsScope`
   - `TestSubagentStatusCurrentTurnSkipsHistoricalDigest`
   - `TestSubagentStatusAutoUsesResolvedFilterScopeForDigest`
   - `TestLocalSupervisionCurrentTurnSubjectIDs`
   - `TestLocalSupervisionCurrentBatchSubjectIDs`
   - `TestLocalSupervisionAutoScopeUsesCurrentTurnWhenSubjectsExist`
   - `TestLocalSupervisionAutoScopeFallsBackToAllWithoutCurrentTurn`
   - `TestHandlerSupervisionToolController_AutoScopeUsesCurrentTurnWhenSubjectsExist`
   - `TestHandlerSupervisionToolController_AutoScopeFallsBackToAllWithoutCurrentTurn`
   - `TestSettleParkedTurnAnnouncesResumeClosure`（扩展 `resume_reason` / `child_results_available`）
   - `TestObserveSynthesisWaitResultsMarksDurableResultsConsumed`
   - `TestSQLiteBatchStoreTurnSuspensionRoundTrip`（扩展 `results_consumed` 往返/重置/重开）
   - `TestTurnSuspensionResultsConsumedMigrationBackfillsLegacyRows`
   - `TestLocalHostMarksTurnResultsConsumed`
   - `TestResumePromptAgentSessionDirectsParentToWait`
   - `TestSweepSettledParkedTurnsKeepsUnconsumedChildResults`（CLI + API）
   - `TestBuildExecFinalizationIncompletePreservesChildResults`
   - `TestBuildExecFinalizationIncompleteWithoutControlPlane`
   - `TestHandleExecTurnErrorEmitsStructuredFinalizationIncomplete`
   - `TestFinalizeAgentWaitResultPeekGuidance`
   - `TestBrokerWaitPassesPeekArg`
   - `TestApplyAgentWaitLedgerPeekPendingOverridesEmptySnapshotGuidance`
   - `TestWaitAgentPeekReturnsImmediately`（CLI + API）
   - `TestLocalActorRegistry_WaitPeekReadsParentMailboxWithoutBlocking`

### 尚未实现（后续）

- synthesis 指标与 UI 展示；

### 验证命令

```text
go test -p 1 ./internal/agent -count=1
go test -p 1 ./internal/toolbroker -count=1
go test ./internal/subagentbatch -count=1
go test ./internal/supervision -count=1
go test -vet=off ./cmd/aicli/commands -run 'TestBuildExecFinalizationIncomplete|TestHandleExecTurnError|TestLocalHostMarksTurnResultsConsumed' -count=1
go test -vet=off ./cmd/aicli/commands -run 'TestWaitAgentPeekReturnsImmediately|TestLocalActorRegistry_WaitPeekReadsParentMailboxWithoutBlocking' -count=1
go test -vet=off ./cmd/aicli/commands -run 'TestLocalSupervisionAutoScope' -count=1
go test -vet=off ./internal/api/runtimeapi -run 'TestHandlerSupervisionToolController_AutoScope|TestWaitAgentPeekReturnsImmediately' -count=1
go test -vet=off ./cmd/aicli/commands -run 'TestWaitAgent|TestLocal.*Batch|TestLocalSubagent|TestLocalAgent' -count=1
go test -vet=off ./internal/api/runtimeapi -run 'Test.*(Subagent|Batch|Wait|Supervision)' -count=1
go test -vet=off ./cmd/aicli/commands -count=1
go test -vet=off ./internal/api/runtimeapi -count=1
go vet ./internal/agent ./internal/subagentbatch ./internal/supervision ./internal/chat ./internal/api/runtimeapi ./cmd/aicli/commands
```

### 边界

- 仍然保留原有 obligation / finalization gate；
- 不在子 Agent 未完成时自动 finalize；
- synthesis 预算耗尽时输出的是 `synthesis_budget_exhausted` 的确定性结果，不伪装成正常成功 finalize；
- 父模型的新用户输入仍按新 episode 处理，不进入 synthesis 强制门。
