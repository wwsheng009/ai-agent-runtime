# Provider 兼容适配层架构优化方案

日期：2026-05-05

状态：in_progress

实施状态：已落地 `providercompat` provider adapter registry，并将 Sensenova / NVIDIA / DeepSeek / Codex 的首批兼容规则拆分到独立 adapter 文件；`ProviderWrapper` 与 `GatewayClient` 已共用内部 request assembly helper；response-side 归一化仍保留在后续 phase。

最新进展：

- 已新增 `backend/internal/llm/provider_adapter_request.go`，集中处理协议 sanitizer、provider compat message transform、tools 构建、`reasoning_effort` 过滤、Codex `max_output_tokens` gating。
- 已将 `ProviderWrapper.convertRequest` 和 `GatewayClient.buildAdapterRequest` 改为薄 wrapper，二者共用同一套 request assembly。
- 已补齐 DeepSeek runtime fallback capability：`reasoning_efforts = [high, max]`。
- 已保留 DeepSeek legacy reasoning model fallback，避免旧配置在没有显式 `model_capabilities` 时重新发送 `temperature`。
- 已新增静态 provider adapter registry：Sensenova / NVIDIA / DeepSeek / ChatGPT Codex backend / Codex default / OpenAI default 均通过 `providercompat.Chain` 按上下文匹配和执行。
- 已将 aicli login 的 DeepSeek 默认 effort 特例改为 providercompat 模型 hint，默认 effort 列表继续由 provider adapter 统一给出。

## 背景

当前运行时已经有 `ProtocolAdapter` 抽象，用于处理 `openai`、`anthropic`、`gemini`、`codex` 等协议的请求体、响应解析和流式事件解析。但近期 Sensenova 修复暴露出一个架构问题：同一协议下不同 provider 的差异处理散落在多个文件和多条调用链里。

典型例子：

- `backend/internal/llm/provider.go` 和 `backend/internal/llm/gateway_client.go` 都在 request assembly 阶段按协议做 message sanitizer，并各自追加 Sensenova 的连续 `system` 合并逻辑。
- `backend/internal/llm/thinking.go` 维护 provider/model capability fallback，例如 Sensenova、NVIDIA 的 `reasoning_effort` 列表。
- `backend/cmd/aicli/commands/provider_login.go` 为 login 写入默认 capability 时，又维护一套 provider 判断和默认 effort 列表。
- `backend/cmd/aicli/commands/chat_reasoning.go` 为 `/model` 和交互选择维护类似 fallback。
- `backend/internal/llm/reasoning_helpers.go` 同时包含协议级 sanitizer、provider 级 Sensenova sanitizer、DeepSeek reasoning replay 判断和工具调用参数规范化。
- `backend/internal/llm/codex_compat.go` 单独处理 ChatGPT Codex backend 是否支持 `max_output_tokens`。
- `backend/internal/llm/adapter/openai.go` 仍保留 DeepSeek / OpenAI reasoning 模型的启发式判断。

这些逻辑本质上不是“协议”本身，而是“某个 provider 在某个协议下的兼容差异”。继续把它们写在 `ProviderWrapper`、`GatewayClient`、aicli login 或协议 adapter 内部，会让后续新增 provider 时不断复制判断条件，也会导致 runtime 与 CLI 的能力认知不一致。

## 目标

建立一个“协议下面的 provider 兼容适配层”，让架构分层变成：

```text
业务请求 LLMRequest / ChatRequest
  -> Runtime request assembly
  -> ProtocolAdapter: 协议形态转换
  -> ProviderCompatAdapter: 同协议下 provider 差异修正
  -> HTTP request / stream
  -> ProtocolAdapter: 协议响应解析
  -> ProviderCompatAdapter: provider 响应归一化
  -> Runtime response / session history
```

目标结果：

