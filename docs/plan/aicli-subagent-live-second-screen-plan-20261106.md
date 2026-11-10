# aicli 子 agent 实时第二屏幕渲染：现状分析与实施方案

- 日期：2026-11-06
- 状态：方案稿（先分析，暂不进入实现）
- 范围：`aicli chat` unified interactive TUI（`/agents`、`/agent`、`/agents view`）
- 非范围：plain/JSON/headless 输出、Web 前端 `SubagentSessionDialog`、子 agent 控制面写入

## 0. 结论摘要

子 agent 与主 agent 实际上走的是同一套 ReAct loop 和 runtime event schema：

- 子 agent 由 `internal/agent/child_factory.go` 创建，并执行
  `childAgent.SetEventBus(parent.GetEventBus())`，与父 agent 共用 EventBus。
- 子 agent 的 `assistant.delta`、`assistant.reasoning`、tool 事件、`llm.request.*`
  与主 agent 具有相同的 payload / 流式语义，只是 `SessionID` 是子会话 ID。
- 父会话 bridge 通过 `isForeignSessionContentEvent` 主动过滤子会话正文/思考/工具，
  这是防止子内容污染父 Scene 的既有隔离约束，必须保留。
- 子会话事件已经持久化到 `EventStore`（按 session_id + seq），同时也在共享 EventBus
  上实时发布；因此“历史回放 + 实时订阅”两条数据源都具备。

当前 `/agents` 与 `/agent <target>` 的问题不是数据没有实时产生，而是 unified
第二屏幕只消费了一次性快照：

- `/agents` 的列表是 `chatScreenAgentsListSpec` 构建的静态 `ScreenList` 快照。
- 选中 agent 后，`chatAgentListAfterCloseHandler` 调用
  `buildChatAgentTranscriptView` 从 EventStore 读取尾部事件，渲染成纯文本行，
  再包装为静态 `ScreenDocument`。
- `/agent <target>` / `/agents view <target>` 的 structured 路径同样只提交静态
  `chatScreenAgentTranscriptSpec(lines)`，并明确输出
  `follow=unavailable（一次性快照输出；交互式 popup 模式支持实时刷新）`。
- 已有 follow 订阅只服务 legacy popup / console 路径，没有接入 unified
  `ScreenDocument`。

第二屏幕框架本身已经具备实时渲染所需的基础：

- `chatScreenSpec.RunDocument` 允许在租约内使用自定义渲染原语。
- `ui.DebugOverlayOptions.Refresh` 已支持 75ms 轮询刷新，但它面向纯文本 body，
  没有 follow-bottom，也不保留结构化样式。
- `ui.RunTranscriptPagerWithLease` 已经支持 `Snapshot()` 每 75ms 轮询、live tail、
  follow-bottom、滚动 anchor、折叠展开；`Ctrl+T` 主 transcript 正在使用它。

推荐方案：

> **为被查看的子会话建立独立的 `EventEncoder + Scene` 投影器，复用主 agent 的
> 事件编码/Scene 语义；第二屏幕优先复用 `TranscriptPager` 的实时快照能力。**
>
> Phase 1 得到“同一套请求响应语义 + 实时更新 + follow-bottom”的可用视图；
> Phase 2 再把 pager 的 plain row formatter 替换为 `render.Line`/`AppRenderFrame`
> 的富呈现，逼近主 agent 的 Markdown、reasoning divider、tool fold 和 active band。

### 0.1 方案 B 审查结论（2026-11-06）

**方向成立，但当前版本不是“可直接开工”的完整闭环方案。**

数据面复用 `EventEncoder + Scene` 是正确的；`TranscriptPager` 的
`Snapshot()` 轮询、follow-bottom、live tail 也足以支撑 Phase 1。但按现文档实现，
有几处会直接导致“看起来能跑、实际会掉事件/卡住/回不了列表”的缺口：

| 等级 | 缺口 | 影响 |
| --- | --- | --- |
| P0 | live 事件队列与 backfill 的先后顺序/去重没有落到具体队列协议 | 可能丢事件、重复显示、乱序 |
| P0 | 两条独立异步订阅会破坏 assistant/reasoning/tool 的跨类型顺序 | reasoning 与 assistant 可能错位 |
| P0 | target 是 pending batch task、child session 尚未绑定时没有入口策略 | 用户选中后无法打开 live 视图 |
| P0 | `subagent.completed` 的 finalize 与晚到终态消息没有明确的 drain 顺序 | active cell 可能不结束，或终稿被截断 |
| P0 | 订阅/worker 的关闭、队列丢弃、投影失败没有统一的降级出口 | 可能泄漏 subscription/goroutine，或屏幕空白 |
| P0 | `renderTranscriptPagerFrame` 使用 `fitFullScreenText`（`strings.Fields`）输出行 | reasoning/代码的缩进与连续空格被折叠，内容不忠实 |
| P0 | pager 的 live tail 对 tool cell 使用 `expanded=true` | 运行中的工具输出可能整段铺屏，滚动/刷新变慢 |
| P1 | 从 live 视图返回列表/切换 agent 的路径未定义 | 每次换 agent 都要退出再 `/agents`，操作不顺手 |
| P1 | pending approval/question 只能看不能操作 | 子 agent 阻塞时用户需要退出再敲命令 |
| P1 | 列表静态刷新与 live 视图的关系未定义 | 新 spawn 的 agent 在列表里看不到 |
| P1 | pager 每 75ms 全量 clone cells / 重建 model | 长期运行/大工具输出时 tick 成本上升 |

结论：**先补齐 P0，再进入 Phase 0 实现；P1 在 Phase 1 内解决“可操作但不越权”的最小路径。**
下面 §4.1.4、§4.2.1、§4.6 和 §12 给出修订后的闭环要求。

## 1. 当前实现现状

### 1.1 `/agents` 与 `/agent` 的命令链路

`/agents` 的 unified 入口：

```text
executeStructuredAgentsCommand
  -> chatScreenDocResult(chatScreenAgentsListSpec(session))
     -> chatScreenSpec{Kind: chatScreenList, Rows: 一次性快照}
     -> AfterClose: chatAgentListAfterCloseHandler(agents)
          -> buildChatAgentTranscriptView(session, opts)
          -> chatScreenAgentTranscriptSpec(view.Lines())
          -> chatScreenOpenAndApply(...)
```

关键位置：

- `backend/cmd/aicli/commands/chat_debug.go:287`：`/agents` 动词分发。
- `backend/cmd/aicli/commands/chat_agent_screen.go:22`：`chatScreenAgentsListSpec`。
- `backend/cmd/aicli/commands/chat_agent_screen.go:125`：
  `chatAgentListAfterCloseHandler`，选中后构建静态 transcript。
- `backend/cmd/aicli/commands/chat_agent_transcript.go:479`：
  `buildChatAgentTranscriptView`，读取 EventStore 尾部事件并渲染纯文本。
- `backend/cmd/aicli/commands/chat_screen_batch3.go:93`：
  `chatScreenAgentTranscriptSpec`，把行文本包装成静态 `ScreenDocument`。

`/agent <target>` / `/agents view <target>`：

```text
executeStructuredAgentTranscriptCommand
  -> buildChatAgentTranscriptView(...)
  -> chatScreenAgentTranscriptSpec(lines)
  -> render + dispatchScreenEffects
```

其 unified 分支在 `chat_agent_transcript.go:118`，当前会追加：

```text
follow=unavailable（一次性快照输出；交互式 popup 模式支持实时刷新）
```

这说明 unified 第二屏幕在设计上主动放弃了 live modal。

### 1.2 `/agents` 列表本身也是静态快照

`chatScreenAgentsListSpec` 只调用一次 `chatAgentGraphItems(session)`：

- 数据源合并 `agent_control_agents` 注册表和 batch task 账本。
- 每行 Detail 是 `status=... state=... depth=... type=... run=... tool=...`
  这类短摘要。
- 列表打开期间不会刷新；新 spawn 的 agent、状态变化、pending approval 都不会
  自动出现在列表里。

列表作为“选择入口”使用静态快照是可以接受的；真正的动态需求应落在选中后的
transcript 视图。若后续需要“列表也实时”，可以复用同一 projector 的 status
投影，但不应把列表和 transcript 绑定成同一条渲染链。

### 1.3 子 agent 的运行时事件链路

子 agent 创建：

