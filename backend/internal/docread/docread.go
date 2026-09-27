// Package docread 实现文档抽取管线：先按 magic bytes（扩展名兜底）识别文档类型，
// 再调用系统已有的 pdftotext / pandoc / soffice 把文档渲染成纯 Markdown 正文。
//
// 约定（对齐 docs/analysis/commandcode-read-tool-design-borrowing-20260926.md §3.9）：
//   - Render.Markdown 只包含正文；页码、转换器、降级原因等元信息只写入 Metadata；
//   - 探测不到转换器（或 zip/binary 这类没有抽取器的类型）时返回 ErrUnsupported，
//     调用方用 MIMENote 输出一行 MIME 提示即可；
//   - 只依赖标准库；外部命令探测/执行通过包级变量 lookPath / runCommand 注入，
//     测试可以在包内覆写（不要在测试里并发改写这两个变量）。
//
// 命名说明：需求同时要求 `type Render` 与 `func Render`，而 Go 不允许包级类型与
// 函数同名（redeclared in this block）。本包保留父会话的调用入口 func Render，
// 结果类型沿用 §3.9 的名称 DocumentRender，并额外提供别名 RenderResult。
package docread

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// DocumentRender 是文档抽取结果（§3.9 的 DocumentRender；因与 func Render 同名
// 冲突，需求中的类型名 Render 无法在此使用）。
type DocumentRender struct {
	// Markdown 是纯正文（标题/列表/表格等结构保留），不含页码/转换器/降级说明。
	Markdown string
	// Metadata 始终包含 doc_kind/doc_mime/doc_size/doc_degraded；
	// 转换成功时含 doc_converter，PDF 另有 doc_pages/doc_pages_without_text 等。
	Metadata map[string]interface{}
}

// RenderResult 是 DocumentRender 的别名，便于按“Render 的结果”命名使用。
type RenderResult = DocumentRender

// Probe 描述一次文档类型探测的结果。
type Probe struct {
	Kind      string // pdf|docx|pptx|xlsx|odt|epub|rtf|svg|zip|binary|text
	MIME      string
	Converter string // pdftotext|pandoc|soffice|""
	Supported bool   // 探测到可用转换器（或 svg/text 走内建）
}

// ErrUnsupported 表示当前环境没有可用抽取器：类型本身不可抽取（zip/binary），
// 或所需转换器不在 PATH 上。此时 Render 返回的 Metadata 仍带
// doc_kind/doc_mime/doc_size/doc_degraded/doc_reason，调用方据此输出 MIMENote。
var ErrUnsupported = errors.New("docread: no extractor available for this document")

// maxDocSourceBytes 是 svg/text 原生渲染路径单次读入内存的源文件上限。
// 这两条路径要把源文件整体物化后交给窗口读取，没有转换器路径那样的流式
// 机会（转换器路径由 maxConverterOutputBytes 限幅），超过上限直接返回错误，
// 而不是把整个超大文件读进内存。与 viewNotebookMaxBytes 一致，允许包内测试
// 覆写成小上限验证边界；生产路径只读，不要在 goroutine 里改写。
var maxDocSourceBytes int64 = 64 << 20

// Detect 识别文档类型并探测可用转换器。
//
// head 是文件前缀（可为 nil；为 nil 时会尝试从 path 读取）。识别顺序为
// magic bytes 优先、扩展名兜底：%PDF- → pdf；zip 内含 [Content_Types].xml 时按
// 首个 word//ppt//xl/ 条目细分 docx/pptx/xlsx；zip 内 mimetype 以
// application/epub+zip 开头 → epub、以 application/vnd.oasis.opendocument 开头
// → odt；{\rtf → rtf；文本且含 <svg → svg；其余 zip → zip，无法识别 → binary/text。
func Detect(path string, head []byte) Probe {
	if len(head) == 0 && path != "" {
		head = readHead(path, sniffLen)
	}
	kind := detectKind(path, head)
	probe := Probe{Kind: kind, MIME: mimeForKind(kind)}
	switch kind {
	case "svg", "text", "pptx", "odp":
		// pptx/odp render from their own zip+XML container; no external
		// converter is involved (2026-09-27 review H13).
		probe.Supported = true
	case "pdf", "docx", "odt", "ods", "rtf", "epub", "xlsx":
		if tool := converterForKind(kind); tool != "" {
			if _, err := lookPath(tool); err == nil {
				probe.Converter = tool
				probe.Supported = true
			}
		}
	}
	return probe
}

