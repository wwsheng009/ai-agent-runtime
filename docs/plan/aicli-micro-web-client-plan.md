# 微型 Web 客户端（aicli Web Client）方案设计

> 文档状态：已审查（审查报告：`docs/review/aicli-micro-web-client-plan-review.md`）  
> 适用范围：`backend/cmd/aicli/pprof.go`、`backend/cmd/aicli/commands/`、`backend/internal/events/bus.go`、`backend/internal/chat/events.go`  
> 关联方案：`docs/plan/aicli-render-gateway-application-scenarios.md`、`docs/plan/runtime-observability-supervision-http-api-plan.md`  
> 关联代码：`chat_debug_screen_http.go`、`chat_debug_display_http.go`、`chat_input_queue.go`、`chat_busy_input.go`、`chat_runtime_events.go`

## 1. 背景与目标

### 1.1 背景

当前 aicli 的调试 HTTP 端点族（`/debug/chat/status`、`/debug/chat/screen`、`/debug/endpoints`）提供的是**静态快照**——每次请求返回当前状态，客户端需要轮询才能获取更新。同时，这些端点是只读的，无法从外部向 aicli 会话注入输入。

现有基础设施提供了构建实时双向通信通道的关键要素：

- **loopback HTTP 服务器**（`pprof.go`）已默认启动，监听 `127.0.0.1:0` 随机端口；
- **EventBus**（`internal/events/bus.go`）支持事件订阅，turn 事件类型（`internal/chat/events.go`）覆盖 LLM 请求、Assistant 流式输出、工具调用、审批请求、提问等全生命周期；
- **SSE 工具函数**（`internal/api/skills/handler.go`）已有 `prepareSSEHeaders`、`writeSSEEvent`、`newSSEEmitter` 等实现可参考；
- **输入注入通道**（`chatInputQueue`）提供 `setExternalInputCaptureActive` + `routeInputText` 机制，已在 `chat_busy_input.go` 中验证。

### 1.2 目标

设计一个**微型 Web 客户端**，通过 loopback HTTP 端点提供：

1. **屏幕内容访问**：获取当前 aicli 渲染界面显示的内容（复用 `BuildChatDebugScreenSnapshot`/`BuildChatDebugScreenText`）；
2. **SSE 实时推送**：订阅 Agent EventBus 的 turn 事件，通过 SSE 协议推送到前端，驱动 UI 自动更新；
3. **用户输入注入**：前端通过 POST 请求注入 prompt，直接进入当前 aicli 会话的输入队列，与交互式或无头模式兼容；
4. **统一入口**：一个极简 HTML 页面（内嵌 JS，无外部构建）整合上述能力，作为微型 Web 客户端的入口。

### 1.3 约束

| 约束 | 说明 |
| --- | --- |
| 端口 | 复用现有 loopback 随机端口，仅本机可访问 |
| 不引入外部构建 | 前端页面为内嵌 HTML + JS，无 npm/webpack/vite |
| 与现有模式兼容 | 输入注入不得破坏交互式 KeyHandler 模式 |
| 安全 | 仅 loopback，无鉴权；输入注入影响会话状态，需互斥 |
| 测试 | 后端端点可单元测试，前端页面不纳入自动化测试 |

> **CORS 说明**：所有端点均为**同源**（页面由 `GET /web/` 从同一 loopback 服务器提供，`EventSource`/`fetch` 请求同一 origin），**无需 CORS 响应头**。实现时不要添加 `Access-Control-Allow-Origin: *`（会扩大攻击面）；若未来从 file:// 或其他端口打开页面，属非支持场景。

---

## 2. 现有基础设施调研

### 2.1 Loopback HTTP 服务器（`pprof.go`）

`startPprofServer(addr)` 默认 `127.0.0.1:0`，使用 `http.NewServeMux()`。已注册端点：

| 路径 | 功能 | 实现位置 |
| --- | --- | --- |
| `/debug/pprof/*` | 标准 pprof | `pprof.go:105-115` |
| `/debug/pprof/executor` | Executor 恢复诊断 | `pprof.go:118` |
| `/debug/chat/status` | 渲染器状态快照 | `pprof.go:27` → `commands.MarshalChatDebugDisplayJSON()` |
| `/debug/chat/screen` | 屏幕合成帧 | `pprof.go:32` → `commands.MarshalChatDebugScreenJSON()` |
| `/debug/endpoints` | 端点清单 | `pprof.go:38` → `commands.BuildChatDebugEndpointsText()` |
| `/api/runtime/observe/v1/*` | 观测平面 | `pprof.go` → `commands.HandleChatDebugObserveRequest` |

**结论**：新端点直接注册到同一 mux，保持单端口统一入口。

### 2.2 SSE 基础设施（`internal/api/skills/handler.go`）

可用参考函数：

```go
func (h *Handler) prepareSSEHeaders(w http.ResponseWriter) { /* Content-Type: text/event-stream, Cache-Control: no-cache, Connection: keep-alive, X-Accel-Buffering: no */ }
func newSSEEmitter(w http.ResponseWriter) *sseEmitter { /* 封装 ResponseController + Flusher */ }
func (e *sseEmitter) Emit(event string, data interface{}) { /* sequence 递增 + writeSSEEventWithEnvelope */ }
func (h *Handler) writeSSEEvent(w http.ResponseWriter, event string, data interface{}) { /* event: + data: 格式 */ }
func writeSSEEventWithEnvelope(w, event, data, sequence int64) { /* 带 envelope 序列号 */ }
func wantsEventStream(r *http.Request) bool { /* Accept: text/event-stream */ }
```

**并发安全警示**：参考实现 `sseEmitter`（`handler.go:8019-8030`）**没有互斥锁**——`Emit` 直接写 `http.ResponseWriter` 并递增 `sequence`。而 `EventBus.Publish`（`bus.go:308`）在锁外同步调用 handler，多个发布者并发时 SSE 写入存在竞态。**新端点必须自带 `sync.Mutex` 保护 SSE 写入**，不能直接照搬参考实现。

**结论**：可直接复用模式或提取公用函数到可导入包。现有实现在 `internal/api/skills/handler.go`（`package api`），新端点位于 `cmd/aicli/commands` 包，需在新端点中独立实现（参考模式，代码量很小）。

### 2.3 EventBus 订阅 API（`internal/events/bus.go`）

```go
func (b *Bus) Subscribe(eventType string, handler Handler)         // eventType="" 订阅全部
func (b *Bus) SubscribeCancelable(eventType, handler Handler) Unsubscribe
```

