package policy

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/executor"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolargs"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolctx"
)

func TestToolExecutionPolicy_AllowToolInfo_BlocksRemoteWrite(t *testing.T) {
	policy := NewToolExecutionPolicy(nil, false)
	err := policy.AllowToolInfo(skill.ToolInfo{
		Name:          "write_file",
		MCPTrustLevel: "untrusted_remote",
		ExecutionMode: "remote_mcp",
	})
	if err == nil {
		t.Fatal("expected remote write-like tool to be blocked")
	}
}

func TestToolExecutionPolicy_AllowToolInfo_AllowsLocalRead(t *testing.T) {
	policy := NewToolExecutionPolicy(nil, true)
	err := policy.AllowToolInfo(skill.ToolInfo{
		Name:          "read_file",
		MCPTrustLevel: "local",
		ExecutionMode: "local_mcp",
	})
	if err != nil {
		t.Fatalf("expected local read tool to be allowed, got %v", err)
	}
}

func TestToolExecutionPolicy_AllowTool_KeepsShellVisibleInReadOnlyMode(t *testing.T) {
	policy := NewToolExecutionPolicy(nil, true)
	// Shell tools remain definition-visible under read-only; mutation risk is
	// enforced per-command in AllowToolCall.
	for _, name := range []string{"shell", "bash", "execute_shell_command", "shell_command", "aicli_exec"} {
		if err := policy.AllowTool(name); err != nil {
			t.Fatalf("expected read-only policy to keep shell surface visible for %q, got %v", name, err)
		}
	}
	for _, name := range []string{"background_task"} {
		if err := policy.AllowTool(name); err == nil {
			t.Fatalf("expected read-only policy to block %q", name)
		}
	}
}

func TestIsShellLikeToolName(t *testing.T) {
	for _, name := range []string{"shell", "bash", "execute_shell_command", "ShellTool", "run_shell"} {
		if !IsShellLikeToolName(name) {
			t.Fatalf("expected %q to be shell-like", name)
		}
	}
	for _, name := range []string{"", "view", "grep", "write", "apply_patch"} {
		if IsShellLikeToolName(name) {
			t.Fatalf("expected %q not to be shell-like", name)
		}
	}
}

func TestToolExecutionPolicy_AllowTool_BlocksDenylistedTool(t *testing.T) {
	policy := NewToolExecutionPolicy(nil, false)
	policy.DeniedTools = map[string]bool{"read_file": true}
	if err := policy.AllowTool("read_file"); err == nil {
		t.Fatal("expected denylisted tool to be blocked")
	}
}

func TestToolExecutionPolicy_AllowTool_AllowsRuntimeSearchOutsideAllowlist(t *testing.T) {
	policy := NewToolExecutionPolicy([]string{"view", "grep"}, true)
	if err := policy.AllowTool("search_tool"); err != nil {
		t.Fatalf("expected runtime search meta-tool to remain executable, got %v", err)
	}
	policy.DeniedTools = map[string]bool{"search_tool": true}
	for _, name := range []string{"search_tool", "Search_Tool"} {
		if err := policy.AllowTool(name); err == nil {
			t.Fatalf("expected explicit deny to override runtime search allowance for %q", name)
		}
	}
	policy.DeniedTools = map[string]bool{"Search_Tool": true}
	if err := policy.AllowTool("search_tool"); err == nil {
		t.Fatal("expected normalized explicit deny to block runtime search")
	}
}

func TestToolExecutionPolicy_AllowTool_AllowsRuntimeOwnedEssentialsOutsideAllowlist(t *testing.T) {
	policy := NewToolExecutionPolicy([]string{"view", "grep"}, false)
	essentials := []string{
		"enter_plan_mode", "exit_plan_mode", "plan_review", "ask_user_question",
		"todos", "get_goal", "update_goal",
		"spawn_agent", "list_agents", "wait_agent", "resolve_agent_approval",
		"task_output", "search_tool",
	}
	for _, name := range essentials {
		if err := policy.AllowTool(name); err != nil {
			t.Fatalf("expected runtime-owned essential %q to remain executable under narrow allowlist, got %v", name, err)
		}
		if !IsRuntimeOwnedEssentialTool(name) {
			t.Fatalf("expected %q to be classified as runtime-owned essential", name)
		}
	}
	// Regular toolkit tools still need to be on the allowlist.
	if err := policy.AllowTool("write"); err == nil {
		t.Fatal("expected non-essential write tool to remain allowlist-gated")
	}
	if err := policy.AllowTool("shell"); err == nil {
		t.Fatal("expected non-essential shell tool to remain allowlist-gated")
	}
	// Explicit deny still wins over essential bypass.
	policy.DeniedTools = map[string]bool{"Enter_Plan_Mode": true}
	if err := policy.AllowTool("enter_plan_mode"); err == nil {
		t.Fatal("expected normalized explicit deny to block enter_plan_mode")
	}
}

func TestToolExecutionPolicy_AllowTool_EmptyAllowlistBlocksEssentials(t *testing.T) {
	// DisableTools synthesizes an enabled empty allowlist; essentials must not
	// leak through that total-disable surface.
	policy := NewToolExecutionPolicy([]string{}, false)
	for _, name := range []string{"search_tool", "get_goal", "update_goal", "spawn_subagents", "todos"} {
		if err := policy.AllowTool(name); err == nil {
			t.Fatalf("expected empty allowlist to block essential %q", name)
		}
		if policy.AllowsDefinition(name) {
			t.Fatalf("expected empty allowlist to hide definition for %q", name)
		}
	}
}

