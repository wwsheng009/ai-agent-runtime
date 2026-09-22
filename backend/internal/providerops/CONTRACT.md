# providerops —— provider 模型发现/分类/元数据共享核心（冻结契约）

状态：**冻结**（2026-09-18）。改动 API 需同时更新 aicli micro web client 与
runtime server 两个调用方。

## 背景与目标

`aicli micro web client`（`backend/cmd/aicli/commands/web/`，后端逻辑在
`backend/cmd/aicli/commands/`）已经实现了三件事：

1. **自动导入分类**：给出 `name` + `base_url`(+`api_key`) 后自动探测协议、
   拉取 `/models`、按 model card / provider template 把模型分组归类，生成
   provider 的 `protocol` / `api_path` / `forward_url` / `supported_models` /
   `default_model` 等字段。
2. **按 api-key 获取模型列表 ID**：`validateProviderModels` 拉 `/models`
   并解析出模型清单（含 display_name / input_modalities / reasoning_efforts /
   max_context_tokens 等端点自带元数据）。
3. **自动匹配模型元数据**：`buildProviderFetchModelMetadata` 用
   `/models` 元数据 → model card → 协议兼容默认值的优先级链，产出前端
   reasoning 编辑器可直接回显的 `ModelCapabilitySpec`。

runtime server（`backend/internal/api/skills/`）的 provider 编辑器需要同样能力。
**硬性要求：两个入口共用同一套后端核心逻辑，不允许复制实现。**

## 约束

- `backend/cmd/aicli/commands` 与 `backend/internal/api/skills` 之间存在
  **测试级 import 环**（`commands/chat_runtime_server_test.go` 导入
  `internal/api/skills`），因此 `skills` 不能直接导入 `commands`，反之亦然。
  共享核心必须落在第三个包：`backend/internal/providerops`。
- 共享包只能依赖低层包：`internal/agentconfig`、`internal/modelcard`、
  `internal/llm/adapter`、`internal/pkg/httpclient` 等；**不得**依赖
  `commands`、`skills`、cobra、TUI。
- `commands` 侧通过 **类型别名 + 函数转发** 保持既有调用点（CLI login、
  micro web client handlers、大量测试）零改动或不破坏编译。
- 移动必须保持行为等价：现有测试即回归基线。

## Go API（冻结）

包路径：`github.com/wwsheng009/ai-agent-runtime/internal/providerops`

### 模型清单

```go
type ModelInfo struct {
	ID                  string                 `json:"id"`
	DisplayName         string                 `json:"display_name,omitempty"`
	InputModalities     []string               `json:"input_modalities,omitempty"`
	ReasoningEfforts    []string               `json:"reasoning_efforts,omitempty"`
	MaxContextTokens    int                    `json:"max_context_tokens,omitempty"`
	SupportsRemoteCodex bool                   `json:"supports_remote_codex,omitempty"`
	Raw                 map[string]interface{} `json:"-"`
}

func ModelIDs(models []ModelInfo) []string
func APIKey(provider agentconfig.Provider) string
func NormalizeLoginProtocol(protocol, mode string) string
func RuntimeProtocolForLoginProtocol(protocol string) string
func IsAutoLoginProtocol(protocol string) bool
func LoginProtocolFromProvider(provider agentconfig.Provider, authMode string) string
func NormalizeModelsForProtocol(models []ModelInfo, loginProtocol string) []ModelInfo
```

来源：`commands/provider_models.go`（`providerModelInfo`、`providerModelIDs`、
`providerModelsAPIKey`、`normalizeLoginProtocol`、`runtimeProtocolForLoginProtocol`、
`isAutoLoginProtocol`、`loginProtocolFromProvider`、
`normalizeProviderLoginModelsForProtocol`）。

### 拉取模型列表

```go
type FetchModelsRequest struct {
	Config        *agentconfig.Config
	ProviderName  string
	Provider      agentconfig.Provider
	LoginProtocol string
	ModelsPath    string
	Timeout       time.Duration
}

type FetchModelsResult struct {
	Endpoint   string      `json:"endpoint"`
	StatusCode int         `json:"status_code"`
	Models     []ModelInfo `json:"models"`
	VerifiedAt string      `json:"verified_at"`
}

// FetchModels 等价于 commands.validateProviderModels。
// ctx 用于请求取消；即使传 nil 也必须安全（等价 context.Background）。
func FetchModels(ctx context.Context, req FetchModelsRequest) (*FetchModelsResult, error)

// ModelsEndpointAllowsAnonymous 等价于 commands.modelsEndpointAllowsAnonymous。
func ModelsEndpointAllowsAnonymous(client *http.Client, endpoint, loginProtocol string) bool
```

