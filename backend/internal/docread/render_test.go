package docread

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// docreadTestLookPath 覆写包级 lookPath：只有 available 里的命令视为存在。
func docreadTestLookPath(t *testing.T, available ...string) {
	t.Helper()
	previous := lookPath
	lookPath = func(file string) (string, error) {
		for _, name := range available {
			if file == name {
				return file, nil
			}
		}
		return "", exec.ErrNotFound
	}
	t.Cleanup(func() { lookPath = previous })
}

// docreadTestRunCommand 覆写包级 runCommand，注入确定性输出。
func docreadTestRunCommand(t *testing.T, fn func(name string, args []string) (stdout, stderr []byte, err error)) {
	t.Helper()
	docreadTestRunCommandWithTruncation(t, func(name string, args []string) (stdout, stderr []byte, truncated bool, err error) {
		stdout, stderr, err = fn(name, args)
		return stdout, stderr, false, err
	})
}

// docreadTestRunCommandWithTruncation additionally reports the capture-limit
// signal so the truncated-stream disclosure can be tested without a real
// converter.
func docreadTestRunCommandWithTruncation(t *testing.T, fn func(name string, args []string) (stdout, stderr []byte, truncated bool, err error)) {
	t.Helper()
	previous := runCommand
	runCommand = func(_ context.Context, name string, args ...string) ([]byte, []byte, bool, error) {
		return fn(name, args)
	}
	t.Cleanup(func() { runCommand = previous })
}

// docreadTestDocx 造一个最小 docx 样本。
func docreadTestDocx(t *testing.T, dir, name string) string {
	t.Helper()
	return docreadTestZip(t, dir, name, [][2]string{
		{"[Content_Types].xml", "<Types/>"},
		{"word/document.xml", "<w:document/>"},
	})
}

func TestSummarizePDFPages(t *testing.T) {
	cases := []struct {
		name      string
		raw       string
		pages     int
		without   []int
		textLayer string
	}{
		{"single page with text", "hello\n\f", 1, []int{}, ""},
		{"middle page missing text", "one\f\fthree\f", 3, []int{2}, ""},
		{"no trailing separator", "one\ftwo", 2, []int{}, ""},
		{"whitespace-only page counts as empty", "one\f \n\t\ftwo\f", 3, []int{2}, ""},
		{"full document without text layer", "\f\f\f", 3, []int{1, 2, 3}, "empty"},
		{"empty output is a single empty page", "", 1, []int{1}, "empty"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pages, without, textLayer := summarizePDFPages(tc.raw)
			if pages != tc.pages {
				t.Fatalf("pages = %d, want %d", pages, tc.pages)
			}
			if !reflect.DeepEqual(without, tc.without) {
				t.Fatalf("pages_without_text = %v, want %v", without, tc.without)
			}
			if textLayer != tc.textLayer {
				t.Fatalf("text_layer = %q, want %q", textLayer, tc.textLayer)
			}
		})
	}
}

func TestSummarizePDFPagesCapsListedPages(t *testing.T) {
	pages, without, textLayer := summarizePDFPages(strings.Repeat("\f", 25))
	if pages != 25 {
		t.Fatalf("pages = %d, want 25", pages)
	}
	if len(without) != maxListedPages {
		t.Fatalf("listed pages = %d, want %d (cap)", len(without), maxListedPages)
	}
	if without[len(without)-1] != maxListedPages {
		t.Fatalf("last listed page = %d, want %d", without[len(without)-1], maxListedPages)
	}
	if textLayer != "empty" {
		t.Fatalf("text_layer = %q, want empty", textLayer)
	}
}