func TestToolExecutionPolicy_AllowTool_EssentialStillHonorsCapabilityScope(t *testing.T) {
	// enter_plan_mode requires CapReadOnly + CapAskUser; network alone is insufficient.
	policy := NewCapabilityScopedToolExecutionPolicy([]string{"view"}, []Capability{CapReadOnly, CapAskUser, CapNetwork})
	if err := policy.AllowTool("enter_plan_mode"); err != nil {
		t.Fatalf("expected enter_plan_mode under read_only+ask_user+network scope, got %v", err)
	}
	// Without CapAskUser, enter_plan_mode is still capability-gated (allowlist bypass is not enough).
	narrow := NewCapabilityScopedToolExecutionPolicy([]string{"view"}, []Capability{CapReadOnly, CapNetwork})
	if err := narrow.AllowTool("enter_plan_mode"); err == nil {
		t.Fatal("expected enter_plan_mode to remain capability-gated without CapAskUser")
	}
	// Agent management still requires CapAgentManagement.
	if err := policy.AllowTool("spawn_agent"); err == nil {
		t.Fatal("expected spawn_agent to remain capability-gated without CapAgentManagement")
	}
}

func TestToolExecutionPolicy_AllowToolCall_BlocksPathOutsideSandbox(t *testing.T) {
	root := t.TempDir()
	policy := NewToolExecutionPolicy(nil, false)
	policy.Sandbox = executor.NewSandbox(&executor.SandboxConfig{
		Enabled:      true,
		AllowedPaths: []string{root},
	})

	err := policy.AllowToolCall(skill.ToolInfo{
		Name:          "read_file",
		MCPTrustLevel: "local",
		ExecutionMode: "local_mcp",
	}, map[string]interface{}{
		"path": filepath.Join(root, "..", "outside.txt"),
	})
	if err == nil {
		t.Fatal("expected path outside sandbox to be blocked")
	}
}

func TestToolExecutionPolicy_AllowToolCall_BlocksDeniedCommand(t *testing.T) {
	policy := NewToolExecutionPolicy(nil, false)
	policy.Sandbox = executor.NewSandbox(&executor.SandboxConfig{
		Enabled:         true,
		AllowedCommands: []string{"git"},
		DeniedCommands:  []string{"powershell"},
	})

	err := policy.AllowToolCall(skill.ToolInfo{
		Name:          "run_command_readonly",
		MCPTrustLevel: "local",
		ExecutionMode: "local_mcp",
	}, map[string]interface{}{
		"cmd": "powershell",
	})
	if err == nil {
		t.Fatal("expected denied command to be blocked")
	}
}

func TestToolExecutionPolicy_AllowToolCall_BlocksNestedPathOutsideSandbox(t *testing.T) {
	root := t.TempDir()
	policy := NewToolExecutionPolicy(nil, false)
	policy.Sandbox = executor.NewSandbox(&executor.SandboxConfig{
		Enabled:      true,
		AllowedPaths: []string{root},
	})

	err := policy.AllowToolCall(skill.ToolInfo{
		Name:          "read_file",
		MCPTrustLevel: "local",
		ExecutionMode: "local_mcp",
	}, map[string]interface{}{
		"options": map[string]interface{}{
			"target": filepath.Join(root, "..", "outside.txt"),
		},
	})
	if err == nil {
		t.Fatal("expected nested path outside sandbox to be blocked")
	}
}

func TestToolExecutionPolicy_AllowToolCall_BlocksNestedShellCommandInReadOnlyMode(t *testing.T) {
	policy := NewToolExecutionPolicy(nil, true)

	err := policy.AllowToolCall(skill.ToolInfo{
		Name:          "run_command_readonly",
		MCPTrustLevel: "local",
		ExecutionMode: "local_mcp",
	}, map[string]interface{}{
		"options": map[string]interface{}{
			"command": "bash",
		},
	})
	if err == nil {
		t.Fatal("expected shell-like command to be blocked in read-only mode")
	}
}

func TestToolExecutionPolicy_AllowToolCall_ReadOnlyBatchValidatesEveryEntry(t *testing.T) {
	policy := NewToolExecutionPolicy(nil, true)
	shellInfo := skill.ToolInfo{
		Name:          "shell",
		MCPTrustLevel: "local",
		ExecutionMode: "local_mcp",
	}

	// Plain-string batch entries must be classified individually: a mutating
	// entry smuggled next to a read-only object entry is a bypass.
	err := policy.AllowToolCall(shellInfo, map[string]interface{}{
		"commands": []interface{}{
			map[string]interface{}{"command": "git status"},
			"rm -rf file.txt",
		},
	})
	if err == nil {
		t.Fatal("expected plain-string mutating batch entry to be blocked in read-only mode")
	}

	// All-plain-string batch with only read-only commands must pass.
	err = policy.AllowToolCall(shellInfo, map[string]interface{}{
		"commands": []string{"git status", "git diff"},
	})
	if err != nil {
		t.Fatalf("expected read-only plain-string batch to be allowed, got %v", err)
	}

	// Object-form batch with a mutating entry must be blocked too.
	err = policy.AllowToolCall(shellInfo, map[string]interface{}{
		"commands": []interface{}{
			map[string]interface{}{"command": "git status"},
			map[string]interface{}{"cmd": "git commit -m x"},
		},
	})
	if err == nil {
		t.Fatal("expected mutating object-form batch entry to be blocked in read-only mode")
	}
}