Agent 实例持有自己的 EventBus（`internal/agent/agent.go:168` `eventBus: runtimeevents.NewBus()`）。在 aicli 进程中，**EventBus 实例位于 `session.LocalRuntimeHost.EventBus`**（`chat_actor_host.go:112` 字段 `EventBus *runtimeevents.Bus`）。Session 的 `RuntimeEventBridge`（`chatRuntimeEventBridge`）已通过 `b.session.LocalRuntimeHost.EventBus.Subscribe("", b.Handle)` 订阅该 EventBus 并消费事件（`chat_runtime_events.go:547`）。

**结论**：新 SSE 端点通过 **`session.LocalRuntimeHost.EventBus`** 获取 EventBus 实例并订阅（`SubscribeCancelable` 获取取消函数）。注意：`RuntimeEventBridge` 是订阅者而非 EventBus 持有者，**不能**作为获取 EventBus 的路径。
### 2.4 Turn 事件类型（`internal/chat/events.go`）

关键事件（payload 带 `turn_id`）：

| 事件常量 | 事件字符串 | 用途 |
| --- | --- | --- |
| `EventSessionStart` | `"session_start"` | 会话开始 |
| `EventSessionEnd` | `"session_end"` | 会话结束 |
| `EventLLMRequestStarted` | `"llm_request_started"` | LLM 请求开始 |
| `EventAssistantDelta` | `"assistant_delta"` | Assistant 流式增量（含 `stream_id`） |
| `EventAssistantReasoningDelta` | `"assistant.reasoning"` | 推理流式增量 |
| `EventLLMRequestFinished` | `"llm_request_finished"` | LLM 请求完成 |
| `EventToolStarted` | `"tool_started"` | 工具调用开始 |
| `EventToolFinished` | `"tool_finished"` | 工具调用完成 |
| `EventApprovalRequested` | `"approval_requested"` | 审批请求（需用户决策） |
| `EventApprovalResolved` | `"approval_resolved"` | 审批已处理 |
| `EventQuestionAsked` | `"question_asked"` | 询问用户 |
| `EventQuestionAnswered` | `"question_answered"` | 用户已回答 |
| `EventCheckpointCreated` | `"checkpoint_created"` | checkpoint 创建 |
| `EventSessionCompactStarted` | `"session_compact_started"` | 压缩开始 |
| `EventSessionCompactCompleted` | `"session_compact_completed"` | 压缩完成 |
| `EventJobStarted` | `"job_started"` | 后台任务开始 |
| `EventJobOutput` | `"job_output"` | 后台任务增量输出 |
| `EventJobFinished` | `"job_finished"` | 后台任务完成 |
| `EventJobCancelled` | `"job_cancelled"` | 后台任务取消 |
| `EventSessionInterrupted` | `"session_interrupted"` | 会话中断（ESC/取消） |
| `EventAssistantMessage` | `"assistant_message"` | Assistant 完整消息（渲染结果） |
| `EventAssistantImageProgress` | `"assistant.image_progress"` | 图像生成进度 |
| `EventToolReceiptRecorded` | `"tool_receipt_recorded"` | 工具回执记录 |
| `EventToolReceiptReplayed` | `"tool_receipt_replayed"` | 工具回执重放 |
| `EventSessionCompactSkipped` | `"session_compact_skipped"` | 压缩跳过 |
| `EventSessionCompactFailed` | `"session_compact_failed"` | 压缩失败 |
| `EventBacktrackStarted` | `"backtrack_started"` | 回溯开始 |
| `EventBacktrackFinished` | `"backtrack_finished"` | 回溯完成 |
| `EventRewindFinished` | `"rewind_finished"` | 回退完成 |

> **命名注意**：推理事件有两个拼写——`"assistant_reasoning"`（`EventAssistantReasoning`，兼容别名）与 `"assistant.reasoning"`（`EventAssistantReasoningDelta`，标准名）。订阅时需两者都覆盖或统一使用标准常量。

**结论**：SSE 端点可选择性订阅这些事件类型，映射为前端 SSE event 名称。

### 2.5 输入注入通道（`chat_input_queue.go` + `chat_busy_input.go`）

关键函数：

```go
func (q *chatInputQueue) setExternalInputCaptureActive(active bool)   // 开关外部输入捕获
func (q *chatInputQueue) hasExternalInputCaptureActive() bool         // 查询是否已捕获
func (q *chatInputQueue) routeInputText(text string) chatInputRouteResult // 路由输入行
func (q *chatInputQueue) readPriorityLineWithPrompt(ctx, prompt) (string, error) // 优先级读取
func (q *chatInputQueue) capturePrompt(defaultPrompt string) (string, bool, uint64) // 捕获当前 prompt
```

现有模式（`startBusyQueuedInputCapture` in `chat_busy_input.go`）：
1. 挂起 `session.KeyHandler.Suspend()`
2. `queue.setExternalInputCaptureActive(true)`
3. 循环：`capturePrompt` → 读取 → `routeInputText(line)` 路由输入
4. 退出时恢复：`setExternalInputCaptureActive(false)` → `KeyHandler.Resume()`

**结论**：Web 客户端注入输入时，可复用此模式或更简单的单次 `routeInputText` 调用（如果会话已处于外部捕获模式）。对于非 busy 状态的会话，需要先 `setExternalInputCaptureActive(true)` 再 `routeInputText`。

### 2.6 屏幕内容获取（`chat_debug_screen_http.go`）

```go
func BuildChatDebugScreenSnapshot() *chatDebugScreenSnapshot  // 结构化快照
func BuildChatDebugScreenText() string                        // 纯文本摘要
func MarshalChatDebugScreenJSON() ([]byte, error)             // JSON 序列化
```

**结论**：直接复用，无需新实现。

---

## 3. 总体架构

```
┌─────────────────────────────────────────────────────────────┐
│  aicli 进程                                                  │
│                                                              │
│  ┌──────────────┐   ┌─────────────────┐   ┌──────────────┐  │
│  │ ChatSession  │   │ Loopback Server │   │ Agent        │  │
│  │  - InputQueue│◄──│  (127.0.0.1:*)  │   │  - EventBus  │  │
│  │  - Runtime   │   │                  │   │              │  │
│  │   EventBridge│   │  /web/           │   │  Publish     │  │
│  │              │   │  /web/api/screen │   │  turn events │  │
│  │  routeInput  │   │  /web/api/events │◄──│  (Subscribe) │  │
│  │  Text(text)  │   │  /web/api/input  │──►│              │  │
│  └──────────────┘   └─────────────────┘   └──────────────┘  │
│                            │ ▲                                │
│                           ▼ │                                │
│                      ┌──────────┐                            │
│                      │  SSE     │                            │
│                      │  Emitter │                            │
│                      └──────────┘                            │
└─────────────────────────────────────────────────────────────┘
         │                                        ▲
         │  GET /web/ (HTML)                      │ SSE events
         │  POST /web/api/input (prompt)          │ screen snapshot
         ▼                                        │
  ┌─────────────────────────────────────────────────┐
  │  Browser (Micro Web Client)                      │
  │  - EventSource → /web/api/events                 │
  │  - Screen display area                           │
  │  - Prompt input box + Send button                │
  └─────────────────────────────────────────────────┘
```

