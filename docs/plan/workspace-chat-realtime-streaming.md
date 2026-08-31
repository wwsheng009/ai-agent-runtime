# workspace 聊天缺少实时 SSE 打字机 — 根因分析与优化方案

> 状态：分析完成，待评审实施
> 日期：2026-08-30
> 范围：`frontend/`（workspace 聊天）+ `backend/internal/api/skills/handler.go` + `backend/internal/agent/loop.go`

## 1. 问题现象

- 在 `http://localhost:8101/workspace/sessions/{session_id}` 页面发起聊天：
  - 发送后没有任何逐字/逐 token 输出；
  - 整个请求（ReAct agent 循环）结束后，最终结果一次性出现在消息区；
  - 上游网关故障时（见 §4.4）表现为"最后才显示一条错误"。
- 期望：与 aicli 终端 / deepseek-harness 一致的实时打字机（边生成边显示）。

## 2. 复现方法

```
# 模式 1：llm_stream 直连（enable_react=false）→ 有逐 token chunk（打字机可用）
POST /api/agent/chat
{"messages":[{"role":"user","content":"...","stream":true,"enable_react":false}}

# 模式 2：ReAct agent（前端默认 enable_react=true）→ 无 chunk，结束时一次性大块
{"messages":[{"role":"user","content":"...","stream":true,"enable_react":true}}
```

用 `curl -N` 或 node 记录每个 SSE 事件到达时间即可复现：模式 2 在等待数秒/数十秒后只出现
`meta → result → chunk(整个 output) → done`。

## 3. 现状架构

### 3.1 前端链路（workspace 聊天）

```
提交 prompt
  → useWorkspaceAgentChatTurn.submitPrompt
      （frontend/src/hooks/workspace/use-workspace-agent-chat-turn.ts）
  → streamAgentChat(request, handlers)
      （frontend/src/api/runtime/sse.ts:192 起，fetch + ReadableStream + SSE 行解析）
  → onChunk / onReasoning / onToolStart / ... 处理器
      （use-workspace-agent-chat-turn.ts:590-708）
  → scheduleStreamingMessage()（rAF + 100ms setTimeout 兜底，:252）
  → renderStreamingMessage() → setStreamingMessage()
  → buildAssistantMessageSegments(累积完整文本, ...)
      （frontend/src/lib/workspace-thread-state.ts:126）
```

- 请求默认带 `enable_react: settings.chat.enableReact`，默认值为 **true**
  （frontend/src/core/settings/local.ts:131）。
- SSE 解析器（`consumeSseResponse`，sse.ts:84 起）是标准实现，支持 `event:` / `data:` 多行、trailing buffer，无缺陷。
- 渲染：`buildStreamingMessageSegments`（workspace-thread-state.ts:561）**每次用完整累积文本重建**一个
  `text` segment；`renderStreamingMessage` 每次触发整条消息的 markdown 全量重渲染。

### 3.2 后端 /api/agent/chat 两条路径（internal/api/skills/handler.go）

```
AgentChat (:1349)
├─ streamingRequested=true
│  ├─ req.EnableReAct && llmRuntime != nil  ──► a.RunReActWithSession(...)  （:1646）
│  │     └─ 执行完毕后 h.streamStaticResult(...)（:1689）← 一次性！§3.3
│  ├─ req.ExecutePlannedSubagents           ──► a.Orchestrate(...) → streamStaticResult
│  └─ 默认（直连 LLM）                        ──► llm_stream 循环（:7187）
│        for chunk := range stream { emitter.Emit("chunk", ...) }  ← 逐 token ✓
└─ stream=false                               ──► streamStaticResult（非流式请求兜底）
```

- **llm_stream 直连分支**（:7193）逐 chunk `emitter.Emit("chunk", buildStreamChunkPayload(...))`，
  每个写出的 SSE 事件都 `flusher.Flush()`（handler.go:7881），后端缓冲区无问题。
- **ReAct / agent 分支**：`RunReActWithSession` 结束后才调 `streamStaticResult`。

### 3.3 streamStaticResult（一次性输出）— handler.go:7292 起

