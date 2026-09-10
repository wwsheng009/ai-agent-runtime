# LLM 缓存信息统一观测与展示方案（Cache Analytics）

> 状态：设计稿 v1.2（v1.1 完整性审查见 §15；可观测性验证与事件名勘误见 §16）
> 日期：2026-09-10
> 范围：aicli micro web client 新增"缓存"页签；同一后端能力同时服务 runtime server 与 frontend；aicli chat TUI 新增 `/usage` 命令入口
> 关联文档：
> - `docs/plan/aicli-micro-web-client-plan.md`（micro web client 总体方案）
> - `docs/plan/aicli-micro-web-client-upgrade-plan.md`（web client 升级方案）
> - `docs/plan/session-usage-analytics-and-agent-diagnostics-plan.md`（会话 usage 分析，离线链路）
> - `docs/plan/runtime-observability-supervision-http-api-plan.md`（观测平面）
> - `docs/architecture/prompt-cache-layout.md`（prompt cache 布局）

---

## 1. 背景与目标

### 1.1 背景

LLM API（OpenAI / Anthropic / DeepSeek / Gemini 等）普遍在响应中返回 prompt cache 相关的 usage 字段：

| Provider | 字段 | 语义 |
|----------|------|------|
| OpenAI | `usage.prompt_tokens_details.cached_tokens` | 本次请求命中缓存的输入 token 数 |
| Anthropic | `usage.cache_read_input_tokens` / `usage.cache_creation_input_tokens` | 缓存读取 / 缓存写入 token 数 |
| DeepSeek | `usage.prompt_cache_hit_tokens` / `usage.prompt_cache_miss_tokens` | 前缀缓存命中 / 未命中 |
| Gemini | `usageMetadata.cachedContentTokenCount` | 上下文缓存命中 token 数 |

后端已在 `internal/llm/usage_normalizer.go` 将上述差异归一化为统一的 `types.TokenUsage`（`internal/types/token.go`）：

```go
type TokenUsage struct {
    UsageSource           string // provider 归一化来源标识
    PromptTokens          int    // 输入 token（各 provider 口径已归一）
    CompletionTokens      int    // 输出 token
    TotalTokens           int
    CachedTokens          int    // 归一化后的缓存命中（= CacheReadTokens 的别名视图）
    CacheReadTokens       int    // 缓存读取
    CacheCreationTokens   int    // 缓存写入
    CacheReadReported     bool   // provider 是否显式上报了 cache read（区分"0 命中"与"未上报"）
    CacheCreationReported bool
    ReasoningTokens       int
}
```

同时，`internal/agent/loop.go`（ReAct 循环）已在每次 LLM 请求结束时把缓存字段写入事件载荷
（`usage_cache_read_tokens` / `usage_cache_creation_tokens` / `usage_cache_hit_ratio` / `usage_cache_status`），
`internal/chat/actor.go` 的 `appendSessionActorUsagePayload` 也输出同样字段；
`internal/types/message_id.go` 已建立稳定的 `message_id`（`msg_<uuid>`）与 `turn_id` 消息身份体系。

**当前缺口**：这些数据分散在事件流、日志与观测平面中，没有任何一个面向用户的界面把"缓存总览 + 每条请求的缓存明细 + 按消息 id 追溯"呈现出来；且 aicli（本地进程）与 runtime server（服务端进程）是两个运行形态，前端有三处（micro web client / frontend React / aicli chat TUI），如果没有统一的后端能力与契约，就会各自实现三遍。

### 1.2 目标

1. **新增页签**：在 aicli micro web client 增加"缓存"页签，展示：
   - 缓存总览（总请求次数、总命中率、缓存读取/写入 token 总量、输入/输出 token 总量、覆盖率）；
   - 每条 LLM 请求的缓存明细列表（请求时间、provider/model、消息 id、输入/输出 token、缓存读取/写入 token、命中率、缓存状态）；
   - 按消息 id 追溯消息记录历史（点击明细行 → 定位该消息在会话历史中的上下文）。
2. **架构通用性**：缓存分析能力做成**独立的后端核心包 + 版本化 HTTP 契约**，同一份实现挂载到：
   - aicli 本地 loopback（micro web client 消费，`/web/api/cache/*`）；
   - runtime server（frontend 消费，`/api/runtime/sessions/{id}/cache/*`）；
   - aicli chat TUI（`/usage` 命令，进程内直调 `CacheAnalyticsSource`，不经 HTTP，见 §6.4）；
   - 未来任何前端（CLI、第三方集成）只依赖契约，不依赖实现。
3. **数据一致**：三个数据源（实时事件流、会话历史、离线 debug.log）归一到同一个 `CacheRequestRecord` 模型，同一请求在任意前端看到相同的数字。
4. **降级可用**：provider 未上报缓存、历史会话缺 message_id、事件环形缓冲溢出等场景均有明确的降级语义，UI 不猜测、不误报。

### 1.3 非目标

- 不做计费/成本核算（v1 只做 token 维度；成本字段预留，Phase 3 再评估）。
- 不修改任何 provider 请求行为（prompt cache epoch/key 机制保持现状）。
- 不做跨进程的缓存统计合并（aicli 本地与 runtime server 各自统计自己的会话；同一会话同一进程内保证一致）。

---

## 2. 现状盘点（代码证据）

### 2.1 数据链路现状

```
provider 响应
   │  (OpenAI/Anthropic/DeepSeek/Gemini 原生 usage)
   ▼
internal/llm/usage_normalizer.go        ── 归一化为 types.TokenUsage
   │
   ▼
internal/agent/loop.go (ReAct 循环)
   │  request_finished 载荷: usage_prompt_tokens / usage_completion_tokens /
   │  usage_cached_tokens / usage_cache_read_tokens / usage_cache_creation_tokens /
   │  usage_cache_read_reported / usage_cache_hit_ratio / usage_cache_status
   │  metadata: prompt_cache_epoch / prompt_cache_key（缓存代际）
   ▼
internal/events Bus（EventBus）
   │
   ├─► internal/chat/actor.go appendSessionActorUsagePayload（会话事件载荷，同字段）
   ├─► cmd/aicli/commands/chat_runtime_events.go（映射为 runtime events，web SSE 消费）
   ├─► internal/runtimeobserve Collector（observe 平面：llm.request.finished / usage.updated）
   └─► logs/debug.log（chataloganalytics 离线解析 StepUsage，含同样的 cache 字段）
```

### 2.2 已存在且可直接复用的组件

