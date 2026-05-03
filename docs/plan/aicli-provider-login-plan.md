# aicli Provider Login 功能计划与方案分析

日期：2026-05-02

状态：implemented-mvp

复审日期：2026-05-02

## 文档定位

本文档同时承担三类用途：

- 需求澄清：把“新增 provider 登录”和“修改现有 provider 登录凭证”拆成可验收流程。
- 实施方案：定义 CLI/TUI 入口、models 校验、配置写回、Codex OAuth、凭证安全等实现边界。
- MVP 复审：记录当前实现已经覆盖的能力、仍需保留的限制和后续 backlog。

因此，文档中的“建议”“计划”描述表示设计约束；“实施记录”“MVP 覆盖矩阵”“实施文件索引”表示当前代码库中的落地状态。

## 实施记录

已按本计划完成 MVP 实施：

- 新增 `aicli login` 子命令，支持新增 provider、修改现有 provider、API key 登录、Codex OAuth device-code 登录、`--models-path`、`--default-model`、`--set-default`、`--dry-run` 和 `--output json`。
- 新增 chat TUI `/login` slash 命令，复用同一套 provider login service，并在修改当前 provider 或显式 `--switch` 时刷新当前 chat session。
- 新增 provider 级 YAML 局部写回工具，写回 `providers.items.<name>` 时保留无关 top-level section 和未修改字段。
- 新增独立 models 校验与标准化逻辑，不复用 chat completion path，覆盖 OpenAI-compatible、Gemini、Codex 以及通用 `data/models/items` 响应。
- 新增 provider schema 字段 `auth_mode`、`auth_ref`、`models_path`、`models_verified_at`。
- 新增用户级 OAuth auth store，Codex OAuth token 写入 `$HOME/.aicli/auth.json`，`config.yaml` 只保存 `auth_mode` 与 `auth_ref`。
- 新增单元测试覆盖 provider 写回、auth store、models 解析、API key 登录、Codex API key、Codex OAuth 成功与失败不落盘等路径。

本次实现未覆盖完整 browser OAuth flow 和 Codex app-server 长驻兼容；当前 OAuth 路径为 device-code MVP。

## MVP 覆盖矩阵

| 原始需求点 | 文档覆盖 | MVP 状态 | 继续关注点 |
| --- | --- | --- | --- |
| 新增 provider 时选择协议 | 已覆盖协议矩阵与交互流程 | 已支持 `openai`、`anthropic`、`gemini`、`codex-apikey`、`codex-oauth` | 后续新增协议时应只扩展 resolver 和模型解析，不复制 login service |
| 新增 provider 时输入名称、`base_url`、`api_key` | 已覆盖新建流程和必填规则 | API key 模式已支持；OAuth 模式使用 `auth_ref` | API key 仍沿用明文 `config.yaml`，未来可升级为 secret ref |
| 提交后测试凭证并请求 `/models` | 已覆盖 endpoint、header、响应标准化和错误分类 | 已实现独立 models 校验，不复用 chat completion path | Anthropic/代理 models endpoint 差异需要依赖 `--models-path` 兜底 |
| 返回 models 并更新 `config.yaml` | 已覆盖 `supported_models`、`default_model`、`models_verified_at` | 已实现 provider 级 YAML 局部写回 | 写 auth store 成功但写 config 失败时仍可能留下 OAuth token 记录 |
| 修改现有 provider，`base_url`/`api_key` 可选 | 已覆盖保留原值、按需覆盖和失败不落盘 | 已支持现有 provider 编辑、保留 API key 和重拉 models | 如果现有 key 是环境变量模板，交互修改时需避免误写展开值 |
| `aicli login` 子命令 | 已覆盖命令面和 flags | 已实现并注册 | `--yes` 当前只表达跳过确认，不跳过 models 校验 |
| chat TUI `/login` 命令 | 已覆盖 slash 命令、参数解析和会话刷新 | 已接入同一 login service，并支持刷新当前 session | API key 输入必须持续验证“不进历史、不进 transcript、不回显” |
| Codex 细分 `codex-apikey` | 已覆盖协议映射与 models resolver | 已写入 runtime `protocol: codex`、`auth_mode: api_key` | ChatGPT 后端和 OpenAI-compatible base URL 需要保持 resolver 回归测试 |
| Codex 细分 `codex-oauth` 并参考 `E:\projects\ai\codex` | 已覆盖 device-code、auth store、分阶段 browser/app-server | 已实现 device-code MVP 和用户级 auth store | browser OAuth flow 与 Codex app-server 长驻兼容仍是后续阶段 |

## 实施文件索引

