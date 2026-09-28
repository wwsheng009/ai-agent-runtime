// Package projectscan provides a bounded, read-only scan that detects project
// types by looking for well-known marker files such as go.mod, package.json,
// pyproject.toml or Cargo.toml.
//
// The scan never reads file contents and never writes to the file system: it
// only lists directory entries and matches entry names against a fixed marker
// table. Every scan is bounded by Options (maximum depth, maximum directories
// visited, maximum entries examined), so it terminates even on very large
// trees; Result.Truncated reports whether a bound cut the traversal short.
//
// Directories that conventionally hold VCS metadata, dependencies, caches or
// build output (.git, node_modules, vendor, target, dist, build, ...) are
// skipped, and symbolic links encountered during traversal are never followed
// (the scan root itself is resolved normally). Marker names are matched
// byte-exactly (case-sensitive) on every platform.
package projectscan

import (
	"cmp"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// Type identifies a project type detected from marker files.
type Type string

// Supported project types.
const (
	TypeGo     Type = "go"
	TypeNode   Type = "node"
	TypePython Type = "python"
	TypeRust   Type = "rust"
	TypeJava   Type = "java"
	TypeDotNet Type = "dotnet"
	TypeRuby   Type = "ruby"
	TypePHP    Type = "php"
)

// Default bounds applied when the corresponding Options field is not positive.
const (
	// DefaultMaxDepth is the default number of directory levels visited,
	// counting the scan root as level 1.
	DefaultMaxDepth = 4
	// DefaultMaxDirs is the default maximum number of directory listings.
	DefaultMaxDirs = 2048
	// DefaultMaxEntries is the default maximum number of directory entries
	// examined.
	DefaultMaxEntries = 32768
)

// Options bounds the work performed by Scan. The zero value is valid and uses
// the Default* bounds. Bounds cannot be disabled: every scan is bounded.
type Options struct {
	// MaxDepth is the maximum number of directory levels to visit, counting
	// the root as level 1; MaxDepth 1 scans only the root directory.
	// Values <= 0 use DefaultMaxDepth.
	MaxDepth int
	// MaxDirs is the maximum number of directory listings to attempt.
	// Values <= 0 use DefaultMaxDirs.
	MaxDirs int
	// MaxEntries is the maximum number of directory entries to examine.
	// Values <= 0 use DefaultMaxEntries.
	MaxEntries int
}

func (o Options) normalized() Options {
	if o.MaxDepth <= 0 {
		o.MaxDepth = DefaultMaxDepth
	}
	if o.MaxDirs <= 0 {
		o.MaxDirs = DefaultMaxDirs
	}
	if o.MaxEntries <= 0 {
		o.MaxEntries = DefaultMaxEntries
	}
	return o
}

// Match describes one marker file found during a scan.
type Match struct {
	// Dir is the directory containing the marker, relative to the scan root
	// with forward slashes; "." for the root itself.
	Dir string
	// Marker is the entry name that matched, e.g. "go.mod".
	Marker string
	// Type is the project type the marker indicates.
	Type Type
}

// Path returns the marker path relative to the scan root, using forward
// slashes. For a marker in the root directory it is just the file name.
func (m Match) Path() string {
	if m.Dir == "" || m.Dir == "." {
		return m.Marker
	}
	return m.Dir + "/" + m.Marker
}

// Result is the outcome of a Scan.
type Result struct {
	// Root is the cleaned path that was scanned.
	Root string
	// Matches lists every marker hit, sorted by directory and then marker
	// name. It is nil when nothing matched.
	Matches []Match
	// Types lists the distinct detected project types, sorted by name.
	// It is nil when nothing matched.
	Types []Type
	// DirsVisited counts directory listings attempted, including directories
	// that could not be read. It never exceeds the effective MaxDirs.
	DirsVisited int
	// EntriesScanned counts directory entries examined. It never exceeds the
	// effective MaxEntries.
	EntriesScanned int
	// DirsUnreadable counts subdirectories whose listing failed, for example
	// because of permissions. The scan continues past them.
	DirsUnreadable int
	// Truncated reports that MaxDirs or MaxEntries stopped the traversal
	// before it was complete. The depth limit does not set Truncated.
	Truncated bool
}

// HasType reports whether the scan detected the given project type.
func (r Result) HasType(t Type) bool {
	return slices.Contains(r.Types, t)
}

// marker maps a marker entry name to a project type. Names containing '*',
// '?' or '[' are glob patterns matched with path.Match; all other names are
// matched exactly.
type marker struct {
	name string
	typ  Type
}

// markerTable is the fixed set of marker files recognized by Scan.
var markerTable = []marker{
	// Go
	{name: "go.mod", typ: TypeGo},
	{name: "go.work", typ: TypeGo},
	// Node
	{name: "package.json", typ: TypeNode},
	// Python
	{name: "pyproject.toml", typ: TypePython},
	{name: "setup.py", typ: TypePython},
	{name: "requirements.txt", typ: TypePython},
	{name: "Pipfile", typ: TypePython},
	// Rust
	{name: "Cargo.toml", typ: TypeRust},
	// Java (Maven / Gradle)
	{name: "pom.xml", typ: TypeJava},
	{name: "build.gradle", typ: TypeJava},
	{name: "build.gradle.kts", typ: TypeJava},
	{name: "settings.gradle", typ: TypeJava},
	{name: "settings.gradle.kts", typ: TypeJava},
	// .NET
	{name: "*.csproj", typ: TypeDotNet},
	{name: "*.fsproj", typ: TypeDotNet},
	{name: "*.vbproj", typ: TypeDotNet},
	{name: "*.sln", typ: TypeDotNet},
	// Ruby
	{name: "Gemfile", typ: TypeRuby},
	// PHP
	{name: "composer.json", typ: TypePHP},
}

// exactMarkers and globMarkers split markerTable by match kind once at init.
var (
	exactMarkers = func() map[string]Type {
		exact := make(map[string]Type, len(markerTable))
		for _, mk := range markerTable {
			if !isGlob(mk.name) {
				exact[mk.name] = mk.typ
			}
		}
		return exact
	}()
	globMarkers = func() []marker {
		var globs []marker
		for _, mk := range markerTable {
			if isGlob(mk.name) {
				globs = append(globs, mk)
			}
		}
		return globs
	}()
)

func isGlob(name string) bool {
	return strings.ContainsAny(name, "*?[")
}

// skippedDirNames are directory names that never contribute project markers:
// VCS metadata, dependency trees, caches and build output.
var skippedDirNames = map[string]struct{}{
	".git":             {},
	".hg":              {},
	".svn":             {},
	".idea":            {},
	".vscode":          {},
	".cache":           {},
	".next":            {},
	".nuxt":            {},
	".tox":             {},
	".mypy_cache":      {},
	".pytest_cache":    {},
	".ruff_cache":      {},
	".terraform":       {},
	".gradle":          {},
	"node_modules":     {},
	"bower_components": {},
	"vendor":           {},
	"__pycache__":      {},
	".venv":            {},
	"venv":             {},
	"target":           {},
	"dist":             {},
	"build":            {},
	"obj":              {},
}

// Scan detects project types under root by matching marker files, bounded by
// opts. It returns an error only when root is empty, missing, not a directory,
// or its own listing fails; unreadable subdirectories are skipped and counted
// in Result.DirsUnreadable.
//
// Scan is strictly read-only: it lists directories and inspects entry names.
// File contents are never opened and nothing on disk is created, modified or
// deleted. Symbolic links encountered while walking are never followed.
func Scan(root string, opts Options) (*Result, error) {
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("projectscan: root path is empty")
	}
	root = filepath.Clean(root)
	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("projectscan: stat root %q: %w", root, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("projectscan: root %q is not a directory", root)
	}

	opts = opts.normalized()
	result := &Result{Root: root}

	type pendingDir struct {
		abs   string
		rel   string
		level int
	}
	queue := []pendingDir{{abs: root, rel: ".", level: 1}}

	for len(queue) > 0 {
		if result.DirsVisited >= opts.MaxDirs {
			result.Truncated = true
			break
		}
		current := queue[0]
		queue = queue[1:]

		result.DirsVisited++
		entries, err := os.ReadDir(current.abs)
		if err != nil {
			if current.level == 1 {
				return nil, fmt.Errorf("projectscan: read root %q: %w", current.abs, err)
			}
			result.DirsUnreadable++
			continue
		}

		exhausted := false
		for _, entry := range entries {
			if result.EntriesScanned >= opts.MaxEntries {
				result.Truncated = true
				exhausted = true
				break
			}
			result.EntriesScanned++

			name := entry.Name()
			isDir := entry.IsDir()
			if !isDir {
				result.Matches = append(result.Matches, matchesFor(name, current.rel)...)
			}

			if !isDir || current.level >= opts.MaxDepth {
				continue
			}
			if _, skip := skippedDirNames[name]; skip {
				continue
			}
			queue = append(queue, pendingDir{
				abs:   filepath.Join(current.abs, name),
				rel:   joinRel(current.rel, name),
				level: current.level + 1,
			})
		}
		if exhausted {
			break
		}
	}

	sortMatches(result.Matches)
	result.Types = collectTypes(result.Matches)
	return result, nil
}

// matchesFor returns the marker matches for one non-directory entry name.
func matchesFor(name, dir string) []Match {
	if typ, ok := exactMarkers[name]; ok {
		return []Match{{Dir: dir, Marker: name, Type: typ}}
	}
	var matches []Match
	for _, mk := range globMarkers {
		if ok, err := path.Match(mk.name, name); err == nil && ok {
			matches = append(matches, Match{Dir: dir, Marker: name, Type: mk.typ})
		}
	}
	return matches
}

func joinRel(dir, name string) string {
	if dir == "" || dir == "." {
		return name
	}
	return dir + "/" + name
}

func sortMatches(matches []Match) {
	slices.SortFunc(matches, func(a, b Match) int {
		if c := cmp.Compare(a.Dir, b.Dir); c != 0 {
			return c
		}
		return cmp.Compare(a.Marker, b.Marker)
	})
}

func collectTypes(matches []Match) []Type {
	var types []Type
	seen := make(map[Type]struct{}, len(matches))
	for _, m := range matches {
		if _, ok := seen[m.Type]; ok {
			continue
		}
		seen[m.Type] = struct{}{}
		types = append(types, m.Type)
	}
	slices.Sort(types)
	return types
}