```
meta → planning → orchestration → route → tool/observation/subagent 事件（回放）→
result → chunk{content: 整个 output} → done
```

- 全部事件在 agent 执行完成后几毫秒内依次 emit，**没有任何中间增量**。
- 前端 `onChunk` 收到这唯一一个大块后一次性渲染 → 与用户观察完全一致。
- 注意：`streamStaticResult` 内部会再次 `prepareSSEHeaders` + 新建 emitter
  （handler.go:7294-7295）——若前置已开流，这套收尾不能原样复用（见方案 A）。

### 3.4 ReAct 循环内部其实已经在流式（关键事实）— internal/agent/loop.go

- 每步 LLM 请求 `req.Stream = true`（loop.go:1467；1469-1493 还强制图片能力也走流式）。
- `llm.WithStreamReporter(ctx, ...)` 回调（loop.go:1498 起）为**每个 text chunk** 执行
  `loop.emitRuntimeEvent("assistant_delta", sessionID, payload)`（:1518），
  payload 含 `stream_id / sequence / mode:"append" / step / delta`；
  reasoning chunk 同样 emit `assistant.reasoning`（:1544）。
- 这些事件进入 **session 事件流**（internal/chat/events.go，`EventAssistantDelta`），
  即 `GET /api/runtime/sessions/{id}/runtime/stream` 长轮询可以拉到的"实时事件"。

**结论：增量数据流在后端 ReAct 循环内已经存在，只是没有接到 /api/agent/chat 的 SSE 上。**

### 3.5 前端"浪费"了已有的实时通道

- workspace 页面同时挂着 `useSessionRuntimeStream`（runtime/stream 长轮询 SSE），
  但它**不处理 `assistant_delta`/`assistant_reasoning`**（全仓库 grep 确认：
  `stream_id`/`assistant_delta` 在 `frontend/src` 无任何消费代码；
  `src/lib/trajectory/golden.ts` 只有头注释里的映射对照表（:13-15），
  其主体是 TUI/TS reducer 对拍的测试向量，不是实际逻辑）。
- 因此 ReAct 执行期间产出的增量事件，前端一条都没被渲染。

## 4. 根因小结

| # | 根因 | 位置 | 影响 |
| --- | --- | --- | --- |
| 1 | 前端默认 `enable_react=true`，聊天恒走 ReAct 分支 | settings/local.ts:131 | 打字机路径（llm_stream）默认不被触发 |
| 2 | ReAct 分支不把循环内增量转发到请求 SSE，结束才 `streamStaticResult` 一次性输出 | handler.go:1646-1689、7292 | "只有请求完成后才显示"的直接原因 |
| 3 | 循环内增量（assistant_delta）已进会话事件流，但 `/api/agent/chat` 与前端渲染均未接线 | loop.go:1518；前端 use-session-runtime-stream | 已有实时通道被浪费 |
| 4 | 前端每次渲染整串重建 text segment（markdown 全量重渲染） | workspace-thread-state.ts:561 | 增量渲染本身低效，长文本会卡顿（与 aicli 历史 UI 卡顿同型） |
| 5 | 上游 LLM 网关可用性差（本机 127.0.0.1:9000 拒连、ttai 网关返回 Cloudflare 拦截页） | 环境 | 放大"等很久才看到（错误）结果"的观感 |

## 5. deepseek-harness 的参考设计

位置：`E:\projects\ai\deepseek-harness`

### 5.1 事件协议（assistant.ts 的 ConversationNodeDefinition）

```
step/start                      → 创建 streaming step 节点（id = `${turn}:${step}`）
assistant/chunk                 → 每上游 chunk 一条 update 事件（同一 id，incremental）
assistant/message(append)       → 最终定型
assistant/step-interrupted      → 中断态
```

- 事件都带**稳定的 `turn:step` 节点 id**，UI 按 id 定位同一节点做**增量更新**，
  而不是"整条消息重建"。
- 中间层 `BlockAssembler`（packages/llm/llm/src/assembler.ts）：把 chunk 流增量组装成
  content blocks（容忍 delta-only 协议、block-end 冻结、重复 delta 丢弃），
  同时保留原始 chunk 日志用于回放保真——组装与渲染解耦。

