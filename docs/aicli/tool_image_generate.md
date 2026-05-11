# 图片生成工具 (openai_image_generate)

## 概述

`openai_image_generate` 是 ai-agent-runtime 内置的图片生成门面工具，支持两类图片生成后端：

1. OpenAI 兼容的 `/v1/images/generations` 端点（Path A）。
2. Codex Responses API 的原生 `image_generation` native tool（Path B）。

chat 内调用时会保存到当前会话的 `generated-images` 目录；`aicli image` 子命令默认保存到当前目录下的 `generated-images`，也可用 `--output-dir` 覆盖。

系统中存在 **两条独立的图片生成路径**：

| 路径 | 入口 | 适用场景 |
|------|------|----------|
| **Path A：工具调用** | `openai_image_generate` → `/v1/images/generations` | 所有兼容 `/v1/images/generations` 端点的 provider |
| **Path B：Codex 原生** | `openai_image_generate` → Codex `image_generation` native tool，或 chat 自动注入 native tool | 仅 Codex 协议且模型支持 `image_generation` 能力时 |

默认策略：

- `path=auto`：优先 Path A；如果找不到 `images_generations_api: true` 的 provider/model，会回退 Path B。
- `path=images_generations_api` / `path=api`：强制 Path A。
- `path=codex_native` / `path=native`：强制 Path B。

也就是说，不指定 `--path` 时并不是固定走 `/v1/images/generations`，而是根据配置自动推断：

| 调用方式 | 配置命中 | 实际路径 |
|----------|----------|----------|
| `aicli image "prompt"` | 找到任意 `images_generations_api: true` 的 provider/model | Path A |
| `aicli image "prompt"` | 找不到 Path A，但找到 Codex `image_generation: true` 且输入模态含 `text`、`image` 的 provider/model | Path B |
| `aicli image --provider CODEX_04 --model gpt-5.4 "prompt"` | 该 provider/model 不支持 Path A，但支持 Path B | Path B |
| `aicli image --provider OPENAI_IMAGE "prompt"` | 该 provider/model 支持 Path A | Path A |
| Path A 和 Path B 都可用，且未指定 `--path` | 默认优先 Path A | Path A |

自动推断完全依赖 provider 配置，不根据 provider 名称或 model 名称做猜测。要让 `--provider CODEX_04 --model gpt-5.4` 自动走 Path B，配置里必须正确声明 `protocol: codex`、`native_tools.image_generation: true`，并且 `input_modalities` 同时包含 `text` 和 `image`。

chat 自动工具曝光仍保持互斥：当 Codex 原生图片生成启用时，普通对话的工具列表会移除 `openai_image_generate`，避免模型同时看到 Path A function tool 和 Path B native tool。显式 `/call openai_image_generate ...` 或 `aicli image --path native ...` 属于直接调用，不受普通曝光互斥影响。

---

## 使用方法

### 独立子命令（推荐）

`aicli image` 是 `openai_image_generate` 的命令行薄封装，适合脚本、调试和不需要进入 chat 的图片生成任务：

```
aicli image "一只在月光下奔跑的猫"
aicli image --provider SENSENOVA_IMAGE "海边日落照片"
aicli image --provider SENSENOVA_IMAGE --output-dir ./generated-images "生成一张产品海报"
aicli image --provider SENSENOVA_IMAGE --json "生成一张壁纸"
aicli image --provider SENSENOVA_IMAGE --debug --json "生成一张壁纸"
aicli image --provider CODEX_04 --model gpt-5.4 "生成一张未来城市海报"
aicli image --provider CODEX_04 --model gpt-5.4 --path codex_native "生成一张未来城市海报"
```

命令会直接复用 `openai_image_generate` 的 provider 选择、failover、请求重试、图片保存和 metadata 结构。默认输出目录是当前工作目录下的 `generated-images`。如果不指定 `--path`，命令使用 `auto` 推断：优先尝试 Path A；当当前 provider/model 或全局候选不满足 `images_generations_api: true` 时，再尝试 Path B。Path B 会调用 Codex Responses API `/v1/responses`，请求中注入 `{"type":"image_generation"}` native tool，并把响应中的 `image_generation_call.result` 保存到本地。

