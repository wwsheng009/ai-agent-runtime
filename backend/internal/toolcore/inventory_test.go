package toolcore_test

import (
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolregistry"
)

func TestCoreInventoryMatchesToolRegistry(t *testing.T) {
	for _, tool := range toolcore.V1CoreInventory() {
		entry, ok := toolregistry.Lookup(tool.Name)
		if !ok {
			t.Fatalf("core inventory tool %q is absent from toolregistry", tool.Name)
		}
		for _, alias := range tool.Aliases {
			if !containsString(entry.Aliases, alias) {
				t.Fatalf("tool %q alias %q is absent from toolregistry aliases %v", tool.Name, alias, entry.Aliases)
			}
		}
	}
}

func TestLocalOnlyToolsAreNotInCoreNames(t *testing.T) {
	core := make(map[string]bool)
	for _, name := range toolcore.CoreToolNames() {
		core[name] = true
	}
	for _, tool := range toolcore.V1CoreInventory() {
		if tool.LocalOnly && core[tool.Name] {
			t.Fatalf("local-only tool %q is exposed as core", tool.Name)
		}
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
