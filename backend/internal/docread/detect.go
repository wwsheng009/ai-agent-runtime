package docread

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// sniffLen 是自动补读文件前缀时的字节数：足够覆盖 magic、XML 声明与首个 <svg>。
const sniffLen = 4096

var (
	pdfMagic        = []byte("%PDF-")
	rtfMagic        = []byte("{\\rtf")
	utf8BOM         = []byte{0xEF, 0xBB, 0xBF}
	zipLocalMagic   = []byte{'P', 'K', 0x03, 0x04}
	zipEOCDMagic    = []byte{'P', 'K', 0x05, 0x06}
	zipSpannedMagic = []byte{'P', 'K', 0x07, 0x08}
)

// extKinds 是 magic bytes 无法识别时的兜底映射（扩展名仅兜底）。
var extKinds = map[string]string{
	".pdf":  "pdf",
	".docx": "docx",
	".docm": "docx",
	".pptx": "pptx",
	".pptm": "pptx",
	".xlsx": "xlsx",
	".xlsm": "xlsx",
	".odt":  "odt",
	".ods":  "odt",
	".odp":  "odt",
	".epub": "epub",
	".rtf":  "rtf",
	".svg":  "svg",
	".zip":  "zip",
}

// kindMIMEs 是文档类型到 MIME 的映射。
var kindMIMEs = map[string]string{
	"pdf":    "application/pdf",
	"docx":   "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	"pptx":   "application/vnd.openxmlformats-officedocument.presentationml.presentation",
	"xlsx":   "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	"odt":    "application/vnd.oasis.opendocument.text",
	"epub":   "application/epub+zip",
	"rtf":    "application/rtf",
	"svg":    "image/svg+xml",
	"zip":    "application/zip",
	"binary": "application/octet-stream",
	"text":   "text/plain; charset=utf-8",
}

// readHead 读取文件前 n 个字节；读取失败返回 nil（调用方按扩展名/文本兜底）。
func readHead(path string, n int) []byte {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	buf := make([]byte, n)
	read, err := io.ReadFull(f, buf)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return nil
	}
	return buf[:read]
}

// detectKind 执行类型判定：magic bytes 优先，扩展名兜底，最后按文本性区分 text/binary。
func detectKind(path string, head []byte) string {
	switch {
	case bytes.HasPrefix(head, pdfMagic):
		return "pdf"
	case hasZipMagic(head):
		if kind, ok := classifyZip(path); ok {
			return kind
		}
		// zip 结构无法读出时退回扩展名；仍是 PK 开头则至少是 zip。
		if kind := kindFromExt(path); kind != "" {
			return kind
		}
		return "zip"
	case bytes.HasPrefix(trimHead(head), rtfMagic):
		return "rtf"
	case isSVG(head):
		return "svg"
	}
	if kind := kindFromExt(path); kind != "" {
		return kind
	}
	if looksLikeText(head) {
		return "text"
	}
	return "binary"
}

func hasZipMagic(head []byte) bool {
	return bytes.HasPrefix(head, zipLocalMagic) ||
		bytes.HasPrefix(head, zipEOCDMagic) ||
		bytes.HasPrefix(head, zipSpannedMagic)
}

// classifyZip 打开 zip 并按目录内容细分；path 不可读或不是 zip 时返回 ok=false。
func classifyZip(path string) (string, bool) {
	if path == "" {
		return "", false
	}
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.IsDir() {
		return "", false
	}
	reader, err := zip.NewReader(f, info.Size())
	if err != nil {
		return "", false
	}

	subKind := ""
	hasContentTypes := false
	var mimeEntry *zip.File
	for _, entry := range reader.File {
		name := entry.Name
		if subKind == "" {
			// 按 zip 内条目顺序取首个匹配：word/ → docx、ppt/ → pptx、xl/ → xlsx。
			switch {
			case strings.HasPrefix(name, "word/"):
				subKind = "docx"
			case strings.HasPrefix(name, "ppt/"):
				subKind = "pptx"
			case strings.HasPrefix(name, "xl/"):
				subKind = "xlsx"
			}
		}
		if name == "[Content_Types].xml" {
			hasContentTypes = true
		}
		if name == "mimetype" && mimeEntry == nil {
			mimeEntry = entry
		}
	}

	if hasContentTypes && subKind != "" {
		return subKind, true
	}
	if mimeEntry != nil {
		switch content := readZipEntryText(mimeEntry, 128); {
		case strings.HasPrefix(content, "application/epub+zip"):
			return "epub", true
		case strings.HasPrefix(content, "application/vnd.oasis.opendocument"):
			return "odt", true
		}
	}
	// 可读但不是已知 OOXML/ODF/EPUB 布局：仍然是一个 zip。
	return "zip", true
}

// readZipEntryText 读取 zip 条目前 max 字节并去掉首尾空白（mimetype 判定用）。
func readZipEntryText(entry *zip.File, max int) string {
	rc, err := entry.Open()
	if err != nil {
		return ""
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, int64(max)))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// trimHead 去掉 UTF-8 BOM 与首尾空白。
func trimHead(head []byte) []byte {
	return bytes.TrimSpace(bytes.TrimPrefix(head, utf8BOM))
}

// isSVG 判定 xml/文本且（trim 后）含 <svg。
func isSVG(head []byte) bool {
	trimmed := trimHead(head)
	if len(trimmed) == 0 || !looksLikeText(trimmed) {
		return false
	}
	return bytes.Contains(bytes.ToLower(trimmed), []byte("<svg"))
}

// looksLikeText 区分文本与二进制：含 NUL、控制字符占比过高或非 UTF-8 视为二进制。
// 前缀可能在多字节 UTF-8 字符中间被截断，因此容忍末尾最多 3 个残字节。
func looksLikeText(head []byte) bool {
	if len(head) == 0 {
		return true
	}
	if bytes.IndexByte(head, 0) >= 0 {
		return false
	}
	control := 0
	for _, b := range head {
		if b < 0x20 && b != '\t' && b != '\n' && b != '\r' && b != '\f' {
			control++
		}
	}
	if control*10 > len(head) {
		return false
	}
	if utf8.Valid(head) {
		return true
	}
	for trim := 1; trim <= 3 && trim < len(head); trim++ {
		if utf8.Valid(head[:len(head)-trim]) {
			return true
		}
	}
	return false
}

// kindFromExt 是扩展名兜底映射。
func kindFromExt(path string) string {
	if path == "" {
		return ""
	}
	return extKinds[strings.ToLower(filepath.Ext(path))]
}

// mimeForKind 返回类型对应的 MIME，未知类型按二进制处理。
func mimeForKind(kind string) string {
	if mime, ok := kindMIMEs[kind]; ok {
		return mime
	}
	return "application/octet-stream"
}