### Chat 内直接调用工具

进入 `aicli chat` 后，可以用 `/call` 或 `/tool` 直接调用同一个工具：

```
/call openai_image_generate 一只在月光下奔跑的猫
/call openai_image_generate {"prompt":"一只在月光下奔跑的猫"}
/tool openai_image_generate 一只在月光下奔跑的猫
```

### 指定 provider

```
aicli image --provider SENSENOVA_IMAGE "测试"
/call openai_image_generate {"prompt":"测试", "provider":"SENSENOVA_IMAGE"}
```

### 指定 model

```
aicli image --model sensenova-u1-fast "测试"
/call openai_image_generate {"prompt":"测试", "model":"sensenova-u1-fast"}
```

### 指定 size（非 GPT Image 模型需注意尺寸兼容性）

```
aicli image --provider SENSENOVA_IMAGE --size 2752x1536 "测试"
/call openai_image_generate {"prompt":"测试", "provider":"SENSENOVA_IMAGE", "size":"2752x1536"}
```

### 完整参数

| 参数 | 类型 | 必填 | 默认值 | 说明 |
|------|------|------|--------|------|
| `prompt` | string | 是 | — | 图片提示词 |
| `model` | string | 否 | `gpt-image-2` | 图像模型名称 |
| `provider` | string | 否 | 自动选择 | Provider 名称（如 `OPENAI_IMAGE`、`SENSENOVA_IMAGE`） |
| `path` | string | 否 | `auto` | 生成路径：`auto`、`images_generations_api`/`api`、`codex_native`/`native` |
| `n` | integer | 否 | `1` | 单次生成图片数量（1~max_n） |
| `size` | string | 否 | GPT Image: `1024x1024`；其他: 空 | 图片尺寸 |
| `quality` | string | 否 | GPT Image: `medium`；其他: 空 | 图片质量 (`low`/`medium`/`high`/`auto`) |
| `background` | string | 否 | 空 | 背景模式 (`transparent`/`opaque`/`auto`) |
| `output_format` | string | 否 | GPT Image: `png`；其他: 空 | 输出格式 (`png`/`jpeg`/`webp`) |
| `output_compression` | integer | 否 | 空 | 压缩级别（0~100） |

> **重要**：`size`、`quality`、`output_format` 的默认值仅对 GPT Image 模型（`gpt-image-*`）自动填充。非 GPT Image 模型不会自动设置这些值，避免向不支持这些参数的 API 发送不兼容的请求。

### `aicli image` 参数映射

| CLI 参数 | 对应 tool 参数 | 说明 |
|----------|----------------|------|
| `[prompt]` / `--prompt/-p` | `prompt` | `--prompt` 优先；未指定时拼接位置参数 |
| `--provider` | `provider` | 限定图片生成 provider |
| `--model/-m` | `model` | 指定图像模型 |
| `--path` | `path` | 选择生成路径：`auto`、`images_generations_api`/`api`、`codex_native`/`native` |
| `--n` | `n` | `0` 表示不显式传入，由工具使用默认值 |
| `--size` | `size` | 图片尺寸 |
| `--quality` | `quality` | 图片质量 |
| `--background` | `background` | 背景模式 |
| `--output-format` | `output_format` | 输出格式 |
| `--output-compression` | `output_compression` | 只有显式传入时才会发送；`--output-compression 0` 也会保留 |
| `--output-dir` | 上下文输出目录 | 不发送给上游 API，用于控制本地保存目录 |
| `--timeout` | 命令 context timeout | 秒；`0` 表示不额外设置命令层 timeout |
| `--debug` | 调试输出 | 向 stderr 输出处理过程，不污染 JSON stdout |
| `--output text|json` / `--json` | 命令输出格式 | JSON 输出包含 `metadata.generated_images` |

### 调试与日志

`aicli image` 支持两类观察手段：

1. **命令级调试**：使用 `--debug`，会把关键步骤输出到 stderr。
2. **全局日志文件**：使用 `--logfile/-l`，会按 aicli 的全局日志配置写程序日志。

