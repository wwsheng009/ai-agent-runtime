# Runtime 运行时监管与观测 HTTP API 方案

> 状态：方案分析（未实施）  
> 日期：2026-08-20  
> 适用范围：`runtime-server`、Agent/Session runtime、LLM gateway/provider、AICLI 语义渲染层  
> 关键词：运行时监管、实时观测、LLM metadata、渲染快照、SSE、脱敏、背压

## 1. 摘要与结论

当前项目已经具备多种运行时信息来源：runtime event bus、SessionActor 的状态与持久化事件、LLM request/retry/stream reporter、usage/trace/analytics read model，以及 AICLI 的 `TuiScene`/`AppState`/`AppRenderFrame` 语义渲染模型。但这些来源的职责、保留时间、序列空间和敏感性不同，不能把现有对象直接 JSON 暴露给 LLM 或远程客户端。

本方案建议新增一个独立的 **Runtime Observation Plane（运行时观测/监管平面）**，只提供只读、低敏、版本化的业务语义观测，不替代 pprof，也不承担控制操作：

1. **复用 runtime-server 主 HTTP 服务和现有 admin authorization**，在 `/api/runtime/observe/v1` 下增加独立命名空间；不把业务数据挂到 `/debug/pprof`。
2. **以投影层为边界**：从现有 bus/store/reporter/renderer adapter 读取数据，经过 allowlist、脱敏、截断、类型校验后，才进入观测 read model 和 HTTP 响应。
3. **同时提供一致性快照、有限历史查询和 SSE 实时流**。SSE 首先发送快照，再发送有序事件；使用独立的 observation sequence/cursor，并明确与 session durable seq、Scene revision、runtime state revision 的区别。
4. **实时链路必须有界且不阻塞业务发布者**：bus 回调只做轻量入队；投影、聚合和每客户端 fan-out 使用有界队列；流式高频事件合并/降采样，慢客户端通过 `gap`/`resync_required` 重新获取快照。
5. **默认不发送 prompt、assistant 输出、reasoning、工具参数、工具结果、完整 HTTP body、Authorization/API key、Cookie 或绝对路径**。默认只发送计数、长度、token、hash/HMAC、状态、耗时、错误分类和关联 ID。
6. **渲染观测导出语义模型而非终端画面**：不读取 ANSI、front/back buffer、cursor cache；同进程使用 renderer adapter，跨进程由 AICLI 主动发布快照，不能跨进程直接读 UI 内存。

第一交付切片应先覆盖 runtime/session/LLM 元数据和 event query，再加入 SSE；renderer adapter 在确认 AICLI 与 runtime-server 部署关系后落地。功能默认可关闭，逐阶段灰度，任何阶段都可回退到现有接口和日志。

## 2. 背景、问题与目标

### 2.1 当前问题

- pprof 能回答进程 CPU、heap、goroutine、mutex、trace 等问题，但不能回答“哪个 session 正在运行哪一轮 Agent”“哪个 provider 的哪次 retry 失败”“渲染 Scene 是否落后”等业务语义问题。
- `/api/runtime/events`、session runtime events、trajectory SSE、runtime logs、analytics 等接口各有 envelope、序列和保留策略，客户端难以统一消费。
- `runtimeevents.Bus.Publish` 会同步调用订阅 handler。SSE handler 若直接在订阅回调中写网络，会把慢客户端延迟传导到 Agent/LLM/tool 发布线程。
- `Event.Payload`、`HTTPDebugEvent`、chat log 和 provider response 可能包含 prompt、工具定义/参数、响应正文、URL、header、原始错误和文件路径，不能原样导出。
- AICLI 的渲染状态在语义 Scene/AppState 中，但 runtime-server 可能是另一个进程；读取终端物理投影既不稳定，也会违反状态所有权。

### 2.2 目标

- 让受信任的 LLM/operator/自动化诊断客户端能通过 HTTP **实时且可恢复地**获取：
  - 进程和 runtime 总体状态；
  - session/agent/turn/tool 生命周期；
  - LLM logical request、provider attempt、retry、stream、usage 元数据；
  - renderer semantic snapshot/revision（可用时）；
  - 低敏事件历史和实时变化。
- 明确数据所有权、快照一致性、事件序列、丢失检测、断线重连和 backpressure。
- 复用现有服务生命周期、路由、鉴权和数据源，避免重复埋点及第二套业务 server。
- 默认关闭高敏内容导出，并让新增观测能力对现有运行链路的延迟、内存和故障面可测量、可限制。

### 2.3 非目标

- 不提供 pause/resume/cancel/retry/approve、修改配置、执行工具、写文件、重启服务等控制能力。控制面继续使用现有 `/api/runtime/supervision`、service、agent-control 等接口，并保持独立鉴权和审计。
- 不将 pprof、runtime/metrics、expvar、日志全文或原始 HTTP artifact 变成业务观测 API 的默认数据源/响应。
- 不在 v1 还原完整终端屏幕、不提供 ANSI、不把 prompt/response/reasoning 原文作为普通监管数据。
- 不承诺跨多个独立持久化 store 的线性一致事务；响应必须标注每个数据域的 revision、时间和 partial/staleness 状态。
- 不让浏览器/LLM 客户端直接访问内存 bus、SQLite、chat log 或 artifact 目录。

## 3. 现状审计与可复用能力

### 3.1 HTTP 与 pprof 边界

当前存在两套 HTTP 体系：

| 体系 | 当前实现 | 结论 |
| --- | --- | --- |
| AICLI pprof | `backend/cmd/aicli/pprof.go`；`--pprof` 或 `AICLI_PPROF` 开启；默认 `127.0.0.1:0`；独立 `http.Server`/`ServeMux`；注册 `/debug/pprof/*` | 只负责 Go 进程 profiling；当前无认证/TLS/访问审计，不能承载业务监管数据 |
| runtime-server API | `backend/cmd/runtime-server/main.go`；监听地址来自 `server.host/server.port` 或 `--listen`，默认部署为 loopback；主路由由 `internal/api/skills/handler.go` 的 Gorilla mux 注册 | 适合挂载版本化、鉴权后的业务观测接口；与 pprof 生命周期一并由主 server 优雅关闭 |

AICLI pprof 在 `main.go` 的 `PersistentPreRunE` 中按需启动，命令退出后 close；`Serve` 错误目前被丢弃，且自定义 `AICLI_PPROF` 地址没有强制 loopback 校验。审计还发现配置文件中的 `monitor.pprof` 字段不是当前有效 Go 配置模型，不能直接假定它已启用或可安全使用。因此本方案不复用该监听器，也不把观测数据塞进 `/debug/pprof`；若未来加远程 pprof，必须另行完成 loopback/TLS/auth/采样限制设计。

