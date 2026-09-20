# 主/子会话 Transcript 分离：现状分析与实施建议（aicli TUI/console + frontend）

- 日期：2026-09-19
- 状态：分析已定稿；P0/P1 已实施（G1 父侧隔离 + G2/G3 只读子会话视图 + G4/G5/G7 实时与交互），
  P2 已落地三项（G9 历史回放 + `/export` 提示 + G8 前端第二入口），其余 P2 项见 §8.3
- 范围：`backend/cmd/aicli`（TUI/console）+ `frontend/`（Workspace Chat）+ 事件桥隔离面
- 需求原文（三条）：
  - (a) 主界面只能查看**主 agent** 的会话 transcript；
  - (b) 进入子会话需通过接口/子命令 `/agents`、`/agent xxx` 查看**子 agent** 的 transcript 界面；
  - (c) frontend 项目可使用单独界面或入口。

---

## 1. 结论摘要

| 需求 | 现状判定 | 摘要 |
|---|---|---|
| (a) 主界面只显示主 agent transcript | **FIXED（本轮）** | 交互式 timeline 渲染段补齐 `isForeignSessionContentEvent` 守卫（G1），子会话 tool/正文/思考行不再进入父 Scene；原锁死旧行为的回归断言已按隔离口径翻转 |
| (b) TUI/console 经 `/agents`、`/agent xxx` 查看子 agent transcript | **P0 DONE（本轮）** | 新增只读视图 `/agents view|open|transcript [target]` 与等价单数入口 `/agent [target]`（G2/G3）；数据源为子会话事件流（尾部窗口），popup/console 双态渲染，视图内不提供发送入口 |
| (c) frontend 单独界面/入口 | **YES（已实现）** | `SubagentSessionDialog` 下钻弹窗已完整落地：入口=父轨迹 `subagent` item，数据源=子会话自己的 `runtime/events` + `runtime/stream`，独立 store、不污染父流，含审批处理（§2.3） |

**总体判断**：这是一次「数据面已就绪、TUI 表现面缺失、隔离面留尾」的功能检查。
- 数据面（子会话事件/消息可读）已具备，无需新建存储或端点；
- 缺的是 TUI 侧的**入口 + 视图 + 只读纪律**（(b)）；
- (a) 的残留泄漏点必须先与既有测试断言口径达成一致（§6 Q1），否则实现子视图后父视图仍不干净。

---

## 2. 现状盘点（证据）

### 2.1 数据面：子会话内容已有可读来源

| 能力 | 位置/证据 | 说明 |
|---|---|---|
| 子会话是真实持久化会话 | `backend/cmd/aicli/commands/chat_actor_registry.go:721`（回滚时 `Host.SessionStore.Delete(ctx, childSession.ID)`）；`chat_actor_approval_test.go:26`（`host.SessionStore.Save(ctx, child)`） | 子会话有独立 SessionID、独立 Messages、独立上下文键（`AgentSessionContextParentSessionID`） |
| 事件按会话持久化 | `chat.EventStore.AppendEvent/ListEvents(sessionID, afterSeq, limit)`；SQLite/InMemory 双实现（`backend/internal/chat/session_runtime_store.go`） | 与前端下钻弹窗同一来源 |
| chat SSE 轨迹事件落库 | `backend/internal/api/skills/trajectory_events.go`（`chat.sse.*` 前缀，帧 `_event.sequence` = 持久化 seq） | 是否覆盖子会话需实测（见 §6 V1） |
| 增量读取端点 | `GET /api/runtime/sessions/{id}/runtime/events?after=&limit=&wait_ms=`；`GET /sessions/{id}/runtime/stream`（durable + live 双路） | TUI 进程内可直接用 EventStore/bus，不必走 HTTP |
| 前端已消费同一来源 | `frontend/src/components/workspace/trajectory/subagent-session-dialog.tsx:4-8`（注释：入口/数据源/隔离）；`frontend/src/hooks/workspace/use-subagent-session.ts:149+` | 证明「按子会话 ID 读事件并独立投影」路径成立 |

**结论**：TUI 侧实现子会话 transcript 视图**不需要新持久化、不需要新端点**；缺的只是读取与渲染。

### 2.2 TUI/console 现状：只有协作概览，没有 transcript 视图

- `/agents` 命令已存在，子命令为 `panel/pick/target/send/followup/routing test/cleanup`
  （`backend/cmd/aicli/commands/chat_slash_command_catalog.go:121-158`）。
- `/agents panel` = **Agent Control Panel**，三栏固定结构：`agents / mailbox / timeline`
  （`chat_debug.go:1603-1610` `chatAgentPanelPaneAgents/Mailbox/Timeline`；modal 渲染 `chat_debug.go:1808+`）。
  内容为：注册表行（`chatAgentPanelRegistryLine`）、agent 行（`chatAgentPanelModalAgentLines`）、mailbox、timeline ——
  **不含子会话的 user/assistant/reasoning/tool transcript 内容**。
