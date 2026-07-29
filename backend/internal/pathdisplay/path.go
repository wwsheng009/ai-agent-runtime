package pathdisplay

import (
	"path/filepath"
	"strings"
)

// InlineFilePathRunes is the largest file path kept in a compact tool title.
// Longer paths are rendered on their own line so the filename is never cut.
const InlineFilePathRunes = 60

var fileArgumentKeys = []string{"file_path", "filepath", "filePath", "filename", "file"}

// File returns the first explicit file-path argument and a display path. When
// possible, absolute paths below the tool working directory become relative.
func File(args map[string]interface{}) (string, string) {
	if len(args) == 0 {
		return "", ""
	}
	for _, key := range fileArgumentKeys {
		raw, ok := args[key].(string)
		if !ok {
			continue
		}
		path := oneLine(raw)
		if path == "" {
			continue
		}
		return key, Relative(path, WorkingDirectory(args))
	}
	return "", ""
}

// WorkingDirectory returns the directory context accepted by shell and file
// tools, normalized to one printable line.
func WorkingDirectory(args map[string]interface{}) string {
	for _, key := range []string{"workdir", "working_directory", "cwd"} {
		if raw, ok := args[key].(string); ok {
			if value := oneLine(raw); value != "" {
				return value
			}
		}
	}
	return ""
}

// Relative shortens an absolute path only when base is an absolute ancestor
// and the resulting relative path is actually shorter.
func Relative(path, base string) string {
	path = oneLine(path)
	base = oneLine(base)
	if path == "" || base == "" || !filepath.IsAbs(path) || !filepath.IsAbs(base) {
		return path
	}
	rel, err := filepath.Rel(base, path)
	if err != nil || rel == "." || outsideBase(rel) {
		return path
	}
	if strings.Contains(path, "/") && !strings.Contains(path, `\`) {
		rel = filepath.ToSlash(rel)
	}
	if len([]rune(rel)) >= len([]rune(path)) {
		return path
	}
	return rel
}

// NeedsOwnLine reports whether a file path should leave the compact title.
func NeedsOwnLine(path string) bool {
	return len([]rune(oneLine(path))) > InlineFilePathRunes
}

// IsFileArgument identifies aliases normalized by the tool manager.
func IsFileArgument(key string) bool {
	for _, candidate := range fileArgumentKeys {
		if key == candidate {
			return true
		}
	}
	return false
}

func outsideBase(rel string) bool {
	clean := filepath.Clean(rel)
	return clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator))
}

func oneLine(value string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
}
