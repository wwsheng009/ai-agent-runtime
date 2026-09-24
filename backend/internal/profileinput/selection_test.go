package profileinput

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	mcpconfig "github.com/wwsheng009/ai-agent-runtime/internal/mcp/config"
	profilesys "github.com/wwsheng009/ai-agent-runtime/internal/profile"
)

func writeSelectionTestFile(t *testing.T, path string, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestBuildSkillFilter_NilWhenNoDeclaration(t *testing.T) {
	if filter := BuildSkillFilter(ResolvedSkillSelection{}); filter != nil {
		t.Fatalf("expected nil filter for empty selection")
	}
	if filter := BuildSkillFilter(ResolvedSkillSelection{Allowlist: []string{"  "}}); filter != nil {
		t.Fatalf("expected nil filter for blank-only selection")
	}
}

func TestBuildSkillFilter_EnforcesSelection(t *testing.T) {
	filter := BuildSkillFilter(ResolvedSkillSelection{
		Allowlist: []string{"docs", "review"},
		Denylist:  []string{"review"},
	})
	if filter == nil {
		t.Fatalf("expected non-nil filter")
	}
	if !filter("docs") {
		t.Fatalf("docs must be allowed")
	}
	if filter("review") {
		t.Fatalf("review must be denied (deny wins)")
	}
	if filter("other") {
		t.Fatalf("other must be rejected by allowlist")
	}
}

func TestApplyMCPSelection_EmptySelectionKeepsInput(t *testing.T) {
	cfg := &mcpconfig.Config{MCPServers: map[string]mcpconfig.MCPConfig{
		"filesystem": {Type: "stdio", Command: "npx"},
	}}
	filtered, dropped := ApplyMCPSelection(cfg, ResolvedMCPSelection{})
	if filtered != cfg {
		t.Fatalf("empty selection must return the input config untouched")
	}
	if len(dropped) != 0 {
		t.Fatalf("expected no dropped servers, got %v", dropped)
	}
}

func TestApplyMCPSelection_FiltersWithoutMutatingInput(t *testing.T) {
	cfg := &mcpconfig.Config{MCPServers: map[string]mcpconfig.MCPConfig{
		"filesystem": {Type: "stdio", Command: "npx"},
		"git":        {Type: "stdio", Command: "npx"},
		"browser":    {Type: "stdio", Command: "npx"},
	}}
	filtered, dropped := ApplyMCPSelection(cfg, ResolvedMCPSelection{
		UseServers:     []string{"filesystem", "git"},
		ExcludeServers: []string{"git"},
	})
	if filtered == cfg {
		t.Fatalf("active selection must return a clone")
	}
	if _, ok := filtered.MCPServers["filesystem"]; !ok {
		t.Fatalf("filesystem should be kept")
	}
	if _, ok := filtered.MCPServers["git"]; ok {
		t.Fatalf("git should be excluded even though it is in use_servers")
	}
	if _, ok := filtered.MCPServers["browser"]; ok {
		t.Fatalf("browser should be dropped by use_servers")
	}
	if len(dropped) != 2 || dropped[0] != "browser" || dropped[1] != "git" {
		t.Fatalf("unexpected dropped list: %v", dropped)
	}
	// 输入快照必须保持不变，避免污染调用方共享的配置。
	if len(cfg.MCPServers) != 3 {
		t.Fatalf("input config was mutated: %v", cfg.MCPServers)
	}
}

func TestApplyMCPSelection_NilConfig(t *testing.T) {
	filtered, dropped := ApplyMCPSelection(nil, ResolvedMCPSelection{ExcludeServers: []string{"x"}})
	if filtered != nil || dropped != nil {
		t.Fatalf("expected nil result for nil config")
	}
}

func TestLoadMCPConfigSnapshot_EmptyPath(t *testing.T) {
	cfg, dropped, err := LoadMCPConfigSnapshot("  ", ResolvedMCPSelection{ExcludeServers: []string{"x"}})
	if err != nil || cfg != nil || dropped != nil {
		t.Fatalf("expected empty result, got cfg=%v dropped=%v err=%v", cfg, dropped, err)
	}
}

func TestLoadMCPConfigSnapshot_AppliesSelection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.yaml")
	writeSelectionTestFile(t, path, `mcpServers:
  filesystem:
    type: stdio
    command: npx
    enabled: true
  browser:
    type: stdio
    command: npx
    enabled: true
`)
	cfg, dropped, err := LoadMCPConfigSnapshot(path, ResolvedMCPSelection{ExcludeServers: []string{"browser"}})
	if err != nil {
		t.Fatalf("LoadMCPConfigSnapshot: %v", err)
	}
	if _, ok := cfg.MCPServers["filesystem"]; !ok {
		t.Fatalf("filesystem should survive filtering")
	}
	if _, ok := cfg.MCPServers["browser"]; ok {
		t.Fatalf("browser should be filtered out")
	}
	if len(dropped) != 1 || dropped[0] != "browser" {
		t.Fatalf("unexpected dropped list: %v", dropped)
	}
}

func TestComposeProfileSystemPrompt(t *testing.T) {
	cases := []struct {
		name    string
		host    string
		profile string
		mode    string
		want    string
	}{
		{"default replace", "host prompt", "profile prompt", "", "profile prompt"},
		{"explicit replace", "host prompt", "profile prompt", "replace", "profile prompt"},
		{"append joins", "host prompt", "profile prompt", "append", "host prompt\n\nprofile prompt"},
		{"append without host", "", "profile prompt", "append", "profile prompt"},
		{"append without profile", "host prompt", "", "append", "host prompt"},
		{"replace without profile", "host prompt", "", "replace", "host prompt"},
		{"replace without host", "", "profile prompt", "replace", "profile prompt"},
	}
	for _, tc := range cases {
		got, err := ComposeProfileSystemPrompt(tc.host, tc.profile, tc.mode)
		if err != nil {
			t.Fatalf("%s: unexpected error %v", tc.name, err)
		}
		if got != tc.want {
			t.Fatalf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestComposeProfileSystemPrompt_UnknownModeIsError(t *testing.T) {
	_, err := ComposeProfileSystemPrompt("host", "profile", "prepend")
	if !errors.Is(err, profilesys.ErrInvalidProfileSpec) {
		t.Fatalf("expected ErrInvalidProfileSpec, got %v", err)
	}
	if !strings.Contains(err.Error(), "prepend") {
		t.Fatalf("error should mention the offending mode: %v", err)
	}
}
