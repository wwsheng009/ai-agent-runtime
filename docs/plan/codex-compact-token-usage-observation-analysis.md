# Codex compact 观察值与 token usage 口径分析

状态: updated

调研日期: 2026-05-02

## 目标

本文件只做一件事: 把 `codex-rs` 里和 compact 相关的 token 观察值口径彻底拆开, 说明哪些值用于:

1. 自动 compact 触发
2. UI 上的上下文显示
3. 会话恢复 / fork 的 token 信息回放
4. OpenAI quota / rate limit 展示

Codex 结论先行:

- **compact 的观察值不是 session 生命周期的累计总和**
- **compact 的观察值是当前活跃上下文的 token 占用快照**
- **`total_token_usage` 和 `usedPercent` 都不是 compact 的主观察值**

本项目最新需求与 Codex 的 compact 观察值语义对齐。`aicli chat` 的 `ctx used` 明确采用:

```text
ctx used = 最近一次 LLM API 响应确认的 active context token 快照
```

它与模型 context window 比较, 达到或超过自动压缩阈值时触发 session-level compact。当前项目没有完整复刻 Codex 的 `TokenUsageInfo.last_token_usage + pending items` 数据结构, 因此用 `ContextTokenCount` 表示最近一次 provider 确认的 active context 快照, 在缺失显式快照时才用当前 history 的本地 token 估算兜底。actor pre-turn auto compact 会优先读取 `aicli_context_token_count`, 并补上最后一次 assistant 之后新加入的本地 pending items 估算, 尽量贴近 Codex 的公式。

这与 Codex 的 `last_token_usage.total_tokens + pending items` 口径保持同一目标, 但数据结构更简化。`aicli` 状态栏面向的是“当前 active context 大小”, 所以 provider usage 可用时优先使用 `total_tokens` / `input_tokens + output_tokens` 形成当前快照; completion/output 是下一轮可见上下文的一部分, 不能只进入累计 API usage。普通请求在 compact 前从逻辑上只会让 context 增大, 所以状态栏更新必须单调递增; 只有 session compact、手动 `/compact`、`/new`、`/clear` 或 reset 边界允许降低。

如果把 `total_token_usage` 或本项目的 `TokenCount` 当成 compact 观察值, 很容易出现两类错误:

- 会话越长, `used` 越大, 甚至超过 context window 数倍
- compact 后 active context 已经变小, 但累计 API usage 不会变小, 导致刚压缩完又继续误触发 compact

`TokenCount` 仍然保留, 但它的含义是“当前会话真实 LLM API usage 的累计记账值”, 用于 `/status` 的 `Token count` / `Token usage` fallback 和调试, 不用于 `ctx used` 和 compact 阈值。

## 一句话结论

在 `codex-rs` 里, 自动 compact / compaction analytics 看的核心值是:

```text
Session.get_total_token_usage()
  -> ContextManager.get_total_token_usage(server_reasoning_included)
  -> last_token_usage.total_tokens
     + items_after_last_model_generated_item 的估算 token
     (+ 视 server_reasoning_included 决定是否补 reasoning)
```

这不是 `TokenUsageInfo.total_token_usage` 的累计总和, 也不是 `RateLimitWindow.used_percent`。

## 关键概念

### 1. 会话累计 usage snapshot

`TokenUsageInfo.total_token_usage` 表示的是“从当前会话开始, 已经累积记账过的 token 使用量”。

它会随着每次 `ResponseEvent::Completed { token_usage, .. }` 追加:

- 进入 `Session.update_token_usage_info(...)`
- 调用 `ContextManager.update_token_info(...)`
- 在 `TokenUsageInfo::new_or_append(...)` 中做累计

这个值用于:

- UI 的 `UsedTokens` 显示
- `thread/tokenUsage/updated` 事件中的 `total`
- 恢复后展示历史 token 使用量

但它**不是**自动 compact 的阈值输入。

