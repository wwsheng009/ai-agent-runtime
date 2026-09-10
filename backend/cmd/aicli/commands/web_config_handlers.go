package commands

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	httpclient "github.com/wwsheng009/ai-agent-runtime/internal/pkg/httpclient"
)

// ---------------------------------------------------------------------------
// Micro web client — 配置管理端点（provider / model / reasoning effort CRUD）
//
// 这些端点直接包装 internal/agentconfig 的持久化 API，供前端“配置”页签做
// 增删改查。每次写成功后刷新会话内存配置（reloadChatConfigForModelCommand），
// 让 /web/api/runtime 与 /model 命令立即看到变更。路由注册见
// backend/cmd/aicli/pprof.go。
// ---------------------------------------------------------------------------

const (
	ChatWebAPIConfigPath                 = "/web/api/config"
	ChatWebAPIConfigProvidersPath        = "/web/api/config/providers"
	ChatWebAPIConfigProvidersDeletePath  = "/web/api/config/providers/delete"
	ChatWebAPIConfigProvidersEnabledPath = "/web/api/config/providers/enabled"
	ChatWebAPIConfigProvidersModelsPath  = "/web/api/config/providers/fetch-models"
	// probe-models 对 assumed（无 model card 数据）的模型用各协议最小补全
	// 请求实测网关支持情况（provider_model_probe.go），结果可写回用户级
	// model_cards.yaml 沉淀为探测卡片，让后续分类把 assumed 升级为 verified。
	ChatWebAPIConfigProvidersProbeModelsPath = "/web/api/config/providers/probe-models"
	// auto-import 参照 aicli login 的自动生成逻辑（provider_login.go
	// runProviderLogin）：由 name + base_url + api_key 探测协议、拉取模型
	// 列表并生成完整 provider 配置（protocol / api_path / forward_url /
	// default_model / supported_models / model_capabilities 等）。
	ChatWebAPIConfigProvidersAutoImportPath = "/web/api/config/providers/auto-import"
	ChatWebAPIConfigChatPath                = "/web/api/config/chat"
)

// ---------------------------------------------------------------------------
// 响应结构
// ---------------------------------------------------------------------------

// chatWebConfigSnapshot 是 GET /web/api/config 的响应体：本地配置文件里
// provider 的完整状态（含禁用项）与 aicli.chat 默认偏好。
type chatWebConfigSnapshot struct {
	ConfigPath      string                  `json:"config_path"`
	DefaultProvider string                  `json:"default_provider"`
	Providers       []chatWebConfigProvider `json:"providers"`
	Chat            chatWebConfigChat       `json:"chat"`
}

type chatWebConfigProvider struct {
	Name       string `json:"name"`
	Protocol   string `json:"protocol"`
	Enabled    bool   `json:"enabled"`
	BaseURL    string `json:"base_url"`
	APIPath    string `json:"api_path"`
	ForwardURL string `json:"forward_url"`
	// APIKeySet 表示任一凭据来源（内联 api_key / api_keys 池 / Key Store
	// api_key_ref / OAuth auth_ref）已配置；绝不回传密钥明文。
	APIKeySet    bool   `json:"api_key_set"`
	APIKeySource string `json:"api_key_source,omitempty"`
	// APIKeyMasked 是已保存 key 的掩码回显（如 sk-1...cdef），仅用于
	// 界面确认“已保存哪个 key”；Key Store / OAuth 来源按设计不读
	// store 内容（见 providerAPIKeySource 注释），统一显示 ****。
	APIKeyMasked    string               `json:"api_key_masked,omitempty"`
	APIKeyRef       string               `json:"api_key_ref,omitempty"`
	AuthMode        string               `json:"auth_mode,omitempty"`
	AuthRef         string               `json:"auth_ref,omitempty"`
	Proxy           *chatWebConfigProxy  `json:"proxy,omitempty"`
	DefaultModel    string               `json:"default_model"`
	SupportedModels []string             `json:"supported_models,omitempty"`
	Models          []chatWebConfigModel `json:"models,omitempty"`
	// Headers 是该 provider 生效的请求头（preset 合并后的值，含模板占位符
	// 原文如 {session_id}）。它同时是编辑入口：保存时整体写回用户
	// config.yaml 的 providers.items.<name>.headers（presets.yaml 只读）。
	Headers map[string]string `json:"headers,omitempty"`
	// EffectiveHeaders 是最终发往上游的请求头（全局 providers.headers 合并
	// provider.headers 后的结果），仅用于展示。
	EffectiveHeaders map[string]string `json:"effective_headers,omitempty"`
}

// chatWebConfigProxy 是 provider 级 proxy 节点的只读视图
// （providers.items.<name>.proxy）。
type chatWebConfigProxy struct {
	Enabled bool   `json:"enabled"`
	HTTP    string `json:"http,omitempty"`
	HTTPS   string `json:"https,omitempty"`
	NoProxy string `json:"no_proxy,omitempty"`
}

// chatWebConfigModel 是单个模型的配置视图：模型名 + 其 capability 中与
// reasoning 相关的字段（前端编辑入口）。
type chatWebConfigModel struct {
	Name                   string   `json:"name"`
	ReasoningModel         bool     `json:"reasoning_model"`
	ReasoningEfforts       []string `json:"reasoning_efforts,omitempty"`
	DefaultReasoningEffort string   `json:"default_reasoning_effort,omitempty"`
	CompactReasoningEffort string   `json:"compact_reasoning_effort,omitempty"`
	MaxContextTokens       int      `json:"max_context_tokens,omitempty"`
	MaxTokens              int      `json:"max_tokens,omitempty"`
}

type chatWebConfigChat struct {
	DefaultProvider string `json:"default_provider"`
	DefaultModel    string `json:"default_model"`
	ReasoningEffort string `json:"reasoning_effort"`
}

// ---------------------------------------------------------------------------
// 请求结构
// ---------------------------------------------------------------------------