func TestCapabilityScopedPolicyExposesOnlyDeclaredCapabilitySurface(t *testing.T) {
	policy := NewCapabilityScopedToolExecutionPolicy(nil, []Capability{CapReadOnly, CapNetwork})
	assert.NoError(t, policy.AllowTool("read_file"))
	assert.NoError(t, policy.AllowTool("web_search"))
	assert.Error(t, policy.AllowTool("write_file"))
	assert.Error(t, policy.AllowTool("spawn_agent"))
	assert.Equal(t, []string{"network", "read_only"}, policy.AllowedCapabilityNames())
}

// TestToolExecutionPolicy_PlanWriteExemptionUnderNarrowAllowlist pins the §4.7
// narrow-allowlist exemption: with plan mode active, write/apply_patch may run
// for plan files even when the product allowlist only listed read tools, while
// explicit deny, read-only, capability scope, and the sandbox boundary keep
// their precedence.
//
// Patch texts are assembled with the helpers declared in engine_test.go so no
// literal patch marker appears in this source file.
func TestToolExecutionPolicy_PlanWriteExemptionUnderNarrowAllowlist(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	ctx := toolctx.WithWorkspaceRoot(context.Background(), workspace)
	writeInfo := skill.ToolInfo{Name: "write", MCPTrustLevel: "local", ExecutionMode: "local_mcp"}
	patchInfo := skill.ToolInfo{Name: "apply_patch", MCPTrustLevel: "local", ExecutionMode: "local_mcp"}
	narrowPlanPolicy := func() *ToolExecutionPolicy {
		return NewToolExecutionPolicy([]string{"view", "grep"}, false).
			SetPlanWriteExemption(true, []string{"plan.md"}, []string{filepath.Join(workspace, "plan.md")})
	}

	t.Run("plan file target is allowed", func(t *testing.T) {
		policy := narrowPlanPolicy()
		require.NoError(t, policy.AllowTool("write"))
		require.NoError(t, policy.AllowToolCallWithContext(ctx, writeInfo, map[string]interface{}{
			"file_path": "plan.md",
			"content":   "# plan",
		}))
	})

	t.Run("non-plan target is denied", func(t *testing.T) {
		policy := narrowPlanPolicy()
		err := policy.AllowToolCallWithContext(ctx, writeInfo, map[string]interface{}{
			"file_path": "main.go",
			"content":   "package main",
		})
		require.Error(t, err)
	})

	t.Run("same base name in another directory is denied", func(t *testing.T) {
		policy := narrowPlanPolicy()
		err := policy.AllowToolCallWithContext(ctx, writeInfo, map[string]interface{}{
			"file_path": "other/plan.md",
			"content":   "# trap",
		})
		require.Error(t, err)
	})

	t.Run("apply_patch limited to the plan file is allowed", func(t *testing.T) {
		policy := narrowPlanPolicy()
		patchText := buildCodexPatch(codexUpdateFile+"plan.md", "@@", "+# step")
		require.NoError(t, policy.AllowToolCallWithContext(ctx, patchInfo, map[string]interface{}{
			"patch": patchText,
		}))
	})

	t.Run("apply_patch with an extra target is denied", func(t *testing.T) {
		policy := narrowPlanPolicy()
		patchText := buildCodexPatch(
			codexUpdateFile+"plan.md",
			"@@",
			"+# step",
			codexUpdateFile+"main.go",
			"@@",
			"+package main",
		)
		err := policy.AllowToolCallWithContext(ctx, patchInfo, map[string]interface{}{
			"patch": patchText,
		})
		require.Error(t, err)
	})

	t.Run("apply_patch without a parseable target is denied", func(t *testing.T) {
		policy := narrowPlanPolicy()
		err := policy.AllowToolCallWithContext(ctx, patchInfo, map[string]interface{}{
			"patch": "plan.md\nnot a patch header\n",
		})
		require.Error(t, err)
	})

	t.Run("without plan mode the narrow allowlist still applies", func(t *testing.T) {
		policy := NewToolExecutionPolicy([]string{"view", "grep"}, false)
		require.Error(t, policy.AllowTool("write"))
		require.Error(t, policy.AllowTool("apply_patch"))
	})

	t.Run("explicit deny wins over the plan exemption", func(t *testing.T) {
		policy := narrowPlanPolicy()
		policy.DeniedTools = map[string]bool{"write": true}
		require.Error(t, policy.AllowTool("write"))
	})

	t.Run("read-only policy still blocks plan writes", func(t *testing.T) {
		policy := NewToolExecutionPolicy([]string{"view"}, true).
			SetPlanWriteExemption(true, []string{"plan.md"}, []string{filepath.Join(workspace, "plan.md")})
		require.Error(t, policy.AllowTool("write"))
		require.Error(t, policy.AllowToolCallWithContext(ctx, writeInfo, map[string]interface{}{
			"file_path": "plan.md",
			"content":   "# plan",
		}))
	})

	t.Run("capability scope still blocks plan writes", func(t *testing.T) {
		policy := NewCapabilityScopedToolExecutionPolicy([]string{"view"}, []Capability{CapReadOnly}).
			SetPlanWriteExemption(true, []string{"plan.md"}, []string{filepath.Join(workspace, "plan.md")})
		require.Error(t, policy.AllowTool("write"))
	})

	t.Run("sandbox path boundary still blocks outside the root", func(t *testing.T) {
		root := t.TempDir()
		outside := filepath.Join(root, "..", "plan.md")
		policy := NewToolExecutionPolicy([]string{"view"}, false).
			SetPlanWriteExemption(true, nil, []string{outside})
		policy.Sandbox = executor.NewSandbox(&executor.SandboxConfig{
			Enabled:      true,
			AllowedPaths: []string{root},
		})
		patchText := buildCodexPatch(codexHeader("Add File: ")+outside, "+# plan")
		err := policy.AllowToolCall(patchInfo, map[string]interface{}{
			"patch": patchText,
		})
		require.Error(t, err, "the plan exemption must not bypass the sandbox boundary")
	})
}