### 分类（自动导入同源）

```go
type ModelGroup struct {
	RuntimeProtocol  string   `json:"protocol"`
	LoginProtocol    string   `json:"login_protocol,omitempty"`
	ProviderTemplate string   `json:"provider_template,omitempty"`
	HasTemplate      bool     `json:"has_template"`
	Primary          bool     `json:"primary"`
	Models           []string `json:"models"`
	OtherModels      []string `json:"other_models,omitempty"`
}

type Classification struct {
	LoginProtocol   string
	RuntimeProtocol string
	Groups          []ModelGroup
	PrimaryModels   []ModelInfo
	VerifiedModels  []ModelInfo
	AssumedModels   []ModelInfo
	TotalModels     int
}

type ClassifyRequest struct {
	ProviderName           string
	RequestedLoginProtocol string // 可为 auto
	GroupingLoginProtocol  string // 必须是具体协议
	Provider               agentconfig.Provider
	Models                 []ModelInfo
	Config                 *agentconfig.Config
}

// Classify 等价于 commands.classifyProviderFetchedModels，nil 表示分类不可用。
func Classify(req ClassifyRequest) *Classification

// ResolveFetchModelsLoginProtocol 等价于 commands.resolveProviderFetchModelsLoginProtocol。
func ResolveFetchModelsLoginProtocol(requested string, provider agentconfig.Provider) string
```

### 元数据匹配

```go
type ModelMetadata struct {
	ID                     string   `json:"id"`
	Name                   string   `json:"name"`
	ReasoningModel         bool     `json:"reasoning_model"`
	ReasoningEfforts       []string `json:"reasoning_efforts,omitempty"`
	DefaultReasoningEffort string   `json:"default_reasoning_effort,omitempty"`
	CompactReasoningEffort string   `json:"compact_reasoning_effort,omitempty"`
	MaxContextTokens       int      `json:"max_context_tokens,omitempty"`
	MaxTokens              int      `json:"max_tokens,omitempty"`
}

type MetadataRequest struct {
	ProviderName  string
	LoginProtocol string
	Provider      agentconfig.Provider
	Models        []ModelInfo
	Config        *agentconfig.Config
}

// MatchMetadata 等价于 commands.buildProviderFetchModelMetadata 的「覆盖重匹配」
// 语义（刻意不把已保存的 ModelCapabilities 作为合并基底）；无元数据的模型不出现
// 在返回值中，由调用方决定是否清空旧配置。
func MatchMetadata(req MetadataRequest) map[string]ModelMetadata

func ModelCapabilityIsEmpty(spec agentconfig.ModelCapabilitySpec) bool

// BuildModelCapabilities 返回 provider 编辑器可直接写入 config 的
// model_capabilities（providerLoginModelCapabilitySpec 的完整 spec，而不是
// MatchMetadata 的前端字段视图）。MatchMetadata 应在内部复用它，两者必须是
// 同一次匹配的不同投影：元数据匹配为空的模型不出现在返回值中。
func BuildModelCapabilities(req MetadataRequest) map[string]agentconfig.ModelCapabilitySpec
```

### model card 目录与模板默认值

```go
type ModelCardWarning struct {
	ProviderTemplate string `json:"provider_template,omitempty"`
	Message          string `json:"message,omitempty"`
}

// LoadModelCardCatalog 等价于 commands.loadProviderLoginModelCardCatalog。
func LoadModelCardCatalog(cfg *agentconfig.Config) (*modelcard.Catalog, []ModelCardWarning, error)

type TemplateDefaults struct {
	APIPath        string
	ForwardURL     string
	SupportTypes   []string
	MaxTokensLimit int
}

// ProviderTemplateDefaults 等价于 commands.providerLoginProviderTemplateDefaults。
func ProviderTemplateDefaults(provider agentconfig.Provider, template modelcard.ProviderTemplate) TemplateDefaults

// ResolveProviderTemplate 等价于 commands.resolveProviderLoginProviderTemplate。
func ResolveProviderTemplate(catalog *modelcard.Catalog, runtimeProtocol string, provider agentconfig.Provider) (modelcard.ProviderTemplate, bool)
```

### 模型探测（协议矩阵）

```go
type ProbeRequest struct {
	Config         *agentconfig.Config
	ProviderName   string
	Provider       agentconfig.Provider
	LoginProtocol  string
	Models         []ModelInfo
	Protocols      []string
	Timeout        time.Duration
	MaxConcurrency int
}

type ProbeResult struct {
	ModelID    string `json:"model_id"`
	Protocol   string `json:"protocol"`
	Verdict    string `json:"verdict"`
	Message    string `json:"message,omitempty"`
	StatusCode int    `json:"status_code,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
}

