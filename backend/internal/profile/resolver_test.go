package profile

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestResolve_UsesDefaultAgentAndProfileRuntimeOverride(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "profile.yaml"), `profile:
  name: dev
  default_agent: coder
providers:
  default_provider: nvidia
agents:
  coder:
    model: z-ai/glm4.7
`)
	writeTestFile(t, filepath.Join(root, "runtime.yaml"), "agent:\n  defaultModel: gpt-5\n")
	mustMkdir(t, filepath.Join(root, "agents", "coder", "sessions"))

	resolved, err := Resolve(ResolveOptions{
		Root:              root,
		GlobalRuntimePath: filepath.Join("configs", "runtime.yaml"),
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if resolved.ProfileName != "dev" {
		t.Fatalf("expected profile name dev, got %q", resolved.ProfileName)
	}
	if resolved.AgentID != "coder" {
		t.Fatalf("expected agent coder, got %q", resolved.AgentID)
	}
	if resolved.Provider != "nvidia" {
		t.Fatalf("expected provider nvidia, got %q", resolved.Provider)
	}
	if resolved.Model != "z-ai/glm4.7" {
		t.Fatalf("expected model z-ai/glm4.7, got %q", resolved.Model)
	}
	if resolved.RuntimeConfig != filepath.Join(root, "runtime.yaml") {
		t.Fatalf("expected profile runtime override, got %q", resolved.RuntimeConfig)
	}
	if resolved.Paths.SessionsDir != filepath.Join(root, "agents", "coder", "sessions") {
		t.Fatalf("unexpected sessions dir: %q", resolved.Paths.SessionsDir)
	}
}

func TestResolve_UsesExplicitAgentOverride(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "profile.yaml"), `profile:
  name: dev
  default_agent: coder
agents:
  coder: {}
  reviewer: {}
`)

	resolved, err := Resolve(ResolveOptions{
		Root:  root,
		Agent: "reviewer",
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolved.AgentID != "reviewer" {
		t.Fatalf("expected agent reviewer, got %q", resolved.AgentID)
	}
	if resolved.Paths.AgentDir != filepath.Join(root, "agents", "reviewer") {
		t.Fatalf("unexpected agent dir: %q", resolved.Paths.AgentDir)
	}
}

func TestResolve_PrefersWorkspaceMCPAndOrdersSkillDirs(t *testing.T) {
	root := t.TempDir()
	globalSkills := t.TempDir()
	globalMCP := filepath.Join(t.TempDir(), "global-mcp.yaml")
	writeTestFile(t, filepath.Join(root, "profile.yaml"), `profile:
  name: dev
  default_agent: coder
agents:
  coder: {}
`)
	writeTestFile(t, filepath.Join(root, "mcp.yaml"), "mcp_servers: {}\n")
	writeTestFile(t, filepath.Join(root, "agents", "coder", "workspace", "mcp.yaml"), "mcp_servers: {}\n")
	mustMkdir(t, filepath.Join(root, "skills"))
	mustMkdir(t, filepath.Join(root, "agents", "coder", "skills"))
	mustMkdir(t, filepath.Join(root, "agents", "coder", "workspace", "skills"))
	writeTestFile(t, globalMCP, "mcp_servers: {}\n")

	resolved, err := Resolve(ResolveOptions{
		Root:            root,
		GlobalMCPPath:   globalMCP,
		GlobalSkillDirs: []string{globalSkills},
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if resolved.MCPConfig != filepath.Join(root, "agents", "coder", "workspace", "mcp.yaml") {
		t.Fatalf("expected workspace mcp override, got %q", resolved.MCPConfig)
	}
	expectedSkills := []string{
		filepath.Join(root, "agents", "coder", "workspace", "skills"),
		filepath.Join(root, "agents", "coder", "skills"),
		filepath.Join(root, "skills"),
		globalSkills,
	}
	if !reflect.DeepEqual(resolved.SkillDirs, expectedSkills) {
		t.Fatalf("unexpected skill dirs: %#v", resolved.SkillDirs)
	}
}

func TestResolve_DetectsPromptFilesAndMergesToolPolicy(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "profile.yaml"), `profile:
  default_agent: coder
tools:
  allowlist: [read_file]
agents:
  coder:
    tools:
      allowlist: [edit_file]
`)
	writeTestFile(t, filepath.Join(root, "agents", "coder", "agent.yaml"), `tools:
  denylist: [delete_file]
`)
	writeTestFile(t, filepath.Join(root, "agents", "coder", "workspace", "workspace.yaml"), `tools:
  denylist: [write_file]
`)
	writeTestFile(t, filepath.Join(root, "agents", "coder", "tools", "policy.yaml"), `allowlist: [git_log]
read_only: true
`)
	writeTestFile(t, filepath.Join(root, "agents", "coder", "prompts", "system.md"), "system")
	writeTestFile(t, filepath.Join(root, "agents", "coder", "prompts", "role.md"), "role")
	writeTestFile(t, filepath.Join(root, "agents", "coder", "prompts", "tools.md"), "tools")
	writeTestFile(t, filepath.Join(root, "agents", "coder", "memory", "memory.json"), `{"summary":"cached profile memory"}`)
	writeTestFile(t, filepath.Join(root, "agents", "coder", "context", "notes.md"), "Profile investigation notes.")

	resolved, err := Resolve(ResolveOptions{Root: root})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	expectedAllowlist := []string{"read_file", "edit_file", "git_log"}
	if !reflect.DeepEqual(resolved.ToolPolicy.Allowlist, expectedAllowlist) {
		t.Fatalf("unexpected allowlist: %#v", resolved.ToolPolicy.Allowlist)
	}
	expectedDenylist := []string{"delete_file", "write_file"}
	if !reflect.DeepEqual(resolved.ToolPolicy.Denylist, expectedDenylist) {
		t.Fatalf("unexpected denylist: %#v", resolved.ToolPolicy.Denylist)
	}
	if resolved.ToolPolicy.ReadOnly == nil || !*resolved.ToolPolicy.ReadOnly {
		t.Fatalf("expected read_only=true, got %#v", resolved.ToolPolicy.ReadOnly)
	}
	if resolved.Prompts.System == "" || resolved.Prompts.Role == "" || resolved.Prompts.Tools == "" {
		t.Fatalf("expected prompt file detection, got %#v", resolved.Prompts)
	}
	if resolved.Paths.MemoryFile != filepath.Join(root, "agents", "coder", "memory", "memory.json") {
		t.Fatalf("unexpected memory file: %q", resolved.Paths.MemoryFile)
	}
	if resolved.Paths.ContextNotesFile != filepath.Join(root, "agents", "coder", "context", "notes.md") {
		t.Fatalf("unexpected context notes file: %q", resolved.Paths.ContextNotesFile)
	}
	expectedSources := []string{
		"profile.inline",
		"agent.inline",
		"agent.file",
		"workspace.file",
		filepath.Join(root, "agents", "coder", "tools", "policy.yaml"),
	}
	if !reflect.DeepEqual(resolved.ToolPolicy.Sources, expectedSources) {
		t.Fatalf("unexpected tool policy sources: %#v", resolved.ToolPolicy.Sources)
	}
}

func TestResolve_InfersSingleAgentDirectoryWhenDefaultMissing(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "profile.yaml"), "profile:\n  name: dev\n")
	mustMkdir(t, filepath.Join(root, "agents", "reviewer"))

	resolved, err := Resolve(ResolveOptions{Root: root})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolved.AgentID != "reviewer" {
		t.Fatalf("expected inferred reviewer agent, got %q", resolved.AgentID)
	}
}

