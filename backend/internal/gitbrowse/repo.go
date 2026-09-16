package gitbrowse

import (
	"context"
	stderrors "errors"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/observability"
)

// RootResolver 把作用域标识解析为绝对根路径。方法集与 internal/fsscope 的同名接口一致，
// 父代理可用一个适配器同时满足两者。
//
// ok=false 表示作用域不存在（映射为 404 scope_not_found）；若需要区分「作用域存在但没有
// 工作目录」，适配器应返回 ErrScopeHasNoRoot（映射为 400 scope_has_no_root）。
type RootResolver interface {
	WorkspaceRoot(ctx context.Context, id string) (path string, ok bool, err error)
	SessionRoot(ctx context.Context, sessionID string) (path string, ok bool, err error)
}

// Deps 是 Service 的注入依赖。
type Deps struct {
	Roots  RootResolver // scope → 绝对根路径；workspace:/session: 必需
	Cwd    string       // scope=cwd 时使用的 runtime 工作目录（空则回退 os.Getwd）
	Limits Limits       // 零值字段自动补默认值（DefaultLimits）
}

// RepoRequest 是所有 /git/* 请求共用的作用域与基准目录参数。
//
//	scope: workspace:<directory_id> | session:<session_id> | cwd（必填，见 §5.1/§5.2）
//	path:  作用域相对目录（空/"/" = 作用域根），用于定位仓库，允许指向子目录并上溯。
type RepoRequest struct {
	Scope string
	Path  string
}

// Service 是 gitbrowse 的门面：作用域校验 + 仓库探测 + 各子命令。
type Service struct {
	roots  RootResolver
	cwd    string
	limits Limits
	runner *runner

	cacheMu   sync.Mutex
	repoCache map[string]repoCacheEntry
}

// NewService 构造服务；Deps 中的零值限额自动补齐为文档 §5.8 的默认值。
func NewService(deps Deps) *Service {
	limits := deps.Limits.withDefaults()
	return &Service{
		roots:     deps.Roots,
		cwd:       deps.Cwd,
		limits:    limits,
		runner:    newRunner(limits),
		repoCache: make(map[string]repoCacheEntry),
	}
}

// Available 探测系统 git 是否可用，供启动期日志或前端降级提示使用。
// 判断是实时的（每次调用重新查 PATH），因此测试可用 t.Setenv 模拟 PATH 无 git。
func (s *Service) Available() bool {
	if s == nil || s.runner == nil {
		return false
	}
	_, err := s.runner.lookPath("git")
	return err == nil
}

// gitContext 是解析后的请求上下文。
type gitContext struct {
	ScopeRoot string // 允许根（绝对路径，已 EvalSymlinks 规范化）
	RepoRoot  string // 仓库根（绝对路径）
}

// prepare 解析 scope/path 并探测仓库（含 TTL 缓存）。scope 校验在包内完成（§5.2）：
// resolver 只负责 scope → 根路径，越界/绝对路径/符号链接逃逸由 gitbrowse 判定。
func (s *Service) prepare(ctx context.Context, req RepoRequest) (*gitContext, error) {
	scopeRoot, err := s.resolveScopeRoot(ctx, req.Scope)
	if err != nil {
		return nil, err
	}
	startDir, err := s.resolveScopedPath(scopeRoot, req.Path)
	if err != nil {
		return nil, err
	}
	if isDir, statErr := isDirectory(startDir); statErr != nil {
		if rootAbs, rootErr := resolveRootPath(scopeRoot); rootErr == nil && startDir == rootAbs {
			return nil, errorf(CodeRepoNotFound, "scope root does not exist: %s", scopeRoot)
		}
		return nil, errorf(CodePathNotFound, "path does not exist: %q", req.Path)
	} else if !isDir {
		return nil, errorf(CodePathNotFound, "path is not a directory: %q", req.Path)
	}

	repoRoot, err := s.probeRepo(ctx, startDir)
	if err != nil {
		return nil, err
	}
	return &gitContext{ScopeRoot: scopeRoot, RepoRoot: repoRoot}, nil
}