- 协议适配器只关心协议规范，不内嵌 Sensenova、NVIDIA、DeepSeek、ModelScope 等 provider 判断。
- `ProviderWrapper` 与 `GatewayClient` 共用同一套 provider compat pipeline。
- aicli login、`/model`、runtime 请求发送使用同一个 provider capability catalog。
- 新增 provider 兼容逻辑时，优先新增一个 provider adapter 文件和测试，而不是修改多处 switch。
- 保持现有配置 schema 兼容，不要求用户迁移 `config.yaml`。

## 非目标

- 不重写现有 `ProtocolAdapter` 的请求和响应解析实现。
- 不改变 `LLMRequest`、`ChatRequest`、`agentconfig.Provider` 的外部字段。
- 不在第一阶段引入插件化动态加载，provider adapter 先采用静态注册。
- 不把所有历史兼容逻辑一次性重构完，先迁移高风险和重复逻辑。

## 当前问题拆解

### 请求链路重复

`ProviderWrapper.convertRequest` 和 `GatewayClient.buildAdapterRequest` 都执行了类似步骤：

1. 解析模型能力。
2. 将 runtime/provider message 转成协议 message。
3. 按协议清理历史工具调用。
4. 按 provider 做额外兼容处理。
5. 构造 tools。
6. 过滤 unsupported `reasoning_effort`。
7. 生成 `adapter.RequestConfig`。

这导致同一个 provider 修复通常要同时改 `provider.go` 和 `gateway_client.go`。Sensenova 的连续 `system` 合并就是当前的直接例子。

### 能力判断分叉

模型能力来源有三类：

- 配置文件中的 `model_capabilities`。
- runtime fallback，例如 `thinking.go` 中的 Sensenova / NVIDIA fallback。
- aicli login 和 `/model` 的 CLI fallback。

这些逻辑现在位于不同包中，容易出现“login 写入的能力”和“runtime 实际过滤的能力”不一致。Sensenova 之前把 `xhigh` 放入默认列表就是这类风险。

### 协议与 provider 边界不清

协议级逻辑示例：

- OpenAI-compatible 工具调用历史必须保证 assistant `tool_calls` 与后续 `tool` message 成对。
- Anthropic 工具结果要转成 user content block。
- Codex Responses API 有 `input`、`tools`、`reasoning` 等不同字段。

provider 级逻辑示例：

- Sensenova OpenAI-compatible 不接受多个连续 `system` message。
- Sensenova 工具调用回放中 `function.arguments` 不能是 `"null"`。
- NVIDIA OpenAI-compatible 的 `reasoning_effort` 可选值与 OpenAI/Codex 不同。
- ChatGPT Codex backend 不支持 `max_output_tokens`。

两类逻辑现在混在同一批 helper 里，不利于判断一个改动的影响范围。

## 建议架构

新增 provider 兼容适配层，建议包路径：

```text
backend/internal/llm/providercompat
```

该包不依赖 `backend/internal/llm` 父包，避免 import cycle；它可以依赖：

- `backend/internal/agentconfig`
- `backend/internal/llm/adapter`
- `backend/internal/types`

父包 `llm` 负责创建 `providercompat.Context`，调用 compat registry，并继续负责 HTTP、retry、debug、runtime orchestration。

核心思想：

- `ProtocolAdapter` 负责“协议格式”。
- `ProviderCompatAdapter` 负责“同协议下 provider 特性和缺陷”。
- request assembly 只调用统一 pipeline，不直接写 provider 特例。

## 接口草案

建议第一版接口保持保守，先覆盖 request config、request body、assistant message 和 model capability。

```go
package providercompat

import (
    "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
    "github.com/wwsheng009/ai-agent-runtime/internal/llm/adapter"
)

type Context struct {
    ProviderName             string
    Protocol                 string
    BaseURL                  string
    APIPath                  string
    Model                    string
    SupportsMaxOutputTokens  *bool
    ConfiguredCapabilities   map[string]agentconfig.ModelCapabilitySpec
}

type TransformReport struct {
    Metadata map[string]interface{}
}

type Adapter interface {
    Name() string
    Match(Context) bool

    DefaultCapabilities(Context) map[string]agentconfig.ModelCapabilitySpec
    PrepareRequestConfig(Context, *adapter.RequestConfig) TransformReport
    PrepareRequestBody(Context, map[string]interface{}) TransformReport
    NormalizeAssistantMessage(Context, map[string]interface{}) TransformReport
}
```

