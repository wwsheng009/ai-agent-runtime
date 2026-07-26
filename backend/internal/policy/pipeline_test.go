package policy

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	runtimehooks "github.com/wwsheng009/ai-agent-runtime/internal/hooks"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestDefaultCapabilityResolverPrefersTaxonomyMetadata(t *testing.T) {
	resolver := DefaultCapabilityResolver{}
	// Heuristic would treat "custom_tool" as read_only, but metadata says edit/mutates.
	caps := resolver.Resolve(EvalRequest{
		ToolName: "custom_tool",
		Metadata: map[string]interface{}{
			types.ToolMetadataKindKey:      types.ToolKindEdit,
			types.ToolMetadataReadOnlyKey:  false,
			types.ToolMetadataMutatesFSKey: true,
		},
	})
	assert.Equal(t, []Capability{CapWriteFS}, caps)
}

func TestDefaultCapabilityResolverUsesKnownTaxonomyTable(t *testing.T) {
	resolver := DefaultCapabilityResolver{}
	assert.Equal(t, []Capability{CapReadOnly}, resolver.Resolve(EvalRequest{ToolName: "view"}))
	assert.Equal(t, []Capability{CapWriteFS}, resolver.Resolve(EvalRequest{ToolName: "write"}))
	assert.Equal(t, []Capability{CapExecShell}, resolver.Resolve(EvalRequest{ToolName: "shell"}))
	assert.Contains(t, resolver.Resolve(EvalRequest{ToolName: "fetch"}), CapNetwork)
	assert.Contains(t, resolver.Resolve(EvalRequest{ToolName: "fetch"}), CapReadOnly)
}

func TestIsShellReadOnlyCommand(t *testing.T) {
	assert.True(t, IsShellReadOnlyCommand("git status"))
	assert.True(t, IsShellReadOnlyCommand("rg pattern backend"))
	assert.True(t, IsShellReadOnlyCommand("ls"))
	assert.True(t, IsShellReadOnlyCommand("pwd"))
	assert.True(t, IsShellReadOnlyCommand("git log --oneline -5"))
	assert.False(t, IsShellReadOnlyCommand("git commit -m x"))
	assert.False(t, IsShellReadOnlyCommand("git status && rm -rf /"))
	assert.False(t, IsShellReadOnlyCommand("echo hi | tee file"))
	assert.False(t, IsShellReadOnlyCommand("git stash push"))
	assert.True(t, IsShellReadOnlyCommand("git stash list"))
}

func TestMemoryGrantStoreRejectsDangerousTools(t *testing.T) {
	store := &MemoryGrantStore{}
	err := store.Remember(Grant{Tool: "shell", Scope: "session"})
	require.Error(t, err)
	_, ok := store.Find("shell", nil)
	assert.False(t, ok)

	require.NoError(t, store.Remember(Grant{Tool: "write", Scope: "session"}))
	grant, ok := store.Find("write", map[string]interface{}{"file_path": "a.txt"})
	assert.True(t, ok)
	assert.Equal(t, "write", grant.Tool)
}

func TestEngineShellReadOnlyAutoAllow(t *testing.T) {
	engine := &Engine{Mode: ModeDefault}
	decision, err := engine.Evaluate(context.Background(), EvalRequest{
		ToolName: "shell",
		Args:     map[string]interface{}{"command": "git status"},
	})
	require.NoError(t, err)
	assert.Equal(t, DecisionAllow, decision.Type)
	assert.Equal(t, StageReadonlyAuto, decision.Stage)
	assert.Contains(t, decision.Reason, "shell_readonly")

	// Non-readonly shell falls through to mode ask under default; without AskHandler → headless deny.
	decision, err = engine.Evaluate(context.Background(), EvalRequest{
		ToolName: "shell",
		Args:     map[string]interface{}{"command": "git commit -m x"},
	})
	require.NoError(t, err)
	assert.Equal(t, DecisionDeny, decision.Type)
	assert.Equal(t, StageHeadlessDeny, decision.Stage)
	assert.Contains(t, decision.Reason, "approval_required")
}

func TestEngineTaxonomyReadOnlyAutoAllow(t *testing.T) {
	engine := &Engine{Mode: ModeDefault}
	decision, err := engine.Evaluate(context.Background(), EvalRequest{ToolName: "view"})
	require.NoError(t, err)
	assert.Equal(t, DecisionAllow, decision.Type)
	assert.Equal(t, StageReadonlyAuto, decision.Stage)
}

