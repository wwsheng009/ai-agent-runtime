package searchcore

import (
	"fmt"
	"strings"
)

// FileTypeToGlob maps ripgrep's --type names to glob patterns: the single
// definition shared by the local builtin engine, the remote schema and the
// ripgrep runner's derived --glob flags. Keeping our own table (instead of
// forwarding --type to ripgrep) is what makes `type=go` mean the same thing on
// every engine.
var FileTypeToGlob = map[string]string{
	"go":    "*.go",
	"py":    "*.py",
	"java":  "*.java",
	"js":    "*.js",
	"ts":    "*.ts",
	"tsx":   "*.tsx",
	"jsx":   "*.jsx",
	"rs":    "*.rs",
	"rb":    "*.rb",
	"cpp":   "*.cpp,*.hpp,*.cc,*.cxx",
	"c":     "*.c,*.h",
	"cs":    "*.cs",
	"swift": "*.swift",
	"kt":    "*.kt",
	"scala": "*.scala",
	"php":   "*.php",
	"html":  "*.html,*.htm",
	"css":   "*.css",
	"json":  "*.json",
	"yaml":  "*.yaml,*.yml",
	"toml":  "*.toml",
	"xml":   "*.xml",
	"md":    "*.md,*.markdown",
	"sh":    "*.sh,*.bash",
	"sql":   "*.sql",
	"proto": "*.proto",
	"dart":  "*.dart",
}

// GrepTypeGlobs returns the glob patterns for one file-type name. An unknown
// type falls back to `*.<type>`, which is what the local tool has always done.
func GrepTypeGlobs(fileType string) []string {
	fileType = strings.TrimSpace(fileType)
	if fileType == "" {
		return nil
	}
	raw, ok := FileTypeToGlob[fileType]
	if !ok {
		return []string{"*." + fileType}
	}
	return splitGrepTypeGlobs(raw)
}

// splitGrepTypeGlobs turns a table entry ("*.cpp,*.hpp") or the right-hand side
// of a type_add definition into individual globs.
func splitGrepTypeGlobs(raw string) []string {
	parts := strings.Split(raw, ",")
	globs := make([]string, 0, len(parts))
	for _, part := range parts {
		if pattern := strings.TrimSpace(part); pattern != "" {
			globs = append(globs, pattern)
		}
	}
	return globs
}

// grepTypeTable builds the per-call file type table: the shared definitions
// with type_add applied first and type_clear removed afterwards. The order is
// the one the local tool has always sent to ripgrep (--type-add before
// --type-clear), so a call that both adds and clears the same name keeps its
// established meaning: the clear wins.
//
// Every engine consumes the resolved globs, not the flags, which is what makes
// type_add/type_clear behave identically on a host with ripgrep and one without.
func grepTypeTable(typeAdd, typeClear []string) (map[string][]string, error) {
	table := make(map[string][]string, len(FileTypeToGlob)+len(typeAdd))
	for name, raw := range FileTypeToGlob {
		table[name] = splitGrepTypeGlobs(raw)
	}
	for _, spec := range typeAdd {
		name, globs, err := parseGrepTypeAdd(spec)
		if err != nil {
			return nil, err
		}
		// Adding to an existing type extends it, matching ripgrep's
		// --type-add semantics.
		table[name] = append(table[name], globs...)
	}
	for _, name := range typeClear {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		delete(table, name)
	}
	return table, nil
}

// parseGrepTypeAdd parses one `name:glob[,glob...]` definition.
func parseGrepTypeAdd(spec string) (string, []string, error) {
	name, rawGlobs, ok := strings.Cut(strings.TrimSpace(spec), ":")
	name = strings.TrimSpace(name)
	if !ok || name == "" {
		return "", nil, fmt.Errorf("invalid type_add %q: format is name:glob, e.g. html:*.html", spec)
	}
	globs := splitGrepTypeGlobs(rawGlobs)
	if len(globs) == 0 {
		return "", nil, fmt.Errorf("invalid type_add %q: format is name:glob, e.g. html:*.html", spec)
	}
	return name, globs, nil
}

// grepTypeGlobsFrom resolves a type name against a per-call table. A name that
// is not defined (never existed, or was just cleared) keeps the long-standing
// fallback `*.<type>`.
func grepTypeGlobsFrom(table map[string][]string, fileType string) []string {
	fileType = strings.TrimSpace(fileType)
	if fileType == "" {
		return nil
	}
	if globs, ok := table[fileType]; ok {
		return append([]string(nil), globs...)
	}
	return []string{"*." + fileType}
}

// validateGrepTypeDefinitions parses the type definitions once, before an
// engine runs, so a malformed type_add is a caller-visible argument error on
// both engines instead of an engine-specific failure.
func validateGrepTypeDefinitions(opts GrepOptions) error {
	_, err := grepTypeTable(opts.TypeAdd, opts.TypeClear)
	return err
}

// EffectiveGrepFilters returns the include/exclude lists with the type/type_not
// parameters folded in. Type globs are a union with the explicit patterns, in
// the same order the local tool appends them.
func EffectiveGrepFilters(opts GrepOptions) (include, exclude []string, err error) {
	table, err := grepTypeTable(opts.TypeAdd, opts.TypeClear)
	if err != nil {
		return nil, nil, err
	}
	include = append(grepTypeGlobsFrom(table, opts.Type), opts.Include...)
	exclude = append(grepTypeGlobsFrom(table, opts.TypeNot), opts.Exclude...)
	return include, exclude, nil
}
