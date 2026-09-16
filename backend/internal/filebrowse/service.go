// Package filebrowse 实现工作区右侧栏文件浏览器（P0–P2）的后端能力：
// 作用域根枚举、单层目录列表、路径元信息、截断式预览、流式下载与分片上传。
//
// 契约：docs/plan/workspace-right-panel-file-browser-and-git-diff-plan.md
// §5.3（浏览）、§5.4（传输）、§5.7（错误模型）、§5.8（限额）。
//
// 纪律：
//   - 所有路径都先经 fsscope 解析与校验，接口层不接受裸绝对路径；
//   - 错误统一用 *fsscope.Error 携带机器码，由 handler 映射 HTTP 状态；
//   - 只用标准库，不新增依赖。
package filebrowse

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/fsscope"
)

// 传输/列表相关的机器可读错误码（scope/path 类的码在 fsscope 包）。
const (
	CodeCursorInvalid         = "cursor_invalid"
	CodeListFailed            = "fs_list_failed"
	CodeTargetExists          = "target_exists"
	CodeUploadTooLarge        = "upload_too_large"
	CodeChunkSizeInvalid      = "chunk_size_invalid"
	CodeUploadOffsetMismat    = "upload_offset_mismatch"
	CodeUploadChecksum        = "upload_checksum_mismatch"
	CodeUploadExpired         = "upload_expired"
	CodeUploadOffsetBadSize   = "upload_size_mismatch"
	CodeUploadIncomplete      = "upload_incomplete"
	CodeUploadChunkInvalid    = "upload_chunk_invalid"
	CodeChunkChecksum         = "upload_chunk_checksum_mismatch"
	CodeUploadNameInvalid     = "upload_name_invalid"
	CodeUploadTargetInvalid   = "upload_target_invalid"
	CodeConflictPolicyInvalid = "conflict_policy_invalid"
)

// RootResolver 由 runtime-server 注入：把作用域 id 解析为绝对路径。
type RootResolver = fsscope.RootResolver

// Deps 是 Service 的注入依赖。
type Deps struct {
	Roots  RootResolver
	Cwd    string
	Limits Limits
}

// Limits 是各端点的限额（0 值表示使用默认值，见 DefaultLimits）。
type Limits struct {
	ListLimitDefault  int
	ListLimitMax      int
	ScanMaxEntries    int
	PreviewTextBytes  int64
	PreviewImageBytes int64
	MaxUploadBytes    int64
	ChunkSizeDefault  int64
	ChunkSizeMin      int64
	ChunkSizeMax      int64
	UploadTTL         time.Duration
	GitProbeTimeout   time.Duration
}

// DefaultLimits 返回规划 §5.8 的默认限额。
func DefaultLimits() Limits {
	return Limits{
		ListLimitDefault:  200,
		ListLimitMax:      1000,
		ScanMaxEntries:    50000,
		PreviewTextBytes:  256 * 1024,
		PreviewImageBytes: 2 * 1024 * 1024,
		MaxUploadBytes:    10 * 1024 * 1024 * 1024,
		ChunkSizeDefault:  4 * 1024 * 1024,
		ChunkSizeMin:      1024 * 1024,
		ChunkSizeMax:      8 * 1024 * 1024,
		UploadTTL:         24 * time.Hour,
		GitProbeTimeout:   5 * time.Second,
	}
}