### 5.2 对我们项目的映射

| deepseek-harness | 本项目现状 | 差距 |
| --- | --- | --- |
| `assistant/chunk`（每 chunk 一事件，稳定 id） | `assistant_delta`（loop.go 已 emit，含 stream_id/sequence） | 已有！只差转发/消费 |
| UI 增量追加渲染 | buildAssistantMessageSegments 整串重建 | 需改增量 |
| BlockAssembler 组装 | llm_stream 分支的 builder strings.Builder（:7178） | llm_stream 够用；ReAct 需要增量快照 |

**核心借鉴：把"agent 循环内已产生的增量事件"接到请求 SSE，并让 UI 增量渲染。**

## 6. 优化方案

### 方案 A（推荐先做，后端转发，前端零改动可立即提速）

让 `/api/agent/chat` 的 ReAct 分支把循环内实时产生的 LLM 增量转发为 SSE
`chunk` / `reasoning` / `image` 事件，结束后只发 `result` / `done` 收尾。

**前置事实（已核实）**：
- loop 内部流式 reporter 建立于 `loop.go:1498`：`callCtx = llm.WithStreamReporter(...)`，
  覆盖 Text/Reasoning/Image 三类 chunk，分别 emit `assistant_delta` /
  `assistant.reasoning` / `assistant.image_progress` 到会话事件流。
- `WithStreamReporter`（internal/llm/stream_reporter.go）是**单值 context key**，
  且 `RunReActWithSession` 的循环是**在 handler 传入 ctx 之上再 WithValue 一层**——
  LLM 侧 `ctx.Value` 取到的是**最内层**（loop 设置的那个）。因此「handler 在调用前
  再包一层 reporter」**收不到任何 chunk**（会被 loop 覆盖），此路不通。
- 反过来，reporter 链上没有只读 getter（`StreamReporterFromContext` 不存在，
  需补导出）。

改动点（按此顺序实现）：

1. `internal/agent/loop.go` + `internal/agent/*`：给 `LoopReActConfig` 增加
   `StreamSink func(chunk llm.StreamChunk)` 字段；在 :1498 的 reporter 闭包内
   （组装 payload 的 `emitRuntimeEvent` 调用旁）追加：
   ```go
   if loop.config != nil && loop.config.StreamSink != nil {
       loop.config.StreamSink(chunk)
   }
   ```
   不用 ctx 链，语义直白；默认 nil，不影响现有行为。
2. `internal/api/skills/handler.go` ReAct 分支（:1646 前）：
   - **开流前置**：`RunReActWithSession` 之前先 `prepareSSEHeaders(w)` +
     `newTrajectoryEmitter(w, session)` + `emitter.Emit("meta", ...)`
     （session_id/source/kind/model 此时已可确定；orchestration 先用执行前
     routeAttempted/routeCandidates 构建的初版，status:"streaming"——前端 onMeta
     只取 session_id/source/kind，收尾 result 里再带完整 orchestration）。
   - 构造 `loopConfig.StreamSink`：
     ```go
     streamSink := func(chunk llm.StreamChunk) {
       switch chunk.Type {
       case llm.EventTypeText:
         if chunk.Content != "" {
           textLen += len(chunk.Content)
           emitter.Emit("chunk", buildStreamChunkPayload(chunk, idx, textLen))
         }
       case llm.EventTypeReasoning:
         if chunk.Content != "" {
           emitter.Emit("reasoning", chunkPayload)
         }
       case llm.EventTypeImage:
         emitter.Emit("chunk", imagePayload)
       }
     }
     ```
     `textLen`/`idx` 在 sink 闭包内维护（llm_stream 分支同样逻辑：builder.Len()）。
   - **收尾不能复用 `streamStaticResult`**：该函数会再次 prepareSSEHeaders +
     新建 emitter，并重发 meta/planning/orchestration/tool 全套 + 结尾
     重发 `chunk{整段 output}`（:7321）——前端 `onChunk` 是 `streamedText += delta`
     纯追加（use-workspace-agent-chat-turn.ts:646-652），会导致**文本翻倍**。
     新增轻量收尾函数：仅 `emitter.Emit("result", resultPayload)` +
     `emitter.Emit("done", ...)`（工具事件如需实时卡片，见下条）。
   - 工具事件：ReAct loop 的工具 emit 走其它路径（不在 stream reporter 内），
     实时转发需另接（LoopReActConfig 上加 tool 回调或复用现有 hook 事件）。
     P0 阶段可暂不实时，收尾 `done` 后前端如有 tool 段渲染缺失，可在收尾补发
     tool/observation/subagent 回放事件（复用 streamStaticResult 后半段逻辑，
     不含 meta/chunk）。
