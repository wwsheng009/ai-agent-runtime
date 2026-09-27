package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/ipynb"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
)

// viewNotebookMaxBytes bounds the whole-file read that JSON parsing requires:
// the normal text path streams, but a notebook must be materialized, so an
// oversized file degrades to a note instead of risking an agent-side OOM.
var viewNotebookMaxBytes int64 = 64 << 20

// looksLikeNotebook reports whether a request should go through the notebook
// renderer: an explicit .ipynb extension, or notebook-shaped JSON head bytes.
// The extension is only a hint; the content decides (analysis §3.10).
func looksLikeNotebook(path string, head []byte) bool {
	if strings.EqualFold(filepath.Ext(strings.TrimSpace(path)), ".ipynb") {
		return true
	}
	text := strings.TrimSpace(string(head))
	return strings.HasPrefix(text, "{") &&
		strings.Contains(text, `"cells"`) &&
		strings.Contains(text, `"nbformat"`)
}

// viewNotebookResult renders a notebook as tagged Markdown and runs it through
// the same window pipeline as plain text: offset/limit/byte budget/line numbers
// all apply, so a huge notebook is paged like any other file. Image outputs are
// persisted and declared through the image passthrough contract.
func (v *ViewTool) viewNotebookResult(absPath, displayPath string, p ViewFileRequest, info os.FileInfo) (*toolkit.ToolResult, bool) {
	head, err := readViewFileHead(absPath, 1024)
	if err != nil || !looksLikeNotebook(absPath, head) {
		return nil, false
	}
	if info != nil && info.Size() > viewNotebookMaxBytes {
		return &toolkit.ToolResult{
			Success:    true,
			OutputKind: toolresult.KindText,
			Content: fmt.Sprintf(
				"notebook 文件: %s\n大小: %d 字节，超过 %d 字节上限，未渲染。\n可用 shell/jq 按 cell 读取，或先用脚本裁剪体积后重试 view。",
				displayPath, info.Size(), viewNotebookMaxBytes,
			),
			Metadata: map[string]interface{}{
				"file_path":    absPath,
				"doc_kind":     "ipynb",
				"doc_degraded": true,
				"doc_reason":   "notebook_too_large",
				"doc_size":     info.Size(),
				"view_skipped": "notebook_too_large",
			},
		}, true
	}
	render, err := ipynb.RenderFile(absPath)
	if err != nil {
		// 内容嗅探可能把普通 JSON 误认为 notebook；只有扩展名明确是 .ipynb
		// 时才把它当成"坏的 notebook"失败，否则退回普通文本读取。
		if !strings.EqualFold(filepath.Ext(absPath), ".ipynb") {
			return nil, false
		}
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      fmt.Errorf("notebook 渲染失败: %w；可用 shell/jq 直接检查原文件", err),
			Metadata: map[string]interface{}{
				"file_path": absPath,
				"doc_kind":  "ipynb",
			},
		}, true
	}

	content, readMeta, readErr := v.readLines(strings.NewReader(render.Markdown), p.Offset, p.Limit)
	if readErr != nil {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      fmt.Errorf("notebook 窗口读取失败: %w", readErr),
		}, true
	}

	outputKind := toolresult.KindText
	metadata := map[string]interface{}{
		"file_path":    absPath,
		"file_size":    info.Size(),
		"doc_kind":     "ipynb",
		"doc_degraded": false,
	}
	for key, value := range viewWindowMetadata(p, readMeta) {
		metadata[key] = value
	}
	for source, target := range map[string]string{
		"cells":                  "notebook_cells",
		"code_cells":             "notebook_code_cells",
		"markdown_cells":         "notebook_markdown_cells",
		"images":                 "notebook_images",
		"outputs_omitted":        "notebook_outputs_omitted",
		"notebook_format":        "notebook_format",
		"notebook_cells_omitted": "notebook_cells_omitted",
	} {
		if value, ok := render.Metadata[source]; ok {
			metadata[target] = value
		}
	}
	// doc_degraded 不再恒为 false：被折叠的输出/超上限的图片意味着模型看到
	// 的正文不是全部（review F11）。
	if omitted := viewMetadataInt(metadata, "notebook_outputs_omitted", 0); omitted > 0 {
		metadata["doc_degraded"] = true
		metadata["doc_reason"] = "notebook_outputs_omitted"
	}
	if omittedCells := viewMetadataInt(metadata, "notebook_cells_omitted", 0); omittedCells > 0 {
		metadata["doc_degraded"] = true
		metadata["doc_reason"] = "notebook_cells_omitted"
	}

	result := &toolkit.ToolResult{
		Success:    true,
		OutputKind: outputKind,
		Content:    content,
		Metadata:   metadata,
	}
	if strings.TrimSpace(render.Markdown) == "" {
		result.Content = strings.TrimSpace(result.Content + "\n\n[note] notebook 没有可渲染的 cell（cells: []）。")
	}

	if attachments := v.persistNotebookImages(render.Images); len(attachments) > 0 {
		paths := make([]string, 0, len(attachments))
		for _, attachment := range attachments {
			paths = append(paths, attachment.Path)
		}
		result.OutputKind = toolresult.KindStructured
		result.Metadata[toolresult.MetadataImagePassthroughKey] = true
		result.Metadata[toolresult.MetadataImagePathKey] = paths[0]
		result.Metadata[toolresult.MetadataImageMimeTypeKey] = attachments[0].MIME
		result.Metadata[toolresult.MetadataImagePathsKey] = paths
		result.Metadata[toolresult.MetadataImageNoteKey] = fmt.Sprintf("notebook 图片输出 %d 张，已随消息附加", len(paths))
		result.Content = strings.TrimRight(result.Content, "\n") +
			fmt.Sprintf("\n\n[note] notebook 的 %d 张图片输出已作为图像输入附加到下一轮对话。", len(paths))
	}

	attachViewEfficiencyHints(result, p, readMeta)
	return result, true
}

