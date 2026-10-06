package ui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// TestSynchronizedFramesToggledOnlyByLegacySurface freezes the DEC 2026
// framing toggle to the legacy FixedBottomSurface path. renderengine writes the
// brackets directly to os.Stdout (registered in the writer inventory:
// renderengine/terminal_lock.go), so a new caller — especially a unified
// session — would re-open a second byte channel beside the session writer.
//
// Allowed non-test callers: fixed_bottom_surface.go (legacy Enable/Disable)
// and terminal_write_lock.go (the ui-package forwarding wrapper itself).
func TestSynchronizedFramesToggledOnlyByLegacySurface(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	uiDir := filepath.Dir(currentFile)
	var paths []string
	err := filepath.WalkDir(uiDir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path != uiDir && (entry.Name() == "testdata" || strings.HasPrefix(entry.Name(), ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		paths = append(paths, path)
		return nil
	})
	if err != nil {
		t.Fatalf("walk ui sources: %v", err)
	}
	sort.Strings(paths)

	fset := token.NewFileSet()
	callers := map[string][]int{}
	for _, path := range paths {
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", filepath.Base(path), err)
		}
		rel, err := filepath.Rel(uiDir, path)
		if err != nil {
			t.Fatalf("rel %s: %v", path, err)
		}
		key := filepath.ToSlash(rel)
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			name := ""
			switch fun := call.Fun.(type) {
			case *ast.Ident:
				name = fun.Name
			case *ast.SelectorExpr:
				name = fun.Sel.Name
			}
			if name == "SetTerminalSynchronizedFrames" {
				callers[key] = append(callers[key], fset.Position(call.Pos()).Line)
			}
			return true
		})
	}

	for key, lines := range callers {
		if key != "fixed_bottom_surface.go" && key != "terminal_write_lock.go" {
			t.Fatalf("SetTerminalSynchronizedFrames called from %s:%v; only the legacy FixedBottomSurface (and the forwarding wrapper) may toggle DEC 2026 framing", key, lines)
		}
	}
	if got := callers["fixed_bottom_surface.go"]; len(got) != 2 {
		t.Fatalf("legacy surface toggle sites = %v, want exactly 2 (Enable/Disable)", got)
	}
}
