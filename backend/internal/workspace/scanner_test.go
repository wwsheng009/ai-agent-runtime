package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetectLanguage(t *testing.T) {
	tests := []struct {
		filePath string
		want     Language
	}{
		{"test.go", LangGo},
		{"test.py", LangPython},
		{"test.js", LangJS},
		{"test.ts", LangTS},
		{"test.tsx", LangTS},
		{"test.jsx", LangJS},
		{"test.java", LangJava},
		{"test.rs", LangRust},
		{"test.cpp", LangCPP},
		{"test.c", LangC},
		{"test.h", LangC},
		{"test.hpp", LangCPP},
		{"test.unknown", LangUnknown},
		{"test.txt", LangUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.filePath, func(t *testing.T) {
			got := DetectLanguage(tt.filePath)
			if got != tt.want {
				t.Errorf("DetectLanguage(%q) = %v, want %v", tt.filePath, got, tt.want)
			}
		})
	}
}

func TestDetectLanguage_CaseInsensitive(t *testing.T) {
	tests := []struct {
		filePath string
		want     Language
	}{
		{"Test.GO", LangGo},
		{"TEST.PY", LangPython},
		{"TeSt.Js", LangJS},
	}

	for _, tt := range tests {
		t.Run(tt.filePath, func(t *testing.T) {
			got := DetectLanguage(tt.filePath)
			if got != tt.want {
				t.Errorf("DetectLanguage(%q) = %v, want %v", tt.filePath, got, tt.want)
			}
		})
	}
}

func TestNewScanner(t *testing.T) {
	scanner := NewScanner(nil)

	if scanner == nil {
		t.Fatal("NewScanner() returned nil")
	}

	if scanner.config == nil {
		t.Error("scanner.config should not be nil")
	}

	if scanner.config.MaxFileSize != 10*1024*1024 {
		t.Errorf("MaxFileSize = %v, want %v", scanner.config.MaxFileSize, 10*1024*1024)
	}
}

func TestNewScanner_WithConfig(t *testing.T) {
	config := &WorkspaceConfig{
		MaxFileSize:     5 * 1024 * 1024,
		MaxChunkSize:    1000,
		ChunkOverlap:    100,
		IncludePatterns: []string{"*.go"},
		ExcludePatterns: []string{"*_test.go"},
	}

	scanner := NewScanner(config)

	if scanner.config.MaxFileSize != 5*1024*1024 {
		t.Errorf("MaxFileSize = %v, want %v", scanner.config.MaxFileSize, 5*1024*1024)
	}

	if scanner.config.MaxChunkSize != 1000 {
		t.Errorf("MaxChunkSize = %v, want 1000", scanner.config.MaxChunkSize)
	}
}

func TestDefaultWorkspaceConfig(t *testing.T) {
	config := DefaultWorkspaceConfig()

	if config == nil {
		t.Fatal("DefaultWorkspaceConfig() returned nil")
	}

	if config.MaxFileSize != 10*1024*1024 {
		t.Errorf("MaxFileSize = %v, want %v", config.MaxFileSize, 10*1024*1024)
	}

	if config.MaxChunkSize != 5000 {
		t.Errorf("MaxChunkSize = %v, want 5000", config.MaxChunkSize)
	}

	if config.ChunkOverlap != 200 {
		t.Errorf("ChunkOverlap = %v, want 200", config.ChunkOverlap)
	}
}

func TestGoSymbolExtractor(t *testing.T) {
	extractor := &GoSymbolExtractor{}

	tests := []struct {
		name      string
		content   string
		file      string
		wantCount int
	}{
		{
			name:      "empty content",
			content:   "",
			file:      "test.go",
			wantCount: 0,
		},
		{
			name: "functions",
			content: `func testFunc() {}
func anotherFunc(n int) int {
	return n
}`,
			file:      "test.go",
			wantCount: 2,
		},
		{
			name: "structs",
			content: `type MyStruct struct {
	field string
}`,
			file:      "test.go",
			wantCount: 1,
		},
		{
			name: "interfaces",
			content: `type MyInterface interface {
	Method()
}`,
			file:      "test.go",
			wantCount: 1,
		},
		{
			name: "constants and variables",
			content: `const MyConst = "value"
var MyVar int`,
			file:      "test.go",
			wantCount: 2,
		},
		{
			name: "mixed",
			content: `const Pi = 3.14
type Circle struct {
	radius float32
}

func Area(c Circle) float32 {
	return Pi * c.radius * c.radius
}`,
			file:      "test.go",
			wantCount: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			symbols, err := extractor.ExtractSymbols(tt.content, tt.file)
			if err != nil {
				t.Fatalf("ExtractSymbols() returned error: %v", err)
			}

			if len(symbols) != tt.wantCount {
				t.Errorf("Extracted %v symbols, want %v", len(symbols), tt.wantCount)
			}

			// Verify symbol types
			for _, sym := range symbols {
				if sym.Language != LangGo {
					t.Errorf("Symbol language = %v, want %v", sym.Language, LangGo)
				}
				if sym.File != tt.file {
					t.Errorf("Symbol file = %v, want %v", sym.File, tt.file)
				}
			}
		})
	}
}

