# Provider 协议兼容 Profile 架构方案

- 日期：2026-08-01
- 范围：`backend/internal/llm`、`providercompat`、provider 配置、通用运行时消息模型与协议适配层
- 背景文档：`docs/plan/provider-upstream-400-dev-role-and-responses-content-analysis.md`
- 状态：设计与实施建议；本文不包含代码改动

## 1. 结论摘要

项目需要面向特定站点/上游的协议兼容机制，但不能以牺牲标准 OpenAI 语义为代价。推荐采用：

> **代码实现安全、可测试的兼容规则；Provider 配置选择明确的 compatibility profile；仅允许少量强类型、受限的 provider/model 覆盖。**

不建议：

- 将 `developer -> system` 写入所有 OpenAI Chat Completions 请求的通用序列化；
- 因某个上游不兼容而修改 canonical runtime message 的业务语义或历史持久化格式；
- 只通过模型名或宽泛 URL substring 推断破坏性兼容规则；
- 用 YAML/JSONPath 允许任意请求体重写；
- 在 `adapter/codex.go` 或 `reasoning_helpers.go` 中埋入特定站点的全局 `if` 分支。

当前项目已有适合承载该能力的基础：`backend/internal/llm/providercompat/`。应在该机制上演进为显式 profile 和可观测规则管道，而不是新建平行适配层。

## 2. 问题与标准协议基线

### 2.1 已确认的上游差异

opencode.ai 的 Console Go 上游存在两条确定性 400 行为：

1. `POST /v1/chat/completions` 拒绝 `role: "developer"`；同一请求将 developer 改为 system 后通过。
2. `POST /v1/responses` 拒绝 assistant message 的 `content: [{"type":"output_text","text":"..."}]`；同一请求将 content 改为字符串后通过。

这两项均是该上游的 OpenAI-compatible 方言限制，不是标准 OpenAI 协议错误。

### 2.2 官方 OpenAI SDK 基线

官方 OpenAI Node/Python SDK 的 Chat Completions 消息不是只有 `system`、`user`。当前正式支持至少包括：

| role | 官方 SDK 支持 | 语义 |
|---|---:|---|
| `developer` | 是 | 开发者指令；对 o1 及更新模型，用于取代此前 system 指令用途 |
| `system` | 是 | 系统指令，继续支持 |
| `user` | 是 | 用户输入 |
| `assistant` | 是 | 模型历史输出及 tool calls |
| `tool` | 是 | 工具调用结果，关联 `tool_call_id` |
| `function` | 是，但属旧兼容形式 | 旧 function-calling 消息形状 |

官方 Python SDK 的 `ChatCompletionMessageParam` 是上述消息类型的联合，`developer` 具有独立的 `ChatCompletionDeveloperMessageParam`，不是未校验的自由字段。官方 Node SDK 的 `chat.completions.create` 示例也直接使用 `role: "developer"`。

因此，标准 OpenAI chat/completions 序列化必须保留 developer；把 developer 全局降级为 system 会错误地降低真实 OpenAI 和其他合规提供商的语义。

## 3. 当前项目能力与差距

### 3.1 已覆盖的通用功能

项目的 canonical runtime message 已支持：

- `developer`、`system`、`user`、`assistant`、`tool`；
- assistant 的多个 tool call；
- tool 结果的 `tool_call_id`；
- OpenAI Chat Completions 的 `messages`、tools、流式 tool-call 增量解析；
- 旧 `function_call` 响应向 function tool call 的兼容；
- Codex/Responses 的 `input`、`instructions`、`function_call`、`function_call_output`；
- Codex custom tool call；
- 文本与本地图片到 Chat `image_url` 或 Responses `input_image` 的转换；
- canonical 消息历史的持久化、恢复与重新协议化。

关键实现位置：

| 能力 | 位置 |
|---|---|
| canonical message、构造器、tool call | `backend/internal/types/message.go` |
| OpenAI request/response/stream | `backend/internal/llm/adapter/openai.go` |
| Codex request/response/stream | `backend/internal/llm/adapter/codex.go` |
| runtime message 到协议 message | `backend/internal/llm/reasoning_helpers.go` |
| 请求 sanitizer 和 providercompat 调用 | `backend/internal/llm/provider_adapter_request.go` |
| HTTP request/body 末端 normalize | `backend/internal/llm/provider.go` |