func TestEngineRememberedGrant(t *testing.T) {
	store := &MemoryGrantStore{}
	require.NoError(t, store.Remember(Grant{Tool: "write", Scope: "session"}))
	engine := &Engine{Mode: ModeDefault, Grants: store}
	decision, err := engine.Evaluate(context.Background(), EvalRequest{
		ToolName: "write",
		Args:     map[string]interface{}{"file_path": "x.go", "content": "hi"},
	})
	require.NoError(t, err)
	assert.Equal(t, DecisionAllow, decision.Type)
	assert.Equal(t, StageGrants, decision.Stage)
}

func TestEngineBypassStillHonorsHookDeny(t *testing.T) {
	engine := &Engine{
		Mode: ModeBypassPermissions,
		Hooks: staticHookDispatcher{
			decision: runtimehooks.Decision{
				Action:  runtimehooks.DecisionBlock,
				Message: "blocked_by_hook",
			},
		},
	}
	decision, err := engine.Evaluate(context.Background(), EvalRequest{ToolName: "view"})
	require.NoError(t, err)
	assert.Equal(t, DecisionDeny, decision.Type)
	assert.Equal(t, StageHooks, decision.Stage)
	assert.Contains(t, decision.Reason, "blocked_by_hook")
}

func TestEngineBypassAllowsWithoutAsk(t *testing.T) {
	engine := &Engine{Mode: ModeBypassPermissions}
	decision, err := engine.Evaluate(context.Background(), EvalRequest{
		ToolName: "write",
		Args:     map[string]interface{}{"file_path": "a.go", "content": "x"},
	})
	require.NoError(t, err)
	assert.Equal(t, DecisionAllow, decision.Type)
	assert.Equal(t, StageMode, decision.Stage)
}

func TestEngineHardDenyRuleUnderBypass(t *testing.T) {
	engine := &Engine{
		Mode: ModeBypassPermissions,
		Rules: []Rule{{
			Tools:    []string{"write"},
			Decision: DecisionDeny,
			Reason:   "hard_deny_write",
		}},
	}
	decision, err := engine.Evaluate(context.Background(), EvalRequest{ToolName: "write"})
	require.NoError(t, err)
	assert.Equal(t, DecisionDeny, decision.Type)
	assert.Equal(t, StageRules, decision.Stage)
}

func TestEngineDecisionStageSetOnPolicyDeny(t *testing.T) {
	engine := &Engine{
		Mode:   ModeBypassPermissions,
		Policy: NewToolExecutionPolicy([]string{"view"}, false),
	}
	decision, err := engine.Evaluate(context.Background(), EvalRequest{ToolName: "write"})
	require.NoError(t, err)
	assert.Equal(t, DecisionDeny, decision.Type)
	assert.Equal(t, StagePolicy, decision.Stage)
}

type rememberApprovalHandler struct {
	remember bool
}

func (h rememberApprovalHandler) RequestApproval(_ context.Context, _ ApprovalRequest) (ApprovalResponse, error) {
	return ApprovalResponse{Allowed: true, Remember: h.remember}, nil
}

func TestEngineApprovalRememberStoresGrantButNotDangerous(t *testing.T) {
	store := &MemoryGrantStore{}
	engine := &Engine{
		Mode:       ModeDefault,
		Grants:     store,
		AskHandler: rememberApprovalHandler{remember: true},
	}
	decision, err := engine.Evaluate(context.Background(), EvalRequest{
		ToolName: "write",
		Args:     map[string]interface{}{"file_path": "a.go", "content": "x"},
	})
	require.NoError(t, err)
	assert.Equal(t, DecisionAllow, decision.Type)
	_, ok := store.Find("write", nil)
	assert.True(t, ok)

	// Dangerous tools never remembered even if Remember=true.
	store2 := &MemoryGrantStore{}
	engine2 := &Engine{
		Mode:       ModeDefault,
		Grants:     store2,
		AskHandler: rememberApprovalHandler{remember: true},
	}
	decision, err = engine2.Evaluate(context.Background(), EvalRequest{
		ToolName: "shell",
		Args:     map[string]interface{}{"command": "git commit -m x"},
	})
	require.NoError(t, err)
	assert.Equal(t, DecisionAllow, decision.Type)
	_, ok = store2.Find("shell", nil)
	assert.False(t, ok)
}
