package docread

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// converterTimeout 是单个外部转换器的执行超时，绑定调用方传入的 ctx。
const converterTimeout = 60 * time.Second

// maxListedPages 是 doc_pages_without_text 最多列出的页码数。
const maxListedPages = 20

// maxConverterOutputBytes 限制单个转换器 stdout/stderr 的抓取量（各自的
// 上限）：文档正文随后要走窗口/分页，没有必要把整个超大输出整体驻留内存。
const maxConverterOutputBytes = 32 << 20

// lookPath 与 runCommand 是包级可替换变量：测试在包内覆写以注入假实现。
// 生产路径只读，不要在 goroutine 里改写。
var (
	lookPath   = exec.LookPath
	runCommand = runCommandDefault
)

// runCommandDefault 绑定 ctx 并设 60s 超时执行命令，返回 stdout/stderr、
// "抓取量是否触顶"与错误。
func runCommandDefault(ctx context.Context, name string, args ...string) (stdout, stderr []byte, truncated bool, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	runCtx, cancel := context.WithTimeout(ctx, converterTimeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, name, args...)
	var stdoutBuf, stderrBuf boundedBuffer
	stdoutBuf.limit = maxConverterOutputBytes
	stderrBuf.limit = maxConverterOutputBytes
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf
	err = cmd.Run()
	if err != nil && runCtx.Err() != nil {
		err = fmt.Errorf("%w: %w", err, runCtx.Err())
	}
	return stdoutBuf.Bytes(), stderrBuf.Bytes(), stdoutBuf.Truncated() || stderrBuf.Truncated(), err
}

// boundedBuffer 是带上限的 io.Writer：超限后继续"消费"写入（返回成功）
// 以免子进程因 EPIPE 提前退出，但只保留前 limit 字节。
type boundedBuffer struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if b.limit <= 0 {
		return len(p), nil
	}
	if remaining := b.limit - b.buf.Len(); remaining > 0 {
		if len(p) <= remaining {
			b.buf.Write(p)
		} else {
			b.buf.Write(p[:remaining])
			b.truncated = true
		}
	} else {
		b.truncated = true
	}
	return len(p), nil
}

func (b *boundedBuffer) Bytes() []byte { return b.buf.Bytes() }

// Truncated reports whether any bytes were dropped after the limit was reached.
// Dropping silently used to let a prefix masquerade as the complete converter
// output, with doc_degraded=false and EOF/page counts computed from the prefix
// (2026-09-27 review).
func (b *boundedBuffer) Truncated() bool {
	if b == nil {
		return false
	}
	return b.truncated || (b.limit > 0 && b.buf.Len() >= b.limit)
}

// converterForKind 返回该类型所需的转换器；svg/text 走内建，返回空串。
func converterForKind(kind string) string {
	switch kind {
	case "pdf":
		return "pdftotext"
	case "docx", "odt", "rtf", "epub":
		return "pandoc"
	case "xlsx", "ods":
		return "soffice"
	// pptx/odp are extracted natively (Impress has no TXT export filter), so
	// they need no converter and must render even on a converter-less host
	// (2026-09-27 review H13).
	default:
		return ""
	}
}

// convertResult 是转换器输出：Markdown 为正文，Raw 保留原始输出供 PDF 分页分析。
type convertResult struct {
	Markdown string
	Raw      string
	// Truncated reports that the converter stream hit the capture limit: the
	// retained prefix is not the whole document.
	Truncated bool
	// SheetTotal / SheetsDelivered / SheetsMissing describe workbook coverage:
	// a CSV conversion that silently delivered only the active sheet used to
	// look like a complete render (2026-09-27 review H14).
	SheetTotal       int
	SheetsDelivered  int
	SheetsMissing    []string
}

// convertDocument 按类型选择转换命令并返回渲染结果。
func convertDocument(ctx context.Context, probe Probe, path string) (convertResult, error) {
	switch probe.Kind {
	case "pdf":
		out, truncated, err := runConverter(ctx, "pdftotext", path, "-")
		if err != nil {
			return convertResult{}, err
		}
		raw := string(out)
		return convertResult{Markdown: pdfMarkdown(raw), Raw: raw, Truncated: truncated}, nil
	case "docx", "odt", "rtf", "epub":
		out, truncated, err := runConverter(ctx, "pandoc", "-f", probe.Kind, "-t", "gfm", path)
		if err != nil {
			return convertResult{}, err
		}
		return convertResult{Markdown: string(out), Raw: string(out), Truncated: truncated}, nil
	case "pptx", "odp":
		// Impress has no TXT export filter: extract the slide text natively
		// (2026-09-27 review H13).
		text, err := extractPresentationText(path, probe.Kind)
		if err != nil {
			return convertResult{}, err
		}
		return convertResult{Markdown: text, Raw: text}, nil
	case "xlsx", "ods":
		return convertWorkbookWithSoffice(ctx, probe.Kind, path)
	default:
		return convertResult{}, fmt.Errorf("docread: no converter for kind %q", probe.Kind)
	}
}