// chatWebProviderWriteRequest 是 POST /web/api/config/providers 的请求体。
// 空字段表示不修改；Resonaning 按模型合并（只改提交的模型与字段）。
type chatWebProviderWriteRequest struct {
	Name     string `json:"name"`
	Protocol string `json:"protocol"`
	BaseURL  string `json:"base_url"`
	APIPath  string `json:"api_path"`
	// 以下指针字段遵循“nil=不修改、空串=清空、非空=写入”的合并语义。
	ForwardURL *string `json:"forward_url,omitempty"`
	APIKey     *string `json:"api_key,omitempty"`
	APIKeyRef  *string `json:"api_key_ref,omitempty"`
	AuthMode   *string `json:"auth_mode,omitempty"`
	AuthRef    *string `json:"auth_ref,omitempty"`
	// APIKeys 整体写回 api_keys 池：nil=不修改，非 nil 空数组=清空。
	APIKeys    *[]string           `json:"api_keys,omitempty"`
	Proxy      *chatWebConfigProxy `json:"proxy,omitempty"`
	ClearProxy bool                `json:"clear_proxy,omitempty"`
	// Headers 整体写回 providers.items.<name>.headers：nil=不修改，非 nil
	// 空 map=清空 headers 节点。保存目标是用户 config.yaml（presets.yaml
	// 只读）；值可含 {session_id} 等模板占位符。
	Headers            *map[string]string                     `json:"headers,omitempty"`
	Enabled            *bool                                  `json:"enabled"`
	DefaultModel       string                                 `json:"default_model"`
	SupportedModels    []string                               `json:"supported_models"`
	SetDefaultProvider bool                                   `json:"set_default_provider"`
	Reasoning          map[string]chatWebModelReasoningUpdate `json:"reasoning"`
}

// chatWebModelReasoningUpdate 描述单个模型 reasoning 相关字段的更新。
// 指针字段允许显式清空（false / 空串 / 空数组）。
type chatWebModelReasoningUpdate struct {
	ReasoningModel         *bool     `json:"reasoning_model"`
	ReasoningEfforts       *[]string `json:"reasoning_efforts"`
	DefaultReasoningEffort *string   `json:"default_reasoning_effort"`
	CompactReasoningEffort *string   `json:"compact_reasoning_effort"`
}

type chatWebProviderDeleteRequest struct {
	Names []string `json:"names"`
}

type chatWebProviderEnabledRequest struct {
	Names   []string `json:"names"`
	Enabled bool     `json:"enabled"`
}

// chatWebProviderFetchModelsRequest 是 POST /web/api/config/providers/fetch-models
// 的请求体。Name 指向磁盘上已保存的 provider（以其配置为基底，含已保存的
// api key / proxy / headers）；provider 尚未保存时可用 Protocol / BaseURL /
// APIKey 直接提供连接参数做“新增前探测”。APIKey 非空时优先用于本次请求
// （与 login 内联 key 语义一致），不会写入磁盘配置。
type chatWebProviderFetchModelsRequest struct {
	Name       string  `json:"name"`
	Protocol   string  `json:"protocol"`
	BaseURL    string  `json:"base_url"`
	APIKey     *string `json:"api_key,omitempty"`
	ModelsPath string  `json:"models_path,omitempty"`
}

// chatWebProviderProbeModelsRequest 是 POST
// /web/api/config/providers/probe-models 的请求体。连接参数解析与
// fetch-models 完全一致（Name 指向已保存 provider 时以磁盘配置为基底，
// 内联 api_key / base_url / protocol 优先）；Models 是待探测模型清单，
// Protocols 缺省时探测 [当前协议, openai, anthropic, codex]。WriteBack
// 默认 true：把 supported 结论写回用户级 model_cards.yaml。
type chatWebProviderProbeModelsRequest struct {
	Name      string   `json:"name"`
	Protocol  string   `json:"protocol"`
	BaseURL   string   `json:"base_url"`
	APIKey    *string  `json:"api_key,omitempty"`
	Models    []string `json:"models"`
	Protocols []string `json:"protocols,omitempty"`
	WriteBack *bool    `json:"write_back,omitempty"`
}

// chatWebProviderAutoImportRequest 是 POST
// /web/api/config/providers/auto-import 的请求体：参照 aicli login
// （provider_login.go runProviderLogin），由 name + base_url + api_key
// 一键自动生成 provider 配置。Protocol 沿用 login 的登录协议取值：空串
// 或 "auto" 表示自动探测（依次尝试 openai / anthropic / gemini /
// codex-apikey），也可显式指定 openai / openai_image / anthropic /
// gemini / codex-apikey。OAuth（codex-oauth）需要设备码交互，不适合
// Web 端，不在此支持。
type chatWebProviderAutoImportRequest struct {
	Name         string `json:"name"`
	BaseURL      string `json:"base_url"`
	APIKey       string `json:"api_key"`
	Protocol     string `json:"protocol"`
	DefaultModel string `json:"default_model"`
	ModelsPath   string `json:"models_path"`
	SetDefault   bool   `json:"set_default"`
	TimeoutSec   int    `json:"timeout_sec"`
}

type chatWebChatWriteRequest struct {
	DefaultProvider string `json:"default_provider"`
	DefaultModel    string `json:"default_model"`
	ReasoningEffort string `json:"reasoning_effort"`
}

// ---------------------------------------------------------------------------
// 辅助函数
// ---------------------------------------------------------------------------

// chatWebRequireMethod 校验 HTTP 方法，不匹配时写入 405 并返回 false。
func chatWebRequireMethod(w http.ResponseWriter, r *http.Request, method string) bool {
	if r.Method != method {
		writeWebAPIJSON(w, http.StatusMethodNotAllowed, map[string]string{
			"status": "rejected",
			"reason": "method not allowed",
		})
		return false
	}
	return true
}

// chatWebWriteError 按统一格式返回错误响应。
func chatWebWriteError(w http.ResponseWriter, status int, err error) {
	reason := "internal error"
	if err != nil {
		reason = err.Error()
	}
	writeWebAPIJSON(w, status, map[string]string{"status": "error", "reason": reason})
}

// chatWebConfigPath 返回当前会话写入配置所用的本地文件路径。
// 会先按 /model 命令的语义刷新一次内存配置（使增量写入基于最新文件）。
func chatWebConfigPath() (string, error) {
	session := chatWebSession()
	if session == nil {
		return "", nil
	}
	_ = reloadChatConfigForModelCommand(session)
	if session.Config != nil && strings.TrimSpace(session.Config.ConfigFilePath) != "" {
		return strings.TrimSpace(session.Config.ConfigFilePath), nil
	}
	if strings.TrimSpace(session.RuntimeConfigPath) != "" {
		// reloadChatConfigForModelCommand 只认 ConfigFilePath，为空时跳过
		// 刷新；快照 GET / fetch-models 等读端点与增量写前都基于
		// session.Config，这里按 RuntimeConfigPath 补齐，保证内存与磁盘
		// 同源（外部/其他命令改配置文件后读路径也能立即看到新凭据）。
		refreshChatWebSessionConfigFromRuntime(session)
		return strings.TrimSpace(session.RuntimeConfigPath), nil
	}
	return "", nil
}

// chatWebRefreshSessionConfig 写成功后刷新会话内存配置，使运行时元数据同步。
// 注意：web 写盘路径（chatWebConfigPath）在 ConfigFilePath 为空时回退
// RuntimeConfigPath，而 reloadChatConfigForModelCommand 只认 ConfigFilePath、
// 为空即跳过刷新——这类会话（chat_setup 装配的真实运行形态）会留下
// “磁盘已更新、内存仍是旧 key”的不一致，fetch-models 等读 session.Config
// 的端点会继续用旧凭据。这里显式按 RuntimeConfigPath 补齐刷新。
func chatWebRefreshSessionConfig() {
	session := chatWebSession()
	if session == nil {
		return
	}
	_ = reloadChatConfigForModelCommand(session)
	refreshChatWebSessionConfigFromRuntime(session)
}

