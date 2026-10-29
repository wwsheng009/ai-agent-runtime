package viewcore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore/localio"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore/pathguard"
)

// ReadFileHead returns up to size leading bytes of path. Both the binary note
// and the format sniffers for documents, notebooks and images start from the
// same head read, so it is one implementation instead of one per caller.
//
// It stays path-shaped because four local sniffers call it without a runtime in
// hand; the seam version below is what the shared handler uses, so the same sniff
// works on a file that lives on another machine.
func ReadFileHead(path string, size int) ([]byte, error) {
	return ReadFileHeadFromAccess(context.Background(), localio.Access, path, size)
}

// ReadFileHeadFromAccess reads the leading bytes of one path through the I/O
// seam. A nil accessor means the local filesystem.
func ReadFileHeadFromAccess(ctx context.Context, access toolcore.Access, path string, size int) ([]byte, error) {
	if access == nil {
		access = localio.Access
	}
	file, err := access.Open(ctx, path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	head := make([]byte, size)
	n, err := io.ReadFull(file, head)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return nil, err
	}
	return head[:n], nil
}

// RefusalResult is the shared special-path refusal. It refuses the same names
// with the same wording on every surface: a device, FIFO or reserved name is an
// argument problem with a recovery route, not an internal failure.
func RefusalResult(absPath, reason string) *toolcore.Result {
	result := toolcore.Error(
		toolcore.ErrInvalidArgument,
		pathguard.RefusalNextAction,
		errors.New(pathguard.RefusalMessage(absPath, reason)),
	)
	result.Metadata = map[string]any{
		"file_path":      absPath,
		"path_refused":   true,
		"refusal_reason": reason,
	}
	return result
}

// BinaryNote describes a non-renderable binary file without failing the call:
// one line of MIME + size + a recovery route, so the model can decide whether
// to download it or pick a converter instead of guessing from NUL bytes.
//
// It takes the accessor because its caller - the shared handler - already
// resolved one. Sniffing the head from a different machine than the metadata is
// exactly the split-brain the seam exists to remove: the size would describe the
// remote file while the MIME type came from a local path that shares its name.
func BinaryNote(ctx context.Context, access toolcore.Access, absPath, displayPath string, size int64) *toolcore.Result {
	mimeType := ""
	if head, err := ReadFileHeadFromAccess(ctx, access, absPath, 512); err == nil {
		mimeType = strings.TrimSpace(http.DetectContentType(head))
	}
	if extMIME := strings.TrimSpace(mime.TypeByExtension(strings.ToLower(filepath.Ext(absPath)))); extMIME != "" &&
		(mimeType == "" || mimeType == "application/octet-stream") {
		mimeType = extMIME
	}
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	return &toolcore.Result{
		Success:    true,
		OutputKind: toolcore.KindText,
		Content: fmt.Sprintf(
			"二进制文件: %s\nMIME: %s，大小: %d 字节。\n未附加文本或图像内容；可用 download 拉取，或按 MIME 选择外部工具转换后重试 view。",
			displayPath, mimeType, size,
		),
		Metadata: map[string]any{
			"file_path":   absPath,
			"is_binary":   true,
			"binary_note": true,
			"binary_mime": mimeType,
			"file_size":   size,
		},
	}
}

// DirectoryListing is the auto-heal for "view was pointed at a directory": a
// shallow listing saves a round trip to ls, and the metadata says the path is a
// directory so the model can ask for a deeper one explicitly.
func DirectoryListing(ctx context.Context, access toolcore.Access, absPath, displayPath string) (*toolcore.Result, error) {
	listing, err := directoryPreview(ctx, access, absPath)
	if err != nil {
		return nil, err
	}
	return &toolcore.Result{
		Success:    true,
		OutputKind: toolcore.KindText,
		Content: fmt.Sprintf(
			"路径是目录，不是文件: %s\n已自动列出目录内容（depth=1，最多 %d 项）。如需递归请改用 ls 并设置 depth。\n\n%s",
			displayPath,
			DirPreviewLimit,
			listing,
		),
		Metadata: map[string]any{
			"file_path":    absPath,
			"is_directory": true,
			"auto_listed":  true,
			"depth":        1,
		},
	}, nil
}