- `/collab`（mailbox 协作事件）与 `/timeline`（team 协作事件）同为**协作控制面**，非内容面（catalog `:159-199`）。
- `/history` 只读当前会话；`/load <session-id>` 会**切换主会话**（不是"查看"）；`OpenTranscript` 只打开当前会话的只读 pager
  （`chat_command_result.go:120-125`；`chat.go:1501` `openChatTranscriptPager(session)`）。
- 目录中**没有** `/agent`（单数）命令；没有任何"子会话 transcript 视图"入口。
- 可复用的现成件：
  - read-only semantic transcript pager：`backend/cmd/aicli/ui/transcript_pager.go` + `canOpenChatTranscriptPager/openChatTranscriptPager`；
  - fixed-bottom popup surface（panel 已在用：`showChatAgentPanelPopup` / `ShowPopupPreserveCursorForOwner`）；
  - agent target 解析：`chatAgentGraphItems` / `chatAgentGraphLinesAndSelectedSession`（`chat_debug.go:1332`，返回 selected 对应会话）、`nextChatAgentPanelTarget`（`chat_debug.go:1562`）。

### 2.3 前端现状：需求 (c) 已实现

| 组件 | 作用 |
|---|---|
| `components/workspace/trajectory/subagent-session-dialog.tsx` | 子会话下钻对话框（P1-5 方案 1）：入口=父轨迹 `subagent` item（`session_id`/`agent_id` 即子会话 ID）；数据源=子会话自己的 `runtime/events` + `runtime/stream`；紧凑实时列表（最近 400 条）；状态机 idle/loading/live/reconnecting/closed/error |
| `components/workspace/trajectory/subagent-session-target.ts` | 下钻目标模型（sessionId/agentId/role/status） |
| `hooks/workspace/use-subagent-session.ts` | 独立 store + 增量拉取 + 重连（`maxReconnects`）+ `pendingApproval/resolveApproval` |
| 接线 | `components/workspace/trajectory/trajectory-view.tsx:512-515` 挂载 dialog |
| 相邻能力 | `session-agents-panel.tsx` / `session-agents-tree.tsx`（会话 agents 面板）、`runtime-teams/*`（团队视图） |
| 测试 | `subagent-session-dialog.test.tsx`、`subagent-session-dialog-trajectory.test.tsx`、`use-subagent-session.test.ts` |

**判定**：(c) 不需要新做；可选增强见 G8（把下钻入口从轨迹页扩展到 session-agents-panel）。

### 2.4 隔离现状：主 transcript 纯净度（需求 (a) 的执行面）

根因：子 agent 与父会话**共用同一 EventBus**（`backend/internal/agent/child_factory.go`：`childAgent.SetEventBus(parent.GetEventBus())`），
桥按事件类型兜底渲染，会话身份校验是后补的。

**已修复（本轮 `1f0d84f8`）**：
- `isForeignSessionContentEvent`（`chat_runtime_events.go:7139`）覆盖 Scene/事件日志/exec JSONL/ACP 输出面；
- `isForeignSessionTerminalEvent`（`chat_runtime_events.go:5675`）阻止子会话 `session_end`/`session_interrupted`
  提前 finalize 父回合的思考块/流式正文（否则任一子代理结束都会把父输出截成多段）；
- 子会话 `llm.retry` 不再写父状态行（`chat_runtime_events.go:4691-4695` 守卫）。

**未决泄漏（P0，需先裁定）**：
- 交互式 console 下，子会话 `tool.started/finished` 仍渲染进父 timeline（探针实测 `• Running view` / `[tool] ls`）；
- 我加的 timeline 守卫能让它消失，但已提交的 `chat_docs_team_regression_test.go:202` 断言
  `[tool] ls` / `[tool] view` **必须出现在父时间线**，因此该测试**依赖这条泄漏路径**（A/B：HEAD PASS → 加守卫 FAIL → 回退 PASS）；
- 设计口径冲突：`backend/internal/supervision/subagent_progress.go` 文件头明确
  "Child progress therefore cannot pollute the parent transcript/replay"（P1-5 方案 2），
  与上述测试断言相反；断言来源是早期 CLI 大提交（`42bba1ce`），并非多代理设计决策。

**设计内放行面（非缺陷）**：team/task 生命周期、mailbox 投递、`subagent.progress` 镜像（SessionID 已归父会话）、
critical subagent 终态、子会话 `session_end`（父侧 TitleNotifier 按会话清理工具行）、身份缺失的启动早期事件。

---

## 3. 差距清单（Gap List）

