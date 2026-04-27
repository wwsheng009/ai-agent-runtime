package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

func TestResolveChatProfileState_AppliesDefaultsPromptAndPolicy(t *testing.T) {
	profilesRoot := t.TempDir()
	globalSkills := t.TempDir()
	profileRoot := filepath.Join(profilesRoot, "dev")

	writeTestFile(t, filepath.Join(profileRoot, "profile.yaml"), `profile:
  name: dev
  default_agent: coder
providers:
  default_provider: nvidia
agents:
  coder:
    model: z-ai/glm4.7
`)
	writeTestFile(t, filepath.Join(profileRoot, "runtime.yaml"), "agent:\n  defaultModel: custom\n")
	writeTestFile(t, filepath.Join(profileRoot, "mcp.yaml"), "mcp_servers: {}\n")
	writeTestFile(t, filepath.Join(profileRoot, "agents", "coder", "prompts", "system.md"), "System prompt.")
	writeTestFile(t, filepath.Join(profileRoot, "agents", "coder", "prompts", "tools.md"), "Use tools carefully.")
	writeTestFile(t, filepath.Join(profileRoot, "agents", "coder", "memory", "memory.json"), `{"summary":"cached profile memory"}`)
	writeTestFile(t, filepath.Join(profileRoot, "agents", "coder", "context", "notes.md"), "Profile investigation notes.")
	writeTestFile(t, filepath.Join(profileRoot, "agents", "coder", "tools", "policy.yaml"), `allowlist: [read_file]
denylist: [write_file]
read_only: true
sandbox:
  allowedPaths: ["."]`)
	mustMkdir(t, filepath.Join(profileRoot, "agents", "coder", "sessions"))
	mustMkdir(t, filepath.Join(profileRoot, "skills"))
	mustMkdir(t, filepath.Join(profileRoot, "agents", "coder", "workspace", "skills"))

	cfg := &config.Config{
		Profiles: &config.ProfilesConfig{Root: profilesRoot},
		AICLI: &config.AICLIConfig{
			MCP: &config.AICLIMCPConfig{ConfigFile: filepath.Join("configs", "mcp.yaml")},
		},
		SkillsRuntime: &config.SkillsRuntimeConfig{
			Enabled:  true,
			SkillDir: globalSkills,
		},
	}

	state, err := resolveChatProfileState(cfg, &chatCommandOptions{ProfileFlag: "dev"})
	if err != nil {
		t.Fatalf("resolveChatProfileState: %v", err)
	}
	if !state.Active() {
		t.Fatal("expected active profile state")
	}
	if state.Resolved.ProfileName != "dev" || state.Resolved.AgentID != "coder" {
		t.Fatalf("unexpected resolved profile: %+v", state.Resolved)
	}
	if state.Resolved.RuntimeConfig != filepath.Join(profileRoot, "runtime.yaml") {
		t.Fatalf("unexpected runtime config path: %q", state.Resolved.RuntimeConfig)
	}
	if state.Resolved.MCPConfig != filepath.Join(profileRoot, "mcp.yaml") {
		t.Fatalf("unexpected mcp config path: %q", state.Resolved.MCPConfig)
	}
	if len(state.Resolved.SkillDirs) != 3 {
		t.Fatalf("unexpected skill dirs: %#v", state.Resolved.SkillDirs)
	}
	if !strings.Contains(state.PromptText, "# System\nSystem prompt.") {
		t.Fatalf("expected composed system prompt, got:\n%s", state.PromptText)
	}
	if !strings.Contains(state.PromptText, "cached profile memory") {
		t.Fatalf("expected profile memory in prompt, got:\n%s", state.PromptText)
	}
	if !strings.Contains(state.PromptText, "Profile investigation notes.") {
		t.Fatalf("expected profile notes in prompt, got:\n%s", state.PromptText)
	}
	if state.ContextValues == nil {
		t.Fatal("expected profile context values")
	}
	if state.ContextValues["profile_memory_path"] != filepath.Join(profileRoot, "agents", "coder", "memory", "memory.json") {
		t.Fatalf("unexpected profile memory path: %#v", state.ContextValues["profile_memory_path"])
	}
	if state.ToolPolicy == nil || !state.ToolPolicy.ReadOnly {
		t.Fatalf("expected read-only tool policy, got %#v", state.ToolPolicy)
	}
	if err := state.ToolPolicy.AllowTool("read_file"); err != nil {
		t.Fatalf("expected read_file to be allowed, got %v", err)
	}
	if err := state.ToolPolicy.AllowTool("write_file"); err == nil {
		t.Fatal("expected write_file to be blocked")
	}

	opts := &chatCommandOptions{}
	applyProfileDefaultsToChatOptions(opts, state)
	if opts.ProviderFlag != "nvidia" {
		t.Fatalf("expected provider default nvidia, got %q", opts.ProviderFlag)
	}
	if opts.ModelFlag != "z-ai/glm4.7" {
		t.Fatalf("expected model default z-ai/glm4.7, got %q", opts.ModelFlag)
	}
	if opts.SessionDirFlag != filepath.Join(profileRoot, "agents", "coder", "sessions") {
		t.Fatalf("unexpected session dir default: %q", opts.SessionDirFlag)
	}
	if !opts.SessionFeaturesRequested {
		t.Fatal("expected session features requested to be enabled for profile mode")
	}
}

func TestEnsureChatSystemPromptMessage_PrependsAndReplaces(t *testing.T) {
	session := &ChatSession{
		SystemPromptText: "Profile system prompt.",
		Messages: []map[string]interface{}{
			{"role": "user", "content": "hello"},
		},
	}

	ensureChatSystemPromptMessage(session)
	if len(session.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(session.Messages))
	}
	expected := composeChatSystemPromptWithGuidance(session)
	if session.Messages[0]["role"] != "system" || session.Messages[0]["content"] != expected {
		t.Fatalf("unexpected leading system message: %#v", session.Messages[0])
	}

	session.SystemPromptText = "Updated prompt."
	ensureChatSystemPromptMessage(session)
	if len(session.Messages) != 2 {
		t.Fatalf("expected no duplicate system message, got %d", len(session.Messages))
	}
	expected = composeChatSystemPromptWithGuidance(session)
	if session.Messages[0]["content"] != expected {
		t.Fatalf("expected system prompt update, got %#v", session.Messages[0]["content"])
	}
}

func writeTestFile(t *testing.T, path string, contents string) {
	t.Helper()
	mustMkdir(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}
