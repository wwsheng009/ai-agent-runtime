package mutatecore

import (
	"fmt"
	"path/filepath"
	"strings"
)

// DocumentOverwriteExtensions lists the document containers that view renders as
// Markdown windows instead of text files. A text write tool must not write into
// them: doing so silently replaces the original document with a plain-text
// fragment that still carries a document extension.
//
// This is a property of the tool, not of the host. A remote surface writing
// report.docx through `write` destroys the same file as a local one would, so
// the guard belongs to the one shared implementation.
var DocumentOverwriteExtensions = map[string]struct{}{
	".pdf": {},

	".doc":  {},
	".docx": {},
	".docm": {},

	".ppt":  {},
	".pptx": {},
	".pptm": {},

	".xls":  {},
	".xlsx": {},
	".xlsm": {},

	".odt":  {},
	".ods":  {},
	".odp":  {},
	".epub": {},
	".rtf":  {},
}

// DocumentExtensionRefusal returns the guarded document extension for path, or
// "" when the target is an ordinary text file.
//
// Windows silently strips trailing dots and spaces, so "report.docx." and
// "report.docx " both address report.docx; an NTFS alternate-data-stream spelling
// ("report.docx::$DATA") reaches the same content. Both are normalized before the
// extension lookup, otherwise the guard is trivially bypassed.
func DocumentExtensionRefusal(path string) string {
	if ext := documentExtensionRefusalForSpelling(path); ext != "" {
		return ext
	}
	// The write lands on the link target: a ".txt" alias pointing at report.rtf
	// would pass an extension-only check while the in-place write replaces the
	// real document. Missing paths keep the lexical verdict (nothing to resolve).
	if resolved, err := filepath.EvalSymlinks(strings.TrimSpace(path)); err == nil && strings.TrimSpace(resolved) != "" {
		return documentExtensionRefusalForSpelling(resolved)
	}
	return ""
}

func documentExtensionRefusalForSpelling(path string) string {
	name := filepath.Base(strings.TrimSpace(path))
	if idx := strings.Index(name, ":"); idx >= 0 {
		name = name[:idx]
	}
	name = strings.TrimRight(name, " .")
	ext := strings.ToLower(filepath.Ext(name))
	if _, ok := DocumentOverwriteExtensions[ext]; ok {
		return ext
	}
	return ""
}

// DocumentRefusalContent is the model-facing text for a refused document write.
// The recovery route is view (Markdown window) or a dedicated converter, never
// another text write.
func DocumentRefusalContent(toolName, verb, path, ext string) (string, string) {
	return fmt.Sprintf(
			"%s 拒绝%s文档格式（%s）: %s；view 会把原文档渲染成 Markdown 窗口，需要修改请使用专用编辑器，或转换后重建文件。",
			toolName, verb, ext, path,
		),
		CodeUnsupported
}
