package agentconfig

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"gopkg.in/yaml.v3"
)

func readSkillsTestDocument(t *testing.T, path string) map[string]interface{} {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	document := map[string]interface{}{}
	if err := yaml.Unmarshal(raw, &document); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	return document
}

func decodeSkillsRuntime(t *testing.T, path string) *SkillsRuntimeConfig {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	return cfg.SkillsRuntime
}

func TestUpdateSkillsDisabledSkills_WritesNormalizedSnakeKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	document := "skills_runtime:\n  enabled: true\ndisabledSkills:\n  - legacy\nproviders:\n  default_provider: gateway\n"
	if err := os.WriteFile(path, []byte(document), 0o644); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	if err := UpdateSkillsDisabledSkills(path, []string{" alpha ", "Beta", "ALPHA", ""}); err != nil {
		t.Fatalf("UpdateSkillsDisabledSkills: %v", err)
	}

	runtime := decodeSkillsRuntime(t, path)
	if runtime == nil {
		t.Fatal("skills_runtime missing after update")
	}
	want := []string{"alpha", "Beta"}
	if got := runtime.DisabledSkillNames(); !reflect.DeepEqual(got, want) {
		t.Fatalf("DisabledSkillNames() = %#v, want %#v", got, want)
	}
	// 兼容 key 必须被清理，避免两个 key 语义分叉。
	if len(runtime.DisabledSkillsCompat) != 0 {
		t.Fatalf("disabledSkills compat key must be cleared, got %#v", runtime.DisabledSkillsCompat)
	}
	// 其它 section 不被改写。
	decoded := readSkillsTestDocument(t, path)
	providers, ok := decoded["providers"].(map[string]interface{})
	if !ok || providers["default_provider"] != "gateway" {
		t.Fatalf("unrelated sections must be preserved: %#v", decoded["providers"])
	}
}

func TestUpdateSkillsDisabledSkills_EmptyClearsKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("skills_runtime:\n  enabled: true\n  disabled_skills:\n    - alpha\n"), 0o644); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	if err := UpdateSkillsDisabledSkills(path, nil); err != nil {
		t.Fatalf("UpdateSkillsDisabledSkills: %v", err)
	}

	runtime := decodeSkillsRuntime(t, path)
	if runtime == nil {
		t.Fatal("skills_runtime must stay present")
	}
	if got := runtime.DisabledSkillNames(); got != nil {
		t.Fatalf("DisabledSkillNames() = %#v, want nil after clear", got)
	}
	decoded := readSkillsTestDocument(t, path)
	skillsRuntime, _ := decoded["skills_runtime"].(map[string]interface{})
	if _, exists := skillsRuntime["disabled_skills"]; exists {
		t.Fatal("disabled_skills key must be removed when the list is empty")
	}
}

func TestUpdateSkillsDisabledSkills_ClearingOnMissingSectionKeepsFileClean(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("providers:\n  default_provider: gateway\n"), 0o644); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	if err := UpdateSkillsDisabledSkills(path, []string{"  "}); err != nil {
		t.Fatalf("UpdateSkillsDisabledSkills: %v", err)
	}

	decoded := readSkillsTestDocument(t, path)
	if _, exists := decoded["skills_runtime"]; exists {
		t.Fatalf("empty update must not create an empty skills_runtime node: %#v", decoded["skills_runtime"])
	}
}

func TestUpdateSkillsDisabledSkills_RequiresPath(t *testing.T) {
	if err := UpdateSkillsDisabledSkills("  ", []string{"alpha"}); err == nil {
		t.Fatal("empty config path must be rejected")
	}
}