### 3.1 组件职责

| 组件 | 职责 |
| --- | --- |
| **Loopback Server** | 复用现有 `pprofHandle` 的 mux，注册 Web 客户端端点 |
| **SSE 订阅器** | 连接到 Agent 的 EventBus，订阅 turn 事件，转换为 SSE 格式 |
| **SSE 发射器** | 管理 SSE 连接生命周期（flusher、keep-alive、断开清理） |
| **输入注入器** | 接收前端 POST 的 prompt，通过 `chatInputQueue` 注入到会话 |
| **前端页面** | 内嵌 HTML + JS，使用 `EventSource` 接收 SSE 更新 |

### 3.2 模块边界

新端点代码位于 `backend/cmd/aicli/commands/` 包（与现有调试端点同包），保持一致性。pprof.go 中注册新端点。

可考虑提取 SSE 工具函数到 `backend/cmd/aicli/commands/` 内的独立文件（如 `web_sse.go`）或引用 `internal/api/skills/handler.go` 中的模式。

---

## 4. 端点设计

### 4.1 端点路径族

所有 Web 客户端端点使用 `/web/` 前缀，与现有 `/debug/` 调试端点族区分。

| 路径 | 方法 | 功能 | 参考实现 |
| --- | --- | --- | --- |
| `GET /web/` | GET | 返回微型 Web 客户端 HTML 页面 | 内嵌 HTML，`Content-Type: text/html` |
| `GET /web/api/screen` | GET | 返回当前屏幕快照 (JSON/text) | 复用 `MarshalChatDebugScreenJSON()` / `BuildChatDebugScreenText()` |
| `GET /web/api/status` | GET | 返回渲染器状态快照 (JSON/text) | 复用 `MarshalChatDebugDisplayJSON()` |
| `GET /web/api/events` | GET | SSE 事件流，推送 turn 事件 | 新实现，EventBus 订阅 + SSE 发射 |
| `POST /web/api/input` | POST | 注入用户 prompt 到会话 | 新实现，routeInputText |
| `GET /web/api/events/schema` | GET | 返回 SSE 事件类型定义文档 | 静态 JSON |

### 4.2 端点详细设计

#### 4.2.1 `GET /web/` — 微型 Web 客户端页面

- **功能**：返回完整的 HTML 页面，内嵌 CSS + JavaScript。
- **Content-Type**：`text/html; charset=utf-8`
- **页面布局**：
  - 顶部：连接状态指示器（已连接/已断开）
  - 中部：屏幕内容显示区（`<pre>` 渲染文本，或 JSON 格式化）
  - 下部：输入框 + 发送按钮
- **JS 行为**：
  - 页面加载后创建 `EventSource('/web/api/events')`
  - 收到 `screen_refresh` 事件 → 自动刷新屏幕内容
  - 收到 `turn_start`/`turn_delta`/`turn_end` 等事件 → 可更新状态指示器
  - 点击发送 → POST `/web/api/input` 发送 prompt
  - 连接断开自动重连（EventSource 原生支持）
- **内嵌资源**：全部内联，无外部依赖。

#### 4.2.2 `GET /web/api/screen` — 屏幕快照

- **查询参数**：
  - `?format=text`（默认）：纯文本面板内容
  - `?format=json`：结构化 JSON 快照
- **实现**：直接调用 `commands.BuildChatDebugScreenText()` 或 `commands.MarshalChatDebugScreenJSON()`
- **Content-Type**：`text/plain; charset=utf-8` 或 `application/json`

#### 4.2.3 `GET /web/api/events` — SSE 事件流

- **功能**：建立 SSE 长连接，实时推送 turn 事件。
- **Content-Type**：`text/event-stream`
- **实现逻辑**：
  1. 获取当前 `ChatSession`（通过 `chatDebugDisplaySession()` 或类似 provider）
  2. 获取 Agent 的 EventBus（通过 **`session.LocalRuntimeHost.EventBus`**，非 `RuntimeEventBridge`）
  3. 创建带 `sync.Mutex` 保护的 SSE 发射器（参考 `internal/api/skills/handler.go` 的 `newSSEEmitter` 模式，但必须**新增互斥锁**）
  4. 订阅 EventBus 相关事件类型，handler 中加锁后调用 `writeSSEEvent`
  5. 注册 `context.WithCancel` 或 `http.CloseNotifier`，连接断开时调用 `Unsubscribe()`
  6. 发送初始化状态事件（`connected` 事件包含 session_active、session_id、last_sequence 等）作为第一个事件
  7. 定期发送 SSE 注释行（`: keepalive`）维持连接
- **SSE 事件映射**（见第 5 节）

#### 4.2.4 `POST /web/api/input` — 注入输入

- **请求体**：`application/json` 或 `text/plain`
  - 普通 prompt：
    ```json
    {"prompt": "用户的输入内容"}
    ```
  - 审批/提问回答（反向交互，Phase 1 支持）：
    ```json
    {"type": "approval", "request_id": "req_123", "allow": true}
    {"type": "question_answer", "question_id": "q_456", "answer": "我的回答"}
    ```
  - `type` 缺省时按普通 prompt 处理；`type=approval` / `type=question_answer` 时跳过 `routeInputText`，改走审批/提问回答通道（见 6.5）。
- **实现逻辑**：
  1. 获取当前 `ChatSession`
  2. 根据 `type` 分派：普通 prompt → 步骤 3；审批/提问回答 → 步骤 6
  3. 获取 `InputQueue`（通过 `ensureChatBufferedInputQueue(session)`）
  4. 如果队列未处于外部捕获模式，调用 `setExternalInputCaptureActive(true)` 挂起 KeyHandler
  5. 调用 `queue.routeInputText(prompt)`，返回 `{"status": "queued"}` 或 `{"status": "rejected", "reason": "..."}`
  6. 审批/提问回答：查找 pending 的审批/提问请求（通过 `session.RuntimeEventBridge` 或会话状态），提交决议；返回 `{"status": "resolved"}` 或 `{"status": "not_found", "reason": "..."}`
- **并发安全**：使用互斥锁（`webInputMu`）防止多个 Web 客户端同时注入

#### 4.2.5 `GET /web/api/events/schema` — 事件模式文档