| # | 需求 | 差距 | 优先级 |
|---|---|---|---|
| G1 | (a) | 子会话 `tool.started/finished` 行泄漏进父 timeline，且被 `chat_docs_team_regression_test.go:202` 锁死 | **P0（先裁定）** |
| G2 | (b) | TUI/console 无任何子会话 transcript 视图 | **P0（核心）** |
| G3 | (b) | `/agents` 无 `view/open/transcript` 子命令；无 `/agent <target>` 别名 | **P0** |
| G4 | (b) | 子会话事件 → 语义 transcript 的渲染适配缺失（父侧 encoder 未按"目标会话"参数化） | P1 |
| G5 | (b) | 子视图内无审批/问题（question）处理入口（前端有，TUI 无） | P1（可复用 supervision 链） |
| G6 | (b) | 子视图"只读"边界未定义（是否允许在视图内发消息） | P1（设计约束） |
| G7 | (b) | agent target（路径）↔ 子会话 SessionID 的映射需在视图入口处统一（panel 已有 target 语义） | P1 |
| G8 | (c) | 前端下钻入口只在轨迹页；可从 session-agents-panel 增加第二入口 | P2 |
| G9 | (b) | 历史（已结束）子会话的回放路径未定义（SessionStore 保留期内可读） | P2 |

---

## 4. 设计方案（TUI/console）

### 4.1 入口设计（满足 (b)）

- 新增 `/agents view [target]`（别名 `open`/`transcript`）：
  - 无 target：打开 agent picker（复用 `pick` 的候选），或列出可下钻子会话；
  - 有 target：解析为子会话 SessionID 并打开视图。
- 新增单数命令 `/agent [target]`：等价 `/agents view [target]`；无参数时列出子会话清单（含 id/role/status）。
- target 解析复用现成语义：`chatAgentGraphItems` / `chatAgentGraphLinesAndSelectedSession`（返回 selected 会话）、
  `nextChatAgentPanelTarget`、`chatSessionSelectedAgentTarget`；G7 要求把"target→SessionID"收敛为单一函数。
- **不要**用 `/load <child-session-id>` 实现子视图：它会把子会话切成主会话，直接破坏需求 (a)。

### 4.2 视图形态

- TUI：复用 fixed-bottom popup surface（与 `/agents panel` 同款）或 read-only pager（`ui/transcript_pager.go`）；
  顶部标题：`agent=<id/path> role=<role> status=<state>` + `只读视图（Esc 关闭）`；
- console（legacy）：同一数据源直接 `fmt.Print` 行（与 `printChatAgentPanel` 同模式），保持两态行为一致；
- 键盘：Esc/q 关闭；↑↓ 滚动；←→ 切换 agent（沿用 panel follow 语义）；`f` 切换 follow（P1）；
- 子视图**不抢输入焦点**：不产生 composer 输入；发送消息仍走 `/agents send` / `/agents followup`（控制面单一，G6）。

### 4.3 数据源（推荐 A，B 作为历史回放补充）

| 方案 | 来源 | 优点 | 缺点 |
|---|---|---|---|
| A（推荐） | EventStore：`ListEvents(childSessionID, after, limit)` + live 订阅（进程内 bus 过滤 SessionID，或 `/runtime/stream`） | 与前端下钻弹窗**同源同构**；含 reasoning/工具中间态；可实时跟随 | 覆盖度取决于子会话是否写入 `chat.sse.*`（见 V1） |
| B | SessionStore canonical messages（`session.Messages`） | 历史完整（user/assistant/tool 结果）；实现简单 | 无实时流；reasoning/中间态缺失；与父视图渲染模型不同构 |

建议：实时跟随用 A；对已结束子会话回放时可 A+B 混合（G9）。

### 4.4 隔离纪律（硬约束，写入实现与测试）

1. 子会话事件只进入子视图的**独立 store/overlay**，绝不写主 Scene / 主 transcript / replay 日志；
2. 主 transcript 只接受主会话身份事件（依赖 G1 修复收口）；
3. 子视图只读：任何输入行为（发送/审批）都回到既有控制面命令，不在视图内私建通道；
4. 视图关闭后主视图**零残留**（需断言）。

### 4.5 与现有命令的关系

| 命令 | 定位 | 变更 |
|---|---|---|
| `/agents panel` | 协作概览（agents/mailbox/timeline） | 保留；agents 栏可加"`/agents view` 查看 transcript"提示 |
| `/agents view`（新） | 内容下钻（子会话 transcript） | 新增 |
| `/agent`（新） | `view` 的便捷入口 | 新增 |
| `/collab` `/timeline` | mailbox/team 协作事件 | 不变 |
| `/load` `/resume` | 主会话切换/恢复 | 不变（不得承载子视图语义） |

---

## 5. 实施计划与验收

### P0（1–2 天）：裁定 + 最小可用

- [x] G1：按隔离设计消除子会话 tool 行泄漏，并同步更新 `chat_docs_team_regression_test.go` 断言（Q1 裁定为"消除"；已实施，见 §8.1）；
- [x] G3：`/agents view [target]` + `/agent [target]` 命令与 target→SessionID 解析（含补全目录项；已实施，见 §8.1）；
- [x] G2：固定 popup/console 双态渲染子会话最近 N 条事件（只读、无 follow；已实施，见 §8.1）。

验收：
- 父 timeline 在子代理运行期间不含子会话 tool/正文/思考行（交互式 console 路径纳入测试）；
- `/agents view <target>` 打开后可见子会话 assistant/思考/tool 行，`/agent <target>` 等价；
- 关闭视图后主 Scene 无新增 cell（断言）；子视图事件不进父 replay 日志。

