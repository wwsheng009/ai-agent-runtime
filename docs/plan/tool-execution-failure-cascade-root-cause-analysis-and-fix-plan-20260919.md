# 工具执行失败、Provider 可用性与观测面异常：核验报告和修订方案

- 日期：2026-09-19
- 会话：`session_20260919100651_78okDvPi`
- 状态：**核验后修订 / P0 已实施并通过 §8 验证矩阵（2026-09-19）**，验证记录见 §11
- 取代版本：同文件 2026-09-19 15:34:52 初稿
- 相关日志：`C:\Users\vince\.aicli\chat-logs\2026\09\19\20260919_105005_695_8f6cc660.debug.log`
- 会话历史：`C:\Users\vince\.aicli\sessions\session_history.sqlite`
- 关联端点：`/debug/chat/status`、`/api/runtime/observe/v1/`

> **重要修订结论**：初稿记录的若干现象真实存在，但“Provider 失败 → 重试/退避 → 流截断 → turn mismatch → 工具结果丢失 → journal 丢弃”的单一级联因果链不成立。后续不得按初稿直接实施全局 turn 容错、简单扩大 stream/journal、或调整 terminal executor backoff。

---

## 0. 执行摘要

指定 debug log 的观察窗口内确有 **21 个失败工具回执**，但同一口径下另有 **771 个成功工具回执**，失败率约 **2.65%**。21 个失败均能由持久化工具消息中的直接错误解释：

- `edit` × 10：`STALE_CONTEXT`，`old_string` 与当前文件不匹配；
- `apply_patch` × 3：`TOOL_INVALID_ARGS / invalid_patch_syntax`；
- `view` × 3：`TOOL_PATH_NOT_FOUND`；
- `write` × 2：缺少必需参数 `file_path`；
- `shell` × 1：`AGENT_RUN_CANCELED`；
- `shell` × 1：30 秒 `TOOL_TIMEOUT`；
- `sourcegraph` × 1：HTTP 403 `Sourcegraph - Firewall Block`。

因此，本次问题必须拆为三个独立修复轨道：

1. **工具调用质量与外部工具错误限界**：stale context、错误路径、缺参、patch 语法，以及 Sourcegraph 巨大错误正文；
2. **Provider 可用性**：过期 API key 和免费额度耗尽；
3. **渲染/观测/控制面卫生**：turn identity、stream coalescing、journal retention、AgentControl registry 漂移。

### 0.1 已证实

- 21 个失败工具回执存在；
- 12:00 的 Provider 终局错误为 HTTP 403 `API_KEY_EXPIRED`；
- 12:32 的 Provider 终局错误为 HTTP 429 `RATE_LIMITED`/免费额度耗尽；
- Sourcegraph 返回 403 Firewall Block，并将大段 HTML 注入错误结果；
- `tool_receipt_recorded` 等事件存在大量前台渲染抑制；
- render output journal 和 runtime-observe retention 都发生过历史淘汰；
- AgentControl registry 存在 `ACTIVE_AGENT_SESSION_TERMINAL` 漂移。

### 0.2 未证实或已被反证

- 未发现 stream coalescing 截断工具调用 JSON 的证据；
- `assistant_delta` 超预算日志明确标记为 `retained`，不是 dropped；
- turn mismatch 抑制的是当前 TUI viewport/transcript 的跨轮事件，不是 Agent 下一步请求中的工具消息；
- `event_journal_drops` 是观测历史淘汰，不代表工具结果从执行链路丢失；
- terminal executor backoff 是 scrollback reset/replay 的渲染恢复保护，不是 Provider/tool retry；
- registry 漂移没有证据解释这 21 个工具失败；
- 指定 debug log 中未核实到与初稿同等级的 504 终局 Provider 失败。

---

## 1. 证据口径和限制

### 1.1 时间口径

必须区分：

1. **工具消息创建时间**：SQLite `session_messages.created_at`，最接近实际工具执行完成时间；
2. **回执发布时间**：debug log 中 `tool_receipt_recorded` 的时间；
3. **Provider 请求失败时间**：`[llm-debug] request_finished ... success=false` 的时间。

工具回执可在 turn 结束或 reconcile 时延迟发布，不能把回执发布时间当成实际执行时间，更不能仅凭相邻日志行推断 Provider → 工具失败因果。

### 1.2 计数口径