### 2. 当前 active context snapshot

`TokenUsageInfo.last_token_usage` 表示的是“最近一次有效 token snapshot”。

在当前实现里, 它是上下文观察值的基础, 因为:

- `ContextManager.get_total_token_usage(...)` 直接拿它做基数
- `ContextRemaining` / `ContextUsed` 的 UI 口径基于它
- compact 之后 `recompute_token_usage(...)` 会重写它

这个值比 `total_token_usage` 更接近“当前上下文还剩多少 / 已经用了多少”。

### 3. quota / rate limit usedPercent

`RateLimitWindow.used_percent` 只表示 OpenAI quota 窗口的消费比例。

它属于账号 / 限额维度, 和模型 context window 没有同义关系。

这个值不能拿来驱动 compact。

## 代码主链路

### 1. compact 触发点读的是 active context, 不是累计总和

文件:

- `E:\projects\ai\codex\codex-rs\core\src\session\turn.rs`

关键逻辑:

```rust
let total_usage_tokens = sess.get_total_token_usage().await;
let auto_compact_limit = turn_context
    .model_info
    .auto_compact_token_limit()
    .unwrap_or(i64::MAX);
if total_usage_tokens >= auto_compact_limit {
    run_auto_compact(...).await?;
}
```

`maybe_run_previous_model_inline_compact(...)` 也使用同一口径:

```rust
let should_run = total_usage_tokens > new_auto_compact_limit
    && previous_model_turn_context.model_info.slug != turn_context.model_info.slug
    && old_context_window > new_context_window;
```

这说明自动 compact 的阈值比较对象是 **当前活跃上下文 token 数**。

### 2. `Session.get_total_token_usage()` 的含义

文件:

- `E:\projects\ai\codex\codex-rs\core\src\session\mod.rs`

实现要点:

```rust
pub(crate) async fn get_total_token_usage(&self) -> i64 {
    let state = self.state.lock().await;
    state.get_total_token_usage(state.server_reasoning_included())
}
```

它的文档注释已经写得很明确:

- 这是“当前 session 缓存的完整 token usage snapshot”
- resume / fork 会从持久化的 `TokenCount` 事件恢复

这里的“完整”指的是 **当前活跃会话快照**, 不是整个生命周期的累计流水。

### 3. 真正的计算公式在 `ContextManager`

文件:

- `E:\projects\ai\codex\codex-rs\core\src\context_manager\history.rs`

核心逻辑:

```rust
pub(crate) fn get_total_token_usage(&self, server_reasoning_included: bool) -> i64 {
    let last_tokens = self
        .token_info
        .as_ref()
        .map(|info| info.last_token_usage.total_tokens)
        .unwrap_or(0);
    let items_after_last_model_generated_tokens = self
        .items_after_last_model_generated_item()
        .iter()
        .map(estimate_item_token_count)
        .fold(0i64, i64::saturating_add);
    if server_reasoning_included {
        last_tokens.saturating_add(items_after_last_model_generated_tokens)
    } else {
        last_tokens
            .saturating_add(self.get_non_last_reasoning_items_tokens())
            .saturating_add(items_after_last_model_generated_tokens)
    }
}
```

这个公式有两个关键点:

1. 基数是 `last_token_usage.total_tokens`
2. 再补上“最后一次模型生成之后新增的本地历史项”

这就是为什么它更像“当前上下文占用”而不是“历史累计总和”。

这里不能只取 input / prompt tokens。一次成功响应后, 下一轮模型可见上下文里通常已经包含了本轮 assistant 输出, 所以 compact 观察值必须覆盖 input + output。Codex 使用的是 `last_token_usage.total_tokens`, 而不是 `last_token_usage.input_tokens`。

`usage.total_tokens` 的语义按主流 OpenAI-compatible / Responses / Chat Completions 口径理解为本次 API 响应总 token, 通常是:

