package filebrowse

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/fsscope"
	"github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
)

// 上传冲突策略（规划 §5.4 /fs/upload/init）。
const (
	ConflictPolicyFail      = "fail"
	ConflictPolicyOverwrite = "overwrite"
	ConflictPolicyRename    = "rename"
)

// 上传落盘动作（complete 响应 action）。
const (
	UploadActionCreate    = "create"
	UploadActionOverwrite = "overwrite"
	UploadActionRename    = "rename"
)

// UploadInitRequest 是 POST /fs/upload/init 的入参。
type UploadInitRequest struct {
	Scope          string
	Dir            string
	Name           string
	Size           int64
	SHA256         string
	ChunkSize      int64
	ConflictPolicy string
}

// UploadTarget 描述上传目标文件。
type UploadTarget struct {
	Path    string `json:"path"`
	AbsPath string `json:"abs_path,omitempty"`
	Exists  bool   `json:"exists"`
	Size    int64  `json:"size,omitempty"`
	SHA256  string `json:"sha256,omitempty"`
}

// UploadInitResult 是 POST /fs/upload/init 的响应。
type UploadInitResult struct {
	UploadID     string       `json:"upload_id"`
	Offset       int64        `json:"offset"`
	Received     int64        `json:"received"`
	ChunkSize    int64        `json:"chunk_size"`
	ExpiresAt    int64        `json:"expires_at"`
	Completed    bool         `json:"completed"`
	Deduplicated bool         `json:"deduplicated"`
	Target       UploadTarget `json:"target"`
}

// UploadChunkRequest 是 PUT /fs/upload/{id}/chunk 的入参（Data 为原始字节）。
//
// End/TotalSize 是**声明式诊断字段**（来自 Content-Range），0 或负数表示未声明；
// 「声明范围与 body 长度是否一致」由接口层校验（那里才知道线协议），服务层只信
// Start（顺序）与 Data 长度（边界），避免两处校验口径漂移。
type UploadChunkRequest struct {
	UploadID  string
	Start     int64
	End       int64
	TotalSize int64
	Data      []byte
	SHA256    string
}

// UploadChunkResult 是分片写入结果。
type UploadChunkResult struct {
	Received int64 `json:"received"`
	Offset   int64 `json:"offset"`
}

// UploadStatusResult 是 GET /fs/upload/{id} 的续传探测结果。
type UploadStatusResult struct {
	UploadID       string `json:"upload_id"`
	Received       int64  `json:"received"`
	Offset         int64  `json:"offset"`
	ChunkSize      int64  `json:"chunk_size"`
	Size           int64  `json:"size"`
	ExpiresAt      int64  `json:"expires_at"`
	TargetPath     string `json:"target_path"`
	ConflictPolicy string `json:"conflict_policy"`
}

// UploadCompleteRequest 是 POST /fs/upload/{id}/complete 的入参。
type UploadCompleteRequest struct {
	UploadID string
	SHA256   string
}

// UploadCompleteResult 是落地后的结果。
type UploadCompleteResult struct {
	Path      string `json:"path"`
	AbsPath   string `json:"abs_path,omitempty"`
	Size      int64  `json:"size"`
	Action    string `json:"action"`
	SHA256    string `json:"sha256,omitempty"`
	ElapsedMS int64  `json:"elapsed_ms"`
}

