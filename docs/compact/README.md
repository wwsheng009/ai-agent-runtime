# Auto Compact 实施方案

## 目标

参考 `E:\projects\ai\codex` 的 auto compact 机制，为 `ai-agent-runtime` 增加统一的压缩适配层，并先落地第一阶段的本地压缩能力。

第一阶段目标已经收敛为：

- 增加统一的压缩适配层，保留本地压缩 / 远程压缩两种模式的扩展位。
- 先实现本地压缩。
- 先做 `pre-turn auto compact`，不做 `mid-turn`。
- 压缩结果不是临时 prompt 注入，而是直接替换并持久化 session history。
- 压缩阈值按 `provider -> model_capabilities -> model` 解析，不再使用全局固定 token limit。

## Codex 自动压缩机制分析

`codex` 的自动压缩可以拆成三层：

1. 触发层
   在真正发起下一次模型请求前判断是否需要压缩。
2. 适配层
   根据能力决定走本地压缩还是远程压缩。
3. 历史替换层
   产出 replacement history，并把 live history 替换成压缩后的历史。

它不是简单的“超过窗口就截断”，而是：

1. 把较早历史交给压缩实现生成 handoff summary。
2. 保留近期消息窗口。
3. 用“压缩摘要 + 近期原始消息”重建历史。
4. 持久化 replacement history，供后续回合继续使用。

这个结构值得直接借鉴，因为：

- 触发条件和实现方式解耦。
- 本地压缩和远程压缩可以挂在同一入口。
- session 语义清晰，后续恢复 / 重放 / 继续对话都基于压缩后的真实历史。

## 当前仓库的基础能力

当前仓库已经有三块可复用能力：

- `backend/internal/chat/session.go`
  已支持 `ReplaceHistory()`，能承载“压缩后替换历史”。
- `backend/internal/chat/actor.go`
  `SessionActor` 是最接近 Codex `pre-turn compact` 的统一入口。
- `backend/internal/contextmgr`
  已有 compaction summary、recent window、checkpoint 保存与复用能力。

但此前仍缺少两点：

- 没有统一 compact adapter/runtime。
- 没有按 provider/model capability 解析的自动触发阈值。

## 配置设计

### 核心原则

压缩 token limit 必须跟具体模型绑定，不能放在全局 `context` 配置下。

原因：

- 不同模型上下文窗口不同。
- 相同 provider 下不同模型的 context window 可能差异很大。
- 自动压缩的安全触发线必须基于具体模型能力，而不是运行时统一常数。

### 配置位置

压缩阈值配置放在：

`providers.items.<provider>.model_capabilities.<model>`

通过扩展 `ModelCapabilitySpec` 承载：

- `max_context_tokens`
- `auto_compact_ratio`
- `auto_compact_token_limit`
- `auto_compact_mode`
- `supports_remote_compact`

示例：

```yaml
providers:
  items:
    openai-main:
      enabled: true
      protocol: openai
      default_model: gpt-5
      model_capabilities:
        gpt-5:
          max_context_tokens: 272000
          auto_compact_ratio: 0.9
          supports_remote_compact: true
          auto_compact_mode: remote
        gpt-5-mini:
          max_context_tokens: 128000
          auto_compact_token_limit: 100000
        "*":
          max_context_tokens: 128000
          auto_compact_ratio: 0.9
```

### 解析规则

当前实现采用下面的解析顺序：

1. 解析当前会话实际使用的 provider / model。
2. 在 `model_capabilities` 中优先匹配精确模型名。
3. 精确模型不存在时回退到 `*`。
4. 如果配置了 `auto_compact_token_limit`，直接使用它。
5. 否则如果配置了 `max_context_tokens`，按 `max_context_tokens * auto_compact_ratio` 计算。
6. 如果 `auto_compact_ratio` 未配置或非法，默认使用 `0.9`。

也就是说，默认触发线是：

`max_context_tokens * 90%`

### Prompt 预算与 context manager

自动压缩阈值和发送前的 prompt 预算使用同一组 provider/model capability 语义。ReAct 每次发起模型请求前，会先解析一个有效 prompt 预算，并把它传给 `contextmgr.Manager.Build()`，避免 context manager 在 Build 阶段先用 balanced profile 的保守默认值裁掉长会话目标。

解析顺序：

1. 如果配置了 `context.maxPromptTokens`，它表示用户显式硬上限，优先级最高。
2. 否则如果模型 capability 配置了 `auto_compact_token_limit`，使用该值。
3. 否则如果模型 capability 配置了 `max_context_tokens`，按 `max_context_tokens * auto_compact_ratio` 计算。
4. 否则如果 provider 暴露了 `MaxContextTokens`，按 provider context limit 的默认比例 `0.9` 计算。
5. 最后才使用 `context.fallbackMaxPromptTokens`；如果该配置为空，则使用内置默认 `32000`。