### P1（2–3 天）：实时与交互 —— 已落地（2026-09-19，证据见 §8.4）

- [x] G4：live 订阅 + 增量合并 + 终态收口（`startChatAgentTranscriptFollow` 按会话身份过滤，
      复用 `isForeignSessionTerminalEvent` 语义，避免子会话终态影响父）；
- [x] 滚动/分页/上限（对齐前端 `MAX_VISIBLE_ROWS=400` 口径：正文尾部窗口 + `truncated_rows=` 标记，
      标记不计入 400 行正文）；
- [x] G5：审批/问题行可操作（视图内提示 `/agents approve|deny|answer`，三个动词已接入真实控制面：
      审批走 `localActorRegistry.ResolveApproval`，问题走 `SessionActor.AnswerQuestion`）；
- [x] G7：target↔SessionID 映射一致性测试（panel / view / send 三处同一结果；
      视图入口优先 `resolveLocalAgentTargetSessionID`）。

验收：子会话运行中视图可实时更新；审批可在视图内闭环；视图不产生父状态行变更。

### P2（可选）—— 已落地三项（2026-09-19：G9 + 导出提示 + G8，证据见 §8.5/§8.6）

- [x] G9：历史子会话回放（SessionStore messages 回退）—— 事件窗口为空时回退 canonical
      messages（`source=messages` + `ended=true`），事件流有覆盖时仍以事件流为准（Q2）；
      回放不建立 live 订阅（follow 语义只在事件流上成立）；
- [x] G8：前端下钻入口扩展（session-agents-panel）—— 面板行在**后端上报会话键**
      （`sessionId`）存在时提供只读「会话记录」入口，复用既有 `SubagentSessionDialog`；
      无会话键的行不给入口（不以 `agentId`/`agentPath` 冒充 `runtime/events` 会话键）；
- [ ] 主 transcript 中 `subagent.progress` 镜像行作为跳转锚点（`/agents view` 快捷进入）——
      现状核查：CLI 父桥不渲染该镜像行（`subagent.progress` 为 live-only，仅 ACP 桥
      `agent_stdio_bridge.go` 合成 tool_call_update 行），无行可挂锚点，归入前端/ACP 面；
- [x] 子会话 transcript 导出（复用 `/export <session-id>` 既有能力）—— 视图 header 增
      `[hint] export: /export <session-id>` 可复制提示（不新增导出实现）。

---

## 6. 开放问题（需决策）与待验证项

| # | 类型 | 内容 | 建议 |
|---|---|---|---|
| Q1 | 决策 | G1 取舍：按隔离设计消除子会话 tool 行泄漏并改 `chat_docs_team_regression_test.go:202` 断言，还是保留现状？ | **建议消除**：需求 (a) 明确"主界面只看主 agent"；`subagent_progress.go` 设计注释与前端下钻实现均已按隔离口径；该断言来自早期 CLI 提交，非多代理设计决策 |
| Q2 | 决策 | 子视图数据源优先级：A 事件流（实时、同前端） vs B canonical messages（历史完整） | 建议 A 为主、B 作历史回放补充 |
| Q3 | 决策 | 命令命名：`/agents view` 单独，还是同时提供 `/agent <target>` 单数入口？ | 建议两者都提供（用户需求原文即含 `/agent xxx`） |
| Q4 | 决策 | 子视图内是否允许直接发消息？ | 建议不允许（保持 `/agents send` 单一控制面） |
| Q5 | 决策 | 历史（已结束）子会话是否纳入首期？ | 建议 P2，先覆盖运行中/近期子会话 |
| V1 | 验证 | 子会话在 EventStore 中 `chat.sse.*` 轨迹事件的实际覆盖度（正文/思考/工具） | 实施前用真实子会话抽查 `/api/runtime/sessions/{child}/runtime/events`；若覆盖不足，P0 先用 B 兜底 |
| V2 | 验证 | TUI 进程内 live 订阅路径（bus 过滤 SessionID）是否已有现成 helper | 复用 `runtime/stream` 同款过滤逻辑，避免新造 |

---

## 7. 非目标

- 不改 ACP 协议面语义（`agent_message_chunk` / `agent_thought_chunk` 等）；
- 不做多子会话并列分屏（P2+ 再评估）；
- 不改 `/load` / `/resume` 语义（主会话切换与"查看子会话"是两件事）；
- 不引入新持久化存储或新端点（复用 EventStore / SessionStore / runtime 端点）；
- 不改变子代理执行的权限/审批语义（视图只是读取面）。

---

## 8. 实施记录（2026-09-19，本轮落地）

