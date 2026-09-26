package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/docread"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
)

// 可注入，便于测试在无转换器的机器上稳定覆盖渲染/降级分支（docread 内部的
// lookPath/runCommand 同样是包级可替换变量，两层各自守住自己的边界）。
var (
	docreadDetect = docread.Detect
	docreadRender = docread.Render
)

// viewDocumentResult renders a document container (pdf/docx/pptx/xlsx/odt/
// epub/rtf, plus unknown zip) through docread and reuses the shared window
// pipeline. When no extractor is available it degrades to one MIME note line
// instead of a binary error (analysis §3.9).
func (v *ViewTool) viewDocumentResult(ctx context.Context, absPath, displayPath string, p ViewFileRequest, info os.FileInfo) (*toolkit.ToolResult, bool) {
	head, err := readViewFileHead(absPath, 512)
	if err != nil {
		return nil, false
	}
	probe := docreadDetect(absPath, head)
	switch probe.Kind {
	case "pdf", "docx", "pptx", "xlsx", "odt", "ods", "odp", "epub", "rtf", "zip":
	default:
		// text/svg keep the normal text path (line semantics unchanged),
		// binary stays with binaryNoteResult.
		return nil, false
	}

	if !probe.Supported {
		return v.documentNoteResult(displayPath, absPath, probe, info), true
	}

	render, err := docreadRender(ctx, absPath)
	if err != nil {
		if errors.Is(err, docread.ErrUnsupported) {
			// Inner Detect may disagree with the outer probe (converter
			// vanished between two lookups): force the unsupported shape so
			// MIMENote keeps the download/convert recovery route.
			probe.Supported = false
			return v.documentNoteResult(displayPath, absPath, probe, info), true
		}
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error: fmt.Errorf(
				"文档渲染失败（%s）: %w；可用 download 拉取后转换，或安装/检查对应转换器后重试",
				probe.Kind, err,
			),
			Metadata: map[string]interface{}{
				"file_path":    absPath,
				"doc_kind":     probe.Kind,
				"doc_mime":     probe.MIME,
				"doc_degraded": true,
			},
		}, true
	}

	content, readMeta, readErr := v.readLines(strings.NewReader(render.Markdown), p.Offset, p.Limit)
	if readErr != nil {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      fmt.Errorf("文档窗口读取失败: %w", readErr),
		}, true
	}

	metadata := map[string]interface{}{
		"file_path": absPath,
		"file_size": info.Size(),
	}
	for key, value := range viewWindowMetadata(p, readMeta) {
		metadata[key] = value
	}
	for key, value := range render.Metadata {
		metadata[key] = value
	}
	result := &toolkit.ToolResult{
		Success:    true,
		OutputKind: toolresult.KindText,
		Content:    content,
		Metadata:   metadata,
	}
	if strings.TrimSpace(render.Markdown) == "" {
		result.Content = strings.TrimSpace(result.Content +
			"\n\n[note] 文档没有可抽取的正文（可能为空文档或整篇都是图片），请用 download 拉取原文件核对。")
	}
	if note := scannedDocumentNote(render.Metadata); note != "" {
		result.Content = strings.TrimRight(result.Content, "\n") + "\n\n" + note
	}
	attachViewEfficiencyHints(result, p, readMeta)
	return result, true
}

// documentNoteResult is the no-extractor degradation: one line of real format
// info plus the download/convert recovery route.
func (v *ViewTool) documentNoteResult(displayPath, absPath string, probe docread.Probe, info os.FileInfo) *toolkit.ToolResult {
	var size int64
	if info != nil {
		size = info.Size()
	}
	reason := "no_converter"
	if probe.Kind == "zip" || probe.Kind == "binary" {
		reason = "unsupported_kind"
	}
	return &toolkit.ToolResult{
		Success:    true,
		OutputKind: toolresult.KindText,
		Content: fmt.Sprintf(
			"文档文件: %s\n真实格式: %s（magic bytes 判定）\n%s\n可用 download 拉取后配合外部工具转换，或安装对应转换器后重试 view。",
			displayPath, probe.Kind, docread.MIMENote(probe, size),
		),
		Metadata: map[string]interface{}{
			"file_path":    absPath,
			"doc_kind":     probe.Kind,
			"doc_mime":     probe.MIME,
			"doc_degraded": true,
			"doc_reason":   reason,
			"binary_note":  true,
			"file_size":    size,
		},
	}
}

// scannedDocumentNote turns the PDF page statistics into an actionable note:
// which pages have no text layer and how to recover them (analysis §3.9-5).
func scannedDocumentNote(metadata map[string]interface{}) string {
	pagesWithoutText, _ := metadata["doc_pages_without_text"].([]int)
	if len(pagesWithoutText) == 0 {
		return ""
	}
	if textLayer, _ := metadata["doc_text_layer"].(string); textLayer == "empty" {
		if pages, ok := metadata["doc_pages"].(int); ok && pages > 0 {
			return fmt.Sprintf(
				"[note] 该 PDF 共 %d 页且没有可抽取的文本层，可能是扫描件；可用 pdftoppm 渲染为图片后经图片通道查看，或 download 后 OCR。",
				pages,
			)
		}
		return "[note] 该 PDF 没有可抽取的文本层，可能是扫描件；可用 pdftoppm 渲染为图片后经图片通道查看，或 download 后 OCR。"
	}
	return fmt.Sprintf(
		"[note] 以下页面没有文本层: %s；如需这些页的内容，可用 pdftoppm 渲染为图片或 download 后 OCR。",
		joinPageNumbers(pagesWithoutText),
	)
}

func joinPageNumbers(pages []int) string {
	parts := make([]string, 0, len(pages))
	for _, page := range pages {
		parts = append(parts, strconv.Itoa(page))
	}
	return strings.Join(parts, ", ")
}
