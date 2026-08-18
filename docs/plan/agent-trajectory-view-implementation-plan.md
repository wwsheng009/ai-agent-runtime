# Agent 运行轨迹（Trajectory）视图与事件日志：实现方案

> 状态：Draft（待评审）
> 日期：2026-08-16
> 范围：`backend/`（Go）+ `frontend/`（React Workspace Chat）
> 参照实现：
> - DeepSeek-Reasonix：`internal/trajectory`（JSONL 事件日志）+ 前端 Transcript / 事件流渲染（`agent:event` 单通道 + 纯函数 reducer + rAF 合并批处理）
> - deepseek-harness：`packages/client/ui-trajectory`（同一事件流的第二种投影：时间线 + 明细表 + 虚拟滚动 + 增量搜索）
>
> 关联文档（docs/plan 已查）：
> - [workspace-chat-streaming-realtime-rendering-plan.md](./workspace-chat-streaming-realtime-rendering-plan.md)：Workspace Chat SSE 实时渲染缺口分析（G1–G8），§5 已深度分析 deepseek-harness 轨迹视图
> - [aicli-event-stream-rendering-order-render-model-spec.md](./aicli-event-stream-rendering-order-render-model-spec.md)：RenderModel / Item 数据结构规格（TUI 侧统一编码器输出模型）
> - [aicli-event-stream-rendering-order-todo.md](./aicli-event-stream-rendering-order-todo.md)：统一编码器实施状态（P1/P2 落地、P3 未完成）
> - [aicli-event-stream-rendering-order-event-encoder-api-design.md](./aicli-event-stream-rendering-order-event-encoder-api-design.md) / [unified-encoder-plan.md](./aicli-event-stream-rendering-order-unified-encoder-plan.md) / [migration-roadmap.md](./aicli-event-stream-rendering-order-migration-roadmap.md)
> - [docs/roadmap.md](../roadmap.md)：「提升消息区的 workflow 表达，补 inline tool steps、消息分组和更明确的运行轨迹展示」

---

## 1. 背景与目标

### 1.1 背景

Workspace Chat 已具备完整 SSE 链路（后端逐 chunk flush、前端逐块渲染），且 2026-08-16 的 `ac5e1f8` 已落地 reasoning 实时显示（G1）与工具行/推理块组件（G2）。但「轨迹」能力（DeepSeek-Reasonix 前端 Transcript 所体现的：**类型化事件流 → 统一渲染模型 → 可回放、可导航、可搜索的视图**）在本项目仍缺三块地基：

1. **chat SSE 轨迹事件未持久化**：后端已有会话事件存储（`chat.EventStore` + `runtime/events` 增量端点，见 §3.1），但 `/api/agent/chat` 的 LLM 轨迹事件（chunk/reasoning/工具参数与输出）未纳入——断线无法按事件序续传、无法离线回放与分析；
2. **前端无统一渲染模型**：turn hook 内 `streamedText / reasoningText / toolPayloads` 三块分离状态，无 ID / Seq / CauseID / 状态机 → 事件身份不稳定、无法重放、流式期间可能被历史同步覆盖；
3. **无轨迹视图**：只有 chat 一种投影，无时间线/明细/搜索/虚拟滚动（roadmap 的「运行轨迹展示」未落地，G7 的 planning/orchestration/subagent 事件仍无实时 UI）。

### 1.2 目标

1. **后端**：事件日志持久化（JSONL，按会话分文件）+ 全局 `seq` 契约 → 断线续传、离线回放、诊断分析；
2. **前端**：统一渲染模型（web 版 Item / ChangeSet 纯函数 reducer）→ 实时流与历史回放走同一管线，事件身份稳定；
3. **前端**：轨迹视图（时间线 + 明细列表 + 虚拟滚动 + 搜索 + 导出）→ 同一事件流的**第二种投影**，与 chat 视图互不干扰。

### 1.3 非目标