func TestDeriveChildForTaskNarrowsParentCapabilities(t *testing.T) {
	parent := NewCapabilityScopedToolExecutionPolicy(nil, []Capability{
		CapReadOnly, CapWriteFS, CapExecShell, CapNetwork, CapAgentManagement,
	})
	child := parent.DeriveChildForTask([]string{"read_file", "web_search", "write_file"}, true, "reviewer", nil)
	assert.True(t, child.ReadOnly)
	assert.Equal(t, []string{"network", "read_only"}, child.AllowedCapabilityNames())
	assert.NoError(t, child.AllowTool("web_search"))
	assert.Error(t, child.AllowTool("write_file"))
	assert.Error(t, child.AllowTool("spawn_agent"))
}

func TestToolExecutionPolicy_AllowToolCall_BlocksPatchPathOutsideSandbox(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(root, "..", "outside.txt")

	policy := NewToolExecutionPolicy(nil, false)
	policy.Sandbox = executor.NewSandbox(&executor.SandboxConfig{
		Enabled:      true,
		AllowedPaths: []string{root},
	})

	patch := strings.Join([]string{
		"*** Begin Patch",
		"*** Add File: " + outside,
		"+hello",
		"*** End Patch",
	}, "\n")
	err := policy.AllowToolCall(skill.ToolInfo{
		Name:          "apply_patch",
		MCPTrustLevel: "local",
		ExecutionMode: "local_mcp",
	}, map[string]interface{}{
		"patch": patch,
	})
	if err == nil {
		t.Fatal("expected patch path outside sandbox to be blocked")
	}
}

func TestToolExecutionPolicy_AllowToolCall_BlocksDeniedURLHost(t *testing.T) {
	policy := NewToolExecutionPolicy(nil, false)
	policy.Sandbox = executor.NewSandbox(&executor.SandboxConfig{
		Enabled:      true,
		AllowedHosts: []string{"example.com"},
		DeniedHosts:  []string{"localhost"},
	})

	err := policy.AllowToolCall(skill.ToolInfo{
		Name:          "fetch_url",
		MCPTrustLevel: "local",
		ExecutionMode: "local_mcp",
	}, map[string]interface{}{
		"options": map[string]interface{}{
			"url": "http://localhost:8080/data",
		},
	})
	if err == nil {
		t.Fatal("expected denied host url to be blocked")
	}
}

func TestToolExecutionPolicy_AllowToolCall_AllowsNestedURLWithinAllowedHosts(t *testing.T) {
	policy := NewToolExecutionPolicy(nil, false)
	policy.Sandbox = executor.NewSandbox(&executor.SandboxConfig{
		Enabled:      true,
		AllowedHosts: []string{"example.com"},
	})

	err := policy.AllowToolCall(skill.ToolInfo{
		Name:          "fetch_url",
		MCPTrustLevel: "local",
		ExecutionMode: "local_mcp",
	}, map[string]interface{}{
		"request": map[string]interface{}{
			"url": "https://api.example.com/search",
		},
	})
	if err != nil {
		t.Fatalf("expected allowed host url to pass, got %v", err)
	}
}

func TestToolExecutionPolicy_DeriveChild_PreservesSandboxAndIntersectsAllowlist(t *testing.T) {
	root := t.TempDir()
	parent := NewToolExecutionPolicy([]string{"read_file", "git_log"}, false)
	parent.Sandbox = executor.NewSandbox(&executor.SandboxConfig{
		Enabled:          true,
		AllowedPaths:     []string{root},
		AllowedHosts:     []string{"example.com"},
		MaxExecutionTime: 5 * time.Second,
	})

	child := parent.DeriveChild([]string{"read_file", "write_file"}, true)
	if child == nil {
		t.Fatal("expected child policy")
	}
	if !child.ReadOnly {
		t.Fatal("expected child policy to be read-only")
	}
	if !child.AllowlistEnabled {
		t.Fatal("expected child allowlist to remain enabled")
	}
	if len(child.AllowedTools) != 1 || !child.AllowedTools["read_file"] {
		t.Fatalf("expected child allowlist to intersect to read_file, got %#v", child.AllowedTools)
	}
	if err := child.AllowTool("write_file"); err == nil {
		t.Fatal("expected write_file to be blocked by derived allowlist")
	}
	if child.Sandbox == nil {
		t.Fatal("expected sandbox to be inherited")
	}
	cfg := child.Sandbox.Config()
	if cfg.MaxExecutionTime != 5*time.Second {
		t.Fatalf("expected sandbox timeout to be preserved, got %v", cfg.MaxExecutionTime)
	}
	if len(cfg.AllowedPaths) != 1 || cfg.AllowedPaths[0] != root {
		t.Fatalf("expected sandbox allowed path to be preserved, got %#v", cfg.AllowedPaths)
	}
}

func TestToolExecutionPolicy_BlockDelegationOverridesRuntimeEssentialBypass(t *testing.T) {
	policy := NewToolExecutionPolicy([]string{"view", "spawn_agent", "spawn_subagents", "spawn_team"}, true)
	policy.BlockDelegation = true

	for _, name := range []string{"spawn_agent", "spawn_subagents", "spawn_team"} {
		if err := policy.AllowTool(name); err == nil {
			t.Fatalf("expected inherited delegation boundary to block %q", name)
		} else if !strings.Contains(err.Error(), "nested delegation is disabled") {
			t.Fatalf("expected actionable delegation error for %q, got %v", name, err)
		}
	}
	if err := policy.AllowTool("view"); err != nil {
		t.Fatalf("expected non-delegation read tool to remain allowed, got %v", err)
	}

	child := policy.DeriveChild([]string{"spawn_agent"}, true)
	if !child.BlockDelegation {
		t.Fatal("expected delegation boundary to be inherited by derived child")
	}
}