// runConverter 执行外部命令；非零退出码返回带 stderr 摘要的错误。
func runConverter(ctx context.Context, name string, args ...string) ([]byte, bool, error) {
	stdout, stderr, truncated, err := runCommand(ctx, name, args...)
	if err != nil {
		return nil, false, commandError(name, stderr, err)
	}
	return stdout, truncated, nil
}

// commandError 把命令失败与 stderr 摘要包装成一个错误。
func commandError(name string, stderr []byte, err error) error {
	if summary := stderrSummary(stderr); summary != "" {
		return fmt.Errorf("docread: %s failed: %w: %s", name, err, summary)
	}
	return fmt.Errorf("docread: %s failed: %w", name, err)
}

// stderrSummary 取 stderr 首个非空行并截断，够定位问题又不淹没窗口。
func stderrSummary(stderr []byte) string {
	text := strings.TrimSpace(string(stderr))
	if text == "" {
		return ""
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	if index := strings.IndexByte(text, '\n'); index >= 0 {
		text = text[:index]
	}
	const maxRunes = 240
	if runes := []rune(text); len(runes) > maxRunes {
		text = string(runes[:maxRunes]) + "..."
	}
	return strings.TrimSpace(text)
}

// sofficeCSVAllSheets asks LibreOffice for one CSV per worksheet: the trailing
// ExportSheets=-1 token (LO >= 7.2) exports every sheet instead of only the
// active one. Older builds reject or ignore it, so the caller falls back to the
// plain filter and the coverage accounting below still discloses the sheets it
// could not deliver (2026-09-27 review H14).
const sofficeCSVAllSheetsFilter = "csv:Text - txt - csv (StarCalc):44,34,76,1,,0,false,true,true,false,false,-1"

// convertWorkbookWithSoffice converts a spreadsheet to CSV and aggregates every
// sheet file the converter produced, instead of silently consuming a single
// file while the metadata claimed a complete render.
func convertWorkbookWithSoffice(ctx context.Context, kind, path string) (convertResult, error) {
	dir, err := os.MkdirTemp("", "docread-soffice-*")
	if err != nil {
		return convertResult{}, fmt.Errorf("docread: temp dir for soffice: %w", err)
	}
	defer os.RemoveAll(dir)

	declared := declaredSheetNames(path, kind)
	csvFiles, truncated, err := runSofficeCSV(ctx, dir, sofficeCSVAllSheetsFilter, path)
	if err != nil || len(csvFiles) == 0 {
		// Retry with the plain filter: keep the previous single-sheet
		// capability on converters without the all-sheets token.
		csvFiles, truncated, err = runSofficeCSV(ctx, dir, "csv", path)
		if err != nil {
			return convertResult{}, err
		}
	}
	if len(csvFiles) == 0 {
		return convertResult{}, fmt.Errorf("docread: soffice produced no .csv output for %s", filepath.Base(path))
	}

	var out strings.Builder
	total := 0
	delivered := 0
	deliveredNames := make([]string, 0, len(csvFiles))
	for _, file := range csvFiles {
		data, readErr := readConvertedFile(file)
		if readErr != nil {
			return convertResult{}, readErr
		}
		name := strings.TrimSuffix(filepath.Base(file), filepath.Ext(file))
		section := fmt.Sprintf("## Sheet: %s\n%s\n", name, strings.TrimRight(string(data), "\n"))
		if total+len(section) > maxConverterOutputBytes {
			truncated = true
			break
		}
		out.WriteString(section)
		out.WriteString("\n")
		total += len(section)
		delivered++
		deliveredNames = append(deliveredNames, name)
	}
	missing := missingSheetNames(declared, deliveredNames)
	return convertResult{
		Markdown:        strings.TrimRight(out.String(), "\n") + "\n",
		Raw:             out.String(),
		Truncated:       truncated,
		SheetTotal:      len(declared),
		SheetsDelivered: delivered,
		SheetsMissing:   missing,
	}, nil
}

// runSofficeCSV runs one soffice conversion and returns the produced CSV files
// in name order.
func runSofficeCSV(ctx context.Context, dir, filter, path string) ([]string, bool, error) {
	args := []string{
		"--headless",
		"--norestore",
		"--nologo",
		"-env:UserInstallation=" + fileURL(filepath.Join(dir, "profile")),
		"--convert-to", filter,
		"--outdir", dir,
		path,
	}
	_, truncated, err := runConverter(ctx, "soffice", args...)
	if err != nil {
		return nil, false, err
	}
	entries, readErr := os.ReadDir(dir)
	if readErr != nil {
		return nil, truncated, nil
	}
	files := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".csv") {
			continue
		}
		files = append(files, filepath.Join(dir, entry.Name()))
	}
	sort.Strings(files)
	return files, truncated, nil
}

