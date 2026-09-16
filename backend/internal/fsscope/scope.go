// Package fsscope 解析「作用域（scope）+ 相对路径」，把前端可控的字符串收敛为一个
// 受作用域根约束的绝对路径（见 docs/plan/workspace-right-panel-file-browser-and-git-diff-plan.md §5.2）。
//
// 设计边界：
//   - scope 只有三种形态：workspace:<id> / session:<id> / cwd；
//   - path 一律相对作用域根；绝对路径直接拒绝（path_must_be_relative），从接口层面
//     杜绝「前端拼错一个变量就逛全盘」；
//   - 顺序固定：URL 解码 → 统一分隔符 → Clean → Join → 越界校验；顺序不可颠倒，
//     否则 %2e%2e 之类的编码绕过会被漏掉；
//   - 校验基于「已规范化的根」：Windows 大小写不敏感比较；目标（或其最近的已存在
//     祖先）经 EvalSymlinks 后仍必须落在根内，防符号链接逃逸。
//
// 诚实说明：这是**能力收敛 + 防误操作 + 让 API 语义可审计**，不是多租户隔离 ——
// 本机 runtime 是单用户信任模型，agent 本来就有 shell（见规划 §1.3 非目标 1）。
package fsscope

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
)

// Kind 是作用域类型。
type Kind string

const (
	KindWorkspace Kind = "workspace"
	KindSession   Kind = "session"
	KindCwd       Kind = "cwd"
)

// 机器可读错误码：与 HTTP 状态一起由 handler 渲染为统一错误响应（规划 §5.7）。
const (
	CodeScopeInvalid       = "scope_invalid"
	CodeScopeNotFound      = "scope_not_found"
	CodeScopeHasNoRoot     = "scope_has_no_root"
	CodePathInvalid        = "path_invalid"
	CodePathMustBeRelative = "path_must_be_relative"
	CodePathOutsideScope   = "path_outside_scope"
	CodePathNotFound       = "path_not_found"
	CodePathNotDirectory   = "path_not_directory"
	CodePathNotFile        = "path_not_file"
	CodePathPermission     = "path_permission_denied"
	CodeFSReadFailed       = "fs_read_failed"
	CodeFSWriteFailed      = "fs_write_failed"
	CodeServiceUnavailable = "service_unavailable"
)

