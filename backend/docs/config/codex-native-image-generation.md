# Codex 原生 `image_generation` 配置说明

## 生效条件

Codex 原生图片工具只由**模型能力**决定。只有以下条件同时满足时，runtime 才会向 Codex Responses 请求追加：

```json
{"type":"image_generation","output_format":"png"}
```

1. Provider 的 `protocol` 为 `codex`。
2. 当前模型命中的 `model_capabilities` 包含：
   - `native_tools.image_generation: true`
   - `input_modalities` 同时包含 `text` 和 `image`

能力是模型级别的，因此同一个 provider 可以只为支持图片生成的模型注入原生工具，不会影响该 provider 下的其他模型。

## 配置示例

`model_capabilities` 写在 `providers.items.<name>` 下：

```yaml
providers:
  items:
    CODEX_OFFICIAL:
      enabled: true
      protocol: codex
      base_url: https://api.openai.com
      api_path: /v1/responses
      forward_url: /v1/responses
      default_model: gpt-6.1-terra
      model_capabilities:
        "*":
          input_modalities:
            - text
          native_tools:
            image_generation: false
        gpt-6.1-terra:
          input_modalities:
            - text
            - image
          native_tools:
            image_generation: true
        gpt-5.5:
          input_modalities:
            - text
          native_tools:
            image_generation: false
```

如果模型能力直接写在 `"*"` 下，则未命中特定模型时使用该通配配置；具体模型配置优先于通配配置。

## 字段语义

| 字段 | 层级 | 作用 |
|------|------|------|
| `model_capabilities.<model>.native_tools.image_generation` | model | 声明模型是否具备原生图片生成能力 |
| `model_capabilities.<model>.input_modalities` | model | 必须同时含 `text` 与 `image` 才会注入 |
| `model_capabilities.<model>.native_tools.images_generations_api` | model | Path A：`/v1/images/generations`，与 Codex 原生工具无关 |

注意：

- `image_generation` 与 `images_generations_api` 是两条不同的图片生成路径。
- 只配置 `images_generations_api` 不会向 Codex Responses 请求注入 `image_generation`。
- 只配置 `native_tools.image_generation: true`，但缺少 `text` 或 `image` 输入模态，也不会注入。
- 不支持图片生成的模型不会因为同属一个 provider 而获得该工具。

## 判定与请求路径

代码侧统一走模型能力判定：

```text
protocol == codex
  && ModelCapabilityHasTextImageNativeGeneration(spec)
```

相关入口：

- `agentconfig.ProviderHasCodexNativeImageGeneration(provider, model)`
- `llm.CodexImageGenerationEnabled(protocol, model, capabilities)`
- `llm.CodexNativeImageGenerationEnabled(provider, model)`
- `llm.BuildToolDefinitionsForRequest(...)`

当原生图片工具启用时，普通对话工具列表中的 `openai_image_generate` 会被隐藏，避免模型同时看到 Path A function tool 与 Codex native tool。

## 排障

请求里没有 `{"type":"image_generation"}` 时，按顺序检查：

1. 当前 provider 的协议是否为 `codex`。
2. 请求使用的实际 model 是否命中 `model_capabilities`，或回退到 `"*"`。
3. 命中的 capability 是否设置 `native_tools.image_generation: true`。
4. `input_modalities` 是否同时包含 `text` 和 `image`。
5. 运行时加载的配置或 snapshot 是否已经刷新。

如果需要关闭某个模型的原生图片生成，应将该模型的 `native_tools.image_generation` 设为 `false`，或移除 `image` 输入模态。