### 3.2 与官方 SDK 设计相比的差距

当前项目为跨协议保持弹性，运行时消息主要以 `Role string`、`Content string`、`map[string]interface{}` 表达；官方 SDK 则用角色判别联合与角色相关字段约束表达请求。当前方式的优点是协议扩展快，风险是：

- 缺少统一 role allowlist 及 role-specific invariants；
- `tool` 必须携带 `tool_call_id` 等约束主要依赖局部 sanitizer；
- legacy `function` role 没有对应的一等 runtime 模型；
- 嵌套 wire 转换大面积使用弱类型 map，规则组合时更依赖测试。

项目不必完全复制 SDK 的类型体系，但应分清以下三层：

1. canonical message 的业务语义与内部不变量；
2. 标准协议序列化的约束；
3. 特定 provider endpoint 的 wire dialect 约束。

三层不能因某个上游缺陷而混合。

### 3.3 ContentParts 的实现与契约不一致

`internal/types/message.go` 注释说明：当 `ContentParts` 存在时，应优先于 flat `Content` 用于协议序列化。

但 `backend/internal/llm/reasoning_helpers.go` 的 `buildProtocolMessageMap` 当前明确忽略该参数：

```go
_ = contentParts
```

现有 OpenAI/Codex 图片路径主要从 `Metadata["input_images"]` 的 sideband 读取。后果是：

| 项目 | 类型层声明 | 当前主出站路径 |
|---|---:|---:|
| 文本 | 是 | 是 |
| 本地图片 metadata | 是 | 是 |
| `ContentParts` 中显式图片 URL | 是 | 不可靠 |
| 文本/图片交错顺序 | 是 | 可能丢失 |
| 音频、文件、视频 | 部分 typed OpenAI 定义存在 | 通用 runtime 管线未完整支持 |

这与本次 400 是相邻但独立的架构问题：应纳入后续协议质量治理，不能为了修复上游 400 一次性进行高风险重构。

## 4. 当前 providercompat 机制评估

### 4.1 已有能力

当前目录：

```text
backend/internal/llm/providercompat/
  adapter.go
  registry.go
  providercompat.go
  openai_default.go
  openai_deepseek.go
  openai_nvidia.go
  openai_sensenova.go
  codex_default.go
  codex_chatgpt.go
  response.go
```

`Adapter` 已提供以下扩展点：

```go
NormalizeOpenAICompatibleMessages(...)
PrepareRequestBody(...)
NormalizeAssistantMessage(...)
NormalizeProcessResult(...)
NormalizeStreamChunk(...)
ReplayableOpenAIReasoningContent(...)
SupportsMaxOutputTokens(...)
```

当前实践示例：

| 适配器 | 现有职责 |
|---|---|
| `openai-default` | OpenAI-compatible reasoning / tool-call 字段归一化 |
| `openai-sensenova` | 合并连续 system 消息 |
| `openai-deepseek` | 推理内容重放与能力推断 |
| `codex-chatgpt-backend` | 移除不被该 backend 接受的 `max_output_tokens` |
| `codex-default` | Codex reasoning 默认值 |

目前调用顺序大致为：

```text
canonical runtime message
  -> protocol message serialization
  -> protocol sanitizer
  -> NormalizeOpenAICompatibleMessages（仅 OpenAI 路径）
  -> protocol adapter BuildRequest
  -> PrepareRequestBody
  -> HTTP

HTTP response/stream
  -> NormalizeStreamChunk / reader
  -> protocol adapter parser
  -> NormalizeAssistantMessage / NormalizeProcessResult
```

该边界划分是正确方向：标准 protocol adapter 负责标准 wire 形状，providercompat 负责上游方言。

### 4.2 当前机制的架构问题

#### 问题一：直连 ProviderWrapper 与 Gateway 的兼容上下文不一致

`providercompat.Context` 有 `ProviderName`、`Protocol`、`BaseURL`、`APIPath`、`Model` 等字段。

但直接 `ProviderWrapper` 路径存在身份缺失：

- `ProviderConfig` 没有稳定的 Provider ID/Name；
- `ProviderWrapper.convertRequest()` 构造 `providerAdapterRequestInput` 时没有传 ProviderName；
- `providerAdapterRequestInput` 没有 APIPath；
- `ProviderWrapper.providerCompatContext()` 没有填 ProviderName；
- Gateway 路径则会经 `SelectedResource.Provider.Name` 传 ProviderName。

