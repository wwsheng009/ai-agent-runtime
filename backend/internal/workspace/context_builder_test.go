package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSymbolIndex_Search(t *testing.T) {
	tmpDir := t.TempDir()
	file := filepath.Join(tmpDir, "main.go")
	content := `package demo

type User struct{}

func LoadUser() User {
	return User{}
}
`
	if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
		t.Fatalf("write file failed: %v", err)
	}

	scanner := NewScanner(nil)
	scan, err := scanner.Scan(tmpDir)
	if err != nil {
		t.Fatalf("scan failed: %v", err)
	}

	index := NewSymbolIndex(scan)
	results := index.Search("load", 5)
	if len(results) == 0 {
		t.Fatal("expected symbol search results")
	}
	if results[0].Name != "LoadUser" {
		t.Fatalf("expected LoadUser, got %s", results[0].Name)
	}
}

func TestReferenceGraph_References(t *testing.T) {
	tmpDir := t.TempDir()
	fileA := filepath.Join(tmpDir, "user.go")
	fileB := filepath.Join(tmpDir, "use.go")
	if err := os.WriteFile(fileA, []byte(`package demo

type User struct{}

func LoadUser() User {
	return User{}
}
`), 0o644); err != nil {
		t.Fatalf("write fileA failed: %v", err)
	}
	if err := os.WriteFile(fileB, []byte(`package demo

func UseUser() {
	_ = LoadUser()
}
`), 0o644); err != nil {
		t.Fatalf("write fileB failed: %v", err)
	}

	scanner := NewScanner(nil)
	scan, err := scanner.Scan(tmpDir)
	if err != nil {
		t.Fatalf("scan failed: %v", err)
	}

	graph := NewReferenceGraph(scan)
	refs := graph.References("LoadUser", 10)
	if len(refs) < 2 {
		t.Fatalf("expected declaration + usage references, got %v", refs)
	}
}

func TestReferenceGraph_UsesIdentifierBoundaries(t *testing.T) {
	tmpDir := t.TempDir()
	fileA := filepath.Join(tmpDir, "symbol.go")
	fileB := filepath.Join(tmpDir, "use.go")
	if err := os.WriteFile(fileA, []byte(`package demo

func Load() {}
`), 0o644); err != nil {
		t.Fatalf("write fileA failed: %v", err)
	}
	if err := os.WriteFile(fileB, []byte(`package demo

func Use() {
	preload()
	Load()
	reload_value := 1
	_ = reload_value
}
`), 0o644); err != nil {
		t.Fatalf("write fileB failed: %v", err)
	}

	scanner := NewScanner(nil)
	scan, err := scanner.Scan(tmpDir)
	if err != nil {
		t.Fatalf("scan failed: %v", err)
	}

	graph := NewReferenceGraph(scan)
	refs := graph.References("Load", 10)
	if len(refs) != 2 {
		t.Fatalf("expected declaration + one exact usage reference, got %v", refs)
	}
}

func TestReferenceGraph_DeduplicatesSameLineMatches(t *testing.T) {
	tmpDir := t.TempDir()
	fileA := filepath.Join(tmpDir, "user.go")
	fileB := filepath.Join(tmpDir, "use.go")
	if err := os.WriteFile(fileA, []byte(`package demo

func LoadUser() {}
`), 0o644); err != nil {
		t.Fatalf("write fileA failed: %v", err)
	}
	if err := os.WriteFile(fileB, []byte(`package demo

func Use() {
	LoadUser(); LoadUser()
}
`), 0o644); err != nil {
		t.Fatalf("write fileB failed: %v", err)
	}

	scanner := NewScanner(nil)
	scan, err := scanner.Scan(tmpDir)
	if err != nil {
		t.Fatalf("scan failed: %v", err)
	}

	graph := NewReferenceGraph(scan)
	refs := graph.References("LoadUser", 10)
	if len(refs) != 2 {
		t.Fatalf("expected declaration + one deduplicated usage reference, got %v", refs)
	}
}

func TestContextBuilder_Build(t *testing.T) {
	tmpDir := t.TempDir()
	fileA := filepath.Join(tmpDir, "user.go")
	fileB := filepath.Join(tmpDir, "service.go")
	if err := os.WriteFile(fileA, []byte(`package demo

type User struct{}

func LoadUser() User {
	return User{}
}
`), 0o644); err != nil {
		t.Fatalf("write fileA failed: %v", err)
	}
	if err := os.WriteFile(fileB, []byte(`package demo

func Handle() {
	_ = LoadUser()
}
`), 0o644); err != nil {
		t.Fatalf("write fileB failed: %v", err)
	}

	scanner := NewScanner(nil)
	scan, err := scanner.Scan(tmpDir)
	if err != nil {
		t.Fatalf("scan failed: %v", err)
	}

	builder := NewContextBuilder(scan, &ContextBuilderConfig{
		MaxFiles:      2,
		MaxSymbols:    3,
		MaxReferences: 2,
		MaxChunks:     3,
	})
	ctx := builder.Build("load user")
	if len(ctx.Files) == 0 {
		t.Fatal("expected files in workspace context")
	}
	if len(ctx.Symbols) == 0 {
		t.Fatal("expected symbols in workspace context")
	}
	if ctx.Summary == "" {
		t.Fatal("expected non-empty summary")
	}
	if len(ctx.References["LoadUser"]) == 0 {
		t.Fatal("expected references for LoadUser")
	}
}
