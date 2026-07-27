# Session Usage Analytics and Agent Diagnostics Plan

> 调查日期：2026-07-27
> 调查范围：`session_history.sqlite`、`session_runtime.sqlite`、aicli `chat_*.json`、`runtime-http/*.json`、runtime events、runtime logs、现有前端路由和 API。
> 目标：在前端提供可信的会话级、轮次级 Token 使用分析，并给出 Agent、工具、重试和错误诊断。

## 0. 2026-07-27 实施状态

首个低敏、只读 MVP 已完成，并已使用本机 `~/.aicli/chat-logs` 的真实历史数据验证。

### 已交付

- 新增 `backend/internal/chataloganalytics`：解析 `chat_*.json` 和 `debug.log`，按 session、trace/用户轮次、LLM request 聚合 Token、错误、耗时、上下文利用率、工具结果与数据覆盖率。
- 新增持久化索引 `~/.aicli/sessions/runtime/usage_analytics.sqlite`，包含 `analytics_sessions`、`analytics_turns`、`analytics_llm_requests`。索引使用自然键去重、文件指纹增量刷新和解析器版本指纹；重复初始化和重复 upsert 已有自动化测试。
- 新增 `/api/runtime/analytics` 下的 overview、summary、sessions、session detail/usage/turns 接口。接口沿用 usage admin 授权，loopback 可访问，远程调用需要 admin token/role。
- API 只返回低敏聚合、稳定错误分类和诊断 code，不返回 prompt、response、命令、原始错误、完整本地路径或 Base URL；session ID 已做路径穿越防护。
- chat payload 超过 16 KiB 时不再整体丢弃诊断字段；截断 envelope 保留事件类型、关联 ID、provider/model、success、step、`usage_*`、上下文预算和 `error_present`。
- `TokenUsage` 与 `session_end` 已传播 `usage_source`，可区分 `provider_reported`、`local_estimate` 和 `mixed`。
- 新增前端 `/usage` 与 `/usage/sessions/:sessionId`：支持时间、provider、model、目录、状态、搜索筛选，URL 状态、KPI、分组趋势、会话分页、数据质量、Agent 诊断、用户轮次及逐 LLM 请求 Token 明细。
- Workspace topbar 已增加“使用分析”入口；中英文 i18n 已补齐。
- 逐请求详情最多渲染最近 250 条，宽表在移动端保留局部横向滚动，根文档在 390px 视口无横向溢出。

### 实测结果

本机首次为 625 个历史会话建立索引约 3.2 秒；3 秒刷新窗口内的缓存 overview 约 43 毫秒，触发增量检查时约 440-470 毫秒。当前样本聚合约 7.38 亿 Token、超过 12,000 个 LLM 请求和 226 个 LLM 错误，逐请求 Token 覆盖率约 98.1%。原始日志被轮转后，已写入索引的低敏事实仍可查询。

### 当前边界

本次交付对应后文“推荐的首个交付切片”，不是 Phase 4/5 的全部能力。HTTP attempt/retry 时间线、subagent lineage、evidence reveal、导出任务、tenant/user scope、cursor 分页、retention job 和实时 recorder 仍属于后续阶段。当前历史回填以 chat/debug 为主，不解析高敏 HTTP body；工具错误率只统计现有日志保留窗口内可观察到的工具结果，并通过 coverage/partial 明示证据边界。

## 1. 结论摘要

当前系统已经采集了相当多的诊断数据，但这些数据分散在不同层级，保留策略、可靠性和权限边界也不一致，**还不能直接由前端拼装成可信的使用分析页面**。

核心结论如下：

1. **每个用户轮次的 Token 总量已经有结构化持久化来源。**
   `session_runtime.sqlite.session_events` 中的 `session_end` 事件按 `session_id + turn_id` 标识一次 Agent 运行，包含 prompt、completion、total、cached、cache read、cache creation、reasoning 等累计 Token 字段。Agent loop 会把该轮内部多次 LLM 调用的 usage 累加到 `Result.Usage`，因此 `session_end` 适合作为“轮次总量”的权威来源。
2. **每次内部 LLM 请求的 Token 明细已采集，但并未完整、统一地持久化。**
   `llm.request.finished` 事件拥有最完整的逐请求 usage、provider/model、step、usage source、cache status 和上下文预算字段。aicli chat log 也会保存这些字段。然而 runtime event bus 只保留内存中的最近 2048 条/8 MiB，当前 session event bridge 并不持久化所有 `llm.request.*` 事件；chat log 又是本地调试文件，不适合作为浏览器主数据源。
3. **HTTP artifact 能诊断请求、响应、失败和重试，并可从 provider 响应中恢复 usage，但不能作为统计主表。**
   它保存 attempt、status code、error、retry reason、error code、delay、路由/fallback、多个关联 ID 以及请求/响应 body。成功流式响应中的 `response.completed.response.usage` 确实包含 Token。但是 HTTP capture 可关闭、body 可截断、文件按 256 个/64 MiB 淘汰，而且含完整 prompt、工具定义、工具参数和响应内容。
4. **是否出错可以判断，但错误语义目前不够统一。**
   `session_end` 有 `success`、`error`、`tool_error_count`、`recovered_tool_error_count`、`unrecovered_tool_error_count`；HTTP artifact 有 status/error/retry；工具运行时有结构化 `outcome` 和 `error_code`。但部分历史 `session_end` 只有自由文本 error，缺少稳定的 error category/code。
5. **现有 `/api/runtime/usage/*` 不能直接复用为会话 Token 分析。**
   该 API 和 `token_usage_history` 当前服务于 skills runtime 的 quota/ledger；记录中没有 session/turn/LLM request 的完整关系。`/api/runtime/sessions/stats` 只统计会话状态、消息数和 tags。
6. **正确方案是新增服务端 analytics read model。**
   在事件产生时写入低敏、结构化、可索引的 turn/request/tool/attempt facts，并由服务端提供聚合 API。浏览器只读聚合契约；原始 HTTP/chat/debug 内容只作为受控的高级证据。

## 2. 本次实际数据检查

### 2.1 `session_runtime.sqlite`