- CLI 入口：`backend/cmd/aicli/commands/login.go`，注册点：`backend/cmd/aicli/main.go`。
- TUI 入口：`backend/cmd/aicli/commands/chat_login_command.go`，路由：`backend/cmd/aicli/commands/command.go`。
- Slash catalog 与补全：`backend/cmd/aicli/commands/chat_slash_command_catalog.go`，`backend/cmd/aicli/commands/chat_slash_argument_completion.go`。
- 共享登录流程：`backend/cmd/aicli/commands/provider_login.go`。
- Models 校验与解析：`backend/cmd/aicli/commands/provider_models.go`。
- Codex OAuth device-code：`backend/cmd/aicli/commands/provider_oauth.go`。
- Provider schema：`backend/internal/agentconfig/config.go`。
- Provider YAML 局部写回：`backend/internal/agentconfig/provider_persistence.go`。
- OAuth auth store：`backend/internal/agentconfig/auth_store.go`。
- 关键测试：`backend/cmd/aicli/commands/provider_login_test.go`，`backend/cmd/aicli/commands/provider_models_test.go`，`backend/internal/agentconfig/provider_persistence_test.go`，`backend/internal/agentconfig/auth_store_test.go`。

## 方案完整性审查结论

原文档已经覆盖了用户提出的主流程：新增 provider、修改 provider、`aicli login`、chat TUI `/login`、凭证校验、写回 `config.yaml`，以及 Codex 的 `codex-apikey` / `codex-oauth` 拆分。

复审结论：文档主干完整，可以指导实现；本次优化主要补齐“实现状态、文件索引、需求覆盖矩阵、遗留边界”四类信息，避免后续读者把计划阶段缺口误认为当前缺口。

立项时原文档仍偏概要，直接进入实现时会遇到几个缺口：

- `GET /models` 不能直接复用 chat adapter 的 `GetAPIPath()`，必须新增按协议解析的 models endpoint。
- 不同协议的 models 响应结构不同，需要先定义标准化模型目录格式。
- `api_key` 可以沿用现有 provider schema，但 OAuth 凭证不能写入普通 `api_key` 字段。
- CLI 与 TUI 必须共享同一个 login service，否则配置写回、模型解析和错误处理会分叉。
- 需要明确配置路径、默认 provider、当前 chat session 刷新、密钥脱敏、失败回滚和测试验收边界。

本版文档在保留原目标的基础上补齐这些实施级约束。

## 目标

为 `aicli` 增加统一的 `login` 能力，用于新增 provider 或修改现有 provider 的登录凭证，并在登录成功后完成模型发现、配置落盘和运行时刷新。

目标行为：

- 支持 `aicli login` 子命令。
- 支持 `aicli chat` TUI 内的 `/login` slash 命令。
- 新增 provider 时，先选择协议，再输入 provider 名称、`base_url`、`api_key` 等信息。
- 修改现有 provider 时，先选择现有 provider，再按需修改 `base_url` 和 `api_key`。
- 登录前必须验证凭证有效，默认通过 `GET /models` 或等价的 provider models 端点完成校验。
- 登录成功后更新 `config.yaml`，并返回模型列表信息。
- Codex 需要拆成 `codex-apikey` 和 `codex-oauth` 两条路径，其中 `codex-oauth` 的授权流参考 `E:\projects\ai\codex`。

## 范围与非目标

本计划覆盖：

- provider API key 登录。
- provider OAuth 登录的配置和运行时边界。
- CLI 与 TUI 的共享 login 流程。
- models endpoint 校验、模型标准化、配置写回。
- 登录后当前 chat session 的可选刷新。

本计划暂不覆盖：

- provider group、provider queue、路由策略的自动重排。
- 多账号密钥池管理 UI。
- 完整 secret manager 集成。
- 运行时服务端 Web UI 的登录入口。
- 自动迁移所有现有 provider 配置到新 auth schema。

## 立项时代码结论

现有仓库已经具备一部分可复用基础：

- `backend/cmd/aicli/main.go` 已经会解析 `--config`，并把最终配置路径交给 `internal/agentconfig.InitGlobalConfig`。
- `backend/internal/agentconfig/config.go` 里的 `Config` 已经包含 `ConfigFilePath`，这意味着命令层可以知道当前有效配置文件。
- `backend/internal/agentconfig/chat_persistence.go` 已经实现了基于 `yaml.Node` 的局部 YAML 写回，且只更新 `aicli.chat` 子树。
- `backend/cmd/aicli/commands/chat_preferences.go` 和 `chat_stream_command.go` 证明了“先改内存状态，再按需写回 config 文件”的模式是可行的。
- `backend/cmd/aicli/commands/test.go` 已经有 provider 级请求构造、header 组装和 HTTP client 复用，可作为登录验证的参考。
- `backend/cmd/aicli/commands/command.go` 已经有 chat slash 命令分发机制，新增 `/login` 只需要接入现有路由和帮助/补全目录。
- `backend/cmd/aicli/commands/chat_slash_command_catalog.go` 已经是命令目录化结构，适合把 `/login` 纳入统一 catalog。