// refreshChatWebSessionConfigFromRuntime 在 reloadChatConfigForModelCommand
// 跳过刷新（Config.ConfigFilePath 为空）时，按 RuntimeConfigPath 重新
// InitGlobalConfig 并替换 session.Config，使内存与磁盘最新内容同源。
// 该会话形态由 chat_setup 装配（内存 Config 不携带文件路径字段）。
func refreshChatWebSessionConfigFromRuntime(session *ChatSession) {
	if session.Config == nil {
		return
	}
	if strings.TrimSpace(session.Config.ConfigFilePath) != "" {
		return
	}
	runtimePath := strings.TrimSpace(session.RuntimeConfigPath)
	if runtimePath == "" {
		return
	}
	reloaded, err := agentconfig.InitGlobalConfig(runtimePath)
	if err == nil && reloaded != nil {
		session.Config = reloaded
	}
}

// chatWebInvalidateRuntimeProvider 使会话本地 runtime 中缓存的 provider
// 失效。LLMRuntime 在 ensureLocalRuntimeProvider 首次构建 provider 时经
// GetAPIKey 固化 API key（Key Store 凭据来自 auth.json），此后更新 key 只
// 改磁盘文件，已缓存的 provider 实例仍携带旧 key——表现为“保存新 key 后
// 请求仍用旧 key”。注销后按最新配置重建 provider（重新解析 key）。
//
// 注销后必须立即重建：LLMRuntime 的内存注册表（providers map）只在会话
// 启动与模型/Provider 切换（refreshLocalRuntimeAfterModelSelection →
// ensureLocalRuntimeProvider）时填充，普通对话轮次不会触发重建。若只注销
// 不重建，保存 api-key 后的下一轮对话会报 provider not found: <name>
// （VALIDATION_FAILED），直到用户在界面上切换 provider 才恢复——这正是
// “更新 api-key 后继续对话报错、切换 provider 后正常”的根因。
func chatWebInvalidateRuntimeProvider(name string) {
	session := chatWebSession()
	if session == nil || session.LocalRuntimeHost == nil || session.LocalRuntimeHost.Bootstrap == nil {
		return
	}
	rt := session.LocalRuntimeHost.Bootstrap.LLMRuntime()
	if rt == nil {
		return
	}
	rt.UnregisterProvider(name)

	// 仅当被更新的 provider 正是会话当前使用的 provider，且仍存在于最新配置
	// 并处于启用状态时重建注册表条目：删除 / 禁用 / 非活动 provider 保持注销
	// 后的原行为，由下一次模型选择路径按需重建。
	name = strings.TrimSpace(name)
	if name == "" || !strings.EqualFold(name, strings.TrimSpace(session.ProviderName)) || session.Config == nil {
		return
	}
	current, ok := session.Config.Providers.Items[name]
	if !ok || !current.Enabled {
		return
	}
	// 同步会话内存中的 provider 对象：保存路径已通过 chatWebRefreshSessionConfig
	// 重载 session.Config（config.yaml / Key Store），但 session.Provider 仍是
	// 旧实例，直接重建会继续携带旧 key。
	session.Provider = current
	_ = ensureLocalRuntimeProvider(rt, session)
}

// mergeModelCapabilities 把前端提交的每模型 reasoning 更新合并进现有
// capabilities：只改动提交涉及的模型与字段，其余模型/字段原样保留，
// 避免全量 map 写回时丢失未编辑模型的能力声明。
func mergeModelCapabilities(current map[string]agentconfig.ModelCapabilitySpec, requested map[string]chatWebModelReasoningUpdate) map[string]agentconfig.ModelCapabilitySpec {
	merged := make(map[string]agentconfig.ModelCapabilitySpec, len(current)+len(requested))
	for name, spec := range current {
		merged[name] = spec
	}
	for name, upd := range requested {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		spec := merged[name]
		if upd.ReasoningModel != nil {
			spec.ReasoningModel = *upd.ReasoningModel
		}
		if upd.ReasoningEfforts != nil {
			efforts := make([]string, 0, len(*upd.ReasoningEfforts))
			for _, e := range *upd.ReasoningEfforts {
				if e = strings.TrimSpace(e); e != "" {
					efforts = append(efforts, e)
				}
			}
			spec.ReasoningEfforts = efforts
		}
		if upd.DefaultReasoningEffort != nil {
			spec.DefaultReasoningEffort = strings.TrimSpace(*upd.DefaultReasoningEffort)
		}
		if upd.CompactReasoningEffort != nil {
			spec.CompactReasoningEffort = strings.TrimSpace(*upd.CompactReasoningEffort)
		}
		merged[name] = spec
	}
	return merged
}

func chatWebTrimNames(names []string) []string {
	seen := make(map[string]struct{})
	out := make([]string, 0, len(names))
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		if _, dup := seen[n]; dup {
			continue
		}
		seen[n] = struct{}{}
		out = append(out, n)
	}
	return out
}

// providerAPIKeySource 返回该 provider 已配置凭据的来源，解析顺序与
// agentconfig.Provider.GetAllAPIKeys 一致：
//   - "oauth"：auth_mode=oauth 且 auth_ref 指向 Key Store 的 OAuth 记录；
//   - "key_store"：api_key_ref 指向 Key Store 中保存的 api_key 凭据；
//   - "pool"：api_keys 密钥池；
//   - "inline"：api_key 内联字段。
//
// 只做配置层判断（ref 非空即视为已配置），不读 Key Store 内容，避免快照
// 渲染引入磁盘 IO 与 store 文件损坏连带失败；空串表示未配置任何来源。
func providerAPIKeySource(p agentconfig.Provider) string {
	if strings.EqualFold(strings.TrimSpace(p.AuthMode), agentconfig.AuthKeyTypeOAuth) && strings.TrimSpace(p.AuthRef) != "" {
		return "oauth"
	}
	if strings.TrimSpace(p.APIKeyRef) != "" {
		return "key_store"
	}
	if len(p.APIKeys) > 0 {
		return "pool"
	}
	if strings.TrimSpace(p.APIKey) != "" {
		return "inline"
	}
	return ""
}

