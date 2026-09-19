# Reasonix 终端 TUI 架构与组件分析

**调研对象：** `E:\projects\ai\DeepSeek-Reasonix`（Go 单仓，模块 `reasonix`）
**分析日期：** 2026-09-19
**方法：** 全程只读（`view` / `grep` / `ls`），未修改被调研仓库、未运行构建或测试；结论均附 `file:line`。
**证据口径：** 行号基于调研当日工作区快照；两路并行调研（TUI 表现层 / TUI↔运行时边界）+ 主调研员交叉复核。

---

## 0. 结论摘要（TL;DR）

| # | 结论 | 关键证据 |
|---|------|----------|
| S1 | TUI 只是单仓多前端之一；与运行时之间是**接口边界**，不是具体类型耦合 | `internal/cli/chat_tui.go:50-51`；`internal/control/port.go:246-259` |
| S2 | 全包**唯一** `tea.Model` 是 `chatTUI`；所有 overlay 都是内嵌状态机，不是独立 Model | `chat_tui.go:901,915,3432`；`chat_tui.go:1351-1452` |
| S3 | 运行时→TUI 是**单向事件 channel 泵**（1024 缓冲 + tea.Cmd 拉取 + 批量 drain） | `cli.go:1117-1121`；`chat_tui.go:1866-1896,4640-4642` |
| S4 | 审批/提问是"事件 + 阻塞 reply channel"的**双向握手** | `controller.go:6159,6214,6223-6229`；`chat_tui.go:4577-4582,3295` |
| S5 | 渲染是**双模式**（alt-screen viewport / Termux 原生 scrollback）+ **双轨 transcript** | `chat_tui.go:46-49,2152-2165`；`transcript.go:19-44` |
| S6 | Markdown 为**自研 ANSI 渲染器**（goldmark 解析），代码块无语法高亮，diff 才用 chroma | `md.go:24-47,309-319`；`diffview.go:248` |
| S7 | 会话持久化走 `agent.Session` + `Controller.Resume`，TUI **不直接依赖** `internal/store` | `agent/save.go:194,201`；`controller.go:3522-3564` |

---

## 一、总体定位：单仓多前端，TUI 是一个"表现层"

Reasonix 是 Go 单仓（`go.mod:1`，模块 `reasonix`），TUI 代码位于 `internal/cli`（约 217 个文件，含测试）。仓库用 lint 强制分层（`tools/repolint/layers.go:13-20`，规则文档 `REASONIX.md:11-17`）：

```
hosts      cmd/ · desktop/(Wails)
frontends  internal/{cli, serve, acp, bot, botruntime, boot}   ← TUI 在这里
control    internal/control   (仅 frontends/hosts 可 import)
agent/tool internal/agent · internal/tool
event      internal/event      (线协议式事件枚举)
store      internal/store      (底层存储，TUI 不直接依赖)
```

- frontend 之间**禁止互相 import**；utility 包不得 import 任何 `reasonix/` 包（`layers.go:24-48`）。
- 同一套 `control.Controller`（`internal/control/controller.go`）同时驱动三端：终端 TUI、HTTP+SSE `internal/serve`、Wails 桌面 `desktop/`（`port.go:243-245,264-277`）。
- `desktop/tabs.go:1543-1575` 的 `tabEventSink` 与 TUI 的 `eventSink` 是同一 `event.Sink` 契约的两种实现。

---

## 二、框架与依赖栈（表现层选型）

| 依赖 | 版本 | 用途 | 证据 |
|---|---|---|---|
| `charm.land/bubbletea/v2` | v2.0.8 | Elm 架构运行时 | `go.mod:9` |
| `charm.land/bubbles/v2` | v2.1.1 | `key` / `textarea`（composer）/ `viewport`（transcript）/ `spinner` | `chat_tui.go:17,75,84,251` |
| `charm.land/lipgloss/v2` | v2.0.5 | 盒/边框/面板样式 | `chat_tui.go:22,2332-2342` |
| `charmbracelet/x/ansi` | v0.11.7 | 宽度测量与硬换行 | `box.go:9-16`；`chat_tui.go:2226-2234` |
| `go-runewidth` + `rivo/uniseg` | v0.0.27 / v0.4.7 | composer 字素簇级视觉布局 | `composer_selection.go:11-12,73,271` |
| `yuin/goldmark` | v1.8.5 | Markdown 解析 | `md.go:9-15` |
| `alecthomas/chroma/v2` | v2.27.0 | **仅** diff 高亮 | `diffview.go:12-15,248` |
| `colorprofile` / `atotto/clipboard` | v0.4.3 / v0.1.4 | 色彩能力探测 / 剪贴板 | `style.go:13-28`；`transcript.go:366-382` |

