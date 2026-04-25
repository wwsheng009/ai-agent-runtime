# Codex `image_generation` Tool 调研与落地方案

日期：2026-04-25

## 1. 目标

目标是在 `ai-agent-runtime` 中补齐与参考项目 `E:\projects\ai\codex` 一致的 `image_generation` 能力，但范围限定为：

- 仅对 `codex` 协议启用。
- 启用前必须通过 provider/model 能力约束。
- 模型生成出的图片结果需要解析、落盘，并接入当前项目的 artifact 体系。

本轮先做调研和方案，不做代码实现。

## 2. 参考项目结论

参考仓库：`E:\projects\ai\codex\codex-rs`

### 2.1 `image_generation` 不是 function tool

参考：

- `codex-rs/tools/src/tool_spec.rs`
- `codex-rs/tools/src/tool_registry_plan.rs`

关键点：

- `ToolSpec` 里存在独立变体 `ImageGeneration { output_format }`。
- 序列化后工具类型是 `type: "image_generation"`。
- 注册名固定为 `image_generation`。
- 注册时 `supports_parallel_tool_calls = false`。

也就是说，这个能力不是：

- 本地可执行 function tool
- MCP tool
- 一个带 `name/parameters` 的普通 function schema

而是 Responses/Codex 协议里的 provider-native tool。

### 2.2 参考项目的启用条件

参考：

- `codex-rs/tools/src/tool_config.rs`

关键逻辑：

- 需要 `image_generation_tool_auth_allowed`
- 需要 feature flag `Feature::ImageGeneration`
- 需要 `supports_image_generation(model_info)`

其中 `supports_image_generation(model_info)` 当前只检查：

- `model_info.input_modalities` 包含 `Image`

这个判断在 `codex-rs` 里成立，是因为它有更完整的模型 catalog。  
在当前仓库里仅凭 `SupportsVision` 不够，需要显式建模“输入模态”和“原生 tool 能力”。

### 2.3 参考项目的响应处理与落盘

参考：

- `codex-rs/core/src/event_mapping.rs`
- `codex-rs/core/src/stream_events_utils.rs`

关键逻辑：

- `ResponseItem::ImageGenerationCall` 会映射成 `TurnItem::ImageGeneration`
- `result` 被当作标准 base64 直接解码
- 输出路径为 `generated_images/<session_id>/<sanitized_call_id>.png`
- 成功后把 `saved_path` 写回 turn item
- 同时追加一条上下文消息，告诉后续对话图片保存目录和输出模板路径

参考项目还覆盖了几个重要边界：

- 非标准 base64 拒绝
- `data:` URL 拒绝
- call id 需要路径清洗
- 允许覆盖旧文件

## 3. OpenAI 官方文档核对

参考：

- `https://platform.openai.com/docs/models/gpt-5.4`

目前官方模型文档对 `GPT-5.4` 的关键信息是：

- `Responses API` 下 `Image generation` 为 `Supported`
- 模型模态仍然是 `Text input: yes`、`Text output: yes`、`Image input: yes`、`Image output: no`

因此这里要实现的是：

- “Codex/Responses 内建 tool 能力”

不是：

- “模型原生图片输出 modality”
- “把图片生成能力并入 `SupportsVision`”

## 4. 当前仓库现状

### 4.1 Codex adapter 已经保留了原始 output items

参考：

- `backend/internal/llm/adapter/codex.go`
- `backend/internal/llm/reasoning_helpers.go`

当前已有能力：

- 非流式时会把 `response.output` 原样挂到 `response_output_items`
- 流式时会累计 `response.output_item.done` 的 `item`
- `ProcessResponse()` 会把 `response_output_items` 放进 `ReasoningBlock.Metadata`
- 多轮重放时也已经优先使用 `response_output_items`

这意味着：

- 当前仓库已经具备“保留原始 Codex output item”的基础
- 第一阶段不必重写整套 Codex 流解析器

但当前缺少：

- 对 `type=image_generation_call` 的专门识别
- base64 图片结果落盘
- 落盘后的 artifact/路径回写

### 4.2 当前 tool 暴露链路默认假设“所有工具都是 function”

参考：

- `backend/internal/tools/manager.go`
- `backend/cmd/aicli/commands/chat_setup.go`
- `backend/cmd/aicli/commands/function_catalog.go`
- `backend/cmd/aicli/functions/builder.go`
- `backend/internal/llm/gateway_client.go`

当前链路的共同特点：

- `ToolDescriptor` 只有 `Name/Description/Parameters`
- `types.ToolDefinition` 也默认围绕 function schema
- `FunctionCallBuilder` 的 Codex 实现仍然输出扁平 function tool
- `GatewayClient.convertTools()` 要求每个 tool 都有 `name`

而 `image_generation` 实际需要的请求体是：

```json
{
  "type": "image_generation",
  "output_format": "png"
}
```

它没有：

- 本地执行入口
- `parameters`
- `name`

所以它不应该直接塞进现有的本地 function tool 管线。

### 4.3 当前 provider/model 能力模型过粗

参考：