// ProbeModels 等价于 commands.runProviderModelProbes。字段名可沿用现有语义，
// 但必须导出且可 JSON 序列化。
func ProbeModels(req ProbeRequest) []ProbeResult
```

### fetch-models 复用面（models / classify 薄转发目标，2026-09-18 补充）

runtime server `fetch-models` / `auto-import` 链路所需的底层助手，本体在
`providerops`，`commands` 侧仅保留别名 + 转发（方案A）。

```go
// models.go —— /models 拉取与解析助手
func BuildProviderModelsHeaders(provider config.Provider, loginProtocol string) map[string]string
func ResolveProviderModelsPath(loginProtocol string, provider config.Provider, override string) string
func DefaultModelsPath(loginProtocol, baseURL string) string
func BuildProviderModelsURL(provider config.Provider, modelsPath string) (string, error)
func ParseProviderModelsResponse(raw []byte, loginProtocol string) ([]ModelInfo, error)
func DedupeProviderModels(models []ModelInfo) []ModelInfo
func NormalizeProviderModelID(id, loginProtocol string) string
func DedupeProviderStringOptions(values []string) []string
func ResponsePreview(raw []byte, limit int) string

// classify.go —— login 分类链路的 fetch-models 复用入口
func ClassifyFetchedModels(providerName string, provider config.Provider,
	requestedLoginProtocol, groupingLoginProtocol string, models []ModelInfo,
	cfg *config.Config) *Classification
func SplitProviderFetchPrimaryModels(catalog *modelcard.Catalog,
	providerName, loginProtocol string, provider config.Provider,
	group LoginModelGroup) (verified, assumed []ModelInfo)
func ProviderFetchModelHasCardBacking(catalog *modelcard.Catalog,
	ctx modelcard.Context, modelID string, group LoginModelGroup) bool
func ProviderFetchGroupModelsOutsidePrimary(models []ModelInfo,
	primaryIDs map[string]struct{}) []string
func MatchFetchedModelMetadata(providerName, loginProtocol string,
	provider config.Provider, models []ModelInfo,
	cfg *config.Config) map[string]ChatWebConfigModel
```

`ChatWebConfigModel`（`capabilities.go`）与 `LoginModelGroup`（`login.go`）
为既有类型，字段见源码；`MatchFetchedModelMetadata` 返回的是前端 reasoning
编辑器可直接回显的配置视图（覆盖语义，已保存 capabilities 不参与合并）。

### /models 解析类别（category，2026-09-22 补充）

不同网关的 `/models` 载荷形状差异很大：OpenAI 兼容网关是扁平键，OpenRouter
及同族聚合网关把元数据嵌在 `architecture` / `reasoning` / `top_provider` /
`supported_parameters` 子对象里。类别解析器把「形状差异」收敛到一处，通用
链路（收集 → 去重 → 能力投影）保持不变。

```go
// models_category.go —— 类别注册表与探测
type ModelListCategory string
const (
	ModelListCategoryGeneric    ModelListCategory = "generic"    // 扁平键：OpenAI 兼容 / Codex / vLLM / 自建站
	ModelListCategoryOpenRouter ModelListCategory = "openrouter" // 嵌套：OpenRouter 及同族聚合网关
)

// DetectModelListCategory 先按 provider 名称 / base_url 命中，再按载荷形状探测
// （只看前 5 条），都无法判定时回退 generic；raw 为空或无法解码也回退 generic。
func DetectModelListCategory(providerName, baseURL string, raw []byte) ModelListCategory
func ModelListCategoryName(category ModelListCategory) string

// ParseProviderModelsResponseForCategory 按显式类别解析；类别为空或未注册时
// 按通用扁平形状解析（等价 ParseProviderModelsResponse）。
func ParseProviderModelsResponseForCategory(raw []byte, loginProtocol string,
	category ModelListCategory) ([]ModelInfo, error)

