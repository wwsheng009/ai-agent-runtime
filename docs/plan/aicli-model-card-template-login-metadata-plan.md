# aicli 模型卡片模板与登录元数据写回方案

日期：2026-05-12

状态：proposal

## 文档定位

本文档为 `aicli login` 增加“模型卡片模板”能力提供实施方案。目标是在登录成功并从 provider models endpoint 拿到模型列表后，用内置或用户自定义的模型元数据卡片补齐 `config.yaml` 中的 `providers.items.<name>.model_capabilities`，减少用户手工维护模型参数、模态能力、reasoning 选项和压缩阈值的成本。

本方案只设计流程和落地点，不直接修改运行时代码。

## 背景与现状

现有代码已经具备可复用基础：

- `backend/cmd/aicli/commands/provider_login.go` 的 `runProviderLogin` 已经完成 provider 登录、models 校验、`supported_models` / `default_model` / `models_verified_at` 写回。
- `backend/cmd/aicli/commands/provider_models.go` 已经把 OpenAI-compatible、Gemini、Codex 等 `/models` 响应标准化为 `providerModelInfo`，字段包括 `id`、`input_modalities`、`reasoning_efforts`、`max_context_tokens`、`supports_remote_codex`。
- `backend/internal/agentconfig/config.go` 已经定义 `ModelCapabilitySpec`，当前配置文件也在 `providers.items.<provider>.model_capabilities.<model>` 下使用同一套字段。
- `backend/internal/agentconfig/provider_persistence.go` 已经支持 provider 级 YAML 局部写回，避免全量重写 `config.yaml`。
- `docs/plan/aicli-provider-login-plan.md` 已记录当前 login MVP 的实现边界和文件索引。

当前不足是：models endpoint 经常只返回模型 id，少量 provider 会返回部分能力字段，但不会稳定返回本项目真正需要的完整运行时元数据。例如 reasoning effort 列表、原生图片工具、`max_context_tokens`、自动压缩阈值、远端 compact 能力等，仍需要在 `config.yaml` 里按 provider/model 手工补齐。

## 目标

- 新增一份模型卡片模板文件，维护“模型 id -> 模型能力元数据”的标准目录。
- `aicli login` 成功拉取模型列表后，如果返回的模型 id 命中模型卡片目录，就把卡片元数据合并到候选 provider 的 `model_capabilities`。
- 写回配置时沿用现有 `UpdateProviderConfig` 机制，只更新目标 provider 节点。
- 模型卡片只保存模型行为元数据，不保存 API key、账号、私有 base URL 等敏感信息。
- 支持内置卡片和用户覆盖卡片，避免每次新增 provider 都要重复手写相同模型能力。
- 保持可回滚：登录校验失败、模型卡片解析失败或 dry-run 时不写盘。

## 非目标

- 不把模型卡片作为 provider 选择或负载均衡的唯一依据。
- 不在模型卡片里保存密钥、OAuth token、账号标识或用户私有 endpoint。
- 不替代 providercompat adapter。协议级请求格式仍由 `protocol` 和 adapter 决定，模型卡片只补模型能力元数据。
- 不在 `/models` 返回混合协议模型时自动创建多个 provider。首阶段只过滤当前 provider 不应接收的模型，并在结果中给出推荐 provider template，避免静默改写用户的 provider 拓扑。
- 不在首阶段自动改写 `provider_groups`、`provider_queue`、`aicli.chat.default_model` 等全局策略。
- 不在首阶段实现联网更新模型卡片目录。卡片文件以仓库内置和本地用户覆盖为主。

## 建议文件与职责

建议新增内置模板源码文件：

- `backend/configs/model_cards.yaml`

运行时不应依赖 `backend/configs/model_cards.yaml` 这个文件系统路径。`aicli` 安装后通常是单个二进制，默认配置也可能位于 `~/.aicli/config.yaml` 或当前工作目录的 `.aicli/config.yaml`，不保证存在源码树目录。因此内置卡片应通过 `go:embed` 嵌入二进制，源码 YAML 只是维护入口。

建议新增可选用户覆盖文件：

- `~/.aicli/model_cards.yaml`
- 后续可扩展工作区覆盖：`.aicli/model_cards.yaml`

建议新增代码模块：