- `backend/internal/agent/child_factory.go:158` 创建 child agent。
- `child_factory.go:187`：`childAgent.SetEventBus(parent.GetEventBus())`。
- `backend/internal/agent/scheduler.go:1127` 起：scheduler 使用 child session ID
  执行 child loop。
- `scheduler.go:1167`：发出 `subagent.started`（event.SessionID = child session）。
- `scheduler.go:1370` 起：发出 `subagent.completed`（event.SessionID = child session）。
- child ReAct loop 的 `assistant.reasoning` / `assistant_delta` / tool 事件
  payload 与主 agent 同构（见 `internal/agent/loop.go:2855-2886`）。

事件总线：

- `backend/internal/events/bus.go:323`：`Publish` 在发布者 goroutine 上同步派发所有
  subscription。
- `backend/internal/events/async.go:98`：`SubscribeAsync` 提供有界队列、独立消费
  goroutine、DropOldest/DropNewest/DropBlock 策略；适合第二屏幕这种“出口型”消费者。

持久化：

- `chat_actor_host.go:1180` 起：EventBus 订阅把持久化事件写入 EventStore。
- EventStore 支持 `ListEventsBefore(sessionID, beforeSeq, limit)`，返回升序尾部窗口，
  并注入 `payload["seq"]`。
- `subagent.progress` 是 live-only 的父会话镜像（`internal/events/contract.go:138`），
  不落 EventStore；它不是子会话 Scene 的必需输入，但可用于父侧状态行。

隔离约束：

- `backend/cmd/aicli/commands/chat_runtime_events.go:9580`
  `isForeignSessionContentEvent` 明确阻止子会话正文/思考/工具进入父 Scene。
- 因此不能通过“让父 bridge 顺便渲染子事件”实现第二屏幕；必须建立独立的
  child-scoped projector，或在父桥之外新增旁路视图。

### 1.4 主 agent 的实时渲染链路

主 agent 当前的数据面：

```text
EventBus
  -> chatRuntimeEventBridge.Handle
     -> stream coalescing / ordered backlog
     -> EventEncoder.Encode
     -> ChangeSet
     -> scene.TuiScene (ChangeSetMapper.Apply)
     -> sceneSnapshot()
     -> UIController / AppState
     -> LayoutAppScreen / ComposeAppRenderFrame
     -> TerminalSessionPresenter
```

关键位置：

- `chat_runtime_events.go:45`：`chatRuntimeEventBridge` 的字段与缓存。
- `chat_runtime_events.go:842`：`newChatRuntimeEventBridge` 初始化
  `EventEncoder`、`TuiScene`、`ChangeSetMapper`。
- `chat_runtime_events.go:3462`：`encodeRenderModelEvent`，统一编码入口。
- `chat_runtime_events.go:5154`：`sceneSnapshot`。
- `chat_runtime_events.go:5170`：`activeMutableCell`。
- `ui/app_screen_layout.go:69`：`LayoutAppScreen`，Transcript + ActiveBand + Bottom。
- `ui/app_render_frame.go:45`：`ComposeAppRenderFrame`，保留 `render.Line` 结构化样式。
- `ui/app_state.go:390`：`ActiveCellFromSnapshot`，从 Scene 快照取 mutable cell。

主 agent 的实时观感来自三层：

1. **语义层**：EventEncoder 按 request/stream/sequence 合并 reasoning、assistant delta；
2. **Scene 层**：mutable cell、committed cell、tool cell、boundary/gap；
3. **presenter 层**：reasoning divider、Markdown、tool fold、active band、状态行。

子 agent 已经具备第 1 层的事件输入，缺的是 child-scoped 的第 1/2 层实例和
第二屏幕 presenter。

### 1.5 第二屏幕现有能力

统一副屏框架：

- `backend/cmd/aicli/commands/chat_screen_framework.go:140`：`chatScreenSpec`。
- `chat_screen_framework.go:483`：`chatScreenRun`。
- `chat_screen_framework.go:495`：`ScreenDocument` 默认走
  `ui.DebugOverlayOptions{Title, Body, Refresh, RefreshHint}`。
- `chat_screen_framework.go:761`：`chatScreenDocumentRunner` 调用
  `ui.RunDebugOverlayWithLease`。

`DebugOverlay`：

- `ui/debug_overlay.go:26`：`DebugOverlayRefreshFunc`。
- `ui/debug_overlay.go:58`：轮询周期 75ms。
- `ui/debug_overlay.go:152`：每帧轮询 `Refresh`，`changed=true` 时重绘。
- 限制：body 是 plain text；无 follow-bottom；无 `render.Line`；滚动 offset 是
  本地状态，没有“用户滚上去后保持锚点”的完整语义。

`TranscriptPager`：

- `ui/transcript_pager.go:17`：`TranscriptPagerSnapshot{Transcript, Active}`。
- `ui/transcript_pager.go:95`：`NewTranscriptPagerModel` 把 mutable cell 变成
  `LiveTail`，避免 history/live 重复。
- `ui/transcript_pager.go:173`：`Rows(width)` 从语义 source 派生行。
- `ui/transcript_pager.go:492`：`TranscriptPagerOptions{View, Snapshot, ViewState,
  PostAction}`。
- `ui/transcript_pager.go:611`：`runTranscriptPagerLoop` 每 75ms 重新读取
  `Snapshot`/`View`，按 revision + live tail 变化决定重绘。
- `ui/transcript_pager.go:322`：`FollowBottom` 语义；用户滚上去后保持 anchor。
- 限制：现有 row formatter 仍是 plain text，header 是固定 `"Transcript"`，
  没有 Markdown/Reasoning divider/tool fold 的富样式。

### 1.6 已有修复与仍然存在的缺口

上一轮已经修复了静态 transcript 中 reasoning 被拆成多个 `[reasoning]` 块的问题：
`chatAgentTranscriptBodyBuilder` 现在按 request/stream 身份聚合 delta。这个修复
只解决了“同一快照内的文本聚合”，没有解决：

- unified 第二屏幕不刷新；
- 没有 child-scoped `EventEncoder/Scene`；
- 没有主 agent 风格的 active cell / Markdown / tool fold；
- 没有 live 事件与历史回填的并发/去重/退订设计；
- 没有状态头和 ended/failed 表达。

现场样本（本机 `session_runtime.sqlite`）也印证了这一点：
`session_20261106131025_dOOBEERM` 有 2449 条事件，其中 2378 条是
`assistant.reasoning`；单个 `llm_request_id` 的 reasoning 由 9 个连续 delta 组成。
静态视图的默认尾部窗口按“事件条数”截取时，很容易只覆盖思考流的一小段；即使
文本聚合正确，仍然不是完整、持续更新的子 agent 视图。

## 2. 目标与非目标

### 2.1 目标

1. 在 unified TUI 中打开某个子 agent 后，第二屏幕持续显示该子会话的实时
   assistant/reasoning/tool 流，而不是一次性快照。
2. 复用主 agent 的 runtime event -> EventEncoder -> Scene 语义，保证
   reasoning 聚合、assistant delta 顺序、tool cell 生命周期与主 agent 一致。
3. 默认 follow-bottom：新事件到达时视图跟随底部；用户向上滚动后保持阅读锚点。
4. 展示子 agent 的身份与状态：path、session id、status、run status、当前工具、
   pending approval/question、elapsed、ended/failed。
5. 保持只读：视图不写子会话 history、不推进子 turn、不修改父会话。
6. 关闭屏幕时释放 EventBus subscription、worker goroutine 和 Scene/encoder 引用。
7. 能力不足或投影器失败时回退到现有静态 transcript，不出现空白屏或 legacy
   stdout 直写。

### 2.2 非目标（首期）

- 不做多 agent 网格/并排多窗格；一次只查看一个 target。
- 不把子 agent 的审批/提问变成本屏可交互输入；首期只展示状态与可复制命令。
- 不改 Web `SubagentSessionDialog`；它已有独立实时 store。
- 不改父会话主屏的隔离规则，也不让子内容进入父 Scene。
- 不做跨进程/远程 runtime 的实时第二屏幕（仍以本地进程内 EventBus/EventStore 为准）。

## 3. 总体方案

### 3.1 架构图

```text
/agents list (静态选择快照)
  └─ 选择 target
      └─ chatAgentLiveScreenSpec(target, sessionID)
          └─ chatAgentSceneProjector
              ├─ EventStore.ListEventsBefore  ── 历史回填（seq 有序）
              ├─ EventBus.SubscribeAsync     ── 子会话实时事件（SessionID 过滤）
              ├─ EventEncoder.Encode         ── 与主 agent 同一编码器
              ├─ ChangeSet -> Scene          ── 与主 agent 同一语义模型
              └─ Snapshot()                  ── TranscriptPagerSnapshot
          └─ ui.RunTranscriptPagerWithLease
              ├─ 75ms Snapshot() 轮询
              ├─ FollowBottom / scroll anchor
              └─ live tail "(streaming)"
```