func TestRenderPDFFakeConverter(t *testing.T) {
	dir := t.TempDir()
	raw := []byte("%PDF-1.4\nscan\n")
	path := docreadTestWrite(t, dir, "scan.pdf", raw)

	docreadTestLookPath(t, "pdftotext")
	var gotName string
	var gotArgs []string
	docreadTestRunCommand(t, func(name string, args []string) ([]byte, []byte, error) {
		gotName = name
		gotArgs = append([]string(nil), args...)
		return []byte("Page one\n\f\fPage three\n\f"), nil, nil
	})

	result, err := Render(context.Background(), path)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if gotName != "pdftotext" {
		t.Fatalf("command = %q, want pdftotext", gotName)
	}
	if !reflect.DeepEqual(gotArgs, []string{path, "-"}) {
		t.Fatalf("args = %v, want [%q -]", gotArgs, path)
	}
	if !strings.Contains(result.Markdown, "Page one") || !strings.Contains(result.Markdown, "Page three") {
		t.Fatalf("Markdown 缺少正文: %q", result.Markdown)
	}
	if strings.ContainsRune(result.Markdown, '\f') {
		t.Fatalf("Markdown 不应包含分页符: %q", result.Markdown)
	}

	wantMeta := map[string]interface{}{
		"doc_kind":               "pdf",
		"doc_mime":               "application/pdf",
		"doc_size":               int64(len(raw)),
		"doc_degraded":           false,
		"doc_converter":          "pdftotext",
		"doc_pages":              3,
		"doc_pages_without_text": []int{2},
	}
	for key, want := range wantMeta {
		if got := result.Metadata[key]; !reflect.DeepEqual(got, want) {
			t.Fatalf("Metadata[%q] = %#v, want %#v", key, got, want)
		}
	}
	if _, ok := result.Metadata["doc_text_layer"]; ok {
		t.Fatalf("有文本层时不应写 doc_text_layer: %#v", result.Metadata)
	}
}

func TestRenderPDFMissingTextLayer(t *testing.T) {
	path := docreadTestWrite(t, t.TempDir(), "scanned.pdf", []byte("%PDF-1.5\n"))
	docreadTestLookPath(t, "pdftotext")
	docreadTestRunCommand(t, func(name string, args []string) ([]byte, []byte, error) {
		return []byte("\f\f\f"), nil, nil
	})

	result, err := Render(context.Background(), path)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if strings.TrimSpace(result.Markdown) != "" {
		t.Fatalf("扫描件正文应为空，got %q", result.Markdown)
	}
	if got := result.Metadata["doc_pages"]; got != 3 {
		t.Fatalf("doc_pages = %#v, want 3", got)
	}
	if got := result.Metadata["doc_pages_without_text"]; !reflect.DeepEqual(got, []int{1, 2, 3}) {
		t.Fatalf("doc_pages_without_text = %#v, want [1 2 3]", got)
	}
	if got := result.Metadata["doc_text_layer"]; got != "empty" {
		t.Fatalf("doc_text_layer = %#v, want empty", got)
	}
}

func TestRenderPandocFakeConverter(t *testing.T) {
	path := docreadTestDocx(t, t.TempDir(), "memo.docx")
	docreadTestLookPath(t, "pandoc")
	var gotArgs []string
	docreadTestRunCommand(t, func(name string, args []string) ([]byte, []byte, error) {
		if name != "pandoc" {
			return nil, nil, errors.New("unexpected command " + name)
		}
		gotArgs = append([]string(nil), args...)
		return []byte("# Memo\n\nbody\n"), nil, nil
	})

	result, err := Render(context.Background(), path)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if result.Markdown != "# Memo\n\nbody\n" {
		t.Fatalf("Markdown = %q", result.Markdown)
	}
	if !reflect.DeepEqual(gotArgs, []string{"-f", "docx", "-t", "gfm", path}) {
		t.Fatalf("args = %v", gotArgs)
	}
	if got := result.Metadata["doc_kind"]; got != "docx" {
		t.Fatalf("doc_kind = %#v, want docx", got)
	}
	if got := result.Metadata["doc_converter"]; got != "pandoc" {
		t.Fatalf("doc_converter = %#v, want pandoc", got)
	}
	if got := result.Metadata["doc_degraded"]; got != false {
		t.Fatalf("doc_degraded = %#v, want false", got)
	}
}

