package tools

import (
	"context"
	"testing"
)

// TestManagerDoesNotRegisterRGToolAlias pins the removal of the `rg` toolkit
// alias: `rg` must remain a shell command only, so a model tool call named
// `rg` cannot shadow shell rg searches.
func TestManagerDoesNotRegisterRGToolAlias(t *testing.T) {
	manager := NewDefaultManager(nil)

	for _, descriptor := range manager.ListTools() {
		if descriptor.Name == "rg" {
			t.Fatal("rg must not be registered as a toolkit tool")
		}
	}

	if _, err := manager.Execute(context.Background(), "rg", map[string]interface{}{
		"pattern": "needle",
	}); err == nil {
		t.Fatal("expected rg tool execution to fail after alias removal")
	}
}