### 3.2 设计不变量

- **I1 数据面单一权威**：live 视图只消费 child-scoped `EventEncoder + Scene`，
  不在新路径里把原始事件直接拼成显示文本。
- **I2 会话隔离**：projector 只接受 `event.SessionID == targetSessionID` 的事件；
  父 bridge 的 `isForeignSessionContentEvent` 保持不变。target 必须先经过
  现有 `resolveChatAgentTarget` / `resolveLocalAgentTargetSessionID` 的可见性
  映射；不能直接把用户输入的任意 session ID 传给 projector。
- **I3 只读**：不调用 child actor 的 Submit/Answer/ResolveApproval；不写
  SessionStore/EventStore。
- **I4 生命周期绑定租约**：projector 在 `RunDocument` 内创建，在函数返回前
  `Close()`；Esc/panic/错误路径都必须退订。
- **I5 有界与非阻塞**：EventBus 发布者不能被第二屏幕拖死；订阅使用异步队列，
  delta 可合并/丢弃可重建增量，终态事件优先保留。
- **I6 fail-closed / 可降级**：无法创建 projector、target 已结束或没有 EventStore
  时，退回现有静态 transcript；不直写 stdout。
- **I7 同一身份/顺序规则**：reasoning/assistant 的 request/stream/sequence、
  tool_call_id、session_end 处理与主 bridge/encoder 保持一致。

### 3.3 可选方案对比

| 方案 | 数据源/渲染 | 优点 | 缺点 | 结论 |
| --- | --- | --- | --- | --- |
| A. 给现有静态 transcript 加 `DebugOverlay.Refresh` | EventStore 重新读取 + 纯文本 builder | 改动最小；复用已有 `[reasoning]` 文本聚合 | 仍是 plain text；无 active cell/Markdown/tool fold；DebugOverlay 无 follow-bottom；每次 tick 重新构建文本，容易 O(n) | 只适合应急，不作为目标 |
| B. child `EventEncoder + Scene` + `TranscriptPager` live snapshot（推荐） | 与主 agent 同一事件编码/Scene 语义；pager 提供 75ms 轮询、live tail、follow-bottom | 数据面与主 agent 同源；实时/滚动/终态都已有原语；改动可控 | pager 当前仍是 plain row formatter，视觉未完全对齐主 agent | **Phase 1 采用** |
| C. child `AppState + ComposeAppRenderFrame` + ANSI pager | 完整复用主 agent 的 `render.Line`/Markdown/active band | 视觉最接近主 agent；一条 presenter 路径 | 需要新的 ANSI-safe pager/frame writer 或改造现有 pager；测试面更大 | Phase 2 收敛方向 |
| D. 每个子 agent 实例化一个 `chatRuntimeEventBridge` | 直接复用主 bridge | 表面上代码复用 | bridge 依赖 UI actor、事件日志、审批/提问、状态行和 primary session 语义；会引入副作用和第二个 Scene 所有权 | 不推荐；只考虑抽取共享 render-plane 后再复用 |

方案 B 的关键判断是：**实时数据面必须先与主 agent 同源，视觉可以后补**。
如果先做方案 A，虽然能“动起来”，但仍会保留一套独立的文本渲染逻辑，后续迁移到
富呈现时仍要重写；如果直接做 C，则在没有验证事件回填/订阅/终态之前就引入新的
presenter，风险过高。

与既有方案记录的关系：`docs/plan/aicli-tui-alt-screen-framework-implementation-record-20260928.md`
曾明确“`/agents panel` 的 live modal 不复活”，那是针对旧式 modal 输入循环的
决定。本方案不复活旧 modal，而是新增一个受统一框架租约管理的只读 live
`ScreenDocument` 渲染体；生命周期、Esc、降级仍走 `openChatScreen`。

## 4. 组件设计

### 4.1 `chatAgentSceneProjector`

建议新增文件：

`backend/cmd/aicli/commands/chat_agent_live_view.go`

职责：把一个 child session 的事件流投影成可被第二屏幕消费的 `Scene` 快照。

建议结构：

```go
type chatAgentSceneProjector struct {
    target    string
    sessionID string

    mu      sync.RWMutex
    encoder *encoding.EventEncoder
    scene   *scene.TuiScene
    mapper  *scene.ChangeSetMapper

    sub     *runtimeevents.AsyncSubscription
    queue   chan runtimeevents.Event
    stopCh  chan struct{}
    doneCh  chan struct{}

    ended   bool
    lastErr string
    revision uint64
}
```

字段说明：

- `encoder`：与主 bridge 相同的 `encoding.NewEventEncoder()`。
- `scene/mapper`：`scene.New()` + `scene.NewChangeSetMapper(scene)`。
- `mu`：保护 encoder/scene/mapper/snapshot 读取；`Snapshot()` 在锁内构建不可变
  `TranscriptPagerSnapshot`。
- `sub`：`EventBus.SubscribeAsync("", handler, AsyncOptions{...})`；handler 只做
  SessionID 过滤与入队，不在发布者 goroutine 上编码。
- `queue/stopCh/doneCh`：单 worker 顺序消费，保证 EventEncoder 非线程安全约束。

#### 4.1.1 启动顺序（避免回填与 live 之间的缺口）

推荐按以下顺序：

1. 解析 target -> child session ID（复用现有 `resolveChatAgentTarget` /
   `resolveLocalAgentTargetSessionID`）。
2. 创建 encoder/scene/mapper 和 projector 队列。
3. **先订阅** EventBus（异步队列开始缓冲 live 事件），但 worker 暂不消费。
4. 从 EventStore 尾部向前分页读取 child session 事件（`ListEventsBefore`），
   按 seq 升序回填到 encoder/scene。
5. 回填完成后，启动 worker 消费第 3 步缓冲的 live 事件。
6. 对重复事件依靠 EventEncoder 的 sequence/identity 幂等性，必要时补一层
   `seen` 集合（tool_call_id + 状态、stream_id + sequence）。

回填与 worker 都必须接受 `ctx.Done()`：用户 Esc 关闭时，分页循环在页间检查
取消，worker 停止消费并 join；不能因为一个 10k 事件的回填让屏幕关不掉。

为什么必须先订阅再回填：

- 如果先回填再订阅，回填完成到订阅建立之间到达的事件会丢失。
- 先订阅会让部分事件在回填里再次出现；EventEncoder 对同一 sequence 的 streaming
  delta、同一 tool_call_id 的 started/finished 本身有幂等/去重语义，重复 encode
  不应产生第二份显示内容。实现时需要补一条回归测试锁定。

回填窗口：

- 复用 `ListEventsBefore` 分页，页大小 1000。
- 设上限（例如 `chatAgentSceneMaxBackfillEvents = 10000`）防止极端会话拖慢开屏；
  达到上限时在 header 标注 `early history omitted`。
- 如果最早读到的 `payload["seq"] > 1`（store retention 已裁掉早期事件）也要
  标注 `early history unavailable`；不能把“从中间开始”伪装成完整历史。
- 子 agent 通常比父会话短；首期不要求像父 transcript 那样把全部历史都装入
  Scene。当前轮 + 最近工具链完整可见即可。

#### 4.1.2 事件过滤

只处理 child session 的事件：

```go
if event.SessionID != p.sessionID { return }
```

`SessionID` 为空的事件一律不接受（不猜、不按 payload 文本猜归属）。子会话
内容必须显式带 child session ID；如果发现某类事件漏带，应修生产端而不是放宽
projector 的过滤，否则会重新引入父子串流。

首期分为两条处理路径，不能把所有事件都塞给 EventEncoder：

**A. 进入 Scene/EventEncoder 的内容事件**

- `assistant.delta` / `assistant_delta`
- `assistant.reasoning` / `assistant_reasoning`
- `assistant.message`
- `llm.request.started` / `llm.request.finished`
- `tool.started` / `tool.requested`
- `tool.progress`
- `tool.finished` / `tool.completed` / `tool.failed`
- `tool_receipt_recorded` / `tool_receipt_replayed`
- `session_end` / `session_interrupted`（encoder 负责收尾 open streams）
- `assistant_stream_reset`（重试边界；encoder 已有处理）

