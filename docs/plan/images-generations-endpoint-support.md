# `/v1/images/generations` 端点调用支持 – 实施方案

状态: **draft**
作者上下文: 用户希望参考 `C:\Users\vince\.codex\skills\.system\imagegen\scripts\image_gen.py` 的调用方式，在本项目里新增对 OpenAI 兼容 `POST /v1/images/generations` 端点的调用支持。
相关方案:
- `docs/plan/codex-image-generation-runtime-server-and-frontend.md`（前置）
- `docs/plan/codex-image-generation-gaps-followup.md`（前置，已完成）
- `docs/codex/image-generation-capability-flow.md`（背景）

---

## 0. 背景与目标

### 0.1 现状

本项目已有的图片生成路径只覆盖一种：

- **Codex Responses 内联工具**: `@backend/internal/llm/codex_image_generation.go` 在请求里追加 `{"type": "image_generation"}` 工具，让 Codex 协议的模型（如 `gpt-5.4-mini`/`gpt-5.4`）在 Responses 流里直接产出 `image_generation_call` item，结果通过 `ProcessCodexAssistantImageGeneration` 落盘到 `generated-images/<sid>/<sanitized-id>.png`。
- 后端 `@backend/internal/api/skills/handler.go` 暴露 `GET /api/runtime/sessions/{id}/generated-images/{name}`（带 ETag / Range / 可配置 `Cache-Control`）。
- 前端有 `image-placeholder` segment + 真图替换链路。

这条链路只对 **Codex 协议且模型 capability 显式声明 `native_tools.image_generation: true`** 的组合生效，覆盖范围是 `codex_*` provider 的少数模型（见 `backend/configs/config.yaml`）。

### 0.2 缺什么

参考 `image_gen.py` 的做法，OpenAI 官方/兼容端点是 **独立的 REST 路径**：

```http
POST {base_url}/v1/images/generations
Authorization: Bearer {api_key}
Content-Type: application/json

{
  "model": "gpt-image-2",
  "prompt": "...",
  "n": 1,
  "size": "1024x1024",
  "quality": "medium",
  "background": "auto",
  "output_format": "png",
  "output_compression": null,
  "moderation": null
}
```

响应：

```json
{ "data": [{ "b64_json": "iVBORw0KGgo..." }, ...] }
```

它和 Responses 内联 tool 的差异：

| 维度 | Codex Responses 内联 | `/v1/images/generations` REST |
| --- | --- | --- |
| 触发方 | LLM 在对话里自行调用 | 用户/agent 主动发起一次性请求 |
| 适用模型 | Responses 协议的对话模型 | 独立的 image 模型（`gpt-image-1` / `gpt-image-1.5` / `gpt-image-2`） |
| 输入 | 对话+工具 schema | prompt + 结构化参数 |
| 编辑场景 | 不支持 | `/v1/images/edits`（multipart） |
| 配置 | `model_capabilities.<m>.native_tools.image_generation` | 需要独立的 provider/model 路由 |

`backend/configs/config.yaml` 里已经为 `/v1/images/generations` / `/v1/images/edits` / `/v1/images/variations` 配了限流和 `openai_group` 路由（`l. 789-984`），但仓库里没有任何 Go 代码会向这些路径发起请求。本方案就是把这块缺口补齐。

### 0.3 目标

1. 让 agent / aicli / runtime-server 触发的会话能调用一个独立的 image 模型（比如 `gpt-image-2`）通过 `/v1/images/generations` 生成图片，且不依赖对话模型本身是否是 Codex 协议。
2. 落盘格式与现有 Codex 路径完全一致（同一个 `generated-images/<sid>/` 目录，同一份 `metadata.generated_images[]` 结构），让 `@backend/internal/api/skills/handler.go` 的 HTTP 端点和前端占位/真图替换可以零改动复用。
3. 配置层支持选择“独立 image provider”，可以和对话 provider 不同（典型场景：对话用 `codex_fox`，画图用 OpenAI 官方 `gpt-image-2`）。
4. 新增 `/v1/images/edits` 留接口位但不强制实现，本方案标记为 **可选阶段**。

