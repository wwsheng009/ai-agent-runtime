# frontend composer 上方浮动任务列表（Todo 面板）实现方案（参考 deepseek-harness）

状态：**已实施（工作树未提交，2026-09-15）**——T0–T3 与 T5 已落地、T4 为可选打磨未做，实施记录与门禁证据见 §11；此前 2026-09-15 完成一轮完整性自审（§10）并按审查结论就地优化（D1–D6 已定稿，见 §10.3）。

日期：2026-09-15

负责范围：`frontend/`（新面板组件、状态派生、i18n、样式、测试）；`backend/`（仅当需要把 todo 快照纳入面向前端的结构化通道时，最小增量）

参考材料：

- 目标项目实现（只读调研）：`E:\projects\ai\deepseek-harness\packages\todo\tool-todo\*`、`packages/client/ui-conversation/src/client/skeleton/TodoPanel.{tsx,module.css}`、`packages/client/ui-conversation/src/client/skeleton/ConversationRoot.tsx`、`packages/client/ui-tool/src/client/tool/toolviews/todo-row.tsx`、`apps/web/tests/todo-row.expected.e2e.ts`
- 目标项目调研报告（本轮子代理产出）：`.tmp/task-panel-analysis/harness-todo-reference.md`
- 当前项目取证报告（本轮子代理产出）：`.tmp/task-panel-analysis/runtime-current-state.md`
- 关联既有计划：`docs/plan/frontend-deepseek-harness-optimization-plan.md`（P2-9 子片 1「后台任务常驻状态条」、子片 3「会话目标四相指示」是本方案的同类先例）、`docs/plan/frontend-deepseek-harness-gap-list.md`
- 证据口径：源码直读（标注 `路径:行`）；行号基于 2026-09-15 工作区快照。

---

## 1. 背景：当前前端有没有渲染任务 / task 的功能？

**结论：没有。** 当前 `frontend/` 不存在任何「常驻任务/待办列表」渲染能力，也不存在对 `todos` 工具的任何特判。逐条证据：

| 检查项 | 取证命令 / 位置 | 结果 |
|---|---|---|
| 前端是否有 todo/task 面板组件 | `frontend/src/**` 全量 grep `todos`（literal，大小写敏感） | **0 命中**——连字符串都没有 |
| 是否有 todo 专用工具行视图 | `components/workspace/message-tool-row.tsx` | 通用工具行：标题固定为 `segment.name`（`message-tool-row.tsx:99`），按 `status` 上色（`:92-95`），无按工具名分派 |
| 是否有 composer 上方常驻条 | `components/workspace/composer-status-row.tsx` | 存在，但它是**卡内**状态行（`border-b`，`:47-52`），语义是附件 / 命令行 / 响应态，不含任务列表 |
| 是否有相近先例 | `pending-interaction-bar.tsx`、`workspace-shell-topbar.tsx`（作业条实现在顶栏内，**无**独立 `jobs-status-bar.tsx`）、`session-goal-indicator.tsx`、`artifact-panel-plan-surface.tsx` | 分别覆盖审批/提问、后台作业计数、会话目标、plan-mode 评审——**没有一个覆盖 agent 的 todo 列表** |
| 既有计划是否已登记该缺口 | `docs/plan/frontend-deepseek-harness-optimization-plan.md` grep `任务列表|todo|TodoPanel` | 0 命中——本方案是**新增缺口**，需回填 gap-list |
| 后端是否有 todo 能力 | `backend/internal/toolkit/tools/todos.go` | **有**，且契约完整（见 §4.1） |

也就是说：**后端已经有一个成熟的 `todos` 工具在跑（模型可以写任务列表并落盘），但这份状态在传输链路上被降维成了「工具行的文本摘要」，前端拿不到结构化列表，也没有常驻面板把它显示出来。**

### 1.1 用户可见的当前行为

模型调用 `todos` 之后，用户在会话流里看到的是一条普通工具行（标题 `todos`、状态图标 + 文本摘要），摘要由 `backend/internal/agent/tool_runtime_events.go:969-981` 的 `todos` 特判放宽到 32 行 / 240 字符——所以用户**能看到一部分任务文本**，但：

1. 它是**历史记录**，不是「当前计划」：往下滚动就被后续消息淹没，不会随状态更新而粘在输入框上方；
2. 它是**文本**，没有逐项状态语义（无法一眼看出哪项 in_progress、进度如何）；
3. 多轮写列表后，会话里会有 N 条这样的行，**没有单一事实源**。

---

## 2. 目标与非目标

### 2.1 目标

1. 在 **composer 输入卡正上方**新增一个浮动/常驻的「当前任务」面板（下称 Todo 面板），显示**本会话最新的** todo 全量快照：逐项 `content` + 三态状态图标，表头给出「已完成 · 进行中 · 待处理」计数。
2. 面板默认**折叠为单行条**，点击展开列表；列表为空时不渲染；全部完成后的可见性策略由 **Q1** 决定（当前默认「立即隐藏」，见 §6.3，亦可改为「保留到下一次调用」）。
3. 面板必须**与实际执行状态同源**：随 `todos` 工具调用实时更新，而不是解析自然语言或前端猜测。
4. 会话切换 / 历史回放 / 刷新后重进时，面板能恢复到「该会话最新的 todo 快照」。
5. 复用当前项目的既有约定（Tailwind 语义 token、i18n 双语资源、Vitest + jsdom + `act` + `createRoot`——仓库**不使用** `@testing-library/react`、行数门禁 ≤ 500 非空行），不引入新依赖、不引入插件/插槽框架。
6. 与既有面不冲突、不重复：工具行照旧显示；plan-mode 评审面板、jobs 状态条、goal 指示器职责不变。

### 2.2 非目标

- 不照搬 deepseek-harness 的 cordis `ctx.slots` 插槽体系与 `conversation.input.dock` 概念；当前项目用**直接组合**（先例：`PendingInteractionBar` 直接写在 `main-section.tsx` 的 composer overlay 容器里）。
- 不引入独立的「todo 事件总线」或第二套会话投影框架；不提「全量投影系统」重构。
- 不做任务面板的**编辑**能力（用户点选改状态、增删任务）——本期只读展示；写入权仍归模型（`todos` 工具）。
- 不做多会话/多 agent 的聚合任务视图（team 面板已有 `task-queue-section`，各自职责不变）。
- 不改变 `todos` 工具的契约、存储位置与上下文注入行为（`todo_state` 上下文层不在本期范围内）。
- 不做 toast/桌面通知（沿用既有通知位约束）。

---

## 3. 参考实现分析：deepseek-harness 的「composer 上方任务条」

调研全文见 `.tmp/task-panel-analysis/harness-todo-reference.md`，本节只保留与移植相关的结论。

### 3.1 数据流（单一路径，全链路结构化）

```
模型调用 todo_write(todos=[{content,status}, ...])
  → 工具 execute：exec.agent.session.append('todo/write', { todos })      ← index.ts:203-210
  → 宿主侧 session-projection 投影单元折叠：key='todos'，整值 LWW
        todo/write → 新列表；turn/start → null；其他事件 → 原引用不变        ← index.ts:134-145
  → （持久化/回放：投影缓存 + 快照重建，stateVersion=2 作为版本守卫）        ← session-projection:429/459/502-537
  → 控制帧把投影值下发浏览器；客户端 ProjectionValueStore 取 seq 更高者
  → useProjection('todos') → TodoDock → <TodoPanel todos t />
```

三个关键契约：

1. **事件只有一种**：`todo/write`，载荷是**全量快照** `{ todos: TodoItem[] }`，无 delta、无 item id。
   `TodoItem = { content: string; status: 'pending' | 'in_progress' | 'completed' }`（`tool-todo/src/types.ts:21-33`）。
2. **投影语义是「整值 + 后写胜」**，并且**下一个 `turn/start` 会把列表清成 `null`**（`turn/end` 不清，保留已完成清单给用户看）。
   这条「新回合开始即退休上一轮计划」的语义，是面板不会跨轮次残留的关键。
3. **面板只读**：没有编辑入口；写入权唯一归模型的 `todo_write` 工具。

### 3.2 挂载位置：不是绝对定位，而是 composer 栈的第一行

参考实现没有用 `position: absolute` 去「飘」。它的做法是：

- 会话根把 composer 区域组织成一个纵向栈 `composerStack`，顺序为
  `Hero（新建态） → 工作区行 → renderSlot('conversation.input.dock') → InputBar`（`ConversationRoot.tsx:346-353`）；