- `backend/internal/modelcard/catalog.go`：模型卡片 schema、加载、校验、合并。
- `backend/internal/modelcard/match.go`：模型 id、协议、provider hint、base URL hint 的匹配逻辑。
- `backend/internal/modelcard/catalog_test.go`：卡片加载、匹配、优先级、合并测试。
- `backend/cmd/aicli/commands/provider_login.go`：在 `buildProviderLoginModelCapabilities` 前后接入模型卡片补全。
- `backend/internal/agentconfig/provider_persistence.go`：必要时增强显式 false 写回能力，避免模板中的负向能力丢失。

`modelcard` 包建议依赖 `agentconfig.ModelCapabilitySpec`，但不要反向让 `agentconfig` 依赖命令层。这样 login、runtime server 或未来 UI 都能复用同一套模型目录。

## 模型卡片模板格式

建议 `backend/configs/model_cards.yaml` 使用版本化 YAML，卡片主体保持贴近现有 `ModelCapabilitySpec`，减少转换成本。

```yaml
version: 1
provider_templates:
  - id: codex.responses
    protocol: codex
    api_path: /v1/responses
    forward_url: /v1/responses
    support_types: [codex, openai, anthropic, gemini]
    max_tokens_limit: 10000

  - id: anthropic.messages
    protocol: anthropic
    api_path: /v1/messages
    forward_url: /v1/messages
    support_types: [anthropic]
    max_tokens_limit: 131072

  - id: openai.chat
    protocol: openai
    api_path: /v1/chat/completions
    forward_url: /v1/chat/completions
    support_types: [openai]

  - id: openai.images
    protocol: openai
    api_path: /v1/images/generations
    forward_url: /v1/images/generations
    support_types: [openai]

cards:
  - id: fallback.openai.chat
    title: OpenAI Chat Fallback
    priority: -100
    fallback: true
    provider_template: openai.chat
    match:
      protocols:
        - openai
    capability:
      input_modalities:
        - text

  - id: openai.gpt-5.4
    title: GPT 5.4
    priority: 100
    provider_template: codex.responses
    match:
      model_ids:
        - gpt-5.4
      protocols:
        - codex
        - openai
    capability:
      max_context_tokens: 270000
      auto_compact_token_limit: 200000
      input_modalities:
        - text
        - image
      native_tools:
        image_generation: true
      reasoning_model: true
      reasoning_efforts:
        - none
        - low
        - medium
        - high
        - xhigh
      compact_reasoning_effort: low

  - id: openai.gpt-image-2
    title: GPT Image 2
    priority: 100
    match:
      model_ids:
        - gpt-image-2
      protocols:
        - openai
    capability:
      input_modalities:
        - text
      native_tools:
        images_generations_api: true
```

字段说明：

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `version` | int | 模板文件版本，首版固定为 `1`。 |
| `provider_templates[].id` | string | provider endpoint 模板稳定 id，例如 `codex.responses`、`anthropic.messages`、`openai.images`。 |
| `provider_templates[].protocol` | string | 写入 provider 的运行时协议，例如 `codex`、`anthropic`、`openai`。 |
| `provider_templates[].api_path` | string | 当前 endpoint 的默认 API path，用于补齐 provider 级配置。 |
| `provider_templates[].forward_url` | string | 网关转发场景使用的默认 forward URL。 |
| `provider_templates[].support_types` | string[] | provider 可接收的请求协议类型。 |
| `provider_templates[].max_tokens_limit` | int | provider 级输出 token 上限默认值。 |
| `cards[].id` | string | 卡片稳定 id，用于日志、测试和 dry-run 输出。 |
| `cards[].title` | string | 展示名，不参与运行时决策。 |
| `cards[].priority` | int | 多张卡命中同一模型时的排序权重，越大越优先。 |
| `cards[].fallback` | bool | 协议或 endpoint 兜底卡片。只有当前模型没有命中任何普通卡片时才会使用。 |
| `cards[].provider_template` | string | 该模型应归属的 provider endpoint 模板。用于过滤混合 `/models` 列表，也用于提示用户应拆分到哪个 provider。 |
| `cards[].match.model_ids` | string[] | 精确模型 id。登录返回的模型 id 命中这里时复制能力元数据。 |
| `cards[].match.aliases` | string[] | 可选别名，只参与匹配，写回时仍使用 provider 返回的真实 id。 |
| `cards[].match.model_patterns` | string[] | 可选正则或 glob，低优先级兜底。首阶段可以先不开放。 |
| `cards[].match.protocols` | string[] | 可选协议约束，避免同一模型 id 在不同协议下能力不同。 |
| `cards[].match.provider_names` | string[] | 可选 provider 名称约束，用于本项目内置 provider 的特殊能力。 |
| `cards[].match.base_url_contains` | string[] | 可选 base URL hint，用于区分 OpenAI API、ChatGPT Codex backend 或私有代理。 |
| `cards[].capability` | object | 直接映射到 `agentconfig.ModelCapabilitySpec`。 |

