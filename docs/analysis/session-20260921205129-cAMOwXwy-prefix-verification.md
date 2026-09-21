# 场景验证报告：手动切换 provider/model 时请求前缀的稳定性

**会话**：`session_20260921205129_cAMOwXwy`
**证据源**：`C:\Users\vince\.aicli\chat-logs\2026\09\21\session_20260921205129_cAMOwXwy\http\`
**样本**：26 个出站请求（`0NN_request_provider_wrapper.json`，共 61 个 artifact 文件）
**日期**：2026-09-21
**方法**：对每个请求的 `body_json` 与 `request_metadata._request_debug` 做逐字段提取与跨请求比对（非推断，全部来自落盘原文）

---

## 0. 结论摘要

| 维度 | 是否中途变化 | 结论 |
|---|---|---|
| **请求 tools** | ❌ **完全不变** | 46 个工具，字节级一致，26/26 请求相同 |
| **system prompt** | ⚠️ **变化（2 个变体）** | `9749` / `12292` 字符，差异是**纯插入** |
| **messages** | ⚠️ **每次都变** | 每次跨 provider 切换，**消息前缀必然断裂** |
| **前缀缓存** | ❌ **跨 provider 无法复用** | 每个切换点都必然 miss |

**一句话结论**：本地结构确实是 provider 中立的，**但"本地结构"不等于"线上字节"**。
`prompt / tools / messages` 在发往不同 provider 时会被**重新序列化**，序列化规则是 **provider 相关**的。
因此前缀缓存的复用边界不是"本地会话"，而是"同一 provider 的连续请求段"。

---

## 1. 验证方法

每个请求 artifact 同时包含：

- `body_json`：实际发往 provider 的请求体（`messages` / `tools` / `system` / `tool_choice`）
- `request_metadata`：运行时自算指纹
  - `prompt_fingerprint` / `tool_surface_fingerprint` / `messages_sha256` / `prompt_cache_epoch` / `route`
  - `_request_debug`：`prompt_layout_sha256`、`prompt_layout_length`、`tools_sha256`、`cache_surface_sha256`、`request_sha256`、`tool_count`、`message_count`、`prompt_cache_key`、`protocol`

所有比对均基于 `body_json` 原文，而非运行时指纹 —— 这一点很重要，因为**运行时指纹本身对某些变化是盲的**（见 §6）。

---

## 2. provider / model 切换时间线（实测）

| 请求序号 | provider | model | 消息数 | rc 模式 |
|---|---|---|---|---|
| 1 – 7 | `hanhe` | `deepseek-v4.1-flash` | 2 | 无 assistant 消息 |
| 9 – 12 | `commandgo` | `poolside/laguna-s-2.1-free` | 2 → 4 | FULL（退化） |
| 13 – 14 | `opencode.ai` | `deepseek-v4.1-flash` | 7 → 9 | **FULL** |
| 15 – 22 | `commandgo` | `poolside/laguna-s-2.1-free` | 12 → 17 | **SPARSE** |
| 23 – 27 | `opencode.ai` | `deepseek-v4.1-flash` | 19 → 29 | **FULL** |
| 28 – 34 | `commandgo` | `poolside/laguna-s-2.1-free` | 31 → 35 | **SPARSE** |
| 35 | `opencode.ai` | `deepseek-v4.1-flash` | 37 | **FULL** |

- `protocol` 字段在 **26/26 个请求中恒为 `openai`** —— 协议层没有切换，切换的只有 provider/model。
- 会话内共 **6 次 provider 切换**，全部为手动切换。

**rc 模式定义**：
- `FULL` = **每一条** assistant 消息都带 `reasoning_content` 键（无推理时以 `""` 占位）
- `SPARSE` = 只有**部分** assistant 消息带 `reasoning_content` 键

---

## 3. 请求 tools —— 完全不变 ✅

| 指标 | 值 |
|---|---|
| `tool_count` | **46**（26/26 请求） |
| 工具目录条目 | 46 |
| `tool_choice` | `auto`（26/26） |
| `tools_sha256` | `c28a3f4b4f54a2037a9610a12cc27a56d36166389f09c8cb42f3ed831197f14b`（26/26 相同） |
| 原始 tools JSON sha256 | `1e9dca54670774167d87a18f77a455ca03afdfad8371209ed3530b891493958a`（**唯一 1 个**） |
| `tool_surface_fingerprint` | `53d270eeb2a22c2f03511a69330a164155d17347b2705ccd7324bbe14042e731`（**唯一 1 个**） |

**工具名列表在 seq 1 与 seq 35 之间逐项一致**。工具集合不随 provider/model 变化，也不随会话推进变化。

> **这是本次验证中唯一完全稳定的一维**，也是前缀缓存能够部分命中的前提。

---

## 4. system prompt —— 变化，2 个变体 ⚠️

| 变体 | sha256 前缀 | 长度 | 出现请求 | 对应 provider |
|---|---|---|---|---|
| **SHORT** | `23304dbdacf0…` | **9749** | 1,3,5,7,13,14,15,16,17,19,20,22,23,24,25,26,27,28,29,30,32,34,35 | commandgo / hanhe / opencode.ai |
| **LONG** | `8fb337e9690e…` | **12292** | **9, 10, 12** | **仅 commandgo** |

**差异是纯插入**（diff opcodes 只有 `equal` + `insert`，无 `delete`/`replace`）：

```
short[84:84] → long[84:108]     插入 24 行
```

LONG 是 SHORT 的**严格超集** —— 从缓存角度看这是"安全方向"（前缀仍可命中），但**它本身仍是一个不同的字节序列**，对以完整 payload 为 key 的缓存而言就是另一个条目。

**根因（已定位到源码）**：`backend\cmd\aicli\commands\chat_actor_host.go:2101-2130`，函数 `composeLocalChatSystemPrompt`：

```go
if workspaceRoot != "" {
    // 插入 "Current workspace root: %s" 及后续说明块（24 行）
}
```

即：**当 `workspaceRoot` 非空时，system prompt 会多出一段工作区根目录说明**。seq 9/10/12 恰好是该条件成立的三次请求。

> 值得注意的是：仓库中**已存在**针对这一行为的回归测试。它被触发说明切换路径上仍有一条分支会让 `workspaceRoot` 从空变为非空，再变回空 —— **同一会话内该值不稳定**。

---

## 5. messages —— 每次都变 ⚠️

messages 的变化有**三个独立成因**，需要分开归因。

### 5.1 `reasoning_content` 键的存在性翻转（**主因**）

这是最主要的、也是**唯一由 provider 切换直接驱动**的不稳定源。

**实测谓词**（26 个请求全部符合，无例外）：

> 一条 assistant 消息在出站请求中带 `reasoning_content` 键，**当且仅当**满足以下任一条：
>
> - **路径 A**：该消息持久化的 `Metadata["reasoning_content"]` 键存在（**允许值为空串**）
> - **路径 B**：**当前** provider 命中 `IsDeepSeek(provider, baseURL, model)` → 返回 `("", true)`，即**给所有 assistant 消息补 `""` 占位**

**代码位置**：`backend\internal\llm\reasoning_helpers.go:982-991`（`buildOpenAIProtocolMessage`）

```go
if strings.EqualFold(strings.TrimSpace(role), "assistant") {
    if reasoningContent, ok := stringMetadataValueAllowEmpty(messageMetadata, "reasoning_content"); ok {
        message["reasoning_content"] = reasoningContent          // 路径 A
    } else if reasoningContent, ok := replayableOpenAIReasoningContent(toolCalls, reasoning, providerHint, modelHint); ok {
        message["reasoning_content"] = reasoningContent          // 路径 B
    }
}
```

路径 B 的实现只在 `deepSeekOpenAIAdapter` 中非平凡（`providercompat\openai_deepseek.go:47-69`）；`BaseAdapter` 恒返回 `("", false)`。

**为什么 `opencode.ai` 会命中 DeepSeek 规则？**

`providercompat\providercompat.go:463-477`：

```go
func IsDeepSeek(providerName, baseURL, modelID string) bool {
    name := strings.ToLower(strings.TrimSpace(providerName))
    normalizedBaseURL := strings.ToLower(strings.TrimSpace(baseURL))
    if strings.Contains(name, "deepseek") || strings.Contains(normalizedBaseURL, "deepseek") {
        return true
    }
    return IsDeepSeekModel(modelID)      // strings.Contains(modelID, "deepseek")
}
```

会话中 `opencode.ai` 使用的模型是 **`deepseek-v4.1-flash`** —— **模型名里含 `deepseek`**，于是 `IsDeepSeekModel` 返回 `true`，**DeepSeek 专属的 reasoning 回放策略被套用到了一个非 DeepSeek 厂商上**。

**观测到的后果**（seq 34 `commandgo` vs seq 35 `opencode.ai`，同一份会话历史）：

| 消息 idx | tool_calls | seq34 (`commandgo`) | seq35 (`opencode.ai`) |
|---|---|---|---|
| 2 | YES | `RC=54` | `RC=54` |
| 4 | no | **ABSENT** | `RC=""` |
| 7 | YES | **ABSENT** | `RC=""` |
| 9 | no | **ABSENT** | `RC=160` |
| 12 | no | **ABSENT** | `RC=""` |
| 14 | YES | **ABSENT** | `RC=""` |
| 16 | no | **ABSENT** | `RC=264` |
| 19 | YES | `RC=929` | `RC=929` |
| 21 | no | **ABSENT** | `RC=86` |
| 24 | no | **ABSENT** | `RC=56` |
| 26 | YES | `RC=125` | `RC=125` |
| 28 | no | **ABSENT** | `RC=""` |
| 32 | no | **ABSENT** | `RC=769` |

**同一份本地历史，在两个 provider 下序列化出 11 处字节差异**，最早的分歧点在 **idx 4**。

> 补充说明：`commandgo` 下保留的 `{2, 19, 26}` 恰好是"**有 `tool_calls` 且推理非空**"的消息 —— 这是因为只有这类消息才会写入路径 A 的 `Metadata`。该集合在 seq 28/29/30/32/34 五次请求中**逐字节稳定**，属于确定性行为而非噪声。

---

### 5.2 注入的 `Verified fact ledger` 系统消息位置漂移

会话历史中会**插入** `Verified fact ledger` 的 system 消息，且其索引**单调增长**：

| 请求 | 该消息所在索引 |
|---|---|
| 13 | `[6]` |
| 15 | `[6, 11]` |
| 17 / 20 | `[6, 11, 14]` |
| 23 | `[6, 11, 18]` |
| 25 | `[6, 11, 18, 23]` |
| 26 | `[6, 11, 18, 23, 26]` |
| 28 | `[6, 11, 18, 23, 30]` |
| 30 | `[6, 11, 18, 23, 31, 34]` |
| 35 | `[…, 36]` |

**问题**：这些消息被**追加进 history 内部**，而不是作为独立的顶层 `system` 参数。它们的出现会把**其后所有消息的索引整体后移** —— 这是一次真正的**前缀中间插入**，对前缀缓存是**最坏情形**（插入点之后全部失效）。

### 5.3 索引位移的放大效应

5.2 造成的索引位移，会让朴素的逐索引 diff 报出大量"变化"，但其中绝大多数是**位移伪影**，不是内容真的改了。

**这一点必须说清楚，否则会误判风险等级**：

- 按"逐索引比对 `content`/`role`/`tool_calls`"的粗粒度统计，会得到数百处差异；
- 剔除位移后，**真正的语义变化只有两类**：5.1 的 `reasoning_content` 与 5.2 的 ledger 插入位置。

---

## 6. 关键细节：运行时指纹对 5.1 是**盲的**

`backend\internal\agent\loop.go:4794-4840` 的 `promptMessageFingerprint` **显式排除了 `reasoning_content` 与 `Metadata`**。

后果：

- 运行时的 `messages_sha256` / `prompt_fingerprint` **看不到** §5.1 的不稳定；
- 也就是说，**"指纹没变"不等于"线上字节没变"**；
- 这会让"前缀缓存失效"在监控上表现为**静默发生**：指纹稳定，但 provider 侧缓存持续 miss。

**建议**：把 provider 相关的序列化结果纳入 `cache_surface_sha256`（该字段已存在，应确认它取的是**序列化后**的 payload，而非本地规范化结构）。

---

## 7. 前缀断裂点：实测清单

**同一 provider 连续段内**：前缀**稳定**
- 9 次纯追加（PURE APPEND）
- 3 次字节级完全一致（同一请求重试）

**每一次 provider 切换**：前缀**必然断裂**

| # | 切换 | 断裂原因 | 最早分歧点 |
|---|---|---|---|
| 1 | seq 7 → 9（hanhe → commandgo） | system prompt SHORT → LONG | prompt 第 84 行 |
| 2 | seq 12 → 13（commandgo → opencode.ai） | system prompt LONG → SHORT | prompt 第 84 行 |
| 3 | seq 14 → 15（opencode.ai → commandgo） | rc 模式 FULL → SPARSE | messages idx 4 |
| 4 | seq 22 → 23（commandgo → opencode.ai） | rc 模式 SPARSE → FULL | messages idx 4 |
| 5 | seq 27 → 28（opencode.ai → commandgo） | rc 模式 FULL → SPARSE | messages idx 4 |
| 6 | seq 34 → 35（commandgo → opencode.ai） | rc 模式 SPARSE → FULL | messages idx 4 |

**6 次切换，6 次断裂，命中率 0%。**

> 这与 §4.1.6 的结论一致：**跨 provider 结构上无法复用前缀缓存**。
> 但本报告补充了一个更精确的归因：**主因不是"厂商缓存相互独立"这种外部事实，而是运行时对同一份本地历史做了 provider 相关的重新序列化** —— 这是**我们自己可控**的部分。

---

## 8. 根因归纳

```
本地规范化历史（provider 中立）
        │
        │  ← 这里才是问题所在
        ▼