**B. 只进入投影器状态机、不进入 Scene 的控制/状态事件**

- `subagent.started` / `subagent.completed`：更新 running/ended/status，
  结束时额外调用 `FinalizeOpenStreams` / `FinalizeOpenToolCells`。
- `approval_requested` / `question_asked`：更新 header 的 pending 状态和可复制
  命令提示；首期不把它们编码成 Scene cell。
- `approval_resolved` / `question_answered`：清除 pending 状态；如果当前版本
  没有稳定事件，则以 registry reconcile 的结果为准。
- `subagent.progress`（若未来需要显示更细的父侧镜像进度）：只更新状态，
  不进入 child Scene；子会话自身已有 tool 事件作为主数据源。

忽略：

- `input.queue.*`、`aicli.chat.dynamic_status` 等 render data-plane 抑制事件。
- 其他与 child Scene 无关的诊断/审计事件。

#### 4.1.3 结束与失败

- 收到 `subagent.completed` 或 `session_end`：
  - 调用 `encoder.FinalizeOpenStreams(status)` 和
    `encoder.FinalizeOpenToolCells(status)`；
  - 应用返回的 ChangeSet；
  - 标记 `ended=true`，保留最终 Scene；
  - 不再接受 streaming delta；迟到的终态消息可按 encoder 规则纠正。
- 收到 `session_interrupted`：同上，status = canceled。
- projector 初始化失败：返回 error，外层回退静态 transcript。
- mapper Apply 失败：记录 `lastErr`；可像主 bridge 一样从
  `encoder.Snapshot()` 重建 Scene（`rebuildSceneFromRenderModel` 的 child 版），
  本轮失败不应导致整个屏幕关闭。

#### 4.1.4 单队列、顺序与背压协议（P0 修订）

不能为 `assistant.delta`、`assistant.reasoning`、tool 事件各建一条独立异步订阅：
`SubscribeAsync` 每条订阅有独立队列和独立消费 goroutine，跨事件类型的到达顺序
会丢失，reasoning 可能排到 assistant 之后，tool finished 可能早于 tool started。

正确做法是**单入口、单有序队列、单 worker**：

```text
EventBus.Publish (同步)
  -> projector.enqueue(event)       // 只过滤 SessionID，非阻塞入队
  -> one ordered queue
       - stream delta: 可合并/可淘汰
       - tool/assistant terminal: 保留，不允许被 delta 挤掉
       - session/control terminal: 保留，独立容量/优先级
  -> one worker goroutine
       -> EventEncoder.Encode
       -> ChangeSet -> Scene
```

具体要求：

- 队列项按流身份合并（type + stream_id + sequence），与主 bridge 的
  `mergeStreamEvents` / `chatRuntimeQueuedEvent` 语义一致；不得另写一套文本
  拼接规则。
- 队列满时只允许淘汰可重建的 stream delta；`assistant.message`、
  `subagent.completed`、`session_end`、`tool.finished` 等终态事件必须保留。
  可借鉴主 bridge 的 class-based backlog（critical reserve）设计。
- 入队发生在 EventBus 发布者 goroutine，必须是非阻塞的；worker 慢时不能让
  LLM 回调链等待。
- 单 worker 串行调用 `EventEncoder`（encoder 非线程安全）。
- 优先把主 bridge 已有的 ordered backlog / class eviction / stream merge
  抽成小接口复用；如果抽取成本过高，至少实现一个最小 class-based queue 并用
  同样的回归测试锁定，不要复制一份难维护的巨型 backlog 代码。
- 记录 `queued/delivered/merged/dropped/control_pressure` 计数，并在 header
  或 `/debug` 暴露；发生 drop 时用户能看到 “live degraded”。
- 即使 delta 被 drop，`llm.request.finished` 携带的
  `assistant_snapshot`/`reasoning_snapshot` 与 `assistant.message` 终态必须能把
  Scene 收敛回完整内容；这是“允许 drop”的前提，必须有回归测试。

**回填/订阅顺序的落地方式：**

1. 先安装单入口订阅，但 worker 暂不启动；队列开始缓冲 live 事件。
2. 同步执行 EventStore 回填并 apply 到 encoder/scene。
3. 启动 worker，按队列顺序消费缓冲的 live 事件。
4. 用 EventEncoder 的 sequence/identity 幂等性吸收回填与 live 的重复；
   对无法幂等的状态事件补 `seen` 键（tool_call_id + 状态、session terminal）。
5. 回填期间队列达到容量上限时，优先保留终态/控制事件，只淘汰可重建 delta；
   不能因为回填慢而丢掉 `subagent.completed`。

### 4.2 `chatAgentLiveScreenSpec`

建议新增：

```go
func chatScreenAgentLiveSpec(session *ChatSession, target string) chatScreenSpec
```

属性：

```go
chatScreenSpec{
    ID:      "agents.live",
    Title:   "Agent: " + target,
    Kind:    chatScreenDocument,
    Doc:     placeholderDoc,      // 仅满足 validate；真正 fallback 懒加载
    Trigger: "command",
    RunDocument: runChatAgentLiveScreen,
}
```

`Doc` 不要在这里同步构建完整静态 transcript：那会为了一个未必使用的降级路径
重复读 EventStore，抵消 live 首屏优化。`Doc` 只放
`"Loading agent transcript..."` 之类的占位；projector 初始化失败时，在
`runChatAgentLiveScreen` 内再调用 `buildChatAgentTranscriptView` 构建 fallback，
或直接用 `DebugOverlay` 展示错误与已缓存的静态行。

`DegradeDoc` 不能沿用占位符：框架在无副屏能力/租约忙/嵌套时会直接走
`chatScreenDegradeInline`，不会进入 `RunDocument`。因此必须给一个真实可读的
降级内容，二选一：

1. 复用现有 `buildChatAgentTranscriptView` 的静态正文（与旧行为一致，最稳）；
2. 给轻量说明：`live screen unavailable: busy/unavailable` +
   `请重试 /agent <target> snapshot`。不能显示“Loading”后停住，也不能空白。

`runChatAgentLiveScreen`：

1. 解析 target -> session ID。
2. 创建 `chatAgentLiveSession`，异步启动 binding/backfill；**不要**在
   `RunDocument` 里同步等待完整历史回填后才返回第一帧。
3. 立即启动 pager；`Snapshot()` 在尚未 ready 时返回 loading/binding 状态，
   回填完成后下一次 75ms tick 自动切到内容。
4. 如果初始化在 lease 前就确定失败（例如 target 不存在、EventStore 不可用）：
   用现有 `buildChatAgentTranscriptView` 的静态正文走
   `chatScreenDocumentRunner`；同时 header 标注 `live unavailable: ...`。
5. 成功后：

```go
ctx, cancel := context.WithCancel(context.Background())
defer cancel()
defer liveSession.Close()
return ui.RunTranscriptPagerWithLease(ctx, terminal, ui.TranscriptPagerOptions{
    Snapshot: liveSession.Snapshot,
    // 首期使用 pager 本地滚动状态：默认 FollowBottom。
}, lease)
```

注意：`RunTranscriptPagerWithLease` 在 `Snapshot` 模式下每 75ms 调用一次
`Snapshot()`；用户滚动由 pager 本地状态处理，不需要 UI actor 的 pager action。
这正好适合只读第二屏幕。

#### 4.2.1 target 未绑定、已结束、不可用（P0 修订）

`/agents` 列表同时合并 batch task 账本，因此用户可能选中一个
`TaskPending/TaskRunning` 但 `child_session_id` 尚未绑定的任务。此时不能直接
构造 live projector，也不能只给一句错误；需要让屏幕进入可恢复的 **binding**
状态。

建议在 `runChatAgentLiveScreen` 外包一层 `chatAgentLiveSession`：

```go
type chatAgentLiveSession struct {
    target string
    mu     sync.RWMutex
    agent  toolbroker.AgentStatusResult
    projector *chatAgentSceneProjector // nil until child session bound
    status chatAgentLiveStatus         // binding/running/ended/unavailable
    err    string
}

func (s *chatAgentLiveSession) Snapshot() ui.TranscriptPagerSnapshot
func (s *chatAgentLiveSession) StartBindingWatch()
func (s *chatAgentLiveSession) Close()
```

状态机：

