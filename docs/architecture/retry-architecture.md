# LLM 重试架构

> 代码位置：`backend/internal/llm/`
> 核心文件：`retry_policy.go`、`retry_executor.go`、`provider_retry.go`、`provider.go`、`runtime.go`、`gateway_client.go`、`retry_config_adapter.go`
> 更新日期：2026-08-31

本文梳理 ai-agent-runtime 中 LLM 调用的完整重试架构与逻辑，包括：两层重试（内层 provider / 外层 runtime）、两套预算（业务预算 / transport 预算）、错误分类、退避策略、层间接力、守卫机制与配置来源。

---

## 1. 总体架构：内外两层 + 两套预算

重试不是单层循环，而是 **内外两层嵌套**。内层有两种实现形态，都受外层循环包裹：

- **ProviderWrapper**（`provider.go`）：绑定单个上游 Provider，有两套预算（业务 / transport）并行约束，见第 4 节。
- **GatewayClient**（`gateway_client.go`）：经 ResourceManager 选路的多上游实现，每次重试会 **换路 failover**（组/提供商/API key 三级），见 4.4 节。

两者被外层循环调用时行为一致：内层预算耗尽后通过 `markRetryExhaustedForNextLayer` 接力外层。

```
┌────────────────────────────────────────────────────────────┐
│ 外层：LLMRuntime.Call / Stream（runtime.go）                 │
│   预算：runtime MaxRetries（默认 10 次重试，共 11 次尝试）      │
│   守卫：连续接力 ≥3 次 → 快速失败                             │
│   职责：总重试次数保证 + 层间接力重分类                        │
│                                                            │
│   for attempt := 1; attempt <= MaxAttempts; attempt++ {     │
│     ┌──────────────────────────────────────────────────┐   │
│     │ 内层：ProviderWrapper.Call / callStreamingAggregate│   │
│     │  预算A：业务预算 MaxAttempts（MaxRetries 决定）      │   │
│     │  预算B：transport 预算 MaxTransportAttempts（默认4）│   │
│     │  for ... && retryAttemptAllowed &&                  │   │
│     │           transportAttemptAllowed {                 │   │
│     │       Chat() / 流式聚合                            │   │
│     │       → 错误分类 + transport 计数 + prepareRetry    │   │
│     │  }                                                 │   │
│     │  耗尽 → 可接力? → markRetryExhaustedForNextLayer   │   │
│     └──────────────────────────────────────────────────┘   │
│     → prepareRetry（外层间隔重试）                          │
│   }                                                       │
└────────────────────────────────────────────────────────────┘
```

### 两套预算的含义

| 预算 | 计数的错误类型 | 默认值 | 语义 |
|---|---|---|---|
| **业务预算** `MaxAttempts` | 所有可重试错误（429/5xx/transport/…） | 内层 10（`ProviderConfig.MaxRetries`），外层 11（runtime `MaxRetries+1`） | 通用的总尝试次数上限 |
| **transport 预算** `MaxTransportAttempts` | 仅连接/响应头超时/TLS/SSE EOF 等 transport 类错误（`isTransportBudgetError`） | 4（`DefaultTransportMaxRetries`） | 对"死连接"收紧：重试一个死连接很少立刻成功，用小预算限制挂起时间 |

> **语义差异**：`MaxTransportAttempts` 是 transport 尝试次数上限（与 MaxAttempts 同为"总次数"语义），不是额外的重试次数。

---

## 2. 错误分类：`classifyRetryableLLMError`

所有错误先经过统一分类（`retry_policy.go`），输出 `retryDecision{Retryable, Delay, BaseDelay, Reason, MaxAttempts, Multiplier}`。

分类优先级（`classifyRetryableLLMErrorWithRules`）：

1. **已耗尽/抑制标记**：`retryExhaustedError` → `retry_exhausted`（不可重试）；`retrySuppressedError` → `retry_suppressed`（不可重试）
2. **取消**：`context.Canceled` → `context_canceled`
3. **配置规则**：`decisionFromRetryRules`（YAML `retry.rules` 命中优先）
4. **配额耗尽**：`isQuotaExhaustionError` → `quota_exhausted`（不可重试）
5. **非可重试响应**：`isRetryableProviderResponseError` 为 false → `non_retryable_response`（请求类错误、API key 错误等）
6. **HTTP 状态码**：
   - `408`/`409`/`425` → 可重试 `http_408`/`http_409`/`http_425`（425 Too Early 见 RFC 8470）
   - `429`：含 rate-limit 关键词 → `rate_limit`；否则 → `http_429`（均携带服务器提示延迟）
   - `400`：先判确定性缺陷 —— `content_inspection_failed`/`invalid_request`（不可重试）；若报文含显式瞬时信号（`transientUpstreamTextHint` 或 `isRetryableTransportError`：bad gateway / service unavailable / gateway timeout / upstream connect error / overloaded / 连接中断 / timeout 等）→ `transient_stream_or_server`（**可重试**，2026-09-14）；都不命中才 → `http_400`（不可重试）
   - 其余 4xx：`401`/`402`/`403`（认证/计费）恒为终态；`404`/`405`/`422`/`499` 等默认终态，但同样可被上述显式瞬时信号救援为 `transient_stream_or_server`（**可重试**，2026-09-14）
   - `5xx` → 可重试 `http_5xx`
7. **内容类错误**：`content_filter`（不可重试）、`insufficient_system_resource`（可重试）、`malformed_tool_call`（`invalid_tool_arguments` **可重试**（2026-09-14）：工具参数不是合法 JSON 对象是响应侧的退化采样，按短退避 + 连续退化 streak 上限治理，且**不**参与 handoff 接力；其余可重试）、`truncated_tool_call`（**可重试**：输出被 completion 预算截断属采样/预算问题，非请求参数错误）、`empty_provider_choices`（**可重试**并按 `empty_reply` 处理：200 + 空/缺失 choices 是退化采样，复用短退避、退化 streak 上限与 handoff 资格）
8. **流中断**：`stream_interrupted`、`empty_reply`、`reasoning_only_empty_reply`（可重试）
9. **transport 错误**：`isRetryableTransportError` → `transport`（可重试）
10. **瞬态关键词**：`transient_stream_or_server`（timeout、connection reset、unexpected eof、bad gateway、service unavailable、gateway timeout、upstream connect error、no healthy upstream、overloaded、connection aborted、broken pipe、goaway、internal server error，可重试；`transientUpstreamTextHint`，retry_policy.go）
11. **兜底**：`default_retryable`（可重试）