- 整个栈放在 `.composerSeat` 内，由 CSS 做 `position: sticky; bottom: 0`，因此**任何插进 dock 位的内容天然浮在输入卡上方并随滚动吸底**；
- dock 位的语义在插槽契约里写明是 "Full-width entries above the composer card"（`contract/slots.ts:166`）；
- Todo 条目注册为 `{ name: 'conversation.input.dock', id: 'todo', order: 0 }`，排在 goal（order 10）与 queue（order 20）之前（`TodoPanel.tsx:133-139`，测试 `todo-panel.client.spec.tsx:125-133` 断言该顺序）。

> 移植结论：当前项目的 composer **在非新建会话态已经是** `absolute inset-x-0 bottom-0 z-30` 的吸底 overlay 容器（`main-section.tsx:418-425` 为三元分支：`isNewThread` 态是 `relative inset-auto`，其余态是 `absolute inset-x-0 bottom-0`；`z-30` 与 `pointer-events-none` 无条件成立），
> 所以「浮动」是免费的——只要把面板作为该容器的**第一个子元素**（在 `PendingInteractionBar` 之前）插入即可，无需新增定位体系。

### 3.3 面板 UI 结构（可直接对拍移植）

| 结构 | 参考实现 | 说明 |
|---|---|---|
| 根元素 | `<section data-testid="todo-panel" aria-label={t('todo.title')}>` | 测试锚点 + 可访问名 |
| 空列表 | `if (todos.length === 0) return null` | 无任务时**整块不渲染**，不是渲染空壳 |
| 默认折叠 | `const [collapsed, setCollapsed] = useState(true)` | 默认单行条，点击展开 |
| 表头按钮 | `<button aria-expanded={!collapsed}>` 含 图标 + 标题 + 计数 + chevron | 整行可点，chevron 随态翻转 |
| 计数文案 | `已完成 N · 进行中 N · 待处理 N`，**零值分段省略**，用 U+2002 宽空格分隔 | `progressLabel()`（`TodoPanel.tsx:75-86`） |
| 列表 | `<ul>` + `<li key={item.content} data-status={item.status}>` | key 用 content（无 id 契约下的自然选择） |
| 状态图标 | completed=实心对勾环 / in_progress=渐变环 + 旋转动画 / pending=虚线环；14×14 画板放进 16×16 格 | 三种状态各有独立 token 色 |
| 色彩 token | completed→`state-success`、in_progress→`state-business`、pending→`label-caption` | 语义 token，不写死色值 |
| 列表滚动 | `max-height: 180px; overflow-y: auto`，滚动条用「l2 浮起」皮肤变量 | 长列表不撑爆输入区 |
| 单项文本 | 单行 `text-overflow: ellipsis`，**不换行** | 保持条状几何稳定 |

### 3.4 会话流里的对应物（不是替代关系）

参考实现**同时**保留了两处呈现，二者职责不同、互不替代：

1. **transcript 工具行**：`todo_write` 调用渲染为一行（标题 `todo_write`、摘要为 `2 进行中` 之类的形态、右侧 `+N` 折叠计数），随消息流滚走；
2. **常驻面板**：始终显示**最新**快照，粘在输入框上方。

导出到本方案的结论：**不要用面板替换工具行**（否则会丢掉历史与可追溯性），也不要让面板去解析工具行文本。

---

## 4. 当前项目现状（逐层取证）

### 4.1 后端：`todos` 工具已经完备，缺的是「面向前端的结构化出口」

| 维度 | 事实 | 证据 |
|---|---|---|
| 工具名 / 分类 | `todos`，`ToolKindControl`、`ReadOnly=true`、`SupportsParallel=false` | `backend/internal/policy/taxonomy.go:65`、`toolkit/tools/todos.go:104-112` |
| 参数契约 | `todos: [{content, status, active_form}]`，三者**均 required**；`status ∈ {pending, in_progress, completed}` | `toolkit/tools/todos.go:62-90` |
| 语义差异（vs 参考实现） | 本项目管理更严：**同一时刻只允许一个 `in_progress`**；多传会「软修复」——保留最后一项、其余降级为 `pending`，并在结果里回报 `multi_in_progress_healed` / `demoted_in_progress` | `toolkit/tools/todos.go:175-178, 242-246, 251-275` |
| 数据模型 | `TodoItem{content, status, active_form, created_at, updated_at, completed_at}`、`TodoList{items}`（比参考实现多 3 个时间戳与 `active_form`） | `toolkit/tools/todos.go:33-46` |
| 结果载荷 | 文本 `Content` = 人类可读差异摘要（新增/状态变更/保持/移除 + 当前列表）；**`Metadata` 里带全量结构化快照**：`total/pending/in_progress/completed/todos[]/storage_mode/session_id/goal_id` | `toolkit/tools/todos.go:220-249` |
| 持久化 | 按 `sessionID × goalID` 分片落盘；显式 `storage` 优先，否则 `<root>/<session>/<goal>.json`（root 默认 `os.TempDir()/ai-agent-runtime/todos`）；沙箱拒绝时退回内存态 | `toolkit/tools/todos.go:387-401, 427-440` |
| 前端可见性 | 只有**文本摘要**进入工具事件流：`tool_runtime_events.go` 对 `todos` 的唯一特判是把摘要行数上限放宽到 32 行、单行 240 字符（普通工具是 3 行 / 120 字符） | `backend/internal/agent/tool_runtime_events.go:969-981` |
| 上下文层（与 UI 无关） | 压缩/裁剪时以 `context_stage=todo_state` 的开发者/助手消息进入 LLM 上下文（"Current todos:\n- [ ] ..."）；`contextreconcile` 另有 `TodoSnapshot{Content,Status}` 做丢失检测 | `contextmgr/compact.go:345-359`、`contextmgr/manager.go:1979-1985`、`contextreconcile/reconcile.go:43,79-80` |

**关键判断**：后端**已经生产**了完美的 UI 数据（`Metadata.todos` 全量快照 + 三态计数），只是这条结构化通道目前**只喂给模型/上下文，不喂给浏览器**。因此本方案的取舍点是「如何把这份快照送到前端并折叠成一个稳定投影」，而不是「怎么造任务数据」。

### 4.2 前端：零 todo 代码，但挂载点与组件范式都已就位

| 结论 | 证据 |
|---|---|
| `frontend/src/**` 内 `todos` 字面量 **0 命中** | 全量 literal grep（本轮实测） |
| 工具行是通用渲染：标题恒为工具名，按 `status` 上色，无按工具名分派 | `components/workspace/message-tool-row.tsx:92-99` |
| composer 吸底 overlay 容器已存在，`z-30`、`pointer-events-none`，子元素各自开 `pointer-events-auto` | `components/workspace/workspace-shell/main-section.tsx:418-426` |
| 容器内已有的「上方常驻件」= `PendingInteractionBar`（审批/提问/计划评审），宽度用 dock 轴 `--app-chat-content-width-dock`；输入卡用 composer 轴（宽一档） | `main-section.tsx:427-446` |
| 卡片内状态行是另一层（`ComposerStatusRow`，`border-b`，附件/命令行/响应态） | `components/workspace/composer-status-row.tsx:46-52` |
| 顶部已有「后台作业常驻状态条」先例（P2-9 子片 1：仅 live>0 渲染、点击开弹层、复用同一数据源）。注意：**没有** `components/workspace/jobs-status-bar.tsx` 这个文件，该状态条实现在顶栏内，其测试文件是 `components/workspace/jobs-status-bar.test.tsx`（imports `WorkspaceShellTopbar`） | `workspace-shell-topbar.tsx` + `jobs-status-bar.test.tsx:1-16` + `docs/plan/frontend-deepseek-harness-gap-list.md:164` |
| `todos` 工具行今天等于「无信息通用行」：工具名未注册（落到 `generic` 扳手图标），且摘要读取器**不读** `summary`/`summary_lines` | `lib/tool-row/kind.ts:13-85`、`lib/thread-state/tools.ts:70-92`、`components/workspace/message-tool-row.tsx:111-138` |
| 前端运行时事件缓冲是最近 **100** 条滚动窗口（面板数据不能只靠它） | `lib/thread-state/history-artifacts.ts:15`、`lib/thread-state/events.ts:387` |
| i18n 资源按域分文件，workspace 域两侧各 12 个 `.ts`（10 个 `panels-*.ts` + `base.ts` + `index.ts`） | `frontend/src/i18n/resources/{zh-CN,en-US}/workspace/` |
| 工程纪律：单文件 ≤ 500 非空行门禁（`scripts/verify-max-lines.mjs`）、i18n 硬编码扫描门禁、`*.bak/.backups` 门禁 | `frontend/scripts/`、`npm run lint` 四件套（见 gap-list §0） |