当前本机数据库：`C:\Users\vince\.aicli\sessions\runtime\session_runtime.sqlite`。

检查结果：

| 项目 | 当前值 | 说明 |
| --- | ---: | --- |
| session events | 3,162 | 结构化 JSON payload |
| 有事件的 session | 152 | 其中 144 个有 `session_end` |
| `session_start` | 751 | 一次 Agent 轮次开始 |
| `session_end` | 740 | 一次 Agent 轮次结束 |
| 带 usage 的 `session_end` | 590 / 740，79.7% | 成功轮次覆盖 99.5%，失败轮次覆盖 56.9% |
| 持久化轮次 Token 合计 | 749,621,058 | 是多步 Agent 调用的实际累计消耗，不是当前上下文大小 |
| event retention | 每 session 默认 2,048 条 | 每 256 个 sequence 执行一次裁剪 |
| 当前单 session 最大事件数 | 318 | 当前样本尚未触发 2,048 上限 |
| tool receipts | 0 | 当前本机样本不能依赖 receipt 做历史工具统计 |

`session_end` 当前高频字段：

- 所有 740 条都有：`turn_id`、`trace_id`、`success`、`error`、`steps`、`duration`、`resume`。
- 590 条有：`usage_prompt_tokens`、`usage_completion_tokens`、`usage_total_tokens`。
- 492 条有：`usage_cache_read_reported`。
- 486 条有：`usage_cached_tokens`、`usage_cache_read_tokens`。
- 489 条有：`usage_reasoning_tokens`。
- 239 条有：`tool_error_count`。
- 210 条有：`recovered_tool_error_count`。
- 67 条有：`unrecovered_tool_error_count`。

注意：`session_end` 的 usage 是该 Agent 轮次内部所有 LLM step 的累加值，不应再与同一轮的 `llm.request.finished` usage 相加。

### 2.2 `session_history.sqlite`

当前本机数据库：`C:\Users\vince\.aicli\sessions\session_history.sqlite`。

- 171 个 session，26,906 条 message。
- `sessions` 表保存 user/state/title/summary/message count/tags/metadata/time，但没有累计 Token、错误率或诊断列。
- `session_messages` 保存完整消息或 artifact 引用，并有 role、tool call count、tool result 等投影，适合补充“消息数/工具消息数”，不适合从文本反推 Token 或错误。
- session 列表 API 当前没有时间、provider、model、status、workspace、分页等分析筛选能力。

### 2.3 最新 aicli chat log 样本

检查样本：`20260727_073349.024_dbc55eea`。

- 1 个用户轮次包含 26 次内部 LLM request/response、82 次 tool call/result。
- 25 个 response 保存了逐请求 `usage_*`，均标记为 `provider_reported`，并含 cache hit 状态。
- chat summary 保存 `total_requests=26`、`total_responses=26`、`total_tool_calls=82`、`total_tokens=1,358,380`、平均响应时间和会话持续时间。
- 可见 response usage 合计为 1,314,660；与 summary 相差 43,720。原因是一个超过 16 KiB 的 response content 被 `boundChatLogContent` 整体替换为 `{truncated, byte_count, preview}`，summary 在截断前已累计 usage，导致总量仍在，但该请求的结构化 usage 明细丢失。

因此 chat summary 可做离线校验，但 chat log 不是理想的在线 read model。后续必须“先提取诊断字段，再裁剪大内容”，不能把整份 payload 一起替换掉。

### 2.4 最新 HTTP artifact 样本

同一最新样本中有 55 个 HTTP artifact：28 个 request、27 个 response。成功 Codex 流式 response 的 `body_text` 中存在 `response.completed` 事件：

- 示例 1：15,053 input + 939 output = 15,992 total，cached 128，reasoning 95。
- 示例 2：54,334 input + 2,531 output = 56,865 total，cached 128，reasoning 53。

最近 6 个有数据的 artifact 目录共检查到 863 个文件：

- request 411 个；response 410 个；retry 42 个。
- retry artifact 都带 `retry_reason` 和 `error_code`，例如 `http_5xx_retry`、`UPSTREAM_UNAVAILABLE`，同时带 attempt/max attempts/delay 和关联 ID。
- 同一个逻辑 LLM request 的多个 retry 共用 `logical_turn_id + llm_request_id`，每次 attempt 有独立 `retry_attempt_id + provider_request_id`。

HTTP artifact 的请求 body 会包含 input、instructions、tools、prompt cache key 等，响应 body 会包含模型输出和工具调用参数；它属于高敏调试证据，不应默认返回给普通分析页面。

### 2.5 现有离线诊断基线

已有脚本 `scripts/analyze-aicli-session-history.ps1` 对最近 30 个有效会话（2026-07-26 14:16 至 23:11）给出的基线是：

| 指标 | 数值 |
| --- | ---: |
| tool results | 3,313 |
| tool hard errors | 40，1.21% |
| tool non-fail rate | 98.79% |
| LLM responses | 1,057 |
| LLM failures | 45，4.26% |
| unmatched tool calls | 0 |
| `update_goal` no-op | 4 |

该结果适合作为首版 analytics rebuild 的对照样本，但不能直接当成线上长期指标：它基于本地文件窗口、受日志保留/截断影响，并且统计范围与 runtime-server 的进程级 metrics 不完全相同。

## 3. 当前数据源能力矩阵

