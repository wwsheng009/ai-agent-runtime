package toolcore

import (
	"context"
	"encoding/json"
	"testing"
)

type testHandler struct {
	def Definition
}

func (h *testHandler) Definition() Definition { return h.def }
func (h *testHandler) Execute(context.Context, *Call) (*Result, error) {
	return &Result{Success: true, OutputKind: KindText, Content: "ok"}, nil
}

func testDefinition(name string, aliases ...string) Definition {
	return Definition{
		Name:        name,
		Aliases:     aliases,
		Description: "test",
		Version:     "1.0.0",
		InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
	}
}

func TestRegistryResolvesAliasAndSchema(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(&testHandler{def: testDefinition("view", "read")}); err != nil {
		t.Fatal(err)
	}
	if _, ok := registry.Resolve("read"); !ok {
		t.Fatal("alias was not resolved")
	}
	if _, ok := registry.Schema("read"); !ok {
		t.Fatal("alias schema was not resolved")
	}
	if _, ok := registry.Resolve("missing"); ok {
		t.Fatal("unknown tool resolved")
	}
}

func TestRegistryRejectsAliasCollision(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(&testHandler{def: testDefinition("view", "read")}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(&testHandler{def: testDefinition("read")}); err == nil {
		t.Fatal("expected alias collision")
	}
}

func TestRegistryListFilter(t *testing.T) {
	registry := NewRegistry()
	read := testDefinition("view")
	read.Capabilities = []Capability{CapReadFS}
	write := testDefinition("write")
	write.Capabilities = []Capability{CapWriteFS}
	if err := registry.Register(&testHandler{def: read}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(&testHandler{def: write}); err != nil {
		t.Fatal(err)
	}
	list := registry.List(Filter{Capabilities: []Capability{CapReadFS}})
	if len(list) != 1 || list[0].Definition().Name != "view" {
		t.Fatalf("unexpected list: %+v", list)
	}
}
