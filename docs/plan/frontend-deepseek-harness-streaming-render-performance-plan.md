# 前端万级流事件渲染性能方案（deepseek-harness 参考实现取证）

> 状态：**P0-1 / P0-2 / P1-1 / P1-2 已实施并验证（2026-09-28，见 §11）；P2-1 渲染面逐项核对完成（无代码改动）；P2-2 基准方法论已固化（基线待空闲窗口复跑回填）；P2-3 性能决策记录已建立；P1-3 起待立项或按需推进**
> 日期：2026-09-28
> 参考仓库：`E:\projects\ai\deepseek-harness`（master @ `c291e7961a`，2026-09-10）
> 目标仓库：本仓库 `frontend/`
> 关联文档：同目录 `frontend-deepseek-harness-optimization-plan.md`、`frontend-deepseek-harness-gap-list.md`
> 取证方式：目标仓库源码直读 + 其 `.agents/notes/*` 设计笔记 + 其 `benchmarks/*` 说明；**本轮未运行其基准**，文中引用数字均为其记录值，仅作参考量级。

## 1. 结论摘要

一个流式回合产生上万条事件时，压垮前端的从来不是"某个组件写得慢"，而是三条放大链：

1. **摄入**：逐条消费的队列如果用 `Array.prototype.shift()`，排空 N 条是 O(N²) 引用搬运，会把同一事件循环上的所有工作一起拖死；
2. **通知/发布**：每条事件都触发一次 React 更新 → N 条事件 = N 次渲染 = N 次协调，风暴不可免；
3. **渲染**：每次更新重解析全量 Markdown、重渲染整条会话列表 → 单帧代价随历史长度增长。

deepseek-harness 的答案是一条**五级闸门 + 一层工程化配套**，每一级独立成立、独立可验证：

| 层 | 机制 | 主要落点（参考仓库） | 效果（其记录） |
|---|---|---|---|
| L1 排队 | 环形双端队列线性排空；容量/合并/拒绝属消费方策略 | `packages/util/deque`；`gateway/src/client/stream-client.ts`（`RemoteStreamMuxClient` / `StreamInbox`） | 200 万条排空 ≈9.66ms（≈4.8ns/条，Node 26 arm64 参考机），消除 `shift()` 的 O(N²) |
| L2 窗口 | 事件窗口 = rope + `revision` + `change` 增量描述；`entries` 惰性物化 | `session-controller/src/client/contract/events.ts` | 消费方按 delta 处理，不为每个新窗口重建/重扫历史 |
| L3 通知 | 结构更新走 microtask、流更新走 rAF 去重；发布分级 `none / animation-frame / immediate`，高频流**跨三次绘制机会**才发布 | `session-controller/src/client/sessions/notifier.ts`；`ui-conversation/.../assembly.ts` | ≤1 次/帧；60fps 下高频流 ≈20Hz 上限；无订阅者不 flush |
| L4 订阅 | per-key 源花名册（整表 / 单节点 / 单过程）+ 全客户端唯一 uSES 桥 | `ui-session/src/client/index.ts`；`ui-renderer/src/client/bind.ts`、`scoped-slots.tsx`；`ui-chat/src/client/apply.ts` | 谁的分片变化谁才重渲染，把提交的爆炸半径压到组件级 |
| L5 渲染 | 回合过程折叠、增量 mdast（冻结块不重解析）、行级 memo、轨迹表局部虚拟化、瞬态帧与持久结算分离 | `ui-chat`、`ui-conversation`、`ui-trajectory` | 每帧工作量 ∝ 尾部活动块，与历史长度解耦 |

**对我们最重要的一条**：最大差距不在渲染技巧，而在 **L3+L4——"一次流式提交的爆炸半径"**。参考实现让每次发布只触及按键订阅的组件；我们目前每次页面级提交都会改写 thread state，`WorkspacePage → WorkspaceShell → 侧栏/面板/消息列`整棵树重渲染（该结论来自我们自己的源码注释与 CDP 实测，见 §9.1）。因此照搬它的"3 帧发布门"没有意义——必须先做订阅切片，再谈放宽发布频率。

## 2. 问题模型：万级事件压垮前端的三条路径

- **路径 A（平方级排空）**：网络/进程侧积压（断线重连、后台标签页、消费慢）后一次性涌入数千帧，逐帧 `shift()` 排空。参考仓库把它当**生产事故**处理（Issue #3270，note `2026-08-28-linear-stream-queue-drain.md`），修复在 L1。
- **路径 B（通知-发布风暴）**：每条 delta 一次 setState / 一次发布。6000 条 delta 按 60 次/秒发布是 100 秒风暴，页面持续被 React 占住。修复在 L3：合帧 + 分级 + 去重 + 惰性。
- **路径 C（整树渲染 + 全量重解析）**：每次发布重渲染整棵页面树、重解析整段 Markdown。修复在 L4（订阅切片）+ L5（折叠、增量渲染、memo）。

三条路径互不替代：只修 A，风暴仍卡；只修 B，历史长了单帧仍重；只修 C，积压时仍 O(N²)。下面的每一级都能单独对照落地。

## 3. L1 摄入与排队：线性排空是底线

### 3.1 传输形态（先澄清前提）

参考实现的浏览器端高频流**不是 EventSource**，而是自有的 WebSocket 多路复用：

- `packages/api/gateway/src/client/stream-client.ts` —— `RemoteStreamMuxClient`，注释原文：*"Keep one physical WebSocket and share it among independently cancellable Remote streams."* 一条物理连接承载多个可独立取消的逻辑流；
- 每条逻辑流一个 `StreamInbox`：`private readonly frames = new Deque<RemoteStreamServerMessage>()` + `wake` 唤醒器 + `failure` 失败态；
- 入队/唤醒是一回事，"怎么消费、要不要丢/合"是另一回事——见 3.3 的边界哲学。

对我们的含义：传输层（我们为 fetch 式 SSE）**不必照搬** WS mux；关键是入队之后的排空与消费策略。

### 3.2 环形双端队列（`packages/util/deque`）

- 数据结构：环形数组 + 头尾索引；`pushBack / pushFront / popFront` 均为 O(1) 摊销；
- 移除即 **立即清槽**（被移出的引用当即可被 GC 回收，不残留到下一次扩容覆盖）；
- 扩容：满则容量翻倍；未满且元素数降到 1/4 容量时减半（避免长期占用）；
- 零依赖模块，附 README 与基准。

事故与数据（note `archived/bug-fix/2026-08-28-linear-stream-queue-drain.md` + deque README）：

- **问题**：`Array.prototype.shift()` 逐帧排空 N 条积压帧 = 每次搬移剩余全部引用，总引用搬运量 O(N²)；线上表现为 `ArrayShift` / `MoveRange` / `memmove` 占满火焰图主导栈，**把相邻的事件循环工作（含渲染）一起拖住**；
- **实测**：2,000,000 条排空 ≈ **9.656ms**（≈4.8ns/条；Node 26，arm64 参考机），随规模近似线性。

消费者清单（note 原文，全部换成同一双端队列）：

- Host Remote 事件源；
- 每个已连接 Client 的 Remote 事件队列；
- **浏览器 Remote stream inbox**；
- 每个 Session 的 history follower；
- Session control stream；
- Workspace follower store。