---

## 5. 数据通道与状态投影（本方案的核心，含本轮实测结论）

### 5.1 事实矩阵：数据在哪里、今天能不能到前端

| # | 事实 | 证据 | 对方案的影响 |
|---|---|---|---|
| F1 | 权威结构化快照在服务端工具结果 `Metadata` 里：`todos`（全量 `[]TodoItem`）、`total/pending/in_progress/completed`、`storage_mode/session_id/goal_id` | `toolkit/tools/todos.go:231-241` | 数据已经存在，不需要新增生产逻辑 |
| F2 | 后端已具备「按会话回读最近一次快照」的能力（从工具消息 metadata 反向扫描） | `chat/compact_reconciliation.go:76-101` | 冷启动权威快照有现成实现可复用 |
| F3 | **SSE 出口不含结构化 todos**：`tool.completed` 的扁平字段只有 `arg_preview / summary / summary_lines / render_output* / file_path / protocol_result`；`protocol_result.metadata` 经白名单过滤 | `agent/tool_runtime_events.go:46-84, 692`；`toolprotocol/result.go:172-177, 186-206` | 必须做一处后端改动，否则前端拿不到 |
| F4 | 现有 `todos` 特判只有「摘要放宽」：32 行 / 240 字符；`arg_preview` 已有 `todos=[N]` 形态 | `tool_runtime_events.go:969-981`；`tool_runtime_events_test.go:328` | 摘要文本有损、只够做兜底，不能当数据源 |
| F5 | **刷新后不会重放历史工具事件**：运行时流首连游标 = `max(本地已消费 seq, 轨迹回放游标)`，窗口回放已把轨迹推进到 `latest_seq`，首连**有意跳过历史 dump** | `hooks/workspace/use-session-runtime-stream.ts:66-80, 311-314` | ★ 纯「从事件流派生」的方案在页面刷新/重开后会**空白**，必须配一条冷启动读通道 |
| F6 | 客户端运行时事件缓冲是**最近 100 条**滚动窗口 | `lib/thread-state/history-artifacts.ts:15`；`lib/thread-state/events.ts:387` | 事件派生只覆盖近场；再次证明需要 F2 的权威读通道 |
| F7 | 历史原文 `session-history-{id}` 逐字保留，内部上下文消息（含 `todo_state`）被过滤掉，但**工具消息 metadata 幸存**；前端保留 `metadata`（仅过滤 `context_stage`/developer 消息） | `lib/thread-state/history-artifacts.ts:151-158, 190-192, 208-211, 220-237`；配上 `agent/loop.go:4221-4233`、`types/message.go:30`、`api/skills/handler.go:2629-2637` | **B1 已由证据链确认为「复用既有契约」而非未知量**（§10.1 V6），T0 由此降级为一条断言（见 §7） |

### 5.2 数据通道设计（推荐 A + B，分两步走）

**通道 A —— 实时结构化出口（后端 1 处改动，必做）**

插入点选在 `attachProtocolResultToPayload`（`agent/tool_runtime_events.go:667-693`），因为**它手里已经有 `toolName`**，天然支持「按工具作用域」放行：

```go
// tool_runtime_events.go，wire 组装处
wire := toolprotocol.ResultFromParts(toolName, toolCallID, content, result.Error, metadata)
// 新增：仅对 todos 工具附带结构化快照（工具作用域白名单，避免污染共享命名空间）
if strings.EqualFold(toolName, "todos") {
    attachTodoSnapshotToProtocolResult(payload, wire, metadata) // 写入 protocol_result.metadata.todo_snapshot
}
payload["protocol_result"] = wire.EventMap()
```

- **只放行 `todos` 一个键**（数组自描述）。**不要**把 `total/pending/completed/in_progress` 加进共享白名单：`thinEventMetadata` 是所有工具共用的命名空间（`toolprotocol/result.go:186-206` 里既有 `toolresult.Metadata*Key` 常量，也有 `file_path/current_snippet` 这类通用名），而 `total`、`pending`、`session_id` 在仓库里被大量其它位置使用（如 `api/skills/handler.go:1291`、`session_runtime_handlers.go:818`），全局放行等于顺带泄漏别的工具的同名字段。计数在前端从数组派生即可。
- **键名定稿 `todo_snapshot`**（D1，不再二选一）：与生产侧 `metadata["todos"]` 在命名上显式分离——后者是 `compact_reconciliation.go:80-101` 的读取契约（F2），**不要改名**；同时保留本段代码示例中的 `attachTodoSnapshotToProtocolResult` 形态，避免后来者「顺手统一命名」而改坏既有实现与测试。
- 结果：`tool.completed` 在 live 与「同页会话内重放」两种情况下都能自给自足，前端不再依赖文本摘要。

**通道 B —— 冷启动/刷新后的权威快照（二选一，B1 已由证据链确认，见 §10.1 V6）**

- **B1（首选，前端零后端改动）**：从 `/history` 返回消息的 `metadata.tool_metadata.todos` 派生（`session-history-{id}` 产物保留同一批消息，见 §10.1 V6 的五步证据链）。
  - ⚠️ **2026-09-15 现场修正**：工具结果元数据在历史消息里是**整体嵌在 `metadata.tool_metadata` 下**的（实测 `curl .../sessions/{id}/history`：`history[3].metadata.tool_metadata.todos`），不是平铺的 `metadata.todos`。按平铺读取会**永远取不到**（面板刷新后不显示），本方案初稿即此错误；现已按嵌套包实现，并保留平铺形状作为兼容回退。
  - **确认（已降级为一条断言，~5 分钟）**：`curl .../sessions/{id}/history | jq '.. | .metadata?.tool_metadata?.todos? // empty'` 命中非空即成立；该形态同时固化为 T2 的单测夹具（含一份实测载荷的逐字回归用例）。
  - 判定：命中 → 直接派生（刷新后仍有列表、零后端成本，且与 `compact_reconciliation.go:80-101` 消费的是同一条链）；未命中 → 转 B2。
- **B2（保底）**：新增只读接口 `GET /api/runtime/sessions/{id}/todos`，复用 F2 的 `latestTodoSnapshot`。
  - 参照实现：后端 `api/skills/handler.go:778-779`（plan mode 同类读接口）；前端 `api/runtime/sessions.ts:361-389` 的 fetch 形态。
  - 返回体建议与通道 A 同构（`{items:[{content,status,active_form}], goal_id, updated_at}`）；计数同样由前端从 `items` 派生（不在返回体里单列），保证前端只有一条解析路径。
- **明确不采用**：客户端解析 `context_stage=todo_state` 的上下文消息（会被 `history-artifacts.ts:190-192` 过滤，且属提示词实现细节）；解析 `summary_lines` 文本（有损，且格式是给模型看的，`todos.go:323-361`）。

**两步走的收益**：通道 A 让「正在跑的会话」立刻可用（用户主要场景）；通道 B 只影响刷新/重开，可以独立排期，且 B1 可能零成本。

### 5.3 前端状态投影（单一事实源，不新增 SSE 通道、不新增 store）

| 层 | 文件 | 职责与约束 |
|---|---|---|
| 纯派生 | `frontend/src/lib/thread-state/todos.ts`（新） | `deriveLatestTodos(events, historyContent?) → TodoSnapshot \| null`：按 `getRuntimeEventSeq`（`lib/thread-state/sessions.ts`）取**最新**一次快照；无数据返回 `null`（**不臆造、不跨 session/goal 合并**）；坏 JSON/缺字段降级为 `null` 而非抛错 |
| 订阅 | `frontend/src/hooks/workspace/use-session-todos.ts`（新） | 仿 `hooks/workspace/use-runtime-plan-mode.ts:79-263`：reload-key 门控（`use-runtime-checkpoints.ts` 的 `buildRuntimeEventReloadKey`）、`tool.completed` 事件类型门控（`:25-32` 的写法）、`sessionId` 为空即清空（`:97-104`） |
| 优先级 | 同上 | 同一会话内 live 与 B 通道都有值时，取 `updated_at`/`seq` 更新者；两者都没有 → 返回 `null`（面板不渲染） |
| 渲染 | `components/workspace/task-panel/*` | 见 §6 |

