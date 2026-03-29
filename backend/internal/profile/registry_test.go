package profile

import (
	"path/filepath"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

func TestRegistry_ResolveRegisteredName(t *testing.T) {
	registry := NewRegistry("")
	root := t.TempDir()
	if err := registry.Register("dev", root); err != nil {
		t.Fatalf("Register: %v", err)
	}
	resolved, err := registry.Resolve("dev")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolved != root {
		t.Fatalf("expected %q, got %q", root, resolved)
	}
}

func TestRegistry_ResolveUsesDefaultRootForNames(t *testing.T) {
	defaultRoot := t.TempDir()
	registry := NewRegistry(defaultRoot)
	resolved, err := registry.Resolve("dev")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	expected := filepath.Join(defaultRoot, "dev")
	if resolved != expected {
		t.Fatalf("expected %q, got %q", expected, resolved)
	}
}

func TestRegistry_ResolveAcceptsExplicitPath(t *testing.T) {
	root := t.TempDir()
	registry := NewRegistry(t.TempDir())
	resolved, err := registry.Resolve(root)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolved != root {
		t.Fatalf("expected %q, got %q", root, resolved)
	}
}

func TestNewRegistryFromProfilesConfig_RegistersNamedRoots(t *testing.T) {
	root := t.TempDir()
	registry := NewRegistryFromProfilesConfig(&agentconfig.ProfilesConfig{
		Root: "profiles",
		Items: map[string]agentconfig.ProfileConfig{
			"dev": {Root: root},
		},
	})
	resolved, err := registry.Resolve("dev")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolved != root {
		t.Fatalf("expected %q, got %q", root, resolved)
	}
}

func TestResolveRef_UsesRegistryAndResolve(t *testing.T) {
	profilesRoot := t.TempDir()
	root := filepath.Join(profilesRoot, "dev")
	writeTestFile(t, filepath.Join(root, "profile.yaml"), `profile:
  default_agent: coder
agents:
  coder: {}
`)
	mustMkdir(t, filepath.Join(root, "agents", "coder"))

	registry := NewRegistry(profilesRoot)
	resolved, err := ResolveRef(registry, "dev", ResolveOptions{})
	if err != nil {
		t.Fatalf("ResolveRef: %v", err)
	}
	if resolved.ProfileRoot != root {
		t.Fatalf("expected %q, got %q", root, resolved.ProfileRoot)
	}
}