推荐调试命令：

```powershell
.\aicli.exe image --provider SENSENOVA_IMAGE --debug --json --output-dir ../tmp/aicli-image-debug "测试图片：蓝色圆形图标" 1> image-result.json 2> image-debug.log
```

`image-result.json` 只包含 JSON 结果，`image-debug.log` 记录处理过程。`--debug` 会透传到 `openai_image_generate` 工具内部，因此除了命令入口日志，还能看到 provider/model 选择、候选列表、API 调用 attempt、响应 item 数量和保存方式。调试输出不会打印 API key，也不会打印完整 prompt；对 URL 图片响应，只记录 `source=url`，不打印可能带签名的图片 URL。

典型调试输出：

```text
[image-debug] start prompt_chars=15 output_dir="E:\\projects\\ai\\ai-agent-runtime\\tmp\\aicli-image-debug"
[image-debug] params prompt_chars=15 provider="SENSENOVA_IMAGE" debug="true"
[image-debug] calling tool=openai_image_generate debug=true
[image-debug/tool] request prompt_chars=15 explicit_provider=true provider="SENSENOVA_IMAGE" explicit_model=false model=""
[image-debug/tool] select requested_provider="SENSENOVA_IMAGE" requested_model="" default_model="gpt-image-2" explicit_provider=true explicit_model=false
[image-debug/tool] candidates count=1 list=SENSENOVA_IMAGE/sensenova-u1-fast
[image-debug/tool] attempt=1/1 provider="SENSENOVA_IMAGE" model="sensenova-u1-fast" api_url="https://token.sensenova.cn/v1/images/generations" timeout=5m0s proxy=false
[image-debug/tool] attempt=1 request n=1 size="" quality="" background="" output_format="" output_compression=<nil>
[image-debug/tool] attempt=1 api_call provider="SENSENOVA_IMAGE" model="sensenova-u1-fast"
[image-debug/tool] attempt=1 api_response created=0 items=1
[image-debug/tool] attempt=1 save index=1 source=url output_format=""
[image-debug/tool] attempt=1 saved index=1 path="E:\\projects\\ai\\ai-agent-runtime\\tmp\\aicli-image-debug\\image_1.png" bytes=1491558 mime="image/png" sha256=...
[image-debug/tool] success provider="SENSENOVA_IMAGE" model="sensenova-u1-fast" generated_count=1 output_dir="E:\\projects\\ai\\ai-agent-runtime\\tmp\\aicli-image-debug"
[image-debug] result success=true provider="SENSENOVA_IMAGE" model="sensenova-u1-fast" generated_count=1
[image-debug] saved_path="E:\\projects\\ai\\ai-agent-runtime\\tmp\\aicli-image-debug\\image_1.png"
```

---

## 架构原理

### 整体流程

```
用户调用 → buildRequest() → resolve path
                        ↓
              path=api / Path A 可用:
              selectProviders(images_generations_api)
                        ↓
              applyRuntimeDefaults()
              NormalizeGenerateRequest()
              Validate()
              Client.Generate(/v1/images/generations)
                        ↓
              保存图片 (b64_json 或 url)
                        ↓
              返回 ToolResult

              path=native / Path A 不可用:
              selectCodexNativeProviders(image_generation)
                        ↓
              BuildToolDefinitionsForRequestWithImageOptions()
              Provider.Call(/v1/responses, tool_choice=image_generation)
                        ↓
              ProcessCodexAssistantImageGenerationWithOptions()
                        ↓
              返回同构 ToolResult
```

### Provider 选择逻辑

`selectProviders` 根据用户是否显式指定 `provider` 和 `model` 构建不同的查询 hint：

1. **显式指定 provider**：仅查找该 provider 下匹配的 model
2. **显式指定 model**：在所有 provider 中查找支持该 model 的
3. **均未指定**：先尝试默认 model，再尝试空 hint（匹配所有可用 provider）

选择结果是一个有序列表，第一个是首选，后续为 failover 候选。