### 3.3 边界哲学：通用集合只保 O(1) 与生命周期

Deque 不做背压、不做合并、不做丢弃——**容量、coalescing、拒绝策略全部由消费方决定**。这样：

- 通用组件的行为可预测（不会"偷偷丢帧"改变语义）；
- 不同消费方可以有不同的过载语义（事件源要全量保序；控制流可合并；UI 面包屑可丢旧）；
- 性能问题在唯一负责语义的那一层解决。

> 对照检查项（我们）：审计 `frontend/src/api/runtime/sse.ts` 与 runtime 事件消费路径是否存在 `shift()` / `splice(0, n)` / 大数组整体拷贝的逐条消费循环。

## 4. L2 事件窗口：rope + 增量描述符，entries 惰性物化

落点：`packages/api/session-controller/src/client/contract/events.ts`。

- 事件窗口是一棵 **rope**：`EventWindowNode = { kind: 'leaf', entries, length } | { kind: 'concat', left, right, length }`，`length` 在拼接时 O(1) 维护；
- 对外快照 `SessionEventWindow = { entries, hasMore, revision, change }`，其中：
  - `revision`：单调递增，订阅方据此判断"这是新窗口"；
  - `change`：**同步增量描述符**，形如 `append / prepend / replace / settle-assistant`（结算场景携带 attemptId 与可选结算条目）；
  - `entries` 是 **getter**：`get entries() { entries ??= materialize(node) }` —— 窗口发布本身**不展开**，只有消费方真的要全量列表时才物化（`materialize` 对 concat 树做一次迭代展开）；
- `MutableSessionEventSource`：每次接受的窗口变更 `revision + 1` 并同步发布（注释：*"every accepted window mutation publishes synchronously"*）——发布是同步的，**合帧责任在消费方（L3）**；
- `settleAssistant(attemptId, entry?)`：把该 attempt 的瞬态行过滤掉、把持久结算按 `seq` 插到正确位置——**重连/结算不做全量重建**。

消费方（会话装配器 `ui-conversation/src/client/conversation/assembly.ts`）直接按 `change` 处理增量。设计要点：

1. 高频路径是"追加"，window 用 rope 表达 O(1) 拼接；
2. 低频路径（重连、结算）有语义化的描述符，不必靠"重新拿全量数组 diff";
3. 即使某消费方需要全量（如导出、测试），物化成本被推迟到真正需要时。

> 对照检查项（我们）：我们已有 live 揭示通道（`frontend/src/lib/live-stream-text.ts`）承担"正在揭示的文本"，与本节思路同源（瞬态与持久分离，见 §7.4）。若后续要承载**万级历史**的随机访问/回补，可再评估引入 revision + change 窗口与惰性物化。

## 5. L3 通知与发布节奏：分档合帧（性能核心）

### 5.1 Notifier：结构 microtask / 流 rAF / 惰性 / 同步逃逸口

落点：`packages/api/session-controller/src/client/sessions/notifier.ts`（全文件 97 行，逻辑完整）：

- `markDirty()`：结构更新 → 标 dirty + 排一个 **microtask**；已排 microtask 则直接返回（去重）；
- `markFrameDirty()`：流更新 → 标 dirty + 排 **rAF**（每帧至多一次）；`scheduled !== 'none'` 直接返回；无 rAF 环境（Node 单测）落 microtask；
- `flush()` 的**惰性**：`if (this.listeners.size === 0) return` —— 没有订阅者就根本不重建、不发布；dirty 保留，下一次 `getSnapshot()` 经 `ensureFresh()` 同步重建（读路径兜底，保证正确性）；
- `notifyNow()`：**同步逃逸口**，只给受控输入用。注释原文理由：*"controlled-input writes must notify in the same tick as onChange, or React rolls the DOM back to the stale value and the caret jumps to the end."* —— 这是被真实 bug 教育出来的例外；
- `scheduleGeneration` 世代号：`notifyNow` 取消挂起帧时使旧回调作废，避免"延迟回调发布陈旧快照"。

### 5.2 发布分级 + 三帧门：高频流 ≈20Hz 上限

落点：`packages/client/ui-conversation/src/client/conversation/assembly.ts:130-147`。

- 发布级别三档：`none < animation-frame < immediate`；
- `animation-frame`：若已有挂起帧直接 return；否则**嵌套三层 rAF** 后才发布，注释原文：*"Cross three paint opportunities before publishing high-frequency stream updates."* —— 60fps 下高频流发布频率上限 ≈ **20Hz**（按帧数而非固定 Hz，高刷屏上限相应更高）；
- `immediate`：取消挂起帧并**立即同步发布**（用户直接动作、resync、settle 等不允许延迟的路径）；
- 一批 append 的发布级别取其中**最高**级别（immediate 一旦出现即立即发布）。

### 5.3 哪些事件值得上屏：节点级 publication 声明

落点：`packages/client/ui-chat/src/client/conversation-nodes/*`。每个节点显式声明自己的发布策略，而不是全局一刀切：

- `assistant.ts:321-323`：`step/start → 'none'`；`assistant/live-chunk` 按 chunk 类型分级（usage / finish 等仅状态更新 → `none`；正文/推理增量 → `animation-frame`）；其余事件 → `immediate`；
- `turn-process.ts:240-241`：`live-chunk` 同样按 chunk 类型分级，未命中 → `immediate`；
- `inbox.ts:125`：所有事件 `→ 'none'`（纯数据面，不驱动上屏）；
- `turn-tail.ts:186`：仅 `turn/end → 'immediate'`，其余 `none`。

**这张节点级策略表是"万级事件只有尾部少数块上屏"的制度保证**：高频低价值事件（usage、step 开始）根本不会触发发布；高价值事件（正文增量）被合帧；关键节点（结算、回合结束）享受同步发布。

### 5.4 客户端 store 引擎：rafBatch 与防饿死

落点：`packages/client/store/src/index.ts`。

- store 引擎 = zustand vanilla + immer + subscribeWithSelector + **rafBatch**；注释原文：*"Batches subscriber notification into one flush per animation frame."* —— N 次变更 = 每帧 1 次通知；无 rAF 环境回退 microtask（同样保证"每 tick 1 次"契约）；
- `notifySubscribers()` 在派发前**拷贝监听器集合**，注释：*"without allowing one callback to starve the rest."* —— 单个慢订阅者不会拖饿其他订阅者；
- 引擎本身 React-free：对外只暴露 observable（subscribe/getSnapshot/update/set），hook 合成归 ui-renderer（见 §6）；
- projection store 按 key 分通道，每通道各自 Notifier + observable face。

> 对照检查项（我们）：我们已有 rAF 合帧（`streaming-frame.ts`）与两层最小提交间隔，但**缺少三件事**：① "无订阅者不重建"的惰性语义；② 分事件类型的发布分级（现在是全局 120ms 节流）；③ 同步逃逸口规则的显式化（我们的 `flush({force:true})` 方向一致，可对照其"受控输入必须同 tick"的判据补齐）。

## 6. L4 订阅粒度：per-key 源花名册 + 唯一 uSES 桥

