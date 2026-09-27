# aicli chat 提交-运行 epoch 撕裂（幽灵 Analyzing）加固方案

- 状态：实施中（P0 已落地，P1 诊断子集已落地；详见 §6 实施记录）
- 事故会话：`session_20260927153033_RWrApbhV`
  （进程 pid 1816490，`aicli --yolo --pprof --debug`，调试端点 `http://127.0.0.1:41167`）
- 关联文档：
  - `docs/plan/aicli-chat-unified-render-stall-analysis-and-hardening.md`（§5.6 run epoch 隔离）
  - `docs/plan/ui-event-bridge-drop-hardening.md`（§6.4 debug 落点 C）

## 1. 事故复盘

### 1.1 现象

- 该会话 TUI 自 15:31:01 起一直显示 `◦ Analyzing (… • esc to interrupt)`，15 分钟后仍未刷新。
- 实际没有任何 run 在运行：
  - runtime observe 平面：`llm.requests_total=0 / requests_in_flight=0`，会话 state=`idle`；
  - `GET /web/api/turn`：`busy=false`、`pending_inputs=0`、无 turn 记录；
  - `/debug/chat/status`：`Turns Running 0`、`Last Turn <none observed>`、`Queued Input 0 pending`。
- 用户输入（“Backend Go Tests - FAILED … 修复这些，测试错误”）只落在 Scene 与
  `runtime-events.jsonl` 的 `{"user_input": …}` 注入记录里，**没有入库**
  （`session_messages` 中只有 15:30:44 的 system/环境消息）。
- 进程无外部 LLM 连接、无 http/shell artifact；pprof goroutine dump（56 个 goroutine）
  全部处于正常 idle 状态，**没有 goroutine 卡在提交路径上**。

### 1.2 时间线

| 时间 | 事件 |
| --- | --- |
| 15:30:33 | 进程启动，新会话 `session_20260927153033_RWrApbhV` |
| 15:30:44.100 | system/环境消息入库（actor 构建期） |
| 15:30:44.107 | `actor stopped: session=…RWrApbhV reason=runtime_refresh:model`（`chat_actor_host.go:1554`；模型切到 `deepseek/deepseek-v4.1-flash`） |
| 15:31:01.525 | 用户输入回显（`RenderSubmittedUserInput` → `bridge.submitUserInput`）并写事件日志 |
| 15:31:01.536 | `aicli.chat.dynamic_status`（“◦ Analyzing”，`started_at=15:31:01`）被 epoch 围栏丢弃：`render suppressed … current_run_epoch=0 run_active=false` |
| 15:31:01.537+ | debug 日志终止；此后无 `BeginRun`、无 turn、无 LLM 请求、无工具调用 |

### 1.3 结论

不是死锁，而是**提交被中途丢失/中止 + UI 状态单向置位**造成的状态撕裂：
run epoch 从未开启（恒为 0），而 UI 已进入等待态（Analyzing）；epoch 围栏对后续事件
只会静默丢弃，UI 自身没有任何途径纠正这个状态，于是“假忙”可以永久停留。

## 2. 根因

- **R1 等待态先于 run 置位。**
  `sendMessage` 在提交入口就调用 `StartWaiting()`
  （`backend/cmd/aicli/commands/chat_send.go`，提交入口处），
  该调用会立刻把状态行切成 “Analyzing” 并发布 `aicli.chat.dynamic_status`；
  而 run 协议 `BeginRun()` 直到 `aicliActorChatExecutor.Execute`
  （`chat_actor_executor.go`，`ensureChatRuntimeEventBridge` 之后）才发生。
  两者之间隔着配置刷新、system prompt、用户消息落库、`ensureChatExecutor`、
  `chatActorForSession`、turn gate、actor 就绪等待等一串可失败/可停滞的步骤。
- **R2 `epoch==0` 语义双关且丢弃不可观测。**
  `isRunEpochCurrent`（`chat_runtime_events.go`）把 `epoch==0` 一律判为陈旧；
  围栏失效时只写一条 debug 日志（`chat_ui_actor.go`：
  “runtime event action targets closed run epoch”），既没有计数也没有状态位。
  同时 `user_submitted` / `dynamic_status` 本来就是被渲染数据面抑制的 mirror 事件
  （`chat_input_events.go`），所以撕裂既不会被纠正、也不会被报出。