// webProviderMaskedSecret 读取 Key Store 中 ref 对应凭据的明文并计算掩码
// 回显（如 sk-proj...OpQr）。仅用于界面识别，明文不随快照回传；store
// 缺失、损坏或 ref 无对应凭据时返回通用掩码 ****，快照渲染不因此失败。
func webProviderMaskedSecret(path, ref, keyType string) string {
	secret, err := agentconfig.LoadProviderAuthSecretFromPath(path, ref, keyType)
	if err != nil {
		return "****"
	}
	masked := maskAPIKeyForDisplay(secret)
	if masked == "" {
		return "****"
	}
	return masked
}

// ---------------------------------------------------------------------------
// GET /web/api/config — 配置快照
// ---------------------------------------------------------------------------

// HandleChatWebAPIConfig 返回本地配置文件的 provider/model/reasoning 全量
// 快照（含禁用 provider），供“配置”页签渲染。
func HandleChatWebAPIConfig(w http.ResponseWriter, r *http.Request) {
	if !chatWebRequireMethod(w, r, http.MethodGet) {
		return
	}
	snap := chatWebConfigSnapshot{
		Providers: []chatWebConfigProvider{},
		Chat:      chatWebConfigChat{},
	}

	path, err := chatWebConfigPath()
	if err != nil {
		chatWebWriteError(w, http.StatusInternalServerError, err)
		return
	}
	snap.ConfigPath = path

	session := chatWebSession()
	if session == nil || session.Config == nil {
		writeWebAPIJSON(w, http.StatusOK, snap)
		return
	}

	cfg := session.Config
	snap.DefaultProvider = strings.TrimSpace(cfg.Providers.DefaultProvider)
	if cfg.AICLI != nil && cfg.AICLI.Chat != nil {
		snap.Chat.DefaultProvider = strings.TrimSpace(cfg.AICLI.Chat.DefaultProvider)
		snap.Chat.DefaultModel = strings.TrimSpace(cfg.AICLI.Chat.DefaultModel)
		snap.Chat.ReasoningEffort = strings.TrimSpace(cfg.AICLI.Chat.ReasoningEffort)
	}

	for name, provider := range cfg.Providers.Items {
		apiKeySource := providerAPIKeySource(provider)
		masked := ""
		switch apiKeySource {
		case "inline":
			masked = maskAPIKeyForDisplay(strings.TrimSpace(provider.APIKey))
		case "pool":
			if len(provider.APIKeys) > 0 {
				masked = maskAPIKeyForDisplay(strings.TrimSpace(provider.APIKeys[0]))
			}
		case "key_store":
			// 读取 Key Store 明文仅用于生成掩码回显（前端识别是哪把 key），
			// 不回传明文；store 缺失/损坏/无凭据时降级为通用掩码 ****，
			// 快照渲染不因 store 问题连带失败。
			masked = webProviderMaskedSecret(agentconfig.DefaultAuthStorePath(),
				strings.TrimSpace(provider.APIKeyRef), agentconfig.AuthKeyTypeAPIKey)
		case "oauth":
			masked = webProviderMaskedSecret(agentconfig.DefaultAuthStorePath(),
				strings.TrimSpace(provider.AuthRef), agentconfig.AuthKeyTypeOAuth)
		}
		entry := chatWebConfigProvider{
			Name:            name,
			Protocol:        strings.TrimSpace(provider.Protocol),
			Enabled:         provider.Enabled,
			BaseURL:         strings.TrimSpace(provider.BaseURL),
			APIPath:         strings.TrimSpace(provider.APIPath),
			ForwardURL:      strings.TrimSpace(provider.ForwardURL),
			APIKeySet:       apiKeySource != "",
			APIKeySource:    apiKeySource,
			APIKeyMasked:    masked,
			APIKeyRef:       strings.TrimSpace(provider.APIKeyRef),
			AuthMode:        strings.TrimSpace(provider.AuthMode),
			AuthRef:         strings.TrimSpace(provider.AuthRef),
			DefaultModel:    strings.TrimSpace(provider.DefaultModel),
			SupportedModels: append([]string(nil), provider.SupportedModels...),
		}
		if len(provider.Headers) > 0 {
			entry.Headers = cloneStringMap(provider.Headers)
		}
		if effective := agentconfig.EffectiveProviderHeaders(cfg.Providers.Headers, provider.Headers); len(effective) > 0 {
			entry.EffectiveHeaders = effective
		}
		if provider.Proxy != nil {
			entry.Proxy = &chatWebConfigProxy{
				Enabled: provider.Proxy.Enabled,
				HTTP:    strings.TrimSpace(provider.Proxy.HTTP),
				HTTPS:   strings.TrimSpace(provider.Proxy.HTTPS),
				NoProxy: strings.TrimSpace(provider.Proxy.NoProxy),
			}
		}
		for _, model := range collectRuntimeProviderModels(provider) {
			m := chatWebConfigModel{Name: model}
			if spec, ok := provider.ModelCapabilities[model]; ok {
				m.ReasoningModel = spec.ReasoningModel
				m.ReasoningEfforts = append([]string(nil), spec.ReasoningEfforts...)
				m.DefaultReasoningEffort = strings.TrimSpace(spec.DefaultReasoningEffort)
				m.CompactReasoningEffort = strings.TrimSpace(spec.CompactReasoningEffort)
				m.MaxContextTokens = spec.MaxContextTokens
				m.MaxTokens = spec.MaxTokens
			}
			entry.Models = append(entry.Models, m)
		}
		snap.Providers = append(snap.Providers, entry)
	}
	sort.Slice(snap.Providers, func(i, j int) bool {
		return snap.Providers[i].Name < snap.Providers[j].Name
	})

	writeWebAPIJSON(w, http.StatusOK, snap)
}

// ---------------------------------------------------------------------------
// POST /web/api/config/providers — 新增 / 更新 provider
// ---------------------------------------------------------------------------

