# Workspace Chat 实时输出（SSE 流式渲染）缺口分析与设计方案

- 状态：Draft（待评审）
- 日期：2026-08-14
- 范围：`frontend/` Workspace Chat（`/workspace/chats/*`）在 LLM SSE 响应期间的实时渲染能力
- 参考实现：`E:\projects\ai\deepseek-harness`（`packages/client/ui-conversation`、`packages/client/ui-primitives/src/markdown/incremental.ts`）

---

## 1. 背景与目标

### 1.1 背景

Workspace Chat 已具备一条**完整但体验不完整**的 SSE 链路：

- 后端 `/api/agent/chat` 已逐 chunk `Flush()` 推送 `meta / chunk / reasoning / tool_start / tool_call / tool_end / planning / orchestration / route / observation / subagent / result / done / error` 事件；
- 前端 `api/runtime/sse.ts` 已实现标准 SSE 解析（含 `event:` 行）；
- `hooks/workspace/use-workspace-agent-chat-turn.ts` 已把 `chunk` 文本增量合入 `streamedText` 并以 `requestAnimationFrame` 批量刷新；
- `components/workspace/message-markdown.tsx` 已实现流式 Markdown（stable/tail 拆分、代码围栏兜底、表格/列表/引用增量渲染）。

即"后端 SSE 响应 → 前端逐块渲染"的主干已存在，但存在多处**实时性/可视性缺口**，导致实际体验接近"等全部输出完成后一次性渲染"（尤其是带 reasoning 的模型，如 DeepSeek R1 系）。

### 1.2 目标

1. 明确现有链路的缺口清单与根因（第 3 节）；
2. 以 deepseek-harness 前端为参照，给出补齐方案（第 5 节）；
3. 方案按阶段落地，每阶段可独立验收、可回滚。

### 1.3 非目标

- 不改动后端 SSE 事件协议（`/api/agent/chat` 事件名与 payload 视为稳定契约）；
- 不重写现有 SSE 客户端解析器（`consumeSseResponse` 已通过 `sse.test.ts` 验证）；
- 不引入新的流式传输通道（WebSocket 等）——SSE 已够用，缺口在渲染侧。

---

## 2. 现状链路（证据）

### 2.1 后端：已实时 flush

`backend/internal/api/skills/handler.go`：

- `streamLLMChat`（:6889）调用 `h.llmRuntime.Stream(ctx, {Stream: true})`（:6951）；
- 事件循环（:7008-7028）逐 chunk 分发：
  - `EventTypeText` → `emitter.Emit("chunk", buildStreamChunkPayload(...))`；
  - reasoning / tool_call / tool_start / tool_end → `emitter.Emit(streamEventName(type), payload)` 并同时 `Emit("chunk", payload)`；
  - `EventTypeImage` → `Emit("chunk", ...)`；`EventTypeError` → `Emit("error", ...)`；
- `writeSSEEventWithEnvelope`（:7670 附近）写出 `data: ...` 后调用 `http.Flusher.Flush()`（:7693-7695）。

结论：**后端无缺口**，LLM 增量到达即 flush。

### 2.2 前端：SSE 客户端已完备

`frontend/src/api/runtime/sse.ts`：

- `consumeSseResponse` 处理 `event:` / `data:` / 注释行 / 粘包与拆包（buffer 按行切分）；
- `streamAgentChat`（:192）把 `meta / chunk / reasoning / tool_start / tool_call / tool_end / planning / orchestration / route / observation / subagent / result / done / error` 全部分发到 handlers。

结论：**传输层无缺口**。

### 2.3 前端：turn 状态机（缺口集中地）

`frontend/src/hooks/workspace/use-workspace-agent-chat-turn.ts`：

