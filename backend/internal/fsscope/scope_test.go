package fsscope

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type stubRoots struct {
	workspaces map[string]string
	sessions   map[string]string
	err        error
}

func (s stubRoots) WorkspaceRoot(_ context.Context, id string) (string, bool, error) {
	if s.err != nil {
		return "", false, s.err
	}
	path, ok := s.workspaces[id]
	return path, ok, nil
}

func (s stubRoots) SessionRoot(_ context.Context, id string) (string, bool, error) {
	if s.err != nil {
		return "", false, s.err
	}
	path, ok := s.sessions[id]
	return path, ok, nil
}

func TestParseScopeAcceptsSupportedForms(t *testing.T) {
	cases := []struct {
		raw  string
		kind Kind
		id   string
	}{
		{"cwd", KindCwd, ""},
		{"CWD", KindCwd, ""},
		{"workspace:wd_ab12", KindWorkspace, "wd_ab12"},
		{" session:sess_9f ", KindSession, "sess_9f"},
		{"workspace:%77d_ab12", KindWorkspace, "wd_ab12"},
	}
	for _, tc := range cases {
		scope, err := ParseScope(tc.raw)
		require.Nil(t, err, tc.raw)
		require.Equal(t, tc.kind, scope.Kind, tc.raw)
		require.Equal(t, tc.id, scope.ID, tc.raw)
		require.NotEmpty(t, scope.Raw, tc.raw)
	}
}

func TestParseScopeRejectsInvalidForms(t *testing.T) {
	for _, raw := range []string{"", "   ", "workspace", "workspace:", "session:", "a:b:c", "/abs", "workspace:a/b"} {
		scope, err := ParseScope(raw)
		require.NotNil(t, err, raw)
		require.Equal(t, CodeScopeInvalid, err.Code, raw)
		require.Equal(t, 400, err.HTTPStatus, raw)
		require.Empty(t, scope.Kind, raw)
	}
}

func TestNormalizeRelPath(t *testing.T) {
	cases := []struct {
		raw      string
		expected string
		code     string
	}{
		{raw: "", expected: ""},
		{raw: ".", expected: ""},
		{raw: "/", expected: ""},
		{raw: "src/main.ts", expected: "src/main.ts"},
		{raw: "src\\main.ts", expected: "src/main.ts"},
		{raw: "src/./main.ts", expected: "src/main.ts"},
		{raw: "src/sub/../main.ts", expected: "src/main.ts"},
		{raw: "..", code: CodePathOutsideScope},
		{raw: "../..", code: CodePathOutsideScope},
		{raw: "../outside.txt", code: CodePathOutsideScope},
		{raw: "..%2f..%2fetc", code: CodePathOutsideScope},
		{raw: "%2e%2e/%2e%2e", code: CodePathOutsideScope},
		{raw: "sub/../../outside", code: CodePathOutsideScope},
		{raw: "E:\\Windows", code: CodePathMustBeRelative},
		{raw: "C:/Windows", code: CodePathMustBeRelative},
		{raw: "/etc/passwd", code: CodePathMustBeRelative},
		{raw: "\\\\server\\share", code: CodePathMustBeRelative},
		{raw: "src/\x00bad", code: CodePathInvalid},
	}
	for _, tc := range cases {
		got, err := NormalizeRelPath(tc.raw)
		if tc.code == "" {
			require.Nil(t, err, tc.raw)
			require.Equal(t, tc.expected, got, tc.raw)
			continue
		}
		require.NotNil(t, err, tc.raw)
		require.Equal(t, tc.code, err.Code, tc.raw)
	}
}

// 双重编码不构成逃逸：解码后只是普通文件名字符，join 后仍在根内。
func TestNormalizeRelPathDoubleEncodingStaysInside(t *testing.T) {
	got, err := NormalizeRelPath("%252e%252e%252fsecret")
	require.Nil(t, err)
	require.Equal(t, "%2e%2e%2fsecret", got)
	require.NotContains(t, got, "..")
}

func TestResolvePathRejectsEscapes(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("s"), 0o644))
	link := filepath.Join(root, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unavailable in this environment: %v", err)
	}
	insideLink := filepath.Join(root, "inner-link")
	require.NoError(t, os.MkdirAll(filepath.Join(root, "inner"), 0o755))
	require.NoError(t, os.Symlink(filepath.Join(root, "inner"), insideLink))

	resolver := NewResolver(stubRoots{workspaces: map[string]string{"wd": root}}, "")
	cases := []string{
		"../outside.txt",
		"..%2f..%2fetc",
		"link/secret.txt",   // 已存在目标经符号链接逃逸
		"link/new-file.txt", // 待创建文件的父目录是逃逸符号链接
	}
	for _, rel := range cases {
		target, err := resolver.ResolvePath(context.Background(), "workspace:wd", rel)
		require.Nil(t, target, rel)
		require.NotNil(t, err, rel)
		require.Equal(t, CodePathOutsideScope, err.Code, rel)
	}

	target, err := resolver.ResolvePath(context.Background(), "workspace:wd", "inner-link/new-file.txt")
	require.Nil(t, err)
	require.True(t, strings.HasPrefix(target.Abs, root))
}

