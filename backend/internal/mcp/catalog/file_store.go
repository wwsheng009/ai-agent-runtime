package catalog

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// FileSnapshotStore 使用 JSON 文件持久化 catalog 快照。
type FileSnapshotStore struct {
	path string
}

// NewFileSnapshotStore 创建一个文件型 snapshot store。
func NewFileSnapshotStore(path string) *FileSnapshotStore {
	return &FileSnapshotStore{path: strings.TrimSpace(path)}
}

// Path 返回底层快照文件路径。
func (s *FileSnapshotStore) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

// LoadCatalogSnapshot 读取快照；文件不存在时返回 nil, nil。
func (s *FileSnapshotStore) LoadCatalogSnapshot() (*Snapshot, error) {
	if s == nil || strings.TrimSpace(s.path) == "" {
		return nil, nil
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read catalog snapshot: %w", err)
	}
	if len(data) == 0 {
		return nil, nil
	}
	var snapshot Snapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return nil, fmt.Errorf("decode catalog snapshot: %w", err)
	}
	return &snapshot, nil
}

// SaveCatalogSnapshot 原子写入快照。
func (s *FileSnapshotStore) SaveCatalogSnapshot(snapshot Snapshot) error {
	if s == nil || strings.TrimSpace(s.path) == "" {
		return nil
	}
	dir := filepath.Dir(s.path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create catalog snapshot dir: %w", err)
		}
	}
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return fmt.Errorf("encode catalog snapshot: %w", err)
	}
	tmpPath := s.path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o644); err != nil {
		return fmt.Errorf("write catalog snapshot temp file: %w", err)
	}
	if err := os.Rename(tmpPath, s.path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename catalog snapshot temp file: %w", err)
	}
	return nil
}