### 关键判定函数

| 函数 | 位置 | 作用 |
|---|---|---|
| `isRetryableTransportError` | retry_policy.go:1398 | net.Error / url.Error / io.EOF / DeadlineExceeded / 关键词（connection refused/reset、dial tcp、timeout、unexpected eof…） |
| `isTransportBudgetError` | retry_policy.go:453 | transport 类错误但 **排除 `providerHTTPError`**（429/5xx 走业务预算） |
| `isRetryableProviderResponseError` | provider_retry.go:60 | 请求类/内容检查/上下文窗口等确定性错误返回 false |
| `IsContextWindowError` | provider_retry.go:100 | 上下文超长（不可重试，需压缩） |
| `isQuotaExhaustionError` | retry_policy.go:775 | 配额/余额耗尽（不可重试） |
| `isOutputBudgetEscalationReason` | provider_retry.go:207 | completion 预算耗尽的 reason（`reasoning_only_empty_reply` / `truncated_tool_call`）才允许 widen `max_tokens` |

---

## 3. 重试间隔：`delayForDecision` 与服务器提示

每次重试前计算间隔（`retry_policy.go:464`），优先级：

1. **服务器提示延迟优先**：若 `decision.Delay > 0` 且无自定义 `Schedule`，直接用 `capServerHintDelay(MaxDelay, 服务器延迟)`。
2. **自定义 Schedule**：命中规则且规则无 BaseDelay/Multiplier → 按 `retryScheduleDelay` 取表中对应第 attempt 项。
3. **指数退避**：`nextRetryDelay` = `base × multiplier^(attempt-1)`，上限 `MaxDelay`。默认 `base=200ms`、`multiplier=2`、`MaxDelay=5s`。
4. **抖动**：默认 ±10%（`defaultLLMRetryRandomization=0.1`，对齐 codex-rs），`Randomization<0` 关闭，`>1` 收敛为 1。抖动避免对共享上游产生同步重试风暴。
5. **服务器提示兜底**：若退避 < 服务器提示，仍用提示（capped）。

### 服务器提示来源（`decisionDelayFromServerHint`）
- `Retry-After-Ms` / `Retry-After` 响应头（`retryAfterDelayFromHeader`）
- 错误消息文本中的 `retry after N s/ms/m` / `try again in N`（`parseRetryAfterFromMessage`）

### 上限保护
- `capServerHintDelay`：服务器提示（如月度限额 Reset 需要 1 天）被 `MaxDelay` 截断，避免请求挂死数小时。
- `degenerateOutputRetryMaxDelay`（2026-09-13）：`empty_reply` / `reasoning_only_empty_reply` / `truncated_tool_call` 属于采样退化（恢复手段是重新采样，而非等待上游），`delayForDecision` 把抖动后的延迟统一截到 **2s** —— 服务器提示同样受该上限约束（先 `capServerHintDelay(MaxDelay)`，再套退化上限）。实测 30s→60s 业务退避把单条重试链拉到 6+ 分钟，而每次返回的仍是同一个退化样本。判定入口 `isDegenerateOutputRetryReason`（retry_policy.go）。
- `truncated_tool_call` 重试化（2026-09-14）：工具调用标记被 completion 预算截断（常伴 `finish_reason=length`）原先直接终态，并给出误导性的 `UPSTREAM_INVALID_REQUEST` + "修正请求参数" 指引。现按采样/预算退化处理：分类层可重试、退避套用 `degenerateOutputRetryMaxDelay`、错误码归入 `UPSTREAM_INVALID_RESPONSE`、`escalateOutputBudgetForDegenerateReply` 把下一次尝试的 `max_tokens` 翻倍；连续 3 次截断仍由 `trackDegenerateOutputReply` 收敛，避免烧穿预算。
- `canRetryAfter`：`MaxElapsedTime > 0` 时，若 `now-start + delay > MaxElapsedTime` 则停止重试。
- `waitRetryDelay`：用 `context` 可取消的 timer 等待，父上下文取消立即返回。

---

## 4. 内层重试循环（ProviderWrapper）

非流式 `Call`（provider.go:1062）与流式 `callStreamingAggregate`（provider.go:1286）采用同一骨架。

### 4.1 请求阶段（连接未响应就断）

```
for attempt := 1; retryAttemptAllowed(MaxAttempts, attempt)
                && transportAttemptAllowed(MaxTransportAttempts, transportAttempts); attempt++ {
    chatResp, err := p.Chat(attemptCtx, chatReq)
    ...
    // 守卫1：无限预算 + 连续 2 次响应头超时 → 判定上游挂死，快速失败并接力外层
    if policy.MaxAttempts <= 0 && policy.MaxTransportAttempts <= 0 &&
       trackHeaderTimeoutStreak(&consecutiveHeaderTimeouts, err) {
        return nil, markRetryExhaustedForNextLayer(...)
    }
    // 守卫2：max_tokens 超限可修复一次（降低请求预算后重建请求）
    if !maxTokensRecovered && applyMaxTokensLimitRecovery(&chatReq.MaxTokens, err) { ... continue }
    // transport 计数：失败后立即计数（不能等 prepareRetry 的退避，否则
    // 等待期间父 context 过期会掩盖 transport 上限）
    if transportFailure := isTransportBudgetError(err); transportFailure {
        transportAttempts++
        if MaxTransportAttempts > 0 && transportAttempts >= MaxTransportAttempts {
            // 响应头都未收到即耗尽 → 上游当前不可达。内层快速失败（不烧业务
            // 预算），但作为瞬态失败接力外层，由外层退避后重试整轮 provider 调用
            return nil, markRetryExhaustedForNextLayer("provider transport call failed after retries", ...)
        }
    }
    retryResult, retryErr := prepareRetry(...)   // 分类 + 退避 + 等待 + 上报事件
    ...
}
// 业务预算耗尽：
if isHandoffEligibleError(lastErr) {
    return nil, markRetryExhaustedForNextLayer(...)  // 瞬态 → 接力外层
}
return nil, markRetryExhausted(...)                   // 确定性失败 → 终态
```

### 4.2 响应阶段（HTTP 200 后 SSE 流中断）