| 组件 | 位置 | 复用点 |
|------|------|--------|
| 统一 TokenUsage | `internal/types/token.go` | 缓存字段的唯一权威模型，`Add()` 支持聚合 |
| usage 归一化 | `internal/llm/usage_normalizer.go` | provider 差异吸收，`ResolveUnifiedTokenUsage` |
| 事件缓存字段 | `internal/agent/loop.go:1838-1865`、`internal/chat/actor.go:4680` | 采集层直接消费，无需改 agent 循环 |
| 消息身份 | `internal/types/message_id.go`（`MetadataKeyMessageID`/`MetadataKeyTurnID`，`EnsureMessageIdentity`） | 追溯关联的主键 |
| 缓存代际 | `internal/agent/loop.go`（`PromptCacheEpoch`、`prompt_cache_key`） | 解释"为什么这一条没命中" |
| 观测平面 | `internal/runtimeobserve`（Event/Correlation/UsageSummary/SSE/cursor/Redactor） | `llm_request_id`/`provider_request_id` 关联模型与脱敏参考 |
| 离线分析 | `internal/chataloganalytics`（StepUsage 含 cache 字段、SessionUsageDetail） | 历史会话/离线兜底数据源 |
| runtime server 路由 | `backend/internal/api/skills/handler.go`（gorilla/mux，`/api/runtime/...`） | 新端点挂载点 |
| micro web client | `backend/cmd/aicli/commands/web/`（tabs：对话/日志/配置；`/web/api/*`） | 新页签挂载点 |
| 背压安全 SSE | `cmd/aicli/commands/web_handlers.go`（chatWebSSEStream：256 帧队列 + 10s 写超时 + 丢帧计数） | 缓存页签实时刷新复用 |
| frontend API 客户端 | `frontend/src/api/runtime/*`（sessions/analytics/...） | 新增 `cache.ts` 的模式参考 |
| frontend 页面 | `frontend/src/pages/usage-analytics-page.tsx` 等 | 新缓存页面的 UI 风格参考 |
| TUI 命令分发 | `backend/cmd/aicli/commands/command.go:335-445`（switch-case：`/status`→`handleStatusCommand` 等） | `/usage` 命令分支的挂接点 |
| TUI 命令目录 | `backend/cmd/aicli/commands/chat_slash_command_catalog.go`（`chatSlashCommandSpec`：Name/Aliases/Usage/Summary/Group/Args） | `/usage` 的目录注册（Group=`session`） |
| TUI 命令输出 | `printChatCommandOutput` / `printfChatCommandOutput`（`command.go:389/417` 等既有调用） | `/usage` 文本渲染复用 |

### 2.3 关键缺口

1. **无统一缓存分析 API**：现有 `/api/runtime/analytics/*` 面向"会话/turn 汇总"（离线日志），observe 面向"进程观测"（低敏、脱敏），两者都没有"按请求列缓存明细 + 按 message_id 追溯"的视图。
2. **LLM 请求 ↔ 消息关联不完整**：`request_finished` 事件携带 `trace_id`/`turn_id`/`step`，但**不直接携带本次请求产出的 assistant `message_id`**，也没有触发请求的 user `message_id`。需要采集层补齐关联（见 §5.2）。
3. **多前端无共享契约**：micro web client 用 `/web/api/*`，frontend 用 `/api/runtime/*`，字段命名与语义未统一。
4. **TUI 无用量/缓存入口**：`command.go` 命令分发表中没有 `/usage`（也没有 `/cache`）；TUI 用户只能靠 `/debug` 等间接观察，无法直接看到缓存总览与明细。

---

## 3. 总体架构

### 3.1 分层视图

```
┌──────────────────────────── 前端层（互不感知，只依赖契约/接口） ─────────────────────────┐
│  aicli micro web client            frontend (React)           aicli chat TUI           │
│  "缓存"页签 /web/                  /usage 页内"缓存"视图      /usage 命令(文本渲染)     │
└──────────────┬──────────────────────────────┬────────────────────────────────────────┘
               │ /web/api/cache/*             │ /api/runtime/sessions/{id}/cache/*
┌──────────────▼──────────────────────────────▼────────────────────────────────────────┐
│ L3 契约层  internal/cacheanalytics/httpapi（版本化 schema：cache.analytics.v1）        │
│   Mount(mux, prefix, source CacheAnalyticsSource) —— 同一 handler，多挂载点            │
├──────────────────────────────────────────────────────────────────────────────────────┤
│ L2 聚合层  internal/cacheanalytics Projector                                          │
│   CacheOverview 增量聚合 + CacheRequestRecord 环形缓冲 + MessageTrace 索引             │
├──────────────────────────────────────────────────────────────────────────────────────┤
│ L1 采集层  internal/cacheanalytics Collector                                          │
│   订阅 EventBus(llm_request_started/finished、assistant 消息持久化)                    │
│   复用 usage_normalizer 归一化；建立 llm_request_id ↔ message_id 关联                  │
├──────────────────────────────────────────────────────────────────────────────────────┤
│ L0 数据源层（已有，不改动语义）                                                        │
│   EventBus 事件流 │ 会话历史(message metadata) │ debug.log(chataloganalytics 离线)     │
└──────────────────────────────────────────────────────────────────────────────────────┘
```

核心原则：**采集与聚合只做一次，HTTP 契约只定义一次，前端各自消费**。

### 3.2 运行形态与挂载点

| 运行形态 | 进程 | EventBus 来源 | 挂载前缀 | 消费方 |
|----------|------|--------------|----------|--------|
| aicli chat（本地） | `cmd/aicli` | `session.LocalRuntimeHost.EventBus` | `/web/api/cache/*`（loopback mux，`pprof.go` 注册点） | micro web client |
| runtime server | `cmd/runtime-server` | 服务端 runtime host EventBus | `/api/runtime/sessions/{id}/cache/*` | frontend React |
| aicli chat TUI | `cmd/aicli` | 同 aicli chat（同一进程同一 EventBus） | 无 HTTP——进程内直调 `CacheAnalyticsSource`（§6.4） | TUI `/usage` 命令 |
| 离线分析（Phase 3） | 两者皆可 | 无（读 debug.log / sqlite） | 同上 | 同上 |

三个在线形态使用**同一个 `Collector` + `Projector`**（泛型于 EventBus 实例，aicli 与 runtime server 的 EventBus 均为 `internal/events.Bus`），差异只在：
- aicli 形态：session 隐含为"当前会话"，同时支持 `?session_id=` 查询本地历史会话；
- runtime server 形态：session 显式在路径中，权限校验复用现有 handler 中间件。
- TUI 形态：与 aicli chat 同进程，直接持有同一个 `LiveSource` 实例（零额外采集开销）；HTTP 契约对 TUI 不可见，仅依赖 `CacheAnalyticsSource` 接口。

### 3.3 包结构（新增）

```
backend/internal/cacheanalytics/
├── types.go          # CacheRequestRecord / CacheOverview / MessageTrace / schema 常量
├── collector.go      # EventBus 订阅 + 事件 → CacheRequestRecord 归一化
├── correlation.go    # llm_request_id ↔ trace/turn/message_id 关联表
├── projector.go      # 内存环形缓冲 + overview 增量聚合 + 查询/过滤
├── source.go         # CacheAnalyticsSource 接口（在线投影 / 历史会话 / 离线日志）
├── httpapi.go        # 版本化 HTTP handler（Mount 函数，纯 adapter）
├── httpapi_test.go
├── collector_test.go
├── projector_test.go
└── correlation_test.go
```

不改动：`internal/agent`、`internal/chat`、`internal/llm`（事件载荷字段已足够；若发现缺口按 §5.4 的"最小补丁"处理）。

---

## 4. 数据模型与契约（cache.analytics.v1）

### 4.1 CacheRequestRecord（每条 LLM 请求一行）

