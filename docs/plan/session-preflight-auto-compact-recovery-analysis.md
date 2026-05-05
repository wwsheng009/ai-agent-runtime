# 会话 preflight 拦截与自动压缩恢复分析

## 背景

本报告分析会话：

- Session File: `C:\Users\vince\.aicli\sessions\session_20260428220328_xp2YwmIC.json`
- Chat Log: `C:\Users\vince\.aicli\chat-logs\20260428_220328\chat_modelscope_openai_deepseek-ai_DeepSeek-V4-Flash_20260428_220328.json`
- HTTP Artifact: `C:\Users\vince\.aicli\chat-logs\20260428_220328\runtime-http`
- 失败提示: `prompt 14297 > budget 12000`
- 失败码: `active_turn_not_compactable`
- 模型: `modelscope / deepseek-ai/DeepSeek-V4-Flash`

用户期望是：当 active-turn replay 继续膨胀时，系统应自动压缩会话内容，并自动恢复执行，而不是让本轮以本地 preflight 拦截结束。

结论：当前实现已经有 active-turn replay 压缩、preflight fail-fast、whole-history compact recovery 和 actor 层重试，但这些机制在本会话里没有形成闭环。根因不是单点缺失，而是预算策略、压缩触发点、内部 compact 请求形态和失败兜底四处不一致。

## 现场证据

### 会话历史形态

会话末尾共有 16 条 history。第二个用户轮次是 `分类提交`，其后系统已经多次插入 active-turn replay 摘要：

- `idx=5..10` 均为 assistant summary，内容前缀为 `Compacted earlier tool replay in current turn:`
- 这些消息带有 `active_turn_compaction=true`
- 最新保留的 replay block 从 `idx=11` 开始：一个 assistant 工具调用消息加 4 个 tool result
- 4 个 tool result 内容长度约为 `8622`、`10933`、`1672`、`12063`

这与错误里的 `latest_replay_block=5` 对齐：最新工具调用块包含 1 条 assistant tool-call 消息和 4 条 tool result。`historyguard.CompactActiveTurnReplayWithCounter` 会保留最新 replay block，以避免破坏 tool-call / tool-result 邻接关系，因此当最新 block 自身超过预算时，会返回不可继续压缩。

### preflight 预算来源

失败提示显示：

- `prompt=14297`
- `budget=12000`
- `budget_source=default_context_max_prompt_tokens`
- `budget_source_detail=contextmgr.DefaultBudget().MaxPromptTokens`

代码位置：

- `backend/internal/contextmgr/manager.go`: `DefaultBudget().MaxPromptTokens = 12000`
- `backend/internal/agent/loop.go`: `resolvePromptPreflightBudget` 先加入 `default_context_max_prompt_tokens`，再加入 context manager 和 provider/model capability 候选，最终选择最小正数

因此即使 `backend/configs/config.yaml` 中 `modelscope` 的 wildcard capability 配了 `max_context_tokens: 270000` 和 `auto_compact_token_limit: 200000`，preflight 仍被 12k 默认预算拦截。

### auto compact 确实被尝试，但失败了

HTTP artifact 显示最后出现两次内部 compact 请求：

- `084_request_provider_wrapper.json`
- `085_request_provider_wrapper.json`

两者都有：

- `internal_operation=compact`
- `compact_mode=local`
- `compact_phase=pre_turn`
- `model=deepseek-ai/DeepSeek-V4-Flash`
- `max_tokens=2048`
- `reasoning_effort=none`
- `stream=false`
- `tools` 中仍包含 `list_mcp_resources`
- `tool_choice=auto`

对应响应：

- `084_response_provider_wrapper.json`: HTTP 200，但 `choices=null`
- `085_response_provider_wrapper.json`: HTTP 200，但 `choices=null`
- usage 全为 0

