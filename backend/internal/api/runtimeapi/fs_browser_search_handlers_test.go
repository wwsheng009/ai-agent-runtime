package runtimeapi

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/filebrowse"
	"github.com/wwsheng009/ai-agent-runtime/internal/fsscope"
)

// TestFSSearchHappyPathAndClamps 覆盖 /fs/search 正常路径、参数解码与超限夹紧
// （limit>50 回显夹紧值；budget_ms/max_depth/max_scan 超限不报错，细则见 filebrowse/search_test.go）。
func TestFSSearchHappyPathAndClamps(t *testing.T) {
	root := t.TempDir()
	fsTestWriteFile(t, filepath.Join(root, "frontend", "src", "lib", "composer-menu.ts"), []byte("x"))
	fsTestWriteFile(t, filepath.Join(root, "backend", "main.go"), []byte("x"))
	router := mux.NewRouter()
	RegisterFSBrowserRoutes(router, fsTestService(t, root))

	recorder := fsTestDo(t, router, http.MethodGet, "/fs/search?scope=workspace:wd_test&q=compmenu", nil)
	require.Equal(t, http.StatusOK, recorder.Code)
	var result filebrowse.SearchResult
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &result))
	require.Equal(t, "workspace:wd_test", result.Scope)
	require.Equal(t, "compmenu", result.Query)
	require.Empty(t, result.Base)
	require.Equal(t, filebrowse.SearchLimitDefault, result.Limit)
	require.False(t, result.HasMore)
	require.False(t, result.Truncated)
	require.NotEmpty(t, result.Items)
	require.Equal(t, "frontend/src/lib/composer-menu.ts", result.Items[0].Path)
	require.Equal(t, "file", result.Items[0].Type)
	require.NotNil(t, result.Items[0].Match, "非空 q 必须带高亮位置")
	require.Greater(t, result.Items[0].Score, 0)
	require.GreaterOrEqual(t, result.Scanned, 1)

	// limit>50 → 服务端夹紧到 50，并在响应里回显实际值。
	recorder = fsTestDo(t, router, http.MethodGet, "/fs/search?scope=workspace:wd_test&q=compmenu&limit=500", nil)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &result))
	require.Equal(t, filebrowse.SearchLimitMax, result.Limit)

	// budget_ms>1000 / max_depth>16 / max_scan>50000 超限不报错（服务端夹紧）。
	oversized := "/fs/search?scope=workspace:wd_test&q=compmenu&limit=999&budget_ms=99999&max_depth=999&max_scan=999999"
	recorder = fsTestDo(t, router, http.MethodGet, oversized, nil)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &result))
	require.Equal(t, filebrowse.SearchLimitMax, result.Limit)
	require.Len(t, result.Items, 1)
}

// TestFSSearchKindsAndHiddenParams：kinds 与 show_hidden 参数确实被解码并生效。
func TestFSSearchKindsAndHiddenParams(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "frontend"), 0o755))
	fsTestWriteFile(t, filepath.Join(root, ".env.local"), []byte("x"))
	router := mux.NewRouter()
	RegisterFSBrowserRoutes(router, fsTestService(t, root))

	recorder := fsTestDo(t, router, http.MethodGet, "/fs/search?scope=workspace:wd_test&q=frontend", nil)
	require.Equal(t, http.StatusOK, recorder.Code)
	var result filebrowse.SearchResult
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &result))
	require.Empty(t, result.Items, "默认 kinds=file 不返回目录")

	recorder = fsTestDo(t, router, http.MethodGet, "/fs/search?scope=workspace:wd_test&q=frontend&kinds=both", nil)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &result))
	require.Len(t, result.Items, 1)
	require.Equal(t, "frontend", result.Items[0].Path)
	require.Equal(t, "dir", result.Items[0].Type)

	recorder = fsTestDo(t, router, http.MethodGet, "/fs/search?scope=workspace:wd_test&q=env", nil)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &result))
	require.Empty(t, result.Items, "show_hidden 缺省不返回点文件")

	recorder = fsTestDo(t, router, http.MethodGet, "/fs/search?scope=workspace:wd_test&q=env&show_hidden=true", nil)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &result))
	require.Len(t, result.Items, 1)
	require.Equal(t, ".env.local", result.Items[0].Path)
}

// TestFSSearchItemsNeverNull：无命中时 items 必须是空数组（前端归一化纪律）。
func TestFSSearchItemsNeverNull(t *testing.T) {
	root := t.TempDir()
	fsTestWriteFile(t, filepath.Join(root, "a.txt"), []byte("x"))
	router := mux.NewRouter()
	RegisterFSBrowserRoutes(router, fsTestService(t, root))

	recorder := fsTestDo(t, router, http.MethodGet, "/fs/search?scope=workspace:wd_test&q=zzz-nomatch", nil)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Contains(t, recorder.Body.String(), `"items":[]`)
	require.NotContains(t, recorder.Body.String(), `"items":null`)
}

// TestFSSearchErrorResponsesUseUnifiedBody：错误体与 /fs/list 同格式
// （{error:{code,message},request_id}），错误码复用 fsscope / cursor_invalid / query_too_long。
func TestFSSearchErrorResponsesUseUnifiedBody(t *testing.T) {
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
		{"cursor invalid", "/fs/search?scope=workspace:wd_test&cursor=%21%21%21", filebrowse.CodeCursorInvalid, http.StatusBadRequest},
		{"query too long", "/fs/search?scope=workspace:wd_test&q=" + strings.Repeat("a", filebrowse.SearchQueryMaxRunes+1), filebrowse.CodeQueryTooLong, http.StatusBadRequest},
		{"path traversal", "/fs/search?scope=workspace:wd_test&path=../..", fsscope.CodePathOutsideScope, http.StatusBadRequest},
		{"absolute path", "/fs/search?scope=workspace:wd_test&path=%2Fetc", fsscope.CodePathMustBeRelative, http.StatusBadRequest},
		{"scope missing", "/fs/search", fsscope.CodeScopeInvalid, http.StatusBadRequest},
		{"scope not registered", "/fs/search?scope=workspace:nope", fsscope.CodeScopeNotFound, http.StatusNotFound},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			recorder := fsTestDo(t, router, http.MethodGet, testCase.target, nil)
			require.Equal(t, testCase.status, recorder.Code)
			// fsTestErrorCode 同时断言 error.code / error.message / request_id 均非空。
			require.Equal(t, testCase.code, fsTestErrorCode(t, recorder))
		})
	}
}

// TestFSSearchRouteWithoutServiceReturns503：nil 注入仍注册路由并统一 503 service_unavailable。
func TestFSSearchRouteWithoutServiceReturns503(t *testing.T) {
	router := mux.NewRouter()
	RegisterFSBrowserRoutes(router, nil)

	recorder := fsTestDo(t, router, http.MethodGet, "/fs/search?scope=cwd&q=a", nil)
	require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
	require.Equal(t, fsscope.CodeServiceUnavailable, fsTestErrorCode(t, recorder))
}