### 0.4 非目标

- 不实现 `/v1/images/variations`。
- 不引入图像缩略图/降采样（参考脚本里 `_downscale_image_bytes` 的能力暂不抄过来）。
- 不做 SSE 进度反馈（OpenAI 该端点本身不流式返回）；前端继续显示一次性 spinner 占位，复用既有 `image-placeholder` 流程。
- 不变更现有 Codex 内联工具的行为；两条路径并存。

---

## 1. 设计总览

```
┌─────────────────────────┐
│ caller                  │   agent loop / aicli skill / API
│  e.g. ImageGenTool.Exec │
└─────────────┬───────────┘
              │ params: {prompt,size,quality,...}
              ▼
┌─────────────────────────┐
│ internal/llm/imagegen   │  ◀── 新增包
│  Client.Generate(req)   │
│  Client.Edit(req)       │ (opt)
└─────────────┬───────────┘
              │ POST /v1/images/generations
              │ Authorization: Bearer …
              ▼
┌─────────────────────────┐
│ provider HTTP transport │  复用 newProviderHTTPClient
│  (openai-compatible)    │
└─────────────┬───────────┘
              │ {data:[{b64_json}]}
              ▼
┌─────────────────────────┐
│ shared persistence      │  复用 codex_image_generation.go 中的
│  saveGeneratedImage()   │  落盘逻辑（抽出 SaveBase64ImageBytes）
└─────────────┬───────────┘
              │ generated-images/<sid>/<id>.png
              ▼
┌─────────────────────────┐
│ HTTP / frontend         │  ✓ 已就绪，无需改动
└─────────────────────────┘
```

核心思路：**新增一个 LLM 子包做 REST 调用 + 抽出公共落盘函数，共享给两条路径**。

---

## 2. 配置层

### 2.1 Provider 复用

`/v1/images/generations` 是 OpenAI 协议的标准端点，直接复用现有 `agentconfig.Provider`（type `openai`）即可，无需引入新 provider 类型。`base_url` + `api_key` 已足够。

但要在 model capability 里增加一个独立标记，区分“对话模型 + 内联工具”和“纯 image 模型 + REST 端点”：

`@backend/internal/agentconfig/config.go`（`NativeToolCapabilities`）扩展：

```go
type NativeToolCapabilities struct {
    ImageGeneration       bool `yaml:"image_generation" ...`           // 现有：Codex 内联
    ImagesGenerationsAPI  bool `yaml:"images_generations_api" ...`     // 新增：可作为 /v1/images/generations 后端
}
```

`config.yaml` 示例（在某个 OpenAI-兼容 provider 下）：

```yaml
providers:
  items:
    openai_image:
      enabled: true
      type: openai
      base_url: https://api.openai.com
      api_key: ${OPENAI_IMAGE_API_KEY}
      default_model: gpt-image-2
      supported_models: [gpt-image-1, gpt-image-1.5, gpt-image-2]
      model_capabilities:
        gpt-image-2:
          input_modalities: [text, image]
          native_tools:
            image_generation: false
            images_generations_api: true
        gpt-image-1.5:
          native_tools:
            images_generations_api: true
```

### 2.2 选择策略

新增 `agentconfig.SelectImagesGenerationsProvider(cfg, hint)`：

1. 如果 `hint.ProviderName != ""`，按名查找；否则
2. 在 `cfg.Providers.Items` 中找第一个 `model_capabilities.<m>.native_tools.images_generations_api == true` 的 provider+model；否则
3. 返回 `ErrNoImagesProvider`，调用方决定是降级还是报错。

### 2.3 Runtime 配置

在 `backend/configs/runtime.yaml` 的 `images:` 块（已有 `cacheMaxAge`）扩展：

