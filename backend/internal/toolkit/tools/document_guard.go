package tools

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
)

// documentOverwriteExtensions lists the document containers that view renders
// as Markdown windows instead of text files (analysis §3.9-6). Text-shaped
// write tools must not write into them: doing so silently replaces the
// original document with a plain-text fragment carrying a document extension.
var documentOverwriteExtensions = map[string]struct{}{
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

// documentExtensionRefusal returns the document extension when path names one
// of the guarded formats, otherwise "".
//
// Windows silently strips trailing dots/spaces from a path, so "report.docx."
// and "report.docx " both land on report.docx: the check trims those before
// looking at the extension, otherwise the guard would be trivially bypassed.
// An NTFS alternate-data-stream spelling ("report.docx::$DATA") addresses the
// same file content, so the stream suffix is cut before the extension lookup.
func documentExtensionRefusal(path string) string {
	if ext := documentExtensionRefusalForSpelling(path); ext != "" {
		return ext
	}
	// The write lands on the link target: a ".txt" alias pointing at
	// report.rtf would pass an extension-only check while the in-place write
	// replaces the real document (2026-09-27 review H16). Missing paths keep
	// the lexical verdict (nothing to resolve yet).
	if resolved, err := filepath.EvalSymlinks(strings.TrimSpace(path)); err == nil && strings.TrimSpace(resolved) != "" {
		return documentExtensionRefusalForSpelling(resolved)
	}
	return ""
}

// documentExtensionRefusalForSpelling applies the extension check to one path
// spelling (no link resolution).
func documentExtensionRefusalForSpelling(path string) string {
	name := filepath.Base(strings.TrimSpace(path))
	if idx := strings.Index(name, ":"); idx >= 0 {
		name = name[:idx]
	}
	name = strings.TrimRight(name, " .")
	ext := strings.ToLower(filepath.Ext(name))
	if _, ok := documentOverwriteExtensions[ext]; ok {
		return ext
	}
	return ""
}

// documentRefusalResult is the shared refusal shape for text tools aimed at a
// document container; the recovery route is view (Markdown window) or a
// dedicated converter, never another text write.
func documentRefusalResult(toolName, verb, path, ext string) *toolkit.ToolResult {
	return &toolkit.ToolResult{
		Success:    false,
		OutputKind: toolresult.KindText,
		Error: fmt.Errorf(
			"%s 拒绝%s文档格式（%s）: %s；view 会把原文档渲染成 Markdown 窗口，需要修改请使用专用编辑器，或转换后重建文件。",
			toolName, verb, ext, path,
		),
		Metadata: map[string]interface{}{
			"file_path":          path,
			"document_refused":   true,
			"document_extension": ext,
		},
	}
}