3. **错误路径**：headers 已写、SSE 已开后，`reactErr != nil` 时不能再走
   `writeAgentChatExecutionError`（writeError 会写 HTTP 状态码/JSON，对已开流的
   连接无效）。需改为 `emitter.Emit("error", {index, message, source})` 后 return
   （llm_stream 分支 :7214 同一模式）。
4. 测试：`internal/api/skills` 增加流式用例（mock LLM 逐 chunk 返回，断言
   SSE 按到达顺序分块发出 chunk/result/done，且文本不翻倍、error 路径正确）。

收益：默认路径（ReAct）立即获得打字机；改动集中在后端两处，前端零改动。

### 方案 B（标准通道：前端消费 runtime/stream 增量，长期演进方向）

把打字机渲染从 `/api/agent/chat` 响应解耦，改为订阅会话事件流
（与 deepseek-harness 一致的"事件驱动渲染"）：

1. 后端：`/api/runtime/sessions/{id}/runtime/stream` 已返回 `assistant_delta`
   （激活条件是 actor 在运行；ReAct 执行期间事件表实时写入——loop.go 已 emit 到 session）。
2. 前端 `use-session-runtime-stream`：为 `assistant_delta` 增加 handler——增量
   append 到当前 assistant 消息。
3. 双通道合并策略：`/api/agent/chat` 的最终 `result` 负责**定型**（覆盖为权威快照），
   runtime/stream 的 delta 负责**过程**。
4. 必须处理的三个对齐问题（否则乱序/回退）：
   - **重连重放**：runtime/stream 断连重连会从持久化 seq 重放事件，前端须按
     `_event.sequence` 幂等去重（可复用 trajectory recovery 的 seq 游标做法）。
   - **多 step 对齐**：每步 LLM 请求独立 `stream_id`（loop.go:1491-1508），而
     最终 `result` payload 不带 stream_id——不能单靠 stream_id 对齐；需以
     step 级文本累积 + 会话级 result（或 `assistant_message` 事件）做定型锚点，
     实施时确认 loop 是否 emit `EventAssistantMessage`（internal/chat/events.go 已定义）。
   - **回退观感**：result 定型覆盖必须"只增不删"（若 result 快照短于已收 delta，
     会出现文本回退闪烁）。
5. 风险：双通道时序；需要与 use-workspace-agent-chat-turn 的阶段性状态机
   （connecting/first-token/streaming）协作测试。

收益：打字机不受请求通道影响（即使 agent/chat 请求失败，过程增量仍可见）；
为未来多端（aicli 终端 / sidecar）共享渲染打基础。建议在方案 A 落地后做。

### 方案 C（前端渲染优化，与 A/B 正交）

- `buildStreamingMessageSegments`/`buildAssistantMessageSegments` 改为**增量 append**：
  保留已渲染前缀，只对新增 delta 做 markdown 解析与追加（text segment 记录 `renderedLen`），
  避免整串 AST 重建（当前整串重建 + goldmark 缓存的历史修复只缓解了峰值）。
- rAF 节流已存在（100ms 兜底），可保持；增量渲染后 rAF 周期内开销稳定 O(delta)。
- 注意 streaming markdown 不完整语法（代码块未闭合、表格未结束）——增量渲染需
  处理"半成品块"：可先按"尾部开放块保持纯文本、闭合后转行内 markdown"策略，或每次
  仍全量渲染但仅对**可见文本增量 diff**（React reconciliation 本身 diff，重渲染成本主要在
  markdown→AST→DOM）。若实测 jank，再做 AST 级增量。