// HandleChatWebAPIConfigProviders 对 providers.items.<name> 做 upsert：
// 更新基本字段、supported_models、default_model，并按模型合并 reasoning
// 相关能力字段；SetDefaultProvider 同时更新 providers.default_provider。
func HandleChatWebAPIConfigProviders(w http.ResponseWriter, r *http.Request) {
	if !chatWebRequireMethod(w, r, http.MethodPost) {
		return
	}
	var req chatWebProviderWriteRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		chatWebWriteError(w, http.StatusBadRequest, err)
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		chatWebWriteError(w, http.StatusBadRequest, errRequiredField("provider name"))
		return
	}
	path, err := chatWebConfigPath()
	if err != nil {
		chatWebWriteError(w, http.StatusInternalServerError, err)
		return
	}
	if path == "" {
		chatWebWriteError(w, http.StatusBadRequest, errNoConfigPath())
		return
	}

	update := agentconfig.ProviderConfigUpdate{Name: req.Name}
	if req.Protocol != "" {
		update.Protocol = webStringPtr(strings.TrimSpace(req.Protocol))
	}
	if req.BaseURL != "" {
		update.BaseURL = webStringPtr(strings.TrimSpace(req.BaseURL))
	}
	if req.APIPath != "" {
		update.APIPath = webStringPtr(strings.TrimSpace(req.APIPath))
	}
	update.ForwardURL = req.ForwardURL
	// API key 保存语义（与 login 命令 provider_login.go 对齐）：仅当请求是
	// “纯 key 更新”（只带 api_key，未显式携带 api_key_ref / auth_ref /
	// auth_mode / api_keys）且目标 provider 已配置 api_key_ref（Key Store
	// 模式、非 OAuth）时，把新 key 写入 Key Store（auth.json）并清除 YAML
	// 内联 api_key，而不是写进 config.yaml 内联字段——否则运行时
	// （GetAllAPIKeys 优先读 ref）与 web 校验（providerModelsAPIKey 优先读
	// 内联）会出现双源分裂：页面保存的 key 不生效，实际请求仍用 store 里
	// 的旧凭据。未配置 ref 的纯内联 provider 保持现状（内联是唯一键源）。
	update.APIKey = req.APIKey
	update.APIKeyRef = req.APIKeyRef
	update.AuthMode = req.AuthMode
	update.AuthRef = req.AuthRef
	if req.APIKey != nil && strings.TrimSpace(*req.APIKey) != "" &&
		req.APIKeyRef == nil && req.AuthRef == nil && req.AuthMode == nil && req.APIKeys == nil {
		provider, ok := agentconfig.Provider{}, false
		if session := chatWebSession(); session != nil && session.Config != nil {
			provider, ok = session.Config.Providers.Items[req.Name]
		}
		if ok && !strings.EqualFold(strings.TrimSpace(provider.AuthMode), agentconfig.AuthKeyTypeOAuth) &&
			strings.TrimSpace(provider.APIKeyRef) != "" {
			ref := strings.TrimSpace(provider.APIKeyRef)
			record := agentconfig.ProviderAuthRecord{
				KeyType:  agentconfig.AuthKeyTypeAPIKey,
				APIKey:   strings.TrimSpace(*req.APIKey),
				AuthMode: agentconfig.AuthKeyTypeAPIKey,
			}
			if err := agentconfig.SaveProviderAuthToPath(agentconfig.DefaultAuthStorePath(), ref, record); err != nil {
				chatWebWriteError(w, http.StatusInternalServerError, fmt.Errorf("保存 API Key 到 Key Store 失败: %w", err))
				return
			}
			update.APIKey = webStringPtr("") // 清除 YAML 内联残留，消除双源分裂
			update.APIKeyRef = nil           // 保留现有 ref 引用
		}
	}
	if req.APIKeys != nil {
		update.APIKeys = req.APIKeys
	}
	if req.ClearProxy {
		update.ClearProxy = true
	} else if req.Proxy != nil {
		update.Proxy = &agentconfig.ProxyConfig{
			HTTP:    strings.TrimSpace(req.Proxy.HTTP),
			HTTPS:   strings.TrimSpace(req.Proxy.HTTPS),
			NoProxy: strings.TrimSpace(req.Proxy.NoProxy),
			Enabled: req.Proxy.Enabled,
		}
	}
	update.Headers = req.Headers
	update.Enabled = req.Enabled
	if req.DefaultModel != "" {
		update.DefaultModel = webStringPtr(strings.TrimSpace(req.DefaultModel))
	}
	if req.SupportedModels != nil {
		models := chatWebTrimNames(req.SupportedModels)
		update.SupportedModels = &models
	}
	update.SetDefaultProvider = req.SetDefaultProvider
	if len(req.Reasoning) > 0 {
		// 以当前 provider 的 capabilities 为基底做按模型合并。
		base := map[string]agentconfig.ModelCapabilitySpec{}
		session := chatWebSession()
		if session != nil && session.Config != nil {
			if current, ok := session.Config.Providers.Items[req.Name]; ok {
				base = current.ModelCapabilities
				if base == nil {
					base = map[string]agentconfig.ModelCapabilitySpec{}
				}
			}
		}
		merged := mergeModelCapabilities(base, req.Reasoning)
		update.ModelCapabilities = &merged
	}

	if _, err := agentconfig.UpdateProviderConfig(path, update); err != nil {
		chatWebWriteError(w, http.StatusInternalServerError, err)
		return
	}
	chatWebRefreshSessionConfig()
	chatWebInvalidateRuntimeProvider(req.Name)
	resp := map[string]interface{}{
		"status":   "ok",
		"provider": req.Name,
	}
	// 回传本次保存 key 的掩码（保存时明文在请求里，无需再读 Key Store），
	// 前端保存成功后立即显示；未携带 key 的普通保存不带该字段。
	if req.APIKey != nil && strings.TrimSpace(*req.APIKey) != "" {
		resp["masked"] = maskAPIKeyForDisplay(strings.TrimSpace(*req.APIKey))
	}
	writeWebAPIJSON(w, http.StatusOK, resp)
}

// ---------------------------------------------------------------------------
// POST /web/api/config/providers/delete — 删除 provider
// ---------------------------------------------------------------------------

// HandleChatWebAPIConfigProvidersDelete 删除一个或多个 provider（含其
// capabilities 等子节点；不级联清理默认指向，由前端先切换默认值）。
func HandleChatWebAPIConfigProvidersDelete(w http.ResponseWriter, r *http.Request) {
	if !chatWebRequireMethod(w, r, http.MethodPost) {
		return
	}
	var req chatWebProviderDeleteRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		chatWebWriteError(w, http.StatusBadRequest, err)
		return
	}
	names := chatWebTrimNames(req.Names)
	if len(names) == 0 {
		chatWebWriteError(w, http.StatusBadRequest, errRequiredField("provider names"))
		return
	}
	path, err := chatWebConfigPath()
	if err != nil {
		chatWebWriteError(w, http.StatusInternalServerError, err)
		return
	}
	if path == "" {
		chatWebWriteError(w, http.StatusBadRequest, errNoConfigPath())
		return
	}
	result, err := agentconfig.DeleteProvidersConfig(path, agentconfig.ProviderDeleteRequest{
		Names:        names,
		ClearDefault: true, // Web 管理删除默认 provider 时同时清空默认指向
	})
	if err != nil {
		chatWebWriteError(w, http.StatusInternalServerError, err)
		return
	}
	if len(result.Blocked) > 0 {
		chatWebWriteError(w, http.StatusBadRequest, &webConfigError{msg: result.Blocked[0].Message})
		return
	}
	if len(result.Deleted) == 0 {
		chatWebWriteError(w, http.StatusBadRequest, &webConfigError{msg: "provider not found: " + strings.Join(result.NotFound, ", ")})
		return
	}
	chatWebRefreshSessionConfig()
	for _, name := range result.Deleted {
		chatWebInvalidateRuntimeProvider(name)
	}
	writeWebAPIJSON(w, http.StatusOK, map[string]interface{}{
		"status": "ok",
		"names":  result.Deleted,
	})
}

