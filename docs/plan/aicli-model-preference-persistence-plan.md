# aicli 模型选择偏好持久化与 /model 增强方案

日期：2026-05-01

当前状态（2026-05-09）：

- `aicli.chat` 偏好 schema 已实现，包含 `default_provider`、`default_model`、`reasoning_effort`、`stream`。
- 启动优先级已落地：flag > loaded session > `aicli.chat` > interactive > provider default。
- `/model` 已迁到 `backend/cmd/aicli/commands/chat_model_command.go`，支持跨 provider/model/reasoning 切换、能力校验、运行态刷新和偏好持久化。
- 本文中“没有 chat 默认模型偏好”“`/model` 无法跨 provider/持久化”“扩展 `chat_model_switch.go`”等表述是实现前背景，不再代表当前代码事实。

## 目标

优化 `aicli chat` 的启动交互，避免每次新开 CLI 都重复选择 provider、model 和 thinking/reasoning effort。

目标行为：

- 用户首次没有配置过模型偏好时，沿用当前交互逻辑选择 provider、model、thinking/reasoning effort。
- 用户完成交互选择后，把选择保存到当前有效的 `config.yaml`。
- 后续打开 `aicli chat` 时，在未通过命令行参数显式指定的情况下，优先读取配置中的用户偏好。
- `/model` 不再只做当前 provider 下的 model 切换，应支持查看和修改 provider、model、thinking/reasoning effort，并把修改后的偏好保存回配置文件。
- 会话历史和会话管理必须保持协议无关。OpenAI、Anthropic、Gemini、Codex 等协议差异只允许出现在请求构造、响应解析、工具调用格式转换等 adapter/provider 边界。

## 实现前代码结论

以下内容记录实现前入口与缺口，当前状态以本文开头“当前状态（2026-05-09）”为准。

相关入口：

- `backend/cmd/aicli/main.go` 负责解析 `--config`，按 `$HOME/.aicli/config.yaml -> ./.aicli/config.yaml -> ./aicli.yaml -> ./configs/config.yaml` 搜索配置。
- 历史状态下 `backend/internal/agentconfig/config.go` 的 `AICLIConfig` 只有 `mcp/log/retry/timeout/theme`，没有 chat 默认模型偏好；当前已经新增 `chat`。
- `backend/cmd/aicli/commands/chat_options.go` 负责解析 chat flags，并提供 `resolveChatProviderName`、`resolveChatModelName`、`resolveChatStreamMode`。
- `backend/cmd/aicli/commands/chat_bootstrap.go` 的 `prepareChatRuntimeState` 组合 provider、model、reasoning effort、adapter、baseURL。
- 历史状态下 `/model` 实现只支持当前 provider 下切换 model；当前主实现已迁到 `backend/cmd/aicli/commands/chat_model_command.go` 并支持跨 provider/model/reasoning 切换。
- `backend/cmd/aicli/commands/command.go` 已经用 `commandMatches(cmdLower, "/model")` 做精确匹配，避免 `/model` 被 `/mode` 误伤。
- `backend/cmd/aicli/commands/chat_provider_turn.go` 在每轮请求时把统一 `types.Message` 转成协议消息，并通过 `session.Adapter.BuildRequest` / `HandleResponse` 做协议适配。
- `backend/internal/chatcore` 和 `backend/internal/types.Message` 已经提供较好的统一会话抽象基础，但 `ChatSession.Messages []map[string]interface{}` 与 `aicli_raw_message_json` 仍然把协议形态带回了 CLI 会话层。

实现前缺口：

- 历史状态下新会话没有读取“用户上次选择”的独立配置，只能从历史会话恢复，或者重新交互选择。
- 历史状态下 `config.yaml` 没有模型偏好 schema，也没有局部 YAML 写回能力。
- 历史状态下 `ChatSession` 没有保存完整 `cfg` 或配置文件路径，`/model` 无法跨 provider 解析和持久化。
- `/model` 跨 provider 切换不只是改字段，还要刷新 adapter、baseURL、HTTP client、function schema builder、capability/reasoning 校验。
- adapter 请求配置没有完整传入 `ReasoningEffortBudgets`、`ReasoningModel`、`Thinking` 等协议相关能力，Anthropic/Gemini 等 thinking 配置需要在请求层补齐，而不是让会话层感知协议细节。

## 配置方案

在 `agentconfig.AICLIConfig` 下新增 chat 偏好配置：

