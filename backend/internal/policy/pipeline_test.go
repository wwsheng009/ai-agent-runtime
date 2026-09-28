package policy

import (
	"context"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	runtimehooks "github.com/wwsheng009/ai-agent-runtime/internal/hooks"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
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

func TestIsShellReadOnlyCommandRejectsMutationFlagsAndOutputFiles(t *testing.T) {
	for _, command := range []string{
		"git branch -D feature",
		"git branch -m old new",
		"git tag v1.0.0",
		"git tag -d v1.0.0",
		"git diff --output=changes.patch",
		"git log --output=history.txt",
		"git remote set-head origin -a",
		"go env -w GOPROXY=https://example.invalid",
		"go env -u GOPROXY",
		"go list -mod=mod ./...",
		"git status & Remove-Item file.txt",
		"git diff $env:GIT_ARG",
		"git diff %GIT_ARG%",
		"git diff !GIT_ARG!",
		`rg --pre "pwsh -Command Remove-Item marker.txt" pattern .`,
		"rg --hostname-bin=malicious-helper pattern .",
		"rg -z pattern archive.gz",
		"file --compile -m custom.magic",
		"python help",
		"node help",
		"yarn version",
	} {
		assert.False(t, IsShellReadOnlyCommand(command), command)
	}
}

func TestIsShellReadOnlyCommandAllowsExplicitQueryForms(t *testing.T) {
	for _, command := range []string{
		"git branch --list feature",
		"git branch --show-current",
		"git tag --list v*",
		"git tag --verify v1.0.0",
		"git remote show origin",
		"git remote get-url origin",
		"git log --format=%H -5",
		"go env GOPROXY",
		"python --version",
		"node -v",
		"npm --help",
		"cargo -V",
	} {
		assert.True(t, IsShellReadOnlyCommand(command), command)
	}
}

func TestAssessShellReadOnlyCommandAllowsReadOnlyCompoundSegments(t *testing.T) {
	assessment := AssessShellReadOnlyCommand("git status; git diff")
	assert.True(t, assessment.Allowed, "every segment is on the read-only table")

	assessment = AssessShellReadOnlyCommand("cat a | head -5 || echo none")
	assert.True(t, assessment.Allowed, "read-only pipeline with fallback stays on the fast path")

	assessment = AssessShellReadOnlyCommand("git status; rm -rf build")
	assert.False(t, assessment.Allowed)
	assert.Equal(t, ShellReadOnlyReasonNotAllowed, assessment.Reason)

	assessment = AssessShellReadOnlyCommand("git status")
	assert.True(t, assessment.Allowed)
	assert.Empty(t, assessment.Reason)
}

func TestAssessShellReadOnlyCommandRejectsUnparsableAndWrappedSegments(t *testing.T) {
	assessment := AssessShellReadOnlyCommand("cat 'unterminated")
	assert.False(t, assessment.Allowed)
	assert.Equal(t, ShellReadOnlyReasonUnparsable, assessment.Reason)

	// Wrapper piercing is allowed for resolvable commands...
	assessment = AssessShellReadOnlyCommand("timeout 5 cat README.md")
	assert.True(t, assessment.Allowed)

	// ...but shell-interpreter payloads stay out of the fast path.
	assessment = AssessShellReadOnlyCommand(`bash -c "cat README.md"`)
	assert.False(t, assessment.Allowed)
	assert.Equal(t, ShellReadOnlyReasonNotAllowed, assessment.Reason)
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
	// §4.8：remember 现在带派生的窄模式（这里是路径），不再退化为工具级授权。
	grants := store.List()
	require.Len(t, grants, 1)
	assert.Equal(t, GrantPatternPath+"a.go", grants[0].Pattern)
	assert.Equal(t, RememberScopeSession, grants[0].Scope)
	_, ok := store.Find("write", map[string]interface{}{"file_path": "a.go"})
	assert.True(t, ok)
	_, ok = store.Find("write", map[string]interface{}{"file_path": "b.go"})
	assert.False(t, ok, "a remembered path must not authorize other paths")

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

// TestAssessShellReadOnlyCommandAllowsStaticPipelineCmdlets 锁定 P1-5：常见只读
// 流水后段 cmdlet 的固定字面量形态进入只读快路径（此前 `... | Select-Object
// -First 5` 会被整条拒绝，造成只读场景误伤）。
func TestAssessShellReadOnlyCommandAllowsStaticPipelineCmdlets(t *testing.T) {
	allowed := []string{
		"Get-ChildItem -LiteralPath . | Select-Object -First 5",
		"Get-ChildItem . | Select-Object -First:10 -Skip 2",
		"Get-Content app.log | Measure-Object -Line",
		"Get-ChildItem . | Sort-Object -Property Length -Descending",
		"Get-ChildItem . | Sort-Object Name",
		"Get-ChildItem . | Where-Object -Property Length -GT 100",
		"Get-ChildItem . | Where-Object Length -gt 1kb",
		"rg -n TODO . | Measure-Object",
		"git -C . status --short",
		"git --no-pager log --oneline -5",
	}
	for _, command := range allowed {
		assessment := AssessShellReadOnlyCommand(command)
		assert.Truef(t, assessment.Allowed,
			"expected read-only fast path for %q (reason=%s segment=%d)", command, assessment.Reason, assessment.SegmentIndex)
	}
}

// TestAssessShellReadOnlyCommandRejectsNonLiteralPipelineCmdlets 锁定 P1-5 的安全
// 侧：脚本块、计算属性、变量、类型转换、额外 token 与位置参数列表一律拒绝；
// 「动态语法」与「未支持静态查询」保持不同稳定原因码。
func TestAssessShellReadOnlyCommandRejectsNonLiteralPipelineCmdlets(t *testing.T) {
	cases := []struct {
		command string
		reason  string
	}{
		{"Get-ChildItem . | Select-Object -Property Name,Length", ShellReadOnlyReasonNotAllowed},
		{"Get-ChildItem . | Select-Object -First 5 -Last 3", ShellReadOnlyReasonNotAllowed},
		{"Get-ChildItem . | Select-Object -First 5s", ShellReadOnlyReasonNotAllowed},
		{"Get-ChildItem . | Select-Object -First -5", ShellReadOnlyReasonNotAllowed},
		{"Get-ChildItem . | Where-Object { Length -gt 100 }", ShellReadOnlyReasonNotAllowed},
		{"Get-ChildItem . | Where-Object -Property Length -GT 100 -And Name -eq \"x\"", ShellReadOnlyReasonNotAllowed},
		{"Get-ChildItem . | Where-Object Length -gt (Get-Random)", ShellReadOnlyReasonNotAllowed},
		{"Get-ChildItem . | Sort-Object -Property @{Expression={$_.Length}}", ShellReadOnlyReasonDynamicSyntax},
		{"Get-ChildItem . | Select-Object -First $n", ShellReadOnlyReasonDynamicSyntax},
		{"git -c core.pager=cat log", ShellReadOnlyReasonNotAllowed},
		{"git -c core.hooksPath=/tmp/hooks status", ShellReadOnlyReasonNotAllowed},
		{"git --config-env=core.pager=PAGER log", ShellReadOnlyReasonNotAllowed},
	}
	for _, tc := range cases {
		assessment := AssessShellReadOnlyCommand(tc.command)
		assert.Falsef(t, assessment.Allowed, "expected rejection for %q", tc.command)
		assert.Equalf(t, tc.reason, assessment.Reason, "stable reason for %q", tc.command)
	}
}

// TestAssessShellReadOnlyCommandReportsFailingSegment 锁定 P1-5：拒绝时带出失败段
// 序号与内容；命令级（未分段）拒绝保持无段信息，避免误导。
func TestAssessShellReadOnlyCommandReportsFailingSegment(t *testing.T) {
	assessment := AssessShellReadOnlyCommand("Get-ChildItem . | Select-Object -First 5 | Remove-Item .")
	assert.False(t, assessment.Allowed)
	assert.Equal(t, ShellReadOnlyReasonNotAllowed, assessment.Reason)
	assert.Equal(t, 3, assessment.SegmentIndex)
	assert.Equal(t, "Remove-Item .", assessment.Segment)

	dynamic := AssessShellReadOnlyCommand("Get-ChildItem . | Select-Object -First $n")
	assert.Equal(t, ShellReadOnlyReasonDynamicSyntax, dynamic.Reason)
	assert.Equal(t, 0, dynamic.SegmentIndex)
	assert.Empty(t, dynamic.Segment)
}

// TestReadOnlyPolicyDenialReportsFailingSegment 锁定 P1-5：策略层拒绝信息包含
// 失败段位置，便于模型直接定位而不是整条命令盲重试。
func TestReadOnlyPolicyDenialReportsFailingSegment(t *testing.T) {
	policy := NewToolExecutionPolicy(nil, true)
	err := policy.AllowToolCallWithContext(context.Background(),
		skill.ToolInfo{Name: "shell", MCPTrustLevel: "local", ExecutionMode: "local_mcp"},
		map[string]interface{}{"command": "Get-ChildItem . | Select-Object -First 5 | Remove-Item ."})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-readonly shell command")
	assert.Contains(t, err.Error(), "segment 3")
	assert.Contains(t, err.Error(), "Remove-Item")
}

// TestReadOnlyPolicyAllowsNullDeviceRedirection 锁定只读执行边界对空设备重定向
// 的豁免：`cmd 2>/dev/null`（Windows `2>NUL`）不再整条拒绝；重定向到真实目标
// 仍然返回同一稳定原因码。
func TestReadOnlyPolicyAllowsNullDeviceRedirection(t *testing.T) {
	policy := NewToolExecutionPolicy(nil, true)
	command := `grep -rn "race" backend/Makefile Makefile 2>/dev/null | head -20`
	if runtime.GOOS == "windows" {
		command = `grep -rn "race" backend/Makefile Makefile 2>NUL | head -20`
	}
	err := policy.AllowToolCallWithContext(context.Background(),
		skill.ToolInfo{Name: "shell", MCPTrustLevel: "local", ExecutionMode: "local_mcp"},
		map[string]interface{}{"command": command})
	require.NoError(t, err)

	err = policy.AllowToolCallWithContext(context.Background(),
		skill.ToolInfo{Name: "shell", MCPTrustLevel: "local", ExecutionMode: "local_mcp"},
		map[string]interface{}{"command": `echo x > real.txt`})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "redirection or dynamic command syntax")
}
