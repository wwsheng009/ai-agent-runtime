# 工作区多会话并发运行方案（multi-session concurrent runtime / 多路 SSE live 通道）

> 状态：**已实施（2026-09-17，Batch 0–4 落地）**；§1–§2 为现状取证（可单独作为盘点依据），§4 起为设计，§0.1 为实施进度与开关口径。
> 日期：2026-09-17（本地 +08:00）
> 适用版本：当前仓库（后端 Go module：`backend`；前端 `frontend`，dev 端口 5193，`/api` 代理到 runtime-server）
> 关联文档：
> - `docs/plan/sse-live-event-channel-optimization-plan.md`（当前 SSE live 通道权威方案；Batch 1–4 已落地/在途，本方案**不得回退**其契约）
> - `docs/plan/workspace-chat-realtime-streaming.md`（方案 A/B/C：双通道拓扑、`deltaCoordinator` claim、`renderLiveDeltas` 闸门）
> - `docs/plan/frontend-deepseek-harness-optimization-plan.md`（§8「多标签/多窗口工作台不采纳」「多标签状态广播会引入双写流与重复 SSE」——本方案 §6.1 属**显式复审**，边界声明见 §0 第 5 条）
> - `docs/plan/ui-event-bridge-drop-hardening.md`（事件桥分级/合并/丢弃加固）
> - `docs/development-guidelines.md`（§10 文档规范、§11 前端工程门禁）

---

## 0. 结论摘要

### 0.1 实施进度（2026-09-17）

| 批次 | 状态 | 实际落点 |
|---|---|---|
| Batch 0 回归基线 | ✅ | 四条契约的回归继续由既有用例承担（tail-first 建连游标 / claim 去重 / 轨迹恢复 / 历史同步），vitest 全绿 |
| Batch 1 去单例 | ✅ | `lib/thread-state/deltas.ts`（多回合键控）、`lib/trajectory/store-pool.ts` + `hooks/workspace/use-trajectory-store-pool.ts`、`hooks/workspace/agent-chat-turn/session-turn-registry.ts`、`hooks/workspace/use-pending-interactions.ts`（按条目路由） |
| Batch 2 注册表与后台订阅 | ✅ | `lib/session-runtime/{types,flags,entry,registry,policy}.ts`、`hooks/workspace/use-session-runtime-registry.ts`、`use-session-stream-supervisor.ts`；`use-session-runtime-stream.ts` 收敛为注册表内的单会话引擎 |
| Batch 3 多会话交互 | ✅ | 去打断式确认、按会话提交/停止（`handleStopRuntimeSession`）、侧栏活动投影（`lib/session-runtime/activity.ts`、`workspace-sidebar/session-attention.ts`）、通知（`lib/session-runtime/notices.ts` + `session-runtime-notices.tsx`） |
| Batch 4 资源治理与观测 | ✅ | 策略层 `lib/session-runtime/policy.ts`（10min 近窗 / 预算内 live / 超预算 poll / 不在集合即释放）、注册表预算与可见性降采样、诊断投影 `lib/session-runtime/diagnostics.ts` + 网络详情面板「会话订阅」区块 |
| 真实链路实测（H1–H5） | ✅ | `frontend/e2e/zz-multi-session-live.manual.ts`：真实 dev server + runtime-server + provider（opencode.ai / `deepseek-v4.1-flash` / `effort=max`）。A 流式（`active_turn.detached=true`）→ 切新线程后侧栏 A 行保持「Running」且后台订阅存活 → B 并行提交并拿到回复 → 切回 A 正文 2165 字符不缩水、`active_turn` 收敛为 `null`；无 console error、无硬失败请求（10 次 abort 全部是客户端主动取消的长连接）。证据：`frontend/.artifacts/live-multi-session/1789613541707.json` |

- **开关与回滚**：`MULTI_SESSION_REGISTRY_ENABLED = true`（`lib/session-runtime/flags.ts`）——真实链路实测通过后开启，此时才挂载注册表、后台订阅与侧栏多会话投影。回滚 = 改回 `false`（§6.3，不引入环境变量），行为回到改造前。
- **预算与降级**：`DEFAULT_FOREGROUND_LIVE_BUDGET = 1` / `DEFAULT_BACKGROUND_LIVE_BUDGET = 2`；后台 live 在回合终态后 10s 宽限（`LIVE_GRACE_MS`）降级 `poll`；`document.hidden` 时后台 live → `poll`，恢复可见按期望模式回升（受预算约束）。
- **观测对账**：网络详情面板新增「会话订阅」块（`live` / `poll` / 订阅数 / 本会话强度 / 页面可见性 / live 预算），仅在注册表存在条目时渲染；`live` 计数即前端实际持有长连接数，用于与后端 `active_connections` 指标对账（Batch 4 验收第 3 条）。

1. **服务端已经支持多会话并发，且"切走继续跑"后端零改动即可成立**：每个会话一个 `SessionActor`（独立 goroutine + 独立状态，无全局回合锁），事件序号/环形缓冲按 `sessionID` 分键，`/runtime/stream` 按路径会话独立成流，`interrupt` / 审批 / 回答都是 per-session 命令端点；`resume_on_disconnect` 已在 web 直连路径逐请求开启（`use-workspace-agent-chat-turn.ts:184`）。后端缺的只有一件事：**没有跨会话的活动聚合视图**（`GET /sessions` 不携带运行态，见 §2 P1-1）。
2. **前端是"单会话渲染"架构**：`WorkspacePage` 全页只有一个 `selectedThread` 主语；live 链路上存在四个"单值/单例"：单条 `/runtime/stream` 连接、单实例 `deltaCoordinator`、单实例 `trajectoryStore`、单份回合状态（`isResponding` / `activeTurnId` / `AbortController`）。其中 `isResponding` 同时充当**全局提交单飞闸门**（`use-workspace-agent-chat-turn.ts:144-148`）——A 在跑时 B 无法提交，这是用户视角最直接的阻塞。
3. **`RuntimeDeltaCoordinator` 是多会话并发的硬伤**：`beginTurn()` 会 `seenKeys.clear()` 并覆盖单个 `activeTurnId`（`lib/thread-state/deltas.ts:26-34`）。跨会话并发下，B 的 `beginTurn` 会清掉 A 的已消费账目并抢走"当前回合"身份——A 的增量要么被判成"非活动回合"整帧丢弃，要么因去重账目被清空而重复渲染。
4. **方案主线 = 把"每页单实例"改造成"按会话分键的注册表 + 分级订阅"**，不改传输层、不改事件契约、不新增第二条写通道：
   - 新增 **Session Runtime Registry**（页面级、跨会话存活的流与状态注册表，`useSyncExternalStore` 对外暴露）；
   - `deltaCoordinator` / `trajectoryStore` / 回合状态全部**按会话（或按回合）分键**；
   - 后台会话默认"**累积状态、不渲染消息**"，切回时按已保存的 `lastSeq` 续传 + 轨迹窗口重建补齐（复用既有 tail-first 与 seq 续传契约）；
   - 侧栏活动从"只投影当前会话"扩展为"消费全局注册表"（`SidebarSessionActivity` 的 `Record<sessionId, …>` 形状已为此预留，见 `session-row-status.ts:132-142`）。