- **R3 驱逐与提交之间没有互斥，也没有提交预算。**
  `runtime_refresh:model` 在 actor 空闲时直接驱逐
  （`chat_actor_host.go`：`refreshLocalRuntimeAfterSelection` / `stopChatSessionActor`），
  重建是惰性的（`chat_profile_switch.go`：`reconcilePendingChatActorRebuild` 在回合入口兑现）；
  提交路径对 `chatActorForSession / acquireActorTurnGate / waitForAICLIActorReady`
  没有预算，失败/停滞对用户不可见。

## 3. 方案

### P0-1 等待态在 BeginRun 之后才置位（本次实施）

- 落点：
  - `chat_send.go`：提交入口仅对**非进程内 actor executor** 调用 `StartWaiting()`
    （判据 `chatExecutorArmsWaitingAfterBeginRun` 用具体类型断言——runtime-server
    虽同为 RuntimeEvents 协议但不会自行置位，必须留在提交侧）；
  - `chat_actor_executor.go`：`bridge.BeginRun()` 成功后才调用
    `session.Interaction.StartWaiting()`。
- 效果：没有开启 run 的提交永远不会出现 “Analyzing”；
  预跑阶段失败/停滞不再留下幽灵等待态。
- 注意：`ClearWaiting/CompleteWaiting` 对未置位的等待态是 no-op
  （`chat_interaction.go` `finishWaiting` 有 `!c.waitingActive` 短路），
  因此 `sendMessage` 既有的 defer 仍可复用，无需双端协调。

### P0-2 提交失败必须可见（部分实施，后续增强）

- 现状：`ensureChatExecutor` / 预跑阶段错误会沿 `sendMessage` 返回，
  调用方（`chat.go`、`chat_backtrack_select.go`）已有 `RenderError` +
  `rememberChatTurnRecovery` 恢复提示路径。
- 后续：为 `Execute` 预跑阶段引入 `chatActorBeginRunBudget`（需可配置，
  避免冷启动 MCP/skills 初始化误伤），超时返回可诊断错误并保证
  “错误上屏 + 可重发”。

### P0-3 提交生命周期埋点与撕裂检测（本次实施诊断子集）

- 落点：
  - `chat_runtime_events.go`：新增 late-action 丢弃统计
    （总数 / closed-epoch 数 / 最后事件类型、原因、时间、epoch），
    新增 `RunEpoch()/RunActive()/LateRuntimeDropStats()` 只读访问器；
  - `chat_debug_turn_metrics.go`：`/debug/chat/status#turn` 增加
    `Run Epoch / Run Active / Waiting Armed / Late Action Drops / Last Late Action`，
    并在 `WaitingArmed && RunEpoch==0 && !RunActive` 时输出
    `Wedge Suspected: yes`。

### P1-1 显式 RunState（待办）

- 用 `Idle / Submitting / Running / Ending` 取代 “epoch 0 = 不可用”的双关；
  `Submitting` 窗口内的事件缓存或放行，只有**已结束 run 的迟到动作**才丢弃。
- 围栏丢弃进入指标（`/web/api/analysis/errors`）与 mesh 健康检查。

### P1-2 UI 真值化 + 看门狗（待办）

- 状态行每帧从运行真值推导（`RunActive` / actor busy / pending submit），
  不再依赖“曾经置位过”；
- `WaitingArmed && RunEpoch==0 && !RunActive` 持续超过预算时自动清态，
  提示“本轮未启动，请重发”，必要时自动重投一次（幂等键）。

### P2 驱逐/提交并发收敛（待办）

- session 级 `actorGeneration / refreshing` 标记：提交入口发现 generation
  变化或 refreshing 时，先在预算内同步重建 actor，再 `BeginRun`；
- refresh（写）与 submit（读）通过 submit gate 互斥，把
  `stopChatSessionActor` 的契约从“调用方确认无在途 run”升级为“钉住提交窗口”。

### P3 持久化与回归（部分实施）

- 用户消息提交即入库（或写 pending 记录），保证“界面看得到 ⇒ 重启后找得回”；
- 新增 `chat_submit_run_epoch_wedge_test.go` 回归（本次）；
- 后续补：evict(runtime_refresh:model)×submit 并发测试、
  e2e“无活动 run 不得出现 Analyzing 帧”断言。

## 4. 验收标准

1. 任一预跑失败路径下，不得出现 “WaitingArmed && RunEpoch==0 && 无 turn”
   的持续状态；