流式 `HandleResponse` 出错时（provider.go:1544 附近）：

```
if handleErr != nil {
    lastErr = ...
    // Partial-output replay（2026-08-31）：已发出可见内容（正文/图片）后,
    // 仅确定性错误保持抑制；瞬态错误（SSE EOF/连接重置/空闲超时/5xx/429/
    // 流中断/空回复）继续重试,重放重新生成全文,接受重复的部分输出。
    // llm.retry 事件带 partial_output=true 标记,UI 可注记重放。
    if emissionState.emittedAnything() {
        lastErr = withPartialOutputMarker(lastErr)
        if mustSuppressRetryAfterEmission(lastErr) { return nil, suppressRetry(lastErr) }
    }
    // transport/stream 类（SSE EOF、连接重置、空闲超时）计入 transport 预算；
    // 耗尽后接力外层，而不是烧光整个业务预算
    if isTransportBudgetError(lastErr) || isRetryableTransportError(...) {
        transportAttempts++
        if MaxTransportAttempts > 0 && transportAttempts >= MaxTransportAttempts {
            return nil, markRetryExhaustedForNextLayer(...)
        }
    }
    retryResult, retryErr := prepareRetry(...)  // meta.PartialOutput = errHasPartialOutput(lastErr)
    ...
}
```

> **请求阶段与响应阶段语义一致（2026-08-31 统一）**：
> - 两个阶段的 transport 预算耗尽都 → **接力外层**。内层小预算快速失败、
>   不烧业务预算；外层大预算负责总重试保证，退避后用全新请求再试
>   （上游恢复即可成功）。外层连续接力由 fast-fail 守卫（3 轮）封顶。

### 4.3 内层循环退出后：`isHandoffEligibleError`

业务预算耗尽时，是否把失败交给外层由 `isHandoffEligibleError`（provider_retry.go:196）决定。白名单：

```
transport | transient_stream_or_server | stream_interrupted | empty_reply
| reasoning_only_empty_reply | insufficient_system_resource | rate_limit
| http_429 | http_408 | http_409 | http_5*（任意 5xx）
```

- `http_5*` 前缀匹配（2026-09-14）：除 500/502/503/504 外，529（Anthropic overloaded）、520-524（Cloudflare 源站故障）、598 等同样交给外层做 provider/key 轮换，而不是内层预算耗尽即终态。
- 命中 → `markRetryExhaustedForNextLayer`（标记 `retryAtNextLayer=true`）
- 未命中（确定性错误或本层用尽即终态的 reason：`quota_exhausted`、`invalid_request`、`content_filter`、`malformed_tool_call`/`invalid_tool_arguments`、`truncated_tool_call` 等）→ `markRetryExhausted`（本层终态）。其中可重试 cause（如 `truncated_tool_call`、`invalid_tool_arguments`）仍会被 `DiagnoseFailure` 还原为 `Retryable: true`，由会话层发起新的有界尝试（`invalid_tool_arguments` 另有执行层 re-prompt 通道，见场景 D2）。

### 4.4 GatewayClient：多上游 failover 重试（gateway_client.go）

`GatewayClient` 是内层的第二种实现，通过 `LLMRuntime.RegisterGatewayClient`（runtime.go:252）注册为普通 Provider，因此同样被外层循环包裹。差异在于 **每次尝试前先经 ResourceManager 选路**，失败后换一个上游再试（failover），而不是固定打同一个 Provider：

```
for attempt := 1; retryAttemptAllowed(policy.MaxAttempts, attempt); attempt++ {
    // 1) 选路：ResourceManager 根据 retryInfo 避开已失败的组/提供商/API key
    selected, err := c.resourceManager.SelectResource(retryInfo)
    // 2) 调用选中上游（流式走 callProviderStreamingAggregate，gateway_client.go:695）
    response, err := c.callProvider(attemptCtx, selected, model, req)
    if err != nil {
        // 3) 失败记账 → 下次选路自动换路
        c.resourceManager.RecordResult(selected, false, err, statusCode, 0)
        lastError = err
        // 4) 记录已尝试的组/提供商/API key（gateway_client.go:385-396）
        retryInfo.TriedGroups    = append(..., selected.GroupName)
        retryInfo.TriedProviders = append(..., selected.Provider.Name)
        retryInfo.TriedAPIKeys[provider] = append(..., selected.KeyID)
        // 5) 不可重试（如 4xx 确定性错误）→ 立即终态
        if !isRetryableGatewayProviderError(err) { return nil, err }
        // 6) 分类 + 退避 + 上报（与 ProviderWrapper 同一 prepareRetry）
        retryResult, retryErr := prepareRetry(..., retryExecutionMeta{Source: "gateway_client"})
        if retryResult.Retry { continue }
        break
    }
    // 成功记账 + 返回
    c.resourceManager.RecordResult(selected, true, nil, 200, 0)
    return response, nil
}
// 预算耗尽：与 ProviderWrapper 相同的接力判定
if isHandoffEligibleError(lastError) {
    return nil, markRetryExhaustedForNextLayer(...)  // 瞬态 → 接力外层
}
return nil, markRetryExhausted(...)                   // 确定性 → 终态
```

要点：

- **选路与重试分离**：重试预算（`maxRetries`）决定"尝试多少次"；ResourceManager 决定"每次试哪个上游"。`TriedGroups / TriedProviders / TriedAPIKeys` 随重试信息累积，选路器据此逐级换组 → 换提供商 → 换 API key。
- **预算默认值**：构造时 `maxRetries: 3`（gateway_client.go:90）；注册进 runtime 时被 `r.config.MaxRetries`（外层默认 10）覆盖（runtime.go:262），因此实际业务预算与 ProviderWrapper 内层一致。
- **transport 预算**：GatewayClient 不单独计 transport 预算（`newProviderRetryPolicy(c.maxRetries, 0, ...)`，gateway_client.go:329），transport 类错误直接计入业务预算；选路失败/上游不可达由换路机制消化。
- **流式**：`Call` 内 `req.Stream` 时走 `callProviderStreamingAggregate`，重试语义与 ProviderWrapper 一致（已发出可见内容 → 抑制重试）。
- **不可重试即终态**：`isRetryableGatewayProviderError`（gateway_client.go:1632）判定失败（如 4xx）时立即返回，不消耗外层预算；可重试错误耗尽预算后按 `isHandoffEligibleError` 接力外层。