### 方案 D（环境/可观测，1-2 小时）

- 上游网关健康检查：`/api/agent/chat` 的 meta 前置探活（9000 拒连、ttai Cloudflare
  拦截返回 HTML 而非 JSON）→ 提前失败并在 SSE 里带可读错误（现 error 事件已存在，
  但错误文案是原始 dial tcp / HTML 片段）。
- 已就位的部分：前端 lastError → thread 降级提示（Topbar"运行时降级/需要恢复关注"）
  在上一轮已交付；本轮无需新增 UI。
- aicli 侧记忆表明 ttai.online 有间歇性 http2 不吐响应头问题——建议在方案 A 的
  reporter 外层保留既有 retry/stream-idle 超时（providers.http_timeout）不变。

## 7. 建议实施顺序与验证

| 阶段 | 内容 | 验证 |
| --- | --- | --- |
| P0 | 方案 A（后端 ReAct→SSE 增量转发） | curl 模式 2 观察 chunk 分块到达；前端页面打字机出现 |
| P1 | 方案 C（前端增量渲染） | 长文本（>5k 字）流式下页面帧率/滚动稳定性；vitest + e2e 补增量渲染用例 |
| P2 | 方案 B（runtime/stream 事件驱动） | 断网/请求失败时过程文本仍可见；双通道合并无重复 |
| P3 | 方案 D（上游健康探活 + 错误可读化） | 9000/ttai 挂起时 3s 内返回可读错误 |

每个阶段独立可交付、可回滚；P0 是恢复用户所见的打字机体验的最小充分改动。

## 8. 附录：事件流对照

| 事件 | 产生方 | 去向（现状） | 前端消费（现状） |
| --- | --- | --- | --- |
| `chunk`（逐 token） | llm_stream 分支 handler.go:7193 | /api/agent/chat SSE | ✅ onChunk → rAF 渲染 |
| `chunk`（整段 output） | streamStaticResult handler.go:7321 | /api/agent/chat SSE | ✅ 但一次性 |
| `assistant_delta` | ReAct loop.go:1518 | session 事件流（runtime/stream） | ❌ 未接线 |
| `assistant.reasoning` | ReAct loop.go:1544 | session 事件流（runtime/stream） | ❌ 未接线 |
| `result` / `done` | streamStaticResult | /api/agent/chat SSE | ✅ 定型 |
## 9. 实施结果（2026-08-30，configfix13）

方案 B 已实施（review 后修复版）：

**已落地**
- 后端白名单放行 `assistant_delta` / `assistant.reasoning` / `assistant.image_progress`（handler.go `shouldPersistRuntimeSessionEvent`）；实测会话事件流中 `assistant_delta`×26 + `assistant.reasoning`×1385 生成期间实时落库可拉。
- 前端 `applyRuntimeDeltaToThread`（增量追加，raw 保真空白；含外部补充的 `expectedTurnId` turn 防线 + delta coordinator 双路径去重）。
- `useSessionRuntimeStream`：`renderLiveDeltas` gate + 常驻重连循环 + 连续失败阈值（3 次）防抖 + 事件到达清除 stream 来源降级标记 + `activeTurnId` 接线。
- 验证：vitest 81/427 全过；thread-link 2 + live-delta 1 e2e 通过；真机 8101 页面（provider=opencode.ai, model=deepseek-v4-flash）回答到达。

**实测边界（非缺陷）**
- opencode.ai/deepseek-v4-flash 的流式分块为"批量到达"（55 chunks 两波、间隔 ~80ms），前端逐事件渲染正确；视觉打字机渐进度由上游分块粒度决定，慢速模型（deepseek 类 reasoning 先行）会更明显。
- 默认 provider（claude-3-5-sonnet→127.0.0.1:9000 本地网关未启动）56s 重试后一次性报错——需在页面选择可用 provider（如 opencode.ai）。
