# A4 定位：会话型子代理被取消时的部分产物缺口（2026-09-27）

## 真机端到端验证（2026-09-27，远程 web API）

- **环境**：node `node-17180` @ `http://127.0.0.1:52607`，会话 `session_20260927130357_TH4YuaUl`；运行二进制 `backend/aicli-2x.exe`（`go version -m` 读出 `vcs.revision=b74cc097`，为 `f4cd672e`/`11e499e5` 的后代，即包含 A3+A4）。
- **方法**：`POST /web/api/invoke` 远程注入 prompt 并等 turn 结束（`client_request_id` 幂等）；证据取自本机事件库 `~/.aicli/sessions/runtime/session_runtime.sqlite` 的 `session_events.payload`。
- **A3 通过（两个触发场景）**：
  1. 子代理 `session_20260927131033_zEWajnFb` 在**父回合结束**（13:10:31）后继续运行，心跳首行 13:11:18，12 轮跑到 13:12:22 自然完成；
  2. 子代理运行中给父会话**注入新消息**（13:11:42 的 `ACK2` turn）后，子代理仍持续写入心跳直到 13:12:22。
- **A4 通过**：对运行中的子代理 `session_20260927131714_t2qLAQJ7` 执行 `close_agent`（中途取消，取消时仍处于第 2 步长工具调用），其 `session_end` 载荷：

```json
{"cancel_source":"execution_context","cancel_cause":"actor_stop","success":false,"status":"stopped",
 "error":"context canceled","steps":2,"partial_source":"last_assistant_message","partial_steps":2,
 "partial_summary":"Step 1 complete (`backend/.tmp/a4b-start.txt` contains `started`).\n\n**Step 2** — single `Start-Sleep -Seconds 150` call (no splitting, no polling; long timeout to cover the full 150s):"}
```

  对照修复前：同类取消的 durable result 只有 16 runes 的 `"context canceled"`，零产物。
- **附带观察（建议单独跟进）**：
  1. `difficulty=easy/normal` 的子代理均报 `route_source=disabled` + `route_warnings=["permission_mode_inherited_from_parent"]`；不阻断执行（走默认模型），但难度路由在这套配置下实际未生效；
  2. **子代理 `queued` 语义澄清（原观察作废）**：子会话在 `Spawn` 内**同步创建**
     （CLI `chat_actor_registry.go:697`、API `session_runtime_support.go:558`），首轮
     prompt 由 `SubmitChildPromptAsync` **立即异步提交**（CLI 同文件 770 行；该处注释
     即 A3 的"子代理执行脱离父 run"取消隔离）；spawn 回执的 `Flags: created, queued`
     只是 `result.Created/Queued`（787-788 行），**不是调度积压**。实测 spawn→子代理
     首次工具执行 30–45s 是**首次 LLM 往返**（大 prompt）耗时；此前记录的 ~3min 系
     时间线推断错误（叠加轮询了错误路径），已在 §7 更正；
  3. `/web/api/invoke` 幂等语义实测正确：同一 `client_request_id` 重放返回 `duplicate=true` 且不重跑（token 无增长）。

- **状态**：**已实施**（`11e499e5`，2026-09-27；A3 见 `f4cd672e`）
- **实施摘要**：
  - 新增 `backend/internal/chat/actor_partial_product.go`：`partialRunProduct(result, session)` 产出有界（2,000 runes + 省略号）部分产物；取源优先级 = 历史里最后一条非空 assistant 消息 → `result.Output` 兜底；`partial_steps` = 历史中已完成的工具结果条数。
  - 接线：`backend/internal/chat/actor.go` 终态 payload（`session_end` 与 hook 共用），触发条件 `!success || cancelSource != ""`。
  - 测试：`TestPartialRunProductPrefersResultOutputThenHistory`、`TestClipPartialProductBoundsRunawayTranscripts`、`TestSessionActorCanceledRunCarriesPartialProduct`（端到端，取消后载荷带 `partial_summary/partial_source/partial_steps`）；`internal/chat` 全包 ok(35.5s)。
  - **实施中实测到的两条语义教训**（已写进实现与测试）：
    1. 取消路径上 agent 可能把 `success` 记为 `true`（带部分结果的"优雅停止"），因此触发条件不能只看 `!success`；
    2. 取消时 `result.Output` 常是宿主罐头停止提示（"当前运行已停止；已保留 N 条工具观察…"），绝不能优先于历史产物——否则真正的产物会被提示语盖住。
  - **已接入 resume 型终态**（`7f7927da`）：`finishPendingBatchRecovery`（恢复未完成工具批失败/被取代）此前只有 `status/error/steps=0`，现复用 `pendingBatchRecoveryPayload` 带出同样的 `partial_*` 字段；`steps` 语义不变，仅在有产物时新增键。
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

