package docread

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

// docreadTestWrite 写入一个测试文件并返回路径。
func docreadTestWrite(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// docreadTestZip 用 archive/zip 在 t.TempDir 里造最小样本，entries 保持顺序。
func docreadTestZip(t *testing.T, dir, name string, entries [][2]string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	writer := zip.NewWriter(f)
	for _, entry := range entries {
		w, err := writer.Create(entry[0])
		if err != nil {
			t.Fatalf("zip create %s: %v", entry[0], err)
		}
		if _, err := w.Write([]byte(entry[1])); err != nil {
			t.Fatalf("zip write %s: %v", entry[0], err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close %s: %v", path, err)
	}
	return path
}

func TestDetectPDFByMagicAndExtensionFallback(t *testing.T) {
	dir := t.TempDir()

	// magic bytes 优先：扩展名是 .bin 也判 pdf；head 为 nil 时自行读文件。
	byMagic := docreadTestWrite(t, dir, "report.bin", []byte("%PDF-1.7\n1 0 obj\n"))
	probe := Detect(byMagic, nil)
	if probe.Kind != "pdf" || probe.MIME != "application/pdf" {
		t.Fatalf("pdf magic: kind=%q mime=%q, want pdf/application/pdf", probe.Kind, probe.MIME)
	}
	if got := Detect("", []byte("%PDF-1.4\n")).Kind; got != "pdf" {
		t.Fatalf("pdf head only: kind=%q, want pdf", got)
	}

	// 扩展名仅兜底：内容无法识别时按 .pdf 判定。
	byExt := docreadTestWrite(t, dir, "legacy.pdf", []byte("plain text, not a pdf"))
	if got := Detect(byExt, nil).Kind; got != "pdf" {
		t.Fatalf("pdf extension fallback: kind=%q, want pdf", got)
	}
}

func TestDetectZipSubtypes(t *testing.T) {
	cases := []struct {
		name     string
		entries  [][2]string
		want     string
		wantMIME string
	}{
		{
			name: "docx",
			entries: [][2]string{
				{"[Content_Types].xml", `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"/>`},
				{"word/document.xml", "<w:document/>"},
			},
			want:     "docx",
			wantMIME: "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		},
		{
			name: "xlsx",
			entries: [][2]string{
				{"[Content_Types].xml", "<Types/>"},
				{"xl/workbook.xml", "<workbook/>"},
			},
			want:     "xlsx",
			wantMIME: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
		},
		{
			name: "pptx",
			entries: [][2]string{
				{"[Content_Types].xml", "<Types/>"},
				{"ppt/presentation.xml", "<p:presentation/>"},
			},
			want:     "pptx",
			wantMIME: "application/vnd.openxmlformats-officedocument.presentationml.presentation",
		},
		{
			name: "odt",
			entries: [][2]string{
				{"mimetype", "application/vnd.oasis.opendocument.text"},
				{"content.xml", "<office:document-content/>"},
			},
			want:     "odt",
			wantMIME: "application/vnd.oasis.opendocument.text",
		},
		{
			name: "epub",
			entries: [][2]string{
				{"META-INF/container.xml", "<container/>"},
				{"mimetype", "application/epub+zip"},
			},
			want:     "epub",
			wantMIME: "application/epub+zip",
		},
		{
			name:     "plain zip",
			entries:  [][2]string{{"readme.txt", "hello"}},
			want:     "zip",
			wantMIME: "application/zip",
		},
		{
			name: "first matching ooxml entry wins",
			entries: [][2]string{
				{"[Content_Types].xml", "<Types/>"},
				{"ppt/presentation.xml", "<p:presentation/>"},
				{"word/document.xml", "<w:document/>"},
			},
			want:     "pptx",
			wantMIME: "application/vnd.openxmlformats-officedocument.presentationml.presentation",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := docreadTestZip(t, t.TempDir(), "sample.bin", tc.entries)
			probe := Detect(path, nil)
			if probe.Kind != tc.want {
				t.Fatalf("kind = %q, want %q", probe.Kind, tc.want)
			}
			if tc.wantMIME != "" && probe.MIME != tc.wantMIME {
				t.Fatalf("mime = %q, want %q", probe.MIME, tc.wantMIME)
			}
		})
	}
}

func TestDetectSVGByContentAndExtension(t *testing.T) {
	dir := t.TempDir()
	body := "  <?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<svg xmlns=\"http://www.w3.org/2000/svg\"><rect/></svg>\n"

	byExt := docreadTestWrite(t, dir, "logo.svg", []byte(body))
	probe := Detect(byExt, nil)
	if probe.Kind != "svg" || probe.MIME != "image/svg+xml" {
		t.Fatalf("svg extension: kind=%q mime=%q", probe.Kind, probe.MIME)
	}
	if !probe.Supported || probe.Converter != "" {
		t.Fatalf("svg 应走内建（Supported=true, Converter=\"\"），got %+v", probe)
	}

	// 内容嗅探：非 .svg 扩展名、trim 后含 <svg 即判 svg。
	byContent := docreadTestWrite(t, dir, "drawing.xml", []byte(body))
	if got := Detect(byContent, nil).Kind; got != "svg" {
		t.Fatalf("svg content sniff: kind=%q, want svg", got)
	}

	// 不含 <svg 的普通文本仍是 text。
	plain := docreadTestWrite(t, dir, "notes.txt", []byte("plain notes, nothing to render here\n"))
	if got := Detect(plain, nil).Kind; got != "text" {
		t.Fatalf("plain text: kind=%q, want text", got)
	}
}

func TestDetectRTFMagic(t *testing.T) {
	path := docreadTestWrite(t, t.TempDir(), "note.dat", []byte("{\\rtf1\\ansi hello}\n"))
	probe := Detect(path, []byte("{\\rtf1\\ansi"))
	if probe.Kind != "rtf" || probe.MIME != "application/rtf" {
		t.Fatalf("rtf: kind=%q mime=%q", probe.Kind, probe.MIME)
	}
}

func TestDetectBinaryAndTextFallback(t *testing.T) {
	dir := t.TempDir()

	blob := docreadTestWrite(t, dir, "blob.bin", []byte{0x00, 0x01, 0x02, 0xff, 0xfe, 0x00, 0x10})
	probe := Detect(blob, nil)
	if probe.Kind != "binary" || probe.MIME != "application/octet-stream" {
		t.Fatalf("binary: kind=%q mime=%q", probe.Kind, probe.MIME)
	}
	if probe.Supported {
		t.Fatalf("binary 不应 Supported: %+v", probe)
	}

	noExt := docreadTestWrite(t, dir, "mystery", []byte("hello world\nsecond line\n"))
	if got := Detect(noExt, nil).Kind; got != "text" {
		t.Fatalf("text fallback: kind=%q, want text", got)
	}

	empty := docreadTestWrite(t, dir, "empty", nil)
	emptyProbe := Detect(empty, nil)
	if emptyProbe.Kind != "text" || !emptyProbe.Supported {
		t.Fatalf("empty file: probe=%+v, want text/supported", emptyProbe)
	}
}

func TestDetectZipSubtypeFromCallerHead(t *testing.T) {
	path := docreadTestZip(t, t.TempDir(), "sample.dat", [][2]string{
		{"[Content_Types].xml", "<Types/>"},
		{"xl/workbook.xml", "<workbook/>"},
	})
	// 调用方只传 4 字节 magic 时，仍要打开 path 细分出 xlsx。
	if probe := Detect(path, []byte{'P', 'K', 0x03, 0x04}); probe.Kind != "xlsx" {
		t.Fatalf("kind = %q, want xlsx", probe.Kind)
	}
}