```yaml
images:
  cacheMaxAge: 1h
  generations:
    default_model: gpt-image-2          # 默认模型
    default_size: "1024x1024"           # auto / WxH
    default_quality: medium             # low|medium|high|auto
    default_output_format: png          # png|jpeg|webp
    request_timeout: 5m
    max_n: 4                            # 单次请求上限
```

`@backend/internal/config/manager.go` 的 `ImagesConfig` 扩展对应 `Generations ImagesGenerationsConfig` 子块；`DefaultRuntimeConfig` 给出兜底值；旧 yaml 不写也能跑。

---

## 3. 新增包 `internal/llm/imagegen`

### 3.1 文件结构

```
internal/llm/imagegen/
  client.go        # HTTP client 封装
  request.go       # 请求/响应类型 + 校验（对齐 image_gen.py）
  persist.go       # SaveBase64ImageBytes（从 codex_image_generation.go 抽出）
  client_test.go
  request_test.go
  persist_test.go
```

### 3.2 请求/响应类型

参考 `image_gen.py` 的 `_validate_generate_payload`：

```go
// request.go
package imagegen

type GenerateRequest struct {
    Model             string `json:"model"`
    Prompt            string `json:"prompt"`
    N                 int    `json:"n,omitempty"`
    Size              string `json:"size,omitempty"`              // "auto" | WxH
    Quality           string `json:"quality,omitempty"`           // low|medium|high|auto
    Background        string `json:"background,omitempty"`        // transparent|opaque|auto
    OutputFormat      string `json:"output_format,omitempty"`     // png|jpeg|webp
    OutputCompression *int   `json:"output_compression,omitempty"`// 0..100
    Moderation        string `json:"moderation,omitempty"`
}

type GenerateResponse struct {
    Created int                    `json:"created"`
    Data    []GenerateResponseItem `json:"data"`
}

type GenerateResponseItem struct {
    B64JSON       string `json:"b64_json"`
    RevisedPrompt string `json:"revised_prompt,omitempty"`
}
```

校验函数 `Validate(req *GenerateRequest)` 严格对齐脚本：

- `n ∈ [1, 10]`（runtime 配置 `max_n` 再做二次截断）。
- `quality ∈ {low, medium, high, auto}`。
- `background ∈ {transparent, opaque, auto, ""}`，且 `transparent` 时要求 `output_format ∈ {png, webp}`。
- `output_compression ∈ [0, 100]`。
- 模型若以 `gpt-image-2` 开头：复用脚本里的尺寸约束（`max_edge ≤ 3840`，长宽 16 的倍数，`min_pixels 655360`，`max_pixels 8294400`，长短边比 ≤ 3:1，且不允许 `transparent`）。
- 模型 `gpt-image-1` / `gpt-image-1.5` 限定旧尺寸集合 `{1024x1024, 1536x1024, 1024x1536, auto}`。

### 3.3 HTTP Client

```go
// client.go
package imagegen

type Client struct {
    httpClient *http.Client
    baseURL    string
    apiKey     string
    headers    map[string]string
}

func NewClient(provider agentconfig.Provider, timeout time.Duration, proxy *agentconfig.ProxyConfig) *Client { ... }

func (c *Client) Generate(ctx context.Context, req *GenerateRequest) (*GenerateResponse, error) {
    if err := Validate(req); err != nil { return nil, err }
    body, _ := json.Marshal(req)
    httpReq, _ := http.NewRequestWithContext(ctx, http.MethodPost,
        strings.TrimRight(c.baseURL, "/") + "/v1/images/generations",
        bytes.NewReader(body))
    httpReq.Header.Set("Content-Type", "application/json")
    httpReq.Header.Set("Authorization", "Bearer " + c.apiKey)
    for k, v := range c.headers { httpReq.Header.Set(k, v) }

    resp, err := c.httpClient.Do(httpReq)
    if err != nil { return nil, err }
    defer resp.Body.Close()
    if resp.StatusCode >= 400 {
        return nil, parseAPIError(resp) // {error:{message,type,code}} 解构
    }
    var out GenerateResponse
    if err := json.NewDecoder(resp.Body).Decode(&out); err != nil { return nil, err }
    return &out, nil
}
```

