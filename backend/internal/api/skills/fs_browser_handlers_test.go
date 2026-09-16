package skills

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/filebrowse"
	"github.com/wwsheng009/ai-agent-runtime/internal/fsscope"
)

// fsTestRoots 是 /fs/* 测试用的作用域解析器（只认一个工作目录）。
type fsTestRoots struct {
	root string
}

func (f *fsTestRoots) WorkspaceRoot(_ context.Context, id string) (string, bool, error) {
	if id != "wd_test" {
		return "", false, nil
	}
	return f.root, true, nil
}

func (f *fsTestRoots) SessionRoot(_ context.Context, sessionID string) (string, bool, error) {
	if sessionID != "sess_1" {
		return "", false, nil
	}
	return f.root, true, nil
}

func (f *fsTestRoots) ListWorkspaceRoots(_ context.Context) ([]fsscope.WorkspaceRoot, error) {
	return []fsscope.WorkspaceRoot{{ID: "wd_test", Path: f.root, Name: "test-root"}}, nil
}

// fsTestSetup 按父代理的接线方式注册路由：/api/runtime 子路由上挂 /fs/*。
func fsTestSetup(t *testing.T) (*mux.Router, string) {
	t.Helper()
	root := t.TempDir()
	router := mux.NewRouter()
	RegisterFSBrowserRoutes(router, fsTestService(t, root))
	return router, root
}

func fsTestService(t *testing.T, root string) *filebrowse.Service {
	t.Helper()
	service := filebrowse.NewService(filebrowse.Deps{Roots: &fsTestRoots{root: root}, Cwd: root})
	require.NotNil(t, service)
	return service
}

func fsTestDo(t *testing.T, router *mux.Router, method, target string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, target, nil)
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}

func fsTestErrorCode(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()
	var payload struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		RequestID string `json:"request_id"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
	require.NotEmpty(t, payload.Error.Code, "错误体必须带机器码")
	require.NotEmpty(t, payload.Error.Message)
	require.NotEmpty(t, payload.RequestID, "错误体必须带 request_id")
	return payload.Error.Code
}

func fsTestWriteFile(t *testing.T, path string, content []byte) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, content, 0o644))
}

func TestFSRootsReturnsRegisteredRoots(t *testing.T) {
	router, root := fsTestSetup(t)
	fsTestWriteFile(t, filepath.Join(root, "a.txt"), []byte("x"))

	recorder := fsTestDo(t, router, http.MethodGet, "/fs/roots", nil)
	require.Equal(t, http.StatusOK, recorder.Code)
	var payload struct {
		Roots []filebrowse.Root `json:"roots"`
		Count int               `json:"count"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
	require.Equal(t, len(payload.Roots), payload.Count)
	require.NotEmpty(t, payload.Roots)
	require.Equal(t, "workspace:wd_test", payload.Roots[0].Scope)
	require.True(t, payload.Roots[0].Exists)
	// 无 git 或非仓库都要能降级：只置 false + probe_error，不是 500。
	require.False(t, payload.Roots[0].IsGitRepo)
	require.NotEmpty(t, payload.Roots[0].ProbeError)
}

// TestFSRootsBridgesSessionIDQuery 覆盖父代理接线新增的 `session_id` 桥接：
// 显式带上会话 id 才附会话根；不带时不得凭空出现（不能猜会话）。
func TestFSRootsBridgesSessionIDQuery(t *testing.T) {
	router, root := fsTestSetup(t)
	fsTestWriteFile(t, filepath.Join(root, "a.txt"), []byte("x"))

	withSession := fsTestDo(t, router, http.MethodGet, "/fs/roots?session_id=sess_1", nil)
	require.Equal(t, http.StatusOK, withSession.Code)
	var withPayload struct {
		Roots []filebrowse.Root `json:"roots"`
	}
	require.NoError(t, json.Unmarshal(withSession.Body.Bytes(), &withPayload))
	require.True(t, fsTestHasScope(withPayload.Roots, "session:sess_1"), "带上 session_id 时应返回会话根")

	withoutSession := fsTestDo(t, router, http.MethodGet, "/fs/roots", nil)
	require.Equal(t, http.StatusOK, withoutSession.Code)
	var withoutPayload struct {
		Roots []filebrowse.Root `json:"roots"`
	}
	require.NoError(t, json.Unmarshal(withoutSession.Body.Bytes(), &withoutPayload))
	require.False(t, fsTestHasScope(withoutPayload.Roots, "session:sess_1"), "未带 session_id 时不得出现会话根")
}