2. `/debug/chat/status#turn` 能一眼看出 RunEpoch / RunActive / WaitingArmed
   是否撕裂（`Wedge Suspected`）；
3. `go build ./...` 与相关包测试通过；
4. 既有行为不回退：非 actor（legacy/shared）executor 仍在提交入口置位等待态。

## 5. 风险与回滚

- 风险点：等待态置位时机后移，actor 预跑阶段（通常亚秒级；本次事故中是 actor
  被驱逐后的重建窗口）将不再显示 “Analyzing”，用户可能短暂看到无活动指示。
- 缓解：P0-2/P1-2 的可见性（错误上屏、看门狗）随后补齐；
  若非 actor executor 判定误伤，会回退为提交入口置位（本方案以 executor
  descriptor 作为唯一判据，可用单测锁定）。
- 回滚：改动集中在 4 个文件（`chat_send.go` / `chat_actor_executor.go` /
  `chat_runtime_events.go` / `chat_debug_turn_metrics.go`）+ 1 个新测试文件，
  可整块回退，不影响未提交的既有 WIP（F5 read-only roots）。

## 6. 实施记录

### 6.1 改动清单（2026-09-27）

| 文件 | 改动 |
| --- | --- |
| `backend/cmd/aicli/commands/chat_send.go` | 等待态判据 `chatExecutorArmsWaitingAfterBeginRun` / `chatSubmitArmsWaitingAtEntry`；actor 路径不再在提交入口置位 |
| `backend/cmd/aicli/commands/chat_actor_executor.go` | 前台 `Execute` 在 `bridge.BeginRun()` 之后 `StartWaiting()` |
| `backend/cmd/aicli/commands/chat_runtime_events.go` | late-action 丢弃统计（total / closed-epoch / last）+ `RunEpoch()` / `RunActive()` / `LateRuntimeDropStats()`；围栏原因收敛为共享常量 |
| `backend/cmd/aicli/commands/chat_ui_actor.go` | 围栏拒绝改用共享常量（统计可对账） |
| `backend/cmd/aicli/commands/chat_interaction.go` | 新增只读诊断访问器 `WaitingArmed()` |
| `backend/cmd/aicli/commands/chat_debug_turn_metrics.go` | `/debug/chat/status#turn` 增加 Run Epoch / Run Active / Waiting Armed / Late Action Drops / Last Late Action / Wedge Suspected |
| `backend/cmd/aicli/commands/chat_submit_run_epoch_wedge_test.go`（新增） | 5 个回归用例（分类判据、sendMessage 级幽灵等待态、executor 预跑失败、丢弃计数、撕裂指纹） |

### 6.2 验证

| 命令 | 结果 |
| --- | --- |
| `cd backend && go build ./...` | PASS |
| `go vet ./cmd/aicli/commands/` | PASS（无输出） |
| `go test ./cmd/aicli/commands/ -run '<5 个新用例>' -count=1 -v` | 5/5 PASS |
| `go test ./cmd/aicli/commands/ -run '<5 个新用例>' -race -count=1` | PASS |
| `go test ./cmd/aicli/commands/ -run 'TestSuccessfulSend\|TestDebug\|TestChatRuntimeEvents_NextRunEpoch' -count=1` | PASS（既有等待态/调试/epoch 语义无回退） |
| `go test ./cmd/aicli/commands/ -run 'TestActorExecutor\|Waiting' -count=1` | PASS |

备注：首次 `-race` 编译撞上并行 WIP（`internal/api/runtimeapi/session_runtime_support.go`
引用当时尚未落盘的 `worktree.ApplyReport.DeferredDeletions`），与本方案无关；
`worktree.go` 落盘后该包 `go build` 恢复，race 回归重跑通过。

### 6.3 后续（未完成项）

- P0-2：`chatActorBeginRunBudget`（预跑阶段预算，需可配置以免冷启动误伤）。
- P1-1：显式 `RunState` 取代 epoch 0 双关；丢弃指标接入 `/web/api/analysis/errors`。
- P1-2：状态行真值化 + 看门狗（等待态超时自动清态 / 提示重发）。
- P2：`actorGeneration/refreshing` 标记 + refresh/submit 互斥（submit gate）。
- P3：用户消息提交即入库；evict(`runtime_refresh:model`)×submit 并发回归与
  e2e「无活动 run 不得出现 Analyzing 帧」断言。