5. **与既有"不采纳多标签"决策的关系（必须写清的边界）**：本方案不做多标签页、不做多窗口状态广播、不引入 `BroadcastChannel` 双写；它解决的是**单窗口内多会话并行运行与切换**。同一时刻的 SSE 连接数由 §4.6 的连接预算与分级订阅收敛（默认 1 条前台 + ≤2 条后台），不会退化成"打开多少会话就挂多少条流"。
6. **分批落地**：Batch 1 去单例（纯重构、行为不变）→ Batch 2 注册表与后台订阅 → Batch 3 多会话 UI（侧栏/通知/停止/审批路由）→ Batch 4 资源治理与降级。每批默认"缺省旧行为"，可独立回滚。

---

## 1. 现状盘点

### 1.1 服务端：多会话并发能力清单（结论：已具备，无需改造）

| 能力 | 现状 | 证据（`backend/` 相对路径） |
|---|---|---|
| 每会话独立执行单元 | 每会话一个 `SessionActor`（独立命令循环 + 独立 `RuntimeState`）；hub 互斥只覆盖 map 读写，**无全局回合锁** | `internal/chat/hub.go:28,40,44,77,129` |
| 活跃会话上限 | 生产用 `NewBoundedSessionHub`，默认 `MaxActors=32` / `IdleTTL=15m` / `Sweep=1m`；驱逐只针对闲置 actor，在跑会话受保护 | `internal/chat/hub.go:19-25,204-217`；装配 `internal/api/skills/session_runtime_support.go:3492,3507` |
| 同会话串行 | `ensureReady` 在 `SessionRunning / WaitingApproval / WaitingInput / Rewinding` 时返回 `ErrSessionBusy` | `internal/chat/actor.go:1570-1581`；应用点 `:830,:899,:1410` |
| 每会话独立事件序号 | `s.seq[event.SessionID]++` + 按会话分键的环形缓冲；`after=<seq>` 即该会话游标 | `internal/chat/session_runtime_store.go:253,770-777` |
| 每会话独立 SSE | `GET /api/runtime/sessions/{id}/runtime/stream`（路径变量取会话，无全局流）；参数 `after`/`poll_ms`/`tail`/`keepalive_ms`/`retry_ms`/`flush_ms`/`coalesce`/`latest_wins`/`live` | `internal/api/skills/handler.go:803`；`internal/api/skills/session_runtime_stream.go:40-117` |
| 连接数 | 无 per-client / per-session 去重与限流；仅 `active_connections` 计数指标 | `internal/api/skills/session_runtime_stream_metrics.go:27,43,50-51,127` |
| 服务端写超时 | 无 `WriteTimeout`/`IdleTimeout`，仅 `ReadHeaderTimeout=10s`；长驻 SSE 靠 15s keepalive 存活 | `cmd/runtime-server/main.go:421-424`；keepalive 常量见 `session_runtime_stream.go` |
| 断线续传 | 重连带 `after=<last seq>`，服务端先补齐再转实时订阅；**仅在环形窗口内成立**（约 2048 条 / 8MB） | `session_runtime_stream.go` 重放/游标段；`session_runtime_store.go` 保留策略 |
| 切走继续跑 | `POST /api/agent/chat` 的 `resume_on_disconnect=true` → `context.WithoutCancel(ctx)`，客户端断开不取消 run | `internal/api/skills/handler.go:1904-1909`；集成测试 `agent_chat_resume_disconnect_test.go:308,362,441` |
| 在途回合可发现 | `/runtime` 快照返回 `active_turn`（含 `detached` / `cancel_source` / `source`） | `internal/api/skills/session_active_turn.go:35-38,64-77` |
| 停止（含 detached 回合） | `POST .../runtime/commands` `{"type":"interrupt","turn_id"?}`；先取消进程内在途回合，再回退 durable actor；响应带 `channel` / `reason`，重复 stop 幂等（`already_cancelled`），回合不匹配回 409 | `internal/api/skills/session_runtime_handlers.go:845-846,876-987,1030-1036` |
| 审批 / 回答 / 续跑命令 | 同一 commands 端点承载 `approve_tool` / `answer_question` / `submit_prompt` / `continue` / `rewind_to` | `session_runtime_handlers.go:876-992` |
| 会话列表运行态 | **无**：`GET /sessions` 不返回 running / active_turn | `internal/api/skills/handler.go:2690-2694`（`ListSessions`） |

> 结论：**"A 会话在服务器上继续运行"当前版本就已成立**（前端切换会话不 abort `POST /api/agent/chat`，服务端 `resume_on_disconnect` 保证回合与连接解耦）。缺口全部在**前端能否同时观察/驱动多个会话**，以及**用户能否在 B 上独立提交**。

### 1.2 前端：单会话渲染架构

```text
App.tsx 路由：/workspace/sessions/:sessionId、/workspace/chats/:threadId
  └─ WorkspacePage（单个 lazy 组件；切换会话 = 同组件内参数变化，页面不卸载）
       ├─ useWorkspaceThreadSelection → selectedThread（唯一主语）
       ├─ useSessionRuntimeState(selectedThread?.sessionId)      // 快照只拉当前会话
       ├─ useTrajectoryRecovery({ sessionId: selectedThread?.sessionId })  // 单实例 store
       ├─ usePendingInteractions({ sessionId: selectedThread?.sessionId }) // 归约全局累积、呈现按会话过滤
       ├─ useWorkspaceLive({
       │     localResponding: isResponding,        // 单值
       │     localTurnId: activeTurnId,            // 单值
       │     sessionId: selectedThread?.sessionId,
       │     trajectoryReady, trajectoryStore,     // 单实例
       │     deltaCoordinator: runtimeDeltaCoordinator, // 单实例（useMemo []）
       │   }) → useSessionRuntimeStream(...)       // 单条 /runtime/stream
       └─ useWorkspaceAgentChatTurn(...) → POST /api/agent/chat（单 AbortController / 单回合状态）
```

关键证据：

- 页面级单例：`frontend/src/pages/workspace-page.tsx:40-43`（`createRuntimeDeltaCoordinator()` 以 `useMemo(…, [])` 创建，跨会话切换复用同一实例）。
- live 接线只认当前会话：`workspace-page.tsx:302-319`（`sessionId: selectedThread?.sessionId`、`localTurnId: activeTurnId`、`localResponding: isResponding`）。
- 侧栏活动只投影当前会话：`workspace-page.tsx:322-334` → `buildSidebarSessionActivity({ sessionId: selectedThread?.sessionId, … })`；函数体确认只返回**单个键值对**（`session-row-status.ts:137-163`）。
- 流连接是"每页一条"：`use-session-runtime-stream.ts:503-513` 的订阅 effect 依赖 `[retryNonce, sessionId, sessionKey, hasThreadId, enabled]` —— 会话切换 = 旧连接 abort + 新连接建立；游标虽然按会话存在 ref 记录（`:113-114` `runtimeEventsRef` / `runtimeSeqRef` 均为 `Record<sessionId, …>`），但**同一时刻只有一条活动连接，且只有当前会话收到事件**。
- 提交单飞闸门：`use-workspace-agent-chat-turn.ts:144-148` `if (!prompt || !selectedThread || isResponding) return;`；`isResponding` / `activeTurnId` 为 `useState` 单值（`:95-97`），回合结束统一 `setIsResponding(false)`（`:474-476`）。
- 切换确认框：`use-workspace-thread-selection.ts:61-75` 在"当前会话仍在生成回复"时弹确认；同文件 `:77-80` 的注释已承认"切到别的会话后 turn 仍在后台继续（isResponding 依旧为 true）"——即**切换本来就不打断**，确认框只是提示，语义与多会话目标冲突。

