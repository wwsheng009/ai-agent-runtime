# ChatSession.Messages 去协议化重构计划

日期：2026-05-01
关联文档：`docs/plan/aicli-model-preference-persistence-plan.md`（第 7 步的展开）

## 背景

`docs/plan/aicli-model-preference-persistence-plan.md` 第 7 条建议把
`ChatSession.Messages` 从 `[]map[string]interface{}` 逐步重构为
`[]types.Message`，并移除 `aicli_raw_message_json` 对会话层的污染。

前 6 步（模型偏好持久化、`/model` 增强、adapter request capability）均已完成。
第 7 步的范围显著大于前述任一步，需要单独拆出计划，按阶段验证。

## 现状扫描

### Messages 字段污染点

以下文件直接读写 `session.Messages` 或其 map 形态，共 17 个文件 98 处引用：

- 运行时：
  - `cmd/aicli/commands/chat_session.go`
  - `cmd/aicli/commands/chat_core.go`
  - `cmd/aicli/commands/chat_actor_registry.go`
  - `cmd/aicli/commands/chat_actor_executor.go`
  - `cmd/aicli/commands/chat_history.go`
  - `cmd/aicli/commands/logger.go`
  - `cmd/aicli/commands/skills_integration.go`
  - `cmd/aicli/commands/image_generation_exposure.go`
  - `cmd/aicli/commands/command.go`
- 测试：
  - `cmd/aicli/commands/chat_core_test.go`
  - `cmd/aicli/commands/chat_profile_test.go`
  - `cmd/aicli/commands/chat_runtime_events_test.go`
  - `cmd/aicli/commands/chat_setup_test.go`
  - `cmd/aicli/commands/chat_local_orchestration_integration_test.go`
  - `cmd/aicli/commands/chat_docs_team_regression_test.go`
  - `cmd/aicli/commands/message_test.go`
  - `cmd/aicli/commands/command_test.go`

### `aicli_raw_message_json` 使用点

metadata 键 `chatRuntimeMessageRawJSONKey`（值为 `aicli_raw_message_json`）出现在：

- `cmd/aicli/commands/chat_session.go`：
  - `runtimeMessageFromAICLIMessage` 把 adapter 产出的 `map[string]interface{}`
    Marshal 后写入 metadata，作为完整协议原文快照。
  - `aicliMessageFromRuntimeMessage` 优先读取这份原文，用于恢复 Codex
    function_call 条目等协议特定结构。
  - `exportRuntimeMessageMetadata` 显式屏蔽该键，避免外泄。
- `cmd/aicli/commands/chat_session_test.go` / `chat_core_test.go`：大量断言
  依赖这份原文（Codex replay、assistant reasoning 元数据等）。

关键事实：

1. `response_output_items` 是 Codex 协议用于 replay function_call 的数组。
   当前既保存在 `reasoning_block.Metadata` 里，也通过 `aicli_raw_message_json`
   的原文形式保留。它是 replay 正确性的主要依据。
2. Anthropic `signature` / Gemini `thoughtSignature` 已经进入统一
   `runtimetypes.ReasoningBlock.OpaqueState`，不依赖 raw JSON。
3. 图片生成结果已有独立工件落盘逻辑，raw JSON 里大块 base64 已被裁剪。

## 目标与非目标

### 目标

- 最终 `ChatSession.Messages` 的底层存储变成 `[]runtimetypes.Message`。
- 运行时不再依赖 `aicli_raw_message_json` metadata；所有协议特定字段通过
  结构化 metadata 或 adapter 回放链路获得。
- 会话层只感知 `runtimetypes.Message/ToolCall/ReasoningBlock/ContentPart`，
  协议转换集中在 `RuntimeMessagesToProtocolMessages` 与 adapter 边界。

### 非目标

- 不重写 adapter 协议解析。adapter 继续产出 `map[string]interface{}`，
  在适配器边界完成到 `runtimetypes.Message` 的转换。
- 不改变持久化会话文件格式（`SessionManager` 本来就以 `types.Message` 存）。
- 不在这一计划内重构 UI/Logger 的渲染管线，除非移除 raw JSON 桥接所必需。

## 阶段划分

### 阶段 A：收约写入点（类型不变）

目的：为后续切换底层类型设防火墙；本阶段不改变二进制行为。

1. 在 `chat_history.go` 新增：
   - `appendAICLIMessage(session, msg)`
   - `replaceAICLIMessages(session, msgs []map[string]interface{})`
   - `truncateAICLIMessages(session, keep int)`
2. 把所有 `session.Messages = append(session.Messages, ...)`、
   `session.Messages = newSlice`、`session.Messages = session.Messages[:n]`
   等直接写入改走封装函数。
3. 所有纯读路径（logger/skills exposure/history 渲染）保持不变。
4. `go test ./cmd/aicli/commands/...` 必须与阶段 A 前行为一致。

验收：
- 没有文件再直接对 `session.Messages` 做 `append/赋值/切片截断`。
- 测试 fixture 写入点同样走封装函数或调用 `replaceAICLIMessages`。

