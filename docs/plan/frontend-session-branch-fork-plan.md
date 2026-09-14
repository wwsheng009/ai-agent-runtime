# 会话分支（在新对话中分支 / Session Fork）前端实施方案

状态：**草案（待评审）**——本文只做方案设计，未改动任何代码。

日期：2026-09-14

负责范围：`frontend/`（会话页消息动作区、会话侧栏、会话路由与刷新链路）；后端接口契约为前置依赖（见 §6，尚未实现）。

参考对象：

- 功能原型（外部只读参照）：`deepseek-harness`——消息级分支入口 `packages/client/ui-chat/src/client/chat/MessageIconActions.tsx:20-24,91-109`、会话行级 Fork `packages/client/ui-workspace/src/client/navigation.ts:34,148` 与 `.../rows/WorkspaceBrowser.tsx:589,716`、端到端口径 `apps/web/tests/message-actions.e2e.ts:174-226`。
- 文案口径（同一参照）：`packages/client/ui-chat/src/client/locale.ts:71-72,182-183`。

关联文档（`docs/plan/` 已查）：

- `frontend-deepseek-harness-optimization-plan.md`（消息投影 / 增量渲染 / 滚动契约等内核，本方案是**会话生命周期**方向的后续专项）
- `frontend-message-rendering-deepseek-alignment-plan.md`（消息流扁平化与动作区视觉规范，本方案的动作区沿用其 `ACTION_BUTTON_CLASS` 口径）
- `session-user-turn-backtrack-plan.md`（会话回溯：同源的历史前缀重写能力，本方案与其共用「锚点 + 前缀」语义）
- `frontend-deepseek-harness-gap-list.md`（功能缺口清单基线）

---

## 1. 背景与结论摘要

### 1.1 需求来源

参照实现提供「**在新对话中分支**」（Branch into a new conversation）能力：在会话流里某个**已完成轮次的最后一条消息**上发起分支，系统创建一条**新会话**，其上下文是原会话在锚点处的**历史前缀**，此后两条会话各自独立演进（各自追加、互不影响）。同时会话列表行操作方法中另有一个**整会话 Fork** 入口（不裁剪历史）。

本仓 `frontend/` 已经有一个同名能力，但语义不同：见 §1.2。

### 1.2 现状：本仓的 Fork 是「伪 Fork」（不复制历史）

现有实现的事实（均已核对到行）：

| 事实 | 证据 |
|---|---|
| 侧栏会话行「Fork」= 以同一标题基名 + 本地化后缀、继承工作目录，**新建一个空会话** | `frontend/src/components/workspace/workspace-sidebar/session-row-actions.ts:1-5,27-47` |
| 文件头显式声明后端无克隆 API、**不复制消息历史、不伪造分支历史** | `session-row-actions.ts:3-5` |
| 执行链：`createRuntimeSession` → `refreshSessions()` → 硬重置轨迹 → 跳转 canonical 路由 | `frontend/src/hooks/workspace/use-workspace-session-actions.ts:84-108` |
| 客户端**没有**「按历史前缀建新会话」的接口调用 | `frontend/src/api/runtime/sessions.ts:88-102`（`createRuntimeSession` 仅 body `{title,user_id,workspace_path,directory_id}`） |
| 会话记录**没有父子/分支字段**（仅 `metadata.context` 可扩展） | `frontend/src/types/runtime/sessions.ts:1-21`；`Thread` 亦然（`frontend/src/data/mock/types.ts:103-121`） |
| 侧栏行顺序**完全由时间决定**，无层级信息 | `frontend/src/lib/thread-state/sessions.ts:10-137`（排序 `:132-136`）、`frontend/src/hooks/workspace/use-runtime-sessions-data.ts:169-230` |
| 后端路由表**没有** fork/clone 端点 | `backend/internal/api/skills/handler.go:735-782`（`/sessions` 下只有 archive/activate/close/history/runtime/agents/checkpoints/backtrack/plan 等） |
| 后端 `CreateSession` 只接受 `user_id/title/workspace_path/directory_id`，**不接受历史 seed** | `backend/internal/api/skills/handler.go:2311-2337` |

已落位的可复用件（这是本方案的乐观面）：

| 可复用能力 | 证据 |
|---|---|
| 稳定消息 id（分支锚点可序列化） | `frontend/src/lib/thread-state/history-mapping.ts:237,264`（`stableId = readHistoryMessageIdentity(message)`，退化 `` `${sessionId}-history-${index}` ``） |
| 锚点选择器归一化（接受 `msg_` 前缀、拒绝合成 id） | `frontend/src/hooks/workspace/session-backtrack/helpers.ts:137-157` |
| 对话框骨架（portal / focus / Esc / `role="dialog"`） | `frontend/src/components/workspace/message-backtrack-dialog.tsx:11-18,32-44,56-61`，挂载点 `frontend/src/components/workspace/workspace-shell/overlays-section.tsx:106-120` |
| 「建会话 + 跳转 + 重置轨迹」的完整链路 | `use-workspace-session-actions.ts:85-108`（Fork）、`:127-140`（目录内新建） |
| 后端**已存在**「把会话历史物理重写为某个前缀」的机制（回溯用） | `backend/internal/chat/backtrack_actor.go:241-279`（`session.ReplaceHistory(prefix)` + `SetHeadOffset(0)` + `persistSession`） |
| 后端**已存在**「按锚点求前缀」的纯函数 | `backend/internal/chat/backtrack.go:258-268`（`PlanBacktrack` 返回 `result` + 前缀消息切片） |
| 后端历史读模型分页可用（新会话 seed 后可读） | `backend/internal/api/skills/handler.go:2517-2566`（`GET /sessions/{id}/history`，`GetHistoryPage`） |

### 1.3 目标

1. **补齐真实分支语义**：从某条消息锚点创建新会话时，新会话上下文 = 原会话在锚点处的历史前缀（消息、轮次、以及可选的锚点文件快照），而不是空会话。
2. **两个入口对齐参照实现**：① 消息流内「在新对话中分支」（锚点入口，主入口）；② 会话行操作里的整会话 Fork（保留现有入口，语义升级为「复制全量历史」或明确标注为「新会话+同目录」二选一，见 §4.2.2）。
3. **子会话可见**：分支产生的新会话在侧栏可辨识其来源（父行附近呈现 + 来源标识），且不破坏现有分组/排序/拖拽账目。
4. **可用性规则可解释**：只有「已完成轮次的最后一条消息」可分支；不满足时按钮可见但不可用，并给出可读原因（对齐参照实现的口径，§2.2）。
5. **最小侵入**：不重写消息投影内核、不改 SSE 协议、不引入消息树可视化（与 `frontend-message-rendering-deepseek-alignment-plan.md:59` 的非目标一致）。

### 1.4 非目标

- 不做「消息树 / 多级分支图」可视化（只做**会话级**父子标识与顺序约束）。
- 不做分支会话的跨会话 diff、合并、rebase（分支后各自独立）。
- 不改 `lib/chat-view/**` 的 flow 投影谓词语义（只在既有 `turn-tail` item 上扩 props）。
- 不改 `lib/workspace/session-order.ts` 的手动排序账目结构（只新增「父子不可拆散」的**约束**，见 §5.5）。
- 不做后端多租户/权限模型改造（沿用现有 `user_id` 与 workspace 目录绑定）。

### 1.5 结论摘要（按优先级）