选型要点：Bubble Tea v2 全线（含 Bubbles/Lip Gloss v2），未引入第三方 Markdown 终端渲染库，Markdown/LaTeX 渲染为自研。

---

## 三、架构核心 1：唯一 tea.Model = `chatTUI`

全包非测试代码中实现 `Init()/Update()/View()` 的类型**只有** `chatTUI`（`internal/cli/chat_tui.go:901,915,3432`，文件约 5167 行）：

- 构造：`newChatTUI(ctrl control.SessionAPI, missing string, eventCh chan event.Event, termW int)`（`chat_tui.go:618`），调用点 `cli.go:1212`。
- 状态机极简：`tuiState` 只有 `tuiIdle`/`tuiRunning`（`chat_tui.go:426-431`），其余全部是字段级 overlay 状态。
- `Init`（`901-909`）= `tea.Batch(textarea.Blink, waitForAgentEvent, fetchBalance, runStatusline, refreshGitStatus)`。

**所有 overlay 都不是独立 tea.Model**，而是"内嵌状态机结构体 + `handle*Key` / `render*` 方法"：

| overlay | 文件 | 说明 |
|---|---|---|
| chooser | `chooser.go` | `ask` 工具多选题卡片（`chat_tui.go:293-295`） |
| rewind | `rewind.go` | 三阶段回滚选择 |
| mcpManager | `mcp_manager.go` + `_view`/`_actions` | 7 阶段状态机 |
| mcpImport | `mcp_import_picker.go` | MCP 导入选择 |
| skillPicker | `skill_picker.go` + `_view` | 技能选择 |
| resumePicker | `resume_picker.go` | 会话恢复选择 |
| quickPicker | `quick_picker.go` | 快捷选择 |
| copyPicker | `copy_picker.go` | 复制目标选择 |
| clearConfirm | `clear_confirm.go` | 清屏确认 |
| completion | `complete.go` | slash / `@` 引用补全 |

键分发优先级链：`chat_tui.go:1351-1452`（overlay 依次抢占按键）。

**代价（代码注释显式标注）：** 新增面板必须同步三处——`hideComposer()`（`chat_tui.go:2306`）、`bottomRows()`（`2256`）、`computeStatusLineCount()`（`4014`）；注释称其为 "load-bearing invariant"（`chat_tui.go:2294-2305,4008-4013`）。

---

## 四、架构核心 2：TUI ↔ 运行时是"接口 + 事件流"边界

### 4.1 依赖是接口，不是具体类型

- `chatTUI.ctrl control.SessionAPI`（`chat_tui.go:50-51`）；内部嵌套结构同样持有 `ctrl`/`oldCtrl`（`chat_tui.go:555-559`）。
- `SessionAPI` 由 12 个子端口组合：`Lifecycle` / `TurnControl` / `Approvals` / `Goals` / `SessionHistory` / `MemoryControl` / `Capabilities` / `Status` / `SessionPersistence` / `Input` / `Settings` / `Inbox`（`internal/control/port.go:246-259`）。
- 子端口分文件段定义：`Lifecycle`（`port.go:34-45`）、`TurnControl`（`49-72`）、`Approvals`（`76-96`）、`Goals`（`99-107`）、`Settings`（`237-241`）。
- 13 行编译期断言防漂移：`_ Lifecycle = (*Controller)(nil)` … `_ SessionAPI = (*Controller)(nil)`（`port.go:264-277`）。
- 动机（包注释原文要点）：具体 `*Controller` 有约 99 个方法，各前端只依赖自己使用的子端口（接口隔离），这些子端口也是后续拆分 Controller 的分解边界（`port.go:22-30`）。

### 4.2 一轮对话的调用链

```
TUI      startTurn (chat_tui.go:4274)
           → startTurnWithRaw (4281) → startControllerTurn (4289)
           └─ m.ctrl.SendWithRaw(sent, raw)                ← 唯一接口调用点
control  SendWithRaw (controller.go:1022) → runGuarded (admission_guard.go:24)
           → runTurn (controller.go:1067-1075) → runGoalLoopWithRawDisplay (1099-1103)
           → turnOrchestrator.runGoalLoopWithRawDisplay (turn_orchestrator.go:78,289)
agent    Agent.Run (agent.go:1402) → executeBatch (execute_batch.go:60)
           → executeOne (execute_one.go:67) → tool.Tool.Execute (tool.go:21-31)
```