// resolveScopeRoot 把 scope 解析为允许根（§5.2 步骤 1–2）。
func (s *Service) resolveScopeRoot(ctx context.Context, scope string) (string, error) {
	trimmed := strings.TrimSpace(scope)
	switch {
	case trimmed == "cwd":
		base := s.cwd
		if base == "" {
			wd, err := os.Getwd()
			if err != nil {
				return "", newError(CodeScopeLookupFailed, "cannot resolve process working directory: "+err.Error())
			}
			base = wd
		}
		return resolveRootPath(base)
	case strings.HasPrefix(trimmed, "workspace:"):
		id := strings.TrimSpace(strings.TrimPrefix(trimmed, "workspace:"))
		if id == "" {
			return "", newError(CodeScopeInvalid, `scope "workspace:" is missing the directory id`)
		}
		if s.roots == nil {
			return "", newError(CodeScopeLookupFailed, "scope resolver is not configured")
		}
		path, ok, err := s.roots.WorkspaceRoot(ctx, id)
		return scopeRootResult(path, ok, err)
	case strings.HasPrefix(trimmed, "session:"):
		id := strings.TrimSpace(strings.TrimPrefix(trimmed, "session:"))
		if id == "" {
			return "", newError(CodeScopeInvalid, `scope "session:" is missing the session id`)
		}
		if s.roots == nil {
			return "", newError(CodeScopeLookupFailed, "scope resolver is not configured")
		}
		path, ok, err := s.roots.SessionRoot(ctx, id)
		return scopeRootResult(path, ok, err)
	default:
		return "", errorf(CodeScopeInvalid, "unsupported scope %q (want workspace:<id>, session:<id> or cwd)", scope)
	}
}

// scopeRootResult 把 resolver 的返回值归一化为根路径或结构化错误。
func scopeRootResult(path string, ok bool, err error) (string, error) {
	switch {
	case stderrors.Is(err, ErrScopeHasNoRoot):
		return "", newError(CodeScopeHasNoRoot, "scope has no workspace root")
	case stderrors.Is(err, ErrScopeNotFound):
		return "", newError(CodeScopeNotFound, "scope not found")
	case err != nil:
		return "", newError(CodeScopeLookupFailed, "scope lookup failed: "+err.Error())
	case !ok:
		return "", newError(CodeScopeNotFound, "scope not found")
	default:
		return resolveRootPath(path)
	}
}

// resolveRootPath 规范化根路径：Abs + Clean + EvalSymlinks（失败则退回 Abs 结果，§5.2 步骤 2）。
func resolveRootPath(path string) (string, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return "", newError(CodeScopeHasNoRoot, "scope resolved to an empty root")
	}
	abs, err := filepath.Abs(trimmed)
	if err != nil {
		return "", newError(CodeScopeInvalid, "cannot resolve root path: "+err.Error())
	}
	abs = filepath.Clean(abs)
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return filepath.Clean(resolved), nil
	}
	return abs, nil
}