buildProtocolMessageMap(role, ..., protocol, providerHint, modelHint, messageMetadata)
        │
        ├─ 路径 A：Metadata["reasoning_content"] 存在 → 原样回放（含 ""）
        └─ 路径 B：ReplayableOpenAIReasoningContent(ctx, ...) → provider 相关策略
                    └─ IsDeepSeek(provider, baseURL, model) 命中 → 全部补 ""
```

**三层结论**：

1. **本地结构确实是 provider 中立的** —— 会话存储的消息、工具目录、prompt 分层都不含 provider 分支。
2. **但序列化不是** —— `providerHint` / `modelHint` 被传入消息构建函数，`reasoning_content` 的**键是否存在**由当前 provider 决定。于是"同一份本地历史"在两个 provider 下产出**不同字节**。
3. **`IsDeepSeek` 的判定过于宽松** —— 仅凭**模型名含 `deepseek`** 就把 DeepSeek 的强制回放策略套用到 `opencode.ai` 上。这是本次会话里 5.1 类断裂的**直接触发条件**。

---

## 9. 风险

| 级别 | 风险 | 说明 |
|---|---|---|
| **高** | 切换即冷启动 | 6/6 次切换前缀全断，每次都伴随完整 prefill 成本 |
| **高** | 监控静默 | §6：运行时指纹看不到 rc 不稳定，问题不会告警 |
| **中** | `IsDeepSeek` 误判扩散 | 任何以 `deepseek*` 命名的第三方托管模型都会触发同一路径 |
| **中** | `workspaceRoot` 不稳定 | §4：同一会话内 system prompt 在两个变体间来回切 |
| **低** | ledger 插入 | 由会话推进驱动，跨 provider 与同 provider 内都会发生 |

---

## 10. 建议

### 10.1 让 `reasoning_content` 回放策略**与 provider 解耦**

当前策略把"是否补 `reasoning_content` 键"绑定在**当前** provider 上，导致同一历史在不同 provider 下字节不同。

可选方向（择一）：

- **A（推荐）**：**统一为"总是回放"** —— 所有 assistant 消息一律携带 `reasoning_content`（无推理时 `""`）。这与 DeepSeek 的要求兼容，且对不要求该字段的 OpenAI 兼容网关无害（多一个被忽略的键）。**序列化结果变成 provider 无关，前缀在切换时保持。**
- **B**：**统一为"从不补空"** —— 只在路径 A 命中时回放。风险是 DeepSeek 真身会 HTTP 400（见 `openai_deepseek.go:58-65` 的注释），**不适用于真实 DeepSeek 端点**。

方向 A 同时消除 `IsDeepSeek` 误判带来的影响。

### 10.2 收紧 `IsDeepSeek` 的模型名匹配

`IsDeepSeekModel` 仅做 `strings.Contains(modelID, "deepseek")`。
建议改为**按 provider 身份判定**（provider 名 / baseURL），把**模型名**降级为辅助信号，避免第三方托管模型误触发。

### 10.3 把 provider 相关的序列化纳入指纹

`promptMessageFingerprint`（`loop.go:4794-4840`）应覆盖 `reasoning_content` 的存在性，或让 `cache_surface_sha256` 明确取自**序列化后**的 payload。否则 §5.1 类回归无法被测试和监控捕获。

### 10.4 稳定 `workspaceRoot` 分支

在会话生命周期内固定 `workspaceRoot`，避免 system prompt 在同一会话内于两个变体之间摆动。

### 10.5 补一条端到端回归测试

新增场景测试：**同一会话历史 → 依次用两个不同 provider 序列化 → 断言产出字节一致**（在 10.1 方向 A 落地后应通过）。这是唯一能防止该问题复发的护栏。

---

## 11. 附录：复现方式

```powershell
# 证据目录
$dir = "$env:USERPROFILE\.aicli\chat-logs\2026\09\21\session_20260921205129_cAMOwXwy\http"

# 请求文件命名
#   0NN_request_provider_wrapper.json   （奇数序号，共 26 个）

# 每个请求可取：
#   .body_json.messages        → 实际发往 provider 的消息数组
#   .body_json.tools           → 工具目录
#   .request_metadata._request_debug.protocol        → 恒为 "openai"
#   .request_metadata._request_debug.tools_sha256    → 26/26 相同
#   .request_metadata._request_debug.prompt_layout_length → 9749 或 12292
```

**关键比对**：对 seq 34 与 seq 35 逐 idx 打印每条 assistant 消息的 `keys()`，
即可直接观察到 `reasoning_content` 键的存在性差异（见 §5.1 表格）。

---

## 12. 一句话回答原问题

> **`prompt` 变了**（2 个变体，纯插入）；
> **`tools` 没变**（46 个，字节级一致）；
> **`messages` 变了** —— 而且**每次跨 provider 切换都变**，主因是 `reasoning_content` 键的存在性由**当前 provider** 决定；
> 所以**跨 provider 的前缀缓存无法复用，6 次切换 6 次断裂**。
> 根因不在厂商，而在**运行时对同一份本地历史做了 provider 相关的重新序列化**。
