package remotetools

import (
	"sort"
	"strings"
	"testing"
)

func toolNames(cfg *Config) []string {
	names := cfg.enabledTools()
	sort.Strings(names)
	return names
}

func TestDefaultProfileAllExposesFullCoreSurface(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Profile != ProfileAll {
		t.Fatalf("DefaultConfig profile = %q, want all", cfg.Profile)
	}
	cfg.Workspace = t.TempDir()
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	got := toolNames(cfg)
	want := []string{
		"append_write", "apply_patch", "artifact_read", "edit", "glob", "grep", "job_cancel", "job_list", "job_output", "job_start", "ls", "shell", "view", "write",
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("profile all tools = %v, want %v", got, want)
	}
}

func TestProfileAllHonorsReadOnlyOverride(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Workspace = t.TempDir()
	cfg.Profile = ProfileAll
	cfg.ReadOnly = true
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	got := toolNames(cfg)
	want := []string{"artifact_read", "glob", "grep", "ls", "view"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("readOnly profile all tools = %v, want %v", got, want)
	}
}

func TestProfileAllHonorsExplicitToolAllowlist(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Workspace = t.TempDir()
	cfg.Profile = ProfileAll
	cfg.Tools = []string{"view", "grep"}
	got := toolNames(cfg)
	if strings.Join(got, ",") != "grep,view" {
		t.Fatalf("narrowed profile all tools = %v, want grep,view", got)
	}
}

func TestProfileAllAddsJobToolsWhenStoreConfigured(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Workspace = t.TempDir()
	cfg.Profile = ProfileAll
	cfg.Jobs.Root = t.TempDir()
	got := toolNames(cfg)
	for _, name := range jobTools {
		found := false
		for _, candidate := range got {
			if candidate == name {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("profile all with jobs.root lacks %q: %v", name, got)
		}
	}
}

func TestNormalizeEmptyProfileDefaultsToAll(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Profile = ""
	cfg.Workspace = t.TempDir()
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if cfg.Profile != ProfileAll {
		t.Fatalf("empty profile normalized to %q, want all", cfg.Profile)
	}
}
