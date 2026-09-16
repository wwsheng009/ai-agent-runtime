package skills

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/gitbrowse"
)

// fakeGitService 记录收到的请求并按配置返回结果/错误，让 handler 测试不必拉起真实 git。
type fakeGitService struct {
	statusResult  *gitbrowse.StatusResult
	statusErr     error
	diffResult    *gitbrowse.DiffResult
	diffErr       error
	commitsResult *gitbrowse.CommitsResult
	commitsErr    error
	stageResult   *gitbrowse.StageResult
	stageErr      error

	gotRepo    gitbrowse.RepoRequest
	gotDiff    gitbrowse.DiffRequest
	gotCommits gitbrowse.CommitsRequest
	gotStage   gitbrowse.StageRequest
}

func (f *fakeGitService) Status(_ context.Context, req gitbrowse.RepoRequest) (*gitbrowse.StatusResult, error) {
	f.gotRepo = req
	return f.statusResult, f.statusErr
}

func (f *fakeGitService) Diff(_ context.Context, req gitbrowse.DiffRequest) (*gitbrowse.DiffResult, error) {
	f.gotDiff = req
	return f.diffResult, f.diffErr
}

func (f *fakeGitService) Commits(_ context.Context, req gitbrowse.CommitsRequest) (*gitbrowse.CommitsResult, error) {
	f.gotCommits = req
	return f.commitsResult, f.commitsErr
}

func (f *fakeGitService) Stage(_ context.Context, req gitbrowse.StageRequest) (*gitbrowse.StageResult, error) {
	f.gotStage = req
	return f.stageResult, f.stageErr
}

// newGitTestRouter 用与生产一致的方式注册路由（router 已是 /api/runtime 子路由）。
func newGitTestRouter(service GitBrowseService) *mux.Router {
	parent := mux.NewRouter()
	sub := parent.PathPrefix("/api/runtime").Subrouter()
	RegisterGitBrowseRoutes(sub, service)
	return parent
}

// doGitRequest 发起一次请求并返回响应。
func doGitRequest(router *mux.Router, method, target string, body string, headers map[string]string) *httptest.ResponseRecorder {
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, reader)
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

// newGitRootRouter 直接在根 router 上注册（验证方法路由用；
// mux 的子路由遇到方法不匹配会归并为 404，掩盖 405 语义）。
func newGitRootRouter(service GitBrowseService) *mux.Router {
	router := mux.NewRouter()
	RegisterGitBrowseRoutes(router, service)
	return router
}