func fsTestHasScope(roots []filebrowse.Root, scope string) bool {
	for _, root := range roots {
		if root.Scope == scope {
			return true
		}
	}
	return false
}

func TestFSListAndStatHappyPath(t *testing.T) {
	root := t.TempDir()
	fsTestWriteFile(t, filepath.Join(root, "src", "main.go"), []byte("package main"))
	fsTestWriteFile(t, filepath.Join(root, "readme.md"), []byte("# hi"))
	router := mux.NewRouter()
	RegisterFSBrowserRoutes(router, fsTestService(t, root))

	recorder := fsTestDo(t, router, http.MethodGet, "/fs/list?scope=workspace:wd_test&path=src&limit=50", nil)
	require.Equal(t, http.StatusOK, recorder.Code)
	var listing filebrowse.ListResult
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &listing))
	require.Equal(t, "src", listing.Dir.Path)
	require.False(t, listing.Dir.IsRoot)
	require.Equal(t, 50, listing.Limit, "响应必须回显实际 limit")
	require.Equal(t, filebrowse.SortTypeThenName, listing.Sort)
	require.Len(t, listing.Entries, 1)
	require.Equal(t, "main.go", listing.Entries[0].Name)
	require.True(t, listing.Entries[0].IsText)
	require.False(t, listing.HasMore)

	recorder = fsTestDo(t, router, http.MethodGet, "/fs/stat?scope=workspace:wd_test&path=readme.md", nil)
	require.Equal(t, http.StatusOK, recorder.Code)
	var stat filebrowse.EntryStat
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &stat))
	require.Equal(t, "readme.md", stat.Path)
	require.Equal(t, "file", stat.Type)
	require.Equal(t, int64(4), stat.Size)
}

func TestFSListCursorInvalid(t *testing.T) {
	root := t.TempDir()
	fsTestWriteFile(t, filepath.Join(root, "a.txt"), []byte("x"))
	router := mux.NewRouter()
	RegisterFSBrowserRoutes(router, fsTestService(t, root))

	recorder := fsTestDo(t, router, http.MethodGet, "/fs/list?scope=workspace:wd_test&cursor=%21%21%21", nil)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Equal(t, filebrowse.CodeCursorInvalid, fsTestErrorCode(t, recorder))
}

func TestFSListPaginationNoDuplicateNoGap(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a.txt", "b.txt", "c.txt", "d.txt", "e.txt"} {
		fsTestWriteFile(t, filepath.Join(root, name), []byte("x"))
	}
	router := mux.NewRouter()
	RegisterFSBrowserRoutes(router, fsTestService(t, root))

	seen := map[string]int{}
	cursor := ""
	for page := 0; page < 10; page++ {
		target := "/fs/list?scope=workspace:wd_test&limit=2"
		if cursor != "" {
			target += "&cursor=" + cursor
		}
		recorder := fsTestDo(t, router, http.MethodGet, target, nil)
		require.Equal(t, http.StatusOK, recorder.Code)
		var listing filebrowse.ListResult
		require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &listing))
		for _, entry := range listing.Entries {
			seen[entry.Name]++
		}
		if !listing.HasMore {
			break
		}
		cursor = listing.NextCursor
		require.NotEmpty(t, cursor)
	}
	require.Len(t, seen, 5)
	for name, count := range seen {
		require.Equal(t, 1, count, "%s 被重复返回", name)
	}
}

