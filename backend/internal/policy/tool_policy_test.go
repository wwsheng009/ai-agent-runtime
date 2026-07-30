package policy

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/wwsheng009/ai-agent-runtime/internal/executor"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
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
		"enter_plan_mode", "exit_plan_mode", "ask_user_question",
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

func TestCapabilityScopedPolicyExposesOnlyDeclaredCapabilitySurface(t *testing.T) {
	policy := NewCapabilityScopedToolExecutionPolicy(nil, []Capability{CapReadOnly, CapNetwork})
	assert.NoError(t, policy.AllowTool("read_file"))
	assert.NoError(t, policy.AllowTool("web_search"))
	assert.Error(t, policy.AllowTool("write_file"))
	assert.Error(t, policy.AllowTool("spawn_agent"))
	assert.Equal(t, []string{"network", "read_only"}, policy.AllowedCapabilityNames())
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