- 事件窗口（F6）导致的「旧快照被裁掉」由 B 通道兜住；B 通道未落地前，面板在刷新后退化为不渲染（**如实降级**，不显示过期列表）。
- 若后续需要在多处复用同一快照，再按 `lib/session-goal/store.ts` 的 `useSyncExternalStore` 先例提升为共享投影——**本方案不做**，避免过早抽象。

### 5.4 必须写进测试的契约

| 侧 | 用例 | 断言 |
|---|---|---|
| 后端 | `agent/tool_runtime_events_test.go` 新增 | `todos` 工具 → `protocol_result.metadata.todo_snapshot` 存在且为**全量**条目（含 `content/status/active_form`）；**非** `todos` 工具 → 该键不存在（防止白名单放宽成全局） |
| 后端 | 既有回归 | `TestToolCompletedEventPayloadPreservesTodosListLines`（`:287-330`）继续通过：摘要行与 `arg_preview` 行为不变 |
| 前端 | `lib/thread-state/todos.test.ts` 新增 | 取最新一次而非第一条；坏 JSON / 缺 `status` → `null`；100 条窗口裁剪后仍能取到最近一次；`session` 切换不串数据 |
| 前端 | `hooks/workspace/use-session-todos.test.ts` 新增 | `sessionId` 为空不请求不渲染；reload key 变化触发重算；B 通道失败不阻塞 A 通道结果 |

---

## 6. UI 与交互设计

### 6.1 挂载：composer 覆盖层栈的第一行（复用现有 DOM 位置，不需要新增定位上下文）

```tsx
// main-section.tsx —— 现有覆盖层容器（z-30 / pointer-events-none / group/composer 轴）
<div className="pointer-events-none absolute inset-x-0 bottom-0 z-30 ...">
  <TodoPanel sessionId={sessionId} />        {/* 新增：第一行，任务列表浮在最上方 */}
  {pendingBar ? <PendingInteractionBar ... /> : null}
  <div className="pointer-events-auto ...">  {/* 输入卡（composer 轴） */}
</div>
```

- **位置**：`TodoPanel` 插在 `PendingInteractionBar` **之前**（DOM 顺序 = 视觉从上到下）。审批/提问是「阻塞且需要立刻决策」，保持在最靠近输入框的位置；任务条是「环境态」，浮在它上面。这与参考实现「任务条是 composer 栈第一行」的语义一致。
- **宽度**：复用 dock 轴 `--app-chat-content-width-dock`（与 `PendingInteractionBar` 同轴），保证两个常驻件左右对齐；输入卡保持宽一档的 composer 轴。
- **命中区**：外层容器 `pointer-events-none` 不要动，`TodoPanel` 内部只在卡片本体上开 `pointer-events-auto`，否则会挡住下方的消息区点选。

### 6.2 组件结构（拆到符合 500 非空行门禁）

| 文件 | 职责 | 预估行数 |
|---|---|---|
| `frontend/src/components/workspace/task-panel/index.tsx` | 面板外壳：可见性判定、折叠状态、进度摘要、展开容器 | ~120 |
| `frontend/src/components/workspace/task-panel/task-panel-header.tsx` | 单行摘要：进度徽标 + 当前 `in_progress` 文案 + 折叠按钮（`aria-expanded`/`aria-controls`） | ~90 |
| `frontend/src/components/workspace/task-panel/task-panel-list.tsx` | 列表渲染：状态图标 / `line-clamp-1` 文案 / 滚动上限 / 「还有 N 项」页脚 | ~110 |
| `frontend/src/components/workspace/task-panel/task-panel-item.tsx` | 单项：三态图标 + 文本样式（in_progress 高亮、completed 删除线+弱化） | ~60 |
| `frontend/src/hooks/workspace/use-session-todos.ts` | 从会话投影读取任务快照（派生函数 `lib/thread-state/todos.ts` 由 §5.3 定义；hook 位置遵循 `hooks/workspace/` 既有布局） | ~60 |
| `frontend/src/i18n/resources/{zh-CN,en-US}/workspace/panels-todos.ts` | 文案（标题、进度、折叠、空态、已完成隐藏策略）；**zh-CN 为形状源**，en-US 以 `satisfies DeepStringShape<typeof zhWorkspace>` 收口，两侧 `workspace/index.ts` 都要登记 | ~15/语言 |

> 复制形态直接沿用 `panels-*.ts` 既有词典文件的写法；文案**不得**硬编码（`frontend/scripts` 的 i18n 扫描门禁会拦）。

### 6.3 视觉与状态映射（对齐参考实现的三态语义）

| 状态 | 图标 | 文本样式 | 说明 |
|---|---|---|---|
| `pending` | 空心圆 | `text-muted-foreground` | 未开始 |
| `in_progress` | 实心/旋转脉冲点 | `text-foreground` + `text-accent-teal` 强调 | 与工具行 `running` 用同一色板（`message-tool-row.tsx:75`） |
| `completed` | 对勾 | `text-muted-foreground` + `line-through` | 完成后弱化 |

- **进度摘要**：`已完成 N · 进行中 N · 待处理 N`（零值分段省略，与 §3.3 参考实现同口径，也即 §2.1 目标 1 的「三态计数」），另附一条当前进行项（取 `active_form`，为空回退 `content`）。参考实现同样以「当前项」作为收起态主信息。
- **折叠态**：默认**收起**，单行 ~36px；展开后列表 `max-h-40 overflow-y-auto`，超过 8 项显示「还有 N 项」并可继续滚动。
- **可见性**：
  - `items.length === 0` → 不渲染（不占位）；
  - 全部 `completed` → 短暂停留后自动隐藏（可用 `retention` 常量控制，默认 0 = 立即隐藏，避免与「已完成」噪声常驻）；
  - 会话切换 → 视为新快照，折叠态**按会话**记在 `sessionStorage`（键 `workspace.todoPanel.collapsed.{sessionId}`，读写均 `try/catch`、失败回落「默认收起」），`sessionId` 变化时按新会话重读（**D2 结项定案（2026-09-15）：保留实现侧的按会话持久化**，与初稿「组件内 state」的差异及理由见 §9.1 决策表与 §11「与方案正文的差异」）。
- **动效**：展开/收起走高度过渡，尊重 `prefers-reduced-motion`；不做自动滚动抢占用户滚动位置。
- **性能（D6）**：派生结果走 `useMemo`（依赖 = 快照事件引用 + 历史内容），并复用 §5.3 的 `tool.completed` 事件类型门控——token 流、工具行状态机等**非** `tool.completed` 的更新不得触发重扫事件窗口；列表最多渲染前 8 项 + 「还有 N 项」，不做虚拟化。
- **可访问性（D6）**：进度摘要容器加 `aria-live="polite"`，其文本**仅在计数或当前进行项变化时**更新（避免每次渲染都触发播报）；折叠按钮带 `aria-expanded` + `aria-controls`（指向列表容器 `id`）；状态图标 `aria-hidden`，语义由文案承载，不靠颜色单独表意。

### 6.4 关键边界（实现时不要踩）

1. **任务列表是 `session × goal` 维度的**：后端落盘路径为 `<root>/<session>/<goal>.json`（`todos.go:387-401`），goal 级子代理有自己的列表。面板只展示**当前会话当前 goal** 的快照，会话/goal 切换必须整体替换而不是合并。
2. **同一时刻只有一个 `in_progress`**：本项目比参考实现更严（多传会被软修复并回报 `multi_in_progress_healed`）。UI 不提供「手动推进」入口，不做本地乐观改写——一切以服务端快照为准，避免出现「界面显示 2 个进行中」的伪状态。
3. **工具被禁用/沙箱回退时**：`todos` 可能走内存态或直接不可用，此时面板静默不渲染即可，不要报错态弹条。
4. **长文本**：`content`/`active_form` 可能很长（工具契约明确要求模型拆分，但 UI 仍要 `line-clamp-1` + `title` 悬浮全文）。

---

## 7. 任务拆解（建议按 T0 → T1 → T2 → T3 → T4 → T5 顺序，T1/T2 可并行；T5 可与 T4 并行）