// decodeGitError 解析统一错误体（§5.7），返回错误对象与 request_id。
func decodeGitError(t *testing.T, rec *httptest.ResponseRecorder) (map[string]interface{}, string) {
	t.Helper()
	var payload struct {
		Error     map[string]interface{} `json:"error"`
		RequestID string                 `json:"request_id"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload), "body=%s", rec.Body.String())
	require.NotNil(t, payload.Error)
	return payload.Error, payload.RequestID
}

// 未注入服务时必须统一 503 git_unavailable，而不是 404/panic。
func TestGitRoutesNilServiceReturns503(t *testing.T) {
	router := newGitTestRouter(nil)
	cases := []struct{ method, target string }{
		{http.MethodGet, "/api/runtime/git/status"},
		{http.MethodGet, "/api/runtime/git/diff?file=a.txt"},
		{http.MethodGet, "/api/runtime/git/commits"},
		{http.MethodPost, "/api/runtime/git/stage"},
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.target, func(t *testing.T) {
			rec := doGitRequest(router, tc.method, tc.target, "", nil)
			require.Equal(t, http.StatusServiceUnavailable, rec.Code)
			payload, requestID := decodeGitError(t, rec)
			require.Equal(t, gitbrowse.CodeGitUnavailable, payload["code"])
			require.Contains(t, payload["message"], "not configured")
			require.Equal(t, "", requestID)
		})
	}
}

func TestRegisterGitBrowseRoutesNilRouterIsSafe(t *testing.T) {
	require.NotPanics(t, func() { RegisterGitBrowseRoutes(nil, &fakeGitService{}) })
}

func TestGitStatusHandlerPassesQueryAndWritesJSON(t *testing.T) {
	service := &fakeGitService{statusResult: &gitbrowse.StatusResult{
		Repo:  gitbrowse.RepoStatus{Root: "/repo", Branch: "main", Head: "abcdef1"},
		Clean: true,
	}}
	router := newGitTestRouter(service)

	rec := doGitRequest(router, http.MethodGet,
		"/api/runtime/git/status?scope=workspace%3Awd&path=pkg", "", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Header().Get("Content-Type"), "application/json")
	require.Equal(t, gitbrowse.RepoRequest{Scope: "workspace:wd", Path: "pkg"}, service.gotRepo)
	require.JSONEq(t, `{"repo":{"root":"/repo","branch":"main","detached":false,"head":"abcdef1",
		"head_full":"","upstream":"","ahead":0,"behind":0,"is_bare":false},
		"clean":true,"generated_at":0,"staged":null,"unstaged":null,"untracked":null,
		"conflicts":null,"renames":null,"warnings":null}`, rec.Body.String())
}

// 二次编码（%252f）必须在进服务前被解开，否则会绕过作用域校验（§5.10）。
func TestGitStatusHandlerDecodesDoubleEncodedPath(t *testing.T) {
	service := &fakeGitService{statusResult: &gitbrowse.StatusResult{}}
	router := newGitTestRouter(service)

	rec := doGitRequest(router, http.MethodGet,
		"/api/runtime/git/status?scope=workspace%253Awd&path=..%252f..%252fetc", "", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "workspace:wd", service.gotRepo.Scope)
	require.Equal(t, "../../etc", service.gotRepo.Path)
}

func TestGitDiffHandlerPassesParams(t *testing.T) {
	service := &fakeGitService{diffResult: &gitbrowse.DiffResult{Target: "staged"}}
	router := newGitTestRouter(service)

	rec := doGitRequest(router, http.MethodGet,
		"/api/runtime/git/diff?scope=cwd&file=src%2Fa.ts&target=staged&context=7&whitespace=ignore_all", "", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, gitbrowse.DiffRequest{
		RepoRequest: gitbrowse.RepoRequest{Scope: "cwd"},
		File:        "src/a.ts",
		Target:      "staged",
		Context:     7,
		Whitespace:  "ignore_all",
	}, service.gotDiff)

	// 回退信息是前端契约的一部分：响应体必须带 effective_target / target_fallback，
	// 否则前端无法说明「当前显示的是哪一侧的改动」。
	var diffBody map[string]any
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&diffBody))
	require.Contains(t, diffBody, "effective_target")
	require.Contains(t, diffBody, "target_fallback")

	// 非法 context 归 0，由服务层套用默认值（默认 3）。
	rec = doGitRequest(router, http.MethodGet, "/api/runtime/git/diff?file=a.txt&context=abc", "", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, 0, service.gotDiff.Context)
	require.Equal(t, "a.txt", service.gotDiff.File)
}

func TestGitCommitsHandlerPassesParams(t *testing.T) {
	sha := strings.Repeat("a", 40)
	service := &fakeGitService{commitsResult: &gitbrowse.CommitsResult{Commits: []gitbrowse.Commit{}}}
	router := newGitTestRouter(service)

	rec := doGitRequest(router, http.MethodGet,
		"/api/runtime/git/commits?scope=session%3As1&limit=25&cursor="+sha, "", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, gitbrowse.CommitsRequest{
		RepoRequest: gitbrowse.RepoRequest{Scope: "session:s1"},
		Limit:       25,
		Cursor:      sha,
	}, service.gotCommits)

	rec = doGitRequest(router, http.MethodGet, "/api/runtime/git/commits?limit=not-a-number", "", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, 0, service.gotCommits.Limit)
}

func TestGitStageHandlerParsesBody(t *testing.T) {
	service := &fakeGitService{stageResult: &gitbrowse.StageResult{Action: "unstage", Files: []string{"src/a.ts"}}}
	router := newGitTestRouter(service)

	rec := doGitRequest(router, http.MethodPost, "/api/runtime/git/stage",
		`{"scope":"workspace:wd","path":"src","action":"unstage","files":["src/a.ts","src/b.ts"]}`, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, gitbrowse.StageRequest{
		RepoRequest: gitbrowse.RepoRequest{Scope: "workspace:wd", Path: "src"},
		Action:      "unstage",
		Files:       []string{"src/a.ts", "src/b.ts"},
	}, service.gotStage)
	require.Contains(t, rec.Body.String(), `"action":"unstage"`)
}

func TestGitStageHandlerRejectsBadPayload(t *testing.T) {
	service := &fakeGitService{stageResult: &gitbrowse.StageResult{}}
	router := newGitTestRouter(service)

	rec := doGitRequest(router, http.MethodPost, "/api/runtime/git/stage", "{not json", nil)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	payload, _ := decodeGitError(t, rec)
	require.Equal(t, gitbrowse.CodeInvalidRequest, payload["code"])
	require.Equal(t, gitbrowse.StageRequest{}, service.gotStage)

	// 超过 body 上限（1MiB）也必须降级为 400，而不是读爆内存。
	huge := `{"action":"stage","files":["` + strings.Repeat("x", gitStageBodyLimit+16) + `"]}`
	rec = doGitRequest(router, http.MethodPost, "/api/runtime/git/stage", huge, nil)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	payload, _ = decodeGitError(t, rec)
	require.Equal(t, gitbrowse.CodeInvalidRequest, payload["code"])
}

func TestGitHandlersMapErrorsToStatusAndEchoRequestID(t *testing.T) {
	cases := []struct {
		name         string
		err          error
		wantStatus   int
		wantCode     string
		wantExitCode bool
	}{
		{name: "git_failed", err: &gitbrowse.Error{Code: gitbrowse.CodeGitFailed, Message: "git status failed", Status: 500, ExitCode: 128}, wantStatus: 500, wantCode: gitbrowse.CodeGitFailed, wantExitCode: true},
		{name: "repo_not_found", err: &gitbrowse.Error{Code: gitbrowse.CodeRepoNotFound, Message: "not a git repository", Status: 404}, wantStatus: 404, wantCode: gitbrowse.CodeRepoNotFound},
		{name: "path_outside_scope", err: &gitbrowse.Error{Code: gitbrowse.CodePathOutsideScope, Message: "outside", Status: 400}, wantStatus: 400, wantCode: gitbrowse.CodePathOutsideScope},
		{name: "git_unavailable", err: &gitbrowse.Error{Code: gitbrowse.CodeGitUnavailable, Message: "no git", Status: 503}, wantStatus: 503, wantCode: gitbrowse.CodeGitUnavailable},
		{name: "plain_error_falls_back_to_500", err: errors.New("boom"), wantStatus: 500, wantCode: gitbrowse.CodeGitFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			service := &fakeGitService{statusErr: tc.err}
			router := newGitTestRouter(service)
			rec := doGitRequest(router, http.MethodGet, "/api/runtime/git/status",
				"", map[string]string{"X-Request-ID": "req-42"})
			require.Equal(t, tc.wantStatus, rec.Code)
			payload, requestID := decodeGitError(t, rec)
			require.Equal(t, tc.wantCode, payload["code"])
			require.Equal(t, "req-42", requestID)
			if tc.wantExitCode {
				require.EqualValues(t, 128, payload["exit_code"])
			} else {
				require.NotContains(t, payload, "exit_code")
			}
		})
	}
}

func TestGitRoutesMethodNotAllowed(t *testing.T) {
	router := newGitRootRouter(&fakeGitService{})

	require.Equal(t, http.StatusMethodNotAllowed,
		doGitRequest(router, http.MethodPost, "/git/status", "{}", nil).Code)
	require.Equal(t, http.StatusMethodNotAllowed,
		doGitRequest(router, http.MethodGet, "/git/stage", "", nil).Code)
	require.Equal(t, http.StatusNotFound,
		doGitRequest(router, http.MethodGet, "/git/unknown", "", nil).Code)
}

// newGitHandlerTestRepo 建立临时仓库并返回真实 gitbrowse 服务（无 git 时跳过）。
func newGitHandlerTestRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available on PATH")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		out, err := cmd.CombinedOutput()
		require.NoErrorf(t, err, "git %v failed: %s", args, out)
	}
	run("init", "-q", "-b", "main")
	run("config", "user.email", "handler@example.com")
	run("config", "user.name", "handler tests")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.txt"), []byte("line1\nline2\n"), 0o644))
	run("add", "a.txt")
	run("commit", "-q", "-m", "init")
	return dir
}

// 端到端：真实 gitbrowse.Service + 真实路由（覆盖参数解码 → 作用域校验 → git 执行）。
func TestGitRoutesEndToEndWithRealService(t *testing.T) {
	root := newGitHandlerTestRepo(t)
	service := gitbrowse.NewService(gitbrowse.Deps{Cwd: root, Limits: gitbrowse.DefaultLimits()})
	router := newGitTestRouter(service)

	rec := doGitRequest(router, http.MethodGet, "/api/runtime/git/status?scope=cwd", "", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	var status gitbrowse.StatusResult
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &status))
	require.True(t, status.Clean)
	require.Equal(t, "main", status.Repo.Branch)
	require.Equal(t, root, status.Repo.Root)
	require.NotEmpty(t, status.Repo.Head)

	rec = doGitRequest(router, http.MethodGet, "/api/runtime/git/commits?scope=cwd&limit=10", "", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	var commits gitbrowse.CommitsResult
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &commits))
	require.Len(t, commits.Commits, 1)
	require.Equal(t, "init", commits.Commits[0].Subject)

	rec = doGitRequest(router, http.MethodGet, "/api/runtime/git/diff?scope=cwd&file=a.txt", "", nil)
	require.Equal(t, http.StatusOK, rec.Code)

	// 作用域内的越界尝试：无论是否二次编码，都必须 400 path_outside_scope。
	for _, pathParam := range []string{"..%2F..%2Fetc", "..%252F..%252Fetc", "%2e%2e%2f%2e%2e"} {
		t.Run(pathParam, func(t *testing.T) {
			rec := doGitRequest(router, http.MethodGet,
				"/api/runtime/git/status?scope=cwd&path="+pathParam, "", nil)
			require.Equal(t, http.StatusBadRequest, rec.Code)
			payload, _ := decodeGitError(t, rec)
			require.Equal(t, gitbrowse.CodePathOutsideScope, payload["code"])
		})
	}
}

// 端到端：POST /git/stage 走真实 git，stage/unstage 各一次。
func TestGitStageRouteEndToEndWithRealService(t *testing.T) {
	root := newGitHandlerTestRepo(t)
	require.NoError(t, os.WriteFile(filepath.Join(root, "new.txt"), []byte("new\n"), 0o644))
	service := gitbrowse.NewService(gitbrowse.Deps{Cwd: root, Limits: gitbrowse.DefaultLimits()})
	router := newGitTestRouter(service)

	rec := doGitRequest(router, http.MethodPost, "/api/runtime/git/stage",
		`{"scope":"cwd","action":"stage","files":["new.txt"]}`, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	var staged gitbrowse.StageResult
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &staged))
	require.Equal(t, "stage", staged.Action)
	require.Len(t, staged.Status.Staged, 1)
	require.Equal(t, "A", staged.Status.Staged[0].Status)

	rec = doGitRequest(router, http.MethodPost, "/api/runtime/git/stage",
		`{"scope":"cwd","action":"unstage","files":["new.txt"]}`, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	var unstaged gitbrowse.StageResult
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &unstaged))
	require.Empty(t, unstaged.Status.Staged)
	require.Len(t, unstaged.Status.Untracked, 1)
}