- 不改 SSE 事件名与 payload 语义（`/api/agent/chat` 视为稳定契约；只允许**新增** `seq` 字段）；
- 不引入 WebSocket 等新传输通道；
- 不重写 `message-list.tsx` / `message-markdown.tsx`（chat 投影继续消费现有 `MessageSegment` 模型）；
- 不做 TUI 侧统一编码器 P3（presenter 切换是 TUI 渲染面的事，本文只管 web 数据面）；
- 不做跨会话轨迹聚合 / 多 team 全局轨迹视图（roadmap 后续项）。

---

## 2. docs/plan 相关文档盘点（已查）

| 文档 | 状态 | 与本方案的关系 |
|---|---|---|
| `workspace-chat-streaming-realtime-rendering-plan.md` | Draft（2026-08-14；G1/G2/G5 已随 ac5e1f8 落地） | §5 是轨迹视图的参照分析（deepseek-harness ui-trajectory）；G4/G6/G7/G8 仍开放，本方案 Phase 1/2 一并承接 |
| `aicli-event-stream-rendering-order-render-model-spec.md` | 实施中（P1/P2 落地） | **本方案前端 reducer 的规格蓝本**（Item：ID/Seq/Kind/CauseID/Status/Head；append/upsert/remove；幂等与终态保护） |
| `aicli-event-stream-rendering-order-todo.md` | partial（P3 未完成） | 已固化的语义清单（乱序缓冲拼接、reasoning 独立索引、Tail 锚定）直接作为 web 版测试用例来源 |
| `aicli-event-stream-rendering-order-unified-encoder-plan.md` | 方案 | 编码器 → ChangeSet → Scene 的分层思想，web 侧简化为 reducer → 快照 → 双投影 |
| `aicli-event-stream-rendering-order-event-encoder-api-design.md` | 方案（§4.2 乱序语义文档待更新） | 接口设计参照 |
| `docs/roadmap.md` | — | :351 明确「更明确的运行轨迹展示」为待办 |

结论：**方案地基已存在**（TUI 规格 + chat 缺口分析），本方案是把「轨迹」作为 web 侧独立功能补齐，并复用 TUI 侧已固化的编码器语义，避免重新发明。

---

## 3. 现状盘点（证据）

### 3.1 后端（Go）

- `/api/agent/chat` SSE 已实时 flush：`backend/internal/api/skills/handler.go:6889` `streamLLMChat`，事件循环 `:7011-7028` 逐 chunk `emitter.Emit("chunk"/"reasoning"/"tool_*"/…)`；`writeSSEEventWithEnvelope`（`:7684`）已带 `sequence int` 参数 → **seq 基础设施已存在，只差暴露给前端并落盘**；
- 事件名（前端 `sse.ts:219-258` 已覆盖）：`meta / chunk / reasoning / tool_start / tool_call / tool_end / planning / orchestration / route / observation / subagent / result / done / error`；
- **⚠️ 评审修正（2026-08-16）：会话事件持久化基础设施已存在，并非空白**：
  - `internal/events` 运行时事件总线：`Event{Type, TraceID, AgentName, SessionID, ToolName, Payload, Timestamp}`（`internal/events/bus.go:11-19`）；
  - `chat.EventStore` 接口：`AppendEvent(ctx, event) (seq, err)` + `ListEvents(ctx, sessionID, afterSeq, limit)`，有 InMemory 与 SQLite 双实现（`chat/session_runtime_store.go:42-46, 992, 3095`）；SQLite 表结构含 `seq/type/trace_id/agent_name/tool_name/payload_json/created_at`；
  - **bus → EventStore 持久化管道已在主路径**：`handler.go:3293-3303` `bus.Subscribe("")` → `shouldPersistRuntimeSessionEvent` 过滤 → `mapRuntimeEventToSession` → `AppendEvent`；
  - 增量拉取端点已存在：`GET /api/runtime/sessions/{id}/runtime/events`（`ListSessionRuntimeEvents`，`session_runtime_handlers.go:489`）**已支持 `after`/`after_seq`/`limit`/`wait_ms` 参数 + `WatchEvents` 长轮询**（:502-558）；
  - 流式端点 `GET /sessions/{id}/runtime/stream`（`session_runtime_stream.go:24`）为 **durable + live 双路**：durable 轮询 EventStore；`live=1` 时订阅 in-process bus 的高频事件（如 `tool.progress`），**live 事件标记 `payload["live"]=true` 且不写 durable store**；
  - **真实缺口**：上述存储承载的是「运行时生命周期事件」（assistant message、mailbox、background job、tool receipt 等）；`/api/agent/chat` 的 **LLM 轨迹事件（chunk/reasoning/工具参数与输出）目前未纳入任何持久化**——轨迹回放/断线续传/离线分析所需的事件序与内容不存在。