func TestPythonSymbolExtractor(t *testing.T) {
	extractor := &PythonSymbolExtractor{}

	content := `class MyClass:
	def __init__(self):
		self.value = 0

	def method(self):
		pass

def standalone_function():
	pass
`

	symbols, err := extractor.ExtractSymbols(content, "test.py")
	if err != nil {
		t.Fatalf("ExtractSymbols() returned error: %v", err)
	}

	if len(symbols) < 2 {
		t.Errorf("Extracted %v symbols, want at least 2", len(symbols))
	}

	// Check that we got a class
	foundClass := false
	for _, sym := range symbols {
		if sym.Type == SymbolClass {
			foundClass = true
		}
	}

	if !foundClass {
		t.Error("Expected to find at least one class symbol")
	}
}

func TestJSSymbolExtractor(t *testing.T) {
	extractor := &JSSymbolExtractor{}

	content := `class MyComponent {
	constructor() {
		this.name = "test";
	}

	render() {
		return "hello";
	}
}

function helperFunction() {
	return "world";
}
`

	symbols, err := extractor.ExtractSymbols(content, "test.js")
	if err != nil {
		t.Fatalf("ExtractSymbols() returned error: %v", err)
	}

	if len(symbols) < 1 {
		t.Errorf("Extracted %v symbols, want at least 1", len(symbols))
	}

	// Verify language
	for _, sym := range symbols {
		if sym.Language != LangJS {
			t.Errorf("Symbol language = %v, want %v", sym.Language, LangJS)
		}
	}
}

func TestScanner_ScanFile(t *testing.T) {
	// Create a temporary directory
	tmpDir := t.TempDir()

	// Create a test file
	testFile := filepath.Join(tmpDir, "test.go")
	content := `package main

import "fmt"

func main() {
	fmt.Println("Hello, World!")
}

type MyStruct struct {
	field string
}
`

	err := os.WriteFile(testFile, []byte(content), 0644)
	if err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Scan the file
	scanner := NewScanner(nil)
	result, err := scanner.scanFile(testFile)
	if err != nil {
		t.Fatalf("scanFile() returned error: %v", err)
	}

	if result == nil {
		t.Fatal("scanFile() returned nil result")
	}

	if len(result.Files) != 1 {
		t.Errorf("Files count = %v, want 1", len(result.Files))
	}

	if result.FileCount != 1 {
		t.Errorf("FileCount = %v, want 1", result.FileCount)
	}

	if len(result.CodeChunks) == 0 {
		t.Error("No code chunks extracted")
	}
}

func TestScanner_ScanDirectory(t *testing.T) {
	// Create a temporary directory
	tmpDir := t.TempDir()

	// Create test files
	testFiles := []struct {
		name    string
		content string
	}{
		{"main.go", `package main
func main() {}`},
		{"utils.py", `def test(): pass`},
		{"README.md", `# Test Project`}, // Should be ignored
	}

	for _, tf := range testFiles {
		path := filepath.Join(tmpDir, tf.name)
		err := os.WriteFile(path, []byte(tf.content), 0644)
		if err != nil {
			t.Fatalf("Failed to create test file %s: %v", tf.name, err)
		}
	}

	// Scan the directory
	scanner := NewScanner(nil)
	result, err := scanner.Scan(tmpDir)
	if err != nil {
		t.Fatalf("Scan() returned error: %v", err)
	}

	if result == nil {
		t.Fatal("Scan() returned nil result")
	}

	// Should have 2 files (go and py, not md)
	if len(result.Files) != 2 {
		t.Logf("Scanned files: %v", result.Files)
		t.Errorf("Files count = %v, want 2", len(result.Files))
	}
}

func TestScanner_ShouldIgnore(t *testing.T) {
	scanner := NewScanner(nil)

	tests := []struct {
		path    string
		ignored bool
	}{
		{".git/config", true},
		{".idea/workspace.xml", true},
		{"node_modules/package/index.js", true},
		{"vendor/github.com/user/repo/file.go", true},
		{"dist/bundle.js", true},
		{"src/main.go", false},
		{"utils/helper.py", false},
		{"test_file_test.go", false}, // Test files are ignored in default patterns, not by this method
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := scanner.shouldIgnore(tt.path)
			if got != tt.ignored {
				t.Errorf("shouldIgnore(%q) = %v, want %v", tt.path, got, tt.ignored)
			}
		})
	}
}