| 级别 | 结论 | 一句话依据 |
|---|---|---|
| P0 | **前端无法独立实现真实分支**，必须先补后端「按锚点 seed 历史建会话」端点，否则只能维持「伪 Fork」 | 现有 `POST /sessions` 不收历史（`handler.go:2311-2337`）；但后端已有 `ReplaceHistory` + `PlanBacktrack` 两个现成积木（`backtrack_actor.go:241-279`、`backtrack.go:258-268`） |
| P0 | 分支的**锚点语义应与参照实现一致**：轮末尾（assistant 侧）而非用户消息 | 参照实现只在 assistant 答案下渲染 branch，且仅「completed transcript tail」可用（`apps/web/tests/message-actions.e2e.ts:190-203`） |
| P0 | 主入口应挂在 **turn-tail 行**（`turn-tail-row.tsx`），而不是用户气泡动作行 | 同 P0 上一条；turn-tail 天然是「轮末尾」渲染位（`frontend/src/lib/chat-view/flow.ts:299-304`） |
| P1 | 子会话呈现采用「**父行后紧跟 + 缩进**」并复用现有树形无障碍口径，但需先定义与手动排序/拖拽的优先级 | 侧栏已有 `ml-4` 组缩进（`sessions-section.tsx:363-365`）与 `role=treeitem/aria-level/paddingLeft` 先例（`session-agents-tree.tsx:165-175`） |
| P1 | 分支关系持久化只能走 `metadata.context`（`parent_session_id` / `fork_anchor_message_id` / `fork_anchor_turn`） | `RuntimeSessionRecord` 无 parent 字段（`types/runtime/sessions.ts:1-21`）；`PATCH /sessions/{id}` 的 `context` 逐键合并是既有惯例（`use-workspace-session-actions.ts:61-62`） |
| P1 | 可用性与原因文案必须同批进 zh-CN / en-US 两套词典，否则 `tsc -b` 失败 | i18n 资源是 `satisfies` 对齐（`frontend/src/i18n/resources/{zh-CN,en-US}/workspace/panels-messages.ts`），且门禁含硬编码文本扫描（`frontend/scripts/verify-frontend-i18n.ts:20-27`） |
| P2 | 「无 Tooltip 原语」是本仓与参照实现的**最大交互落差**，需要新定不可用原因的呈现方式 | 全仓 `components/ui/` 无 tooltip；现有降级是 `aria-disabled` + `sr-only`（`user-message-bubble.tsx:203-206`） |
| P2 | 分支后新会话的侧栏可见性依赖既有 pinned 深链兜底，存在可见延迟 | `runtime-sessions-data/loading.ts:13-60`（`mergePinnedRuntimeSession`）与 `use-workspace-session-actions.ts:101-107`（先 refresh 再 navigate） |

---

## 2. 参照实现拆解（deepseek-harness）

> 本节只记录**可核对**的参照事实，用于对齐语义与文案；实现细节以本仓技术栈为准。

### 2.1 两个入口

| 入口 | 位置 | 语义 |
|---|---|---|
| 消息级「在新对话中分支」 | `packages/client/ui-chat/src/client/chat/MessageIconActions.tsx:91-109`（与 Copy 同排的图标动作簇） | 以**该消息为锚点** fork 当前会话 |
| 会话行级 Fork | `packages/client/ui-workspace/src/client/navigation.ts:34,148`、`.../rows/WorkspaceBrowser.tsx:589,716` | 以**整个会话**为源 fork |

对宿主的影响：fork 出的会话在会话头里带父会话标识（`agent.session.header.parentSession === SessionId(SEED_ID)`，`apps/web/tests/message-actions.e2e.ts:225`）。

### 2.2 可用性规则（可执行口径）

- branch 动作由 `onBranch` 是否存在决定**是否渲染**；`branchUnavailable` 只决定**可用与否**，不可用时按钮仍可见（`MessageIconActions.tsx:20-24`）。
- 不可用时**不使用原生 `disabled`**，而是 `aria-disabled` + `aria-describedby` 指向一段 visually-hidden 的原因文本——注释明确写了原因：原生 disabled 按钮不派发 Tooltip 需要的 hover/focus 事件（`MessageIconActions.tsx:93-101,107-109`）。
- 规则：**只有 assistant 答案下渲染**（用户气泡没有 branch），且**只有已完成轮次的最后一条消息**才可用（`apps/web/tests/message-actions.e2e.ts:190-203`）。

### 2.3 文案口径（原样沿用）

| key | zh-CN | en-US |
|---|---|---|
| `message.branch` | 在新对话中分支 | Branch into a new conversation |
| `message.branchUnavailable` | 仅可从已完成轮次的最后一条消息分支 | Available only on the last message of a completed turn |

证据：`packages/client/ui-chat/src/client/locale.ts:71-72,182-183`。

### 2.4 与「回溯」的关系

参照实现与本仓的「回溯（backtrack）」共享同一套**锚点 + 前缀**语义：都是「选定一条消息 → 得到历史前缀 → 后续从该前缀继续」。差别在落点：

- 回溯：**原地重写**当前会话（前缀覆盖当前历史，丢弃其后的内容）。
- 分支：**不改动源会话**，把前缀写进**新会话**，源会话其后内容保留。

本仓后端已经具备第一种（`backend/internal/chat/backtrack_actor.go:241-279`），本方案要求把同一套前缀计算复用到第二种。

---

## 3. 本仓现状盘点

### 3.1 消息流渲染与动作区

**渲染管线**：`Message[] → ChatMessage[] → FlowItem[] → 行组件`。

- flow 投影：`frontend/src/lib/chat-view/flow.ts:299-304` 生成 `turn-tail` item（语义 = 轮末尾）。
- 行组件：
  - 用户气泡 `frontend/src/components/workspace/message-list/user-message-bubble.tsx`：动作行门控 `:199`，复制按钮 `:212-224`，编辑/回溯按钮 `:225-257`，按钮样式常量 `ACTION_BUTTON_CLASS :51-52`（28×28 圆钮，hover 显现见 `frontend/src/styles/globals/base.css:186-200`）。
  - 轮尾行 `frontend/src/components/workspace/message-list/turn-tail-row.tsx:61-107`，props 契约 `:13-20`。
- 宿主：`frontend/src/components/workspace/message-list.tsx`——`showBacktrack` 门控 `:195-196`、`actionsDisabled = backtrackPending || isResponding || backtrackNavigationActive :202-205`、用户气泡入参 `:222-240`、assistant 卡片 → turn tail `:263-273`、流式判定 `:63-65`。

**关键事实**：本仓当前**没有任何轮末尾/完成态判定**可用于「是否可分支」——`actionsDisabled` 只表达「正在忙」，不表达「这条消息是不是完成轮次的最后一条」。这是新增能力（见 §4.1 G3）。

**历史与实时边界坑**：`frontend/src/lib/thread-state/history-artifacts.ts:33-41` 会把 `streaming/interrupted` 的 live-only 消息追加在历史之后，因此「其后无任何记录」这类判定必须计入这类消息，否则会把可分支点误判为否。

### 3.2 会话与线程模型

- `RuntimeSessionRecord`：`frontend/src/types/runtime/sessions.ts:1-21`——字段 `id / userId? / state? / metadata{title,titleSource,summary,tags,totalTurns,lastAgent,lastSkill,lastModel,createdBy,context} / createdAt? / updatedAt? / expiresAt?`。
  - **无顶层 `status`**（由 `frontend/src/lib/thread-state/sessions.ts:174-186` 从 `state` 派生）；
  - **无 `parentSessionId`**（全仓 parent 语义只存在于 agent 体系：`frontend/src/types/runtime/agents.ts:22`、`frontend/src/hooks/use-session-agents.ts:108-127`）；
  - **无 `workspace_path` 顶层字段**——目录归属存在 `metadata.context`，多键读取见 `frontend/src/components/workspace/workspace-sidebar-shared.ts:170-200`。
- 创建请求：`RuntimeCreateSessionRequest :23-30` = `{title?, user_id?, workspace_path?, directory_id?}`；响应 `:32-34`。
- 线程合并：`frontend/src/lib/thread-state/sessions.ts:10-137`
  - 匹配键 `:23-29`；物化新线程 `:46-85`（title 回落 `` `Runtime session ${id.slice(0,10)}` ``，tags 含 `runtime-session`）；已存在则合并标题/摘要/时间/状态/tags 并**保留本地 messages** `:88-120`；最终按 `updatedAt` 倒序 `:132-136`。
  - **该函数是「分支关系」必须搭车的唯一入口**：快照刷新后，parent 信息要么在这里写进 `Thread`，要么在侧栏视图层另算。

### 3.3 侧栏渲染管线与会话行动作

数据流（自上而下）：

```text
会话快照 (use-runtime-sessions-data.ts:169-230)
  → 目录分组 (workspace-sidebar-shared.ts:118-150, :253+)
  → 视图组装 (use-session-group-view.ts:95-104 合并/过滤 → :108-119 orderFor + promoteBlankSessions)
  → 组内可见性裁剪 (lib/workspace/session-grouping.ts:51-87 + use-session-group-visibility.ts:22-47)
  → 行渲染 (workspace-sidebar/sessions-section.tsx:296-356 组 → :367-455 行 → :456-467 Show N more)
```