### 3.2 前端（React）

- SSE 解析完备：`frontend/src/api/runtime/sse.ts`（`consumeSseResponse` 处理 `event:`/`data:`/粘包拆包）；
- turn 状态机 `frontend/src/hooks/workspace/use-workspace-agent-chat-turn.ts`：
  - `onChunk`（:617）→ `streamedText += delta; scheduleStreamingMessage()` ✅；
  - `onReasoning`（:621-635）→ 已 `scheduleStreamingMessage()`（**G1 已修复**）✅；
  - `onToolStart/Call/End`（:638-646）→ `toolPayloads.push` → 映射为 `ToolMessageSegment`（**G2 已修复**，组件 `message-tool-row.tsx`）✅；
  - `finalizeTurn`（:363）→ 结束时一次性 `buildAssistantMessageSegments` 全量落盘；
- 渲染组件：`message-reasoning-row.tsx`（折叠/摘要/流式脉冲）、`message-tool-row.tsx`（started/running/finished/error 状态徽章）、`message-markdown-streaming.ts`（stable/tail 拆分）、`message-list.tsx`（:116-141 atBottom 滚动跟随，**G5 已修复**）；
- 线程数据模型：`frontend/src/lib/workspace-thread-state.ts`（`MessageSegment` 判别联合，含 tool/reasoning 段）；
- **⚠️ 评审修正：前端已具备「seq 增量续传」机制**：`hooks/workspace/use-session-runtime-stream.ts` 通过 `streamSessionRuntime(sessionId, {after: seq, pollMs: 500, onEvent})` 轮询 `/runtime/events`，并用 `getRuntimeEventSeq` / `mergeRuntimeEvent` 按 seq 去重合并——**Phase 3 的断线续传可直接对齐此机制，而非新造**；
- **仍开放**：G4（stable 每帧全量重解析）、G6（首 token 阶段感知）、G7（planning/orchestration/route/observation/subagent 无实时 UI）、G8（中断/错误流式尾巴呈现）；
- **缺失**：统一渲染模型（无 ID/Seq/CauseID）、轨迹视图、虚拟滚动、搜索、事件日志消费。

### 3.3 TUI 侧可复用资产（`cmd/aicli`，P1/P2 已落地）

- `EventEncoder → ChangeSet → Scene` 已实现并有测试：乱序缓冲拼接（`1,3,2 → ABC`）、reasoning 独立索引、终态保护、幂等、Tail 锚定插入、事件日志 replay 重建等价；
- 语义规格见 `render-model-spec.md`；**web 侧按同一规格在 TS 实现 reducer**，并用共享测试向量（golden 事件序列 → 期望快照）对齐 TUI 语义，避免双份实现语义漂移（见风险 R1）。

---

## 4. 参照：DeepSeek-Reasonix 轨迹实现原理 → 可迁移机制