// InitUpload 创建（或秒传）上传会话，返回续传起点 offset。
func (s *Service) InitUpload(ctx context.Context, req UploadInitRequest) (*UploadInitResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, fsscopeReadError("upload init", err)
	}
	name, nameErr := normalizeUploadName(req.Name)
	if nameErr != nil {
		return nil, nameErr
	}
	if req.Size < 0 {
		return nil, fsscope.NewError(CodeUploadTargetInvalid, 400, "size must be greater than or equal to 0")
	}
	if req.Size > s.limits.MaxUploadBytes {
		return nil, fsscope.NewErrorf(CodeUploadTooLarge, 400, "size exceeds the %d byte limit", s.limits.MaxUploadBytes)
	}
	chunkSize, chunkErr := s.normalizeChunkSize(req.ChunkSize)
	if chunkErr != nil {
		return nil, chunkErr
	}
	policy, policyErr := normalizeConflictPolicy(req.ConflictPolicy)
	if policyErr != nil {
		return nil, policyErr
	}
	target, ferr := s.resolver.ResolvePath(ctx, req.Scope, req.Dir)
	if ferr != nil {
		return nil, ferr
	}
	if ferr := s.requireDir(target); ferr != nil {
		return nil, ferr
	}
	if containsUploadDir(target.Rel) {
		return nil, fsscope.NewError(CodeUploadTargetInvalid, 400, "cannot upload into the internal upload directory")
	}
	relPath := listPath(target.Rel, name)
	absPath := filepath.Join(target.Abs, name)
	info, exists, statErr := statExistingFile(absPath)
	if statErr != nil {
		return nil, fsscope.NewErrorf(fsscope.CodeFSWriteFailed, 500, "stat upload target failed: %v", statErr)
	}
	if exists && info.IsDir() {
		return nil, fsscope.NewErrorf(fsscope.CodePathNotFile, 400, "upload target is an existing directory: %s", relPath)
	}
	wantHash := strings.ToLower(strings.TrimSpace(req.SHA256))
	if exists && wantHash != "" {
		if existing, hashErr := hashFile(absPath); hashErr == nil && existing == wantHash {
			// 秒传：目标已存在且内容一致，不创建会话。
			return &UploadInitResult{
				ChunkSize:    chunkSize,
				Completed:    true,
				Deduplicated: true,
				Target: UploadTarget{
					Path: relPath, AbsPath: absPath, Exists: true, Size: info.Size(), SHA256: existing,
				},
			}, nil
		}
	}
	nameAdjusted := false
	switch policy {
	case ConflictPolicyFail:
		if exists {
			return nil, fsscope.NewErrorf(CodeTargetExists, 409, "target already exists: %s", relPath).
				WithDetail("target", UploadTarget{Path: relPath, AbsPath: absPath, Exists: true, Size: info.Size()})
		}
	case ConflictPolicyRename:
		if exists {
			candidate, candidateAbs, renameErr := uniqueTargetName(target.Abs, name)
			if renameErr != nil {
				return nil, renameErr
			}
			name, nameAdjusted = candidate, true
			relPath, absPath = listPath(target.Rel, candidate), candidateAbs
		}
	}
	uploadID, idErr := newUploadID()
	if idErr != nil {
		return nil, fsscope.NewErrorf(fsscope.CodeFSWriteFailed, 500, "generate upload id failed: %v", idErr)
	}
	uploadDir := filepath.Join(target.Abs, uploadDirName)
	if mkErr := os.MkdirAll(uploadDir, 0o700); mkErr != nil {
		return nil, fsscope.NewErrorf(fsscope.CodeFSWriteFailed, 500, "create upload temp directory failed: %v", mkErr)
	}
	partPath := filepath.Join(uploadDir, uploadID+".part")
	file, createErr := os.OpenFile(partPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if createErr != nil {
		return nil, fsscope.NewErrorf(fsscope.CodeFSWriteFailed, 500, "create part file failed: %v", createErr)
	}
	file.Close()
	state := uploadState{
		UploadID:       uploadID,
		Scope:          target.Root.Scope.Raw,
		Dir:            target.Rel,
		Name:           name,
		TargetPath:     relPath,
		TargetAbs:      absPath,
		PartPath:       partPath,
		Size:           req.Size,
		Offset:         0,
		ChunkSize:      chunkSize,
		SHA256:         wantHash,
		ConflictPolicy: policy,
		NameAdjusted:   nameAdjusted,
		ExpiresAt:      s.now().Add(s.limits.UploadTTL).Unix(),
	}
	if writeErr := s.writeSidecar(state); writeErr != nil {
		_ = os.Remove(partPath)
		return nil, fsscope.NewErrorf(fsscope.CodeFSWriteFailed, 500, "persist upload session failed: %v", writeErr)
	}
	s.mu.Lock()
	s.uploads[uploadID] = &uploadSession{state: state}
	s.mu.Unlock()
	logger.Infof("fs upload: init upload_id=%s scope=%s path=%s size=%d policy=%s", uploadID, state.Scope, relPath, req.Size, policy)
	return &UploadInitResult{
		UploadID:  uploadID,
		Offset:    0,
		Received:  0,
		ChunkSize: chunkSize,
		ExpiresAt: state.ExpiresAt,
		Target:    UploadTarget{Path: relPath, AbsPath: absPath, Exists: exists},
	}, nil
}