- 行视图模型：`frontend/src/components/workspace/workspace-sidebar/session-row-view-model.ts:63-109`（标题优先级 thread → metadata.title → id；`isActive` `:101-103`）。
- 行组件与菜单：`frontend/src/components/workspace/workspace-sidebar/session-item.tsx:306-395`；菜单项 Fork `:243-256`、Restore `:257-271`、Archive `:272-285`、Delete `:286-299`；可用性判定 `:334-338`。
- Fork 调用链：`sessions-section.tsx:411-419` → `workspace-sidebar.tsx:63,396` → `workspace-shell/sidebar-section.tsx:60,129` → `workspace-shell.tsx:268` → `pages/workspace-page.tsx:386` → `use-workspace-session-actions.ts:85-108`。
- 分组模式与排序模式正交：`frontend/src/lib/workspace/session-grouping.ts:5-16,24-28`；排序账目 `frontend/src/lib/workspace/session-order.ts` + `frontend/src/hooks/workspace/use-session-order.ts`；组内拖拽 `frontend/src/components/workspace/workspace-sidebar/use-session-drag-reorder.ts`。
- **缩进先例**：`frontend/src/components/workspace/session-agents-tree.tsx:165-175`（`role=treeitem` / `aria-level` / `data-depth` / `paddingLeft: depth*14`）。会话侧栏目前只有目录组一层 `ml-4`（`sessions-section.tsx:363-365`），**没有**行级缩进先例。

### 3.4 路由、选中与刷新

- 路由：`frontend/src/App.tsx:131-139`（`/workspace/sessions/:sessionId`）、`:140-148`（`/workspace/chats/:threadId`）。
- 加载链：`frontend/src/pages/workspace-page.tsx:45`（取参）→ `:81-84`（`pinnedSessionId = routeSessionId`）→ `use-runtime-sessions-data.ts:169-230`（拉取/重试/写缓存）→ `runtime-sessions-data/loading.ts:13-60`（**pinned 会话不在快照时单独 `getRuntimeSession` 兜底**）→ `lib/thread-state/sessions.ts:46-85`（物化线程）。
- 占位线程：`frontend/src/hooks/workspace/use-workspace-thread-selection.ts:80-93`（draft，id=`"new"`）、`:95-111`（pending，`updatedAt=1970-01-01`、tags `["runtime-session","loading"]`）。
- 历史同步：`frontend/src/hooks/workspace/use-session-history-sync.ts:30`（内部 `getSessionHistory` `:54`），挂接 `workspace-page.tsx:212-217`。
- 切换重置：`workspace-page.tsx:152-157` 先 `trajectoryStore.reset({hard:true})`；`:162-187` 生成中切换先弹确认；分支路径 `use-workspace-session-actions.ts:105-106` 同口径。
- **分支新会话的既有兜底**：URL 先到 canonical → 未命中时 pending 占位 → pinned 拉取 → 物化；侧栏行**没有**乐观插入先例（handler 不写 `setThreads`）。行点击在线程未命中时直接 return（`use-workspace-thread-selection.ts:262-265`）。

### 3.5 后端契约现状（本次只读核对）

| 能力 | 现状 | 证据 |
|---|---|---|
| 建会话 | `POST /api/runtime/sessions`，body 仅 `user_id/title/workspace_path/directory_id` | `backend/internal/api/skills/handler.go:2304-2371` |
| 读历史（分页） | `GET /api/runtime/sessions/{id}/history?limit&before_seq` → `history/count/total/has_more/first_seq/last_seq/next_before_seq` | `handler.go:2517-2566` |
| 历史重写为前缀 | **无 HTTP 端点**，但内部已有实现：`ReplaceHistory(prefix)` + `SetHeadOffset(0)` + `AppendBacktrackTombstone` + `persistSession` | `backend/internal/chat/backtrack_actor.go:241-279` |
| 前缀计算 | **无 HTTP 端点**，纯函数已存在：`PlanBacktrack(...) (*BacktrackResult, []Message, error)` | `backend/internal/chat/backtrack.go:258-268` |
| 回溯 HTTP | `POST /sessions/{id}/backtrack/preview`、`POST /sessions/{id}/backtrack`、`GET /sessions/{id}/backtrack/audit` | `handler.go:770-772`、`backend/internal/api/skills/backtrack_handlers.go:16,96,102` |
| 存储接口 | `SessionStorage{Save,Load,Update,AddMessage,GetMessages,...}`；可选 `AddMessageWithLimit` | `backend/internal/chat/storage.go:24-60,67-70` |
| 分支/克隆端点 | **不存在**（路由表逐条核对） | `handler.go:735-782` |
| 会话存储模型 | `History []types.Message` **随会话记录整段持久化**；`HeadOffset` = 可见历史长度；`CanonicalMessageCount` = 累计消息数 | `backend/internal/chat/session.go:56-77,632-641,974-990` |
| 历史替换原语 | `ReplaceHistory(messages)`：深拷贝 + `EnsureHistoryMessageIdentities`（**重铸缺失的 message/turn id**）+ `HistoryLoaded=true` + 调整 `HeadOffset` | `backend/internal/chat/session.go:175-196` |
| 会话创建实现 | `Manager.Create`（`NewSession` → `storage.Save`）；`CreateSession` 是其别名，**无 history 参数** | `backend/internal/chat/manager.go:69-85,103-106`；`session.go:96-110` |
| API 层「克隆 + 替换历史 + 落库」既有范例 | turn 结束写回：`execSession.Clone()` + `ReplaceHistory` + `sessionManager.Update` | `handler.go:1833-1839,2127-2133`；`persistChatTurn` `handler.go:3351+` |
| 会话级 parent 字段 | **不存在**。现有谱系只以 `metadata.context` 键存在：压缩 `compact_*`、子代理 `agent_*` | `session.go:34-38`；`backend/internal/agentcontrol/registry.go:9-24`；`backend/internal/toolbroker/types.go:949-969` |
| checkpoint 会话快照 | 可返回 `ConversationMessages`，但**仅 `exact && !preview`**；checkpoint 记录本身不含消息数组 | `backend/internal/checkpoint/manager.go:420,501-522`；`backend/internal/checkpoint/types.go:60-67` |
| 持久化时机 | actor 统一写回 `persistSession → sessionStore.Update`；turn 内节流 15s；file 后端列表**不加载 history**（`HistoryLoaded=false`） | `backend/internal/chat/actor.go:2701-2757`；`backend/internal/chat/file_storage.go:585-616` |

> 结论：后端**不缺积木，缺一个端点**。把 `PlanBacktrack` 的「前缀」语义与 `applyBacktrackHistory` 的「物理重写」能力，从「原地截断」改造成「写进一个新会话」，即可实现真实分支（§6 给出契约建议）。更完整的可行性核查结论：**「新建会话时 seed 一段历史前缀」可行，属小–中改造**——`NewSession`（空历史）→ `ReplaceHistory(prefix)`（自动补全 id、`HistoryLoaded=true`）→ `storage.Save/Update` 三步即可，无需 schema 变更。

### 3.6 i18n 与质量门禁

- 词典：`frontend/src/i18n/resources/{zh-CN,en-US}/workspace/*.ts`；现有 fork 文案 `zh-CN/workspace/base.ts:140-141`（`fork` / `forkSuffix`）、`en-US/workspace/base.ts:148-149`。消息区文案在 `panels-messages.ts`（两语言必须**同批**补齐，编译期 `satisfies` 对齐）。
- 硬编码拦截：`frontend/scripts/verify-frontend-i18n.ts:20-27` 用 AST 扫 `aria-label/title/placeholder/alt/label/confirmLabel`——新增按钮的 `aria-label` 必须走 `t()`。
- 门禁序列（`frontend/package.json` `lint`）：`eslint .` → `verify-frontend-i18n.ts` → `verify-no-backups.mjs` → `verify-max-lines.mjs` → `verify-message-tokens.mjs`；其中 `verify-max-lines.mjs:23-26` 限 **≤500 非空行**（`i18n/resources/**` 豁免）。`npm test` = `vitest run`；`npm run test:e2e` = Playwright（**先 `build` 再跑**）。
- 现有测试面（分支改动会直接波及）：
  - 单测：`session-row-actions.test.ts:9-49`、`lib/workspace/session-grouping.test.ts:11,21,39-107`、`lib/workspace/session-order.test.ts:22-310`、`hooks/workspace/use-session-order.test.tsx:29-190`、`workspace-sidebar/use-session-drag-reorder.test.tsx:69-400`、`hooks/workspace/use-workspace-thread-selection.test.ts:47-312`、`workspace-sidebar.test.ts:104`、`workspace-sidebar-cross-group-move.test.tsx:99`。
  - e2e：`sidebar-session-actions.spec.ts:46`「Fork 生成带分支后缀的独立新会话」（拦截 POST body、断言 title 含 `(branch)`、`toHaveURL(/workspace/sessions/e2e-fork-1)`）、`:67`「删除会话后该行从侧栏列表消失」；`session-grouping.spec.ts:260,296,323`；`session-order.spec.ts:69,95,127`；`session-collapse.spec.ts:130`；mock 端 `e2e/mock-server.mjs`（无 id 的 POST 会分配确定 id）。