`compactruntime.LocalAdapter` 在 `extractCompactSummaryText` 得到空 summary 后返回 `compact summary response is empty`。所以系统确实尝试过恢复压缩，但 compact 请求没有生成摘要。

## 当前实现链路

### active-turn replay 压缩

代码位置：

- `backend/internal/historyguard/active_turn.go`
- `backend/internal/agent/message_builder.go`
- `backend/internal/chatcore/provider_loop.go`

当前策略是：

1. 找到最近的 user turn。
2. 如果 active turn 超过 byte/token 预算，则只压缩最新 replay block 之前的 assistant/tool 片段。
3. 最新 replay block 保留原样，因为 provider 协议要求 assistant tool call 和 tool result 能正确配对。
4. 如果没有更早 replay block 可压缩，则返回 `active_turn_not_compactable`。

这套策略对“多轮工具调用产生很多旧输出”的场景有效，但对“最新一次并行工具调用直接返回大量输出”的场景无效。本会话正是后者。

### ReActLoop preflight 恢复

代码位置：

- `backend/internal/agent/loop.go`

关键路径：

- `think()` 调用 `enforcePromptPreflight()`
- preflight 超预算时先尝试 active-turn replay 压缩
- 若压缩后仍超限，返回 `PromptPreflightError`
- `Run()` / `RunWithSession()` 捕获该错误后调用 `trySessionCompactionRecovery()`
- recovery 成功时替换 session history、重建 `MessageBuilder` 并 `continue` 当前 step

也就是说，agent loop 已经具备“preflight 失败后自动 whole-history compact 再继续”的框架。

### SessionActor 二次恢复

代码位置：

- `backend/internal/chat/actor.go`

关键路径：

- `maybeAutoCompactSession()` 在 turn start 时做 pre-turn compact，但只按 model capability 的 trigger limit 判断
- `runLoop()` 返回 `PromptPreflightError` 后，actor 还会调用 `runManualCompact(..., ModeLocal)`
- manual compact 成功时再次调用 `runLoop()`

因此本会话中不是没有恢复逻辑，而是恢复逻辑依赖 LLM 生成 compact summary；当 compact summary 请求返回空 choices 时，恢复链路就中断。

## 根因

### 根因 1：preflight 硬预算与 auto compact 触发阈值不一致

本会话的 auto compact 触发阈值来自模型能力，约为 `200000`；preflight 硬预算来自默认 context budget，为 `12000`。

结果是：

- pre-turn auto compact 认为 `14297 < 200000`，不需要提前压缩
- prompt preflight 认为 `14297 > 12000`，必须本地拦截

这导致同一个请求在“压缩触发层”和“发送前拦截层”得到相反结论。Codex 的参考实现以 model auto compact token limit 作为主触发线，不再叠加一个更小的默认 12k 硬门槛。

### 根因 2：默认预算被当成用户显式限制参与 min 选择

`resolvePromptPreflightBudget()` 当前总是先加入 `default_context_max_prompt_tokens=12000`，然后从所有候选里取最小值。这个行为适合未知模型的保守 fallback，但不适合已有明确 model capability 的 provider。

更合理的语义应是：

- 用户显式配置的 `context_max_prompt_tokens` 可以作为硬上限
- provider/model capability 的 `auto_compact_token_limit` 是已知模型的默认 preflight 上限
- `contextmgr.DefaultBudget().MaxPromptTokens` 只应在无法解析 provider/model capability 时作为 fallback

当前实现无法区分“默认 12k”和“用户显式要求 12k”，所以把默认值错误地升级成了全局硬限制。

### 根因 3：内部 compact 请求不应携带工具面

`084` 和 `085` compact 请求虽然是内部摘要请求，但实际 body 里带了：

- `tools=[list_mcp_resources]`
- `tool_choice=auto`

原因在 `backend/internal/llm/provider.go`：

- `ProviderWrapper.convertTools()` 调用 `BuildToolDefinitionsForRequest(..., includeMeta=true)`
- 即使 `req.Tools` 为空，也会注入 MCP meta tool