### 1.3 阻塞点：四个"单值/单例"

| # | 单实例 | 位置 | 多会话下会发生什么 |
|---|---|---|---|
| B1 | `deltaCoordinator`（单 `activeTurnId` + 单 `seenKeys`，`beginTurn` 清空账目） | `lib/thread-state/deltas.ts:17-34`（`MAX_RUNTIME_DELTA_KEYS=512`） | B 的 `beginTurn` 清掉 A 的 claim 账目并抢走活动回合身份：A 的增量帧 `isTurnActive()` 判定失败被整帧过滤（表现为 A 的流"看着在跑但不涨字"），或去重账目被清空导致同一段增量重复渲染 |
| B2 | `trajectoryStore`（单实例，切换即 `reset({hard:true})`） | `workspace-page.tsx:191,206`（`onResetTrajectory`）；`use-trajectory-snapshot.ts`（`createTrajectoryStore`） | 切换到 B 会**硬重置** A 的轨迹窗口与尾窗游标；切回 A 只能全量重放，tail-first 优化失效；轨迹面板无法体现多个会话 |
| B3 | 回合状态单值（`isResponding` / `activeTurnId` / `phase` / `activeRequestControllerRef`） | `use-workspace-agent-chat-turn.ts:95-97,144-148,474-476,493` | ① 全局单飞：A 在跑 → B 提交被 `isResponding` 直接 return；② B 提交后若 `beginTurn` 被 B 抢走，A 的停止/终态收口会错乱（`finalizeTurn` 的 `activeTurnIdRef` 比对失败）；③ composer 的停止态、`streamStalled` 提示、`useSessionHistorySync` 的 `shouldSyncSessionHistory(!isResponding)` 全都把 A 的忙碌算到 B 头上 |
| B4 | 单条 `/runtime/stream` 连接 | `use-session-runtime-stream.ts:503-513` | 后台会话完全没有事件来源：侧栏无 running 标记、A 的审批/提问不会浮出、完成无提示；`usePendingInteractions` 的"全局累积"因此只积累了当前会话的事件 |

> 补充（非阻塞但需一并处理）：
> - `usePendingInteractions` 的**决定路由用的是当前会话 id**（`use-pending-interactions.ts:106-145,147-187` 的 `resolveApproval` / `answerQuestion` 都以 hook 入参 `sessionId` 发起请求）——后台会话的卡片即使浮出，点"允许/回答"也会打到错误的会话上。多会话必须改为**按条目自身 `sessionId` 路由**（条目类型已带 `sessionId` 字段，见 `lib/pending-interaction/snapshot.ts:44-54`）。
> - `useSessionRuntimeState` 是"当前会话单次拉取"（`use-session-runtime-state.ts:58-90`，按会话键存但只服务一个调用点）；后台会话的 `active_turn` / 待交互重建需要轮询版。
> - `useResumedSessionTurn` 的续传心跳 `RESUMED_TURN_HEARTBEAT_MS = 5_000` 同样只服务当前会话。

### 1.4 已经具备的并发基础（可复用资产，不要另起炉灶）

| 资产 | 现状 | 复用方式 |
|---|---|---|
| `resume_on_disconnect` + `/runtime.active_turn` | 回合与请求解耦；刷新后新页面可重挂回合身份 | 后台会话恢复订阅时同一套身份挂载逻辑 |
| per-session `seq` + `after` 续传 | 每条流独立游标；`runtimeEventsRef`/`runtimeSeqRef` 已按会话分键存储 | 注册表为每会话保存 `lastSeq`，断线/切回按游标续传，不重复 dump |
| tail-first 建连闸门 + `getReplayCursor` | `use-session-runtime-stream.ts:65-79`；`use-workspace-live.ts:198-200` | 后台会话只订阅"窗口之后"的新事件；切回时由轨迹窗口补齐历史 |
| `SidebarSessionActivity` 数据结构 | `session-row-status.ts:19-30` 定义 `Record<sessionId, activity>`；`:132-136` 注释明确"来源可以是本地流派生或未来的全局事件源" | 侧栏零改动即可消费全局注册表投影 |
| 待交互注册表"全局累积 + 按会话过滤" | `use-pending-interactions.ts:26-27` 注释；`selectPendingInteraction(state, sessionId)` 按会话呈现 | 注册表把所有订阅会话的事件投给它，即得到全局待办 |
| per-session 命令端点 | `interrupt` / `approve_tool` / `answer_question` 都是 `POST /sessions/{id}/runtime/commands` | 后台会话的停止/审批无需打开流即可投递（HTTP 一次性请求） |
| 线程写入按会话/线程身份定位 | 回合收尾闭包捕获发起时的 `threadSnapshot` 与 `updateCurrentThread`（`use-workspace-agent-chat-turn.ts:150-158,368-416`），`setThreads` 按 thread id 映射 | 后台回合并发写 `threads` 数组时天然按线程隔离（不自相冲突）；但**同一线程仍是单写者**，需保持 |

### 1.5 用户体验缺口（问题视角）

1. A 在跑时切到 B：B 的 composer 被 `isResponding` 判为"生成中"，显示停止按钮且无法提交（假象：B 在跑）。
2. 切回 A：`useTrajectoryRecovery` 全量重放 + 单条流重建，长会话首屏成本翻倍；A 的增量在切换窗口可能被 `deltaCoordinator` 的清空逻辑丢弃或重复。
3. 侧栏：A 没有任何"运行中/等你审批"标记（`buildSidebarSessionActivity` 只产出当前会话条目）。
4. A 跑完 / A 需要审批：没有任何跨会话提示（完成通知只在当前会话终态触发，见 `agent-chat-turn/finalize-turn.ts` 的通知路径）。
5. 切换确认框把"后台继续跑"包装成"打断"，与真实语义不符（真实语义是"不打断"）。

---

## 2. 问题清单（按优先级）

