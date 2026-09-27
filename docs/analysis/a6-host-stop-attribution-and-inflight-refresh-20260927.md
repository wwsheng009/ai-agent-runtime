# A6 根因：宿主"运行时刷新"硬停 actor 取消在途 turn（2026-09-27）

## 一句话根因

`refreshLocalRuntimeAfterModelSelection`（`backend/cmd/aicli/commands/chat_actor_host.go`）**无条件**
`SessionHub.StopContext` 驱逐会话 actor，而 actor 正是在途 run 的所有者：
`StopContext → StopAsync → cancelActive()` 以 `actor_stop` 取消 run ctx。于是任何一次
运行时切换（`/model`、`/provider`、`/reasoning`、`/routing`、`/add-dir`、模型选择器、ACP 配置项，
含**同值重选**）都会把正在工作的回合打断，用户只看到一句 `context canceled`，
现场没有任何"是谁停的"记录。

## 1. 现象与证据

会话 `session_20260927073805_QbWBceF5`（`aicli resume <sid> --yolo --pprof --debug`，
PID 15632，15:39:43 启动）在回合执行中被停：

```
seq 570440  2026-09-27T07:45:29Z  session_end
{"cancel_cause":"actor_stop","cancel_source":"execution_context","success":false,
 "status":"stopped","error":"context canceled","steps":3,"duration":290641,
 "partial_source":"last_assistant_message","partial_steps":89}
```

时间线（本地 CST）：

| 时刻 | 事件 |
|---|---|
| 15:40:35 | 用户提交「继续执行」，run 开始（step 1/unlimited） |
| 15:42:32 | step 3 请求发出 |
| 15:45:09 | step 3 响应成功（completion 11413 tokens），模型发出 7 个工具调用 |
| 15:45:12–15:45:15 | 5 个 `write` 全部成功（transport 层 4 个文件） |
| 15:45:15.0 | 两个 `grep` 已请求；**延迟约 8.5s** 后才开始执行 |
| 15:45:23.6 / 15:45:26.3 | 两个 `grep` 以 `context canceled`（`AGENT_RUN_CANCELED`）结束 |
| 15:45:26.5 | `agent.turn.finished`（elapsed 290.6s） |
| 15:45:29 | `session_end`（actor_stop）+ 罐头消息「当前运行已停止；已保留 11 条工具观察…」 |

**归因链（代码证实）**：

- `actor_stop` 在全仓库只有一个生产点：`SessionActor.cancelActive()`
  （`backend/internal/chat/actor.go`），只被 `StopAsync()/StopContext()` 调用；
- CLI 非测试代码里能停"当前会话 actor"的调用点共 6 处（`chat_interrupt.go` 用户中断先打标；
  `chat_actor_registry.go` 子会话回滚/close_agent；`chat_profile_switch.go` 有在途延迟守卫；
  `chat_team_lifecycle.go` 只处理 teammates；`hub` 驱逐只针对 idle/已停），
  **唯一能在无 `user_interrupt` 打标、有在途 run 时停掉当前会话的就是
  `chat_actor_host.go:refreshLocalRuntimeAfterModelSelection`**；
- 它的调用方即模型/provider/reasoning/路由/目录/会话恢复一族
  （`chat_model_switch.go`、`chat_model_command.go`、`chat_model_picker.go`、
  `chat_reasoning_switch.go`、`chat_reasoning_command.go`、`chat_command_result.go`、
  `chat_routing_command.go`、`chat_add_dir.go`、`chat_session.go`），且
  `applyUnifiedModelCommandSelection` **明确支持"同目标重选"**（仅为能力窗口对账）。

**排除项**：非用户中断（会写 `user_interrupt`，运行二进制 `02920a8a` 已含 `9e9a681f`）；
非 close_agent（本会话 `/root`、无 parent）；非超时/停摆（cause 不同，`runStallTimeout` 默认关闭）；
非 supervision 控制动作（窗口内 `supervision_actions` 0 行）；非 mesh 抢租约（journal 无事件，
租约 owner/renewed 正常）；非进程退出（进程存活并在服务 56053）；非其它会话/进程（时间窗内
事件库只有本会话事件）。

**为什么"像 LLM 自然停止导致"**：run ctx 被 `cancelActive` 取消后，工具层只拿到裸
`context.Canceled`（`internal/agent/result_contract.go` 由 message 反推 `AGENT_RUN_CANCELED`），
loop 退出路径回填罐头文案（`loop.go`），goal 自动续跑对 canceled ctx 直接跳过
（`chat_goal_auto_continue.go`）。实测 LLM 自然结束**不会**打断循环：step 3 流正常结束后
loop 继续执行了 7 个工具；`UPSTREAM_INVALID_RESPONSE` 由重试吸收、run 结束才汇总上报。

## 2. 修复（三层，均非"吞掉 Canceled"）

1. **在途 turn 绝不打断（根因修复）**：新增统一入口
   `refreshLocalRuntimeAfterSelection(session, selectionChanged, reason)`：
   - actor 有在途 run → 只登记延迟重建（`markPendingChatActorRebuild`），**整包刷新
     （provider 配置重载 + actor 驱逐 + 预热）推迟到回合入口**
     `reconcilePendingChatActorRebuild` 在 actor 空闲后一次性兑现；
   - actor 空闲且选择变化 → 立即整包刷新（保留旧行为）；
   - actor 空闲且同值重选 → 只重载 provider 配置，**不驱逐 actor**（不重建 agent）。
   为什么整包都推迟：`ReloadProviderConfigs` 会替换运行时 provider 注册表
   （旧模型被注销），在途 run 的后续 LLM 步骤会被打断——配置级刷新同样有"半途生效"风险。