// Error 是带机器码的错误；handler 用 Code / HTTPStatus / Details 统一渲染错误响应，
// 不依赖字符串匹配。
type Error struct {
	Code       string
	Message    string
	HTTPStatus int
	Details    map[string]interface{}
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

// NewError 构造带机器码与 HTTP 状态的错误。
func NewError(code string, status int, message string) *Error {
	return &Error{Code: code, Message: message, HTTPStatus: status}
}

// NewErrorf 是 NewError 的格式化版本。
func NewErrorf(code string, status int, format string, args ...interface{}) *Error {
	return NewError(code, status, fmt.Sprintf(format, args...))
}

// WithDetail 返回附加上下文字段的副本（不修改原对象，便于错误复用）。
func (e *Error) WithDetail(key string, value interface{}) *Error {
	if e == nil {
		return nil
	}
	clone := &Error{Code: e.Code, Message: e.Message, HTTPStatus: e.HTTPStatus}
	if len(e.Details) > 0 {
		clone.Details = make(map[string]interface{}, len(e.Details)+1)
		for k, v := range e.Details {
			clone.Details[k] = v
		}
	} else {
		clone.Details = make(map[string]interface{}, 1)
	}
	clone.Details[key] = value
	return clone
}

// AsError 把 error 还原为 *Error（含包装链）。
func AsError(err error) (*Error, bool) {
	if err == nil {
		return nil, false
	}
	var target *Error
	if errors.As(err, &target) {
		return target, true
	}
	return nil, false
}

// CodeOf 返回错误的机器码；非 *Error 返回空串。
func CodeOf(err error) string {
	if target, ok := AsError(err); ok {
		return target.Code
	}
	return ""
}

// StatusOf 返回错误应映射的 HTTP 状态；非 *Error 时用 fallback。
func StatusOf(err error, fallback int) int {
	if target, ok := AsError(err); ok && target.HTTPStatus > 0 {
		return target.HTTPStatus
	}
	return fallback
}

// DetailsOf 返回错误附带的响应字段（如 expected_offset）。
func DetailsOf(err error) map[string]interface{} {
	if target, ok := AsError(err); ok {
		return target.Details
	}
	return nil
}

// IsCode 判断错误链上的机器码是否等于 code。
func IsCode(err error, code string) bool {
	return CodeOf(err) == code
}

// Scope 是解析后的作用域。
type Scope struct {
	Raw  string // 规范化后的原样文本（如 workspace:wd_ab12），前端可直接回传
	Kind Kind
	ID   string // workspace/session 的 id；cwd 为空
}

// ParseScope 解析 scope 参数；格式非法返回 400 scope_invalid。
func ParseScope(raw string) (Scope, *Error) {
	value := strings.TrimSpace(DecodeQueryValue(raw))
	if value == "" {
		return Scope{}, NewError(CodeScopeInvalid, 400, "scope is required (workspace:<id>|session:<id>|cwd)")
	}
	if strings.EqualFold(value, string(KindCwd)) {
		return Scope{Raw: string(KindCwd), Kind: KindCwd}, nil
	}
	kind, id, found := strings.Cut(value, ":")
	if !found {
		return Scope{}, NewError(CodeScopeInvalid, 400, "scope must be workspace:<id>, session:<id> or cwd")
	}
	id = strings.TrimSpace(id)
	if id == "" || strings.ContainsAny(id, "/\\") {
		return Scope{}, NewError(CodeScopeInvalid, 400, "scope id is empty or invalid")
	}
	switch Kind(strings.ToLower(strings.TrimSpace(kind))) {
	case KindWorkspace:
		return Scope{Raw: string(KindWorkspace) + ":" + id, Kind: KindWorkspace, ID: id}, nil
	case KindSession:
		return Scope{Raw: string(KindSession) + ":" + id, Kind: KindSession, ID: id}, nil
	default:
		return Scope{}, NewError(CodeScopeInvalid, 400, "unsupported scope kind: "+kind)
	}
}

// RootResolver 由调用方注入：把作用域 id 解析为绝对路径。
// ok=false 表示「查询成功但没有对应记录」，与 err!=nil（查询本身失败）语义不同。
type RootResolver interface {
	WorkspaceRoot(ctx context.Context, id string) (path string, ok bool, err error)
	SessionRoot(ctx context.Context, sessionID string) (path string, ok bool, err error)
}

// WorkspaceRoot 是注册工作目录的最小投影（供 /fs/roots 枚举）。
type WorkspaceRoot struct {
	ID   string
	Path string
	Name string
}

// RootLister 是 RootResolver 的**可选扩展**：枚举已注册工作目录。
// 未实现时 /fs/roots 只返回会话根与 cwd，不影响其它端点（additive，见规划 §5.3）。
type RootLister interface {
	ListWorkspaceRoots(ctx context.Context) ([]WorkspaceRoot, error)
}

// Root 是解析并校验后的允许根。
type Root struct {
	Scope   Scope
	Path    string // 规范化绝对路径（存在时已 EvalSymlinks），用于 join 与越界比较
	Raw     string // resolver 给出的原始路径（仅用于展示「复制绝对路径」）
	Name    string // 展示名（缺省为目录名）
	Exists  bool
	Session string // session scope 的会话 id（workspace/cwd 为空）
}

// Resolver 负责 scope → 根路径、相对 path → 目标路径的解析与校验。
type Resolver struct {
	Roots RootResolver
	Cwd   string
}

// NewResolver 构造解析器；cwd 为空时使用 os.Getwd()。
func NewResolver(roots RootResolver, cwd string) *Resolver {
	return &Resolver{Roots: roots, Cwd: strings.TrimSpace(cwd)}
}

// Resolve 解析作用域本身（不含具体路径）。
func (r *Resolver) Resolve(ctx context.Context, rawScope string) (*Root, *Error) {
	if r == nil {
		return nil, NewError(CodeScopeInvalid, 400, "scope resolver is not configured")
	}
	scope, scopeErr := ParseScope(rawScope)
	if scopeErr != nil {
		return nil, scopeErr
	}
	raw, rootErr := r.resolveRootPath(ctx, scope)
	if rootErr != nil {
		return nil, rootErr
	}
	if !looksAbsolute(raw) {
		abs, err := filepath.Abs(raw)
		if err != nil {
			return nil, NewErrorf(CodeFSReadFailed, 500, "resolve scope root failed: %v", err)
		}
		raw = abs
	}
	canonical := filepath.Clean(raw)
	if resolved, ok := evalSymlinks(raw); ok {
		canonical = resolved
	}
	root := &Root{Scope: scope, Path: canonical, Raw: filepath.Clean(raw), Name: filepath.Base(canonical)}
	if scope.Kind == KindSession {
		root.Session = scope.ID
	}
	info, statErr := os.Stat(canonical)
	switch {
	case statErr == nil && !info.IsDir():
		return nil, NewError(CodePathNotDirectory, 400, "scope root is not a directory: "+root.Raw)
	case statErr == nil:
		root.Exists = true
	}
	return root, nil
}

func (r *Resolver) resolveRootPath(ctx context.Context, scope Scope) (string, *Error) {
	switch scope.Kind {
	case KindCwd:
		if r.Cwd != "" {
			return r.Cwd, nil
		}
		cwd, err := os.Getwd()
		if err != nil {
			return "", NewErrorf(CodeFSReadFailed, 500, "resolve process cwd failed: %v", err)
		}
		return cwd, nil
	case KindWorkspace:
		if r.Roots == nil {
			return "", NewError(CodeScopeNotFound, 404, "workspace registry is not available")
		}
		value, ok, err := r.Roots.WorkspaceRoot(ctx, scope.ID)
		if err != nil {
			return "", NewErrorf(CodeFSReadFailed, 500, "resolve workspace root failed: %v", err)
		}
		if !ok {
			return "", NewErrorf(CodeScopeNotFound, 404, "workspace %s is not registered", scope.ID)
		}
		return value, nil
	case KindSession:
		if r.Roots == nil {
			return "", NewError(CodeScopeHasNoRoot, 400, "session workspace is not available")
		}
		value, ok, err := r.Roots.SessionRoot(ctx, scope.ID)
		if err != nil {
			return "", NewErrorf(CodeFSReadFailed, 500, "resolve session workspace failed: %v", err)
		}
		if !ok {
			return "", NewErrorf(CodeScopeHasNoRoot, 400, "session %s has no workspace directory", scope.ID)
		}
		return value, nil
	default:
		return "", NewError(CodeScopeInvalid, 400, "unsupported scope")
	}
}

// Target 是解析并校验后的目标路径。
type Target struct {
	Root   *Root
	Rel    string // 相对根的路径（分隔符统一 `/`；根为 ""）
	Abs    string // 目标绝对路径（不保证存在）
	Exists bool
}

// ResolvePath 解析 scope + 相对 path，并完成越界与符号链接校验。
func (r *Resolver) ResolvePath(ctx context.Context, rawScope, rawPath string) (*Target, *Error) {
	root, rootErr := r.Resolve(ctx, rawScope)
	if rootErr != nil {
		return nil, rootErr
	}
	rel, relErr := NormalizeRelPath(rawPath)
	if relErr != nil {
		return nil, relErr
	}
	abs := root.Path
	if rel != "" {
		abs = filepath.Join(root.Path, filepath.FromSlash(rel))
	}
	if !pathWithin(root.Path, abs) {
		return nil, NewError(CodePathOutsideScope, 400, "path escapes the selected scope root")
	}
	canonical, exists := evalWithMissingTail(abs)
	if !pathWithin(root.Path, canonical) {
		return nil, NewError(CodePathOutsideScope, 400, "path escapes the selected scope root through a symbolic link")
	}
	return &Target{Root: root, Rel: rel, Abs: canonical, Exists: exists}, nil
}

// NormalizeRelPath 归一化相对路径：URL 解码 → 统一分隔符 → 拒绝绝对路径 → Clean。
func NormalizeRelPath(raw string) (string, *Error) {
	value := DecodeQueryValue(raw)
	if strings.ContainsRune(value, '\x00') {
		return "", NewError(CodePathInvalid, 400, "path contains illegal characters")
	}
	unified := strings.ReplaceAll(strings.TrimSpace(value), "\\", "/")
	if unified == "/" { // 规划 §5.2：空 / "." / "/" 均表示作用域根
		return "", nil
	}
	if looksAbsolute(unified) {
		return "", NewError(CodePathMustBeRelative, 400, "path must be relative to the scope root")
	}
	cleaned := path.Clean(unified)
	switch {
	case cleaned == ".", cleaned == "/":
		return "", nil
	case cleaned == "..", strings.HasPrefix(cleaned, "../"):
		return "", NewError(CodePathOutsideScope, 400, "path escapes the selected scope root")
	}
	return cleaned, nil
}

// DecodeQueryValue 对查询参数做一次额外 URL 解码（net/url 已解码一层），用于覆盖
// %252e%252e 之类的双重编码。解码失败或原文无转义时原样返回，不静默改写用户输入。
//
// 用 PathUnescape 而非 QueryUnescape：后者会把文件名里的 `+` 变成空格。
func DecodeQueryValue(value string) string {
	if !strings.Contains(value, "%") {
		return value
	}
	decoded, err := url.PathUnescape(value)
	if err != nil {
		return value
	}
	return decoded
}

// looksAbsolute 跨平台判定绝对路径（含 Windows 盘符与 UNC，避免在非 Windows 上漏判）。
func looksAbsolute(p string) bool {
	if p == "" {
		return false
	}
	if filepath.IsAbs(p) {
		return true
	}
	if p[0] == '/' || p[0] == '\\' {
		return true
	}
	if len(p) >= 2 && p[1] == ':' {
		c := p[0]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
			return true
		}
	}
	return false
}