```text
binding
  ├─ subagent.started / registry 出现 child session  -> running
  ├─ subagent.completed / task failed/canceled        -> ended (no live data)
  └─ timeout / no resolver                            -> unavailable -> static fallback

running
  ├─ child session_end / subagent.completed            -> ended
  └─ projector error                                  -> degraded -> static fallback/retry
```

具体行为：

- **binding**：pager 显示
  `waiting for child session binding: /root/... (task=t1 status=pending)`；
  `Snapshot()` 返回空 Transcript + `EmptyText` 等待提示，pager 持续刷新 header。
  - watcher 可以订阅 `subagent.started`/`subagent.completed`，并低频
    （例如 500ms）调用 `resolveChatAgentTarget` 读取 registry；一旦拿到
    session ID，创建 projector 并在锁内替换。
  - 绑定成功后不需要重开副屏，下一次 75ms tick 自然切到 live snapshot。
- **running**：正常 live 视图。
  - 以低频（建议 1s，而不是 75ms）读取一次 agent registry/session state，
    用于纠正漏掉的 `subagent.completed`：如果 registry 已 terminal 而 projector
    仍认为 running，则在同一 worker 内执行 finalize 并标记 ended。
  - 这个 reconcile 只读 registry，不订阅第二套事件，也不阻塞 pager tick。
- **ended**：保留 projector 最终 Snapshot（如果有历史事件）；如果没有历史事件，
  显示最终 status/失败原因。订阅可停止，不自动关闭屏幕。
- **unavailable**：projector 初始化失败、EventStore 不可用或 target 不存在；
  `Snapshot()` 返回一个带 `live unavailable: ...` header 和 `EmptyText`
  的静态视图，并提供 `r`/重新打开提示。绝不返回空白 pager。

这条状态机是方案 B 闭环的必要条件；否则“批量任务尚未绑定 child session”
会被误判成 target 不存在或直接打开失败。

### 4.3 `/agents` 列表与命令入口

首期：

- `/agents` 列表仍为静态选择快照；打开列表后新 spawn 的 agent 不实时插入。
  这符合“列表是选择入口”的定位，也避免引入列表刷新与选择下标漂移。
- 列表 `AfterClose` 选中 target 后：
  - unified interactive：打开 `chatScreenAgentLiveSpec`；
  - 非 unified / 无副屏能力：保持现有静态 transcript 输出。
- `/agent <target>`、`/agents view <target>`：
  - unified interactive 默认打开 live screen；
  - `limit=N` 解释为 live 回填/展示上限（默认 10000，显式值可更小），
    不再因为传了 limit 就意外关闭 live；
  - `snapshot` / `--snapshot` 显式强制静态 transcript；plain/JSON/headless
    保持静态；
  - 若为了兼容旧脚本必须保留“`limit=N` 即静态”的口径，则需要在命令帮助和
    `/agent` 文案中显式说明，并加一条兼容测试，不能让用户猜。
  - 现有 `chatAgentTranscriptMaxLimit = 2000` 是静态 transcript 的尾部事件
    上限；live backfill 的 10000 上限应使用独立常量
    `chatAgentSceneMaxBackfillEvents`，不要复用/悄悄改小静态上限。
- 移除 unified 路径的 `follow=unavailable` 文案；legacy popup 的 follow 行为保留。

命令注册表/帮助/补全也必须同步，否则检查器会出现“声明 inline、实际返回
Screen”的事实源漂移：

- `chat_runtime_command_registry.go` 中 `/agent` 当前是
  `runtimeModeInline`，而 structured 路径实际返回 ScreenDocument；需要改为
  `screen + read`（或为 live/snapshot 变体显式声明）。
- `/agents view|open|transcript` 需要登记为 screen read；`/agents view ... snapshot`
  仍是 screen read（只读静态页），不能落回 legacy inline 直写。
- `chat_slash_command_catalog.go`、`chat_slash_argument_completion.go` 更新
  `snapshot`/`live`/`limit` 文案，避免补全里出现未实现的参数。

后续可选：

- 列表状态刷新（`r` 或定时刷新），但不在首期阻塞。
- live screen 内增加 agent 切换键（例如 `[`/`]`）或返回列表；首期 Esc 回主屏即可。

### 4.4 Pager header / status

现有 `renderTranscriptPagerFrame` 的标题固定为 `"Transcript"`，不足以表达
“我在看哪个子 agent”。建议给 `TranscriptPagerSnapshot` 或其上层 View 增加：

```go
Title      string
StatusLine string
EmptyText  string
Truncated  bool
```

建议显示：

```text
Agent /root/session_xxx  status=running run=running tool=shell 01:23
```

注意：`runTranscriptPagerLoop` 现在只按 `model.Revision`、live tail 和 view state
判断是否重绘；如果只改 title/status/elapsed，内容 revision 没变就不会刷帧。
因此 pager model 必须增加 `HeaderRevision`（或把 title/status/emptyText 纳入
dirty 比较），否则“实时状态头”和 elapsed 计时不会更新。

状态来源：

- `AgentStatusResult` 的初始 status/path/session/agent_type/depth；
- `subagent.started` payload 中的 role/goal/child_agent_name（用于 header 的
  “这个子 agent 在做什么”摘要；子会话事件流通常没有独立的 user prompt 事件）；
- projector 从 `tool.started/finished` 推导当前工具；
- `subagent.completed` / `session_end` 推导 ended（completed/failed/canceled）；
- elapsed 由 projector 记录 startedAt，pager 在 75ms tick 时更新；
- pending approval/question 从最近事件与 agent 快照合并。

若不想扩展 `TranscriptPagerSnapshot`，也可以在 `TranscriptPagerOptions` 增加
`Title`/`Status func() string`；但 title 和状态在同一帧里保持一致性更简单，
因此建议把动态 header 放到 snapshot 里，由 projector 统一生成。

建议的首屏形态（Phase 1，plain row；Phase 2 再加样式）：

```text
Agent /root/session_20261106131025_dOOBEERM  status=running run=running tool=shell
Reasoning (streaming)
  ...child reasoning body...
Assistant (streaming)
  ...child answer...
Tool
  • Running cd ... && cargo test ...
  ...result preview / fold marker...
```

底部保留 pager 提示：

```text
bottom  123/456   j/k 滚动   PgUp/PgDn 翻页   e 展开全部   Esc 返回
```

首期不新增输入框，也不抢占审批输入；pending approval/question 只在 header
和正文末尾显示可复制命令（`/agents approve|deny|answer ...`）。

### 4.5 富呈现（Phase 2）

当前 pager 的 `Rows()` 仍是 plain text：

- `appendTranscriptPagerCellRows` 只取 `cell.Source`，加固定标签 `"Reasoning"`、
  `"Assistant"`、`"Tool"`；
- 不做 Markdown、reasoning divider、tool fold、active band 样式。

在进入 Phase 2 之前，Phase 1 还必须修一个基础显示问题：
`renderTranscriptPagerFrame` 目前对每行调用 `fitFullScreenText`，而
`fitFullScreenText` 会 `strings.Fields` 折叠连续空白。reasoning 的缩进、
代码块/工具输出中的连续空格会丢失，看起来“不像主 agent”。应改为
“按显示宽度截断 + 填充”的 preserving writer（保留普通空格），Phase 2 再接
ANSI-aware 版本。否则即使数据面正确，内容呈现仍不忠实。

另一个 Phase 1 必须处理的 UX 问题是 live tool cell：`TranscriptPagerModel.Rows`
对 live tail 调用 `appendTranscriptPagerCellRows(..., expanded=true)`，会把
当前工具的全部 `cell.Source` 展开。运行中的 shell/测试输出可能非常大，导致
`Rows()` 行数暴涨、75ms tick 变慢。live tool 应使用与主视图同源的
`BuildPreview`/head-tail fold（至少保留 head + 最近 N 行/字节），并在工具
结束后沿用折叠标记；不能因为“正在流式”就把完整输出铺满第二屏。

Phase 2 可选两条路线：

#### 路线 A：pager 增加 `render.Line` row provider（推荐）

- 新增导出 helper（或把现有 `structuredTranscriptScreenRows` /
  `reasoningScreenRows` 抽成 `ui.RenderTranscriptCellLines(cell, width, theme)`）。
- `TranscriptPagerRow` 增加 `Line render.Line`；`Text` 继续用于 anchor/position。
- `renderTranscriptPagerFrame` 对每行使用 `render.ANSIBackend` 编码，按
  `render.LineWidth` / `render.Truncate` 做 ANSI-safe 宽度处理，而不是
  `fitFullScreenText` 的 plain-text 假设。