裁定（对应 §6）：Q1=消除泄漏；Q2=事件流（A）为主、canonical messages 仅作 P2 历史回放补充；
Q3=双入口（`/agents view` + `/agent`）；Q4=只读（不提供发送入口）；Q5=历史子会话回放放 P2。
P0 三项已全部落地；全包回归通过：`cd backend; go test ./cmd/aicli/commands -count=1`
→ `ok … 98.260s`（含 `TestChatInteractiveDirectWriterInventory` 输出边界围栏与
`TestChatRuntimeEvents_SerializesConcurrentApprovalsAndQuestions` 单输入面串行化围栏），
`go build ./...` 通过。

P1（G4 follow / G5 审批闭环 / G7 映射一致性 + 400 行上限）同日落地并补齐回归：
`cd backend; go test ./cmd/aicli/commands -count=1` → `ok … 97.664s`；`go build ./...`、
`go vet ./cmd/aicli/commands` 通过（变更与证据见 §8.1、§8.4）。

P2 三项落地：G9 历史子会话回放（事件窗口为空时回退 SessionStore canonical messages，
`source=messages`）、`/export <session-id>` 可复制提示（见 §8.5）、G8 前端第二入口
（agents 面板行 → 只读 transcript 对话框，见 §8.6）。`subagent.progress` 锚点经核查
归入前端/ACP 面（CLI 父桥不渲染该 live-only 镜像行），CLI 侧无可挂锚点的行。

### 8.1 已落地变更

| # | 变更 | 位置 | 说明 |
|---|---|---|---|
| G1 | 父 timeline 身份守卫 | `backend/cmd/aicli/commands/chat_runtime_events.go`（`handleEvent` timeline 渲染段，`shouldSuppressTimelineDuringAssistantStream` 之后） | `isForeignSessionContentEvent(event)` 命中即 return：子代理正文/思考/工具行不再进入父 Scene；team/task/mailbox/subagent 终态等控制面投影仍放行 |
| G1-test | 回归断言翻转 | `backend/cmd/aicli/commands/chat_docs_team_regression_test.go` | teammate 的 `[tool]` 行不得出现在 lead timeline；控制面 `[task] blocked/completed` 行断言保留 |
| G2 | 只读子会话 transcript 视图 | 新增 `backend/cmd/aicli/commands/chat_agent_transcript.go` | `chatAgentTranscriptLines`：target→agent→SessionID；尾部读取 `ListEventsBefore`（store 未实现时退回 `ListEvents`）；assistant 正文直出、reasoning 带 `[reasoning]` 前缀、tool/控制面事件复用 `renderChatRuntimeTimelineEvent`；popup owner `agent_transcript` 与协作面板互不覆盖；`limit=N` 上限 2000（默认 200） |
| G3 | 双入口 + 补全 | `command.go`（`/agent` 精确 token 分发，不会吞掉 `/agents`）、`chat_command_result.go:346`（unified 管线）、`chat_debug.go`（`/agents view\|open\|transcript`）、`chat_slash_command_catalog.go`、`chat_slash_argument_completion.go` | `/agent [target] [limit=N]` ≡ `/agents view [target]`；补全目录项同步 |
| G1b | 交互例外 + 渲染隔离（裁定关键点） | `chat_runtime_events.go`（`handleEvent` 守卫与渲染段） | 子会话 `approval_requested`/`question_asked` 必须放行到父 console 单输入面（复用 `runtimeEventRequiresLegacyInteraction`），否则子代理等待输入永久阻塞；放行后 `rendered` 强制置空——子会话 prompt 正文/审批详情不进入父 timeline，交互提示只是输入面、审计回放走 `/agent <id>` |
| G3b | legacy 输出走统一边界（最佳实践） | `chat_agent_transcript.go`（`handleChatAgentTranscriptCommand`） | 去除 `fmt.Print*`，改经 `printChatCommandOutput`/`printfChatCommandOutput`（`chat_surface_output.go` 的 TerminalSession 所有权边界）；不向 `chatDirectWriterInventory` 债务账本新增原始 writer |
| G4 | follow 实时订阅 + 增量合并 + 终态收口 | `chat_agent_transcript.go`（`startChatAgentTranscriptFollow` / `refreshChatAgentTranscriptFollowPopup` / `stopChatAgentTranscriptFollow`；`/agent ... follow`、`/agents view ... follow`） | popup 模式非阻塞订阅 EventBus 并按目标会话身份过滤；到达事件用与快照相同的渲染口径增量合并；子会话终态渲染最终快照后解除订阅，父/兄弟终态被 `isForeignSessionTerminalEvent` 语义挡掉；legacy console 无 popup 时退化为"等待一次刷新"（`chatAgentTranscriptFollowOnceLines`） |
| G4-test | follow 回归断言 | `chat_agent_transcript_test.go: TestAgentTranscriptFollow_MergesChildEventsAndStopsOnChildTerminal` | 父 SessionEnd 不收口子视图；子增量 `follow-delta` 入视图；子终态后 `FollowActive=false`、`ended=true`；父 `History` 零变更 |
| P1-limit | 400 行上限 | 同上（`chatAgentTranscriptMaxVisibleRows`、`capChatAgentTranscriptRows`） | 尾部窗口与前端 `items.slice(-400)` 同口径；`truncated_rows=` 标记不计入 400 行正文；`rows=` 报正文行数（修正 trim 造成的 off-by-one） |
| G5 | 审批/问题视图内闭环 | `chat_agent_transcript.go`（`chatAgentTranscriptActionLines`、`handleChatAgentApprovalCommand`、`handleChatAgentAnswerCommand`）、`chat_debug.go`、`chat_slash_argument_completion.go` | pending 审批/问题在视图正文渲染可复制命令；`/agents approve\|deny [target] [request_id=]` 与 `/agents answer [target] <question_id> <text>` 接入真实控制面（此前只有提示文案、无命令实现）；补全目录同步 |
| G5-test | 闭环回归断言 | `chat_agent_transcript_test.go: TestAgentTranscriptView_PendingActionHintsCloseLoop` | 提示命令文本、参数解析（`request_id=`/位置 token）、缺参用法提示、dispatcher 动词识别 |
| G7 | target→SessionID 单一映射 | `chat_agent_transcript.go: buildChatAgentTranscriptView` | 优先 `host.ActorRegistry.resolveLocalAgentTargetSessionID`（与 send/approve/answer 同源），失败回退 agent 快照（保住轻量 harness 的 P0 口径） |
| G7-test | 映射一致性回归 | `chat_agent_transcript_test.go: TestAgentTranscriptView_TargetSessionMappingConsistent` | registry / view / panel picker 三处对同一 target 解析出同一 SessionID |
| G5b | legacy 输出边界围栏修复 | `chat_debug.go`（approve/deny/answer 三个 case） | 新动作输出改走 `printChatCommandOutput`：`TestChatInteractiveDirectWriterInventory` 要求 `handleChatAgentsCommand` 保持 6 条基线，不得为交互特性新增原始 writer |
| G9 | 历史子会话回放（messages 兜底） | `chat_agent_transcript.go`（`loadChatAgentTranscriptSession`、`chatAgentTranscriptMessageLines`、`buildChatAgentTranscriptView`；`source=` 标记） | Q2 口径：EventStore 事件窗口为空时回退 SessionStore 的 canonical messages（与 `/export <session-id>` 同源的 durable 会话），标记 `source=messages` + `ended=true`；事件流一旦有覆盖仍以事件流为准（`source=events`）。messages 回放不建立 live 订阅（follow 语义只在事件流上成立） |
| G9-test | 回放回归断言 | `chat_agent_transcript_test.go: TestAgentTranscriptView_ReplaysStoredMessagesWhenEventStreamIsEmpty` | messages 兜底渲染（多行正文、`tool_calls` 行）、父内容不泄漏、事件流恢复后回到 `source=events`；fixture 用真实会话上下文绑定（`agent_path`/parent/root，与 G7 materialize 的 sweep 口径一致），避免合成 SessionStore 被 sweep 成 stale |
| P2-export | 导出快捷提示 | `chat_agent_transcript.go: chatAgentTranscriptHeaderLines` | header 增 `[hint] export: /export <session-id>`，复用既有 `/export [current\|latest\|<session-id>]` 能力（不新增导出实现） |

