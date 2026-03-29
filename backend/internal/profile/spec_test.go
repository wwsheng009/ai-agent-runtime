package profile

import (
	"path/filepath"
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
mcp:
  merge_strategy: merge
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
	if spec.MCP.MergeStrategy != "merge" {
		t.Fatalf("expected merge strategy, got %q", spec.MCP.MergeStrategy)
	}
	if got := spec.Skills.Extras["expose"]; got != "profile" {
		t.Fatalf("expected skills expose=profile, got %#v", got)
	}
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
