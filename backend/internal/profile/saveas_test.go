package profile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// §23 G1/D24 + A9：save-as 产物只写差分 section（不得出现空 section 或全量快照），
// 且必须能被同一 loader/校验器接受（"CLI 说合法、解析器不认"是第二套方言）。
func TestRenderSaveAsProfileWritesDiffSectionsOnly(t *testing.T) {
	raw, err := RenderSaveAsProfile("my-review", "从会话固化", "default", SaveAsSurface{
		ToolDenylist: []string{"shell", "write_file", "shell"},
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	text := string(raw)
	for _, banned := range []string{"runtime:", "providers:", "prompts:", "allowlist:", "skills:", "mcp:"} {
		if strings.Contains(text, banned) {
			t.Fatalf("产物不应包含 %q（无差分即不落 section）:\n%s", banned, text)
		}
	}
	// agent 只做空声明：解析器要求 profile 能解析出 agent（ErrAgentUnresolved），
	// 但空声明不携带基线内容。
	if !strings.Contains(text, "agents:\n    default: {}") {
		t.Fatalf("产物应内联空声明 agent（解析器要求），got:\n%s", text)
	}
	if !strings.Contains(text, "# 未包含 prompt") {
		t.Fatalf("产物头部应明示 prompt 未包含（D24）:\n%s", text)
	}

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "profile.yaml"), raw, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	spec, err := LoadProfile(root)
	if err != nil {
		t.Fatalf("产物无法被 LoadProfile 解析: %v", err)
	}
	if spec.Profile.Name != "my-review" {
		t.Fatalf("声明名 = %q，want my-review", spec.Profile.Name)
	}
	if spec.Profile.DefaultAgent != "default" {
		t.Fatalf("default_agent = %q，want default", spec.Profile.DefaultAgent)
	}
	if _, ok := spec.Agents["default"]; !ok {
		t.Fatalf("agents 应内联声明 default: %+v", spec.Agents)
	}
	if got := strings.Join(spec.Tools.Denylist, ","); got != "shell,write_file" {
		t.Fatalf("denylist = %q，want shell,write_file（去重 + 升序）", got)
	}
	if len(spec.Tools.Allowlist) != 0 || spec.Tools.ReadOnly != nil {
		t.Fatalf("无差分的字段不应落盘: %+v", spec.Tools)
	}
	for _, issue := range ValidateProfileSpec(spec) {
		if issue.Severity == ProfileSpecIssueError {
			t.Fatalf("产物未通过同一校验器: %+v", issue)
		}
	}

	// 纯函数：同一 surface 恒得同一字节。
	again, err := RenderSaveAsProfile("my-review", "从会话固化", "default", SaveAsSurface{
		ToolDenylist: []string{"write_file", "shell"},
	})
	if err != nil {
		t.Fatalf("render again: %v", err)
	}
	if string(again) != text {
		t.Fatalf("同一差分面应产出同一字节:\n--- first ---\n%s\n--- second ---\n%s", text, again)
	}
}

// A9：空差分面必须报错，绝不产出空 profile。
func TestRenderSaveAsProfileRejectsEmptySurface(t *testing.T) {
	if _, err := RenderSaveAsProfile("empty", "", "default", SaveAsSurface{}); err == nil {
		t.Fatal("空差分面必须报错")
	}
	if _, err := RenderSaveAsProfile("  ", "", "default", SaveAsSurface{ReadOnly: true}); err == nil {
		t.Fatal("空名称必须报错")
	}
	if _, err := RenderSaveAsProfile("noagent", "", "  ", SaveAsSurface{ReadOnly: true}); err == nil {
		t.Fatal("空 agent id 必须报错（产物会解析不出 agent）")
	}
}

// D24 固化面：allowlist 形态 / read_only / skills / mcp 各自按需落 section。
func TestRenderSaveAsProfileSectionForms(t *testing.T) {
	raw, err := RenderSaveAsProfile("frozen", "", "coder", SaveAsSurface{
		ToolAllowlist:     []string{"grep", "view"},
		ReadOnly:          true,
		SkillAllowlist:    []string{"docs"},
		MCPExcludeServers: []string{"remote"},
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "profile.yaml"), raw, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	spec, err := LoadProfile(root)
	if err != nil {
		t.Fatalf("LoadProfile: %v", err)
	}
	if got := strings.Join(spec.Tools.Allowlist, ","); got != "grep,view" {
		t.Fatalf("allowlist = %q，want grep,view", got)
	}
	if spec.Tools.ReadOnly == nil || !*spec.Tools.ReadOnly {
		t.Fatalf("read_only 应为 true: %+v", spec.Tools)
	}
	if got := strings.Join(spec.Skills.Allowlist, ","); got != "docs" {
		t.Fatalf("skills.allowlist = %q，want docs", got)
	}
	if got := strings.Join(spec.MCP.ExcludeServers, ","); got != "remote" {
		t.Fatalf("mcp.exclude_servers = %q，want remote", got)
	}
	if strings.Contains(string(raw), "read_only: false") {
		t.Fatalf("read_only=false 是默认值，不应落盘（差分原则）:\n%s", raw)
	}
}