- `onChunk`（:475）：`streamedText += delta; scheduleStreamingMessage();` → rAF 合并刷新，**正文实时渲染已通**；
- `onReasoning`（:505）：`reasoningText += delta; if (streamedText) scheduleStreamingMessage();` → **reasoning 先行时不触发任何渲染**（缺口 G1）；
- `onToolStart/Call/End`（:520/:536/:552）：仅 `toolPayloads.push` + 生成 JSON artifact + 写 `lastRuntimeEventType`，**聊天区无任何可视卡片**（缺口 G2）；
- `onPlanning/onOrchestration/onRoute/onObservation/onSubagent`：仅收集进 final artifact，**无实时 UI**（缺口 G7）；
- `finalizeTurn`（:253）：结束时一次性 `buildAssistantMessageSegments(finalText, source, reasoningText)` 全量落盘。

### 2.4 前端：渲染组件

- `components/workspace/message-markdown.tsx`：`MessageMarkdown`（:449 起）在 `streaming` 时用 `useDeferredValue` + `splitStreamingMarkdown` 拆分 stable/tail，tail 按 code fence / 结构化块 / 纯文本三种模式增量渲染。**能力已有，但 stable 部分每帧全量重新 `ReactMarkdown` 解析**（缺口 G4）；
- `components/workspace/message-list.tsx`：通过 `streamingMessageId` 给最后一条 assistant 消息标记 `aria-busy`，但**没有流式自动滚动跟随**（仅 backtrack 场景有 `scrollIntoView`，:121）（缺口 G5）；
- `components/workspace/message-rich-content.tsx`：reasoning/tool 等非 text segment 的渲染入口（需扩展）。

### 2.5 前端：历史同步与运行时事件流

- `use-session-runtime-stream.ts`：轮询/长连 `sessions/:id/runtime/stream` 的 `runtime_event`，用于把后端会话事件合入 thread（`applyRuntimeEventToThread`）；
- `use-session-history-sync.ts`：非响应期把历史同步回 thread；
- 两路与 agent-chat SSE 并存，需注意**流式期间避免被历史同步覆盖**（风险 R3）。

---

## 3. 缺口分析（Gap Analysis）

| # | 缺口 | 根因 | 影响 | 严重度 |
|---|------|------|------|--------|
| G1 | **reasoning（思维链）不实时显示** | `onReasoning` 只在 `streamedText` 非空时才 `scheduleStreamingMessage()`；reasoning 阶段 UI 只有 "Runtime stream active" 转圈 | 推理模型（R1 系）首段输出长时间无任何文字，体验≈"卡死/未实时" | **高** |
| G2 | **工具调用无实时可视反馈** | `tool_*` 事件只落 JSON artifact，聊天区无 tool 卡片/行 | 用户看不到"正在调用工具/读取文件"等进度 | **高** |
| G3 | **后台标签页 rAF 节流导致流式中断** | `scheduleStreamingMessage` 依赖 `requestAnimationFrame`；后台标签 rAF 不触发 | 切走再切回时内容"跳变"，实时性失效 | 中 |
| G4 | **流式渲染 O(n²)** | 每帧对全量 stable 文本重新 `ReactMarkdown` 解析；消息列表每次 `setThreads` 全量重建 | 长回复（>4k tokens）帧率下降、输入卡顿 | 中 |
| G5 | **无自动滚动跟随** | `message-list.tsx` 无 scroll 逻辑 | 输出长文本时用户停在顶部看不到新内容 | 中 |
| G6 | **首 token 延迟无阶段感知** | 提交后到首个 chunk 之间只有固定转圈，无"已连接/等待首 token"状态 | 弱网/慢模型时无法区分"未开始"与"已排队" | 低 |
| G7 | **部分事件无 UI** | planning/orchestration/route/observation/subagent 仅进 final artifact | 多 agent/编排场景实时信息缺失 | 中 |
| G8 | **中断/错误的流式呈现不完整** | `stopResponding` 只走 `finalizeTurn({stopped})`，流式尾巴无视觉标记；`onErrorEvent` 路径需确认是否保留已流式文本 | 用户不清楚"已停止 vs 失败" | 低-中 |

> 补充说明（现状已具备，不应重复建设）：正文 `chunk` 的逐块渲染、流式 Markdown 的 stable/tail 拆分、SSE 解析、AbortController 停止、finalize 时 artifact 落盘——这些都已实现，方案只做增强。