- 每条失败回执中 `failure_category:"tool_error"` 可出现两次；直接按字符串出现次数统计会把失败数翻倍；
- debug log 在初稿完成后仍持续追加。初稿的 `4240/1528/646/4` 没有记录 `captured_at`、byte range 或文件 hash，无法作为稳定基线；
- 后续所有日志统计必须同时记录：`captured_at`、文件大小、SHA-256、读取 byte range、过滤条件和按 session/type/reason 的分组结果；
- 完整会话历史中还有一条更早的 `grep TOOL_PATH_NOT_FOUND`，但它不在初稿所用 debug-log 窗口的 21 个发布失败回执内。

### 1.3 可信数据源优先级

1. SQLite 中持久化的工具消息及其 `error_code/failure_class`；
2. Provider 终局 `request_finished success=false`；
3. runtime event 的结构化 payload；
4. TUI render suppression 日志；
5. 仅有字符串和相邻时间的推断。

---

## 2. 21 个失败工具回执的校正明细

下表同时列出工具消息创建时间和延迟发布的回执时间，时区均为 CST（UTC+8）。

| # | 消息创建 | 回执发布 | 工具 | 错误码/分类 | message bytes | 直接原因 |
|---:|---|---|---|---|---:|---|
| 1 | 10:52:27 | 10:54:42 | sourcegraph | `AGENT_PERMISSION`（误分类） | 33748 | HTTP 403 Firewall Block，完整 HTML 被包装进错误 |
| 2 | 10:56:41 | 10:56:41 | shell | `AGENT_RUN_CANCELED` | 3321 | 执行上下文被取消 |
| 3 | 11:59:26 | 12:00:24 | view | `TOOL_PATH_NOT_FOUND` | 4463 | 文件路径不存在 |
| 4 | 12:27:03 | 12:32:50 | view | `TOOL_PATH_NOT_FOUND` | 2851 | 文件路径不存在 |
| 5 | 12:29:12 | 12:32:50 | view | `TOOL_PATH_NOT_FOUND` | 4512 | 文件路径不存在 |
| 6 | 12:39:48 | 12:53:46 | edit | `STALE_CONTEXT` | 4928 | `old_string` 不匹配 |
| 7 | 12:43:45 | 12:53:46 | edit | `STALE_CONTEXT` | 9656 | `old_string` 不匹配 |
| 8 | 12:45:11 | 12:53:46 | edit | `STALE_CONTEXT` | 10127 | `old_string` 不匹配 |
| 9 | 12:49:07 | 12:53:46 | edit | `STALE_CONTEXT` | 8367 | `old_string` 不匹配 |
| 10 | 12:49:23 | 12:53:46 | apply_patch | `TOOL_INVALID_ARGS / invalid_patch_syntax` | 2666 | 缺少合法 patch 起始结构 |
| 11 | 12:49:54 | 12:53:46 | apply_patch | `TOOL_INVALID_ARGS / invalid_patch_syntax` | 2887 | patch hunk/header 非法 |
| 12 | 12:49:54 | 12:53:46 | apply_patch | `TOOL_INVALID_ARGS / invalid_patch_syntax` | 2632 | 缺少合法 patch 起始结构 |
| 13 | 13:28:11 | 13:34:43 | write | `TOOL_INVALID_ARGS` | 2329 | 缺少 `file_path` |
| 14 | 13:31:57 | 13:34:43 | shell | `TOOL_TIMEOUT` | 5048 | 命令超过 30 秒 |
| 15 | 13:39:27 | 13:49:48 | edit | `STALE_CONTEXT` | 5912 | `old_string` 不匹配 |
| 16 | 13:52:08 | 13:54:11 | write | `TOOL_INVALID_ARGS` | 2305 | 缺少 `file_path` |
| 17 | 14:03:24 | 14:05:23 | edit | `STALE_CONTEXT` | 6586 | `old_string` 不匹配 |
| 18 | 14:03:43 | 14:05:23 | edit | `STALE_CONTEXT` | 7578 | `old_string` 不匹配 |
| 19 | 14:12:08 | 14:17:35 | edit | `STALE_CONTEXT` | 6740 | `old_string` 不匹配 |
| 20 | 14:23:49 | 14:25:10 | edit | `STALE_CONTEXT` | 5799 | `old_string` 不匹配 |
| 21 | 14:53:57 | 14:55:48 | edit | `STALE_CONTEXT` | 5427 | `old_string` 不匹配 |

### 2.1 直接结论