| 编号 | 级别 | 问题 | 证据 | 后果 |
|---|---|---|---|---|
| P0-1 | P0 | 提交单飞闸门以全局 `isResponding` 判定 | `use-workspace-agent-chat-turn.ts:144-148` | A 运行中无法在 B 提交；多会话并行不可用 |
| P0-2 | P0 | `deltaCoordinator` 单回合账目 | `lib/thread-state/deltas.ts:26-34` | 跨会话并发时增量丢帧或重复渲染（数据正确性） |
| P0-3 | P0 | 单条 `/runtime/stream`，仅当前会话有事件源 | `use-session-runtime-stream.ts:503-513`；`workspace-page.tsx:302-319` | 后台会话状态/审批/完成不可知 |
| P0-4 | P0 | `trajectoryStore` 单实例 + 切换硬重置 | `workspace-page.tsx:191,206` | 后台会话轨迹断档、切回全量重放 |
| P1-1 | P1 | `/sessions` 无运行态，前端无跨会话活动聚合 | `handler.go:2690-2694`；`workspace-page.tsx:322-334` | 侧栏无法显示"哪个会话在跑/在等我" |
| P1-2 | P1 | 待交互决定按当前会话路由 | `use-pending-interactions.ts:106-187` | 后台会话审批/回答会打错会话 |
| P1-3 | P1 | 切换确认框语义与"后台继续跑"冲突 | `use-workspace-thread-selection.ts:61-93` | 误导用户以为切换会中断 |
| P2-1 | P2 | 无连接预算/后台降采样策略 | 服务端无连接上限（`session_runtime_stream_metrics.go` 仅计数） | 会话多开时前端 SSE 连接数线性增长 |
| P2-2 | P2 | 完成/需要交互无跨会话通知 | `agent-chat-turn/finalize-turn.ts` 通知只覆盖本地终态 | 用户不知道后台会话何时需要自己 |
| P2-3 | P2 | `useSessionHistorySync` / `streamStalled` 等以全局 `isResponding` 为条件 | `workspace-page.tsx:221`；`types.ts:113-114` | 后台会话的忙碌状态污染当前会话的历史同步与提示 |

---

## 3. 目标、非目标与不变式

### 3.1 目标（可验收口径）

| 编号 | 目标 | 验收口径（用户可观察） |
|---|---|---|
| G1 | **并行提交**：同会话内单飞、跨会话并行 | A 运行中切到 B，B 的 composer 可输入、可发送；A 的流继续增长；两边互不等待（后端 `ErrSessionBusy` 只约束同会话） |
| G2 | **多路 live 通道**：后台会话有独立事件源 | 侧栏 A 行显示"运行中"；A 的审批/提问在侧栏出现等待标记；A 完成时当前页收到通知 |
| G3 | **切换无损**：切换不打断、不丢帧、不重放全量 | 切走再切回 A：seq 连续（无重复、无空洞）、轨迹窗口不重置、首屏不重新 dump 全量事件 |
| G4 | **全局可观测**：单窗口内可见所有会话的实时状态 | 侧栏每行状态来自注册表投影；顶栏连接状态、流尾提示只对应当前会话 |
| G5 | **资源受控**：会话数增长时连接/内存有界 | 后台 SSE 连接数 ≤ 预算；超出走轮询降级；关页/长时间空闲自动释放 |

### 3.2 非目标（明确不做）

1. 多标签页 / 多窗口工作台、跨标签状态广播（维持 `frontend-deepseek-harness-optimization-plan.md:721,736` 的结论；本方案不触碰）。
2. 同一会话内并行多个回合（服务端 `ErrSessionBusy` 是权威约束，前端不得绕过）。
3. 服务端事件契约与帧格式变更（信封 `skill_runtime.sse.v1` 不升版；§4.9 的可选聚合接口只读、不改既有流）。
4. 跨设备/跨用户会话语义变化。

### 3.3 必须保持的不变式（违反即回退）

| 编号 | 不变式 | 来源 |
|---|---|---|
| IN1 | **tail-first 建连闸门**：任何实时流建连前，必须先完成该会话的尾窗回放；新连接 `after = max(本地已消费 seq, 轨迹窗口 lastEventSeq)`，禁止无意义 `after=0` 全量 dump | `use-session-runtime-stream.ts:65-79`；`use-workspace-live.ts:198-200`；回归基线 `use-session-runtime-stream.tail-first.test.tsx` |
| IN2 | **游标语义**：runtime stream 游标 = EventStore 行 `seq`；`persist` 失败帧不得冒充持久 seq（宁缺勿假） | `sse-live-event-channel-optimization-plan.md` §1.3、Batch 1（`b24f4bfc`） |
| IN3 | **双通道共享 claim**：`/api/agent/chat` 与 `/runtime/stream` 的同一段增量只能被渲染一次；新增任何 live 写入路径必须接入同一 claim 体系 | `sse-live-event-channel-optimization-plan.md:30-37`；`deltas.ts:11-22` |
| IN4 | **live 渲染闸门**：只有"存在在途回合身份"时才把打字机增量应用到 thread；历史回放/reload 不渲染增量 | `use-workspace-live.ts:230-240`（方案B）；`renderLiveDeltas` |
| IN5 | **单一写通道**：会话事件写入 thread 只经事件归约（`applyRuntimeEventToThread` / `applyRuntimeDeltaToThread`）与历史快照同步（`applySessionHistoryToThread`）；不新增旁路写 | `lib/thread-state/events.ts`、`events-live.ts`；`workspace-thread-state` 导出面 |
| IN6 | **会话身份纪律**：所有网络请求与状态写入必须显式携带 `sessionId`（或 threadKey），禁止"当前会话"隐式兜底 | 本方案新增约束（对 P0-2 的根治） |
| IN7 | **同会话串行**：同一会话同一时刻只有一个在途回合；停止/审批/回答都按回合或请求 id 幂等收敛 | `backend/internal/chat/actor.go:1570-1581`；`session_runtime_handlers.go:845-987` |

---

## 4. 方案设计

### 4.1 总体架构

```text
┌──────────────────────────── WorkspacePage（单页，不随会话切换卸载）────────────────────────────┐
│                                                                                              │
│  SessionRuntimeRegistry（页面级单例，跨会话存活；useSyncExternalStore 暴露）                    │
│   ├─ entries: Map<sessionId, SessionRuntimeEntry>                                            │
│   ├─ 每会话一条订阅（宏观状态机：live → poll → idle；连接预算 + 可见性驱动）                    │
│   ├─ 每会话独立：seq 游标 / 重连退避 / 连接状态 / 在途回合 / 待交互计数 / 子代理计数            │
│   └─ 事件出口（唯一写入口的扇出点）                                                            │
│        ├─ 可见会话 → applyRuntimeEventToThread / applyRuntimeDeltaToThread（现有归约，写 threads）│
│        ├─ 所有会话 → applyPendingInteractionEvent（全局累积、按会话过滤）                       │
│        ├─ 所有会话 → 侧栏活动投影（Record<sessionId, SidebarSessionActivity>）                 │
│        └─ 所有会话 → 通知（完成 / 需要交互）                                                   │
│                                                                                              │
│  SessionTurnRegistry（回合状态按 threadKey 分键）                                              │
│   ├─ isResponding / activeTurnId / phase / AbortController（每会话一份）                       │
│   └─ POST /api/agent/chat（本地直连回合）；与注册表的 active_turn 身份统一                      │
│                                                                                              │
│  TrajectoryStore 池（Map<sessionId, TrajectoryStore>，LRU + dispose）                          │
│                                                                                              │
│  选中会话 = 唯一"渲染主语"；后台会话 = "状态主语"（有事件、有计数、无消息渲染）                    │
└──────────────────────────────────────────────────────────────────────────────────────────────┘
                    │  SSE（每会话独立连接）                        │  HTTP（per-session 命令）
                    ▼                                              ▼
   GET /sessions/{id}/runtime/stream?after=&live=1        POST /sessions/{id}/runtime/commands
   GET /sessions/{id}/runtime（轮询降级 / 快照）           （interrupt / approve_tool / answer_question）
```