| 数据源 | 已有关键字段 | Token 粒度 | 错误/诊断能力 | 保留和完整性 | 前端定位 |
| --- | --- | --- | --- | --- | --- |
| `session_history.sqlite` | session、user、状态、标题、workspace context、消息 role、tool message 投影 | 无稳定 usage | 可看消息结构，不能可靠判断执行错误 | 长期持久化；历史可能被 compact/backtrack 重写 | 会话目录、标题、时间和归属的辅助源 |
| `session_runtime.sqlite/session_events` | session/seq/type/trace/agent/tool/payload/time | `session_end` 有轮次累计 usage | 轮次成功、error、tool error/recovery、compaction、approval、subagent | 每 session 默认只保留最近 2,048 条；当前并未持久化全部 LLM/tool 事件 | 当前最可靠的轮次事实源，但需改造保留和索引 |
| runtime event bus | 完整 `llm.request.*`、`tool.*`、context、retry、subagent、patch 等事件 | 最完整的逐 LLM request usage | 最完整的实时 Agent 诊断 | 仅内存最近 2,048 条且受 8 MiB 限制；重启丢失 | 只用于实时刷新，不能做历史主源 |
| aicli `chat_*.json` | request/response/tool call/tool result/turn/request/tool ID、duration、summary | 逐 LLM request 和文件 summary | tool error、响应 error、原始 SSE 尾部 | 每文件最多 1,024 message；content 16 KiB、raw 32 KiB；多 provider 文件需去重 | CLI 离线审计和数据回填，不让浏览器直接扫描 |
| `runtime-http/*.json` | request/response/retry、status、error code、route、attempt 和 body | 可从协议 body 解析 | provider/网络/HTTP/retry/fallback 证据最佳 | capture 可关闭；256 文件/64 MiB；body 可截断和淘汰 | 管理员高级证据，不是分析事实源 |
| runtime service log | level/module/message/request/trace/session/provider/model/status/upstream error | 通常无稳定 usage | 适合服务异常、关联排查 | API 最多读最近 8 MiB；受日志轮转影响 | 与分析页互相跳转，不参与核心计数 |
| `/usage/stats`、usage ledger | skill entrypoint、scope、quota、prompt/completion/total | skills runtime 调用 | success/status code | stats 主要在内存；ledger 可选开启 | 保留为 quota/skills usage，不能冒充 chat usage |
| tool efficiency metrics | outcome、error code、preflight、repeat、失败分类 | 无 | 全局工具效率强 | 进程级累计、无 session/tool name 维度、重启归零 | 全局运行健康参考，不能替代会话诊断 |

## 4. 关键缺口与风险

### 4.1 数据完整性缺口

1. `llm.request.finished` 没有统一持久化，所以逐 LLM 请求明细在 runtime-server 和 aicli 两条执行链上不一致。
2. `session_end` 没有保留 `usage_source` 和完整 cache quality，无法判断数字来自 provider 还是 local estimate。
3. chat content 超过 16 KiB 后会整体降级为 preview，连同 usage/diagnostic 字段一起丢失。
4. HTTP response usage 藏在不同 provider 协议的 JSON/SSE body 中，不能靠前端临时解析。
5. session event 默认按条数裁剪；长会话早期 usage 会被删除，导致“会话总量”随时间反向下降。
6. `session_events` 只有 `(session_id, seq)` 索引，不适合按 user、workspace、时间、provider、model、outcome 做全局分析。
7. 当前 `session_start` 与 `session_end` 数量不相等，必须明确 running/interrupted/missing terminal 的 partial 状态，不能默认当作失败或成功。

### 4.2 计数与去重风险

必须区分以下五个实体，UI 和 API 不得都称作“轮”：

| 实体 | 推荐 ID | 含义 |
| --- | --- | --- |
| session | `session_id` | 一条可恢复会话 |
| user turn / agent run | `session_id + turn_id` | 一次用户提交触发的完整 Agent 运行 |
| LLM request | `session_id + llm_request_id` | ReAct 内部一次模型调用；同一 turn 可有多次 |
| HTTP attempt | `retry_attempt_id`，后备为 request+attempt | 一个 LLM request 的一次 provider 尝试 |
| tool invocation | `session_id + tool_call_id` | 一次工具调用，requested/completed 是同一事实的两个阶段 |

禁止以下错误聚合：

- 把 `session_end.usage_total_tokens` 与其内部所有 `llm.request.finished` 再次相加。
- 把 request、response、retry artifact 都计为 LLM request。
- 把每次 HTTP retry 当成独立用户轮次或独立成功请求。
- 同时累计 child session 的 `session_end` 和 parent 上镜像的 `subagent.completed` usage。
- 将 cache 字段缺失解释为 cache 0；“provider 未报告”和“明确报告为 0”必须分开。
- 将没有 usage 的失败轮次当成消耗 0；应显示 `unavailable`，总量下界与覆盖率分开报告。

### 4.3 错误语义风险

“出错”至少需要四层状态：

1. `turn_outcome`：success / failed / interrupted / running / partial。
2. `llm_outcome`：success / provider_error / network_error / cancelled / exhausted_retries / incomplete_stream。
3. `tool_outcome`：success / empty / partial / failed / denied / replayed。
4. `recovery_outcome`：not_needed / recovered / unrecovered / retrying / fallback_used。

一个 turn 可以最终成功，但中间发生多次 retry 或 recovered tool error；这类情况必须显示为“成功，有恢复”，不能被简单成功率掩盖。

## 5. 指标定义

### 5.1 Token 指标

每个指标返回数值的同时返回质量字段：

```json
{
  "value": 15992,
  "quality": "provider_reported",
  "coverage": 1.0,
  "complete": true
}
```

`quality` 取值：

- `provider_reported`：provider 明确返回。
- `local_estimate`：本地 tokenizer/字符估算。
- `mixed`：聚合范围同时包含 reported 和 estimated。
- `unavailable`：没有数据，不能显示为 0。

核心 Token 指标：

- `prompt_tokens`、`completion_tokens`、`total_tokens`。
- `cached_tokens` / `cache_read_tokens`、`cache_creation_tokens`。
- `reasoning_tokens`。
- `cache_hit_ratio = cache_read_tokens / prompt_tokens`，只在 cache read 被明确报告时计算。
- `context_prompt_tokens` 与 `context_window_tokens` 用于上下文压力，不计入账单 Token 总量。
- `compaction_tokens` 单独归类为 `operation_kind=compaction`，同时明确是否已纳入 turn usage。

### 5.2 会话与轮次指标