## 匹配规则

登录时每个返回模型都独立匹配一次模型卡片。

输入上下文：

- `providerName`：当前登录的 provider 名称。
- `loginProtocol`：用户选择的登录协议，例如 `openai`、`gemini`、`codex-apikey`、`codex-oauth`。
- `runtimeProtocol`：写入 provider 的运行时协议，例如 `codex-apikey` 和 `codex-oauth` 都映射为 `codex`。
- `baseURL`：当前候选 provider 的 base URL。
- `model.ID`：models endpoint 返回并标准化后的模型 id。
- `model.Raw`：可选原始响应，首阶段只用于调试，不参与模板匹配。

建议匹配顺序：

1. 精确匹配 `match.model_ids`，保留 provider 返回 id 的原始大小写和分隔符作为写回 key。
2. 再匹配 `match.aliases`。别名只用于命中卡片，不改写 `supported_models`。
3. 最后才匹配 `match.model_patterns`，并要求模板显式设置较低 `priority`，防止宽泛规则误伤。
4. 如果卡片声明了 `protocols`，必须匹配 `runtimeProtocol` 或 `loginProtocol`。
5. 如果卡片声明了 `provider_names`，必须与当前 provider 名称大小写不敏感相等。
6. 如果卡片声明了 `base_url_contains`，当前 `baseURL` 小写后必须包含任意一个 hint。
7. 多张卡片命中同一模型时，按 `priority`、精确度、`id` 字典序排序，最终合并为一个 capability。

精确度建议：

| 命中条件 | 分值 |
| --- | ---: |
| `model_ids` 精确命中 | 100 |
| `aliases` 命中 | 70 |
| `model_patterns` 命中 | 30 |
| 同时命中 `protocols` | +20 |
| 同时命中 `provider_names` | +20 |
| 同时命中 `base_url_contains` | +10 |

这样可以让“同一个模型 id 的通用卡片”和“某个 provider/protocol 的特殊卡片”共存，并由更具体的卡片覆盖通用字段。

## 协议兜底与混合协议 `/models` 列表处理

模型卡片支持协议兜底配置。兜底卡片使用 `fallback: true`，通常只声明 `match.protocols` 和 `provider_template`，不需要 `model_ids`、`aliases` 或 `model_patterns`。

兜底规则：

1. 先尝试精确 id、模糊 id、alias、pattern 等普通模型卡片。
2. 只有没有任何普通卡片命中时，才尝试 `fallback: true` 卡片。
3. 兜底卡片必须能匹配当前协议，或者能匹配当前 provider template。
4. 如果同一协议下存在多个 endpoint，例如 `openai.chat` 和 `openai.images`，登录流程会把当前 provider template 传入匹配上下文，避免只按 `openai` 协议误选图片或聊天模板。
5. 兜底能力保持保守，只补确定无害的字段，例如 `input_modalities: [text]`；上下文窗口、最大输出 token、reasoning effort 等强事实仍应由具体模型卡片或 endpoint 返回值提供。

部分聚合网关的 `/models` 会一次返回 OpenAI Chat、OpenAI Images、Anthropic Messages、Codex Responses 等不同 endpoint 的模型。首阶段处理原则是“同一 provider 配置只绑定一种 provider template”，不把不同 endpoint 的模型写进同一个 provider。

登录流程增加以下规则：

1. 根据当前登录协议和现有 provider 配置确定当前 provider template。
   - 如果 provider 已经有 `api_path` 或 `forward_url`，优先按路径匹配同协议的 template。
   - 如果没有显式路径，则按登录协议选择默认 template，例如 `openai -> openai.chat`、`anthropic -> anthropic.messages`、`codex -> codex.responses`。