Path B 使用独立的 provider 选择逻辑：provider 必须启用、协议必须是 `codex`，并且目标 model 的能力必须满足 `image_generation: true` 且 `input_modalities` 同时包含 `text` 和 `image`。`path=auto` 会先运行 Path A 的选择逻辑；只有 Path A 没有候选时，才运行 Path B 的选择逻辑。因此，显式指定 Codex provider/model 时，只要该组合没有 `images_generations_api: true`，但有 `image_generation: true`，就会自动推断为 Path B，不需要额外加 `--path codex_native`。

### Provider 匹配条件

一个 provider/model 对必须同时满足以下条件才会被选中：

1. provider 已启用（`enabled: true`）
2. model 在 provider 的 `model_capabilities` 中配置了 `images_generations_api: true`

关键配置项对照：

```yaml
# ✅ 正确：enable images_generations_api
model_capabilities:
  gpt-image-2:
    native_tools:
      images_generations_api: true    # Path A 需要这个

# ❌ 错误：image_generation ≠ images_generations_api
model_capabilities:
  gpt-5.4:
    native_tools:
      image_generation: true          # 这是 Path B (Codex native) 用的
```

| 字段 | 作用 | 路径 |
|------|------|------|
| `images_generations_api: true` | 标识该 model 支持 `/v1/images/generations` 端点 | Path A |
| `image_generation: true` | 标识该 model 支持 Codex 原生 `image_generation` tool；还需要 `input_modalities: [text, image]` | Path B |

### Failover 机制

当存在多个候选 provider/model 时，按顺序尝试：

1. 对第一个候选发起请求
2. 如果成功，立即返回
3. 如果失败（非 context 取消），尝试下一个候选
4. 所有候选都失败则返回最后一个错误

### 请求重试

`Client.Generate` 内置最多 3 次重试，触发条件：

- HTTP 429（速率限制）
- HTTP 5xx（服务器错误）
- 网络超时、连接重置等临时错误

重试延迟策略：优先使用响应中的 `Retry-After` 头，否则指数退避（500ms、1s、2s）。

---

## 响应格式与图片保存

### 两种响应格式

不同 provider 的图片生成 API 返回格式不同：

| 格式 | 说明 | 适用 provider |
|------|------|---------------|
| `b64_json` | Base64 编码的图片数据 | OPENAI_IMAGE（GPT Image 系列） |
| `url` | 可下载的图片 URL | SENSENOVA_IMAGE 等非 GPT Image provider |

工具会自动检测响应格式并选择对应的保存方式：

- **`b64_json`**：解码 Base64 → 直接写入文件
- **`url`**：HTTP 下载图片 → 写入文件（自动推断 Content-Type 格式）

### 文件命名

- 首次保存：`image_1.png`、`image_2.png`...
- 冲突时追加时间戳：`image_1_1713167890000000000_0.png`

### `aicli image --json` 输出

JSON 输出保留工具结果和图片落盘 metadata，适合脚本消费：

```json
{
  "success": true,
  "output": "Generated image saved to ...",
  "output_dir": "E:\\projects\\ai\\ai-agent-runtime\\tmp\\aicli-image-live-test",
  "metadata": {
    "provider": "SENSENOVA_IMAGE",
    "model": "sensenova-u1-fast",
    "generated_count": 1,
    "generated_images": [
      {
        "id": "image_1",
        "status": "completed",
        "mime_type": "image/png",
        "saved_path": "E:\\projects\\ai\\ai-agent-runtime\\tmp\\aicli-image-live-test\\image_1.png",
        "sha256": "..."
      }
    ]
  },
  "images": [
    {
      "id": "image_1",
      "status": "completed",
      "saved_path": "E:\\projects\\ai\\ai-agent-runtime\\tmp\\aicli-image-live-test\\image_1.png"
    }
  ]
}
```

---

## GPT Image 模型特殊验证

对于 `gpt-image-*` 模型，有额外的校验规则：

### gpt-image-1（旧版）

- `size` 仅允许：`1024x1024`、`1536x1024`、`1024x1536`、`auto`

### gpt-image-2