- `backend/internal/llm/runtime.go`
- `backend/internal/llm/provider.go`
- `backend/internal/llm/gateway_client.go`
- `backend/internal/agentconfig/config.go`

现状：

- `ModelCapabilities` 只有 `SupportsVision/SupportsTools/SupportsStreaming/SupportsJSONMode`
- `Provider.GetCapabilities()` 是 provider 级，不是 model 级
- `agentconfig.Provider` 只有 `supported_models` 字符串列表
- `support_types` 当前表示“兼容哪些协议类型”，不是“输入模态”

因此当前无法准确表达：

- 某个 `codex` provider 是否允许 `image_generation`
- 某个具体模型是否支持 `image_generation`
- 某个模型是否允许 `text/image` 输入模态

### 4.4 当前 artifact 能力可复用，但没有图片专用约定

参考：

- `backend/internal/output/gateway.go`
- `backend/internal/output/tool_result_content.go`
- `backend/internal/toolresult/kind.go`
- `backend/internal/artifact/store.go`
- `backend/internal/artifact/checkpoint_files.go`

现状：

- `artifact.Store` 已支持文本 artifact
- `artifact.Store` 也已有 `SaveBlob/LoadBlob`
- `output.Gateway` 目前主要处理文本/结构化输出
- `toolresult.Kind` 只有 `text/structured/binary/empty`

这说明：

- 当前项目并不是没有持久化基础
- 但还缺“图片 artifact”的统一约定和展示语义

## 5. 推荐设计

## 5.1 范围与原则

建议按下面原则实现：

- 只在 `provider.protocol == codex` 时考虑暴露
- 不走本地可执行 tool manager
- 不把它伪装成 function tool
- 不把图片生成能力混进 `SupportsVision`
- 启用判断必须落到“具体 provider + 具体 model”

## 5.2 启用条件

建议新增 model 级能力配置，而不是复用现有 `support_types`。

推荐新增配置形态：

```yaml
providers:
  items:
    CODEX_03:
      protocol: codex
      supported_models:
        - gpt-5.4
        - gpt-5.4-mini
      model_capabilities:
        gpt-5.4:
          input_modalities: [text, image]
          native_tools:
            image_generation: true
        gpt-5.4-mini:
          input_modalities: [text]
          native_tools:
            image_generation: false
        "*":
          input_modalities: [text]
          native_tools:
            image_generation: false
```

推荐启用公式：

```text
provider.protocol == codex
&& model_capabilities.input_modalities 包含 text
&& model_capabilities.input_modalities 包含 image
&& model_capabilities.native_tools.image_generation == true
```

说明：

- `text` 和 `image` 都要求显式存在，符合本次需求
- 不建议复用 `SupportsVision`
- 不建议复用 `support_types`

## 5.3 请求侧方案

第一阶段建议不要把 `image_generation` 注册进 `internal/tools.Manager`。

原因：

- `Manager` 代表本地可执行工具
- `chat_setup.go` 会把这些工具注册成可执行 function
- `image_generation` 并没有本地执行入口

推荐做法：

1. 保持本地 function tool 暴露逻辑不变。
2. 在 Codex 请求装配阶段额外注入 provider-native tool。
3. 该 tool 只在能力判断通过时加入：

```json
{
  "type": "image_generation",
  "output_format": "png"
}
```

4. 当请求里包含该 tool 时，建议同步设置：

```json
{
  "parallel_tool_calls": false
}
```

这样更接近参考项目 `supports_parallel_tool_calls = false` 的约束，能减少图片生成与普通 function tool 并发时的状态复杂度。

推荐落点：

- `backend/cmd/aicli/commands/chat_core.go` 的 `adapterRequestConfig()`
- `backend/internal/llm/gateway_client.go` 的 `convertTools()` 附近

建议新增一个共享 helper，例如：

- `buildCodexNativeTools(provider, model) []map[string]interface{}`

然后在 Codex 请求里把：

- function tools
- native tools

合并到同一个 `tools` 数组中。

这条路径比改造整个 `FunctionCatalog` 更小，也更贴近本次“只做 codex 协议”的需求。

## 5.4 响应解析与保存方案

推荐分成两层：

### A. adapter 层继续负责“保留原始 output item”

当前 `backend/internal/llm/adapter/codex.go` 已能保留 `response_output_items`，所以第一阶段只需要补一个 helper，从 output items 里筛出：

- `type == "image_generation_call"`

需要读取的字段：

- `id`
- `status`
- `revised_prompt`
- `result`

### B. chat/runtime 层负责“解码、落盘、写 artifact”

保存动作不要放在 adapter 内部，原因是 adapter 不掌握：

- 会话目录
- logger 目录
- artifact store

推荐在拿到最终 response 后、写回最终 assistant message 前执行图片提取与保存。

## 5.5 文件保存路径

推荐路径设计向当前项目现有 artifact 目录风格靠拢，而不是直接照抄 `codex_home/generated_images`。

推荐优先级：

1. 如果 `logger.SessionDirPath()` 可用，保存到：

```text
<chat-log-session-dir>/generated-images/<sanitized_call_id>.png
```

2. 否则回退到与 runtime session 文件并列的 artifact 目录：