```jsonc
{
  "schema_version": "cache.analytics.v1",
  "llm_request_id": "llmreq_...",          // 主键；与 observe Correlation.LLMRequestID 同源
  "session_id": "sess_...",
  "trace_id": "trace_...",                  // turn 内唯一，串联同 turn 多次请求
  "turn_id": "turn_...",
  "step": 3,                                // ReAct 步骤序号
  "provider": "anthropic",
  "model": "claude-...",
  "stream": true,
  "status": "success",                      // success | error | retrying
  "attempt": 1,                             // 重试序号（同一 llm_request_id 多 attempt 时取最终成功 attempt，明细另列）
  "started_at": "2026-09-10T12:00:00Z",
  "duration_ms": 1234,
  "usage": {                                // 归一化后 TokenUsage（字段与 internal/types/token.go 一致）
    "usage_source": "anthropic",
    "prompt_tokens": 10000,
    "completion_tokens": 500,
    "total_tokens": 10500,
    "cached_tokens": 8000,
    "cache_read_tokens": 8000,
    "cache_creation_tokens": 1200,
    "cache_read_reported": true,
    "cache_creation_reported": true,
    "reasoning_tokens": 0
  },
  "cache_hit_ratio": 0.8,                   // cache_read / prompt_tokens（仅 cache_read_reported=true 时非空）
  "cache_write_ratio": 0.12,                // cache_creation / prompt_tokens
  "cache_status": "hit",                    // hit | write | reported_zero | not_reported | error
  "cache_epoch": 2,                         // prompt_cache_epoch（缓存代际）
  "prompt_cache_key": "sess_...#prompt-cache-epoch-2",
  "prompt_fingerprint": "pf_...",           // 请求提示词指纹（started 载荷已含，loop.go:1685-1692）；
                                            // 用于反查"提示词前缀相同却未命中"的请求对
  "user_message_id": "msg_user_...",        // 触发本 turn 的 user 消息（经 turn_id 关联）
  "assistant_message_id": "msg_asst_...",   // 本请求产出的 assistant 消息（见 §5.2 回填）
  "provider_request_id": "req_...",         // provider 侧请求 id（observe 已有，尽力而为）
  "error_category": ""                      // status=error 时的错误分类
}
```

### 4.2 CacheOverview（总览）

```jsonc
{
  "schema_version": "cache.analytics.v1",
  "session_id": "sess_...",
  "generated_at": "2026-09-10T12:05:00Z",
  "window": { "from": "...", "to": "..." },   // 数据覆盖窗口
  "requests_total": 42,
  "requests_with_usage": 40,                   // 覆盖率分母口径
  "requests_cache_reported": 38,               // provider 显式上报 cache 字段的请求数
  "tokens": {
    "prompt_tokens": 420000,
    "completion_tokens": 21000,
    "total_tokens": 441000,
    "cache_read_tokens": 336000,               // Σ 缓存读取
    "cache_creation_tokens": 36000,            // Σ 缓存写入
    "reasoning_tokens": 0
  },
  "cache_hit_ratio": 0.8,                      // Σcache_read / Σprompt（仅对 reported 请求求和）
  "cache_write_ratio": 0.086,
  "cache_status_distribution": {               // 状态分布，UI 直接画饼
    "hit": 30, "write": 6, "reported_zero": 2, "not_reported": 4
  },
  "coverage": {
    "usage_request_rate": 0.952,               // requests_with_usage / requests_total
    "cache_report_rate": 0.905,                // requests_cache_reported / requests_with_usage
    "partial": false,                          // 环形缓冲溢出/采集断档时 true
    "partial_reasons": []
  }
}
```

**口径定义（重要，前后端必须一致）**：

| 指标 | 公式 | 说明 |
|------|------|------|
| 缓存命中率（单请求） | `cache_read_tokens / prompt_tokens` | 分母是归一化后的 prompt_tokens。Anthropic 的 `cache_read_input_tokens` 已计入 `input_tokens`，OpenAI 的 `cached_tokens` 已计入 `prompt_tokens`——usage_normalizer 已保证该口径（见 `internal/llm/usage_normalizer.go:215-281` 的 firstPositiveInt/取大逻辑） |
| 缓存命中率（总览） | `Σcache_read / Σprompt`（仅对 `cache_read_reported=true` 的请求求和） | 未上报请求不进分子分母，避免把"未知"当"0 命中"稀释比率 |
| 缓存写入率 | `Σcache_creation / Σprompt`（仅 `cache_creation_reported=true`） | 写入发生在冷启动/缓存代际切换，是成本而非收益 |
| 缓存状态 | `hit`（read>0）/ `write`（creation>0 且 read=0）/ `reported_zero`（显式上报但为 0）/ `not_reported`（provider 未给字段）/ `error` | 与 `internal/agent/loop.go:1856-1865` 现有 `usage_cache_status` 语义对齐，扩展 `write` 一档 |

### 4.3 MessageTrace（按消息 id 追溯）

```jsonc
{
  "schema_version": "cache.analytics.v1",
  "session_id": "sess_...",
  "message_id": "msg_asst_...",
  "message_role": "assistant",
  "turn_id": "turn_...",
  "produced_by": {                    // 该消息由哪个 LLM 请求产出（assistant 消息）
    "llm_request_id": "llmreq_...",
    "usage": { ... },                  // 同 4.1 usage
    "cache_hit_ratio": 0.8,
    "cache_status": "hit"
  },
  "consumed_by": [                    // 该消息作为输入被后续哪些请求消费（user/assistant/tool 消息）
    { "llm_request_id": "llmreq_...", "step": 5, "cache_hit_ratio": 0.83 }
  ],
  "neighbors": {                      // 相邻消息链（供 UI 定位上下文，浅引用）
    "prev_message_id": "msg_user_...",
    "next_message_id": "msg_tool_..."
  },
  "history_available": true           // 会话历史中可查（true 时前端可再拉 /history 定位全文）
}
```

追溯语义：**"这条消息的缓存从哪来、到哪去"**——产出它的请求（写缓存/命中缓存的时刻）+ 消费它的后续请求（缓存收益兑现的时刻）。

### 4.4 HTTP 契约

所有端点返回 `application/json; charset=utf-8`；错误 envelope 复用 observe 风格（稳定错误码，不回传原始 Go error）。

| 端点（挂载前缀 `{base}` 见 §3.2） | 方法 | 说明 |
|-----------------------------------|------|------|
| `{base}/cache/capabilities` | GET | 能力发现：schema 版本、数据源（live/history/offline）、是否支持 SSE |
| `{base}/cache/overview` | GET | 总览。aicli 形态 `?session_id=` 可选（默认当前会话）；runtime 形态 session 在路径中 |
| `{base}/cache/requests` | GET | 明细分页列表。过滤参数：`trace_id`、`turn_id`、`message_id`、`status`、`cache_status`、`from`、`to`、`limit`（默认 50，最大 200）、`offset`；按 `started_at` 倒序 |
| `{base}/cache/requests/{llm_request_id}` | GET | 单请求详情（含 retry attempts 明细） |
| `{base}/cache/messages/{message_id}/trace` | GET | 消息追溯（§4.3） |

错误码：`cache_analytics_disabled` / `cache_invalid_request` / `cache_session_not_found` / `cache_not_found`（请求或消息不存在）/ `cache_internal`。

**SSE 增量（复用现有通道，不新增端点）**：
- aicli：现有 `/web/api/events` SSE 事件映射表新增 `cache_request_finished`（载荷 = CacheRequestRecord 投影），micro web client 收到后增量刷新缓存页签；
- runtime server / frontend：复用 `/api/runtime/sessions/{id}/runtime/events`（事件流中已含 usage 字段）或 observe SSE 的 `usage.updated`；frontend 按现有 `use-session-runtime-stream` 模式消费。

---

## 5. 后端核心设计