## 7. 残余与判断（2026-09-27 收尾）

- **`session_interrupted` 型终态已接入（两阶段：`0b3c76b8` 首版 → `8a9d17fe` +
  `87d0b0d8` 修正；真机验证通过）**：中断路径（用户中断 `handleInterrupt`；停摆超时
  `abortStalledRun`）以 `session_interrupted` 作为该 turn 的终态，现带 `partial_*`。
  1. **首版为何不够**：`0b3c76b8` 从会话存储读产物；真机复验（node-32004/59920）显示
     ESC 后该事件仍为空——运行期只有按 `checkpointInterval` 节流的中途落库，权威落库在
     turn 结束（`actor.go:2896`），中断时存储快照可能只有用户 prompt。
  2. **修正**：产物快照挂到 run 上（`runPartialProduct`，atomic 整体替换）并在每个
     durable 历史提交点刷新；`8a9d17fe` 后真机仍失败，原因是 loop 在
     `AppendAssistantAction`（`loop.go:1191`）后**直接执行工具**、中间无 checkpoint 通知，
     工具运行期间 assistant 文本"已产出但未提交"——`87d0b0d8` 让 loop 在工具执行前补发一次
     **只通知不落库**的 checkpoint（契约要求宿主实现幂等），chat 侧改为优先用回调携带的
     `messages` 取产物。取源优先级：run 快照（零 I/O）→ **没有活动 run 时**兜底读存储
     （fail-open + 200ms 上限）；有 run 但尚无产物时不回退存储（避免把上一轮旧产物当本轮）。
  3. **真机证据（node-29052/64090，二进制含 `87d0b0d8`；A7）**：长工具
     （`Start-Sleep -Seconds 120`）运行中 ESC，`session_interrupted` 载荷
     `{"turn_id":…,"reason":"interrupt","partial_summary":"A7-PROOF 我准备执行一条休眠 120 秒
     …","partial_source":"last_assistant_message","partial_steps":0}`；30ms 后 `session_end`
     同样带产物（`steps:1`）。停摆路径没有 `session_end` 兜底，故该修复对 stall 尤为关键。
- **"难度路由 disabled + permission 告警"不是缺陷**：`route_source=disabled` 是"未配置
  difficulty→模型映射"的如实上报（子代理走默认模型）；`permission_mode_inherited_from_parent`
  是 `internal/toolbroker/spawn_agent_permission.go:15-18` 的有意设计（让父代理能解释子代理
  实际采用的权限模式），并有意进入路由回执/统计。
- **子代理 `queued`→启动延迟：查证为非问题（原观察作废）**：子会话在 `Spawn` 内同步
  创建（`chat_actor_registry.go:697` / `session_runtime_support.go:558`），首轮 prompt
  立即异步提交（`chat_actor_registry.go:770`，其注释即 A3 修复），`queued` 仅表示
  "已提交首轮 prompt"（787-788 行）。spawn→首次工具执行的 30–45s 属首次 LLM 往返
  耗时，非调度积压；~3min 的说法是时间线推断错误。**无需改动**（supervision 的
  wake/resume 容量门控只影响**父会话**的自动唤醒，与子代理启动无关）。
