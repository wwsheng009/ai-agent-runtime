package toolcore

import (
	"encoding/json"
	"fmt"
	"testing"
)

// benchSchema approximates the shape of a real tool schema (a few scalar
// properties plus an enum), so the list benchmark includes canonicalization
// cost rather than an empty object.
var benchSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "path": {"type": "string", "description": "workspace path"},
    "limit": {"type": "integer", "minimum": 1},
    "mode": {"type": "string", "enum": ["read", "write"]}
  },
  "required": ["path"],
  "additionalProperties": false
}`)

func benchmarkRegistry(b *testing.B, tools int) *Registry {
	b.Helper()
	registry := NewRegistry()
	for i := 0; i < tools; i++ {
		def := Definition{
			Name:         fmt.Sprintf("bench_tool_%03d", i),
			Description:  "benchmark tool with a representative schema",
			Version:      "1.0.0",
			InputSchema:  benchSchema,
			Capabilities: []Capability{CapReadFS},
		}
		if err := registry.Register(&testHandler{def: def}); err != nil {
			b.Fatalf("register %s: %v", def.Name, err)
		}
	}
	return registry
}

// BenchmarkRegistryDefinitions256 maps to the design's "tools/list at 256
// tools" gate: Definitions is the schema snapshot a list surface serializes.
func BenchmarkRegistryDefinitions256(b *testing.B) {
	registry := benchmarkRegistry(b, 256)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = registry.Definitions()
	}
}

// BenchmarkRegistryList256 measures the filtered handler snapshot used by
// MCP/builtin model surfaces.
func BenchmarkRegistryList256(b *testing.B) {
	registry := benchmarkRegistry(b, 256)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = registry.List(Filter{})
	}
}
