package profile

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadProfile_ParsesExtendedRuntimeMCPAndSkillsSpecs(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "profile.yaml"), `profile:
  name: dev
  default_agent: coder
runtime:
  overrides:
    max_steps: 20
skills:
  expose: profile
agents:
  coder: {}
`)

	spec, err := LoadProfile(root)
	if err != nil {
		t.Fatalf("LoadProfile: %v", err)
	}
	if spec.Profile.Name != "dev" {
		t.Fatalf("expected profile name dev, got %q", spec.Profile.Name)
	}
	if got := spec.Skills.Extras["expose"]; got != "profile" {
		t.Fatalf("expected skills expose=profile, got %#v", got)
	}
	// 夹具说明：`max_steps` 只用于验证 raw 解析，不在 D14 白名单内，因此本用例
	// 只调用 LoadProfile（不触发 ValidateProfileSpec）。
	overrides, ok := spec.Runtime.Overrides["max_steps"]
	if !ok {
		t.Fatal("expected runtime overrides.max_steps")
	}
	switch value := overrides.(type) {
	case int:
		if value != 20 {
			t.Fatalf("expected max_steps=20, got %d", value)
		}
	case uint64:
		if value != 20 {
			t.Fatalf("expected max_steps=20, got %d", value)
		}
	default:
		t.Fatalf("unexpected max_steps type/value: %#v", overrides)
	}
}

// TestValidateProfileSpec_RejectsRemovedMCPMergeStrategy 钉住 Q11/V12：字段已从
// spec 移除，但 MCPSpec 的 inline Extras 会吞掉未知键，所以必须由校验器显式报错，
// 否则它仍是一个"看起来能配、实际无效"的 dormant 陷阱（R9）。
func TestValidateProfileSpec_RejectsRemovedMCPMergeStrategy(t *testing.T) {
	spec := &ProfileSpec{MCP: MCPSpec{Extras: map[string]interface{}{"merge_strategy": "merge"}}}
	issues := ValidateProfileSpec(spec)
	if len(issues) != 1 {
		t.Fatalf("issues = %+v, want 1", issues)
	}
	if issues[0].Severity != ProfileSpecIssueError || issues[0].Path != "mcp.merge_strategy" {
		t.Fatalf("issue = %+v, want error at mcp.merge_strategy", issues[0])
	}
	if !strings.Contains(issues[0].Message, "use_servers") {
		t.Fatalf("message = %q, want pointer to use_servers/exclude_servers", issues[0].Message)
	}
}

// TestValidateProfileSpec_ReportsOverrideWhitelistIssues 保证 `profile validate`
// 与解析期共用同一份覆盖白名单结论（D14 双执行）。
func TestValidateProfileSpec_ReportsOverrideWhitelistIssues(t *testing.T) {
	spec := &ProfileSpec{Runtime: RuntimeSpec{Overrides: map[string]interface{}{
		"providers": map[string]interface{}{
			"items": map[string]interface{}{
				"openai": map[string]interface{}{"api_key": "sk-x"},
			},
		},
	}}}
	issues := ValidateProfileSpec(spec)
	if len(issues) != 1 {
		t.Fatalf("issues = %+v, want 1", issues)
	}
	if issues[0].Severity != ProfileSpecIssueError || !strings.Contains(issues[0].Path, "runtime.overrides") {
		t.Fatalf("issue = %+v, want error under runtime.overrides", issues[0])
	}
}