- 18/21 是参数、路径、patch 语法或 stale context；
- 2/21 是取消/超时；
- 1/21 是外部 HTTP 403；
- 初稿表题写“21 次”，却编号到 23，是因为把两次 Provider 失败混进了工具失败表；
- 12:00 和 12:32 的失败 `view` 实际都早于相邻 Provider 终局错误，原因链在时间上是倒置的。

---

## 3. 独立问题 A：Provider 可用性

### 3.1 已核实的终局失败

| 时间 | HTTP | 错误 | 判定 |
|---|---:|---|---|
| 12:00:24 | 403 | `API_KEY_EXPIRED` | 凭据已过期，应 fail-fast 或切换有效凭据 |
| 12:32:50 | 429 | `RATE_LIMITED`，当日免费额度耗尽 | 应尊重 reset/retry-after 或切换模型/provider |

这两次错误会中止对应 LLM step/turn，但不是那 21 个工具错误的统一原因。

### 3.2 修订措施

1. 复核 `backend/internal/llm/retry_policy.go`、`provider_retry.go` 和 provider adapter 的状态码分类；
2. 保持 401/普通 403 非重试；对 `API_KEY_EXPIRED` 明确输出 `PERMISSION_DENIED`/credential-expired next action；
3. 对 quota 403 和 429 输出 `UPSTREAM_QUOTA_EXHAUSTED`/`UPSTREAM_RATE_LIMITED`；
4. 429 仅在 reset/retry-after 处于允许窗口时重试，否则立即建议换模型/provider；
5. 防止内外层 retry budget 相乘；终局事件必须包含 attempt、max_attempts、status/error_code 和 next_action；
6. Provider 失败和工具失败分开计数、分开展示。

### 3.3 验收标准

- 401、过期 key、普通 403 不发生无意义重试；
- quota exhausted 不在短周期内连续重放相同请求；
- 429 正确解析 retry-after/reset；
- 终局 Provider 错误不会增加 `tool_failure_total`；
- 单元测试覆盖 401/403 credential、403 quota、429、5xx 和 transport timeout。

---

## 4. 独立问题 B：工具调用质量与 Sourcegraph 错误放大

### 4.1 Sourcegraph 的真实问题

`backend/internal/toolkit/tools/sourcegraph.go` 当前已经配置 30 秒 HTTP timeout（约第 59–63 行），所以本次失败不是“未设置超时”。真正问题在约第 185–199 行：

- 使用 `io.ReadAll` 无界读取响应；
- 非 200 时把完整 body 拼入错误；
- 403 Firewall Block 被上层误分类为权限错误；
- 大段 HTML 进入工具消息并消耗上下文。

### 4.2 P0 修复：有界读取和状态感知分类

实施位置：`backend/internal/toolkit/tools/sourcegraph.go`

1. 非 2xx body 使用有界读取，建议最多保留 4 KiB 预览并标记 `truncated`；
2. 禁止把完整 HTML 错误页直接注入模型上下文；如需完整诊断，写入 artifact 并只返回引用；
3. 错误结果保留 HTTP status、content-type、request-id、body preview；
4. 401/403 分类为认证、策略或 firewall block，不标成 timeout；
5. 429 和可重试 5xx 才允许有限重试；普通 403 不重试；
6. 保留已有 30 秒 timeout，除非另有明确 SLA 数据证明需要调整。

验收标准：

- 1 MiB 的 403 HTML 响应不会产生超过约 4–8 KiB 的内联错误；
- `Sourcegraph - Firewall Block` 被分类为 HTTP 403/policy，而非 timeout；
- 错误正文截断可见，完整正文若保留则有 artifact 引用；
- `httptest` 覆盖大 403、429、500、连接失败和 context deadline。

### 4.3 P0 修复：按真实错误码治理工具失败

不得再用统一 `tool_error` 推导“传输损坏”。新增或补齐以下维度：

```text
tool_failure_total{
  tool_name,
  error_code,
  failure_class,
  retryable
}
```

行为要求：

- `STALE_CONTEXT`：必须先按 current snippet 或 suggested offset 重读，不允许原参数重试；
- `TOOL_PATH_NOT_FOUND`：使用 candidates 或重新列目录，不允许原路径重试；
- `TOOL_INVALID_ARGS`：在执行前通过 required fields/patch grammar preflight；
- `TOOL_TIMEOUT`：先判断进程是否仍在运行，再缩小范围或显式提高 timeout；
- `AGENT_RUN_CANCELED`：归类为取消，不计为工具实现故障。