- TUI 侧忙碌判定：`m.ctrl.RuntimeStatus()` 叠加 `m.pendingApproval != nil || m.chooser != nil`（`chat_tui.go:445-446`）。
- headless 路径（非 TUI）：`Controller.Run`（`controller.go:1929`），runner 调用点 `controller.go:1973`。
- 取消：`Cancel`（`controller.go:2024`）；中途 steer：接口 `port.go:64`，事件 `event.Steer`（`event.go:92`）。

### 4.3 事件流：单向 channel 泵（`internal/event`）

- `event.Kind` 枚举 24 种：`TurnStarted` / `Reasoning` / `Text` / `Message` / `ToolDispatch` / `ToolResult` / `Usage` / `Notice` / `Phase` / `ApprovalRequest` / `AskRequest` / `TurnDone` / `CompactionStarted` / `CompactionDone` / `ToolProgress` / `MCPSurfaceReady` / `Retrying` / `Steer` / `GuardianAssessment` / `ExtensionSurface` / `ExtensionStatus` / `StreamAttempt` / `ContextMaintenanceEvent` / `WorkspaceChanged`（`event/event.go:23-119`）。
- **线协议约束**：Kind 数值是线协议的一部分，新 Kind 只能插在 `KindCount` 哨兵上方（`event.go:75,80,85,98,103,108` 注释反复强调 "appended last to keep the Kind values before it wire-stable"）。
- 生产者：`event.Sink` 接口（`event.go:831-833`，注释要求 channel 型 sink 必须有缓冲或活跃读者）→ agent `SetSink`（`agent.go:963-973`）→ TUI 侧 `eventSink.Emit = s.ch <- e`（`chat_tui.go:5462-5469`）。
- 缓冲装配：`eventCh := make(chan event.Event, 1024)` + 装饰器链 `withNotifications` → `reporter.Wrap`（`cli.go:1117-1121`）。
- 消费者：`waitForAgentEvent(ch)` 返回 `tea.Cmd`，把 `<-ch` 转成 `agentEventMsg`（`chat_tui.go:449-450,4640-4642`）。
- 消费循环：`Update` 的 `case agentEventMsg` → `drain:` 标签下非阻塞批量吸收（上限 `maxEventDrain=512`，注释 `chat_tui.go:452-453`）→ `ingestEvent`（`4352`）→ 处理完重新挂起 `waitForAgentEvent`（`1896`）。
- **单读者约束**：`chat_tui.go:1987-1991` 注释明确禁止在别处重复发起 `waitForAgentEvent`——两个 goroutine 竞争同一 channel 会导致流式文本乱序。
- 端到端时序：

```
Agent.Run (agent.go:1402)
  └─ executeBatch (execute_batch.go:60) → executeOne (execute_one.go:67) → tool.Tool.Execute (tool.go:28)
       ├─ Emit ToolDispatch    (agent.go:2419/2436；流式 partial: 2189/2205)
       ├─ Emit ToolProgress    (execute_one.go:703-706，经 tool.WithProgress)
       └─ Emit ToolResult      (execute_batch.go:361；截断 Notice :363)
Controller 转发/包装 (reporter.Wrap, cli.go:1121)
  └─ eventSink.Emit → eventCh(1024)   (chat_tui.go:5465-5469；cli.go:1117)
       └─ waitForAgentEvent (tea.Cmd) → agentEventMsg (chat_tui.go:4640-4642)
            └─ Update case agentEventMsg → drain → ingestEvent (chat_tui.go:1866-1896, 4352)
```

- 子代理事件经 `nested_sink` 重写父 ID（`internal/agent/nested_sink.go:20-24`）；`task` 工具的 dispatch/result 在 `internal/agent/fleet.go:328/345/355`、`parallel_tasks.go:173/202/209`。
- 旁路消费者：桌面端 `tabEventSink`（`desktop/tabs.go:1543-1575`）。

### 4.4 审批 / 提问：事件 + 阻塞 reply channel 的双向握手

**审批：**