这是让 §5 的合帧真正省下来的前提：**发布一次，不等于重渲染一棵树**。

### 6.1 全客户端唯一的 hook 构造器

落点：`packages/client/ui-renderer/src/client/bind.ts`。

- `bindSnapshotSelector(w)`：把任意裸 observable（subscribe/getSnapshot）桥成 `useSyncExternalStoreWithSelector` 风格的 hook；subscribe/getSnapshot 按 source 捕获、按 source 缓存（binding site 缓存）；
- 全仓库只有这一处 uSES 桥——**订阅语义统一，不存在多套自研订阅各自出 bug**。

### 6.2 会话源花名册：hooks / keyedHooks / props 三类

落点：`packages/client/ui-session/src/client/index.ts:148-205`。

- 每个会话视图可声明三类源：`hooks`（整表级 observable）、`keyedHooks`（按 key 解析的 observable）、`props`（静态注入）；
- 内置源：`hooks: ['session']`、`keyedHooks: ['projection']`（`key => binding.session.projections.faceOf(key)`）；
- 物化：`materialize(binding)` 校验并拷贝所有贡献方的声明（`copyDeclared`），挂到 `bindStoreScope`；
- 组件在 `ui-renderer/src/client/scoped-slots.tsx` 里被 `bindInjectSources` 绑定：`hooks` 直接成组件 props 上的 hook，`keyedHooks` 在**每个 key 上**生成独立订阅源。

### 6.3 实例：聊天节点级订阅

落点：`packages/client/ui-chat/src/client/apply.ts`。

- `chatSource(binding)`：按 binding 用 `WeakMap` 缓存整表 chat 快照源；
- 会话注册：`ctx.uiSession.provide({ hooks: ['chat'], resolve: binding => ({ hooks: { chat: chatSource(binding) } }) })`；
- 视图 inject（apply.ts:113-119）声明 **keyed hooks**：

  ```ts
  keyedHooks: {
    chatNode: key => chat.getSnapshot().nodes.source(key),
    chatNodeProcess: key => chat.getSnapshot().nodes.processSource(key),
  }
  ```

- 于是聊天区渲染时，每个消息节点、每个"过程"（推理/工具组）各自订阅自己的源：**流式期间真正重渲染的只是活动节点的那几个订阅者**，整表 `chat` 源只被需要全局信息的组件订阅；
- 关键设计：`nodes.source(key)` 是**per-node 稳定 identity 的 observable**（节点创建时注册、结算后内容冻结），所以行组件可以安全 memo（见 §7.3）。

> 对照检查项（我们）：我们若干旁路 store（`lib/live-diagnostics/store.ts`、`lib/session-goal/store.ts`、`session-turn-registry.ts`、`use-trajectory-snapshot.ts` 等）已用 `zustand/useSyncExternalStore/subscribeWithSelector` 型订阅；但**主聊天流式提交仍写页面级 thread state，触发工作区整树重渲染**（见 §9.1）。把"整表 / 单节点 / 单过程"三级粒度引进主聊天路径，是收益最大的单项改造。

## 7. L5 渲染面收缩：折叠、增量 Markdown、memo、局部虚拟化、瞬态/持久分离

### 7.1 回合过程折叠（note `2026-08-14-web-turn-process-folding.md`）

- 推理、工具调用、系统提示收进"过程"区（默认折叠/摘要化），默认渲染面 = 最终正文 + 活动块；
- 效果：**静止的历史行退出渲染面**——这是数量级闸门：历史越长，渲染面不随之增长。

### 7.2 增量 Markdown（note `2026-08-06-web-markdown-incremental-ast-renderer.md`）

- mdast 直渲染器（不经过 HTML 字符串中转）；
- **已冻结块不重解析**：只重解尾部活动块；冻结元素缓存按"源偏移"key；
- 与流式节奏配合：每个合帧周期只有"最后一块"是脏的。

### 7.3 行级 memo 与稳定 props

- `AssistantMarkdown`、`MessageItem`、`CompactionItem` 等行组件全部 `memo`，props 保持稳定（含 locale revision 这类全局值走稳定引用）；
- 节点级订阅（§6.3）+ 行级 memo 组合后：一次流式发布重渲染的组件数 ≈ 活动节点数（常数），而非消息数。

### 7.4 瞬态帧与持久结算分离（note `2026-08-31-live-assistant-stream-frames.md`）

- 每个 chunk 的 live 帧是**瞬态 Client entries**；持久 settlement 到达后按 `seq` **有界展开替换**（配合 §4 的 `settle-assistant`）；
- 重连携带 baseline 收敛：**不重建已结算历史**；
- 语义收益：UI 看到的是"一回合一条持久消息 + 尾部瞬态增量"，而不是"逐 chunk 追加的伪消息"。

### 7.5 虚拟化的使用边界

在 `packages/client` 全量搜索 `virtuali|overscan`，命中仅 `ui-trajectory`（`TrajectoryTable.tsx` + `trajectory-virtual-rows.ts`）。

- **Trajectory 表做窗口化**（逻辑行索引 + overscan）；
- **Chat 消息列表不做窗口化**——依赖折叠 + memo + 增量 Markdown 把每帧代价压到"尾部可见块"，而不是靠虚拟滚动。这是一个刻意取舍：聊天区有富交互（选中、滚动锚定、图片、折叠动画），虚拟化的复杂度/回归风险高于收益；
- 我们的现状与其一致：已对 Trajectory（`trajectory-virtual-rows.ts`）、Diff 行列表（`diff/virtual-line-list.tsx`）、文件浏览器（`file-browser/tree-list.tsx`）做虚拟化，Chat 消息列未窗口化——**方向正确，无需为"对齐"而虚拟化聊天**。

## 8. 工程化配套：设计笔记制度 + 基准预算

参考实现把性能治理当作**制度**而非一次性优化，两部分值得借鉴：

### 8.1 设计笔记（`.agents/notes/`）

- 目录按状态分：`archived / implemented / planned`，按类型分：`bug-fix / architecture / feature / testing`；
- 每篇结构基本为：Problem（问题/事故）→ Decision（决策与理由）→ 验证（基准/实测数字）→ 后果与边界；
- 本文引用的 5 篇即证据：排空（bug-fix）、增量 markdown（architecture）、过程折叠（feature）、live 帧分离（architecture）、性能预算（testing）；
- 收益：**每个性能决策可追溯、可反驳、可回滚**；新人从 notes 就能理解"为什么代码长这样"。

### 8.2 基准与预算

- `benchmarks/long-session-browser/`：合成 **240 回合**长会话，一条串行 Chromium 工作流——冷开 → 翻旧页 → 首次激活 Trajectory → 回 Chat → 配速流式回复 + 真实键盘输入；
- `benchmarks/active-stream-reconnect/`：重连携带 baseline 时，生产 Client 折叠路径的性能；
- 两条基准进**独立浏览器 CI lane**；
- 纪律（note `2026-09-06-frontend-performance-budgets.md`）：预算只锁定现状、**只在重复测量证明收益后才收紧**——预算不是 KPI，防止"为预算而优化"；且笔记明确自认不覆盖：小时级 soak、GPU 呈现、真实模型延迟等边界。