### 5.1 采集层（Collector）

**输入事件**（均为现有事件，无需新增发布点；事件名已对照发布点核实，2026-09-10）：

| 事件（实际发布的类型名） | 发布点 | 用途 |
|------|------|------|
| `llm.request.started` | `internal/agent/loop.go:1693`、`internal/api/skills/handler.go:7354` | 登记 in-flight 请求：llm_request_id、trace_id、**logical_turn_id**（载荷字段名，即本方案的 turn_id）、step、provider/model、started_at、cache_epoch/key、**prompt_fingerprint** |
| `llm.request.finished` | 成功 `loop.go:1874`；**失败路径同样发布** `loop.go:1784`（`success:false` + `error`/`error_code`/`retryable`/`next_action`） | 终态：归一化 usage（载荷已含 `usage_*` 全部字段）、duration、status、error_category |
| `llm.request_parameter.downgraded` / `llm.parallel_tool_calls.downgraded` / `llm.max_output_tokens.escalated`（`loop.go:1700/1722`） | 同一 llm_request_id 内的参数降级/升级重Call | attempt 链证据（明细用，聚合取最终成功 Call；无独立 `llm_retry` 事件） |
| assistant 消息持久化/`message` 类事件 | `internal/chat/actor.go`（载荷含 `usage_*`，见 `appendSessionActorUsagePayload:4680-4713`） | 用 trace_id/turn_id 反查 in-flight/finished 记录，回填 `assistant_message_id`（§5.2） |
| turn 开始时的 user 消息事件 | 同上 | 建立 `turn_id → user_message_id` 映射 |

> **命名勘误（v1.2）**：`internal/chat/events.go:16-17` 的下划线常量 `llm_request_started/finished` 只被消费端作兼容别名使用（各 bridge 均为 `case EventLLMRequestStarted, "llm.request.started":` 双匹配，如 `chat_runtime_events.go:2706-2709`），**实际发布的类型名是点号格式**。Collector 订阅必须匹配点号名（可同时兼容下划线别名）；载荷中的 turn 字段名为 `logical_turn_id`（消费端读取示例 `chat_runtime_events.go:5285`）。

**归一化规则**：
- 载荷中的 `usage_*` 字段直接映射到 `CacheRequestRecord.usage`；若载荷缺 usage（provider 未返回），整条记录 `usage` 为零值 + `cache_status=not_reported`，**不从本地估算伪造**（沿用 `internal/agent/loop.go:1863` 的 `not_available_for_local_estimate` 语义，v1 不实现本地估算）。
- `cache_hit_ratio`/`cache_write_ratio` 在 Collector 内统一计算（单一实现，避免 loop.go 与前端各自算出不同值）。
- 所有记录写入前过 `runtimeobserve.Redactor` 同款脱敏策略（v1 记录本身不含 prompt 内容，风险极低，但 provider_request_id 等仍按 profile 处理）。

**并发与生命周期**：
- Collector 挂在 EventBus 订阅上（`SubscribeCancelable`），与 `chat_runtime_events.go` 的 RuntimeEventBridge 同模式；session 结束时退订。
- 每条记录处理为纯内存操作（O(1)），不阻塞发布者（EventBus.Publish 是同步调用 handler，Collector 内禁止任何 IO）。

### 5.2 关联层（Correlation）—— message_id 如何补齐

这是本方案最关键的正确性设计。现状：`llm.request.finished` 载荷有 `trace_id`/`logical_turn_id`（即本方案的 turn_id，见 §5.1 命名勘误），但没有 assistant `message_id`。

**关联表**（内存，随 Projector 生命周期）：

```
turn_id ──► user_message_id          （turn 开始时登记，1:1）
trace_id ──► llm_request_id          （request_started 登记，1:1）
turn_id ──► [llm_request_id...]      （同 turn 多步 ReAct，1:N）
message_id ──► llm_request_id        （assistant 消息产出关联，见下）
```

**assistant_message_id 回填的两级策略**：

1. **主路径（事件流）**：assistant 消息完成事件（会话事件流中 assistant 消息持久化时携带 `metadata.message_id` 与 `trace_id`/`turn_id`）到达时，按 `trace_id` 找到本 turn 最后一个成功请求，回填其 `assistant_message_id`。ReAct 一个 turn 内有 N 次请求时，取"最后一次成功请求"（中间 tool 调用步的请求产出的 assistant 消息同样按 trace_id 顺序回填，保持 1:1）。
2. **兜底路径（历史查询）**：查询时若 `assistant_message_id` 仍为空（事件丢失/旧会话），`Source` 实现按 `turn_id` 从会话历史（`/history` 返回的消息 metadata 已含 `message_id`/`turn_id`）匹配同 turn 的 assistant 消息补齐，并在记录上标注 `correlation_source: "history_inferred"`，UI 可显示"推断"徽标。

**user_message_id**：turn 开始事件 → user 消息 id；兜底同上（按 turn_id 从历史匹配 role=user 的消息）。

### 5.3 聚合层（Projector）

- **环形缓冲**：每 session 保留最近 `N=1000` 条 `CacheRequestRecord`（可配置）；溢出时 `coverage.partial=true` + `partial_reasons=["ring_overflow"]`，overview 数值改为"基于保留窗口"口径并在 UI 明示。
- **增量聚合**：overview 不重算全表——每条记录终态时增量更新 Σ 各项与状态分布；record 不可变（终态后不再修改，除 assistant_message_id 回填外），回填仅改关联字段不改 usage，因此聚合无需回滚。
- **查询**：`requests` 端点在环形缓冲上做线性过滤（1000 条 × O(1) 字段比较，微秒级，无需索引）；`message trace` 走关联表哈希。
- **持久化（Phase 3 可选）**：复用 `session_runtime.sqlite` 增加只读镜像表（`cache_requests`），启动时回放历史会话记录；v1 先纯内存 + 会话历史兜底，已满足页签需求。

### 5.4 对现有代码的最小补丁（仅当缺口确认）

实施 Phase 1 时需验证以下两点，若现有事件流已携带则**零改动**：

| # | 待验证 | 若缺失的补丁 |
|---|--------|--------------|
| P1 | assistant 消息持久化事件是否携带 `trace_id`/`turn_id` + `message_id` | 在 `internal/chat/actor.go` 消息持久化路径的载荷中补 `trace_id`/`turn_id`（消息 metadata 已有 turn_id，仅透传） |
| P2 | turn 开始事件是否携带 user `message_id` | 同上，user 消息入 history 时载荷补 `message_id` |

两处均为"载荷加字段"，不改变事件语义，旧消费者不受影响（新增字段向后兼容）。

### 5.5 Source 接口（通用性核心）

```go
// internal/cacheanalytics/source.go
type CacheAnalyticsSource interface {
    // Capabilities 报告数据源能力（live/history/offline、窗口、是否支持 SSE）
    Capabilities(ctx context.Context) (Capabilities, error)
    // Overview 返回指定会话的总览
    Overview(ctx context.Context, sessionID string) (CacheOverview, error)
    // Requests 返回明细分页（含过滤）
    Requests(ctx context.Context, sessionID string, q RequestQuery) (RequestListResponse, error)
    // Request 返回单请求详情
    Request(ctx context.Context, sessionID, llmRequestID string) (CacheRequestRecord, error)
    // MessageTrace 返回消息追溯
    MessageTrace(ctx context.Context, sessionID, messageID string) (MessageTrace, error)
}
```