func (l Limits) normalized() Limits {
	defaults := DefaultLimits()
	if l.ListLimitDefault <= 0 {
		l.ListLimitDefault = defaults.ListLimitDefault
	}
	if l.ListLimitMax <= 0 {
		l.ListLimitMax = defaults.ListLimitMax
	}
	if l.ListLimitDefault > l.ListLimitMax {
		l.ListLimitDefault = l.ListLimitMax
	}
	if l.ScanMaxEntries <= 0 {
		l.ScanMaxEntries = defaults.ScanMaxEntries
	}
	if l.PreviewTextBytes <= 0 {
		l.PreviewTextBytes = defaults.PreviewTextBytes
	}
	if l.PreviewImageBytes <= 0 {
		l.PreviewImageBytes = defaults.PreviewImageBytes
	}
	if l.MaxUploadBytes <= 0 {
		l.MaxUploadBytes = defaults.MaxUploadBytes
	}
	if l.ChunkSizeDefault <= 0 {
		l.ChunkSizeDefault = defaults.ChunkSizeDefault
	}
	if l.ChunkSizeMin <= 0 {
		l.ChunkSizeMin = defaults.ChunkSizeMin
	}
	if l.ChunkSizeMax <= 0 {
		l.ChunkSizeMax = defaults.ChunkSizeMax
	}
	if l.UploadTTL <= 0 {
		l.UploadTTL = defaults.UploadTTL
	}
	if l.GitProbeTimeout <= 0 {
		l.GitProbeTimeout = defaults.GitProbeTimeout
	}
	return l
}

// Service 是文件浏览器后端服务。
type Service struct {
	resolver *fsscope.Resolver
	limits   Limits
	now      func() time.Time // 测试可替换，便于构造过期会话

	mu      sync.Mutex
	uploads map[string]*uploadSession
}

// NewService 构造服务；Deps.Cwd 为空时回落到 os.Getwd()。
func NewService(deps Deps) *Service {
	cwd := strings.TrimSpace(deps.Cwd)
	if cwd == "" {
		if value, err := os.Getwd(); err == nil {
			cwd = value
		}
	}
	return &Service{
		resolver: fsscope.NewResolver(deps.Roots, cwd),
		limits:   deps.Limits.normalized(),
		now:      time.Now,
		uploads:  make(map[string]*uploadSession),
	}
}