立项时缺口：

- 还没有 provider 级别的局部写回工具，无法安全更新 `providers.items.<name>` 而不重写整份 YAML。
- 还没有统一的“provider 登录”流程对象，CLI 与 TUI 若各写一套，会很快分叉。
- `Provider` 结构只有 `api_key/base_url/protocol/default_model/supported_models` 这类字段，尚未为 OAuth 凭证建立独立承载。
- 还没有 `GET /models` 级别的标准化校验入口。

MVP 实施已经补齐上述四项缺口：

- 新增 `UpdateProviderConfig` 做 provider 级 YAML 局部写回。
- 新增 `runProviderLogin` 作为 CLI 和 TUI 的共享登录服务。
- 新增 `auth_mode`、`auth_ref`、`models_path`、`models_verified_at` provider 字段。
- 新增 `validateProviderModels` 做独立 models 校验与响应标准化。

## 核心架构

建议新增一个命令层共享的 login service，而不是在 `aicli login` 和 `/login` 中分别实现。

建议模块：

- `backend/cmd/aicli/commands/login.go`：Cobra 子命令入口、flags 解析、结构化输出。
- `backend/cmd/aicli/commands/chat_login_command.go`：chat `/login` 入口，只负责把 session 和用户输入转交给 login service。
- `backend/cmd/aicli/commands/provider_login.go`：登录核心流程，处理创建/修改 provider、交互步骤编排、结果渲染。
- `backend/cmd/aicli/commands/provider_models.go`：models endpoint 请求、响应解析、标准化。
- `backend/internal/agentconfig/provider_persistence.go`：provider 级 YAML 局部写回。
- `backend/internal/agentconfig/provider_persistence_test.go`：配置写回单元测试。

核心数据流：

```text
CLI flags or TUI input
  -> provider login request
  -> build candidate provider config
  -> validate credentials via models endpoint
  -> normalize models
  -> select default_model
  -> write providers.items.<name>
  -> update in-memory cfg
  -> optionally refresh active chat session
```

关键原则：

- 所有入口共享同一个 `providerLoginRequest` / `providerLoginResult`。
- 登录校验必须发生在写盘之前。
- 登录成功后的配置写回和内存更新必须使用同一份标准化结果。
- 输出中默认脱敏 `api_key`，JSON 结构也不返回原始 key。

## 协议支持矩阵

MVP 建议先支持这些协议：

| 登录协议 | Provider `protocol` | Auth mode | Models endpoint | 备注 |
| --- | --- | --- | --- | --- |
| `openai` | `openai` | `api_key` | `/v1/models` | 兼容 OpenAI-compatible 网关。 |
| `anthropic` | `anthropic` | `api_key` | `/v1/models` 或显式覆盖 | Anthropic 模型列表能力在代理实现中差异较大，需要允许 `--models-path`。 |
| `gemini` | `gemini` | `api_key` | `/v1beta/models` | 请求头使用 `x-goog-api-key`。 |
| `codex-apikey` | `codex` | `api_key` | OpenAI-compatible `/v1/models` 或 ChatGPT backend `/backend-api/codex/models` | 取决于 base URL 指向 OpenAI API 还是 ChatGPT Codex 后端。 |
| `codex-oauth` | `codex` | `oauth` | ChatGPT backend `/backend-api/codex/models` 或已包含 `/backend-api/codex` 时的 `/models` | 当前 MVP 为 device-code + auth store，不依赖 app-server 长驻进程。 |

说明：

- “登录协议”是用户选择项；Provider `protocol` 是现有请求 adapter 使用的协议字段。
- `codex-apikey` 和 `codex-oauth` 是登录方式差异，不建议拆成两个 runtime protocol。
- 所有协议都必须允许高级用户通过参数或交互输入覆盖 models endpoint。

## 需求拆解

### 1. 新 provider 登录

建议流程：

1. 选择协议，候选来自当前项目支持的 provider 协议。
2. 输入 provider 名称。
3. 输入 `base_url`。
4. 输入 `api_key`。
5. 用临时 provider 配置发起 models 校验。
6. 解析返回的模型列表。
7. 选择或确认 `default_model`。
8. 落盘到 `config.yaml`。

建议写入内容：

- `providers.items.<name>.enabled = true`
- `providers.items.<name>.protocol`
- `providers.items.<name>.base_url`
- `providers.items.<name>.api_key`
- `providers.items.<name>.supported_models`
- `providers.items.<name>.default_model`

注意点：