// PutChunk 顺序写入一个分片；起点必须等于已落盘 offset，否则 409 + expected_offset。
func (s *Service) PutChunk(ctx context.Context, req UploadChunkRequest) (*UploadChunkResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, fsscopeReadError("upload chunk", err)
	}
	session, ferr := s.session(req.UploadID)
	if ferr != nil {
		return nil, ferr
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if ferr := s.ensureNotExpired(session); ferr != nil {
		return nil, ferr
	}
	state := session.state
	if req.TotalSize > 0 && req.TotalSize != state.Size {
		return nil, fsscope.NewErrorf(CodeUploadOffsetBadSize, 400, "declared total size %d does not match the session size %d", req.TotalSize, state.Size)
	}
	if req.Start != state.Offset {
		return nil, fsscope.NewErrorf(CodeUploadOffsetMismat, 409, "chunk start %d does not match the received offset %d", req.Start, state.Offset).
			WithDetail("expected_offset", state.Offset)
	}
	if len(req.Data) == 0 {
		return nil, fsscope.NewError(CodeUploadChunkInvalid, 400, "chunk body is empty")
	}
	if state.Offset+int64(len(req.Data)) > state.Size {
		return nil, fsscope.NewErrorf(CodeUploadChunkInvalid, 400, "chunk exceeds the declared file size %d", state.Size)
	}
	if wantChunkHash := strings.ToLower(strings.TrimSpace(req.SHA256)); wantChunkHash != "" {
		sum := sha256.Sum256(req.Data)
		if hex.EncodeToString(sum[:]) != wantChunkHash {
			return nil, fsscope.NewError(CodeChunkChecksum, 400, "chunk checksum mismatch")
		}
	}
	written, ferr := s.appendChunk(state, req.Data)
	if ferr != nil {
		return nil, ferr
	}
	state.Offset = written
	session.state = state
	if err := s.writeSidecar(state); err != nil {
		return nil, fsscope.NewErrorf(fsscope.CodeFSWriteFailed, 500, "persist upload state failed: %v", err)
	}
	return &UploadChunkResult{Received: written, Offset: written}, nil
}

