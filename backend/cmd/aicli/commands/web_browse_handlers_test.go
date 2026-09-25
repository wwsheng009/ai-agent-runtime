package commands

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/fsscope"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
)

// 「文件」/「GIT」页签的后端端点（/web/api/fs/* 与 /web/api/git/*）门禁：
//   - 作用域根只来自当前 aicli 会话的工作目录，前端把 roots 返回的 scope 原样回传；
//   - 未知子路径 / 方法不符 / 越界路径统一走 {"error":{"code",…}} envelope；
//   - fs 与 git 共用同一根解析：同一个会话工作目录在两个页签里必须指向同一目录。

// chatWebBrowseTestSession 注入当前会话（工作区绑定 workspace；空串表示未绑定）。
func chatWebBrowseTestSession(t *testing.T, sessionID, workspace string) *ChatSession {
	t.Helper()
	session := runtimechat.NewSession("browse-web-user")
	session.ID = sessionID
	if workspace != "" {
		session.Metadata.Context = map[string]interface{}{sessionmeta.WorkspacePath: workspace}
	}
	current := &ChatSession{RuntimeSession: session}
	prev := chatDebugDisplaySessionProvider
	t.Cleanup(func() { chatDebugDisplaySessionProvider = prev })
	RegisterChatDebugDisplayProvider(func() *ChatSession { return current })
	return current
}

// chatWebBrowseCall 发一次请求（body 为空时用 nil）。
func chatWebBrowseCall(t *testing.T, handler http.HandlerFunc, method, target string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		reader = bytes.NewReader(body)
	}
	req := httptest.NewRequest(method, target, reader)
	rec := httptest.NewRecorder()
	handler(rec, req)
	return rec
}

// chatWebBrowseJSON 解码 JSON 对象响应；非 JSON 直接失败。
func chatWebBrowseJSON(t *testing.T, rec *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	var payload map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("响应不是 JSON 对象: %v (%s)", err, rec.Body.String())
	}
	return payload
}

// chatWebBrowseErrorCode 取错误 envelope 里的机器码。
func chatWebBrowseErrorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	payload := chatWebBrowseJSON(t, rec)
	envelope, ok := payload["error"].(map[string]interface{})
	if !ok {
		t.Fatalf("响应缺少 error 对象: %s", rec.Body.String())
	}
	code, _ := envelope["code"].(string)
	if code == "" {
		t.Fatalf("错误 envelope 缺少 code: %s", rec.Body.String())
	}
	return code
}

// chatWebBrowseHasPath 在状态分组里找路径（用 JSON 文本比对，避免绑定具体字段名）。
func chatWebBrowseHasPath(t *testing.T, list interface{}, want string) bool {
	t.Helper()
	items, ok := list.([]interface{})
	if !ok {
		return false
	}
	for _, item := range items {
		raw, err := json.Marshal(item)
		if err != nil {
			continue
		}
		if strings.Contains(string(raw), `"`+want+`"`) {
			return true
		}
	}
	return false
}