### 3.2 现有 runtime 路由与数据源

`backend/internal/api/skills/handler.go:RegisterRoutes` 已在 `canonicalRuntimeEntrypoint` 下注册：

- `/status`、`/health`、`/events`、`/logs`、`/logs/stream`；
- `/traces`、`/traces/{trace_id}`、`/traces/stats`、`/analytics/*`；
- `/sessions/{id}/runtime`、`events`、`stream`、`tools`、`tool-receipts`；
- `/supervision/*` 控制/查询接口。

Handler 已持有 `LLMRuntime`、runtime event bus、SessionManager、RuntimeStateStore、EventStore、SessionHub 及相关 service/store。因此优先在同一 Handler/依赖注入层接入一个独立的 `ObservationService`，而不是新建第二个业务 server。新接口即使复用底层数据，也必须经过新的低敏 projection，不改变现有 `/events` 的兼容语义。

### 3.3 事件与 Session 状态

- `internal/events.Bus` 支持 Publish、Subscribe、Cancelable subscription、retention、byte limit、Query 和 trace summary；但 Publish 同步调用 handler。
- `RuntimeStateStore` 提供 `LoadState/SaveState/DeleteState`；`EventStore` 提供 `AppendEvent/ListEvents`。
- `EventWatcherStore.WatchEvents` 只适合作为唤醒通知，调用方仍必须以 `after_seq` 从 durable store catch-up；`EventSequenceStore.LastEventSeq` 是 session 局部 durable 高水位。
- `SessionActorConfig` 将 Agent、LLMRuntime、StateStore、EventStore、EventBus 接在一起；SessionActor 串行 command loop，是单 session runtime state 的权威写入者。
- 内存 store 已有约 2048 条/按字节上限的 ring retention；不能把其 retention index 当成全局可恢复 cursor。

### 3.4 LLM 事件与元数据

可复用的低层能力包括：

- `internal/llm/http_debug.go` 的 `HTTPDebugEvent`：provider/protocol/model/attempt、logical turn、LLM request、retry attempt、provider request、stream correlation，以及 request/response metadata/body/status/error；其中 body/raw/body preview 必须严格屏蔽。
- `internal/llm/retry_events.go` 的 `RetryEvent`：attempt、max attempts、retry reason、error code、delay 和 correlation ID。
- `internal/llm/stream_reporter.go` 的 `StreamReporter`：stream chunk 回调；监管端只应发布统计或节流摘要，不应复制全文 delta。
- Agent loop 已产生 `llm.request.started`、stream/reasoning/image、retry 等 runtime events；skills agent chat 也产生 request started/finished 和 trajectory SSE。
- 现有 analytics 设计已经证明 `session_end` 可作为一次 Agent turn 的累计 usage 来源，而逐 `llm.request.finished` 适合 request 明细；两者不能重复相加。

LLM 审计结论是：底层采集能力足够，缺少的是统一 logical request → attempt → stream → usage 的监管协议，以及集中、强类型、默认安全的投影层。

### 3.5 渲染状态

AICLI 已有以下语义模型：

- `TuiScene/Snapshot/TranscriptCell`：Scene 唯一权威 transcript 语义状态，copy-on-write snapshot；有 SceneID、revision、content version、cell kind/phase/sequence。
- `AppState`：不可变布局输入，含 Transcript、ActiveCell、Bottom、Geometry、Lease、LayoutGeneration 等。
- `AppRenderFrame`：从 AppState 纯派生的结构化 frame；不应包含 ANSI、终端 cache 或 cursor 物理状态。
- `chatRuntimeEventBridge` 已有 bounded queue、stream coalescing、render mutex、scene mapper 和 epoch/order 机制，可作为观测采样和背压策略的参考，但不能把其内部锁/队列直接暴露给 HTTP。

renderer 观测必须通过明确的 `RendererObservationSource`/publisher 导出语义快照。若 AICLI 与 runtime-server 跨进程，runtime-server 只能消费 AICLI 发布的脱敏快照/事件；“按 session ID 读取另一个进程的 UI 内存”不是可接受方案。

## 4. 总体架构

### 4.1 组件分层

建议新增包（最终命名可在实现阶段按仓库习惯调整）：

```text
internal/runtimeobserve/
  config.go              # 启用、限额、保留、采样、脱敏配置
  model.go               # versioned snapshot/event/cursor 类型
  collector.go           # 轻量 ingress、序列、聚合和 ring retention
  projector.go           # event/state/LLM/renderer allowlist projection
  redaction.go           # 递归字段策略、HMAC、URL/path/header scrub
  subscription.go        # per-client bounded queue、coalesce、gap
  renderer.go            # RendererObservationSource 接口和快照 adapter
  service.go              # query snapshot/events、订阅生命周期
```

在 `internal/api/skills` 中新增 handler 文件，仅负责：认证、参数解析、调用 `ObservationService`、设置响应/错误，不直接操作 bus/store 的原始 payload。Handler 初始化时注入：

```text
ObservationService
  ├─ RuntimeSnapshotSource       # status/health、进程摘要、session manager
  ├─ SessionRuntimeSource        # StateStore、EventStore、Watcher、SequenceStore
  ├─ RuntimeEventSource          # Bus（仅通过轻量 ingress）
  ├─ LLMMetadataSource           # request/retry/stream reporter 的投影入口
  ├─ RendererObservationSource   # 可选，同进程 adapter 或跨进程 publisher
  └─ Redactor + bounded retention/subscriber registry
```

### 4.2 采集路径

1. 业务代码继续发布现有 runtime event；不在每个 HTTP handler 中重新轮询 LLM provider。
2. Observation collector 对 bus 做一个全局订阅。Bus handler 只分配单调 `event_seq`、记录接收时间，并以 `select { case ingress <- item: default: markDropped() }` 非阻塞入队；不得执行 JSON 编码、磁盘 I/O、网络写入或等待锁的长操作。
3. collector goroutine 从 ingress 中读取，按事件类型做白名单投影、脱敏、大小限制和聚合，然后写入有界 observation ring，并更新 runtime/session/LLM aggregate read model。
4. durable session events 通过 `EventStore` 为查询和断线 catch-up 提供权威事实；collector 的内存 ring 只作为低延迟全局流缓存。若同一事实同时从 bus 和 durable store 到达，使用 `(source, session_id, durable_seq, event_type, correlation_id)` 去重，不因重放重复计数。
5. 每个 SSE 客户端拥有独立有界 queue。collector 不直接写客户端；慢客户端由 delta 合并、低优先级丢弃、gap 记录和 resync 机制处理。
6. `ObservationService` 的快照由只读 read model 组成，并带组件 revision、生成时间、freshness/partial 标记；不在 HTTP 请求中持有 SessionActor 锁或执行昂贵的全量历史聚合。