| 机制 | DeepSeek-Reasonix 做法 | 本项目现状 | 迁移方式 |
|---|---|---|---|
| 单通道 + kind 判别 | `agent:event` 通道，`WireEvent` 判别联合（30 种 kind） | SSE `event:` 行 + 13 类事件 | 已有，仅补 `seq` |
| sink 装饰器链 | `trajectory.Recorder` / `tabEventSink` / botSink 一次产生多处消费 | `emitter.Emit` 单点 | Phase 0 挂 Recorder（不阻塞主链路） |
| 纯函数 reducer | `applyEvent(s, e) → State`，`Item` 判别联合 | hook 内 if/else 三块分离状态 | Phase 1 新建 `trajectory-reducer.ts` |
| rAF 合并批处理 | `coalesceStreamDeltas`：帧内同 kind 合并，每帧一次 reducer 派发 | `scheduleStreamingMessage`（仅文本单通道） | Phase 1 引入 `stream-batch.ts` |
| 实时/历史同一管线 | `historyMessagesToItems` 与实时流同构 `Item[]` | 历史 sync 全量重建 thread | Phase 1/3 事件日志成为唯一事实源 |
| 身份稳定 | `runtimeEpoch` + `submissionId` + tool id | `tool_call.id` 部分具备 | Phase 1 ID/Seq/CauseID 编码器分配 |
| 推理轨迹优化 | 流式展开/折叠 + 稳定窗口裁剪 + 摘要 + 显示偏好 | `MessageReasoningRow` 有折叠/摘要 | Phase 2 补稳定窗口（可选） |
| 事件日志 | `internal/trajectory` JSONL（schema/seq/ts + 审计） | **已有 `chat.EventStore`（seq/after 增量）+ bus 持久化管道，但 chat SSE 轨迹事件未入存储**（评审修正） | Phase 0 复用 EventStore 接入 chat SSE 事件；JSONL 仅作导出格式（Phase 3） |

---

## 5. 差距分析

| # | 差距 | 根因 | 影响 | 解决 |
|---|---|---|---|---|
| D1 | **chat SSE 轨迹事件未持久化**（评审修正：EventStore 已存在，但只存运行时生命周期事件） | `/api/agent/chat` 的 emitter 只写 SSE，未调 `AppendEvent`；`writeSSEEventWithEnvelope` 的 sequence 未外露 | 断线无法增量续传；无离线回放/分析 | Phase 0（复用 EventStore + 暴露 seq） |
| D2 | 前端无统一渲染模型 | hook 三块分离状态，无 ID/状态机 | 身份不稳定（行闪动）；无法 replay；流式期间可能被历史同步覆盖（原 R3） | Phase 1 |
| D3 | 无轨迹视图 | 只有 chat 一种投影 | roadmap「运行轨迹展示」未落地；G7 事件（planning/subagent…）无实时 UI | Phase 2 |
| D4 | 流式 O(n²)（原 G4） | stable 每帧全量 `ReactMarkdown` 解析 | 长回复帧率下降 | Phase 2（增量缓存/稳定 key，与 streaming 文档口径一致） |
| D5 | 无虚拟滚动/搜索 | 长会话无窗口化 | 千级事件轨迹不可用 | Phase 2 |
| D6 | 首 token 阶段感知缺失（原 G6） | 提交后到首 chunk 无中间状态 | 弱网/慢模型体验差 | Phase 2（时间线派生 TTFT，顺带补） |
| D7 | **双事件源未统一**（评审新增）：chat SSE（LLM 轨迹）与 runtime/events（生命周期事件，含 live 不落盘事件）是两路 | 二者分别由 `emitter.Emit` 与 `bus.Publish` 产生，无关联 | 轨迹视图数据面来源不清；可能重复/遗漏事件 | Phase 0 界定主源（chat SSE 入 EventStore）；runtime 事件按需映射（后续项） |

> 与 `workspace-chat-streaming-realtime-rendering-plan.md` 的关系：G1/G2/G5 已落地；G3 由 Phase 1 的 rAF+setTimeout 兜底承接；G4/G6/G7/G8 由本方案 Phase 1/2 承接。**本文不重复原文档已给出的设计**，只在其缺口清单上做增量。

---

## 6. 目标架构

