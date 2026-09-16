package skills

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/filebrowse"
)

// fsTestSend 发送带 body / 自定义头的请求（chunk 是原始字节，不能走 fsTestDo）。
func fsTestSend(t *testing.T, router *mux.Router, method, target string, body []byte, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, target, bytes.NewReader(body))
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}

func fsTestInitUpload(t *testing.T, router *mux.Router, payload map[string]interface{}) *httptest.ResponseRecorder {
	t.Helper()
	encoded, err := json.Marshal(payload)
	require.NoError(t, err)
	return fsTestSend(t, router, http.MethodPost, "/fs/upload/init", encoded, map[string]string{"Content-Type": "application/json"})
}

func fsTestDecode(t *testing.T, recorder *httptest.ResponseRecorder, target interface{}) {
	t.Helper()
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), target), "响应体不是合法 JSON: %s", recorder.Body.String())
}

func fsTestSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestFSUploadChunkedFlowCreatesFile(t *testing.T) {
	router, root := fsTestSetup(t)
	fsTestWriteDir(t, filepath.Join(root, "docs"))

	initRecorder := fsTestInitUpload(t, router, map[string]interface{}{
		"scope": "workspace:wd_test", "dir": "docs", "name": "a.txt", "size": 11,
	})
	require.Equal(t, http.StatusOK, initRecorder.Code)
	var init filebrowse.UploadInitResult
	fsTestDecode(t, initRecorder, &init)
	require.NotEmpty(t, init.UploadID)
	require.EqualValues(t, 0, init.Offset)
	require.Equal(t, "docs/a.txt", init.Target.Path)
	require.False(t, init.Target.Exists)
	require.EqualValues(t, filebrowse.DefaultLimits().ChunkSizeDefault, init.ChunkSize)
	require.False(t, init.Completed)

	partPath := filepath.Join(root, "docs", ".aicli-uploads", init.UploadID+".part")
	require.FileExists(t, partPath, "临时文件必须落在目标目录下的 .aicli-uploads（同卷 rename）")

	chunkURL := "/fs/upload/" + init.UploadID + "/chunk"
	recorder := fsTestSend(t, router, http.MethodPut, chunkURL, []byte("hello"), map[string]string{"Content-Range": "bytes 0-4/11"})
	require.Equal(t, http.StatusOK, recorder.Code)
	var chunk filebrowse.UploadChunkResult
	fsTestDecode(t, recorder, &chunk)
	require.EqualValues(t, 5, chunk.Offset)

	statusRecorder := fsTestDo(t, router, http.MethodGet, "/fs/upload/"+init.UploadID, nil)
	require.Equal(t, http.StatusOK, statusRecorder.Code)
	var status filebrowse.UploadStatusResult
	fsTestDecode(t, statusRecorder, &status)
	require.EqualValues(t, 5, status.Offset)
	require.EqualValues(t, 11, status.Size)

	// 第二片用 X-Upload-Offset 声明起点（线序来源优先级低于 Content-Range）。
	recorder = fsTestSend(t, router, http.MethodPut, chunkURL, []byte(" world"), map[string]string{"X-Upload-Offset": "5"})
	require.Equal(t, http.StatusOK, recorder.Code)

	completeBody, err := json.Marshal(map[string]interface{}{"sha256": fsTestSHA256([]byte("hello world"))})
	require.NoError(t, err)
	recorder = fsTestSend(t, router, http.MethodPost, "/fs/upload/"+init.UploadID+"/complete", completeBody, nil)
	require.Equal(t, http.StatusOK, recorder.Code)
	var complete struct {
		File   filebrowse.UploadCompleteResult `json:"file"`
		Path   string                          `json:"path"`
		Action string                          `json:"action"`
	}
	fsTestDecode(t, recorder, &complete)
	require.Equal(t, filebrowse.UploadActionCreate, complete.Action)
	require.Equal(t, "docs/a.txt", complete.Path)
	require.Equal(t, "docs/a.txt", complete.File.Path)

	content, readErr := os.ReadFile(filepath.Join(root, "docs", "a.txt"))
	require.NoError(t, readErr)
	require.Equal(t, "hello world", string(content))
	require.NoFileExists(t, partPath, "complete 之后临时分片必须被清理")
}