`httpClient` 复用 `@backend/internal/llm/http_client.go:newProviderHTTPClient` 的工厂以保留 proxy / TLS / 流式语义；`stream=false`。

`parseAPIError` 复用 `@backend/internal/llm/provider_retry.go` 里现有的 OpenAI error envelope 解析；如果不易直接复用，先写一个最小版本（reads body, returns `&APIError{Status, Type, Message}`），后续合并。

### 3.4 落盘抽离

把 `@backend/internal/llm/codex_image_generation.go:saveGeneratedImage` 拆为：

```go
// internal/llm/imagegen/persist.go
package imagegen

type SavedImage struct {
    ID            string
    SavedPath     string
    SHA256        string
    ByteCount     int
    MimeType      string
    RevisedPrompt string
    Status        string
}

// SaveBase64Image 把 b64_json 解码后写入 outputDir/<sanitized>.<ext>
// 与 Codex 路径共用同一份实现，避免双套逻辑。
func SaveBase64Image(outputDir, idHint, b64, format string) (SavedImage, error) { ... }
```

`codex_image_generation.go` 改为调用 `imagegen.SaveBase64Image`，并把 `GeneratedImage` 类型也迁移到 `imagegen` 包里（`llm.GeneratedImage = imagegen.SavedImage` 加别名以保持外部 import 不破）。

> 兼容性：现有 `MetadataKeyGeneratedImageOutputDir` / `MetadataKeyGeneratedImages` 常量保持原 `package llm` 暴露，内部转向新包。

---

## 4. 暴露给 agent 的入口

两种入口可选，**本方案推荐先做方式 A**（小改动、能立即跑通），方式 B 留给后续：

### 4.1 方式 A: toolkit 工具 `openai_image_generate`

新增 `@backend/internal/toolkit/tools/openai_image_generate.go`：

```go
type ImageGenerateTool struct {
    *toolkit.BaseTool
    cfgResolver func() *agentconfig.Config           // 注入
    runtimeResolver func() *config.RuntimeConfig     // 注入（拿 images.generations 默认值）
    sessionDirResolver func(ctx context.Context) string // 从 ctx 拿 sessionID -> generated-images/<sid>/
}
```

参数 schema（对齐脚本，但只暴露常用字段，避免参数爆炸）：

```jsonc
{
  "type": "object",
  "properties": {
    "prompt":            {"type": "string"},
    "model":             {"type": "string", "description": "默认 runtime.images.generations.default_model"},
    "n":                 {"type": "integer", "minimum": 1, "maximum": 4, "default": 1},
    "size":              {"type": "string", "default": "1024x1024"},
    "quality":           {"type": "string", "enum": ["low","medium","high","auto"], "default": "medium"},
    "background":        {"type": "string", "enum": ["transparent","opaque","auto"]},
    "output_format":     {"type": "string", "enum": ["png","jpeg","webp"], "default": "png"},
    "output_compression":{"type": "integer", "minimum": 0, "maximum": 100}
  },
  "required": ["prompt"]
}
```

`Execute` 流程：

1. 从 ctx 拿 `sessionID`，组装 `outputDir = <artifact_root>/generated-images/<sid>/`，逻辑直接复用 `@backend/internal/agent/loop.go:generatedImageOutputDirForAgentSession`（提取为公共函数 `agent.GeneratedImageOutputDir(agent, sid)` 或在 loop 里 export）。
2. 解析参数 + runtime 默认值。
3. 用 `agentconfig.SelectImagesGenerationsProvider` 拿 provider/model。
4. 调 `imagegen.Client.Generate`，遍历 `Data[].B64JSON` 调 `imagegen.SaveBase64Image`，得到 `[]SavedImage`。
5. 返回 `ToolResult`：
   - `OutputKind = toolresult.KindStructured`（让前端走 image segment 流程）。
   - `Metadata`:
     - `generated_images`: 与 Codex 路径同结构（让 `@backend/internal/agent/loop.go` 现有 `metadata.generated_images` 合并逻辑零改动接住）。
     - `revised_prompt` / `model` / `provider` 等回放信息。
   - `Content`: `GeneratedImageSummary(images)`（已有公共函数）。