2. 对 `/models` 返回的每个模型，用模型卡推荐 provider template。
   - 推荐时只看模型 id、alias、pattern、provider name 和 base URL hint，不因当前登录协议不同而跳过推荐。
   - 例如当前以 `openai` 登录时，`claude-sonnet-4-6` 仍会被识别为 `anthropic.messages`。
3. 如果模型推荐 template 与当前 provider template 一致，则保留并写入 `supported_models` / `model_capabilities`。
4. 如果推荐 template 与当前 provider template 不一致，则从当前 provider 写回中排除，并在结果的 `models_skipped_by_protocol` 中输出：
   - `model`
   - `current_provider_template`
   - `recommended_provider_template`
   - `current_protocol`
   - `recommended_protocol`
   - `reason`
5. 如果没有任何模型能保留，登录失败并提示被跳过模型的推荐 template，避免创建一个不可用 provider。
6. 如果模型没有命中任何卡片，也没有可靠 template 推荐，则保守地保留在当前 provider。未知模型不能仅凭名称被强行丢弃。

这意味着不同协议或不同 endpoint 通常仍应创建不同 provider 配置。例如：

- Anthropic Messages 模型使用独立 provider：`protocol: anthropic`、`api_path: /v1/messages`。
- OpenAI Chat Completions 模型使用独立 provider：`protocol: openai`、`api_path: /v1/chat/completions`。
- OpenAI Images 模型即使也是 `protocol: openai`，也应使用独立 provider：`api_path: /v1/images/generations`。
- Codex Responses 模型使用独立 provider：`protocol: codex`、`api_path: /v1/responses`。

关于 Codex path：内置 template 默认使用 adapter 标准路径 `/v1/responses`。当登录的 base URL 指向 ChatGPT Codex backend（例如包含 `chatgpt.com/backend-api/codex`）时，登录写回会使用 `/responses`，避免把 ChatGPT backend 和 OpenAI `/v1` API 混用。

## 合并策略

配置写回必须保护用户已有配置。建议按字段级别合并，而不是简单覆盖整个 `ModelCapabilitySpec`。

数据源优先级：

1. 现有 `config.yaml` 中已经存在的 `providers.items.<provider>.model_capabilities.<model>`。
2. models endpoint 返回的显式元数据，例如 `input_modalities`、`reasoning_efforts`、`max_context_tokens`。
3. 模型卡片模板中的元数据。
4. providercompat 提供的协议默认值，例如 Codex 默认 reasoning efforts。

解释：

- 用户已经手工写在配置里的值优先级最高，`aicli login` 不应无提示覆盖。
- endpoint 明确返回的事实字段优先于模板默认值。
- 模板用于补齐 endpoint 缺失的信息。
- providercompat 默认值是最后兜底，避免没有模板时能力为空。

不能直接复用当前 `mergeProviderLoginModelCapabilitySpec` 作为模型卡片合并函数。该函数当前语义是“update 有值就覆盖 base”，适合登录发现阶段补充能力，但不满足“已有用户配置优先”的模型卡片需求。应新增专用合并函数，例如：

```go
func MergeLoginModelCardCapability(existing, remote, card, compat agentconfig.ModelCapabilitySpec) agentconfig.ModelCapabilitySpec
```

字段级默认策略：

| 字段 | 默认合并策略 | 说明 |
| --- | --- | --- |
| `input_modalities` | existing wins；否则 remote；否则 card；否则 compat | 用户可能故意限制模态，不能被模板静默放宽。 |
| `native_tools.image_generation` | existing true wins；否则 remote true；否则 card true；否则 compat true | 首阶段只合并正向能力，false 与未声明不区分。 |
| `native_tools.images_generations_api` | existing true wins；否则 remote true；否则 card true；否则 compat true | 同上。 |
| `reasoning_model` | existing true wins；否则 remote true；否则 card true；否则 compat true | bool 零值无法区分 false 与未声明，首阶段只做正向合并。 |
| `reasoning_efforts` | existing wins；否则 remote；否则 card；否则 compat | 用户可能按 provider 实际限制收窄 effort 列表。 |
| `reasoning_effort_budgets` | existing wins；否则 remote；否则 card；否则 compat | budgets 是强约束，不能被模板覆盖。 |
| `default_reasoning_effort` | existing wins；否则 remote；否则 card；否则 compat | 保留用户偏好。 |
| `max_context_tokens` | existing wins；否则 remote；否则 card；否则 compat | 避免覆盖用户按代理实际窗口调小的值。 |
| `max_tokens` | existing wins；否则 remote；否则 card；否则 compat | 输出预算通常与 provider 策略有关。 |
| `auto_compact_ratio` | existing wins；否则 remote；否则 card；否则 compat | compact 策略属于本地运行策略。 |
| `auto_compact_token_limit` | existing wins；否则 remote；否则 card；否则 compat | 同上。 |
| `auto_compact_mode` | existing wins；否则 remote；否则 card；否则 compat | 同上。 |
| `supports_remote_compact` | existing true wins；否则 remote true；否则 card true；否则 compat true | 首阶段只做正向合并。 |
| `compact_reasoning_effort` | existing wins；否则 remote；否则 card；否则 compat | 保留用户对 compact 成本/质量的选择。 |