---

## 4. deepseek-harness 参照点（证据）

| 参照 | 位置 | 做法 | 对应我们的缺口 |
|------|------|------|----------------|
| 增量 Markdown 解析 | `packages/client/ui-primitives/src/markdown/incremental.ts` | `IncrementalMarkdownParser`：append-only 流只冻结前 `UNSTABLE_TAIL_BLOCKS` 之前的 block（frozen），仅重解析尾部；render key 用 block 的**源 offset**（`blockKey`），前缀不变时 key 稳定，不重挂载；非 append 输入（回退/改写）`generation++` 使缓存失效 | G4 |
| reasoning 实时行 | `packages/client/ui-conversation/src/client/chat/ReasoningRow.tsx` + `AssistantMarkdown.tsx`（:69） | reasoning 作为 Think 摘要行渲染，`running={streaming && i === last}` 时持续更新，可展开看全文 | G1 |
| 分块有序渲染 | `AssistantMarkdown.tsx` | assistant 内容为 `AssistantBlock[]`（text/reasoning/image/tool-call），流式追加 block，key 稳定；tool-call 头不在此渲染，由 ChatView 分组为 tool rows | G2 |
| 节点状态建模 | `AssistantNodeView.tsx` | `streaming = data.status === 'running'`；`interrupted` 单独状态渲染 stopped 标记 | G8 |
| turn 级状态条 | `ChatView.tsx` `TurnStatus`（:106） | 覆盖首 token 等待 → 工具执行 → 流式阶段，整轮不闪烁 | G6 |
| 自动滚动契约 | `ChatView.tsx`（:408 `atBottom`）+ `tests/chat-scroll-contract.e2e.ts` | 用户位于底部时跟随最新输出，用户上翻时暂停跟随 | G5 |

---

## 5. 轨迹（Trajectory）视图实现思路深度分析

> 本章深入分析 deepseek-harness 的"轨迹"视图（`packages/client/ui-trajectory`）如何实现流式数据下的实时、可导航、可搜索的回放视图，并对照 workspace chat 的现状给出可移植机制清单。核心结论：**轨迹不是独立数据源，而是同一会话事件流上的第二种投影**——实时渲染的关键不在"推流"，而在"把事件流投影成可增量更新的视图状态"。

### 5.1 一句话总结

"轨迹"与 chat 视图共享**同一运行时事件流**（dsh-client-runtime），但各自注册独立的**节点定义（纯函数状态机）**、各自构建**不可变快照**、再投影为各自的 UI。它证明：实时渲染能力是**视图层的投影问题**，不是数据/传输问题——这与我们在第 2 章的结论（后端 SSE 与前端传输层已就绪）完全一致。

### 5.2 分层架构（事件到像素的 6 层）

| 层 | 位置（ui-trajectory 包） | 职责 |
|----|--------------------------|------|
| 事件源 | dsh-client-runtime 会话事件流 | 统一的 chunk / tool / usage / finish / 会话事件 |
| 节点定义 | `src/client/trajectory-assistant-definition.ts`、`trajectory-tool-definition.ts`、`trajectory-request-header-definition.ts`、`trajectory-compaction-definition.ts`、`trajectory-message-definitions.ts` | 纯函数状态机：`match(event)` → `start/update`（累积状态）→ `buildViewNode(state)` 产出不可变视图节点 |
| 快照构建 | `src/client/trajectory-snapshot-builder.ts` | 按 anchorSeq（事件序号）合并各节点贡献 → 单一不可变快照 |
| 布局投影 | `src/client/layout.ts` | 快照 → turn → group → cells 分层模型；runningCalls/partial 增量合并 |
| 记录结构 | `src/client/trajectory-record.ts` | 扁平可序列化 record，单行摘要与详情面板共用 |
| 视图组织 | `TrajectoryView` / `TrajectoryTimeline` / `TrajectoryToolbar` / `TrajectoryTable` | Toolbar + 时间线概览 + 明细表格 + 虚拟滚动 |

