package filebrowse

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/wwsheng009/ai-agent-runtime/internal/fsscope"
)

// 预览类型（规划 §5.4 D6）。
const (
	KindText     = "text"
	KindImage    = "image"
	KindBinary   = "binary"
	KindTooLarge = "too_large"
)

// 二进制判定原因，与 frontend/src/lib/file-preview/decode.ts 的 reason 同名。
const (
	ReasonNulByte     = "nul-byte"
	ReasonInvalidUTF8 = "invalid-utf8"
	ReasonTooLarge    = "too_large"
)

// PreviewRequest 是 GET /fs/preview 的入参；MaxBytes<=0 时用服务端默认上限。
type PreviewRequest struct {
	Scope    string
	Path     string
	MaxBytes int
}

// Preview 是截断式预览结果。
type Preview struct {
	Kind       string  `json:"kind"`
	Path       string  `json:"path"`
	AbsPath    string  `json:"abs_path,omitempty"`
	Size       int64   `json:"size"`
	Mtime      int64   `json:"mtime"`
	Mime       string  `json:"mime,omitempty"`
	Text       *string `json:"text,omitempty"`
	DataBase64 string  `json:"data_base64,omitempty"`
	Truncated  bool    `json:"truncated"`
	Reason     string  `json:"reason,omitempty"`
	LimitBytes int64   `json:"limit_bytes,omitempty"`
	LineCount  int     `json:"line_count"`
	Encoding   string  `json:"encoding,omitempty"`
}

// Preview 返回文本/图片/二进制/超限四种分流结果，绝不伪造内容。
func (s *Service) Preview(ctx context.Context, req PreviewRequest) (*Preview, error) {
	if err := ctx.Err(); err != nil {
		return nil, fsscopeReadError("preview", err)
	}
	target, ferr := s.resolver.ResolvePath(ctx, req.Scope, req.Path)
	if ferr != nil {
		return nil, ferr
	}
	if ferr := s.requireFile(target); ferr != nil {
		return nil, ferr
	}
	info, err := os.Stat(target.Abs)
	if err != nil {
		return nil, statError(err, target)
	}
	name := info.Name()
	preview := &Preview{
		Path:    target.Rel,
		AbsPath: target.Abs,
		Size:    info.Size(),
		Mtime:   info.ModTime().Unix(),
		Mime:    MimeForName(name, false),
	}

	if isImageName(name) {
		if info.Size() > s.limits.PreviewImageBytes {
			preview.Kind = KindTooLarge
			preview.Reason = ReasonTooLarge
			preview.LimitBytes = s.limits.PreviewImageBytes
			return preview, nil
		}
		data, readErr := os.ReadFile(target.Abs)
		if readErr != nil {
			return nil, readFileError(readErr, target)
		}
		preview.Kind = KindImage
		preview.DataBase64 = base64.StdEncoding.EncodeToString(data)
		return preview, nil
	}

	limit := s.previewTextLimit(req.MaxBytes)
	truncated := info.Size() > limit
	head, readErr := readHead(target.Abs, limit)
	if readErr != nil {
		return nil, readFileError(readErr, target)
	}
	if truncated {
		head = trimIncompleteTail(head)
	}
	if kind, reason := classifyPreviewBytes(head); kind == KindBinary {
		preview.Kind = KindBinary
		preview.Reason = reason
		preview.Truncated = truncated
		if truncated {
			preview.LimitBytes = limit
		}
		return preview, nil
	}
	text := strings.TrimPrefix(string(head), "\uFEFF")
	preview.Kind = KindText
	preview.Text = &text
	preview.Truncated = truncated
	preview.Encoding = "utf-8"
	preview.LineCount = CountPreviewLines(text)
	if truncated {
		preview.LimitBytes = limit
	}
	return preview, nil
}

func (s *Service) previewTextLimit(requested int) int64 {
	limit := s.limits.PreviewTextBytes
	if requested > 0 && int64(requested) < limit {
		limit = int64(requested)
	}
	return limit
}

// classifyPreviewBytes 与 frontend/src/lib/file-preview/decode.ts 完全同口径：
// 先查 NUL 字节，再查 UTF-8 合法性。
func classifyPreviewBytes(data []byte) (string, string) {
	if len(data) == 0 {
		return KindText, ""
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return KindBinary, ReasonNulByte
	}
	if !utf8.Valid(data) {
		return KindBinary, ReasonInvalidUTF8
	}
	return KindText, ""
}

// CountPreviewLines 行数口径与 decode.ts 的 countPreviewLines 一致：
// 末行换行不计入新行；空文本为 0 行。
func CountPreviewLines(text string) int {
	if text == "" {
		return 0
	}
	normalized := strings.TrimSuffix(text, "\n")
	if normalized == "" {
		return 0
	}
	count := 1
	for index := 0; index < len(normalized); index++ {
		if normalized[index] == '\n' {
			count++
		}
	}
	return count
}

func readHead(absPath string, limit int64) ([]byte, error) {
	file, err := os.Open(absPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	buffer := make([]byte, limit)
	read, err := io.ReadFull(file, buffer)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, err
	}
	return buffer[:read], nil
}

// trimIncompleteTail 去掉末尾被截断切开的 UTF-8 序列，避免把「截断」误判为「非法 UTF-8」。
func trimIncompleteTail(data []byte) []byte {
	for index := 1; index <= utf8.UTFMax && index <= len(data); index++ {
		current := data[len(data)-index]
		if current < utf8.RuneSelf {
			return data // 遇到 ASCII 首字节，说明末尾没有不完整序列
		}
		if current&0xC0 != 0xC0 {
			continue // 继续字节，继续向前找序列首字节
		}
		if runeSize(current) == index {
			return data // 序列完整
		}
		return data[:len(data)-index]
	}
	return data
}

func runeSize(lead byte) int {
	switch {
	case lead&0x80 == 0:
		return 1
	case lead&0xE0 == 0xC0:
		return 2
	case lead&0xF0 == 0xE0:
		return 3
	case lead&0xF8 == 0xF0:
		return 4
	default:
		return 0
	}
}

func readFileError(err error, target *fsscope.Target) *fsscope.Error {
	switch {
	case os.IsNotExist(err):
		return fsscope.NewErrorf(fsscope.CodePathNotFound, 404, "path does not exist: %s", displayPath(target))
	case os.IsPermission(err):
		return fsscope.NewErrorf(fsscope.CodePathPermission, 403, "permission denied: %s", displayPath(target))
	default:
		return fsscope.NewErrorf(fsscope.CodeFSReadFailed, 500, "read %s failed: %v", displayPath(target), err)
	}
}