```yaml
aicli:
  chat:
    default_provider: nvidia
    default_model: gpt-5.4-mini
    reasoning_effort: medium
    stream: true
```

字段说明：

- `default_provider`：用户在 `aicli chat` 中选择的默认 provider 名称。
- `default_model`：用户在该 provider 下选择的默认 model。保存“用户输入/选择的模型名”还是“映射后的模型名”需要统一，建议保存映射后的实际请求模型，避免下次启动展示与请求不一致。
- `reasoning_effort`：内部字段名沿用现有 `--reasoning-effort` 和 `ReasoningEffort`。UI 文案可以显示为 thinking effort，但配置字段不建议新增 `thinking_effort` 别名，避免和未来完整 `thinking` 对象混淆。
- `stream`：对应交互式“选择输出模式”和 `--stream` flag。使用 `*bool` 区分“未配置”、“显式 true”、“显式 false”三种状态；写回时按 `omitempty` 处理，nil 时不出现在配置文件里。

Go 类型建议：

```go
type AICLIConfig struct {
    MCP     *AICLIMCPConfig     `yaml:"mcp" mapstructure:"mcp"`
    Log     *AICLILogConfig     `yaml:"log" mapstructure:"log"`
    Retry   *AICLIRetryConfig   `yaml:"retry" mapstructure:"retry"`
    Timeout *AICLITimeoutConfig `yaml:"timeout" mapstructure:"timeout"`
    Theme   *AICLIThemeConfig   `yaml:"theme" mapstructure:"theme"`
    Chat    *AICLIChatConfig    `yaml:"chat" mapstructure:"chat"`
}

type AICLIChatConfig struct {
    DefaultProvider string `yaml:"default_provider" mapstructure:"default_provider"`
    DefaultModel    string `yaml:"default_model" mapstructure:"default_model"`
    ReasoningEffort string `yaml:"reasoning_effort" mapstructure:"reasoning_effort"`
    Stream          *bool  `yaml:"stream,omitempty" mapstructure:"stream"`
}
```

配置写回原则：

- 不要 `yaml.Marshal(cfg)` 整体重写配置。当前配置大量使用 `${ENV:-default}` 模板，整体 marshal 会展开环境变量、丢注释、重排字段，甚至可能把密钥写成明文。
- 新增局部 YAML 更新工具，只修改 `aicli.chat` 子树，尽量保留原文件其它内容。
- 写文件前保留原权限，采用临时文件 + rename。失败时不影响当前会话继续运行，只打印 warning。
- 如果 `--config` 指定了路径，优先写该路径。
- 如果未指定 `--config` 且当前解析到了某个配置文件，MVP 写当前有效配置文件。
- 如果没有任何配置文件，不建议创建只有 `aicli.chat` 的 `$HOME/.aicli/config.yaml`，因为当前配置加载是“首个命中文件”而不是分层 merge，创建局部配置会导致 providers 丢失。若要写用户级偏好，应先实现分层配置合并。

## 启动解析优先级

新会话启动时按以下优先级解析：

1. 命令行参数：`--provider`、`--model`、`--reasoning-effort` 永远最高优先级，且默认不写回配置，避免一次性命令污染默认偏好。
2. 显式恢复/加载的历史会话：`--session`、`--resume`、`/load` 应优先使用会话 metadata 中的 provider/model/reasoning effort，保证历史会话可复现。
3. `aicli.chat` 用户偏好：只在未通过命令行指定且不是历史会话恢复时使用。
4. 交互选择：只有对应字段没有配置、配置失效、或当前 provider/model 不可用时才弹出选择。
5. provider 默认值：非交互模式或无法交互时，退回 `providers.default_provider` 和 provider 的 `default_model`。

建议调整：

- `resolveChatProviderName` 增加读取 `cfg.AICLI.Chat.DefaultProvider` 的分支，并校验 provider 存在且 enabled。
- `resolveChatModelName` 增加读取 `cfg.AICLI.Chat.DefaultModel` 的分支。配置模型不强制要求出现在 `supported_models`，因为当前交互也允许自定义模型名，但必须走 `resolveProviderExecutionContext` 统一应用 model mapping。
- `prepareChatRuntimeState` 在 reasoning 解析时增加 `cfg.AICLI.Chat.ReasoningEffort` 作为非显式来源。
- 记录本轮哪些字段来自交互选择。只有发生交互选择或 `/model` 修改时才写回配置。
- 如果配置中的 reasoning effort 不被当前模型支持，显式参数应报错；配置偏好应 warning 后清空或重新交互选择，避免静默发送无效请求。

