package tools

import (
	"fmt"
	"os"
	"path/filepath"
)

// writeFileAtomicLocal replaces path with data through a sibling temp file
// followed by a rename, so concurrent readers never observe a half-written
// file. The existing file permissions are preserved; fallbackMode applies only
// when the target does not exist yet (0 means "no explicit chmod").
//
// The Local suffix is deliberate: agentconfig / runtimeserver / planstore /
// mesh each have their own `writeFileAtomic` implementing the shared config
// write channel, and the config-write guard walks the tree by name. This helper
// is a package-local atomic write for arbitrary user files, not that channel.
func writeFileAtomicLocal(path string, data []byte, fallbackMode os.FileMode) error {
	mode := fallbackMode
	if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
		mode = info.Mode().Perm()
	}
	// A rename replaces the directory entry: it would turn a symlink into a
	// regular file (the link target keeps the old bytes) and split a hard-link
	// set. The pre-atomic implementation wrote through both, so link-bearing
	// targets keep that behavior (review M4).
	if linkInfo, err := os.Lstat(path); err == nil {
		if linkInfo.Mode()&os.ModeSymlink != 0 || hasMultipleHardLinks(linkInfo) {
			return writeFileInPlace(path, data, mode)
		}
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

// writeFileInPlace truncates and rewrites the file through its path: symlinks
// keep pointing at their target and every hard link keeps sharing the inode.
// It trades atomic replacement for link fidelity, matching the write-through
// semantics callers had before the atomic path was introduced.
func writeFileInPlace(path string, data []byte, mode os.FileMode) error {
	if mode == 0 {
		mode = writeFileModeDefault
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return fmt.Errorf("写入文件失败: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return fmt.Errorf("写入文件失败: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("同步文件失败: %w", err)
	}
	return file.Close()
}

// writeFileModeDefault is used when a brand-new file is created without an
// existing mode to preserve.
const writeFileModeDefault os.FileMode = 0o644