| ID | 任务 | 交付物 | 文件（新增 / 修改） | 依赖 |
|---|---|---|---|---|
| **T0** | **B1 契约确认**（**已降级为 ~5 分钟**：静态证据链成立，见 §10.1 V6） | 一条断言输出 `curl .../sessions/{id}/history \| jq '.. \| .metadata?.todos? // empty'` 非空；顺带确认 `gateway.Process` 入参 metadata 逐键透传（唯一未逐行追证的一环） | 无代码改动（复用现有历史接口） | — |
| **T1** | 通道 A：`todos` 工具作用域白名单 | 后端放行 + 单测 | 改 `backend/internal/agent/tool_runtime_events.go`（`attachProtocolResultToPayload:667-693`）、必要时 `backend/internal/toolprotocol/result.go:172-217`；测试 `agent/tool_runtime_events_test.go` | — |
| **T2** | 前端数据层 | 纯派生 + hook + 单测 | 新 `frontend/src/lib/thread-state/todos.ts`、`frontend/src/hooks/workspace/use-session-todos.ts`、对应 `.test.ts` | T1 |
| **T2b** | 仅当 T0 判定 B1 不可用时：B2 只读接口 | 后端 handler + 前端 api + 测试 | 后端仿 `api/skills/handler.go:778-779`、复用 `chat/compact_reconciliation.go:76-101`；前端在 `api/runtime/sessions.ts` 新增取数函数（形态仿同文件 `:361-389` 的 plan-mode GET；既有历史分页读取先例见 `lib/trajectory/history-fallback.ts:33`） | T0 |
| **T3** | UI 面板 + 挂载 + i18n | 4 个组件 + 双语词典 + 组件测试 | 新 `frontend/src/components/workspace/task-panel/{index,task-panel-header,task-panel-list,task-panel-item}.tsx`、`i18n/resources/{zh-CN,en-US}/workspace/panels-todos.ts`；改 `components/workspace/workspace-shell/main-section.tsx:418-446`、两侧 `workspace/index.ts` | T2 |
| **T4** | 打磨（可独立排期，非阻塞） | `todos` 工具行不再是无摘要的通用行 | 改 `frontend/src/lib/tool-row/kind.ts:13-85`、`lib/tool-row/state.ts:98-162`、`lib/thread-state/tools.ts:70-92`（读 `summary_lines`） | T3 |
| **T5** | **文档回填**（D5，非阻塞，但须在结项前完成） | 三处文档同步：缺口登记、优化计划状态、事件契约说明 | 改 `docs/plan/frontend-deepseek-harness-gap-list.md`（登记本缺口与交付态）、`docs/plan/frontend-deepseek-harness-optimization-plan.md`（P2-9 邻位补本子片状态）、`docs/plan/grok-harness-productization-implementation-plan.md:500`（`protocol_result` 契约处补 `todo_snapshot`：仅 `todos` 工具作用域出现、additive） | T1（键名已定稿，T1 合并后即可写） |

**范围控制**：T1 只改「放行」，不改生产侧 metadata 键名与 `compact_reconciliation` 的读取契约；T3 只加一个 DOM 节点，不动 composer 内部结构与 `pointer-events` 语义；T5 只改文档，不触碰代码与既有契约表述。

---

## 8. 验收标准

### 8.1 自动化（沿用仓库既有四件套口径）

| 侧 | 命令 | 期望 |
|---|---|---|
| 后端 | `go build ./...`、`go vet ./...`、`go test ./internal/agent/... ./internal/toolprotocol/... ./internal/toolkit/...` | 全绿；含 §5.4 新增用例 |
| 前端类型 | `npx tsc -b --force` | exit 0 |
| 前端 lint | `npx eslint <改动文件>`、`npm run lint` | 0 error（基线 warning 不新增）；i18n 扫描、行数门禁（≤500 非空行）、备份门禁、消息 token 门禁全过 |
| 前端单测 | `npm test` | 全绿，含新增 `todos.test.ts` / `use-session-todos.test.ts` / `task-panel` 组件测试（jsdom + `act` + `createRoot`，**不要引入 testing-library**） |
| 构建/e2e | `npm run build` → `npm run test:e2e` | **必须先 build 再跑 e2e**（e2e 只吃 `dist/` 产物，仓库已有一次假失败教训）；**本方案不新增 e2e 用例**（D3，已在文中写明理由）：面板数据依赖 live SSE 的 `tool.completed.payload.todo_snapshot`，e2e 跑的是静态 `dist/` 产物、无法稳定构造该事件，成本高于收益 → 覆盖改由组件测试 + §8.2 手工清单第 1–3、10–11 条承担 |
| 性能 / a11y（D6） | 代码审查 + `npm test`（jsdom 断言 + 手工核对） | 性能：`useMemo` 依赖仅含快照事件引用与历史内容，**非 `tool.completed` 的流更新不触发派生重算**（可在组件测试里用渲染计数断言）；列表渲染上限 8 项 + 页脚。a11y：进度摘要 `aria-live="polite"` 且只在计数/当前项变化时改文本、折叠按钮 `aria-expanded`/`aria-controls` 与列表 `id` 对应、状态语义不靠颜色单独表意 |

### 8.2 手工验收清单

1. 会话内模型首次调用 `todos` → 面板在 composer 上方出现，进度为 `0/N`，进行项高亮。
2. 状态推进 → 面板实时更新；完成后对应项变灰+删除线。
3. 全部完成 → 面板按既定策略隐藏（见 Q1）。
4. **刷新页面** → 若 T0/B1 或 B2 落地，面板恢复显示最近一次快照；未落地时**不显示**（不显示过期数据）。
5. 会话切换 → 列表整体替换，不串上一个会话的任务。
6. 折叠/展开 → 状态在会话内保持；键盘可达（`aria-expanded`）。
7. 窄屏（`sm` 断点下）→ 不遮挡输入框与审批条；审批条出现时两者叠放顺序正确。
8. 中英双语切换 → 文案无硬编码、无缺键。
9. 无 `todos` 调用的会话 → 面板完全不渲染（不占位、不产生空白间距）。
10. **性能**：在长会话里让模型持续流式输出，用 React DevTools Profiler 观察 → 面板**不随 token 重渲染**，仅在 `todos` 快照更新时渲染一次。
11. **a11y**：开启屏幕阅读器（Windows 讲述人 / VoiceOver）→ 任务计数变化时进度摘要只播报一次；Tab 到折叠按钮可用 Enter/Space 展开收起，展开后列表项能被顺序朗读。

---

## 9. 风险、决策记录与开放问题

### 9.1 已做出的关键决策（含理由）

| 决策 | 理由 |
|---|---|
| 只放行 `todos` 一个键，计数前端派生 | 白名单是跨工具共享命名空间；`total/pending/completed/session_id` 属通用名，全局放行会泄漏其它工具字段（§5.2） |
| A 通道发的是**面向前端的裁剪视图**：`items` 只含 `content/status/active_form` | 面板不需要时间戳；避免体积膨胀与无谓字段外泄。`created_at/updated_at/completed_at` 与 `storage_mode` 留在服务端（`todos.go:33-41`），需要时再按需追加 |
| 刷新后「无数据即不渲染」，不用本地旧值兜底 | 与仓库既有的「如实降级」纪律一致（如 goal 指示条刷新后不回落猜测值） |
| 面板放在审批条**之前**（DOM 顺序） | 审批是阻塞决策、贴近输入框；任务条是环境态（§6.1） |
| 不新增 SSE 通道、不新增全局 store | 现有流 + 一次派生足够；先例（plan mode / session goal）都不引入新通道 |
| A 通道键名**定稿 `todo_snapshot`**（D1） | 与生产侧 `metadata["todos"]`（`compact_reconciliation` 的读取契约）在命名上显式分离；名字不同＝不可互换，避免后来者「顺手统一命名」而改坏既有实现与测试 |
| 折叠态**按会话**记在 `sessionStorage`，不做跨会话共享（D2 结项定案） | 初稿的判断（「`sessionStorage` 在 `frontend/src` 0 命中、仓库无先例」）在实现期复核为**不构成阻塞**：面板虽在同会话内常驻挂载，但用户手动展开/收起后刷新或重挂载会丢失选择，持久化更贴合 §8.2 第 6 条「会话内保持」。实现为 2 个纯函数（`readTodoPanelCollapsed` / `writeTodoPanelCollapsed`）+ 会话前缀键隔离；写入只发生在点击回调内（不在渲染期 `setState`），异常路径回落默认收起，成本与回归面都可控，已由 `use-session-todos.test.tsx` 覆盖（含「重新挂载后仍是展开态」与「会话切换不串味」两条断言） |
| 新增事件键按 **additive** 处理，不改任何既有消费者（D4） | 事件载荷前后端均为宽松类型；旧消费者按未知字段忽略即可，唯一动作是 T5 补契约文档 |

### 9.2 风险