---

## 5. 外层重试循环（LLMRuntime）

`runtime.go:Call`（非流式）与 `runtime.go:Stream`（流式）使用相同策略构造与循环。

```
maxRetries, retryTuning, retryRules := r.RetryConfigSnapshot()
policy := newRuntimeRetryPolicy(maxRetries, 0, retryTuning, retryRules)
policy = applyRequestRetryPolicy(policy, req.Metadata)   // 请求级关闭重试
consecutiveHandoffs := 0

for attempt := 1; retryAttemptAllowed(policy.MaxAttempts, attempt); attempt++ {
    response, err := provider.Call(attemptCtx, req)       // 内层整体作为一步
    if err == nil { return response, nil }
    lastError = err
    // 连续接力守卫：内层持续快速失败接力 → 最多 3 轮后终止
    if is retryExhaustedError && retryAtNextLayer {
        consecutiveHandoffs++
        if consecutiveHandoffs >= maxConsecutiveFastFailHandoffs {
            return nil, markRetryExhausted("... repeated fast-fail retries", ...)
        }
    } else { consecutiveHandoffs = 0 }
    // prepareRetry：对接力错误重分类（decisionForRetry）后做间隔重试
    retryResult, retryErr := prepareRetry(attemptCtx, policy, startedAt, attempt, err, ...)
    ...
}
```

### 外层对接力的重分类：`decisionForRetry`

`prepareRetry` 调 `decisionForRetry`（retry_policy.go:366），它比 `decisionForError` 多做一件事：

- 错误是 `retry_exhausted` 时，检查是否 `retryAtNextLayer=true`：
  - 是 → 用底层 cause 重新分类（429 → `rate_limit`，transport → `transport`），**恢复可重试 + 服务器提示延迟**
  - 否 → 保持终态
- 边界守卫：
  - `retrySuppressedError`（已输出内容 + 确定性错误）→ 永不重试；已输出内容 + 瞬态错误（2026-08-31 起）→ 正常重试并带 partial_output 标记
  - **外层无限预算**（`initialMaxAttempts() <= 0`）→ 保持终态，防止无限循环放大内层快速失败
  - 最多沿 Unwrap 链追踪 16 层（防御 GatewayClient 多层包装）

---

## 6. 层间接力与守卫汇总

| 机制 | 位置 | 触发条件 | 效果 |
|---|---|---|---|
| `markRetryExhaustedForNextLayer` | retry_policy.go:597 | 内层预算耗尽且错误可接力 | 标记 `retryAtNextLayer=true`，外层可重试 |
| `markRetryExhausted` | retry_policy.go:579 | 内层/外层普通预算耗尽 | 终态（保留 cause 供诊断） |
| `suppressRetry` | retry_policy.go:607 | 流式已输出可见内容后出错且错误为确定性（quota/invalid_request/content_filter/取消/预算耗尽） | 永不重试（重放必然再失败）；瞬态错误不再进入此路径 |
| `maxConsecutiveFastFailHandoffs` | retry_policy.go:276 | 外层连续收到 3 次接力 | 外层快速失败，避免死上游耗尽整个外层预算 |
| `trackHeaderTimeoutStreak` | retry_policy.go:710 | 连续 2 次响应头超时 | 无限预算内层快速失败并接力外层 |
| `retryAttemptsCeiling` | retry_policy.go:269 | 正数预算 >100 | 截断为 100（对齐 codex-rs） |
| `canRetryAfter` | retry_policy.go:551 | `MaxElapsedTime>0` 且超时 | 停止重试 |
| `capServerHintDelay` | retry_policy.go:492 | 服务器提示 > MaxDelay | 截断提示延迟 |

---

## 7. 配置来源与优先级

### 7.1 内层（ProviderConfig）
- `MaxRetries`（业务，默认 10）、`MaxTransportRetries`（默认 4）
- `RetryTuning`：BaseDelay / MaxDelay / MaxElapsedTime / Multiplier / Randomization / Schedule
- `RetryRules`：按关键词/错误码/状态码匹配的自定义规则（action=retry|stop，可覆盖次数/延迟/倍率）
- `ResponseHeaderTimeout`（默认 60s）：响应头等待上限，挂死上游的最后防线
- `StreamReadTimeout`：流式空闲超时

### 7.1.1 内层（GatewayClient，多上游形态）
- 构造默认 `maxRetries: 3`（gateway_client.go:90）
- 注册进 runtime 时被覆盖：`RegisterGatewayClient` → `SetMaxRetries(r.config.MaxRetries)`（runtime.go:262），默认 10
- `SetRetryTuning` / `SetRetryRules` / `SetStreamReadTimeout` 同样从 `r.config` 注入（runtime.go:263-265）
- 选路预算（组/提供商/API key 的 failover 范围）由 ResourceManager 实现方决定，不在此配置

### 7.2 外层（RuntimeConfig.MaxRetries）
- 代码默认：`NewLLMRuntime(nil)` → `MaxRetries: 10`
- 启动时被 bootstrap 覆盖：`runtimeRetryConfigFromProviderConfigs` 取第一个 provider 的 `MaxRetries` → `UpdateRetryConfig`
- YAML：`providers.max_retries: ${PROVIDERS_MAX_RETRIES:-10}`；兜底 `retry.default_max_retries: ${RETRY_DEFAULT_MAX_RETRIES:-10}`
- 环境变量：`PROVIDERS_MAX_RETRIES`、`RETRY_DEFAULT_MAX_RETRIES`、`PROVIDERS_TRANSPORT_MAX_RETRIES`、`PROVIDERS_RETRY_DELAY_1`（初始退避，默认 30s）、`PROVIDERS_RETRY_MAX_INTERVAL`（默认 6m）

### 7.3 请求级覆写
- `applyRequestRetryPolicy`（retry_policy.go:426）：请求 metadata `disableRetries=true` 时，MaxAttempts/MaxTransportAttempts 全部置 1、规则清空 → 单次尝试。

### 7.4 配置适配（retry_config_adapter.go）
- `ProviderMaxRetriesFromAgentConfig`：`providers.max_retries` <0 直接返回；<=0 用 `retry.default_max_retries`；仍 <=0 用 10
- `ProviderMaxTransportRetriesFromAgentConfig`：<0 无限；<=0 用 `DefaultTransportMaxRetries=4`
- `RetryTuningFromAgentConfig` / `RetryRulesFromAgentConfig`：从 `agentconfig.Config` 转换