三个实现：
1. `LiveSource`（内存 Projector，aicli 与 runtime server 共用）；
2. `HistorySource`（从会话历史 + 事件流兜底推断，处理旧会话/重启后场景）；
3. `LogFileSource`（Phase 3，包装 `chataloganalytics`，把 debug.log 的 StepUsage 投影为同一模型）。

`httpapi.Mount(mux, prefix, source)` 只依赖接口——**这就是"同一个后端，不同前端"的落点**。

---

## 6. 前端与 TUI 设计

### 6.1 aicli micro web client：新增"缓存"页签

**改动文件**（全部在 `backend/cmd/aicli/commands/`）：

| 文件 | 改动 |
|------|------|
| `web/index.html` | tabs 区新增 `<button id="tab-cache-btn">缓存</button>` + `<div id="tab-cache" class="tab-panel">` 骨架（overview 卡片区 + 明细表 + 追溯弹窗容器） |
| `web/js/cache.js`（新增 ES 模块） | 页签逻辑：拉取 overview/requests、渲染表格、过滤/分页、SSE 增量刷新、追溯弹窗 |
| `web/style.css` | 缓存页签样式（复用现有卡片/表格/徽标样式变量） |
| `web_cache_handlers.go`（新增） | `HandleChatWebAPICacheCapabilities/Overview/Requests/Request/MessageTrace`——薄 adapter，内部调用 `cacheanalytics` 包 |
| `web_schema.go` | 注册 `/web/api/cache/*` 路径常量与 SSE schema 新事件 `cache_request_finished` |
| `pprof.go`（loopback mux 注册点） | 注册新 handler |

**页签布局**：

```
┌─ 缓存 ────────────────────────────────────────────────────────────┐
│ [总览卡片行]                                                       │
│  ┌────────┐ ┌────────┐ ┌────────┐ ┌────────┐ ┌────────┐           │
│  │总请求 42│ │命中率 80%│ │读 336k │ │写 36k  │ │覆盖 95% │           │
│  └────────┘ └────────┘ └────────┘ └────────┘ └────────┘           │
│  状态分布: ●hit 30 ●write 6 ●zero 2 ●not_reported 4   [刷新] [⟳自动] │
│ [明细表]（倒序，虚拟滚动/分页）                                        │
│  时间 │ 模型 │ 消息id │ 输入 │ 输出 │ 缓存读 │ 缓存写 │ 命中率 │ 状态   │
│  12:03 claude… msg_a… 10.0k 500  8.0k  1.2k  80%  ●hit            │
│   … 点击行 → 追溯弹窗                                                │
│ [追溯弹窗]                                                          │
│  msg_asst_… 由 llmreq_… 产出（hit 80%）→ 被后续 2 个请求消费         │
│  [在对话中查看] → 切到"对话"页签并高亮该消息（复用会话历史定位）           │
└───────────────────────────────────────────────────────────────────┘
```

**交互细节**：
- 数据获取：页签首次激活时拉 overview + requests；SSE `cache_request_finished` 到达时增量插入表头 + 重算 overview（或直接重拉 overview，二选一由实现按数据量定）。
- 会话切换：复用现有 sidebar 会话切换逻辑，切换后清空并重拉。
- 消息追溯：弹窗内展示 `produced_by`/`consumed_by`；"在对话中查看"复用现有会话历史渲染（web client 已有完整 transcript 渲染），按 `message_id` 定位滚动并高亮；若 `history_available=false` 显示"该消息不在当前历史窗口"。
- 降级显示：`cache_status=not_reported` 的行命中率列显示"—"而非 0%；`coverage.partial=true` 时表格顶部显示"仅保留最近 1000 条请求"横幅。

### 6.2 frontend（React）：缓存分析页面

**改动文件**（`frontend/src/`）：

| 文件 | 改动 |
|------|------|
| `api/runtime/cache.ts`（新增） | 契约客户端：`getCacheOverview/getCacheRequests/getCacheRequest/getMessageTrace`，模式对齐 `api/runtime/analytics.ts` |
| `types/runtime.ts` | 新增 `CacheOverview/CacheRequestRecord/MessageTrace` 类型（与 §4 契约一一对应） |
| `pages/cache-analytics-page.tsx`（新增） | 缓存视图组件：overview 卡片 + 请求明细表 + 追溯侧栏；风格对齐 `usage-analytics-page.tsx` |
| `pages/usage-analytics-page.tsx` | 页内新增"用量 / 缓存"tab 切换（缓存 tab 渲染 `CacheAnalyticsView`） |
| `App.tsx`（路由） | 新增 `/usage/cache` 与 `/usage/cache/sessions/:sessionId`（`App.tsx:39-41` 已有 `/usage`、`/usage/sessions/:sessionId`、`/analytics` → `UsageAnalyticsPage`，缓存视图挂同一页面树下） |
| `i18n/resources/zh-CN.ts` / `en-US.ts` | 文案 |

**入口决策**：缓存视图作为 `/usage` 页内 tab（而非独立顶级路由）——用量与缓存同属"消耗观测"心智，且复用 `UsageAnalyticsPage` 既有的 session 选择、时间过滤与布局骨架；直接访问 `/usage/cache` 时默认选中缓存 tab。

**与 workspace 的关系**：v1 作为 `/usage` 页内视图（全局缓存视图，可按 session 过滤）；Phase 3 可在 workspace 会话内嵌"缓存"侧板（数据同源，只是视图不同）。

**实时刷新**：复用 `hooks/workspace/use-session-runtime-stream.ts` 的事件流订阅，过滤 usage 相关事件触发重拉；不做独立 SSE 连接。

### 6.3 追溯链路的端到端一致性

```
明细行(assistant_message_id) ──► GET trace ──► produced_by/consumed_by
        │                                              │
        ▼                                              ▼
会话历史 /history（metadata.message_id）──► 定位消息全文与上下文 ──► UI 高亮
```

同一 `message_id` 贯穿：消息 metadata（`EnsureMessageIdentity` 保证稳定）→ 缓存记录关联 → 历史定位。旧会话无 message_id 时走 legacy `id` 回退（`message_id.go` 已实现）。

### 6.4 aicli chat TUI：`/usage` 命令入口

**现状**：`command.go` 命令分发表（`command.go:335-445`）中没有 `/usage`（也没有 `/cache`），TUI 目前无任何用量/缓存入口（§2.3 缺口 4）。新增命令完全遵循既有模式：

| 步骤 | 文件 | 内容 |
|------|------|------|
| 1. 命令实现 | `chat_usage_command.go`（新增）+ `chat_usage_command_test.go` | `handleUsageCommand(session, command)`，命名对齐 `chat_theme_command.go`/`chat_model_command.go` |
| 2. 目录注册 | `chat_slash_command_catalog.go` | 新 spec：`Name: "/usage"`、`Usage: "/usage [cache]"`、`Summary: "显示会话用量与缓存统计"`、`Group: session`、`AcceptsArgs: true` |
| 3. 分发挂接 | `command.go` | `case "/usage": return handleUsageCommand(session, command)`（与 `/status`→`handleStatusCommand` 同型） |
| 4. 渲染 | `printChatCommandOutput` / `printfChatCommandOutput` | 纯文本输出，风格对齐 `/timeline`/`/sessions` |

**命令语义**（子命令为未来用量维度扩展预留，v1 只实现 cache）：