- 新 provider 不要在校验成功前写盘。
- 不要全量 `yaml.Marshal(cfg)` 重写整份配置。
- `supported_models` 应来自真实 models 端点，而不是用户手工输入。
- 如果返回模型为空，应该视为登录失败或不完整配置。

### 2. 修改现有 provider

建议流程：

1. 从现有 provider 列表中选择 provider。
2. 允许只改 `base_url` 或只改 `api_key`，两者都可留空表示保持原值。
3. 重新做 models 校验。
4. 如果校验通过，更新 `supported_models` 和必要的默认模型信息。
5. 保存到原 provider 节点。

注意点：

- 只改用户明确填写的字段，其他 provider 属性保持不变。
- 如果修改后当前 `default_model` 不再出现在返回列表中，应提示用户重新选默认模型。
- 如果当前会话正绑定该 provider，登录成功后需要刷新 session 里的 provider execution context。

## Models 校验与模型标准化

登录校验的核心不是“能发出 HTTP 请求”，而是“能用提交的凭证拿到可用模型目录”。因此应新增独立的 models 校验逻辑，不复用 chat completion path。

### Endpoint 解析

建议优先级：

1. 命令行或交互显式输入的 `models_path`。
2. provider 配置中未来新增的 `models_path`。
3. 按登录协议推导默认值。

建议默认值：

- `openai`：`/v1/models`
- `anthropic`：`/v1/models`
- `gemini`：`/v1beta/models`
- `codex-apikey`：根据 `base_url` 判断，OpenAI-compatible API 使用 `/v1/models`；ChatGPT Codex 后端使用 `/backend-api/codex/models`；如果 base URL 已经包含 `/backend-api/codex`，则使用 `/models`。
- `codex-oauth`：当前 MVP 不引入 app-server 长驻进程，默认走 ChatGPT Codex backend models endpoint；未来接入 app-server 后可优先走 `model/list`。

URL 拼接应复用或抽取 `BuildUpstreamURLWithPath` 风格的逻辑，避免 `base_url` 尾斜杠、`api_path` 和相对路径组合错误。

### Header 解析

建议复用 adapter 的 `BuildHeaders` 逻辑，但 models 请求不需要 request body。

默认 headers：

- `openai` / `codex-apikey`：`Authorization: Bearer <api_key>`
- `anthropic`：`x-api-key: <api_key>`，`anthropic-version: 2023-06-01`
- `gemini`：`x-goog-api-key: <api_key>`
- `codex-oauth`：由 OAuth auth provider 生成 Authorization 或 session headers，不从 `api_key` 读取。

### 响应标准化

建议内部标准结构：

```go
type providerModelInfo struct {
    ID                  string
    DisplayName         string
    InputModalities     []string
    ReasoningEfforts    []string
    MaxContextTokens    int
    SupportsRemoteCodex bool
    Raw                 map[string]interface{}
}
```

解析策略：

- OpenAI-compatible：优先读取 `data[].id`。
- Gemini：读取 `models[].name`，保存时可去掉 `models/` 前缀，或同时保留原始 name 和展示名。
- Codex models endpoint：读取 `models[].slug`，并尽量同步 reasoning effort、input modalities、context window 等能力信息。
- Anthropic：当前 MVP 默认仍要求目标服务或代理提供可解析的 models endpoint；如果后续允许 fallback 到用户输入模型列表，必须把登录结果标记为 `models_verified=false` 或等价状态，避免误认为凭证已完成端到端验证。

### 写入 supported_models 和 default_model

写入规则：

- `supported_models` 使用标准化后的模型 ID，按返回顺序去重。
- `default_model` 优先保留现有值，前提是它仍在新模型列表中。
- 新 provider 默认选择第一个可用模型，但交互模式应允许用户选择。
- 非交互模式下，如果没有 `--default-model` 且模型列表非空，使用第一项。
- 如果模型列表为空，默认视为校验失败。

### 错误处理

必须区分这些失败类型：

- 网络错误。
- TLS / 代理错误。
- 401 / 403 凭证错误。
- 404 endpoint 错误。
- 2xx 但响应结构不可解析。
- 2xx 且结构可解析但模型为空。

输出中应显示 endpoint、HTTP status、错误摘要，但不能显示完整 `api_key`。

## 命令面设计

建议把同一套核心流程暴露成两个入口：

- `aicli login`
- `aicli chat` 内的 `/login`

推荐的最小命令面：

```text
aicli login [--provider <name>] [--protocol <protocol>] [--mode apikey|oauth]
            [--base-url <url>] [--api-key <key>] [--models-path <path>]
            [--default-model <model>] [--output text|json] [--yes]

/login
/login <provider>
/login --provider <name> --base-url <url>
```

建议语义：