`CanDirectCall = true`；不走 MCP。

注册：在 `@backend/internal/toolkit/registry.go` 调用方（典型 `internal/agent/toolset.go` 或类似 bootstrap）里加一行 `registry.Register(tools.NewImageGenerateTool(...))`，gated by runtime 配置（`generations.default_model != ""` 且 `SelectImagesGenerationsProvider` 不返回错误）。

### 4.2 方式 B (后续): runtime-server HTTP 端点

为 aicli / 外部调用方暴露 `POST /api/runtime/sessions/{id}/images/generations`，body 直接是 `imagegen.GenerateRequest`，handler 内部走 §4.1 同一份实现。本方案不强制做，留作 follow-up。

### 4.3 与 Codex 内联工具的关系

- 当对话模型本身已经支持 Codex 内联 `image_generation`（capability `image_generation: true`），优先让模型用对话内联工具，运行时不再二次注入 `openai_image_generate` toolkit 工具，避免双工具竞争（在 `BuildToolDefinitionsForRequest` 之外的 toolkit 注入处加一个 `if CodexImageGenerationEnabled(...) { skip register openai_image_generate }` 守卫）。
- 当对话模型不支持内联 image_generation 但 runtime 配了独立 image provider，注入 `openai_image_generate` toolkit 工具，由 LLM 主动调用。
- 当两者都没配，工具不出现。

---

## 5. 错误处理 / 重试

- `Client.Generate` 直接传播 4xx 错误（不重试）：参数类问题，重试无意义。
- 5xx / 超时 / 连接重置：在 `imagegen.Client` 内做最多 3 次指数退避，跟 `@backend/internal/llm/retry_policy.go` 的 `IsTransient` 复用判断。
- 速率限制 (`429`)：解析 `Retry-After`/`retry_after`，复用 `@backend/internal/llm/provider_retry.go` 的辅助；若不易共享，本包内先写一个简化版，TODO 后续合并。
- Tool 层面失败仍返回 `ToolResult{Success:false, Error: ...}`，不 panic；前端会展示错误占位。

---

## 6. 测试计划

| 范围 | 文件 | 用例 |
| --- | --- | --- |
| 请求校验 | `imagegen/request_test.go` | gpt-image-2 尺寸边界 / transparent 与 format 互斥 / n 越界 / output_compression 越界 |
| HTTP client | `imagegen/client_test.go` | httptest server 桩，断言 method/path/headers/body；注入 4xx/5xx/429 路径 |
| 落盘 | `imagegen/persist_test.go` | 多次保存 ID 冲突时的命名 / 非法 b64 / outputDir 不可写 |
| Tool 集成 | `toolkit/tools/image_generate_test.go` | 注入 mock client，断言 metadata.generated_images 与 Codex 路径同形 |
| Codex 路径回归 | `internal/llm/codex_image_generation_test.go` | 确认抽离 `imagegen.SaveBase64Image` 后行为不变 |
| Agent 注入门控 | `internal/agent/loop_test.go`（新增 case） | 对话模型支持 Codex 内联时不注册 `openai_image_generate`；不支持时注册 |
| HTTP 端点回归 | `api/skills/generated_image_handlers_test.go` | 由 toolkit 路径生成的图片同样能被 `/generated-images/{name}` 200/304/206 |

---

## 7. 推进顺序

按风险从低到高、可独立合入：

1. **基础**: 抽 `imagegen` 包 + 把 `codex_image_generation.go` 的落盘改成调用新包。零行为变化，纯重构 + 单测。
2. **Client**: 实现 `imagegen.Client.Generate` + Validate，配 httptest 单测。不接入 agent。
3. **配置**: `NativeToolCapabilities.ImagesGenerationsAPI` + `ImagesGenerationsConfig` + `SelectImagesGenerationsProvider`。
4. **工具集成**: `openai_image_generate` toolkit tool + 注册门控；端到端跑通 aicli 一次画图。
5. **(可选)** `/v1/images/edits` 多模态编辑（需要 multipart writer，复杂度高）。
6. **(可选)** runtime-server HTTP 端点 §4.2。