优先以现有 result contract 的 `next_action` 驱动 Agent 行为；只有证据表明现有 preflight 缺失时才修改工具实现。

### 4.4 上下文连续性回归

增加回归测试证明：

1. 工具消息成功或失败后先写入 session history；
2. 下一次 LLM request 能读取对应 tool result；
3. TUI turn guard 即使抑制一个 runtime event，也不会删除持久化工具消息；
4. 同一 stale args digest 不被自动原样重试。

建议测试位置：

- `backend/internal/chat/actor_test.go`
- `backend/internal/chat/tool_receipt_reconcile_test.go`
- Agent loop/tool-result contract 测试包

---

## 5. 独立问题 C：渲染、观测与控制面卫生

### 5.1 Stream coalescing：真实语义

控制点：

- 限额：`backend/cmd/aicli/commands/chat_runtime_events.go:297-305`
- 合并路径：约 `1881-1970`
- pending 路径：约 `1998-2020`

源码明确规定 assistant 文本超预算时记录 `assistant delta retained` 并继续合并；主要被丢弃的是 reasoning 等非权威显示增量。该队列处于 provider/tool-call 解析之后，不能解释工具参数 JSON 不完整。

修订措施：

- 不简单把 1 MiB 提高到 2–4 MiB；
- 保持 assistant 文本顺序和不可丢语义；
- 对 reasoning 使用可接受的降采样/终态摘要；
- 对重复 overflow 日志做采样或限流；
- 暴露 retained bytes、dropped by event type、oldest pending age；
- 如内存压力成立，采用分段 coalescing 或权威终态快照，而不是无界 retained。

验收标准：

- assistant text sequence 无空洞；
- 工具调用参数不经过该截断路径；
- 高压测试内存有界；
- reasoning drop 与 assistant retained 分开计量；
- overflow 日志不会形成新的日志风暴。

### 5.2 Turn mismatch：修 identity，不放宽隔离

`chat_runtime_events.go:4444-4479` 表明事件先写 structured log，再做前台渲染 ownership 检查；`4799-4845` 的目标是保护当前 viewport/transcript。

当前具体缺陷是工具回执事件的 turn identity 不明确：

- `backend/internal/chat/actor.go:5209-5255` 的 `publishToolReceiptEvent` 参数名为 `traceID`；
- 调用方实际传入 `CurrentTurnID`；
- event `TraceID` 被填入该值，但 payload 没有显式 `turn_id`；
- turn guard 从 payload 读取 `turn_id`，因此这些回执容易被当成 identity-less 事件抑制。

P1 修复：

1. 在工具回执事件契约中显式区分 `trace_id` 与 `turn_id`；
2. 所有 recorded/replayed 路径填入正确 `turn_id`；
3. 保留严格的 active/retired turn fence；
4. 只对具有独立 durable identity 的事件类型做窄豁免；
5. 禁止“同 session + 5 秒”之类时间容错；
6. 禁止对 `tool_result`、`assistant_delta` 全局绕过 ownership。

验收标准：

- 当前 turn 的 receipt 不再被误判为 identity-less；
- 旧 turn receipt 仍不能污染新 turn transcript；
- auto-wake、并发 turn 和 resumed turn 均有测试；
- 工具执行/持久化结果与前台渲染结果分别断言。

### 5.3 Event journal：两个组件不得混用

Render output journal：

- 生产配置：`backend/cmd/aicli/commands/chat_ui_actor.go:164-165`
- delivery journal：512 items / 4 MiB
- event journal：1024 items / 8 MiB
- 淘汰逻辑：`backend/cmd/aicli/ui/render/output/observer.go:395-404`

Runtime-observe retention：

- `backend/internal/runtimeobserve/config.go:62-64`
- 4096 events / 16 MiB / 10 min

两者都是观测历史保留，不是 Agent 工具结果队列。`event_journal_drops` 表示最旧观察记录被淘汰，不等于 primary delivery 或工具结果丢失。

修订措施：

- 不把 journal 扩容列为工具可靠性 P0；
- 分别暴露 `event_journal_evictions`、`delivery_journal_evictions`、`observer_subscriber_drops`、`runtime_observe_ring_evictions`；
- debug 页面标注“observability-only / does not imply primary loss”；
- 只有在给出调试查询窗口和内存预算后才调整 retention；
- 不引入尚无需求证明的事件优先级队列。

### 5.4 Terminal executor backoff：不与 Provider retry 混淆

