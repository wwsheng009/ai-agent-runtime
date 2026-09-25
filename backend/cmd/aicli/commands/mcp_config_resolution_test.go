package commands

import (
	"os"
	"path/filepath"
	"testing"

	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

func writeMCPResolutionFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func mcpResolutionConfig() *agentconfig.Config {
	return &agentconfig.Config{
		AICLI: &agentconfig.AICLIConfig{
			MCP: &agentconfig.AICLIMCPConfig{ConfigFile: "configs/mcp.yaml"},
		},
	}
}

// TestResolveChatMCPConfigPathPrefersWorkspaceDotAICLI 固定 chat 启动与管理面/服务端
// 共用同一优先级：工作区 ./.aicli/mcp.yaml 高于 configs/mcp.yaml。
func TestResolveChatMCPConfigPathPrefersWorkspaceDotAICLI(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	t.Setenv("AICLI_HOME", "")
	t.Setenv("AICLI_FOLDER_TRUST", "")

	writeMCPResolutionFixture(t, filepath.Join(root, "configs", "mcp.yaml"), "mcpServers: {}\n")
	workspaceOverride := filepath.Join(root, ".aicli", "mcp.yaml")
	writeMCPResolutionFixture(t, workspaceOverride, "mcpServers: {}\n")
	t.Chdir(root)

	cfg := mcpResolutionConfig()
	if got := resolveChatMCPConfigPath(cfg, nil); filepath.Clean(got) != filepath.Clean(workspaceOverride) {
		t.Fatalf("workspace .aicli/mcp.yaml should win: got %q want %q", got, workspaceOverride)
	}

	startup, ok := resolveChatMCPStartupConfigPath(cfg, nil)
	if !ok || filepath.Clean(startup) != filepath.Clean(workspaceOverride) {
		t.Fatalf("startup path = %q ok=%v, want %q", startup, ok, workspaceOverride)
	}

	// 会话/profile 显式指定的路径仍然最高优先。
	explicit := filepath.Join(root, "explicit.yaml")
	session := &ChatSession{MCPConfigPath: explicit}
	if got := resolveChatMCPConfigPath(cfg, session); got != explicit {
		t.Fatalf("session override should win: got %q want %q", got, explicit)
	}
}

// TestResolveChatMCPConfigPathFallsBackToConfigsDir 保证没有工作区覆盖时行为不变。
func TestResolveChatMCPConfigPathFallsBackToConfigsDir(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	t.Setenv("AICLI_HOME", "")
	t.Setenv("AICLI_FOLDER_TRUST", "")

	portable := filepath.Join(root, "configs", "mcp.yaml")
	writeMCPResolutionFixture(t, portable, "mcpServers: {}\n")
	t.Chdir(root)

	got := resolveChatMCPConfigPath(mcpResolutionConfig(), nil)
	if filepath.Clean(got) != filepath.Clean(portable) {
		t.Fatalf("expected fallback to %q, got %q", portable, got)
	}
}

// TestResolveChatMCPConfigPathWithoutConfigFile 固定“未设置 aicli.mcp.config_file 时
// 工作区层依然生效”，防止再次退回“必须显式写 config_file 才能用 ./.aicli/mcp.yaml”。
func TestResolveChatMCPConfigPathWithoutConfigFile(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	t.Setenv("AICLI_HOME", "")
	t.Setenv("AICLI_FOLDER_TRUST", "")

	writeMCPResolutionFixture(t, filepath.Join(root, "configs", "mcp.yaml"), "mcpServers: {}\n")
	workspaceOverride := filepath.Join(root, ".aicli", "mcp.yaml")
	writeMCPResolutionFixture(t, workspaceOverride, "mcpServers: {}\n")
	t.Chdir(root)

	cases := map[string]*agentconfig.Config{
		"nil mcp block":     {AICLI: &agentconfig.AICLIConfig{}},
		"empty config_file": {AICLI: &agentconfig.AICLIConfig{MCP: &agentconfig.AICLIMCPConfig{}}},
	}
	for name, cfg := range cases {
		if got := resolveChatMCPConfigPath(cfg, nil); filepath.Clean(got) != filepath.Clean(workspaceOverride) {
			t.Fatalf("%s: resolveChatMCPConfigPath = %q, want %q", name, got, workspaceOverride)
		}
		startup, ok := resolveChatMCPStartupConfigPath(cfg, nil)
		if !ok || filepath.Clean(startup) != filepath.Clean(workspaceOverride) {
			t.Fatalf("%s: startup path = %q ok=%v, want %q", name, startup, ok, workspaceOverride)
		}
	}
}