| 风险 | 影响 | 缓解 |
|---|---|---|
| B1 断言未命中（`/history` 消息不含工具 metadata） | 刷新后无列表 | 静态证据链已成立（§10.1 V6），剩余概率低；未命中则转 B2 只读接口（T2b），排期增加约一个后端 handler + 测试 |
| 快照进入每个 `tool.completed` 事件带来体积增长 | 大会话流带宽 | 已裁剪为三字段；若仍超标，进一步只发「与上一次相比变化的部分」——**不推荐**，会让前端失去自解释性，优先保留全量小视图 |
| `main-section.tsx` 是并行开发热点 | 挂载改动冲突 | 只加一个兄弟节点 + 一个 props 透传，控制 diff 面 |
| `goal_id` 维度不一致（子代理有独立列表） | 面板显示错会话的任务 | 快照带 `goal_id`；当前端能确定当前 goal 且不一致时**不渲染**，否则按「当前会话最近一次」展示（已知近似，记入文档） |
| 事件窗口 100 条裁掉旧快照 | 长会话尾部事件把快照挤出窗口 | B 通道兜底；A 通道仅保证近场实时性 |
| **事件新增 `todo_snapshot` 键影响其它消费者**（D4） | 旧消费者解析失败 / 契约漂移 | 结论：**additive，无需改任何既有消费者**——前后端事件载荷本就是宽松类型（`payload?: Record<string, unknown>`，§10.1 V4），chat-log 离线分析、审计轨迹与外部 API 消费方按未知字段忽略；唯一动作是文档同步（T5 在 `grok-harness-productization-implementation-plan.md:500` 的 `protocol_result` 契约处登记该键与「仅 `todos` 工具作用域出现」约束） |

### 9.3 开放问题（建议实现前与用户确认）

- **Q1**：全部任务完成后，面板是「立即隐藏」还是「保留数秒/保留到下一次调用」？（§6.3 默认立即隐藏）
- **Q2**：是否需要「点击任务项 → 跳转到对应工具行/消息」？（本方案明确不做）
- **Q3**：是否需要展示子代理（goal 级）的任务列表？（当前只做当前会话）

---

## 10. 完整性自审（2026-09-15，审查记录）

> 本节是**审查记录**，不属于方案本体（§1–§9 为被审对象）。结论：**结构与取证完整，可用于评审；进入实现前需消除内部矛盾并定稿若干待决项，无 blocker 级缺失。**

### 10.1 本轮审查新增的独立验证（均通过）

| # | 核对项 | 结论 | 证据 |
|---|---|---|---|
| V1 | §4.1 声称「生产端 Metadata 含全量快照」 | 成立 | `todos.go:231-241` 实测含 `"todos": todos` 与三态计数 |
| V2 | §1 声称「gap-list 未登记该缺口」 | 成立 | `docs/plan/frontend-deepseek-harness-gap-list.md` grep `todo|任务列表` **0 命中** |
| V3 | §1 引用的 P2-9 先例真实存在 | 成立 | `frontend-deepseek-harness-optimization-plan.md:3`（子片 1 后台任务常驻状态条、子片 3 会话目标四相指示，均已交付） |
| V4 | A 通道是否需要改前端 TS 类型 | **不需要** | `frontend/src/types/runtime/events.ts:1-10`：`payload?: Record<string, unknown>`，事件载荷本就宽松类型 |
| V5 | A 通道数据是否会被**客户端**二次裁剪 | **不会** | `api/runtime/sse.ts:231-238` 原样透传 `payload as SessionRuntimeEvent`；`lib/thread-state/history-artifacts.ts:160-176` 把 `events` 整体存入产物（含完整 payload） |
| V6 | **B1 的可行性（原定为「唯一未知量」）** | 已可由静态证据链确认，T0 应降级 | 见下 |
| V7 | 组件拆分是否满足 500 非空行门禁 | 满足 | §6.2 预估 120/90/110/60 + hook 60，均远低于阈值 |

**V6 证据链（B1：刷新后从历史取 `metadata.todos`）**

1. 工具结果 Metadata 进入 envelope：`agent/loop.go:2394-2399`（另有 `:2465-2470`、`:2628-2633`、`:2785-2789`、`tool_parallel_scheduler.go:283-287` 共 5 处同构）`gateway.Process(...)` → `result.Envelope = envelope`；
2. envelope.Metadata 逐键写入工具消息：`agent/loop.go:4221-4233`（`toolExecutionResultMessage`，与 `approved_tool.go:106-115` 同构）；
3. 消息 Metadata 参与历史序列化：`backend/internal/types/message.go:30`（`Metadata Metadata \`json:"metadata,omitempty"\``）；
4. `/history` 直接返回该批消息：`api/skills/handler.go:2629-2637`（`history: page.Messages`）；
5. 前端历史消息保留 metadata：`lib/thread-state/history-artifacts.ts:208-211`（`metadata: item.metadata ?? undefined`，仅过滤 `context_stage` / developer 消息，见 `:220-248`）；
6. 该链**已被既有实现消费**：`chat/compact_reconciliation.go:80-101` 正读 `message.Metadata["todos"]` + `goal_id`，并有测试夹具 `compact_reconciliation_test.go:18-21` 构造同形态工具消息。

> 因此 B1 不是「押运气」，而是**复用既有契约**。建议把 T0 从「30 分钟 devtools 手工探针」降级为「一条断言」——`curl .../sessions/{id}/history | jq '.. | .metadata?.todos? // empty'`，或直接由 T2 的单测夹具覆盖。**唯一尚未逐行追证的一环**是第 1 步中 `gateway.Process` 的入参 metadata 是否逐键等于工具结果 Metadata（本审查已定位到写入点，未回溯到 gateway 内部）；保留 5 分钟确认即可，不影响选型。**→ 本轮已落地**：§5.2 通道 B 与 §7 T0 已按此结论改写（T0 降级为一条 `curl | jq` 断言），§5.1 F7 同步改判。

### 10.2 内部矛盾（已在本轮就地修正）

| # | 冲突 | 处置 |
|---|---|---|
| M1 | §2.1 目标 2「全部完成时**仍可见**（便于回看）」 vs §6.3 可见性「全部 completed → **默认立即隐藏**」 | 已把 §2.1 目标 2 改为「由 **Q1** 决定（当前默认立即隐藏）」，消除二义，决策权回到 §9.3 |
| M2 | 计数口径三处不一致：§2.1「已完成 · 进行中 · 待处理」／§3.3「已完成 N · 进行中 N · 待处理 N，零值省略」／§6.3「已完成 N / 总数 M」 | 已统一 §6.3 为三态口径（零值分段省略），与 §2.1 目标 1、§3.3 参考实现一致 |
| M3 | §5.2 B2 返回体含 `counts`，却称「与通道 A 同构」（A 通道明确不放计数，计数前端派生） | 已改为 `{items:[...], goal_id, updated_at}`，计数同样由前端从 `items` 派生 |

---

### 10.3 未决 / 含糊项（审查时 6 项 → **本轮已全部按建议定稿**，落地记录见本节末表）

| # | 位置 | 问题 | 建议 |
|---|---|---|---|
| D1 | §5.2 A 通道 | 键名写作「`todo_snapshot`（或 `todos`）」，**两个选项并存**，实现者无法据此落笔 | 定稿 **`todo_snapshot`**：与生产侧 `metadata["todos"]`（`compact_reconciliation` 的读取契约）在命名上显式区分，防止后来者「顺手统一命名」而改坏 F2 的既有实现 |
| D2 | §6.3 | 折叠态用 `sessionStorage` 持久化——`frontend/src` 全量搜索 **0 命中**，仓库无先例；面板在同会话内是常驻挂载，本身无跨挂载持久化需求 | 初审建议「改为组件内 state」；**结项定案（2026-09-15）：保留实现侧的按会话 `sessionStorage`**——收益（刷新/重挂载后保持用户选择，贴合 §8.2 第 6 条「会话内保持」）大于成本（2 个纯函数 + 2 条测试），会话前缀键保证隔离、写入只在点击回调内；§6.3 / §9.1 已同步为实现口径 |
| D3 | §8.1 | e2e 只写了「必须先 `npm run build` 再跑」，**未定义是否新增用例** | 建议**不新增** e2e 并在文中写明理由（面板依赖 live SSE、e2e 只吃 `dist/` 产物，成本高于收益），改由组件测试 + §8.2 手工清单第 1–3 条覆盖 |
| D4 | §9.2 | 未评估「给事件新增 metadata 键」对**其它消费者**的影响（chat-log 离线分析、审计轨迹、外部 API 消费方） | 补一句结论：新增键是 **additive**，旧消费者按未知字段忽略；但事件契约文档需同步（见 D5） |
| D5 | §7 | T0–T4 **缺文档回填任务**（§1 已声明「需回填 gap-list」却无人认领） | 新增 **T5（文档）**：回填 `frontend-deepseek-harness-gap-list.md`、更新 `optimization-plan` 状态与 §9.4、补事件契约说明（`docs/plan/grok-harness-productization-implementation-plan.md:500` 正是 `protocol_result` 契约的登记处） |
| D6 | §8.1 / §8.2 | 缺两条工程口径：**性能**（每次 `tool.completed` 都触发派生与渲染）与 **a11y 动态播报**（进度变化时屏幕阅读器无感知） | 补：派生结果用 `useMemo` 缓存 + 复用 §5.3 的事件类型门控（只对 `tool.completed` 重算）；进度摘要容器加 `aria-live="polite"`（只在计数变化时播报，避免每次渲染都打断） |