- `aicli login` 默认走交互式向导。
- 不传 `--provider` 时显示“新建 provider / 修改 provider”的选择。
- 传入不存在的 `--provider` 时视为新建。
- 传入已存在的 `--provider` 时视为编辑。
- 新 provider 必须指定协议。
- 修改现有 provider 时默认不改协议。
- `--output json` 不进入交互，缺少必要参数时直接失败并返回结构化错误。
- `--yes` 只跳过确认，不跳过凭证校验。

推荐 flags：

- `--provider, -p`：provider 名称。
- `--protocol`：`openai|anthropic|gemini|codex-apikey|codex-oauth`。
- `--mode`：`apikey|oauth`，可由 `--protocol codex-oauth` 推断。
- `--base-url`：provider base URL。
- `--api-key`：API key。交互模式下输入应隐藏回显。
- `--models-path`：覆盖模型列表 endpoint。
- `--default-model`：非交互模式指定默认模型。
- `--set-default`：登录成功后同步更新 `providers.default_provider`。
- `--dry-run`：只校验，不写配置。
- `--output` / `--json`：复用现有结构化输出风格。

TUI `/login` 行为：

- `/login` 无参数时进入交互向导。
- `/login <provider>` 进入编辑指定 provider。
- `/login --provider p --base-url u` 支持轻量参数解析，解析规则可复用 `/model` 的 token parser。
- TUI 环境优先使用 `FixedBottomSurface.ShowPopupInput`，不支持时退回 legacy prompt。
- 登录期间应暂停普通 chat prompt，避免输入队列把凭证误当成用户消息。
- API key 输入不能进入 scrollback、日志和 runtime session history。

## Codex 特殊处理

Codex 不应被简单当成“另一个带 key 的普通 provider”。

建议拆成两条登录路径：

- `codex-apikey`
- `codex-oauth`

对齐 `E:\projects\ai\codex` 的参考结论：

- Codex 上游已经把认证模式拆成 `apiKey`、`chatgpt`、`chatgptDeviceCode`。
- `apiKey` 是显式 API key 登录。
- `chatgpt` 是浏览器 OAuth 登录。
- `chatgptDeviceCode` 是设备码登录。

对 aicli 的建议映射：

- `codex-apikey`：直接走 API key 校验和 `GET /models`。
- `codex-oauth`：作为独立 auth mode，不要硬塞进 `api_key` 字段。
- `codex-oauth` 下再区分 browser flow 和 device-code flow。

实现建议：

- `codex-apikey` 可以复用普通 provider 登录向导。
- `codex-oauth` 需要独立的 OAuth 状态机和取消/完成通知语义。
- 如果 OAuth token 需要持久化，优先单独存储，不建议把 refresh token 明文写进 `config.yaml`。
- `config.yaml` 只保存 provider 选择、auth mode 标记和必要的引用信息更稳妥。

### Codex OAuth 分阶段方案

建议不要在第一阶段完整复制 `E:\projects\ai\codex` 的 app-server 体系，而是分阶段接入。

Phase 1：Codex API key

- 支持 `codex-apikey`。
- 使用现有 `CodexAdapter`。
- 凭证校验走 models endpoint。
- 配置写入 `protocol: codex` 和 `auth_mode: api_key`。

Phase 2：OAuth auth store

- 新增用户级 auth store。
- 定义 `auth_ref` 到 token 数据的映射。
- 实现 token 读取、过期判断和刷新接口。
- Provider runtime 请求时按 `auth_mode=oauth` 从 auth store 取 bearer token。

Phase 3：Device code login

- 优先实现 `chatgptDeviceCode`，因为它不依赖本地浏览器回调端口。
- CLI 显示 `verificationUrl` 和 `userCode`。
- 等待完成事件或轮询 token endpoint。
- 成功后保存 auth store，再拉 models。

Phase 4：Browser login

- 实现本地 callback server。
- 打开浏览器或输出 auth URL。
- 支持取消和超时。
- 成功后保存 auth store，再拉 models。

Phase 5：Codex app-server 兼容

- 如果未来本仓库接入 Codex app-server，可直接复用其 `account/login/start`、`account/login/completed`、`account/read`、`model/list` 语义。
- 在未引入 app-server 前，aicli 应只实现必要 OAuth 子集，不引入额外长期驻留进程。

### Codex models 对齐

参考 Codex 上游，models client 会请求相对路径 `models`，在 ChatGPT 后端场景最终可落到 `/backend-api/codex/models`，并可能附带 `client_version` 查询参数。aicli 需要把这一点做成 codex-specific resolver，而不是硬编码普通 OpenAI `/v1/models`。

Codex models 响应中除了模型 ID，还可能包含：

- reasoning effort 选项。
- input modalities。
- context window。
- hidden / availability 信息。
- model migration / upgrade 信息。

MVP 可以先写 `supported_models` 和 `default_model`；后续再把高价值字段同步到 `model_capabilities`。