结果是：按 provider 配置名精确匹配的兼容规则，可能只在 gateway 生效，而在直接 ProviderWrapper 路径失效。新增 opencode profile 前应先统一这一点。

#### 问题二：注册顺序就是隐式优先级

`registry.go` 以全局 adapter slice 的注册顺序组合规则。优点是能叠加，缺点是：

- 同一字段被多个 adapter 修改时，顺序决定最终结果；
- 优先级没有类型化或文档化；
- 新 adapter 容易因注册位置改变既有行为；
- 没有明确的 profile 互斥或协议约束；
- URL/model 的猜测匹配会随站点增加而提升误判风险。

例如 `IsDeepSeek` 可由模型名命中。聚合站点即便路由 DeepSeek 模型，也不意味着它采用 DeepSeek 官方 wire dialect。因此，不能以 `IsDeepSeek(model)` 作为 opencode 破坏性变换的条件。

#### 问题三：缺少显式且可审计的 wire compatibility 配置

`agentconfig.Provider` 已包含 protocol、base URL、API path、模型能力、headers、`SupportsMaxOutputTokens`、`EnableImageGeneration` 等信息，但没有兼容 profile。

现有 `site_type` 是站点/账号检测、余额和认证相关语义，**不能复用为协议兼容 profile**：站点账户类型与 endpoint 能否接受 developer 或 content array 是两个正交维度。

## 5. 本次问题的架构决策

### 5.1 canonical runtime 与历史层保持 developer

fact ledger 和 active goal 当前以 `developer` 表达：

- `backend/internal/contextmgr/facts.go:160`；
- `backend/internal/contextmgr/manager.go:858`。

这符合其“高优先级指令上下文”业务语义，不应为兼容最弱上游而改为 system。若修改 canonical role：

1. 会降低真实 OpenAI / 新模型的标准语义；
2. 会改写新持久化历史；
3. 可能影响 `promptContextMessageKey` 中含 Role 的快照去重；
4. 不能覆盖其他未来 provider 方言。

采用下列原则：

```text
canonical runtime / SQLite 历史：developer
标准 OpenAI Chat：developer
opencode Chat 出站 wire message：developer -> system
```

存量历史无需数据迁移；每次重放时在出站 clone 上应用规则即可。

### 5.2 标准协议序列化保持标准形状

以下标准形状不应为 opencode 而全局变化：

- OpenAI Chat 的 `role: developer`；
- Responses assistant message 的 `content: [{type: output_text, text: ...}]` 数组。

真实 OpenAI 和其他兼容端可能依赖或完整接受这些形状。特定上游的不完整实现只能由站点 profile 覆盖。

## 6. 推荐目标设计：代码 Profile + 配置选择 + 受限覆盖

### 6.1 职责边界

#### 代码：实现“如何安全变换”

兼容规则应由代码实现，因为它们需要理解嵌套协议结构并保证：

- 只变换指定角色、item type、endpoint 与协议；
- 保留 tool calls、reasoning、multimodal 数据；
- 深拷贝，绝不 mutation canonical history 或调用方 request；
- 遇到不可无损转换的结构不静默删除；
- 能以 fixture 和单元测试覆盖。

例如 assistant content array 压平并非简单 JSON 字段替换，必须区分：`output_text`、多个 text part、refusal、reasoning、function/custom-tool item、图片及未来未知 output part。自由 YAML JSONPath rewrite 无法安全表达、验证或演进这类逻辑。

#### 配置：决定“何时启用规则”

Provider 配置应选择稳定 profile ID，而不是描述任意变换脚本：

```yaml
providers:
  opencode:
    protocol: openai
    base_url: https://opencode.ai/zen/go/v1
    compatibility:
      profile: opencode-console-go-2026-07
```

相同站点的 Codex/Responses 配置：

```yaml
providers:
  opencode-codex:
    protocol: codex
    base_url: https://opencode.ai/zen/go/v1
    compatibility:
      profile: opencode-console-go-2026-07
```

允许少量强类型覆盖，但覆盖项必须是代码定义的枚举：