// Root 是 /fs/roots 返回的可用根（规划 §5.3）。
type Root struct {
	Scope      string `json:"scope"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	Path       string `json:"path"`
	Exists     bool   `json:"exists"`
	IsGitRepo  bool   `json:"is_git_repo"`
	GitRoot    string `json:"git_root,omitempty"`
	ProbeError string `json:"probe_error,omitempty"`
}

// ListRoots 返回可用作用域根：注册工作目录（可选枚举）+ 会话根（ctx 携带）+ cwd。
// 单个根探测失败不影响整体结果（失败只置 is_git_repo=false + probe_error）。
func (s *Service) ListRoots(ctx context.Context) ([]Root, error) {
	if err := ctx.Err(); err != nil {
		return nil, fsscope.NewErrorf(fsscope.CodeFSReadFailed, 500, "list roots canceled: %v", err)
	}
	roots := make([]Root, 0, 4)
	if lister, ok := s.resolver.Roots.(fsscope.RootLister); ok && lister != nil {
		items, err := lister.ListWorkspaceRoots(ctx)
		if err != nil {
			return nil, fsscope.NewErrorf(fsscope.CodeFSReadFailed, 500, "list workspace roots failed: %v", err)
		}
		for _, item := range items {
			roots = append(roots, s.describeWorkspaceRoot(ctx, item))
		}
	}
	if sessionID := fsscope.SessionIDFromContext(ctx); sessionID != "" {
		if root, ferr := s.resolver.Resolve(ctx, "session:"+sessionID); ferr == nil {
			roots = append(roots, s.describeRoot(ctx, root, root.Name))
		}
	}
	if cwdRoot, ferr := s.resolver.Resolve(ctx, "cwd"); ferr == nil {
		roots = append(roots, s.describeRoot(ctx, cwdRoot, "runtime cwd"))
	}
	s.sweepExpiredUploads(ctx, roots)
	return roots, nil
}

func (s *Service) describeWorkspaceRoot(ctx context.Context, item fsscope.WorkspaceRoot) Root {
	name := strings.TrimSpace(item.Name)
	scope := string(fsscope.KindWorkspace) + ":" + strings.TrimSpace(item.ID)
	root, ferr := s.resolver.Resolve(ctx, scope)
	if ferr != nil {
		if name == "" {
			name = filepath.Base(strings.TrimSpace(item.Path))
		}
		return Root{
			Scope:      scope,
			Kind:       string(fsscope.KindWorkspace),
			Name:       name,
			Path:       strings.TrimSpace(item.Path),
			ProbeError: ferr.Message,
		}
	}
	if name == "" {
		name = root.Name
	}
	return s.describeRoot(ctx, root, name)
}

func (s *Service) describeRoot(ctx context.Context, root *fsscope.Root, name string) Root {
	entry := Root{
		Scope:  root.Scope.Raw,
		Kind:   string(root.Scope.Kind),
		Name:   strings.TrimSpace(name),
		Path:   root.Raw,
		Exists: root.Exists,
	}
	if entry.Name == "" {
		entry.Name = root.Name
	}
	if !entry.Exists {
		return entry
	}
	isRepo, gitRoot, probeErr := s.probeGit(ctx, root.Path)
	entry.IsGitRepo = isRepo
	entry.GitRoot = gitRoot
	entry.ProbeError = probeErr
	return entry
}

// probeGit 用 `git -C <dir> rev-parse --show-toplevel` 探测仓库根；
// 失败不返回错误，只回填 probe_error（规划 §5.3）。
func (s *Service) probeGit(ctx context.Context, dir string) (bool, string, string) {
	gitBin, err := exec.LookPath("git")
	if err != nil {
		return false, "", "git not found in PATH"
	}
	probeCtx, cancel := context.WithTimeout(ctx, s.limits.GitProbeTimeout)
	defer cancel()
	cmd := exec.CommandContext(probeCtx, gitBin, "-C", dir, "rev-parse", "--show-toplevel")
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if probeCtx.Err() != nil {
			return false, "", "git probe timed out"
		}
		if message := strings.TrimSpace(stderr.String()); message != "" {
			return false, "", message
		}
		return false, "", err.Error()
	}
	gitRoot := strings.TrimSpace(stdout.String())
	if gitRoot == "" {
		return false, "", "git rev-parse returned an empty root"
	}
	return true, gitRoot, ""
}

// displayPath 把相对路径渲染为展示用文本（根显示为 "/"）。
func displayPath(target *fsscope.Target) string {
	if target == nil {
		return ""
	}
	if target.Rel == "" {
		return "/"
	}
	return target.Rel
}

// requireDir 校验目标存在且是目录。
func (s *Service) requireDir(target *fsscope.Target) *fsscope.Error {
	info, err := os.Stat(target.Abs)
	if err != nil {
		return statError(err, target)
	}
	if !info.IsDir() {
		return fsscope.NewErrorf(fsscope.CodePathNotDirectory, 400, "path is not a directory: %s", displayPath(target))
	}
	return nil
}

// requireFile 校验目标存在且是常规文件（目录/设备文件给出明确错误）。
func (s *Service) requireFile(target *fsscope.Target) *fsscope.Error {
	info, err := os.Stat(target.Abs)
	if err != nil {
		return statError(err, target)
	}
	if info.IsDir() {
		return fsscope.NewErrorf(fsscope.CodePathNotFile, 400, "path is a directory, not a file: %s", displayPath(target))
	}
	if !info.Mode().IsRegular() {
		return fsscope.NewErrorf(fsscope.CodePathNotFile, 400, "path is not a regular file: %s", displayPath(target))
	}
	return nil
}

func statError(err error, target *fsscope.Target) *fsscope.Error {
	switch {
	case os.IsNotExist(err):
		return fsscope.NewErrorf(fsscope.CodePathNotFound, 404, "path does not exist: %s", displayPath(target))
	case os.IsPermission(err):
		return fsscope.NewErrorf(fsscope.CodePathPermission, 403, "permission denied: %s", displayPath(target))
	default:
		return fsscope.NewErrorf(fsscope.CodeFSReadFailed, 500, "stat %s failed: %v", displayPath(target), err)
	}
}

func (s *Service) unavailable() *fsscope.Error {
	return fsscope.NewError(fsscope.CodeServiceUnavailable, 503, "file browser service is not configured")
}
