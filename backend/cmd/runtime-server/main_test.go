package main

import (
	"path/filepath"
	"testing"
)

func TestResolvePathFromConfigFile(t *testing.T) {
	configFile := filepath.Join("backend", "configs", "runtime.yaml")
	resolved := resolvePathFromConfigFile(configFile, "../data/runtime/sessions")

	expected := filepath.Clean(filepath.Join("backend", "data", "runtime", "sessions"))
	if resolved != expected {
		t.Fatalf("expected %q, got %q", expected, resolved)
	}
}