```yaml
compatibility:
  profile: opencode-console-go-2026-07
  overrides:
    chat_developer_role: system
    responses_assistant_content: string_only
```

禁止开放如下能力：

```yaml
# 不建议：无法可靠校验，且会形成不可维护的隐藏脚本系统
jsonpath_rewrites:
  - path: "$.input[*].content"
    expression: "..."
```

### 6.2 建议配置模型

兼容配置应与 `ModelCapabilitySpec` 分离：前者描述 endpoint 的 wire dialect，后者描述模型能力。

```go
type CompatibilityConfig struct {
    // Profile 是内置、版本化、稳定的 profile ID；空值表示使用选择策略。
    Profile string `yaml:"profile,omitempty" mapstructure:"profile" json:"profile,omitempty"`

    // Strict 表示不可无损兼容时应报错，而不是只记录诊断后保持标准形状。
    Strict *bool `yaml:"strict,omitempty" mapstructure:"strict" json:"strict,omitempty"`

    Overrides CompatibilityOverrides `yaml:"overrides,omitempty" mapstructure:"overrides" json:"overrides,omitempty"`
}

type CompatibilityOverrides struct {
    ChatDeveloperRole         DeveloperRoleMode   `yaml:"chat_developer_role,omitempty"`
    ResponsesAssistantContent AssistantContentMode `yaml:"responses_assistant_content,omitempty"`
}

type DeveloperRoleMode string

const (
    DeveloperRoleNative DeveloperRoleMode = "native"
    DeveloperRoleSystem DeveloperRoleMode = "system"
)

type AssistantContentMode string

const (
    AssistantContentStandardParts AssistantContentMode = "standard_parts"
    AssistantContentStringOnly    AssistantContentMode = "string_only"
)
```

字段归属：

| 信息 | 归属 |
|---|---|
| 是否接受 developer role | provider endpoint / protocol compatibility profile |
| Responses assistant content 是否接受数组 | provider endpoint / protocol compatibility profile |
| 是否支持 `max_output_tokens` | endpoint profile；现有配置可保留显式开关 |
| 是否支持 native image generation tool | endpoint profile / provider 级开关 |
| 上下文窗口、reasoning、输入模态 | `ModelCapabilitySpec` |
| 某一模型在某 endpoint 的例外 | profile 下的 model overlay 或强类型配置覆盖 |

### 6.3 Profile 选择优先级

固定选择顺序：

```text
显式 compatibility.profile
  > 精确 provider ID + host/path 的内置识别
  > 标准 protocol profile
```

规则：

1. 默认必须保持标准协议语义。
2. 用户显式选择 `standard` 时，必须禁止自动识别的破坏性改写。
3. 自动识别只允许针对已知、精确且有测试的 endpoint host/path；不得仅凭模型名应用改写。
4. profile 不存在、profile 与 protocol 不匹配、override 互相冲突，均应在配置加载/Provider 初始化时失败。
5. profile 应版本化，例如 `opencode-console-go-2026-07`，便于上游修复后保留旧行为和迁移审计。

opencode 的匹配应优先是显式 profile；fallback 才能匹配精确 host/path，例如 `opencode.ai` 与已确认 API path。不能将问题归因成 `IsDeepSeek`，因为实测问题属于 opencode Console Go 网关，而非所有 DeepSeek 模型的共同协议特征。

## 7. Profile 规则管道

保留 `providercompat.Adapter` 基础接口，并逐步让它具备 profile、阶段和 trace 语义。概念模型：

```text
Canonical Message
  -> Standard Protocol Serializer
  -> NormalizeProtocolMessages
  -> Protocol Adapter BuildRequest
  -> PrepareRequestBody
  -> HTTP
  -> NormalizeStream / NormalizeResponse
  -> Protocol Parser
  -> Canonical Assistant Result
```

当前 hook 与推荐职责对应如下：

| hook | 推荐职责 |
|---|---|
| `NormalizeOpenAICompatibleMessages` | Chat message 级的特定上游规则 |
| `PrepareRequestBody` | 已形成最终 wire body 后的 Responses/Chat body 规则 |
| `NormalizeAssistantMessage` | 非流 response 的字段兼容 |
| `NormalizeProcessResult` | parser 后内部结果归一化 |
| `NormalizeStreamChunk` | SSE event / 增量字段兼容 |