```
/usage                          → 当前会话缓存总览（默认视图）
/usage cache                    → 同上（显式子命令）
/usage cache requests [N]       → 最近 N 条请求明细（默认 20，上限 100）
/usage cache trace <message_id> → 按消息 id 追溯（produced_by/consumed_by + 上下文摘要）
```

**总览输出示例**：

```
会话缓存统计（session: a1b2c3...，覆盖最近 1000 条请求）
  请求总数: 42   命中: 31   未命中: 8   未上报（未知）: 3
  输入 token: 128,400（缓存读取 96,200 / 缓存写入 12,800）
  输出 token: 18,900
  缓存读取率: 74.9%   缓存写入率: 10.0%
```

**数据源**：进程内直调 `CacheAnalyticsSource`（aicli chat 形态的 `LiveSource`，session 隐含当前会话）。TUI **不 import `httpapi`、不构造 HTTP 请求**——这是 Source 接口"同一后端，不同前端"的第二个落点（第一个是两种 HTTP 挂载前缀）。

**渲染降级规范**（与 §6.1 web client 语义一致）：
- `cache_status=not_reported` → 命中率列显示 `--`，不计入命中率分母的展示口径在表头注明；
- `coverage.partial=true` → 输出首行标注 `仅统计最近 N 条请求`；
- `correlation_source=history_inferred` → trace 输出的 message_id 后标注 `(推断)`；
- 空会话/无记录 → `当前会话暂无 LLM 请求记录`；
- 后端未启用缓存分析（capabilities 探测失败）→ `缓存分析不可用（当前后端版本不支持）`。

**与现有命令的关系**：`/status` 保持会话状态语义不变；`/usage` 聚焦用量与缓存维度，不重复 `/debug` 的诊断信息。Phase 3 成本维度落地时以 `/usage cost` 子命令扩展，复用同一 Source。

**测试**（`chat_usage_command_test.go`）：无会话、空记录、正常总览、明细分页边界（N=0/超上限）、trace 命中与未命中、`not_reported` 渲染、partial 提示、未知子命令提示。

---

## 7. 通用性与扩展设计

### 7.1 "同一后端，不同前端"的保证机制

| 机制 | 说明 |
|------|------|
| 核心包零 cmd 依赖 | `internal/cacheanalytics` 不 import `cmd/aicli/commands`、`internal/api/skills`；方向依赖单向（cmd → cacheanalytics） |
| Source 接口 | 前端差异被吸收在 Source 实现里（当前会话语义 vs 路径 session 语义），HTTP 层完全一致 |
| 版本化 schema | `cache.analytics.v1`；新增字段向后兼容（前端忽略未知字段），破坏性变更升 `v2` 并保留 `v1` 一个版本周期 |
| capabilities 端点 | 前端启动时探测：数据源类型、支持的事件、窗口限制；不支持缓存分析的旧后端返回 404/`cache_analytics_disabled`，前端隐藏页签（优雅降级） |
| 事件契约复用 | SSE 增量走既有事件通道（`/web/api/events`、`/runtime/events`、observe SSE），不为缓存新建长连接体系 |

### 7.2 未来扩展点（预留，不实现）

- **成本维度**：`CacheRequestRecord` 预留 `cost` 字段位；Phase 3 结合 model_cards 计价后再启用。
- **CLI/第三方集成**：同样只消费 `CacheAnalyticsSource` 接口（TUI `/usage` 已纳入 v1 交付，见 §6.4；独立 CLI 与第三方集成另行立项）。
- **跨会话聚合**：runtime server 形态可增加 `/api/runtime/cache/overview`（全局），复用同一 Projector 多实例聚合。
- **导出**：`?format=csv` 由 httpapi 统一支持（同 handler 分支）。

---

## 8. 安全与隐私

1. **只读**：全部端点 GET，无输入注入面；不修改会话状态。
2. **脱敏**：记录不含 prompt/completion 内容；`provider_request_id` 等外部标识按 `runtimeobserve` 的 redaction profile 处理（复用 `NewRedactor`，aicli 形态默认 `safe_default`）。
3. **访问控制**：
   - aicli loopback：沿用现有 `/web/` 端点族仅绑定 loopback 的既有约束；
   - runtime server：挂载在既有 runtime 路由树内，自动继承现有鉴权中间件（与 `/api/runtime/sessions/{id}/history` 同级同策略）。
4. **无密钥泄露**：契约中不出现 API key、BaseURL 凭据；`provider`/`model` 为展示性字段。

---

## 9. 性能与容量

| 项 | 预算 | 手段 |
|----|------|------|
| 采集开销 | 每请求 O(1) 内存操作 | Collector 纯内存，禁止 IO；EventBus 同步发布路径不受影响 |
| 内存上限 | 每 session ≤ 1000 条记录 × ~1KB ≈ 1MB | 环形缓冲 + 可配置上限；多 session 总量受 runtime 既有 session 上限约束 |
| 查询延迟 | 明细分页 < 5ms | 1000 条线性过滤足够；不建索引 |
| SSE 增量 | 单帧 < 2KB | `cache_request_finished` 只发单条记录投影 |
| 背压 | 慢客户端不阻塞 agent | aicli 复用 chatWebSSEStream（256 帧队列 + 10s 写超时 + 丢帧计数）；frontend 走轮询/既有事件流 |

---

## 10. 兼容性

1. **win7 构建**：`internal/cacheanalytics` 仅用标准库 + 既有内部依赖，无 cgo、无新第三方依赖；`go.win7.mod` 无需变更。
2. **旧会话**：无 usage / 无 message_id 的历史记录按 §4.1 状态机显示 `not_reported` / 推断徽标，不报错。
3. **旧前端**：新增端点对既有前端零影响；SSE 新事件类型旧 web client 忽略（现有事件分发按类型路由，未知类型已安全跳过）。
4. **provider 差异**：全部吸收在 usage_normalizer（已存在）；新增 provider 只需保证 normalizer 产出 `CacheReadTokens/CacheCreationTokens/Reported` 标志。

---

## 11. 实施计划

### Phase 1 — 后端核心 + micro web client 页签（本次交付）

1. `internal/cacheanalytics`：types / collector / correlation / projector + 单测（含 `-race`）。
2. 验证 §5.4 P1/P2 两个事件字段；缺失则打最小补丁。
3. aicli 挂载：`web_cache_handlers.go` + `/web/api/cache/*` 注册（`pprof.go` loopback mux + `web_schema.go`）。
4. web client：`tab-cache` 页签 + `js/cache.js` + SSE `cache_request_finished` 映射 + 追溯弹窗。
5. TUI：`chat_usage_command.go` + `chat_slash_command_catalog.go` 注册 + `command.go` 分发挂接（§6.4），实现 `/usage` 总览、`/usage cache requests [N]` 明细、`/usage cache trace <message_id>` 追溯。
6. 端到端验证：真实会话产生缓存命中/未命中/写入三类记录，页签数字与 debug.log 的 `chataloganalytics` 结果交叉核对；TUI `/usage` 与 web client 页签数字一致性抽查。

### Phase 2 — runtime server + frontend