func TestFSListPathEscapeIsRejected(t *testing.T) {
	root := t.TempDir()
	fsTestWriteFile(t, filepath.Join(root, "a.txt"), []byte("x"))
	router := mux.NewRouter()
	RegisterFSBrowserRoutes(router, fsTestService(t, root))

	cases := []struct {
		name   string
		target string
		code   string
		status int
	}{
		{"parent", "/fs/list?scope=workspace:wd_test&path=../..", fsscope.CodePathOutsideScope, http.StatusBadRequest},
		{"encoded", "/fs/list?scope=workspace:wd_test&path=..%2f..%2fetc", fsscope.CodePathOutsideScope, http.StatusBadRequest},
		{"double-encoded", "/fs/list?scope=workspace:wd_test&path=%252e%252e%252f%252e%252e", fsscope.CodePathOutsideScope, http.StatusBadRequest},
		{"absolute", "/fs/list?scope=workspace:wd_test&path=" + "C:\\Windows", fsscope.CodePathMustBeRelative, http.StatusBadRequest},
		{"absolute-unix", "/fs/list?scope=workspace:wd_test&path=%2Fetc", fsscope.CodePathMustBeRelative, http.StatusBadRequest},
		{"scope", "/fs/list?scope=..%2fetc", fsscope.CodeScopeInvalid, http.StatusBadRequest},
		{"unknown-workspace", "/fs/list?scope=workspace:wd_missing", fsscope.CodeScopeNotFound, http.StatusNotFound},
		{"missing", "/fs/list?scope=workspace:wd_test&path=nope", fsscope.CodePathNotFound, http.StatusNotFound},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			recorder := fsTestDo(t, router, http.MethodGet, testCase.target, nil)
			require.Equal(t, testCase.status, recorder.Code)
			require.Equal(t, testCase.code, fsTestErrorCode(t, recorder))
		})
	}
}

func TestFSListSymlinkEscapeIsRejected(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	fsTestWriteFile(t, filepath.Join(outside, "secret.txt"), []byte("top secret"))
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Skipf("symlink unavailable in this environment: %v", err)
	}
	router := mux.NewRouter()
	RegisterFSBrowserRoutes(router, fsTestService(t, root))
	recorder := fsTestDo(t, router, http.MethodGet, "/fs/list?scope=workspace:wd_test&path=escape", nil)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Equal(t, fsscope.CodePathOutsideScope, fsTestErrorCode(t, recorder))
}

func TestFSPreviewTextBinaryImage(t *testing.T) {
	root := t.TempDir()
	fsTestWriteFile(t, filepath.Join(root, "big.txt"), []byte(strings.Repeat("line\n", 100)))
	fsTestWriteFile(t, filepath.Join(root, "raw.bin"), []byte{0x00, 0x01})
	fsTestWriteFile(t, filepath.Join(root, "pixel.png"), []byte{0x89, 0x50})
	router := mux.NewRouter()
	RegisterFSBrowserRoutes(router, fsTestService(t, root))

	recorder := fsTestDo(t, router, http.MethodGet, "/fs/preview?scope=workspace:wd_test&path=big.txt&max_bytes=10", nil)
	require.Equal(t, http.StatusOK, recorder.Code)
	var text filebrowse.Preview
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &text))
	require.Equal(t, filebrowse.KindText, text.Kind)
	require.True(t, text.Truncated)
	require.NotNil(t, text.Text)
	require.Equal(t, "line\nline\n", *text.Text)
	require.Equal(t, int64(10), text.LimitBytes)

	recorder = fsTestDo(t, router, http.MethodGet, "/fs/preview?scope=workspace:wd_test&path=raw.bin", nil)
	var binary filebrowse.Preview
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &binary))
	require.Equal(t, filebrowse.KindBinary, binary.Kind)
	require.Equal(t, filebrowse.ReasonNulByte, binary.Reason)
	require.Nil(t, binary.Text)

	recorder = fsTestDo(t, router, http.MethodGet, "/fs/preview?scope=workspace:wd_test&path=pixel.png", nil)
	var image filebrowse.Preview
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &image))
	require.Equal(t, filebrowse.KindImage, image.Kind)
	require.Equal(t, "iVA=", image.DataBase64)
}