建议补充的基础要素：

- 每个规则的 profile ID、rule ID、支持 protocol；
- 明确 precedence，而非依赖 registry slice 的隐式顺序；
- 规则幂等性；
- copy-on-write；
- 规则命中 trace；
- 可在 debug 元数据中记录 profile/rule ID，不记录 API key 或敏感完整内容；
- profile 与配置的启动校验。

建议最终将规则层级限定为：

```text
1 个标准 protocol adapter（必有）
+ 0/1 个 site compatibility profile
+ 0/1 个 model overlay（仅针对明确、强类型的例外）
```

避免任意数量的全局 adapter 无限制叠加。

## 8. opencode Console Go Profile 的具体规则

### 8.1 OpenAI Chat：developer -> system

归属：

```text
profile: opencode-console-go-2026-07
protocol: openai
stage: NormalizeProtocolMessages
rule: chat.developer_to_system
```

伪代码：

```go
if ctx.Profile == opencodeConsoleGo && ctx.Protocol == "openai" {
    for _, message := range messages {
        if message["role"] == "developer" {
            clone(message)["role"] = "system"
        }
    }
}
```

要求：

- 仅改变 `role == developer` 的出站 map；
- 不重排、不合并、不删除消息；
- 不修改 canonical history、SQLite payload 或原始 request；
- 只在 OpenAI Chat profile 下生效；
- 记录 `compat_profile=opencode-console-go-2026-07` 和 `compat_rule=chat.developer_to_system`。

### 8.2 Codex Responses：assistant output_text array -> string

归属：

```text
profile: opencode-console-go-2026-07
protocol: codex
stage: PrepareRequestBody
rule: responses.assistant_output_text_to_string
```

此规则必须放在 `PrepareRequestBody`，因为 `CodexAdapter.BuildRequest` 已将内部 assistant history 构造成最终 Responses `input` / output item 形状。

仅对以下结构生效：

```json
{
  "type": "message",
  "role": "assistant",
  "content": [
    {"type": "output_text", "text": "..."}
  ]
}
```

变换后：

```json
{
  "type": "message",
  "role": "assistant",
  "content": "..."
}
```

严格约束：

1. 仅当所有 part 均为可无损拼接的文本 output part 时压平；
2. 多个文本 part 按原顺序拼接；
3. user `input_image`、user content array 不受影响；
4. reasoning、function_call、custom_tool_call、image generation 等独立 item 不受影响；
5. 对未知、图片或其他非文本 assistant content：禁止静默删除；
6. strict 模式下应返回带 rule ID 的明确错误；非 strict 模式可保留标准结构并写出诊断 trace；
7. 标准 OpenAI、ChatGPT Codex 等 profile 必须保留原标准数组。

## 9. 实施计划

### P0：安全修复本次 400

1. 为直连 ProviderWrapper 增加稳定 Provider ID/Name，并在构造 `ProviderConfig` 时从配置 map key 传入。
2. 为 `providerAdapterRequestInput` 增加 APIPath、Provider identity 和 compatibility 配置。
3. 抽取单一 `providercompat.Context` factory，供 Gateway 与 ProviderWrapper 共用，确保两条链路一致。
4. 增加 `CompatibilityConfig` 和 profile 选择、校验逻辑。
5. 实现 `opencode-console-go-2026-07`：
   - OpenAI message 阶段的 developer -> system；
   - Codex body 阶段的 assistant output_text array -> string。
6. 在 HTTP debug metadata 中记录 profile/rule 命中信息，禁止暴露密钥和完整敏感请求内容。
7. 使用已保存的失败 request fixtures 和真实上游 A/B 重放验证修复。

### P1：机制治理

1. 将 registry 的隐式顺序演进为显式 base/site/model 优先级。
2. 对 profile、protocol、override 建立配置加载时校验。
3. 自动识别收紧为精确 provider identity、host/path；模型名只可用于非破坏性的能力推断。
4. 增加 profile 选择、匹配原因、规则命中指标与诊断。
5. 明确 `standard` profile 能关闭自动兼容转换。

### P2：通用协议模型质量

1. 让 `ContentParts` 真正成为 runtime -> wire 的一等输入。
2. 统一 text/image/audio/file/video 的支持矩阵。
3. 不支持的 content part 必须返回可观测错误，禁止静默丢弃。
4. 逐步以 protocol-specific validator 或判别类型收敛 `map[string]interface{}` 的关键路径。
5. 对 legacy `function` role 明确策略：完整建模与测试，或明确弃用与转换边界。