```text
total_tokens = input_tokens(prompt_tokens) + output_tokens(completion_tokens)
```

`cached_input_tokens` 是 input 中命中缓存的子集, 不能从 `total_tokens` 中扣掉; `reasoning_output_tokens` 是 output / completion 侧的推理 token 明细, 当前项目不额外加一次, 避免在 provider 已把 reasoning 纳入 `total_tokens` 时重复计算。

Anthropic 兼容响应还可能返回 `cache_read_input_tokens` / `cache_creation_input_tokens`。这类字段在部分 provider 中不是 `input_tokens` 的子集, 而是额外的缓存输入明细; 当响应没有显式 `total_tokens` 时, 本项目按 `input_tokens + output_tokens + cache_read_input_tokens + cache_creation_input_tokens` 合成 total, 避免把 active context 低估成“非缓存 input + output”。

### 4. `TokenUsageInfo.total_token_usage` 只是累计记账

文件:

- `E:\projects\ai\codex\codex-rs\protocol\src\protocol.rs`

逻辑:

```rust
pub fn new_or_append(
    info: &Option<TokenUsageInfo>,
    last: &Option<TokenUsage>,
    model_context_window: Option<i64>,
) -> Option<Self> {
    ...
    if let Some(last) = last {
        info.append_last_usage(last);
    }
    ...
}

pub fn append_last_usage(&mut self, last: &TokenUsage) {
    self.total_token_usage.add_assign(last);
    self.last_token_usage = last.clone();
}
```

这说明:

- `total_token_usage` 会累加
- `last_token_usage` 会被覆盖成最新快照

所以这两个字段语义不同, 不能互换。

### 5. compact 后会重写 `last_token_usage`

文件:

- `E:\projects\ai\codex\codex-rs\core\src\compact.rs`

compact 完成后会调用:

```rust
sess.replace_compacted_history(...).await;
client_session.reset_websocket_session();
sess.recompute_token_usage(&turn_context).await;
```

`recompute_token_usage(...)` 的行为是:

```rust
let estimated_total_tokens =
    history.estimate_token_count_with_base_instructions(&base_instructions)?;

let mut info = state.token_info().unwrap_or(TokenUsageInfo {
    total_token_usage: TokenUsage::default(),
    last_token_usage: TokenUsage::default(),
    model_context_window: None,
});

info.last_token_usage = TokenUsage {
    total_tokens: estimated_total_tokens.max(0),
    ..TokenUsage::default()
};
```

这说明 compact 后:

- 历史被替换
- `last_token_usage` 被重算成新的压缩后上下文占用
- `total_token_usage` 仍然保留为累计流水

所以 compact 的观察值会随着压缩动作被“重新定标”。

## 恢复与重置边界

### 1. Resume / fork 会恢复最后一次 token snapshot

文件:

- `E:\projects\ai\codex\codex-rs\core\src\session\mod.rs`

恢复逻辑:

```rust
if let Some(info) = Self::last_token_info_from_rollout(&rollout_items) {
    let mut state = self.state.lock().await;
    state.set_token_info(Some(info));
}
```

也就是说:

- 从 rollout 里找最后一个 `EventMsg::TokenCount`
- 用它恢复 `token_info`

这就是为什么 resume / fork 之后, 即使还没打新 token, UI 也可能直接显示一个非零值。

### 2. `/new`、`/clear` 不应继承旧 token snapshot

文件:

- `E:\projects\ai\codex\codex-rs\core\src\session\session.rs`
- `E:\projects\ai\codex\codex-rs\tui\src\app\event_dispatch.rs`

`InitialHistory::New | InitialHistory::Cleared` 会走新会话初始化路径, 不从旧 rollout 恢复 token snapshot。

对应 TUI 行为:

- `/clear` 会启动新会话, 且 `ThreadStartSource::Clear`
- `/new` 也是新会话

因此, 这两个命令都是 **token observation reset boundary**。