```
后端 streamLLMChat 事件循环（handler.go:7008-7028，唯一发射点）
  │ emitter.Emit(kind, payload)
  ├─► SSE data: 帧（现有，新增顶层 seq 字段 = AppendEvent 返回 seq）
  └─► chat.EventStore.AppendEvent（Phase 0 接入；Type 命名空间 chat.sse.*，
         Payload 存原始事件载荷；复用现有 SQLite 存储与 seq/after 机制；
         失败仅降级计数，不阻塞主链路）
              │
              ▼
      GET /api/runtime/sessions/{id}/runtime/events?after=N（已存在，
         评审修正：无需新端点；轨迹事件按 Type 前缀 chat.sse.* 过滤）
              │
              ▼
前端 trajectory-reducer.ts（Phase 1 新增，纯函数，规格对齐 render-model-spec）
  │  append / upsert / remove（ID/Seq/Kind/CauseID/Status/Head）
  │  输入经 stream-batch.ts 做 rAF 帧内同 kind 合并（对齐 streamDeltaBatch）
  ▼
TrajectorySnapshot（不可变 Item[] 快照 + Tail）
  ├─► chat 投影：Item[] → MessageSegment[]（映射器，message-list 零改动）
  └─► trajectory 投影：TrajectoryView（Phase 2：时间线 + 明细 + 虚拟滚动 + 搜索）

（已有且不动：runtime 事件总线 → EventStore 持久化管道 handler.go:3293；
  useSessionRuntimeStream 增量轮询；两者与 chat SSE 轨迹为双事件源，见 D7/R5）
```

分层职责（对齐 DeepSeek-Reasonix 前端与 TUI 编码器）：

| 层 | 对应 DeepSeek-Reasonix | 本项目落点 |
|---|---|---|
| 事件源 | `agent:event` 单通道 | SSE `event:` 行（已存在，补 seq） |
| 状态机 | `useController.ts applyEvent` | `trajectory-reducer.ts`（纯函数） |
| 帧合并 | `streamDeltaBatch.ts coalesceStreamDeltas` | `stream-batch.ts` |
| 快照 | `State.items: Item[]` | `TrajectorySnapshot`（不可变） |
| 历史/实时统一 | `historyMessagesToItems` | 事件日志（`runtime/events?after=`）→ 同一 reducer 重放 |
| 渲染 | `Transcript.tsx` 虚拟化行 | chat 投影沿用现有组件；轨迹投影新建 |

---

## 7. 分阶段实施

### Phase 0：chat SSE 轨迹事件入 EventStore + seq 契约（预估 1–2 天，评审修正）✅ 已实施（2026-08-16）

> **实施记录**：`backend/internal/api/skills/trajectory_events.go`（`newTrajectoryEmitter`：`persist` 钩子先行 `AppendEvent(Type: "chat.sse."+event)`，失败降级为连接内计数；SSE 帧 `_event.sequence` = 持久化 seq）；`streamLLMChat`/`streamStaticResult` 切换新 emitter；`sseEmitter` 增加可选 `persist` 字段（log_viewer/runtime_stream 不挂钩子，行为不变）。集成测试 `trajectory_events_test.go` 覆盖 seq 一致、after 补齐、错误隔离、双事件源边界，skills 包全量回归通过。

**改动点**

1. **复用 `chat.EventStore` 承载 chat SSE 轨迹事件**（不新建 JSONL 录制；`internal/events` 的 `Event{Type, SessionID, Payload, Timestamp}` 结构可直接承载）：
   - `streamLLMChat` 事件循环（handler.go:7008-7028）每 emit 一帧时同步调 `sessionEventStore.AppendEvent(ctx, runtimeevents.Event{Type: "chat.sse."+kind, SessionID: sessionID, Payload: 原始载荷, Timestamp: now})`；
   - **seq 契约**：SSE `data:` 帧新增顶层 `seq` 字段 = `AppendEvent` 返回的 seq（复用 `writeSSEEventWithEnvelope` 的 sequence 位置，改为真值）；前端据此增量续传；
   - **错误隔离**：`AppendEvent` 失败仅计数并降级，绝不阻塞 SSE 主链路（对齐 DeepSeek-Reasonix「Recording failures never block forwarding」）；
   - **并发安全**：复用 EventStore 自带锁（InMemory `s.mu` / SQLite 事务），无需新增同步原语。