---

## 4. 差距分析与关键决策

### 4.1 差距清单

| 编号 | 差距 | 现状证据 | 影响面 |
|---|---|---|---|
| G1 | 后端没有「按锚点 seed 历史建会话」端点 | `handler.go:735-782`（路由表无 fork/branch）；`CreateSession` 不收历史 `handler.go:2311-2337` | 阻塞项：不补则只能维持伪 Fork |
| G2 | 前端没有分支 API 客户端与编排 hook | `api/runtime/sessions.ts:88-102` 只有 `createRuntimeSession`；`use-workspace-session-actions.ts:84-108` 是旧语义 | 前端主链路 |
| G3 | 没有「已完成轮次最后一条消息」判定 | `message-list.tsx:202-205` 的 `actionsDisabled` 只表达「忙」；`turn-tail-row.tsx:13-20` 无相关 props | 决定按钮可用性 |
| G4 | 没有消息级入口（只有侧栏行级 Fork） | 入口清单见 §3.1；`session-item.tsx:243-256` | 交互形态 |
| G5 | 会话无父子字段，侧栏无层级呈现 | `types/runtime/sessions.ts:1-21`；`lib/thread-state/sessions.ts:132-136` 纯时间序 | 侧栏可辨识性 |
| G6 | 无不可用原因呈现原语（全仓无 Tooltip） | `components/ui/` 无 tooltip 文件；现有降级 `user-message-bubble.tsx:203-206` | 可用性可解释性 |
| G7 | 命名只有固定后缀，无序号/去重口径 | `session-row-actions.ts:14-21`；e2e 断言 title 含 `(branch)`（`sidebar-session-actions.spec.ts:46`） | 次要，但改动会破测试 |
| G8 | 测试与 mock 只覆盖「伪 Fork」 | `session-row-actions.test.ts:9-49`；`sidebar-session-actions.spec.ts:46`；`e2e/mock-server.mjs` | 验收面 |

### 4.2 关键决策点

#### 4.2.1 锚点语义：轮末尾（推荐）而不是用户消息

- 参照实现：branch 只出现在 assistant 答案下，且仅「已完成轮次的最后一条消息」可用（`apps/web/tests/message-actions.e2e.ts:190-203`）。
- 本仓回溯：锚点是**用户消息 id**，`resolveBacktrackMessageSelector` 接受 `msg_` 前缀并**拒绝**合成 id（`session-backtrack/helpers.ts:137-157`）；用户气泡动作行天然是用户消息（`user-message-bubble.tsx:225-257`）。

**决策**：分支的 UI 锚点取「**轮末尾消息**」（turn tail），请求体统一用 `anchor_message_id` 表达，服务端负责把锚点归一化为「该消息所在轮次的前缀上界」；前端不自行裁剪历史，避免前后端两套前缀语义。

理由：

1. 与参照实现的可用性规则一致（「最后一条消息」）；
2. 轮末尾天然对应「一个完整上下文快照」，而用户消息锚点会把该轮的用户输入也切掉，语义是「回到发送前」而不是「在此分叉」；
3. 本仓已有 `turn-tail` flow item（`lib/chat-view/flow.ts:299-304`）作为渲染位，不需要新增投影谓词。

> 若产品坚持「从某条用户消息分叉」，则等价于在该用户消息**之前**切分；这属于同一个 `anchor_message_id` 的服务端归一化分支，接口不需要改。

#### 4.2.2 历史 seed 的后端契约：三个选项

| 选项 | 做法 | 优点 | 缺点 | 结论 |
|---|---|---|---|---|
| **A. 新增服务端 branch 端点** | `POST /api/runtime/sessions/{id}/branch`，服务端在一个编排里：建新会话 → 取源历史 → 求前缀 → `ReplaceHistory(prefix)` → 落 lineage metadata → 返回新会话 | 复用已有 `PlanBacktrack` + `ReplaceHistory`（`backtrack.go:258-268`、`backtrack_actor.go:241-279`）；原子、可审计（可复用 `AppendBacktrackTombstone` 的落库口径）；前端只需一次请求 | 需要后端排期 | **推荐主路径** |
| B. 前端组合现有 API | 前端 `createRuntimeSession` 后再逐条把前缀消息写进新会话 | 不改后端 | 需要新暴露「原始消息写接口」（现在只有 `AddMessage` 的存储层 `storage.go:47`，无 HTTP）；逐条写会破坏幂等/顺序/审计；大历史下 N 次请求 | 不推荐 |
| C. 只做会话级 Fork（复制全量历史） | 复用 A 的端点，锚点 = 会话末尾 | 可作为 A 的**子集**与降级路径 | 不提供「从中间分叉」 | 作为 A 的**默认锚点**（侧栏入口） |

**决策**：按 A 实施；侧栏「Fork」入口改为调用同一端点并使用「会话末尾」为锚点（等价于 C），从而消除现有「伪 Fork」的语义谎报（`session-row-actions.ts:3-5` 明确写了不复制历史）。

**降级策略**（后端未就绪时）：保持现状行为，但**文案与 UI 不得出现「分支」字样**（现名 `sidebar.session.fork` / `forkSuffix`，`zh-CN/workspace/base.ts:140-141`），避免用户以为历史被复制。

#### 4.2.3 子会话呈现：缩进子行 + 来源徽标（A+B 组合）

| 方案 | 改动面 | 优点 | 风险 | 结论 |
|---|---|---|---|---|
| A. 缩进子行（紧邻父行） | 视图组装 `use-session-group-view.ts:108-119` 之后重排；行模型 `session-row-view-model.ts:63-109` 增 depth/parent；行渲染 `sessions-section.tsx:367-455` 加 padding；`Thread`/`RuntimeSessionRecord` 传递 parent | 贴合参照形态，平铺模式下也能表达父子 | 与手动排序账目（`lib/workspace/session-order.ts`）和拖拽（`use-session-drag-reorder.ts`）的优先级需要新定；折叠计数口径要定 | **主线**（配合 B 的徽标） |
| B. 独立行 + 来源徽标 | 只改 `session-row-view-model.ts:63-109` 与 `session-item.tsx` 标题区 | 改动最小、与现有排序零冲突 | 不满足「紧邻/缩进」 | **并入 A**（徽标始终显示，便于跨组识别） |
| C. 组内树形（`role=tree`） | 新增 forest/flatten 纯逻辑（可对照 `session-agents-panel-shared.ts`），行容器改 `role="tree"`，行加 `aria-level/data-depth` | 长期可承载多级分支 | a11y 与拖拽协调复杂；`sessions-section.tsx` 受 ≤500 非空行门禁，需要先抽子组件 | 留作演进（不在本方案） |

**决策**：A+B。层级**只在渲染层表达**（父行后紧跟子行并缩进 12–14px），不改变 `updatedAt` 排序账目的存储；当用户手动拖拽导致父子分离时，以**父子簇**为最小拖拽单元（见 §5.5 约束 2）。

#### 4.2.4 命名与序号口径

- 现状：`buildForkSessionTitle(sourceTitle, fallbackSessionId, suffix)` 只做「基名 + 固定后缀」（`session-row-actions.ts:14-21`），e2e 断言标题含 `(branch)`（`sidebar-session-actions.spec.ts:46`）。
- 参照实现：分支标题带**同名后缀序号**（"… (2)" 形态）；本仓无任何同名计数逻辑。

**决策**：首个子会话沿用「基名 + 本地化后缀」，第 2 个及以后由**服务端**在创建时按「同一父会话下同名」去重生成序号（前端只负责传 `source_title` 兜底值，不做计数）。这样既不破坏现有 e2e 断言，也避免多客户端并发下的序号竞态。

#### 4.2.5 不可用态交互（无 Tooltip 原语的降级）

参照实现的关键细节：不可用时**不用原生 `disabled`**，否则按钮不派发 Tooltip 需要的 hover/focus 事件（`MessageIconActions.tsx:93-101`）。

本仓现状：全仓无 Tooltip 原语；`aria-disabled` 全仓仅 1 处（`components/ui/select.tsx:410`）。

