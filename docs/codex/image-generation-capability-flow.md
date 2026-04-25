# Codex Image Generation Capability Flow

本文说明 `model_capabilities.native_tools.image_generation` 及其配套
`input_modalities` 对 Codex 请求构造、模型可见工具、响应处理和图片落盘的影响。

## 相关参数

配置位于 provider 的 `model_capabilities` 下：

```yaml
model_capabilities:
  "*":
    input_modalities:
      - text
    native_tools:
      image_generation: false
  gpt-5.4-mini:
    input_modalities:
      - text
      - image
    native_tools:
      image_generation: true
```

字段含义：

- `native_tools.image_generation`: 表示该 provider/model 组合允许向 Codex Responses 请求暴露原生 `image_generation` tool。
- `input_modalities`: 必须同时包含 `text` 和 `image`，否则即使 `image_generation: true`，本地也不会注入图片生成工具。
- `"*"`: 通配能力。精确 model 没命中时会回退到这里。

能力解析逻辑在 `backend/internal/llm/model_capability.go`：

1. 先按请求 model 做精确匹配。
2. 精确匹配不存在时，回退到 `"*"`。
3. 如果 `"*"` 是 text-only 且 `image_generation: false`，该 model 就不会获得图片生成能力。

## 开关判定

Codex 图片工具的总开关在 `backend/internal/llm/codex_image_generation.go` 的
`CodexImageGenerationEnabled`：

1. `protocol` 必须是 `codex`。
2. `ResolveModelCapabilitySpec(model, modelCapabilities)` 必须能解析到能力配置。
3. `capability.NativeTools.ImageGeneration` 必须为 `true`。
4. `capability.InputModalities` 必须同时包含 `text` 和 `image`。

任一条件不满足，返回 `false`。

## 请求构造影响

请求工具列表由 `BuildToolDefinitionsForRequest` 生成。

当 `CodexImageGenerationEnabled(...) == true` 时，会在已有本地 function tools
后追加一个原生工具：

```json
{
  "type": "image_generation",
  "output_format": "png"
}
```

这就是模型在请求 payload 里能看到的能力。模型不会只因为 skill 文本提到
image generation 就获得该工具；必须由本地 runtime 在 `tools` 数组中显式注入。

当开关为 `false` 时：

- `tools` 里仍可能包含普通 function tools，例如 `execute_shell_command`、`apply_patch`。
- `tools` 里不会包含 `{"type":"image_generation"}`。
- 模型无法调用原生 `image_generation_call`，只能选择普通工具或文本回答。

主要入口：

- `backend/cmd/aicli/commands/chat_provider_turn.go`: CLI chat turn 构造 provider config。
- `backend/internal/llm/provider.go`: ProviderWrapper 路径构造请求。
- `backend/internal/llm/gateway_client.go`: GatewayClient 路径构造请求。

## 响应与图片保存流程

如果模型调用了原生图片工具，Codex Responses 响应中会出现：

```json
{
  "type": "image_generation_call",
  "id": "...",
  "status": "...",
  "revised_prompt": "...",
  "result": "<base64 png>"
}
```

保存逻辑由 `ProcessCodexAssistantImageGeneration` 处理：

1. 从 assistant message 的 `response_output_items` 或 reasoning metadata 中提取 output items。
2. 找到 `type == "image_generation_call"` 的 item。
3. 读取 `result` 字段中的 base64 图片数据。
4. 写入 `generated_image_output_dir` 指定目录，默认扩展名为 `.png`。
5. 计算 `sha256` 和 `byte_count`。
6. 从 replay metadata 中移除原始 `result`，避免后续会话携带大体积 base64。
7. 在 assistant message metadata 中写入 `generated_images`。
8. 如果 assistant content 为空，生成一条图片保存路径摘要。

`generated_image_output_dir` 不是模型可见的 native tool。它是本地 runtime metadata，
用于告诉响应处理器把返回的图片数据保存到哪里。

## 当前会话结论

测试会话：

- Session: `session_20260425164420_dht7QdNF`
- Provider/model: `codex_fox` + `gpt-5.4-mini`
- HTTP artifacts: `backend/chat-logs/20260425_164420/runtime-http`

检查结果：

- 请求里有 `tool_choice: "auto"` 和 `tools`，工具数量为 26。
- 请求里没有 `{"type":"image_generation"}`。
- 响应里没有 `image_generation_call`。
- 响应里出现了普通 function tool，例如 `execute_shell_command`。
- `tool_usage.image_gen.total_tokens` 为 0。
- 本地图片输出目录不存在：
  `C:\Users\vince\AppData\Local\Temp\ai-agent-runtime\generated-images\session_20260425164420_dht7QdNF`

因此这次没有返回图片数据，原因不是模型拿到了图片工具但没有选择，而是请求 payload
没有把原生 `image_generation` tool 暴露给模型。

## 配置差异

当前主配置 `backend/configs/config.yaml` 中，`codex_fox` 已包含
`gpt-5.4-mini` 的图片能力：

```yaml
gpt-5.4-mini:
  input_modalities:
    - text
    - image
  native_tools:
    image_generation: true
```

但 `backend/configs/config.runtime.snapshot.yaml` 中的 `codex_fox` 仍只声明了
`"*"` 和 `gpt-5.4`，没有 `gpt-5.4-mini`。如果运行时加载的是 snapshot，
`gpt-5.4-mini` 会回退到 `"*"`：

```yaml
"*":
  input_modalities:
    - text
  native_tools:
    image_generation: false
```

这种情况下，本地会正确地禁止注入 `image_generation`，最终表现就是：
请求有普通工具，但没有图片生成工具。

## 排障清单

遇到“生成图片却没有图片文件”时，按下面顺序查：

1. 查请求 payload 的 `tools` 数组是否有 `{"type":"image_generation"}`。
2. 查响应是否有 `image_generation_call`。
3. 查响应 `tool_usage.image_gen.total_tokens` 是否大于 0。
4. 查 `generated_image_output_dir` 是否存在，以及目录下是否有 `.png`。
5. 查实际加载的 provider 配置是否包含精确 model 能力。
6. 如果精确 model 不存在，检查 `"*"` 是否把图片能力关闭。
7. 对比 `config.yaml` 和 `config.runtime.snapshot.yaml`，确认运行时使用的配置没有落后。

## 行为总结

`native_tools.image_generation` 是“是否允许本地 runtime 暴露 Codex 原生图片工具”的
能力声明。它不直接生成图片，也不直接保存图片。

真正影响链路如下：

```text
provider model_capabilities
  -> ResolveModelCapabilitySpec
  -> CodexImageGenerationEnabled
  -> BuildToolDefinitionsForRequest
  -> request.tools includes image_generation
  -> model may emit image_generation_call
  -> ProcessCodexAssistantImageGeneration
  -> save png and attach generated_images metadata
```

只要前半段没有注入 native tool，后半段保存逻辑就不会被触发。
