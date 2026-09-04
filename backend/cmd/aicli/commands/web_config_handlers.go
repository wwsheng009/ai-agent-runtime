package commands

import (
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
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
	ChatWebAPIConfigChatPath             = "/web/api/config/chat"
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
	Name            string               `json:"name"`
	Protocol        string               `json:"protocol"`
	Enabled         bool                 `json:"enabled"`
	BaseURL         string               `json:"base_url"`
	APIPath         string               `json:"api_path"`
	ForwardURL      string               `json:"forward_url"`
	DefaultModel    string               `json:"default_model"`
	SupportedModels []string             `json:"supported_models,omitempty"`
	Models          []chatWebConfigModel `json:"models,omitempty"`
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
	Name               string                                 `json:"name"`
	Protocol           string                                 `json:"protocol"`
	BaseURL            string                                 `json:"base_url"`
	APIPath            string                                 `json:"api_path"`
	ForwardURL         string                                 `json:"forward_url"`
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
		return strings.TrimSpace(session.RuntimeConfigPath), nil
	}
	return "", nil
}

// chatWebRefreshSessionConfig 写成功后刷新会话内存配置，使运行时元数据同步。
func chatWebRefreshSessionConfig() {
	if session := chatWebSession(); session != nil {
		_ = reloadChatConfigForModelCommand(session)
	}
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
		entry := chatWebConfigProvider{
			Name:            name,
			Protocol:        strings.TrimSpace(provider.Protocol),
			Enabled:         provider.Enabled,
			BaseURL:         strings.TrimSpace(provider.BaseURL),
			APIPath:         strings.TrimSpace(provider.APIPath),
			ForwardURL:      strings.TrimSpace(provider.ForwardURL),
			DefaultModel:    strings.TrimSpace(provider.DefaultModel),
			SupportedModels: append([]string(nil), provider.SupportedModels...),
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
	if req.ForwardURL != "" {
		update.ForwardURL = webStringPtr(strings.TrimSpace(req.ForwardURL))
	}
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
	writeWebAPIJSON(w, http.StatusOK, map[string]interface{}{
		"status":   "ok",
		"provider": req.Name,
	})
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
	writeWebAPIJSON(w, http.StatusOK, map[string]interface{}{
		"status":  "ok",
		"enabled": req.Enabled,
		"names":   names,
	})
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