// chatWebBrowseScope 取 roots 响应里指定目录的作用域（前端同款做法：原样回传 scope）。
func chatWebBrowseScope(t *testing.T, workspace string) string {
	t.Helper()
	rec := chatWebBrowseCall(t, HandleChatWebAPIFs, http.MethodGet, ChatWebAPIFsPath+"/roots", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("roots status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	payload := chatWebBrowseJSON(t, rec)
	roots, ok := payload["roots"].([]interface{})
	if !ok {
		t.Fatalf("roots 必须是数组（不能是 null）: %s", rec.Body.String())
	}
	want := filepath.Clean(workspace)
	for _, item := range roots {
		root, _ := item.(map[string]interface{})
		path, _ := root["path"].(string)
		if filepath.Clean(path) != want {
			continue
		}
		if kind, _ := root["kind"].(string); kind != string(fsscope.KindSession) {
			t.Fatalf("工作目录根 kind = %q, want %q: %s", kind, fsscope.KindSession, rec.Body.String())
		}
		scope, _ := root["scope"].(string)
		if scope == "" {
			t.Fatalf("工作目录根缺少 scope: %s", rec.Body.String())
		}
		return scope
	}
	t.Fatalf("roots 未包含会话工作目录 %s: %s", want, rec.Body.String())
	return ""
}

// TestChatWebFsEndpointsBrowseWorkspace 覆盖「文件」页签的只读端点族：
// roots → list → stat → preview → download → search 全链路，作用域根为会话工作目录。
func TestChatWebFsEndpointsBrowseWorkspace(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "hello.txt"), []byte("hello world\n"), 0o644); err != nil {
		t.Fatalf("写测试文件: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(workspace, "sub"), 0o755); err != nil {
		t.Fatalf("建测试目录: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "sub", "nested.txt"), []byte("nested\n"), 0o644); err != nil {
		t.Fatalf("写嵌套文件: %v", err)
	}
	chatWebBrowseTestSession(t, "sess-browse-fs", workspace)
	scope := chatWebBrowseScope(t, workspace)

	// list：根目录一层，hello.txt 在列。
	rec := chatWebBrowseCall(t, HandleChatWebAPIFs, http.MethodGet,
		ChatWebAPIFsPath+"/list?scope="+scope+"&path=", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	list := chatWebBrowseJSON(t, rec)
	if entries, ok := list["entries"].([]interface{}); !ok || len(entries) == 0 {
		t.Fatalf("list 未返回条目: %s", rec.Body.String())
	}
	if !chatWebBrowseHasPath(t, list["entries"], "hello.txt") {
		t.Fatalf("list 缺少 hello.txt: %s", rec.Body.String())
	}
	if dir, ok := list["dir"].(map[string]interface{}); !ok || dir["is_root"] != true {
		t.Fatalf("list 根目录标记缺失: %s", rec.Body.String())
	}

	// stat：单路径元信息。
	rec = chatWebBrowseCall(t, HandleChatWebAPIFs, http.MethodGet,
		ChatWebAPIFsPath+"/stat?scope="+scope+"&path=hello.txt", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("stat status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if stat := chatWebBrowseJSON(t, rec); stat["name"] != "hello.txt" {
		t.Fatalf("stat name = %v, want hello.txt (%s)", stat["name"], rec.Body.String())
	}

	// preview：文本预览。
	rec = chatWebBrowseCall(t, HandleChatWebAPIFs, http.MethodGet,
		ChatWebAPIFsPath+"/preview?scope="+scope+"&path=hello.txt", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("preview status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	preview := chatWebBrowseJSON(t, rec)
	if preview["kind"] != "text" {
		t.Fatalf("preview kind = %v, want text (%s)", preview["kind"], rec.Body.String())
	}
	if text, _ := preview["text"].(string); !strings.Contains(text, "hello world") {
		t.Fatalf("preview text 缺少文件内容: %s", rec.Body.String())
	}

	// download：附件下载，内容原样。
	rec = chatWebBrowseCall(t, HandleChatWebAPIFs, http.MethodGet,
		ChatWebAPIFsPath+"/download?scope="+scope+"&path=hello.txt", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("download status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if disposition := rec.Header().Get("Content-Disposition"); !strings.Contains(disposition, "attachment") {
		t.Fatalf("download Content-Disposition = %q, want attachment", disposition)
	}
	if body := rec.Body.String(); !strings.Contains(body, "hello world") {
		t.Fatalf("download body = %q, want 文件内容", body)
	}

	// search：按名称字面匹配。
	rec = chatWebBrowseCall(t, HandleChatWebAPIFs, http.MethodGet,
		ChatWebAPIFsPath+"/search?scope="+scope+"&q=hello", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("search status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	search := chatWebBrowseJSON(t, rec)
	if items, ok := search["items"].([]interface{}); !ok || len(items) == 0 {
		t.Fatalf("search 未命中 hello.txt: %s", rec.Body.String())
	}
}

// TestChatWebFsEndpointsRejectBadInput 锁定错误路径：未知子路径 404、
// 方法不符 405、越界路径 4xx、未知 scope 404 —— 全部带机器码。
func TestChatWebFsEndpointsRejectBadInput(t *testing.T) {
	workspace := t.TempDir()
	chatWebBrowseTestSession(t, "sess-browse-guard", workspace)
	scope := chatWebBrowseScope(t, workspace)

	rec := chatWebBrowseCall(t, HandleChatWebAPIFs, http.MethodGet, ChatWebAPIFsPath+"/nope", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("未知子路径 status = %d, want 404 (%s)", rec.Code, rec.Body.String())
	}
	if code := chatWebBrowseErrorCode(t, rec); code != "fs_endpoint_not_found" {
		t.Fatalf("未知子路径 code = %q, want fs_endpoint_not_found", code)
	}

	rec = chatWebBrowseCall(t, HandleChatWebAPIFs, http.MethodPost, ChatWebAPIFsPath+"/list", nil)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /fs/list status = %d, want 405 (%s)", rec.Code, rec.Body.String())
	}
	// 405 沿用 web 端点既有体（{"status":"rejected","reason":"method not allowed"}）。
	if body := rec.Body.String(); !strings.Contains(body, "method not allowed") {
		t.Fatalf("405 body = %s, want method not allowed", body)
	}

	rec = chatWebBrowseCall(t, HandleChatWebAPIFs, http.MethodGet,
		ChatWebAPIFsPath+"/preview?scope="+scope+"&path=../../../../../../etc/passwd", nil)
	if rec.Code < 400 {
		t.Fatalf("越界路径 status = %d, want 4xx (%s)", rec.Code, rec.Body.String())
	}
	_ = chatWebBrowseErrorCode(t, rec)

	rec = chatWebBrowseCall(t, HandleChatWebAPIFs, http.MethodGet,
		ChatWebAPIFsPath+"/list?scope=session:does-not-exist&path=", nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("未知 scope status = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
	// 会话存在但没有工作目录 → scope_has_no_root（绝不回退进程 cwd）。
	if code := chatWebBrowseErrorCode(t, rec); code != fsscope.CodeScopeHasNoRoot {
		t.Fatalf("未知 scope code = %q, want %q", code, fsscope.CodeScopeHasNoRoot)
	}
}

// TestChatWebFsRootsWithoutSession 无当前会话时 roots 仍是数组（契约不允许 null），
// 且不因为缺少会话而 5xx。
func TestChatWebFsRootsWithoutSession(t *testing.T) {
	prev := chatDebugDisplaySessionProvider
	t.Cleanup(func() { chatDebugDisplaySessionProvider = prev })
	RegisterChatDebugDisplayProvider(func() *ChatSession { return nil })

	rec := chatWebBrowseCall(t, HandleChatWebAPIFs, http.MethodGet, ChatWebAPIFsPath+"/roots", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("roots status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	payload := chatWebBrowseJSON(t, rec)
	if _, ok := payload["roots"].([]interface{}); !ok {
		t.Fatalf("roots 必须是数组: %s", rec.Body.String())
	}
}

// TestChatWebGitEndpointsStatusDiffCommitsStage 覆盖「GIT」页签：
// status（干净 → 脏）→ diff → commits → stage/unstage，作用域根与「文件」页签同源。
func TestChatWebGitEndpointsStatusDiffCommitsStage(t *testing.T) {
	repo := chatWebGitTestRepo(t)
	chatWebBrowseTestSession(t, "sess-browse-git", repo)
	scope := chatWebBrowseScope(t, repo)

	// 同一个工作目录：文件页签的根必须认出这是 git 仓库。
	rootsRec := chatWebBrowseCall(t, HandleChatWebAPIFs, http.MethodGet, ChatWebAPIFsPath+"/roots", nil)
	rootsPayload := chatWebBrowseJSON(t, rootsRec)
	foundGitRoot := false
	for _, item := range rootsPayload["roots"].([]interface{}) {
		root, _ := item.(map[string]interface{})
		if path, _ := root["path"].(string); filepath.Clean(path) == filepath.Clean(repo) {
			foundGitRoot = root["is_git_repo"] == true
		}
	}
	if !foundGitRoot {
		t.Fatalf("fs roots 未把会话工作目录标记为 git 仓库: %s", rootsRec.Body.String())
	}

	// 干净仓库。
	rec := chatWebBrowseCall(t, HandleChatWebAPIGit, http.MethodGet,
		ChatWebAPIGitPath+"/status?scope="+scope+"&path=", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("git status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	status := chatWebBrowseJSON(t, rec)
	repoInfo, _ := status["repo"].(map[string]interface{})
	if root, _ := repoInfo["root"].(string); filepath.Clean(root) != filepath.Clean(repo) {
		t.Fatalf("git status repo.root = %v, want %s", repoInfo["root"], repo)
	}
	if status["clean"] != true {
		t.Fatalf("新仓库应为 clean: %s", rec.Body.String())
	}

	// 制造脏工作区：改一个已跟踪文件 + 新增未跟踪文件。
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("line1\nline2 changed\n"), 0o644); err != nil {
		t.Fatalf("改文件: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "untracked.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatalf("写未跟踪文件: %v", err)
	}
	rec = chatWebBrowseCall(t, HandleChatWebAPIGit, http.MethodGet,
		ChatWebAPIGitPath+"/status?scope="+scope+"&path=", nil)
	status = chatWebBrowseJSON(t, rec)
	if status["clean"] != false {
		t.Fatalf("改动后 clean 应为 false: %s", rec.Body.String())
	}
	if !chatWebBrowseHasPath(t, status["unstaged"], "a.txt") {
		t.Fatalf("unstaged 缺少 a.txt: %s", rec.Body.String())
	}
	if !chatWebBrowseHasPath(t, status["untracked"], "untracked.txt") {
		t.Fatalf("untracked 缺少 untracked.txt: %s", rec.Body.String())
	}

	// diff：工作区 vs 暂存区，至少一个 hunk。
	rec = chatWebBrowseCall(t, HandleChatWebAPIGit, http.MethodGet,
		ChatWebAPIGitPath+"/diff?scope="+scope+"&path=&file=a.txt&target=working", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("git diff = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	diff := chatWebBrowseJSON(t, rec)
	hunks, ok := diff["hunks"].([]interface{})
	if !ok || len(hunks) == 0 {
		t.Fatalf("diff 未返回 hunk: %s", rec.Body.String())
	}
	if parseError, _ := diff["parse_error"].(string); parseError != "" {
		t.Fatalf("diff parse_error = %q, want 空", parseError)
	}

	// commits：至少一次提交，subject 为 init。
	rec = chatWebBrowseCall(t, HandleChatWebAPIGit, http.MethodGet,
		ChatWebAPIGitPath+"/commits?scope="+scope+"&path=&limit=5", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("git commits = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	commits, ok := chatWebBrowseJSON(t, rec)["commits"].([]interface{})
	if !ok || len(commits) == 0 {
		t.Fatalf("commits 为空: %s", rec.Body.String())
	}
	first, _ := commits[0].(map[string]interface{})
	if first["subject"] != "init" {
		t.Fatalf("首条提交 subject = %v, want init", first["subject"])
	}

	// stage：写入后响应自带最新 status（前端直接替换本地缓存）。
	payload, err := json.Marshal(map[string]interface{}{
		"scope":  scope,
		"path":   "",
		"action": "stage",
		"files":  []string{"a.txt", "untracked.txt"},
	})
	if err != nil {
		t.Fatalf("构造 stage 请求体: %v", err)
	}
	rec = chatWebBrowseCall(t, HandleChatWebAPIGit, http.MethodPost, ChatWebAPIGitPath+"/stage", payload)
	if rec.Code != http.StatusOK {
		t.Fatalf("git stage = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	staged := chatWebBrowseJSON(t, rec)
	if staged["action"] != "stage" {
		t.Fatalf("stage action = %v, want stage", staged["action"])
	}
	stagedStatus, _ := staged["status"].(map[string]interface{})
	if !chatWebBrowseHasPath(t, stagedStatus["staged"], "a.txt") {
		t.Fatalf("stage 响应里 staged 缺少 a.txt: %s", rec.Body.String())
	}
	if !chatWebBrowseHasPath(t, stagedStatus["staged"], "untracked.txt") {
		t.Fatalf("stage 响应里 staged 缺少 untracked.txt: %s", rec.Body.String())
	}
	if chatWebBrowseHasPath(t, stagedStatus["unstaged"], "a.txt") {
		t.Fatalf("stage 后 unstaged 仍含 a.txt: %s", rec.Body.String())
	}

	// unstage：撤回暂存，状态回到未暂存。
	payload, err = json.Marshal(map[string]interface{}{
		"scope":  scope,
		"action": "unstage",
		"files":  []string{"a.txt"},
	})
	if err != nil {
		t.Fatalf("构造 unstage 请求体: %v", err)
	}
	rec = chatWebBrowseCall(t, HandleChatWebAPIGit, http.MethodPost, ChatWebAPIGitPath+"/stage", payload)
	if rec.Code != http.StatusOK {
		t.Fatalf("git unstage = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	unstaged := chatWebBrowseJSON(t, rec)
	unstagedStatus, _ := unstaged["status"].(map[string]interface{})
	if chatWebBrowseHasPath(t, unstagedStatus["staged"], "a.txt") {
		t.Fatalf("unstage 后 staged 仍含 a.txt: %s", rec.Body.String())
	}
	if !chatWebBrowseHasPath(t, unstagedStatus["unstaged"], "a.txt") {
		t.Fatalf("unstage 后 unstaged 缺少 a.txt: %s", rec.Body.String())
	}
}

// TestChatWebGitEndpointsRejectBadInput 锁定 git 端点的错误路径：
// 未知子路径 404、GET /stage 405、非法 JSON 400、非仓库目录返回 git 错误码。
func TestChatWebGitEndpointsRejectBadInput(t *testing.T) {
	workspace := t.TempDir()
	chatWebBrowseTestSession(t, "sess-browse-git-guard", workspace)
	scope := chatWebBrowseScope(t, workspace)

	rec := chatWebBrowseCall(t, HandleChatWebAPIGit, http.MethodGet, ChatWebAPIGitPath+"/nope", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("未知子路径 status = %d, want 404 (%s)", rec.Code, rec.Body.String())
	}
	if code := chatWebBrowseErrorCode(t, rec); code != "git_endpoint_not_found" {
		t.Fatalf("未知子路径 code = %q, want git_endpoint_not_found", code)
	}

	rec = chatWebBrowseCall(t, HandleChatWebAPIGit, http.MethodGet, ChatWebAPIGitPath+"/stage", nil)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET /git/stage status = %d, want 405 (%s)", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); !strings.Contains(body, "method not allowed") {
		t.Fatalf("405 body = %s, want method not allowed", body)
	}

	rec = chatWebBrowseCall(t, HandleChatWebAPIGit, http.MethodPost, ChatWebAPIGitPath+"/stage", []byte("{not json"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("非法 JSON status = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
	_ = chatWebBrowseErrorCode(t, rec)

	// 非 git 仓库：如实返回错误码（不伪造成空状态）。
	rec = chatWebBrowseCall(t, HandleChatWebAPIGit, http.MethodGet,
		ChatWebAPIGitPath+"/status?scope="+scope+"&path=", nil)
	if rec.Code == http.StatusOK {
		t.Fatalf("非仓库目录不该返回 200: %s", rec.Body.String())
	}
	_ = chatWebBrowseErrorCode(t, rec)
}

// chatWebGitTestRepo 建一个带一次提交的临时仓库（git 不在 PATH 时跳过）。
func chatWebGitTestRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git 不在 PATH，跳过 git 端点测试")
	}
	dir := t.TempDir()
	runChatWebGit(t, dir, "init", "-q", "-b", "main")
	runChatWebGit(t, dir, "config", "user.email", "web-browse-tests@example.com")
	runChatWebGit(t, dir, "config", "user.name", "web browse tests")
	runChatWebGit(t, dir, "config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("line1\nline2\n"), 0o644); err != nil {
		t.Fatalf("写仓库文件: %v", err)
	}
	runChatWebGit(t, dir, "add", "a.txt")
	runChatWebGit(t, dir, "commit", "-q", "-m", "init")
	return dir
}

// runChatWebGit 在 dir 下执行 git 子命令，失败即测试失败。
func runChatWebGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_DATE=2026-01-01T00:00:00+00:00",
		"GIT_COMMITTER_DATE=2026-01-01T00:00:00+00:00",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v 失败: %v (%s)", args, err, out)
	}
}