- **功能**：返回 SSE 事件类型定义的 JSON 文档，供前端开发者/调试用。
- **Content-Type**：`application/json`
- **响应结构**：JSON 数组，每项为一个事件定义：
  ```json
  [
    {
      "event": "turn_start",
      "description": "LLM 请求开始（turn 开始）",
      "source_event": "llm_request_started",
      "fields": [
        {"name": "turn_id", "type": "string", "description": "当前 turn 的标识"},
        {"name": "request_id", "type": "string", "description": "LLM 请求标识"},
        {"name": "model", "type": "string", "description": "使用的模型名称"},
        {"name": "timestamp", "type": "string (RFC3339)", "description": "事件时间戳"}
      ],
      "example": "{\"turn_id\":\"turn_abc\",\"request_id\":\"req_123\",\"model\":\"gpt-4\",\"timestamp\":\"2026-09-01T12:00:00Z\"}"
    }
  ]
  ```
- **schema_version**：所有事件 data 的 `_event.schema_version` 字段当前为 `"skill_runtime.sse.v1"`，schema 端点返回的版本与之一致。
- **维护方式**：静态 JSON 文件（手工维护，与事件映射表同步），或从代码的常量/类型描述自动生成（Phase 3 可选）。

#### 4.2.6 `GET /web/api/status` — 渲染器状态快照

- **功能**：返回当前渲染器状态快照（JSON/text），与 `/debug/chat/status` 相同数据源。
- **查询参数**：
  - `?format=json`（默认）：结构化 JSON 快照（`MarshalChatDebugDisplayJSON()`）
  - `?format=text`：纯文本摘要（`BuildChatDebugDisplayText()`）
- **响应结构**（JSON 模式）：
  ```json
  {
    "available": true,
    "captured_at": "2026-09-01T12:00:00Z",
    "session": {
      "session_id": "...",
      "transport": "stdio",
      "debug_mode": false
    },
    "routing": { "current_mode": "normal", "turns": 3 },
    "runtime": { "event_bus": { "subscriber_count": 2 }, "input_queue": { "mode": "idle" } }
  }
  ```
- **无数据时**：`{"available": false, "reason": "no active chat session"}`

### 4.3 HTTP 错误码与输入校验（总则）

所有端点遵循以下通用规则：

| 场景 | HTTP 状态码 | 响应体示例 |
| --- | --- | --- |
| 成功 | 200 | 按端点定义的 JSON 或文本 |
| 请求体非法 JSON | 400 | `{"error": "invalid json body", "detail": "parse error at line 1: ..."}` |
| 必填字段缺失/空值 | 400 | `{"error": "missing required field", "field": "prompt"}` |
| 查询参数非法 | 400 | `{"error": "invalid query parameter", "param": "format", "allowed": ["json", "text"]}` |
| 请求体过大（> 64KB） | 413 | `{"error": "request body too large", "max_bytes": 65536}` |
| 方法不允许 | 405 | `{"error": "method not allowed", "allowed": ["GET"]}` |
| Content-Type 不支持 | 415 | `{"error": "unsupported content type", "allowed": ["application/json", "text/plain"]}` |
| 无活跃会话 | 503 | `{"error": "no active session", "available": false}` |
| 注入超时 | 504 | `{"error": "injection timeout", "timeout_sec": 5}` |
| 内部错误 | 500 | `{"error": "internal server error"}` |

- **POST body 大小限制**：64KB（`http.MaxBytesReader`）。
- **输入超时**：`POST /web/api/input` 整体超时 5s（context.WithTimeout），超时后返回 504。
- **SSE 端点**：检查 `Accept: text/event-stream`，不匹配时返回 406（`{"error": "not acceptable", "detail": "requires Accept: text/event-stream"}`）。

---

## 5. SSE 事件流设计

### 5.1 EventBus → SSE 事件映射

| EventBus 事件 | SSE event 名称 | SSE data 字段 | 说明 |
| --- | --- | --- | --- |
| `session_start` | `session_start` | `{turn_id, session_id}` | 会话开始 |
| `session_end` | `session_end` | `{turn_id, session_id}` | 会话结束 |
| `llm_request_started` | `turn_start` | `{turn_id, request_id, model, timestamp}` | LLM 请求开始 |
| `assistant_delta` | `assistant_delta` | `{turn_id, stream_id, sequence, text}` | 流式文本增量 |
| `assistant.reasoning` | `reasoning_delta` | `{turn_id, stream_id, sequence, content}` | 推理增量 |
| `llm_request_finished` | `turn_end` | `{turn_id, request_id, finish_reason, usage}` | LLM 请求完成 |
| `tool_started` | `tool_start` | `{turn_id, tool_name, tool_call_id, arguments}` | 工具调用开始 |
| `tool_finished` | `tool_end` | `{turn_id, tool_name, tool_call_id, result_summary}` | 工具调用完成 |
| `approval_requested` | `approval_requested` | `{turn_id, request_id, tool_name, prompt}` | 审批请求 |
| `approval_resolved` | `approval_resolved` | `{turn_id, request_id, allowed}` | 审批已处理 |
| `question_asked` | `question_asked` | `{turn_id, question_id, prompt, suggestions}` | 询问用户 |
| `question_answered` | `question_answered` | `{turn_id, question_id, answer}` | 用户已回答 |
| `checkpoint_created` | `checkpoint_created` | `{turn_id, checkpoint_id}` | checkpoint |
| `session_compact_started` | `compact_start` | `{turn_id}` | 压缩开始 |
| `session_compact_completed` | `compact_end` | `{turn_id}` | 压缩完成 |
| `session_compact_skipped` | `compact_skipped` | `{turn_id}` | 压缩跳过 |
| `session_compact_failed` | `compact_failed` | `{turn_id, error}` | 压缩失败 |
| `job_started` | `job_start` | `{job_id, description}` | 后台任务 |
| `job_output` | `job_output` | `{job_id, text}` | 后台任务增量输出 |
| `job_finished` | `job_end` | `{job_id, success}` | 后台任务完成 |
| `job_cancelled` | `job_cancel` | `{job_id}` | 后台任务取消 |
| `session_interrupted` | `session_interrupted` | `{turn_id}` | 会话中断 |
| `assistant_message` | `assistant_message` | `{turn_id, content}` | Assistant 完整消息 |
| `assistant.image_progress` | `image_progress` | `{turn_id, progress}` | 图像生成进度 |
| `backtrack_started` | `backtrack_start` | `{turn_id}` | 回溯开始 |
| `backtrack_finished` | `backtrack_end` | `{turn_id}` | 回溯完成 |
| `rewind_finished` | `rewind_end` | `{turn_id}` | 回退完成 |

> **序列号**：每个 SSE 事件 data 的 `_event.sequence` 字段为连接内单调递增序列号（参考 `writeSSEEventWithEnvelope` 的 envelope 机制）。前端用它检测事件丢失并在重连时请求补偿。

### 5.2 特殊合成事件

除 EventBus 直接映射外，SSE 端点还合成以下事件：