compact 请求的目标是生成纯文本 handoff summary，不应该允许模型调用 MCP meta tool。携带工具面会增加模型输出不确定性，也可能触发 provider 对工具请求的兼容性问题。

### 根因 4：compact 默认发送 `reasoning_effort=none`

代码位置：

- `backend/internal/llm/model_capability.go`
- `backend/internal/compactruntime/local.go`
- `backend/internal/llm/adapter/openai.go`

`CompactSummarySettings()` 在 capability 未声明 compact reasoning effort 时默认返回 `none`。OpenAI-compatible provider 不一定都接受 `reasoning_effort=none`，尤其是 ModelScope 这种 OpenAI 兼容层。当前请求虽然 HTTP 200，但响应是空 choices，说明 provider 侧没有按预期生成普通 assistant message。

该字段应改为“未显式配置则省略”，或者只在已知支持该值的协议/provider 上发送。

### 根因 5：空 choices 被过早视为成功响应

代码位置：

- `backend/internal/llm/provider.go`

`ProviderWrapper.Call()` 在 `p.Chat()` 无 error 且 `chatResp != nil` 时，会直接 `toLLMResponse()`。`toLLMResponse()` 对 `len(resp.Choices)==0` 返回一个空的 `LLMResponse`。

这使得 compact 层只能看到“摘要为空”，而不是 provider 响应异常。副作用：

- 没有按 retry policy 重试
- 没有把空 choices 作为 provider contract violation 记录
- actor 层只能得到 `summary_generation_failed`

应在 provider wrapper 层把 `choices` 为空识别为错误，至少对非流式普通 completion 和内部 compact 请求如此。

### 根因 6：最新 replay block 缺少内容级降载

active-turn replay compaction 为了协议正确性保留最新 assistant tool-call + tool-result block，这是合理的。但本会话最新 block 里 4 个 tool result 合计超过 30k 字符，其中至少一个 git diff 输出还带了 artifact path。

当前缺少“保留 tool result 消息和 tool_call_id，但压缩 tool result content”的降载策略。例如：

- tool result content 保留摘要、行数、字节数、artifact path 和关键片段
- 原始完整输出只通过 artifact 引用保存
- 对最新 replay block 也应用 per-tool token/byte cap

如果最新 block 自身超过预算，仅压缩更早 replay 永远不够。

## Codex 对照

参考目录：`E:\projects\ai\codex`

关键文件：

- `codex-rs/core/src/session/turn.rs`
- `codex-rs/core/src/compact.rs`
- `codex-rs/core/src/compact_remote.rs`
- `codex-rs/core/src/session/mod.rs`

Codex 的关键行为：

1. `run_pre_sampling_compact()` 在新 turn sampling 前检查 `model_info.auto_compact_token_limit()`，超过才运行 pre-turn compact。
2. sampling 后如果 `total_usage_tokens >= auto_compact_limit` 且模型需要 follow-up，会执行 mid-turn compact，然后 `continue` 当前 loop。
3. compact 是独立任务，生成 summary 后调用 `replace_compacted_history()` 原子替换会话 history，并持久化 `CompactedItem`。
4. local compact 失败于 context window 时，会移除最旧 history item 后重试，而不是一次失败即放弃。
5. replacement history 不是保留所有工具输出，而是保留用户消息窗口加 compact summary；用户消息窗口有 `COMPACT_USER_MESSAGE_MAX_TOKENS=20000` 限制。
6. pre-turn/manual compact 使用 `DoNotInject`，让下一次普通 turn 重新注入初始上下文；mid-turn compact 使用 `BeforeLastUserMessage`，保证同一轮继续时 context 不丢。
7. remote compact provider 支持时走 `/responses/compact`，避免把 compact 当成普通带工具的 chat completion。