### 8.2 验收对照（§5 P0）

| 验收项 | 结果 | 证据 |
|---|---|---|
| 父 timeline 不含子会话 tool/正文/思考行 | ✅ | G1 守卫 + `chat_docs_team_regression_test.go` 翻转断言（`go test -run DocsPromptRegression` 通过） |
| `/agents view <target>` 可见子会话 assistant/思考/tool 行 | ✅ | `chat_agent_transcript_test.go: TestAgentTranscriptView_SeparatesChildTimelineFromParent` |
| `/agent <target>` 与 `/agents view <target>` 等价 | ✅ | 同上：两个入口的渲染文本逐字相等 |
| 只读：不载入父 `Session.History`、父事件流零新增 | ✅ | 同上：`History` 为空，父事件流仅保留既有 1 条事件 |
| 未知 target 报错且不伪装 transcript；空子会话显式空态；`/agent close` 关闭视图 | ✅ | `TestAgentTranscriptView_EmptyUnknownAndClose` |

复现命令：`cd backend; go test ./cmd/aicli/commands -run 'AgentTranscriptView|DocsPromptRegression|TestChatInteractiveDirectWriterInventory|TestChatRuntimeEvents_SerializesConcurrentApprovalsAndQuestions' -count=1`

### 8.4 验收对照（§5 P1）