// missingSheetNames matches declared sheet names against the converter's output
// file stems (LibreOffice may prefix them with the workbook base name).
func missingSheetNames(declared, delivered []string) []string {
	if len(declared) == 0 || len(delivered) == 0 {
		return nil
	}
	missing := make([]string, 0)
	for _, sheet := range declared {
		found := false
		for _, name := range delivered {
			if strings.Contains(strings.ToLower(name), strings.ToLower(sheet)) {
				found = true
				break
			}
		}
		if !found {
			missing = append(missing, sheet)
		}
	}
	return missing
}

// fileURL 把本地路径转成 file:// URL（soffice 的 UserInstallation 需要）。
func fileURL(path string) string {
	slash := filepath.ToSlash(path)
	if !strings.HasPrefix(slash, "/") {
		slash = "/" + slash
	}
	return (&url.URL{Scheme: "file", Path: slash}).String()
}

// readConvertedOutput 读取 soffice 的输出文件；导出的扩展名与预期不一致时兜底扫描目录。
// 进程 stdout/stderr 的上限管不到这个文件产物，因此两条回读路径都必须限幅：
// 无界 os.ReadFile 会让任意大的 CSV/TXT 在窗口化之前整体驻留内存
// （2026-09-27 review: 32 MiB+1 字节产物被完整读入）。
func readConvertedOutput(dir, base, wantExt string) ([]byte, error) {
	if data, err := readConvertedFile(filepath.Join(dir, base+wantExt)); err == nil {
		return data, nil
	} else if !os.IsNotExist(err) {
		// A real read failure (including the size ceiling) must not be masked by
		// the extension fallback as "no output produced".
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err == nil {
		var firstErr error
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasPrefix(entry.Name(), base+".") {
				continue
			}
			data, readErr := readConvertedFile(filepath.Join(dir, entry.Name()))
			if readErr == nil {
				return data, nil
			}
			if firstErr == nil && !os.IsNotExist(readErr) {
				firstErr = readErr
			}
		}
		if firstErr != nil {
			return nil, firstErr
		}
	}
	return nil, fmt.Errorf("docread: soffice produced no %s output", wantExt)
}

// readConvertedFile reads one converter product with the same byte ceiling as
// the process streams. Oversize output fails loudly instead of silently keeping
// a prefix the caller would present as the complete document.
func readConvertedFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxConverterOutputBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxConverterOutputBytes {
		return nil, fmt.Errorf(
			"docread: converted output %s exceeds the %d-byte limit; narrow the source or inspect it with shell/jq",
			path, maxConverterOutputBytes,
		)
	}
	return data, nil
}

// summarizePDFPages 依据 pdftotext 输出的 \f 分页符统计页数与缺文本层页。
// pdftotext 每页末尾都会写 \f（含最后一页），因此先丢掉末尾空段再计数；
// 整页仅含空白视为无文本层；全文无文本层时 doc_text_layer = "empty"；
// 页码列表最多列 maxListedPages 个。
func summarizePDFPages(raw string) (pages int, pagesWithoutText []int, textLayer string) {
	if raw == "" {
		// 空输出按“单页且无文本层”处理：PDF 至少有一页。
		return 1, []int{1}, "empty"
	}
	segments := strings.Split(raw, "\f")
	if last := len(segments) - 1; last >= 0 && segments[last] == "" {
		segments = segments[:last]
	}
	pages = len(segments)
	pagesWithoutText = make([]int, 0, len(segments))
	for index, segment := range segments {
		if strings.TrimSpace(segment) == "" {
			pagesWithoutText = append(pagesWithoutText, index+1)
		}
	}
	if pages > 0 && len(pagesWithoutText) == pages {
		textLayer = "empty"
	}
	if len(pagesWithoutText) > maxListedPages {
		pagesWithoutText = pagesWithoutText[:maxListedPages]
	}
	return pages, pagesWithoutText, textLayer
}

// pdfMarkdown 把分页符换成换行并收敛尾部空行，保证正文里不残留 \f。
func pdfMarkdown(raw string) string {
	if raw == "" {
		return ""
	}
	markdown := strings.ReplaceAll(raw, "\f", "\n")
	return strings.TrimRight(markdown, "\n") + "\n"
}
