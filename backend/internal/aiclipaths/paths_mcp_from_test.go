package aiclipaths

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// isolateMCPHome 把用户主目录指向临时目录，避免真实的 ~/.aicli/mcp.yaml 影响断言。
func isolateMCPHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
}

func writeMCPFile(t *testing.T, path string, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestResolveMCPConfigPathDetailedFromAnchorsProjectAtBase(t *testing.T) {
	isolateMCPHome(t)
	base := t.TempDir()
	project := filepath.Join(base, ".aicli", "mcp.yaml")
	writeMCPFile(t, project, "mcpServers: {}\n")

	resolution := ResolveMCPConfigPathDetailedFrom(base, "")

	if resolution.Source != "project" {
		t.Fatalf("expected project source, got %q (%s)", resolution.Source, resolution.Path)
	}
	if filepath.Clean(resolution.Path) != filepath.Clean(project) {
		t.Fatalf("expected %s, got %s", project, resolution.Path)
	}

	wantLocal, err := LocalMCPConfigPath(base)
	if err != nil {
		t.Fatalf("LocalMCPConfigPath: %v", err)
	}
	foundLocal := false
	for _, candidate := range resolution.Candidates {
		if candidate.Source != "local" {
			continue
		}
		foundLocal = true
		clean := filepath.Clean(candidate.Path)
		wantJSON := filepath.Clean(filepath.Join(filepath.Dir(wantLocal), DefaultMCPConfigJSONFileName))
		if clean != filepath.Clean(wantLocal) && clean != wantJSON {
			t.Fatalf("local candidate anchored wrong: got %s want %s (或同目录 mcp.json)", candidate.Path, wantLocal)
		}
	}
	if !foundLocal {
		t.Fatal("expected a local candidate in the candidate list")
	}
}

func TestResolveMCPConfigPathDetailedFromWalksUpFromBase(t *testing.T) {
	isolateMCPHome(t)
	root := t.TempDir()
	base := filepath.Join(root, "workspace", "nested")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatalf("mkdir base: %v", err)
	}
	upward := filepath.Join(root, "configs", "mcp.yaml")
	writeMCPFile(t, upward, "mcpServers: {}\n")

	resolution := ResolveMCPConfigPathDetailedFrom(base, "")

	if resolution.Source != "upward" {
		t.Fatalf("expected upward source, got %q (%s)", resolution.Source, resolution.Path)
	}
	if filepath.Clean(resolution.Path) != filepath.Clean(upward) {
		t.Fatalf("expected %s, got %s", upward, resolution.Path)
	}

	// default 候选必须锚定在 base（显式基准目录时不允许相对进程 cwd）。
	defaultPath := filepath.Join(base, DefaultMCPConfigRelativePath)
	defaultJSONPath := filepath.Join(base, DefaultMCPConfigJSONRelativePath)
	for _, candidate := range resolution.Candidates {
		if candidate.Source != "default" {
			continue
		}
		clean := filepath.Clean(candidate.Path)
		if clean != filepath.Clean(defaultPath) && clean != filepath.Clean(defaultJSONPath) {
			t.Fatalf("default candidate anchored wrong: got %s want %s/%s", candidate.Path, defaultPath, defaultJSONPath)
		}
	}
}

func TestResolveMCPConfigPathDetailedFromKeepsExplicitOverride(t *testing.T) {
	isolateMCPHome(t)
	base := t.TempDir()
	override := filepath.Join(t.TempDir(), "custom", "servers.yaml")
	writeMCPFile(t, override, "mcpServers: {}\n")

	resolution := ResolveMCPConfigPathDetailedFrom(base, override)

	if resolution.Source != "explicit" {
		t.Fatalf("expected explicit source, got %q (%s)", resolution.Source, resolution.Path)
	}
	if filepath.Clean(resolution.Path) != filepath.Clean(override) {
		t.Fatalf("expected override %s, got %s", override, resolution.Path)
	}
}

func TestResolveMCPConfigPathDetailedFromDoesNotFallBackIntoBaseWhenEmpty(t *testing.T) {
	isolateMCPHome(t)
	base := t.TempDir()

	resolution := ResolveMCPConfigPathDetailedFrom(base, "")

	// 空工作区：不得命中 base 下的 project/upward/default 候选。
	if resolution.Path != "" {
		cleanPath := filepath.Clean(resolution.Path)
		if strings.HasPrefix(strings.ToLower(cleanPath), strings.ToLower(filepath.Clean(base))) {
			t.Fatalf("empty workspace must not resolve into base, got %q (%s)", resolution.Path, resolution.Source)
		}
	}
}