func TestScanner_IsCodeFile(t *testing.T) {
	scanner := NewScanner(nil)

	tests := []struct {
		file   string
		isCode bool
	}{
		{"test.go", true},
		{"test.py", true},
		{"test.js", true},
		{"test.ts", true},
		{"test.java", true},
		{"test.rs", true},
		{"test.cpp", true},
		{"test.c", true},
		{"test.h", true},
		{"test.txt", false},
		{"test.md", false},
		{"Makefile", false},
	}

	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			got := scanner.isCodeFile(tt.file)
			if got != tt.isCode {
				t.Errorf("isCodeFile(%q) = %v, want %v", tt.file, got, tt.isCode)
			}
		})
	}
}

func TestScanner_RegisterSymbolExtractor(t *testing.T) {
	scanner := NewScanner(nil)

	// Verify default extractors exist
	if _, ok := scanner.symbolExtractors[LangGo]; !ok {
		t.Error("Go symbol extractor should be registered by default")
	}

	if _, ok := scanner.symbolExtractors[LangPython]; !ok {
		t.Error("Python symbol extractor should be registered by default")
	}

	// Add a custom extractor
	scanner.RegisterSymbolExtractor(LangUnknown, &GoSymbolExtractor{})

	if _, ok := scanner.symbolExtractors[LangUnknown]; !ok {
		t.Error("Custom symbol extractor should be registered")
	}
}

func TestChunkContent(t *testing.T) {
	scanner := NewScanner(nil)

	content := "line 1\nline 2\nline 3\nline 4\nline 5\nline 6\nline 7\nline 8\nline 9\nline 10"

	chunks := scanner.chunkContent(content, "test.go", LangGo, []Symbol{})

	if len(chunks) == 0 {
		t.Fatal("chunkContent() produced no chunks")
	}

	// Verify chunks have correct properties
	for i, chunk := range chunks {
		if chunk.FilePath != "test.go" {
			t.Errorf("Chunk %v FilePath = %v, want test.go", i, chunk.FilePath)
		}

		if chunk.Language != LangGo {
			t.Errorf("Chunk %v Language = %v, want %v", i, chunk.Language, LangGo)
		}

		if chunk.StartLine < 1 {
			t.Errorf("Chunk %v StartLine = %v, want >= 1", i, chunk.StartLine)
		}

		if chunk.EndLine < chunk.StartLine {
			t.Errorf("Chunk %v EndLine < StartLine", i)
		}
	}
}

func TestChunkContent_SmallFileProducesSingleChunk(t *testing.T) {
	scanner := NewScanner(nil)

	chunks := scanner.chunkContent("line 1\nline 2", "small.go", LangGo, nil)

	if len(chunks) != 1 {
		t.Fatalf("chunkContent() chunk count = %v, want 1", len(chunks))
	}

	if chunks[0].StartLine != 1 || chunks[0].EndLine != 2 {
		t.Fatalf("chunk lines = %d-%d, want 1-2", chunks[0].StartLine, chunks[0].EndLine)
	}
}

func TestScanner_WithCustomPatterns(t *testing.T) {
	config := &WorkspaceConfig{
		MaxFileSize:     10 * 1024 * 1024,
		MaxChunkSize:    5000,
		ChunkOverlap:    200,
		IncludePatterns: []string{"*.go"}, // Only include Go files
		ExcludePatterns: []string{"*_test.go"},
	}

	scanner := NewScanner(config)

	// Create a temporary directory
	tmpDir := t.TempDir()

	// Create test files
	_ = os.WriteFile(filepath.Join(tmpDir, "test.go"), []byte("package main"), 0644)
	_ = os.WriteFile(filepath.Join(tmpDir, "test_test.go"), []byte("package main_test"), 0644)
	_ = os.WriteFile(filepath.Join(tmpDir, "test.py"), []byte("# python"), 0644)

	// Scan with custom patterns
	result, err := scanner.Scan(tmpDir)
	if err != nil {
		t.Fatalf("Scan() returned error: %v", err)
	}

	// Should only have test.go (not test.py or test_test.go)
	if len(result.Files) != 1 {
		t.Logf("Scanned files: %v", result.Files)
		t.Errorf("Files count = %v, want 1", len(result.Files))
	}
}
