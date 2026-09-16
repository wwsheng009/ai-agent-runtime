package filebrowse

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/fsscope"
)

// DownloadTarget 是已打开的下载目标：持有文件句柄，避免「校验路径后文件被替换」的 TOCTOU。
// 调用方（handler）必须 Close。
type DownloadTarget struct {
	File        *os.File
	Name        string
	RelPath     string
	AbsPath     string
	Size        int64
	ModTime     time.Time
	ContentType string
}

// Close 释放文件句柄（可重复调用）。
func (t *DownloadTarget) Close() error {
	if t == nil || t.File == nil {
		return nil
	}
	file := t.File
	t.File = nil
	return file.Close()
}

// ETag 遵循规划 D5：`"<size>-<mtimeUnixNano>"`。
// 作为弱校验的强 ETag：先过 Range/If-Range，再由 http.ServeContent 决定 200/206/416。
func (t *DownloadTarget) ETag() string {
	if t == nil {
		return ""
	}
	return fmt.Sprintf("\"%d-%d\"", t.Size, t.ModTime.UnixNano())
}

// OpenDownload 打开文件并返回下载元信息（GET/HEAD /fs/download）。
func (s *Service) OpenDownload(ctx context.Context, req PathRequest) (*DownloadTarget, error) {
	if err := ctx.Err(); err != nil {
		return nil, fsscopeReadError("download", err)
	}
	target, ferr := s.resolver.ResolvePath(ctx, req.Scope, req.Path)
	if ferr != nil {
		return nil, ferr
	}
	if ferr := s.requireFile(target); ferr != nil {
		return nil, ferr
	}
	file, err := os.Open(target.Abs)
	if err != nil {
		return nil, readFileError(err, target)
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, statError(err, target)
	}
	if info.IsDir() {
		file.Close()
		return nil, fsscope.NewErrorf(fsscope.CodePathNotFile, 400, "path is a directory, not a file: %s", displayPath(target))
	}
	return &DownloadTarget{
		File:        file,
		Name:        info.Name(),
		RelPath:     target.Rel,
		AbsPath:     target.Abs,
		Size:        info.Size(),
		ModTime:     info.ModTime(),
		ContentType: MimeForName(info.Name(), false),
	}, nil
}