// testPatchMarker keeps the Codex apply_patch markers out of the patch hunk
// that defines them.
const testPatchMarker = "***"

func TestToolExecutionPolicy_AllowToolCall_RejectsUninspectableArgumentKinds(t *testing.T) {
	root := t.TempDir()
	info := skill.ToolInfo{Name: "read_file", MCPTrustLevel: "local", ExecutionMode: "local_mcp"}
	newSandboxedPolicy := func() *ToolExecutionPolicy {
		policy := NewToolExecutionPolicy(nil, false)
		policy.Sandbox = executor.NewSandbox(&executor.SandboxConfig{
			Enabled:      true,
			AllowedPaths: []string{root},
		})
		return policy
	}

	cases := []struct {
		name string
		args map[string]interface{}
		want string
	}{
		{
			name: "scalar path",
			args: map[string]interface{}{"path": 42},
			want: `"path" must be a string, got number`,
		},
		{
			name: "nested bool target",
			args: map[string]interface{}{"options": map[string]interface{}{"target": true}},
			want: `"target" must be a string, got bool`,
		},
		{
			name: "list with numeric entry",
			args: map[string]interface{}{"paths": []interface{}{"notes.txt", 7}},
			want: `"paths" must be a string, got number`,
		},
		{
			name: "object patch",
			args: map[string]interface{}{"patch": map[string]interface{}{"file_path": "notes.txt"}},
			want: `"patch" must be a patch/diff string, got object`,
		},
		{
			name: "numeric command",
			args: map[string]interface{}{"command": 1.5},
			want: `"command" must be a string, got number`,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			err := newSandboxedPolicy().AllowToolCall(info, testCase.args)
			if err == nil {
				t.Fatalf("expected %#v to be rejected instead of silently skipping sandbox checks", testCase.args)
			}
			if !strings.Contains(err.Error(), "cannot verify") {
				t.Fatalf("expected actionable policy error, got %v", err)
			}
			if !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("expected error to mention %q, got %v", testCase.want, err)
			}
		})
	}
}

func TestToolExecutionPolicy_AllowToolCall_KeepsInspectableArgumentsAllowed(t *testing.T) {
	root := t.TempDir()
	policy := NewToolExecutionPolicy(nil, false)
	policy.Sandbox = executor.NewSandbox(&executor.SandboxConfig{
		Enabled:      true,
		AllowedPaths: []string{root},
	})
	info := skill.ToolInfo{Name: "apply_patch", MCPTrustLevel: "local", ExecutionMode: "local_mcp"}

	inside := filepath.Join(root, "notes.txt")
	args := map[string]interface{}{
		"paths": []interface{}{inside},
		"options": map[string]interface{}{
			"workdir": root,
		},
		"patch": strings.Join([]string{
			testPatchMarker + " Begin Patch",
			testPatchMarker + " Add File: " + inside,
			"+hello",
			testPatchMarker + " End Patch",
		}, "\n"),
	}
	if err := policy.AllowToolCall(info, args); err != nil {
		t.Fatalf("expected inspectable in-sandbox arguments to pass, got %v", err)
	}

	// An empty patch carries no mutation and must not trip the kind check.
	if err := policy.AllowToolCall(info, map[string]interface{}{"patch": ""}); err != nil {
		t.Fatalf("expected empty patch to pass the kind check, got %v", err)
	}
}

func TestToolExecutionPolicy_AllowToolCall_KindCheckRequiresActiveEnforcement(t *testing.T) {
	// Without a sandbox and outside read-only mode there is nothing to bypass,
	// so the kind check must stay out of the way.
	policy := NewToolExecutionPolicy(nil, false)
	if err := policy.AllowToolCall(skill.ToolInfo{Name: "read_file"}, map[string]interface{}{"path": 42}); err != nil {
		t.Fatalf("expected no enforcement without sandbox or read-only mode, got %v", err)
	}
}

func TestToolExecutionPolicy_AllowToolCall_InspectsRawFallbackArguments(t *testing.T) {
	policy := NewToolExecutionPolicy(nil, true)
	info := skill.ToolInfo{Name: "read_file", MCPTrustLevel: "local", ExecutionMode: "local_mcp"}

	err := policy.AllowToolCall(info, map[string]interface{}{"_raw": `{"command": "bash"}`})
	if err == nil {
		t.Fatal("expected shell-like command hidden in the _raw fallback to be blocked")
	}

	if err := policy.AllowToolCall(info, map[string]interface{}{"_raw": `{"path": "notes.txt"}`}); err != nil {
		t.Fatalf("expected benign _raw fallback to stay allowed, got %v", err)
	}
}

func TestToolExecutionPolicy_AllowToolCall_MinesPathsFromPatchTextAlias(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(root, "..", "outside.txt")
	policy := NewToolExecutionPolicy(nil, false)
	policy.Sandbox = executor.NewSandbox(&executor.SandboxConfig{
		Enabled:      true,
		AllowedPaths: []string{root},
	})

	patch := strings.Join([]string{
		testPatchMarker + " Begin Patch",
		testPatchMarker + " Add File: " + outside,
		"+hello",
		testPatchMarker + " End Patch",
	}, "\n")
	err := policy.AllowToolCall(skill.ToolInfo{
		Name:          "apply_patch",
		MCPTrustLevel: "local",
		ExecutionMode: "local_mcp",
	}, map[string]interface{}{
		"patch_text": patch,
	})
	if err == nil {
		t.Fatal("expected path outside sandbox mined from patch_text to be blocked")
	}
}