设计要点：

1. **注册表是"订阅与渲染解耦"的边界**：连接存活与页面选中状态无关；渲染仍然是"单会话"（只有选中会话写 `threads`），因此不需要重写既有归约与渲染链路。
2. **后台会话不做消息渲染**（§4.3 D1）：后台只维护状态投影（运行态、待交互、子代理计数、最后事件时间）。切回时用"注册表 `lastSeq` 续传 + 轨迹窗口 + 历史同步"补齐，全部复用已有路径。
3. **新增任何写入路径都必须过 claim**（IN3）：注册表内部对每条事件先做 claim 判定再分发。

### 4.2 Session Runtime Registry（对应 P0-3，核心新增模块）

**建议落点**（命名可按实现调整，职责必须单一）：

| 文件 | 职责 |
|---|---|
| `frontend/src/lib/session-runtime/types.ts` | `SessionRuntimeEntry` / `SubscriptionMode` / 事件出口类型（纯类型，无 React） |
| `frontend/src/lib/session-runtime/entry.ts` | 单会话订阅状态机：建连/续传/退避/降级/释放（纯逻辑，可单测，复用 `streamSessionRuntime` + `consumeSseStream`） |
| `frontend/src/lib/session-runtime/registry.ts` | `createSessionRuntimeRegistry()`：entries 管理、预算与调度策略、订阅者通知（错误隔离 + 浅比较） |
| `frontend/src/hooks/workspace/use-session-runtime-registry.ts` | React 绑定：`useSyncExternalStore` + 选择器；`useSessionRuntimeEntry(sessionId)` |
| `frontend/src/hooks/workspace/use-session-stream-supervisor.ts` | 策略接线：把"当前会话/后台活跃/可见性/页面隐藏"翻译成注册表调用（保留在页面层，便于测试与回滚） |

**数据结构（草案）**：

```ts
export type SubscriptionMode =
  | "live"   // 常驻 SSE（/runtime/stream?live=1），前台会话或后台活跃回合
  | "poll"   // 低频快照轮询（GET /runtime），后台非活跃/超预算会话
  | "idle";  // 不订阅（归档/关闭/长时间空闲）

export type SessionRuntimeEntrySnapshot = {
  sessionId: string;
  mode: SubscriptionMode;
  status: ConnectionStatus;          // connecting / live / reconnecting / offline
  lastSeq: number;                   // 已消费的最大 seq（续传游标，跨切换保持）
  activeTurn: RuntimeSessionActiveTurn | null;
  detached: boolean;                 // 后台续传中（服务端回合与本地请求解耦）
  pending: { approvals: number; questions: number; planPending: boolean };
  runningAgents: number;
  lastEventAt: number | null;
  lastError: string | null;
};
```

**订阅策略（分级 + 预算）**：

| 会话类别 | 默认模式 | 触发/退出 |
|---|---|---|
| 当前选中会话 | `live`（`live=1`） | 始终；路由切换不释放（见 §4.5） |
| 后台"活跃回合"会话 | `live` | 本地提交回合 / `/runtime` 快照 `active_turn != null` / 最近 60s 内有事件；回合终态 + 10s 宽限后降级为 `poll` |
| 后台"最近活动"会话 | `poll`（3s → 30s 指数退避） | 会话 `updated_at` 在 10min 内且未被释放；发现 `active_turn` 或待交互变化 → 升级 `live` |
| 归档 / 关闭 / 长时间空闲 | `idle` | 列表状态驱动；被再次选中/更新时重新评估 |

**预算**：`live` 连接总数默认 `1（前台）+ 2（后台）= 3`（可配置）。超预算的后台会话一律 `poll`，并在侧栏提供显式"跟随实时"入口（`ensure(id, { promote: true })`，此时按最近活动 LRU 顶掉一条后台 `live`）。

**对外接口（草案）**：

```ts
type Registry = {
  ensure(sessionId: string, reason: "selected" | "active-turn" | "recent" | "explicit"): void;
  release(sessionId: string, reason: "deselected" | "idle" | "evicted"): void;
  retry(sessionId: string): void;                     // 复用既有重试语义（harness 计划 P1-8）
  subscribeEntry(sessionId: string, cb: () => void): () => void;   // 订阅者错误隔离 + 浅比较
  snapshot(sessionId: string): SessionRuntimeEntrySnapshot | undefined;
  entriesSnapshot(): ReadonlyMap<string, SessionRuntimeEntrySnapshot>;
  onEvent(handler: (sessionId: string, event: SessionRuntimeEvent) => void): () => void;
  dispose(): void;
};
```

**为什么用单例注册表而不是"每会话一个 hook"**：hook 的生命周期与组件挂载绑定，而多会话要求"会话切换不重建连接、后台会话不因不可见而断流"。注册表作为模块级单例（页面卸载时 `dispose()`）与既有 `live-diagnostics/store.ts` 的全局 store 形态一致，且满足 `frontend-deepseek-harness-optimization-plan.md:702` 对"状态层订阅者错误隔离 + 选择器浅比较"的既有要求。

### 4.3 状态隔离改造（对应 P0-2 / P0-4 / P2-3 与 B1–B4）

**D1（关键取舍）：后台会话"累积状态、不渲染消息"。**

| 方案 | 做法 | 评价 |
|---|---|---|
| D1-A 全量渲染（每会话独立 thread store，事件照常归约） | 注册表把事件写给各会话自己的 thread 投影 | 需要重写所有写路径的"当前线程"前提（`updateCurrentThread`、`setThreads` 映射、finalize 收口），且后台渲染无用户收益；回归面最大 |
| **D1-B（推荐）后台只做轻量归约** | 后台会话：更新 entry（状态/计数/最后事件时间）+ 喂待交互注册表；**不写 `threads`**。切回时：`lastSeq` 续传 + 轨迹窗口 + 历史同步补齐 | 写路径完全复用；切回成本可控；符合 IN5"单一写通道"；缺点：后台会话的消息流在切回前不可见（符合预期） |

D1-B 下"切回补齐"的三步（全部是既有能力）：

1. 注册表把 `lastSeq` 交给可见会话的订阅（`useSessionRuntimeStream` 的建连游标 `getReplayCursor` 取 `max(轨迹窗口 lastEventSeq, entry.lastSeq)`）→ 不重复 dump；
2. `useTrajectoryRecovery` 用**该会话自己的** `TrajectoryStore` 回放尾窗（见下）；
3. `useSessionHistorySync` 在"该会话非响应中"时拉取权威历史（含后台完成的最终消息）。

**改造清单（按文件）**：

