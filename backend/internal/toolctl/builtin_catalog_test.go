package toolctl

import (
	"testing"

	runtimetools "github.com/wwsheng009/ai-agent-runtime/internal/tools"
)

// TestConstructToolsFromCatalogCoversEveryUnconditionalCanonical pins the
// Phase 6a contract: toolctl must build from internal/tools' single catalog,
// not from a second hand-maintained map. Conditional rows (for example the
// image tool without a configured provider) and rows whose runtime state the
// surface does not bind (artifact_read) are allowed to be absent.
func TestConstructToolsFromCatalogCoversEveryUnconditionalCanonical(t *testing.T) {
	constructed := constructToolsFromCatalog("ops")
	if len(constructed) == 0 {
		t.Fatal("ops profile constructed no tools")
	}

	catalogCanonicals := map[string]bool{}
	unconditional := map[string]bool{}
	for _, spec := range runtimetools.BuiltinToolkitCatalog() {
		catalogCanonicals[spec.Canonical] = true
		if spec.Condition == nil && !toolctlUnsupportedCatalogTool(spec.Canonical) {
			unconditional[spec.Canonical] = true
		}
	}

	for name := range constructed {
		if !catalogCanonicals[name] {
			t.Fatalf("toolctl constructed %q, which is absent from the shared catalog", name)
		}
	}
	for name := range unconditional {
		if constructed[name] == nil {
			t.Fatalf("toolctl did not construct unconditional catalog row %q", name)
		}
	}
	for name := range catalogCanonicals {
		if !toolctlUnsupportedCatalogTool(name) {
			continue
		}
		if _, ok := constructed[name]; ok {
			t.Fatalf("toolctl constructed %q without binding its required runtime state", name)
		}
	}
	for _, aliasInstance := range []string{"bash", "execute_shell_command", "read", "rg"} {
		if _, ok := constructed[aliasInstance]; ok {
			t.Fatalf("toolctl constructed alias instance %q; aliases must be projected from the canonical registry entry", aliasInstance)
		}
	}
}

// TestConstructedCanonicalToolsPreserveRegistryAliases proves that
// deduplicating catalog rows by canonical name does not break compatibility
// spellings: the toolcore registry still resolves them to the canonical
// handler.
func TestConstructedCanonicalToolsPreserveRegistryAliases(t *testing.T) {
	cli := &CLI{}
	surface, err := cli.buildSurface(&commonOptions{
		workspace:  t.TempDir(),
		profile:    "ops",
		allowWrite: true,
		allowCmd:   true,
	})
	if err != nil {
		t.Fatalf("buildSurface: %v", err)
	}

	aliases := map[string]string{
		"read":                  "view",
		"rg":                    "grep",
		"bash":                  "shell",
		"execute_shell_command": "shell",
	}
	for alias, canonical := range aliases {
		handler, ok := surface.registry.Resolve(alias)
		if !ok {
			t.Fatalf("alias %q did not resolve", alias)
		}
		if got := handler.Definition().Name; got != canonical {
			t.Fatalf("alias %q resolved to %q, want %q", alias, got, canonical)
		}
		if !surface.visible[canonical] {
			t.Fatalf("canonical tool %q (alias %q) is not visible", canonical, alias)
		}
	}
}
