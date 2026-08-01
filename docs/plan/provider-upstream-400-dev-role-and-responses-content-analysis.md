# aicli 上游 400 错误根因分析与协议兼容修复方案

- 日期：2026-08-01
- 范围：backend（llm 协议适配层 + contextmgr 消息注入 + 会话历史持久化）
- 关联协议：openai（chat/completions）、codex（responses）、anthropic、gemini、openai-compatible

## 1. 背景

opencode.ai 上游网关（Console Go，`https://opencode.ai/zen/go/v1/...`）在以下两类请求上返回确定性 400：

```
{"error":{"message":"Error from provider (Console Go): Upstream request failed",
          "type":"invalid_request_error","param":null,"code":"invalid_request_error"}}
```

两个真实会话各命中一类：

| 会话 | 协议/端点 | 请求体特征 | 触发点 |
|---|---|---|---|
| session_..._Z53eBXi1（openai） | `POST /v1/chat/completions` | 10 条消息，含 fact ledger 注入的 `role:developer` | developer 角色消息 |
| session_..._muXCLB6q（codex） | `POST /v1/responses` | 3 条消息（user/assistant/user） | assistant 消息 content 数组 |

本地 debug.log 均只记录 `streaming aggregate call failed after retries: HTTP 400`，无法定位根因，故用真实 API key 做 A/B 复现与变量隔离测试定位。

## 2. 现状分析

### 2.1 问题一：openai chat/completions 拒绝 developer 角色

**注入点**：`backend/internal/contextmgr/facts.go:158-160` fact ledger 生成 `types.NewDeveloperMessage(...)`，标记 `context_stage=fact_ledger`；`manager.go:858` goal 消息同为 `NewDeveloperMessage`。

**序列化点**：`buildOpenAIProtocolMessage`（`backend/internal/llm/reasoning_helpers.go:935-985`）对 role 原样透传（`message["role"] = role`），developer 直接进入请求体 messages。

**A/B 证据**（真实 API，同请求体逐项变更）：

| 变体 | 变更 | 结果 |
|---|---|---|
| A 原始（developer 在对话中段） | 无 | 400 稳定 |
| B developer 移到 system 之后 | 仅移位置 | 400 稳定 |
| 删除 developer 消息 | 移除该消息 | 200 稳定 |
| developer → system（原位） | 改角色 | 200 |
| 递增前缀 1-8（无 developer） | 逐条加消息 | 全部 200 |
| 递增前缀 9-10（含 developer） | 加入该消息 | 400 稳定 |

结论：**上游对 chat/completions 的 `role:developer` 无条件拒绝，与位置无关**；删除或改为 `system` 均可通过。注意：真实 OpenAI 的 chat/completions 支持 developer 角色且要求位于 user 之前，但 opencode.ai Console Go 上游（deepseek 系后端）不支持。

### 2.2 问题二：codex/responses 拒绝 assistant 消息 content 数组

**序列化点**：`buildCodexAssistantMessageItem`（`backend/internal/llm/adapter/codex.go:3419-3430`）生成 `content: [{type:"output_text", text}]` 数组；`buildCodexProtocolMessage` → `codexProtocolOutputItems`（`reasoning_helpers.go:1044-1084`）同样生成 output_text 数组。

**A/B 证据**（真实 API `/v1/responses`，同一请求体逐项变更）：

| 变体 | assistant content 形式 | 结果 |
|---|---|---|
| 原样重放 002 | `[{type:"output_text",text:"..."}]` | 400 稳定 |
| input 只留 user | —（无 assistant） | 200 |
| 含 assistant 消息任意组合（A / U,A / A,U） | 数组 | 400 稳定 |
| assistant content → 字符串 | `"Hello!..."` | 200 |
| 完整 3 条消息 + assistant 字符串 | 字符串 | 200 |
| 去掉 tools / reasoning / include / store / instructions | 数组 | 仍 400 |

**官方 SDK 交叉验证**（ai v7.0.44 + @ai-sdk/openai v4.0.25）：

| 用例 | 发送内容 | 结果 |
|---|---|---|
| SDK 原样多轮（`convert-to-openai-responses-input.ts:341` 生成数组） | 数组 | 400 |
| SDK + fetch 拦截把 content 改字符串 | 字符串 | 200 |
| 单轮（无 assistant 历史） | — | 200 |
| @ai-sdk/openai-compatible 多轮（chat/completions，字符串 content） | 字符串 | 全部 200 |