### 4.3 三类序列必须分开

| 名称 | 作用 | 所有者 | 是否可用作恢复 cursor |
| --- | --- | --- | --- |
| `observation_seq` | 观测平面接受/发布的全局有序序列；只对投影协议有效 | Observation collector | 是，但只在 retention 范围内 |
| `session_event_seq` | 某 session durable EventStore 的局部序列 | Session EventStore | 是，配合 session + `after_seq` |
| `scene_revision` / `content_version` | Scene 内容变更版本 | AICLI Scene | 否；用于判断 renderer snapshot 是否过期 |
| `runtime_state_revision` | SessionActor runtime state 变更版本 | RuntimeStateStore/Actor | 否；用于快照组件一致性 |
| `snapshot_revision` | 观测 read model 生成的复合快照版本 | ObservationService | 用于 ETag/比较，不替代事件 cursor |

响应必须同时返回实际使用的序列和 `source`。禁止将 Bus ring 下标、SSE 连接内计数、session durable seq 互相冒充。

### 4.4 数据所有权与一致性

- Agent/Session 执行状态以 SessionActor/RuntimeStateStore 为准；观测 read model 是只读投影，不能反向修改 actor。
- LLM request/attempt 以 request/retry reporter 与结构化 runtime event 为准；`session_end.usage` 是 turn 累计汇总，不与 request 明细重复求和。
- renderer 以 Scene/AppState snapshot 为准；display rows 是派生投影，不是事实源。
- process counters 通过低频、短耗时采样取得；pprof profile 仍由 pprof endpoint 单独提供。
- 若组件在不同时间读取，响应示例中明确：

```json
{
  "snapshot_revision": 1842,
  "generated_at": "2026-08-20T08:30:10.120Z",
  "consistency": "component_consistent",
  "partial": false,
  "components": {
    "runtime": {"revision": 712, "captured_at": "..."},
    "sessions": {"revision": 981, "captured_at": "..."},
    "renderer": {"revision": 44, "captured_at": "..."}
  }
}
```

如果 renderer publisher 未连接或状态跨越两个 revision，返回 `partial: true`、`stale_components` 和 `warnings`，而不是伪装成全局原子快照。

## 5. 版本化 HTTP API

### 5.1 路由选择

首选新增：

```text
GET /api/runtime/observe/v1/capabilities
GET /api/runtime/observe/v1/snapshot
GET /api/runtime/observe/v1/sessions/{session_id}
GET /api/runtime/observe/v1/events
GET /api/runtime/observe/v1/stream
GET /api/runtime/observe/v1/renderers/{renderer_id}
```

在 `RegisterRoutes` 的 runtime subrouter 下注册，所有接口均为 GET（SSE 也是 GET）。建议同时保留一个受控内部注册入口供 AICLI publisher 使用，但不要把 publisher 写入能力暴露给普通观测 token；跨进程 push 的认证、签名和生命周期应另立协议。

为什么不用现有路径：

- `/api/runtime/events` 继续保持现有原始/兼容查询语义，直接改变 payload 会破坏已有客户端。
- `/api/runtime/supervision` 已承载 supervision control plane，混入观测会模糊“读”和“写”的安全边界。
- `/api/runtime/traces` 和 `/analytics` 是查询/聚合域，不包含实时 renderer snapshot 和统一 cursor。
- `/debug/pprof` 是 Go profiler namespace，不能暴露 prompt、session 或 provider metadata。
- `v1` 使 envelope、字段和脱敏承诺可演进；新字段只向后兼容添加，语义破坏时升版本。

### 5.2 统一响应 envelope

普通 JSON 响应统一为：

```json
{
  "ok": true,
  "schema_version": "runtime.observe.v1",
  "request_id": "req_01J...",
  "data": {},
  "warnings": [],
  "redaction": {
    "profile": "safe_default",
    "omitted_fields": ["prompt", "tool_arguments", "provider_http_body"]
  }
}
```

错误响应至少包含 `error.code`、`error.message`（不带原始 provider body）、`retryable`（若适用）和 `request_id`；不要把 Go error string 原样返回。

所有 list/query 响应包含 `next_cursor` 或明确为空、`oldest_available_seq`、`latest_seq`、`partial`。默认/最大 limit 必须由服务端强制，例如默认 50、最大 200，具体值配置化但不得由客户端无限放大。

### 5.3 `/snapshot`

推荐返回：

```json
{
  "snapshot_revision": 1842,
  "captured_at": "2026-08-20T08:30:10.120Z",
  "freshness_ms": 18,
  "process": {
    "instance_id": "runtime-7f2c",
    "pid": 4216,
    "uptime_ms": 3820012,
    "goroutines": 87,
    "heap_bytes": 73400320,
    "observation_enabled": true
  },
  "runtime": {
    "active_sessions": 2,
    "running_turns": 1,
    "active_llm_requests": 1,
    "active_tools": 0,
    "pending_approvals": 0,
    "event_ingress_dropped": 0,
    "last_event_at": "2026-08-20T08:30:09.980Z"
  },
  "llm": {
    "requests_total": 132,
    "requests_in_flight": 1,
    "retries_total": 4,
    "stream_count": 96,
    "usage": {"prompt_tokens": 12040, "completion_tokens": 3190, "total_tokens": 15230},
    "by_provider": {"provider_a": {"requests": 80, "errors": 2}}
  },
  "sessions": {
    "items": [
      {
        "session_id": "session_123",
        "state": "running",
        "turn_id": "turn_9",
        "trace_id": "trace_abc",
        "runtime_state_revision": 72,
        "last_event_seq": 418,
        "active_request_id": "llm_req_42",
        "renderer": {"renderer_id": "aicli-1", "scene_revision": 301, "fresh": true}
      }
    ],
    "count": 1
  },
  "cursor": {"observation_seq": 9912, "oldest_available_seq": 9701}
}
```

`pid`、instance ID、provider/model 名称是否对外暴露需由部署策略决定；至少不能暴露环境变量、主机名、绝对路径或 API key。process summary 是低成本摘要，不能替代 pprof profile。

### 5.4 `/sessions/{session_id}`

返回单 session 的状态、turn/agent/tool/LLM 摘要、renderer link 和事件高水位。通过 path 参数做长度、字符集和 scope 校验，不能用于任意文件路径拼接。推荐支持：

```text
?include=runtime,llm,tools,renderer
?after_seq=418&limit=50       # 可选，返回该 session 的低敏增量事件
```

默认不返回 tool definitions 的完整 schema、tool arguments、receipt message JSON 或历史消息正文；如确实需要工具能力诊断，返回 tool name、schema fingerprint、count、enabled/disabled。

### 5.5 `/events`

请求参数：