> 对照检查项（我们）：我们已有手动探针 `e2e/zz-perf-probe.manual.ts`（曾用于量化 §9.1 的整树成本），方向一致；缺的是"固化为 CI 基准 + 先校准后收紧"的制度化。

## 9. 对照 ai-agent-runtime：现状盘点与可落地建议

### 9.1 现状盘点（本仓库，已核实）

**已具备**

| 能力 | 落点 | 说明 |
|---|---|---|
| rAF 合帧调度 | `frontend/src/hooks/workspace/agent-chat-turn/streaming-frame.ts` | rAF 节流；后台标签页 `setTimeout(100ms)` 兜底；可见性恢复立即 flush |
| 页面级最小提交间隔 | 同上，`MIN_COMMIT_INTERVAL_MS = 120` | 页面级提交 ≥120ms 间隔（≈8 次/秒） |
| store 正文副本节流 | 同上，`STRUCTURAL_COMMIT_INTERVAL_MS = 1000` | store 里的正文副本 1 次/秒；豁免：首块期无可见正文、`flush({force:true})` 里程碑 |
| live 揭示通道 | `frontend/src/lib/live-stream-text.ts` | "正在揭示的文本"旁路——瞬态/持久分离的雏形（对齐参考 §7.4） |
| 选择器订阅（部分） | `lib/live-diagnostics/store.ts`、`lib/session-goal/store.ts`、`agent-chat-turn/session-turn-registry.ts`、`use-trajectory-snapshot.ts` 等 | 已用 `zustand/useSyncExternalStore/subscribeWithSelector` 型订阅 |
| 虚拟化（部分） | `trajectory/trajectory-virtual-rows.ts`、`diff/virtual-line-list.tsx`、`file-browser/tree-list.tsx` | 与参考一致：轨迹/长列表虚拟化，Chat 不虚拟化 |
| 手动性能探针 | `e2e/zz-perf-probe.manual.ts` | 已用于量化下述整树成本 |

**根因差距（我们自己的实测，来自 `streaming-frame.ts` 注释）**

- 文本/推理 delta 的每次提交都会改写**页面级 thread state**，导致 `WorkspacePage → WorkspaceShell → 侧栏 / 面板 / 消息列`**整棵树重渲染**；
- CDP CPU profile（按 rAF 节奏提交时）：流式期间约 **330ms/s** 主线程花在这条链上（i18next 每帧几十次 `t()`、React 全页协调、RecalcStyle ≈80ms/s、Layout ≈30ms/s）；长会话下还会出现 **150-200ms 长任务**（用户可见的"整页卡住"）；
- 把结构提交间隔从 120ms 放到 1000ms：`ScriptDur 3.167s → 1.049s（−67%）`——证明成本在**提交的爆炸半径**，不在流本身。

**五级差距对照**

| 层 | 参考实现 | 我们 | 差距 |
|---|---|---|---|
| L1 排队 | Deque 线性排空 | `api/runtime/sse.ts` 自研解析；排空结构待审计 | ⚠️ 待审计（`shift()`/拷贝风险） |
| L2 窗口 | revision + change + 惰性物化 | live/持久分离已有雏形；无窗口协议 | ◐ 部分覆盖，万级历史场景待评估 |
| L3 通知 | 惰性 + 分级 + 帧门 + 同步逃逸口 | rAF 合帧 + 时间节流（120ms/1000ms） | ◐ 有合帧，缺惰性/分级 |
| L4 订阅 | per-key 三级粒度 | 旁路 store 有切片；主聊天路径无 | ❌ **最大差距** |
| L5 渲染 | 折叠 + 增量 mdast + memo | 折叠方向一致；memo/增量策略待逐项核对 | ◐ 待核对 |
| 工程化 | notes 制度 + CI 预算 | 手动探针 | ❌ 缺制度化 |

### 9.2 建议清单（按实施顺序）

**P0-1 排空复杂度审计**（低风险，先行）

- 动作：审计 `frontend/src/api/runtime/sse.ts` 与 runtime/chat 双通道消费循环，排查 `shift()` / `splice(0, n)` / 大数组整体拷贝的逐条消费；如有，改为索引游标或环形队列。
- 依据：§3；我们已有重连循环（`use-session-runtime-stream.ts`）与后台标签页场景，积压一次性涌入是现实路径。
- 预期：积压恢复时间 O(N²) → O(N)。
- 验证：单测模拟 10 万条积压排空耗时（应近似线性）；重连压测。

**P0-2 惰性通知语义**（低风险）

- 动作：thread store / 各 observable 源引入"无订阅者不 rebuild、读时 `ensureFresh` 兜底"语义；隐藏标签页零工作。
- 依据：§5.1（`flush()` 的 `listeners.size === 0` 早退）。
- 预期：不可见页面/未挂载视图不产生渲染与重建成本。
- 验证：隐藏标签页 CPU 对比；单测覆盖"读路径兜底重建"。
- 注意：读取路径必须能同步重建（参考 `ensureFresh`），否则会出现陈旧快照。

**P1-1 订阅切片（最大杠杆，需立项）**

- 动作：把主聊天流式提交从"页面级 thread state 整树重渲染"迁到**按会话/按节点 observable 分片**（对齐参考的三级粒度：整表 / 单节点 / 单过程）；消息行组件订阅各自节点源。
- 依据：§6；我们的 330ms/s 与长任务实测即整树成本。
- 预期：单帧重渲染面从"工作区整树"降到"活动节点"；之后可把 `MIN_COMMIT_INTERVAL_MS` 放宽回帧对齐（平滑度提升）而总成本下降。
- 验证：`zz-perf-probe`（ScriptDur / RecalcStyle / Layout / 长任务数）+ React Profiler 重渲染组件计数；与当前 120ms 节流做 A/B。
- 风险：中高（状态架构改动）。建议以"消息列"为窄切面试点，thread state 保留为骨架，逐步迁移，避免大爆炸式重构。

**P1-2 发布分级 + 帧门**（中风险，需与 P1-1 配合）

- 动作：为流事件类型声明 publication 策略：usage / finish → `none`；正文 / 推理增量 → 帧门（可先 2-3 帧）；结算 / 工具行 / 可见性 → `immediate`（force）。
- 依据：§5.2 / §5.3。
- 预期：高频低价值事件不再触发任何提交；突发时发布频率 ≤ 帧门上限。
- 验证：事件计数 vs 提交计数应远小于 1:1；配速流下的帧率与长任务。
- 注意：**先 P1-1 再 P1-2**——订阅未切片时放宽频率只会放大整树成本（见 §9.3）。

**P1-3 历史窗口评估**（按需）

- 动作：若产品需要承载/回补万级历史（翻旧页、导出、多视图同步），评估引入 `revision + change` 窗口与惰性物化（§4）。
- 风险：中；先需求确认，再决定是否动协议。

**P2-1 渲染面逐项核对**（小）

- 动作：核对消息行 memo 覆盖、Markdown 增量/冻结块策略、折叠边界（对照同目录 `frontend-message-rendering-deepseek-alignment-plan.md`）。
- 验证：Profiler 重渲染计数。

**P2-2 基准固化 + 预算制度**（中）