## 配置与落盘

建议新增 provider 级局部更新工具，风格参考 `UpdateAICLIChatPreferences`：

- 只更新 `providers.items.<name>` 子树。
- 保留其它 top-level section、注释和环境变量模板。
- 使用 `yaml.Node` 做局部 patch。
- 使用临时文件 + 原子替换写盘。

推荐的更新边界：

- 新 provider：创建或补全 provider 节点。
- 现有 provider：只更新用户修改过的字段。
- 登录验证失败：不写盘。
- 登录成功但写盘失败：保留内存态，打印 warning。

### Provider 写回结构

API key provider 的最小写回示例：

```yaml
providers:
  items:
    my_openai:
      enabled: true
      protocol: openai
      base_url: https://api.openai.com
      api_key: sk-...
      supported_models:
        - gpt-5.4
        - gpt-5.4-mini
      default_model: gpt-5.4-mini
```

OAuth provider 建议写回引用，不直接写 token：

```yaml
providers:
  items:
    codex_oauth:
      enabled: true
      protocol: codex
      base_url: https://chatgpt.com/backend-api/codex
      auth_mode: oauth
      auth_ref: codex_oauth_default
      supported_models:
        - gpt-5.4
      default_model: gpt-5.4
```

需要新增的 provider 字段建议：

- `auth_mode`：`api_key|oauth`。为空时按兼容逻辑视为 `api_key`。
- `auth_ref`：OAuth 凭证引用名，不保存 secret 本体。
- `models_path`：可选，覆盖默认 models endpoint。
- `models_verified_at`：可选，记录最近一次模型校验时间。

这些字段加入 `agentconfig.Provider` 时应保持 `omitempty` 语义，避免现有配置产生无意义字段。

### 凭证安全

API key：

- 当前仓库已有 `api_key` 明文配置模式，MVP 可以沿用。
- 交互输入必须隐藏回显。
- 日志、错误、JSON 输出都只显示脱敏值，例如 `sk-...abcd`。
- 后续可以支持 `api_key_ref`，把 key 写入专门 secret store，但这不应阻塞 MVP。

OAuth：

- refresh token、access token、id token 不写入 `config.yaml`。
- 建议落到用户级 auth 文件，例如 `$HOME/.aicli/auth.json` 或复用 Codex auth 存储。
- `config.yaml` 只保存 `auth_mode` 和 `auth_ref`。
- auth 文件权限应在 Windows/Unix 下尽量限制当前用户可读。

### 配置路径策略

写回目标：

- `--config` 指定时写该文件。
- 未指定时写 `cfg.ConfigFilePath`。
- 如果 `cfg.ConfigFilePath` 为空，交互模式下应提示无法保存；非交互模式直接失败。

不建议在没有完整配置加载能力前自动创建 `$HOME/.aicli/config.yaml`，因为当前配置搜索是“首个存在文件生效”，创建局部配置可能遮蔽项目内完整 provider 配置。

## 文件级调整建议

建议优先关注这些位置：

- `backend/internal/agentconfig/config.go`：如果 OAuth 需要新增 auth mode / reference 字段，这里是 provider 配置 schema 的入口。
- `backend/internal/agentconfig/chat_persistence.go`：provider 级 YAML 局部写回可以沿用这里的实现风格，但建议抽出共享 YAML helper，避免复制 `parseYAMLDocument` / `writeFileAtomic`。
- `backend/internal/agentconfig/provider_persistence.go`：新增 provider 局部写回工具。
- `backend/cmd/aicli/main.go`：注册 `aicli login` 子命令。
- `backend/cmd/aicli/commands/login.go`：新增 CLI login 入口。
- `backend/cmd/aicli/commands/provider_login.go`：新增共享登录流程。
- `backend/cmd/aicli/commands/provider_models.go`：新增 models 校验与解析。
- `backend/cmd/aicli/commands/command.go`：在 chat 命令路由中接入 `/login`。
- `backend/cmd/aicli/commands/chat_login_command.go`：新增 TUI `/login` 入口。
- `backend/cmd/aicli/commands/chat_slash_command_catalog.go`：把 `/login` 纳入命令目录和补全源。
- `backend/cmd/aicli/commands/chat_setup.go`：登录成功后如果当前 session 绑定同一 provider，这里需要协助刷新运行态。
- `backend/cmd/aicli/commands/test.go`：复用 provider HTTP 构造、headers 和 client 逻辑做 models 校验。

## 运行时刷新

登录成功后，如果当前 chat session 正在使用被修改的 provider，需要刷新：

- `ProviderName`
- `Provider`
- `Adapter`
- `Model`
- `BaseURL`
- `HTTPClient`
- `FunctionBuilder`
- logger 元数据
- 状态栏显示
- runtime session metadata
- `cfg.Providers.Items[providerName]` 内存态