## /model 命令增强

保留兼容行为：

- `/model`：交互式修改 provider、model、reasoning effort，并保存为新的默认偏好。
- `/model <name>`：兼容当前行为，在当前 provider 下切换 model，并保存偏好。
- `/model status` 或 `/model --status`：显示当前 provider、protocol、model、reasoning effort、baseURL。

新增建议语法：

```text
/model --provider <provider> --model <model> --reasoning-effort <effort>
/model --provider <provider>
/model --model <model>
/model --reasoning-effort <effort>
/model clear-reasoning
```

实现要点：

- slash 命令不要引入复杂 shell 语法；实现一个小型参数 parser 即可，支持 `--key value` 和 `--key=value`。
- provider 切换必须复用 `resolveProviderExecutionContext` 或同等逻辑，不能只改 `session.ProviderName`。
- 跨 provider 切换后必须刷新 `ProviderName`、`Provider`、`Adapter`、`Model`、`ReasoningEffort`、`BaseURL`、`HTTPClient`、function schema builder、logger 元数据、runtime session metadata 和 UI 状态栏。
- 跨 provider 切换后不应重写历史消息。历史仍然保持统一 `types.Message` 抽象，下一轮请求时再由新 adapter 转成目标协议。
- 如果新 provider 的协议不支持当前历史中的某些结构，应该在 `RuntimeMessagesToProtocolMessages` 或 adapter sanitizer 中降级/过滤，而不是在 `/model` 命令里做协议特殊处理。

## 协议适配与会话抽象边界

目标边界：

- 会话层：只保存统一抽象，包括 `types.Message`、`types.ToolCall`、`types.ReasoningBlock`、`types.ContentPart`、token usage 和 provider/model metadata。
- 请求层：根据当前 provider protocol 把统一消息转换为 OpenAI/Anthropic/Gemini/Codex 请求体。
- 响应层：adapter 把协议响应转换回统一 assistant message、tool calls、reasoning block、usage。

当前需要约束和后续重构：

- `ChatSession.Messages []map[string]interface{}` 是历史包袱。短期可继续通过 `buildRuntimeHistoryFromAICLIMessages` 和 `buildAICLIMessagesFromRuntimeHistory` 过渡，但新增逻辑不要直接依赖协议字段。
- `aicli_raw_message_json` 会把协议响应形态保存在 message metadata 中。它对 Codex replay 有现实作用，但长期应拆成标准化 metadata，例如 reasoning block、tool calls、generated image metadata，而不是保存整段协议原文。
- `RuntimeMessagesToProtocolMessages` 是协议转换的正确位置。OpenAI 工具消息顺序、Anthropic tool_result、Gemini parts、Codex output items 都应继续集中在这里和 adapter 内处理。
- `adapterRequestConfig` 应从模型能力中填充 `ReasoningModel`、`ReasoningEffortBudgets`，并在需要时传入统一 `Thinking` 配置。Anthropic/Gemini 的 thinking budget/adaptive thinking 不应泄露到会话管理代码。

## 文件级调整清单

- `backend/internal/agentconfig/config.go`：新增 `AICLIChatConfig`，并给 `Config` 增加非 YAML 字段记录 source path，或提供等价的配置路径访问能力。
- `backend/cmd/aicli/main.go`：在 `InitGlobalConfig(configPath)` 后保留最终解析路径，确保 commands 层可写回同一个 `config.yaml`。
- `backend/cmd/aicli/commands/chat_options.go`：扩展 provider/model/reasoning 解析逻辑，加入用户偏好来源和“是否需要交互/是否需要保存”的状态。
- `backend/cmd/aicli/commands/chat_bootstrap.go`：在 `prepareChatRuntimeState` 完成校验和交互后，按需保存偏好。`ChatSession` 构建时保存 `cfg` 或可用于 `/model` 的 provider catalog。
- `backend/cmd/aicli/commands/chat_model_command.go`：当前统一 provider/model/reasoning 切换入口；保留 model-only 快捷路径；成功后保存配置。
- `backend/cmd/aicli/commands/command.go`：更新 `/help` 文案，明确 `/model` 可修改 provider/model/thinking effort。
- `backend/cmd/aicli/commands/chat_provider_turn.go`：补齐 adapter request config 的 model capability 信息，保证不同协议只在请求/响应层分化。
- `backend/internal/llm/reasoning_helpers.go`：继续作为统一消息到协议消息的转换边界；后续减少对 raw protocol JSON 的依赖。