| SSE event 名称 | 触发条件 | data 字段 | 目的 |
| --- | --- | --- | --- |
| `screen_refresh` | 仅用于方法一节流推送（见 8.6） | 屏幕文本快照或 diff | 驱动前端 UI 刷新 |
| `heartbeat` | 每 30s 无事件时 | `{timestamp, last_sequence}` | 保持连接存活 + 通知前端序列号状态 |
| `error` | 订阅/处理异常 | `{code, message, turn_id}` | 通知前端异常 |
| `connected` | 连接建立时 | `{session_active, session_id, session_busy, turn_id, last_sequence, server_version, pending_approval, pending_question}` | 初始状态同步 |

`connected` 事件中 `pending_approval` 与 `pending_question` 为可选字段，仅在断线重连时存在 pending 审批/提问请求时携带：

```json
{
  "session_active": true,
  "session_id": "sess_abc",
  "session_busy": true,
  "turn_id": "turn_123",
  "last_sequence": 42,
  "server_version": "aicli/1.0.0",
  "pending_approval": {
    "request_id": "req_xxx",
    "tool_name": "read_file",
    "prompt": "读取文件 /etc/passwd"
  },
  "pending_question": null
}
```

> **keep-alive 机制**：采用 SSE 注释行（`: keepalive`）每 30s 发送一次，保持 TCP 连接存活（负载均衡器/proxy 超时防护）。`heartbeat` 事件额外在 30s 无事件时发送，带 `last_sequence` 供前端验证序列连续性。两者分工：注释行对前端透明，`heartbeat` 事件触发前端状态校验。

### 5.3 SSE 连接生命周期

```
客户端 → 服务端: GET /web/api/events (Accept: text/event-stream)
服务端 → 客户端: HTTP 200
                 Content-Type: text/event-stream
                 Cache-Control: no-cache
                 Connection: keep-alive

服务端 → 客户端: event: connected
                 data: {"session_active":true,"session_id":"sess_abc","session_busy":false,"last_sequence":0,"server_version":"aicli/1.0.0"}

服务端 → 客户端: event: turn_start
                 data: {"turn_id":"...","model":"..."}

服务端 → 客户端: event: assistant_delta
                 data: {"turn_id":"...","text":"..."}

...

服务端 → 客户端: event: turn_end
                 data: {"turn_id":"..."}

服务端 → 客户端: event: screen_refresh （可选，方法一时推送）
                 data: {"text":"...当前屏幕内容..."}

                 :keepalive (每 30s)

客户端断开 / 服务端关闭
```

### 5.4 订阅实现要点

1. **EventBus 引用获取**：通过 `chatDebugDisplaySession()` 获取当前会话，通过 **`session.LocalRuntimeHost.EventBus`** 获取 EventBus 实例（`chat_actor_host.go:112`，`chat_runtime_events.go:547` 验证）。`RuntimeEventBridge` 是订阅者，**不是** EventBus 持有者。
2. **订阅过滤**：`SubscribeCancelable("", handler)` 订阅全部事件，在 handler 中按 `event.Type` 过滤。避免使用 eventType 精确匹配（可能遗漏未预定义或带别名的事件如 `assistant_reasoning` / `assistant.reasoning`）。
3. **并发安全**：参考实现 `sseEmitter`（`handler.go:8019`）**没有互斥锁**。新端点创建 SSE 发射器时必须包含 `sync.Mutex`，在 `Emit` 中加锁保护 `http.ResponseWriter.Write` 和 `sequence` 递增。EventBus.Publish 在锁外调用 handler，多个发布者并发时必须互斥。
4. **断开清理**：使用 `context.WithCancel`（推荐，替代废弃的 `http.CloseNotifier`），连接断开时 defer 调用 `Unsubscribe()` 取消 EventBus 订阅。
5. **初始快照**：连接建立后立即发送 `connected` 事件（含 `session_id`、`session_busy`、`last_sequence` 等），并发送 `screen_refresh` 事件携带当前屏幕快照。**若当前存在 pending 审批/提问请求，`connected` 事件携带 `pending_approval`/`pending_question` 字段**（从 `localActorRegistry` 或会话状态解析），保证断线重连后前端可恢复等待 UI。
6. **序列号与断线重连补偿**：每个 SSE 事件 data 的 `_event.sequence` 单调递增（`writeSSEEventWithEnvelope` 注入）。断线重连时：
   - 前端收到 `connected` 事件，比较 `last_sequence` 与本地记录的 `last_received_sequence`
   - 若缺口存在（`last_sequence > local_last_seq`），前端主动调用 `GET /web/api/screen` 拉取全量快照，重置状态
   - 无缺口则直接继续，无需额外操作
7. **keep-alive**：每 30s 发送 SSE 注释行（`: keepalive`）维持 TCP 连接。若 30s 内无事件，额外发送 `heartbeat` 事件供前端校验序列号。**keepalive 定时器必须在连接断开时停止**（使用与连接同源的 `context.WithCancel` 派生 ctx 驱动 `time.Ticker`，handler 退出时 defer `ticker.Stop()` + cancel，避免 goroutine 泄漏）。

## 6. 输入注入机制

### 6.1 注入流程

```
POST /web/api/input {"prompt": "列出当前目录"}
  │
  ├─ 1. 获取 ChatSession (chatDebugDisplaySession())
  ├─ 2. ensureChatBufferedInputQueue(session) → InputQueue
  ├─ 3. 加锁 (webInputMu)
  ├─ 4. 如果 KeyHandler 存在且未挂起:
  │     ├─ session.KeyHandler.Suspend()
  │     └─ queue.setExternalInputCaptureActive(true)
  ├─ 5. queue.routeInputText(prompt)
  │     ├─ 如果 prompt 以 / 开头 → 作为 slash 命令处理
  │     └─ 否则 → 作为普通消息排队
  ├─ 6. 如之前未捕获，恢复 KeyHandler
  ├─ 7. 解锁
  └─ 8. 返回 {"status": "queued"} 或 {"status": "rejected", "reason": "..."}
```

### 6.2 与现有 busy 输入捕获的兼容

现有 `startBusyQueuedInputCapture` 在 Agent 运行时挂起 KeyHandler 并开启外部捕获。Web 客户端注入需要：

- **场景 A：Agent 空闲（ready）** → 直接 `routeInputText`，无需挂起 KeyHandler（因为普通输入处理正常进行）
- **场景 B：Agent 运行中（busy）** → 如果已有 `startBusyQueuedInputCapture` 在运行（外部捕获已激活），则直接 `routeInputText` 即可；如果未激活，需先 `setExternalInputCaptureActive(true)` 再 `routeInputText`

**设计决策**：Web 客户端注入始终走 `setExternalInputCaptureActive(true)` → `routeInputText` → `setExternalInputCaptureActive(false)` 的完整路径，确保无论 Agent 处于何种状态都能注入。如果捕获已激活，重复设置 `true` 是幂等的。

### 6.3 并发控制