// notebookImageAttachment pairs a persisted path with the MIME of exactly that
// image: persistence can skip images, so indexing render.Images is wrong.
type notebookImageAttachment struct {
	Path string
	MIME string
}

// persistNotebookImages writes decoded notebook images to the shared
// content-addressed image directory so provider requests (and history replay)
// keep resolving them. Duplicate content collapses onto one path.
func (v *ViewTool) persistNotebookImages(images []ipynb.Image) []notebookImageAttachment {
	if len(images) == 0 {
		return nil
	}
	attachments := make([]notebookImageAttachment, 0, len(images))
	seen := make(map[string]struct{}, len(images))
	for _, image := range images {
		ext := ".png"
		switch {
		case strings.EqualFold(image.MIME, "image/jpeg"):
			ext = ".jpg"
		case strings.EqualFold(image.MIME, "image/gif"):
			ext = ".gif"
		}
		path := persistRenderedImage(image.Data, ext)
		if path == "" {
			continue
		}
		if _, duplicate := seen[path]; duplicate {
			continue
		}
		seen[path] = struct{}{}
		attachments = append(attachments, notebookImageAttachment{Path: path, MIME: image.MIME})
	}
	return attachments
}

// persistRenderedImage is persistViewImage's in-memory sibling: same
// content-addressed directory, bytes instead of a source path.
func persistRenderedImage(data []byte, ext string) string {
	if len(data) == 0 {
		return ""
	}
	dir := filepath.Join(os.TempDir(), "ai-agent-runtime-images")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ""
	}
	name := fileBytesSHA256(data)[:16] + strings.ToLower(ext)
	dst := filepath.Join(dir, name)
	if _, err := os.Stat(dst); err == nil {
		return dst
	}
	if err := writeFileAtomicLocal(dst, data, writeFileModeDefault); err != nil {
		return ""
	}
	return dst
}