**落地记录（2026-09-15，本轮优化 · 全部已写入 §1–§9 正文）**

| # | 落点 | 实际改动 |
|---|---|---|
| D1 | §5.2 通道 A、§9.1 | 键名**定稿 `todo_snapshot`**，删去「或 `todos`」二选一；决策表补一行「命名与生产侧 `metadata["todos"]` 分离」 |
| D2 | §6.3 可见性、§9.1 | 初审改写为**组件内 state**；实现交付后经结项复核**改回按会话 `sessionStorage`**（`readTodoPanelCollapsed` / `writeTodoPanelCollapsed` + 会话前缀键，读失败回落「默认收起」），§6.3 / §9.1 再次同步；差异与理由见 §11「与方案正文的差异」 |
| D3 | §8.1 | e2e 行写明**不新增用例**及理由（live SSE 依赖 vs `dist/` 静态产物），覆盖改由组件测试 + 手工清单承担 |
| D4 | §9.2、§9.1 | 新增「事件新增键的兼容性」风险行：**additive**、旧消费者忽略、无需改代码，唯一动作是 T5 补契约文档 |
| D5 | §7 | 新增 **T5 文档回填**任务（gap-list / optimization-plan / 事件契约），表头与「范围控制」同步 |
| D6 | §6.3、§8.1、§8.2 | 新增性能（`useMemo` + `tool.completed` 门控 + 渲染上限）与 a11y（`aria-live="polite"`、`aria-expanded`/`aria-controls`）口径，并落到自动化与手工验收项 |
| V6 | §5.1 F7、§5.2 通道 B、§7 T0 | B1 由「未知量」改判为「复用既有契约」，T0 从 30 分钟 devtools 探针**降级为一条 `curl \| jq` 断言** |
| M4 | §2.1 目标 5 | 顺手修正与仓库纪律冲突的表述：「Vitest + testing-library」→ **jsdom + `act` + `createRoot`**，并写明不使用 `@testing-library/react`（与 §8.1 一致） |

### 10.4 已覆盖、无需补的项（避免下一轮重复审查）

- 事件乱序 / 重连重复投递：`lib/thread-state/events.ts:371-378` 以 `payload.seq` 幂等去重，§5.3 再取最新 seq，两层已够。
- 事件窗口裁剪（100 条）：§5.1 F6 + §5.3 已明确由 B 通道兜底、B 未落地时如实降级。
- 工具行与面板并存：§3.4 已明确「不替换工具行」，T4 只做摘要打磨。
- goal 维度不一致：§9.2 已登记为「已知近似 + 不一致时不渲染」。
- 工程门禁：§8.1 已纳入行数门禁、i18n 扫描、备份门禁、消息 token 门禁与「先 build 再 e2e」教训。
- 安全面：A 通道只放行工具作用域的单个数组键，不放 `session_id`/`goal_id` 这类可被其它工具复用的通用名（§5.2 已论证）。

### 10.5 完整性判定

| 维度 | 判定 |
|---|---|
| 数据链路（生产 → 出口 → 投影 → 渲染） | 完整（§4/§5/§6 三段闭环，且 V5 证明客户端不会二次裁剪） |
| 参考实现可移植性 | 完整（§3 给出数据流、挂载语义、UI 结构三层对拍） |
| 实施可执行性 | **完整**：T0–T5 均有文件级落点、依赖与验收（T5 由 D5 补齐）；T0 已降级为一条断言（V6）；键名与折叠态已定稿（D1/D2） |
| 测试与验收 | **完整**：后端新增 + 回归、前端单测、手工清单十一条（含新增的性能与 a11y 两条）；e2e 已明确「不新增用例」并写明理由（D3） |
| 风险与决策 | **完整**（§9 三张表 + 3 个产品向开放问题）；兼容性结论已补：新增键 additive、无需改既有消费者（D4） |

**总评：无 blocker 级缺失，完整性足以支撑评审**。§10.2 的三处内部矛盾与 §10.3 的六项（D1–D6）**已全部落地**（另含 V6 的 T0 降级与 M4 的一处表述修正，见 10.3 落地记录）；当前仅剩 §9.3 三个**产品向**开放问题（Q1–Q3，不阻塞开工）与 T0 的 5 分钟 `gateway.Process` 透传确认，可直接进入 T1。

### 10.6 锚点逐条复核（两轮独立只读子代理 · 结果已回填）

**复核范围**：本文档内本仓库锚点（去重后 **55 处**；另约 10 处属参考仓库 `deepseek-harness`，按约定忽略）。两轮独立只读子代理，判定口径 `OK / 轻微偏移 / 不匹配 / 文件不存在`。

| 维度 | 结论 |
|---|---|
| 文件不存在 | **0** —— 唯一被引用的不存在路径 `jobs-status-bar.tsx` 由文档自身声明（§4.2、附录），本轮又在 §1 表格就地标注 |
| 行号越界 | **0** —— 最紧区间 `use-runtime-plan-mode.ts:79-263`（文件 264 行）仍有效 |
| 内容错配 / 伪造锚点 | **0** |
| 轻微偏移 / 语义不符 | **2**（下表 A1、A2），已修 |
| 口径瑕疵 | **2**（下表 A3、A4），已修 |

| # | 位置 | 问题（子代理证据） | 处置 |
|---|---|---|---|
| A1 | §4.1 语义差异行 | `todos.go:175-178, 251-275` 覆盖了调用点与软修复函数体，但**回报字段的写入点在 `242-246`**（`buildTodosResultMetadata` 内） | 锚点补为 `175-178, 242-246, 251-275` |
| A2 | §4.2 「摘要读取器」行 | `message-tool-row.tsx:166-173` 实际是 `<ToolRowPanels/>` JSX，不含摘要读取逻辑；正确落点是 `111-138`（`hasSummary` / `presentation.summary.parts`） | 锚点改为 `message-tool-row.tsx:111-138`；同行的 `tools.ts:70-92` 复核为 OK，保留 |
| A3 | §3.2 移植结论 | `main-section.tsx:418-425` 是**三元分支**：新建会话态为 `relative inset-auto`，非新建态才是 `absolute inset-x-0 bottom-0` | 措辞改为「非新建会话态」并列出两分支（面板仅在「有会话 + 有数据」时渲染，不影响挂载方案） |
| A4 | §4.2 i18n 行 | workspace 域实际是 10 个 `panels-*.ts` + `base.ts` + `index.ts`（共 12 个 `.ts`） | 措辞已按实测改准 |

**一处「不匹配」裁定为误报**：子代理报 `api/runtime/sessions.ts:361-389`「实为 plan mode」。经复核，该锚点在 §7 T2b 的用途是「**取数函数形态参照**」（plan-mode 的 GET/POST 读接口正是 B2 的同类先例），从未声称它是历史接口；已顺带补上既有历史分页读取先例 `lib/trajectory/history-fallback.ts:33`（`fetchSessionHistoryMessages`）以免后来者误读。

其余高频锚点（`todos.go:33-46/62-90/220-249/387-401/427-440`、`taxonomy.go:65`、`tool_runtime_events.go:46-84/667-693/969-981`、`tool_runtime_events_test.go:287-330`、`result.go:172-177/186-206`、`compact_reconciliation.go:76-101`、`contextmgr/compact.go:345-359`、`kind.ts:13-85`、`history-artifacts.ts:15/151-158/190-192/220-237`、`events.ts:387`、`use-session-runtime-stream.ts:66-80/311-314`、`jobs-status-bar.test.tsx:1-16`）两轮均判定 **OK**。

---

