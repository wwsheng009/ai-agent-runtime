package agent

import (
	"path/filepath"
	"testing"
	"time"

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