func TestHasMutationHintsDetectsEveryMutationShape(t *testing.T) {
	beginPatchText := testPatchMarker + " Begin Patch"
	updateFileText := testPatchMarker + " Update File: a.go"
	mutating := []map[string]interface{}{
		{"patch": beginPatchText},
		{"patch_text": beginPatchText},
		{"diff": []interface{}{updateFileText}},
		{"patch": map[string]interface{}{"file_path": "a.go"}},
		{"mutated_paths": []interface{}{1}},
		{"mutated_files": []string{"a.go"}},
		{"changed_paths": 3},
		{"changed_files": map[string]interface{}{"a.go": true}},
	}
	for _, args := range mutating {
		if !HasMutationHints(args) {
			t.Fatalf("expected mutation hint for %#v", args)
		}
	}

	benign := []map[string]interface{}{
		nil,
		{},
		{"patch": ""},
		{"diff": "   "},
		{"mutated_paths": []interface{}{}},
		{"mutated_files": []string{}},
		{"path": "notes.txt"},
	}
	for _, args := range benign {
		if HasMutationHints(args) {
			t.Fatalf("expected no mutation hint for %#v", args)
		}
	}
}

// sandboxedPolicyAllowing builds the same shape of policy a directory-bound
// session uses: a sandbox that permits only the given paths.
func sandboxedPolicyAllowing(allowed ...string) *ToolExecutionPolicy {
	policy := NewToolExecutionPolicy(nil, false)
	policy.Sandbox = executor.NewSandbox(&executor.SandboxConfig{
		Enabled:      true,
		AllowedPaths: allowed,
	})
	return policy
}

func localToolInfo(name string) skill.ToolInfo {
	return skill.ToolInfo{Name: name, MCPTrustLevel: "local", ExecutionMode: "local_mcp"}
}

// TestToolExecutionPolicy_InspectsCanonicalToolkitArgumentNames covers the gap
// where the executor reads file_path/root/target_path/... while the policy only
// knew the legacy alias names, so the sandbox path check silently covered
// nothing for the common built-in call shape.
func TestToolExecutionPolicy_InspectsCanonicalToolkitArgumentNames(t *testing.T) {
	workspace := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.txt")

	denied := []struct {
		tool string
		args map[string]interface{}
	}{
		{"view", map[string]interface{}{"file_path": outside}},
		{"write", map[string]interface{}{"file_path": outside, "content": "x"}},
		{"edit", map[string]interface{}{"filename": outside, "old_string": "a", "new_string": "b"}},
		{"download", map[string]interface{}{"url": "https://example.com/x", "target_path": outside}},
		{"grep", map[string]interface{}{"pattern": "todo", "root": outside}},
		{"glob", map[string]interface{}{"pattern": "*.go", "directory": outside}},
		{"ls", map[string]interface{}{"search_path": outside}},
		{"shell", map[string]interface{}{"command": "git status", "workdir": outside}},
	}
	for _, testCase := range denied {
		t.Run("denies_"+testCase.tool, func(t *testing.T) {
			err := sandboxedPolicyAllowing(workspace).AllowToolCall(localToolInfo(testCase.tool), testCase.args)
			if err == nil {
				t.Fatalf("expected %s %#v to be sandbox-checked", testCase.tool, testCase.args)
			}
			if !strings.Contains(err.Error(), "outside sandbox allowlist") {
				t.Fatalf("expected sandbox denial for %s, got %v", testCase.tool, err)
			}
		})
	}

	allowed := []struct {
		tool string
		args map[string]interface{}
	}{
		{"view", map[string]interface{}{"file_path": filepath.Join(workspace, "notes.md")}},
		{"write", map[string]interface{}{"file_path": filepath.Join(workspace, "notes.md"), "content": "x"}},
		{"grep", map[string]interface{}{"pattern": "todo", "root": workspace}},
		{"ls", map[string]interface{}{"directory": workspace}},
		{"shell", map[string]interface{}{"command": "git status", "workdir": workspace}},
		{"apply_patch", map[string]interface{}{"patch": strings.Join([]string{
			testPatchMarker + " Begin Patch",
			testPatchMarker + " Add File: " + filepath.Join(workspace, "notes.md"),
			"+hello",
			testPatchMarker + " End Patch",
		}, "\n")}},
	}
	for _, testCase := range allowed {
		t.Run("allows_"+testCase.tool, func(t *testing.T) {
			if err := sandboxedPolicyAllowing(workspace).AllowToolCall(localToolInfo(testCase.tool), testCase.args); err != nil {
				t.Fatalf("expected in-sandbox %s call to pass, got %v", testCase.tool, err)
			}
		})
	}
}

