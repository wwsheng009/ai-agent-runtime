package skills

import (
	"context"
	"net/http"
	"strings"

	"github.com/gorilla/mux"
	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimeerrors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	"github.com/wwsheng009/ai-agent-runtime/internal/providerops"
)

// RuntimeProviderModelsRequest 是 runtime provider 编辑器的「获取模型列表 /
// 自动导入 / 模型探测」三类请求的公共载荷。
//
// 语义与 aicli micro web client 的 /web/api/config/providers/* 保持一致：
// 未保存的 provider 允许只传 name + base_url(+api_key)；已保存的 provider 可只传
// name，其余字段从 runtime 的 aicli 配置快照补齐。请求里显式给出的字段优先于
// 配置快照（尤其是 api_key：编辑器里新输入的 key 必须优先于 auth store 里的旧值）。
type RuntimeProviderModelsRequest struct {
	Name           string            `json:"name,omitempty"`
	BaseURL        string            `json:"base_url,omitempty"`
	APIKey         string            `json:"api_key,omitempty"`
	APIKeyRef      string            `json:"api_key_ref,omitempty"`
	Protocol       string            `json:"protocol,omitempty"`
	AuthMode       string            `json:"auth_mode,omitempty"`
	ModelsPath     string            `json:"models_path,omitempty"`
	Headers        map[string]string `json:"headers,omitempty"`
	TimeoutSeconds int               `json:"timeout_seconds,omitempty"`
}

// RuntimeProviderAutoImportRequest 在公共载荷之上允许指定默认模型。
type RuntimeProviderAutoImportRequest struct {
	RuntimeProviderModelsRequest
	DefaultModel string `json:"default_model,omitempty"`
}

// RuntimeProviderProbeRequest 指定要探测的模型与协议；都为空时按 provider 已
// 保存的 supported_models 与协议探测。
type RuntimeProviderProbeRequest struct {
	RuntimeProviderModelsRequest
	Models    []string `json:"models,omitempty"`
	Protocols []string `json:"protocols,omitempty"`
}

// RuntimeProviderClassification 是 /models 清单按协议 / provider template
// 分类后的前端视图（与 micro web client fetch-models 的 groups 口径同源）。
type RuntimeProviderClassification struct {
	LoginProtocol    string                   `json:"login_protocol"`
	RuntimeProtocol  string                   `json:"runtime_protocol"`
	TotalModels      int                      `json:"total_models"`
	PrimaryModelIDs  []string                 `json:"primary_model_ids"`
	VerifiedModelIDs []string                 `json:"verified_model_ids"`
	AssumedModelIDs  []string                 `json:"assumed_model_ids"`
	OtherModelsCount int                      `json:"other_models_count"`
	Groups           []providerops.ModelGroup `json:"groups"`
}

// RuntimeProviderModelsResult 是「获取模型列表」的响应。model_ids 是可直接
// 合并进 supported_models 的口径（openai 协议含 assumed；其它协议只保留
// verified，assumed 单独列出供用户手动确认）。
type RuntimeProviderModelsResult struct {
	Endpoint         string                               `json:"endpoint"`
	StatusCode       int                                  `json:"status_code"`
	VerifiedAt       string                               `json:"verified_at"`
	AnonymousAllowed bool                                 `json:"anonymous_allowed"`
	ModelIDs         []string                             `json:"model_ids"`
	AllModelIDs      []string                             `json:"all_model_ids,omitempty"`
	AssumedModelIDs  []string                             `json:"assumed_model_ids,omitempty"`
	Models           []providerops.ModelInfo              `json:"models"`
	LoginProtocol    string                               `json:"login_protocol,omitempty"`
	RuntimeProtocol  string                               `json:"runtime_protocol,omitempty"`
	Classification   *RuntimeProviderClassification       `json:"classification"`
	Metadata         map[string]providerops.ModelMetadata `json:"metadata,omitempty"`
	Warnings         []string                             `json:"warnings,omitempty"`
}