| 目标 | 现状 | 改造 | 测试锚点 |
|---|---|---|---|
| `deltaCoordinator` | 单 `activeTurnId` + 单 `seenKeys`，`beginTurn` 清空（`deltas.ts:26-34`） | 改为**多回合键控**：`Map<turnId, Set<key>>` + `activeTurns: Set<turnId>`；`endTurn(turnId)` 只清该回合账目；保留 `MAX_RUNTIME_DELTA_KEYS` 语义（每回合上限 + 总上限，避免跨会话并发撑爆内存）；`claim` 行为与既有单会话完全一致 | `use-session-runtime-stream.test.tsx`（"ignores a durable delta from another turn"、"keeps the delta key unclaimed…"）；新增跨界并发用例 |
| `trajectoryStore` | 单实例 + 切换 `reset({hard:true})`（`workspace-page.tsx:191,206`） | 改为 **`Map<sessionId, TrajectoryStore>` 池**（LRU=3，`dispose()` 释放旧 store）；`reset({hard:true})` 只保留给"用户显式回溯/删除/归档"路径 | `use-trajectory-recovery.test.tsx`（tail-first 窗口/前插）；新增"切回不重置"用例 |
| 回合状态 | `isResponding` / `activeTurnId` / `phase` / `activeTurnStateRef` / `activeRequestControllerRef` 单值（`use-workspace-agent-chat-turn.ts:95-97`） | 抽 `SessionTurnRegistry`：`Map<threadKey, TurnRuntime>`（`threadKey = sessionId || threadId`）；对外提供 `useSessionTurnState(threadKey)`；页面把**选中会话**的那份传给 composer/顶栏 | `use-workspace-agent-chat-turn` 既有测试；新增"A 跑 B 提交"用例 |
| 停止 / 中断 | `stopResponding()` 单值 + `requestSessionTurnInterrupt(sessionId, turnId)` | `stopResponding(sessionKey)` 按会话定位 `AbortController` 与 turnId；服务端打断仍走 per-session commands（无需打开流） | `session-turn-control.test.ts`；续传停止路径 `use-workspace-live.test.tsx` |
| 待交互路由 | `resolveApproval` / `answerQuestion` 用 hook 入参 `sessionId`（`use-pending-interactions.ts:106-187`） | 改为**按条目 `sessionId`** 发请求（条目已带会话身份）；`pending` 呈现仍按选中会话过滤；注册表所有订阅会话事件都喂入归约 | `pending-interaction/*.test.ts`；新增"后台会话审批路由"用例 |
| 历史同步 / 提示条件 | `useSessionHistorySync({ isResponding })`、`streamStalled` 为全局值 | 传选中会话的响应态；后台会话响应态由注册表 entry 提供 | `use-session-history-sync` 测试 |

### 4.4 提交流程与回合状态（对应 P0-1）

```ts
// 现状（单飞）
if (!prompt || !selectedThread || isResponding) return;

// 目标（按会话单飞）
const targetKey = sessionKeyOf(selectedThread);         // sessionId || threadId
if (!prompt || !selectedThread || turnRegistry.isBusy(targetKey)) return;
```

- `submitPrompt` 的其余流程不变：`deltaCoordinator.beginTurn(turnId)`（多回合版）→ `streamAgentChat(payload, …)`（`resume_on_disconnect: true` 保持）→ 终态收口写回**发起时的** `threadSnapshot`/`updateCurrentThread` 闭包（已按线程定位，天然支持后台收尾）。
- 草稿会话（`NEW_THREAD_ID`）提交后仍 `navigate` 到新会话；**导航不得影响其它会话在跑的回合**（当前实现已满足：页面不卸载）。
- 每会话的 `phase` / `streamStalled` / `connectionStatus` 由注册表 entry + 回合注册表派生；顶栏与流尾只呈现选中会话。
- 停止按钮：前台会话走本地 abort + 服务端 interrupt（既有）；后台会话走服务端 interrupt（`requestSessionTurnInterrupt`，无需本地 controller）。

### 4.5 切换语义（对应 P1-3）

| 场景 | 现状 | 目标 |
|---|---|---|
| 当前会话在跑，切到空闲会话 | 弹确认框 | 不弹框；目标行/顶栏提示"A 仍在后台运行"；切换即时生效 |
| 切到"后台在跑"的会话 | — | 直接进入其 live 视图；连接已在（或按 `lastSeq` 续传）；不重放已消费事件 |
| 切走再切回 | 全量重放 + store 重置 | 尾窗 + 增量续传；`deltaCoordinator` 账目保留 |
| 同会话重复点击 | 不弹框（既有） | 不变 |
| 关闭/删除正在运行的会话 | 既有归档/删除路径 | 保持：先提示在途回合；不静默丢弃服务端回合（`interrupt` 可选投递） |

### 4.6 资源治理与降级（对应 P2-1）

| 维度 | 策略 |
|---|---|
| 连接预算 | `live` ≤ 3（1 前台 + 2 后台，可配置）；超预算降 `poll`；侧栏提供"跟随实时"显式升级 |
| 轮询退避 | `poll` 周期 3s 起，按 1.5～2 倍退避至 30s；`active_turn` 出现或待交互变化 → 立刻升级 `live` |
| 页面可见性 | `document.hidden` 时：后台 `live` 降 `poll`；前台保留（用户切回来即最新）；重新可见时按策略回升 |
| 内存 | 每会话事件缓冲/轨迹 store LRU（默认 3 个会话）；`live-stream-text` 已有 messageId 维度上限机制（沿用） |
| 服务端对齐 | 后端 `MaxActors=32`（`hub.go:19-25`）；前端订阅 ≤3，长期不触碰上限；`active_connections` 指标可用于观测（`session_runtime_stream_metrics.go:127`） |
| 降级可观测 | 注册表 entry 暴露 `mode`/`lastError`；侧栏行以"运行中（轮询）/连接降级"区分（复用 `transport: "error"` 既有提示位） |

### 4.7 侧栏活动、待办与通知（对应 P1-1 / P2-2）

1. **侧栏活动来源切换**：新增纯函数 `projectSidebarActivity(entries: ReadonlyMap<string, SessionRuntimeEntrySnapshot>) → Map<sessionId, SidebarSessionActivity>`，替代 `buildSidebarSessionActivity` 的"单会话"用法（函数本身保留，作为无注册表时的回退/单测基线）。
   - `running` ← `entry.activeTurn != null || mode === "live" && 最近事件在途`；
   - `pendingApprovals` / `waitingAnswer` / `planPending` ← `entry.pending`；
   - `runningAgents` ← `entry.runningAgents`（subagent 事件已按会话落在事件流里）。
   - 侧栏行状态优先级保持不变（`session-row-status.ts:69-99`）。
2. **待办聚合**：`pendingCounts` 汇总为"哪个会话在等我"的全局入口（顶栏/侧栏组头），点击直达该会话。呈现层仍按会话过滤（既有 `selectPendingInteraction`）。
3. **完成通知**：`finalize-turn` 的通知路径按发起会话身份触发；非选中会话的完成/待交互走桌面通知 + 页内 toast（复用既有 `maybeShowDesktopNotification`，`agent-chat-turn/notifications.ts`）。
4. **不做**：多标签广播、跨窗口同步（§3.2）。

### 4.8 后台会话的写操作路径（对应 P1-2 写入侧；无需打开流）

| 操作 | 路径 | 说明 |
|---|---|---|
| 停止后台会话 | `POST /sessions/{id}/runtime/commands {"type":"interrupt","turn_id"?}` | 已实现；409 回合换代按 `suggested_action` 退避提示，不做重试风暴 |
| 审批 / 回答 | 同端点 `approve_tool` / `answer_question` | 按条目 `sessionId` 路由（§4.3） |
| 继续跑 / 补提交 | 同端点 `continue` / `submit_prompt` | 可选：后台会话的"继续"入口，避免用户必须先切回 |
| 草稿 | `useComposerDraft({ thread })` 已按 `sessionId` 优先分键 | 切会话不丢草稿（harness 计划 P1-4 已交付，直接复用） |