1. `Controller.requestApprovalDecisionWithOptions`（`controller.go:6159`）
2. 注册 pending：`approvalManager.registerDecisionKindWithInput`（`internal/control/approval.go:258-274`）
3. `c.sink.Emit(c.approvalRequestEvent(...))`（`controller.go:6214`，事件构造 `6232-6234`，`Kind: event.ApprovalRequest`）
4. **阻塞等待**：`select { case r := <-reply: ...; case <-waitCtx.Done(): c.approval.cancel(id) }`（`controller.go:6223-6229`）
5. TUI 侧：`ingestEvent` 把 `e.Approval` 存进 `m.pendingApproval`（`chat_tui.go:4577-4582`）；按键模态路由到 `handleApprovalKey`（`chat_tui.go:1415-1418,3275`）；最终 `m.ctrl.Approve(id, allow, session, persist)`（`chat_tui.go:3295`）；恢复卡走 `m.ctrl.ResolveRecovery`（`3287`）
6. 控制面回执：`Approve`（`controller.go:2108-2150`，`pending.reply <- approvalReply{...}` `:2149`）；`ResolvePlanDecision`（`:2154-2175`）；决策回执通知 `recordDecisionReceipt`（`:2177-2205`）
7. 交互模式开关：`EnableInteractiveApproval`（`controller.go:2212`），TUI 启动时调用（`cli.go:1204`）

**`ask` 工具（同构）：** `event.AskRequest` 发射点 `controller.go:2495,2632`；回传 `AnswerQuestion`（`controller.go:2512-2529`）；TUI 卡片字段 `chooser`（`chat_tui.go:293-295`）。

### 4.5 会话持久化 / 恢复

- 落盘：`Session.Save`（`internal/agent/save.go:194`）、`Session.SaveSnapshot`（`save.go:201`）。
- 恢复入口：`agent.LoadSession`（`cli.go:1156`）→ `ctrl.Resume(loaded, resumePath)`（`cli.go:1161`）；chat 路径 `cli.go:724`；web/HTTP 路径 `cli.go:949-953`；`ctrl.EnsureSessionPath()`（`cli.go:1163`）。
- 控制面：`Controller.Resume`（`controller.go:3522-3564`）、`Snapshot`（`:3637-3661`）、`SessionPath`（`:4738`）、`EnsureSessionPath`（`internal/control/sessionpath.go:15`）。
- TUI 选择器：`recentSessions`（`internal/cli/resume.go:15-23`，经 `agent.ListSessions`）；`openResumePicker`（`resume_picker.go:27-33`，`m.ctrl.SessionDir()` + `reclaimCLIRecoveryBranches`）；分支重放 `branch.go:117-131`。
- **TUI 不直接依赖 `internal/store`**：`chat_tui.go` 无 store 导入；store 的导入面为 `internal/agent/save.go:27`、`internal/serve/serve.go:33`、`desktop/app.go:58`、`desktop/tabs.go:35`、`internal/cli/session_machine.go:23`、`internal/cli/web_runtime.go:21`。
- `internal/history` 不是 resume 入口，而是给模型用的只读历史检索工具（`internal/history/tool.go:16-27`）。

---

## 五、渲染管线（表现层）

### 5.1 渲染双模式（终端兼容性设计）

- **默认**：alt-screen + `viewport.Model` 自绘 transcript（`View` 尾部分支 `chat_tui.go:3563-3579`）。
- **Termux 例外**：留在 normal buffer，把已定稿行用 `tea.Println` 写入终端原生 scrollback，使触摸滚动/软键盘仍可用（`chat_tui.go:46-49,64-66`；`finalize` `2152-2165`；`prepareNativeScrollback` `cli.go:1273-1285`）；此模式下鼠标捕获默认关闭（`mouse_reenable.go:33-39`）。

### 5.2 transcript 双轨结构（性能护栏）

- `[]string transcript`（每帧渲染快路径）+ `[]transcriptSource`（语义源，9 种 kind：`Fixed` / `Markdown` / `User` / `Reasoning` / `ToolCard` / `Banner` / `ReplayBundle` / `TurnReceipt` / `SubagentProgress`，`transcript.go:19-44`）。
- 宽度变化时 `reflowTranscript` 重放全部 source（`transcript.go:226-235`），配增量换行缓存（`wrap_cache.go:5-27`）。

### 5.3 Markdown / 数学 / diff

- **自研 ANSI 渲染器**：goldmark 解析 + 手写块/行内渲染（`md.go:24-47,193-331,340-401`）。
- 围栏代码块**不做**语法高亮，只加 accent 着色（`md.go:309-319`）。
- 数学公式：自定义 inline parser（`mathnode.go:13,23`）+ LaTeX→Unicode（`latex.go:8-45`）。
- diff 高亮用 chroma（`diffview.go:42-48,248`）——chroma 全仓仅此一处使用。