2. **同值重选不重建**：`snapshotChatRuntimeSelection` 捕捉 provider/model/reasoning
   快照，各切换入口传入 `changed` 布尔；`chat_model_command.go`/`chat_model_picker.go`
   的"同目标重选只做能力窗口对账"不再付出一次 actor 重建。
3. **停因可归因**：`SessionActor.SetNextStopReason(reason)`（`actor.go`）在
   `cancelActive` 中消费，落到 `session_end.cancel_reason`（如 `runtime_refresh:model`），
   同时 `stopChatSessionActor`/延迟路径写结构化日志
   （`actor stopped: session=… reason=…`、`runtime refresh deferred: … turn=in_flight`）。
   命名同时统一：`profileRebuildPending` → `actorRebuildPending`，
   `reconcilePendingChatProfileRebuild` → `reconcilePendingChatActorRebuild`。

## 3. 验证

- `backend/internal/chat`：`TestSessionActorHostStopReasonLandsInSessionEnd`（新增）+
  既有 `TestSessionActorMarkUserInterruptBeforeStopKeepsUserInterruptSource` /
  `TestSessionActorParentContextCancelRecordsCancelCause` /
  `TestSessionActorInterruptConvergesStoppedStateAndTelemetry` 全通过；
  日志实测：`source=execution_context cause=actor_stop reason=runtime_refresh:model`。
- `backend/cmd/aicli/commands`：新增
  `TestRuntimeRefreshDoesNotInterruptInFlightTurn`（阻塞 provider 的在途 run + 刷新 →
  不驱逐/不取消，回合成功跑完，随后 reconcile 驱逐）、
  `TestRuntimeRefreshEvictsIdleActorWhenSelectionChanged`、
  `TestRuntimeRefreshKeepsActorForUnchangedSelection`；`TestProfileSwitch*` 全通过。
- 契约：模型/provider/reasoning/路由/目录切换一律"下一轮生效"（与既有文档一致），
  在途轮次继续使用旧 agent 跑完。CLI 的 `/routing` 写入撞上在途 turn 时输出
  `chatActorRebuildDeferredNote`（"检测到在途 turn：旧 actor 保留到本轮结束，
  刷新自下一轮入口生效"），不再静默返回"无警告"；
  对应测试 `TestChatRoutingRefreshAfterWriteReportsDeferralWhileTurnInFlight`。

## 4. 服务端平面补齐（同日第二轮）

runtime-server 平面（`aicli exec` / web 宿主）原先把"路由写入"做成**无条件停 actor**：
`internal/api/runtimeapi/session_routing_handlers.go:invalidateSessionRoutingActor` 直接
`hub.StopContext` → 同样的 `cancelActive(actor_stop)` 取消在途 run。已按 profile 切换的
既有范式补齐：

- **标记泛化**：新增 `session_actor_rebuild.go`，把 profile 专用标记
  （`profileSwitchPending` / `reconcilePendingProfileSwitch`）泛化为
  `actorRebuildPending[sessionID]=reason` + `markPendingActorRebuild` /
  `clearPendingActorRebuild` / `hasPendingActorRebuild` / `pendingActorRebuildReason` /
  `reconcilePendingActorRebuild`（命令入口 `session_runtime_handlers.go:912` 兑现）；
- **路由写入在途守卫**：`invalidateSessionRoutingActor` 返回 `(invalidated, deferred)`，
  在途 turn 只登记延迟重建（原因 `runtime_refresh:routing_write`）并保持
  `actor_invalidated=true`（写入自下一轮生效），响应追加
  `sessionRoutingDeferredActorWarning`（前端 `session-detail-routing-section` 已渲染
  `warnings`）；空闲 actor 仍立即驱逐；
- **停因可归因**：新增 `stopSessionActorForRebuild(hub, sessionID, reason)` ——
  `SetNextStopReason(reason)` + 有界停止 + 结构化日志（`actor stopped: session=… reason=…`），
  profile 切换（`profile_switch`）与路由写入共用；
- **测试**：`TestSessionRoutingPatchInFlightTurnIsNotInterrupted`（PATCH /routing 端到端：
  在途 actor 不被驱逐、回合正常结束、响应带 warning、标记落位，边界兑现后驱逐）、
  `TestSessionRoutingPatchEvictsIdleActor`（空闲立即驱逐，不报延迟）；
  `internal/api/runtimeapi` 全包 ok(131.7s)。

**仍保持无条件停止的是生命周期操作**（非运行时刷新，刻意保留）：
`session_runtime_support.go:633`（spawn 失败回滚，子会话正在被删除）、
`:2811`（close_agent / 会话关闭）、`handler.go:5099`（runtime store 迁移时 `oldHub.StopAll()`，
旧存储已关闭，无法延迟）。

## 5. 关联

- 取消来源埋点 `2797664a`、用户中断先打标 `9e9a681f`（A5 前序）
- `docs/analysis/a4-agent-session-cancel-partial-output-20260927.md`（部分产物）
- 本次涉及文件：`chat_actor_host.go`、`chat_profile_switch.go`、`chat_actor_executor.go`、
  `chat.go`、`chat_model_switch.go`、`chat_model_command.go`、`chat_model_picker.go`、
  `chat_reasoning_switch.go`、`chat_reasoning_command.go`、`chat_command_result.go`、
  `chat_routing_command.go`、`chat_add_dir.go`、`chat_session.go`、
  `internal/chat/actor.go`