2. **拉取**：不新建端点——`GET /api/runtime/sessions/{id}/runtime/events?after=N` 已支持增量（`session_runtime_handlers.go:502-558`）；如需与生命周期事件区分，评估增加 `type_prefix=chat.sse.` 过滤参数（可选，默认拉全量由前端按 Type 前缀过滤）。
3. **敏感性与清理**：轨迹事件与现有 runtime 事件同库（SQLite），沿用既有存储保留/清理策略（见风险 R4；不引入 recovery_gc——本项目无此机制）。
4. 测试：
   - 集成测试（对齐 `session_agent_controller_test.go` 的 `ListEvents` 断言风格）：SSE 帧 seq 单调、与 `ListEvents` 返回一致、`after` 拉取补齐正确、`AppendEvent` 错误注入后 SSE 不受影响；
   - 双事件源边界测试：确认 chat SSE 事件与 runtime 生命周期事件在存储中可区分（Type 前缀），互不干扰。

**验收（评审修正）**：一场含推理+工具的 chat 后，`GET /runtime/events?after=0` 能按 seq 拉回全部 `chat.sse.*` 事件（含内容）；SSE 帧 `seq` 与存储一致；模拟断线后 `after` 补齐无缺口；EventStore 写入失败时 chat 仍正常。

### Phase 1：前端统一渲染模型（预估 3–5 天）

**改动点**

1. `frontend/src/lib/trajectory/types.ts`：`TrajectoryEventKind`（复用 13 类 SSE 事件）、`Item`（ID/Seq/Kind/CauseID/Status/Head/Created/Updated，照搬 `render-model-spec.md` §3）、`ChangeSet`（Append/Upsert/Remove + Revision + Tail）。
2. `frontend/src/lib/trajectory/trajectory-reducer.ts`（纯函数，核心）：
   - `append / upsert / remove` + 幂等规则 + 终态保护（语义逐条对齐 TUI 编码器已固化清单：乱序缓冲拼接 `1,3,2→ABC`、reasoning 独立索引、孤儿 final 直接终态）；
   - 工具状态机：`tool_start→tool_call→tool_end` 折叠为单 Item（status: started→running→finished/error），输出经 `CauseID` 锚定；
   - G7 事件（planning/orchestration/route/observation/subagent）映射为可折叠 Item（数据面先行，UI 在 Phase 2）。
3. `frontend/src/lib/trajectory/stream-batch.ts`：rAF 帧合并（同 kind 连续 delta 合并为 segment；空 delta 保留以完成 live reasoning 边界；`setTimeout` 兜底后台标签页——承接 G3）。
4. 接入 `use-workspace-agent-chat-turn.ts`：
   - `onChunk/onReasoning/onTool*` 改为「先派发 reducer、再以快照映射 segments」（保留现有渲染节流）；
   - 新增 `useTrajectorySnapshot()`：返回不可变快照 + chat 投影映射器（`Item[] → MessageSegment[]`，复用 `workspace-thread-state.ts` 的 upsert 辅助函数）；`message-list.tsx` **零改动**。
5. 测试：reducer 单测对齐 TUI `encoder_test` 用例（乱序/幂等/终态/reasoning 索引）；hook 集成测试（流式→finalize 快照一致、stop 路径）；**replay 等价**（同一事件序列两次构建快照深相等）。

**验收**：流式期间 tool/reasoning/text 以稳定 ID upsert（行不闪动）；replay 幂等；chat 视图无回归（现有 e2e 全绿）。

### Phase 2：轨迹视图 UI（预估 3–5 天）

**改动点**

