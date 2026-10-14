package commands

import (
	"context"
	"testing"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
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

// 编译期契约（E3.4a-2）：`aicliRuntimeDeps` 的签名必须与设计一致。
//
// 运行时契约（Deps 各回调非空、LeaseMode=actor-scoped）**不在此处断言**：
// `aicliRuntimeDeps` 会走 `buildLocalChatAgent` → `host.Bootstrap.LLMRuntime()`，
// 单测环境没有 bootstrap/LLM runtime（会 nil deref）。该断言在 **E3.4b** 切换后
// 由真实调用路径 + 契约测试覆盖（见设计 §6）。
var _ func(*localChatRuntimeHost, *ChatSession, runtimechat.SessionStorage, *runtimecfg.RuntimeConfig, string, string) runtimehost.Deps = (*localChatRuntimeHost).aicliRuntimeDeps

// TestAicliExecutorReuseEnabled 验证灰度开关语义（第 3 步：**默认开启**）：
// 未设置/空串/任意未识别值 → 启用；显式回退值（0/false/off/no）→ 旧路径。
// 这样整套既有 aicli 测试都会经统一执行核运行（大规模验证），回滚只需设 env。
func TestAicliExecutorReuseEnabled(t *testing.T) {
	cases := []struct {
		value string
		want  bool
	}{
		{"", true},
		{"1", true},
		{"true", true},
		{"TRUE", true},
		{"on", true},
		{"yes", true},
		{" yes ", true},
		{"anything-else", true},
		{"0", false},
		{"false", false},
		{"FALSE", false},
		{"off", false},
		{"no", false},
		{" off ", false},
	}
	for _, tc := range cases {
		t.Setenv("AICLI_EXECUTOR_REUSE", tc.value)
		if got := aicliExecutorReuseEnabled(); got != tc.want {
			t.Fatalf("AICLI_EXECUTOR_REUSE=%q: got %v want %v", tc.value, got, tc.want)
		}
	}
}