- 动作：把 `zz-perf-probe` 固化为两条 CI 基准（长会话：冷开 → 翻旧页 → 回 Chat → 配速流 + 键盘输入；活跃重连），先锁定基线，**优化后重复测量再收紧**。
- 依据：§8.2。
- 风险：低（CI 资源成本）。

**P2-3 性能决策记录**（小）

- 动作：对性能决策按"问题 / 决策 / 验证 / 后果"留痕（可沿用 `docs/plan/` 或新增轻量目录）。
- 依据：§8.1。

### 9.3 不建议照抄的部分

1. **不换传输**：WS mux 是参考实现"多路逻辑流复用一条物理连接"的需求产物；我们为 fetch 式 SSE 单流，无需为此替换传输层。重点始终在消费路径（L1 的排空、L3 的节奏、L4 的订阅）。
2. **不单独搬 3 帧门**：3 帧门依赖 L4 的订阅切片；在没有切片的前提下，它只是"更频繁地重渲染整树"。
3. **不照抄数字**：其 9.656ms、≈20Hz、240 回合等均为其参考机/场景记录值；我们的基线用我们自己的探针校准。
4. **不为对齐而虚拟化 Chat**：参考实现同样不虚拟化聊天列表（§7.5），复杂度/回归风险高于收益。
5. **不动参考仓库**：本轮为只读取证（目标仓库无写入）。

## 10. 验证与验收建议（我们自己的基准）

### 10.1 两条基准工作流（对齐参考场景）

1. **长会话流式**：打开长会话（数百回合）→ 翻旧页 → 首次激活 Trajectory → 回 Chat → 配速流式回复 + 真实键盘输入；
2. **活跃重连**：流式中断线重连（携带基线）→ 收敛 → 继续流式。

### 10.2 观测指标

| 指标 | 说明 | 期望方向 |
|---|---|---|
| ScriptDur（主线程脚本时间） | 流式期间每秒脚本耗时 | 显著下降（当前 330ms/s 是基线痛点） |
| RecalcStyle / Layout | 样式重算与布局耗时 | 下降（当前 ≈80ms/s / ≈30ms/s） |
| 长任务 | >50ms 任务数与最长值 | 长任务消失（当前长会话 150-200ms） |
| 提交次数 : 事件数 | 发布/提交与收到事件的比值 | 远小于 1:1（分级+帧门生效） |
| 重渲染组件数 | React Profiler 每帧重渲染计数 | **不随历史长度增长**（≈活动节点数） |
| 积压排空耗时 | 10 万条积压的排空时间 | 近似线性（P0-1 验收） |
| 隐藏标签页 CPU | 页面不可见时 | ≈0（P0-2 验收） |

### 10.3 纪律

- **先基线、后优化、再收紧**：与参考实现一致——预算只在重复测量证明收益后收紧，避免"为预算而优化"；
- 每项改造单独留痕（问题 / 决策 / 验证 / 后果），与 P2-3 呼应；
- 本文档数字（9.656ms、≈20Hz、240 回合、330ms/s、−67% 等）分属两个仓库的参考机记录，**不可直接作为验收阈值**。

---

## 11. 实施记录（2026-09-28）

### P0-1 排空复杂度审计（已实施）

**问题**

- 审计 `frontend/src/api/runtime/sse.ts`：事件解析为字符串扫描 + `indexOf` 游标，无逐条 `shift()`；唯一 `splice(0, len)` 是连接关闭时一次性唤醒 waiter 列表（有界，非事件热路径）→ **无需改动**。
- 审计 `frontend/src/hooks/workspace/use-session-runtime-stream.ts`：`pendingRuntimeCommits.splice(0, length)` 是每帧批量提交的整批取走（有界，非逐条消费）→ **无需改动**。
- 真实热点在 `frontend/src/lib/trajectory/trajectory-reducer/`：`applyEvent` / `advanceSeqCursor` 的乱序缓冲续接用数组 `queue.shift()` 逐条消费（剩余引用搬运），且 `applyEvents` 逐条调用 `applyEvent`——**每条事件整份克隆** `items / revisions / pending`（批量 K 条 × S 行 = O(K·S) 对象拷贝）。

**决策**

- 缓冲续接改为**单链游标推进**：每步至多续接一个事件，直接用游标推进，不建队列数组、无 `shift()`；
- `applyEvents` 改为**整批只克隆一次**，复用同一 `applySequencedWithBuffer` 就地顺序应用；
- `applyEvent`（单条）保留逐条克隆语义，行为不变。

**验证**

- `trajectory-reducer.*`（sequence / state / events / golden / replay / convergence）、`stream-batch`、`use-trajectory-snapshot` 等 20+ 测试文件全绿；
- 相关范围（`src/lib/trajectory` + `src/hooks/workspace`）**81 文件 / 621 测试全绿**；全量前端测试 **358 文件 / 2996 测试全绿**；`tsc -b`、eslint 干净。

**后果**

- 批量应用成本从 O(K·S) 降为 O(S + K)；万级积压排空近似线性（待 `zz-perf-probe` 复测留档）。

### P0-2 惰性通知语义（已实施）

**问题**

- rAF 回调无条件提交（无变化也空提交整树）；隐藏标签页仍排帧/排兜底定时器（白耗 CPU）；
- 重复帧 / 无落点批次仍产生新引用，触发整棵工作区树重渲染；
- 订阅源缺少「读路径同步兜底（ensureFresh）」语义。

**决策与落点**

| 文件 | 改动 |
|---|---|
| `hooks/workspace/agent-chat-turn/streaming-frame.ts` | 新增脏位 `dirty`；隐藏标签页零工作（不排 rAF/定时器，`flush` 只记脏）；`visibilitychange` 恢复时一次兑现；无脏位回调不空提交 |
| `hooks/workspace/use-session-runtime-stream.ts` | 整批提交做**变更追踪**：无落点（重复帧/非当前线程/已定稿）时返回原引用，`setState` 旁路 |
| `lib/trajectory/stream-batch.ts` | `onFlushed` 发布与 apply 分离；隐藏标签页零工作；新增 `ensureFresh()` 读路径同步兜底（渲染期不发布，下一次 flush 补发）；新增 `shouldFlush` 惰性闸门 + `flushNow(force)` 显式同步点 |
| `hooks/workspace/use-trajectory-snapshot.ts` | `getSnapshot` 走 `ensureFresh`；`shouldFlush: () => listeners.size > 0`（无订阅者不 rebuild）；游标推进 / 重建 / 显式 flush 走强制兑现；无真实变化（changes / lastEventSeq / pending 均未变）时丢弃克隆、保持旧引用且不发布 |

**验证**

- 新增/更新单测：`streaming-frame.test.ts`（4 项）、`stream-batch.test.ts`（+4）、`use-trajectory-snapshot.test.ts`（+3），覆盖「隐藏零工作」「恢复可见一次冲刷」「读路径兜底」「无落点不发布/引用稳定」「无订阅者不 rebuild / 强制同步点」；
- 全量前端测试 **358 文件 / 2996 测试全绿**；`tsc -b`、eslint 干净。

**后果**

- 隐藏标签页流式期间 ≈0 工作（CPU 验收项待探针复测）；
- no-op 批次不产生重渲染；读路径无陈旧快照，且不在 React 渲染期同步通知订阅方。