```text
session_id, trace_id, agent_id, turn_id, provider, model, event_type
after_seq, before_seq, since, until, limit, cursor
```

查询优先使用 observation ring/read model；session 级 durable catch-up 可由 service 走 EventStore。响应至少包含：

```json
{
  "events": [
    {
      "observation_seq": 9912,
      "timestamp": "2026-08-20T08:30:09.980Z",
      "type": "llm.request.finished",
      "source": "agent_loop",
      "schema_version": "runtime.observe.event.v1",
      "correlation": {
        "session_id": "session_123",
        "trace_id": "trace_abc",
        "turn_id": "turn_9",
        "llm_request_id": "llm_req_42",
        "stream_id": "stream_7"
      },
      "payload": {
        "provider": "provider_a",
        "model": "model-x",
        "attempt": 1,
        "duration_ms": 1830,
        "usage": {"prompt_tokens": 1200, "completion_tokens": 90, "total_tokens": 1290, "source": "provider_reported"},
        "tool_call_count": 0,
        "content_present": true,
        "content_bytes": 840,
        "content_hash": "hmac:v1:..."
      }
    }
  ],
  "after_seq": 9911,
  "latest_seq": 9912,
  "oldest_available_seq": 9701,
  "next_cursor": null,
  "partial": false
}
```

`content_present` 只表示存在，不表示内容可读；hash 使用部署级 HMAC/domain separation，而不是可被低熵 prompt 字典恢复的裸 SHA-256。

## 6. LLM metadata 监管模型

### 6.1 关联层级

所有 LLM 观测事件都应能在以下层级间关联，而不要求客户端解析 provider 私有字段：

```text
session_id
  └─ trace_id / turn_id (一次 Agent 运行)
       └─ llm_request_id (一次逻辑 LLM 请求)
            ├─ retry_attempt_id + attempt (一次 provider 尝试)
            └─ stream_id (可选，一次流式输出)
```

字段分为三类：

1. **身份/关联**：session、trace、turn、agent、request、attempt、stream、tool call ID。
2. **状态/计量**：started/finished、duration、TTFB（若可得）、attempt/max attempts、retryable、HTTP status category、token usage、cache status、tool call count、stream chunk/byte/count。
3. **内容存在性**：长度、token estimate、presence、HMAC/fingerprint；不放正文。

请求开始事件的建议投影：

```json
{
  "type": "llm.request.started",
  "correlation": {
    "session_id": "session_123",
    "trace_id": "trace_abc",
    "turn_id": "turn_9",
    "llm_request_id": "llm_req_42",
    "stream_id": "stream_7"
  },
  "payload": {
    "provider": "provider_a",
    "protocol": "openai-compatible",
    "model": "model-x",
    "stream": true,
    "message_count": 14,
    "tool_count": 6,
    "prompt_chars": 18240,
    "prompt_tokens_estimated": 4630,
    "context_window": 128000,
    "max_output_tokens": 2048,
    "reasoning_enabled": true,
    "reasoning_visibility": "metadata_only",
    "prompt_fingerprint": "hmac:v1:...",
    "tool_surface_fingerprint": "hmac:v1:..."
  }
}
```

`message_count/tool_count` 是数量，不是数组正文。provider/model 也要通过 allowlist 和长度约束；不要把 resource key、KeyValue、BaseURL、路由 header 或 credential metadata 传出。

### 6.2 attempt、retry、stream 与 usage

- `llm.attempt.started/finished`：provider、protocol、attempt/max、duration、status class、provider request ID（按配置允许）、retryable、错误分类；URL 只保留 scheme/host 的受控逻辑名或 provider 名，去掉 query/path 中的凭证。
- `llm.retry`：attempt、retry reason、stable error code/category、delay、next action；不发送原始错误全文。
- `llm.stream.summary`：first byte/TTFB、duration、chunk count、delta bytes、reasoning bytes、image bytes、tool delta count、finish reason；默认不发送每个 delta。
- `llm.request.finished`：成功/失败、累计 duration、usage source（`provider_reported`/`local_estimate`/`mixed`）、prompt/completion/total/cache/reasoning token、tool call count、response presence/length/fingerprint。
- request 和 turn 的 usage 必须明确 `aggregation_level`；客户端不得把 `session_end`/turn 汇总与 request 明细相加。

provider response 的 `ProviderRequestID` 可用于跨系统排障，但这是可关联的外部标识，建议配置 `expose_provider_request_id`，默认仅同 loopback 或更高 scope 可见；原始 response body、headers、request body 永不进入 safe stream。

### 6.3 事件白名单

v1 建议白名单：

```text
runtime.started / runtime.ready / runtime.shutdown
session.started / session.state_changed / session.finished
agent.turn.started / agent.turn.finished
llm.request.started / llm.request.finished
llm.attempt.started / llm.attempt.finished / llm.retry
llm.stream.summary
usage.updated
 tool.started / tool.finished / tool.failed / tool.progress.summary
renderer.snapshot.changed
observation.gap / observation.resync_required
```

列表中的空格仅为 Markdown 排版，实际 event type 为 `tool.*`。未知事件默认丢弃并增加 `unknown_event_dropped` 计数，不将任意 `Payload` 自动透传。

## 7. Renderer 观测设计

### 7.1 语义快照而非终端画面

renderer snapshot 的 v1 只允许导出以下低敏结构：

```json
{
  "renderer_id": "aicli-1",
  "session_id": "session_123",
  "captured_at": "2026-08-20T08:30:10Z",
  "scene": {
    "scene_id": 7,
    "revision": 301,
    "content_version": 498,
    "cell_count": 18,
    "mutable_cell_count": 1,
    "kinds": {"user": 4, "assistant": 5, "tool_chain": 8, "runtime_event": 1},
    "latest_sequence": 21
  },
  "active": {
    "cell_id": 44,
    "kind": "assistant",
    "phase": "mutable",
    "revision": 19,
    "source_chars": 840,
    "source_bytes": 910,
    "source_fingerprint": "hmac:v1:...",
    "stable_chars": 780,
    "acked_chars": 720
  },
  "layout": {
    "layout_generation": 88,
    "width": 120,
    "height": 40,
    "output_bottom_row": 35,
    "cursor_present": true
  },
  "overlays": {"status_present": true, "active_band_rows": 2, "popup": false},
  "display_text": {"included": false, "reason": "safe_default"}
}
```

默认不导出 `TranscriptCell.Source`、display rows、markdown/document span 文本、status message、用户消息和 assistant 内容。若未来增加受控内容 scope，必须是独立 profile，按字段递归脱敏、最大字节/行数、租户隔离、审计和短 TTL；不应改变 safe default 的契约。

### 7.2 同进程与跨进程