---

## 8. 典型场景推演

### 场景 A：持续 429（rate limit）
1. 内层 `Chat` 返回 429 → 分类 `rate_limit`（携带 Retry-After 提示）
2. 内层在业务预算内按提示延迟重试（capped 到 MaxDelay）
3. 内层 10 次耗尽 → `isHandoffEligibleError(429)` 命中 → 接力外层
4. 外层 `decisionForRetry` 重分类为 `rate_limit` → 继续按提示延迟重试
5. 若内层持续接力 → 外层最多 3 轮接力后快速失败
6. 总请求上限 ≈ 3 轮 × 内层 11 次 ≈ 33 次（每层之间都有间隔）

### 场景 B：连接挂死（响应头超时）
1. 内层第一次 `Chat` 等待响应头 60s 超时 → transport 计数 1
2. 第二次再超时 → transport 计数 2 … 直到 `MaxTransportAttempts=4` 耗尽
3. 请求阶段耗尽 → `provider transport call failed` **接力外层**（瞬态失败；
   总请求上限 ≈ 3 轮 × 4 次 = 12 次，由外层 fast-fail 守卫封顶）
4. 外层接力重分类 `transport` → 按间隔重试，连续 3 轮接力后终止
5. （若内层预算无限）连续 2 次响应头超时触发 streak 守卫 → 同样接力外层

### 场景 C：SSE 流中断（HTTP 200 后 EOF）
1. 内层收到 200，流读取到一半 EOF → `read SSE stream: unexpected EOF`
2. 未输出可见内容 → 不是 `retrySuppressed`；计入 transport 预算
3. transport 预算耗尽 → **接力外层**（全新请求值得再试）
4. 若已输出正文且错误为确定性 → `suppressRetry` 终态；瞬态错误继续重试（partial-output replay，接受重复的部分输出，`llm.retry` 事件带 `partial_output=true`）

### 场景 D：确定性错误（配额/参数错误）
- `quota_exhausted`、`invalid_request`、`content_filter` 等
- 内层立即终态；`isHandoffEligibleError` 未命中 → 外层也终态
- `DiagnoseFailure` 给出 `UPSTREAM_QUOTA_EXHAUSTED` / `UPSTREAM_INVALID_REQUEST` 等错误码与 `NextAction`
- `invalid_tool_arguments` 自 2026-09-14 起**不**属于本场景（见场景 D2）

### 场景 D2：非法工具参数（`invalid_tool_arguments`，2026-09-14 起可重试）
1. 适配器聚合 `tool_calls` 时发现参数不是合法 JSON 对象 → `MalformedToolCallError`（`code=invalid_tool_arguments`），并在丢弃响应前带上本响应的 `finish_reason`（`FinishReason` / `Truncated`），把两类成因分开处置：
   - **A 预算截断**（`finish_reason=length`）：参数被 completion 预算切断，必须放宽预算或拆分 payload；
   - **B 语法退化**（`finish_reason=tool_calls`）：模型写出了非法 JSON 字面量（如裸 token `120s`），靠重采样 + schema re-prompt 恢复。
2. 分类为可重试的退化采样：`isDegenerateOutputRetryReason` 命中 → 退避封顶 `degenerateOutputRetryMaxDelay`（2s），且**不**参与 `isHandoffEligibleError` 接力（避免三层循环各自重放满预算）
3. 预算型截断同时把下一次尝试的 `max_tokens` 翻倍（`isOutputBudgetEscalationReason`）；runtime 层重试耗尽后，agent loop 仍会为 A 类补一次 8k→64k 的一次性升级（`shouldEscalateTruncatedToolCallBudget`），因为截断响应的 `finish_reason` 只存在于错误对象里、成功路径的 escalate 不可达
4. 连续 `degenerateOutputReplyMaxStreak`（3）次仍是非法参数 → `markRetryExhausted` 终态；Unwrap 链保留 `*MalformedToolCallError`
5. 执行层第二通道：agent loop 捕获该错误后把「工具未执行 + 参数原文 + 工具 schema」回注为 `tool_result` re-prompt（`maxMalformedToolCallRecoveries = 2`），turn 不因单次非法采样终止
6. 工具从未执行 → CLI 恢复提示明确「无副作用」，不再复述通用的"避免重复工具副作用"告警
7. 降级计数是**连续**语义：任一工具成功执行即清零（`resetMalformedToolCallRecoveriesOnSuccess`，成功=该次调用真正执行且无拒绝/执行错误），避免跨步骤的历史累计提前耗尽 `maxMalformedToolCallRecoveries`
8. CLI turn 级有界自动重跑（`maybeAutoRetryDegenerateTurn`，2026-09-14）：`sendMessage` 的错误分支识别 `*MalformedToolCallError`（工具未执行、无副作用）后自动重跑同一轮；次数由 `AICLI_TURN_AUTO_RETRY_LIMIT` 控制（默认 2，0 关闭），退避 800ms 起指数放大、封顶 5s，等待可被 Esc/Ctrl+C 取消。重跑成功即按成功收尾（继续 goal 续跑），仍失败才落回既有错误渲染与 `/retry` 提示；重跑期间错误类别变化（额度/传输等）立即停止

### 场景 E：GatewayClient 多上游 failover（首个上游 503）
1. `SelectResource` 选中组 `default` 的提供商 A（API key a1）
2. A 返回 503 → `RecordResult(A, false)` 记账；`TriedGroups/TriedProviders/TriedAPIKeys` 记录 A/a1
3. `isRetryableGatewayProviderError(503)` 命中 → `prepareRetry` 退避后重试
4. 下一次 `SelectResource` 依据 retryInfo 避开 A → 选提供商 B（或 A 的其他 key）
5. B 成功 → `RecordResult(B, true)`，返回；失败则继续换路，直到 `maxRetries` 耗尽
6. 全部候选耗尽且 `isHandoffEligibleError(503)` 命中 → 接力外层，外层按间隔整体重试一轮
7. 若 A 一直 503 且 B 每次都成功 → 内层 1 次失败 + 剩余尝试全部成功，几乎不消耗外层预算

---

## 9. 可观测性