func TestFSUploadChunkOutOfOrderReturns409(t *testing.T) {
	router, root := fsTestSetup(t)
	fsTestWriteDir(t, filepath.Join(root, "docs"))

	initRecorder := fsTestInitUpload(t, router, map[string]interface{}{
		"scope": "workspace:wd_test", "dir": "docs", "name": "b.txt", "size": 10,
	})
	require.Equal(t, http.StatusOK, initRecorder.Code)
	var init filebrowse.UploadInitResult
	fsTestDecode(t, initRecorder, &init)

	// 乱序：先送第二片（offset 5），期望 409 + expected_offset=0。
	recorder := fsTestSend(t, router, http.MethodPut, "/fs/upload/"+init.UploadID+"/chunk",
		[]byte("fghij"), map[string]string{"Content-Range": "bytes 5-9/10"})
	require.Equal(t, http.StatusConflict, recorder.Code)
	require.Equal(t, filebrowse.CodeUploadOffsetMismat, fsTestErrorCode(t, recorder))
	var payload map[string]interface{}
	fsTestDecode(t, recorder, &payload)
	require.EqualValues(t, 0, payload["expected_offset"], "409 必须平铺 expected_offset 供前端续传")

	// 声明总大小与会话不符 → 400。
	recorder = fsTestSend(t, router, http.MethodPut, "/fs/upload/"+init.UploadID+"/chunk",
		[]byte("hello"), map[string]string{"Content-Range": "bytes 0-4/9"})
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Equal(t, filebrowse.CodeUploadOffsetBadSize, fsTestErrorCode(t, recorder))

	// 声明长度与 body 不符 → 400（接口层校验）。
	recorder = fsTestSend(t, router, http.MethodPut, "/fs/upload/"+init.UploadID+"/chunk",
		[]byte("hello"), map[string]string{"Content-Range": "bytes 0-9/10"})
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Equal(t, filebrowse.CodeUploadChunkInvalid, fsTestErrorCode(t, recorder))

	// 空 body → 400。
	recorder = fsTestSend(t, router, http.MethodPut, "/fs/upload/"+init.UploadID+"/chunk", nil,
		map[string]string{"X-Upload-Offset": "0"})
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Equal(t, filebrowse.CodeUploadChunkInvalid, fsTestErrorCode(t, recorder))
}

func TestFSUploadChecksumMismatchKeepsPartFile(t *testing.T) {
	router, root := fsTestSetup(t)
	fsTestWriteDir(t, filepath.Join(root, "docs"))

	initRecorder := fsTestInitUpload(t, router, map[string]interface{}{
		"scope": "workspace:wd_test", "dir": "docs", "name": "c.txt", "size": 5,
		"sha256": fsTestSHA256([]byte("wrong")),
	})
	require.Equal(t, http.StatusOK, initRecorder.Code)
	var init filebrowse.UploadInitResult
	fsTestDecode(t, initRecorder, &init)

	recorder := fsTestSend(t, router, http.MethodPut, "/fs/upload/"+init.UploadID+"/chunk",
		[]byte("right"), map[string]string{"X-Upload-Offset": "0"})
	require.Equal(t, http.StatusOK, recorder.Code)

	recorder = fsTestSend(t, router, http.MethodPost, "/fs/upload/"+init.UploadID+"/complete", nil, nil)
	require.Equal(t, http.StatusConflict, recorder.Code)
	require.Equal(t, filebrowse.CodeUploadChecksum, fsTestErrorCode(t, recorder))
	require.NoFileExists(t, filepath.Join(root, "docs", "c.txt"), "校验失败不得落盘")

	// 未完成的上传（offset < size）→ 400 upload_incomplete。
	initRecorder = fsTestInitUpload(t, router, map[string]interface{}{
		"scope": "workspace:wd_test", "dir": "docs", "name": "d.txt", "size": 10,
	})
	fsTestDecode(t, initRecorder, &init)
	recorder = fsTestSend(t, router, http.MethodPost, "/fs/upload/"+init.UploadID+"/complete", nil, nil)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Equal(t, filebrowse.CodeUploadIncomplete, fsTestErrorCode(t, recorder))
}

