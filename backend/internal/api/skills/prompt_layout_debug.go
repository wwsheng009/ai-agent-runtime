package skills

import (
	"encoding/json"
	"net/http"
	"strings"

	runtimeprompt "github.com/wwsheng009/ai-agent-runtime/internal/prompt"
)

type promptLayoutPreviewRequest struct {
	Profile       string              `json:"profile,omitempty"`
	Agent         string              `json:"agent,omitempty"`
	Provider      string              `json:"provider,omitempty"`
	Model         string              `json:"model,omitempty"`
	UserID        string              `json:"user_id,omitempty"`
	TenantID      string              `json:"tenant_id,omitempty"`
	ProjectID     string              `json:"project_id,omitempty"`
	WorkspacePath string              `json:"workspace_path,omitempty"`
	Messages      []map[string]string `json:"messages,omitempty"`
}

type promptLayoutPreviewResponse struct {
	ProfileReference    string                   `json:"profile_reference,omitempty"`
	Provider            string                   `json:"provider,omitempty"`
	Model               string                   `json:"model,omitempty"`
	WorkspacePath       string                   `json:"workspace_path,omitempty"`
	Fragments           []runtimeprompt.Fragment `json:"fragments,omitempty"`
	InstructionMessages []promptLayoutMessage    `json:"instruction_messages,omitempty"`
	PromptLayout        string                   `json:"prompt_layout,omitempty"`
}

type promptLayoutMessage struct {
	Role     string                 `json:"role,omitempty"`
	Content  string                 `json:"content,omitempty"`
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

func (h *Handler) PreviewPromptLayout(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeUsageAdmin(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}

	var req promptLayoutPreviewRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}

	ctx := r.Context()
	usageScope := h.resolveUsageScope(r, req.TenantID, req.ProjectID, req.UserID)
	effectiveProfile := strings.TrimSpace(req.Profile)
	if effectiveProfile == "" && isAutoProfileRef(h.profileDefaultRef) {
		effectiveProfile = h.profileDefaultRef
	}
	if isAutoProfileRef(effectiveProfile) {
		effectiveProfile = routeProfileForPrompt(extractLastUserPrompt(req.Messages))
	}

	workspacePath := strings.TrimSpace(req.WorkspacePath)
	profileState, cleanup, err := h.resolveProfileRuntimeState(ctx, effectiveProfile, req.Agent, usageScope, workspacePath)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	if cleanup != nil {
		defer cleanup()
	}

	selectedConfig := h.resolveRuntimeConfig(usageScope)
	if profileState != nil && profileState.RuntimeConfig != nil {
		selectedConfig = profileState.RuntimeConfig
	}
	provider := strings.TrimSpace(req.Provider)
	if provider == "" {
		provider = resolveAgentProvider(profileState, selectedConfig, h.llmRuntime)
	}
	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = resolveAgentModel(profileState, selectedConfig, h.llmRuntime)
	}

	layers := buildRuntimeInstructionLayers(profileState, workspacePath)
	instructionMessages := buildRuntimeInstructionMessages(profileState, workspacePath, provider)
	resp := promptLayoutPreviewResponse{
		Provider:      provider,
		Model:         model,
		WorkspacePath: workspacePath,
		PromptLayout:  runtimeprompt.RenderInstructionMessagesLayout(instructionMessages),
	}
	if profileState != nil {
		resp.ProfileReference = strings.TrimSpace(profileState.Reference)
	}
	if layers != nil && len(layers.Fragments) > 0 {
		resp.Fragments = append([]runtimeprompt.Fragment(nil), layers.Fragments...)
	}
	if len(instructionMessages) > 0 {
		resp.InstructionMessages = make([]promptLayoutMessage, 0, len(instructionMessages))
		for _, message := range instructionMessages {
			payload := promptLayoutMessage{
				Role:    strings.TrimSpace(message.Role),
				Content: strings.TrimSpace(message.Content),
			}
			if len(message.Metadata) > 0 {
				payload.Metadata = make(map[string]interface{}, len(message.Metadata))
				for key, value := range message.Metadata {
					payload.Metadata[key] = value
				}
			}
			resp.InstructionMessages = append(resp.InstructionMessages, payload)
		}
	}

	h.writeJSON(w, http.StatusOK, resp)
}