### 4.9 可选后端增强（P2，非本方案前置）

**`GET /api/runtime/sessions/activity`（只读）**：返回当前非空闲会话的轻量列表 `[{session_id, state, active_turn, pending_approval, pending_question, updated_at}]`。

- 动机：`GET /sessions` 不带运行态（`handler.go:2690-2694`），前端若有多条后台会话则需 N 路 `/runtime` 轮询；一个聚合端点可把"发现活跃会话"的成本压到 1 次请求/周期。
- 约束：不得 `GetOrCreate` 新 actor（只读 hub 现有 actor 集合 + `activeTurnRegistry` + 会话元数据），不写事件、不改 SSE 契约；缺省不启用，仅在前端 `poll` 批量超阈值（如后台轮询对象 > 5）时接入。
- 若不做：用 §4.2 的 `poll` 策略即可满足 ≤5 个后台会话的规模（每次请求 ~KB 级）。

---

## 5. 分批实施计划

> 纪律：每批默认"缺省旧行为"；新增行为一律挂在显式开关/显式调用点上；每批都带定向回归 + 可回滚。

### Batch 0：回归基线（先锁链路，再动结构）

| 项 | 内容 |
|---|---|
| 落点 | 前端测试目录（vitest） |
| 内容 | 按 `sse-live-event-channel-optimization-plan.md` §5 的口径，把四条契约写成回归：① tail-first 建连游标（`after = max(本地 seq, 窗口 lastEventSeq)`）；② `deltaCoordinator` claim 去重（含"跨 turn 不误丢"）；③ 轨迹恢复内容帧判据；④ 历史同步等价 |
| 验收 | 现有用例全绿 + 新用例先红后绿（锁定行为） |
| 回滚 | 仅测试，无运行时影响 |

### Batch 1：去单例（纯重构，行为不变）

| 项 | 内容 |
|---|---|
| 落点 | `lib/thread-state/deltas.ts`（多回合键控）、`pages/workspace-page.tsx`（trajectory store 池）、`hooks/workspace/use-workspace-agent-chat-turn.ts`（回合注册表）、`hooks/workspace/use-pending-interactions.ts`（按条目路由） |
| 关键点 | 单会话语义必须逐字节等价：`beginTurn/endTurn/isTurnActive/claim` 的对外语义不变；页面只传"选中会话"的那份状态给 UI |
| 验收 | 既有 vitest 全绿；新增"A 跑 B 提交不互斥""跨会话增量不丢不重""后台审批路由正确"用例 |
| 回滚 | 各改造点保留旧分支（`Map` 尺寸为 1 时行为等价），出问题回退为单实例 |

### Batch 2：注册表与后台订阅（能力批）

| 项 | 内容 |
|---|---|
| 落点 | 新增 `lib/session-runtime/*`、`hooks/workspace/use-session-runtime-registry.ts`、`use-session-stream-supervisor.ts`；改造 `use-session-runtime-stream.ts` 为注册表内的单会话引擎（或注册表直接复用 `streamSessionRuntime`，保留 hook 作为前台适配层） |
| 关键点 | 后台订阅必须继承 IN1/IN2：`after` 取该会话 `lastSeq`（首次为 0 时也必须等尾窗就绪或走 `tail` 语义）；`live=1` 仅对"活跃回合"会话开启 |
| 验收 | 侧栏出现后台会话"运行中"；A 后台跑完，切回 A 无重复消息、无空洞（以 seq 单调性断言） |
| 回滚 | 开关 `multiSessionRegistry=false` → 退化为"仅前台订阅 + 既有侧栏" |

### Batch 3：多会话交互（用户价值批）

| 项 | 内容 |
|---|---|
| 落点 | `use-workspace-thread-selection.ts`（移除打断式确认）、`use-workspace-agent-chat-turn.ts`（按会话提交/停止）、`workspace-page.tsx`（活动投影/通知）、`workspace-shell`（顶栏/侧栏提示） |
| 验收 | ① A 运行中切 B → B 可提交且 A 继续增长；② 侧栏 A 显示运行中/等待审批；③ 后台会话停止按钮生效（`channel` 回报）；④ 完成/待交互有通知 |
| 回滚 | UI 开关（确认框与"后台运行中"提示二选一） |

### Batch 4：资源治理与观测（收敛批）

| 项 | 内容 |
|---|---|
| 落点 | 注册表策略层、`live-diagnostics` 扩展（可选：`mode` / 连接数 / 轮询次数进诊断面板） |
| 验收 | 打开 10 个会话：`live` 连接 ≤3，其余 `poll`；`document.hidden` 降采样生效；`active_connections` 指标与前端观测一致 |
| 回滚 | 预算参数可调（`1/2/3`），关闭降采样即回退到"全 live"（不推荐） |

**依赖关系**：Batch 0 → Batch 1 → Batch 2 → Batch 3 → Batch 4；Batch 2 的注册表在 Batch 1 之前落地也可以（互不冲突），但推荐按序以缩小回归面。

---

## 6. 风险、回滚与既有决策冲突

### 6.1 与既有决策的冲突点（必须显式声明）

| 冲突点 | 既有结论 | 本方案立场 | 处置 |
|---|---|---|---|
| 多标签 / 多窗口工作台 | 「当前路由与状态模型不支撑，收益不明」→ 不采纳（`frontend-deepseek-harness-optimization-plan.md:721`） | **不冲突**：本方案不做多标签/多窗口，仍是单窗口单视图 | 文档层面写明边界；不触碰路由与窗口模型 |
| 多标签状态广播 | 「会引入双写流与重复 SSE」→ 不采纳（同文件 `:736`） | **不引入广播**：注册表只在单页内工作，不新增跨标签通道；SSE 连接数有预算 | §4.6 预算与分级订阅即该风险的收敛手段 |
| 「既有前端资产只允许增强/复用」 | SSE 常驻重连 + seq 续传、tail-first、单调揭示打字机等不得另起第二套 | **遵守**：注册表复用 `streamSessionRuntime`/`consumeSseStream`/既有归约；不新写 SSE 解析器 | Batch 2 落点表明确"复用而非重写" |
| 事件信封 `skill_runtime.sse.v1` 不全局升版 | 新增帧内字段只允许可选新增 | **遵守**：不新增帧类型、不改帧格式 | 本方案无服务端流改动 |
| 单会话 `deltaCoordinator` 去重是"兜底" | 服务端冗余仍在（`sse-live-event-channel-optimization-plan.md:183`） | 多回合化不改变该定位；不得借机放宽服务端去重 | Batch 1 保持 claim 语义逐字节等价 |

### 6.2 主要风险与缓解