### 5.3 事件状态机与发布节流（核心机制）

- 每个节点定义是**纯函数状态机**：`match`（该事件是否属于我）→ `start/update`（累积增量状态）→ `buildViewNode`（产出不可变视图节点，供快照收集）；
- 事件发布带 `publication` 策略：流式事件按 **animation-frame 节流发布**（每帧最多合并发布一次）；`usage`/`finish` 等**不影响显示**的事件发布为 `none`，不触发渲染；
- 前端另有 `ui-conversation/src/client/chat/use-throttled-visual-update.ts`：**3 帧 rAF 合并**的稳定调度器（期间多次调用只保留最新一次，intervalFrames 帧后执行），用于非关键视觉对齐（如滚动位置校准）。
- **关键洞察：节流在发布层做掉，渲染层每帧拿到的就是"最多一次更新"的快照**，天然避免逐 chunk 重渲染、也避免高频事件打爆 React 调度。

### 5.4 快照构建器与工具调用树

- 快照构建器按 **anchorSeq** 排序合并各节点贡献，产出 `{ eventNodes, eventLocations, requests, callSchemas, partial, runningCalls }` 单一不可变对象；
- **增量更新**：upsert 只替换对应位置，仅 structural 变化（插入/删除节点）才重排，不做全量重建；
- **工具调用树**：call → result → subCalls 嵌套，缩进深度表达层级；流式过程中 `runningCalls`/`partial` 与最终节点**合并到同一 cell**（同一 step 持续更新，不产生重复行）；
- **派生指标**：TTFT、解码时长等从 `startedAt`/`timeSeconds` 事件时间戳计算，而非另起计时器；`duration-store.ts` 提供"实际耗时 vs 记录耗时"的持久化偏好（浏览器级共享）。

---

### 5.5 记录结构与虚拟滚动

- `TrajectoryCellKind` 为**闭合枚举**（system / user / context / compacted / assistant / tool / request-header / …），每个 record 携带全部详情（inputDetail / outputDetail / thinkingDetail / schemaDetail / result / sourceBlocks），**单行摘要与详情面板共用同一对象**，避免两套数据；
- `trajectoryRecordId`：callId / sourceSeq / index **三级回退**，保证跨事件稳定身份（流式更新时行不闪动）；
- 虚拟行（`trajectory-virtual-rows.ts`）：把 record 分组为**可测量行**（内容行 30px / 折叠摘要行 20px / 终末分隔 9px），request-only 零高度行合并到下一内容行，供虚拟滚动精确测量；
- 搜索索引（`trajectory-search-index.ts`）：**增量全文索引**，sources 签名比较避免重复 markdown 解析，3s 节流重建，terms AND 匹配——长会话下搜索不卡。

### 5.6 时间线（timeline.ts）

- 四种投影模式：**sequence**（事件顺序）/ **duration**（时长轴，可压缩 idle）/ **time**（时钟）/ **actual**（实际耗时）；
- 三车道布局：tool=2 / message=1 / 其他=0，一眼区分工具密集段与纯对话段；
- focus 区间可反查记录索引，**时间线选区 ↔ 表格高亮双向联动**（概览导航）。

### 5.7 与 chat 视图的关系

- 同一事件流、同一运行时，不同 view target（`'chat'` vs `'trajectory'`），各自注册节点定义、各自构建快照、互不干扰；
- 轨迹的 assistant 节点与 chat 的 assistant-step 定义**共享同一套 chunk 累积逻辑**（updateChunk、firstVisibleSeq/firstTokenTime、resetForRetry、interrupted 合成节点）——说明"节点状态机"是**可复用单元**；
- 对 workspace chat 的直接启示：我们不需要复制整套架构，只需把 hook 里的事件处理提炼成**纯函数累积器 + 增量快照**（见 5.8 对照），即可获得同样的实时性保证。

### 5.8 对 workspace chat 的启示与差距对照