### 3. `/compact` 是“重算”不是“清零”

compact 不是把 token usage 清零, 而是:

1. 替换历史
2. 重新估算当前上下文占用
3. 继续保留累计总和

所以 `/compact` 之后:

- `last_token_usage` 会反映压缩后的当前上下文
- `total_token_usage` 继续保留历史累计

这也是为什么 compact 观察值必须看 active context, 而不能看累计总和。

## UI 侧的三个不同指标

### 1. `UsedTokens`

文件:

- `E:\projects\ai\codex\codex-rs\tui\src\chatwidget\status_surfaces.rs`

实现:

```rust
StatusLineItem::UsedTokens => {
    let usage = self.status_line_total_usage();
    let total = usage.tokens_in_context_window();
    if total <= 0 {
        None
    } else {
        Some(format!("{} used", format_tokens_compact(total)))
    }
}
```

这里显示的是 `total_token_usage` 的总 token 数。

这更像“会话累计账单”, 不是 compact 阈值输入。

### 2. `ContextRemaining` / `ContextUsed`

文件:

- `E:\projects\ai\codex\codex-rs\tui\src\chatwidget.rs`

实现:

```rust
fn status_line_context_remaining_percent(&self) -> Option<i64> {
    let Some(context_window) = self.status_line_context_window_size() else {
        return Some(100);
    };
    let usage = self
        .token_info
        .as_ref()
        .map(|info| &info.last_token_usage)
        .unwrap_or(&default_usage);
    Some(usage.percent_of_context_window_remaining(context_window).clamp(0, 100))
}
```

这里用的是 `last_token_usage`。

注意它还有一个固定 baseline:

- `BASELINE_TOKENS = 12000`

所以 `percent_of_context_window_remaining()` 不是简单的 `used / window`。

### 3. `usedPercent` of rate limit

文件:

- `E:\projects\ai\codex\codex-rs\app-server-protocol\src\protocol\v2.rs`

它是 quota / rate limit 的消费比例, 只用于 `account/rateLimits/updated`。

它和 context usage 没有直接关系。

## 事件链路

### 1. 正常 turn 完成后的 token 回写

文件:

- `E:\projects\ai\codex\codex-rs\core\src\session\mod.rs`
- `E:\projects\ai\codex\codex-rs\app-server\src\bespoke_event_handling.rs`

链路是:

```text
ResponseEvent::Completed(token_usage)
  -> Session.update_token_usage_info(...)
  -> ContextManager.update_token_info(...)
  -> EventMsg::TokenCount(info + rate_limits)
  -> app-server 转成 thread/tokenUsage/updated
  -> TUI / status / client 侧更新显示
```

### 2. Resume / fork 的回放

`thread/tokenUsage/updated` 不是 compact 事件本身, 而是 token snapshot 的恢复通知。

当线程恢复或 fork 时, 服务器会先从 rollout 里恢复 `TokenCount` 事件, 然后再把这个 snapshot 回放给客户端。

### 3. compact 后的回写

compact 完成后会走 `recompute_token_usage(...)`, 也会发出 `TokenCount`。

所以 compact 之后看到的 usage 变化, 本质上是“新的上下文快照”。

## 为什么会出现“刚进 TUI 就有值”

从 `codex-rs` 的实现看, 只有几种可能:

1. 当前会话是 resume / fork, token snapshot 从 rollout 恢复了
2. 服务器已经回放了持久化 `TokenCount`
3. 当前会话已经经过某次 turn / compact, token snapshot 已经被写入

如果是一个真正的全新 `New` / `Clear` 会话, 且还没有任何 token 回写, 那么不应该凭空出现一个累计值。

## 为什么会出现“used 超过 100% / 500%”

这通常意味着把不同口径混用了。

最常见错误是:

- 用 `total_token_usage` 去对比 context window
- 没有在 `/new`、`/clear`、`/compact` 后重置当前观察值
- 把 `usedPercent` 当成 context usage