func TestResolve_ErrorsWhenProfileFileMissing(t *testing.T) {
	_, err := Resolve(ResolveOptions{Root: t.TempDir()})
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrProfileNotFound) {
		t.Fatalf("expected ErrProfileNotFound, got %v", err)
	}
}

func TestResolve_ErrorsWhenAgentMissing(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "profile.yaml"), `profile:
  default_agent: coder
`)

	_, err := Resolve(ResolveOptions{Root: root})
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrAgentNotFound) {
		t.Fatalf("expected ErrAgentNotFound, got %v", err)
	}
}

func TestResolve_WorkspaceOverridesProviderAndModel(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "profile.yaml"), `profile:
  default_agent: coder
providers:
  default_provider: profile-provider
agents:
  coder:
    provider: inline-provider
    model: inline-model
`)
	writeTestFile(t, filepath.Join(root, "agents", "coder", "agent.yaml"), `provider: file-provider
model: file-model
`)
	writeTestFile(t, filepath.Join(root, "agents", "coder", "workspace", "workspace.yaml"), `provider: workspace-provider
model: workspace-model
`)

	resolved, err := Resolve(ResolveOptions{Root: root})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolved.Provider != "workspace-provider" {
		t.Fatalf("expected workspace provider override, got %q", resolved.Provider)
	}
	if resolved.Model != "workspace-model" {
		t.Fatalf("expected workspace model override, got %q", resolved.Model)
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
