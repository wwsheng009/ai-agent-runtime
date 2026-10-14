package statepath

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestCanonicalStatePathHonorsOverride(t *testing.T) {
	override := filepath.Join(t.TempDir(), "custom", "state.json")
	t.Setenv(EnvOverride, override)
	got, err := CanonicalStatePath()
	if err != nil {
		t.Fatalf("CanonicalStatePath: %v", err)
	}
	if !strings.EqualFold(filepath.Clean(got), filepath.Clean(override)) {
		t.Fatalf("override ignored: got %q, want %q", got, override)
	}
}

func TestCanonicalStatePathAnchorsUserHome(t *testing.T) {
	t.Setenv(EnvOverride, "")
	got, err := CanonicalStatePath()
	if err != nil {
		t.Fatalf("CanonicalStatePath: %v", err)
	}
	if filepath.Base(got) != FileName {
		t.Fatalf("unexpected file name: %q", got)
	}
	// 不断言完整路径（CI 家目录各异），只断言锚定在用户目录的 .aicli/data/connectors 下。
	if filepath.Base(filepath.Dir(filepath.Dir(filepath.Dir(got)))) != ".aicli" {
		t.Fatalf("state path not anchored under .aicli: %q", got)
	}
}

func TestLegacyCandidatesExcludesCanonical(t *testing.T) {
	override := filepath.Join(t.TempDir(), "canonical", "state.json")
	t.Setenv(EnvOverride, override)
	configPath := filepath.Join(t.TempDir(), "project", "config.yaml")
	candidates := LegacyCandidates(configPath)

	for _, candidate := range candidates {
		if strings.EqualFold(filepath.Clean(candidate.Path), filepath.Clean(override)) {
			t.Fatalf("canonical path leaked into legacy candidates: %v", candidates)
		}
	}

	// configPath 同级候选必须出现（旧 aicli 落点）。
	want := filepath.Clean(filepath.Join(filepath.Dir(configPath), "data", "connectors", FileName))
	found := false
	for _, candidate := range candidates {
		if strings.EqualFold(filepath.Clean(candidate.Path), want) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("configPath sibling candidate missing: %v", candidates)
	}
}