这部分建议复用 `/model` 已经在做的刷新思路，避免登录和模型切换出现两套状态同步逻辑。

刷新规则：

- 如果 `/login` 新增 provider，默认只保存配置，不自动切换当前 chat provider。
- 如果 `/login <当前 provider>` 修改当前 provider，则成功后刷新当前会话。
- 如果用户显式指定 `--switch`，登录成功后切换当前 chat 会话到该 provider。
- 切换 provider 时必须重新调用 provider execution context 解析，应用 model mapping。
- 刷新失败不能回滚已经成功写入的配置，但必须显示 warning。
- 登录过程不能改写历史消息，下一轮请求仍由 adapter 在请求边界转换消息。

## 建议实施顺序

1. 抽取 YAML 局部更新 helper，保留现有 `UpdateAICLIChatPreferences` 行为不变。
2. 实现 provider 级 YAML 局部写回工具和单元测试。
3. 实现 models endpoint resolver、HTTP 校验和响应标准化。
4. 实现通用 login service，先覆盖 `openai` API key。
5. 接入 `aicli login` 子命令和 JSON 输出。
6. 接入 chat TUI `/login`，复用同一 login service。
7. 补充 `anthropic`、`gemini` 的 models 校验差异。
8. 补充 Codex `codex-apikey`。
9. 增加当前 chat session 刷新和 `--switch`。
10. 实现 Codex OAuth auth store 和 device-code flow。
11. 视需要补 browser flow 和 app-server 兼容。
12. 补齐 `/help`、slash command catalog、补全和文档。

## 测试计划

单元测试：

- 新 provider 登录成功后，`config.yaml` 正确写入 provider 节点。
- 修改现有 provider 时，只改用户输入的字段，其他字段不变。
- 登录失败时，配置文件不发生变化。
- `GET /models` 校验失败时，不能落盘。
- YAML 写回保留环境变量模板和无关 top-level section。
- `api_key` 在文本输出、JSON 输出、错误和日志中脱敏。
- models 响应解析覆盖 OpenAI-compatible、Gemini、Codex 格式。
- `default_model` 选择规则覆盖保留、重选、非交互第一项、空列表失败。

命令测试：

- `aicli login --output json` 缺少必填参数时返回结构化错误。
- `aicli login --dry-run` 校验成功但不写盘。
- `aicli login --provider existing` 编辑现有 provider。
- `aicli login --provider new --protocol openai` 创建新 provider。
- `--models-path` 覆盖默认 endpoint。

TUI 测试：

- `/login` 无参数进入交互向导。
- `/login <provider>` 编辑现有 provider。
- 当前 chat provider 被修改后刷新状态栏、BaseURL、HTTPClient、logger metadata。
- API key 输入不进入 session history 和 chat transcript。

Codex 测试：

- `codex-apikey` 使用 Codex adapter，校验 models 后写入 `protocol: codex`。
- `codex-oauth` device-code flow 能保存 auth store 引用。
- OAuth token 不写入 `config.yaml`。
- OAuth 取消、超时和失败路径不会写 provider 配置。

回归测试：

- 现有 `aicli config --models` 仍能读取登录写入的 `supported_models`。
- 现有 `/model` 切换仍能使用登录写入的 provider。
- `aicli chat --provider <new>` 能用新 provider 启动。

## MVP 验证记录

当前实现至少应通过以下 login 相关测试集：

```text
go test ./internal/agentconfig
go test ./cmd/aicli/commands
go test ./cmd/aicli
```

已覆盖的关键断言：

- Provider YAML 写回可以新建 provider、局部编辑 provider，并在 OAuth provider 中移除 `api_key`。
- Auth store 可以保存和读取用户级 OAuth token 记录，默认路径为 `$HOME/.aicli/auth.json`。
- Models 解析覆盖 OpenAI-compatible、Gemini 和 Codex 响应，并覆盖空模型列表失败。
- API key 登录成功后写入 `supported_models`、`default_model`、`models_path`、`models_verified_at`。
- Models 校验失败时不写 provider 配置。
- 编辑现有 provider 时，空 API key 输入表示保留旧值。
- `codex-apikey` 写入 runtime `protocol: codex` 和 `auth_mode: api_key`。
- `codex-oauth` 成功时 token 写入 auth store，`config.yaml` 只保存 `auth_mode` 和 `auth_ref`。
- `codex-oauth` models 校验失败时不写 auth store、不写 provider 配置。
- `/login` 参数解析支持 provider、protocol、base URL、models path、`--set-default` 和 `--switch`。
- TUI `/login` 的 API key 输入使用 secret prompt；测试覆盖不写入 InputBox history、不消费 shared reader，并在密钥输入前丢弃已排队的普通输入。

