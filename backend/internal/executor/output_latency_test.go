package executor

import (
	"os/exec"
	"testing"
)

func TestWithEnvOverrideAddsMissingValue(t *testing.T) {
	env := withEnvOverride([]string{"PATH=/bin"}, "PYTHONUNBUFFERED", "1")

	if !containsEnvEntry(env, "PYTHONUNBUFFERED=1") {
		t.Fatalf("expected PYTHONUNBUFFERED override, got %#v", env)
	}
	if !containsEnvEntry(env, "PATH=/bin") {
		t.Fatalf("expected existing env to be preserved, got %#v", env)
	}
}

func TestWithEnvOverrideReplacesExistingValue(t *testing.T) {
	env := withEnvOverride([]string{"pythonunbuffered=0", "PATH=/bin"}, "PYTHONUNBUFFERED", "1")

	if !containsEnvEntry(env, "PYTHONUNBUFFERED=1") {
		t.Fatalf("expected case-insensitive env replacement, got %#v", env)
	}
	if containsEnvEntry(env, "pythonunbuffered=0") {
		t.Fatalf("expected old env value to be replaced, got %#v", env)
	}
}

func TestPrepareCommandForLowLatencyOutputSetsPythonUnbuffered(t *testing.T) {
	cmd := exec.Command("sh", "-c", "echo ok")
	cmd.Env = []string{"PATH=/bin"}

	PrepareCommandForLowLatencyOutput(cmd)

	if !containsEnvEntry(cmd.Env, "PYTHONUNBUFFERED=1") {
		t.Fatalf("expected command env to include PYTHONUNBUFFERED=1, got %#v", cmd.Env)
	}
}

func containsEnvEntry(env []string, entry string) bool {
	for _, candidate := range env {
		if candidate == entry {
			return true
		}
	}
	return false
}