| 机制 | 轨迹做法 | workspace chat 现状 | 差距 / 启示 |
|------|---------|--------------------|-------------|
| 节流位置 | 发布层 animation-frame 节流 | 每 chunk 直接 `scheduleStreamingMessage`（无节流） | 对齐 G3：rAF + setTimeout 兜底 |
| 快照 | 单一不可变快照 + upsert | segments 数组全量重建 | G4：增量更新 |
| 状态建模 | 纯函数节点定义（match/buildViewNode） | hook 内 if/else 分支 | 可维护性（后续迭代） |
| 身份稳定性 | trajectoryRecordId 三级回退 | messageId / tool_call.id（部分具备） | Phase 1 G2：用 tool_call.id 做 tool segment 的 upsert key |
| 工具树 | call → result → subCalls 嵌套 | 无 | Phase 1 G2：工具卡片流 |
| 增量 Markdown | frozen/tail + 源 offset 稳定 key | stable/tail 已有（未缓存解析） | Phase 3 G4 对齐 |
| 派生指标 | TTFT / 时长从事件时间戳 | 无 | G6 阶段感知可顺带显示首 token 耗时 |
| 长会话 | 虚拟滚动 + 增量搜索索引 | 无 | 后续迭代（不在本期） |

### 5.9 可直接移植清单（按 Phase 对齐）

1. **发布层节流语义** → `scheduleStreamingMessage` 的 rAF + `setTimeout` 兜底（Phase 2 / G3）；
2. **工具调用折叠/展开 + 稳定 key** → `message-tool-row.tsx`（Phase 1 / G2）；
3. **frozen/tail 增量解析 + `generation++` 失效** → Phase 3 / G4；
4. **事件时间戳派生 TTFT** → G6 状态条显示"首 token 耗时"；
5. **虚拟行 / 增量搜索索引** → 长会话优化，明确不在本期范围。

### 5.10 与第 6 章设计方案的关系

本章只补充参照证据与可移植机制，**不改变第 6 章（设计方案）的任何结论**；Phase 1/2/3 保持不变。"轨迹"的"发布层节流 + 增量快照"思想已体现在第 6 章 6.2 目标数据流（scheduleFlush）与 6.3 分阶段实施（G3/G4）中，两者是同一范式在不同规模下的落地。

---

## 6. 设计方案

### 6.1 总体思路

**不重开链路，做三层增强**：

1. **状态层（hook）**：把"文本 + reasoning + 工具事件 + 阶段"统一提升为**可增量订阅的流式状态**，每次 SSE 事件 → 状态更新 → 渲染，并修复 G1/G3；
2. **渲染层（组件）**：新增推理行、工具卡片、滚动跟随；引入增量解析缓存降低 G4 成本；
3. **体验层**：首 token 阶段状态、中断/错误尾巴标记（G6/G8）。

### 6.2 目标数据流

```
后端 /api/agent/chat SSE（已实时 flush，不变）
   │  chunk / reasoning / tool_* / meta / result / done / error
   ▼
sse.ts streamAgentChat（不变）
   ▼
useWorkspaceAgentChatTurn（改造）
   ├─ streamedText  += delta            → scheduleFlush()（rAF + setTimeout 兜底）
   ├─ reasoningText += delta            → scheduleFlush()（去掉 G1 的 if 条件）
   ├─ toolEvents.push(...)              → 更新 tool 活动行（started/running/finished）
   ├─ phase: connecting|first-token|streaming|tool|finalizing
   └─ done/error/stopped                → finalize（含 interrupted 标记）
   ▼
Thread 状态（segments 扩展：reasoning 段 + tool 段 + text 段）
   ▼
message-list / message-markdown（改造）
   ├─ ReasoningRow（可折叠，running 实时更新）
   ├─ ToolRowGroup（工具调用卡片流）
   ├─ 流式自动滚动（atBottom 契约）
   └─ 增量 Markdown 解析缓存（frozen/tail）
```

### 6.3 分阶段实施

#### Phase 1：让"过程"可见（G1、G2、G8，价值最高）