### P1-1 订阅切片——会话级 live 分片 + 内容提交结构门（已实施）

**问题**

- 运行时通道的高帧率内容提交（打字机增量 / tool 进度，合帧节流后仍可达 ~8 次/秒，见
  P0-2 记录 330ms/s）此前全部落页面级 thread state：一次提交触发
  `WorkspacePage → WorkspaceShell → 侧栏 / topbar / 面板 / 消息列` 整树重渲染；
- 参考仓库（§6）的粒度是「页面结构 / 会话 / 会话内节点」三级 observable，本仓库此前只有
  页面级 thread state 一个粒度。

**改动**

- 新增 `frontend/src/lib/thread-state/thread-store.ts`：**live / 结构双通道**外部 store。
  - 结构通道：多线程全量快照，页面级订阅（`useSyncExternalStore`），现有消费方语义不变；
  - live 通道：按线程 id 的每会话分片 + 全局通知——结构提交同时推进两通道；live-only
    提交只通知该会话订阅者；
  - `update(updater, { live: true })` 即内容提交结构门；不传 `live` 时与改造前同语义；
  - `useLiveThread(threadId)`：会话级 uSES 选择 hook，线程不存在时返回 undefined。
- `hooks/workspace/use-workspace-thread-selection.ts`：thread state 收口到 store；
  `writeThreads` 保持 `mergeRuntimeSessionsIntoThreads` 合并语义；新增 `setThreadsLive`
  （live-only 写入器），`setThreads` 仍走结构通道。
- `hooks/workspace/use-session-runtime-stream.ts`：**内容提交结构门**——整批
  `batch.every(shouldApplyLiveDelta)` 且非 transport error 降级态时走 `setThreadsLive`
  （消息列订阅）；任何含持久事件 / 降级恢复语义的批次回落结构通道；未接双通道的调用方
  （测试 / 独立环境）回落 `setThreads`，行为不变。
- `hooks/workspace/use-workspace-live.ts`：透传 `setThreadsLive`（可选）。
- `components/workspace/message-list.tsx`（+ `types.ts`）：新增可选 `threadId` prop；
  组件内按会话订阅 live 分片，live 无值（无 store / 该会话不在 store）时回落 props——
  独立渲染与测试环境保持改造前行为。
- `pages/workspace-page.tsx`、`workspace-shell/main-section.tsx`：接线（页面传
  `setThreadsLive`；消息列传 `threadId={selectedThread.id}`）。

**效果**

- 流式内容帧只重渲染消息列子树（按会话订阅），侧栏 / topbar / 面板不再随高频内容提交重渲染；
- 结构提交（新消息 / 回滚 / 会话状态 / 降级恢复）仍走结构通道，页面级语义零变化；
- 消息列 props 回落保证测试与独立渲染路径完全兼容（全部既有 message-list 用例未改即绿）。

**验证**

- 全量前端测试 **358 文件 / 2996 测试全绿**（含 use-session-runtime-stream /
  use-workspace-live / message-list / use-workspace-thread-selection / frame-intake /
  history-sync 等 242 项流式路径定向用例）；`tsc -b` 干净；
- 渲染性能验收（侧栏重渲染次数随内容帧下降）待 `zz-perf-probe` 或 React Profiler 复测留档。

### P1-2 发布分级 + 帧门（已实施）

**问题**

- 流事件此前一律「入队 + 120ms 合帧提交」：低频持久信号（结算 / 工具生命周期 /
  回滚）与高频内容增量同节奏，immediate 类事件的延迟被合帧窗口吞掉；
- live-only 进度镜像（`tool.progress` / `subagent.progress`，P1-5 后端节流后仍按
  节流窗口逐条投递）与正文增量同频竞争提交节奏，事件数 : 提交数不够「远小于 1:1」；
- 参考实现（§5.2 / §5.3）对事件声明 publication 级别：usage/finish → `none`、
  正文增量 → `animation-frame`（连跨三次绘制机会 ≈20Hz）、结算/工具 →
  `immediate`（取消挂起帧立即发布）。

**改动**

- 新增 `lib/thread-state/publication.ts`：`getPublicationLevel(event)` 三档分级——
  delta 家族（`getRuntimeDeltaKind` ≠ null：正文 / 推理 / 图片增量）+ live-only
  节流镜像（`tool/subagent.progress`，有 UI 落点）→ `animation-frame`；其余持久
  事件 → `immediate`；`none` 档保留类型（占位对齐参考 usage/finish——本仓库事件
  契约暂无对应类型，usage 走 analytics REST，不在地图内伪造落点）。
- `agent-chat-turn/streaming-frame.ts`：调度器增加 pace 参数（`StreamingPace`）：
  - `animation-frame` 档 = **120ms 频率下界 + 三帧门**（`PUBLICATION_FRAME_GATE_FRAMES
    = 3`，连跨三次绘制机会才提交；挂起帧门去重）——120ms 维持提交频率预算
    （「提交:事件 远小于 1:1」验收不回归），三帧门保证帧节奏（两次提交至少隔
    3 个绘制机会，突发多事件合帧、上屏平稳不抖动）；
  - `structural` 档（默认）**逐字节不变**：既有 120ms 最小间隔 + 一帧 rAF 对齐
    （chat 通道与 330ms/s、−67% 历史验证语义不动）；
  - `flush()` 即 immediate：取消挂起帧门立即提交（对齐参考
    `assembly.ts:145-146` 的 `cancelFrame(); flush()`）。
- `hooks/workspace/use-session-runtime-stream.ts`：onEvent 尾部按分级调度——
  `immediate` 事件 `flush()`（对齐参考 `notifier.notifyNow`）；`animation-frame`
  事件 `schedule({ pace: "animation-frame" })`；`none` 事件入队不调度（随下一次
  合帧批顺带兑现，状态不丢、不为此开提交）。

**效果**

- 持久信号（结算 / 工具生命周期 / 回滚 / recovery）不再等 120ms 合帧：立即上屏
  并取消挂起的内容帧，终态延迟下降；
- 正文 / 进度增量保持 120ms 频率下界 + 三帧帧节奏（线性抖动消除，突发有界）；
- 分级判定是纯函数、只决定「是否触发提交 / 以什么节奏」，不改变入队与合并兑现
  语义（线程状态推进与「一条一次提交」顺序一致，P0-2 无落点旁路不受影响）。

**验证**

- 新增 `publication.test.ts`（5 项：三档映射 + 契约代表性快照防漂移）；
  `streaming-frame.test.ts` 追加 4 项（三帧门节奏 / 门内去重 / immediate 打断 /
  隐藏零工作）；`use-session-runtime-stream.test.tsx` 既有 11 项全绿（含
  live-only 镜像提交回归——progress 归 animation-frame 档后仍按时兑现）；
- 全量前端测试 359 文件 / 3005 测试全绿（基线 358/2996 + 新增 publication 5 项 +
  streaming-frame 4 项）；`tsc -b` 干净。全量验证以 `--maxWorkers=4` 低并发执行，
  规避默认并发 spawn 的 ENOMEM（本机内存紧张，非代码问题）。