### 5.4 高度 / 布局链

- `transcriptHeight()`（`chat_tui.go:2315`）= `height - bottomRows()`（`2256`）。
- `computeStatusLineCount()`（`4014`）复刻 View 的换行行数（与 `bottomRows` 的三处同步约束见 §3）。
- `inputHeightLimit()`（`4071`，`maxInputRows=8`；`4047` 附近为相关常量）。

### 5.5 主题与样式

- 全局可变单例 `activeCLITheme` + `refreshCLIStyles()`（`style.go:9-52`；`theme.go:57,473`）。
- 主题切换是 `themeSweep` 18 帧擦除动画（`theme_sweep.go:11-70`）；因样式是全局的，动画必须整帧预渲染两次；运行中切换直接放弃（`theme_sweep.go:37`）。

---

## 六、组件清单（`internal/cli` 内部）

| 类别 | 组件（文件） | 说明 |
|---|---|---|
| overlay（内嵌状态机） | `chooser.go` | `ask` 多选题卡片 |
| | `rewind.go` | 三阶段回滚 |
| | `mcp_manager.go` + `_view` / `_actions` | MCP 管理，7 阶段状态机 |
| | `mcp_import_picker.go` | MCP 导入选择 |
| | `skill_picker.go` + `_view` | 技能选择 |
| | `resume_picker.go` | 会话恢复选择 |
| | `quick_picker.go` | 快捷选择 |
| | `copy_picker.go` | 复制目标选择 |
| | `clear_confirm.go` | 清屏确认 |
| | `complete.go` | slash / `@` 引用补全（`482-547`） |
| 输入层扩展 | `chat_tui_paste.go` | 折叠粘贴块（≥1000 字符 / ≥5 行）、图片附件、剪贴板 |
| | `composer_selection.go` | 应用内选择、字素簇视觉布局、滚轮/滚动条（`11-12,73,271`） |
| | `chat_tui_inbox_keys.go` + `inbox_queue.go` | 运行中消息队列（inbox） |
| 渲染支撑 | `status_footer.go` | 状态行 / 回合回执 |
| | `toolcard.go` | 工具调用卡片 |
| | `receipt_card.go` | 决策回执卡片 |
| | `diffview.go` | diff 视图（chroma） |
| | `box.go` | 宽度原语（`9-16`） |
| | `scroll_state.go` | tail-follow 滚动状态机 |
| | `wrap_cache.go` | 增量换行缓存 |
| | `view_helpers.go` | 视图辅助 |
| | `theme_sweep.go` | 主题切换动画 |
| 系统集成 | `gitstatus.go` | git 状态轮询（700ms 超时） |
| | `mouse_reenable.go` | ConPTY 鼠标失效重启用（`33-39`） |
| | `tui_diagnostics.go` + `chat_tui_watchdog.go` | 四相 watchdog、10s 停滞判定 |
| 非 Bubble Tea 交互 | `select.go` | raw-mode 单选菜单 |
| | `setup_manager.go` / `doctor.go` / `mcp.go` 等 | 纯文本输出命令层 |

---

## 七、架构观察（含取舍与风险）

1. **单 Model + 内嵌 overlay 的取舍**：换来 overlay 与 composer 的紧耦合（chooser 可复用 textarea 自由输入），代价是 `hideComposer` / `bottomRows` / `computeStatusLineCount` 三处布局函数必须手工同步（`chat_tui.go:2294-2305,4008-4013` 注释自警，称 "load-bearing invariant"）。
2. **性能护栏密集**：
   - 思考流只渲染尾部 4096B / 12 行（`chat_tui.go:2399-2406`）；
   - 工具输出只保留尾部（`chat_tui.go:181-191`）；
   - slash 补全目录快照缓存（`complete.go:66-70`）；
   - 事件批量 drain（`maxEventDrain=512`，`chat_tui.go:452-453,1880-1895`）；
   - 增量换行缓存（`wrap_cache.go:5-27`）。
3. **终端兼容补丁多**（终端 TUI 的现实成本）：
   - Warp 清屏特殊处理（`chat_tui.go:1016-1022`）；
   - ConPTY 鼠标失效重启用（`mouse_reenable.go`）；
   - Termux 原生 scrollback 模式（`chat_tui.go:46-49,2152-2165`）；
   - 宽字符 `ClearScreen`（`chat_tui.go:2139-2147`）。