// RuntimeProviderAutoImportResult 是「自动导入」的草稿补丁：调用方把它合并进
// 当前编辑草稿，由既有保存链路落盘（runtime server 不在本接口内写配置）。
type RuntimeProviderAutoImportResult struct {
	Name               string                                     `json:"name"`
	Protocol           string                                     `json:"protocol"`
	LoginProtocol      string                                     `json:"login_protocol,omitempty"`
	BaseURL            string                                     `json:"base_url"`
	APIPath            string                                     `json:"api_path,omitempty"`
	ForwardURL         string                                     `json:"forward_url,omitempty"`
	DefaultModel       string                                     `json:"default_model,omitempty"`
	SupportedModels    []string                                   `json:"supported_models"`
	AssumedModelIDs    []string                                   `json:"assumed_model_ids,omitempty"`
	SupportTypes       []string                                   `json:"support_types,omitempty"`
	MaxTokensLimit     int                                        `json:"max_tokens_limit,omitempty"`
	ModelCapabilities  map[string]agentconfig.ModelCapabilitySpec `json:"model_capabilities,omitempty"`
	SiteType           string                                     `json:"site_type,omitempty"`
	SiteTypeConfidence string                                     `json:"site_type_confidence,omitempty"`
	SiteTypeScores     map[string]int                             `json:"site_type_scores,omitempty"`
	Account            *agentconfig.ProviderAccountSnapshot       `json:"account,omitempty"`
	Models             []providerops.ModelInfo                    `json:"models,omitempty"`
	Warnings           []string                                   `json:"warnings,omitempty"`
}

// RuntimeProviderProbeResult 汇总探测矩阵。
type RuntimeProviderProbeResult struct {
	Results []providerops.ProbeResult `json:"results"`
}

// ProviderOpsService 是 runtime server 侧 provider 模型发现能力的服务抽象。
// 实现必须复用 internal/providerops（与 aicli CLI / micro web client 同一份
// 后端核心逻辑），不允许在 skills 包内复制实现。
type ProviderOpsService interface {
	FetchModels(context.Context, RuntimeProviderModelsRequest) (*RuntimeProviderModelsResult, error)
	AutoImport(context.Context, RuntimeProviderAutoImportRequest) (*RuntimeProviderAutoImportResult, error)
	ProbeModels(context.Context, RuntimeProviderProbeRequest) (*RuntimeProviderProbeResult, error)
}

// SetProviderOpsService 注入 provider ops 服务；为 nil 时 RegisterRoutes 会安装
// 默认实现（它只依赖 handler 的 aicli 配置快照）。
func (h *Handler) SetProviderOpsService(service ProviderOpsService) {
	if h == nil {
		return
	}
	h.providerOpsService = service
}

func (h *Handler) providerOps() ProviderOpsService {
	if h == nil {
		return nil
	}
	if h.providerOpsService != nil {
		return h.providerOpsService
	}
	return newRuntimeProviderOpsService(h.aicliConfigSnapshot)
}

// FetchRuntimeProviderModels 处理 POST /api/runtime/providers/fetch-models。
func (h *Handler) FetchRuntimeProviderModels(w http.ResponseWriter, r *http.Request) {
	service := h.providerOps()
	if service == nil {
		h.writeError(w, http.StatusServiceUnavailable, runtimeerrors.New(runtimeerrors.ErrConfigInvalid, "provider ops service not configured"))
		return
	}
	var req RuntimeProviderModelsRequest
	if err := decodeSiteAccountRequest(r, &req, false); err != nil {
		h.writeError(w, http.StatusBadRequest, runtimeerrors.New(runtimeerrors.ErrValidationFailed, "invalid provider fetch-models payload"))
		return
	}
	if err := validateRuntimeProviderRequest(req); err != nil {
		h.writeError(w, http.StatusBadRequest, runtimeerrors.Wrap(runtimeerrors.ErrValidationFailed, "invalid provider fetch-models payload", err))
		return
	}
	result, err := service.FetchModels(r.Context(), req)
	if err != nil {
		h.writeProviderOpsError(w, err)
		return
	}
	h.writeJSON(w, http.StatusOK, result)
}