- `size` 可以是 `auto` 或 `WIDTHxHEIGHT` 格式
- 单边最大 3840px
- 宽高都必须是 16 的倍数
- 长短边比例不超过 3:1
- 总像素数范围：655,360 ~ 8,294,400
- 不支持 `transparent` 背景

---

## 配置要点

### 1. Provider 配置

在 `config.yaml` 的 `providers.items` 中配置图片生成 provider。以 SENSENOVA_IMAGE 为例：

```yaml
SENSENOVA_IMAGE:
  api_key: ${SENSENOVA_API_KEY:-}
  api_path: /v1/images/generations
  base_url: https://token.sensenova.cn
  default_model: sensenova-u1-fast
  enabled: true
  forward_url: /v1/images/generations
  model_capabilities:
    '*':
      native_tools:
        image_generation: false
        images_generations_api: false
    sensenova-u1-fast:
      native_tools:
        image_generation: false
        images_generations_api: true    # 关键：必须是 true
  protocol: openai
  supported_models:
    - sensenova-u1-fast
  timeout: 300s
```

**关键字段说明**：

| 字段 | 说明 |
|------|------|
| `api_path` / `forward_url` | 请求路径，通常为 `/v1/images/generations` |
| `base_url` | Provider API 地址 |
| `default_model` | 默认使用的模型 |
| `model_capabilities.*.native_tools.images_generations_api` | 必须为 `true` 才能被工具发现 |
| `protocol` | 通信协议，非 Codex 类 provider 使用 `openai` |

### 2. 环境变量

在 `~/.aicli/.env` 中配置 API Key：

```
SENSENOVA_API_KEY=your-api-key-here
CODEX_04_API_KEYS=your-openai-key-here
```

缺少 API Key 会导致 `no image generations provider configured` 错误。

### 3. 运行时默认值

在 `runtime.yaml`（或 `RuntimeConfig`）中可配置默认值：

```yaml
images:
  cacheMaxAge: 1h
  generations:
    default_model: gpt-image-2
    default_size: 1024x1024
    default_quality: medium
    default_output_format: png
    request_timeout: 5m
    max_n: 4
```

| 配置项 | 默认值 | 说明 |
|--------|--------|------|
| `default_model` | `gpt-image-2` | 未指定 model 时的默认模型 |
| `default_size` | `1024x1024` | GPT Image 模型的默认尺寸 |
| `default_quality` | `medium` | GPT Image 模型的默认质量 |
| `default_output_format` | `png` | GPT Image 模型的默认输出格式 |
| `request_timeout` | `5m` | 请求超时时间 |
| `max_n` | `4` | 单次请求最大图片数量 |

> 这些默认值仅对 GPT Image 模型生效。非 GPT Image 模型的 `size`、`quality`、`output_format` 不会自动填充，由上游 API 自行决定默认值。

### 4. 已有 Provider 配置参考

项目 `config.yaml` 中预配置的图片生成 provider：

| Provider 名称 | base_url | 默认模型 | 协议 |
|---------------|----------|----------|------|
| `OPENAI_IMAGE` | `${CODEX_04_BASE_URL}` | `gpt-image-2` | openai |
| `SENSENOVA_IMAGE` | `https://token.sensenova.cn` | `sensenova-u1-fast` | openai |

---

## 实际验证记录

本仓库在 Windows PowerShell 下已用真实 provider 跑通过 `aicli image` 主路径：

```powershell
go run ./cmd/aicli image --provider SENSENOVA_IMAGE --output-dir ../tmp/aicli-image-live-test --timeout 180 --json "测试图片：一个简洁的蓝色圆形图标，白色背景"
```

验证结果：

| 项目 | 结果 |
|------|------|
| 命令结果 | `success: true` |
| Provider | `SENSENOVA_IMAGE` |
| Model | `sensenova-u1-fast` |
| 保存文件 | `tmp/aicli-image-live-test/image_1.png` |
| 文件大小 | `1491558` bytes |
| 图片尺寸 | `2752x1536` |
| MIME | `image/png` |
| SHA256 | `8b14680c5afc3fc31b7cb5d67c3142c5cc4778070850d71f39026f2946af43b7` |

同时已验证编译后的二进制支持该子命令：