全量回归建议仍执行 `go test ./...`，但如果失败，需要先判断失败包是否与 login 改动相关。当前文档不把其它模块既有失败作为 provider login 的验收阻塞项。

## 风险与取舍

- 直接整文件重写 `config.yaml` 会破坏环境变量模板和注释，必须避免。
- OAuth 凭证不适合直接塞进普通 YAML 明文配置。
- 不同 provider 的 `models` 端点和请求头可能不同，必须通过适配层或统一的 provider client 处理。
- 登录后是否自动切换当前 chat 会话到新 provider，属于额外 UX 决策，建议不要和凭证保存绑死在一起。
- Anthropic 官方/代理 models endpoint 差异较大，MVP 应允许用户覆盖 `models_path`，并允许显式模型列表作为受限 fallback。
- Gemini 返回模型名通常带 `models/` 前缀，需要统一展示名和请求名，否则后续 chat 可能请求错误模型。
- Windows 下交互输入和命令长度需要遵守仓库 AGENTS.md 的保守限制，复杂 OAuth 脚本不要塞进长 `powershell -Command`。
- 如果配置文件中 provider 使用 `${ENV}` 形式的 `api_key`，编辑时默认不应把展开后的真实 key 写回覆盖模板，除非用户明确输入新 key。
- TUI `/login` 的 API key 输入安全不仅要求“不进历史”，还要求“不回显、不进入 scrollback、不进入 InputQueue 的普通消息路径”。这需要持续以专门测试覆盖。
- 当前 OAuth 保存顺序是先通过 token 拉取 models，再保存 auth store 和 provider 配置；如果保存 auth store 成功但 provider 配置写回失败，用户级 auth store 可能留下孤立 token 记录。该风险不影响配置一致性，但后续可增加失败回滚或垃圾清理。
- `--yes` 不应跳过 models 校验；如果未来增加二次确认，它只能跳过确认步骤。
- `--output json` 不应返回原始 API key 或 OAuth token；新增字段时必须默认脱敏。

## 验收标准

功能验收：

- `aicli login` 可以新建一个 OpenAI-compatible provider，校验 `/v1/models`，写入 `supported_models` 和 `default_model`。
- `aicli login --provider <existing>` 可以修改现有 provider 的 `base_url` 或 `api_key`，校验失败时配置不变。
- `aicli chat` 内 `/login` 可以复用同一流程，当前 provider 被修改后当前会话可继续下一轮请求。
- `codex-apikey` 路径可写入 `protocol: codex` provider。
- `codex-oauth` 至少有明确的 auth store、配置引用和 device-code 实施路径。

安全验收：

- API key 不出现在 stdout 的普通日志、JSON 输出、chat transcript、runtime session history、InputBox history 和测试快照中。
- CLI 交互模式 API key 使用隐藏输入；TUI `/login` 也必须使用等价的 secret prompt，不应通过普通 transient prompt 回显。
- OAuth refresh token 不写入 `config.yaml`。
- 写配置使用临时文件和原子替换，失败不产生半截 YAML。

兼容验收：

- 未使用 login 的现有 provider 配置无需迁移即可继续工作。
- `aicli config`、`aicli test`、`aicli chat` 和 `/model` 仍能读取旧字段。
- `aicli.chat` 偏好写回行为不因抽取 YAML helper 发生变化。

## 后续 Backlog

这些事项不阻塞 MVP，但应作为后续迭代入口：

- 实现 Codex browser OAuth flow，包括本地 callback server、浏览器打开、取消和超时处理。
- 如果项目引入 Codex app-server，再对齐 `account/login/start`、`account/login/completed`、`account/read`、`model/list`。
- 增加 `api_key_ref` 或 secret store，把 API key 从 `config.yaml` 明文迁移到用户级凭证存储。
- 为 Anthropic 或其它不稳定 models endpoint 的 provider 增加显式模型列表 fallback，并明确 `models_verified=false` 语义。
- 对 OAuth auth store 增加 token 过期检测、refresh token 刷新和孤立记录清理。
- 扩展 `model_capabilities`，把 Codex `reasoning_efforts`、`input_modalities`、`max_context_tokens` 等模型能力写入配置或运行态缓存。

## 结论

这个功能可以落地，且和当前代码库的配置局部写回、provider 请求构造、chat slash 命令体系是兼容的。

推荐实施路径是：先完成 API key provider 登录和 provider 级 YAML 局部写回，再接入 CLI/TUI 两个入口，随后补 Codex API key，最后分阶段实现 Codex OAuth。这样能先交付最常用的登录能力，同时把 OAuth token 存储、device-code/browser flow、Codex app-server 兼容这些复杂点隔离在后续阶段。