1. `frontend/src/components/workspace/trajectory/trajectory-view.tsx`：
   - 工具栏：搜索、导出 JSONL、筛选（仅工具/仅消息/全部）、时间线模式（sequence；duration 可选）；
   - 时间线概览：事件泳道（tool 高亮车道，对齐 ui-trajectory timeline 的三车道思路）；
   - 明细列表：每条 Item 单行摘要（对齐 `trajectory-record.ts` 单对象双用：摘要 + 详情面板展开参数/输出/推理片段）。
2. 虚拟滚动 `trajectory-virtual-rows.ts`：行 = Item，行高缓存；request-only 零高行合并（对齐 DeepSeek-Reasonix `transcriptRows.ts` 思路）——解决 D5。
3. 搜索索引（若 Phase 2 时间紧可推迟到 Phase 3）：增量全文索引 + 3s 节流 + terms AND。
4. 入口：`workspace-shell` 顶部或 artifact 面板加「轨迹」切换（roadmap「消息区 workflow 表达」的落地形态，开放问题 Q1）。
5. 流式窗口优化（可选）：`MessageReasoningRow` 补稳定窗口裁剪（对齐 DeepSeek-Reasonix `STREAMING_REASONING_WINDOW_STEP_CHARS/LINES`）解决超长推理重排抖动。
6. G4（stable 增量解析）：在轨迹明细的 markdown 渲染处直接采用「frozen/tail + 源 offset 稳定 key」（deepseek-harness `incremental.ts` 思路）；chat 视图的 G4 修复遵循 streaming 文档口径，不在本文重复。
7. e2e（Playwright + mock SSE）：事件逐条出现、搜索命中、1000+ 事件滚动不卡、导出文件与日志一致。

**验收**：含推理+多工具+subagent 的会话可在轨迹视图完整回放；搜索/导出可用；千级事件不掉帧。

### Phase 3：恢复、重放与离线分析（预估 2–3 天）

**改动点**

1. 重连恢复：**对齐既有 `useSessionRuntimeStream` 机制**（`streamSessionRuntime({after, pollMs})` + `getRuntimeEventSeq`/`mergeRuntimeEvent`）——挂载时以 `after=本地已收最大 seq` 拉取 `chat.sse.*` 事件 → 同一 reducer 重放 → 快照与中断前一致（替换 `use-session-history-sync` 的全量重建路径，缓解 R3；保留 history sync 作为最终兜底）。
2. 导出：轨迹视图「导出 JSONL」——从 EventStore 读取 `chat.sse.*` 事件（按 seq）生成 JSONL 文件下载（DeepSeek-Reasonix `internal/trajectory` 格式的轻量版：`{seq, ts, kind, payload}`）。
3. 离线分析脚本 `scripts/analyze-trajectory.mjs`：TTFT、工具耗时、重试率、token 分布、reasoning 占比（DeepSeek-Reasonix `cmd/e2ebench` 的轻量版）。
4. 测试：kill -9 恢复 e2e；回放等价性；脚本对样例 JSONL 输出稳定指标；导出内容与 `runtime/events` 拉取结果逐条一致。

**验收**：会话中断后重开，轨迹视图与中断前逐条一致；导出的日志可用脚本产出诊断报告。

---

## 8. 测试策略

| 层 | 用例 | 对齐参考 |
|---|---|---|
| Go 集成 | SSE 帧 seq 与 EventStore 一致；`after` 补齐；错误注入隔离 | `session_agent_controller_test.go` 的 `ListEvents` 断言风格、`handler_session_persistence_test.go` |
| TS 单测 | reducer 乱序/幂等/终态/reasoning 索引 | TUI `encoder_test.go` 用例（共享 golden 向量） |
| TS 组件 | 轨迹视图摘要/详情/筛选/虚拟行 | `message-tool-row` 测试风格 |
| e2e | mock SSE 全流程；1000 事件；断线恢复 | Playwright（`frontend/e2e/`，已有 `mock-server.mjs` + `workspace-chat.spec.ts`） |

---

## 9. 风险与开放问题