在 `codex-rs` 里, 正确的 compact 观察值应该对齐:

```text
last_token_usage.total_tokens
+ items_after_last_model_generated_item 的估算
```

而不是:

```text
total_token_usage.total_tokens
```

## 对当前项目的固化建议

当前项目按 Codex 口径对齐后的最终约定如下:

1. **主观察值**: 最近一次 LLM API 响应确认的 active context token 快照, 即 `ContextTokenCount`。
2. **重置边界**: `/new`、`/clear` 清空 `TokenCount`、`ContextTokenCount`、`TurnContextTokenCount`; 成功 `/compact` 或成功 auto compact 只更新 `ContextTokenCount` 并清空 `TurnContextTokenCount`, 不清空 `TokenCount`。
3. **恢复边界**: resume / history replay 分别从 runtime session metadata 恢复 `aicli_token_count` 与 `aicli_context_token_count`; context metadata 缺失时可以按当前 history 估算初始化, 但普通 history sync 不允许把已有的 provider-confirmed context 快照刷小。
4. **compact 判断**: 以 `ContextTokenCount` 为基数, actor pre-turn 会补上最后一次 assistant 后新增的 pending items 估算并与当前 history 估算取较大值; 若没有显式 `ContextTokenCount`, 使用当前 history 的 token 估算作为 active-context fallback。若没有显式 limit, 则使用 `max_context_tokens * auto_compact_ratio`, 默认 ratio 为 `0.9`; 若 provider/model 没有声明能力, 使用与 TUI 状态行一致的默认窗口 `256000` 和默认 ratio `0.9`。
5. **禁止混用**: `TokenCount`、`TurnContextTokenCount`、quota `usedPercent` 都不能作为 `ctx used` 主来源。
6. **精确修正优先级**: compact `token_after` / compact 后 history 估算可以重置显示值; 普通 provider usage 使用 `total_tokens` 确认 active-context 快照并按单调递增写入; provider usage 缺失或无法解析时才使用 request `context_prompt_tokens` / 当前 history 本地估算兜底。
7. **usage 明细边界**: `ContextTokenCount` 使用 provider `total_tokens` 作为 post-response active-context 快照。没有 `total_tokens` 时使用 `PromptTokens + CompletionTokens`; Anthropic/Mimo-style cache-read/cache-creation input 若在 provider usage 中作为 prompt 之外的字段返回, 需要补入 active context。OpenAI-style cached tokens 通常已包含在 input/total tokens 中, 不重复追加。reasoning tokens 不单独追加到 `ctx used`, 避免 provider 已纳入 `total_tokens` 时重复计算。`context_prompt_tokens` 是 request prompt 诊断值, request-start 时只进入 `TurnContextTokenCount`, 不提前显示。

保留字段含义:

- `TokenCount`: 当前会话累计真实 LLM API token usage。它是展示/记账值, 不驱动 `ctx used` 和 auto compact。
- `ContextTokenCount`: 最近一次 LLM API 响应确认的 active context token 快照。它驱动状态行 `ctx used`、`/status Context used` 和 auto compact。
- `TurnContextTokenCount`: 当前 turn 内请求 prompt token 诊断累计, 不驱动状态行百分比, 不参与 compact。
- `RateLimitUsedPercent`: quota / rate limit 窗口消费比例, 不参与 context compact。

## 本项目实现对齐记录

本项目在 `backend/cmd/aicli/commands` 里存在三个容易混淆的计数:

- `TokenCount`: 当前会话累计 API token usage。它用于 `/status` 的 `Token count` / `Token usage` fallback 和调试, 不参与 compact 阈值。
- `ContextTokenCount`: 最近一次 LLM API 响应确认的 active context token 快照。它是 TUI 状态行 `ctx <window> used <n> <pct>%`、`/status Context used` 和 auto compact 的主来源。
- `TurnContextTokenCount`: 当前 turn 内请求 prompt token 的诊断累计。它只用于 debug / 观测请求链路, 不参与 status 百分比, 不参与 session-level compact。