与本仓库差异最大的是：Codex 没有用一个固定 12k default prompt budget 去压过模型能力阈值，也没有让 compact summary 请求携带普通工具面。

## 修复目标

修复后应满足：

1. 已知 provider/model capability 时，preflight budget、pre-turn compact trigger 和 mid-turn compact trigger 使用同一套阈值来源。
2. 默认 12k 只作为未知模型 fallback，不再压过 `model_capability_auto_compact_token_limit`。
3. 内部 compact 请求不暴露普通工具和 MCP meta tool。
4. compact provider 返回空 choices 时视为错误并进入 retry/降级路径。
5. latest replay block 即使需要保留协议结构，也能压缩 tool result content。
6. preflight 失败后系统能自动恢复：先压缩并重试当前 loop；若 provider compact 不可用，则使用本地确定性 fallback summary，避免直接把用户卡死。
7. 普通 `contextmgr.Manager.Build()` 默认保留完整原始 history；`MaxMessages`、`KeepRecentMessages`、`MaxPromptTokens` 和 recent-window / ledger / summary 重组只在显式 `BuildInput.EnablePromptCompaction=true` 时参与 prompt-view compaction。
8. prompt-only preflight compaction 只修改当次发送给 LLM API 的 prompt view，不默认回写或持久化 replacement history；只有 session-level compact recovery、显式 `/compact`、`/new`、`/clear` 这类边界允许替换历史并让 `ctx used` 降低。

## 修复方案

### P0：修正预算选择语义

建议新增一个统一预算解析模块，例如 `backend/internal/promptbudget`，供 agent loop、shared chatcore、skills API 复用。

规则：

1. 如果用户显式设置 `context_max_prompt_tokens`，它是硬限制候选。
2. 如果解析到 `model_capability.auto_compact_token_limit`，使用它作为主要 prompt preflight budget。
3. 如果没有显式 token limit，但有 `model_capability.max_context_tokens`，用 `max_context_tokens * auto_compact_ratio`。
4. 如果只有 provider `MaxContextTokens`，用 provider context limit 的 ratio。
5. 只有无法解析 provider/model capability 时，才 fallback 到 `context.fallbackMaxPromptTokens`；未配置时使用内置默认 `32000`。
6. `remainingBudget` 如果存在，仍可作为更小的运行期上限。

需要改动：

- `backend/internal/agent/loop.go`: `resolvePromptPreflightBudget`
- `backend/cmd/aicli/commands/chat_core.go`: `resolveSharedChatPromptBudget`
- `backend/internal/api/skills/*`: 如有重复 budget 推导，统一复用
- `backend/internal/contextmgr`: 增加“预算是否显式配置”的信息，或在 agent config options 中保留 explicit 标记

核心测试：

- capability 为 `auto_compact_token_limit=200000` 且用户未显式设置 context max 时，preflight budget 应为 200000，不是 12000。
- 用户显式设置 `context_max_prompt_tokens=12000` 时，仍应以 12000 拦截。
- capability 缺失时，fallback 到可配置的 `context.fallbackMaxPromptTokens`，默认 `32000`。

### P0：隔离内部 compact 请求的工具面

建议在 `LLMRequest` 或 metadata 中加入显式语义：

- `DisableTools bool`
- 或 metadata: `suppress_tools=true`
- 或根据 `internal_operation=compact` 自动禁用工具

实现点：

- `backend/internal/compactruntime/local.go` 发 compact 请求时设置禁用工具标记。
- `backend/internal/llm/provider.go` 的 `convertTools()` 不应在 compact 请求中 `includeMeta=true`。
- OpenAI-compatible 请求中 compact 应设置 `tool_choice=none` 或直接不发送 `tools/tool_choice`。

核心测试：

- compact request 的 HTTP body 不包含 `tools`。
- compact request 的 HTTP body 不包含 `tool_choice=auto`。
- 普通 chat request 仍保留 MCP meta tool。