// ---------------------------------------------------------------------------
// POST /web/api/config/providers/enabled — 启用 / 禁用
// ---------------------------------------------------------------------------

// HandleChatWebAPIConfigProvidersEnabled 批量设置 provider 的 enabled 状态。
func HandleChatWebAPIConfigProvidersEnabled(w http.ResponseWriter, r *http.Request) {
	if !chatWebRequireMethod(w, r, http.MethodPost) {
		return
	}
	var req chatWebProviderEnabledRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		chatWebWriteError(w, http.StatusBadRequest, err)
		return
	}
	names := chatWebTrimNames(req.Names)
	if len(names) == 0 {
		chatWebWriteError(w, http.StatusBadRequest, errRequiredField("provider names"))
		return
	}
	path, err := chatWebConfigPath()
	if err != nil {
		chatWebWriteError(w, http.StatusInternalServerError, err)
		return
	}
	if path == "" {
		chatWebWriteError(w, http.StatusBadRequest, errNoConfigPath())
		return
	}
	if _, err := agentconfig.SetProvidersEnabledConfig(path, names, req.Enabled); err != nil {
		chatWebWriteError(w, http.StatusInternalServerError, err)
		return
	}
	chatWebRefreshSessionConfig()
	for _, name := range names {
		chatWebInvalidateRuntimeProvider(name)
	}
	writeWebAPIJSON(w, http.StatusOK, map[string]interface{}{
		"status":  "ok",
		"enabled": req.Enabled,
		"names":   names,
	})
}

// ---------------------------------------------------------------------------
// POST /web/api/config/providers/fetch-models — 拉取 provider 的模型列表
// ---------------------------------------------------------------------------

// HandleChatWebAPIConfigProvidersFetchModels 调用 provider 的 GET /models
// 端点拉取模型清单（复用 login/doctor 的 validateProviderModels），供前端
// “获取模型列表”按钮填充支持模型列表。name 存在时以磁盘已保存配置为基底
// （含 api key / proxy / headers）；否则用请求中的 protocol / base_url /
// api_key 临时构造，支持新增 provider 前先探测。api_key 只用于本次请求，
// 不会写进磁盘配置。
//
// 拉取结果会按 aicli login 同源的 model card 分类链路按协议分组：响应中
// models 只包含与 provider 协议一致的主组模型；其他协议模型归入 groups
// （含各自协议与模型列表），other_models_count 为被过滤的模型总数，
// models_total 为归一化后的全量模型数。分类不可用时 models 退化为原始
// 全量列表。
func HandleChatWebAPIConfigProvidersFetchModels(w http.ResponseWriter, r *http.Request) {
	if !chatWebRequireMethod(w, r, http.MethodPost) {
		return
	}
	var req chatWebProviderFetchModelsRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		chatWebWriteError(w, http.StatusBadRequest, err)
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	path, err := chatWebConfigPath()
	if err != nil {
		chatWebWriteError(w, http.StatusInternalServerError, err)
		return
	}
	if path == "" {
		chatWebWriteError(w, http.StatusBadRequest, errNoConfigPath())
		return
	}

	var provider agentconfig.Provider
	session := chatWebSession()
	if session != nil && session.Config != nil {
		if saved, ok := session.Config.Providers.Items[req.Name]; ok {
			provider = saved
		}
	}
	if req.Name == "" && strings.TrimSpace(req.BaseURL) == "" {
		chatWebWriteError(w, http.StatusBadRequest, errRequiredField("provider name 或 base_url"))
		return
	}
	if strings.TrimSpace(provider.BaseURL) == "" && strings.TrimSpace(req.BaseURL) == "" {
		chatWebWriteError(w, http.StatusBadRequest, errRequiredField("base_url（provider 未保存时需要）"))
		return
	}
	if provider.BaseURL == "" {
		provider.Protocol = strings.TrimSpace(req.Protocol)
		provider.BaseURL = strings.TrimSpace(req.BaseURL)
	}
	if req.APIKey != nil && strings.TrimSpace(*req.APIKey) != "" {
		// 内联 key 优先（与 login 的 providerModelsAPIKey 语义一致）。
		provider.APIKey = strings.TrimSpace(*req.APIKey)
	}

	result, err := validateProviderModels(providerModelsValidationRequest{
		Config:        configPtrOrNil(session),
		Provider:      provider,
		LoginProtocol: req.Protocol,
		ModelsPath:    req.ModelsPath,
		Timeout:       15 * time.Second,
	})
	if err != nil {
		chatWebWriteError(w, http.StatusBadGateway, err)
		return
	}
	// 探测该 models 端点是否校验 API key：部分网关（如 opencode.ai）的
	// GET /models 是公开端点，匿名也能拉取完整列表——此时“获取模型列表成功”
	// 不代表 key 有效，必须明确提示用户，避免把无效 key 误判为已生效。
	authNotice := ""
	probeClient := probeClientForProvider(session, &provider)
	if modelsEndpointAllowsAnonymous(probeClient, result.Endpoint, normalizeLoginProtocol(req.Protocol, provider.AuthMode)) {
		authNotice = "该模型列表端点未校验 API key（匿名可访问），获取模型列表成功不代表 key 有效；key 是否有效请以聊天/补全请求为准。"
	}
	response := map[string]interface{}{
		"status":      "ok",
		"endpoint":    result.Endpoint,
		"models":      providerModelIDs(result.Models),
		"auth_notice": authNotice,
	}
	// 与 aicli login 同源的按协议分类：聚合网关（如 new-api）的 /models 会
	// 同时列出 gpt-*/claude-*/gemini-* 等多协议模型，provider 只绑定一种
	// 协议，直接把全量 ID 合并进 supported_models 会污染配置。这里按
	// model card 推荐分组后只把与 provider 协议一致的主组作为 models 返回，
	// 其他协议组放入 groups 供前端提示（需要时走「自动导入」生成独立
	// provider）。分类不可用时退化为原始全量列表（旧行为）。
	//
	// 主组内部再区分 verified / assumed：assumed 是“未匹配任何 model card、
	// 仅跟随当前协议 fallback”的模型。/v1/models 是 OpenAI 风格端点，列出
	// 的模型几乎必然支持 chat/completions，因此 openai 协议下照旧合并
	// （旧行为）；codex/anthropic 等协议下无证据假定会得到错误列表——实测
	// opencode.ai 网关对 minimax/kimi 走 /v1/responses 会明确拒绝——这些
	// 模型单独放入 assumed_models 供前端提示，不并入 supported_models。
	groupingProtocol := resolveProviderFetchModelsLoginProtocol(req.Protocol, provider)
	requestedProtocol := strings.TrimSpace(req.Protocol)
	if requestedProtocol == "" {
		requestedProtocol = groupingProtocol
	}
	if classification := classifyProviderFetchedModels(req.Name, provider, requestedProtocol, groupingProtocol, result.Models, configPtrOrNil(session)); classification != nil {
		primaryCount := len(classification.PrimaryModels)
		models := classification.PrimaryModels
		if !strings.EqualFold(classification.RuntimeProtocol, "openai") && len(classification.AssumedModels) > 0 {
			models = classification.VerifiedModels
			response["assumed_models"] = providerModelIDs(classification.AssumedModels)
		}
		response["models"] = providerModelIDs(models)
		response["models_total"] = classification.TotalModels
		response["verified_models_count"] = len(classification.VerifiedModels)
		response["assumed_models_count"] = len(classification.AssumedModels)
		response["protocol"] = classification.RuntimeProtocol
		response["login_protocol"] = classification.LoginProtocol
		response["groups"] = classification.Groups
		response["other_models_count"] = classification.TotalModels - primaryCount
	}
	writeWebAPIJSON(w, http.StatusOK, response)
}