func TestRenderSofficeFakeConverter(t *testing.T) {
	path := docreadTestZip(t, t.TempDir(), "sheet.xlsx", [][2]string{
		{"[Content_Types].xml", "<Types/>"},
		{"xl/workbook.xml", "<workbook/>"},
	})
	docreadTestLookPath(t, "soffice")
	docreadTestRunCommand(t, func(name string, args []string) ([]byte, []byte, error) {
		if name != "soffice" {
			return nil, nil, errors.New("unexpected command " + name)
		}
		outDir := ""
		for index, arg := range args {
			if arg == "--outdir" && index+1 < len(args) {
				outDir = args[index+1]
			}
		}
		if outDir == "" {
			return nil, []byte("soffice: missing --outdir"), errors.New("exit status 1")
		}
		if err := os.WriteFile(filepath.Join(outDir, "sheet.csv"), []byte("a,b\n1,2\n"), 0o644); err != nil {
			return nil, nil, err
		}
		return nil, nil, nil
	})

	result, err := Render(context.Background(), path)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if result.Markdown != "a,b\n1,2\n" {
		t.Fatalf("Markdown = %q", result.Markdown)
	}
	if got := result.Metadata["doc_kind"]; got != "xlsx" {
		t.Fatalf("doc_kind = %#v, want xlsx", got)
	}
	if got := result.Metadata["doc_converter"]; got != "soffice" {
		t.Fatalf("doc_converter = %#v, want soffice", got)
	}
}

// TestRenderSofficeFakeConverterForOpenDocumentFamily: ODS 走 csv、ODP 走 txt，
// 且 doc_kind 如实报告真实格式（此前两者都按 odt→pandoc 处理并失败）。
func TestRenderSofficeFakeConverterForOpenDocumentFamily(t *testing.T) {
	cases := []struct {
		fileName string
		mime     string
		wantKind string
		wantArg  string
		outName  string
		payload  string
	}{
		{"table.ods", "application/vnd.oasis.opendocument.spreadsheet", "ods", "csv", "table.csv", "a,b\n1,2\n"},
		{"slides.odp", "application/vnd.oasis.opendocument.presentation", "odp", "txt", "slides.txt", "slide one\n"},
	}
	for _, tc := range cases {
		t.Run(tc.wantKind, func(t *testing.T) {
			path := docreadTestZip(t, t.TempDir(), tc.fileName, [][2]string{
				{"mimetype", tc.mime},
				{"content.xml", "<office:document-content/>"},
			})
			docreadTestLookPath(t, "soffice")
			var gotFilter string
			docreadTestRunCommand(t, func(name string, args []string) ([]byte, []byte, error) {
				if name != "soffice" {
					return nil, nil, errors.New("unexpected command " + name)
				}
				outDir := ""
				for index, arg := range args {
					if arg == "--convert-to" && index+1 < len(args) {
						gotFilter = args[index+1]
					}
					if arg == "--outdir" && index+1 < len(args) {
						outDir = args[index+1]
					}
				}
				if outDir == "" {
					return nil, []byte("soffice: missing --outdir"), errors.New("exit status 1")
				}
				if err := os.WriteFile(filepath.Join(outDir, tc.outName), []byte(tc.payload), 0o644); err != nil {
					return nil, nil, err
				}
				return nil, nil, nil
			})

			result, err := Render(context.Background(), path)
			if err != nil {
				t.Fatalf("Render: %v", err)
			}
			if gotFilter != tc.wantArg {
				t.Fatalf("filter = %q, want %q", gotFilter, tc.wantArg)
			}
			if result.Markdown != tc.payload {
				t.Fatalf("Markdown = %q, want %q", result.Markdown, tc.payload)
			}
			if got := result.Metadata["doc_kind"]; got != tc.wantKind {
				t.Fatalf("doc_kind = %#v, want %s", got, tc.wantKind)
			}
			if got := result.Metadata["doc_converter"]; got != "soffice" {
				t.Fatalf("doc_converter = %#v, want soffice", got)
			}
		})
	}
}