// Render 把 path 指向的文档抽取成 Markdown。
//
// 错误语义：
//   - ErrUnsupported：zip/binary 没有抽取器，或 pdf/docx/... 探测不到转换器；
//     此时 Metadata 的 doc_reason 为 unsupported_kind / no_converter；
//   - 其它错误：读取失败、svg/text 源文件超过 maxDocSourceBytes，或外部命令
//     执行失败（错误里带转换器与 stderr 摘要）。
func Render(ctx context.Context, path string) (DocumentRender, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	info, err := os.Stat(path)
	if err != nil {
		return DocumentRender{}, fmt.Errorf("docread: stat %s: %w", path, err)
	}
	if info.IsDir() {
		return DocumentRender{}, fmt.Errorf("docread: %s is a directory", path)
	}

	probe := Detect(path, nil)
	meta := map[string]interface{}{
		"doc_kind": probe.Kind,
		"doc_mime": probe.MIME,
		"doc_size": info.Size(),
	}

	switch probe.Kind {
	case "svg", "text":
		// 内建通道：svg / 纯文本原样返回，不做任何改写；源文件要整体驻留
		// 内存，按 maxDocSourceBytes 限幅读取（转换器路径由 maxConverterOutputBytes 限幅）。
		data, err := readDocSource(path, info.Size())
		if err != nil {
			meta["doc_degraded"] = true
			meta["doc_reason"] = "read_error"
			return DocumentRender{Metadata: meta}, err
		}
		meta["doc_degraded"] = false
		return DocumentRender{Markdown: string(data), Metadata: meta}, nil
	case "zip", "binary":
		meta["doc_degraded"] = true
		meta["doc_reason"] = "unsupported_kind"
		return DocumentRender{Metadata: meta}, ErrUnsupported
	}

	if !probe.Supported {
		meta["doc_degraded"] = true
		meta["doc_reason"] = "no_converter"
		return DocumentRender{Metadata: meta}, ErrUnsupported
	}

	meta["doc_converter"] = probe.Converter
	converted, err := convertDocument(ctx, probe, path)
	if err != nil {
		meta["doc_degraded"] = true
		meta["doc_reason"] = "convert_failed"
		return DocumentRender{Metadata: meta}, err
	}
	if converted.Truncated {
		// The converter hit the capture limit: the retained prefix is not the
		// whole document, so the window/EOF/page numbers below describe the
		// prefix. Say so instead of reporting a complete render (2026-09-27
		// review).
		meta["doc_degraded"] = true
		meta["doc_reason"] = "output_truncated"
		meta["doc_output_truncated"] = true
		meta["doc_truncated_at_bytes"] = maxConverterOutputBytes
	} else if converted.SheetTotal > converted.SheetsDelivered {
		// A CSV conversion that delivered fewer sheets than the container
		// declares used to return doc_degraded=false, so the missing worksheets
		// were invisible and unrecoverable (2026-09-27 review H14).
		meta["doc_degraded"] = true
		meta["doc_reason"] = "partial_sheets"
		meta["doc_sheets_total"] = converted.SheetTotal
		meta["doc_sheets_delivered"] = converted.SheetsDelivered
		if len(converted.SheetsMissing) > 0 {
			meta["doc_sheets_missing"] = converted.SheetsMissing
		}
	} else {
		meta["doc_degraded"] = false
	}
	if converted.SheetTotal > 0 {
		meta["doc_sheets_total"] = converted.SheetTotal
		meta["doc_sheets_delivered"] = converted.SheetsDelivered
	}

	if probe.Kind == "pdf" {
		// 页码信息只放元数据，正文保持纯 Markdown。
		pages, pagesWithoutText, textLayer := summarizePDFPages(converted.Raw)
		meta["doc_pages"] = pages
		meta["doc_pages_without_text"] = pagesWithoutText
		if textLayer != "" {
			meta["doc_text_layer"] = textLayer
		}
	}

	return DocumentRender{Markdown: converted.Markdown, Metadata: meta}, nil
}

// readDocSource 读取 svg/text 原生路径的源文件，并保证驻留内存不超过
// maxDocSourceBytes：先用调用方 stat 得到的 size 预检（错误带路径/大小/上限，
// 且不打开文件），再用 io.LimitReader 多读 1 字节兜住 stat 之后文件继续增长的
// 竞态，超限错误同样带路径与上限。
func readDocSource(path string, size int64) ([]byte, error) {
	limit := maxDocSourceBytes
	if size > limit {
		return nil, fmt.Errorf("docread: %s is %d bytes, exceeds the %d-byte limit for svg/text rendering",
			path, size, limit)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("docread: read %s: %w", path, err)
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, fmt.Errorf("docread: read %s: %w", path, err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("docread: %s grew beyond the %d-byte limit for svg/text rendering",
			path, limit)
	}
	return data, nil
}

// MIMENote 生成一行降级提示（不含换行），供调用方在 ErrUnsupported 时输出。
func MIMENote(probe Probe, size int64) string {
	kind := probe.Kind
	if kind == "" {
		kind = "unknown"
	}
	mime := probe.MIME
	if mime == "" {
		mime = "application/octet-stream"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s (%s), %s", kind, mime, formatSize(size))
	switch {
	case kind == "zip":
		b.WriteString("; archive not expanded, download the file to inspect it")
	case kind == "binary":
		b.WriteString("; binary content not rendered, download the file to inspect it")
	case !probe.Supported && converterForKind(kind) != "":
		fmt.Fprintf(&b, "; no converter available (%s not found), download the file and convert it externally",
			converterForKind(kind))
	case !probe.Supported:
		b.WriteString("; no extractor available, download the file and convert it externally")
	}
	return b.String()
}

// formatSize 以 1024 进制输出人类可读大小（MIMENote 一行提示用）。
func formatSize(size int64) string {
	const unit = 1024
	if size < unit {
		return fmt.Sprintf("%d B", size)
	}
	value := float64(size)
	suffixes := []string{"KB", "MB", "GB", "TB"}
	index := -1
	for value >= unit && index < len(suffixes)-1 {
		value /= unit
		index++
	}
	return fmt.Sprintf("%.1f %s", value, suffixes[index])
}