// ---------------------------------------------------------------------------
// POST /web/api/config/providers/probe-models — 探测模型协议支持
// ---------------------------------------------------------------------------

// providerProbeMaxModels 单次探测的模型数上限：防止误传全量列表把网关打挂。
const providerProbeMaxModels = 200

// HandleChatWebAPIConfigProvidersProbeModels 对请求中的模型清单做跨协议
// 最小补全探测（provider_model_probe.go），返回模型 × 协议结论矩阵。
// 连接参数解析与 fetch-models 一致：已保存 provider 以磁盘配置为基底
// （含 key / proxy / headers），内联 api_key / base_url / protocol 优先；
// api_key 只用于本次请求，不写磁盘。write_back（默认 true）把 supported
// 结论写回用户级 model_cards.yaml，后续分类自动把对应模型升级为 verified。
func HandleChatWebAPIConfigProvidersProbeModels(w http.ResponseWriter, r *http.Request) {
	if !chatWebRequireMethod(w, r, http.MethodPost) {
		return
	}
	var req chatWebProviderProbeModelsRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		chatWebWriteError(w, http.StatusBadRequest, err)
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	models := chatWebTrimNames(req.Models)
	if len(models) == 0 {
		chatWebWriteError(w, http.StatusBadRequest, errRequiredField("models"))
		return
	}
	if len(models) > providerProbeMaxModels {
		chatWebWriteError(w, http.StatusBadRequest, &webConfigError{msg: fmt.Sprintf("一次最多探测 %d 个模型（收到 %d 个）", providerProbeMaxModels, len(models))})
		return
	}
	path, err := chatWebConfigPath()
	if err != nil {
		chatWebWriteError(w, http.StatusInternalServerError, err)
		return
	}
	if path == "" {
		chatWebWriteError(w, http.StatusBadRequest, errNoConfigPath())
		return
	}

	var provider agentconfig.Provider
	session := chatWebSession()
	if session != nil && session.Config != nil {
		if saved, ok := session.Config.Providers.Items[req.Name]; ok {
			provider = saved
		}
	}
	if req.Name == "" && strings.TrimSpace(req.BaseURL) == "" {
		chatWebWriteError(w, http.StatusBadRequest, errRequiredField("provider name 或 base_url"))
		return
	}
	if strings.TrimSpace(provider.BaseURL) == "" && strings.TrimSpace(req.BaseURL) == "" {
		chatWebWriteError(w, http.StatusBadRequest, errRequiredField("base_url（provider 未保存时需要）"))
		return
	}
	if provider.BaseURL == "" {
		provider.Protocol = strings.TrimSpace(req.Protocol)
		provider.BaseURL = strings.TrimSpace(req.BaseURL)
	}
	if req.APIKey != nil && strings.TrimSpace(*req.APIKey) != "" {
		provider.APIKey = strings.TrimSpace(*req.APIKey)
	}
	if strings.EqualFold(strings.TrimSpace(provider.AuthMode), agentconfig.AuthKeyTypeOAuth) {
		chatWebWriteError(w, http.StatusBadRequest, &webConfigError{msg: "OAuth provider 不支持 API key 自动探测"})
		return
	}
	if strings.TrimSpace(providerModelsAPIKey(provider)) == "" {
		chatWebWriteError(w, http.StatusBadRequest, errRequiredField("api key"))
		return
	}

	currentLoginProtocol := resolveProviderFetchModelsLoginProtocol(req.Protocol, provider)
	currentRuntimeProtocol := runtimeProtocolForLoginProtocol(currentLoginProtocol)
	protocols := req.Protocols
	if len(protocols) == 0 {
		// 缺省探测矩阵：当前协议 + 三大通用协议（gemini 仅显式指定时探测，
		// 其 URL 结构差异大、且聚合网关较少托管）。
		protocols = []string{currentRuntimeProtocol, "openai", "anthropic", "codex"}
	}

	results := runProviderModelProbes(providerModelProbeRequest{
		Config:    configPtrOrNil(session),
		Provider:  provider,
		Models:    models,
		Protocols: protocols,
		Timeout:   15 * time.Second,
	})

	// 当前协议下实测支持的模型：可直接合并进 supported_models。
	supported := make([]string, 0, len(results))
	for _, result := range results {
		for _, probe := range result.Probes {
			if strings.EqualFold(probe.Protocol, currentRuntimeProtocol) && probe.Verdict == string(probeVerdictSupported) {
				supported = append(supported, result.Model)
				break
			}
		}
	}

	writeBack := true
	if req.WriteBack != nil {
		writeBack = *req.WriteBack
	}
	writtenCards := []string(nil)
	cardsPath := ""
	writeBackError := ""
	if writeBack {
		cards, resolvedPath, writeErr := appendProviderModelProbeCards(configPtrOrNil(session), req.Name, provider, results)
		writtenCards = cards
		cardsPath = resolvedPath
		if writeErr != nil {
			writeBackError = writeErr.Error()
		}
	}

	response := map[string]interface{}{
		"status":             "ok",
		"protocol":           currentRuntimeProtocol,
		"results":            results,
		"supported_models":   supported,
		"written_cards":      writtenCards,
		"user_cards_path":    cardsPath,
		"write_back_error":   writeBackError,
		"write_back_enabled": writeBack,
	}
	writeWebAPIJSON(w, http.StatusOK, response)
}

// ---------------------------------------------------------------------------
// POST /web/api/config/providers/auto-import — 自动导入 Provider
// ---------------------------------------------------------------------------