// pathWithin 判断 target 是否等于 root 或位于 root 之下（Windows 大小写不敏感）。
func pathWithin(root, target string) bool {
	rootClean := filepath.Clean(root)
	targetClean := filepath.Clean(target)
	if equalPath(rootClean, targetClean) {
		return true
	}
	sep := string(filepath.Separator)
	prefix := rootClean
	if !strings.HasSuffix(prefix, sep) {
		prefix += sep
	}
	if len(targetClean) < len(prefix) {
		return false
	}
	return equalPath(targetClean[:len(prefix)], prefix)
}

func equalPath(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// evalWithMissingTail 解析符号链接：目标不存在时向上找到最近的已存在祖先再解析，
// 一并覆盖「已存在目标」与「待创建文件（父目录是符号链接）」两种逃逸路径。
func evalWithMissingTail(absPath string) (string, bool) {
	if resolved, ok := evalSymlinks(absPath); ok {
		return resolved, true
	}
	current := filepath.Clean(absPath)
	tail := ""
	for {
		parent := filepath.Dir(current)
		if parent == current {
			return filepath.Clean(absPath), false
		}
		tail = filepath.Join(filepath.Base(current), tail)
		current = parent
		if resolved, ok := evalSymlinks(current); ok {
			return filepath.Join(resolved, tail), false
		}
	}
}

func evalSymlinks(p string) (string, bool) {
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil || strings.TrimSpace(resolved) == "" {
		return "", false
	}
	abs, err := filepath.Abs(resolved)
	if err != nil {
		return "", false
	}
	return filepath.Clean(abs), true
}

type sessionIDContextKey struct{}

// WithSessionID 把当前会话 id 放进 ctx，供 /fs/roots 附带会话根（不改变注入接口签名）。
func WithSessionID(ctx context.Context, sessionID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, sessionIDContextKey{}, strings.TrimSpace(sessionID))
}

// SessionIDFromContext 读取 WithSessionID 写入的会话 id（缺省为空）。
func SessionIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	value, _ := ctx.Value(sessionIDContextKey{}).(string)
	return strings.TrimSpace(value)
}