func TestFSUploadConflictPolicies(t *testing.T) {
	router, root := fsTestSetup(t)
	fsTestWriteDir(t, filepath.Join(root, "docs"))
	fsTestWriteFile(t, filepath.Join(root, "docs", "dup.txt"), []byte("old"))

	require.Equal(t, http.StatusConflict, fsTestInitUpload(t, router, map[string]interface{}{
		"scope": "workspace:wd_test", "dir": "docs", "name": "dup.txt", "size": 5,
	}).Code)
	recorder := fsTestInitUpload(t, router, map[string]interface{}{
		"scope": "workspace:wd_test", "dir": "docs", "name": "dup.txt", "size": 5,
	})
	require.Equal(t, filebrowse.CodeTargetExists, fsTestErrorCode(t, recorder))

	policyCases := []struct {
		policy     string
		wantAction string
		wantPath   string
		content    string
	}{
		{policy: filebrowse.ConflictPolicyOverwrite, wantAction: filebrowse.UploadActionOverwrite, wantPath: "docs/dup.txt", content: "brand"},
		{policy: filebrowse.ConflictPolicyRename, wantAction: filebrowse.UploadActionRename, wantPath: "docs/dup (1).txt", content: "other"},
	}
	for _, testCase := range policyCases {
		t.Run(testCase.policy, func(t *testing.T) {
			initRecorder := fsTestInitUpload(t, router, map[string]interface{}{
				"scope": "workspace:wd_test", "dir": "docs", "name": "dup.txt",
				"size": len(testCase.content), "conflict_policy": testCase.policy,
			})
			require.Equal(t, http.StatusOK, initRecorder.Code)
			var init filebrowse.UploadInitResult
			fsTestDecode(t, initRecorder, &init)
			require.Equal(t, testCase.wantPath, init.Target.Path)

			recorder := fsTestSend(t, router, http.MethodPut, "/fs/upload/"+init.UploadID+"/chunk",
				[]byte(testCase.content), map[string]string{"X-Upload-Offset": "0"})
			require.Equal(t, http.StatusOK, recorder.Code)

			recorder = fsTestSend(t, router, http.MethodPost, "/fs/upload/"+init.UploadID+"/complete", nil, nil)
			require.Equal(t, http.StatusOK, recorder.Code)
			var complete struct {
				Path   string `json:"path"`
				Action string `json:"action"`
			}
			fsTestDecode(t, recorder, &complete)
			require.Equal(t, testCase.wantAction, complete.Action)
			require.Equal(t, testCase.wantPath, complete.Path)

			content, readErr := os.ReadFile(filepath.Join(root, filepath.FromSlash(testCase.wantPath)))
			require.NoError(t, readErr)
			require.Equal(t, testCase.content, string(content))
		})
	}
}

func TestFSUploadAbortCleansTempFiles(t *testing.T) {
	router, root := fsTestSetup(t)
	fsTestWriteDir(t, filepath.Join(root, "docs"))

	initRecorder := fsTestInitUpload(t, router, map[string]interface{}{
		"scope": "workspace:wd_test", "dir": "docs", "name": "e.txt", "size": 10,
	})
	require.Equal(t, http.StatusOK, initRecorder.Code)
	var init filebrowse.UploadInitResult
	fsTestDecode(t, initRecorder, &init)

	recorder := fsTestSend(t, router, http.MethodPut, "/fs/upload/"+init.UploadID+"/chunk",
		[]byte("abc"), map[string]string{"X-Upload-Offset": "0"})
	require.Equal(t, http.StatusOK, recorder.Code)

	recorder = fsTestDo(t, router, http.MethodDelete, "/fs/upload/"+init.UploadID, nil)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.NoFileExists(t, filepath.Join(root, "docs", ".aicli-uploads", init.UploadID+".part"))
	require.NoFileExists(t, filepath.Join(root, "docs", ".aicli-uploads", init.UploadID+".json"))

	// 会话消失后按过期处理：410，前端重新 init。
	recorder = fsTestDo(t, router, http.MethodGet, "/fs/upload/"+init.UploadID, nil)
	require.Equal(t, http.StatusGone, recorder.Code)
	require.Equal(t, filebrowse.CodeUploadExpired, fsTestErrorCode(t, recorder))

	recorder = fsTestSend(t, router, http.MethodPut, "/fs/upload/"+init.UploadID+"/chunk",
		[]byte("abc"), map[string]string{"X-Upload-Offset": "0"})
	require.Equal(t, http.StatusGone, recorder.Code)
	require.Equal(t, filebrowse.CodeUploadExpired, fsTestErrorCode(t, recorder))
}