// appendChunk 以 O_APPEND 写入，并用 Stat 双重确认「写入位置 == 已落盘 offset」。
func (s *Service) appendChunk(state uploadState, data []byte) (int64, *fsscope.Error) {
	file, err := os.OpenFile(state.PartPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return 0, fsscope.NewErrorf(fsscope.CodeFSWriteFailed, 500, "open part file failed: %v", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return 0, fsscope.NewErrorf(fsscope.CodeFSWriteFailed, 500, "stat part file failed: %v", err)
	}
	if info.Size() != state.Offset {
		return 0, fsscope.NewErrorf(CodeUploadOffsetMismat, 409, "part file size %d drifted from the recorded offset %d", info.Size(), state.Offset).
			WithDetail("expected_offset", info.Size())
	}
	if _, err := file.Write(data); err != nil {
		return 0, fsscope.NewErrorf(fsscope.CodeFSWriteFailed, 500, "write chunk failed: %v", err)
	}
	after, err := file.Stat()
	if err != nil {
		return 0, fsscope.NewErrorf(fsscope.CodeFSWriteFailed, 500, "stat part file after write failed: %v", err)
	}
	expected := state.Offset + int64(len(data))
	if after.Size() != expected {
		_ = file.Truncate(expected)
		return 0, fsscope.NewErrorf(fsscope.CodeFSWriteFailed, 500, "unexpected part size %d after write", after.Size())
	}
	return after.Size(), nil
}

// CompleteUpload 校验整文件 sha256 后原子落盘（Windows 目标已存在时显式处理）。
func (s *Service) CompleteUpload(ctx context.Context, req UploadCompleteRequest) (*UploadCompleteResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, fsscopeReadError("upload complete", err)
	}
	session, ferr := s.session(req.UploadID)
	if ferr != nil {
		return nil, ferr
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if ferr := s.ensureNotExpired(session); ferr != nil {
		return nil, ferr
	}
	state := session.state
	if state.Offset != state.Size {
		return nil, fsscope.NewErrorf(CodeUploadIncomplete, 400, "upload is incomplete: %d/%d bytes received", state.Offset, state.Size).
			WithDetail("expected_offset", state.Offset)
	}
	started := s.now()
	sum, hashErr := hashFile(state.PartPath)
	if hashErr != nil {
		return nil, fsscope.NewErrorf(fsscope.CodeFSReadFailed, 500, "hash part file failed: %v", hashErr)
	}
	want := state.SHA256
	if value := strings.ToLower(strings.TrimSpace(req.SHA256)); value != "" {
		want = value
	}
	if want != "" && sum != want {
		// 临时文件保留，便于前端「重传最后一片」。
		return nil, fsscope.NewErrorf(CodeUploadChecksum, 409, "checksum mismatch: got %s", sum)
	}
	info, exists, statErr := statExistingFile(state.TargetAbs)
	if statErr != nil {
		return nil, fsscope.NewErrorf(fsscope.CodeFSWriteFailed, 500, "stat upload target failed: %v", statErr)
	}
	if exists && state.ConflictPolicy == ConflictPolicyFail && !state.NameAdjusted {
		return nil, fsscope.NewErrorf(CodeTargetExists, 409, "target already exists: %s", state.TargetPath).
			WithDetail("target", UploadTarget{Path: state.TargetPath, AbsPath: state.TargetAbs, Exists: true, Size: info.Size()})
	}
	action := UploadActionCreate
	if exists {
		action = UploadActionOverwrite
	}
	if state.NameAdjusted {
		action = UploadActionRename
	}
	if renameErr := renameIntoPlace(state.PartPath, state.TargetAbs); renameErr != nil {
		if os.IsNotExist(renameErr) {
			return nil, fsscope.NewErrorf(fsscope.CodePathNotFound, 404, "upload target directory no longer exists: %s", state.TargetPath)
		}
		return nil, fsscope.NewErrorf(fsscope.CodeFSWriteFailed, 500, "finalize upload failed: %v", renameErr)
	}
	s.dropSession(state.UploadID)
	_ = os.Remove(state.sidecarPath())
	logger.Infof("fs upload: complete upload_id=%s scope=%s path=%s size=%d action=%s", state.UploadID, state.Scope, state.TargetPath, state.Size, action)
	return &UploadCompleteResult{
		Path:      state.TargetPath,
		AbsPath:   state.TargetAbs,
		Size:      state.Size,
		Action:    action,
		SHA256:    sum,
		ElapsedMS: s.now().Sub(started).Milliseconds(),
	}, nil
}

// UploadStatus 返回续传探测结果；offset 以磁盘真实大小为准校正。
func (s *Service) UploadStatus(ctx context.Context, uploadID string) (*UploadStatusResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, fsscopeReadError("upload status", err)
	}
	session, ferr := s.session(uploadID)
	if ferr != nil {
		return nil, ferr
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if ferr := s.ensureNotExpired(session); ferr != nil {
		return nil, ferr
	}
	state := session.state
	if info, statErr := os.Stat(state.PartPath); statErr == nil && info.Size() != state.Offset {
		offset := info.Size()
		if offset > state.Size {
			offset = state.Size
		}
		state.Offset = offset
		session.state = state
	}
	return &UploadStatusResult{
		UploadID:       state.UploadID,
		Received:       state.Offset,
		Offset:         state.Offset,
		ChunkSize:      state.ChunkSize,
		Size:           state.Size,
		ExpiresAt:      state.ExpiresAt,
		TargetPath:     state.TargetPath,
		ConflictPolicy: state.ConflictPolicy,
	}, nil
}

// AbortUpload 中止会话并清理临时文件；会话已不存在时幂等成功。
func (s *Service) AbortUpload(ctx context.Context, uploadID string) error {
	if err := ctx.Err(); err != nil {
		return fsscopeReadError("upload abort", err)
	}
	id := strings.TrimSpace(uploadID)
	s.mu.Lock()
	session := s.uploads[id]
	s.mu.Unlock()
	if session == nil {
		return nil
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	state := session.state
	s.dropSession(id)
	s.discardUpload(state)
	logger.Infof("fs upload: abort upload_id=%s path=%s", id, state.TargetPath)
	return nil
}

func (s *Service) session(uploadID string) (*uploadSession, *fsscope.Error) {
	id := strings.TrimSpace(uploadID)
	if id == "" {
		return nil, fsscope.NewError(CodeUploadExpired, 410, "upload session id is required")
	}
	s.mu.Lock()
	session := s.uploads[id]
	s.mu.Unlock()
	if session == nil {
		// 会话只存活于当前 runtime 进程：重启/懒清理后按过期处理，前端重新 init 续传。
		return nil, fsscope.NewErrorf(CodeUploadExpired, 410, "upload session %s is unknown or expired", id)
	}
	return session, nil
}

func (s *Service) ensureNotExpired(session *uploadSession) *fsscope.Error {
	state := session.state
	if state.ExpiresAt <= 0 || s.now().Unix() < state.ExpiresAt {
		return nil
	}
	s.dropSession(state.UploadID)
	s.discardUpload(state)
	return fsscope.NewErrorf(CodeUploadExpired, 410, "upload session %s expired", state.UploadID)
}