- 使用包级 `sync.Mutex`（`webInputMu`）保护注入流程
- 一次只允许一个注入请求执行
- 注入超时：5s（防止后端阻塞导致 HTTP 挂起）

### 6.4 输入回显

- 注入成功后，如果 `session.Interaction` 存在，调用 `RenderLocalSupplement` 显示注入提示（可选）
- 注入的 prompt 与普通终端输入一样，通过 `InputQueue` 进入会话流程
- 前端应有本地回显：输入框显示"已发送: xxx"，不依赖后端回显

### 6.5 审批/提问回答通道（反向交互）

审批请求（`approval_requested`）和提问（`question_asked`）是"前端需用户操作"的场景。`POST /web/api/input` 的 `type=approval` / `type=question_answer` 请求体走此通道，**不经过 `routeInputText`**。

- **审批回答**：
  - 接口：`SessionActor.ApproveTool(ctx, requestID, allow)`（`internal/chat/actor.go:403`）
  - 获取 actor：`session.LocalRuntimeHost.SessionHub.GetOrCreate(sessionID)`，或通过 `localActorRegistry.ResolveApproval`（`chat_actor_registry.go:2080`）
  - 请求体：`{"type":"approval", "request_id":"req_xxx", "allow":true}`
  - 响应：`{"status":"resolved"}` 或 `{"status":"not_found", "reason":"..."}`
- **提问回答**：
  - 接口：`SessionActor.AnswerQuestion(ctx, questionID, answer)`（`actor.go:440`）
  - 请求体：`{"type":"question_answer", "question_id":"q_xxx", "answer":"我的回答"}`
  - 响应：`{"status":"resolved"}` 或 `{"status":"not_found", "reason":"..."}`
- **安全**：同样在 `webInputMu` 互斥锁保护下执行，与普通注入互斥

---

## 7. 前端页面设计

### 7.1 页面结构

```html
<!DOCTYPE html>
<html>
<head>
  <title>aicli Web Client</title>
  <style>
    /* 深色终端风格，与 aicli TUI 一致 */
  </style>
</head>
<body>
  <div id="status-bar">
    <span id="connection-status" class="conn-disconnected">● 连接中...</span>
    <span id="session-status" class="session-idle">会话: 无</span>
    <span id="turn-status">就绪</span>
    <span id="error-status" class="error-hidden"></span>
  </div>
  <div id="screen">
    <pre id="screen-content">等待连接...</pre>
  </div>
  <div id="input-area">
    <input type="text" id="prompt-input" placeholder="输入 prompt 后按 Enter 发送..." />
    <button id="send-btn">发送</button>
    <span id="send-status" class="send-idle"></span>
  </div>
  <script>
    // 内联 JavaScript
  </script>
</body>
</html>
```

### 7.2 JS 行为

| 行为 | 触发条件 | 操作 |
| --- | --- | --- |
| 连接建立 | `EventSource` `onopen` | `connection-status` → "已连接" |
| 收到 `connected` | SSE event | 更新连接/会话状态（session_active、session_id、session_busy）；记录 `last_sequence`；**若为断线重连且有序列缺口 → 调用 GET `/web/api/screen` 全量刷新**；若无缺口 → 保持当前屏幕；**若 `pending_approval`/`pending_question` 非空 → 恢复对应等待 UI** |
| 收到 `screen_refresh` | SSE event（方法一） | 更新 `<pre>` 内容 |
| 收到 `session_start` | SSE event | 清空 turn 状态、重置为 `idle`；刷新屏幕（新会话界面） |
| 收到 `turn_start` | SSE event | `turn-status` → "LLM 请求中"；记录当前 turn_id |
| 收到 `reasoning_delta` | SSE event | 保持忙碌；可更新推理计数（可选） |
| 收到 `tool_start` | SSE event | `turn-status` → "工具执行中"（保持忙碌） |
| 收到 `tool_end` | SSE event | `turn-status` → "忙碌"（工具阶段完成）；**调用 GET `/web/api/screen` 刷新** |
| 收到 `assistant_delta` | SSE event | 更新增量计数（可选），保持忙碌 |
| 收到 `turn_end` | SSE event | `turn-status` → "就绪"；**调用 GET `/web/api/screen` 刷新** |
| 收到 `session_end` | SSE event | `session-status` → "已结束"；清空 turn 状态 |
| 收到 `session_interrupted` | SSE event | `turn-status` → "中断"；刷新屏幕 |
| 收到 `approval_requested` | SSE event | 显示审批弹窗（允许/拒绝按钮）；turn-status → "等待审批" |
| 收到 `approval_resolved` | SSE event | 关闭审批弹窗；turn-status 回到忙碌（等待后续 turn 事件）；刷新屏幕 |
| 收到 `question_asked` | SSE event | 显示提问输入框；turn-status → "等待回答" |
| 收到 `question_answered` | SSE event | 关闭提问输入框；turn-status 回到忙碌；刷新屏幕 |
| 收到 `error` | SSE event | `error-status` 显示错误信息；刷新屏幕（若可用） |
| 收到 `heartbeat` | SSE event | 校验 `last_sequence` 连续性；有缺口则刷新 |
| 连接断开 | `EventSource` `onerror` | `connection-status` → "已断开"；触发自动重连（EventSource 原生） |
| 重连成功 | `onopen` + `connected` | 重置连接状态；按 `connected.last_sequence` 判断是否需要全量刷新；重置 turn 状态为"就绪"（若会话非 busy）；**若 `connected.pending_approval`/`pending_question` 存在 → 恢复等待 UI 而非就绪** |
| 点击发送 | 按钮 click / Enter key | `send-status` → "发送中"；POST `/web/api/input` |
| 发送成功 | fetch 200 + `status: queued/resolved` | `send-status` → "已发送"; 清空输入框 |
| 发送失败 | fetch 非 200 / `status: rejected` | `send-status` → "失败"；**保留输入内容**，显示错误原因 |
| 收到其他事件（`compact_*`/`job_*`/`checkpoint_created`/`backtrack_*`/`rewind_end`/`assistant_message`/`image_progress`） | SSE event | **忽略**（不改变 UI 状态；仅可记录到事件日志面板） |

> **屏幕刷新策略（方法二，默认）**：前端收到 `turn_end`、`tool_end`、`session_end`、`session_interrupted`、`error` 等**关键事件**后主动调用 `GET /web/api/screen?format=text` 更新 `<pre>` 内容。`screen_refresh` SSE 事件仅在启用"方法一（服务端节流推送）"时使用（见 8.6）。

### 7.3 设计原则

- **极简**：一个页面，零外部依赖，内联样式和脚本
- **终端风格**：深色背景、等宽字体，与 aicli TUI 视觉一致
- **响应式**：基本适配窗口大小调整
- **可访问性**：发请求后显示加载状态，错误可见

### 7.4 Turn 状态机（前端）

前端维护一个明确的状态机，`turn-status` 指示器反映当前状态：