func TestRenderNoConverterUnsupported(t *testing.T) {
	dir := t.TempDir()
	docxPath := docreadTestDocx(t, dir, "contract.docx")
	pdfPath := docreadTestWrite(t, dir, "scan.pdf", []byte("%PDF-1.5\n"))

	docreadTestLookPath(t) // 环境里没有任何转换器

	cases := []struct {
		name     string
		path     string
		wantKind string
		wantMIME string
		wantTool string
	}{
		{"docx", docxPath, "docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", "pandoc"},
		{"pdf", pdfPath, "pdf", "application/pdf", "pdftotext"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			info, err := os.Stat(tc.path)
			if err != nil {
				t.Fatalf("stat: %v", err)
			}
			result, err := Render(context.Background(), tc.path)
			if !errors.Is(err, ErrUnsupported) {
				t.Fatalf("err = %v, want ErrUnsupported", err)
			}
			if err != ErrUnsupported {
				t.Fatalf("应原样返回 ErrUnsupported（供调用方 == 比较）: %v", err)
			}
			if result.Markdown != "" {
				t.Fatalf("降级时 Markdown 应为空，got %q", result.Markdown)
			}

			wantMeta := map[string]interface{}{
				"doc_kind":     tc.wantKind,
				"doc_mime":     tc.wantMIME,
				"doc_size":     info.Size(),
				"doc_degraded": true,
				"doc_reason":   "no_converter",
			}
			for key, want := range wantMeta {
				if got := result.Metadata[key]; !reflect.DeepEqual(got, want) {
					t.Fatalf("Metadata[%q] = %#v, want %#v", key, got, want)
				}
			}
			if _, ok := result.Metadata["doc_converter"]; ok {
				t.Fatalf("无转换器时不应写 doc_converter: %#v", result.Metadata)
			}

			note := MIMENote(Detect(tc.path, nil), info.Size())
			if strings.ContainsRune(note, '\n') {
				t.Fatalf("MIMENote 必须是单行: %q", note)
			}
			if !strings.Contains(note, tc.wantKind) || !strings.Contains(note, tc.wantTool) {
				t.Fatalf("MIMENote 缺少类型/转换器提示: %q", note)
			}
		})
	}
}

func TestRenderBinaryUnsupported(t *testing.T) {
	path := docreadTestWrite(t, t.TempDir(), "blob.bin", []byte{0x00, 0x01, 0x02, 0x03, 0xff})
	result, err := Render(context.Background(), path)
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
	if got := result.Metadata["doc_kind"]; got != "binary" {
		t.Fatalf("doc_kind = %#v, want binary", got)
	}
	if got := result.Metadata["doc_degraded"]; got != true {
		t.Fatalf("doc_degraded = %#v, want true", got)
	}
	if got := result.Metadata["doc_reason"]; got != "unsupported_kind" {
		t.Fatalf("doc_reason = %#v, want unsupported_kind", got)
	}
	note := MIMENote(Detect(path, nil), 5)
	if !strings.Contains(note, "binary") || !strings.Contains(note, "download") {
		t.Fatalf("MIMENote 缺少二进制恢复路径: %q", note)
	}
}

func TestRenderSVGAndTextPassthrough(t *testing.T) {
	dir := t.TempDir()

	svg := []byte("<svg xmlns=\"http://www.w3.org/2000/svg\"><rect/></svg>\n")
	svgPath := docreadTestWrite(t, dir, "logo.svg", svg)
	svgResult, err := Render(context.Background(), svgPath)
	if err != nil {
		t.Fatalf("Render svg: %v", err)
	}
	if svgResult.Markdown != string(svg) {
		t.Fatalf("svg Markdown = %q, want 原文", svgResult.Markdown)
	}
	wantSVGMeta := map[string]interface{}{
		"doc_kind":     "svg",
		"doc_mime":     "image/svg+xml",
		"doc_size":     int64(len(svg)),
		"doc_degraded": false,
	}
	for key, want := range wantSVGMeta {
		if got := svgResult.Metadata[key]; !reflect.DeepEqual(got, want) {
			t.Fatalf("svg Metadata[%q] = %#v, want %#v", key, got, want)
		}
	}
	if _, ok := svgResult.Metadata["doc_converter"]; ok {
		t.Fatalf("内建通道不应写 doc_converter: %#v", svgResult.Metadata)
	}

	textBody := []byte("# notes\n\nhello\n")
	textPath := docreadTestWrite(t, dir, "notes.md", textBody)
	textResult, err := Render(context.Background(), textPath)
	if err != nil {
		t.Fatalf("Render text: %v", err)
	}
	if textResult.Markdown != string(textBody) {
		t.Fatalf("text Markdown = %q", textResult.Markdown)
	}
	if got := textResult.Metadata["doc_kind"]; got != "text" {
		t.Fatalf("doc_kind = %#v, want text", got)
	}
	if got := textResult.Metadata["doc_degraded"]; got != false {
		t.Fatalf("doc_degraded = %#v, want false", got)
	}
}