- active tail 使用与主 agent 相同的 `reasoningProjectionLines` / Markdown tail。

#### 路线 B：为第二屏幕复用 `ComposeAppRenderFrame`

- 用 child projector 构造最小 `ui.AppState`：

```go
state := ui.AppState{
    Transcript: ui.NewTranscriptState(snapshot),
    Active:     active,
    SemanticActiveCellProjection: true,
    Geometry:   ui.GeometryState{Width: width, Height: height},
    Theme:      ui.CurrentThemeContext(),
    Bottom:     minimalBottomState,
}
frame := ui.ComposeAppRenderFrame(state)
```

- 写一个新的 frame pager，把 `AppRenderRow.Line` 直接编码到 ANSI；
- 优点：与主 agent 的 presenter 路径完全同源；
- 成本：需要新的 pager/scroll 逻辑或把 `TranscriptPager` 改为 frame source。

建议先走路线 A：改动小、复用现有 pager 的滚动/follow 语义；路线 B 作为后续
“完全同视觉”的收敛方向。

### 4.6 用户操作流与返回路径（P1 修订）

目标操作流：

```text
/agents
  -> Agent 列表（↑↓ 选择，Enter 查看）
     -> live Agent 视图
        -> j/k、PgUp/PgDn、g/G 滚动/跟随
        -> e 展开/收起工具折叠（pager 既有键）
        -> b 返回 Agent 列表（建议）
        -> Esc/q 返回主屏
```

首期最小闭环：

- `Esc/q` 关闭 live 视图回主屏；这与现有 pager 的关闭语义一致。
- header 明确显示 target、status、ended 和 `Esc 返回`；底部保留滚动提示。
- pending approval/question 在 header 和正文末尾显示可复制命令：

```text
[action] approve: /agents approve /root/child req-123
[action] deny:    /agents deny /root/child req-123
[action] answer:  /agents answer /root/child q-456 <text>
```

这是只读边界内的最小闭环：用户能看见阻塞点，也能复制命令解决，不会因为
“屏幕上没有任何可操作提示”而卡死。

如果 request_id/question_id 没有随 live/backfill 事件到达、registry 也没有
暴露对应 id，则不能伪造命令；退化为
`pending approval; run /agents panel or /debug supervision list to get request id`。
这比显示一个不可执行的命令更诚实。

P1 增强（建议在 Phase 1 后半或 Phase 1.5 完成）：

- **返回列表**：为 pager 增加 `b` 键，映射到
  `chatScreenOutcome{Result: chatScreenClosedBack}`（或在 `RunDocument` 返回
  专用 sentinel）；live spec 的 `AfterClose` 在 `Back` 时重新打开
  一份新构建的 `chatScreenAgentsListSpec`（不要复用旧 rows/旧下标快照）。
  这样用户可以连续查看多个 agent，而不是每次
  Esc 后重输 `/agents`。live spec 需要记录打开来源（list / direct command）；
  从直接 `/agent <target>` 进入时，`b` 可以回列表，也可以回主屏，但必须在
  footer 说清楚，不能出现“按 b 后不知道去了哪里”。
- **切换 agent**：在 live 视图内用 `[`/`]` 或 `n/p` 切换到列表相邻的 target；
  首期可不做，但设计上不要把 target 写死成不可替换的单例。
- **列表刷新**：列表页增加 `r 刷新` 或低频自动刷新（例如 1s），至少让新
  spawn 的 batch task 可见；如果 `FullScreenList` 不支持刷新，可用
  `RunScreen` 包装既有列表原语，不要为刷新另造一套选择器。刷新时以
  target/session ID 作为稳定键，不能用捕获时的 `agents` 下标直接映射，
  否则列表插入/删除后选中的是另一个 agent。
- **pending 交互**：若要在一个屏幕内直接 approve/deny/answer，需要明确的
  输入焦点和权限边界；这属于控制面写入，不应偷偷塞进只读 pager。
  首期只提供可复制命令，后续再评估专用 action bar。

可操作性判断：

- “查看一个正在运行的 agent”是顺手且闭环的。
- “查看另一个 agent”在首期需要 Esc + `/agents` + 选择；这是可接受的最小
  路径，但不是最佳路径，因此把 `b` 返回列表列为 P1。
- “处理审批”在首期是“看见 + 复制命令 + 退出执行”；安全但不完全顺手。
  如果需要完全闭环，必须在后续版本设计显式 action 输入，而不是复用聊天输入框。

## 5. 事件生命周期与顺序

### 5.1 时序

```text
open screen
  -> projector created
  -> subscribe async (live events buffered)
  -> backfill EventStore tail -> encoder -> scene
  -> worker starts; drain buffered live events
  -> steady state: event -> queue -> encoder -> mapper -> scene -> revision++
  -> pager tick 75ms: Snapshot() -> compare revision/live tail -> redraw
close screen
  -> projector.Close()
  -> unsubscribe
  -> stop worker / drain bounded
  -> release lease
```

### 5.2 去重与顺序

- `EventEncoder` 已经按 `sequence` 处理 assistant/reasoning delta 的重复、乱序和
  重传；child projector 必须继续使用它，不能自己按文本猜重复。
- tool 事件按 `tool_call_id` 生命周期合并；receipt 事件复用同一 tool cell。
- `assistant_stream_reset` 清空失败 attempt 的 partial stream（encoder 已有逻辑）。
- `session_end` / `subagent.completed` 只处理一次；重复终态不重复 finalize。
- 回填与 live 的重复由 encoder 幂等性吸收；如发现某类非流事件重复，补
  `seen` 集合而不是按文本比较。

### 5.3 背压

`EventBus.Publish` 是同步派发，第二屏幕绝不能在每个 delta 上阻塞发布者。
建议：

- 使用 `SubscribeAsync`，队列大小 2048。
- streaming delta 使用 DropOldest（丢的是可重建增量，终态 snapshot 会收敛）；
- `session_end` / `subagent.completed` / `assistant.message` / tool terminal
  使用 DropBlock（或独立高优先级队列），确保终态不丢。
- 记录 dropped/delivered 计数，在 `/debug` 或 header 降级提示中可见。
- 若复用主 bridge 的 `mergeStreamEvents`/`streamAccumulator`，必须保持它对
  reasoning `append/snapshot` 的语义一致，不能引入第二套文本合并规则。

### 5.4 终态收口与晚到事件

`subagent.completed` 与 `session_end` 不能作为“立即停止消费”的信号：

- `EventBus.Publish` 是同步的，scheduler 在 child loop 返回后发
  `subagent.completed`；正常情况下它排在所有 child 事件之后。
- projector 的 worker 仍应按队列顺序消费；到达 `subagent.completed` 时，
  之前的内容事件已经 apply 完毕。
- 在 worker 内执行 finalize（`FinalizeOpenStreams` / `FinalizeOpenToolCells`），
  然后标记 ended；不要从 EventBus 回调 goroutine 直接改 Scene。
- 允许一个短的有界 drain 窗口，处理已经入队但尚未消费的终态
  `assistant.message` / `tool.finished`；drain 后仍可保留 subscription，
  但忽略后续 stream delta，直到屏幕关闭。
- 若终态消息晚于 `subagent.completed` 到达，encoder 的 terminal correction
  仍可修正 final item；不要重新打开 mutable cell。

### 5.5 Scene 驻留与容量

- 首期回填上限建议 `chatAgentSceneMaxBackfillEvents = 10000`；达到上限时
  header 标注 `early history omitted`。
- live 阶段不无限追加历史窗口：保留当前 child run 的完整 Scene 即可；
  多次 run 的子会话可按 `subagent.started` 分段，或后续复用 scene trim 策略。
- 若 Scene cell 数量/行数超过阈值，优先保留最近 run 的 cell，并保证 pager
  anchor 指向的 cell 不被移除；否则 pager 需要显式标注截断。
- 需要在 `/debug` 或 header 暴露 `scene_cells`、`queue_depth`、`dropped_total`，
  便于判断“少显示”是数据丢失还是历史窗口裁剪。

## 6. 文件级改动建议