- `session.direct_tokens`：该 session 自身去重后的 terminal turn usage 之和。
- `session.descendant_tokens`：子 Agent session 的直接用量之和。
- `session.inclusive_tokens`：direct + descendant；只有 lineage 完整时才给出 complete=true。
- `turn.request_count`：去重 LLM request 数，不是 HTTP attempt 数。
- `turn.attempt_count`：provider attempt 数。
- `turn.retry_count = attempt_count - request_count`，下限为 0。
- `turn.tool_call_count`：唯一 tool call 数。
- `turn.duration_ms`：Agent turn 总耗时；另提供 LLM/tool duration 分布，不能相加推导 wall time，因为可能并行。
- `turn.usage_reconciliation_delta`：turn aggregate 与 LLM request 明细之差，用于发现缺失/重复，不直接展示成用户费用。

### 5.3 错误率与恢复率

- `turn_failure_rate = failed terminal turns / terminal turns`。
- `llm_request_failure_rate = failed logical LLM requests / completed logical LLM requests`。
- `attempt_failure_rate = failed HTTP attempts / all HTTP attempts`，必须明确这是重试层指标。
- `tool_hard_error_rate = failed tool calls / completed tool calls`。
- `tool_non_success_rate = (empty + partial + failed + denied) / completed tool calls`。
- `recovery_rate = recovered incidents / recoverable incidents`。
- `retry_success_rate = 最终成功且发生 retry 的 LLM requests / 发生 retry 的 LLM requests`。
- `terminal_without_usage_rate = terminal turns with unavailable usage / terminal turns`。

所有比例都返回 numerator、denominator 和 sample size；样本数过小时只显示原始计数，不生成“健康/不健康”结论。

### 5.4 Agent 效率诊断

首版规则使用稳定字段，不分析 prompt 文本：

- 重复工具调用：同一 turn 内 `tool_name + normalized_args_fingerprint` 重复。
- 无效重复：连续 empty/partial/failed 后仍用相同参数重试。
- 工具失败分类：shell compatibility、path missing、invalid args、timeout、stale context、spawn depth、permission、network、execution、not found、other。
- LLM 恢复：retry、parameter downgrade、model/provider fallback、stream reconnect、exhausted retries。
- 上下文压力：prompt/context window 比例、preflight compaction、mid-turn compaction、compaction failed。
- Agent 限制：step limit、tool-call limit、doom-loop warning/termination、completion requirement 未满足。
- 子 Agent：spawned/completed/failed、route provider/model、usage、depth limit、父子用量占比。

诊断输出必须包含证据计数和 event/turn 引用，例如 `code=PATH_MISSING_REPEAT, severity=warning, affected_turns=3`，不能只输出自然语言评价。

## 6. 目标架构

### 6.1 数据流

```text
Agent / LLM / tool / provider retry events
                 |
                 v
        AnalyticsRecorder（共享库）
          |                 |
          |                 +--> 现有 event bus / debug artifact
          v
usage_analytics.sqlite（低敏结构化事实）
          |
          v
AnalyticsQueryService（聚合、权限、质量、诊断规则）
          |
          v
/api/runtime/analytics/*
          |
          v
React Query/hooks -> 全局分析页 / 会话详情页 / 高级证据跳转
```

设计原则：

1. Recorder 位于共享 runtime 层，aicli 本地 host 和 runtime-server 使用同一套写入逻辑。
2. 统计事实在 payload 被截断、日志被轮转之前提取。
3. 所有写入按自然键 upsert，允许事件重放，不产生重复计费。
4. analytics store 不保存 prompt、response 正文、tool args、tool output、URL query 或 error 原文。
5. runtime event/chat log/HTTP artifact 继续存在，但降级为证据源和回填源。
6. 聚合 API 明确返回 coverage、quality、partial reasons 和 schema version。

### 6.2 存储选择

建议新增：

`~/.aicli/sessions/runtime/usage_analytics.sqlite`

不直接复用 `session_events` 的原因：

- `session_events` 有每 session 2,048 条裁剪语义，analytics 总量不能随裁剪丢失。
- analytics 需要跨 session 的 user/workspace/time/provider/model 索引。
- 原始事件 payload 体积大，而分析事实应是低敏、窄表、可长期保留。
- 独立迁移、备份、retention 和重建更容易，不影响 session actor 的实时状态存储。

SQLite 使用 WAL、busy timeout 和单写队列；批量 flush 不超过短事务。未来更换集中式数据库时，保持 QueryService/API 契约不变。

### 6.3 事实表

#### `analytics_sessions`

- `session_id` primary key
- `user_id`、`workspace_id`、`workspace_path_hash`
- title 可选；默认不存 summary
- `created_at`、`updated_at`、`last_activity_at`
- `state`、`provider_last`、`model_last`
- `data_start_at`、`analytics_complete_from_start`
- `source`：runtime / aicli_backfill / mixed

workspace 默认保存稳定 hash 和用户可见 display label。只有已有权限模型允许时才返回原始路径。

#### `analytics_turns`

- natural key：`(session_id, turn_id)`
- `trace_id`、`parent_turn_id`、`started_at`、`completed_at`、`duration_ms`
- `outcome`、`error_code`、`error_category`、`cancel_source`
- `steps`、`limit_reached`、`limit_reason`
- prompt/completion/total/cached/cache read/cache creation/reasoning tokens
- `usage_quality`、`usage_complete`、`cache_read_reported`
- tool total/failed/empty/partial/recovered/unrecovered
- LLM request/attempt/retry counts
- `operation_kind`：user_turn / resume / compact / system

#### `analytics_llm_requests`

- natural key：`(session_id, llm_request_id)`
- `turn_id`、`trace_id`、`step`、`stream_id`
- requested/effective provider、model、reasoning effort
- `started_at`、`completed_at`、`duration_ms`、`outcome`
- usage 全字段、`usage_quality`、cache status
- context prompt/window、prompt budget、preflight before/after
- attempt/retry count、fallback used/reason
- `terminal_error_code`、`terminal_http_status`

#### `analytics_http_attempts`

- natural key：`retry_attempt_id`；缺失时使用 `(llm_request_id, attempt)`
- session/turn/LLM request/provider request/stream 关联 ID
- attempt/max attempts、started/completed/duration
- status code、outcome、error code/category
- retry reason/delay、fallback route 元数据
- request/response byte count、captured/truncated 标记
- `evidence_ref` 可选，仅保存不可猜测的 artifact ID，不保存文件系统绝对路径

