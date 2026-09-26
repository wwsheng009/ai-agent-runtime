package docread

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
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

// runCommandDefault 绑定 ctx 并设 60s 超时执行命令，返回 stdout/stderr 与错误。
func runCommandDefault(ctx context.Context, name string, args ...string) (stdout, stderr []byte, err error) {
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
	return stdoutBuf.Bytes(), stderrBuf.Bytes(), err
}

// boundedBuffer 是带上限的 io.Writer：超限后继续"消费"写入（返回成功）
// 以免子进程因 EPIPE 提前退出，但只保留前 limit 字节。
type boundedBuffer struct {
	buf   bytes.Buffer
	limit int
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
		}
	}
	return len(p), nil
}

func (b *boundedBuffer) Bytes() []byte { return b.buf.Bytes() }

// converterForKind 返回该类型所需的转换器；svg/text 走内建，返回空串。
func converterForKind(kind string) string {
	switch kind {
	case "pdf":
		return "pdftotext"
	case "docx", "odt", "rtf", "epub":
		return "pandoc"
	case "pptx", "xlsx":
		return "soffice"
	default:
		return ""
	}
}

// convertResult 是转换器输出：Markdown 为正文，Raw 保留原始输出供 PDF 分页分析。
type convertResult struct {
	Markdown string
	Raw      string
}

// convertDocument 按类型选择转换命令并返回渲染结果。
func convertDocument(ctx context.Context, probe Probe, path string) (convertResult, error) {
	switch probe.Kind {
	case "pdf":
		out, err := runConverter(ctx, "pdftotext", path, "-")
		if err != nil {
			return convertResult{}, err
		}
		raw := string(out)
		return convertResult{Markdown: pdfMarkdown(raw), Raw: raw}, nil
	case "docx", "odt", "rtf", "epub":
		out, err := runConverter(ctx, "pandoc", "-f", probe.Kind, "-t", "gfm", path)
		if err != nil {
			return convertResult{}, err
		}
		return convertResult{Markdown: string(out), Raw: string(out)}, nil
	case "pptx", "xlsx":
		return convertWithSoffice(ctx, probe.Kind, path)
	default:
		return convertResult{}, fmt.Errorf("docread: no converter for kind %q", probe.Kind)
	}
}

// runConverter 执行外部命令；非零退出码返回带 stderr 摘要的错误。
func runConverter(ctx context.Context, name string, args ...string) ([]byte, error) {
	stdout, stderr, err := runCommand(ctx, name, args...)
	if err != nil {
		return nil, commandError(name, stderr, err)
	}
	return stdout, nil
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

// convertWithSoffice 通过 soffice 把 xlsx 转 csv、pptx 转 txt，输出到临时目录再读回。
func convertWithSoffice(ctx context.Context, kind, path string) (convertResult, error) {
	dir, err := os.MkdirTemp("", "docread-soffice-*")
	if err != nil {
		return convertResult{}, fmt.Errorf("docread: temp dir for soffice: %w", err)
	}
	defer os.RemoveAll(dir)

	filter, outExt := "csv", ".csv"
	if kind == "pptx" {
		filter, outExt = "txt", ".txt"
	}
	args := []string{
		"--headless",
		"--norestore",
		"--nologo",
		"-env:UserInstallation=" + fileURL(filepath.Join(dir, "profile")),
		"--convert-to", filter,
		"--outdir", dir,
		path,
	}
	if _, err := runConverter(ctx, "soffice", args...); err != nil {
		return convertResult{}, err
	}

	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	data, err := readConvertedOutput(dir, base, outExt)
	if err != nil {
		return convertResult{}, err
	}
	return convertResult{Markdown: string(data), Raw: string(data)}, nil
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
func readConvertedOutput(dir, base, wantExt string) ([]byte, error) {
	if data, err := os.ReadFile(filepath.Join(dir, base+wantExt)); err == nil {
		return data, nil
	}
	entries, err := os.ReadDir(dir)
	if err == nil {
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasPrefix(entry.Name(), base+".") {
				continue
			}
			if data, err := os.ReadFile(filepath.Join(dir, entry.Name())); err == nil {
				return data, nil
			}
		}
	}
	return nil, fmt.Errorf("docread: soffice produced no %s output", wantExt)
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
