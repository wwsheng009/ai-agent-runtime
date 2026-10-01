package tools

import (
	"reflect"
	"testing"
)

// TestRGToolIsGrepAlias pins the alias contract: `rg` is the same tool under a
// shell-familiar name (same schema, same execution entry point), so a tool call
// named `rg` can never degrade into "tool not found".
func TestRGToolIsGrepAlias(t *testing.T) {
	grep := NewGrepTool()
	rg := NewRGTool()

	if grep.Name() != "grep" {
		t.Fatalf("NewGrepTool name = %q, want grep", grep.Name())
	}
	if rg.Name() != "rg" {
		t.Fatalf("NewRGTool name = %q, want rg", rg.Name())
	}
	if !reflect.DeepEqual(grep.Parameters(), rg.Parameters()) {
		t.Fatal("rg alias must expose the same parameter schema as grep")
	}
	if rg.Description() == "" || rg.Description() == grep.Description() {
		t.Fatalf("rg alias must carry its own alias description, got %q", rg.Description())
	}
}
