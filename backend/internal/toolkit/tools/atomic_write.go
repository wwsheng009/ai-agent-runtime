package tools

import (
	"fmt"
	"os"
	"path/filepath"
)

// writeFileAtomic replaces path with data through a sibling temp file followed
// by a rename, so concurrent readers never observe a half-written file. The
// existing file permissions are preserved; fallbackMode applies only when the
// target does not exist yet (0 means "no explicit chmod").
func writeFileAtomic(path string, data []byte, fallbackMode os.FileMode) error {
	mode := fallbackMode
	if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
		mode = info.Mode().Perm()
	}

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("创建临时文件失败: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() {
		_ = os.Remove(tmpPath)
	}

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("写入临时文件失败: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("同步临时文件失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("关闭临时文件失败: %w", err)
	}
	if mode != 0 {
		if err := os.Chmod(tmpPath, mode); err != nil {
			cleanup()
			return fmt.Errorf("设置文件权限失败: %w", err)
		}
	}
	if err := os.Rename(tmpPath, path); err != nil {
		cleanup()
		return fmt.Errorf("原子替换文件失败: %w", err)
	}
	return nil
}

// writeFileModeDefault is used when a brand-new file is created without an
// existing mode to preserve.
const writeFileModeDefault os.FileMode = 0o644