### 9.1 RetryEvent（retry_events.go）
每次实际重试前通过 `prepareRetry` 上报，字段：
`Source / Provider / Protocol / Model / Attempt / MaxAttempts / Error / RetryReason / ErrorCode / RetryDelayMS / LogicalTurnID / LLMRequestID / RetryAttemptID / ProviderRequestID / StreamID / PartialOutput`

- `PartialOutput`（2026-08-31）：本次重试前的失败尝试已流出用户可见内容（partial-output replay）；事件 payload 落为 `partial_output=true`，aicli 渲染为 `partial_output=true` 注记
- 注册：`WithRetryEventReporter(ctx, reporter)`（可组合多个）
- 上下文中的尝试状态（`withHTTPDebugRetryAttempt`）自动回填 ID 字段

### 9.2 HTTPDebugEvent
`reportHTTPDebug` 上报 `retry` 阶段（prepareRetry 内）以及各阶段失败详情（响应体预览等）。

### 9.3 失败诊断（retry_policy.go）
- `ClassifyFailureCode(err)`：稳定的厂商中立错误码
- `DiagnoseFailure(err) → FailureDiagnostic{ErrorCode, Retryable, NextAction}`

**耗尽还原语义**：`retryExhaustedError` 包装本身表示"该层预算耗尽"，但 `DiagnoseFailure` 会解开包装、按底层 cause 还原语义：

- cause 是瞬态/采样可重试错误（5xx / transport / 流中断 / 系统资源不足 / 429 / 截断工具调用）→ `Retryable: true`，NextAction 指引"bounded backoff 后换 provider/上报"，让会话层（goal 自动续跑、下轮用户输入）自动发起新尝试，而不是把预算耗尽误报为"不可重试、必须手动继续"
- cause 是确定性错误（quota / invalid_request / content_filter / 用户取消）→ `Retryable: false`，保持终态；`retrySuppressedError`（已输出内容 + 确定性错误）同样保持终态

错误码映射（`classifyLLMFailureCode`）：

| 错误码 | 来源 |
|---|---|
| `USER_CANCELLED` | context.Canceled |
| `CONTEXT_BUDGET_EXCEEDED` | 上下文超长 |
| `UPSTREAM_QUOTA_EXHAUSTED` | 配额/余额耗尽 |
| `PERMISSION_DENIED` | 401/403 |
| `UPSTREAM_INVALID_REQUEST` | 400 / invalid_request / 非法工具参数 |
| `UPSTREAM_RATE_LIMITED` | 429 / rate_limit |
| `STREAM_INTERRUPTED` | 流中断 / 空回复 |
| `UPSTREAM_UNAVAILABLE` | 5xx / transport / 系统资源不足 |
| `CONTENT_FILTERED` | 内容过滤 |
| `UPSTREAM_INVALID_RESPONSE` | malformed_tool_call / truncated_tool_call / 格式错误 |

---

## 10. 设计要点与约束