`backend/cmd/aicli/ui/terminal_session_executor.go:131-163` 防止 scrollback reset/replay 紧循环：

- failure backoff：100 ms；
- yield：10 ms；
- success-mode retry window：2 s；
- 同 generation 最多 3 次 reset。

`diagnosis` 是 since-start 历史结论，`window_diagnosis` 才表示当前状态。本次观察中当前窗口曾为 `idle`，不能据 lifetime counter 判断仍有 Provider 退避风暴。

修订措施：

- 删除“基础退避改为 5 秒、最大 30 秒”的方案；
- 不新增 `backoffEventBuffer` 来保存工具结果；
- 仅在当前窗口持续出现同 generation reset/replay 且有 CPU/内存证据时调参；
- debug UI 同时展示 lifetime 和 current-window 诊断，避免误读。

### 5.5 AgentControl registry：使用现有 reconciler

一致性审计位于 `backend/internal/agentcontrol/consistency_audit.go:49-91`。项目已有：

- `backend/internal/agentcontrol/reconcile.go:91-123` 的 observe/enforce 收敛；
- `backend/internal/api/skills/agent_registry_reconcile.go:15-87` 的周期 RunLoop；
- reclaim 和 terminal retention/purge；
- 默认 10 分钟、observe-only 的安全配置（同文件约 `156-162`）。

P2 修复步骤：

1. 先保存 observe 报告并按 issue code、age、root session 分组；
2. 确认 session projection 已刷新，再审计；
3. 在测试环境启用已有 enforce 模式；
4. 使用现有 CAS/idempotent 收敛路径关闭 provably terminal/missing/stale 记录；
5. 验证 quota 释放、wake event 和 terminal retention；
6. 再决定生产是否从 observe 切换到 enforce。

禁止新增第二套 reconciler，禁止由只读 audit 函数直接删除记录。

验收标准：

- 连续两个 reconcile 周期后 terminal drift 收敛；
- 活跃 session 不被误关闭；
- 重复 pass 幂等；
- registry issue 作为控制面指标单独展示，不计入工具失败。

---

## 6. 实施优先级

| 优先级 | 工作项 | 原因 |
|---|---|---|
| P0 | Sourcegraph 非 2xx body 限界、状态感知分类和测试 | 已证实的错误放大器，单次注入约 33 KiB |
| P0 | 工具失败按 `error_code/failure_class` 统计并执行 next_action | 直接覆盖 21 个失败的真实原因 |
| P0 | 工具消息 → 下一次 LLM request 的上下文连续性回归 | 防止以后把渲染问题误判为执行丢失 |
| P1 | Provider 403/429 fail-fast、reset/fallback 语义验证 | 独立的可用性问题 |
| P1 | 工具 receipt 显式 `turn_id`，保留严格 ownership | 修复 UI/诊断误抑制而不引入串轮 |
| P2 | Stream overflow 指标、日志限流、内存有界化 | 渲染背压治理，不是工具根因 |
| P2 | 使用现有 registry reconciler 从 observe 验证到 enforce | 治理控制面漂移 |
| P3 | Journal retention 或 terminal backoff 调参 | 仅在独立容量/性能证据成立后处理 |

---

## 7. 明确禁止按初稿实施的项目

- 不采用“同 session 且 5 秒内即可绕过 turn mismatch”；
- 不让所有 `tool_result` 或 `assistant_delta` 无条件绕过 ownership；
- 不把 stream 单事件/总缓冲简单扩大到 2–4 MiB 作为根因修复；
- 不把 render journal 直接扩大到 8192/32 MiB/30 min 作为工具可靠性修复；
- 不把 terminal executor backoff 改成 5–30 秒来处理 Provider 错误；
- 不新增重复的 AgentControl reconciler；
- 不把 Sourcegraph 403 Firewall Block描述为网络超时；
- 不再用 `message_bytes` 或相邻时间代替 `error_code` 和实际执行时间。

---

## 8. 测试与验证矩阵

### 8.1 定向单元测试

```powershell
cd E:\projects\ai\ai-agent-runtime\backend

go test ./cmd/aicli/commands -run 'TestChatRuntimeEventBridge_.*(Mismatch|Stream|AgentReclaimed)|Test.*Coalesc' -count=1
go test ./cmd/aicli/ui/render/output -run 'Test.*(Journal|Overload|Drop)' -count=1
go test ./cmd/aicli/ui -run 'Test.*(Backoff|ExecutorDiag)' -count=1
go test ./internal/toolkit/tools -run 'Test.*Sourcegraph' -count=1
go test ./internal/agentcontrol -run 'Test.*(Consistency|Reconcile)' -count=1
```

