package main

import (
	"os"
	"path/filepath"
	"testing"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/aiclipaths"
)

func isolateMCPResolutionHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return home
}

func TestResolveRuntimeMCPConfigResolutionPrefersProjectFile(t *testing.T) {
	isolateMCPResolutionHome(t)

	projectDir := t.TempDir()
	projectConfig := filepath.Join(projectDir, ".aicli", "mcp.yaml")
	if err := os.MkdirAll(filepath.Dir(projectConfig), 0o755); err != nil {
		t.Fatalf("create project mcp dir: %v", err)
	}
	if err := os.WriteFile(projectConfig, []byte("mcpServers: {}\n"), 0o644); err != nil {
		t.Fatalf("write project mcp config: %v", err)
	}
	chdirTest(t, projectDir)

	cfg := &config.Config{AICLI: &config.AICLIConfig{MCP: &config.AICLIMCPConfig{ConfigFile: "configs/mcp.yaml"}}}
	resolution := resolveRuntimeMCPConfigResolution(cfg)
	if resolution.Path != projectConfig || resolution.Source != "project" {
		t.Fatalf("resolution = %+v, want path=%q source=project", resolution, projectConfig)
	}
	if got := resolveRuntimeMCPConfigPath(cfg); got != projectConfig {
		t.Fatalf("resolveRuntimeMCPConfigPath = %q, want %q", got, projectConfig)
	}
}

func TestResolveRuntimeMCPConfigResolutionFallsBackToUserLevel(t *testing.T) {
	home := isolateMCPResolutionHome(t)
	chdirTest(t, t.TempDir())

	// resolver 会从 cwd 逐级向上搜索（docs/aicli/install.md：每级先 .aicli/mcp.yaml，
	// 再 configs/mcp.yaml）。开发机上 %TEMP% 位于用户主目录之下时，祖先链上真实的
	// ~/.aicli/mcp.yaml 会先命中，此时不存在“任何候选都不存在”的前提 —— 这是环境
	// 事实而非缺陷（干净环境如 CI 仍会执行下面的断言），跳过而不是误报失败。
	if probe := aiclipaths.ResolveMCPConfigPath(aiclipaths.DefaultMCPConfigRelativePath); filepath.ToSlash(probe) != aiclipaths.DefaultMCPConfigRelativePath {
		t.Skipf("环境提供祖先 mcp.yaml：解析到 %s（向上搜索按文档命中），跳过 user-fallback 断言", probe)
	}

	cfg := &config.Config{AICLI: &config.AICLIConfig{MCP: &config.AICLIMCPConfig{ConfigFile: "configs/mcp.yaml"}}}
	resolution := resolveRuntimeMCPConfigResolution(cfg)
	want := filepath.Join(home, ".aicli", "mcp.yaml")
	if resolution.Path != want || resolution.Source != "user-fallback" {
		t.Fatalf("resolution = %+v, want path=%q source=user-fallback", resolution, want)
	}
}

// user-fallback 规则本身：约定默认值（相对 configs/mcp.yaml）不存在时改落用户级，
// 已存在的路径与其它显式路径原样返回。该规则不依赖向上搜索，任何环境都可确定断言。
func TestApplyMCPUserFallbackRewritesConventionDefaultOnly(t *testing.T) {
	home := isolateMCPResolutionHome(t)
	chdirTest(t, t.TempDir())
	want := filepath.Join(home, ".aicli", "mcp.yaml")

	got := applyMCPUserFallback(aiclipaths.MCPConfigResolution{
		Path:   aiclipaths.DefaultMCPConfigRelativePath,
		Source: "explicit",
	})
	if got.Path != want || got.Source != "user-fallback" {
		t.Fatalf("convention default = %+v, want path=%q source=user-fallback", got, want)
	}

	existing := filepath.Join(t.TempDir(), "mcp.yaml")
	if err := os.WriteFile(existing, []byte("mcpServers: {}\n"), 0o644); err != nil {
		t.Fatalf("write existing mcp config: %v", err)
	}
	if got := applyMCPUserFallback(aiclipaths.MCPConfigResolution{Path: existing, Source: "explicit"}); got.Path != existing || got.Source != "explicit" {
		t.Fatalf("existing path = %+v, want path=%q source=explicit", got, existing)
	}

	override := filepath.Join("custom", "mcp.yaml")
	if got := applyMCPUserFallback(aiclipaths.MCPConfigResolution{Path: override, Source: "explicit"}); got.Path != override || got.Source != "explicit" {
		t.Fatalf("non-convention relative path = %+v, want path=%q source=explicit", got, override)
	}

	if got := applyMCPUserFallback(aiclipaths.MCPConfigResolution{}); got.Path != "" || got.Source != "" {
		t.Fatalf("empty resolution = %+v, want empty", got)
	}
}

func TestResolveRuntimeMCPConfigResolutionEmptyConfig(t *testing.T) {
	if got := resolveRuntimeMCPConfigResolution(nil); got.Path != "" {
		t.Fatalf("nil cfg resolution = %+v, want empty", got)
	}

	// 未设置 config_file（含 MCP 节整体缺失）是“发现”语义：工作区
	// ./.aicli/mcp.yaml 直接生效，不需要额外写 aicli.mcp.config_file。
	projectDir := t.TempDir()
	projectConfig := filepath.Join(projectDir, ".aicli", "mcp.yaml")
	if err := os.MkdirAll(filepath.Dir(projectConfig), 0o755); err != nil {
		t.Fatalf("create project mcp dir: %v", err)
	}
	if err := os.WriteFile(projectConfig, []byte("mcpServers: {}\n"), 0o644); err != nil {
		t.Fatalf("write project mcp config: %v", err)
	}
	chdirTest(t, projectDir)

	cases := map[string]*config.Config{
		"nil mcp block":     {AICLI: &config.AICLIConfig{}},
		"empty config_file": {AICLI: &config.AICLIConfig{MCP: &config.AICLIMCPConfig{}}},
	}
	for name, cfg := range cases {
		resolution := resolveRuntimeMCPConfigResolution(cfg)
		if resolution.Path != projectConfig || resolution.Source != "project" {
			t.Fatalf("%s: resolution = %+v, want path=%q source=project", name, resolution, projectConfig)
		}
		if got := resolveRuntimeMCPConfigPath(cfg); got != projectConfig {
			t.Fatalf("%s: resolveRuntimeMCPConfigPath = %q, want %q", name, got, projectConfig)
		}
	}
}