1. **两层互相配合**：内层在 transport 小预算上快速失败（连接超时后快速断开），外层用大预算保证总重试次数；内层"快速断开 + 接力"避免把外层预算浪费在死连接上。
2. **transport 预算绑定死连接**：请求阶段 transport 耗尽在内层快速失败（不烧业务预算），但作为瞬态失败接力外层；外层 fast-fail 守卫（3 轮）防止对不可达上游无限建连。
3. **已输出内容 + 瞬态错误 → 强制重试（partial-output replay，2026-08-31）**：流式中途出错若已产生可见内容，只有确定性错误（quota/invalid_request/content_filter/取消）保持 `suppressRetry` 终态；瞬态/采样类错误（SSE EOF/连接重置/空闲超时/5xx/429/流中断/空回复/截断工具调用）继续走完整重试链路，重放重新生成全文并接受重复的部分输出。重放事件通过 `llm.retry` 的 `partial_output=true` 标记透传（RetryEvent.PartialOutput → loop.go/skills handler payload → aicli `chatLLMRetryParts` 渲染 `partial_output=true`）。判定入口：`mustSuppressRetryAfterEmission`（retry_policy.go，非 `classifyRetryableLLMError().Retryable` 即抑制）；标记：`withPartialOutputMarker`/`errHasPartialOutput`（`partialOutputError` 包装，对分类透明）。
4. **已输出内容永不重复计费重试的例外已收窄**：历史行为是"已输出即永不重试"，导致 aicli 端 `read SSE stream: unexpected EOF` 在已流出部分正文后零重试直接终态（2026-08-31 修复前）；现仅确定性错误保留该语义。
4. **无限预算防放大**：外层无限（MaxRetries=-1）时接力不会扩展循环；连续接力 3 轮封顶。
5. **尊重服务器提示但设上限**：Retry-After 优先，但被 MaxDelay 截断，防止异常巨大的等待时间挂死请求。
6. **确定性错误零重试**：配额、请求参数（`invalid_request`）、内容过滤等重放无法修复的错误立即终态并给出明确 NextAction。例外（2026-09-14）：4xx 报文里出现显式上游瞬时信号时不再按状态码一刀切终态，而是按 `transient_stream_or_server` 重试；判定顺序保证确定性 needle（`invalid_request_error`/api key/内容检查）与认证计费类状态（401/402/403）仍然优先终态。
7. **退避带抖动**：默认 ±10% 抖动，避免同步重试风暴。
8. **退化输出 = 短退避 + 输出预算升级（2026-09-13，2026-09-14 扩展到截断/非法工具调用）**：`empty_reply` / `reasoning_only_empty_reply` / `truncated_tool_call` / `invalid_tool_arguments` 的成因是模型把整个 completion 预算烧在 reasoning 或被预算截断（常伴 `finish_reason=length`），用同样预算重放基本只复现同一退化样本。两条对策：①外层延迟上限见 §3 `degenerateOutputRetryMaxDelay`（2s）；②内层在 `reasoning_only_empty_reply` / `truncated_tool_call` / `invalid_tool_arguments` 上把下一次尝试的 `max_tokens` 翻倍（`escalateOutputBudgetForDegenerateReply`，provider_retry.go；最多 2 次、封顶 `EscalatedMaxTokens`（64000，2026-09-14 从 32768 对齐，见 §11.5-1）；请求未显式设置预算（`<=0`）时改用模型默认预算 `DefaultModelMaxOutputTokens`（32000，不含 8k 槽位保留上限）作为升级起点、只升不降；已达上限/次数用尽不改；`empty_reply` 与预算无关，不 widen）。接入三处：`Call`（max_tokens 超限恢复块之后）、`callStreamingAggregate`（升级后必须 `buildStreamingBody` 重建 `adapterRequest`/`requestBody`/`bodyBytes`/`headers`，否则重建请求仍带旧预算）、GatewayClient（每次调用从 `req` 重建 body，无需手工重建）。重试分类与尝试次数语义不变。
9. **重试面审计补遗（2026-09-14）**：按"能重试就重试"复核全部终态分支后收敛出四类缺口并修复：①4xx 瞬时信号救援（`transientUpstreamTextHint` + `isRetryableTransportError`，400 与 404/405/422/499 等，401/402/403 除外）；②HTTP `425` Too Early 纳入可重试状态；③`empty_provider_choices`（200 + 空 choices）归入 `empty_reply`，获得 2s 短退避、3 次退化 streak 上限与 handoff 资格（旧分类落到 `default_retryable`，要等完整指数退避且不受 streak 保护）；④`isHandoffEligibleError` 改为 `http_5*` 前缀匹配，让 529/520-524/598 等 5xx 也能接力外层。同时 `classifyLLMFailureCode` 让"瞬态 reason 优先于裸状态码"，避免瞬时 400 被上报成 `UPSTREAM_INVALID_REQUEST` + "修正请求参数"的误导性 NextAction。仍保持终态：`retry_exhausted`/`retry_suppressed`/`context_canceled`/`quota_exhausted`/上下文窗口/`invalid_request`/api key/`content_filter`/内容检查。⑤`invalid_tool_arguments`（非法/截断的工具参数）从「确定性零重试」改为可重试的退化采样：`RetryErrorCode()` 驱动分类、短退避封顶 2s、预算型截断翻倍 `max_tokens`、连续 3 次 streak 收敛、不 handoff；收敛后由执行层按 schema re-prompt（agent loop，`maxMalformedToolCallRecoveries = 2`），CLI 恢复提示明确「工具未执行（无副作用）」而不是通用告警。
10. **退化 streak 上限下沉到 runtime 层（2026-09-14）**：`LLMRuntime.Call` / `openStreamWithRetry` / `forwardStreamWithRetry` 三处循环此前对本层 Provider 直接返回的退化错误（测试/自定义 Provider、`ProviderWrapper` 之外的实现）没有 streak 保护，会各自重放满 runtime 预算（生产默认 `MaxRetries=10` → 11 次）。现三处复用 `trackDegenerateOutputReply`：非 handoff 的退化 cause 连续 `degenerateOutputReplyMaxStreak`（3）次即 `markRetryExhausted` 收敛（消息 `LLM call aborted after repeated degenerate replies` / `LLM stream aborted after repeated degenerate replies`），`retryAtNextLayer=true` 的接力（`isNextLayerHandoffError`）仍走既有的连续接力守卫（`maxConsecutiveFastFailHandoffs = 3` 轮）以保留 provider/key 轮换。
11. **CLI turn 级有界自动重跑（2026-09-14，`chat_turn_auto_retry.go`）**：`invalid_tool_arguments` 收敛为 `MalformedToolCallError` 时工具未执行、无副作用，此前只能由用户手动重发或 `/retry`。现在 `sendMessage` 的错误分支先调用 `maybeAutoRetryDegenerateTurn`：命中即按 800ms 起、指数放大、封顶 5s 的退避重跑整轮（重建 `RequestTimeout` 预算、Esc/Ctrl+C 可取消、每次渲染 `[turn] 自动重跑 n/m` 本地提示），成功则直接进入既有的 goal 自动续跑与成功收尾；仍失败才提示 `自动重跑已达上限` 并落回错误渲染 + `/retry` 建议。次数由 `AICLI_TURN_AUTO_RETRY_LIMIT` 控制（默认 2，0 关闭），重跑期间错误类别变化（额度/传输/协议）立即停止，保证总调用次数有界。
    - **「无副作用」由断言升级为构造性前提（2026-09-14 补口）**：整轮重放会重放本轮**已经执行过**的工具调用（一次多步 turn 完全可能第 1 步执行成功、第 2 步才退化失败），所以 `shouldAutoRetryTurnError` 现在额外要求 `chatTurnToolExecutions(session) == 0`。CLI 自己就是工具执行方（`aicliToolExecutor`），在参数截断预检通过、真正调用 handler 之前记账（`recordChatTurnToolExecution`）；计数在每个用户 turn 开始时清零（`resetChatTurnToolExecutions`），自动重跑不经过该入口，因此重跑期间计数会累积——一旦某次重跑执行过工具，后续重放立即停止。这与 `/retry` 既有规则一致（「本轮可能已部分执行工具」时不自动执行），恢复提示也据此改写。
    - **`empty_reply` 纳入可重跑类别**：它是唯一「既没有渲染过输出（聚合校验器先分流 `reasoning_only_empty_reply` / `truncated_tool_call`，之后才判空）、又不在输出预算升级集合（`isOutputBudgetEscalationReason`）里」的退化类别，重采样没有可用杠杆，此前只能等内层 streak 耗尽后终止整轮。现在 `turnAutoRetryReason` 在 `MalformedToolCallError` 之外接纳 `llm.IsEmptyReplyError`（穿透 `retryExhaustedError` 终态包装逐层分类），提示文案分型为「模型本轮回复为空（未渲染内容、未执行工具）」。`reasoning_only_empty_reply` / `truncated_tool_call` / `stream_interrupted` 仍排除（可能已渲染半截输出）。

---

## 11. `invalid_tool_arguments` 根因与取证（2026-09-14）

### 11.1 症状
`invalid_tool_arguments` 在 2026-09-13/14 两天集中出现：`~/.aicli/chat-logs` 下 31 个会话文件、264 行命中，折算约 110–150 次真实失败；同一会话同一工具在数十秒内反复失败（`max_attempts=10` 跑满），单个会话烧掉 45,044,921 tokens / 647 steps。provider 全为 `opencode.ai`、model 全为 `deepseek-v4.1-flash`，涉事工具以 `shell` 为主（另有 `grep` / `multiedit` / `edit`）。