`context.profile` 仍然控制组织策略，例如 recent messages、recall 数量、observation 数量和 ledger/summary 策略；它不再单独把已知大上下文模型的 prompt 预算压到 12k。需要主动限制请求大小时，应配置 `context.maxPromptTokens`。

兜底预算可在 `backend/configs/runtime.yaml` 中配置：

```yaml
context:
  fallbackMaxPromptTokens: 32000
```

### 模式解析规则

压缩模式也按 model capability 解析，但它和 token trigger line 是两件事。

当前实现顺序是：

1. 如果请求显式指定 `mode=local|remote`，优先使用请求值。
2. 否则读取 `model_capabilities.<model>.auto_compact_mode`。
3. 如果没有显式 mode，但 `supports_remote_compact=true`，自动选择 `remote`。
4. 其他情况默认 `local`。

这意味着：

- 默认仍然是本地压缩。
- 只有 model capability 明确声明远端能力时，才会切到 remote。
- `context.compactionMode` 仍然只表示 context manager 的摘要/ledger 策略，不表示 auto compact 的 local/remote 模式。

### `context` 配置的职责

`runtime config` 下的 `context` 配置仍然保留，但职责变为：

- `keepRecentMessages`
- `maxMessages`
- `compactionMode`
- `recall / observation` 等上下文治理参数

它不再负责 auto compact 的 token trigger line。

第一阶段里，`context.keepRecentMessages` 只影响“压缩后保留多少近期原始消息”。

## 第一阶段落地架构

### 1. 新增 `compactruntime`

新增包：

`backend/internal/compactruntime`

职责：

- 根据 provider/model capability 解析自动压缩阈值。
- 判断当前 session 是否达到压缩线。
- 调用本地压缩适配器生成 replacement history。
- 输出压缩结果给调用方，由调用方决定何时安装到 session。

这一层是后续增加 `RemoteAdapter` 的预留点。

### 2. 本地压缩适配器

第一阶段只实现 `LocalAdapter`，行为如下：

1. 保留所有 system 消息。
2. 用 recent window 切出“近期原始消息”。
3. 将较早历史发送给当前 provider/model 做一次内部 compact summary 请求。
4. 构造 replacement history：
   - system 消息
   - 一条 assistant compaction summary
   - 近期原始消息
5. 成功后由调用方替换并持久化 session history。

本地 summary 请求约束：

- 不带 tools。
- `temperature=0`。
- metadata 带 `internal_operation=compact`。
- 这次请求不会写成用户可见会话回合。

### 3. checkpoint 复用

第一阶段本地压缩会把 summary 落到现有 artifact checkpoint。

当前实现复用了现有 summary checkpoint reason：

- `history_window_summary_segment`

并在相同历史片段再次压缩时优先复用已有 checkpoint summary，避免重复触发一次本地模型总结。

### 4. replacement history 形状

当前项目使用原生 `types.Message`，不照搬 Codex 的内部 item 形状。

replacement history 统一为：

```text
system prompt(s)
assistant: Compacted context from earlier turns: ...
recent raw messages
```

compaction message 的 metadata 包含：

- `context_stage=compaction`
- `compact_mode=local`
- `compact_phase=pre_turn`
- `checkpoint_id`
- `segment_start`
- `segment_end`

## 接入点

### 1. SessionActor

入口：

`backend/internal/chat/actor.go`

位置：

- `startSessionRun()` 中，在真正进入 `runLoop()` / `continueLoop()` 之前。

当前实现行为：

- `resume` 场景直接跳过。
- 有 `PendingTool / PendingApproval / PendingQuestion` 时跳过。
- 压缩成功后先持久化，再继续运行本轮。
- 持久化失败时回滚到原历史，不污染当前会话对象。

### 2. `agent_chat` 的 session 路径

入口：

`backend/internal/api/skills/handler.go`

位置：

- 解析完 provider/model、创建 agent 后
- 生成 `historyForAgent` 之前

当前实现只在真正使用 session history 的请求里触发：

- `session_id + 单条 user message`
- `session_id + 空 messages`

这样可以保证：

- `SessionActor` 路径和 `agent_chat` 直调路径看到同一份 replacement history。
- 不会对“客户端自己上传完整 messages 数组”的请求错误修改 session。

### 3. 本地 aicli actor host

本地 `aicli` actor host 之前没有把 runtime context 选项合并进 agent options。

当前实现已补齐：

- `context.keepRecentMessages`
- `context.maxPromptTokens`
- `context.fallbackMaxPromptTokens`
- `context.profile`
- `context.compactionMode`
- 其他 context 相关覆盖项

这样本地 actor 路径也能使用同样的 recent window 规则。

