# A4 定位：会话型子代理被取消时的部分产物缺口（2026-09-27）

- **状态**：**已实施**（`11e499e5`，2026-09-27；A3 见 `f4cd672e`）
- **实施摘要**：
  - 新增 `backend/internal/chat/actor_partial_product.go`：`partialRunProduct(result, session)` 产出有界（2,000 runes + 省略号）部分产物；取源优先级 = 历史里最后一条非空 assistant 消息 → `result.Output` 兜底；`partial_steps` = 历史中已完成的工具结果条数。
  - 接线：`backend/internal/chat/actor.go` 终态 payload（`session_end` 与 hook 共用），触发条件 `!success || cancelSource != ""`。
  - 测试：`TestPartialRunProductPrefersResultOutputThenHistory`、`TestClipPartialProductBoundsRunawayTranscripts`、`TestSessionActorCanceledRunCarriesPartialProduct`（端到端，取消后载荷带 `partial_summary/partial_source/partial_steps`）；`internal/chat` 全包 ok(35.5s)。
  - **实施中实测到的两条语义教训**（已写进实现与测试）：
    1. 取消路径上 agent 可能把 `success` 记为 `true`（带部分结果的"优雅停止"），因此触发条件不能只看 `!success`；
    2. 取消时 `result.Output` 常是宿主罐头停止提示（"当前运行已停止；已保留 N 条工具观察…"），绝不能优先于历史产物——否则真正的产物会被提示语盖住。
  - 未接入：`actor.go` 约 4140 行的 `resume` 型终态发射点（如需覆盖再补）。
- **关联**：
  - 复盘 `docs/analysis/session-20260926205017-subagent-runtime-postmortem-20260926.md`
  - A3 提交 `f4cd672e`（子代理执行脱离父 run 的干净结束/挂起）
  - 取消来源埋点 `2797664a`；用户中断先打标 `9e9a681f`

## 1. 现象（本机三次复现）

会话型子代理（`spawn_agent`）在父回合挂起后被 `context canceled`：

| 子会话 | input tokens | output | durable result |
|---|---|---|---|
| `session_20260927120118_zlCpjgMb` | 828,893 | 15,330 | 16 runes（`"context canceled"`） |
| `session_20260927120609_fv1KEUSd` | 946,892 | 16,039 | 16 runes |
| `session_20260927123904_6YABRsPX` | 880,834 | 6,854 | 16 runes |

`subagent_inspect_task` 返回 `source=completion_payload`，`summary="context canceled"`，无任何工作痕迹；父代理只能从 token 级事件流（13k+ 事件）里大海捞针（实测捞不回）。

## 2. 现状核实：批次路径**已有**部分产物，会话路径没有

- **批次（`spawn_subagents`）已实现**：
  - `internal/agent/subagent_batch_coordinator.go:1715-1716,1749-1750` 把 `Findings/Patches` 写入任务 `result_json`；
  - `internal/subagentbatch/types.go:309-310`（`Result.Findings/Patches`）；
  - 验收测试 `internal/agent/subagent_batch_acceptance_test.go:18`（"result_json.summary 里已经存有完整交付物，失败原因只是…"）；
  - `internal/agent/result_contract.go:66,111` 明确"失败但 Findings/Changes 非空"要单独保留。
- **会话（`spawn_agent`）缺失**：终态 payload 只有 `status/errors/usage/summary=err.Error()`，没有任何 in-flight 产物。

## 3. 缺口定位（修复点）

终态 payload 的链路是"**run 终态事件 → 投影**"，没有独立的快照层：

1. 投影（读取侧，不用改）：
   - CLI：`cmd/aicli/commands/chat_actor_registry.go:1293` → `supervision.ProjectAgentCompletion(...)`
   - API：`internal/api/runtimeapi/session_runtime_support.go:1092,1162`
   - 二者只接收 `status` + `sourceEventType`，payload 取自事件本身。
2. 事件发射（**修复点**）：`internal/chat/actor.go` 的 SessionActor run 终态路径 —— 已有取消来源埋点（`2797664a`：`cancel_cause`）与"用户中断先同步打标"（`9e9a681f`）；`cancelCause(errSessionRunFinished)` 在 `actor.go:2850`。
3. 矩阵行 `reason="child session failed"` 来自 `internal/supervision/projection.go:156`（纯展示，不是修复点）。

## 4. 最小修复规格

在 **run 终态且非成功**（canceled / interrupted / failed）时，向终态事件 payload 增加一个有界的 `partial_summary`：

- 取子会话最后一条 assistant 消息内容，截断到 N runes（建议 2,000–4,000，与 `MaxSnapshotResultSummaryRunes=512` 分开，避免再触发 P1-1 的 512 截断争议）；
- 附 `partial_steps`：已完成的工具调用计数/最近 3–5 条工具结果摘要（复用 `cache_safe_summary` 侧既有归约，勿新造）；
- 投影层（`ProjectAgentCompletion`）原样落库，`subagent_inspect_task` 的 summary 展示路径已能读；
- **不改**：失败状态语义、`wait_agent`/`suspend` 判定、预算逻辑；R1 无自动重派。

## 5. 回归测试规格

`internal/chat` 或 `internal/agentcontrol` 侧加一条：
1. 起一个会产出至少一条 assistant 消息的子 run；
2. 触发真实取消（`CancelSessionRun`/中断源，**不用** `errSessionRunFinished` 的干净结束——那条已被 A3 测试覆盖）；
3. 断言终态 payload 的 `partial_summary` 非空、且等于最后一条 assistant 消息的前缀（含截断语义）；`partial_steps` 计数与已产生的工具调用一致。

## 6. 边界与风险

- A3 已修复"父回合干净结束/挂起"导致的取消；**A4 只覆盖真实取消**（用户中断、显式停止、超时/父被显式取消）。
- 大小上限必须由宿主强制（否则大 transcript 会反噬历史预算，与 `output/tool_result_content.go` 的折叠策略一致即可）。
- 若未来要覆盖"任务中途崩溃"（非取消），同一 payload 字段可直接复用。