**决策**（两档，均为 P1）：

1. **最小档**：`aria-disabled` + 原生 `title={t("...branchUnavailable")}` + 视觉降透明度 + 点击拦截（不做 `disabled`），并配 `sr-only` 原因文本（复用 `user-message-bubble.tsx:203-206` 的写法）。
2. **进阶档（可选）**：在 `components/ui/` 新增最小 Tooltip 原语（`role="tooltip"` + 焦点/悬停双触发），再把消息动作区切过去；该原语同时可为侧栏行操作复用。

> 注意：`title` 属性同样受 i18n 门禁扫描（`verify-frontend-i18n.ts:20-27`），必须写成 `t()` 调用。

---

## 5. 推荐方案

### 5.1 总览

```text
[入口 1] 轮尾行「在新对话中分支」（主入口，锚点=该轮末尾）
[入口 2] 侧栏会话行 Fork（锚点=会话末尾，整会话分支）

        └─► use-session-branch（可用性判定 + 请求编排 + 跳转）
                 │
                 ├─► POST /api/runtime/sessions/{id}/branch   ← 新增（后端批次 0）
                 │        └─ 服务端：取源历史 → 求前缀 → 建会话 → ReplaceHistory(前缀) → 写 lineage → 落库
                 │
                 ├─► refreshSessions()（快照刷新）
                 ├─► onResetTrajectory()（硬重置，避免轨迹串台）
                 └─► navigate(`/workspace/sessions/{newId}`)

[侧栏] 快照 → merge（携带 lineage）→ 组内排序 → 父子簇稳定化 → 缩进行 + 来源徽标
```

设计要点：**前缀只由服务端裁剪**；前端只传锚点、只消费新会话；父子关系**只存于 `metadata.context`**（不改表结构）。

### 5.2 批次 0：后端契约（前置，独立排期）

本方案不实现后端，但把契约固定下来（§6 给出字段级定义）。实现建议（供后端同学参考，均已核对到现有积木）：

1. 新增 handler `BranchSession`，注册到 `backend/internal/api/skills/handler.go:742` 一带（`/sessions/{id}/branch`，`http.MethodPost`）。
2. 复用 `chat.PlanBacktrack(sessionID, messages, checkpoints, req)` 求前缀（`backend/internal/chat/backtrack.go:258-268`）；缺省锚点 = 最后一条消息（全量前缀）。
3. seed 三步（已核实的最小改造面）：`NewSession(userID)`（空历史，`session.go:96-110`）→ `ReplaceHistory(prefix)`（深拷贝 + 补全 message/turn id + `HistoryLoaded=true`，`session.go:175-196`）→ `storage.Save`（对齐 `manager.go:69-85` 的创建路径）。`HeadOffset` 保持 `0` 即前缀全量可见。
4. 也可直接复刻 API 层既有范例（`handler.go:1833-1839,2127-2133`）：`Clone()` + `ReplaceHistory` + `sessionManager.Update`。
5. lineage 写入新会话 `metadata.context`：**新增专用键**，建议 `fork_parent_session_id` / `fork_root_session_id` / `fork_source_message_id` / `fork_origin_title`；**不要**复用 `agent_parent_session_id`（`backend/internal/agentcontrol/registry.go:9-24`），否则新会话会被 agent-control 面板当成子代理展示。写入沿用 `PATCH /sessions/{id}` 已有的「context 逐键合并」口径（`use-workspace-session-actions.ts:61-62`）。
6. 事务性：建会话成功但 seed 失败时必须**删除已建会话**再返回错误，避免留下空壳会话污染侧栏。
7. **可选项**：若要支持「从压缩点/文件快照分支」，锚点类型可扩展为 `checkpoint_id`（`backend/internal/checkpoint/manager.go:420,501-522` 已能返回 `ConversationMessages`，但仅 `exact && !preview` 无损）。

### 5.3 批次 1：API 客户端与编排 hook

| 文件 | 改动 |
|---|---|
| `frontend/src/types/runtime/sessions.ts` | 新增 `RuntimeSessionBranchRequest` / `RuntimeSessionBranchResponse`；为 `RuntimeSessionRecord` 增派生的只读视图类型（不改后端字段） |
| `frontend/src/api/runtime/sessions.ts` | 新增 `branchRuntimeSession(sessionId, request)`（`POST /api/runtime/sessions/{id}/branch`），错误路径复用 `fetchRuntimeJson` |
| `frontend/src/hooks/workspace/use-session-branch.ts`（新增） | 可用性门 + `pending` 状态 + 成功后的 `refreshSessions / onResetTrajectory / navigate` 编排（对齐 `use-workspace-session-actions.ts:85-108`） |
| `frontend/src/components/workspace/workspace-sidebar/session-row-actions.ts` | 把 `buildForkSessionRequest` 升级为 `buildBranchSessionRequest`（新增 `anchorMessageId` 透传；标题函数保持 `buildForkSessionTitle` 兼容 e2e） |
| `frontend/src/pages/workspace-page.tsx` | 实例化 hook（`:328-350` 一带），把 props 透传到 `WorkspaceShell`（`:415-428`） |

约束：hook 内**不写 `setThreads` 乐观插入**（保持与现有删除/重命名一致，靠快照兜底）；`pending` 期间按钮禁用并显示 spinner（复用现有 `LoaderCircleIcon` 写法，见 `user-message-bubble.tsx:250-254`）。

### 5.4 批次 2：消息动作区入口（主入口）

**可用性模型**（新增纯逻辑，单测友好）：

```ts
// frontend/src/lib/chat-view/branch-availability.ts（新增）
type BranchAvailability =
  | { kind: "available"; messageId: string }
  | { kind: "unavailable"; reasonKey: string };

/** 在 flow items 上求「唯一可分支锚点」：已完成轮次的最后一条消息。 */
export function resolveBranchAnchor(
  items: FlowItem[],
  options: { isResponding: boolean; hasPendingApproval?: boolean },
): BranchAvailability;
```

判定规则（与参照实现一致，`apps/web/tests/message-actions.e2e.ts:190-203`）：

1. 会话**不在**流式/等待审批中（`isResponding === false`）；
2. 锚点必须是**已完成轮次**的**最后一条**内容消息（其后没有该轮的新增内容）；
3. 该消息必须是**可寻址**的真实消息（有稳定 id，`history-mapping.ts:237,264`）；合成 id（`` `${sessionId}-history-${index}` `` 退化分支）不产生锚点，避免与服务端 id 对不上。

> 判定必须在**整条流**上求一次（O(n)），再把布尔值下发给每一行；**不要**在行组件里做「我是不是最后一条」的判断（会引入 O(n²) 与实时边界误判，`history-artifacts.ts:33-41` 的 live-only 追加是典型陷阱）。

**渲染接线**（纯透传为主）：

| 文件 | 改动 |
|---|---|
| `frontend/src/components/workspace/message-list/turn-tail-row.tsx:13-20,61-107` | 新增 props `canBranch / branchPending / branchDisabledReason / onBranch`；渲染 `IconBranch` 风格按钮（`aria-disabled` + 原因，不用 `disabled`） |
| `frontend/src/components/workspace/message-list/assistant-message-card.tsx` | 透传上述 props 到 `TurnTailRow` |
| `frontend/src/components/workspace/message-list.tsx:263-273` | 用 `resolveBranchAnchor(...)` 的结果驱动 `canBranch`，并在 `actionsDisabled` 中并入 `branchPending` |
| `frontend/src/components/workspace/message-list/types.ts:13-41` | 扩展 props 签名（`onBranchFromMessage(messageId)`、`branchPendingMessageId`、`branchDisabledReason(messageId)`） |
| `frontend/src/components/workspace/workspace-shell/main-section.tsx:341-368`、`workspace-shell.tsx:91-98,305-325`、`workspace-shell/types.ts:110-121` | 透传（无逻辑） |

**用户气泡是否也放一个入口**：参照实现**没有**（`message-actions.e2e.ts:190-191`：用户气泡不带 branch）。本方案**同样不加**，避免与「回溯（回到发送前）」语义混淆——两者在用户气泡上会变成两个含义相近但结果不同的按钮。

### 5.5 批次 3：侧栏谱系呈现