### 阶段 B：落地 runtime message 镜像

目的：为切换底层类型做数据铺垫，但仍暴露 map 形态给旧调用方。

1. 在 `ChatSession` 内新增 `runtimeMessages []runtimetypes.Message`，
   与 `Messages []map[string]interface{}` 并存。
2. 封装函数统一维护两份镜像：
   - 写入时：map 形态走 `runtimeMessageFromAICLIMessage` 同步到
     `runtimeMessages`。
   - 读取时：新增 `runtimeMessagesSnapshot(session) []runtimetypes.Message`
     作为后续调用方的切换入口。
3. 把 `chat_actor_executor.go`、`chat_provider_turn.go` 等构造
   `ProviderTurnRequest.Messages` 的地方改成直接读 `runtimeMessages`，
   不再经过 `buildRuntimeHistoryFromAICLIMessages`。
4. 保留 `aicli_raw_message_json` 作为兼容桥，不在本阶段删除。

验收：
- 所有 provider turn 调用链从 `runtimeMessages` 取输入。
- 对比阶段 A 的测试基线，response 行为不变。

### 阶段 C：移除 raw JSON 桥接

目的：彻底去除 `aicli_raw_message_json` 对会话层的污染。

1. 把 Codex replay 依赖的 `response_output_items` 显式写入
   `ReasoningBlock.Metadata`，并在 `RuntimeMessagesToProtocolMessages` /
   Codex adapter 里直接读结构化字段。
2. Anthropic/Gemini 的签名已在 `ReasoningBlock.OpaqueState`，确认无回归后
   停止在 metadata 中保存原文。
3. 把 `runtimeMessageFromAICLIMessage` 改为不再写 `chatRuntimeMessageRawJSONKey`；
   `aicliMessageFromRuntimeMessage` 直接从结构化字段重建 map。
4. 更新 `chat_session_test.go` / `chat_core_test.go` 的断言到结构化字段。
5. 删除 `chatRuntimeMessageRawJSONKey` 常量与相关过滤逻辑。

验收：
- `grep -R "aicli_raw_message_json" backend/` 仅剩文档引用。
- Codex replay、Anthropic thinking replay、Gemini thought replay 回归测试全绿。
- 持久化会话的存量 message metadata 里即使仍含旧键，也不再被运行时使用。

### 阶段 D：切换底层类型

目的：完成最终目标。

1. 把 `ChatSession.Messages` 的类型改为 `[]runtimetypes.Message`，
   删除镜像字段 `runtimeMessages`。
2. logger/skills exposure/history 渲染改为直接消费
   `runtimetypes.Message`，或通过集中的 `aicliViewMessage(msg)`
   转换为 map 形态供遗留 UI 使用。
3. 删除 `buildRuntimeHistoryFromAICLIMessages` / `buildAICLIMessagesFromRuntimeHistory`
   中只为双向桥存在的路径；保留必要的 map 视图函数。
4. 更新所有测试 fixture，直接构造 `runtimetypes.Message`。

验收：
- `ChatSession` 中不再出现 `map[string]interface{}` 作为历史消息的底层表示。
- 运行时逻辑只通过 `runtimetypes` 抽象读写历史消息。
- 全量测试、`docs-team` regression、local orchestration integration 测试绿。

## 风险与取舍

- **测试面庞大**：每阶段都必须跑 `go test ./cmd/aicli/commands/...` 和
  `./internal/llm/...`。阶段 C 必须额外跑 Codex replay 专项测试。
- **持久化兼容**：旧会话文件可能携带 `aicli_raw_message_json`。阶段 C
  要保留读取兼容，只是不再写入；阶段 D 可逐步弱化读取路径。
- **adapter 边界漂移**：阶段 C 要求 Codex adapter 能从结构化
  `ReasoningBlock.Metadata.response_output_items` 直接重建 replay payload，
  需要在 adapter 内补单元测试覆盖。
- **UI 回归**：logger 与交互式 UI 对 map 形态有隐含假设（如
  `content` 必须是 string，`tool_calls` 必须是 `[]map[string]interface{}`）。
  阶段 D 切换类型时必须同步 UI 适配层。

## 建议推进节奏

1. 阶段 A 作为独立 PR 合入，不改变行为，便于快速验证。
2. 阶段 B 作为独立 PR 合入，引入 runtime 镜像。
3. 阶段 C 需要额外的 adapter 结构化 metadata 测试，建议单独 PR。
4. 阶段 D 在前三阶段稳定运行一段时间后合入，保留可回滚。

## 当前状态

本计划对应的实现已在当前工作区落地：

- `ChatSession.Messages` 已切换为 `[]runtimetypes.Message`。
- 运行时路径已不再依赖 `aicli_raw_message_json`。
- 相关命令、历史、会话恢复和测试已完成迁移并通过全量回归。

后续如果继续压缩协议边界，可以在此基础上再推进 adapter / replay 层的进一步整理。