```
idle ──(turn_start)──▶ llm_requesting ──(assistant_delta)──▶ streaming
  ▲                          │                                   │
  │                          ▼                                   ▼
  │                   tool_executing ◀──(tool_start)── ... ──(tool_end)──▶ busy
  │                          │
  │                          ├─(approval_requested)──▶ waiting_approval
  │                          ├─(question_asked)──────▶ waiting_answer
  │                          └─(session_interrupted)──▶ interrupted
  │
  └──(turn_end / session_end / reconnected)──◀────────────────────┘
```

| 状态 | 进入条件 | 退出条件 |
| --- | --- | --- |
| `idle` | 初始 / `turn_end` / 重连成功且非 busy | `turn_start` |
| `llm_requesting` | `turn_start` | `assistant_delta` / `tool_start` / `turn_end` |
| `streaming` | `assistant_delta` / `reasoning_delta` | `turn_end` / `tool_start` / `error` |
| `tool_executing` | `tool_start` | `tool_end` / `turn_end` / `error` |
| `waiting_approval` | `approval_requested` | `approval_resolved` / `session_interrupted` |
| `waiting_answer` | `question_asked` | `question_answered` / `session_interrupted` |
| `interrupted` | `session_interrupted` | `turn_start` / 重连重置 |
| `error` | `error` 事件 | `turn_start` / 重连重置 |

> 前端必须**在任何状态下保持忙碌**直到收到 `turn_end`，不能因中间 `tool_start`/`approval_requested` 等事件错误地回落为"就绪"。

## 8. 关键边界与风险

### 8.1 会话不存在或未初始化

**风险**：`chatDebugDisplaySession()` 返回 nil 的场景（无活跃会话）。

**处理**：所有端点首先检查 session 是否为空，返回 HTTP 503 + `{"error": "no active session", "available": false}`（见 4.3）。SSE 连接在 `connected` 事件中设置 `session_active: false`，前端显示"无活跃会话"。

### 8.2 多个 SSE 连接

**风险**：用户打开多个浏览器标签页，建立多个 SSE 连接。

**处理**：每个 SSE 连接独立订阅 EventBus，独立断开清理。EventBus 的 handler 在 Publish 内同步调用（`bus.go:324-329`），多个订阅者会依次执行写+Flush。**性能风险**：高频 `assistant_delta` 下，N 个连接对每个事件产生 N 次写+Flush，增加发布者延迟。缓解：Phase 1 限制连接数上限（如 8）；Phase 2/3 引入**异步缓冲队列**（事件先入 chan，SSE goroutine 从 chan 读取后写入，发布者不阻塞）。

### 8.3 输入注入竞态

**风险**：Web 客户端注入与终端交互式输入同时发生。

**处理**：
- `webInputMu` 互斥锁确保一次一个注入操作
- `setExternalInputCaptureActive(true)` 会挂起 KeyHandler，防止终端输入与 Web 注入混淆
- 注入完成后恢复 KeyHandler
- 审批/提问回答通道同样受 `webInputMu` 保护，避免与普通注入交错

### 8.4 SSE 连接断开后清理

**风险**：客户端断开后，EventBus 订阅未取消，导致内存泄漏。

**处理**：使用 `context.WithCancel`（`http.CloseNotifier` 已废弃）监听连接关闭，在 defer 中调用 `Unsubscribe()` 取消 EventBus 订阅。**同时停止 keepalive 定时器 goroutine**（`ticker.Stop()` + 关闭派生 ctx），避免 goroutine 泄漏。EventBus handler 若持有连接引用，随 `Unsubscribe` 一并释放。

### 8.5 EventBus 引用生命周期

**风险**：Agent 在会话过程中可能重新创建（如 `/session switch`），EventBus 引用失效。

**处理**：EventBus 实例位于 `session.LocalRuntimeHost.EventBus`（host 级，`chat_actor_host.go:112`）。SSE handler 每次处理事件时**重新调用 `chatDebugDisplaySession()`** 解析当前 session 并对比其 `LocalRuntimeHost.EventBus`；若实例变化（session 切换），重新 `SubscribeCancelable` 并取消旧订阅。这样无需在 `session_start` 事件中额外处理，天然跟随当前会话。

### 8.6 屏幕内容刷新频率

**风险**：每次事件后都推送全量屏幕快照，高频事件（如 `assistant_delta`）可能造成 SSE 大量数据。

**处理**：
- 方法一：**节流**（throttle）—— 每 200ms 最多推送一次 `screen_refresh`
- 方法二：**仅事件**—— 不推送 `screen_refresh`，前端收到事件后自行决定是否刷新（通过 `GET /web/api/screen`）
- 方法三：**增量 diff**—— 推送屏差分（复杂度高，初版不建议）

**推荐**：初版采用方法二，前端在收到 `turn_end`、`tool_end`、`session_end` 等关键事件后主动刷新。后续可升级为方法一（节流推送）。

---

## 9. 实现路线图

### Phase 1：核心端点（SSE + 输入注入 + 反向交互）

| 步骤 | 文件 | 工作内容 |
| --- | --- | --- |
| 1.1 | `backend/cmd/aicli/commands/web_sse.go` | 新建带 `sync.Mutex` 的 SSE 发射器、通过 `session.LocalRuntimeHost.EventBus` 订阅、事件映射、序列号注入、keep-alive（含 ticker 清理）、`connected` 事件携带 pending 状态 |
| 1.2 | `backend/cmd/aicli/commands/web_input.go` | 输入注入 handler：`setExternalInputCaptureActive` → `routeInputText` → 恢复；审批/提问回答通道（`ApproveTool`/`AnswerQuestion`）；`webInputMu` 互斥；body 大小限制（64KB）+ 5s 超时 |
| 1.3 | `backend/cmd/aicli/pprof.go` | 在 mux 中注册 `/web/`、`/web/api/screen`、`/web/api/status`、`/web/api/events`、`/web/api/input`、`/web/api/events/schema` |
| 1.4 | `backend/cmd/aicli/commands/web_handler.go` | `GET /web/` 返回内嵌 HTML 页面（含状态机、重连补偿、pending 恢复 UI 逻辑） |
| 1.5 | `backend/cmd/aicli/commands/web_schema.go` | `GET /web/api/events/schema` 静态事件类型 JSON |
| 1.6 | 测试 | 端到端测试：SSE 连接收到事件、POST input 成功、审批/提问回答、断线重连补偿（含 pending 恢复）、错误码（400/405/406/413/503/504）、keepalive、`-race` 无竞态 |

### Phase 2：前端页面优化