- **同进程**：在 UI actor/renderer commit 后调用 adapter，读取 immutable `AppState` 或 `TuiScene.Snapshot`，复制需要的计数和 revision 后立即释放 UI 锁；不得由 HTTP handler 直接持有渲染锁。
- **跨进程**：AICLI 作为 publisher，在 Scene/AppState revision 变化或最多 100ms/配置窗口一次，向 runtime-server 的受认证 ingestion 或共享事件桥发送低敏 snapshot。消息带 `renderer_id`、`instance_id`、`session_id`、`scene_revision`、publisher timestamp、签名/连接 epoch；runtime-server 只保存最新快照和有限变化事件。
- publisher 断线、进程重启或 renderer_id 重用时创建新 `publisher_epoch`，旧快照标记 stale，不能把不同进程的同名 revision 当成连续序列。
- standalone AICLI 若没有 runtime-server/观测 listener，不承诺可从 runtime-server 查询 renderer；可在后续单独设计本地 loopback observation listener，但不能暗中复用 pprof。

### 7.3 Render frame 选择

建议 v1 只输出 semantic snapshot 摘要；v1.1 可增加 `row_ownership`（row index、owner、cell ID、line count、truncated），仍不带文字。若自动化 LLM 确实需要判断“页面是否卡住”，优先使用 `scene_revision`、`layout_generation`、active phase、queue/ack ranges、last render timestamp 等信号，而不是读取屏幕文本。

## 8. 实时 SSE 协议

### 8.1 建连与恢复

`GET /api/runtime/observe/v1/stream` 支持：

```text
session_id / trace_id / event_type / provider / model
cursor=<opaque cursor> 或 after_seq=<observation_seq>
include=snapshot,events,renderer
heartbeat_ms（只能在服务端允许范围内）
```

优先级：显式 `cursor` > `Last-Event-ID` > `after_seq` > 从最新快照开始。服务端必须校验 cursor 的 instance/epoch/schema/expiry，不能接受任意客户端伪造跨实例 cursor。

建立连接的固定顺序：

1. 鉴权、限流、登记客户端；
2. 读取快照并返回 `snapshot`；
3. 以快照返回的 `observation_seq` 做 catch-up；
4. 注册实时订阅并再次 catch-up 一次，消除快照与订阅之间的竞态；
5. 进入实时循环。

如果请求的 cursor 早于 `oldest_available_seq`，不要静默返回不完整事件，发送 `resync_required`（或 HTTP 410 对 query 请求），客户端重新 GET snapshot，再以新 cursor 建流。

### 8.2 事件格式

沿用现有 SSE 的 `event:` + `data:` + flush 习惯，但使用独立 schema：

```text
event: snapshot
id: 9912
data: {"schema_version":"runtime.observe.sse.v1","kind":"snapshot","snapshot_revision":1842,"observation_seq":9912,"data":{...}}

event: event
id: 9913
data: {"schema_version":"runtime.observe.sse.v1","kind":"event","observation_seq":9913,"event":{...}}

: heartbeat {"latest_seq":9913,"server_time":"2026-08-20T08:30:25Z"}
```

正常事件的 `id` 与 `observation_seq` 一致；不要使用现有 trajectory emitter 的连接内 sequence 充当观测恢复 ID。快照 event 也应携带 snapshot cursor，客户端可记录最后应用的 revision。

### 8.3 gap、合并与慢客户端

每条客户端队列有最大事件数和最大字节数。策略按优先级：

1. `llm.stream.delta` 等高频内容默认不进入 safe stream；如启用，仅保留 100ms 窗口的统计摘要/最后状态。
2. 相同 `(session, stream, type)` 的 pending summary 合并，计数和时间范围累加。
3. 工具 progress、重复 status 等低优先级事件可丢弃；记录 `dropped_count/from_seq/to_seq`。
4. lifecycle、request finished、retry、failure、renderer revision 等高优先级事件尽量保留；若队列满仍无法保留，发送 `observation.resync_required` 并关闭连接或要求下一条为完整 snapshot。
5. 不允许通过阻塞 collector 或 bus handler 来“保证不丢”。

gap 示例：

```json
{
  "kind": "gap",
  "observation_seq": 9940,
  "missing": {"from": 9920, "to": 9939, "count": 20},
  "reason": "subscriber_queue_overflow",
  "action": "fetch_snapshot_then_resume"
}
```

ingress queue 溢出也必须体现在 process/runtime snapshot 的 `event_ingress_dropped`、`gap_count`、`last_gap_at` 中。客户端能看到的序列跳跃、gap 事件和 snapshot cursor 三者必须一致可解释。

### 8.4 心跳、关闭与连接限制

- 默认 heartbeat 15s（配置范围例如 5s–60s）；只发 SSE comment 或低敏 heartbeat event，不制造业务事件序列。
- 客户端断开立即取消 watcher/subscription，释放 queue；服务端设置最大连接数、每 IP/身份连接数、最大 stream lifetime、写入空闲超时和请求 header 限制。
- shutdown 时发送 `closed`（若尚可写）并带 `reason`/`last_seq`，随后关闭；不能等待无限期的 SSE client 阻塞主 server 优雅关闭。
- SSE 写失败、marshal 失败、projection 失败只影响该连接/该事件，并记录受限计数；不得把原始数据作为错误响应返回。

## 9. 安全、脱敏与权限

### 9.1 认证和授权

v1 复用 `Handler.authorizeUsageAdmin` 的现有兼容机制：有效 admin token、可信 admin role 或 loopback。该机制是兼容起点，不代表生产远程部署可以只依赖来源地址；loopback 判断和 proxy trust 必须审计，不能把任意 `X-Forwarded-For` 当成客户端地址。

建议新增明确 scope：

```text
runtime.observe.read                 # snapshot、低敏 events、SSE
runtime.observe.read.session         # 指定 session 维度
runtime.observe.read.llm            # provider/attempt/usage metadata
runtime.observe.read.renderer       # renderer semantic snapshot
runtime.observe.debug.raw            # 后续受控证据，不进入 v1 safe API
```

- 现有 admin token 在兼容期可以访问 safe read，但响应仍受 safe redaction profile 约束。
- 远程生产部署推荐 TLS/mTLS 或 JWT/API key scope；不要依赖“端口是内网”作为唯一边界。
- tenant/project/user scope 若现有 `ScopeResolver` 能提供，应在 query filter 和每个 session projection 同时校验，不能只过滤列表而遗漏单资源 endpoint。
- 认证失败统一返回 401/403，不泄露 session 是否存在；审计记录 actor/scope/route/result/bytes/count/request_id，不记录 payload。
- `Authorization`、Cookie、API key、provider key、JWT、内部签名绝不写入 event ring、SSE、日志或 error message。

### 9.2 默认安全字段策略