如果后续需要让用户刷新模板能力，应新增显式开关，例如 `--overwrite-model-capabilities` 或 `--refresh-model-cards`。没有显式开关时，模型卡片只能填空，不能静默覆盖已有字段。

推荐实现上可以把合并拆成两个函数：

```text
remoteSpec = capabilityFromProviderModelInfo(model)
cardSpec = catalog.Resolve(providerContext, model.ID)
compatSpec = providercompat default for protocol/model

merged = MergeLoginModelCardCapability(existingSpec, remoteSpec, cardSpec, compatSpec)
```

如果实现想更直观，也可以按“先构造低优先级默认，再用更高优先级填空”的方式：

```text
merged = empty
FillMissing(merged, existing config non-zero fields)
FillMissing(merged, remote explicit metadata)
FillMissing(merged, model card)
FillMissing(merged, compat defaults)
```

这里的 `FillMissing` 必须按字段判断“目标字段是否已设置”，不能简单把整个 struct 覆盖掉。

写回 key 必须使用 provider 返回的模型 id：

```yaml
providers:
  items:
    my_provider:
      supported_models:
        - gpt-5.4
      model_capabilities:
        gpt-5.4:
          input_modalities:
            - text
            - image
```

即使模型卡片用别名命中，也不要把 alias 写进 `supported_models` 或 `model_capabilities`。

## `aicli login` 接入流程

现有登录主流程位于 `runProviderLogin`。建议在 models 校验成功后接入：

```text
runProviderLogin
  -> validateProviderModels
  -> supportedModels = providerModelIDs(modelsResult.Models)
  -> load model card catalog
  -> discoveredCapabilities = buildProviderLoginModelCapabilities(...)
  -> cardCapabilities = resolve card capability for each returned model
  -> candidate.ModelCapabilities = field-level merge(existing, endpoint metadata, card, compat)
  -> buildProviderPersistenceUpdate
  -> UpdateProviderConfig
```

建议修改点：

- `providerLoginRequest` 增加可选字段：
  - `ModelCardCatalogPath string`
  - `DisableModelCards bool`
  - `ModelCardsStrict bool`
- `providerLoginResult` 增加只读输出字段：
  - `ModelCardsApplied []modelCardAppliedInfo`
  - `ModelCardsSkipped []modelCardSkippedInfo`
  - `ModelCardWarnings []modelCardWarning`
- CLI 增加可选参数：
  - `--model-cards <path>`：指定额外模型卡片目录。
  - `--no-model-cards`：禁用模板补齐，只使用 endpoint 和 providercompat。
  - `--model-cards-strict`：模型卡片解析失败时让登录失败；默认解析失败只警告并继续。

默认行为建议：

- 默认启用通过 `go:embed` 嵌入的内置 catalog；源码文件为 `backend/configs/model_cards.yaml`。
- 用户覆盖文件存在时自动加载 `~/.aicli/model_cards.yaml`。
- 用户通过 `--model-cards` 指定文件时，按“embedded builtin -> configured builtin_path -> 用户默认 -> 命令行指定”的顺序加载，后加载卡片可以用更高 `priority` 覆盖。
- `--dry-run` 仍然加载和匹配卡片，但不写入配置；JSON 输出中展示将应用的卡片。

## 配置文件扩展

首阶段不强制要求在 `config.yaml` 增加开关。为了让功能默认可用，内置卡片应从二进制内嵌资源加载，而不是从程序所在目录或仓库 starter config 相对路径解析。

如果需要显式配置，建议放在 `aicli.model_cards` 下：