// resolveScopedPath 把「作用域相对路径」解析为绝对路径并做越界校验（§5.2 步骤 3–5）。
// 校验不依赖目标是否存在（diff 的目标可能是已删除文件），存在时额外做一次符号链接复核。
func (s *Service) resolveScopedPath(root, rel string) (string, error) {
	value := strings.TrimSpace(rel)
	if strings.ContainsRune(value, 0) {
		return "", newError(CodePathInvalid, "path must not contain NUL bytes")
	}
	if filepath.IsAbs(value) || filepath.VolumeName(value) != "" ||
		strings.HasPrefix(value, "/") || strings.HasPrefix(value, `\`) {
		return "", errorf(CodePathMustBeRelative, "path must be relative to the selected root: %q", rel)
	}
	clean := filepath.Clean(filepath.FromSlash(value))
	if clean == "." {
		clean = ""
	}
	target := filepath.Join(root, clean)
	if !withinRoot(root, target) {
		return "", errorf(CodePathOutsideScope, "path escapes the selected root: %q", rel)
	}
	// 目标已存在时按符号链接复核一次，防止链接逃逸。
	if resolved, err := filepath.EvalSymlinks(target); err == nil {
		if !withinRoot(root, resolved) {
			return "", errorf(CodePathOutsideScope, "path resolves outside the selected root: %q", rel)
		}
		return filepath.Clean(resolved), nil
	}
	return target, nil
}

// withinRoot 判断 target 是否位于 root 之内：Windows 大小写不敏感，分隔符严格比较（§5.2 步骤 4）。
func withinRoot(root, target string) bool {
	rootClean := filepath.Clean(root)
	targetClean := filepath.Clean(target)
	if targetClean == rootClean {
		return true
	}
	if runtime.GOOS == "windows" {
		rootClean = strings.ToLower(rootClean)
		targetClean = strings.ToLower(targetClean)
	}
	return strings.HasPrefix(targetClean, rootClean+string(filepath.Separator))
}

// isDirectory 返回路径是否为目录；不存在的错误原样返回。
func isDirectory(path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	return info.IsDir(), nil
}

// revisionPattern 限定可传给 git 的版本/游标形态：字母数字开头，后续允许字母数字与 . _ / -。
// 这样既拒绝 `-` 开头的选项注入，也拒绝空格/引号等 shell 元字符（即使我们不经过 shell）。
var revisionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`)

// validRevision 判断版本/游标字符串是否可以安全地作为 git 参数。
func validRevision(rev string) bool {
	return revisionPattern.MatchString(strings.TrimSpace(rev))
}

// hexRevisionPattern 限定分页游标必须是 7–40 位十六进制 sha（我们自己的 next_cursor 形态）。
// 比 validRevision 更严：游标不接受 HEAD/分支名这类引用，避免游标被当成任意版本解析。
var hexRevisionPattern = regexp.MustCompile(`^[0-9a-fA-F]{7,40}$`)

// isHexRevision 判断字符串是否为合法的 commit sha 游标。
func isHexRevision(rev string) bool {
	return hexRevisionPattern.MatchString(strings.TrimSpace(rev))
}

// rejectOptionLike 拒绝形似命令行选项的路径参数（§D7：参数数组 + `--` 分隔）。
func rejectOptionLike(kind, value string) error {
	if strings.HasPrefix(value, "-") {
		return errorf(CodePathInvalid, "%s must not start with '-': %q", kind, value)
	}
	return nil
}

// repoRelative 把「作用域相对路径」转换为 git 需要的「仓库相对 POSIX 路径」。
// 无法表达（落在仓库之外）时返回错误。
func repoRelative(scopeRoot, repoRoot, scopeRel string) (string, error) {
	abs := filepath.Join(scopeRoot, filepath.FromSlash(scopeRel))
	rel, err := filepath.Rel(repoRoot, abs)
	if err != nil {
		return "", errorf(CodePathOutsideScope, "file is outside the repository: %q", scopeRel)
	}
	rel = filepath.ToSlash(rel)
	if rel == ".." || strings.HasPrefix(rel, "../") {
		return "", errorf(CodePathOutsideScope, "file is outside the repository: %q", scopeRel)
	}
	return rel, nil
}

// scopeRelative 把仓库内绝对路径转换为「作用域相对 POSIX 路径」。
// ok=false 表示该路径在作用域之外（调用方应隐藏该条并记录 warning，而不是静默丢弃）。
func scopeRelative(scopeRoot, abs string) (string, bool) {
	rel, err := filepath.Rel(scopeRoot, abs)
	if err != nil {
		return "", false
	}
	rel = filepath.ToSlash(rel)
	if rel == ".." || strings.HasPrefix(rel, "../") {
		return "", false
	}
	if rel == "." {
		return "", true
	}
	return rel, true
}

// repoCacheEntry 是仓库探测结果（成功或失败）的缓存条目。
type repoCacheEntry struct {
	root    string
	err     error
	created time.Time
}

// probeRepo 从 startDir 上溯探测仓库根，结果按 startDir 缓存 TTL（默认 30s，§5.5）。
// 探测规则：`rev-parse --show-toplevel --is-inside-work-tree`；裸库/.git 目录不提供浏览。
func (s *Service) probeRepo(ctx context.Context, startDir string) (string, error) {
	key := filepath.Clean(startDir)
	if runtime.GOOS == "windows" {
		key = strings.ToLower(key)
	}
	s.cacheMu.Lock()
	if entry, ok := s.repoCache[key]; ok && time.Since(entry.created) < s.limits.RepoCacheTTL {
		s.cacheMu.Unlock()
		return entry.root, entry.err
	}
	s.cacheMu.Unlock()

	result, err := s.runner.git(ctx, startDir, "git rev-parse", s.limits.StatusTimeout, probeMaxOutputBytes,
		"rev-parse", "--show-toplevel", "--is-inside-work-tree")
	var repoRoot string
	switch {
	case err == nil:
		if result.truncated {
			// 探测输出被截断会解析出错误的仓库根，宁可直接失败也不静默用坏路径。
			err = newError(CodeGitFailed, "git rev-parse output truncated")
			break
		}
		repoRoot, err = parseToplevel(result.stdout)
	case IsCode(err, CodeGitFailed) && ExitCode(err) != 0:
		// rev-parse 非零退出 = 不是仓库（含裸库 / .git 目录）→ 404，而不是 500。
		err = newError(CodeRepoNotFound, "not a git repository: "+err.Error())
	}

	s.cacheMu.Lock()
	if len(s.repoCache) > 256 {
		s.repoCache = make(map[string]repoCacheEntry)
	}
	s.repoCache[key] = repoCacheEntry{root: repoRoot, err: err, created: time.Now()}
	s.cacheMu.Unlock()
	return repoRoot, err
}

// parseToplevel 解析 rev-parse 的两行输出；非 worktree（裸库 / .git 目录）按 repo_not_found 处理。
func parseToplevel(stdout []byte) (string, error) {
	lines := strings.Split(strings.TrimRight(string(stdout), "\r\n"), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) == "" {
		return "", newError(CodeRepoNotFound, "not a git repository")
	}
	root := filepath.Clean(filepath.FromSlash(strings.TrimSpace(lines[0])))
	if len(lines) > 1 && strings.TrimSpace(lines[1]) != "true" {
		return "", newError(CodeRepoNotFound, "not a git working tree")
	}
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = filepath.Clean(resolved)
	}
	return root, nil
}

