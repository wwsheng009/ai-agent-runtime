package filebrowse

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/wwsheng009/ai-agent-runtime/internal/fsscope"
	"github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
)

// uploadDirName 是分片上传的临时目录名：放在**目标目录下**，保证 complete 时
// 同卷 os.Rename 的原子性（跨卷 rename 会失败，规划 §5.4）。
const uploadDirName = ".aicli-uploads"

// uploadState 是上传会话的持久化状态（sidecar JSON）。
type uploadState struct {
	UploadID       string `json:"upload_id"`
	Scope          string `json:"scope"`
	Dir            string `json:"dir"`
	Name           string `json:"name"`
	TargetPath     string `json:"target_path"`
	TargetAbs      string `json:"target_abs"`
	PartPath       string `json:"part_path"`
	Size           int64  `json:"size"`
	Offset         int64  `json:"offset"`
	ChunkSize      int64  `json:"chunk_size"`
	SHA256         string `json:"sha256,omitempty"`
	ConflictPolicy string `json:"conflict_policy"`
	NameAdjusted   bool   `json:"name_adjusted,omitempty"`
	ExpiresAt      int64  `json:"expires_at"`
}

// uploadSession 是带上互斥锁的会话：同一 upload_id 的分片请求串行化（防交错写坏文件）。
type uploadSession struct {
	mu    sync.Mutex
	state uploadState
}

func (state uploadState) sidecarPath() string {
	return filepath.Join(filepath.Dir(state.PartPath), state.UploadID+".json")
}

func newUploadID() (string, error) {
	buffer := make([]byte, 12)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return "up_" + hex.EncodeToString(buffer), nil
}

// writeSidecar 原子替换 sidecar（Windows 上 rename 不能覆盖已存在文件，需先删）。
func (s *Service) writeSidecar(state uploadState) error {
	payload, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	path := state.sidecarPath()
	temp := path + ".tmp"
	if err := os.WriteFile(temp, payload, 0o600); err != nil {
		return err
	}
	if err := os.Rename(temp, path); err == nil {
		return nil
	}
	if removeErr := os.Remove(path); removeErr != nil && !os.IsNotExist(removeErr) {
		return removeErr
	}
	return os.Rename(temp, path)
}

func readUploadStateFile(path string) (uploadState, error) {
	payload, err := os.ReadFile(path)
	if err != nil {
		return uploadState{}, err
	}
	var state uploadState
	if err := json.Unmarshal(payload, &state); err != nil {
		return uploadState{}, err
	}
	return state, nil
}

// discardUpload 清理会话的临时文件（幂等）。
func (s *Service) discardUpload(state uploadState) {
	if strings.TrimSpace(state.PartPath) != "" {
		_ = os.Remove(state.PartPath)
	}
	if strings.TrimSpace(state.UploadID) != "" {
		_ = os.Remove(state.sidecarPath())
	}
}

func (s *Service) dropSession(uploadID string) {
	s.mu.Lock()
	delete(s.uploads, uploadID)
	s.mu.Unlock()
}

// sweepExpiredUploads 在 /fs/roots 调用时顺带清理各根下的过期会话（无后台定时器）。
func (s *Service) sweepExpiredUploads(ctx context.Context, roots []Root) {
	for _, root := range roots {
		if ctx.Err() != nil {
			return
		}
		if !root.Exists || strings.TrimSpace(root.Path) == "" {
			continue
		}
		s.sweepUploadDir(ctx, root.Path)
	}
}

// sweepUploadDir 清理 dir/.aicli-uploads 下的过期会话与孤儿分片；
// 任何错误都只跳过（懒清理不能影响主流程）。
func (s *Service) sweepUploadDir(ctx context.Context, dir string) {
	uploadDir := filepath.Join(dir, uploadDirName)
	entries, err := os.ReadDir(uploadDir)
	if err != nil {
		return
	}
	now := s.now()
	removed := 0
	for _, entry := range entries {
		if ctx.Err() != nil {
			return
		}
		name := entry.Name()
		switch {
		case strings.HasSuffix(name, ".json"):
			state, readErr := readUploadStateFile(filepath.Join(uploadDir, name))
			if readErr != nil {
				continue
			}
			if state.ExpiresAt > 0 && now.Unix() < state.ExpiresAt {
				continue
			}
			s.discardUpload(state)
			s.dropSession(state.UploadID)
			removed++
		case strings.HasSuffix(name, ".part"):
			info, infoErr := entry.Info()
			if infoErr != nil {
				continue
			}
			if now.Sub(info.ModTime()) < s.limits.UploadTTL {
				continue // 可能是正在进行的会话（sidecar 尚未落盘）
			}
			if removeErr := os.Remove(filepath.Join(uploadDir, name)); removeErr == nil {
				removed++
			}
		}
	}
	if removed > 0 {
		logger.Infof("fs upload: swept %d expired upload artifact(s) under %s", removed, uploadDir)
	}
}

func hashFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// copyFile 是 rename 失败时的回退路径（跨卷或目标被占用）。
func copyFile(source, target string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(output, input); err != nil {
		output.Close()
		return err
	}
	if err := output.Sync(); err != nil {
		output.Close()
		return err
	}
	return output.Close()
}

func statExistingFile(path string) (os.FileInfo, bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return info, true, nil
}

func (s *Service) normalizeChunkSize(value int64) (int64, *fsscope.Error) {
	if value <= 0 {
		return s.limits.ChunkSizeDefault, nil
	}
	if value < s.limits.ChunkSizeMin || value > s.limits.ChunkSizeMax {
		return 0, fsscope.NewErrorf(CodeChunkSizeInvalid, 400, "chunk_size must be between %d and %d bytes", s.limits.ChunkSizeMin, s.limits.ChunkSizeMax)
	}
	return value, nil
}

func normalizeConflictPolicy(value string) (string, *fsscope.Error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", ConflictPolicyFail:
		return ConflictPolicyFail, nil
	case ConflictPolicyOverwrite:
		return ConflictPolicyOverwrite, nil
	case ConflictPolicyRename:
		return ConflictPolicyRename, nil
	default:
		return "", fsscope.NewError(CodeConflictPolicyInvalid, 400, "conflict_policy must be fail, overwrite or rename")
	}
}

func normalizeUploadName(raw string) (string, *fsscope.Error) {
	name := strings.TrimSpace(raw)
	switch {
	case name == "", name == ".", name == "..":
		return "", fsscope.NewError(CodeUploadNameInvalid, 400, "upload name is required")
	case strings.ContainsAny(name, "/\\"):
		return "", fsscope.NewError(CodeUploadNameInvalid, 400, "upload name must be a plain file name")
	case strings.ContainsAny(name, "<>:\"|?*"), strings.ContainsRune(name, '\x00'):
		// Windows 非法文件名字符；跨平台统一拒绝，避免落盘时才报错。
		return "", fsscope.NewErrorf(CodeUploadNameInvalid, 400, "upload name contains illegal characters: %s", name)
	}
	return name, nil
}

// uniqueTargetName 为 rename 策略生成不冲突的 "base (n).ext"。
func uniqueTargetName(dirAbs, name string) (string, string, *fsscope.Error) {
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	for index := 1; index <= 1000; index++ {
		candidate := fmt.Sprintf("%s (%d)%s", base, index, ext)
		abs := filepath.Join(dirAbs, candidate)
		if _, err := os.Lstat(abs); err != nil {
			return candidate, abs, nil
		}
	}
	return "", "", fsscope.NewErrorf(CodeTargetExists, 409, "cannot find a free name for %s", name)
}

func containsUploadDir(rel string) bool {
	for _, part := range strings.Split(rel, "/") {
		if strings.EqualFold(part, uploadDirName) {
			return true
		}
	}
	return false
}

// renameIntoPlace 原子落盘：Windows 的 MoveFile 不能覆盖已存在目标，必须先显式删除；
// 删除或 rename 失败时回退「复制后删除」（跨卷场景）。
func renameIntoPlace(source, target string) error {
	if err := os.Rename(source, target); err == nil {
		return nil
	}
	if _, statErr := os.Lstat(target); statErr == nil {
		if removeErr := os.Remove(target); removeErr == nil {
			if retryErr := os.Rename(source, target); retryErr == nil {
				return nil
			}
		}
	}
	if copyErr := copyFile(source, target); copyErr != nil {
		return copyErr
	}
	return os.Remove(source)
}