允许导出的字段必须是显式 allowlist。递归处理 map、slice、JSON body 和 metadata 时采用 deny-first：

- key 名大小写不敏感匹配 `authorization`、`api_key`、`apikey`、`token`、`secret`、`password`、`cookie`、`set-cookie`、`private_key`、`request_body`、`response_body` 等；
- header 只保留允许的非敏感名称，或只返回 header presence；
- URL 去 query、fragment、凭证、完整 API path；优先返回 `provider`/逻辑 endpoint name；
- prompt、system instruction、user/assistant content、reasoning、tool schema、tool arguments、tool result、MCP message 默认只保留 presence、字符/字节/token 数和 HMAC fingerprint；
- 原始错误映射到 `error_category/error_code/retryable/http_status_class`，移除 provider 原文、URL、path、response preview；
- 本地 workspace、artifact、image、日志路径只返回 opaque artifact ID、逻辑 scope、相对安全名、size/hash；
- 所有字符串、数组深度、map keys、事件和响应都有独立上限；超过上限返回 `truncated: true` 和数量/字节统计，不保留头尾拼接的隐性敏感片段。

HMAC key 由部署配置/secret manager 注入；hash domain 应区分 prompt、tool surface、content、renderer source，避免不同数据域可比对。HMAC key 不能由观测 API 返回，key rotation 需在 fingerprint 中带 key version。

### 9.3 原始证据隔离

`HTTPDebugEvent` 当前可以包含最多约 256 KiB 的 request/response raw/preview；这些数据只适合受控离线 debug，不应由 `runtime.observe` 直接引用路径或返回 body。后续若实现 raw evidence：

1. 单独的短 TTL、加密 artifact store；
2. 单独 scope（`runtime.observe.debug.raw`）、二次 scrub 和审计；
3. 一次性/短时下载 token，不允许目录遍历；
4. 默认关闭、默认不采集，独立限流和大小上限；
5. safe event 只能返回 opaque `evidence_id` 和 `available`，不能返回文件路径。

v1 明确不包含 raw evidence endpoint，避免把“实时观测”误变成敏感数据导出。

## 10. 有界内存、背压与故障策略

### 10.1 建议默认限额（待压测校准）

| 项目 | 建议默认 | 说明 |
| --- | ---: | --- |
| ingress queue | 1024 items / 4 MiB | Bus 回调只入队；满时丢弃并计 gap |
| observation ring | 4096 events / 16 MiB | 按事件序列淘汰；超龄按 TTL 淘汰 |
| per-client queue | 256 events / 1 MiB | 每连接独立，不能共享可变 payload |
| max clients | 32 | 超限 429 或服务不可用错误 |
| query limit | 50，max 200 | SSE catch-up 每批也受限 |
| single event | 64 KiB | projection 后再次检查 |
| snapshot size | 256 KiB | 超限拆分或返回摘要/partial |
| max body/header | 沿用 server 安全上限 | v1 全部 GET，不读取 request body |
| heartbeat | 15s | 允许范围 5–60s |
| query timeout | 2s | SSE 使用 request context + server 上限 |

具体数值必须通过并发/大 session/高频 stream 压测校准，并且在 capabilities 中公布 effective limits。每个队列同时以 item 和 bytes 限制，防止少量巨大事件击穿内存。

### 10.2 丢弃语义

- 丢弃不是静默行为：保存 reason、priority、from/to seq、count、last timestamp；只对客户端发低敏统计。
- 丢弃 delta/进度可通过合并摘要恢复语义；丢弃 request lifecycle/failure 等关键事件则必须将该 client 标记为需要 resync。
- 发生 projection 错误时，丢弃该事件并增加 `projection_errors`；不得 fallback 到 raw payload。
- event ring 淘汰后，query 返回 `oldest_available_seq`；cursor 过旧返回 410/`resync_required`。
- 进程重启后 observation_seq 从新 `instance_epoch` 开始，不复用旧序列。若要跨重启恢复，只能以 durable session seq/query 重新建立，不能伪造全局连续序列。

### 10.3 业务链路隔离

- collector 不执行 HTTP、磁盘、数据库慢查询，不持有 actor/render 锁。
- projector 只使用 bounded copy；禁止在 bus callback 中调用可能回调 bus 的函数，避免递归/死锁。
- SSE emitter 的网络写完全在 handler goroutine；写阻塞只占用该连接，超时或 context cancel 后清理。
- shutdown 顺序：停止接受新观测连接 → 标记 collector closed → 发送/丢弃 pending → unsubscribe bus → 停止 publisher watcher → 关闭 ring；主 HTTP server 的 Shutdown deadline 必须覆盖但不无限等待 SSE。

## 11. 配置与生命周期

建议在 runtime server 有效配置模型中新增明确的 `runtime.observe` 段，而不是复用当前遗留 `monitor.pprof`：

```yaml
runtime:
  observe:
    enabled: false                 # 生产默认 false；loopback 开发可显式 true
    route_prefix: /api/runtime/observe/v1
    retention_events: 4096
    retention_bytes: 16777216
    retention_ttl: 10m
    ingress_queue_events: 1024
    ingress_queue_bytes: 4194304
    subscriber_queue_events: 256
    subscriber_queue_bytes: 1048576
    max_clients: 32
    heartbeat: 15s
    max_snapshot_bytes: 262144
    max_event_bytes: 65536
    include_renderer: true
    renderer_publish_interval: 100ms
    expose_provider_request_id: false
    redaction_profile: safe_default
    hmac_key_ref: runtime-observe-fingerprint-v1
```

原则：

- 配置热更新只能改变采样、限额和启停；降低 retention 时安全淘汰，不阻塞业务。
- `enabled=false` 时不注册或返回明确 disabled（推荐不注册敏感路由，health/status 只显示 capability disabled），不能因为访问 loopback 就自动开启。
- 观测服务初始化失败时，runtime-server 应根据配置选择 fail-closed（生产推荐：明确启动失败）或 disabled fallback；不能宣称 ready 但观测半初始化。
- 在 `/capabilities` 中公布 schema、enabled、instance_epoch、retention/cursor 能力、redaction profile、renderer availability、limits。
- 运行时关闭观测必须 unsubscribe 并清空敏感 ring；新连接立即返回 disabled，不能遗留旧客户端继续读取。

默认采用 runtime-server 主 listener；若未来需要把观测 API 绑定到独立 listener，应使用单独配置/证书/认证和生命周期，不与 AICLI pprof listener 共用。独立 listener 适合跨网络监管，但不是 v1 必需项。

## 12. 实施分阶段计划

### Phase 0：契约、边界和开关（先行）

目标是先固定安全与序列契约，不改变业务行为：

