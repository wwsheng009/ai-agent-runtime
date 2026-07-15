package skills

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/errors"
)

type AgentRoutePreviewRequest struct {
	Document ConfigDocumentSaveRequest `json:"document"`
	Scope    string                    `json:"scope,omitempty"`
	Workflow string                    `json:"workflow,omitempty"`
	Parent   AgentRoutePreviewParent   `json:"parent,omitempty"`
	Task     AgentRoutePreviewTask     `json:"task,omitempty"`
}

type AgentRoutePreviewParent struct {
	Provider        string `json:"provider,omitempty"`
	Model           string `json:"model,omitempty"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
	MaxTokens       int    `json:"max_tokens,omitempty"`
	Timeout         string `json:"timeout,omitempty"`
}

type AgentRoutePreviewTask struct {
	Role                string `json:"role,omitempty"`
	Goal                string `json:"goal,omitempty"`
	Difficulty          string `json:"difficulty,omitempty"`
	DifficultyRationale string `json:"difficulty_rationale,omitempty"`
	Provider            string `json:"provider,omitempty"`
	Model               string `json:"model,omitempty"`
	ReasoningEffort     string `json:"reasoning_effort,omitempty"`
	BudgetTokens        int    `json:"budget_tokens,omitempty"`
	Timeout             string `json:"timeout,omitempty"`
	ReadOnly            *bool  `json:"read_only,omitempty"`
}

type AgentRoutePreviewResult struct {
	Scope          string                    `json:"scope"`
	RoutingSource  string                    `json:"routing_source"`
	RoutingEnabled bool                      `json:"routing_enabled"`
	Parent         AgentRoutePreviewParent   `json:"parent"`
	Decision       AgentRoutePreviewDecision `json:"decision"`
}

type AgentRoutePreviewDecision struct {
	Difficulty          string   `json:"difficulty,omitempty"`
	DifficultySource    string   `json:"difficulty_source,omitempty"`
	DifficultyRationale string   `json:"difficulty_rationale,omitempty"`
	Provider            string   `json:"provider,omitempty"`
	Model               string   `json:"model,omitempty"`
	ReasoningEffort     string   `json:"reasoning_effort,omitempty"`
	MaxTokens           int      `json:"max_tokens,omitempty"`
	Timeout             string   `json:"timeout,omitempty"`
	Source              string   `json:"source,omitempty"`
	Warnings            []string `json:"warnings,omitempty"`
	FallbackUsed        bool     `json:"fallback_used,omitempty"`
	FallbackReason      string   `json:"fallback_reason,omitempty"`
}

type AgentRoutePreviewService interface {
	PreviewAgentRoute(req AgentRoutePreviewRequest) (*AgentRoutePreviewResult, error)
}

func (h *Handler) PreviewAgentRoute(w http.ResponseWriter, r *http.Request) {
	service, ok := h.configDocumentService.(AgentRoutePreviewService)
	if !ok || service == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(
			errors.ErrConfigInvalid,
			"agent route preview service not configured",
		))
		return
	}

	req := &AgentRoutePreviewRequest{}
	if err := json.NewDecoder(r.Body).Decode(req); err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(
			errors.ErrValidationFailed,
			"invalid agent route preview payload",
		))
		return
	}
	if req.Document.Raw == nil && req.Document.Parsed == nil {
		h.writeError(w, http.StatusBadRequest, errors.New(
			errors.ErrValidationFailed,
			"agent route preview requires raw or parsed config content",
		))
		return
	}
	req.Document.Mode = strings.TrimSpace(req.Document.Mode)

	result, err := service.PreviewAgentRoute(*req)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, errors.Wrap(
			errors.ErrConfigInvalid,
			"failed to preview agent route",
			err,
		))
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{"route": result})
}