func TestFSUploadInitValidation(t *testing.T) {
	router, root := fsTestSetup(t)
	fsTestWriteDir(t, filepath.Join(root, "docs"))
	fsTestWriteDir(t, filepath.Join(root, ".aicli-uploads")) // 目的：让「目标落在内部目录」这条走到策略校验而不是 404

	cases := []struct {
		name    string
		payload map[string]interface{}
		status  int
		code    string
	}{
		{"chunk-size", map[string]interface{}{"scope": "workspace:wd_test", "dir": "docs", "name": "x.txt", "size": 4, "chunk_size": 3}, http.StatusBadRequest, filebrowse.CodeChunkSizeInvalid},
		{"name", map[string]interface{}{"scope": "workspace:wd_test", "dir": "docs", "name": "../evil.txt", "size": 4}, http.StatusBadRequest, filebrowse.CodeUploadNameInvalid},
		{"policy", map[string]interface{}{"scope": "workspace:wd_test", "dir": "docs", "name": "x.txt", "size": 4, "conflict_policy": "bogus"}, http.StatusBadRequest, filebrowse.CodeConflictPolicyInvalid},
		{"scope", map[string]interface{}{"scope": "workspace:wd_unknown", "dir": "docs", "name": "x.txt", "size": 4}, http.StatusNotFound, "scope_not_found"},
		{"dir-missing", map[string]interface{}{"scope": "workspace:wd_test", "dir": "nope", "name": "x.txt", "size": 4}, http.StatusNotFound, "path_not_found"},
		// 不允许把目标目录选在内部临时目录里（否则清理逻辑会互相踩）。
		{"internal-dir", map[string]interface{}{"scope": "workspace:wd_test", "dir": ".aicli-uploads", "name": "x.txt", "size": 4}, http.StatusBadRequest, filebrowse.CodeUploadTargetInvalid},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			recorder := fsTestInitUpload(t, router, testCase.payload)
			require.Equal(t, testCase.status, recorder.Code)
			require.Equal(t, testCase.code, fsTestErrorCode(t, recorder))
		})
	}

	// size 超过服务端上限：换一个上限极小的服务实例。
	limited := filebrowse.NewService(filebrowse.Deps{
		Roots:  &fsTestRoots{root: root},
		Cwd:    root,
		Limits: filebrowse.Limits{MaxUploadBytes: 8},
	})
	limitedRouter := mux.NewRouter()
	RegisterFSBrowserRoutes(limitedRouter, limited)
	recorder := fsTestInitUpload(t, limitedRouter, map[string]interface{}{
		"scope": "workspace:wd_test", "dir": "docs", "name": "big.bin", "size": 20,
	})
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Equal(t, filebrowse.CodeUploadTooLarge, fsTestErrorCode(t, recorder))
}

func TestFSUploadSessionExpiresWithTTL(t *testing.T) {
	root := t.TempDir()
	fsTestWriteDir(t, filepath.Join(root, "docs"))
	service := filebrowse.NewService(filebrowse.Deps{
		Roots:  &fsTestRoots{root: root},
		Cwd:    root,
		Limits: filebrowse.Limits{UploadTTL: time.Millisecond},
	})
	router := mux.NewRouter()
	RegisterFSBrowserRoutes(router, service)

	initRecorder := fsTestInitUpload(t, router, map[string]interface{}{
		"scope": "workspace:wd_test", "dir": "docs", "name": "ttl.txt", "size": 4,
	})
	require.Equal(t, http.StatusOK, initRecorder.Code)
	var init filebrowse.UploadInitResult
	fsTestDecode(t, initRecorder, &init)

	time.Sleep(10 * time.Millisecond)
	recorder := fsTestSend(t, router, http.MethodPut, "/fs/upload/"+init.UploadID+"/chunk",
		[]byte("ab"), map[string]string{"X-Upload-Offset": "0"})
	require.Equal(t, http.StatusGone, recorder.Code)
	require.Equal(t, filebrowse.CodeUploadExpired, fsTestErrorCode(t, recorder))
	require.NotContains(t, strings.ToLower(recorder.Body.String()), "service_unavailable")
}

func fsTestWriteDir(t *testing.T, path string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(path, 0o755))
}