### P0：不要默认发送 `reasoning_effort=none`

调整 `CompactSummarySettings()`：

- 默认 compact reasoning effort 返回空字符串，表示不发送该字段。
- 只有 capability 显式配置 `compact_reasoning_effort` 时才发送。
- 对 Codex/OpenAI 原生 provider 如确需 `none`，在 capability 中配置，而不是全局默认。

核心测试：

- ModelScope/OpenAI-compatible compact 请求未配置时不包含 `reasoning_effort`。
- 已配置 `compact_reasoning_effort=max` 时请求包含该字段。

### P0：空 choices 视为 provider error

调整 `ProviderWrapper.Call()`：

- 非流式 `ChatResponse` 如果 `len(Choices)==0`，返回错误，例如 `empty provider choices`。
- 对 internal compact 请求附加上下文：`compact summary provider returned empty choices`。
- 让 retry policy 有机会处理该错误。

核心测试：

- 非流式空 choices 不再生成空 `LLMResponse`。
- compact layer 能收到明确错误并发出 `session_compact_failed`。

### P1：为 latest replay block 做内容级降载

在保留工具协议结构的前提下压缩 tool result content：

- 新增 `historyguard.CompactLatestReplayBlockContent(...)` 或扩展现有 `CompactActiveTurnReplayWithCounter`
- 对 latest replay block 中的 tool result content 做 per-message byte/token cap
- 优先保留：
  - tool name / tool_call_id
  - success/error
  - output kind
  - total lines / bytes
  - artifact path
  - head/tail 或 reducer summary
- 不删除 assistant tool-call 消息和 tool result 消息，避免破坏 replay

核心测试：

- 最新 replay block 超预算但 tool result 有 artifact refs 时，content 被替换为摘要，tool_call_id 保持不变。
- provider 请求仍通过 OpenAI/Codex tool replay 校验。
- 对没有 artifact 的小输出不做降载。

### P1：增加确定性 compact fallback

LLM compact 失败时，不应直接让 preflight 卡死。建议增加 deterministic fallback：

1. 从现有 history 中抽取用户消息、assistant 最终文本、工具调用摘要和 artifact refs。
2. 不调用模型，生成结构化 summary message。
3. replacement history 保留最近用户消息和 fallback summary。
4. metadata 标记：
   - `context_stage=compaction`
   - `compact_mode=local_fallback`
   - `compact_fallback_reason=<provider error>`
5. fallback 只在自动恢复路径启用，手动 `/compact` 可以继续报告 provider compact 失败。

这样即使 provider quota、空 choices 或工具兼容性导致 LLM summary 失败，系统仍可自动收缩上下文并重试一次。

### P1：实现真正的 mid-turn compact continuation

参考 Codex：

- 当 provider 返回 tool calls 后，如果 tool output 推高 token 并且还需要继续请求模型，应在下一次 provider request 前运行 mid-turn compact。
- mid-turn compact 的 replacement history 应把 summary 放在模型可继续的位置，并保留当前用户意图。
- compact 成功后重置 provider client/session cache，再继续当前 tool loop。

当前 ReActLoop 已有 `trySessionCompactionRecovery()`，shared chatcore 也有 `HistoryCompactor`，但需要补强：

- active-turn 不可压缩时，优先尝试 latest block content 降载。
- 降载后仍超限，运行 whole-history compact。
- whole-history compact 后仍超限，启用 deterministic fallback。
- 每条路径最多重试一次，避免无限循环。

### P2：补齐观测和用户提示

需要把“已尝试 compact 但 provider 返回空 choices”清楚暴露出来：

- `session_compact_failed` payload 添加 `provider_response_empty=true`
- 添加 `compact_request_had_tools`、`compact_reasoning_effort`、`compact_request_message_count`
- preflight final error 中显示 recovery attempts：
  - active-turn compact attempted
  - latest-block reduction attempted
  - whole-history compact attempted
  - fallback compact applied or failed