| 验收项 | 结果 | 证据 |
|---|---|---|
| 子会话运行中视图可实时更新 | ✅ | popup 订阅 + 增量合并：`TestAgentTranscriptFollow_MergesChildEventsAndStopsOnChildTerminal`（子事件入视图，渲染口径与快照一致） |
| 终态收口且不影响父视图 | ✅ | 同上：子 `session_end` 后 `FollowActive=false`、`ended=true`；父终态被身份过滤挡掉 |
| 上限对齐前端 400 行 | ✅ | `TestAgentTranscriptView_CapsRowsAtFrontendLimit`（`rows=400 truncated_rows=25`，标记不计入正文口径） |
| 审批/问题可在视图内闭环 | ✅ | `TestAgentTranscriptView_PendingActionHintsCloseLoop` + `/agents approve\|deny\|answer` 真实命令接入（审批链 `ResolveApproval`、问题 `AnswerQuestion`） |
| 视图不产生父状态行变更 | ✅ | P0 `TestAgentTranscriptView_SeparatesChildTimelineFromParent`（父事件流/History 零新增）+ P1 follow 测试 `History` 断言 |
| panel / view / send 映射一致 | ✅ | `TestAgentTranscriptView_TargetSessionMappingConsistent` |
| 输出边界围栏不新增原始 writer | ✅ | `TestChatInteractiveDirectWriterInventory`（`handleChatAgentsCommand` 保持 6 条基线） |

复现命令：`cd backend; go test ./cmd/aicli/commands -run 'AgentTranscript' -count=1` → `ok … 0.988s`；
P1 全量回归：`cd backend; go test ./cmd/aicli/commands -count=1` → `ok … 97.664s`（`go build ./...`、`go vet ./cmd/aicli/commands` 通过）。

### 8.5 验收对照（§5 P2，本轮落地部分）

| 验收项 | 结果 | 证据 |
|---|---|---|
| 事件流无覆盖的历史子会话可回放 | ✅ | `TestAgentTranscriptView_ReplaysStoredMessagesWhenEventStreamIsEmpty`：`source=messages` + `ended=true`，正文含 `[user]`/`[assistant]`（多行）与 `tool_call` 行 |
| 事件流优先，不被 messages 覆盖 | ✅ | 同上：为子会话补一条事件后视图回到 `source=events`，历史消息不再出现 |
| 回放只读且不泄漏父内容 | ✅ | 同上：父会话事件内容不出现在子视图 |
| 回放不建立 live 订阅 | ✅ | 代码口径：`buildChatAgentTranscriptView` 仅空事件窗口回退，follow 分支对 `source=messages` 直接 `stopChatAgentTranscriptFollow` |
| 导出提示可复制 | ✅ | 同上：header `[hint] export: /export replay-child-session`；命令目录既有 `/export [current\|latest\|<session-id>]` |
| `subagent.progress` 锚点 | ⏸ 归入 G8 批次 | 现状核查：CLI 父桥不渲染该镜像行（live-only；仅 ACP 桥合成 `tool_call_update`），无可挂锚点的行 |
| G8 前端第二入口 | ✅ | 见 §8.6（前端改动、验证命令与证据单列） |

复现命令：`cd backend; go test ./cmd/aicli/commands -run 'AgentTranscript' -count=1`。

### 8.6 验收对照（§5 P2-G8，前端第二入口）

| 验收项 | 结果 | 证据 |
|---|---|---|
| 面板行内出现只读 transcript 入口 | ✅ | `session-agents-panel.test.tsx`：`worker-1` 行渲染 `[data-testid="agent-transcript"]`（文案「会话记录」）并回传 target |
| 入口只认后端上报的会话键 | ✅ | `session-agents-panel-shared.test.ts: agentTranscriptTarget`（3 例）：`sessionId` 原样透传；无 `agent_path` 时标题回退 `agentId`；`sessionId` 缺失/空白 → `null` |
| 无会话键的行不给入口 | ✅ | `session-agents-panel.test.tsx`：`worker-2`（`sessionId=null`）行内无 `agent-transcript` 节点，不拿 `agentId` 冒充会话键 |
| 复用既有只读视图，不新造对话框 | ✅ | `workspace-shell/main-section.tsx` 挂载既有 `SubagentSessionDialog`（与轨迹页同源）；打开前先收起 agents 面板（避免两层 modal 的 Esc 竞态），面板数据有缓存、重开不重复拉取 |
| 展示状态沿用既有收敛口径 | ✅ | `agentTranscriptTarget` 的 `status` 取 `agentDisplayStatus`（`runtimeState=idle` → `ended`），与面板 Badge 同源 |
| i18n 双语文案齐备 | ✅ | `zh-CN/en-US/workspace/panels-agents.ts` 增 `transcript` / `transcriptLabel`；`node scripts/verify-frontend-i18n.ts` → `scanned=840, violations=0` |
| 行数预算不被破坏 | ✅ | 本轮 8 个改动文件非空行：`session-agents-panel-shared.ts` 440、`.test.ts` 397、`session-agents-tree.tsx` 321、`session-agents-panel.tsx` 279、`.test.tsx` 301、`main-section.tsx` 468（均 ≤ 500） |

改动文件：`frontend/src/components/workspace/session-agents-panel-shared.ts`（`agentTranscriptTarget`）、
`session-agents-tree.tsx`（行内入口）、`session-agents-panel.tsx`（prop 透传）、
`workspace-shell/main-section.tsx`（对话框接线 + 面板收起）、`i18n/resources/{zh-CN,en-US}/workspace/panels-agents.ts`。