| 文件 | 改动 |
| --- | --- |
| `backend/cmd/aicli/commands/chat_agent_live_view.go`（新增） | `chatAgentSceneProjector`、`chatAgentLiveScreenSpec`、`runChatAgentLiveScreen`、child Scene 回填/订阅/关闭 |
| `backend/cmd/aicli/commands/chat_agent_screen.go` | `chatAgentListAfterCloseHandler` 改为打开 live spec；保留静态 fallback |
| `backend/cmd/aicli/commands/chat_agent_transcript.go` | 静态 builder 继续作为 fallback；解析 `snapshot`/`live` 语义；unified 输出文案更新 |
| `backend/cmd/aicli/commands/chat_screen_batch3.go` | 可新增 live spec 构造函数（或在 live view 文件中就近定义） |
| `backend/cmd/aicli/commands/chat_runtime_command_registry.go` | `/agent`、`/agents view` 声明为 screen+read，避免 inline/screen 事实源漂移 |
| `backend/cmd/aicli/commands/chat_slash_command_catalog.go` / `chat_slash_argument_completion.go` | 帮助/补全加入 `snapshot`/`limit` 语义，登记 live 视图说明 |
| `backend/cmd/aicli/ui/transcript_pager.go` | snapshot 增加 title/status/empty/truncated；Phase 2 增加 `render.Line` row provider |
| `backend/cmd/aicli/ui/app_screen_layout.go` / `app_render_frame.go` | Phase 2 导出 cell 级 rich line helper，或复用 `ComposeAppRenderFrame` |
| `backend/cmd/aicli/commands/chat_agent_live_view_test.go`（新增） | projector/命令/生命周期/去重/回填测试 |
| `backend/cmd/aicli/ui/transcript_pager_live_test.go`（新增或扩展） | follow-bottom、live tail、status header、resize、rich row 测试 |

## 7. 测试与验收

### 7.1 单元测试

1. projector 回填 + live：
   - 先写入历史 reasoning/assistant/tool 事件；
   - 启动 projector；
   - 在回填后发布 live delta；
   - `Snapshot()` 包含完整 reasoning 块、assistant delta、tool cell。
2. 回填/live 重复：
   - 同一 stream sequence 在回填和 live 各出现一次；
   - Scene 只产生一份内容。
3. 不同请求隔离：
   - 两个 stream/request 的 reasoning 不粘连；
   - 不同 child session 的事件被过滤。
4. 终态：
   - `subagent.completed`/`session_end` 后 `ended=true`，mutable cell 完成；
   - 迟到 delta 不重新打开 cell。
5. 重试：
   - `assistant_stream_reset` 清空 partial reasoning/assistant。
6. 错误回退：
   - 无 EventStore / target 不存在 / projector 初始化失败 -> 静态 transcript。
7. 单队列顺序：
   - reasoning/assistant/tool 事件交错发布，worker 按发布顺序消费；
   - 队列饱和时只淘汰 stream delta，`subagent.completed`/`assistant.message`
     不被淘汰。
8. 回填/实时重复：
   - 同一 stream sequence 与同一 tool_call_id + 状态在回填和 live 各出现一次；
   - Scene 只产生一份内容；非流事件补 `seen` 键后幂等。
9. 晚到终态：
   - `subagent.completed` 已入队后，`assistant.message`/`tool.finished` 仍在
     drain 窗口内到达；最终 Scene 包含终稿，active cell 已完成。
10. target binding：
    - pending batch task 没有 child session 时显示 binding；
    - `subagent.started` 或 registry 绑定后同一屏幕切到 live；
    - task failed/canceled 无 child session 时显示 ended/失败原因；
    - 人为丢弃 `subagent.completed` 后，registry terminal reconcile 仍会把
      projector 标记 ended。
11. 降级可见：
    - projector error / EventStore 不可用 / 队列 drop 超阈值时 header 有
      `live degraded`，屏幕不空白。

### 7.2 集成测试

1. `/agents` -> 选择 target -> `chatScreenSpec.ID == "agents.live"`；
2. fake `RunDocument` 捕获 `ui.TranscriptPagerOptions`，断言 `Snapshot != nil`；
3. Esc 关闭后 subscription 退订、worker 退出、无父会话写入；
4. `snapshot` / plain / JSON 仍走静态 transcript；`limit=N` 在 live 模式控制
   回填上限而不关闭 live；
5. 父 Scene/History 不出现 child 正文/思考/工具行。
6. pending batch task -> binding -> live 的入口闭环；
7. 如果实现 `b` 返回列表：Esc 回主屏、`b` 回列表、列表再次选中另一个
   target 后能打开新的 projector；
8. 子 agent 完成后再次打开：从 EventStore 回填最终 Scene 并显示 ended，
   不建立无意义的 live subscription。
9. 命令注册表/帮助/补全守卫：`/agent`、`/agents view` 的 screen+read
   声明与实际返回值一致；`snapshot`/`limit` 文案和补全存在。

### 7.3 UI 测试

- pager 默认 FollowBottom；新 snapshot 增长时 offset 跟随底部；
- pager 首帧在 projector 尚未回填完成时显示 loading/binding，不出现空白等待；
- 用户向上滚动后，新事件不改变阅读锚点；
- `g/G`、j/k、PgUp/PgDn 行为与主 transcript pager 一致；
- header 显示 target/status/ended/truncated；
- 仅 header/status/elapsed 变化（content revision 不变）时仍会重绘；
- reasoning/代码行中的连续空格与前导缩进不被 pager 输出层折叠；
- live tool 输出 1MB 时不整段展开；行数/字节有界且带 fold 提示；
- resize 后行宽与 scroll anchor 稳定；
- Phase 2：ANSI 行宽不超过终端宽度；Markdown/reasoning divider/tool fold 与主视图一致。

### 7.4 性能/稳定性

- 10k reasoning delta 的 projector 回填与 live 消费不阻塞 LLM 发布者；
- 75ms pager tick 不重复全量编码；projector 的 `Snapshot()` 按 revision
  缓存/克隆，必要时再给 pager 增加 revision gate（当前 pager 每 tick 会
  `NewTranscriptPagerModel`，首期子 agent 规模下可接受）；
- 队列丢弃计数可观测；最大回填事件数有界；
- 关闭屏幕后 goroutine/subscription 数量回落。

### 7.5 验收标准

- 运行中的子 agent 在 unified `/agents` 中选中后，第二屏幕能在一次 75ms tick
  内看到新 reasoning/assistant/tool 行。
- reasoning 按逻辑块聚合，不因 transport delta 重复 `[reasoning]`。
- tool running -> finished 生命周期可见，工具折叠与主视图同语义。
- 子 agent 结束后 header 显示 ended/completed/failed，保留最终快照。
- 关闭屏幕后不收到该 session 的后续事件，父主屏不受影响。
- 无副屏/旧终端/plain/JSON 路径行为保持静态快照，不回归。

## 8. 分批实施建议

### Phase 0（1–2 天）：live 数据面骨架

- 新增 `chatAgentSceneProjector`。
- 完成单入口有序队列（不是多条独立 async subscription）、EventStore 回填、
  订阅先于回填、encoder/scene/mapper、Snapshot。
- 落地 class-based 背压：终态不丢，delta 可合并/淘汰，snapshot 可收敛。
- 用单元测试锁定回填/live/顺序/重复/终态 drain/队列饱和。

### Phase 1（1–2 天）：第二屏幕接入

- 新增 `chatAgentLiveScreenSpec` + `RunDocument`。
- 新增 `chatAgentLiveSession` binding/running/ended/unavailable 状态机，
  支持 pending batch task 的 child session 绑定。
- `/agents` 选择、`/agent <target>` unified 入口切换到 live screen。
- 复用 `RunTranscriptPagerWithLease`，默认 follow-bottom。
- 修正 pager 输出行的空白折叠（`fitFullScreenText` -> preserving writer），
  保证 reasoning/代码缩进忠实。
- 静态 transcript 作为 fallback；显式 `snapshot` 保持静态；`limit=N` 控制
  live 回填上限。
- header 增加 target/status/ended。
- 完成 P0 集成测试与真实 ConPTY E2E。
- 视时间加入 `b` 返回列表；若不做，必须在 header/footer 明确
  “Esc 返回主屏，重新 /agents 切换”。

### Phase 2（2–3 天）：富呈现收敛

- pager 增加 `render.Line` row provider，或复用 `ComposeAppRenderFrame`。
- Markdown、reasoning divider、tool fold、active band、状态行与主 agent 对齐。
- ANSI-safe 宽度/截断/scroll anchor 测试。

### Phase 3（可选）：列表与多视图

- `/agents` 列表定时/按键刷新；
- live screen 内 agent 切换或返回列表；
- 多 agent 并排/网格不在首期。

## 9. 风险与待决问题

