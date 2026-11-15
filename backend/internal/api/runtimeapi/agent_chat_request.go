package runtimeapi

import (
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// agentChatRequest is the public /api/agent/chat request contract. It is kept
// as a named type so the actor and direct execution paths can be unit-tested
// against the same wire shape without duplicating anonymous structs.
type agentChatRequest struct {
	Messages                   []map[string]string          `json:"messages"`
	Profile                    string                       `json:"profile,omitempty"`
	Agent                      string                       `json:"agent,omitempty"`
	Provider                   string                       `json:"provider,omitempty"`
	Model                      string                       `json:"model,omitempty"`
	ReasoningEffort            string                       `json:"reasoning_effort,omitempty"`
	Thinking                   *runtimetypes.ThinkingConfig `json:"thinking,omitempty"`
	SessionID                  string                       `json:"session_id,omitempty"`
	TeamID                     string                       `json:"team_id,omitempty"`
	TaskID                     string                       `json:"task_id,omitempty"`
	UserID                     string                       `json:"user_id,omitempty"`
	TenantID                   string                       `json:"tenant_id,omitempty"`
	ProjectID                  string                       `json:"project_id,omitempty"`
	WorkspacePath              string                       `json:"workspace_path,omitempty"`
	MaxSteps                   int                          `json:"max_steps,omitempty"`
	BudgetTokens               int                          `json:"budget_tokens,omitempty"`
	DisableTools               bool                         `json:"disable_tools,omitempty"`
	EnableRoute                bool                         `json:"enable_routing,omitempty"`
	EnableReAct                bool                         `json:"enable_react,omitempty"`
	ExposeSkills               []string                     `json:"expose_skills,omitempty"`
	SkillArgs                  []string                     `json:"skill_args,omitempty"`
	PlanningMode               string                       `json:"planning_mode,omitempty"`
	ExecutePlannedSubagents    bool                         `json:"execute_planned_subagents,omitempty"`
	AllowWritePlannedSubagents bool                         `json:"allow_write_planned_subagents,omitempty"`
	PatchDecisionPolicy        string                       `json:"patch_decision_policy,omitempty"`
	ApproveBlockedPatches      bool                         `json:"approve_blocked_patches,omitempty"`
	PatchApprovalNote          string                       `json:"patch_approval_note,omitempty"`
	PatchApproval              *agent.PatchApproval         `json:"patch_approval,omitempty"`
	TurnID                     string                       `json:"turn_id,omitempty"`
	Stream                     bool                         `json:"stream,omitempty"`
	ResumeOnDisconnect         bool                         `json:"resume_on_disconnect,omitempty"`
}

// agentChatUsesSessionActor selects the durable actor path for the core Web
// text turn. All request fields that only affect the ReAct turn are accepted;
// the actor applies Team/Task as context-only options and DisableTools as a
// per-run execution override.
func (h *Handler) agentChatUsesSessionActor(session *chat.Session, req *agentChatRequest) bool {
	if h == nil || session == nil || req == nil {
		return false
	}
	if h.sessionManager == nil || h.llmRuntime == nil {
		return false
	}
	if !req.EnableReAct {
		return false
	}
	if len(req.Messages) == 0 {
		return false
	}
	last := req.Messages[len(req.Messages)-1]
	if !strings.EqualFold(strings.TrimSpace(last["role"]), "user") {
		return false
	}
	return true
}