第一版不要求每个 adapter 实现所有能力。可以提供 `BaseAdapter` 空实现，具体 provider 只覆盖必要方法。

建议 registry：

```go
func ForContext(ctx Context) Chain

type Chain struct {
    adapters []Adapter
}

func (c Chain) MergeDefaultCapabilities(ctx Context, configured map[string]agentconfig.ModelCapabilitySpec) map[string]agentconfig.ModelCapabilitySpec
func (c Chain) PrepareRequestConfig(ctx Context, cfg *adapter.RequestConfig) map[string]interface{}
func (c Chain) PrepareRequestBody(ctx Context, body map[string]interface{}) map[string]interface{}
func (c Chain) NormalizeAssistantMessage(ctx Context, msg map[string]interface{}) map[string]interface{}
```

`Chain` 允许一个请求同时应用协议级默认兼容和 provider 级兼容。例如 `openai` 协议可以先应用 OpenAI-compatible 通用 sanitizer，再应用 Sensenova 专属 sanitizer。

## Provider Context 来源

`ProviderWrapper` 和 `GatewayClient` 的 context 来源不同，但最终应归一成同一个结构。

| 字段 | ProviderWrapper 来源 | GatewayClient 来源 |
| --- | --- | --- |
| `ProviderName` | 目前缺失，第一阶段可为空；后续建议 `ProviderConfig` 增加内部 `Name` 字段或构造时注入 | `selected.Provider.Name` |
| `Protocol` | `ProviderConfig.Type` | selected provider type / config protocol |
| `BaseURL` | `ProviderConfig.BaseURL` | `selected.Provider.BaseURL` 或原始 `agentconfig.Provider.BaseURL` |
| `APIPath` | `ProviderConfig.APIPath` | `selected.Provider.APIPath` |
| `Model` | resolved model | resolved model |
| `SupportsMaxOutputTokens` | `ProviderConfig.SupportsMaxOutputTokens` | selected provider field |
| `ConfiguredCapabilities` | `ProviderConfig.ModelCapabilities` | selected provider capabilities 或原始 config |

后续可以把 context 构建函数放在父包 `llm`：

```go
func providerCompatContextFromWrapper(p *ProviderWrapper, model string) providercompat.Context
func providerCompatContextFromSelected(selected *SelectedResource, protocol string, model string) providercompat.Context
```

## 请求处理流程

建议把 `ProviderWrapper.convertRequest` 和 `GatewayClient.buildAdapterRequest` 中重复的核心逻辑提取为父包内部共享函数：

```go
type adapterRequestInput struct {
    Protocol       string
    ProviderName   string
    BaseURL        string
    APIPath        string
    Model          string
    Messages       []types.Message
    Tools          []types.ToolDefinition
    Metadata       map[string]interface{}
    ReasoningEffort string
    Thinking       *ThinkingConfig
    MaxTokens      int
    Temperature    float64
    Stream         bool
}

func buildProviderAdapterRequest(input adapterRequestInput) adapter.RequestConfig
```

流程：

1. 构建 `providercompat.Context`。
2. 使用配置能力 + provider compat fallback 得到最终 `modelCapabilities`。
3. 将 runtime message 转成 protocol message。
4. 执行协议级 sanitizer。
5. 执行 provider compat `PrepareRequestConfig`。
6. 构造 tools。
7. 过滤 `reasoning_effort`。
8. 返回 `adapter.RequestConfig`。
9. `ProtocolAdapter.BuildRequest` 生成 body。
10. 执行 provider compat `PrepareRequestBody`。

关键约束：

- 协议级 sanitizer 应保留在 protocol 层或通用 compat 中，不再由 `ProviderWrapper` / `GatewayClient` 分别调用。
- provider adapter 修改 request 时必须把行为记录到 `adapter.RequestConfig.Metadata`，方便 HTTP debug 和回归定位。
- `PrepareRequestBody` 只处理确实需要看到最终 JSON body 的差异，优先在 `PrepareRequestConfig` 阶段完成。