| 风险 | 触发条件 | 影响 | 缓解 |
|---|---|---|---|
| 后台流与前台流重复渲染同一段增量 | 双通道（chat SSE + runtime stream）在同一回合上同时到达 | 文本重复/错序 | 多回合 `deltaCoordinator` + IN3；Batch 0 回归锁定 |
| 切回时"补帧"把已渲染内容再渲染一次 | `lastSeq` 与轨迹窗口游标不一致 | 消息重复 | 建连游标取 `max(本地 seq, 窗口 lastEventSeq)`（既有契约）；新增"切回不重放"用例 |
| 后台会话数量增长导致连接/内存膨胀 | 用户多开会话长期不关 | 资源占用、代理侧断连 | 连接预算 + `poll` 降级 + 轨迹 store LRU + 页面隐藏降采样 |
| 后台回合完成但前端不知道（漏终态） | 订阅在终态事件到达前被释放 | 侧栏状态悬挂 | 释放前必须"回合终态 + 宽限"；宽限后仍悬挂则由 `poll` 快照兜底收敛 |
| 同会话并发提交（前端双开/快速点击） | 前端 gate 竞态 | 服务端 `ErrSessionBusy` / 409 | 前端 per-session gate + 服务端权威校验；409 不做重试风暴（按 `suggested_action` 提示） |
| 环形缓冲窗口溢出导致续传缺口 | 后台会话长时间 `poll` 后升级 `live`，`lastSeq` 已出环 | 丢历史 | 升级时若 `lastSeq < 可回溯最早 seq`，先走轨迹/历史重建再建连（复用 `useSessionHistorySync` 恢复路径），不得静默断层 |

### 6.3 回滚策略

- 总开关：`multiSessionRegistry`（`lib/session-runtime/flags.ts` 的 `MULTI_SESSION_REGISTRY_ENABLED`；当前 `true`。改回 `false` = 旧行为：仅前台订阅 + 单实例状态 + 单飞闸门）。
- 分项开关：`perSessionDeltaCoordinator`（多回合 claim）、`backgroundSubscriptions`（后台订阅）、`switchWithoutConfirm`（切换确认框）。
- 每批独立合入，任何一批出问题可只回退该批（Batch 1 的 Map 尺寸为 1 时与旧实现等价）。

---

## 7. 验收与测试计划

### 7.1 单测 / 组件测试（vitest）

| 用例 | 断言 |
|---|---|
| 跨会话并发：A 回合在跑时提交 B | 两边 `isResponding` 独立；A 的增量继续应用；B 的增量独立应用 |
| `deltaCoordinator` 跨回合 | B `beginTurn` 后 A 的已 claim key 仍返回 `false`；A 的新 key 返回 `true`；`endTurn(B)` 不影响 A |
| 会话切换无损 | 切走再切回：`after` = `max(本地 seq, 窗口 lastEventSeq)`；收到的 seq 严格单调、无重复 |
| 后台审批路由 | 后台会话条目的 `resolveApproval/answerQuestion` 打到条目自身 `sessionId` |
| 轨迹 store 池 | 切换会话不 `reset`；显式回溯/归档才 `reset` |
| 订阅策略 | 10 个会话下 `live` 实例 ≤ 预算；回合终态后降级 `poll`；`document.hidden` 降采样 |

### 7.2 端到端（人工 + 脚本）

1. 会话 A 提交长任务 → 切到 B → B 提交短任务：两个会话的流同时推进（可用 `scripts/analyze-sse-live-audit.py` 口径抽样观测帧数与重复率）。
2. A 需要工具审批（可用 mock/回放夹具）→ 侧栏出现等待标记 → 在 B 页面直接允许 → A 继续。
3. 停止后台 A：`interrupt` 返回 `channel=active_turn`，A 的侧栏标记收敛；重复点击返回 `already_cancelled` 且不报错。
4. 刷新页面（IN2）：刷新后 A 仍以 `active_turn` 认领回合身份并续传（复用既有轨迹恢复路径，不在本方案重写）+ 注册表恢复（多会话版）。
5. 长会话（>2000 事件）切换：首屏不出现全量 dump（网络面板无 MB 级重放）。

### 7.3 门禁

- `npm run build` / `npm run test`（vitest）/ `npm run test:e2e`（如涉及）/ `lint:i18n`（新增文案）。
- 后端：`go test ./... -race`（仅在触及后端可选接口时）。
- 文档：本文件与 `sse-live-event-channel-optimization-plan.md` 的状态标注一致（"现状 vs 历史"分节）。

---

## 8. 开放问题

| # | 问题 | 状态 | 建议 |
|---|---|---|---|
| Q1 | 后台 SSE 预算默认值（1+2 是否够） | 开放 | 先按 1+2 实现，观测 `active_connections` 与实际体验后调整 |
| Q2 | 是否引入 §4.9 的 `sessions/activity` 聚合端点 | 开放（可选） | 后台轮询对象 > 5 时再启用；须保证只读、不建 actor |
| Q3 | 后台会话的"切回补齐"是否要预取历史（用户可能频繁来回切） | 开放 | 先用"切回时同步"，若首屏可感知延迟再考虑预热最近 1 个会话 |
| Q4 | 侧栏"跟随实时"升级交互（顶掉策略） | 开放 | 默认 LRU 顶掉；需要时可给"钉住"选项 |
| Q5 | 后台会话的"继续跑"入口（`continue` 命令）是否进 Batch 3 | 开放 | 建议延后（Batch 3 只做停止/审批/回答），避免一次改太多 |
| Q6 | Electron/桌面通知权限与降噪（多个后台会话同时完成） | 开放 | 合并通知（"2 个会话需要你"），窗口聚焦时不发桌面通知 |

---

## 附录 A：取证方法与样本

- 本文档的前端结论来自 `frontend/src` 的静态阅读（`view`/`grep`），关键行号已在正文标注；后端结论来自 `backend/` 只读检索与一次只读子代理调研（`handler.go`、`session_runtime_stream.go`、`session_runtime_store.go`、`hub.go`、`actor.go`、`session_runtime_handlers.go`、`session_active_turn.go`）。
- 环境：本机 dev 前端 `http://localhost:5193`（Vite，`/api` 代理到 runtime-server）；用户观察样本为 `/workspace/sessions/session_20260917073647_6qRn453N`。
- 复现建议：用同一会话开两个浏览器标签分别停留在 A/B（当前版本可观察"后台继续跑"与"侧栏无标记"两个现象，作为改造前基线）。

## 附录 B：现状 → 目标 diff 速览（审阅用）

| 能力 | 现状 | 目标 | 主要改动 |
|---|---|---|---|
| 跨会话并行提交 | ❌ 全局单飞 | ✅ 按会话单飞 | `SessionTurnRegistry` + 提交 gate 按 `threadKey` |
| 多路 SSE | ⚠️ 仅前台 1 条 | ✅ 前台 + ≤2 后台（超预算轮询） | `SessionRuntimeRegistry` |
| 增量去重 | ❌ 单回合账目 | ✅ 多回合账目 | `deltas.ts` 多回合键控 |
| 轨迹/游标 | ❌ 切换重置 | ✅ 每会话保留 | store 池 + `lastSeq` |
| 侧栏运行态 | ❌ 只有当前会话 | ✅ 全量投影 | `projectSidebarActivity` |
| 后台审批/停止 | ⚠️ 路径存在但路由错 | ✅ 按条目会话路由 | pending 路由 + commands |
| 切换确认框 | ⚠️ 误导 | ✅ 不打断语义 | 移除打断式确认，改为运行提示 |