## 运行时事件

新增 session 级事件：

- `session_compact_started`
- `session_compact_completed`
- `session_compact_skipped`
- `session_compact_failed`

关键字段：

- `session_id`
- `phase`
- `mode`
- `reason`
- `token_before`
- `token_after`
- `trigger_token_limit`
- `max_context_tokens`
- `provider`
- `model`
- `compacted_messages`
- `checkpoint_id`
- `checkpoint_ids`

## 触发方式

当前支持两类触发：

### 1. 自动触发

自动压缩发生在每次真正发起新一轮模型调用前，也就是 `pre-turn`。

触发条件：

- 先按 `provider -> model_capabilities -> model` 解析当前模型的压缩阈值
- 默认阈值为 `max_context_tokens * 0.9`
- 如果配置了 `auto_compact_token_limit`，则优先使用显式值
- 当当前 history token 估算值超过阈值时，自动执行 compact

### 2. `aicli` 手动触发

`aicli chat` 中已增加：

```text
/compact [mode]
```

支持：

- `/compact`
  - 默认按 `auto` 模式解析，优先遵循模型 capability 的配置
- `/compact local`
  - 强制走本地压缩
- `/compact remote`
  - 强制走远端压缩

手动触发会绕过“是否超过阈值”的判断，直接执行压缩；但仍然保留 provider/model capability 解析，以及 pending tool / approval / question 等安全跳过条件。

## 当前实现状态

第一阶段已完成：

- model capability 扩展：
  - `max_context_tokens`
  - `auto_compact_ratio`
  - `auto_compact_token_limit`
- `llm` 侧共享 capability 解析
- `compactruntime` 本地适配层
- summary checkpoint 复用
- `SessionActor` 接入
- `agent_chat` session 路径接入
- 本地 `aicli` actor host context 透传补齐
- 单元 / 集成测试

第二阶段已完成第一版远端压缩：

- `compactruntime` 已支持 `local | remote` 模式选择
- `ModelCapabilitySpec` 已扩展：
  - `auto_compact_mode`
  - `supports_remote_compact`
- `llm.ProviderWrapper` 已实现 Codex 远端压缩
- `llm.GatewayClient` 已实现 Codex 远端压缩
- 真实远端协议已对接：
  - `POST /v1/responses/compact`
- 远端返回的 `compaction` item 已被持久化为可回放的 assistant message
- 后续 Codex 请求会通过 `reasoning_details.metadata.response_output_items` 自动回放 opaque compaction state

## 已验证范围

已补测试覆盖：

- 精确模型 capability 命中
- wildcard fallback
- 显式 `auto_compact_token_limit`
- 默认 `90%` 比例
- summary checkpoint 复用
- `SessionActor` 历史替换
- pending 状态跳过
- `agent_chat` session 路径压缩
- provider config 中 model capability 透传
- context manager Build 阶段使用同一有效 prompt 预算，不再被 balanced profile 默认 12k 提前裁剪
- `context.fallbackMaxPromptTokens` 只在无法解析 capability/provider context limit 时作为兜底预算
- Codex remote compact endpoint 路径构造
- 远端 `compaction` item 的持久化与回放
- gateway 路径下的 remote compact

建议验证命令：

```powershell
cd E:\projects\ai\ai-agent-runtime\backend
go test ./internal/llm ./internal/compactruntime ./internal/chat ./internal/api/skills ./internal/runtimeserver ./internal/bootstrap ./cmd/aicli/commands
```

## 第二阶段建议

第二阶段当前状态：

1. `compactruntime` 已增加 `RemoteAdapter` 和 mode 选择逻辑。
2. Codex provider 已接入真实远端 compact endpoint。
3. 当前 provider 若声明 `remote` 但没有实现远端压缩接口，会显式 skip，reason 为：
   `remote_compact_unsupported`

后续可以继续补：

1. 其他 provider 的 `RemoteCompactor`
   沿用同一条 replacement history 安装链路接入真实 compact endpoint。
2. `mid-turn compact`
   需要和 pending tool / approval / question 的恢复语义一起设计。
3. model downshift compact
   当后续回合切换到更小 context 模型时，提前压缩旧历史。

## 结论

本项目的 auto compact 第一阶段已经从“context 里的全局 token limit 方案”调整为“provider/model capability 驱动”的实现。

当前正式语义是：

- 触发阈值按具体模型能力解析。
- 默认阈值是 `max_context_tokens * 0.9`。
- `context` 只负责保留窗口等上下文治理参数。
- 当前已支持本地 `pre-turn` 压缩，以及 Codex 远端 `pre-turn` 压缩，并直接替换持久化 session history。

这条链路已经为后续远程压缩预留好适配层，不需要再重做 session 语义。