结论：**opencode.ai Console Go 上游对 responses 协议的 assistant 消息只接受字符串 content，不接受 output_text 数组**（连官方 SDK 原样发送也失败）。`@ai-sdk/openai-compatible` 恰好使用 chat/completions + 字符串 content，故全通过——这也印证修复方向。

### 2.3 历史固化（持久化）现状

`session_history.sqlite` 的 `session_prompt_messages` 与消息表以 `payload_json` 原样保存 `types.Message`（`sqlite_storage.go` `insertCanonicalMessageTx`），**role 按注入时的值固化存储**：

- fact ledger / goal 等上下文消息若以 `developer` 注入，则**固化到历史库时就是 `developer`**。
- 恢复会话 / compact 后重放时，`RuntimeMessagesToProtocolMessages`（`reasoning_helpers.go:467-490`）会原样重建这些消息再走序列化层。

因此：

1. **只改注入点（facts.go / manager.go 改为 system）**：只影响新产生的消息；已固化的旧历史中 `developer` 消息在恢复重放时依旧 400。
2. **只改序列化层（openai 协议 developer→system）**：新旧历史统一被归一化，但需要处理"哪些协议需要归一化、哪些不需要"的协议差异（见 3.3）。

两者**缺一不可**：注入层保证新消息固化时就是合规角色；序列化层保证存量历史重放安全。且需注意 `promptContextMessageKey`（`loop.go:3660-3662`）用 `Role+stage+ToolCallID+Content` 做上下文快照去重，角色变更会影响 key 匹配（存量 `context_snapshot` 消息以旧 role 记录，重放时以新 role 构建会重复注入——见 3.4）。

### 2.4 协议兼容性矩阵（现状）

| 协议 | developer 角色 | assistant content 数组 | 现状 |
|---|---|---|---|
| openai chat/completions（本地上游） | 上游拒绝 | content 为字符串，无数组 | **400** |
| codex responses（本地上游） | 上游接受（SDK 单轮 developer 200） | 上游拒绝数组，只收字符串 | **400**（数组） |
| anthropic | leading → system 文本；residual → user（`anthropic.go:119-155`） | 内部块转字符串（`mergeAnthropicSameRoleMessage`） | 已兼容 |
| gemini | `convertRole` 落 `model`/`user`（`gemini.go:655-666`） | 字符串 | 已兼容 |
| 真实 OpenAI responses | 接受（`systemMessageMode:'developer'` 可用） | 接受数组 | 兼容（上游差异即在此） |

关键结论：**本地代码的"标准 OpenAI 序列化"在真实 OpenAI 上合法，但 opencode.ai Console Go 上游不是完整 OpenAI 实现**——chat 不认 developer、responses 不认数组。需要的是**协议内 provider 级兼容层**（现有 `providercompat` 机制正是为此设计），而不是全局改动标准序列化。

## 3. 建议方案

### 3.1 修复一：openai chat/completions developer → system

**注入层**（保证新消息固化合规）：

- `contextmgr/facts.go:160`：`NewDeveloperMessage` → `NewSystemMessage`。
- `contextmgr/manager.go:858`（goal 消息）同改。
- 其他 `NewDeveloperMessage` 注入点（`compact.go`、`contextreconcile/reconcile.go` 等，见 2.3 相关清单）逐一评估：凡是以"指令上下文"语义注入、最终走 openai chat 协议的，统一改 `system`。

**序列化层**（保证存量历史重放安全，核心）：

在 `providercompat` 新增/复用归一化钩子，对 openai chat/completions 协议做 `developer → system`：

- 推荐位置：`providercompat.openai_default.go` 的 `NormalizeOpenAICompatibleMessages`（或 `PrepareRequestBody`），逐消息把 `role == "developer"` 改写为 `"system"`。
- `provider_adapter_request.go:73` 已调用 `NormalizeOpenAICompatibleMessages`，链路现成。
- 注意顺序：先归一化再进 adapter，保证 `MaxTokens`/token 预估等下游看到统一角色。

**验证**：`body_A_original.json`（固化 400 复现体）重放 → 400；同体 developer→system → 200；存量 sqlite 会话恢复后重放 → 200。

### 3.2 修复二：codex/responses assistant content 数组 → 字符串

**序列化点**：

- `adapter/codex.go:3419` `buildCodexAssistantMessageItem`：content 数组改为 `content: text` 字符串。
- `reasoning_helpers.go:1063-1073` `codexProtocolOutputItems` 中的 message item 同改（`output_text` 数组 → 字符串）。
- 检查 `canonicalizeCodexMessageContentParts`（`reasoning_helpers.go:374-397`）等解析侧：接收上游响应时仍按数组解析、内部继续用数组表达，**只在出站序列化时压平**，避免内部状态漂移。