## 首批 Provider Adapter

### OpenAI-compatible 通用 adapter

适用范围：`protocol == openai`。

职责：

- 保留当前 `sanitizeOpenAICompatibleProtocolMessages` 的通用工具回放清理。
- 保留工具调用参数空值规范化，将空字符串和 `"null"` 转为 `"{}"`。
- 不做任何具体 provider 的 capability 默认值。

迁移来源：

- `backend/internal/llm/reasoning_helpers.go`
- `backend/internal/llm/provider.go`
- `backend/internal/llm/gateway_client.go`

### Sensenova OpenAI adapter

匹配条件：

- provider name 包含 `sensenova`
- 或 base URL host 包含 `sensenova.cn`

职责：

- 默认 capability：`reasoning_efforts = [low, medium, high, none]`。
- 合并连续 `system` message。
- 保留 `reasoning_effort` 顶层字段，但过滤到官方可选值。
- 为后续响应兼容预留：支持 `delta.reasoning` 作为 stream reasoning 字段。

迁移来源：

- `fallbackProviderModelCapability` 中的 Sensenova 分支。
- `sanitizeSensenovaOpenAICompatibleProtocolMessages`。
- `provider_login.go` 和 `chat_reasoning.go` 中的 Sensenova 默认 efforts。

### NVIDIA OpenAI adapter

匹配条件：

- provider name 为 `nvidia`
- 或 base URL 包含 `integrate.api.nvidia.com`

职责：

- 默认 capability：`reasoning_efforts = [minimal, low, medium, high]`。
- 过滤 unsupported `reasoning_effort`。

迁移来源：

- `fallbackProviderModelCapability` 中的 NVIDIA 分支。
- `provider_login.go` 和 `chat_reasoning.go` 中的 NVIDIA 默认 efforts。

### DeepSeek OpenAI adapter

匹配条件：

- provider name / base URL / model id 包含 `deepseek`。

职责：

- 默认 capability：`reasoning_efforts = [high, max]`，仅在模型目录未提供 capability 时兜底。
- 处理 DeepSeek reasoning replay 规则，例如工具调用回放需要空 `reasoning_content`。
- 从 OpenAI adapter 中移除 DeepSeek 模型启发式。

迁移来源：

- `replayableOpenAIReasoningContent`。
- `adapter/openai.go` 中 `isDeepSeekOpenAIModel`。
- `provider_login.go` 中 DeepSeek 默认 efforts。

### Codex ChatGPT backend adapter

匹配条件：

- `protocol == codex`
- base URL 包含 `chatgpt.com/backend-api/codex/responses`

职责：

- 设置 `supports_max_output_tokens=false`。
- 后续可承接 ChatGPT Codex backend 的 header/body 差异。

迁移来源：

- `backend/internal/llm/codex_compat.go`。

## Capability Catalog 统一

当前 CLI 与 runtime 分别维护 provider fallback。建议 providercompat 暴露一个只依赖 context 的 capability catalog：

```go
func DefaultCapabilities(ctx Context) map[string]agentconfig.ModelCapabilitySpec
func MergeCapabilities(ctx Context, configured map[string]agentconfig.ModelCapabilitySpec) map[string]agentconfig.ModelCapabilitySpec
```

runtime 使用：

- `ProviderWrapper.modelCapabilities`
- `selectedProviderModelCapabilities`

aicli 使用：

- `buildProviderLoginModelCapabilities`
- `reasoningEffortCapabilityForModel`

为了避免 `cmd/aicli` 直接依赖 runtime 父包过多实现细节，建议 providercompat 包只暴露纯数据能力：

```go
type CapabilityHint struct {
    ReasoningModel   bool
    ReasoningEfforts []string
    Wildcard         bool
}
```

CLI login 可以先调用 providercompat 得到 provider 默认 capability，再与 `/models` 返回内容合并。这样新增 provider 默认能力只需要修改一个 adapter。