```yaml
aicli:
  model_cards:
    enabled: true
    builtin_path: ""
    user_path: ~/.aicli/model_cards.yaml
    strict: false
```

对应 Go schema：

```go
type AICLIModelCardsConfig struct {
    Enabled     *bool  `yaml:"enabled" mapstructure:"enabled"`
    BuiltinPath string `yaml:"builtin_path" mapstructure:"builtin_path"`
    UserPath    string `yaml:"user_path" mapstructure:"user_path"`
    Strict      bool   `yaml:"strict" mapstructure:"strict"`
}
```

`enabled` 建议使用 `*bool`，这样可以区分“用户没有配置”和“用户明确关闭”。`builtin_path` 为空时使用 `go:embed` 内置 catalog；只有用户显式配置时才从文件系统读取额外 builtin 覆盖文件。

## 显式 false 能力的处理

现有 `ModelCapabilitySpec.NativeTools` 使用普通 bool 字段：

```go
type NativeToolCapabilities struct {
    ImageGeneration      bool
    ImagesGenerationsAPI bool
}
```

这会导致一个限制：`false` 和“未声明”在 Go 零值中无法区分。当前 `provider_persistence.go` 写 YAML 时也只会写入 `true` 的 native tool 字段。因此模型卡片如果需要明确表达“该模型不支持某个 native tool”，首阶段有两种选择：

- 保守方案：模型卡片只写正向能力，不把 `false` 作为必须落盘的事实。运行时按缺省 false 处理。
- 完整方案：新增模型卡片专用 tri-state schema，例如 `*bool`，并增强 persistence 支持显式 false 写回。

建议首阶段采用保守方案，因为运行时对未声明 native tool 的默认语义已经等价于不支持。只有当需要在 `model_capabilities.*` 通配能力里明确关闭某个工具时，再升级到完整方案。

如果升级完整方案，建议新增中间结构而不是直接破坏 `ModelCapabilitySpec`：

```go
type ModelCardNativeTools struct {
    ImageGeneration      *bool `yaml:"image_generation"`
    ImagesGenerationsAPI *bool `yaml:"images_generations_api"`
}
```

加载模型卡片时保留 tri-state，合并到配置写回时再决定是否需要把显式 false 写入 YAML。

## 模型卡片内容建议

首批内置卡片应优先覆盖仓库现有 `backend/configs/config.yaml` 中已经手工维护过的模型，避免引入未经验证的新假设。

建议从这些已有精确 capability 的模型开始抽取：

- Codex / OpenAI Responses 系列：
  - `gpt-5.2-codex`
  - `gpt-5.4`
  - `gpt-5.4-mini`
- Images Generations API 系列：
  - `gpt-image-1`
  - `gpt-image-1.5`
  - `gpt-image-2`
- DeepSeek / OpenAI-compatible reasoning 系列：
  - `deepseek-v4`
  - `deepseek-v4-flash`
  - `deepseek-ai/DeepSeek-V4-Pro`
- Mimo / Anthropic-compatible 系列：
  - `mimo-v2.5-pro`
  - `mimo-v2.5-pro-search`
- Gemini 系列：
  - `gemini-3.1-pro-high`
  - `gemini-3.1-pro-low`
- Sensenova 图片模型：
  - `sensenova-u1-fast`

待验证候选模型：

- `gpt-5.5`：当前配置中主要出现在 `supported_models` / `model_mappings`，缺少精确 `model_capabilities.gpt-5.5`。首阶段不要为它生成精确能力卡片，除非先补充明确来源或实测记录。

原则：

- 只迁移当前配置里已经存在、项目已使用的能力字段。
- 不凭模型名称猜测上下文窗口或 native tool 能力。
- 对 provider 明显相关的能力增加 `protocols` 或 `base_url_contains` 约束。
- 卡片 id 使用稳定命名，例如 `openai.gpt-5.4.codex`、`openai.gpt-image-2.images-api`。

## 示例：登录后写回结果

假设用户执行：

```powershell
aicli login --provider codex_foo --protocol codex-apikey --base-url https://api.openai.com --api-key sk-... --set-default
```

models endpoint 返回：

```json
{
  "data": [
    {"id": "gpt-5.4"},
    {"id": "gpt-5.4-mini"}
  ]
}
```

模型卡片命中后，写回配置应类似：