### P2-1 渲染面逐项核对（已核对，2026-09-28）

**问题**

- P1-1/P1-2 消除了「提交路径」的放大（订阅切片 + 分级帧门），需确认「渲染面」
  本身没有第二处放大链：消息行是否被整列重建、增量 Markdown 是否重复解析、
  折叠展开是否在流式期造成额外协调。

**核对结论（未改代码）**

- memo 覆盖 **已达标**：`message-row.tsx:191` `MessageRow = memo(MessageRowImpl)`
  是唯一列表级挡板；行 props 全部可浅比较——`artifactMap`（:73）与
  `branchAnchors`（:104）useMemo 缓存、`key={message.id}` 稳定、宿主回调直传、
  行内按 id 绑定的回调在行内 `useCallback` 建一次；流式期只有增长中的行与其
  active 标记切换涉及的两行重渲染。
- 增量 Markdown **已达标**：`StreamingMarkdown`（segment-components.tsx）经
  `useTypewriter` 单调揭示——冻结前缀比对永远判「追加」、只对增长中目标文本
  打字；`liveTextRef` 只订阅 ref 不订阅渲染（增量与打字机单驱动，实测 ScriptDur
  1.05s vs 双驱动 1.95s、LayoutCount 201 vs 442 的既有记录不回归）。
- 折叠边界 **已达标**：折叠谓词是纯函数（`lib/chat-view/collapse.ts`，
  `findFinalAnswerStart` 等，无 React 依赖）；两层折叠——回合级统计折叠行
  `turn-process-row.tsx`（收起态过程行不渲染，统计口径来自折叠摘要）+
  行级展开 `chat-process-row.tsx`（`expandable && expanded` 才渲染详情）；
  语义与 `frontend-message-rendering-deepseek-alignment-plan.md`（批次 A–F 已
  实施，e2e 73/73 全绿）一致。
- 验证：以上文件位于 P1-1 的 3005 项全量测试覆盖内（message-list /
  segment-rendering / collapse-rendering / live-stream-rendering 等既有用例全绿，
  全量运行即本组验证）。

### P2-2 基准固化 + 预算制度（方法论已固化；基线复跑待内存空闲窗口）

**问题**

- P0-1 → P2-1 的收益（排空线性化、合帧频率预算、订阅切片、分级帧门）目前只有
  定向实测数字（330ms/s、−67% 等），没有可重复的基准脚本与预算线，缺少
  「优化后复测收紧」的落点。

**改动（方法论与制度固化，无代码改动）**

- 基准载体：`frontend/e2e/zz-perf-probe.manual.ts`（既有诊断探针）——采集
  `PerformanceObserver("longtask")`（>50ms 阻塞）、rAF 帧间隔分布、CDP
  `Performance.getMetrics` 增量（Script / Layout / RecalcStyle）、CPU profile
  自耗时/包含耗时 Top-N + bundle 归因、Chrome trace（样式重算/布局波及节点数）、
  MutationObserver 全量统计（total / byType / byTarget / perFrame 峰值）。
  `PERF_ROUNDS`（默认 40 历史轮，0 = 空会话）区分「整列重渲染」与「流式行内」
  开销；`PERF_TAG` 命名报告。
- 复跑命令：`npm run test:manual -- e2e/zz-perf-probe.manual.ts`（探针自带 dist
  新鲜度守卫——src 新于 dist 直接拒绝，需先 `tsc -b && vite build`）。
- 两条基准场景（对齐 §8.2）：(a) 长会话冷开 → 翻旧页 → 回 Chat → 配速流 +
  键盘输入；(b) 活跃重连（connection-recovery 流回放）。
- 预算条目草案（先校准后收紧，具体阈值待基线回填后定）：长任务（数量 / 最长 /
  总时长）、掉帧率（帧间隔 >33ms 占比）、Script / Layout / RecalcStyle 单回合
  增量、提交:事件比（P1-2 验收延续）、Mutation 总量与 perFrame 峰值。

**状态**

- 2026-09-28 环境受限（物理空闲 3.6GB / 虚拟空闲 0.6GB，他任务构建进程
  compile/link 占用；`tsc -b` 与 `vite build` 均 OOM 崩溃）——基准本体顺延至
  空闲窗口复跑回填；CI 接线（release-aicli / build-aicli-win7 工作流）标注后续。

### P2-3 性能决策记录（已实施，2026-09-28）

对齐 §8.1 设计笔记制度（问题 → 决策 → 验证 → 后果/边界），把本方案已落地的
关键性能决策收拢为一张决策账（§11 各段为展开细节）：

| # | 问题 | 决策 | 落选备选（否决理由） | 验证 | 后果 / 边界 |
|---|---|---|---|---|---|
| D1 | SSE 每字节事件驱动，通知 1:1 放大（P0-1/P0-2） | 排空语义线性化；惰性通知——无订阅者不 flush、无 UI 落点事件走旁路 | 节流丢弃事件（消息会丢，违背流式契约） | 330ms/s、−67% 既有实测 + 全量测试 | 通知次数与事件数解耦；事件完整性不变 |
| D2 | 页面级 thread state：一次提交整树重渲染（P1-1） | live / 结构双通道：消息列按会话订阅 live 分片（uSES），结构通道语义不变 | Chat 窗口化（富交互复杂度/回归风险高于收益，§7.5）；整树逐层 memo（治标不治本） | 定向 242 项 + 全量 359 文件 / 3005 测试 | 流式期只重渲染增长行 ± active 行；独立渲染/测试回落 props 行为不变 |
| D3 | 所有事件同一节奏提交，突发放大（P1-2） | 发布分级：delta 家族 + live-only progress → animation-frame（120ms 下界 + 三帧门）；持久事件 → immediate；none 档保留类型占位 | progress 归 none（纯进度流冻结 UI，测试即门禁）；帧门去 120ms 只留三帧（提交:事件 1.6:1 不达「远小于 1:1」验收） | publication 5 项 + streaming-frame +4 项；4:1 提交比 | 终态延迟下降；正文/进度突发有界、帧节奏平稳；structural 档逐字节不变 |
| D4 | 增量到达与打字机双驱动同一 markdown 尾块（批次 D3 曾删打字机） | useTypewriter 单调揭示（冻结前缀永远判追加）+ liveTextRef 只订阅 ref 不订阅渲染（单驱动） | 删打字机（滞后帧 → 重写判定 → generation++ → 冻结块 remount，产品要求恢复打字） | ScriptDur 1.05s vs 1.95s；LayoutCount 201 vs 442 | 增量只在下一帧被揭示顺带消费，不再各自驱动一次解析 |
| D5 | 过程证据常驻高度拖长会话 | 两层折叠：回合级统计折叠行 + 行级 24px 展开，收起态过程行不渲染；流式期不折叠 | 单层折叠（摘要行/回答贴紧语义丢失）；流式期折叠（用户看不到过程） | collapse-rendering / live-stream-rendering 既有用例全绿 | 静止行退出渲染面；折叠谓词纯函数可单测 |
| D6 | Chat 列长会话重渲染成本 | 不窗口化 Chat 消息列——依赖折叠 + memo + 增量 Markdown 把每帧代价压到尾部可见块 | 虚拟滚动（参考仓库同样不虚拟化 Chat，§9.3） | 既有结构核对（P2-1）+ 全量测试 | Trajectory / Diff / 文件浏览器保持虚拟化，Chat 明确不窗口化 |