### 11.2 同一个错误码折叠了两类成因
判定点只有一处：聚合完成后对 `arguments` 做 `json.Unmarshal(..., map[string]any)`，失败或为 `null` 即报错（`adapter/openai.go` 的 `validateOpenAIStreamState` / `validateOpenAIRawToolCalls`）。它**不看 `finish_reason`**，于是两类完全不同的故障被压成同一个错误：

| 类别 | 成因 | 取证 |
|---|---|---|
| A 预算截断 | 参数在聚合前被 completion 预算切断，`finish_reason=length` | `20260914_083936_522_5d1a080c.http/456_response_provider_wrapper.json`：`multiedit` 把一整段 Go 测试代码当参数流到一半被切断，`body_bytes=2,136,062`（抓包按 262,144 字节截断，`body_truncated=true`） |
| B 语法退化 | 模型写出非法 JSON 字面量，`finish_reason=tool_calls` | `20260914_075729_196_5a7e6021.http/1173`、`/1178`：SSE 增量复原后为 `{"command": "...", "timeout": 120s, "workdir": "..."}` —— 裸 token `120s` 未加引号，拼接后在 358/102 字符处解析失败；`completion_tokens` 仅 1724/212（reasoning 1392/111），**远低于 8000 预算**，同响应另一条 `edit` 调用是合法 JSON |

### 11.3 机制链
1. `provider.go` 先调 `HandleResponse`，拿到 `MalformedToolCallError` 直接 return；而 `truncated_tool_call` 判定（`assistantMessageHasTruncatedToolCall`，要求 `finish_reason=length`）在其之后，永不执行 → A 类被误贴"JSON 语法错"，`finish_reason` 未进错误对象，后续基于 `finish_reason` 的补救全部失效。
2. 旧策略把 `invalid_tool_arguments` 判为不可重试（"重放无法修复非法 JSON"）：对 B 类不成立（裸 token 是采样随机性，重采样有机会拿到合法参数），对 A 类更不成立（同预算必然再次截断）→ 零重试、零预算扩展、终点报错。
3. B 类的直接诱因是工具面：`timeout` 声明为 `type: string`，描述示例却写作裸 token（`例如 30s、2m、5m`）→ 模型照抄产出 `120s`。同一工具面还有 4 个重叠超时参数（`timeout` / `timeout_ms` / `timeout_sec`），描述里自己都写着"小于 100 的数值会视为模型单位混淆"。
4. A 类的放大器是 8000 token 槽位预留（`CappedDefaultMaxTokens`）遇到"参数即正文"的 `write_file` / `multiedit`。

### 11.4 修复映射
- **错误对象携带 `FinishReason` / `Truncated`**：适配器在丢弃响应前把 `finish_reason` 带出来（流式取 `state.FinishReason`；非流式在参数校验**之前**取出 `choice.finish_reason`；codex 取 `incomplete_details.reason`），`Truncated` 由 `length` / `max_tokens` / `max_output_tokens` 命中；消息改为 "cut off by the completion budget"。归因码仍是 `invalid_tool_arguments`（分类键与重试语义不变，避免影响 retry policy 的消息匹配）。
- **截断型可升级预算**：`shouldEscalateTruncatedToolCallBudget`（agent loop）在 error 路径复用成功路径的一次性 8k→64k escalate（`llm.max_output_tokens.escalated`，payload 带 `reason=truncated_tool_call` 与 `finish_reason`）。响应没有返回、`finish_reason` 只存在于错误对象里，这条通道此前不可达；语法退化型**不** widen 预算（扩大预算只会放大同类样本）。
- **聚合层截断同样可升级（2026-09-14 补口）**：判据在 `MalformedToolCallError.Truncated` 之外接纳聚合校验层的 `truncated_tool_call` reason（`llm.IsTruncatedToolCallError` 穿透 `retryExhaustedError` 终态包装逐层分类）。此前该 reason 是唯一没有上层出口的截断形态：`MalformedToolCallError` 可升级 + re-prompt + turn 自动重跑，`reasoning_only_empty_reply` 至少可升级，而它只能等内层退化 streak 耗尽后终止整轮（也不满足 re-prompt / turn 自动重跑的 `MalformedToolCallError` 判据）。
- **re-prompt 文案分型**：截断型提示"拆分 payload / 多次小写入"，语法型保持"按 schema 重发"。
- **turn 级自动重跑的两个补口（2026-09-14）**：①`empty_reply` 成为可恢复类别——`llm.IsEmptyReplyError` 穿透终态包装逐层分类（聚合校验器先分流 reasoning/truncated 再判空，因此该类别必无渲染），CLI 判据在 `MalformedToolCallError` 之外接纳它，`turnAutoRetryLimit()` 仍统一收敛次数；②「无副作用」由注释断言升级为构造性保证——`chatTurnToolExecutions` 在工具真正执行前记账（`aicliToolExecutor.ExecuteTool`，参数截断预检之后）、每个用户 turn 开始时清零；计数 > 0 时不再自动重跑，恢复提示也改为「本轮已执行过工具，为避免重复副作用系统未做自动重跑」，与 `/retry` 既有规则对齐。
- **工具面文案**：4 处 `timeout` 描述改为带引号的 JSON 字面量示例（`例如 "30s"、"2m"、"5m"`），并显式标注"裸写 30s 会让整个 arguments 变成非法 JSON"。

### 11.5 仍未闭环
1. runtime 层 `outputBudgetEscalationCeiling` 已对齐 loop 层 `EscalatedMaxTokens`（64000，2026-09-14）。取证补充：对 8k 槽位预留路径，真正的约束是 `degenerateOutputReplyMaxStreak`（3）而不是天花板——三次采样跑在 8k/16k/32k，第三次预算由第二次翻倍（2×16k=32k）决定，旧天花板 32768 本就不构成限制；对齐只对显式预算落在 (16k, 32k] 的请求生效（这类请求 loop 按设计不做一次性升级，内层翻倍是唯一杠杆，旧天花板会把第一次翻倍从 40000 之类的值压回 32768）。超大 `multiedit`（>64k 输出）仍会截断，此时靠 re-prompt 拆分 payload 收敛。
2. 原始 arguments 仍只落在 http 抓包里（按 262,144 字节截断），错误载荷只有 index + 工具名，排障依赖抓包。
3. 4 个重叠 `timeout` 参数仍在；长期应收敛为单一的 `timeout_sec`（integer），从接口上消灭单位混淆与字符串引号问题。