// HandleChatWebAPIConfigProvidersAutoImport 一键生成并保存 provider 配置，
// 逻辑与 `aicli login --provider <name> --base-url <url> --api-key <key>`
// 完全同源（复用 provider_login.go 的 runProviderLogin）：探测/校验协议、
// 拉取并校验 models 列表、按 model card 分组生成 api_path / forward_url /
// default_model / supported_models / model_capabilities，并把 API key 写入
// Key Store（auth.json），config.yaml 只落 api_key_ref。无交互（Interactive
// 关闭），任何需要人工输入的环节直接报错，不会挂起。
func HandleChatWebAPIConfigProvidersAutoImport(w http.ResponseWriter, r *http.Request) {
	if !chatWebRequireMethod(w, r, http.MethodPost) {
		return
	}
	var req chatWebProviderAutoImportRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		chatWebWriteError(w, http.StatusBadRequest, err)
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.BaseURL = strings.TrimSpace(req.BaseURL)
	req.APIKey = strings.TrimSpace(req.APIKey)
	req.Protocol = strings.TrimSpace(req.Protocol)
	if req.Name == "" {
		chatWebWriteError(w, http.StatusBadRequest, errRequiredField("provider name"))
		return
	}
	if req.BaseURL == "" {
		chatWebWriteError(w, http.StatusBadRequest, errRequiredField("base_url"))
		return
	}
	if req.APIKey == "" {
		chatWebWriteError(w, http.StatusBadRequest, errRequiredField("api_key"))
		return
	}
	if req.Protocol == "" {
		req.Protocol = providerLoginProtocolAuto
	}
	if !isSupportedLoginProtocol(req.Protocol) {
		chatWebWriteError(w, http.StatusBadRequest, fmt.Errorf("unsupported login protocol %q", req.Protocol))
		return
	}
	if strings.EqualFold(req.Protocol, "codex-oauth") {
		chatWebWriteError(w, http.StatusBadRequest, fmt.Errorf("codex-oauth 需要设备码交互，请在终端使用 aicli login 或选择 codex-apikey"))
		return
	}
	path, err := chatWebConfigPath()
	if err != nil {
		chatWebWriteError(w, http.StatusInternalServerError, err)
		return
	}
	if path == "" {
		chatWebWriteError(w, http.StatusBadRequest, errNoConfigPath())
		return
	}

	timeout := time.Duration(req.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	if timeout > 120*time.Second {
		timeout = 120 * time.Second
	}
	loginReq := providerLoginRequest{
		Context:        r.Context(),
		Config:         configPtrOrNil(chatWebSession()),
		ConfigPath:     path,
		ProviderName:   req.Name,
		LoginProtocol:  req.Protocol,
		BaseURL:        req.BaseURL,
		APIKey:         req.APIKey,
		DefaultModel:   strings.TrimSpace(req.DefaultModel),
		ModelsPath:     strings.TrimSpace(req.ModelsPath),
		SetDefault:     req.SetDefault,
		Interactive:    false,
		Timeout:        timeout,
		SkipSiteDetect: true, // Web 端不做站点类型探测/账号同步，保持导入快速聚焦
		SkipAccount:    true,
	}
	result, err := runProviderLogin(loginReq)
	if err != nil {
		chatWebWriteError(w, http.StatusBadGateway, err)
		return
	}

	// runProviderLogin 内部已通过 applyProviderLoginConfigUpdate 更新传入的
	// session.Config，并写盘（config.yaml + auth.json）。与其他配置端点一致，
	// 这里再按磁盘重载会话内存配置，并失效运行时缓存的 provider 实例，
	// 让 /web/api/runtime 与 /model 立即看到新配置。
	chatWebRefreshSessionConfig()
	for _, info := range result.ProviderConfigs {
		chatWebInvalidateRuntimeProvider(info.ProviderName)
	}
	writeWebAPIJSON(w, http.StatusOK, result)
}

// probeClientForProvider 返回匿名探测用的 HTTP client：复用 provider 的
// 代理/头配置（与 validateProviderModels 同源），仅覆盖短超时，避免公开端点
// 探测因慢响应拖慢整个 fetch-models 流程。
func probeClientForProvider(session *ChatSession, provider *agentconfig.Provider) *http.Client {
	var client *http.Client
	if session != nil && session.Config != nil {
		client = httpclient.GetHTTPClientWithProvider(session.Config, provider)
	}
	if client == nil {
		client = http.DefaultClient
	}
	cloned := *client
	cloned.Timeout = 6 * time.Second
	return &cloned
}

// configPtrOrNil 返回会话的 agentconfig.Config 指针；无会话时为 nil
// （validateProviderModels 在 nil 时退化为 http.DefaultClient 直连）。
func configPtrOrNil(session *ChatSession) *agentconfig.Config {
	if session == nil {
		return nil
	}
	return session.Config
}

// ---------------------------------------------------------------------------
// POST /web/api/config/chat — aicli.chat 默认偏好
// ---------------------------------------------------------------------------

// HandleChatWebAPIConfigChat 更新 aicli.chat 的默认 provider / model /
// reasoning_effort（仅写非空字段；reasoning_effort 由持久化层归一化）。
func HandleChatWebAPIConfigChat(w http.ResponseWriter, r *http.Request) {
	if !chatWebRequireMethod(w, r, http.MethodPost) {
		return
	}
	var req chatWebChatWriteRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		chatWebWriteError(w, http.StatusBadRequest, err)
		return
	}
	path, err := chatWebConfigPath()
	if err != nil {
		chatWebWriteError(w, http.StatusInternalServerError, err)
		return
	}
	if path == "" {
		chatWebWriteError(w, http.StatusBadRequest, errNoConfigPath())
		return
	}
	update := agentconfig.AICLIChatPreferenceUpdate{}
	if v := strings.TrimSpace(req.DefaultProvider); v != "" {
		update.DefaultProvider = webStringPtr(v)
	}
	if v := strings.TrimSpace(req.DefaultModel); v != "" {
		update.DefaultModel = webStringPtr(v)
	}
	if v := strings.TrimSpace(req.ReasoningEffort); v != "" {
		update.ReasoningEffort = webStringPtr(v)
	}
	if _, err := agentconfig.UpdateAICLIChatPreferences(path, update); err != nil {
		chatWebWriteError(w, http.StatusInternalServerError, err)
		return
	}
	chatWebRefreshSessionConfig()
	writeWebAPIJSON(w, http.StatusOK, map[string]interface{}{"status": "ok"})
}

func webStringPtr(s string) *string { return &s }

func errRequiredField(field string) error {
	return &webConfigError{msg: field + " is required"}
}

func errNoConfigPath() error {
	return &webConfigError{msg: "no active session config path"}
}

// webConfigError 是配置端点使用的轻量错误（避免依赖 fmt 的通用包装）。
type webConfigError struct{ msg string }

func (e *webConfigError) Error() string { return e.msg }