func TestRenderConverterFailureIncludesStderr(t *testing.T) {
	path := docreadTestDocx(t, t.TempDir(), "broken.docx")
	docreadTestLookPath(t, "pandoc")
	docreadTestRunCommand(t, func(name string, args []string) ([]byte, []byte, error) {
		return nil, []byte("pandoc: cannot decode input\nsecond line detail\n"), errors.New("exit status 2")
	})

	result, err := Render(context.Background(), path)
	if err == nil {
		t.Fatal("want conversion error")
	}
	if errors.Is(err, ErrUnsupported) {
		t.Fatalf("转换失败不应是 ErrUnsupported: %v", err)
	}
	message := err.Error()
	if !strings.Contains(message, "pandoc failed") {
		t.Fatalf("错误缺少命令名: %v", err)
	}
	if !strings.Contains(message, "cannot decode input") {
		t.Fatalf("错误缺少 stderr 摘要: %v", err)
	}
	if strings.Contains(message, "second line detail") {
		t.Fatalf("stderr 摘要只保留首个非空行: %v", err)
	}
	if got := result.Metadata["doc_reason"]; got != "convert_failed" {
		t.Fatalf("doc_reason = %#v, want convert_failed", got)
	}
	if got := result.Metadata["doc_degraded"]; got != true {
		t.Fatalf("doc_degraded = %#v, want true", got)
	}
	if got := result.Metadata["doc_converter"]; got != "pandoc" {
		t.Fatalf("doc_converter = %#v, want pandoc", got)
	}
}

func TestMIMENoteLine(t *testing.T) {
	docx := MIMENote(Probe{
		Kind:      "docx",
		MIME:      "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		Supported: false,
	}, 2048)
	for _, want := range []string{"docx", "2.0 KB", "pandoc", "download"} {
		if !strings.Contains(docx, want) {
			t.Fatalf("MIMENote(%q) 缺少 %q", docx, want)
		}
	}

	binary := MIMENote(Probe{Kind: "binary", MIME: "application/octet-stream"}, 12)
	if !strings.Contains(binary, "binary") || !strings.Contains(binary, "12 B") || !strings.Contains(binary, "download") {
		t.Fatalf("binary note = %q", binary)
	}

	supported := MIMENote(Probe{Kind: "pdf", MIME: "application/pdf", Converter: "pdftotext", Supported: true}, 5*1024)
	if !strings.Contains(supported, "pdf") || !strings.Contains(supported, "5.0 KB") {
		t.Fatalf("supported note = %q", supported)
	}
	if strings.Contains(supported, "no converter") {
		t.Fatalf("supported note 不应包含降级提示: %q", supported)
	}
}

func TestRunCommandDefaultHonorsContextAndMissingBinary(t *testing.T) {
	if converterTimeout != 60*time.Second {
		t.Fatalf("converterTimeout = %s, want 60s", converterTimeout)
	}

	// ctx 已取消：错误必须能被 errors.Is 命中 context.Canceled。
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, _, err := runCommandDefault(ctx, "docread-no-such-command-xyz"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled ctx err = %v, want context.Canceled", err)
	}

	// 不存在的可执行文件：返回错误而不是 panic。
	if _, _, _, err := runCommandDefault(context.Background(), "docread-no-such-command-xyz"); err == nil {
		t.Fatal("missing binary: want error")
	}
}