| 文件 | 改动 |
|---|---|
| `frontend/src/lib/thread-state/sessions.ts:88-120` | merge 时把 `metadata.context` 的 lineage 映射到 `Thread`（新增可选字段 `forkedFrom?: { sessionId: string; anchorMessageId?: string }`） |
| `frontend/src/data/mock/types.ts:103-121` | `Thread` 增加上述可选字段（纯前端视图字段，不入后端请求） |
| `frontend/src/hooks/workspace/use-session-group-view.ts:108-119` | 排序后追加一步 `stabilizeLineageOrder(...)`：子行紧随父行（父不在本组/不存在时保持原位） |
| `frontend/src/lib/workspace/session-lineage.ts`（新增） | 纯函数：`buildLineageIndex(threads)` / `stabilizeLineageOrder(rows)` / `resolveRowDepth(row)` |
| `frontend/src/components/workspace/workspace-sidebar/session-row-view-model.ts:63-109` | 行模型增加 `depth`（0/1）与 `forkBadge`（来源提示） |
| `frontend/src/components/workspace/workspace-sidebar/sessions-section.tsx:367-455` | 渲染缩进（`paddingLeft: depth*12`）与徽标；`role=treeitem` + `aria-level={depth+1}`（对齐 `session-agents-tree.tsx:165-175` 口径） |
| `frontend/src/components/workspace/workspace-sidebar/session-item.tsx:306-395` | 徽标位与 `title` 提示；菜单项 `Fork` 改名/改语义（批次 1 已改 builder） |

**三条必须遵守的约束**（否则侧栏账目会崩）：

1. **折叠计数**：`session-grouping.ts:51-87` 的可见性裁剪需要明确「子行是否计入 `limit`」。建议：**计入**（保持「Show N more」只按行数说话），但**子行不单独折叠**——父行隐藏时子行一并隐藏。
2. **拖拽**：`use-session-drag-reorder.ts` 以**父子簇**为最小移动单元；不允许把子行拖到与其父不同的位置（拖拽落点计算需先归并簇）。
3. **排序账目**：`lib/workspace/session-order.ts` 的手动顺序**优先于**层级稳定化（用户显式排过的顺序不被自动重排覆盖）；但父行被手动移到别处时，其子簇跟随。

### 5.6 批次 4：i18n、测试与门禁

**i18n（两语言同批）**：

| 语言文件 | 新增 key |
|---|---|
| `frontend/src/i18n/resources/{zh-CN,en-US}/workspace/panels-messages.ts` | `branch`（=「在新对话中分支」/ "Branch into a new conversation"）、`branchUnavailable`（=「仅可从已完成轮次的最后一条消息分支」/ "Available only on the last message of a completed turn"）、`branchFailed`（失败提示） |
| `frontend/src/i18n/resources/{zh-CN,en-US}/workspace/base.ts:140-149` | `forkMenuItem` 措辞（若侧栏入口语义升级为「整会话分支」）、`forkBadge`（子会话来源徽标） |

> 文案直接沿用参照实现的中英文（`packages/client/ui-chat/src/client/locale.ts:71-72,182-183`），避免自创口径造成后续对比困难。

**测试计划**：

| 层级 | 文件 | 断言 |
|---|---|---|
| 单测 | `frontend/src/lib/chat-view/branch-availability.test.ts`（新增） | 空流/流式中/中断消息尾部/合成 id/正常末尾 五种输入的锚点结论 |
| 单测 | `frontend/src/lib/workspace/session-lineage.test.ts`（新增） | 父行在后、父行跨组、父行缺失、多子行、与手动排序冲突时的稳定化结果 |
| 单测 | `frontend/src/components/workspace/workspace-sidebar/session-row-actions.test.ts:9-49` | 扩展：`buildBranchSessionRequest` 带 `anchor_message_id`；不带锚点时退化为整会话分支 |
| 单测 | `frontend/src/hooks/workspace/use-session-branch.test.tsx`（新增） | 可用性门、pending、成功后的 navigate 与 reset 调用顺序、失败不改路由 |
| e2e | `frontend/e2e/sidebar-session-actions.spec.ts:46` | 既有「Fork 生成带分支后缀的独立新会话」保持通过（或不改断言、只改实现） |
| e2e | `frontend/e2e/session-branch.spec.ts`（新增） | ① 仅轮末尾按钮可用，其余 `aria-disabled=true` 且有原因；② 点击后发出 `POST /sessions/{id}/branch` 且 body 带 `anchor_message_id`；③ 新会话 URL 落 canonical 且侧栏出现缩进子行；④ 源会话历史不变（回源后消息数一致） |

**门禁命令**（提交前逐条）：

```bash
cd frontend
npm run lint        # eslint + i18n + no-backups + max-lines + message-tokens
npm test            # vitest run
npm run build && npm run test:e2e
```

### 5.7 改动文件总表（按批次）

| 批次 | 文件数 | 新增 | 修改 |
|---|---|---|---|
| 1（API+hook） | 5 | `hooks/workspace/use-session-branch.ts` | `types/runtime/sessions.ts`、`api/runtime/sessions.ts`、`session-row-actions.ts`、`pages/workspace-page.tsx` |
| 2（消息入口） | 6 | `lib/chat-view/branch-availability.ts` | `turn-tail-row.tsx`、`assistant-message-card.tsx`、`message-list.tsx`、`message-list/types.ts`、`workspace-shell/*`(3 文件透传) |
| 3（侧栏谱系） | 7 | `lib/workspace/session-lineage.ts` | `lib/thread-state/sessions.ts`、`data/mock/types.ts`、`use-session-group-view.ts`、`session-row-view-model.ts`、`sessions-section.tsx`、`session-item.tsx` |
| 4（i18n/测试） | 6+ | 3 个测试文件 | 4 个 i18n 资源文件、2 个既有测试文件 |

> 注意 `verify-max-lines.mjs:23-26` 的 ≤500 非空行门禁：`sessions-section.tsx` 与 `message-list.tsx` 都已接近上限，批次 2/3 落地时**先抽子组件再接线**。

---

## 6. 接口契约（建议稿）

### 6.1 `POST /api/runtime/sessions/{id}/branch`

> 端点命名：本文统一写作 `/branch`（贴合消息级入口语义）；后端调研建议命名为 `/fork`（与参照实现的 `forkSession` 一致）。**落地时以实际路由为准，前端只需改一处常量。**

**请求**

```json
{
  "anchor_message_id": "msg_01J...",
  "include_anchor": true,
  "title": "可选：覆盖默认标题（缺省由服务端按源标题 + 后缀生成）",
  "user_id": "可选：多用户视图下的归属用户"
}
```

字段语义：

- `anchor_message_id`：**可缺省**。缺省 = 会话末尾（整会话分支，供侧栏入口使用）。给定时，服务端把锚点归一化为「该锚点所在的**已完成轮次**的历史前缀上界」；若锚点不是已完成轮次的末尾，返回 `409`（而不是静默截断）。
- `include_anchor`：锚点消息本身**是否计入前缀**，缺省 `true`（消息级入口的预期：新会话的末尾就是点击的那条消息，可以直接接着提问）。显式设 `false` 时新会话停在锚点**之前**（等价「重发该轮」语义）。
- `title`：缺省时服务端生成（源标题 + 本地化后缀 + 同名序号，见 §4.2.4）。

**响应 201**

```json
{
  "session": {
    "id": "session_...",
    "state": "active",
    "metadata": {
      "title": "原会话标题（分支）",
      "context": {
        "fork_parent_session_id": "session_source",
        "fork_root_session_id": "session_source",
        "fork_source_message_id": "msg_01J...",
        "fork_created_at": "2026-09-14T10:00:00Z",
        "workspace_path": "继承源会话的目录绑定"
      }
    }
  },
  "anchor": {
    "source_message_id": "msg_01J...",
    "turn_index": 3,
    "included": true
  }
}
```