如果最终仍失败，提示应是“已自动尝试恢复但失败”，而不是只说“请开启新轮次”。

## 推荐实施顺序

1. 先改预算语义，避免已知大上下文模型被默认 12k 误拦截。
2. 隔离 compact 请求工具面，并省略默认 `reasoning_effort=none`。
3. 把空 choices 作为 provider error，补测试。
4. 加 latest replay block content reducer，解决最新并行工具输出过大的核心场景。
5. 加 deterministic fallback compact，保证自动恢复链路不完全依赖 provider。
6. 最后统一 agent/shared/skills 的 budget 和 compact event 元数据。

## 验收用例

### 复现本会话类问题

构造一个 session：

- 用户发起一次任务。
- 模型返回 4 个并行 shell/git diff 工具调用。
- 每个 tool result 含 8k 到 12k 字符，且带 artifact refs。
- prompt token 估算略高于 12k，但远低于模型 capability `auto_compact_token_limit`。

预期：

- 未显式设置 12k context max 时，不应被默认 12k preflight 拦截。
- 如果设置显式 12k，则 latest replay block reducer 应先压缩 tool result content。
- reducer 后仍超限时，whole-history compact 或 fallback compact 应接管。
- 成功后继续当前任务，而不是返回 `active_turn_not_compactable`。

### compact 请求形态

对 `internal_operation=compact` 的 HTTP artifact 断言：

- 不含 `tools`
- 不含 `tool_choice=auto`
- 未显式配置时不含 `reasoning_effort`
- response 空 choices 时触发 retry 或结构化错误

### Codex 行为对齐

对照 Codex 的语义验收：

- pre-turn compact 以 model auto compact limit 为触发线。
- mid-turn compact 发生后能继续当前 tool loop。
- session-level compact 的 replacement history 原子替换并持久化；prompt-only preflight compaction 不应把 replacement history 默认回写到 session store。
- compact 失败时有明确错误事件，不静默退化为空 summary。

## 风险与取舍

- 放宽默认 12k 后，请求可能更大；但对已有 `max_context_tokens/auto_compact_token_limit` 的 provider 是合理的。未知 provider 使用 `context.fallbackMaxPromptTokens`，当前默认 `32000`。
- latest block content reducer 会牺牲模型对完整工具输出的直接可见性；通过 artifact path 和关键片段可以补偿。
- deterministic fallback summary 质量不如 LLM summary，但比直接中断更可恢复，且只用于 provider compact 失败场景。
- 内部 compact 禁用工具后，summary 不能主动读取 MCP 资源；这是正确的，因为 compact 应只总结已在 history 中出现的信息。

## 最终判断

这次失败的直接原因是：latest replay block 已经大到超过 12k preflight budget，active-turn replay 没有更早块可压缩；随后系统尝试 local compact，但 compact 请求携带 MCP meta tool、`tool_choice=auto` 和默认 `reasoning_effort=none`，ModelScope 返回 HTTP 200 空 choices，导致 compact summary 为空，恢复链路失败。

更深层的设计问题是：默认 12k preflight budget、model capability auto compact limit、context manager budget 和 active-turn replay reducer 没有统一成同一套恢复策略。修复应优先统一预算语义，并让 compact 请求成为无工具、可重试、可降级的内部摘要操作。

## 修复执行记录

本次已完成 P0/P1 核心修复：