1. 定义 `runtime.observe.v1` 的 Go 类型、JSON schema、事件 allowlist、错误码、cursor 和 redaction profile。
2. 新增配置结构与默认关闭开关；明确 runtime-server 主 listener、admin scope、max limits 和 instance epoch。
3. 实现纯函数/独立包的递归 redactor、HMAC fingerprint、URL/header/path scrub、bounded JSON size checker。
4. 为已有 `HTTPDebugEvent`、`RetryEvent`、runtime event payload 编写 projection，不直接复用 struct JSON tags。
5. 加入 schema/capabilities 文档和兼容性约束；记录 raw field 禁止清单。

交付验收：没有新业务事件丢失风险；在 enabled=false 时零额外订阅、零敏感 ring；redaction 单元测试覆盖嵌套 map、数组、大小写 key、恶意 JSON 和 unicode。

### Phase 1：低敏 snapshot read model

1. 实现 `ObservationService` 和 process/runtime/LLM aggregate snapshot。
2. 从现有 `RuntimeStateStore`、SessionManager、usage/trace analytics read model 读取低频摘要；每个组件标注 revision/captured_at。
3. 接入 `/capabilities`、`/snapshot`、`/sessions/{id}`，先不做全局 live SSE。
4. 用 ETag/`If-None-Match`（`snapshot_revision`）降低 LLM 轮询开销；支持 `wait_ms` 的短轮询但设置上限。

交付验收：snapshot 在 actor 高负载/大量 session 时有响应时间上限；不锁住 command loop；对不存在/越权 session 不泄露信息。

### Phase 2：统一 event projection 与 query

1. 添加 bus 轻量 ingress、collector goroutine、observation ring 和 `observation_seq`/epoch。
2. 将 session durable event 通过 EventStore 做 catch-up，区分 `session_event_seq` 和 observation sequence。
3. 实现 `/events` 过滤、分页、`after_seq`/opaque cursor、retention gap 和 partial 标记。
4. 对 `llm.request.*`、retry、stream summary、tool/session lifecycle 进行 allowlist 投影和去重。
5. 在 snapshot 中加入 ingress drop/projection error/ring oldest/latest 等自监控指标。

交付验收：bus Publish 的 handler 只做非阻塞 bounded enqueue；吞吐、丢弃和重放行为在 race test/benchmark 中可量化；raw payload 永不出现在 response。

### Phase 3：SSE live stream

1. 实现 subscriber registry、每客户端 bounded queue、优先级、stream summary coalescing、gap/resync。
2. 实现固定建连顺序（snapshot → catch-up → register → catch-up → live）、`Last-Event-ID`/cursor、heartbeat、closed。
3. 加入 max clients、per-principal/IP quota、write idle timeout 和 graceful shutdown。
4. 若现有 session runtime stream 的 durable watcher/poll 逻辑可复用，只复用 watcher/catch-up 原语，不直接共用旧 payload builder；统一投影后再写 SSE。

交付验收：慢客户端不会拖慢 LLM/tool；人为填满 ingress/client queue 能得到可解释 gap/resync；断线重连不会重复应用或静默跳过关键生命周期事件。

### Phase 4：Renderer observation adapter

1. 在 AICLI UI actor/Scene commit 后实现同进程 `RendererObservationSource`，只复制 semantic snapshot 摘要。
2. 设计跨进程 publisher 的认证、签名、publisher epoch、session scope 和限频；AICLI 与 runtime-server 不共享 UI 内存。
3. 接入 `renderer.snapshot.changed`、`/renderers/{renderer_id}` 和 snapshot 的 renderer component。
4. 增加 stale/partial/renderer unavailable 语义；不把 renderer unavailable 当 server failure。

交付验收：resize、streaming、scene replacement、publisher 重启不会造成 data race 或 revision 回退；绝不返回 ANSI/front buffer。

### Phase 5：生产强化与可选证据

1. 压测并校准 queue/bytes/clients/heartbeat/query 上限；增加 runtime metrics（仅低敏计数）。
2. 增加 security review、tenant scope、TLS/mTLS/JWT scope、访问审计和配置热更新测试。
3. 评估是否需要独立 observation listener；默认仍与主 runtime-server listener 共用。
4. 若确有诉求，再单独设计 raw evidence artifact，不修改 safe v1 契约。
5. 灰度开关、kill switch、运行手册、故障排查和容量预算落地。

## 13. 测试与验收矩阵

### 13.1 契约和安全

- schema version、required/optional 字段、未知字段兼容、错误 envelope、cursor 编解码。
- 未授权、错误 scope、过期 token、跨 tenant session、伪造 forwarded header、CORS/CSRF（若浏览器访问）测试。
- redaction：API key、Authorization、cookie、JWT、密码、prompt、reasoning、tool args、provider body、URL query、绝对路径、base64 图片、嵌套恶意 key。
- 长字符串、深层数组、循环/非 JSON 值、unicode、无效 UTF-8、marshal 失败、single-event/snapshot 超限。
- 审计事件只含 actor/route/result/count/bytes/request_id，不含 payload。

### 13.2 并发、背压与生命周期

- `go test -race` 覆盖 bus Publish 与 unsubscribe、collector shutdown、subscriber disconnect、renderer snapshot publish。
- bus handler 延迟/阻塞检测：模拟慢 projector/client，证明业务 Publish 与 LLM stream callback 不等待网络。
- ingress 满、ring 淘汰、client queue 满、关键事件丢失、coalescing、gap range、resync_required。
- 同一事件 bus + durable store 到达的去重；session watcher 唤醒与 poll fallback；after_seq 边界（0、latest、oldest-1、过期）。
- runtime-server SIGTERM/Shutdown 时连接收到 closed 或在 deadline 内退出；观测禁用/重新启用不留旧 subscriber/敏感内存。
- max clients、per IP/principal rate limit、SSE 长连接、heartbeat、写超时和异常断开。

### 13.3 LLM 和 renderer 语义

- logical request/attempt/retry/stream 关联 ID 在普通、重试、fallback、stream error、tool-call loop 场景保持一致。
- usage source、cache、reasoning、session_end 汇总与 request 明细聚合层级正确，不重复统计。
- provider HTTP 事件只有投影字段；响应 status/error category 稳定；provider request ID 按 scope 开关。
- renderer SceneID/scene revision/content version/layout generation/active phase 在 append/update/finalize/resize/replay 后单调或正确换 epoch。
- AICLI standalone、同进程、跨进程、publisher 断线/重启、renderer unavailable、旧快照 stale。

### 13.4 性能指标

上线前至少测量并设预算：

```text
bus Publish 额外同步耗时（p50/p95/p99）
collector ingress drop rate
projection CPU / allocation / queue depth
snapshot p50/p95 和最大响应字节
SSE catch-up 延迟、heartbeat 开销、每连接内存
renderer publish CPU 和 UI actor 延迟
```