```text
<session-id>.artifacts/generated-images/<sanitized_call_id>.png
```

这与现有 `runtime-http` 目录组织方式一致，更符合当前仓库的 session 习惯。

建议新增 helper，风格类似：

- `currentRuntimeHTTPArtifactDir(session)`
- 新增 `currentGeneratedImageArtifactDir(session)`

## 5.6 artifact 保存方案

建议同时保留两份信息：

### 本地文件

- 便于用户直接打开图片
- 与参考项目行为一致

### artifact store 元数据

建议：

- 用 `artifact.Store.SaveBlob()` 保存原始 PNG bytes
- 用 `artifact.Store.Put()` 保存可检索记录

建议 metadata 至少包含：

- `artifact_type: image`
- `mime_type: image/png`
- `image_call_id`
- `saved_path`
- `blob_id`
- `sha256`
- `revised_prompt`
- `provider`
- `model`

`artifact.Record.Content` 不建议塞原始 base64，而建议保存一个小的 JSON 摘要，例如：

```json
{
  "type": "image_generation",
  "mime_type": "image/png",
  "saved_path": "E:/.../generated-images/ig_xxx.png",
  "blob_id": "blob_xxx",
  "revised_prompt": "..."
}
```

这样有几个好处：

- artifact 搜索结果更稳定
- 不会把大段 base64 塞进文本 artifact
- 后续 UI 层可以根据 `blob_id` 或 `saved_path` 去取图

## 5.7 base64 处理约束

建议对齐参考项目：

- 只接受标准 base64
- 拒绝 `data:` URL
- 拒绝 urlsafe base64
- 文件名必须清洗
- 默认输出 `.png`

这部分建议单独抽成 helper，例如：

- `decodeImageGenerationPayload(result string) ([]byte, error)`
- `sanitizeGeneratedImageCallID(callID string) string`

## 5.8 给模型和用户的回写

图片保存成功后，建议回写两类信息：

1. assistant message metadata

- 保留原始 `response_output_items`
- 额外补充 `generated_images` 元数据列表

2. 面向用户的轻量提示

例如：

- 图片已保存到哪个路径
- 对应 artifact id / blob id

这样更接近参考项目“保存完成后给下一轮上下文一个稳定引用”的思路。

## 6. 建议的最小改造面

建议优先改这些位置：

- `backend/internal/agentconfig/config.go`
  - provider 增加 `model_capabilities`
- `backend/cmd/aicli/commands/chat_core.go`
  - 根据 provider/model 能力注入 Codex native tools
- `backend/internal/llm/gateway_client.go`
  - 同步支持 native tool 注入，避免两条请求链路行为不一致
- `backend/internal/llm/adapter/codex.go`
  - 增加 image output item 提取 helper
- `backend/internal/types/codex/types.go`
  - 补 `image_generation_call` 相关结构
- `backend/internal/toolresult/kind.go`
  - 可选新增 `image`
- `backend/cmd/aicli/commands`
  - 新增图片保存与 artifact 记录 helper

## 7. 测试计划

至少覆盖下面几组：

### 7.1 启用判断

- `protocol != codex` 时不暴露
- `codex` 但 `native_tools.image_generation=false` 时不暴露
- `codex` 且缺少 `text` 模态时不暴露
- `codex` 且缺少 `image` 模态时不暴露
- `codex` 且 `text/image + image_generation=true` 时暴露

### 7.2 请求体

- 请求中包含：

```json
{ "type": "image_generation", "output_format": "png" }
```

- 不会被错误转换成：

```json
{ "type": "function", "name": "image_generation" }
```

### 7.3 响应解析

- 非流式响应能从 `response.output` 提取图片项
- 流式响应能从 `response.output_item.done` 提取图片项
- 同一响应多个图片项时能全部保存

### 7.4 文件与 artifact

- 标准 base64 能成功保存为 `.png`
- 非标准 base64 会报错
- `data:` URL 会报错
- call id 会被清洗，不能目录穿越
- artifact store 能写入 `blob_id` 和摘要记录

### 7.5 回归保护

- 现有 function tool 调用链不受影响
- 现有 `response_output_items` replay 不受影响
- 非图片 Codex 响应不受影响

## 8. 实施顺序建议

建议按下面顺序做：

1. 先补 provider/model capability 配置与解析。
2. 再补 Codex native tool 注入。
3. 然后补 image output item 提取与 base64 保存。
4. 最后接 artifact store、用户提示和测试。

这样可以把风险拆开：

- 先解决“什么时候暴露”
- 再解决“怎么请求”
- 最后解决“怎么保存”

## 9. 结论

本功能在当前仓库里可行，但不适合走“把它当本地 function tool 注册”的路线。  
更稳妥的方案是：

- 把 `image_generation` 当成 Codex 专属 native tool 注入请求
- 用 provider/model 级能力控制启用
- 复用当前 `response_output_items` 保留链路
- 在 chat/runtime 层完成 base64 解码、文件落盘和 artifact 记录

如果后续要进入实现阶段，建议先从：

- provider `model_capabilities`
- `chat_core.go` / `gateway_client.go` 的 native tool 注入

这两块开始。
