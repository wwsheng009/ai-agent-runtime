# `enable_image_generation` 配置说明

## 背景

部分模型卡片会声明：

```yaml
model_capabilities:
  gpt-5.4:
    input_modalities: [text, image]
    native_tools:
      image_generation: true
```

这只表示 **模型能力** 支持 Codex Responses 原生 `image_generation` tool。

旧行为会在看到该能力后，自动向 Codex 请求注入：

```json
{"type":"image_generation","output_format":"png"}
```

但不少第三方 Codex 兼容站点会拒绝这个 tool，导致请求失败。

因此 runtime 增加了 **provider 级 opt-in** 开关：

```yaml
enable_image_generation: true
```

默认关闭（字段缺失或 `false` 都视为关闭）。

## 生效条件

Codex 原生图片生成仅在以下条件 **同时** 成立时启用：

1. Provider `protocol` 为 `codex`
2. Provider 显式设置 `enable_image_generation: true`
3. 目标 model 的 `model_capabilities` 满足：
   - `native_tools.image_generation: true`
   - `input_modalities` 同时包含 `text` 和 `image`

任一条件不满足时：

- chat / gateway / provider wrapper **不会** 自动注入 `{"type":"image_generation"}`
- Path B（`aicli image --path codex_native` / 自动回退 Path B）也不会把该 provider/model 当作可用候选

## 配置位置

写在 `providers.items.<name>` 下，与 `protocol`、`model_capabilities` 同级：

```yaml
providers:
  items:
    CODEX_OFFICIAL:
      enabled: true
      protocol: codex
      base_url: https://api.openai.com
      api_path: /v1/responses
      forward_url: /v1/responses
      default_model: gpt-5.4
      # 关键：provider 级 opt-in；默认关闭
      enable_image_generation: true
      model_capabilities:
        "*":
          input_modalities:
            - text
          native_tools:
            image_generation: false
        gpt-5.4:
          input_modalities:
            - text
            - image
          native_tools:
            image_generation: true
```

## 字段语义

| 字段 | 层级 | 默认 | 作用 |
|------|------|------|------|
| `enable_image_generation` | provider | 关闭（`nil`/`false`） | 是否允许该 provider 使用 Codex 原生 `image_generation` |
| `model_capabilities.*.native_tools.image_generation` | model | 通常 false | 模型是否具备原生图片生成能力 |
| `model_capabilities.*.input_modalities` | model | 依模型而定 | 必须同时含 `text` 与 `image` 才会注入 |
| `model_capabilities.*.native_tools.images_generations_api` | model | false | Path A：`/v1/images/generations`，与 Path B 无关 |

注意：

- `image_generation` ≠ `images_generations_api`
- 仅配置 model capability **不会** 自动注入 native tool
- 仅配置 `enable_image_generation: true` 但 model 能力不足，同样不会注入

## 判定入口

代码侧统一走：

```text
Provider.AllowsCodexImageGeneration()
  && protocol == codex
  && ModelCapabilityHasTextImageNativeGeneration(spec)
```

公开 helper：

- `agentconfig.ProviderHasCodexNativeImageGeneration(provider, model)`
- `llm.CodexNativeImageGenerationEnabled(provider, model)`

model-only 判定 `llm.CodexImageGenerationEnabled(...)` 仍然只检查协议 + model capability；
真正构造请求时还要再叠加 provider opt-in。

## 影响面

开启后会影响：

1. **chat 自动注入**  
   Codex chat 请求的 `tools` 可能追加 `{"type":"image_generation"}`。

2. **工具曝光互斥**  
   当原生图片生成启用时，普通对话工具列表会隐藏 `openai_image_generate`，避免模型同时看到 Path A function tool 与 Path B native tool。

3. **Path B 选择**  
   `aicli image` / `/image` / `openai_image_generate` 在 `path=auto|codex_native` 时，只会选择已 opt-in 的 Codex provider。

关闭后（默认）：

- 即使 model card 写了 `image_generation: true`，也不会自动注入 native tool
- 第三方兼容站更安全，不会因未知 tool 被拒
- 仍可继续使用 Path A（`images_generations_api: true` 的图片 provider）

## 推荐实践

| 场景 | 建议 |
|------|------|
| 官方 OpenAI / 确认支持 Codex native image 的站点 | `enable_image_generation: true` |
| 第三方 Codex 兼容中转、未确认是否支持该 tool | 保持缺省关闭 |
| 只想走 `/v1/images/generations` | 配置 Path A provider，不必打开本开关 |
| 调试“为什么没有注入 image_generation” | 先查 provider 是否 opt-in，再查 model capability |

## 持久化

通过 provider 配置更新接口写入时，字段名为 YAML snake_case：

```yaml
enable_image_generation: true
```

对应结构体：

```go
EnableImageGeneration *bool `yaml:"enable_image_generation,omitempty" ...`
```

运行时 `ProviderConfig` JSON 使用 camelCase：

```json
{"enableImageGeneration": true}
```

## 排障

请求里没有 `{"type":"image_generation"}` 时，按顺序检查：

1. provider `protocol` 是否为 `codex`
2. provider 是否写了 `enable_image_generation: true`
3. 实际加载的 config / snapshot 是否包含该字段
4. 当前 model 是否命中 capability（或回退到 `"*"`）
5. capability 是否同时具备：
   - `native_tools.image_generation: true`
   - `input_modalities: [text, image]`

常见误区：

- 只改了 `model_cards.yaml`，但运行时 provider 覆盖未 opt-in
- 只改了 `config.yaml`，但实际加载的是旧的 `config.runtime.snapshot.yaml`
- 把 `images_generations_api` 当成了 Codex native 开关