上述测试已在本次核验中通过；实现修改后必须再次执行，并新增本方案要求的回归用例。

### 8.2 新增回归用例

1. Sourcegraph 大体积 403 HTML 被限界并正确分类；
2. Sourcegraph 429/5xx 与普通 403 的 retry disposition 不同；
3. active turn receipt 带显式 `turn_id` 并可渲染；
4. retired turn receipt 仍被抑制；
5. render suppression 不影响 session history 中的 tool result；
6. 下一次 LLM request 包含上一工具结果；
7. stale edit 的相同 args digest 不被原样重试；
8. assistant text 超预算仍保持 sequence 连续；
9. journal eviction 不影响 primary delivery；
10. registry observe/enforce 重复执行幂等且不误关活跃 session。

### 8.3 实跑验收指标

- 工具失败率按错误码报告，而非只报告 `tool_error`；
- `STALE_CONTEXT` 原参数重复率接近 0；
- Sourcegraph 内联错误正文不超过既定上限；
- Provider 403/429 不再与工具失败混合；
- `tool_receipt_recorded` 的 identity-less 抑制降为 0，旧 turn 抑制仍有效；
- stream overflow 按 retained/dropped 和 event type 分开；
- 所有统计附 `captured_at`、文件 hash/offset 或观测 cursor。

---

## 9. 回滚策略

- Sourcegraph 限界和分类修改可独立回滚，不涉及数据迁移；
- receipt identity 修改应保留向后兼容读取，并可通过窄开关回退旧事件编码；
- registry enforce 若出现误判，立即切回 observe，保留审计报告，不删除历史证据；
- journal/backoff 默认不改，因此本轮没有相应回滚负担；
- 任一阶段失败不得通过放宽全局 turn ownership 临时掩盖。

---

## 10. 最终判定

初稿的“21 个失败回执、Provider 403/429、渲染抑制、journal 淘汰和 registry 漂移”等现象可以保留；其单一失败级联、Sourcegraph 超时判断、工具结果丢失判断，以及 stream/journal/backoff 的修复优先级必须废止。

本修订版将问题重新定义为：

1. **工具调用质量和外部错误正文限界问题**；
2. **Provider 凭据/配额可用性问题**；
3. **渲染事件 identity 与观测/控制面卫生问题**。

三个轨道分别实施、分别计量、分别验收，不再相互替代因果证据。

---

## 11. 实施与验证记录（2026-09-19 核验轮）

本轮按 §6 优先级先做 P0，再复核 §8 验证矩阵；以下以当前工作区为准（改动尚未提交）。

### 11.1 实施落点

**§4.2 Sourcegraph 限界与状态感知分类** — `backend/internal/toolkit/tools/sourcegraph.go`

- 非 2xx body 经 `readSourcegraphErrorPreview` 有界读取（`io.LimitReader`，上限 `sourcegraphErrorBodyPreviewBytes`），超限追加 `[truncated]`，空 body 回退 `http.StatusText(http.StatusBadGateway)`；
- `classifySourcegraphHTTPFailure` 覆盖 401（认证）/ 403（策略；preview 含 `firewall block` 时升级为 `upstream_firewall_block`）/ 429（限流，可重试）/ 5xx（上游不可用，可重试）/ 其他 4xx（不可重试），并给出各自 `next_action`；
- 失败元数据保留 `http_status`、`content_type`、`body_preview_truncated`、`request_id`、`retryable`；
- 30 秒 HTTP timeout 未调整（符合 §4.2-6）。

**§4.3 按真实错误码治理工具失败** — `backend/internal/observability/`、`backend/internal/agent/`、`backend/internal/toolexec/`

- 新增 `tool_failure_total{tool_name,error_code,failure_class,retryable}`（`metrics.go`、`tool_efficiency.go: RecordToolFailure`）；
- `ToolFailureSnapshot` 输出 `ByToolName / ByErrorCode / ByFailureClass / ByRetryable / Series`，旧数据回退到 `Outcomes.ByErrorCode`；
- 生产者 `internal/agent/loop.go: recordToolResultMetrics`：经 `toolresult.Diagnose` 归一化错误码后同时记录 outcome 与 failure 维度；
- `internal/toolexec/preflight.go: strengthenNoRetryAction / strengthenEmptyReplayAction` 为 §4.3 行为要求提供统一兜底 `next_action`（STALE_CONTEXT / PATH_NOT_FOUND / INVALID_ARGS 等不允许原样重试）。

