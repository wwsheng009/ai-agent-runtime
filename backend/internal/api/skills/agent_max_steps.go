package skills

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/errors"
)

// runtimeAgentMaxStepsLimit 是后端接受的最大步骤数上限（0 = 不限制，交给模型自行结束）。
const runtimeAgentMaxStepsLimit = 100

// AgentMaxStepsPersister 把 agent 最大步骤数落盘并同步内存配置，返回实际写入的配置文件路径。
type AgentMaxStepsPersister func(maxSteps int) (configFile string, err error)

// SetAgentMaxStepsPersister 设置 agent 最大步骤数持久化回调（runtime-server 里接 RuntimeManager）。
func (h *Handler) SetAgentMaxStepsPersister(persister AgentMaxStepsPersister) {
	h.agentMaxStepsPersister = persister
}

// AgentMaxStepsProvider 读取当前生效的 agent 最大步骤数及其来源配置文件路径。
type AgentMaxStepsProvider func() (maxSteps int, configFile string, err error)

// SetAgentMaxStepsProvider 设置 agent 最大步骤数读取回调（runtime-server 里接 RuntimeManager）。
func (h *Handler) SetAgentMaxStepsProvider(provider AgentMaxStepsProvider) {
	h.agentMaxStepsProvider = provider
}

// UpdateAgentMaxSteps 更新 agent 最大步骤数：同时写入 runtime 配置的内存快照与配置文件。
//
// 该值只影响「请求未显式指定 max_steps」时的后端缺省；前端工作区设置会随每轮请求带上
// max_steps，因此对下一轮立即生效，而本接口保证重启 runtime 后缺省值不丢失。
func (h *Handler) UpdateAgentMaxSteps(w http.ResponseWriter, r *http.Request) {
	if h == nil {
		return
	}
	if h.agentMaxStepsPersister == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid,
			"agent max steps persister is not configured"))
		return
	}

	var req struct {
		MaxSteps *int `json:"max_steps"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed,
			"failed to parse request body"))
		return
	}
	if req.MaxSteps == nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed,
			"max_steps is required"))
		return
	}
	maxSteps := *req.MaxSteps
	if maxSteps < 0 || maxSteps > runtimeAgentMaxStepsLimit {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed,
			fmt.Sprintf("max_steps must be between 0 and %d", runtimeAgentMaxStepsLimit)))
		return
	}

	configFile, err := h.agentMaxStepsPersister(maxSteps)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, errors.Wrap(errors.ErrConfigInvalid,
			"failed to persist agent max steps", err))
		return
	}
	configFile = strings.TrimSpace(configFile)

	h.publishSkillsChangedEvent(r, map[string]interface{}{
		"action":      "agent-max-steps-update",
		"status":      "success",
		"max_steps":   maxSteps,
		"config_file": configFile,
		"changed_by":  requestChangedBy(r),
	})

	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"updated":     true,
		"max_steps":   maxSteps,
		"config_file": configFile,
	})
}

// GetAgentMaxSteps 读取 agent 最大步骤数的服务端缺省值，以及该值所在的配置文件路径。
//
// 只读接口：不改内存快照、不写配置文件，因此也不发 skills-changed 事件
// （前端只是把服务端缺省值回显到设置页）。
func (h *Handler) GetAgentMaxSteps(w http.ResponseWriter, r *http.Request) {
	if h == nil {
		return
	}
	if h.agentMaxStepsProvider == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid,
			"agent max steps provider is not configured"))
		return
	}

	maxSteps, configFile, err := h.agentMaxStepsProvider()
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, errors.Wrap(errors.ErrConfigInvalid,
			"failed to load agent max steps", err))
		return
	}

	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"limit":       runtimeAgentMaxStepsLimit,
		"max_steps":   maxSteps,
		"config_file": strings.TrimSpace(configFile),
	})
}