## 测试计划

- provider/model/reasoning 启动解析优先级：flags > loaded session > `aicli.chat` > interactive > provider default。
- 已配置偏好时，新会话不再触发 provider/model/reasoning 选择。
- 配置缺失任一字段时，只对缺失字段交互选择，并写回完整偏好。
- 配置 provider 不存在或 disabled 时，交互重新选择并覆盖旧偏好。
- 配置 reasoning effort 不适配当前模型时，交互模式重新选择，非交互模式 warning 后清空或报错策略保持一致。
- `/model <name>` 保持兼容，切换 model 后保存配置。
- `/model --provider p --model m --reasoning-effort high` 能跨 provider 切换，并刷新 adapter、baseURL、HTTP client、function builder。
- `/model` 跨协议切换后，历史会话仍使用统一 `types.Message`，下一轮请求由 adapter 生成对应协议 payload。
- YAML 写回只修改 `aicli.chat`，保留环境变量模板、其它配置字段和文件权限。

## 推荐实施顺序

1. 增加配置 schema、配置路径记录、局部 YAML 写回工具及单元测试。
2. 改启动解析优先级，只处理新会话默认偏好，不碰 `/model`。
3. 在启动交互选择完成后保存偏好。
4. 增强 `/model` 参数解析和交互流程，先支持当前 provider 下保存，再支持跨 provider。
5. 补齐跨 provider 切换后的 session 状态刷新和测试。
6. 收敛协议边界：补 adapter request capability，新增跨协议历史 replay 测试。
7. 后续重构 `ChatSession.Messages` 为 `[]types.Message`，逐步移除 `aicli_raw_message_json` 对会话层的污染。详细拆分见 [`aicli-chat-session-messages-decouple-plan.md`](./aicli-chat-session-messages-decouple-plan.md)。

## 风险与取舍

- 直接写当前有效配置文件是 MVP 最小改动，但可能修改仓库内 `configs/config.yaml`。如果要真正做到用户级偏好，应先做分层配置 merge，否则创建 `$HOME/.aicli/config.yaml` 会遮蔽项目配置。
- `/model` 跨 provider 会改变后续请求协议，但不应主动转换已有历史。历史转换必须延迟到请求边界，否则会把协议复杂度带入会话层。
- thinking/reasoning 命名需要保持克制。当前内部字段是 `ReasoningEffort`，UI 可展示 thinking effort；如果未来支持完整 `thinking` 对象，应新增结构化配置，而不是把 `reasoning_effort` 改名。

## 实施结果

本次实施已经落地以下能力：

- `aicli.chat` 偏好已接入启动流程，`default_provider`、`default_model`、`reasoning_effort`、`stream` 会在未显式指定参数时生效。
- 交互式首次选择会在启动 bootstrap 成功后写回当前有效 `config.yaml`，且写回仅更新 `aicli.chat` 子树，不会整文件重写。
- 启动交互“选择输出模式”所选的 stream 取值会一并写回 `aicli.chat.stream`，下次启动若未传 `--stream` 也未恢复历史会话，会直接使用该偏好，不再二次询问。
- `/model` 已扩展为统一偏好修改入口，支持查看状态、修改 provider/model/reasoning effort、清空 reasoning，并在成功后落盘。
- 启动偏好写回已移动到最终校验之后，避免 `--output json` 与 `--stream` 等启动级错误污染配置。
- 会话层仍保持协议无关，协议差异继续限制在请求构造、响应解析和 adapter 边界。

## 兼容性说明

当前实现对 reasoning-effort 的处理保留了一项兼容策略：

- 如果 provider/model 已声明 reasoning capability 且值不在支持集内，非显式来源会 warning 后清空，显式 `/model --reasoning-effort` 会报错。
- 如果 provider/model 没有声明 reasoning capability，继续按原值透传，避免对现有未补齐 capability metadata 的 provider 造成破坏性行为。

这意味着，文档里“配置偏好应 warning 后清空或重新交互选择”的目标在实现上被收敛为“对已知支持集严格校验，对未知能力保持兼容透传”。这是当前版本的刻意取舍。

## 后续建议

- 如果后续要进一步收紧 reasoning 行为，应先为更多 provider/model 补齐 capability 元数据，再把“不声明 capability 时透传”逐步收敛成默认清空或强制交互。
- 如果后续要避免写回项目级 `configs/config.yaml`，应优先做分层配置合并，再把偏好落到用户级配置目录。
