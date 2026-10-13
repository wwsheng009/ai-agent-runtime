package runtimehost

import (
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

func sessionWithContext(ctx map[string]interface{}) *chat.Session {
	return &chat.Session{Metadata: chat.SessionMetadata{Context: ctx}}
}

// TestExtractSessionContextNilSession：nil 会话返回零值（不 panic）。
func TestExtractSessionContextNilSession(t *testing.T) {
	if got := ExtractSessionContext(nil); got != (SessionContext{}) {
		t.Fatalf("nil session must yield zero value, got %+v", got)
	}
}

// TestExtractSessionContextFull：逐字段核验抽取语义（与抽取前内联块一致）。
func TestExtractSessionContextFull(t *testing.T) {
	ctx := map[string]interface{}{
		sessionmeta.LegacyAPIProfileReference:               "/profiles/coding",
		sessionmeta.ProfileAgent:                            "agent-1",
		sessionmeta.WorkspacePath:                           "/workspace",
		agentconfig.SessionRoutingOverrideContextKey:        `{"provider":"p"}`,
		toolbroker.AgentSessionContextAgentType:             "child",
		toolbroker.AgentSessionContextCompletionRequirement: "complete_task",
		sessionmeta.ProviderName:                            "openai",
		toolbroker.AgentSessionContextRequestedModel:        "gpt-x",
		sessionmeta.ReasoningEffort:                         "high",
		sessionmeta.Stream:                                  true,
		sessionmeta.DisableTools:                            true,
		toolbroker.AgentSessionContextReadOnly:              true,
		toolbroker.AgentSessionContextDepth:                 2,
		sessionmeta.ActiveTeamTaskID:                        "task-9",
	}
	got := ExtractSessionContext(sessionWithContext(ctx))
	want := SessionContext{
		WorkspacePath:              "/workspace",
		ProfileRef:                 "/profiles/coding",
		AgentID:                    "agent-1",
		RoutingOverrideRaw:         `{"provider":"p"}`,
		ChildAgentType:             "child",
		ChildCompletionRequirement: "complete_task",
		ChildReadOnly:              true,
		ChildDepth:                 2,
		RequestedProvider:          "openai",
		RequestedModel:             "gpt-x",
		RequestedReasoningEffort:   "high",
		StreamRequested:            true,
		DisableTools:               true,
		ObservationTaskID:          "task-9",
	}
	if got != want {
		t.Fatalf("ExtractSessionContext mismatch:\n got=%+v\nwant=%+v", got, want)
	}
}

// TestExtractSessionContextWorktreeOverridesWorkspace：worktree 路径覆盖 workspace。
func TestExtractSessionContextWorktreeOverridesWorkspace(t *testing.T) {
	ctx := map[string]interface{}{
		sessionmeta.WorkspacePath:                  "/workspace",
		toolbroker.AgentSessionContextWorktreePath: "/worktree",
	}
	got := ExtractSessionContext(sessionWithContext(ctx))
	if got.WorkspacePath != "/worktree" {
		t.Fatalf("worktree must override workspace, got %q", got.WorkspacePath)
	}
}

// TestExtractSessionContextModelFallback：请求模型为空回落 sessionmeta.Model；非空则不回落。
func TestExtractSessionContextModelFallback(t *testing.T) {
	fallback := ExtractSessionContext(sessionWithContext(map[string]interface{}{
		sessionmeta.Model: "fallback-model",
	}))
	if fallback.RequestedModel != "fallback-model" {
		t.Fatalf("model fallback failed, got %q", fallback.RequestedModel)
	}
	requested := ExtractSessionContext(sessionWithContext(map[string]interface{}{
		toolbroker.AgentSessionContextRequestedModel: "requested",
		sessionmeta.Model: "fallback-model",
	}))
	if requested.RequestedModel != "requested" {
		t.Fatalf("requested model must win over fallback, got %q", requested.RequestedModel)
	}
}

// TestExtractSessionContextAbsentFlagsStayZero：缺失的布尔/整数元数据不得覆盖零值。
func TestExtractSessionContextAbsentFlagsStayZero(t *testing.T) {
	got := ExtractSessionContext(sessionWithContext(map[string]interface{}{}))
	if got.StreamRequested || got.DisableTools || got.ChildReadOnly || got.ChildDepth != 0 {
		t.Fatalf("absent flags must stay zero, got %+v", got)
	}
	if got != (SessionContext{}) {
		t.Fatalf("empty metadata must yield zero value, got %+v", got)
	}
}
