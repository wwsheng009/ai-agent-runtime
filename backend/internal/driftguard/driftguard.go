// Package driftguard holds the static guards that keep the tool layer from
// drifting back into per-backend forks.
//
// Why a package instead of a script under scripts/: this repository already
// enforces structural invariants through ordinary tests - the Makefile notes that
// `make test` runs cmd/contractgen's TestGeneratedFileIsUpToDate, which is what
// makes the generated frontend types trustworthy. A check nobody has to remember
// to run is not a check.
//
// The counting is AST-based rather than regex-based on purpose. A grep for `os.`
// matches prose in comments and string literals, so its number would move when
// someone edits a comment; a guard that reports phantom regressions gets ignored
// within a week, and an ignored guard is worse than none because it teaches the
// wrong lesson. Parsing only moves when code moves.
package driftguard

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// FilesystemCalls names the os.* entry points that reach the host filesystem.
//
// These are exactly the calls listed in docs/plan/tool-layer-io-seam-design-20261028.md
// §3.1, and they must stay in sync with it: a tool core that makes them directly
// can only ever describe the machine it runs on, which is the assumption that
// forced the envd backend to grow its own copy of every file tool.
var FilesystemCalls = map[string]bool{
	"ReadFile":     true,
	"WriteFile":    true,
	"Open":         true,
	"OpenFile":     true,
	"Create":       true,
	"Truncate":     true,
	"Stat":         true,
	"Lstat":        true,
	"Remove":       true,
	"RemoveAll":    true,
	"Rename":       true,
	"Mkdir":        true,
	"MkdirAll":     true,
	"ReadDir":      true,
	"Glob":         true,
	"WalkDir":      true,
	"Chmod":        true,
	"Chown":        true,
	"Chtimes":      true,
	"EvalSymlinks": true,
	"Getwd":        true,
}

// Finding is one located violation, reported as file:line so a reader can go
// straight to it.
type Finding struct {
	Pos     string
	Message string
}

func (f Finding) String() string { return fmt.Sprintf("%s: %s", f.Pos, f.Message) }

// FormatFindings renders violations for a test failure message, capped so a
// regression does not bury the assertion under hundreds of lines.
func FormatFindings(findings []Finding, limit int) string {
	if len(findings) == 0 {
		return "(none)"
	}
	var builder strings.Builder
	shown := findings
	if limit > 0 && len(shown) > limit {
		shown = shown[:limit]
	}
	for index, finding := range shown {
		if index > 0 {
			builder.WriteByte('\n')
		}
		builder.WriteString(finding.String())
	}
	if len(findings) > len(shown) {
		fmt.Fprintf(&builder, "\n... and %d more", len(findings)-len(shown))
	}
	return builder.String()
}

// FilesystemCallCounts counts direct os.* filesystem calls in the non-test Go
// files under root, returning the total and one location per call.
//
// A missing root is an error rather than a silent zero: a guard that quietly
// measures nothing would pass forever.
func FilesystemCallCounts(root string) (int, []Finding, error) {
	files, err := goFiles(root)
	if err != nil {
		return 0, nil, err
	}
	var found []Finding
	for _, path := range files {
		calls, err := filesystemCallsInFile(path)
		if err != nil {
			return 0, nil, err
		}
		found = append(found, calls...)
	}
	return len(found), found, nil
}

// filesystemCallsInFile parses one file and reports the os.* calls it contains.
func filesystemCallsInFile(path string) ([]Finding, error) {
	set := token.NewFileSet()
	parsed, err := parser.ParseFile(set, path, nil, parser.SkipObjectResolution)
	if err != nil {
		return nil, fmt.Errorf("driftguard: parse %s: %w", path, err)
	}
	if !importsOS(parsed) {
		// Without an os import no os.* call can exist, and skipping the walk keeps
		// the scan cheap over a large tree.
		return nil, nil
	}
	var found []Finding
	ast.Inspect(parsed, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := selector.X.(*ast.Ident)
		if !ok || pkg.Name != "os" {
			return true
		}
		if !isFilesystemCall(selector.Sel.Name) {
			return true
		}
		found = append(found, Finding{
			Pos:     fmt.Sprintf("%s:%d", displayPath(path), set.Position(selector.Pos()).Line),
			Message: fmt.Sprintf("direct os.%s call", selector.Sel.Name),
		})
		return true
	})
	return found, nil
}

// isFilesystemCall reports whether name belongs to the guarded set, ignoring the
// placeholder entries that exist only to keep the map readable.
func isFilesystemCall(name string) bool {
	enabled, known := FilesystemCalls[name]
	return known && enabled
}

