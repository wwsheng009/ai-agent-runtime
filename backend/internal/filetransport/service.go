package filetransport

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// WriteResult describes one filesystem transfer mutation.
type WriteResult struct {
	Path         string `json:"path"`
	BytesWritten int    `json:"bytes_written"`
	SizeBefore   int64  `json:"size_before"`
	SizeAfter    int64  `json:"size_after"`
	Created      bool   `json:"created"`
	Action       string `json:"action"`
}

// Service provides a program-side file transfer surface similar in spirit to
// Codex fs/writeFile.
type Service interface {
	ReadFile(ctx context.Context, path string) ([]byte, string, error)
	WriteFile(ctx context.Context, path string, data []byte) (*WriteResult, error)
	AppendFile(ctx context.Context, path string, data []byte) (*WriteResult, error)
}

// LocalService implements Service against the local filesystem.
type LocalService struct{}

func NewLocalService() *LocalService {
	return &LocalService{}
}

func (s *LocalService) ReadFile(ctx context.Context, path string) ([]byte, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	absPath, err := resolveTransportPath(path)
	if err != nil {
		return nil, "", err
	}
	data, err := os.ReadFile(absPath)
	if err != nil {
		return nil, "", fmt.Errorf("读取文件失败: %w", err)
	}
	return data, absPath, nil
}

func (s *LocalService) WriteFile(ctx context.Context, path string, data []byte) (*WriteResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	absPath, err := resolveTransportPath(path)
	if err != nil {
		return nil, err
	}

	sizeBefore, created, err := ensureWritableFileTarget(absPath)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(absPath, data, 0o644); err != nil {
		return nil, fmt.Errorf("写入文件失败: %w", err)
	}
	info, err := os.Stat(absPath)
	if err != nil {
		return nil, fmt.Errorf("读取写入后文件信息失败: %w", err)
	}
	return &WriteResult{
		Path:         absPath,
		BytesWritten: len(data),
		SizeBefore:   sizeBefore,
		SizeAfter:    info.Size(),
		Created:      created,
		Action:       actionForWrite(created),
	}, nil
}

func (s *LocalService) AppendFile(ctx context.Context, path string, data []byte) (*WriteResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	absPath, err := resolveTransportPath(path)
	if err != nil {
		return nil, err
	}

	sizeBefore, created, err := ensureWritableFileTarget(absPath)
	if err != nil {
		return nil, err
	}
	file, err := os.OpenFile(absPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("以追加模式打开文件失败: %w", err)
	}
	defer file.Close()

	if _, err := file.Write(data); err != nil {
		return nil, fmt.Errorf("追加写入文件失败: %w", err)
	}
	info, err := os.Stat(absPath)
	if err != nil {
		return nil, fmt.Errorf("读取写入后文件信息失败: %w", err)
	}
	action := "append"
	if created {
		action = "create_append"
	}
	return &WriteResult{
		Path:         absPath,
		BytesWritten: len(data),
		SizeBefore:   sizeBefore,
		SizeAfter:    info.Size(),
		Created:      created,
		Action:       action,
	}, nil
}

func resolveTransportPath(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("path 不能为空")
	}
	absPath, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", fmt.Errorf("解析文件路径失败: %w", err)
	}
	return absPath, nil
}

func ensureWritableFileTarget(absPath string) (sizeBefore int64, created bool, err error) {
	if absPath == "" {
		return 0, false, fmt.Errorf("path 不能为空")
	}
	info, statErr := os.Stat(absPath)
	switch {
	case statErr == nil:
		if !info.Mode().IsRegular() {
			return 0, false, fmt.Errorf("目标路径不是常规文件: %s", absPath)
		}
		sizeBefore = info.Size()
	case os.IsNotExist(statErr):
		created = true
	default:
		return 0, false, fmt.Errorf("访问文件失败: %w", statErr)
	}

	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		return 0, false, fmt.Errorf("创建目录失败: %w", err)
	}
	return sizeBefore, created, nil
}

func actionForWrite(created bool) string {
	if created {
		return "create"
	}
	return "overwrite"
}