// TestToolExecutionPolicy_ResolvesRelativePathsAgainstSessionWorkspace pins the
// resolution contract: the policy must validate the file the executor touches,
// which anchors relative paths to the session workspace root instead of the
// server process working directory.
func TestToolExecutionPolicy_ResolvesRelativePathsAgainstSessionWorkspace(t *testing.T) {
	workspace := t.TempDir()
	policy := sandboxedPolicyAllowing(workspace)
	info := localToolInfo("write")
	args := map[string]interface{}{"file_path": "notes/todo.md", "content": "x"}

	// Without session context the legacy cwd-based resolution applies, which
	// falls outside this sandbox.
	if err := policy.AllowToolCall(info, args); err == nil {
		t.Fatal("expected cwd-based resolution to fall outside the sandbox allowlist")
	}

	ctx := toolctx.WithWorkspaceRoot(context.Background(), workspace)
	if err := policy.AllowToolCallWithContext(ctx, info, args); err != nil {
		t.Fatalf("expected relative path to resolve inside the session workspace, got %v", err)
	}

	// A workspace root anchors relative paths; it never widens the sandbox.
	absoluteOutside := map[string]interface{}{
		"file_path": filepath.Join(filepath.Dir(workspace), "escape.md"),
		"content":   "x",
	}
	if err := policy.AllowToolCallWithContext(ctx, info, absoluteOutside); err == nil {
		t.Fatal("expected absolute path outside the workspace to stay denied")
	}

	traversal := map[string]interface{}{"file_path": filepath.Join("..", "escape.md"), "content": "x"}
	if err := policy.AllowToolCallWithContext(ctx, info, traversal); err == nil {
		t.Fatal("expected traversal to be cleaned before the sandbox check")
	}
}

// TestToolExecutionPolicy_KeepsProviderDefinedArgumentNamesUntouched freezes the
// other half of the contract: widening built-in coverage must not reinterpret
// arguments of tools the runtime does not own.
func TestToolExecutionPolicy_KeepsProviderDefinedArgumentNamesUntouched(t *testing.T) {
	workspace := t.TempDir()
	args := map[string]interface{}{
		"root":      filepath.Join(t.TempDir(), "index"),
		"directory": "corpus",
		"script":    "echo hi",
	}
	if err := sandboxedPolicyAllowing(workspace).AllowToolCall(localToolInfo("mcp_search_docs"), args); err != nil {
		t.Fatalf("expected provider-defined arguments to stay untouched, got %v", err)
	}
}

// TestPolicyArgKeys_CoverEveryToolkitCanonicalArgument is the drift guard: when a
// built-in tool starts reading a new argument, the policy must either classify it
// or deliberately treat it as an uninspectable payload.
func TestPolicyArgKeys_CoverEveryToolkitCanonicalArgument(t *testing.T) {
	payloadCanonicalArgs := map[string]bool{
		"content":    true,
		"pattern":    true,
		"patterns":   true,
		"old_string": true,
		"new_string": true,
	}

	toolNames := toolargs.ToolkitToolNames()
	if len(toolNames) == 0 {
		t.Fatal("expected the shared toolkit alias table to list the built-in tools")
	}
	for _, toolName := range toolNames {
		aliases, ok := toolargs.ToolkitArgAliasesFor(toolName)
		if !ok {
			t.Fatalf("expected alias table entry for built-in tool %q", toolName)
		}
		keys := policyArgKeysForTool(toolName)
		pairs := append([]toolargs.ToolkitArgAliasPair{}, aliases.Args...)
		for _, field := range aliases.ListFields {
			pairs = append(pairs, field...)
		}
		if len(pairs) == 0 {
			t.Fatalf("expected %q to declare at least one argument", toolName)
		}
		for _, pair := range pairs {
			canonical := strings.ToLower(strings.TrimSpace(pair.Canonical))
			if payloadCanonicalArgs[canonical] {
				if keys.scalar[canonical] {
					t.Fatalf("%q of %q carries a payload but is classified as policy-relevant", canonical, toolName)
				}
				continue
			}
			if !keys.scalar[canonical] {
				t.Fatalf("canonical argument %q of built-in tool %q is not classified by the policy; add it to policyToolkitCanonicalArgClasses or to the payload allowlist", canonical, toolName)
			}
			for _, alias := range pair.Aliases {
				normalizedAlias := strings.ToLower(strings.TrimSpace(alias))
				if normalizedAlias == "" {
					continue
				}
				if !keys.scalar[normalizedAlias] {
					t.Fatalf("alias %q (of %q in %q) is promoted by the executor but not inspected by the policy", alias, canonical, toolName)
				}
			}
		}
	}
}

func TestResolvePolicyPath(t *testing.T) {
	workspace := t.TempDir()
	ctx := toolctx.WithWorkspaceRoot(context.Background(), workspace)
	policy := NewToolExecutionPolicy(nil, false)

	if got := policy.resolvePolicyPath(ctx, filepath.Join(workspace, "a.txt")); got != filepath.Join(workspace, "a.txt") {
		t.Fatalf("expected absolute path to be preserved, got %q", got)
	}
	if got := policy.resolvePolicyPath(ctx, filepath.Join("sub", "a.txt")); got != filepath.Join(workspace, "sub", "a.txt") {
		t.Fatalf("expected relative path to join the workspace root, got %q", got)
	}
	if got := policy.resolvePolicyPath(ctx, filepath.Join("..", "escape.txt")); got != filepath.Clean(filepath.Join(workspace, "..", "escape.txt")) {
		t.Fatalf("expected traversal to be cleaned, got %q", got)
	}
	if got := policy.resolvePolicyPath(context.Background(), filepath.Join("sub", "a.txt")); got != filepath.Join("sub", "a.txt") {
		t.Fatalf("expected path to stay untouched without a workspace root, got %q", got)
	}
	if got := policy.resolvePolicyPath(nil, "a.txt"); got != "a.txt" {
		t.Fatalf("expected nil context to keep the raw path, got %q", got)
	}
	if got := policy.resolvePolicyPath(ctx, "   "); got != "" {
		t.Fatalf("expected blank path to stay blank, got %q", got)
	}

	relativeRoot, err := filepath.Abs(filepath.Join("relative", "root"))
	if err != nil {
		t.Fatalf("resolve relative root: %v", err)
	}
	relativeCtx := toolctx.WithWorkspaceRoot(context.Background(), filepath.Join("relative", "root"))
	if got := policy.resolvePolicyPath(relativeCtx, "a.txt"); got != filepath.Join(relativeRoot, "a.txt") {
		t.Fatalf("expected non-absolute workspace root to be resolved, got %q", got)
	}
}