4. **可迁移性设计清晰**：控制面只暴露子端口接口，TUI / serve / desktop 共用同一 `Controller` 与 `event.Kind` 线协议；`port.go:22-30` 明说子端口是后续拆分 Controller 的分解边界。
5. **主题为全局可变状态**：`activeCLITheme` 单例使主题动画必须整帧预渲染（两次渲染成本），且运行中切换直接放弃动画（`theme_sweep.go:37`）——以简单换可预测。

---

## 八、证据索引与存疑项（诚实标注）

### 8.1 关键证据文件索引

- 分层规则：`tools/repolint/layers.go:13-48`；`REASONIX.md:11-17`。
- 边界接口：`internal/control/port.go:22-30,34-45,49-72,76-96,99-107,237-241,243-259,264-277`。
- TUI 装配：`internal/cli/cli.go:1114-1121,1155-1163,1201-1212,1273-1285`。
- 事件：`internal/event/event.go:23-119,526-569,831-833`；`internal/cli/chat_tui.go:449-450,1866-1896,1987-1991,4352,5462-5469,4640-4642`。
- 调用链：`internal/control/controller.go:1017-1022,1067-1075,1099-1103,1929,1973,2024`；`admission_guard.go:24`；`turn_orchestrator.go:78,289`；`internal/agent/agent.go:1399-1402`；`execute_batch.go:60`；`execute_one.go:67`；`internal/tool/tool.go:21-31`。
- 审批：`internal/control/controller.go:6120-6159,6201-6234,2108-2150,2154-2205,2212,2495,2512-2529,2632`；`approval.go:233-274,278,312-358,373-408`；`chat_tui.go:287-295,1415-1418,3170-3180,3275-3298,4577-4582`。
- 持久化：`internal/agent/save.go:194,201`；`controller.go:3522-3564,3637-3661,4738`；`sessionpath.go:15`；`resume.go:15-23`；`resume_picker.go:27-33`；`branch.go:117-131`。
- 渲染：`chat_tui.go:46-49,64-66,181-191,2152-2165,2226-2234,2256,2306,2315,2332-2342,2399-2406,3563-3579,4014,4047,4071`；`transcript.go:19-44,226-235,366-382`；`wrap_cache.go:5-27`；`md.go:9-15,24-47,193-331,309-319,340-401`；`mathnode.go:13,23`；`latex.go:8-45`；`diffview.go:12-15,42-48,248`；`box.go:9-16`；`style.go:9-52`；`theme.go:57,473`；`theme_sweep.go:11-70`。
- 复用：`internal/serve/serve.go:33,55,82-99,661,763`；`desktop/tabs.go:35,168,1543-1575`；`desktop/app.go:58`。

### 8.2 存疑与边界说明

1. **`Text`/`Reasoning` 增量的精确发射行号**：确认了 Kind 定义（`event.go:29-33`）、消费端（`ingestEvent`）与 `agent.go:2180-2210` 流式 chunk 循环（该处已确认发出 partial `ToolDispatch`），但未逐行定位 `event.Text` / `event.Reasoning` 的具体 `Emit` 行。调用链结论不依赖该行号。
2. **`TurnStarted`/`TurnDone` 的发射行号**：确认了 `coordinator.go:657` 的消费分支与 `event.go:59-62` 语义，未在 agent 包内逐一定位两处 `Emit` 行号。
3. **部分子端口方法清单**：本轮只精确读取了 `port.go:22-107` 与 `:236-259` 两段；`SessionHistory` / `MemoryControl` / `Capabilities` / `Status` / `SessionPersistence` / `Input` / `Inbox` 的方法集合未逐条列出（组合关系已在 `port.go:247-258` 确认）。
4. **`internal/store` 内部 API**：仅确认导入关系与包存在，未展开函数级入口；"TUI 与 store 无直接依赖"这一结论成立（`chat_tui.go` 不导入 store）。
5. **`approval.go` 中 `resolveAsk` 精确行号**：`cancelAsk` 在 `approval.go:408`，`resolveAsk` 定义位置仅记录为紧随其后，未精确到行。
6. **方法边界**：全程只读 `view`/`grep`/`ls`；未运行构建、测试或 lint；未修改被调研仓库任何文件。`help_view.go`、`memory_view.go`、`run_*` 等非交互输出链路未逐一展开。

---

*文档由 AI 调研生成：主调研 + 两路只读子代理（表现层组件盘点 / 运行时边界梳理）交叉复核；所有结论附 `file:line`，行号基于 2026-09-19 工作区快照。*