这三个值不能互相 fallback。尤其不能用 `TokenCount` 或 `TurnContextTokenCount` 填充 `ContextTokenCount`, 否则会出现:

- `ctx used` 退化成当前 request prompt size。
- 一个带工具调用的 turn 内多次 request 只显示最后一次 prompt, 而不是最后一次成功响应后的 active-context 快照。
- 会话累计 API usage 被当成 context window 占用, 导致 compact 后仍显示过高或反复触发 compact。

对齐后的写入规则:

1. 只有真实 LLM API 响应里的 `usage.TotalTokens` / `usage_total_tokens` 可以累加到 `TokenCount`。
2. `llm.request.started` / `llm.request.finished` 的 `context_prompt_tokens` 只能用于 request/turn 诊断, 不能写入 `TokenCount`, 也不能直接成为 `ctx used`。
3. `ContextTokenCount` 在 provider usage 可用时按 active context 精确修正; 优先使用 `TotalTokens`, 缺失 total 时使用 `PromptTokens + CompletionTokens`; Anthropic/Mimo-style provider 在 cache read / cache creation tokens 作为 prompt 之外字段返回时补入 `CachedTokens`。如果 provider 只返回 total, 直接使用 `TotalTokens`。
4. `ctx used` 和 `/status Context used` 读取 `ContextTokenCount`; 缺失显式快照时可以用当前 history 的本地估算兜底, 但空会话或只有 system prompt 时必须为 0。
5. session-level auto compact 使用同一个 active-context 观察值 `ContextTokenCount` / 当前 history 估算, 不使用 runtime metadata `aicli_token_count`。
6. 成功 compact 后, `ContextTokenCount` 记录 `token_after` 或压缩后 history 估算值; `TokenCount` 保持累计 API usage 不变。
7. `/new`、`/clear` 清空 `TokenCount`、`ContextTokenCount`、`TurnContextTokenCount`。
8. `ContextTokenCount=0` 是有效观察值; 新会话、刚 `/clear` 或 system-only history 不能因为 system prompt 本地估算值误显示为已使用。
9. 普通请求路径的 `ContextTokenCount` 写入是单调递增的。即使 provider usage 或 history sync 给出更小估算值, 也不能降低状态栏显示; 只有 compact / reset 边界可以降低。

当前实现与需求的核心差异曾经在于: request 事件和 provider usage、history 估算被混到同一个显示值里, 于是 `ctx used` 有时表现成“当前 request / turn”, 有时又表现成“整会话 API usage 累计”。修正后, `ctx used` 读取 `ContextTokenCount` 或当前 history 的 active-context 估算; request prompt 只保留在 `TurnContextTokenCount` 诊断字段中, `TokenCount` 只做累计 API usage 记账。普通 `contextmgr.Build` 也不再默认按 recent-window 裁掉旧 history, 避免在 session compact 前让 active context 变小并导致状态栏从大到小抖动。

## 参考代码

- `E:\projects\ai\codex\codex-rs\core\src\session\turn.rs`
- `E:\projects\ai\codex\codex-rs\core\src\session\mod.rs`
- `E:\projects\ai\codex\codex-rs\core\src\context_manager\history.rs`
- `E:\projects\ai\codex\codex-rs\core\src\compact.rs`
- `E:\projects\ai\codex\codex-rs\core\src\session\session.rs`
- `E:\projects\ai\codex\codex-rs\app-server\src\bespoke_event_handling.rs`
- `E:\projects\ai\codex\codex-rs\app-server-protocol\src\protocol\v2.rs`
- `E:\projects\ai\codex\codex-rs\tui\src\chatwidget.rs`
- `E:\projects\ai\codex\codex-rs\tui\src\chatwidget\status_surfaces.rs`