## 10. 测试与验收

### 10.1 标准协议契约测试

- Chat 的 `developer/system/user/assistant/tool/function` role 矩阵；
- assistant tool calls 与 tool `tool_call_id` 关联；
- content 的 string、null、content parts；
- 标准 OpenAI profile 下 developer 不得转换为 system；
- 标准 Responses profile 下 assistant `output_text` 数组必须保留；
- 使用官方 SDK/OpenAPI JSON fixtures 建立请求形状断言。

### 10.2 opencode Profile 回归

- developer 仅在 `protocol=openai` 时变为 system；
- system、user、assistant、tool role 不受误伤；
- 原始 canonical message、历史 message 不被 mutation；
- 旧 SQLite developer 历史恢复后发往 opencode 仍安全；
- assistant 单一和多个 `output_text` part 正确压平并保序；
- 非文本 assistant content 不静默删除；
- user image、tool、reasoning、custom tool、image generation item 不受误伤；
- 标准 OpenAI 与 ChatGPT Codex 后端请求体不改变。

### 10.3 配置和链路一致性测试

- 显式 profile 优先于 URL 自动识别；
- `standard` profile 禁用自动识别；
- 未知 profile、protocol 不匹配、冲突 override 在初始化时报错；
- Gateway 与 ProviderWrapper 针对同一配置生成相同 compatibility context 和同样 request body；
- host/path 匹配具有边界，拒绝宽泛 substring 误判；
- debug 只输出 profile/rule ID 等安全元数据。

## 11. 当前验证状态

P0（安全修复本次 400）已按本方案实施完毕，以下验证均通过：

```text
go test ./internal/llm/... ./internal/agentconfig/... ./internal/runtimeserver/...
go build ./...
```

- `CompatibilityConfig`、`CompatibilityProfileStandard`、`CompatibilityProfileOpenCodeConsoleGo` 与
  `ValidateCompatibilityProfile` 已加入 `internal/agentconfig`（含协议匹配校验测试）。
- `opencode-console-go-2026-07` profile 已实现：OpenAI Chat 阶段 developer -> system、
  Codex Responses 阶段 assistant `output_text` array -> string，规则与回归测试见
  `internal/llm/providercompat/opencode_console_go.go`。
- **运行时投影、不固化契约**（`opencode_console_go_test.go` 锁定）：转换只作用于出站
  请求的副本，全部 developer 消息（fact ledger/goal/todo 等注入层）逐条投影为 system，
  原始 body/消息数组与本地聊天历史保持 developer 不变；同一历史在不同 profile 下可逆
  投影（opencode -> system，standard -> developer），保证切换 provider 无损。
- Gateway 与直连 ProviderWrapper 通过单一 `providercompat.Context` factory 共享
  profile 上下文（`gatewayProviderCompatContext` / `providerCompatContext`）。
- HTTP debug 元数据记录 `compatibility_profile` 与 `protocol`（仅安全元数据，请求体以
  SHA-256 指纹形式呈现，不暴露密钥与完整敏感内容）。
- 显式 profile 优先于 URL 自动识别；`standard`/空 profile 关闭非标准转换。

P1/P2 机制治理与通用协议模型质量项（registry 显式优先级、精确 identity 自动识别、
ContentParts 一等输入等）为后续演进，不在本次 P0 范围内。

## 12. 相关文件

- 上游 400 证据与初步修复分析：`docs/plan/provider-upstream-400-dev-role-and-responses-content-analysis.md`
- 兼容 adapter 接口：`backend/internal/llm/providercompat/adapter.go`
- adapter registry：`backend/internal/llm/providercompat/registry.go`
- compatibility chain：`backend/internal/llm/providercompat/providercompat.go`
- 请求构建入口：`backend/internal/llm/provider_adapter_request.go`
- 直连 provider 请求/响应入口：`backend/internal/llm/provider.go`
- gateway compatibility context：`backend/internal/llm/provider_compat_response.go`
- runtime message 序列化：`backend/internal/llm/reasoning_helpers.go`
- canonical message 模型：`backend/internal/types/message.go`