- `backend/internal/agent/loop.go`：调整 `resolvePromptPreflightBudget()` 语义。已知 model capability 或 provider context limit 时，不再让 `contextmgr.DefaultBudget().MaxPromptTokens=12000` 参与最小值竞争；显式 `context_max_prompt_tokens` 仍作为硬限制；`context_profile` 只控制上下文组织策略。未知模型/provider 使用可配置的 `context.fallbackMaxPromptTokens`，未配置时使用内置默认 `32000`。
- `backend/internal/compactruntime/local.go`：内部 compact 请求增加 `internal_operation=compact`、`disable_tools=true`、`disable_meta_tools=true`；未显式配置时不再发送 `reasoning_effort=none`；compact provider 报错或返回空 summary 时生成 deterministic fallback summary。
- `backend/internal/llm/provider.go`、`backend/internal/llm/request_tools.go`、`backend/internal/llm/gateway_client.go`：`internal_operation=compact` 自动禁用普通工具和 MCP meta tools；OpenAI-compatible 非流式响应的 `choices=null` 或空数组会作为 `empty_provider_choices` 错误进入 retry/error 链路。
- `backend/internal/llm/model_capability.go`：`CompactSummarySettings()` 默认只保留 `max_tokens=2048`，不再默认返回 `reasoning_effort=none`；仅在 capability 显式配置 `compact_reasoning_effort` 时发送。
- `backend/internal/historyguard/active_turn.go`：新增 latest replay block tool result 内容降载。超大 tool result 会保留消息结构、`tool_call_id` 和 head/tail/关键 artifact 引用，避免破坏 provider 的 tool-call/tool-result 邻接约束。
- `backend/internal/contextmgr/manager.go`、`backend/internal/agent/loop.go`：ReAct 在 context manager Build 前解析有效 prompt 预算，并传入 `BuildInput`；Build 阶段记录 `budget_max_prompt_tokens_source`。普通 Build 默认保留完整原始 history，不再按 balanced profile 的 12k、`MaxMessages` 或 recent-window 裁剪；只有显式 `EnablePromptCompaction=true` 时才启用 recent-window / ledger / summary prompt-view compaction。
- `backend/internal/config/manager.go`、`backend/configs/runtime.yaml`：新增 `context.fallbackMaxPromptTokens`，默认 `32000`，用于无法解析 model capability/provider context limit 时的兜底 prompt 预算；该值不是已知模型的硬上限。

已补充覆盖测试：

- budget 测试覆盖 ModelScope/DeepSeek 类 `auto_compact_token_limit=200000` 不再被默认 12k 截断、普通 `contextmgr.Manager.Build()` 默认保留完整 history 且不被 balanced profile 默认 12k / `MaxMessages` / recent-window 提前裁剪、`context.fallbackMaxPromptTokens` 可配置未知模型兜底预算，以及显式 `context_max_prompt_tokens=12000` 仍可约束。
- context manager 测试覆盖显式 `EnablePromptCompaction=true` 时仍可触发 recent-window / ledger / summary prompt-view compaction，默认关闭时不发出 `context.compact.*` 事件。
- compact 请求形态测试覆盖禁用 tools/meta tools 和默认省略 reasoning effort。
- provider wrapper 测试覆盖 compact 请求不再发送 `tools`/`tool_choice`，以及空 choices 被识别为错误。
- active-turn 测试覆盖 latest replay tool result 降载且不破坏 tool call 配对。
- compactruntime 测试覆盖 provider 空 summary/错误时 deterministic fallback summary 生效。
- aicli 状态栏测试覆盖 request-start 本地估算不提前显示为 `ctx used`、普通 provider usage 不降低已显示快照、provider `total_tokens` 作为 active context 快照、Anthropic/Mimo cache-read/cache-creation input 在 total 缺失或偏低时补入 active context、compact/reset 路径允许降低为 compact 后的 `token_after`。

验证结果：

- `go test ./internal/historyguard ./internal/llm ./internal/compactruntime ./internal/agent ./internal/chatcore ./cmd/aicli/commands`：通过。
- `go test ./...`：首次运行时 `internal/background` 的 `TestManagerRecoversPendingAndFailsInterruptedRunningJobs` 40 秒等待超时失败；该包未被本次修改触碰，随后单独执行 `go test ./internal/background` 通过，判断为偶发超时/时序型失败。
