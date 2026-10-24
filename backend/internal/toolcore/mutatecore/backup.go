package mutatecore

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// BackupDirName is the directory a non-versioned workspace keeps rollback
// points in. A git work tree does not need it: `git diff`/`git checkout` already
// hold the previous content, so writing a second copy would just litter.
const BackupDirName = ".backups"

// CreateBackup writes a timestamped copy of one file and returns its path.
//
// The timestamp is second-granular, so two backups of the same file within one
// second would collide. O_EXCL plus a numeric suffix makes every call land in a
// new file instead of silently truncating the previous rollback point.
func CreateBackup(filePath, dirName string, content []byte) (string, error) {
	absPath, err := filepath.Abs(filePath)
	if err != nil {
		return "", err
	}
	if dirName == "" {
		dirName = BackupDirName
	}
	backupDir := filepath.Join(filepath.Dir(absPath), dirName)
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		return "", err
	}
	timestamp := time.Now().Format("20060102-150405")
	baseName := filepath.Base(absPath)
	for attempt := 0; attempt < 1000; attempt++ {
		backupName := fmt.Sprintf("%s.%s.bak", baseName, timestamp)
		if attempt > 0 {
			backupName = fmt.Sprintf("%s.%s-%d.bak", baseName, timestamp, attempt)
		}
		backupPath := filepath.Join(backupDir, backupName)
		file, openErr := os.OpenFile(backupPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if openErr != nil {
			if errors.Is(openErr, os.ErrExist) {
				continue
			}
			return "", openErr
		}
		if _, writeErr := file.Write(content); writeErr != nil {
			_ = file.Close()
			_ = os.Remove(backupPath)
			return "", writeErr
		}
		if closeErr := file.Close(); closeErr != nil {
			return "", closeErr
		}
		return backupPath, nil
	}
	return "", fmt.Errorf("创建备份文件失败：%s 下同名备份过多", backupDir)
}