```yaml
providers:
  default_provider: codex_foo
  items:
    codex_foo:
      enabled: true
      protocol: codex
      base_url: https://api.openai.com
      auth_mode: api_key
      api_key_ref: providers.codex_foo.api_key
      models_path: /v1/models
      models_verified_at: "2026-05-12T08:30:00Z"
      default_model: gpt-5.4
      supported_models:
        - gpt-5.4
        - gpt-5.4-mini
      model_capabilities:
        gpt-5.4:
          max_context_tokens: 270000
          auto_compact_token_limit: 200000
          input_modalities:
            - text
            - image
          native_tools:
            image_generation: true
          reasoning_model: true
          reasoning_efforts:
            - none
            - low
            - medium
            - high
            - xhigh
        gpt-5.4-mini:
          max_context_tokens: 270000
          auto_compact_token_limit: 200000
          input_modalities:
            - text
            - image
          native_tools:
            image_generation: true
          reasoning_model: true
          reasoning_efforts:
            - none
            - low
            - medium
            - high
            - xhigh
```

注意：`supported_models` 只来自 provider 返回结果；模型卡片不会凭空增加 provider 未返回的模型。

## 实施阶段

### 阶段 1：模型卡片 catalog 与 loader

新增 `backend/configs/model_cards.yaml` 和 `backend/internal/modelcard`。

验收点：

- 能加载内置 YAML。
- 能加载多个 catalog 并按顺序合并。
- 能校验 `version`、`cards[].id`、`match.model_ids`、`capability`。
- 能输出稳定排序的命中结果。
- 模板解析失败时支持 strict / non-strict 两种模式。

### 阶段 2：接入 `aicli login`

在 `runProviderLogin` 中接入 catalog。

验收点：

- 登录返回模型 id 命中卡片时，写入 `model_capabilities.<model>`。
- 未命中卡片时维持现有行为。
- `--dry-run` 不写盘，但 JSON 输出包含将应用的卡片。
- `--no-model-cards` 完全跳过 catalog。
- 修改现有 provider 时保留用户已配置的能力字段。

### 阶段 3：完善 CLI/TUI 观测

扩展 `aicli login --output json` 和 TUI `/login` 提示信息。

建议输出：

```json
{
  "provider": "codex_foo",
  "supported_models": ["gpt-5.4"],
  "model_cards_applied": [
    {
      "model": "gpt-5.4",
      "card_id": "openai.gpt-5.4.codex",
      "fields": ["input_modalities", "native_tools.image_generation", "reasoning_efforts", "max_context_tokens"]
    }
  ]
}
```

普通文本输出只需要简洁提示：

```text
已应用模型卡片: gpt-5.4 <- openai.gpt-5.4.codex
```

warning 必须进入结构化结果，不能只写日志或 stdout。建议结构：

```json
{
  "model_card_warnings": [
    {
      "source": "~/.aicli/model_cards.yaml",
      "code": "parse_failed",
      "message": "failed to parse model card catalog"
    }
  ]
}
```

文本输出可以把 warning 追加在登录成功信息之后；JSON 输出必须保留 `model_card_warnings`，便于测试和自动化调用方判断模板是否完整应用。

### 阶段 4：显式 false 与 schema 增强

只有当实际遇到需要模板写入显式 false 的场景时再做。

验收点：

- 能区分 false 和未声明。
- `provider_persistence.go` 能保留 `native_tools.image_generation: false`。
- 不破坏现有 `ModelCapabilitySpec` 的 JSON/YAML 兼容读取。

## 测试计划

新增或扩展以下测试：

- `backend/internal/modelcard/catalog_test.go`
  - 加载合法 catalog。
  - 拒绝缺失 `version`、空 `id`、空匹配条件的卡片。
  - 精确 model id 命中。
  - alias 命中但写回 key 仍使用 provider 返回 id。
  - protocol 不匹配时跳过。
  - provider/base URL 约束更具体的卡片优先。
  - 多文件加载时后加载卡片可通过更高 priority 覆盖。