```powershell
go build -o aicli.exe ./cmd/aicli
.\aicli.exe image --help
```

---

## 常见错误与排查

### `no image generations provider configured`

**原因**：没有找到任何 `images_generations_api: true` 的 provider/model 对。

排查步骤：
1. 确认 provider 的 `enabled: true`
2. 确认目标 model 在 `model_capabilities` 中设置了 `images_generations_api: true`
3. 确认环境变量中的 API Key 已配置且非空

### `HTTP 400: field Size invalid`

**原因**：非 GPT Image 模型收到了不兼容的 `size` 值。

解决：显式指定该 provider 支持的 `size`，或确保 `images_generations_api` 已正确配置（工具会根据模型类型自动跳过默认值）。

### `image generation response item 0 contained neither b64_json nor url`

**原因**：API 返回的数据项既没有 `b64_json` 也没有 `url` 字段。

排查：检查 provider 的响应格式是否为标准 OpenAI 格式。

### `HTTP 429: model_cooldown` via Codex provider

**原因**：这通常来自 **Path B**（Codex 原生图片生成），而非 `openai_image_generate` 工具。Codex provider 的 API Key 可能被限流。

解决：使用 Path A 显式指定图片专用 provider（如 `OPENAI_IMAGE` 或 `SENSENOVA_IMAGE`）。

---

## Codex 原生图片生成（Path B）

当使用 Codex 协议的模型（如 `gpt-5.4`）且模型能力中 `image_generation: true` 时，系统会启用 Codex 原生图片生成：

- 工具名称：`image_generation`（非 `openai_image_generate`）
- `openai_image_generate` 工具会自动从工具列表中移除
- 图片由 Codex Response API 直接返回，格式为 `b64_json`
- 判断条件：`protocol == "codex"` && `image_generation == true` && 输入模态包含 `text` 和 `image`
- 直接调用入口：`aicli image --path codex_native --provider <codex-provider> --model <model> "prompt"`，或 `/call openai_image_generate {"prompt":"...","path":"codex_native","provider":"...","model":"..."}`
- 请求约束：直接 Path B 会向 Codex 请求设置 `tool_choice={"type":"image_generation"}`，避免模型只返回文本。
- 参数映射：`size`、`quality`、`background`、`output_format`、`output_compression` 会写入 native tool 定义；`n>1` 会写入用户提示词约束模型生成多张图，因为 Codex native tool 本身不是 `/v1/images/generations` 的 `n` 参数。

```yaml
# Codex 原生图片生成的模型配置示例
gpt-5.4:
  input_modalities:
    - text
    - image
  native_tools:
    image_generation: true      # Path B 用这个
    # images_generations_api 不需要设置
```

---

## 源码参考

| 模块 | 文件路径 | 职责 |
|------|----------|------|
| CLI 子命令 | `backend/cmd/aicli/commands/image.go` | `aicli image` 参数解析、输出目录、JSON/text 渲染 |
| CLI 注册 | `backend/cmd/aicli/main.go` | 注册 `image` / `img` root 子命令 |
| CLI 测试 | `backend/cmd/aicli/commands/image_test.go` | 子命令请求、参数透传、图片落盘回归测试 |
| 工具主体 | `backend/internal/toolkit/tools/openai_image_generate.go` | 参数解析、provider 选择、failover、图片保存 |
| 请求/响应类型 | `backend/internal/llm/imagegen/request.go` | 请求结构、响应结构、Normalize、Validate |
| HTTP 客户端 | `backend/internal/llm/imagegen/client.go` | API 调用、重试、错误解析 |
| 图片持久化 | `backend/internal/llm/imagegen/persist.go` | Base64/URL 图片保存、SHA256 计算 |
| Provider 选择 | `backend/internal/agentconfig/images.go` | Provider 过滤、候选模型排序 |
| Codex 原生 | `backend/internal/llm/codex_image_generation.go` | Codex native tool 处理 |
| 运行时配置 | `backend/internal/config/manager.go` | 默认值定义 |
| 工具名称 | `backend/internal/toolnames/image_tools.go` | 工具名常量 |