1. `LiveSource` 接入 runtime server 事件总线；`/api/runtime/sessions/{id}/cache/*` 注册（`internal/api/skills/handler.go` 路由树）。
2. `HistorySource`：会话历史兜底推断（assistant/user message_id）。
3. frontend：`api/runtime/cache.ts` + `cache-analytics-page.tsx` + i18n + 入口（`/usage` 页内"缓存"tab + `/usage/cache` 路由，§6.2）。
4. e2e：`scripts/test-runtime-server-browser-e2e.ps1` 风格的浏览器验证。

### Phase 3 — 增强（另行立项）

sqlite 持久化回放、离线 `LogFileSource`（chataloganalytics 集成）、成本估算、CSV 导出、跨会话全局视图、observe 平面 `cache.updated` 事件类型。

---

## 12. 测试与验收标准

**单测**（Go，`internal/cacheanalytics/*_test.go`）：
- 归一化：OpenAI/Anthropic/DeepSeek 三类 usage 载荷 → CacheRequestRecord 字段逐一断言（对齐 `usage_normalizer_test.go` 的既有用例口径）。
- 关联：同 turn 多步 ReAct（N 请求 1 assistant 消息）、assistant_message_id 回填主路径与 history 兜底路径。
- 聚合：overview 增量更新与全量重算一致性（property 风格随机用例）；环形缓冲溢出 → partial 标记。
- HTTP：Mount 后各端点状态码/错误码/分页/过滤；`-race` 通过。

**前端**：
- web client：页签切换拉取、SSE 增量、追溯弹窗定位、not_reported 降级显示（手测 + 既有 e2e 脚本扩展）。
- frontend：`cache-analytics-page` 组件测试（mock 契约响应），对齐 `usage-analytics-page.test.tsx` 模式。
- TUI：`chat_usage_command_test.go` 覆盖 §6.4 测试清单（无会话/空记录/分页边界/降级渲染）。

**验收清单**：
- [ ] micro web client 出现"缓存"页签，总览数字与明细表可交叉对账（Σ明细 = 总览）。
- [ ] 点击明细行可追溯消息，"在对话中查看"能定位到正确消息。
- [ ] 同一会话在 micro web client 与 frontend（经 runtime server）看到的 overview 数值一致。
- [ ] TUI `/usage` 总览数字与 web client 页签一致（同一会话同一时刻）；`/usage cache requests` 与 `/usage cache trace` 可用。
- [ ] provider 未上报缓存的请求显示 `not_reported`，不污染命中率。
- [ ] 旧会话（无 message_id）可打开页签，显示推断徽标，无 JS 报错。
- [ ] `-race` 与 win7 构建通过。

---

## 13. 风险与待确认事项

| # | 风险/待确认 | 影响 | 缓解 |
|---|------------|------|------|
| R1 | assistant 消息事件缺 trace_id/turn_id（§5.4 P1） | 关联回填主路径失效 | 兜底走 history 匹配；Phase 1 首日验证 |
| R2 | runtime server 会话事件流与 aicli 本地事件流字段不完全一致 | LiveSource 需两套适配 | Collector 输入做字段容错（缺失字段置空而非报错）；Phase 2 用真实 runtime 会话验证 |
| R3 | Anthropic cache_read 计入 prompt_tokens 的口径误解 | 命中率算错 | 口径以 usage_normalizer 归一结果为准（§4.2 表格），单测锁定 |
| R4 | 环形缓冲溢出导致总览与明细不全 | 用户误读 | partial 标记 + UI 横幅；Phase 3 持久化根治 |
| R5 | 事件总线在 session 结束后仍推送（悬挂订阅） | 泄漏 | 复用 RuntimeEventBridge 的订阅/退订生命周期模式 |
| W1 | web client"在对话中查看"依赖 transcript 渲染可按 message_id 定位 | 追溯体验 | 实施时确认现有渲染消息节点是否保留 message_id；缺失则渲染层补 data-message-id 属性 |
| W2 | runtime server 形态下"当前会话"语义不存在 | aicli 专用默认值 | 契约强制 session 显式化；aicli 端 `?session_id=` 缺省取当前会话仅为本地便利 |
| W3 | TUI `/usage` 与 web client 共享同一 `LiveSource`，命令渲染期间数据仍在更新，总览与明细可能非同一时刻快照 | 数字轻微不一致（显示层） | Projector 单次查询持读锁返回一致快照；TUI 在输出头部标注统计时间戳，总览与明细差异不视为缺陷 |

---

## 14. 附录：字段来源映射

| CacheRequestRecord 字段 | 来源 |
|--------------------------|------|
| llm_request_id | observe Correlation 同源；事件载荷 request id |
| trace_id / turn_id / step | `llm.request.started/finished` 载荷（turn 字段名为 `logical_turn_id`，见 §5.1 命名勘误） |
| provider / model / stream | 请求开始事件 |
| usage.* | 载荷 `usage_prompt_tokens` / `usage_completion_tokens` / `usage_cached_tokens` / `usage_cache_read_tokens` / `usage_cache_creation_tokens` / `usage_cache_read_reported`（`internal/agent/loop.go:1838-1853`、`internal/chat/actor.go:4680-4706`） |
| cache_hit_ratio / cache_status | Collector 统一计算（语义对齐 `loop.go:1854-1865`，扩展 write 档） |
| cache_epoch / prompt_cache_key | 载荷 `prompt_cache_epoch` / `prompt_cache_key`（`loop.go:1393-1394`） |
| prompt_fingerprint | 载荷 `prompt_fingerprint`（`loop.go:1685-1692`，started 与 finished 均携带） |
| user_message_id / assistant_message_id | Correlation 表（§5.2），兜底 history metadata（`internal/types/message_id.go`） |
| provider_request_id | observe 投影（`internal/runtimeobserve/model.go:67`，尽力而为） |
| duration_ms / status / error_category | finished 载荷 |

---

## 15. 完整性审查记录（v1.1）

审查时间：2026-09-10。审查范围：后端架构、双前端（micro web client / frontend React）、aicli chat TUI 入口、实施与验收覆盖度。审查方法：对照代码库证据（`command.go`、`chat_slash_command_catalog.go`、`App.tsx`、`usage_normalizer.go`、`runtimeobserve`）逐项核对方案引用。

### 15.1 审查发现与处置

| # | 发现 | 处置 |
|---|------|------|
| F1 | TUI 入口原列为"未来扩展"且命名为 `/cache`，与需求（`/usage`）不符；且代码库中尚无 `/usage` 命令（`command.go:335-445` 分发表无此项，需全新实现而非扩展） | 新增 §6.4 正式设计：`/usage` 命令（目录注册 + 分发挂接 + 文本渲染），数据源进程内直调 `CacheAnalyticsSource`；§7.2、§11、§12 同步修正 |
| F2 | frontend 入口原描述为"实施时按现有导航结构定"，不具体 | §6.2 具体化：`App.tsx:39-41` 已有 `/usage`、`/usage/sessions/:sessionId`、`/analytics` 路由 → 缓存视图挂为 `/usage` 页内 tab，新增 `/usage/cache` 路由 |
| F3 | §3.2 运行形态表缺 TUI 形态 | 补 TUI 行（同进程直调 Source，无 HTTP 前缀） |
| F4 | 实施计划、测试、验收清单未覆盖 TUI | §11 Phase 1 增加第 5 项（TUI 命令）；§12 增加 TUI 测试与验收项 |
| F5 | 多前端一致性快照问题未显式记录（TUI 两次调用间数据可能更新） | §13 新增 W3 |
| F6 | TUI 命令分发/目录/输出的代码证据未盘点 | §2.2 补 3 行 TUI 复用点；§2.3 新增缺口 4 |