**§4.4 上下文连续性** — `backend/internal/agent/loop_test.go`、`backend/internal/chat/`

- `RunWithSession` 用例断言：工具执行后下一次 LLM request 必含 role=tool 结果，且 session history 中 tool 消息先于终局 assistant 落盘；
- `internal/chat/actor.go: publishToolReceiptEvent` 显式携带 `turn_id`（payload 与 receipt 双写，保留旧字段兼容），retired turn 抑制保持不变。

### 11.2 验证命令与结果（2026-09-19，全部通过）

```powershell
cd E:\projects\ai\ai-agent-runtime\backend
go test ./cmd/aicli/commands -run 'TestChatRuntimeEventBridge_.*(Mismatch|Stream|AgentReclaimed)|Test.*Coalesc' -count=1  # ok
go test ./cmd/aicli/ui/render/output -run 'Test.*(Journal|Overload|Drop)' -count=1                                          # ok
go test ./cmd/aicli/ui -run 'Test.*(Backoff|ExecutorDiag)' -count=1                                                        # ok
go test ./internal/toolkit/tools -run 'Test.*Sourcegraph' -count=1                                                          # ok
go test ./internal/agentcontrol -run 'Test.*(Consistency|Reconcile)' -count=1                                               # ok
go test ./internal/agent ./internal/observability ./internal/toolkit/tools ./internal/toolexec -count=1                     # ok
go test ./internal/chat -count=1                                                                                            # ok
go test ./cmd/aicli/commands -count=1                                                                                       # ok (90.3s)
```

### 11.3 §8.2 回归用例映射

| # | 用例 | 落点（已验证） |
|---|------|----------------|
| 1 | 大体积 403 HTML 限界+分类 | `internal/toolkit/tools/sourcegraph_test.go: TestSourcegraphTool_LargeFirewall403IsBoundedAndClassified` |
| 2 | 429/5xx 与 403 retry 差异 | `sourcegraph_test.go: TestSourcegraphTool_HTTPRetryDisposition` |
| 3 | active receipt 显式 turn_id | `internal/chat/tool_receipt_reconcile_test.go` + `actor.go: publishToolReceiptEvent` |
| 4 | retired turn receipt 抑制 | `cmd/aicli/commands/chat_runtime_events_turn_budget_test.go`、`chat_agent_reclaim_events_test.go: TestChatRuntimeEventBridge_AgentReclaimedSurvivesActiveRun` |
| 5 | 抑制不删除持久化 tool 消息 | `chat_runtime_events_test.go`（persisted tool message 断言，约 4692–4701 行） |
| 6 | 下一次 LLM request 含 tool 结果 | `internal/agent/loop_test.go`（RunWithSession 连续性断言）、`loop_malformed_tool_call_test.go` |
| 7 | 相同 args digest 不原样重试 | `internal/toolexec/preflight.go: strengthenNoRetryAction / strengthenEmptyReplayAction`（`internal/toolexec` 测试通过） |
| 8 | 超预算保持 sequence 连续 | `chat_runtime_events_stream_coalesce_test.go: ...OverPendingBudget / ...OverByteBudget / TestStreamMergeKeyIncludesIdentityAndSequence` |
| 9 | journal eviction 不影响 primary | `cmd/aicli/ui/render/output: TestSustainedOverloadBoundedMirrorAndPrimaryUnaffected` |
| 10 | registry reconcile 幂等且不误关活跃 session | `internal/agentcontrol: TestReconcileAgentSessionConsistency_Idempotent / _LeavesLiveAndWaitingApprovalAlone` |

### 11.4 说明与遗留

- 验证窗口内曾出现 `cmd/aicli/commands` 全量测试的瞬时失败（chat-logger 路径类用例），隔离重跑与后续全量重跑均通过；同期 `cmd/aicli/commands/doctor.go` 存在其他工作流的并发编辑（chat-logs 分区布局），与本计划无关，未做改动。
- §8.3 实跑指标（工具失败率按错误码、STALE_CONTEXT 原参数重复率、identity-less 抑制降为 0、stream overflow 分类计数）需在生产/长会话数据上复核；本轮完成代码侧验收，不阻塞 P0 收口。
- 本计划相关改动仍处于工作区未提交状态；回滚策略见 §9。