1. **G1 – reasoning 实时渲染**
   - `use-workspace-agent-chat-turn.ts` `onReasoning`：去掉 `if (streamedText)` 条件，reasoning delta 到达即 `scheduleStreamingMessage()`；
   - `buildAssistantMessageSegments`（`lib/workspace-thread-state.ts` :117）：reasoning 由"文本前缀"改为独立 `reasoning` segment（`type: "reasoning"`，`content` 为累计全文，`running: true`）；
   - 新增 `components/workspace/message-reasoning-row.tsx`（参照 ReasoningRow）：折叠态显示摘要/标题行，展开显示全文，`running` 时显示光标/脉冲；
   - `message-rich-content.tsx` / `message-list.tsx` 的 `renderMessageSegment` 增加 `reasoning` 分支；key 用 messageId（不随内容变化，避免重挂载丢折叠状态）。
   - 测试：`message-markdown-streaming.test.ts` 同目录新增 `message-reasoning-row.test.tsx`；hook 层新增 reasoning-only 回合（无正文 chunk）的用例，断言 UI 出现推理文本。

2. **G2 – 工具调用实时卡片**
   - 新增 `components/workspace/message-tool-row.tsx`：tool 行含名称、状态徽标（started/running/finished/error）、参数摘要（折叠）、结果摘要；
   - `use-workspace-agent-chat-turn.ts` `onToolStart/Call/End`：不再只写 JSON artifact，同时更新 assistant 消息的 `tool` segment 列表（以 `tool_call.id` 或 `tool.name` 为 key 的 Map，`upsertToolSegment`）；
   - 工具结束后保留最终态（finished/error），并在 `finalizeTurn` 时把 `toolPayloads` 落为 artifact（现有逻辑保留）。
   - 测试：模拟 tool_start→tool_call→tool_end 序列，断言三态渲染与最终 artifact 落盘。

3. **G8 – 中断/错误尾巴**
   - `finalizeTurn({stopped})`：给 assistant 消息附加 `interrupted: true` 标记；`MessageMarkdown` 在 `streaming=false && interrupted` 时渲染 "已停止" 尾巴（参照 deepseek-harness `AssistantMarkdown` 的 `interrupted` 分支）；
   - `onErrorEvent`：若已有流式文本，保留文本 + 追加错误横幅，而不是整条替换。

#### Phase 2：性能与体验（G3、G5、G6）

4. **G3 – rAF 兜底**：`scheduleStreamingMessage` 增加 `setTimeout(…, 100)` 兜底（rAF 不触发时）；后台标签页恢复后立即 flush 一次。
5. **G5 – 自动滚动跟随**：`message-list.tsx` 增加容器 ref + `atBottom` 判定（滚动位置距底部 < 阈值），`isResponding && atBottom` 时跟随最新内容；用户上翻即暂停（参照 deepseek-harness `chat-scroll-contract` 语义）。注意与现有 backtrack `scrollIntoView` 共存。
6. **G6 – 首 token 阶段感知**：hook 暴露 `phase`（`connecting → first-token → streaming/tool → finalizing`）；`message-list.tsx` 底部状态条按 phase 显示"连接中 / 等待首个输出 / 流式输出中 / 调用工具中"，替代固定 "Runtime stream active"。

#### Phase 3：渲染性能（G4，可选但推荐）

7. **增量 Markdown 解析缓存**：在 `MessageMarkdown` 内引入轻量 `IncrementalMarkdownParser`（移植/仿照 deepseek-harness 思路：frozen blocks 缓存 + 尾部重解析 + 源 offset 稳定 key）：
   - 限定范围：只缓存 `stableContent` 的解析结果（ReactMarkdown 的 AST → 渲染），tail 每帧重解析（现状）；
   - 退化路径：内容非 append（回溯改写/历史同步覆盖）时 `generation++` 全量重建，与 `useDeferredValue` 叠加使用；
   - 若移植成本高，可先做**降级优化**：`stableContent` 用 `memo` + 字符串前缀比较（`content.startsWith(prev)` 时复用），收益接近但实现简单。
   - 基准：`docs/` 下补充渲染基准说明（可选），验收以"4k token 回复下无肉眼卡顿"为准。