func TestFSDownloadRangeHeadAndFourSixteen(t *testing.T) {
	root := t.TempDir()
	fsTestWriteFile(t, filepath.Join(root, "数据.bin"), []byte("0123456789"))
	router := mux.NewRouter()
	RegisterFSBrowserRoutes(router, fsTestService(t, root))
	target := "/fs/download?scope=workspace:wd_test&path=" + "%E6%95%B0%E6%8D%AE.bin"

	recorder := fsTestDo(t, router, http.MethodGet, target, nil)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "0123456789", recorder.Body.String())
	require.Equal(t, "bytes", recorder.Header().Get("Accept-Ranges"))
	require.NotEmpty(t, recorder.Header().Get("ETag"))
	require.Contains(t, recorder.Header().Get("Content-Disposition"), "filename*=UTF-8''")
	require.Contains(t, recorder.Header().Get("Content-Disposition"), "%E6%95%B0%E6%8D%AE.bin")

	recorder = fsTestDo(t, router, http.MethodGet, target, map[string]string{"Range": "bytes=2-5"})
	require.Equal(t, http.StatusPartialContent, recorder.Code)
	require.Equal(t, "2345", recorder.Body.String())
	require.Equal(t, "bytes 2-5/10", recorder.Header().Get("Content-Range"))

	recorder = fsTestDo(t, router, http.MethodGet, target, map[string]string{"Range": "bytes=100-200"})
	require.Equal(t, http.StatusRequestedRangeNotSatisfiable, recorder.Code)
	require.Equal(t, "bytes */10", recorder.Header().Get("Content-Range"))

	// 多区间不支持：显式 416，而不是 multipart/byteranges。
	recorder = fsTestDo(t, router, http.MethodGet, target, map[string]string{"Range": "bytes=0-1,4-5"})
	require.Equal(t, http.StatusRequestedRangeNotSatisfiable, recorder.Code)
	require.Equal(t, "bytes */10", recorder.Header().Get("Content-Range"))

	recorder = fsTestDo(t, router, http.MethodHead, target, nil)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Empty(t, recorder.Body.String())
	require.Equal(t, "10", recorder.Header().Get("Content-Length"))
	require.Equal(t, "bytes", recorder.Header().Get("Accept-Ranges"))
	require.NotEmpty(t, recorder.Header().Get("ETag"))

	// 目录不可下载。
	require.NoError(t, os.MkdirAll(filepath.Join(root, "dir"), 0o755))
	recorder = fsTestDo(t, router, http.MethodGet, "/fs/download?scope=workspace:wd_test&path=dir", nil)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Equal(t, fsscope.CodePathNotFile, fsTestErrorCode(t, recorder))
}

func TestFSRoutesWithoutServiceReturn503(t *testing.T) {
	router := mux.NewRouter()
	RegisterFSBrowserRoutes(router, nil)
	targets := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/fs/roots"},
		{http.MethodGet, "/fs/list?scope=cwd"},
		{http.MethodGet, "/fs/stat?scope=cwd&path=a"},
		{http.MethodGet, "/fs/preview?scope=cwd&path=a"},
		{http.MethodGet, "/fs/download?scope=cwd&path=a"},
		{http.MethodHead, "/fs/download?scope=cwd&path=a"},
		{http.MethodPost, "/fs/upload/init"},
		{http.MethodPut, "/fs/upload/up_1/chunk"},
		{http.MethodGet, "/fs/upload/up_1"},
		{http.MethodPost, "/fs/upload/up_1/complete"},
		{http.MethodDelete, "/fs/upload/up_1"},
	}
	for _, testCase := range targets {
		recorder := fsTestDo(t, router, testCase.method, testCase.path, nil)
		require.Equal(t, http.StatusServiceUnavailable, recorder.Code, "%s %s", testCase.method, testCase.path)
		require.Equal(t, fsscope.CodeServiceUnavailable, fsTestErrorCode(t, recorder))
	}
}

func TestFSContentDispositionEncodesCRC5987(t *testing.T) {
	require.Equal(t, `attachment; filename="plain.txt"; filename*=UTF-8''plain.txt`, fsContentDisposition("plain.txt"))
	encoded := fsContentDisposition("大 文件.txt")
	require.Contains(t, encoded, "filename*=UTF-8''")
	require.Contains(t, encoded, "%E5%A4%A7%20%E6%96%87%E4%BB%B6.txt")
	require.NotContains(t, encoded, `filename="`)
}
