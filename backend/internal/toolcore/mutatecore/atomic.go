package mutatecore

import (
	"fmt"
	"os"
	"path/filepath"
)

// DefaultFileMode is used when a new file is created without an explicit mode.
const DefaultFileMode os.FileMode = 0o644

// WriteAtomic replaces path with data through a sibling temp file followed by
// a rename, so concurrent readers never observe a half-written file.
//
// Symlink and hard-link targets keep write-through semantics: a rename would
// replace the directory entry instead of the linked inode. The same fallback
// is used on Windows for symlinks; hard-link counts are not portable there.
func WriteAtomic(path string, data []byte, fallbackMode os.FileMode) error {
	mode := fallbackMode
	if mode == 0 {
		mode = DefaultFileMode
	}
	if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
		mode = info.Mode().Perm()
	}
	if linkInfo, err := os.Lstat(path); err == nil {
		if linkInfo.Mode()&os.ModeSymlink != 0 || hasMultipleHardLinks(linkInfo) {
			return writeInPlace(path, data, mode)
		}
	}

	dir := filepath.Dir(path)
	temp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return newError(CodeInvalidArgument, "check directory permissions", fmt.Errorf("create temp file in %s: %w", dir, err))
	}
	tempPath := temp.Name()
	cleanup := func() {
		_ = temp.Close()
		_ = os.Remove(tempPath)
	}
	if _, err := temp.Write(data); err != nil {
		cleanup()
		return newError(CodeInternal, "retry the write", fmt.Errorf("write temp file: %w", err))
	}
	if err := temp.Sync(); err != nil {
		cleanup()
		return newError(CodeInternal, "retry the write", fmt.Errorf("flush temp file: %w", err))
	}
	if err := temp.Close(); err != nil {
		cleanup()
		return newError(CodeInternal, "retry the write", fmt.Errorf("close temp file: %w", err))
	}
	if err := os.Chmod(tempPath, mode); err != nil {
		_ = os.Remove(tempPath)
		return newError(CodeInternal, "retry the write", fmt.Errorf("set file mode: %w", err))
	}
	if err := os.Rename(tempPath, path); err != nil {
		_ = os.Remove(tempPath)
		return newError(CodeInternal, "retry the write", fmt.Errorf("replace %s: %w", path, err))
	}
	return nil
}

// WriteInPlace truncates and rewrites the file through its path, preserving
// symlink and hard-link identity at the cost of atomic replacement.
func writeInPlace(path string, data []byte, mode os.FileMode) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return newError(CodeInvalidArgument, "check file permissions", fmt.Errorf("open %s for write: %w", path, err))
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return newError(CodeInternal, "retry the write", fmt.Errorf("write %s: %w", path, err))
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return newError(CodeInternal, "retry the write", fmt.Errorf("sync %s: %w", path, err))
	}
	if err := file.Close(); err != nil {
		return newError(CodeInternal, "retry the write", fmt.Errorf("close %s: %w", path, err))
	}
	return nil
}