### 15.2 审查后覆盖度结论

| 维度 | 覆盖 | 依据 |
|------|------|------|
| 后端架构 | ✅ | §3 分层与挂载点、§5 采集/关联/聚合/Source/HTTP、§5.4 最小补丁 |
| micro web client | ✅ | §6.1 页签设计、§11 Phase 1 |
| runtime server | ✅ | §3.2 形态表、§5.5 Source、§11 Phase 2 |
| frontend React | ✅ | §6.2（路由已具体化为 `/usage` 页内 tab）+ §11 Phase 2 |
| aicli chat TUI | ✅ | §6.4 `/usage` 命令、§11 Phase 1 第 5 项、§12 验收 |
| 契约通用性 | ✅ | §4 `cache.analytics.v1`、§5.5 Source 接口、§7.1 保证机制 |
| 安全/性能/兼容 | ✅ | §8 / §9 / §10 |
| 风险与待确认 | ✅ | §13 R1-R5 + W1-W3 |

---

## 16. 可观测性验证记录（v1.2）

验证时间：2026-09-10。验证问题：① 按本方案能否观察到一个对话的**所有** LLM API 请求及其缓存信息；② 观察到某个请求缓存失效（miss）后，能否**反查**到相关请求历史与原始记录。方法：对照事件发布点与载荷的真实代码逐项核实。

### 16.1 问题①结论：能覆盖"所有请求"，含失败请求；三个已声明边界

**成立的前提（已逐项核实）**：

| 前提 | 证据 | 结论 |
|------|------|------|
| 每次 provider 调用前发 started | `loop.go:1693`（agent loop）、`skills/handler.go:7354`（runtime server 技能路径） | ✅ 两条运行路径均发布 |
| 失败请求也有终态事件 | `loop.go:1736-1785`：`err != nil` 时发布 `finished`，载荷含 `success:false`/`error`/`error_code`/`retryable`/`next_action` | ✅ "所有请求"包含失败请求，记录 `status=error` + `error_category` |
| usage 缺失不伪造 | `loop.go:1834` 仅在 `response.Usage != nil` 时写 `usage_*`；Collector 对缺省载荷置 `not_reported`（§5.1） | ✅ overview 用 `requests_with_usage`/`requests_cache_reported` 自报覆盖率（§4.2） |
| 同一逻辑请求内的重试可归并 | 参数降级/升级重 Call（`loop.go:1695-1710/1712-1735`）不换 `llm_request_id`，降级/升级事件可作 attempt 链证据 | ✅ 聚合取最终成功 Call，明细可展开 |

**三个边界（均为设计内行为，UI 必须可见）**：

1. **环形缓冲上限**（§5.3）：每 session 保留最近 1000 条；超长会话更早请求被淘汰，overview 降级为"窗口口径"并标 `coverage.partial=true`。Phase 3 sqlite 持久化后消除。
2. **进程重启后 per-request 粒度降级**：内存投影丢失，HistorySource 从会话历史重建；历史消息只带**每条消息**的 usage 元数据（`appendSessionActorUsagePayload` 写入持久化载荷，`actor.go:4680-4713`），同 turn 多次请求/重试的 per-request 粒度不可恢复，记录标 `correlation_source: "history_inferred"`。
3. **崩溃/强杀时 in-flight 记录悬挂**：started 已登记但 finished 永不到达。Projector 在 session 结束事件时将未终态的 in-flight 记录落为 `status=error`、`error_category="interrupted"`（实施时补充此规则，见 §16.3）。

### 16.2 问题②结论：反查链路成立，四级证据可达

以一条 `cache_status=reported_zero`（缓存失效）的请求为起点：

| 级别 | 反查动作 | 数据来源 | 可回答的问题 |
|------|----------|----------|--------------|
| L1 同类定位 | `GET {base}/cache/requests?cache_status=reported_zero`（§4.4 过滤参数） | Projector 环形缓冲 | 这个会话还有哪些请求也没命中？ |
| L2 同上下文归因 | 记录的 `trace_id`/`turn_id` → 同 turn 请求列表；`cache_epoch`/`prompt_cache_key` 对比前后请求 | §5.2 关联表（turn → [llm_request_id] 1:N） | 是缓存代际切换导致的吗？前一次 write 是谁？ |
| L3 消息链追溯 | `assistant_message_id` → `GET {base}/cache/messages/{message_id}/trace`（§4.3）→ `produced_by` + `consumed_by` | Correlation 表 + 兜底 history 匹配 | 这次 miss 的产出消息，后来被哪些请求消费？收益本该在哪兑现？ |
| L4 原始记录 | `user_message_id`/`assistant_message_id` → 会话历史全文（`/history`，`history_available=true`）；web client"在对话中查看"定位（§6.1）；`provider_request_id` → observe 平面/provider 控制台；`prompt_fingerprint` → 比对"提示词前缀相同却未命中"的请求对 | 会话历史 + observe + started 载荷指纹（`loop.go:1685-1692`） | 当时的对话原文是什么？和命中请求的输入差在哪？ |

**"原始记录"的口径（重要）**：v1 记录**不含** prompt/completion 原文（§5.1 脱敏设计）。"原始记录"指：① 会话历史消息全文（含 usage 元数据）；② observe 平面的请求级元数据（provider_request_id、耗时、错误分类）；③ Phase 3 的 debug.log StepUsage（`LogFileSource`，§5.5）。**provider HTTP 报文本体不在 v1 范围**——需要时经 `provider_request_id` 到 provider 控制台核对，或另行扩展 observe 的请求体日志（超出本方案范围）。

### 16.3 本轮修订清单

| # | 修订 | 位置 |
|---|------|------|
| C1 | 事件名勘误：实际发布为点号名 `llm.request.started/finished`（下划线常量仅为消费端兼容别名）；Collector 订阅匹配点号名 | §5.1、§14 |
| C2 | turn 字段名勘误：载荷字段为 `logical_turn_id`（消费端读取示例 `chat_runtime_events.go:5285`），即本方案的 turn_id | §5.1、§5.2、§14 |
| C3 | 重试机制修正：无独立 `llm_retry` 事件；attempt 链证据为 `llm.request_parameter.downgraded`/`llm.parallel_tool_calls.downgraded`/`llm.max_output_tokens.escalated`（同一 llm_request_id 内重 Call） | §5.1 |
| C4 | 新增 `prompt_fingerprint` 字段：started 载荷已含（`loop.go:1685-1692`），用于"前缀相同却未命中"的反查证据 | §4.1、§14 |
| C5 | 新增 in-flight 悬挂处理规则：session 结束时未终态记录落为 `status=error, error_category="interrupted"`（实施项） | §16.1 边界 3 |

### 16.4 验证后结论

- **问题①**：✅ 能。started/finished 双事件覆盖成功与失败请求，usage 缺失以 `not_reported` 显式呈现且 overview 自报覆盖率；边界为环形缓冲窗口、重启后粒度降级、in-flight 悬挂（三者均有 UI 可见的降级标记）。
- **问题②**：✅ 能。`cache_status` 过滤 → turn/epoch 归因 → MessageTrace 双向链（produced_by/consumed_by）→ 会话历史原文 + observe 元数据 + prompt_fingerprint 比对，四级反查路径在 v1 契约内闭环；provider 报文本体除外（经 provider_request_id 外部核对）。
