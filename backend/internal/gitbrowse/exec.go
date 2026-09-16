// Package gitbrowse 实现「工作区右侧栏 Git 变更浏览器」的后端能力：通过 git CLI
// （exec.CommandContext + 参数数组，不拼 shell 字符串）提供仓库探测、变更状态、
// 结构化 diff、提交列表与 stage/unstage。
//
// 设计约束见 docs/plan/workspace-right-panel-file-browser-and-git-diff-plan.md
// （D7 与 §5.5–§5.8）：
//   - 固定全局参数 `-C <root> --no-pager --no-optional-locks`；
//   - 固定环境 GIT_OPTIONAL_LOCKS=0 / GIT_TERMINAL_PROMPT=0 / LC_ALL=C / GIT_PAGER=cat；
//   - 子命令独立超时（status 5s / diff 15s / log 10s），输出上限 4MiB（超限截断并标记）；
//   - git 缺失 → 503 git_unavailable（不 panic、不 500）；非零退出 → 500 git_failed +
//     exit_code + stderr 截断尾部（≤2KB）。
//
// 本文件只负责「进程沙箱 + 错误模型 + 限额」，不做任何 git 语义解析。
package gitbrowse

import (
	"bytes"
	"context"
	stderrors "errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

// 错误码：与规划文档 §5.7 的错误表对齐。
// target_invalid / action_invalid / files_required / scope_lookup_failed 是实施期补充的
// 附加码，用于错误表中未列出的输入校验分支，命名与语义风格保持一致。
const (
	CodeGitUnavailable     = "git_unavailable"
	CodeGitFailed          = "git_failed"
	CodeRepoNotFound       = "repo_not_found"
	CodeScopeInvalid       = "scope_invalid"
	CodeScopeNotFound      = "scope_not_found"
	CodeScopeHasNoRoot     = "scope_has_no_root"
	CodeScopeLookupFailed  = "scope_lookup_failed"
	CodePathMustBeRelative = "path_must_be_relative"
	CodePathOutsideScope   = "path_outside_scope"
	CodePathInvalid        = "path_invalid"
	CodePathNotFound       = "path_not_found"
	CodeCursorInvalid      = "cursor_invalid"
	CodeTargetInvalid      = "target_invalid"
	CodeActionInvalid      = "action_invalid"
	CodeFilesRequired      = "files_required"
	CodeInvalidRequest     = "invalid_request"
)

// 限制相关常量：stderr 只保留尾部用于排障（§5.7），字节数值与文档 §5.8 一致。
const (
	maxStderrBytes = 2 * 1024
	maxOutputBytes = 4 * 1024 * 1024
	maxCommitLimit = 200
	maxDiffLines   = 20000
	// maxUntrackedScanBytes 见 Limits.MaxUntrackedScanBytes。
	maxUntrackedScanBytes = 16 * 1024 * 1024
	// probeMaxOutputBytes 是仓库探测（rev-parse）的内部输出上限：
	// 它是内部命令而非响应内容，固定给足空间，避免被 MaxOutputBytes 截断后解析出错误的仓库根。
	probeMaxOutputBytes = 1 << 20
)

// codeHTTPStatus 给出错误码对应的 HTTP 状态码（§5.7）。
func codeHTTPStatus(code string) int {
	switch code {
	case CodeScopeInvalid, CodeScopeHasNoRoot, CodePathMustBeRelative,
		CodePathOutsideScope, CodePathInvalid, CodeCursorInvalid,
		CodeTargetInvalid, CodeActionInvalid, CodeFilesRequired, CodeInvalidRequest:
		return http.StatusBadRequest
	case CodeScopeNotFound, CodePathNotFound, CodeRepoNotFound:
		return http.StatusNotFound
	case CodeGitUnavailable:
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}

// Error 是 gitbrowse 暴露给 HTTP 层的结构化错误：错误码 + 人类可读消息 + 可选 git 退出码。
// 非 Error 类型的错误由 HTTPStatus/Code 按 git_failed(500) 兜底，绝不 panic。
type Error struct {
	Code     string
	Message  string
	Status   int
	ExitCode int
}

// Error 实现 error 接口。
func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

// newError 构造结构化错误，HTTP 状态码由错误码推导。
func newError(code, message string) *Error {
	return &Error{Code: code, Message: message, Status: codeHTTPStatus(code)}
}

// errorf 是 newError 的格式化版本。
func errorf(code, format string, args ...interface{}) *Error {
	return newError(code, fmt.Sprintf(format, args...))
}

// asError 把任意 error 归一化为 *Error；nil 返回 nil。
func asError(err error) *Error {
	if err == nil {
		return nil
	}
	var structured *Error
	if stderrors.As(err, &structured) {
		return structured
	}
	return &Error{Code: CodeGitFailed, Message: err.Error(), Status: http.StatusInternalServerError}
}

// Code 返回错误码；非结构化错误按 git_failed 处理。
func Code(err error) string {
	if structured := asError(err); structured != nil {
		return structured.Code
	}
	return ""
}

// HTTPStatus 返回错误对应的 HTTP 状态码；nil 返回 200。
func HTTPStatus(err error) int {
	if structured := asError(err); structured != nil {
		return structured.Status
	}
	return http.StatusOK
}

// ExitCode 返回 git 非零退出的退出码；无则返回 0。
func ExitCode(err error) int {
	if structured := asError(err); structured != nil {
		return structured.ExitCode
	}
	return 0
}

// IsCode 判断 err 是否为指定错误码（供 handler 与测试使用）。
func IsCode(err error, code string) bool {
	return Code(err) == code
}

// 作用域解析哨兵错误：父代理提供的 RootResolver 适配器在无法区分「作用域不存在」与
// 「作用域存在但没有工作目录」时，返回这两个哨兵，gitbrowse 会分别映射为
// 404 scope_not_found 与 400 scope_has_no_root（§5.2 步骤 1）。
var (
	// ErrScopeNotFound 表示 workspace/session 记录不存在。
	ErrScopeNotFound = stderrors.New("gitbrowse: scope not found")
	// ErrScopeHasNoRoot 表示作用域存在但没有可用的根目录。
	ErrScopeHasNoRoot = stderrors.New("gitbrowse: scope has no root")
)

// Limits 控制子命令超时、输出上限与探测缓存 TTL（§5.8）；零值字段由 NewService 补默认值。
type Limits struct {
	StatusTimeout  time.Duration // git status 超时，默认 5s
	DiffTimeout    time.Duration // git diff 超时，默认 15s
	LogTimeout     time.Duration // git log 超时，默认 10s
	StageTimeout   time.Duration // git add / restore 超时，默认 15s
	MaxOutputBytes int64         // 单个子命令输出上限，默认 4MiB
	// MaxUntrackedScanBytes 是单次 status 统计未跟踪文件行数的总扫描预算，默认 16MiB：
	// 未跟踪文件不在 numstat 里，只能逐个读文件；极端仓库不能把一次列表请求变成无上限的磁盘读取。
	MaxUntrackedScanBytes int64
	MaxDiffLines          int           // 结构化 diff 行数上限，默认 20000
	MaxCommits            int           // commits 的 limit 上限，默认 200
	RepoCacheTTL          time.Duration // 仓库探测缓存，默认 30s
}

// DefaultLimits 返回文档 §5.8 的默认限额。
func DefaultLimits() Limits {
	return Limits{
		StatusTimeout:         5 * time.Second,
		DiffTimeout:           15 * time.Second,
		LogTimeout:            10 * time.Second,
		StageTimeout:          15 * time.Second,
		MaxOutputBytes:        maxOutputBytes,
		MaxUntrackedScanBytes: maxUntrackedScanBytes,
		MaxDiffLines:          maxDiffLines,
		MaxCommits:            maxCommitLimit,
		RepoCacheTTL:          30 * time.Second,
	}
}

// withDefaults 用默认值补齐零值字段。
func (l Limits) withDefaults() Limits {
	defaults := DefaultLimits()
	if l.StatusTimeout <= 0 {
		l.StatusTimeout = defaults.StatusTimeout
	}
	if l.DiffTimeout <= 0 {
		l.DiffTimeout = defaults.DiffTimeout
	}
	if l.LogTimeout <= 0 {
		l.LogTimeout = defaults.LogTimeout
	}
	if l.StageTimeout <= 0 {
		l.StageTimeout = defaults.StageTimeout
	}
	if l.MaxOutputBytes <= 0 {
		l.MaxOutputBytes = defaults.MaxOutputBytes
	}
	if l.MaxUntrackedScanBytes <= 0 {
		l.MaxUntrackedScanBytes = defaults.MaxUntrackedScanBytes
	}
	if l.MaxDiffLines <= 0 {
		l.MaxDiffLines = defaults.MaxDiffLines
	}
	if l.MaxCommits <= 0 {
		l.MaxCommits = defaults.MaxCommits
	}
	if l.RepoCacheTTL <= 0 {
		l.RepoCacheTTL = defaults.RepoCacheTTL
	}
	return l
}

// cmdResult 是一次子命令执行的原始结果。
type cmdResult struct {
	stdout    []byte
	stderr    []byte
	truncated bool // stdout 是否因超过上限被截断
	exitCode  int
}

// limitedBuffer 收集子命令输出：超过 limit 后丢弃多余字节并置 truncated。
// Write 始终返回 len(p)，否则管道写满会阻塞子进程（死锁）。
type limitedBuffer struct {
	buf       bytes.Buffer
	limit     int64
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.limit <= 0 {
		if len(p) > 0 {
			b.truncated = true
		}
		return len(p), nil
	}
	remaining := b.limit - int64(b.buf.Len())
	if remaining <= 0 {
		if len(p) > 0 {
			b.truncated = true
		}
		return len(p), nil
	}
	if int64(len(p)) > remaining {
		b.buf.Write(p[:remaining])
		b.truncated = true
		return len(p), nil
	}
	b.buf.Write(p)
	return len(p), nil
}

// runProcess 以参数数组执行外部命令（不经过 shell），带超时 kill 与输出截断。
// 所有失败都返回结构化 *Error，调用方不需要再做分类。
func runProcess(ctx context.Context, label, path string, args, env []string, timeout time.Duration, maxBytes int64) (*cmdResult, error) {
	return runProcessWithAllowedExit(ctx, label, path, args, env, timeout, maxBytes, nil)
}

// runProcessWithAllowedExit 是 runProcess 的扩展：allowedExit 中的退出码按成功处理。
// 典型场景是 `git diff --no-index`：两侧存在差异时退出码为 1，那是正常结果而不是失败。
func runProcessWithAllowedExit(ctx context.Context, label, path string, args, env []string, timeout time.Duration, maxBytes int64, allowedExit map[int]bool) (*cmdResult, error) {
	runCtx := ctx
	if timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	cmd := exec.CommandContext(runCtx, path, args...)
	cmd.Env = env
	stdout := &limitedBuffer{limit: maxBytes}
	stderr := &limitedBuffer{limit: maxStderrBytes}
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	runErr := cmd.Run()
	result := &cmdResult{
		stdout:    stdout.buf.Bytes(),
		stderr:    stderr.buf.Bytes(),
		truncated: stdout.truncated,
	}
	if runErr == nil {
		return result, nil
	}

	// 父 context 被取消（客户端断开）优先于超时判定：这不是 git 的失败。
	if ctxErr := ctx.Err(); ctxErr != nil {
		return result, ctxErr
	}
	if runCtx.Err() != nil && timeout > 0 {
		return result, newError(CodeGitFailed, fmt.Sprintf("%s timed out after %s", label, timeout))
	}

	var exitErr *exec.ExitError
	if stderrors.As(runErr, &exitErr) {
		result.exitCode = exitErr.ExitCode()
		if allowedExit[result.exitCode] {
			return result, nil
		}
		message := fmt.Sprintf("%s failed (exit %d): %s", label, result.exitCode, stderrTail(result.stderr))
		structured := newError(CodeGitFailed, strings.TrimSpace(message))
		structured.ExitCode = result.exitCode
		return result, structured
	}

	// 可执行文件不存在 / 无法启动 → 统一按「git 不可用」降级，不 panic、不 500。
	if stderrors.Is(runErr, exec.ErrNotFound) || stderrors.Is(runErr, os.ErrNotExist) {
		return result, newError(CodeGitUnavailable, "git executable not found on PATH")
	}
	return result, errorf(CodeGitFailed, "%s could not be started: %v", label, runErr)
}

// stderrTail 返回 stderr 的截断尾部（≤2KB），供前端与排障使用。
func stderrTail(stderr []byte) string {
	text := strings.TrimSpace(string(stderr))
	if text == "" {
		return "(no stderr)"
	}
	if len(text) <= maxStderrBytes {
		return text
	}
	return "..." + text[len(text)-maxStderrBytes:]
}

// formatBytes 用于 truncated_reason 的可读描述。
func formatBytes(n int64) string {
	const (
		kib = int64(1024)
		mib = 1024 * kib
	)
	switch {
	case n >= mib:
		return fmt.Sprintf("%dMiB", n/mib)
	case n >= kib:
		return fmt.Sprintf("%dKiB", n/kib)
	default:
		return fmt.Sprintf("%dB", n)
	}
}

// runner 是 git 进程沙箱：固定全局参数、固定环境、独立超时与输出上限。
type runner struct {
	limits   Limits
	lookPath func(string) (string, error)
}

// newRunner 用给定限额构造 runner；lookPath 可被测试替换（模拟 PATH 无 git）。
func newRunner(limits Limits) *runner {
	return &runner{limits: limits, lookPath: exec.LookPath}
}

// gitEnv 在父进程环境之上覆盖 git 需要的固定变量（Windows 依赖 PATH，因此保留原环境）。
// 同名变量的后出现者生效（os/exec 会去重并保留最后一个）。
func gitEnv() []string {
	return append(os.Environ(),
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_TERMINAL_PROMPT=0",
		"LC_ALL=C",
		"GIT_PAGER=cat",
	)
}

// git 执行 `git -C <root> --no-pager --no-optional-locks <args...>`。
// maxBytes <= 0 时使用 limits.MaxOutputBytes。
func (r *runner) git(ctx context.Context, root, label string, timeout time.Duration, maxBytes int64, args ...string) (*cmdResult, error) {
	return r.gitAllowingExit(ctx, root, label, timeout, maxBytes, nil, args...)
}

// gitAllowingExit 与 git 相同，但把 allowedExit 中的退出码视为成功
// （`git diff --no-index` 在两侧存在差异时退出码为 1）。
func (r *runner) gitAllowingExit(ctx context.Context, root, label string, timeout time.Duration, maxBytes int64, allowedExit map[int]bool, args ...string) (*cmdResult, error) {
	gitPath, err := r.lookPath("git")
	if err != nil {
		return nil, newError(CodeGitUnavailable, "git executable not found on PATH: "+err.Error())
	}
	if maxBytes <= 0 {
		maxBytes = r.limits.MaxOutputBytes
	}
	return runProcessWithAllowedExit(ctx, label, gitPath, gitArgs(root, args), gitEnv(), timeout, maxBytes, allowedExit)
}

// gitArgs 组装固定的 git 全局参数：`-C <root> --no-pager --no-optional-locks <args…>`。
// 子命令参数一律以数组传入（不拼 shell 字符串），路径类参数由调用方用 `--` 分隔。
func gitArgs(root string, args []string) []string {
	full := make([]string, 0, len(args)+4)
	full = append(full, "-C", root, "--no-pager", "--no-optional-locks")
	return append(full, args...)
}