该表来自 provider wrapper 的结构化 debug callback，不从落盘 HTTP body 反向解析。即使 raw HTTP capture 关闭，也应记录低敏 attempt 元数据。

#### `analytics_tool_calls`

- natural key：`(session_id, tool_call_id)`
- turn/trace/step/tool name/source
- started/completed/duration
- outcome：success / empty / partial / failed / denied / replayed
- error code/category、retryable、recovered
- requested/succeeded/failed item count
- args fingerprint；不得保存 args preview、command、path 或正文
- artifact count、result truncated、parallel group 可选

#### `analytics_agent_edges`

- parent/child session、parent turn、agent name/type/role/difficulty
- route provider/model/reasoning effort/source
- started/completed、outcome、depth
- mirrored event ID 和 usage authority

#### `analytics_diagnostics`

- deterministic ID：`hash(rule_version + scope + evidence keys)`
- scope type/id：global / session / turn / request / tool
- code、category、severity、status
- evidence count、first/last seen、related IDs
- params JSON 仅允许白名单低敏标量
- rule version、generated at、resolved at

#### `analytics_daily_rollups`

- day + user/workspace/provider/model 维度
- session/turn/request/attempt/tool 计数
- Token 分项、成功/失败/恢复计数
- usage coverage 和 source 计数

日聚合用于全局趋势；详情仍从 facts 查询。rollup 必须可由 facts 重建。

### 6.4 索引

至少添加：

- session facts：`(user_id, last_activity_at DESC)`、`(workspace_id, last_activity_at DESC)`。
- turns：`(session_id, started_at)`、`(outcome, started_at)`。
- LLM requests：`(session_id, turn_id, step)`、`(provider, model, started_at)`、`(outcome, started_at)`。
- attempts：`(llm_request_id, attempt)`、`(error_code, started_at)`。
- tools：`(session_id, turn_id)`、`(tool_name, outcome, started_at)`、`(error_code, started_at)`。
- diagnostics：`(scope_type, scope_id, severity, last_seen_at DESC)`。
- rollup：覆盖 API 常用 group-by 的复合主键。

所有全局时间范围查询必须有 `from/to` 上限；默认 7 天，最大 90 天。更长范围只查 daily rollup。

### 6.5 写入与对账

1. turn start 创建 running row；turn end 更新 terminal outcome 和累计 usage。
2. `llm.request.started/finished` upsert request row；provider retry callback upsert attempt row。
3. `tool.requested/completed/denied/replayed` upsert tool row。
4. 每个 terminal turn 执行 reconciliation：
   `sum(unique llm request usage)` 对比 `session_end aggregate usage`。
5. 默认以 `session_end` 为 turn total authority；LLM request sum 用于明细。如果差值非零，标记 `usage_reconciliation_mismatch` 和 partial reason。
6. subagent 以 child session turn usage 为 authority；parent mirror 只建立 lineage。child facts 缺失时，才把 `subagent.completed` usage 标记为 fallback estimate。
7. 正在运行的 turn 可显示 request 明细的 provisional sum，但不并入“已结算会话总量”，直到 terminal 或显式超时收敛。

### 6.6 历史回填

提供一次性 CLI：

```text
aicli analytics rebuild --source session-events,chat-logs,http-artifacts
```

回填优先级：

1. session metadata 和 `session_end` 建立 session/turn 总量。
2. chat logs 建立逐 LLM/tool 明细，按 `turn_id/request_id/tool_call_id` 去重。
3. HTTP artifacts 只补 attempt、retry、provider error 和缺失 usage，不覆盖更高质量的 runtime facts。
4. 标记 `backfilled=true`、source、coverage 和最早可信时间。

回填不得要求前端访问本地文件；由服务端或 CLI 离线执行。扫描时忽略 Authorization，error/prompt/body 不写入 analytics store。

## 7. 后端 API 设计

统一前缀：`/api/runtime/analytics`，所有响应带：

- `schema_version`
- `generated_at`
- `data_window`
- `coverage`
- `partial`、`partial_reasons`
- `filters`

### 7.1 全局概览

`GET /api/runtime/analytics/overview`

Query：`from`、`to`、`user_id`、`workspace_id`、`provider`、`model`、`outcome`、`include_descendants`、`timezone`。

返回：

- Token totals 和 usage quality distribution。
- session/turn/LLM request/attempt/tool counts。
- turn、LLM request、HTTP attempt、tool 四层错误率。
- retry/recovery/cache/compaction/subagent 汇总。
- 按天趋势和 provider/model breakdown。
- Top diagnostics 仅返回 code、severity、count，不返回敏感内容。

### 7.2 会话分析列表

`GET /api/runtime/analytics/sessions`

额外 query：`query`、`sort`、`direction`、`cursor`、`limit`。默认 limit 50，最大 200。

每行返回：

- session identity、title、user/workspace display、last activity。
- direct/inclusive Token、usage quality/coverage。
- turns、LLM requests、retries、tool calls。
- outcome、error/recovery counts、最高诊断级别。
- `analytics_complete_from_start` 和 partial reasons。

采用稳定 cursor：`(sort_value, session_id)`；不能用 offset 扫描大表。

### 7.3 单会话概览

`GET /api/runtime/analytics/sessions/{sessionId}`

返回：

- 概览指标、direct/descendant/inclusive usage。
- provider/model/time breakdown。
- error/recovery/cache/context/compaction/subagent 摘要。
- 数据完整性和 reconciliation 状态。
- 最近诊断列表。

### 7.4 轮次列表与详情

```text
GET /api/runtime/analytics/sessions/{sessionId}/turns
GET /api/runtime/analytics/sessions/{sessionId}/turns/{turnId}
GET /api/runtime/analytics/sessions/{sessionId}/turns/{turnId}/llm-requests
GET /api/runtime/analytics/sessions/{sessionId}/turns/{turnId}/tools
```

轮次列表默认按 started_at DESC cursor 分页。详情返回 attempt timeline、并行 tool group、usage reconciliation 和相关 diagnostics。