func TestResolvePathAcceptsInsidePaths(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "src", "api"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "src", "main.ts"), []byte("x"), 0o644))

	resolver := NewResolver(stubRoots{workspaces: map[string]string{"wd": root}}, "")
	target, err := resolver.ResolvePath(context.Background(), "workspace:wd", "src/main.ts")
	require.Nil(t, err)
	require.Equal(t, "src/main.ts", target.Rel)
	require.True(t, target.Exists)
	require.Equal(t, filepath.Join(root, "src", "main.ts"), target.Abs)

	rootTarget, err := resolver.ResolvePath(context.Background(), "workspace:wd", "")
	require.Nil(t, err)
	require.Equal(t, "", rootTarget.Rel)
	require.Equal(t, filepath.Clean(root), filepath.Clean(rootTarget.Abs))

	missing, err := resolver.ResolvePath(context.Background(), "workspace:wd", "src/missing.txt")
	require.Nil(t, err)
	require.False(t, missing.Exists)
}

func TestResolveScopeErrorMapping(t *testing.T) {
	root := t.TempDir()
	resolver := NewResolver(stubRoots{
		workspaces: map[string]string{"wd": root},
		sessions:   map[string]string{"sess": root},
	}, "")

	_, err := resolver.Resolve(context.Background(), "workspace:missing")
	require.NotNil(t, err)
	require.Equal(t, CodeScopeNotFound, err.Code)
	require.Equal(t, 404, err.HTTPStatus)

	_, err = resolver.Resolve(context.Background(), "session:missing")
	require.NotNil(t, err)
	require.Equal(t, CodeScopeHasNoRoot, err.Code)
	require.Equal(t, 400, err.HTTPStatus)

	root2, err := resolver.Resolve(context.Background(), "session:sess")
	require.Nil(t, err)
	require.Equal(t, "sess", root2.Session)

	_, err = resolver.Resolve(context.Background(), "cwd")
	require.Nil(t, err)

	noResolver := NewResolver(stubRoots{}, "")
	cwdRoot, err := noResolver.Resolve(context.Background(), "cwd")
	require.Nil(t, err)
	require.True(t, filepath.IsAbs(cwdRoot.Path))
}

// 会话 id 通过 ctx 传递（/fs/roots 附加会话根），不改变注入接口签名。
func TestSessionIDContext(t *testing.T) {
	ctx := WithSessionID(context.Background(), " sess_1 ")
	require.Equal(t, "sess_1", SessionIDFromContext(ctx))
	require.Equal(t, "", SessionIDFromContext(context.Background()))
}

func TestErrorHelpers(t *testing.T) {
	err := NewError(CodePathOutsideScope, 400, "bad path")
	require.Contains(t, err.Error(), "bad path")
	require.Equal(t, CodePathOutsideScope, CodeOf(err))
	require.Equal(t, 400, StatusOf(err, 500))
	require.Nil(t, DetailsOf(err))

	detailed := err.WithDetail("expected_offset", int64(42))
	require.Equal(t, int64(42), detailed.Details["expected_offset"])
	require.Nil(t, err.Details["expected_offset"], "WithDetail 不应修改原错误")

	require.Equal(t, 500, StatusOf(context.Canceled, 500))
	require.Equal(t, "", CodeOf(nil))

	wrapped := NewErrorf(CodePathOutsideScope, 400, "escapes %s", "root")
	require.True(t, IsCode(wrapped, CodePathOutsideScope))
}

func TestPathWithinPrefixBoundary(t *testing.T) {
	require.True(t, pathWithin("/tmp/root", "/tmp/root"))
	require.True(t, pathWithin("/tmp/root", "/tmp/root/a/b"))
	require.False(t, pathWithin("/tmp/root", "/tmp/root2/a"))
	require.False(t, pathWithin("/tmp/root", "/tmp"))
}

// Windows 大小写不敏感比较（非 Windows 跳过），对应 §5.10「大小写变体」负例。
func TestPathWithinWindowsCaseInsensitive(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-only case-insensitive comparison")
	}
	require.True(t, pathWithin(`C:\Temp\Root`, `c:\temp\root\file.txt`))
	require.False(t, pathWithin(`C:\Temp\Root`, `c:\temp\root2\file.txt`))
}