// AutoImportRuntimeProvider 处理 POST /api/runtime/providers/auto-import。
func (h *Handler) AutoImportRuntimeProvider(w http.ResponseWriter, r *http.Request) {
	service := h.providerOps()
	if service == nil {
		h.writeError(w, http.StatusServiceUnavailable, runtimeerrors.New(runtimeerrors.ErrConfigInvalid, "provider ops service not configured"))
		return
	}
	var req RuntimeProviderAutoImportRequest
	if err := decodeSiteAccountRequest(r, &req, false); err != nil {
		h.writeError(w, http.StatusBadRequest, runtimeerrors.New(runtimeerrors.ErrValidationFailed, "invalid provider auto-import payload"))
		return
	}
	if err := validateRuntimeProviderRequest(req.RuntimeProviderModelsRequest); err != nil {
		h.writeError(w, http.StatusBadRequest, runtimeerrors.Wrap(runtimeerrors.ErrValidationFailed, "invalid provider auto-import payload", err))
		return
	}
	result, err := service.AutoImport(r.Context(), req)
	if err != nil {
		h.writeProviderOpsError(w, err)
		return
	}
	h.writeJSON(w, http.StatusOK, result)
}

// ProbeRuntimeProviderModels 处理 POST /api/runtime/providers/probe-models。
func (h *Handler) ProbeRuntimeProviderModels(w http.ResponseWriter, r *http.Request) {
	service := h.providerOps()
	if service == nil {
		h.writeError(w, http.StatusServiceUnavailable, runtimeerrors.New(runtimeerrors.ErrConfigInvalid, "provider ops service not configured"))
		return
	}
	var req RuntimeProviderProbeRequest
	if err := decodeSiteAccountRequest(r, &req, false); err != nil {
		h.writeError(w, http.StatusBadRequest, runtimeerrors.New(runtimeerrors.ErrValidationFailed, "invalid provider probe-models payload"))
		return
	}
	if err := validateRuntimeProviderRequest(req.RuntimeProviderModelsRequest); err != nil {
		h.writeError(w, http.StatusBadRequest, runtimeerrors.Wrap(runtimeerrors.ErrValidationFailed, "invalid provider probe-models payload", err))
		return
	}
	result, err := service.ProbeModels(r.Context(), req)
	if err != nil {
		h.writeProviderOpsError(w, err)
		return
	}
	h.writeJSON(w, http.StatusOK, result)
}

// validateRuntimeProviderRequest 要求 base_url（或可解析出 base_url 的 name）存在；
// 已保存 provider 只传 name 的请求由服务层从配置快照补齐，这里放行。
func validateRuntimeProviderRequest(req RuntimeProviderModelsRequest) error {
	if strings.TrimSpace(req.BaseURL) != "" || strings.TrimSpace(req.Name) != "" {
		return nil
	}
	return runtimeerrors.New(runtimeerrors.ErrValidationFailed, "base_url or name is required")
}

func (h *Handler) writeProviderOpsError(w http.ResponseWriter, err error) {
	status := http.StatusBadGateway
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "required") || strings.Contains(message, "invalid") || strings.Contains(message, "not found"):
		status = http.StatusBadRequest
	case strings.Contains(message, "timeout") || strings.Contains(message, "deadline"):
		status = http.StatusGatewayTimeout
	}
	h.writeError(w, status, runtimeerrors.Wrap(runtimeerrors.ErrConfigInvalid, "provider models request failed", err))
}

// registerProviderOpsRoutes 挂载 provider 模型发现端点（与 micro web client 的
// /web/api/config/providers/{fetch-models,auto-import,probe-models} 同源）。
func (h *Handler) registerProviderOpsRoutes(runtimeRouter *mux.Router) {
	if runtimeRouter == nil {
		return
	}
	runtimeRouter.HandleFunc("/providers/fetch-models", h.FetchRuntimeProviderModels).Methods(http.MethodPost)
	runtimeRouter.HandleFunc("/providers/auto-import", h.AutoImportRuntimeProvider).Methods(http.MethodPost)
	runtimeRouter.HandleFunc("/providers/probe-models", h.ProbeRuntimeProviderModels).Methods(http.MethodPost)
}