### 7.5 诊断

```text
GET /api/runtime/analytics/diagnostics
GET /api/runtime/analytics/sessions/{sessionId}/diagnostics
```

支持 severity/category/code/status/time 筛选。诊断文本由前端 i18n 根据稳定 code + params 渲染，API 不返回包含路径、命令或 provider body 的自由文本。

### 7.6 高级证据

```text
GET /api/runtime/analytics/evidence/{evidenceId}/metadata
POST /api/runtime/analytics/evidence/{evidenceId}/reveal
```

- metadata 只返回 phase、time、status、size、truncated、关联 ID 和是否可用。
- reveal 需要 usage admin 权限、明确审计记录、短时 token，并执行字段级脱敏。
- 默认不提供 bulk raw download；确有需要时走独立管理员导出任务。
- 普通用户只能从分析详情跳到现有 `/logs`，并自动带 session/request/trace 查询参数，不能读取 raw HTTP artifact。

### 7.7 维度与导出

```text
GET  /api/runtime/analytics/dimensions
POST /api/runtime/analytics/exports
GET  /api/runtime/analytics/exports/{jobId}
```

dimensions 返回当前权限和时间范围可见的 user/workspace/provider/model/outcome 选项。导出默认 CSV/JSONL，仅含分析事实；禁止导出 prompt/body/args/error 原文。

### 7.8 响应示例

```json
{
  "schema_version": 1,
  "session": {
    "id": "session-id",
    "title": "会话标题",
    "last_activity_at": "2026-07-27T07:42:47Z"
  },
  "usage": {
    "direct": {
      "prompt_tokens": 1293951,
      "completion_tokens": 20709,
      "total_tokens": 1314660,
      "cached_tokens": 725248,
      "reasoning_tokens": 858,
      "quality": "provider_reported",
      "coverage": 0.962,
      "complete": false
    },
    "descendants": {
      "total_tokens": 0,
      "quality": "unavailable",
      "coverage": 0,
      "complete": false
    }
  },
  "execution": {
    "turns": 1,
    "llm_requests": 26,
    "http_attempts": 26,
    "retries": 0,
    "tool_calls": 82
  },
  "outcomes": {
    "terminal_turns": 1,
    "failed_turns": 0,
    "recovered_incidents": 1
  },
  "partial": true,
  "partial_reasons": ["one_llm_request_usage_detail_truncated"]
}
```

## 8. 权限和隐私

### 8.1 角色

- 普通用户：仅查看自己 session 的低敏聚合和诊断 code。
- project/tenant operator：查看授权 scope 的聚合，默认不见原始内容。
- usage admin：跨用户筛选、查看 provider/model 维度、申请高级证据 reveal。
- system admin：配置 retention、执行 rebuild/export、查看审计记录。

不能继续依赖任意 `?user_id=` 作为授权。query 参数只是筛选条件，实际 scope 必须由认证 identity、tenant/project/user claim 和 admin role 计算。

### 8.2 数据分级

| 级别 | 示例 | 默认策略 |
| --- | --- | --- |
| L0 聚合 | Token、计数、比例 | 允许所属用户查看 |
| L1 标识 | session/turn/request ID、provider/model | 所属用户可见；跨用户需授权 |
| L2 诊断 | error code、tool name、workspace label | 最小权限、可审计 |
| L3 敏感 | workspace path、command、URL、error 原文 | 默认脱敏，管理员按需 reveal |
| L4 高敏 | prompt、response、tool args/result、HTTP body | 默认不进入 analytics API |

analytics DB 只存 L0-L2 和必要的 hash/reference。L3-L4 留在现有受控 artifact 中。

### 8.3 Retention

- analytics facts：默认 90 天，可配置；session aggregate 可保留更久。
- daily rollup：默认 1 年。
- HTTP/chat/debug 原始证据：沿用更短 retention，并在 UI 显示实际可用性。
- 删除 session 时，根据产品策略同步软删除或匿名化 analytics facts。
- 所有 export/reveal/rebuild 操作写 audit event。

## 9. 前端信息架构

### 9.1 入口和路由

新增顶级路由：

```text
/usage
/usage/sessions/:sessionId
/usage/sessions/:sessionId/turns/:turnId
```

在 `WorkspaceShellTopbar` 的 Logs 与 Runtime 之间增加带 `ChartNoAxesCombinedIcon` 的“使用分析”入口。当前会话存在 session ID 时，入口可带 `?session_id=...`，进入页面后仍允许清除筛选查看全局数据。

在 session 列表项的 overflow menu 增加“查看使用分析”，直接跳到 session 详情。不要把多列 Token 数永久塞进窄 sidebar。

### 9.2 全局分析页

页面顺序：

1. 固定顶部栏：返回 Workspace、页面标题、数据更新时间、刷新、导出。
2. 筛选行：时间范围、workspace、provider、model、outcome；admin 才显示 user。
3. 紧凑 KPI 行：总 Token、会话数、轮次失败率、工具 hard error、retry/recovery、coverage。
4. 趋势区域：Token 折线/柱状切换，支持 prompt/completion/cached/reasoning segmented control。
5. 会话表：页面主操作面，支持排序、cursor 分页和状态筛选。

会话表建议列：会话、最近活动、provider/model、Token、轮次、工具、错误/恢复、数据质量。小屏隐藏 provider/model 和工具列，通过行详情查看。

不要在首屏同时常驻大表、多个饼图、错误列表和 raw JSON。趋势仅保留一个主图；provider/model breakdown 放在切换 tab 或展开区。

### 9.3 单会话详情

详情使用独立路由，不使用覆盖整个工作台的超宽 modal。页头显示标题、session ID copy、时间、workspace、返回会话按钮和“在 Workspace 打开”。

Tabs：

1. `概览`：direct/inclusive Token、执行计数、最终状态、数据质量、最重要诊断。
2. `Token`：按 turn 和 LLM request 展开；显示 prompt/completion/cached/reasoning、provider reported/estimated 标记和 reconciliation。
3. `调用`：LLM request 与 HTTP attempt 时间线、provider/model/fallback/retry。
4. `工具`：tool outcome、duration、error category、recovery、重复调用。
5. `错误`：按层级展示 turn/LLM/attempt/tool 错误和恢复链。
6. `Agent`：subagent lineage、route、direct/descendant usage、compaction/limit/doom-loop 信号。
7. `原始诊断`：仅授权用户可见，默认折叠，只展示 evidence metadata 和 reveal 操作。