---

## 8. 兼容性 & 回归

- 现有 Codex 内联路径完全不动，仅落盘函数换了实现位置；老的 `metadata.generated_images[]` 结构、文件名、`sha256` 都保持一致。
- 新工具默认不启用：runtime/yaml 没配 `images.generations.default_model` 或没有 provider 标 `images_generations_api: true` 时，工具不注册，agent 行为零变化。
- 老 `runtime.yaml` 缺 `images.generations` 块时 YAML 零值走默认（`gpt-image-2` / `1024x1024` / `medium` / `png` / 5min / `max_n=4`），不会 panic。
- `frontend` 完全无需改动：image segment / artifact / `generated-images/<name>` HTTP 流程已就绪。

---

## 9. 风险

| 风险 | 级别 | 缓解 |
| --- | --- | --- |
| OpenAI image API 错误结构差异（官方 vs 兼容网关） | 中 | `parseAPIError` 容错读取 `error.message` / `message` 两种字段；4xx 全文体打印到日志 |
| `gpt-image-2` 严格的尺寸约束被用户参数破坏 | 中 | `Validate` 严格按脚本规则执行，错误前置返回，避免上游浪费配额 |
| 图片大 / 网络慢导致 5min 默认超时不够 | 低 | runtime `request_timeout` 可配；超时后调用方看到 `ToolResult.Success=false` |
| 同一 sessionID 多个并发 openai_image_generate 调用导致文件名冲突 | 低 | `imagegen.SaveBase64Image` 内部用 `idHint + "_" + nanoTime + suffix` 兜底，避免覆盖 |
| 对话模型 + 独立 image provider 同时存在导致双工具混淆 | 中 | §4.3 注入门控；在系统提示词里说明 `openai_image_generate` 工具用法（可在 prompt 注入时附简短说明） |
| API key 泄漏到日志 | 高 | `http_debug.go` 已有脱敏；`imagegen.Client` 走同一 `newProviderHTTPClient` 工厂确保走脱敏中间件 |

---

## 10. 验收标准

- `go test ./internal/llm/imagegen/... ./internal/llm/... ./internal/toolkit/...` 全绿。
- aicli 在 `runtime.yaml` 配好 `images.generations` + `openai_image` provider 后，发送 prompt `"画一只橘猫坐在窗台上"`：
  1. agent 调用 `openai_image_generate` 工具；
  2. `chat-logs/<ts>/<sid>.artifacts/generated-images/` 出现 PNG，`sha256` / `byte_count` 正确；
  3. `assistant` 消息 metadata 含 `generated_images[].saved_path`。
- runtime-server 启动后访问 `GET /api/runtime/sessions/<sid>/generated-images/<name>` 返回 200 + 正确 `Content-Type` + ETag。
- 关闭 `images.generations` 配置后，`openai_image_generate` 工具不出现在 toolkit 列表里，对话流程零变化。

---

## 11. 待确认问题

1. 是否需要支持把 image API 走另一个 `provider_groups` 而不是单独 provider，以便和现有限流/路由统一？短期方案是单 provider，长期可对接 `provider_groups` 的 `match_path: /v1/images/generations` 路由（config.yaml 已存在）。
2. 是否需要在 `openai_image_generate` 工具的返回里直接附 `data:image/png;base64,...` 让一些不会拉 `generated-images/` 的客户端可以渲染？默认 **不附**（避免重复占用上下文 token），调用方有需要时通过 ToolResult 元数据中的 `saved_path` + HTTP 端点拉取。
3. 是否要把 `revised_prompt` 也写回对话历史（assistant 消息）？建议 **不写**，仅放在 metadata 里供 artifact 面板展示，与 Codex 路径一致。