| 步骤 | 内容 |
| --- | --- |
| 2.1 | 屏幕内容显示区域（终端样式） |
| 2.2 | 输入框 + 发送按钮 + Enter 快捷键 + 发送状态指示 |
| 2.3 | 连接/会话/turn/错误状态指示器 + 自动重连 + 序列号补偿 |
| 2.4 | 审批/提问弹窗交互（前端 UI 完善） |
| 2.5 | 事件日志面板（可选） |

### Phase 3：增强（可选）

| 步骤 | 内容 |
| --- | --- |
| 3.1 | 屏幕内容增量 diff（减少 SSE 传输量） |
| 3.2 | 多会话支持（session 切换时 EventBus 订阅跟随，见 8.5） |
| 3.3 | 异步缓冲队列（缓解多连接同步写 + Flush 的性能压力） |

---

## 10. 验收标准

| # | 验收项 | 验证方式 |
| --- | --- | --- |
| 1 | `GET /web/` 返回 HTML 页面，状态码 200 | curl / 浏览器 |
| 2 | `GET /web/api/screen` 返回当前屏幕内容，JSON 和 text 格式均正常 | curl |
| 3 | `GET /web/api/events` 建立 SSE 连接，初始收到 `connected` 事件（含 session_id、last_sequence） | curl |
| 4 | 执行 aicli 命令（如 `/ask`）时，SSE 收到 `turn_start` → `assistant_delta` → `turn_end` 事件序列，且每个事件带递增 `_event.sequence` | curl 观察 |
| 5 | `POST /web/api/input {"prompt":"hello"}` 返回 `{"status":"queued"}`，且输入注入到会话 | curl + 终端观察 |
| 6 | `POST /web/api/input {"type":"approval",...}` 能解析审批请求，返回 `{"status":"resolved"}` | curl + 终端观察 |
| 7 | `POST /web/api/input {"type":"question_answer",...}` 能回答提问，返回 `{"status":"resolved"}` | curl + 终端观察 |
| 8 | 多个 SSE 连接同时建立，各自独立接收事件 | 两个浏览器标签页 |
| 9 | SSE 连接断开后，EventBus 订阅正确取消 | 内存泄漏检测 |
| 10 | 断线重连后，前端收到 `connected.last_sequence` 与本地序列对比，有缺口时拉取全量快照 | 浏览器 devtools 观察网络请求 |
| 11 | 断线重连时若存在 pending 审批/提问，`connected` 事件携带 `pending_approval`/`pending_question`，前端恢复等待 UI | 浏览器 devtools 观察 SSE data |
| 12 | 无活跃会话时，端点和 SSE 返回 `session_active: false`（HTTP 503） | curl |
| 13 | `GET /web/api/status` 返回 JSON/text 快照，无会话时返回 503 | curl |
| 14 | `GET /web/api/events/schema` 返回事件类型 JSON 数组，含字段与示例 | curl + jq |
| 15 | 非法请求体/非法 JSON → 400；超大 body（>64KB）→ 413；错误方法 → 405；SSE 无 `Accept: text/event-stream` → 406 | curl |
| 16 | SSE 连接在 30s 无事件时收到 `heartbeat` 与 `: keepalive`，连接保持存活 | curl 观察 / 浏览器 |
| 17 | 所有测试通过：`go test ./cmd/aicli/...` | 测试运行 |
| 18 | 无竞态问题：`go test -race ./cmd/aicli/...` | 测试运行 |

---

## 11. 附录

### A. 参考文件清单

| 文件 | 用途 |
| --- | --- |
| `backend/cmd/aicli/pprof.go` | Loopback HTTP 服务器，端点注册入口 |
| `backend/cmd/aicli/commands/chat_debug_screen_http.go` | 屏幕快照构建函数 |
| `backend/cmd/aicli/commands/chat_debug_display_http.go` | 渲染器状态快照构建函数 |
| `backend/cmd/aicli/commands/chat_input_queue.go` | 输入队列，外部捕获/路由输入 |
| `backend/cmd/aicli/commands/chat_busy_input.go` | 现有外部输入捕获模式 |
| `backend/cmd/aicli/commands/chat_runtime_events.go` | RuntimeEventBridge，EventBus 订阅参考 |
| `backend/internal/events/bus.go` | EventBus 订阅 API |
| `backend/internal/chat/events.go` | Turn 事件类型定义 |
| `backend/internal/api/skills/handler.go` | SSE 工具函数参考 |
| `backend/cmd/aicli/commands/web_sse.go` | （新建）SSE 发射器 + EventBus 订阅 |
| `backend/cmd/aicli/commands/web_input.go` | （新建）输入注入 handler |
| `backend/cmd/aicli/commands/web_handler.go` | （新建）HTML 页面 + 状态机逻辑 |
| `backend/cmd/aicli/commands/web_schema.go` | （新建）事件 schema JSON |

### B. 与现有调试端点的关系

| 维度 | 现有 `/debug/chat/` | 新增 `/web/` |
| --- | --- | --- |
| 用途 | 调试诊断（静态快照） | 微型 Web 客户端（实时双向交互） |
| 数据方向 | 只读（GET 快照） | 双向（GET 快照 + SSE 推送 + POST 输入） |
| 前端 | 无（curl/浏览器直接看 JSON） | 内嵌 HTML 页面 |
| 复用 | — | 复用 `chat_debug_*` 的构建函数 |

### C. 待确认事项

1. ~~**EventBus 引用获取方式**~~ ✅ **已确认**：通过 `session.LocalRuntimeHost.EventBus`（`chat_actor_host.go:112`），无需新增访问器。`RuntimeEventBridge` 是订阅者，不能用于获取 EventBus。
2. ~~**SSE 工具函数归属**~~ ✅ **已确认**：`commands` 内独立实现（带互斥锁），暂不跨包，避免改动 `internal/api/skills`。新增 `web_sse.go`。
3. ~~**会话 provider 生命周期**~~ ✅ **已确认**：`chatDebugDisplaySession()` 通过 `chatDebugDisplaySessionProvider` 返回当前会话，Session 创建时注册、结束时注销（`chat_debug_display_http.go:19-37`）。SSE handler 每次事件处理时重新解析即可跟随 session 切换。
4. **输入注入对交互式会话的影响**：注入期间 KeyHandler 挂起是否会导致终端 UI 闪烁或输入丢失，需要端到端验证。
5. **审批/提问回答通道的桥接方式**：`POST /web/api/input` 的 `type=approval` / `type=question_answer` 走 `SessionActor.ApproveTool`/`AnswerQuestion`（`internal/chat/actor.go:403/440`），需确认 Web 端点到 SessionActor 的桥接（通过 `session.LocalRuntimeHost.SessionHub.GetOrCreate` 或 `localActorRegistry`）。
6. **pending 审批/提问状态的解析来源**：`connected` 事件携带 `pending_approval`/`pending_question`，需确认从何处解析当前 pending 请求（`localActorRegistry` 的 pending 记录 vs `session.RuntimeSession` 状态）。

---