// models_openrouter.go —— openrouter 类别逐条解析（包内私有，由注册表引用）
// openRouterEntryMatches(item) bool                     // 形状判定
// openRouterModelInfoFromMap(item, loginProtocol) ModelInfo
```

`FetchModels` 会在拉取后写入 `FetchModelsResult.Category`，调用方可用它记录/
展示本次实际生效的解析类别。

新增同族网关时只需在 `modelListCategorySpecs` 追加一条
`modelListCategorySpec{category, matchesProvider, matchesEntry, parseEntry}`：
`matchesProvider` 命中 provider 名称 / base_url，`matchesEntry` 覆盖别名域名与
自建镜像站，`parseEntry` 负责逐条解析为 `ModelInfo`。

openrouter 类别当前解析的必要参数（端点声明优先，未声明则不写）：
`architecture.input_modalities`（缺失时由 `architecture.modality` 推导）、
`reasoning.supported_efforts` / `reasoning.default_effort`、
`reasoning` 对象或 `supported_parameters` 中的推理参数（→ `reasoning_model`）、
`min(context_length, top_provider.context_length)`（→ `max_context_tokens`，
请求会路由到 top provider，取较小值才不会触发上游 400）、
`top_provider.max_completion_tokens`（→ `max_tokens`）、
`supported_parameters` 含 `tools`（→ `supports_tools`，仅供展示，不写入
`ModelCapabilitySpec`）。`pricing` 与 `:free` 等变体后缀一律不参与解析，
模型 ID 原样保留。

## commands 侧的兼容方式

在 `backend/cmd/aicli/commands/` 新增一个 `providerops_alias.go`：

- `type providerModelInfo = providerops.ModelInfo`
- `type providerModelsValidationRequest = providerops.FetchModelsRequest`
- `type providerModelsValidationResult = providerops.FetchModelsResult`
- `type providerLoginModelGroup = providerops.ModelGroup`（字段按需对齐）
- `var validateProviderModels = providerops.FetchModels`（注意 `ctx` 参数：保留一个
  同名的 `commands` 包装函数，内部传 `context.Background()`，签名与旧版一致）
- 其余被移走的私有函数同理：**类型别名 + 转发函数/变量**，不允许复制实现。

> 转发层的唯一目的：让 CLI / micro web client 的既有调用点与测试无需改动；
> 逻辑本体必须只有 `providerops` 一份。

## runtime server HTTP 契约（冻结）

前缀 `/api/runtime`（与既有 `siteaccount` 端点同一 router）。

### `POST /api/runtime/providers/fetch-models`

请求：

```json
{
  "name": "my-gateway",
  "base_url": "https://api.example.com",
  "api_key": "sk-...",
  "api_key_ref": "",
  "protocol": "openai",
  "auth_mode": "api_key",
  "models_path": "",
  "models_verified_only": false,
  "timeout_seconds": 30
}
```

响应：

```json
{
  "endpoint": "https://api.example.com/v1/models",
  "status_code": 200,
  "verified_at": "2026-09-18T00:00:00Z",
  "anonymous_allowed": false,
  "model_ids": ["gpt-4o", "claude-sonnet-4"],
  "models": [{"id": "gpt-4o", "display_name": "gpt-4o"}],
  "login_protocol": "openai",
  "runtime_protocol": "openai",
  "classification": {
    "login_protocol": "openai",
    "runtime_protocol": "openai",
    "total_models": 2,
    "primary_model_ids": ["gpt-4o"],
    "verified_model_ids": ["gpt-4o"],
    "assumed_model_ids": [],
    "groups": [{"protocol": "openai", "has_template": true, "primary": true,
                 "models": ["gpt-4o"], "other_models": []}]
  },
  "metadata": {"gpt-4o": {"id": "gpt-4o", "name": "gpt-4o", "reasoning_model": true,
                           "reasoning_efforts": ["low", "medium", "high"]}},
  "model_cards_applied": ["openai/gpt-4o"],
  "model_cards_skipped": [],
  "warnings": []
}
```

`classification` 在分类不可用时为 `null`（前端退化为只回填模型 ID 列表）。

### `POST /api/runtime/providers/auto-import`

请求同上，另加可选 `default_model`。

响应（**draft patch**，不落盘；runtime 编辑器把它合并进当前编辑草稿）：

```json
{
  "name": "my-gateway",
  "protocol": "openai",
  "login_protocol": "openai",
  "base_url": "https://api.example.com",
  "api_path": "/v1/chat/completions",
  "forward_url": "",
  "default_model": "gpt-4o",
  "supported_models": ["gpt-4o", "claude-sonnet-4"],
  "support_types": ["chat"],
  "model_capabilities": {"gpt-4o": {"reasoning_model": true, "reasoning_efforts": ["low","medium","high"]}},
  "site_type": "new-api",
  "site_type_confidence": "medium",
  "site_type_scores": {"new-api": 3},
  "account": null,
  "warnings": [],
  "models": [{"id": "gpt-4o"}]
}
```

### `POST /api/runtime/providers/probe-models`

请求同上，另加 `models: ["..."]`、`protocols: ["openai", "anthropic"]`。

响应：`{"results": [{"model_id": "...", "protocol": "...", "verdict": "ok|invalid|unsupported|error", "message": "", "status_code": 200, "duration_ms": 120}]}`
