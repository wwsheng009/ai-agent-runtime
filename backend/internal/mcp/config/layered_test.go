package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfigFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func stdioServer(command string) string {
	return "    type: stdio\n    command: " + command + "\n    timeout: 30s\n"
}

// 用户级提供基础项，项目级同名覆盖并追加增量项。
func TestLoadLayeredMergesByNameWithHigherPriorityWinning(t *testing.T) {
	root := t.TempDir()
	userPath := writeConfigFile(t, root, "home/.aicli/mcp.yaml", `mcpServers:
  base:
    type: stdio
    command: base-cmd
    timeout: 30s
  shared:
    type: stdio
    command: from-user
    timeout: 30s
global:
  connectTimeout: 5s
`)
	projectPath := writeConfigFile(t, root, "project/.aicli/mcp.yaml", `mcpServers:
  shared:
    type: streamable
    url: https://project.example.com/mcp
    timeout: 30s
  added:
    type: stdio
    command: added-cmd
    timeout: 30s
global:
  connectTimeout: 9s
`)

	result, err := LoadLayered([]SourceFile{
		{Path: userPath, Source: "user"},
		{Path: projectPath, Source: "project"},
	})
	if err != nil {
		t.Fatalf("LoadLayered: %v", err)
	}
	if len(result.Config.MCPServers) != 3 {
		t.Fatalf("合并后 server 数 = %d, want 3 (%v)", len(result.Config.MCPServers), keysOf(result.Config.MCPServers))
	}
	// 项目级同名覆盖：整体取自项目文件（url 语义不能被用户级 stdio 污染）。
	shared := result.Config.MCPServers["shared"]
	if shared.URL != "https://project.example.com/mcp" || shared.Command != "" {
		t.Fatalf("同名 server 应整体覆盖: %#v", shared)
	}
	if base := result.Config.MCPServers["base"].Command; base != "base-cmd" {
		t.Fatalf("低优先级基础项应保留: %#v", result.Config.MCPServers["base"])
	}
	if added := result.Config.MCPServers["added"].Command; added != "added-cmd" {
		t.Fatalf("高优先级增量项应被合并: %#v", result.Config.MCPServers["added"])
	}
	// global 取最高优先级文件。
	if result.Config.Global.ConnectTimeout.Duration.String() != "9s" {
		t.Fatalf("global 应取 primary: %v", result.Config.Global.ConnectTimeout.Duration)
	}

	origin, ok := result.Origin("shared")
	if !ok || origin.Source != "project" || origin.Path != projectPath {
		t.Fatalf("shared 来源 = %#v", origin)
	}
	if len(origin.Shadowed) != 1 || origin.Shadowed[0].Path != userPath || origin.Shadowed[0].Source != "user" {
		t.Fatalf("shadow 关系未记录: %#v", origin.Shadowed)
	}
	if baseOrigin, _ := result.Origin("base"); baseOrigin.Source != "user" {
		t.Fatalf("base 来源 = %#v", baseOrigin)
	}
	if result.Primary.Path != projectPath || result.Primary.Source != "project" {
		t.Fatalf("primary = %#v", result.Primary)
	}
}

// 低优先级文件损坏：跳过并告警，不影响高层配置；高优先级文件损坏：直接报错。
func TestLoadLayeredMalformedFiles(t *testing.T) {
	root := t.TempDir()
	brokenPath := writeConfigFile(t, root, "user/mcp.yaml", "{not-json")
	goodPath := writeConfigFile(t, root, "project/mcp.yaml", "mcpServers:\n  ok:\n"+stdioServer("ok-cmd"))

	result, err := LoadLayered([]SourceFile{
		{Path: brokenPath, Source: "user"},
		{Path: goodPath, Source: "project"},
	})
	if err != nil {
		t.Fatalf("低优先级损坏不应阻断加载: %v", err)
	}
	if _, ok := result.Config.MCPServers["ok"]; !ok {
		t.Fatalf("高优先级配置应生效: %#v", result.Config.MCPServers)
	}
	if len(result.Warnings) == 0 || !strings.Contains(result.Warnings[0], "user") {
		t.Fatalf("损坏文件应记入 Warnings: %#v", result.Warnings)
	}

	// 反过来：高优先级损坏必须报错，避免「配置整体消失」而无提示。
	if _, err := LoadLayered([]SourceFile{
		{Path: goodPath, Source: "user"},
		{Path: brokenPath, Source: "project"},
	}); err == nil {
		t.Fatal("primary 文件损坏应报错")
	}

	// 低优先级文件缺失只跳过；显式文件缺失报错。
	if _, err := LoadLayered([]SourceFile{
		{Path: filepath.Join(root, "nope.yaml"), Source: "user"},
		{Path: goodPath, Source: "project"},
	}); err != nil {
		t.Fatalf("缺失的候选应被跳过: %v", err)
	}
	_, err = LoadLayered([]SourceFile{{Path: filepath.Join(root, "nope.yaml"), Source: "explicit"}})
	if err == nil || !strings.Contains(err.Error(), "nope.yaml") {
		t.Fatalf("显式文件缺失应给出路径: %v", err)
	}
}