**后果总览**：L1 排空、L3 节奏、L4 订阅三级放大链均已收敛；「先测量、后收紧」的
预算制度落点见 P2-2；后续任何性能改动应先在此账本增行（编号顺延），再动代码。

### 未实施（后续）

- **P1-3 历史窗口评估**（按需）；
- **P2-2 基准固化 + 预算制度**（方法论已固化，基线待空闲窗口复跑回填；CI 接线标注后续）。

---

## 附录 A 证据索引

### A.1 参考仓库（`E:\projects\ai\deepseek-harness` @ `c291e7961a`，master）

| 路径 | 要点 |
|---|---|
| `packages/util/deque/`（含 README） | 环形双端队列：O(1) 两端操作、移除清槽、倍增/1/4 收缩；基准 |
| `packages/api/gateway/src/client/stream-client.ts` | `RemoteStreamMuxClient`（单物理 WS 复用多逻辑流）；`StreamInbox` 基于 `Deque` |
| `packages/api/session-controller/src/client/contract/events.ts` | rope 窗口（leaf/concat）、`revision`、`change`（append/prepend/replace/settle-assistant）、`entries` 惰性物化、`settleAssistant` |
| `packages/api/session-controller/src/client/sessions/notifier.ts` | 结构→microtask、流→rAF 去重、无订阅者不 flush、`ensureFresh`、`notifyNow`（受控输入）、世代号 |
| `packages/api/session-controller/src/client/sessions/assistant-stream.ts` | live 帧与持久 settlement 的折叠/替换语义 |
| `packages/client/ui-conversation/src/client/conversation/assembly.ts:130-147` | publication 分级 `none/animation-frame/immediate`；三帧门；immediate 取消挂起帧 |
| `packages/client/ui-chat/src/client/conversation-nodes/assistant.ts:321-323`、`turn-process.ts:240-241`、`inbox.ts:125`、`turn-tail.ts:186` | 节点级 publication 规则（哪些事件上屏、以什么节奏） |
| `packages/client/store/src/index.ts` | `rafBatch`（N 变更=每帧 1 通知）；`notifySubscribers` 拷贝派发防饿死；React-free 引擎 |
| `packages/client/ui-renderer/src/client/bind.ts` | 唯一 uSES 桥 `bindSnapshotSelector`（按 source 缓存） |
| `packages/client/ui-renderer/src/client/scoped-slots.tsx` | `bindInjectSources`；`hooks`/`keyedHooks` 绑成组件 props 上的订阅源 |
| `packages/client/ui-session/src/client/index.ts:148-205, 404-424` | 源花名册 `hooks/keyedHooks/props`；内置 `session`/`projection`；`materialize(binding)` |
| `packages/client/ui-chat/src/client/apply.ts:57-76, 113-119` | `chatSource`（WeakMap 缓存）；`provide({hooks:['chat']})`；`keyedHooks: chatNode/chatNodeProcess` |
| `packages/client/ui-trajectory/src/client/TrajectoryTable.tsx`、`trajectory-virtual-rows.ts` | 全客户端唯一虚拟化落点（`virtuali/overscan` 仅命中此处） |
| `.agents/notes/archived/bug-fix/2026-08-28-linear-stream-queue-drain.md` | 排空事故（Issue #3270）：`shift()` O(N²)、消费者清单、200 万条 9.656ms |
| `.agents/notes/archived/architecture/2026-08-06-web-markdown-incremental-ast-renderer.md` | 增量 mdast：冻结块不重解析、源偏移缓存 |
| `.agents/notes/archived/feature/2026-08-14-web-turn-process-folding.md` | 回合过程折叠：静止行退出渲染面 |
| `.agents/notes/archived/architecture/2026-08-31-live-assistant-stream-frames.md` | 瞬态帧/持久结算分离、重连基线收敛 |
| `.agents/notes/implemented/testing/2026-09-06-frontend-performance-budgets.md` | 预算纪律（先校准后收紧）与未覆盖边界 |
| `benchmarks/long-session-browser/README.md`、`benchmarks/active-stream-reconnect/README.md` | 240 回合长会话工作流；活跃重连折叠基准 |

### A.2 本仓库（`E:\projects\ai\ai-agent-runtime`）

| 路径 | 要点 |
|---|---|
| `frontend/src/hooks/workspace/agent-chat-turn/streaming-frame.ts` | rAF 合帧 + 后台兜底 + 可见性 flush；`MIN_COMMIT_INTERVAL_MS=120`；`STRUCTURAL_COMMIT_INTERVAL_MS=1000`；330ms/s、−67% 实测记录 |
| `frontend/src/api/runtime/sse.ts`（+ `sse.test.ts`） | 自研 SSE 流解析器（P0-1 审计对象） |
| `frontend/src/lib/live-stream-text.ts` | live 揭示文本通道（瞬态/持久分离雏形） |
| `frontend/src/lib/thread-state/thread-store.ts` | live / 结构双通道线程 store；按会话 live 分片（P1-1） |
| `frontend/src/lib/thread-state/publication.ts` | 发布分级 `none / animation-frame / immediate`（P1-2） |
| `message-row.tsx` / `segment-components.tsx` / `lib/chat-view/collapse.ts` / `turn-process-row.tsx` | P2-1 核对对象：memo 挡板 / 单调揭示增量 Markdown / 两层折叠（已核对，无代码改动） |
| `frontend/src/hooks/workspace/use-session-runtime-stream.ts` | 重连循环与 runtime 提交调度 |
| `frontend/src/e2e/` 手动探针 `zz-perf-probe.manual.ts`（见 streaming-frame.ts 注释） | 现有性能探针 |
| `frontend/src/components/workspace/trajectory/trajectory-virtual-rows.ts` 等 | 我们已有的虚拟化落点（轨迹/Diff/文件浏览器） |
| `docs/plan/frontend-deepseek-harness-optimization-plan.md`、`frontend-deepseek-harness-gap-list.md` | 前置对照文档 |

## 附录 B 术语表

- **uSES**：`useSyncExternalStore`（含 `WithSelector` 变体），React 官方外部 store 订阅接口；
- **rope**：以树结构表达序列拼接，使"追加/前插"为 O(1) 结构操作，全量展开推迟到真正需要时（惰性 `entries`）；
- **合帧（coalescing）**：同一帧内到达的多次更新合并为一次发布/渲染；
- **publication（发布级别）**：`none / animation-frame / immediate`——事件"是否上屏、以什么节奏上屏"的声明；
- **三帧门**：高频流发布前连跨三次绘制机会（60fps 下 ≈20Hz 上限）；
- **瞬态帧 / 持久结算（transient / settlement）**：live chunk 只存在于临时层，持久结算到达后按 seq 有界替换；
- **keyedHooks**：按 key（会话内节点/过程）解析出的独立 observable 源，组件订阅到节点粒度。

---

*取证部分为只读产物（参考仓库未发生任何改动）；P0-1 / P0-2 / P1-1 / P1-2 实施与验证记录见 §11。*