## 响应处理扩展点

第一阶段重点迁移请求兼容。第二阶段增加响应扩展点：

```go
NormalizeAssistantMessage(ctx Context, msg map[string]interface{}) TransformReport
NormalizeProcessResult(ctx Context, result *adapter.ProcessResult) TransformReport
NormalizeStreamChunk(ctx Context, chunk map[string]interface{}) TransformReport
```

用途：

- provider 返回非标准 reasoning 字段时，在 provider adapter 中统一归一化。
- provider 返回工具调用参数为对象或空值时，在 provider adapter 中规范成统一结构。
- provider 对 usage 字段命名不一致时，集中处理 usage normalize。

现有 `ProtocolAdapter.HandleResponse` 仍然先完成协议级解析；provider adapter 只做补丁式归一化，不承担完整解析。

## 文件迁移建议

新增：

```text
backend/internal/llm/providercompat/context.go
backend/internal/llm/providercompat/adapter.go
backend/internal/llm/providercompat/registry.go
backend/internal/llm/providercompat/openai.go
backend/internal/llm/providercompat/openai_sensenova.go
backend/internal/llm/providercompat/openai_nvidia.go
backend/internal/llm/providercompat/openai_deepseek.go
backend/internal/llm/providercompat/codex_chatgpt.go
backend/internal/llm/provider_compat_bridge.go
```

迁移后可删除或瘦身：

- `backend/internal/llm/thinking.go` 中 provider name / base URL 判断。
- `backend/internal/llm/codex_compat.go` 中 ChatGPT backend 判断。
- `backend/internal/llm/reasoning_helpers.go` 中 Sensenova 专属 sanitizer。
- `backend/internal/llm/provider.go` 与 `backend/internal/llm/gateway_client.go` 中 provider-specific if/switch。
- `backend/cmd/aicli/commands/provider_login.go` 中 provider 默认 reasoning effort 判断。
- `backend/cmd/aicli/commands/chat_reasoning.go` 中 provider fallback 判断。

保留：

- `ProtocolAdapter` 各协议完整请求体/响应解析。
- `ResolveModelCapabilitySpec` 和 capability clone 工具。
- protocol-level sanitizer，如 OpenAI-compatible tool replay 清理、Anthropic tool result 转换、Codex output item 规范化。

## 分阶段实施计划

### Phase 1: 建立无行为变化的 compat 框架

任务：

- 新增 `providercompat.Context`、`Adapter`、`Chain`、registry。
- 新增 no-op adapter 和测试。
- 在 `ProviderWrapper` / `GatewayClient` 构建 context，但暂不迁移行为。

验收：

- `go test ./internal/llm ./cmd/aicli/commands ./internal/agentconfig`
- HTTP debug 请求体与迁移前一致。

### Phase 2: 抽取共享 request assembly

任务：

- 把 `ProviderWrapper.convertRequest` 与 `GatewayClient.buildAdapterRequest` 中重复逻辑抽到父包共享 helper。
- 保持旧函数作为薄 wrapper，避免一次性改太多调用点。
- 新增 fixture 测试对比 wrapper 与 gateway 生成的 `adapter.RequestConfig`。

验收：

- Sensenova、NVIDIA、DeepSeek、Codex 现有测试全部通过。
- wrapper 与 gateway 对同一 provider/model/request 的 messages、tools、metadata、reasoning_effort 输出一致。

### Phase 3: 迁移 Sensenova 兼容逻辑

任务：

- 新增 `openai_sensenova.go`。
- 将 Sensenova capability fallback、连续 `system` 合并、`arguments` 空值修正迁入 adapter。
- `provider.go` 和 `gateway_client.go` 删除 Sensenova 判断。
- CLI login 和 `/model` 改用 providercompat capability catalog。

验收：

- 复用当前 Sensenova regression：
  - `reasoning_effort=max/xhigh` 不发送。
  - `reasoning_effort=high` 正常发送。
  - 两个连续 `system` 合并。
  - `function.arguments="null"` 规范为 `"{}"`。