- `backend/cmd/aicli/commands/provider_login_test.go`
  - models 返回 `gpt-5.4` 时自动写入卡片能力。
  - models 返回未知模型时不写入模板能力。
  - 现有 provider 已有 `model_capabilities.gpt-5.4.max_context_tokens` 时不被模板覆盖。
  - 现有 provider 已有 `input_modalities`、`reasoning_efforts`、`reasoning_effort_budgets`、`native_tools`、`auto_compact_*`、`supports_remote_compact`、`compact_reasoning_effort` 时不被模板覆盖。
  - `--dry-run` 输出命中的模型卡片但不修改文件。
  - `--no-model-cards` 时保持当前 login 行为。
  - 非 strict 模式下 catalog 文件损坏时登录继续，`providerLoginResult.ModelCardWarnings` 有结构化 warning。
  - strict 模式下 catalog 文件损坏时登录失败且不写盘。
  - 安装形态不依赖源码目录：不提供 filesystem builtin path 时仍可从 embedded builtin catalog 匹配。
- `backend/internal/agentconfig/provider_persistence_test.go`
  - 写入模型卡片补齐后的 `model_capabilities` 时保持排序稳定。
  - 如阶段 4 实施，覆盖显式 false 写回。

回归命令：

```powershell
go test ./internal/modelcard ./cmd/aicli/commands ./internal/agentconfig
```

## 失败与回滚策略

- models endpoint 校验失败：沿用现有逻辑，登录失败，不加载或写入模型卡片。
- 模型卡片加载失败：
  - 默认 non-strict：输出 warning，继续使用现有 endpoint/providercompat 能力。
  - strict：登录失败，不写 config。
- 单个模型卡片不合法：建议 strict 下整体失败，non-strict 下跳过该卡并记录 warning。
- 写 config 失败：沿用现有 `UpdateProviderConfig` 错误返回，内存更新也不应被视为持久成功。
- 用户想回滚：删除对应 provider 的 `model_capabilities` 或使用 `--no-model-cards` 重新 login。

## 兼容性与迁移

对现有用户配置的影响：

- 没有模型卡片命中时，行为与当前 login 一致。
- 已有 `model_capabilities` 的 provider 不会被整段替换，合并时保留用户配置。
- `supported_models`、`default_model`、`models_verified_at` 仍由现有 login 逻辑控制。
- 模型卡片不会改变 `model_mappings`，避免影响当前模型路由。

迁移建议：

1. 从 `backend/configs/config.yaml` 中抽取重复模型能力到 `backend/configs/model_cards.yaml`。
2. 保留 `config.yaml` 里现有 provider 能力，不做自动删除。
3. 新登录 provider 逐步开始依赖模型卡片补齐。
4. 后续确认稳定后，再考虑提供 `aicli model-cards migrate` 之类的清理命令，但不作为本需求的一部分。

## 风险与约束

- 模型 id 不是全局唯一语义。同一个 id 在不同 provider、不同协议、不同代理后端下可能能力不同，所以卡片必须支持 `protocols`、`provider_names`、`base_url_contains` 约束。
- endpoint 返回字段可能不完整，也可能与模板冲突。合并策略必须可解释，并在 JSON 输出里展示模板命中结果。
- `ModelCapabilitySpec` 当前无法区分 false 和未声明。首阶段不要依赖模板写入负向 bool 事实。
- 模型卡片会让配置写回内容变多，测试需要覆盖 YAML 排序稳定性，避免不必要的 diff。
- Windows 下不要用超长内联命令批量生成大 YAML。内置卡片文件应按小补丁维护。

## 验收标准

- 仓库存在版本化的 `backend/configs/model_cards.yaml` 模板文件。
- 内置 catalog 通过 `go:embed` 随 `aicli` 二进制发布，安装后不依赖源码树路径。
- `aicli login` 在 models 返回 id 命中模板时，会把模板能力合并写入 `providers.items.<provider>.model_capabilities.<returned_model_id>`。
- 登录返回 JSON 能展示哪些模型应用了哪些模型卡片。
- 登录返回 JSON 能展示模型卡片加载或解析 warning。
- `--dry-run`、`--no-model-cards`、strict/non-strict 行为可测试。
- 对已有 provider 的用户自定义能力字段不做静默覆盖。
- 现有 provider login、models parsing、provider persistence 单元测试仍全部通过。

## 推荐结论

建议按“内置模型卡片 catalog + 登录时按返回模型 id 补齐 capability + 保留用户配置优先级”的方式实施。

这个方案能最大化复用现有 `model_capabilities` schema 和 provider 局部写回机制，避免新增一套运行时能力解析路径。模型卡片是配置生成辅助层，最终运行时仍只读取 `config.yaml` 中已经落盘的 provider/model capability，因此对聊天、图片生成、compact、模型切换等路径的影响面可控。