| 风险/问题 | 说明 | 建议 |
| --- | --- | --- |
| 回填与 live 竞态 | 订阅/回填顺序错误会丢事件或重复 | 先订阅后回填；encoder 幂等；回归测试锁定 |
| 发布者被阻塞 | `Bus.Publish` 同步派发 | `SubscribeAsync` + 有界队列 + delta 合并 |
| 终态事件丢失 | DropOldest 可能丢 session_end | 终态/控制事件独立高优先级或 DropBlock |
| child 没有 session_end | scheduler 只发 `subagent.completed` | projector 同时处理 `subagent.completed` 并 finalize |
| Scene 过大 | 长子会话 reasoning 事件多 | 回填上限 10k、snapshot revision 缓存、必要时 trim/窗口 |
| pager plain 限制 | 无 Markdown/tool 富样式 | Phase 1 先保证 live tool 预览折叠与实时/顺序；Phase 2 富 row provider |
| 列表静态 | 新 spawn 不出现 | 首期接受；Phase 3 加列表刷新 |
| 审批/提问 | 只读屏幕无法直接回答 | 首期显示状态与 `/agents approve/answer` 提示；交互后续评估 |
| projector 复用主 bridge | 主 bridge 依赖 UI actor/日志/审批，过重 | 首期独立轻量 projector；后续抽取共享 render-plane |

## 10. 代码索引

### 10.1 现有命令/屏幕

- `backend/cmd/aicli/commands/chat_debug.go:287` — `/agents` structured dispatch
- `backend/cmd/aicli/commands/chat_agent_screen.go:22` — `/agents` list spec
- `backend/cmd/aicli/commands/chat_agent_screen.go:125` — selection -> transcript
- `backend/cmd/aicli/commands/chat_agent_transcript.go:118` — structured transcript
- `backend/cmd/aicli/commands/chat_agent_transcript.go:479` — static view builder
- `backend/cmd/aicli/commands/chat_screen_batch3.go:93` — static transcript spec
- `backend/cmd/aicli/commands/chat_screen_framework.go:140` — `chatScreenSpec`
- `backend/cmd/aicli/commands/chat_screen_framework.go:483` — screen run
- `backend/cmd/aicli/commands/chat_screen_framework.go:761` — document runner

### 10.2 实时数据面

- `backend/cmd/aicli/commands/chat_runtime_events.go:45` — bridge fields
- `chat_runtime_events.go:842` — bridge constructor (`EventEncoder`/`Scene`)
- `chat_runtime_events.go:3462` — encode event -> ChangeSet -> Scene
- `chat_runtime_events.go:9580` — foreign child content filter
- `backend/cmd/aicli/ui/render/encoding/encoder.go:75` — reasoning operation classifier
- `encoder.go:2263` / `2291` — `FinalizeOpenStreams` / `FinalizeOpenToolCells`
- `backend/internal/agent/child_factory.go:187` — child shares parent EventBus
- `backend/internal/agent/scheduler.go:1167` / `1370` — subagent started/completed
- `backend/internal/events/bus.go:323` — synchronous Publish
- `backend/internal/events/async.go:98` — async subscription/backpressure

### 10.3 第二屏幕/presenter

- `backend/cmd/aicli/ui/debug_overlay.go:26` — refresh callback
- `backend/cmd/aicli/ui/debug_overlay.go:58` — 75ms poll
- `backend/cmd/aicli/ui/transcript_pager.go:17` — pager snapshot
- `backend/cmd/aicli/ui/transcript_pager.go:95` — pager model / live tail
- `backend/cmd/aicli/ui/transcript_pager.go:173` — plain rows
- `backend/cmd/aicli/ui/transcript_pager.go:492` — pager options
- `backend/cmd/aicli/ui/transcript_pager.go:611` — pager live loop
- `backend/cmd/aicli/ui/transcript_pager.go:432` — pager frame
- `backend/cmd/aicli/ui/app_screen_layout.go:69` — LayoutAppScreen
- `backend/cmd/aicli/ui/app_render_frame.go:45` — ComposeAppRenderFrame
- `backend/cmd/aicli/ui/app_state.go:390` — ActiveCellFromSnapshot

## 11. 最终建议

先把“同一套事件语义”做实，再追求“同一套视觉”：

1. **Phase 1 必须完成**：child-scoped `EventEncoder + Scene` projector，实时
   EventBus 订阅，EventStore 回填，pager live snapshot，follow-bottom，终态收口，
   静态 fallback。
2. **Phase 2 再做**：把 pager row 从 plain text 升级为 `render.Line` 富呈现，
   复用主 agent 的 Markdown/reasoning/tool fold/active band。
3. **不要**让父 bridge 直接渲染子事件，也不要在第二屏幕里新造一套 reasoning /
   assistant 文本合并规则；这两条会分别破坏隔离和顺序单源。

## 12. 方案 B 闭环审查清单

### 12.1 Go / No-Go

**结论：方向 Go；当前文档按原样实现为 No-Go。**

补齐 §4.1.4、§4.2.1、§5.4 的 P0 项，并按 §4.6/§12 的 Phase 1 最小 UX 清单
完成交互后，才建议进入 Phase 0/1 的合并评审。
原因是这些不是“后续优化”，而是会让实时视图出现丢事件、错序、打不开或
无法结束的正确性问题。

按目标维度拆开看：

| 维度 | Phase 1 能否闭环 | 说明 |
| --- | --- | --- |
| 实时数据/顺序 | 可以 | 单队列 + encoder/Scene + snapshot 收敛 |
| 生命周期/失败恢复 | 可以 | binding/running/ended/unavailable + fallback |
| 基本查看操作 | 可以 | `/agents` -> live，滚动/follow/Esc |
| 连续切换 agent | 需要 P1 | `b` 返回列表或切换键 |
| 审批/提问处理 | 部分闭环 | 首期“看见 + 复制命令 + 退出执行” |
| 视觉完全像主 agent | Phase 2 | Phase 1 是 plain row semantic view，不是 Markdown/active band 同视觉 |

### 12.2 最小 Phase 1 DoD

必须全部满足：

- [ ] child-scoped `EventEncoder + Scene` projector，父 Scene 零污染。
- [ ] 单入口有序队列；终态事件不被 delta 淘汰。
- [ ] 先订阅后回填；回填/live 重复由 encoder/seen 键吸收。
- [ ] `subagent.completed`/`session_end` 在 worker 内 finalize；晚到终态可修正。
- [ ] pending batch task 的 binding -> running -> ended 状态机可工作。
- [ ] live projector 失败/EventStore 不可用时回到静态 transcript，用户可见原因。
- [ ] `TranscriptPager` live snapshot、follow-bottom、滚动 anchor、ended header。
- [ ] pending approval/question 在屏幕上给出可复制命令。
- [ ] Esc 关闭后退订/停 worker；无 subscription/goroutine 泄漏。
- [ ] 单元/集成/E2E 覆盖：顺序、重复、队列饱和、binding、终态、降级、父隔离。

### 12.3 用户操作体验判断

| 用户目标 | 首期体验 | 判断 |
| --- | --- | --- |
| 查看一个运行中的子 agent | `/agents` -> 选中 -> 实时第二屏 | 顺手，闭环 |
| 观察 reasoning/assistant/tool 流 | 75ms 刷新、follow-bottom、滚动/follow | 顺手 |
| 子 agent 结束后查看最终结果 | ended header + 最终 Scene | 顺手 |
| 查看另一个子 agent | Esc -> `/agents` -> 再选择 | 可用，但多一步；建议 P1 加 `b` 返回列表 |
| 处理子 agent 审批/提问 | 屏幕显示命令，退出后执行 | 安全但不完全顺手；首期可接受 |
| 列表里新 spawn 的 agent | 需要重新打开 `/agents` | 首期可接受；P1 加列表刷新 |

### 12.4 不建议的做法

- 不要用 `DebugOverlay.Refresh` 直接重读 EventStore 并重建纯文本：会重复
  解析、没有 follow-bottom、也绕开了统一 Scene。
- 不要为 reasoning/assistant/tool 各建一条独立 `SubscribeAsync`：跨类型顺序
  会丢。
- 不要把 `subagent.completed` 当作“立刻停止消费”：终态/晚到消息需要在 worker
  内按顺序收口。
- 不要在第二屏幕里直接写子会话 history 或复用父 bridge 的 primary 状态。
- 不要为了“能 approve”而把写操作塞进只读 pager 的隐式快捷键；需要独立、
  明确的 action 设计。