// 只有 mcp.json 时按同一发现链自动命中（project 层）。
func TestResolveMCPConfigPathDetailedFromDiscoversJSONSibling(t *testing.T) {
	isolateMCPHome(t)
	base := t.TempDir()
	jsonPath := filepath.Join(base, ".aicli", "mcp.json")
	writeMCPFile(t, jsonPath, `{"mcpServers":{"example":{"command":"npx"}}}`)

	resolution := ResolveMCPConfigPathDetailedFrom(base, "")

	if resolution.Source != "project" || filepath.Clean(resolution.Path) != filepath.Clean(jsonPath) {
		t.Fatalf("resolution = %+v, want project/%s", resolution, jsonPath)
	}
	// 候选清单同时枚举同层 yaml 与 json；候选按「高→低」，yaml 必须在前。
	yamlIndex, jsonIndex := -1, -1
	for index, candidate := range resolution.Candidates {
		if candidate.Source != "project" {
			continue
		}
		switch filepath.Base(candidate.Path) {
		case DefaultMCPConfigFileName:
			yamlIndex = index
		case DefaultMCPConfigJSONFileName:
			jsonIndex = index
		}
	}
	if yamlIndex < 0 || jsonIndex < 0 || yamlIndex > jsonIndex {
		t.Fatalf("project candidates 顺序错误: yaml=%d json=%d (%+v)", yamlIndex, jsonIndex, resolution.Candidates)
	}
}

// 同目录 mcp.yaml 与 mcp.json 同时存在：生效路径取 mcp.yaml（yaml 优先）。
func TestResolveMCPConfigPathDetailedFromPrefersYAMLWhenBothExist(t *testing.T) {
	isolateMCPHome(t)
	base := t.TempDir()
	yamlPath := filepath.Join(base, ".aicli", "mcp.yaml")
	jsonPath := filepath.Join(base, ".aicli", "mcp.json")
	writeMCPFile(t, yamlPath, "mcpServers: {}\n")
	writeMCPFile(t, jsonPath, `{"mcpServers":{}}`)

	resolution := ResolveMCPConfigPathDetailedFrom(base, "")

	if resolution.Source != "project" || filepath.Clean(resolution.Path) != filepath.Clean(yamlPath) {
		t.Fatalf("resolution = %+v, want project/%s", resolution, yamlPath)
	}
}

// 向上搜索同样发现 configs/mcp.json；命中 yaml 时同目录 json 一并作为候选。
func TestResolveMCPConfigPathDetailedFromUpwardPair(t *testing.T) {
	isolateMCPHome(t)
	root := t.TempDir()
	base := filepath.Join(root, "workspace", "nested")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatalf("mkdir base: %v", err)
	}
	upwardYAML := filepath.Join(root, "configs", "mcp.yaml")
	upwardJSON := filepath.Join(root, "configs", "mcp.json")
	writeMCPFile(t, upwardYAML, "mcpServers: {}\n")
	writeMCPFile(t, upwardJSON, `{"mcpServers":{}}`)

	resolution := ResolveMCPConfigPathDetailedFrom(base, "")

	if resolution.Source != "upward" || filepath.Clean(resolution.Path) != filepath.Clean(upwardYAML) {
		t.Fatalf("resolution = %+v, want upward/%s", resolution, upwardYAML)
	}
	upwardJSONFound := false
	for _, candidate := range resolution.Candidates {
		if candidate.Source == "upward" && filepath.Clean(candidate.Path) == filepath.Clean(upwardJSON) {
			upwardJSONFound = true
		}
	}
	if !upwardJSONFound {
		t.Fatalf("命中 yaml 时同目录 mcp.json 也应进入候选: %+v", resolution.Candidates)
	}

	// 只有 json 时向上搜索同样命中。
	if err := os.Remove(upwardYAML); err != nil {
		t.Fatalf("remove yaml: %v", err)
	}
	jsonOnly := ResolveMCPConfigPathDetailedFrom(base, "")
	if jsonOnly.Source != "upward" || filepath.Clean(jsonOnly.Path) != filepath.Clean(upwardJSON) {
		t.Fatalf("json-only upward resolution = %+v, want upward/%s", jsonOnly, upwardJSON)
	}
}

// local 层（~/.aicli/projects/<slug>/）的 mcp.json 同样被自动发现。
func TestResolveMCPConfigPathDetailedFromDiscoversLocalJSON(t *testing.T) {
	isolateMCPHome(t)
	base := t.TempDir()
	localYAML, err := LocalMCPConfigPath(base)
	if err != nil {
		t.Fatalf("LocalMCPConfigPath: %v", err)
	}
	localJSON := filepath.Join(filepath.Dir(localYAML), DefaultMCPConfigJSONFileName)
	writeMCPFile(t, localJSON, `{"mcpServers":{}}`)

	resolution := ResolveMCPConfigPathDetailedFrom(base, "")

	if resolution.Source != "local" || filepath.Clean(resolution.Path) != filepath.Clean(localJSON) {
		t.Fatalf("resolution = %+v, want local/%s", resolution, localJSON)
	}
}

// mcp.json 一侧的约定写法（裸文件名 / configs/mcp.json）继续走发现链，
// 不会被当成真实覆盖把分层合并降级成单文件加载。
func TestResolveMCPConfigPathDetailedFromJSONConventionIsNotOverride(t *testing.T) {
	isolateMCPHome(t)
	base := t.TempDir()
	projectJSON := filepath.Join(base, ".aicli", "mcp.json")
	writeMCPFile(t, projectJSON, `{"mcpServers":{}}`)

	for _, convention := range []string{"mcp.json", DefaultMCPConfigJSONRelativePath} {
		resolution := ResolveMCPConfigPathDetailedFrom(base, convention)
		if resolution.Source != "project" || filepath.Clean(resolution.Path) != filepath.Clean(projectJSON) {
			t.Fatalf("convention %q resolution = %+v, want project/%s", convention, resolution, projectJSON)
		}
	}
}