> **message id 稳定性**：`ReplaceHistory` 会对缺失身份的消息调用 `EnsureHistoryMessageIdentities`（`backend/internal/chat/session.go:175-196`），因此**新会话内的消息 id 可能与源会话不完全一致**。响应里的 `anchor.source_message_id` 用于把「用户点击的那条」与「新会话内的对应消息」对上；前端不得假设跨会话 id 相等（本仓现有 UI 也没有该假设：历史投影自带 id，见 `history-mapping.ts:237,264`）。
```

**错误码**

| 状态 | 场景 | 前端处理 |
|---|---|---|
| 400 | 锚点格式非法 | 提示不可用原因，不跳转 |
| 404 | 源会话不存在 | 刷新快照并提示 |
| 409 | 锚点不在完成轮末尾 / 源会话正在生成中 | 提示原因（复用 `branchUnavailable` 文案） |
| 503 | 会话库被占用（既有 `writeSessionStoreError` 口径） | 提示可重试 |
| 501/404（端点未实现） | 后端未排期 | 降级：提示「当前后端不支持真实分支」，不改路由 |

### 6.2 读侧复用（不新增）

- 新会话历史：直接走既有 `GET /api/runtime/sessions/{id}/history`（`handler.go:2517-2566`），前端**无需**为新会话做特殊渲染。
- 父子关系：由 `metadata.context` 携带，前端在 merge 阶段解析（§5.5）。

---

## 7. 时序（成功路径）

```text
用户 → 轮尾按钮（仅可用态可点）
  → use-session-branch.branch(messageId)
    → resolveBranchAnchor 复核（防抖/竞态）
    → POST /sessions/{source}/branch {anchor_message_id}
      → 后端：load 源会话 → PlanBacktrack(前缀) → CreateSession
              → ReplaceHistory(prefix) + SetHeadOffset(0) + persist
              → 写 metadata.context lineage
      ← 201 { session }
    → refreshSessions()            // 触发快照拉取
    → onResetTrajectory()          // 硬重置，避免轨迹串台
    → navigate(`/workspace/sessions/{newId}`)
  → 侧栏：pinned 兜底拉取 → merge（解析 lineage）→ 组内排序 → 父子簇稳定化 → 缩进渲染
  → 会话页：history 拉取 → 线程物化 → 消息流渲染（前缀 + 可继续对话）
