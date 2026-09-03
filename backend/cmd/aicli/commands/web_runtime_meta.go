package commands

import (
	"net/http"
	"sort"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

// ChatWebAPIRuntimePath 是 provider/model/reasoning_effort 运行时元数据端点。
// 供 micro web client 的配置选择器拉取：当前生效值 + 可用 provider 及模型列表。
// 切换动作本身不走此端点——前端把 `/model ...` 命令注入 /web/api/input，
// 由主循环统一执行（与 TTY 行为一致）。
const ChatWebAPIRuntimePath = "/web/api/runtime"

// chatWebRuntimeMeta 是 GET /web/api/runtime 的响应体。
type chatWebRuntimeMeta struct {
	Current   chatWebRuntimeCurrent   `json:"current"`
	Providers []chatWebRuntimeProvider `json:"providers"`
}

// chatWebRuntimeCurrent 是当前会话生效的 provider/model/reasoning 配置。
type chatWebRuntimeCurrent struct {
	Provider        string `json:"provider"`
	Protocol        string `json:"protocol"`
	Model           string `json:"model"`
	ReasoningEffort string `json:"reasoning_effort"`
	BaseURL         string `json:"base_url"`
}

// chatWebRuntimeProvider 是单个可用 provider 及其可选模型。
type chatWebRuntimeProvider struct {
	Name         string   `json:"name"`
	Protocol     string   `json:"protocol"`
	DefaultModel string   `json:"default_model"`
	Models       []string `json:"models,omitempty"`
}

// HandleChatWebAPIRuntime 返回当前会话的运行时 provider/model 元数据。
func HandleChatWebAPIRuntime(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeWebAPIJSON(w, http.StatusMethodNotAllowed, map[string]string{
			"status": "rejected",
			"reason": "method not allowed",
		})
		return
	}

	meta := chatWebRuntimeMeta{
		Current:   chatWebRuntimeCurrent{},
		Providers: []chatWebRuntimeProvider{},
	}

	session := chatWebSession()
	if session == nil {
		writeWebAPIJSON(w, http.StatusOK, meta)
		return
	}

	meta.Current = chatWebRuntimeCurrent{
		Provider:        strings.TrimSpace(session.ProviderName),
		Protocol:        strings.TrimSpace(session.Provider.GetProtocol()),
		Model:           strings.TrimSpace(effectiveRuntimeModel(session)),
		ReasoningEffort: strings.TrimSpace(session.ReasoningEffort),
		BaseURL:         strings.TrimSpace(session.BaseURL),
	}

	// 与 /model 命令一致：使用前重新读取配置文件，让 /login 等方式新增的
	// provider 立即进入选择器。读取失败时保留内存配置（不影响当前会话）。
	_ = reloadChatConfigForModelCommand(session)

	if session.Config != nil {
		for name, provider := range session.Config.Providers.Items {
			if !provider.Enabled {
				continue
			}
			entry := chatWebRuntimeProvider{
				Name:         name,
				Protocol:     strings.TrimSpace(provider.Protocol),
				DefaultModel: strings.TrimSpace(provider.DefaultModel),
				Models:       collectRuntimeProviderModels(provider),
			}
			meta.Providers = append(meta.Providers, entry)
		}
		sort.Slice(meta.Providers, func(i, j int) bool {
			return meta.Providers[i].Name < meta.Providers[j].Name
		})
	}

	writeWebAPIJSON(w, http.StatusOK, meta)
}

// collectRuntimeProviderModels 返回 provider 的可选模型（default + supported，
// 去重并按名称排序）。为空时前端允许自由输入模型名。
func collectRuntimeProviderModels(provider agentconfig.Provider) []string {
	seen := make(map[string]struct{})
	models := make([]string, 0, len(provider.SupportedModels)+1)
	add := func(m string) {
		m = strings.TrimSpace(m)
		if m == "" {
			return
		}
		if _, dup := seen[m]; dup {
			return
		}
		seen[m] = struct{}{}
		models = append(models, m)
	}
	add(provider.DefaultModel)
	for _, m := range provider.SupportedModels {
		add(m)
	}
	sort.Strings(models)
	return models
}