复现命令：`cd frontend; npx vitest run src/components/workspace/session-agents-panel.test.tsx
src/components/workspace/session-agents-panel-shared.test.ts src/components/workspace/session-agents-tree.test.tsx`
→ `Test Files 3 passed / Tests 47 passed`；`npx eslint <8 个改动文件>` → exit 0。

### 8.3 未落地（按 §5 排期）

- P1 残留（非阻塞）：§4.2 的 `f` 键在 popup 内切换 follow 尚未接线——REPL 输入是整行读取
  （`bufio.Reader.ReadString('\n')`）而非 raw-key，单键无输入通道；等价操作是重发
  `/agent <target> follow`（开启）/ `/agent <target>`（停止）。若后续要真正的单键切换，
  需要 raw-mode 输入层，不在本视图范围内；
- unified 一次性单元格路径的 follow 只给出 `follow=unavailable` 说明（设计口径，见
  `executeStructuredAgentTranscriptCommand`）；"picker 内 Enter 直接下钻 transcript"的交互细化；
- P2 剩余：`subagent.progress` 镜像行锚点（CLI 侧无可挂锚点的行；前端/ACP 面需先有该 live
  镜像行的可渲染行，再谈跳转）；
- 已知边界（P0 明确不承诺，P1 已收口 live 部分）：`/agent` 无 target 时复用现有 picker 选中态
  （`chatSessionSelectedAgentTarget`）。
- 既有红项（非本轮 G8 引入，供后续排期）：`npm run verify:lines` 仍有 6 个既有超限文件
  （`lib/trajectory/recovery.ts` 571、`lib/thread-state/chat-sse-bridge.test.ts` 530、
  `api/runtime/skills.ts` 521、`lib/trajectory/recovery.test.ts` 520、
  `components/workspace/message-markdown.test.tsx` 515、
  `components/workspace/workspace-sidebar/directories-section.tsx` 509）；
  `npx tsc -b` 有 6 个既有类型错误（`api/runtime/analytics.ts`、`lib/trajectory/recovery.test.ts`、
  `pages/usage-analytics/artifact-flow-panel.tsx`）；前端全量 vitest 3 例失败，其中
  `use-workspace-live.test.tsx` 在纯 HEAD（本轮改动全部 stash 后）同样失败，
  `event-contract.test.ts` 2 例由工作区未提交的 `event-contract.ts` 编辑（新增
  `subagent.completed` 落盘声明）引起，均不在 G8 改动面内。

---

## 附录 A：关键文件索引

| 主题 | 路径 |
|---|---|
| 事件桥与隔离守卫 | `backend/cmd/aicli/commands/chat_runtime_events.go`（`:2680` scene 守卫、`:3924` exec、`:4623` ACP、`:4691` retry、`:5675` 终态、`:7139` 内容判定） |
| 隔离回归测试 | `backend/cmd/aicli/commands/chat_runtime_events_foreign_session_test.go` |
| 冲突断言 | `backend/cmd/aicli/commands/chat_docs_team_regression_test.go:202` |
| Agent 面板（协作概览） | `backend/cmd/aicli/commands/chat_debug.go:1603-1610`（三栏）、`:1808+`（modal 渲染）、`:1562`（target 切换） |
| 命令目录 | `backend/cmd/aicli/commands/chat_slash_command_catalog.go:121-158`（`/agents`） |
| 只读 transcript pager | `backend/cmd/aicli/ui/transcript_pager.go`；`chat_command_result.go:120-125`；`chat.go:1501` |
| 子会话工厂/共享 EventBus | `backend/internal/agent/child_factory.go` |
| 子会话注册/回滚 | `backend/cmd/aicli/commands/chat_actor_registry.go` |
| 进度镜像设计注释 | `backend/internal/supervision/subagent_progress.go` |
| 前端下钻弹窗 | `frontend/src/components/workspace/trajectory/subagent-session-dialog.tsx`（挂载：`trajectory-view.tsx:512-515`） |
| 前端下钻 hook | `frontend/src/hooks/workspace/use-subagent-session.ts` |
| 轨迹事件落库 | `backend/internal/api/skills/trajectory_events.go` |
| 会话事件存储 | `backend/internal/chat/session_runtime_store.go`（`AppendEvent` / `ListEvents`） |

## 附录 B：TUI 与前端能力对照

| 能力 | frontend | aicli TUI/console | 差距动作 |
|---|---|---|---|
| 子会话列表/选择 | session-agents-panel、轨迹页 | `/agents panel`、`/agents pick` | 复用 |
| 子会话 transcript 视图 | `SubagentSessionDialog`（独立 store + 实时 + 审批） | **无** | **新建（G2）** |
| 子会话入口 | 父轨迹 `subagent` item | **无** | **新建（G3）** |
| 主视图隔离 | 父流与子 store 分离（已实现） | 内容面已隔离；tool 行泄漏待收口 | **G1** |
| 审批处理 | 弹窗内可审批 | 仅 `/agents` 面板/digest 提示 | P1（G5） |