Tab 标题使用短文本；数量放 badge。Token 明细表按 user turn 分组，默认折叠内部 LLM requests，避免长会话一次渲染数千行。

### 9.4 Turn 详情

Turn 详情主体是从开始到结束的单列时间线：

- LLM request 卡片只展示 provider/model、usage、duration、outcome 和 attempt count。
- 有 retry 时展开 attempt 子行，显示 status/error code/delay/final recovery。
- tool calls 按实际时间插入，parallel group 使用同一组标识，不把并行 duration 相加。
- terminal 区展示 turn aggregate、明细对账和成功/失败/恢复结果。

卡片圆角不超过现有系统规范；工具、错误、重试和展开动作使用 lucide icon + tooltip。

### 9.5 页面状态

- `loading`：保留筛选和表格骨架，避免整个页面跳动。
- `empty`：区分“范围内没有会话”和“筛选无匹配”。
- `partial`：顶部状态条列出缺失范围、coverage 和原因；已有数字仍可查看。
- `unavailable usage`：显示 `--` 和“未报告”状态，不显示 0。
- `live`：正在运行的 turn 标为 provisional，Token 小计不并入 terminal total。
- `stale`：显示最后生成时间，后台刷新失败时保留旧数据并标记 stale。
- `forbidden`：不渲染残留数据，提供返回 Workspace 操作。
- `analytics not configured`：隐藏无效图表，显示后端能力状态。
- `evidence expired`：保留低敏事件事实，明确原始证据已清理。

错误颜色只表达状态；成功、恢复、warning、failed 使用现有语义色，不用单一红/绿色替代文本和图标。图表必须有表格/tooltip 数值，不能只依赖颜色。

### 9.6 前端模块建议

```text
frontend/src/pages/usage-page.tsx
frontend/src/pages/session-usage-page.tsx
frontend/src/pages/turn-diagnostics-page.tsx
frontend/src/api/runtime/analytics.ts
frontend/src/hooks/use-runtime-analytics.ts
frontend/src/components/analytics/analytics-filters.tsx
frontend/src/components/analytics/usage-kpis.tsx
frontend/src/components/analytics/usage-trend.tsx
frontend/src/components/analytics/session-usage-table.tsx
frontend/src/components/analytics/turn-usage-table.tsx
frontend/src/components/analytics/execution-timeline.tsx
frontend/src/components/analytics/data-quality-status.tsx
frontend/src/components/analytics/diagnostics-list.tsx
frontend/src/types/runtime-analytics.ts
```

沿用现有 CSS variables、Button/Badge/Tabs/Table/Tooltip 和 i18n，不引入第二套 design system。图表优先复用仓库已安装依赖；若没有成熟库，再评估 Recharts，避免手写坐标、tooltip 和响应式行为。

## 10. 现有 API 的复用边界

| API | 可复用内容 | 不能解决的问题 |
| --- | --- | --- |
| `GET /api/runtime/sessions` | session identity、title、state、metadata | 无分析筛选/分页/Token/错误 |
| `GET /api/runtime/sessions/stats` | session 状态和消息数总览 | 无 Token、时间趋势、provider/model |
| `GET /api/runtime/sessions/{id}/runtime/events` | 单 session 最近的 durable events | retention 有界，事件类型不完整，跨 session 聚合昂贵 |
| `GET /api/runtime/events` | 最近完整 runtime events | 仅内存 2,048/8 MiB，重启丢失，admin only |
| `GET /api/runtime/traces/stats` | 最近 trace 的工具/subagent/recovery 聚合 | 同样受 event bus retention 限制，不是历史账本 |
| `GET /api/runtime/status` | 全局 tool efficiency、execution diagnostics、persistence status | 进程级累计，无 session/time 维度 |
| `GET /api/runtime/logs` | 原始服务日志和关联跳转 | 最多最近 8 MiB，内容敏感，字段不稳定 |
| `GET /api/runtime/usage/stats` | skills quota/usage | 不是 chat/session usage |
| `GET /api/runtime/usage/ledger` | skills 的持久化调用 ledger | 缺 session/turn/tool/attempt 关系，且可选关闭 |

上述 API 保持兼容。新页面只通过 analytics API 获取核心指标，避免前端并发拉取所有 session events 后本地 group-by。

## 11. 分阶段实施

### Phase 0：契约和采集修正

目标：先消除未来数据继续丢失。

1. 定义 `AnalyticsRecorder` interface、事实 DTO、usage quality 和 outcome enums。
2. `session_end` 增加 `usage_source`、cache status、request/attempt/retry counts 和稳定 error code/category。
3. chat logger 改为保留 diagnostic envelope：大 payload 截断时仍保留 `usage_*`、success、event type、provider/model、request IDs；仅裁剪正文。
4. aicli 与 runtime-server 统一发布 `llm.request.*`、`tool.*`、retry facts。
5. 为每个用户提交建立稳定 turn ID；所有内部 LLM/tool/attempt 都传播该 ID。

完成标准：fixture 中不论正文大小，usage 和关联 ID 都不丢失。

### Phase 1：Analytics Store 与回填

1. 新增 SQLite schema/migrations、WAL writer、upsert 和索引。
2. 实现 session/turn/LLM request/attempt/tool/agent edge recorder。
3. 实现 reconciliation 和 daily rollup。
4. 实现 `aicli analytics rebuild`，支持 dry-run、增量 checkpoint、统计报告。
5. 使用现有 `scripts/analyze-aicli-session-history.ps1` 结果做独立对账样本。

完成标准：重复回放同一输入两次，所有 totals 不变化；重启后历史仍可查询。

### Phase 2：查询服务与 API