// WalkerStyleCalls names the host-filesystem calls that FilesystemCallCounts is
// structurally blind to, keyed by their qualified form.
//
// Why a second list: the guard above matches the selector package `os`, so
// filepath.WalkDir - which walks the very same host tree and is exactly as
// machine-bound as os.ReadDir - never shows up in its numbers. Without this list a
// core could report zero host calls while still describing only the machine it
// runs on, and a reader would conclude the seam had landed when it had not.
//
// fs.WalkDir is deliberately absent: whether it touches the host depends on the
// fs.FS it is handed, which cannot be decided from the call site alone. That case
// needs a review note, not a counter that guesses.
var WalkerStyleCalls = map[string]bool{
	"filepath.WalkDir":      true,
	"filepath.Walk":         true,
	"filepath.Glob":         true,
	"filepath.EvalSymlinks": true,
	"os.DirFS":              true,
}

// WalkerStyleCallCounts counts the calls in WalkerStyleCalls under root, in
// non-test Go files. It exists to be ratcheted exactly like FilesystemCallCounts.
func WalkerStyleCallCounts(root string) (int, []Finding, error) {
	files, err := goFiles(root)
	if err != nil {
		return 0, nil, err
	}
	var found []Finding
	for _, path := range files {
		calls, err := walkerStyleCallsInFile(path)
		if err != nil {
			return 0, nil, err
		}
		found = append(found, calls...)
	}
	return len(found), found, nil
}

func walkerStyleCallsInFile(path string) ([]Finding, error) {
	set := token.NewFileSet()
	parsed, err := parser.ParseFile(set, path, nil, parser.SkipObjectResolution)
	if err != nil {
		return nil, fmt.Errorf("driftguard: parse %s: %w", path, err)
	}
	var found []Finding
	ast.Inspect(parsed, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := selector.X.(*ast.Ident)
		if !ok {
			return true
		}
		qualified := pkg.Name + "." + selector.Sel.Name
		if !WalkerStyleCalls[qualified] {
			return true
		}
		found = append(found, Finding{
			Pos:     fmt.Sprintf("%s:%d", displayPath(path), set.Position(selector.Pos()).Line),
			Message: fmt.Sprintf("host-tree call %s reaches the filesystem without saying os.*", qualified),
		})
		return true
	})
	return found, nil
}

// importsOS reports whether a file imports the os package under that name.
func importsOS(file *ast.File) bool {
	for _, spec := range file.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil || path != "os" {
			continue
		}
		if spec.Name != nil && spec.Name.Name != "os" {
			// Imported under another name: its calls are not `os.*` in source.
			continue
		}
		return true
	}
	return false
}

// BackendNameLiterals reports string literals under root that name a backend.
//
// A core that branches on a backend name is a core that has stopped being shared:
// that branch is where a second implementation of the same behaviour begins.
// Backends are meant to differ only through declared capabilities, so this guard
// holds a hard zero rather than a ratchet.
func BackendNameLiterals(root string, names ...string) ([]Finding, error) {
	files, err := goFiles(root)
	if err != nil {
		return nil, err
	}
	wanted := make(map[string]bool, len(names))
	for _, name := range names {
		wanted[name] = true
	}
	var found []Finding
	for _, path := range files {
		set := token.NewFileSet()
		parsed, err := parser.ParseFile(set, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, fmt.Errorf("driftguard: parse %s: %w", path, err)
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			literal, ok := node.(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				return true
			}
			value, err := strconv.Unquote(literal.Value)
			if err != nil || !wanted[value] {
				return true
			}
			found = append(found, Finding{
				Pos:     fmt.Sprintf("%s:%d", displayPath(path), set.Position(literal.Pos()).Line),
				Message: fmt.Sprintf("backend name %q as a literal: branch on a capability instead", value),
			})
			return true
		})
	}
	return found, nil
}

// CapabilityCallCounts counts method calls named method (for example
// "Capabilities") in the non-test Go files under root.
//
// This keeps a capability decision in one place: once every core asks the backend
// "can you do X?", the single downgrade rule has become N of them - the exact
// drift this design exists to remove.
func CapabilityCallCounts(root, method string) (int, []Finding, error) {
	files, err := goFiles(root)
	if err != nil {
		return 0, nil, err
	}
	var found []Finding
	for _, path := range files {
		set := token.NewFileSet()
		parsed, err := parser.ParseFile(set, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return 0, nil, fmt.Errorf("driftguard: parse %s: %w", path, err)
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || selector.Sel.Name != method {
				return true
			}
			found = append(found, Finding{
				Pos:     fmt.Sprintf("%s:%d", displayPath(path), set.Position(call.Pos()).Line),
				Message: fmt.Sprintf("calls %s()", method),
			})
			return true
		})
	}
	return len(found), found, nil
}

// goFiles lists the non-test .go files under root, sorted for a stable report.
func goFiles(root string) ([]string, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("driftguard: scan root %s: %w", root, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("driftguard: scan root %s is not a directory", root)
	}
	var files []string
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		files = append(files, path)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("driftguard: walk %s: %w", root, err)
	}
	sort.Strings(files)
	return files, nil
}

// displayPath renders a path for a message: absolute Windows paths bury the part
// that matters, so the module-relative form is used whenever it is available.
func displayPath(path string) string {
	cleaned := filepath.ToSlash(path)
	if marker := strings.Index(cleaned, "/internal/"); marker >= 0 {
		return cleaned[marker+1:]
	}
	return cleaned
}