**归属**：此修复是"本地 codex 适配器对 opencode.ai 上游的兼容"，放 `providercompat`（如 `codex_default.go` / 新增 `opencode` provider 适配器）更稳妥——避免影响其他 codex 兼容上游（如 ChatGPT Codex 后端，其 responses 支持数组）。

**验证**：`codex_002_original.json` 重放 → 400；同体 assistant content 改字符串 → 200（已实测）；SDK 交叉验证已通过。

### 3.3 通用性设计（协议差异放 providercompat，不放全局序列化）

原则：**标准序列化保持"标准 OpenAI 语义"（developer 角色、output_text 数组），把上游差异收敛到 providercompat 适配器**。

| 适配器 | 归一化动作 |
|---|---|
| openai-default（新增 developer→system） | chat/completions 消息 `developer→system` |
| opencode.ai codex 适配器（新增 content 压平） | responses input 的 assistant content 数组→字符串 |
| anthropic（已有） | leading system/developer→system 文本；residual→user |
| gemini（已有） | role 映射 user/model |

`providercompat.registry.go` 现有机制可直接扩展：新增 `opencodeAIAdapter`（匹配 `IsDeepSeek`/providerName=opencode.ai）承载上述两条规则，避免污染默认适配器。

### 3.4 历史固化与快照 key 的处理

- **存量历史**：sqlite 中已固化的 `developer` 消息依赖 3.1 序列化归一化兜底；不需要数据迁移。
- **快照去重 key**（`loop.go:3660` `Role+stage+...`）：注入层改 system 后，新快照 key 的 Role 段变化。`reusablePromptHistory`/`appendMissingContextSnapshots` 以 content+stage 匹配为主（`manager.go:476-489` 冻结规则），Role 变化只影响同 content 下的新旧 key 不重叠——需在改注入层后跑 `chat_actor_host_test.go` 与 `contextmgr` 测试确认无重复注入；如受影响，将 key 改为 `stage+ToolCallID+Content`（去 Role）以解耦。
- **goal / todo_state / team / warm_memory / project_memory 等注入点**：统一评审注入角色，尽量全部收敛为 `system`（chat 协议下）或保持 `developer`（responses 协议下由适配器归一化），避免每协议一套语义。

### 3.5 回归与测试

1. **A/B 复现脚本**（已建，可入 `scripts/` 或文档附录）：
   - `ab_isolate.ps1` / `ab_incremental.ps1` / `ab_repeat.ps1`：chat 协议变量隔离。
   - `ab_codex_isolate.ps1` / `ab_codex_bisect.ps1` / `ab_codex_asst.ps1`：responses 协议变量隔离。
   - `aisdk-test/`（Node）：官方 SDK 交叉验证（ai + @ai-sdk/openai + @ai-sdk/openai-compatible）。
2. **单元测试**：
   - providercompat：`NormalizeOpenAICompatibleMessages` developer→system 单测。
   - codex 适配器：`BuildRequest` 输出 assistant content 为字符串断言。
   - 既有 `codex_request_test.go` / `anthropic_request_test.go` 的 developer 用例同步调整断言。
3. **端到端**：真实 key 重放 `body_A_original.json` 与 `codex_002_original.json` 修复体，均 200。

## 4. 待确认问题

- [ ] opencode.ai Console Go 是否后续支持 developer 角色 / output_text 数组（决定适配器是否需要 provider 白名单开关）。
- [ ] `codexProtocolOutputItems` 压平后，多模态/图片 responses 消息是否受影响（需图片生成回归）。
- [ ] 快照 key 去 Role 是否会与其他上下文重放逻辑冲突（`loop.go` 三处调用点）。

## 5. 参考资料

- 失败请求/响应 artifact：`~/.aicli/chat-logs/2026/07/31/20260731_200419.995_8d847ddc/runtime-http/004_*`
- codex 会话：`~/.aicli/chat-logs/2026/07/31/20260731_195554.631_df9cbeec/runtime-http/002_*`
- A/B 脚本与请求体：`C:\Users\vince\AppData\Local\Temp\opencode\ab_*.ps1`、`ab_out/`
- SDK 源码分析：`@ai-sdk/openai` `convert-to-openai-responses-input.ts:339-346`、`convert-to-openai-chat-messages.ts:291-299`；`@ai-sdk/openai-compatible` `convert-to-openai-compatible-chat-messages.ts:224-230`