## 附：本方案引用的关键锚点

- 参考实现：`E:\projects\ai\deepseek-harness`（数据流 / 挂载 / 面板结构见 §3）
- 生产数据：`backend/internal/toolkit/tools/todos.go:33-90, 220-241, 387-401`
- 出口缺口：`backend/internal/agent/tool_runtime_events.go:46-84, 667-693, 969-981`、`backend/internal/toolprotocol/result.go:172-177, 186-206`
- 回读能力：`backend/internal/chat/compact_reconciliation.go:76-101`
- 前端缺口：`frontend/src/lib/thread-state/{events.ts:387, history-artifacts.ts:15,151-158,190-192}`、`frontend/src/hooks/workspace/use-session-runtime-stream.ts:66-80,311-314`
- 挂载与先例：`frontend/src/components/workspace/workspace-shell/main-section.tsx:418-446`、`components/workspace/{pending-interaction-bar.tsx, workspace-shell-topbar.tsx, jobs-status-bar.test.tsx}`、`hooks/workspace/use-runtime-plan-mode.ts:79-263`、`lib/session-goal/store.ts`

---

## 11. 实施记录（2026-09-15，T0–T3 + T5 已落地 · 工作树未提交）

| 任务 | 状态 | 落地内容 | 验证 |
|---|---|---|---|
| **T0** 通道确认 | 完成 | 按 V6 降级口径执行（不跑 devtools 探针）：静态核对生产侧 metadata 写入点（`internal/toolkit/tools/todos.go` → `buildTodosResultMetadata`）与 `tool.completed` 出口（`internal/agent/tool_runtime_events.go` → `attachProtocolResultToPayload`）→ 判定 **B1（复用既有出口）可用**，无需 B2 新接口 | 代码取证 + §5.1/§5.2 事实矩阵；后端新增用例覆盖 |
| **T1** 后端 additive 出口 | 完成 | `toolprotocol.Result.EventMapWithMetadataKeys`（按单次结果 opt-in，不并入共享 thin allowlist）+ `agent` 侧 `attachTodoSnapshotToProtocolResult`（仅工具名等价 `todos` 时裁剪 `content/status/active_form` + owner id；空 `content` / 未知 `status` 丢弃；**不改写生产侧 metadata**） | `go build ./...` / `go vet ./...` / `go test ./internal/agent/... ./internal/toolprotocol/... ./internal/toolkit/...` 全绿；含「非 `todos` 工具不得出现该键」「生产侧 metadata 不被改写」用例 |
| **T2** 前端投影 | 完成 | `lib/thread-state/todos.ts`（派生 / 合并 / 折叠）+ `hooks/workspace/use-session-todos.ts`（可见性 / 三态计数 / 当前项 / 折叠态）；`lib/thread-state/events.ts` 接入折叠调用，私有文本工具搬迁到 `lib/thread-state/text-utils.ts`（语义不变，过 ≤500 非空行门禁）；`history-artifacts.ts` 补**通道 B1** 历史兜底 | `todos.test.ts` 17 例、`use-session-todos.test.tsx` 6 例 |
| **T2b** 只读接口 | **不需要** | T0 判定 B1 可用，未新增任何后端读接口 | — |
| **T3** UI 面板 + 挂载 + i18n | 完成 | `components/workspace/task-panel/{index,task-panel-header,task-panel-list,task-panel-item}.tsx`；`workspace-shell/main-section.tsx` 在 `chatSurfaceVisible` 分支、`PendingInteractionBar` **之前**挂载（复用既有吸底 overlay）；双语 `panels.todos.*` 逐键对齐；`data/mock/types.ts` 补可选 `todoSnapshot` | `task-panel.test.tsx` 7 例（jsdom + `act` + `createRoot`，未引入 testing-library）；`npm run build` exit 0 |
| **T4** 工具行摘要打磨 | **未做（可选，非阻塞）** | 留待独立排期（`lib/tool-row/*`） | — |
| **T5** 文档回填 | 完成 | ① `frontend-deepseek-harness-gap-list.md` 新增 `### 批次 18（2026-09-15）` 并补 §2 B5 行的子片 4；② `frontend-deepseek-harness-optimization-plan.md` 状态头 + §9.3 台账行；③ `grok-harness-productization-implementation-plan.md` §C1 `protocol_result` 契约处补 `todo_snapshot`（additive、仅 `todos` 工具作用域）+ 变更记录行 | 三处文档 diff 已复核 |
| **R1** 现场回归修复（2026-09-15 晚） | 完成 | 真实会话刷新后面板不显示：通道 B1 改读生产形状 `metadata.tool_metadata.todos`（保留平铺 `metadata.todos` 兼容回退），并从同一嵌套包取 `session_id` / `goal_id`；`todos.test.ts` 夹具换成生产形状 + 新增 3 例（实测载荷逐字回归 / 平铺兼容 / `tool_metadata` 无 `todos` 键时不误读其它工具）；`history-projection.test.ts` 新增通道 B1 集成用例 | `npx vitest run src/lib/thread-state` **93 例全绿**；`npm test` **216 文件 / 1679 例全绿**；`npx tsc -b --force` exit 0；`npm run lint` 0 error / 1 基线 warning |

**与方案正文的差异（实现口径）**：§5.3 写的 `deriveLatestTodos(events, historyContent?)` 在实现中拆成三个纯函数——`deriveTodoSnapshotFromRuntimeEvent`（单事件解析）、`deriveLatestTodosFromRuntimeEvents`（事件序列取最新）、`deriveLatestTodosFromHistoryMessages`（历史兜底），职责与断言口径不变、便于单测；折叠调用集中在 `applyTodoSnapshotToThread`，并保证「与 todo 无关的事件返回入参同一引用」，不打断下游 memo。

**门禁证据（工作树，2026-09-15）**：`npx tsc -b --force` exit 0；`npm run build`（`tsc -b && vite build`）exit 0；`npm run lint` **0 error / 1 基线 warning**（`artifact-detail-dialog.tsx:50` 既有 `react-hooks/exhaustive-deps`；i18n scanned=684 / violations=0、备份门禁 1035 文件 0 残留、行数门禁 973 个 `.ts/.tsx` 中 0 个 > 500（最大 `hooks/workspace/use-session-runtime-stream.ts`=500）、消息 token 门禁 37 文件 0 处）；`npm test` **216 文件 / 1669 用例全绿**（本批新增 30 例：`todos.test.ts` 17、`use-session-todos.test.tsx` 6、`task-panel.test.tsx` 7）；后端 `go build ./...` / `go vet ./...` / `go test ./internal/agent/... ./internal/toolprotocol/... ./internal/toolkit/...` 全绿。

**e2e**：按 D3 **不新增用例、未改任何既有 e2e 文件**（实现期间创建的两个临时探针脚本已在收尾时删除）。全量 `npm run test:e2e` 的失败项已全部定性为**基线既有**：`live-delta.spec.ts:85` / `thread-link.spec.ts:52` / `thread-link.spec.ts:116` 在干净 HEAD `2d6dd61c` 独立 worktree + 独立 fresh build 上**同样失败**（同断言、同超时）；`trajectory.spec.ts:71`（P2-1 搜索过滤）同口径在 HEAD 基线复现；`workspace-chat.spec.ts:205`（P1-3a 阅读位置保持）为**时序敏感 flaky**——`.artifacts/e2e-failures/` 存有 2026-09-14 起的多次失败留图（18:21 / 21:11 / 22:36 / 23:38 等，均早于本批），本批 `--repeat-each=3` **3 passed**、干净 HEAD 单跑亦通过。未修任何基线红项（不在本方案范围）。

**交付物清单（工作树未提交）**：后端 4 文件（`internal/toolprotocol/result.go` + `result_test.go`、`internal/agent/tool_runtime_events.go` + `tool_runtime_events_test.go`）；前端**新增 11 个文件**（`components/workspace/task-panel/` 4 组件 + 组件测试、`lib/thread-state/todos.ts` + `todos.test.ts`、`hooks/workspace/use-session-todos.ts` + `use-session-todos.test.tsx`、双语 `i18n/resources/{zh-CN,en-US}/workspace/panels-todos.ts`）；前端**修改 7 个文件**（`workspace-shell/main-section.tsx`、`data/mock/types.ts`、两侧 `workspace/index.ts`、`lib/thread-state/events.ts`、`history-artifacts.ts`、`text-utils.ts`）；文档 4 篇（本方案 + 缺口清单 + 优化计划 + `grok-harness` 事件契约）。