// The static policy must resolve relative path arguments against the same base
// path the toolkit executes against (SetBasePath) whenever the context carries no
// workspace root; otherwise it validates a different file than the tool touches.
func TestResolvePolicyPathFallsBackToAnchorRoot(t *testing.T) {
	anchor := t.TempDir()
	ctxRoot := t.TempDir()

	policy := NewToolExecutionPolicy(nil, false)
	policy.SetPathAnchorRoot(anchor)

	if got := policy.resolvePolicyPath(context.Background(), filepath.Join("sub", "a.txt")); got != filepath.Join(anchor, "sub", "a.txt") {
		t.Fatalf("expected relative path to join the anchor root, got %q", got)
	}
	if got := policy.resolvePolicyPath(nil, "a.txt"); got != filepath.Join(anchor, "a.txt") {
		t.Fatalf("expected nil context to use the anchor root, got %q", got)
	}
	if got := policy.resolvePolicyPath(context.Background(), filepath.Join("..", "escape.txt")); got != filepath.Clean(filepath.Join(anchor, "..", "escape.txt")) {
		t.Fatalf("expected traversal to be cleaned against the anchor root, got %q", got)
	}
	if got := policy.resolvePolicyPath(context.Background(), filepath.Join(anchor, "abs.txt")); got != filepath.Join(anchor, "abs.txt") {
		t.Fatalf("expected absolute path to be preserved, got %q", got)
	}

	// The session-bound root from ctx always wins over the fallback anchor.
	ctx := toolctx.WithWorkspaceRoot(context.Background(), ctxRoot)
	if got := policy.resolvePolicyPath(ctx, "a.txt"); got != filepath.Join(ctxRoot, "a.txt") {
		t.Fatalf("expected context root to win over the anchor, got %q", got)
	}

	// A blank anchor keeps the historical "leave it to the sandbox" behavior.
	blank := NewToolExecutionPolicy(nil, false)
	blank.SetPathAnchorRoot("   ")
	if got := blank.resolvePolicyPath(context.Background(), "a.txt"); got != "a.txt" {
		t.Fatalf("expected blank anchor to keep the raw path, got %q", got)
	}
}

// A clone must keep the anchor so derived child policies resolve paths the same
// way as the parent (DeriveChild clones before narrowing the allowlist).
func TestSetPathAnchorRootSurvivesCloneAndDeriveChild(t *testing.T) {
	anchor := t.TempDir()
	parent := NewToolExecutionPolicy([]string{"read_file"}, false).SetPathAnchorRoot(anchor)
	if parent.PathAnchorRoot != anchor {
		t.Fatalf("expected anchor %q on parent, got %q", anchor, parent.PathAnchorRoot)
	}
	clone := parent.Clone()
	if clone.PathAnchorRoot != anchor {
		t.Fatalf("expected clone to keep anchor %q, got %q", anchor, clone.PathAnchorRoot)
	}
	child := parent.DeriveChild([]string{"read_file"}, true)
	if child.PathAnchorRoot != anchor {
		t.Fatalf("expected derived child to keep anchor %q, got %q", anchor, child.PathAnchorRoot)
	}
}

// Without a context root the sandbox must still decide on the file the executor
// will touch. The process working directory differs from the anchored workspace
// here, so a cwd-anchored check would reach the opposite verdict for the first
// call and would silently validate the wrong file for the second.
func TestAllowToolCallAnchorsRelativePathToPolicyRoot(t *testing.T) {
	anchor := t.TempDir()
	policy := NewToolExecutionPolicy(nil, false).SetPathAnchorRoot(anchor)
	policy.Sandbox = executor.NewSandbox(&executor.SandboxConfig{
		Enabled:      true,
		AllowedPaths: []string{anchor},
	})
	info := skill.ToolInfo{Name: "view"}

	if err := policy.AllowToolCall(info, map[string]interface{}{"file_path": "inside.txt"}); err != nil {
		t.Fatalf("expected anchored relative path inside the workspace to be allowed, got %v", err)
	}
	if err := policy.AllowToolCall(info, map[string]interface{}{"file_path": filepath.Join("..", "escape.txt")}); err == nil {
		t.Fatal("expected anchored traversal out of the workspace to be denied")
	}
}

func TestHasMutationHints_DetectsThirdPartyMutationNames(t *testing.T) {
	mutations := []map[string]interface{}{
		{"write_paths": []interface{}{"a.go"}},
		{"files_written": []interface{}{"a.go"}},
		{"deleted_files": "a.go"},
		{"created_paths": []string{"a.go"}},
		{"mutated_uris": "https://example.com"},
		{"patched_uri": "https://example.com"},
		{"renamed_files": map[string]interface{}{"a.go": "b.go"}},
		{"writes": 2},
	}
	for _, args := range mutations {
		if !HasMutationHints(args) {
			t.Fatalf("expected %#v to be detected as a mutation hint", args)
		}
	}

	reads := []map[string]interface{}{
		{"path": "a.go"},
		{"pattern": "todo"},
		{"created_after": "2026-01-01"},
		{"updated_at": "2026-01-01"},
		{"updated_ids": []interface{}{1}},
		{"search_paths": []interface{}{"docs"}},
		{"write_enabled": true},
	}
	for _, args := range reads {
		if HasMutationHints(args) {
			t.Fatalf("expected %#v to stay a read hint", args)
		}
	}
}