---

## 12. P1/P2 核验记录（2026-09-19，第二核验轮）

本轮针对 §5 的 P1/P2 项逐条复核工作区实现与测试。结论：§5.1–§5.5 的修订措施在当前工作区均已落地并有测试覆盖；§6 表中 P1 的 Provider fail-fast / reset 语义亦已具备。

### 12.1 落点对照

| 计划项 | 落点 | 关键实现 |
|---|---|---|
| §5.1 stream overflow 指标与日志限流 | `cmd/aicli/commands/chat_runtime_events.go` | `chatStreamQueueStats`：`Retained/Dropped Events+Bytes`、`RetainedByType/DroppedByType`、`OldestPendingAge`、`OverflowLogsSuppressed`；快照经 `/debug` HTTP（`chat_debug_display_http.go:491`）与文档（`chat_debug_document.go:642`）暴露；溢出日志按 `chatStreamOverflowLogInterval=64` 限流 |
| §5.2 receipt turn identity | `internal/chat/actor.go: publishToolReceiptEvent` + `cmd/aicli/commands/chat_runtime_events.go: shouldSuppressMismatchedPrimaryTurnEvent` | payload 显式 `turn_id`；guard 测试覆盖放行/抑制/auto turn（`chat_runtime_events_test.go:7343–7368`、`chat_agent_reclaim_events_test.go:225/236`、`chat_runtime_events_supplement_test.go:355–384`） |
| §5.3 journal 计数分离 | `cmd/aicli/commands/chat_debug_display_http.go:218–220`、`internal/runtimeobserve/model.go:158` | `observer_subscriber_drops` / `event_journal_evictions` / `delivery_journal_evictions` / `runtime_observe_ring_evictions` 分列；debug 文档标注 observability-only（`chat_debug_document.go:315–319`） |
| §5.4 lifetime vs window 诊断 | `cmd/aicli/ui/terminal_session_executor.go:69–113`、`executor_diag_export.go:47`、`chat_debug_document.go:471–475` | `WindowDiagnosis`（当前窗口）与 `Diagnosis`（since-start）并列展示；测试 `TestExecutorDiagWindowDiagnosis*` |
| §5.5 现有 reconciler | `internal/agentcontrol/reconcile.go` / `reconciler.go`；宿主接线 `internal/api/skills/agent_registry_reconcile.go`、`cmd/aicli/commands/chat_actor_reconcile.go` | 默认 observe（`ParseReconcileMode("")` / `ModeOrDefault`），enforce 复用同一 CAS 路径；RunLoop 启动即跑 + 周期 + 幂等二次 pass（`reconciler_test.go:100`、`reconcile_test.go:214`）；issue 展示在 `/debug` consistency 段，独立于工具失败 |
| §6 P1 Provider 403/429 | `internal/llm/provider_retry.go:78–97`（fail-fast 清单含 `api_key_expired`）、`retry_policy.go:1374–1395` + `decisionDelayFromServerHint` | 403 凭据类不再重试；429/5xx 尊重 Retry-After / 正文 reset 提示（`gateway_client.go:1671–1674`） |

### 12.2 验证命令（2026-09-19，全部通过）

```powershell
cd E:\projects\ai\ai-agent-runtime\backend
go test ./internal/llm -run 'Test.*(Retry|FailureCode|Delay)' -count=1                     # ok
go test ./internal/agentcontrol -count=1                                                    # ok
go test ./cmd/aicli/ui -run 'TestExecutorDiag' -count=1                                     # ok
go test ./internal/runtimeobserve -count=1                                                  # ok
go test ./cmd/aicli/commands -run 'Test(ChatRuntimeEventBridge|EnqueueStreamEvent|StreamMerge|Adopt|ChatDebug)' -count=1  # ok
go test ./internal/api/skills -run 'Test.*(StreamMetrics|Coalesce|Delivery|RegistryReconcile|Reconcile)' -count=1          # ok
```

### 12.3 遗留

- §5.1 的内存有界化采用软预算：assistant 文本超预算仍保留（不可丢语义），reasoning 可丢并计入 `DroppedByType`；P3 的 journal retention / terminal backoff 调参仍按计划仅在独立容量证据成立后处理。
- §5.5 生产是否从 observe 切到 enforce 需按 §5.5 步骤 1–6 先在测试环境跑观察报告；本轮未改默认值。
