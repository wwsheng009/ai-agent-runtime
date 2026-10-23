package filetransport

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore/mutatecore"
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
	result, err := mutatecore.Write(absPath, string(data), mutatecore.WriteOptions{
		Mode:          0o644,
		CreateParents: true,
	})
	if err != nil {
		if info, statErr := os.Stat(absPath); statErr == nil && info.IsDir() {
			return nil, pathKindMismatchError(absPath)
		}
		return nil, err
	}
	return &WriteResult{
		Path:         absPath,
		BytesWritten: len(data),
		SizeBefore:   result.OldSize,
		SizeAfter:    result.NewSize,
		Created:      result.Created,
		Action:       actionForWrite(result.Created),
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
	result, err := mutatecore.Append(ctx, absPath, string(data), mutatecore.AppendOptions{
		CreateIfMissing: true,
		Mode:            0o644,
	})
	if err != nil {
		if info, statErr := os.Stat(absPath); statErr == nil && info.IsDir() {
			return nil, pathKindMismatchError(absPath)
		}
		return nil, err
	}
	action := "append"
	if result.Created {
		action = "create_append"
	}
	return &WriteResult{
		Path:         absPath,
		BytesWritten: len(data),
		SizeBefore:   result.SizeBefore,
		SizeAfter:    result.SizeAfter,
		Created:      result.Created,
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

func actionForWrite(created bool) string {
	if created {
		return "create"
	}
	return "overwrite"
}

func pathKindMismatchError(path string) error {
	if hint := runtimeexecutor.BuildPathKindMismatchHintForPath(path, ""); hint != "" {
		return fmt.Errorf("目标路径是目录，不是文件: %s\n%s", path, hint)
	}
	return fmt.Errorf("目标路径是目录，不是文件: %s", path)
}