建议初始目标：bus callback 只做 O(1) 入队且 p99 < 1ms；单连接内存由配置上限决定；观测开启不显著增加 LLM stream callback 延迟。具体阈值必须以现有负载基线评审，不在方案阶段硬编码为产品 SLO。

## 14. 风险与应对

| 风险 | 影响 | 应对 |
| --- | --- | --- |
| 原始 payload 被新字段/未知事件意外透传 | prompt、密钥、工具参数泄露 | 纯 allowlist projection；未知事件丢弃；redaction 后 schema 校验和安全测试 |
| SSE 慢客户端阻塞 bus/LLM | Agent 卡顿、请求超时 | bus 轻量 ingress、per-client bounded queue、coalesce/drop/gap；禁止同步网络写 |
| sequence 混用 | 重连重复/漏事件、错误诊断 | observation/session/scene/runtime revision 分域；cursor 带 instance epoch/schema |
| 多 store 快照不一致 | LLM 得到错误因果判断 | component revisions、captured_at、partial/stale/warnings；不承诺跨 store 原子性 |
| 内存 retention 无界 | 长时间运行 OOM | item+byte+TTL 三重上限；snapshot/事件单项上限；动态统计和压测 |
| renderer 跨进程直读 | data race、部署不可行 | publisher/adapter；没有 publisher 就明确 unavailable |
| 复用 pprof listener | 业务敏感数据与无认证 profiler 混在一起 | 主 runtime API/独立 observation listener；pprof 仅 profiling |
| 现有 admin auth 过宽 | 远程低敏 API 意外暴露 | safe profile 固定脱敏；新增 scope；生产推荐 mTLS/JWT；审计和限流 |
| 高速 delta 事件占满队列 | 关键完成事件丢失 | 默认不发送正文 delta；摘要合并；优先级队列；关键丢失触发 resync |
| 观测自身故障影响运行 | 业务链路降级 | fail-closed 只针对观测启动；运行中 projector/collector 故障降级为 dropped/disabled，不回压业务 |
| 字段语义漂移 | LLM 误判运行状态 | schema version、capabilities、aggregation_level、usage_source、freshness 明示；兼容性测试 |
| legacy `monitor.pprof` 配置误导 | 误开放 `:6060` 等通配监听 | 文档标为无效/遗留；任何 pprof 配置接入另行审计 loopback/TLS/auth |

## 15. 需要在实现前确认的开放问题

1. runtime-server 是否是所有 Agent/LLM 执行的唯一进程？AICLI 是否总通过 session/runtime server 运行？这决定 renderer publisher 是同进程 adapter 还是跨进程 ingestion。
2. LLM 观测客户端是内部 operator、另一个 LLM agent、Web UI，还是租户级远程服务？不同主体需要不同 scope、tenant filtering 和 provider ID 暴露策略。
3. 是否需要跨 runtime-server 重启查询？若需要，应定义持久化 observation read model/instance epoch；v1 可先只承诺 session durable event 恢复。
4. 现有 `EventStore` 的 session retention/SQLite 部署是否足以支撑全局 query？若不能，需新增低敏 observation SQLite/read model，但不能让 HTTP 请求扫描所有原始事件。
5. provider request ID、model/provider 名称、pid/goroutine/heap 是否允许发送给 LLM？建议默认允许最小必要字段，按 capability/scope 控制详细程度。
6. 是否存在反向 proxy、TLS termination、可信 forwarded header 规范？在未确认前，不能放宽 loopback/admin auth。
7. 对 safe stream 是否需要输出任何 assistant/user 文本？本方案默认不输出；若业务必须提供，需单独做内容 profile、租户隔离、短 TTL、审计和合规评审。
8. 实时流的“关键事件不可丢”是硬要求还是允许 resync 后以 snapshot 恢复？建议协议层允许丢失但强制可发现和 resync，避免无界阻塞。

## 16. Rollout、回退与运行手册

### 16.1 灰度顺序

1. 代码默认 `enabled=false`，仅单元/集成测试启用。
2. loopback + admin token 小范围启用 Phase 1 snapshot，观察 CPU、内存、延迟。
3. 启用 Phase 2 query，验证 redaction、retention 和 cursor；不先开放远程 SSE。
4. 小连接数启用 Phase 3 SSE，重点压测慢客户端、断线和 server shutdown。
5. renderer publisher 仅在明确部署拓扑的实例启用；跨进程 ingestion 单独灰度。
6. 最后才评估远程 mTLS/JWT、tenant scope 和独立 listener。

### 16.2 回退条件

出现以下任一条件立即关闭 `runtime.observe.enabled` 或 kill switch：

- bus Publish/LLM stream callback p99 超预算或出现阻塞证据；
- redaction 失败、safe response 出现禁止字段、schema 校验异常；
- queue/memory 持续增长、drop/gap 无法解释、SSE 连接无法释放；
- actor/render race、runtime server shutdown 超时、观测故障改变业务结果；
- provider/prompt/tool 敏感数据在日志或客户端被发现。

关闭后应：停止新连接、断开订阅、清空内存 ring、记录关闭原因；现有 Agent/LLM/renderer 继续走原链路。回退不删除 durable session events，不修改已有 `/api/runtime/events`、`/sessions/*/runtime/stream` 和 pprof 行为。

### 16.3 运行手册最小内容

- 如何查看 `/capabilities`、当前 instance epoch、retention oldest/latest、drop/gap/projection error 计数。
- 如何用 snapshot + cursor 重建状态；遇到 410/gap/resync 如何恢复。
- 如何判断 session state 与 renderer snapshot stale/partial。
- 如何区分 process pprof、runtime events、session durable events、LLM analytics 和 observation events。
- 如何轮换 HMAC fingerprint key、撤销 admin token/scope、清理可选 evidence。
- 如何关闭功能并确认 subscriber/ring 已释放；如何验证 runtime-server ready/shutdown。

## 17. 最终建议

采用 **主 runtime-server listener + `/api/runtime/observe/v1` 版本化只读接口 + 统一低敏 projection + bounded event collector + snapshot/SSE 双模式**。先实现 Phase 0–2，确认契约、安全和性能后再上线 SSE 与 renderer adapter。pprof 继续作为独立的本机进程 profiling 能力，既不承载业务语义，也不与监管 API 共用未认证监听器。

这套边界能让 LLM 获得足够的“当前运行到哪里、哪个请求/模型/provider 在工作、是否重试/失败、usage 和渲染 revision 是否推进”等可操作信息，同时把 prompt、工具参数、provider 原文和终端物理状态留在受控的内部数据域中，并在背压、断线、进程重启和跨进程部署时保持可解释、可恢复和可回退。