```

失败路径：任一环节失败都**不改变当前路由与选中会话**；`branchFailed` 以现有错误提示位（会话页 notice）呈现，`pending` 态复位。

---

## 8. 验收标准与发布清单

### 8.1 功能验收（可勾选）

> 落地证据（2026-09-14）：前端门禁 `npm run lint` / `npx vitest run`（192 文件 / 1424 用例）/
> `npm run build` / `npx playwright test`（76 用例）全绿；后端新增用例见
> `backend/internal/chat/branch_test.go`、`backend/internal/api/skills/session_branch_handlers_test.go`。

- [x] 从**轮末尾**发起分支后，新会话历史 = 源会话在该轮次结束处的**完整前缀**（消息内容、角色、顺序一致）。
      （`PlanBranch` 前缀单测 `TestPlanBranchTurnTailAnchor` + handler `TestBranchSessionFromTurnTailAnchor`
      断言 `sourceIDs[:2] == branchMessageIDs`；e2e ③ 断言分支会话仍见两轮答案）
- [x] 源会话**零改动**：分支前后源会话的历史条数、最新轮次、`updatedAt` 语义不变。
      （`TestPlanBranchPrefixIsIndependentCopy`；`TestBranchSessionFromTurnTailAnchor` 断言源会话历史/标题/lineage 全未变；
      e2e ④ 逐字段比对分支前后 `GET /history`；`cloneBranchPrefix` 深拷贝 + 只对**新会话**调 `ApplyForkLineage`）
- [x] 分支后的新会话可以**继续对话**，其上下文包含前缀（发送新消息后模型可见前缀）。
      （CI 口径：分支会话经既有 `sessionManager.CreateSession` + `ReplaceHistory`（置位 `HistoryLoaded`）
      + `Update` 落库，读回即普通可继续会话，且可被 `List` 发现；实机 `SubmitPrompt` 确认记入 §10 Q9）
- [x] 非末尾消息 / 流式中 / 待审批时，入口**可见但不可用**，且有可读原因。
      （`branch-availability.test.ts` 五种输入；e2e ① 断言 `data-branch-state=unavailable` +
      `aria-disabled=true` + `aria-describedby` 指向原因文案）
- [x] 侧栏中新会话**紧随父会话**并缩进显示，且带来源徽标。
      （`session-lineage.test.ts`（父行在后 / 跨组 / 父行缺失 / 多子行 / 手动排序冲突）；
      e2e ③ 断言 `aria-level=2` + `data-depth=1` + `session-fork-badge` = "Branch" 且位于父行下方）
- [x] 切换会话时轨迹面板**不串台**（沿用 `trajectoryStore.reset({hard:true})`，`workspace-page.tsx:152-157`）。
      （`use-session-branch.test.tsx` 断言成功路径调用顺序 refresh → reset → navigate；
      `workspace-page.tsx:205,220` 均为 `reset({hard:true})`）

### 8.2 兼容性验收

- [x] 既有 e2e「Fork 生成带分支后缀的独立新会话」（`frontend/e2e/sidebar-session-actions.spec.ts:46`）在语义升级后仍通过（**已同步更新断言**，原因见下）。
      - 原因 1：侧栏入口语义升级为「整会话分支」，请求从 `POST /sessions` 变为 `POST /sessions/{id}/branch`，
        断言改为拦截 `/branch`、校验 body 无 `anchor_message_id`、落点正则 `e2e-branch-\d+`。
      - 原因 2：Playwright glob 的 `*` **不跨 `/`**，旧 glob `**/api/runtime/sessions*` 拦不到新端点；
        改为 `**/api/runtime/sessions/*/branch`（同文件 DELETE 用例早已有同样注释）。
      - 新增断言：分支后侧栏仍是「源行（depth 0）+ 子行（depth 1）」两行，源会话不被消耗。
- [x] 既有分组/排序/折叠 e2e（`session-grouping.spec.ts`、`session-order.spec.ts`、`session-collapse.spec.ts`）全绿。
      （全量 `npx playwright test`：76 passed；含 order/grouping/collapse/search/stats/thread-link 等侧栏相关用例）
- [x] `frontend/scripts/verify-max-lines.mjs` 非空行门禁通过（必要时先抽子组件）。
      （`[verify-max-lines] OK（918 个 .ts/.tsx，0 个 > 500 非空行，最大 sessions-section.tsx = 500）`）
- [x] 两语言词典齐备（`zh-CN` / `en-US`），`verify-frontend-i18n.ts` 无硬编码告警。
      （`i18n lint OK（scanned=656, violations=0）`；新增 `branch` / `branchUnavailable` / `branchFailed` /
      `forkBadge` / `forkBadgeTitle` / `forkSuffix` 两语言同步）

### 8.3 发布清单

1. 后端分支端点可用（健康检查：`POST /sessions/{id}/branch` 返回 201）。
2. 前端按批次 1 → 2 → 3 → 4 合并，每批次独立跑门禁。
3. 灰度观察项：分支请求失败率、seed 耗时（P95）、侧栏渲染耗时（父子簇稳定化的 O(n) 开销）。

---

## 9. 风险与回滚

| 编号 | 风险 | 触发条件 | 缓解 / 回滚 |
|---|---|---|---|
| R1 | 前后端契约漂移（`anchor_message_id` 语义不一致） | 前端按「轮末尾」传锚点，后端按「消息索引」裁剪 | 契约冻结（§6）；后端归一化职责写进接口文档；前端只传不裁剪 |
| R2 | 侧栏排序/拖拽回归 | 父子簇重排与手动账目冲突 | 稳定化**只在组内**生效，且以 `lib/workspace/session-order.ts` 账目为最高优先级；拖拽按簇归并；批次 3 单独提交便于回滚 |
| R3 | 大历史 seed 性能/体积 | 超长会话（数千条消息）在轮末尾分支 | 后端设前缀上限与超限策略（截断尾部保留 + 提示）；前端 pending 态给 spinner 与超时提示 |
| R4 | 子会话跨组（父绑定目录、子未绑定或反之） | 父会话 `metadata.context.workspace_path` 缺失 | 子会话继承父的 `workspace_path`（与现有 `buildForkSessionRequest` 一致 `session-row-actions.ts:36-46`）；无法继承时提示「未分组」 |
| R5 | 重复点击产生多个分支 | 网络抖动 / 双击 | `pending` 门 + 按钮禁用；后端可选支持幂等键（同 `source+anchor` 在 N 秒内复用） |
| R6 | 轨迹/事件串台 | 分支后仍在旧会话轨迹上 | 沿用既有 `onResetTrajectory()` + canonical 路由（`use-workspace-session-actions.ts:105-106`） |
| R7 | 新会话消息 id 与源会话不一致（`ReplaceHistory` 会重铸 id） | 任何分支 | 前端不做跨会话 id 等值假设；用响应里的 `anchor.source_message_id` 建立映射（§6.1） |
| R8 | file 存储后端历史加载路径未完全核实 | 使用 file backend 的部署 | 分支端点落地前先补 file backend 的 `Load` 完整 history 用例（`backend/internal/chat/file_storage.go:585-616`） |
| 回滚 | 需要整体下线 | — | 批次 1–4 均为前端增量：批次 2 可单独移除按钮渲染；批次 3 的 lineage 稳定化是纯函数，可通过返回空索引直接停用；后端端点无副作用（不调用即不生效） |

---

## 10. 未决问题（需产品 / 后端确认）

| 编号 | 问题 | 影响 | 建议 |
|---|---|---|---|
| Q1 | 分支标题序号口径（`(N)` 全局 / 按父 / 按标题） | 命名一致性 | 服务端按「同一父会话 + 同名基名」去重生成；前端不做计数 |
| Q2 | 分支是否包含**工作区文件快照** | 会影响「代码是否一起分叉」的预期 | 一期只做**对话历史**分支；文件快照作为二期（可复用 checkpoint：`handler.go:765-768`） |
| Q3 | 子行是否计入「Show N more」的 `limit` | 折叠行为 | 计入行数，但父行隐藏时子行一并隐藏（§5.5 约束 1） |
| Q4 | 侧栏菜单项存量 `Fork` 是否保留 | 与消息级入口并存 | 保留但语义升级为「整会话分支」；文案明确不裁剪历史 |
| Q5 | 单会话分支数量上限 | 存储膨胀 | 后端加软上限（如 50）+ 超限 409 |
| Q6 | 后端端点的排期与归属 | 阻塞前置 | 本方案 §5.2 已给出可直接照搬的现有积木与落库手法 |
| Q7 | 分支会话是否需要「回到父会话」的跳转 | 导航体验 | 徽标可点击 → 跳父会话（二期） |
| Q8 | lineage 用哪些 context 键 | 与 agent-control / 压缩谱系的展示冲突 | 新建专用 `fork_*` 键（§5.2 第 5 条）；不复用 `agent_parent_session_id` |
| Q9 | fork 出的会话能否**直接接续对话**（actor hub 是否需预热/注册） | 影响「分支后即可提问」的验收项 | 后端实现时显式验证：对新会话直接 `SubmitPrompt` |
| Q10 | 是否要求消息 id 跨会话稳定（引用/高亮场景） | 影响 `ReplaceHistory` 的 id 重铸策略 | 一期按「允许不一致 + 响应回显锚点」处理（R7） |

---

## 11. 证据索引（本次调研核对清单）

**参照实现（`deepseek-harness`，外部仓库）**

- `packages/client/ui-chat/src/client/chat/MessageIconActions.tsx:20-24`（`onBranch`/`branchUnavailable` 语义）、`:91-109`（按钮渲染、`aria-disabled`、原因文本）
- `packages/client/ui-chat/src/client/locale.ts:71-72`（zh 文案）、`:182-183`（en 文案）
- `packages/client/ui-workspace/src/client/navigation.ts:34,148`（`forkSession` 编排）、`.../contract/slots.ts:121`
- `packages/client/ui-workspace/src/client/rows/WorkspaceBrowser.tsx:589,716`（行级 `onFork`）
- `apps/web/tests/message-actions.e2e.ts:174-204`（可用性规则）、`:220-226`（fork 后父会话标识）

**本仓前端**

- 动作区：`frontend/src/components/workspace/message-list/user-message-bubble.tsx:51-52,199,212-224,225-257`；`frontend/src/components/workspace/message-list/turn-tail-row.tsx:5,13-20,61-107`
- flow：`frontend/src/lib/chat-view/flow.ts:299-304`
- 宿主与门控：`frontend/src/components/workspace/message-list.tsx:63-65,195-205,222-240,263-273`
- 稳定 id 与实时边界：`frontend/src/lib/thread-state/history-mapping.ts:237,264`、`frontend/src/lib/thread-state/history-artifacts.ts:33-41`
- 回溯锚点：`frontend/src/hooks/workspace/session-backtrack/helpers.ts:137-157`；对话框 `frontend/src/components/workspace/message-backtrack-dialog.tsx:11-18,32-44,56-61`；挂载 `frontend/src/components/workspace/workspace-shell/overlays-section.tsx:106-120`
- 会话类型：`frontend/src/types/runtime/sessions.ts:1-44`；线程 `frontend/src/data/mock/types.ts:103-121`
- 合并与排序：`frontend/src/lib/thread-state/sessions.ts:10-137,174-186`
- 快照与兜底：`frontend/src/hooks/workspace/use-runtime-sessions-data.ts:169-230`；`frontend/src/hooks/workspace/runtime-sessions-data/loading.ts:13-60`
- 侧栏管线：`frontend/src/components/workspace/workspace-sidebar-shared.ts:118-150,170-200,253+`；`frontend/src/hooks/workspace/use-session-group-view.ts:95-119`；`frontend/src/lib/workspace/session-grouping.ts:5-16,24-28,51-87`
- 侧栏渲染：`frontend/src/components/workspace/workspace-sidebar/sessions-section.tsx:296-356,363-365,367-455,456-467`；`.../session-row-view-model.ts:63-109`；`.../session-item.tsx:243-299,306-395`
- 缩进先例：`frontend/src/components/workspace/session-agents-tree.tsx:165-175`；`frontend/src/components/workspace/session-agents-panel-shared.ts`
- Fork 现状：`frontend/src/components/workspace/workspace-sidebar/session-row-actions.ts:1-5,14-21,27-47`；`frontend/src/hooks/workspace/use-workspace-session-actions.ts:61-62,84-108,127-140`
- API 客户端：`frontend/src/api/runtime/sessions.ts:39-54,88-102,104-163`
- 路由与页面：`frontend/src/App.tsx:131-139,140-148`；`frontend/src/pages/workspace-page.tsx:45,81-84,152-157,162-187,212-217,328-350,386,415-428`；`frontend/src/hooks/workspace/use-session-history-sync.ts:30,54`
- 线程选择/占位：`frontend/src/hooks/workspace/use-workspace-thread-selection.ts:80-93,95-111,262-265`
- i18n：`frontend/src/i18n/resources/zh-CN/workspace/base.ts:140-141`、`.../en-US/workspace/base.ts:148-149`、`.../{zh-CN,en-US}/workspace/panels-messages.ts`
- 门禁：`frontend/package.json`（`lint` / `test` / `test:e2e`）、`frontend/scripts/verify-frontend-i18n.ts:20-27`、`frontend/scripts/verify-max-lines.mjs:23-26`
- 测试面：`frontend/src/components/workspace/workspace-sidebar/session-row-actions.test.ts:9-49`、`frontend/src/lib/workspace/session-grouping.test.ts`、`frontend/src/lib/workspace/session-order.test.ts`、`frontend/src/hooks/workspace/use-session-order.test.tsx`、`frontend/src/components/workspace/workspace-sidebar/use-session-drag-reorder.test.tsx`、`frontend/src/hooks/workspace/use-workspace-thread-selection.test.ts`、`frontend/e2e/sidebar-session-actions.spec.ts:46,67`、`frontend/e2e/session-grouping.spec.ts:260,296,323`、`frontend/e2e/session-order.spec.ts:69,95,127`、`frontend/e2e/session-collapse.spec.ts:130`、`frontend/e2e/mock-server.mjs`

**本仓后端（契约可行性）**

- 路由表：`backend/internal/api/skills/handler.go:735-782`（无 fork/clone；backtrack `:770-772`；checkpoints `:765-768`；`DELETE /history` `:782`）
- 建会话：`backend/internal/api/skills/handler.go:2304-2371`
- 历史读：`backend/internal/api/skills/handler.go:2517-2566`
- 前缀重写实现：`backend/internal/chat/backtrack_actor.go:241-279`
- 前缀计算：`backend/internal/chat/backtrack.go:258-268`（`PlanBacktrack`）、`:75-81,120-150`
- 回溯端点：`backend/internal/api/skills/backtrack_handlers.go:16,96,102`
- 存储接口：`backend/internal/chat/storage.go:24-60,67-70`
- 会话模型与创建：`backend/internal/chat/session.go:34-38,56-77,96-110,175-196,632-641,974-990`；`backend/internal/chat/manager.go:69-85,103-106`
- 持久化路径：`backend/internal/chat/actor.go:2701-2757`；`handler.go:1833-1839,2127-2133,3351+`；`backend/internal/chat/file_storage.go:585-616`
- checkpoint 会话快照：`backend/internal/checkpoint/manager.go:420,501-522`；`backend/internal/checkpoint/types.go:60-67`