- **R1 双份状态机漂移**：TUI 编码器（Go）与 web reducer（TS）并存。短期：共享 golden 测试向量（同一事件 JSON → 期望快照）锁语义；中期评估把编码器上提 `internal/` 供 web 后端复用（成本高，需单独评估）。
- **R2 seq 契约**（评审修正）：`writeSSEEventWithEnvelope` 的 sequence 目前是单连接内计数；改后 = `AppendEvent` 返回的 EventStore seq（按 session 持久化）。需处理：并发多连接同时 append 的可见顺序、`ListEvents` 分页 limit 与 seq 断档、与既有 runtime 生命周期事件共用一个 seq 序列（`chat.sse.*` 前缀区分类型即可）。
- **R3 历史同步覆盖流式**：Phase 3 事件日志成为唯一事实源后缓解；在此之前保持现状保护（流式期间暂停 history sync）。
- **R4 日志敏感性与容量**（评审修正）：轨迹事件含 prompt/工具参数，与会话记录同级敏感。复用 EventStore（SQLite）现有保留/清理策略；导出时提供脱敏选项；不引入本项目不存在的 `recovery_gc` 机制。
- **R5 双事件源边界**（评审新增）：chat SSE（`chat.sse.*` 落 EventStore）与 runtime 生命周期事件（bus→EventStore 管道）共存于同一 store；`tool.progress` 等 live 事件不落盘（`session_runtime_stream.go:18-23` 已定义）。轨迹视图只消费 `chat.sse.*`；runtime 事件（approval/notice）是否映射进轨迹列为后续项（Q4）。
- **Q1** 轨迹视图入口形态：Tab 切换 vs 并排分栏？（建议先 Tab 切换，工作量最小）
- **Q2** duration 时间线（idle 压缩）是否本期做？（建议 Phase 2 只做 sequence，duration 列为后续）
- **Q3** 增量搜索索引是否本期做？（建议 Phase 3；Phase 2 先用线性过滤）
- **Q4**（评审新增）runtime 生命周期事件（approval/notice/team 编排）是否映射进轨迹视图？（建议后续项；本期只做 `chat.sse.*`）

---

## 10. 验收清单（对齐 roadmap.md）

- [x] Phase 0：chat SSE 事件入 EventStore（`chat.sse.*`）+ SSE 帧 `seq` + 复用 `runtime/events?after=` 增量拉取（集成测试全绿）
- [ ] Phase 1：web 统一 reducer 落地并接入 turn hook；replay 等价；chat 视图零回归
- [ ] Phase 2：轨迹视图（时间线/明细/筛选/虚拟滚动）可用；G4/G6 顺带解决；e2e 通过
- [ ] Phase 3：断线恢复一致；导出 + 离线分析脚本产出诊断
- [ ] roadmap「运行轨迹展示」验收项达成（多 session/多 team 工作流稳定复用不受影响）

---

## 11. 参考

- DeepSeek-Reasonix：`internal/trajectory/recorder.go`；`desktop/app.go`（eventChannel/tabEventSink）；`desktop/tabs.go`（Emit 装饰链）；`frontend/src/lib/streamDeltaBatch.ts`、`useController.ts`、`transcriptStore.ts`、`transcriptRows.ts`、`AssistantReasoningPanel.tsx`
- deepseek-harness：`packages/client/ui-trajectory`（经 `workspace-chat-streaming-realtime-rendering-plan.md` §5 转述：节点定义/快照构建/发布节流/虚拟行/时间线）
- 本项目：`backend/internal/api/skills/handler.go:6889-7028,7684`；`session_runtime_stream.go`；`frontend/src/api/runtime/sse.ts`；`hooks/workspace/use-workspace-agent-chat-turn.ts`；`components/workspace/message-reasoning-row.tsx`、`message-tool-row.tsx`、`message-markdown-streaming.ts`、`message-list.tsx`；`lib/workspace-thread-state.ts`
- 本项目 TUI 规格：`docs/plan/aicli-event-stream-rendering-order-render-model-spec.md`、`todo.md`、`unified-encoder-plan.md`、`event-encoder-api-design.md`