// StageRequest 是 POST /git/stage 的请求体（P4-1）。files 为作用域相对路径，
// action 只允许 stage | unstage（commit 本期不做，规划 Q4）。
type StageRequest struct {
	RepoRequest
	Action string   `json:"action"`
	Files  []string `json:"files"`
}

// StageResult 是 stage/unstage 的结果：实际生效的动作、文件与更新后的状态摘要
// （Status 与 GET /git/status 同构，前端可直接替换本地缓存）。
type StageResult struct {
	Action      string        `json:"action"`
	Files       []string      `json:"files"`
	Status      *StatusResult `json:"status"`
	GeneratedAt int64         `json:"generated_at"`
}

// Stage 执行 stage（git add）/ unstage（git restore --staged），并返回更新后的状态摘要。
// 写操作固定记录一条结构化日志（§5.2：scope/path/files/action）。
func (s *Service) Stage(ctx context.Context, req StageRequest) (*StageResult, error) {
	action := strings.ToLower(strings.TrimSpace(req.Action))
	if action != "stage" && action != "unstage" {
		return nil, errorf(CodeActionInvalid, `action must be "stage" or "unstage": %q`, req.Action)
	}
	if len(req.Files) == 0 {
		return nil, newError(CodeFilesRequired, "files must not be empty")
	}
	gitCtx, err := s.prepare(ctx, req.RepoRequest)
	if err != nil {
		return nil, err
	}

	// 先全量校验再执行：不允许「一半文件越界、一半已 stage」的部分生效。
	pathspecs := make([]string, 0, len(req.Files))
	files := make([]string, 0, len(req.Files))
	for _, file := range req.Files {
		scopeRel, err := s.normalizeFile(gitCtx.ScopeRoot, file)
		if err != nil {
			return nil, err
		}
		repoRel, err := repoRelative(gitCtx.ScopeRoot, gitCtx.RepoRoot, scopeRel)
		if err != nil {
			return nil, err
		}
		if err := rejectOptionLike("file", repoRel); err != nil {
			return nil, err
		}
		pathspecs = append(pathspecs, repoRel)
		files = append(files, scopeRel)
	}

	args, label := append([]string{"add", "--"}, pathspecs...), "git add"
	if action == "unstage" {
		args, label = append([]string{"restore", "--staged", "--"}, pathspecs...), "git restore --staged"
	}
	if _, err := s.runner.git(ctx, gitCtx.RepoRoot, label, s.limits.StageTimeout, 0, args...); err != nil {
		if action != "unstage" || !isUnknownSubcommand(err) {
			return nil, err
		}
		// 旧版 git（<2.23）没有 restore：退回 `reset -- <paths>` 兼容。
		fallback := append([]string{"reset", "--"}, pathspecs...)
		if _, resetErr := s.runner.git(ctx, gitCtx.RepoRoot, "git reset", s.limits.StageTimeout, 0, fallback...); resetErr != nil {
			return nil, resetErr
		}
	}

	observability.InfoWithFields(map[string]interface{}{
		"event":  "git_stage",
		"scope":  strings.TrimSpace(req.Scope),
		"path":   strings.TrimSpace(req.Path),
		"files":  files,
		"action": action,
		"root":   gitCtx.RepoRoot,
	}, "git 写操作")

	status, err := s.status(ctx, gitCtx)
	if err != nil {
		return nil, err
	}
	return &StageResult{
		Action:      action,
		Files:       files,
		Status:      status,
		GeneratedAt: time.Now().Unix(),
	}, nil
}

// normalizeFile 校验单个文件参数并返回「作用域相对 POSIX 路径」。
func (s *Service) normalizeFile(scopeRoot, file string) (string, error) {
	trimmed := strings.TrimSpace(file)
	if trimmed == "" {
		return "", newError(CodeFilesRequired, "file must not be empty")
	}
	if err := rejectOptionLike("file", trimmed); err != nil {
		return "", err
	}
	abs, err := s.resolveScopedPath(scopeRoot, trimmed)
	if err != nil {
		return "", err
	}
	rel, ok := scopeRelative(scopeRoot, abs)
	if !ok {
		return "", errorf(CodePathOutsideScope, "file is outside the selected root: %q", file)
	}
	return rel, nil
}

// isUnknownSubcommand 判断错误是否为「git 不认识该子命令/选项」，用于 restore → reset 降级。
func isUnknownSubcommand(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "is not a git command") || strings.Contains(message, "unknown option")
}