1. 实现 overview/session/turn/diagnostics/dimensions endpoints。
2. 添加 cursor pagination、时间上限、query timeout、response size limit。
3. 接入 tenant/project/user/admin authorization。
4. 返回 coverage/quality/partial 信息。
5. 加入 ETag 或 `generated_at + query fingerprint` 短缓存；terminal 数据可缓存，live 数据短轮询。

完成标准：API 的总量与数据库事实、回填报告完全一致，越权查询返回 403 且无数据泄露。

### Phase 3：前端 MVP

1. 增加 `/usage`、session、turn 路由和 topbar 入口。
2. 完成筛选、KPI、单一趋势图、会话表、session 概览和 Token tab。
3. 完成 data quality、partial/unavailable/live/stale 状态。
4. 添加中英文 i18n 和 URL query state；刷新/前进后退保持筛选。
5. 移动端优先保留会话、Token、状态和详情操作。

完成标准：用户可以从全局找到高消耗/失败会话，并在两次点击内查看该会话的 turn Token 明细。

### Phase 4：Agent 诊断

1. 增加调用、工具、错误、Agent tabs 和执行时间线。
2. 实现稳定规则：重复调用、恢复失败、上下文压力、compaction、limits、subagent lineage。
3. 与 `/logs` 建立带 session/request/trace 参数的安全跳转。
4. 增加管理员 evidence metadata/reveal 和审计。

完成标准：每条诊断都能定位到 turn/request/tool，并能解释 numerator/denominator 或 evidence count。

### Phase 5：上线和运营

1. feature flag：`analytics.enabled`、`analytics.capture_facts`、`analytics.raw_evidence_access`。
2. 先 shadow write，不展示；验证 7 天 reconciliation/coverage。
3. 再只读 beta；最后开启 export 和 raw evidence。
4. 监控 recorder drop、write latency、DB size、query latency、reconciliation mismatch、coverage。
5. 提供 compact/vacuum/retention job 和健康检查。

## 12. 测试计划

### 12.1 后端单元测试

- usage aggregation：多 LLM step、cache、reasoning、local estimate、缺失 usage。
- 去重：重复 event、request/response pair、retry attempts、provider switch、subagent mirror。
- outcome：最终成功但中间失败、全部 retry 失败、cancelled、interrupted、limit reached。
- tool disposition：success/empty/partial/failed/denied/replayed 及 error category。
- reconciliation：turn aggregate 相等、缺 request、重复 request、compaction/subagent 边界。
- redaction：facts 中不能出现 prompt、response、command、path、URL、API key。

### 12.2 存储和迁移测试

- 空库初始化、逐版本升级、幂等 migrate。
- 并发 recorder、busy timeout、进程重启、WAL 恢复。
- cursor pagination 稳定性；写入期间不重复/漏行。
- retention 只删除明细时，rollup/session lifetime 策略符合配置。
- rebuild 中断后继续，不重复累计。

### 12.3 API 测试

- 普通用户、operator、usage admin、system admin 权限矩阵。
- 任意 user_id 越权、猜测 evidence ID、过期 reveal token。
- from/to/limit/cursor 边界和 90 天限制。
- empty/partial/unavailable/live/stale 响应契约。
- 同一过滤范围 overview 与 sessions sum 对账。

### 12.4 前端测试

- formatter：大 Token 数、百分比、unavailable、mixed quality。
- URL filter round trip 和浏览器前进/后退。
- 表格排序/cursor/load-more，选择会话后路由稳定。
- Tab lazy loading；长 session 不一次渲染全部行。
- partial banner、403、503、stale cache、live provisional。
- i18n 中英文长度、键盘操作、tooltip、focus、颜色以外的状态表达。

### 12.5 E2E 与性能

- Playwright 桌面 1440x900、移动端 390x844：无重叠、无横向溢出、表格关键列可用。
- 操作流：筛选 -> 排序 -> session -> turn -> retry/tool -> logs -> 返回并保留筛选。
- 10 万 turns / 100 万 requests 合成库：overview P95 < 500 ms，session list P95 < 300 ms，详情首屏 P95 < 500 ms。
- recorder 不阻塞 Agent 主链：入队 P99 < 2 ms；队列满时记录 drop metric，不静默丢失。

## 13. 验收标准

1. 任一 terminal turn 都能显示 usage 数值或明确的 unavailable 原因。
2. provider reported、local estimate、missing 三种状态不会混为 0。
3. 会话 total 与去重后的 turn total 一致；不重复累计 LLM request、retry 或 subagent mirror。
4. 一个 turn 内可查看每个 LLM request 的 usage、provider/model、attempt 和最终 outcome。
5. 用户可区分“最终失败”“成功但发生 retry”“成功但工具错误已恢复”。
6. 全局页可以按时间、workspace、provider、model、outcome 筛选并分享 URL。
7. 普通用户无法读取其他用户数据或原始 HTTP/chat 内容。
8. 删除/轮转 raw artifact 不影响已保存的低敏 usage totals。
9. 长会话超过 2,048 runtime events 后，analytics lifetime total 不下降。
10. 前端所有 partial/live/stale/unavailable 状态有明确且不误导的呈现。
11. rebuild 可重复执行且总量不变，并输出 coverage/reconciliation 报告。
12. API、前端和离线分析器对同一 fixture 的工具 hard error、LLM failure 和 Token totals 一致。

## 14. 推荐的首个交付切片

第一版不要同时实现 raw evidence 和复杂 Agent 评分。最小但完整的切片应包括：

1. `AnalyticsRecorder + analytics_sessions + analytics_turns + analytics_llm_requests`。
2. session_end usage source 修正和 chat 大 payload 诊断字段保留。
3. overview、session list、session detail、turn list 四个 API。
4. `/usage` 全局页和 `/usage/sessions/:id` 的概览/Token 两个 tabs。
5. coverage/quality/partial/reconciliation 全链路。
6. session events + chat logs 回填，不在首版解析 raw HTTP body。

这个切片已经能回答用户最核心的三个问题：每个会话用了多少 Token、每个用户轮次用了多少 Token、哪些会话或轮次失败。工具/重试/Agent 深度诊断在相同事实模型上继续扩展，不需要推翻首版接口。
