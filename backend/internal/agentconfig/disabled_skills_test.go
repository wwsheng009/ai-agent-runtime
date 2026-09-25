package agentconfig

import (
	"reflect"
	"testing"

	"gopkg.in/yaml.v3"
)

// SK-6：disabled_skills 支持 snake/camel 两种写法，归一化去空白、去重、保序。
func TestSkillsRuntimeConfig_DisabledSkillNames(t *testing.T) {
	cfg := &SkillsRuntimeConfig{
		DisabledSkills:       []string{" alpha ", "", "Beta"},
		DisabledSkillsCompat: []string{"ALPHA", "gamma"},
	}
	got := cfg.DisabledSkillNames()
	want := []string{"alpha", "Beta", "gamma"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DisabledSkillNames() = %#v, want %#v", got, want)
	}
}

func TestSkillsRuntimeConfig_DisabledSkillNames_EmptyAndNil(t *testing.T) {
	if got := (*SkillsRuntimeConfig)(nil).DisabledSkillNames(); got != nil {
		t.Fatalf("nil config must return nil, got %#v", got)
	}
	cfg := &SkillsRuntimeConfig{DisabledSkills: []string{" ", ""}}
	if got := cfg.DisabledSkillNames(); got != nil {
		t.Fatalf("blank names must be dropped, got %#v", got)
	}
}

// YAML 解析：snake 与 camel 两种 key 都要能被读取并合并。
func TestSkillsRuntimeConfig_DisabledSkillsYAMLCompat(t *testing.T) {
	document := []byte("skills_runtime:\n  enabled: true\n  disabled_skills:\n    - snake-skill\n  disabledSkills:\n    - camel-skill\n")
	var cfg Config
	if err := yaml.Unmarshal(document, &cfg); err != nil {
		t.Fatalf("yaml unmarshal: %v", err)
	}
	if cfg.SkillsRuntime == nil {
		t.Fatal("skills_runtime missing")
	}
	got := cfg.SkillsRuntime.DisabledSkillNames()
	if len(got) != 2 || got[0] != "snake-skill" || got[1] != "camel-skill" {
		t.Fatalf("DisabledSkillNames() = %#v", got)
	}
}
