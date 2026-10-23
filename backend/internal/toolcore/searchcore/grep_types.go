package searchcore

import "strings"

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
	parts := strings.Split(raw, ",")
	globs := make([]string, 0, len(parts))
	for _, part := range parts {
		if pattern := strings.TrimSpace(part); pattern != "" {
			globs = append(globs, pattern)
		}
	}
	return globs
}

// EffectiveGrepFilters returns the include/exclude lists with the type/type_not
// parameters folded in. Type globs are a union with the explicit patterns, in
// the same order the local tool appends them.
func EffectiveGrepFilters(opts GrepOptions) (include, exclude []string) {
	include = append(GrepTypeGlobs(opts.Type), opts.Include...)
	exclude = append(GrepTypeGlobs(opts.TypeNot), opts.Exclude...)
	return include, exclude
}