### 6.4 涉及文件清单（预估）

| 文件 | 变更 |
|------|------|
| `frontend/src/hooks/workspace/use-workspace-agent-chat-turn.ts` | onReasoning 条件移除；tool segment 状态；phase 状态；interrupted 标记 |
| `frontend/src/lib/workspace-thread-state.ts` | segments 增加 reasoning/tool 类型；`upsertToolSegment`；`buildAssistantMessageSegments` 拆分 reasoning |
| `frontend/src/types/runtime.ts` | （如需要）补充 segment 联合类型 |
| `frontend/src/components/workspace/message-reasoning-row.tsx` | **新增**：推理行（折叠 + running） |
| `frontend/src/components/workspace/message-tool-row.tsx` | **新增**：工具调用卡片 |
| `frontend/src/components/workspace/message-rich-content.tsx` | reasoning/tool segment 渲染分支 |
| `frontend/src/components/workspace/message-list.tsx` | 自动滚动跟随；phase 状态条 |
| `frontend/src/components/workspace/message-markdown.tsx` | interrupted 尾巴；Phase 3 增量解析缓存 |
| `frontend/src/components/workspace/message-markdown-streaming.ts` | （如 Phase 3）增量解析辅助 |
| `frontend/src/i18n/resources/en-US.ts` / `zh-CN.ts` | 新文案（推理中/工具调用中/已停止/连接中） |
| 测试文件 | `message-reasoning-row.test.tsx`、`message-tool-row.test.tsx`、hook 回合用例、滚动跟随用例 |

### 6.5 测试与验收标准

**单测**（vitest，沿用现有测试风格）：
1. reasoning-only 回合：无正文 chunk 时推理文本实时出现（G1）；
2. tool_start→call→end 三态渲染 + final artifact（G2）；
3. 停止后保留已流式文本 + stopped 标记（G8）；
4. 滚动：底部跟随 / 上翻暂停 / 恢复跟随（G5）。

**验收（手工 + 可选 e2e）**：
1. 选择 DeepSeek R1 系模型发起长任务：**首段推理文字应在首个 chunk 到达后 1 帧内出现**，正文逐块增长；
2. 任务中调用工具（如 shell/read）：聊天区实时出现工具卡片及状态变化；
3. 输出中切走标签页再切回：内容不丢、继续流动（G3）；
4. 长回复（≥2k tokens）期间输入框与滚动无明显卡顿（G4）；
5. 停止按钮：立即停止并显示已停止标记。

### 6.6 风险与注意事项

- **R1 历史同步覆盖**：`useSessionHistorySync` 在 `isResponding` 时跳过（已实现），但流式结束与历史同步的时序仍需在 Phase 1 回归确认，防止 reasoning/tool segment 被历史快照覆盖为纯文本；
- **R2 segment 兼容**：新增 segment 类型会影响 `message-rich-content` 的懒加载分支与测试快照，需同步更新既有测试；
- **R3 性能回退**：Phase 3 若引入解析缓存，必须保证回溯改写（backtrack）与历史同步路径走 `generation++` 全量重建，否则出现"显示旧内容"；
- **R4 协议假设**：方案假设 `tool_start/tool_call/tool_end` payload 含稳定的 `tool_call.id`/`name`（`buildToolEventPayload` 已提供），若实际缺 id 需退化为按 name+index 组合 key；
- **R5 范围控制**：G7（planning/orchestration/observation/subagent 实时 UI）价值取决于产品形态，建议 Phase 1 只做"事件出现时在状态条展示计数/名称"，完整面板放后续迭代。

---

## 7. 结论

后端 SSE 与前端传输/解析链路已经就绪，缺口集中在 **hook 状态机对 reasoning/工具事件的实时暴露**、**渲染层的工具/推理可视化**、**滚动跟随与后台节流** 三处。按 Phase 1 → 2 → 3 实施，可在一个迭代内把"等全部输出"升级为"边出边渲染"的实时体验；Phase 1 即交付主要价值。