- `go test ./internal/llm ./cmd/aicli/commands`

### Phase 4: 迁移 NVIDIA / DeepSeek / Codex provider 特例

任务：

- 新增 NVIDIA adapter，迁移 `minimal/low/medium/high` efforts。
- 新增 DeepSeek adapter，迁移 `high/max` efforts 和 reasoning replay。
- 新增 Codex ChatGPT backend adapter，迁移 `supports_max_output_tokens=false`。
- 从 `adapter/openai.go` 移除 provider-specific model 判断，保留协议级 fallback 或标记 deprecated。

验收：

- NVIDIA unsupported effort 测试仍然清空 `max`。
- DeepSeek `max` 仍可发送，工具调用 replay 不回归。
- ChatGPT Codex backend 不发送 `max_output_tokens`。

### Phase 5: 响应归一化扩展

任务：

- 增加 `NormalizeAssistantMessage` 和 `NormalizeProcessResult` 调用点。
- 把 provider-specific response 补丁从协议 adapter 中逐步迁出。
- 对 streaming aggregate 和 non-streaming 两条路径都加 fixture。

验收：

- 相同 provider 响应在 stream / non-stream 下生成一致的 runtime `LLMResponse`。
- reasoning、tool_calls、usage、metadata 不丢失。

## 测试策略

单元测试：

- `providercompat` registry 匹配测试。
- 每个 provider adapter 的 `DefaultCapabilities` 测试。
- 每个 provider adapter 的 request config/body transform 测试。
- wrapper/gateway shared assembly 的 golden tests。

集成级回归：

- `go test ./internal/llm`
- `go test ./cmd/aicli/commands`
- `go test ./internal/agentconfig`

建议新增 fixture：

```text
backend/internal/llm/testdata/providercompat/sensenova_tool_replay_request.json
backend/internal/llm/testdata/providercompat/nvidia_reasoning_effort_request.json
backend/internal/llm/testdata/providercompat/deepseek_reasoning_replay_request.json
backend/internal/llm/testdata/providercompat/codex_chatgpt_backend_request.json
```

这些 fixture 应只保存脱敏请求体，不保存 API key、真实 session 文件或用户路径。

## 风险与控制

- Import cycle 风险：`providercompat` 不依赖父包 `llm`，父包调用子包。
- 行为漂移风险：先抽 shared helper，再迁 provider adapter，每一步都用现有测试锁住行为。
- CLI/runtime 分叉风险：capability fallback 只保留一处 providercompat catalog。
- 过度抽象风险：第一版接口只覆盖已出现的差异，不预设复杂插件系统。
- Debug 可见性风险：provider adapter 每次改写 request 都写入 metadata，例如 `provider_compat.sensenova.system_messages_merged=1`。

## 验收标准

完成后应满足：

- `ProviderWrapper` 和 `GatewayClient` 中不再出现 `sensenova`、`nvidia`、`deepseek` 这类 provider name / host 判断。
- `cmd/aicli/commands/provider_login.go` 和 `chat_reasoning.go` 不再维护 provider 默认 reasoning effort 分支。
- 新增 provider 兼容行为只需要新增或修改 `providercompat` adapter，并补充该 adapter 的测试。
- 当前 Sensenova、NVIDIA、DeepSeek、Codex 兼容行为保持不变。
- `backend/configs/config.yaml` 不需要结构迁移。

## 建议实施顺序

优先顺序：

1. Phase 1 和 Phase 2 作为独立 PR，纯架构铺垫和去重，不改变行为。
2. Phase 3 单独迁移 Sensenova，因为近期问题最明确、回归样本最完整。
3. Phase 4 分 provider 小步迁移，NVIDIA、DeepSeek、Codex 各自独立提交。
4. Phase 5 在请求层稳定后再处理响应层，避免同时改 request 和 stream parser。

不建议一次性把所有 helper 和 adapter 都搬迁。当前 provider 行为涉及请求体、stream、session replay、login 写回和 remote compact，分阶段迁移能减少回归范围。