func TestLoadLayeredNoFiles(t *testing.T) {
	_, err := LoadLayered([]SourceFile{{Path: filepath.Join(t.TempDir(), "missing.yaml"), Source: "user"}})
	if !errors.Is(err, ErrNoConfigFiles) {
		t.Fatalf("err = %v, want ErrNoConfigFiles", err)
	}
}

// 发现链顺序：低→高 = upward/default < user < project；显式覆盖只认单文件。
func TestDiscoverSourcesOrderAndExplicitOverride(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOMEDRIVE", "")
	t.Setenv("HOMEPATH", "")

	project := t.TempDir()
	t.Chdir(project)

	userPath := writeConfigFile(t, home, ".aicli/mcp.yaml", "mcpServers: {}\n")
	projectPath := writeConfigFile(t, project, ".aicli/mcp.yaml", "mcpServers: {}\n")
	upwardPath := writeConfigFile(t, project, "configs/mcp.yaml", "mcpServers: {}\n")

	sources := DiscoverSources("")
	if len(sources) < 3 {
		t.Fatalf("候选来源太少: %#v", sources)
	}
	// 低→高：configs/mcp.yaml 最先，project 最后。
	lowest := filepath.Clean(sources[0].Path)
	if lowest != filepath.Clean(upwardPath) && lowest != filepath.Clean("configs/mcp.yaml") {
		t.Fatalf("首个来源应为 upward/default: %#v", sources)
	}
	if sources[0].Source != "upward" && sources[0].Source != "default" {
		t.Fatalf("首个来源层级 = %q", sources[0].Source)
	}
	// local 层（~/.aicli/projects/<slug>/mcp.yaml）优先级最高，但本用例未创建该文件：
	// 它应仍出现在链尾且 Exists=false，避免「写了 local 却不生效」。
	last := sources[len(sources)-1]
	if last.Source != "local" || last.Exists {
		t.Fatalf("链尾应为未创建的 local 层: %#v", sources)
	}
	order := map[string]int{}
	for index, source := range sources {
		order[source.Source] = index
	}
	if !(order["user"] < order["project"]) {
		t.Fatalf("user 必须低于 project: %#v", sources)
	}
	if !(order["project"] < order["local"]) {
		t.Fatalf("local 必须高于 project: %#v", sources)
	}
	projectLayer := filepath.Clean(projectPath)
	for _, source := range sources {
		if filepath.Clean(source.Path) == projectLayer && !source.Exists {
			t.Fatalf("project 文件存在，应标记 Exists: %#v", source)
		}
	}
	for _, source := range sources {
		if filepath.Clean(source.Path) == filepath.Clean(userPath) && !source.Exists {
			t.Fatalf("存在的用户级文件应标记 Exists: %#v", source)
		}
	}

	// 显式覆盖：不参与合并。
	override := filepath.Join(project, "custom", "mcp.yaml")
	explicit := DiscoverSources(override)
	if len(explicit) != 1 || explicit[0].Source != "explicit" || explicit[0].Exists {
		t.Fatalf("显式覆盖来源 = %#v", explicit)
	}
	if _, err := LoadEffective(override); err == nil {
		t.Fatal("显式覆盖缺失时应报错")
	}
}

func keysOf(servers map[string]MCPConfig) []string {
	out := make([]string, 0, len(servers))
	for name := range servers {
		out = append(out, name)
	}
	return out
}
