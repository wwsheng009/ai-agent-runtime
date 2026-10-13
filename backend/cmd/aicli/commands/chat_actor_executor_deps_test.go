package commands

import (
	"context"
	"testing"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/runtimehost"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

// TestDeriveLocalSessionContext 钉住 E3.4a-1 抽取出的派生语义（与抽取前内联块一致）：
// 子会话读全量；基础会话只读 agent_type/completion requirement；
// 存储不可用/会话缺失 → 零值且 workspace 不变。
func TestDeriveLocalSessionContext(t *testing.T) {
	ctx := context.Background()
	store := runtimechat.NewInMemoryStorage()
	session := runtimechat.NewSession("derive-user")
	session.ID = "child-1"
	session.SetContext(toolbroker.AgentSessionContextAgentType, "explore")
	session.SetContext(toolbroker.AgentSessionContextCompletionRequirement, "complete_task")
	session.SetContext(sessionmeta.WorkspacePath, "/ws")
	session.SetContext(sessionmeta.ProviderName, "openai")
	session.SetContext(sessionmeta.Model, "m1")
	session.SetContext(sessionmeta.ReasoningEffort, "high")
	session.SetContext(toolbroker.AgentSessionContextReadOnly, true)
	session.SetContext(toolbroker.AgentSessionContextDepth, 2)
	if err := store.Save(ctx, session); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// 子会话：读全量，并按 workspace 覆盖。
	child, childWS := deriveLocalSessionContext(store, "child-1", false, "/base")
	if child.ChildAgentType != "explore" || child.ChildCompletionRequirement != "complete_task" {
		t.Fatalf("agent_type/completion mismatch: %+v", child)
	}
	if childWS != "/ws" {
		t.Fatalf("child workspace must be overridden by session workspace, got %q", childWS)
	}
	if child.RequestedProvider != "openai" || child.RequestedModel != "m1" || child.RequestedReasoningEffort != "high" {
		t.Fatalf("provider/model/effort mismatch: %+v", child)
	}
	if !child.ChildReadOnly || child.ChildDepth != 2 {
		t.Fatalf("read_only/depth mismatch: %+v", child)
	}

	// 基础会话：agent_type/completion 仍读；provider/model/workspace 覆盖不读。
	base, baseWS := deriveLocalSessionContext(store, "child-1", true, "/base")
	if base.ChildAgentType != "explore" || base.ChildCompletionRequirement != "complete_task" {
		t.Fatalf("base session must still read agent_type/completion: %+v", base)
	}
	if baseWS != "/base" {
		t.Fatalf("base session must not override workspace, got %q", baseWS)
	}
	if base.RequestedModel != "" || base.RequestedProvider != "" || base.ChildReadOnly || base.ChildDepth != 0 {
		t.Fatalf("base session must not read child-only fields: %+v", base)
	}

	// 存储为 nil / 会话不存在 → 零值 + workspace 不变。
	zero, zeroWS := deriveLocalSessionContext(nil, "child-1", false, "/base")
	if zero != (runtimehost.SessionContext{}) || zeroWS != "/base" {
		t.Fatalf("nil store must yield zero context and keep workspace: %+v ws=%q", zero, zeroWS)
	}
	missing, missingWS := deriveLocalSessionContext(store, "no-such-session", false, "/base")
	if missing != (runtimehost.SessionContext{}) || missingWS != "/base" {
		t.Fatalf("missing session must yield zero context and keep workspace: %+v ws=%q", missing, missingWS)
	}
}
