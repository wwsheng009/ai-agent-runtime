继续。下面我直接把 **启动层、HTTP API、Anthropic client、两个基础工具** 补齐，这样你就能把 P0 真正跑起来。

这次给你 6 个文件骨架：

* `cmd/agentd/main.go`
* `internal/api/server.go`
* `internal/api/handlers.go`
* `internal/model/anthropic/client.go`
* `internal/tools/local/read_file.go`
* `internal/tools/local/grep_repo.go`

最后再给你一条**最小启动链路**。

---

## 1. `cmd/agentd/main.go`

这个文件负责把所有组件串起来。

```go
package main

import (
	"context"
	"database/sql"
	"log"
	"net/http"
	"os"

	_ "modernc.org/sqlite"

	"yourmod/internal/api"
	"yourmod/internal/artifact"
	"yourmod/internal/contextmgr"
	"yourmod/internal/events"
	"yourmod/internal/model/anthropic"
	"yourmod/internal/runtime"
	"yourmod/internal/scheduler"
	sqlstore "yourmod/internal/store/sqlite"
	"yourmod/internal/tools"
	"yourmod/internal/tools/local"
	"yourmod/internal/tools/reducers"
)

func main() {
	ctx := context.Background()

	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		log.Fatal("missing ANTHROPIC_API_KEY")
	}

	db, err := sql.Open("sqlite", "./agent.db")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	if err := runSchema(db); err != nil {
		log.Fatal(err)
	}

	sessionRepo := &sqlstore.SessionRepo{DB: db}
	messageRepo := &sqlstore.MessageRepo{DB: db}
	memoryRepo := &sqlstore.MemoryRepo{DB: db}
	artifactRepo := &sqlstore.ArtifactRepo{DB: db}
	eventRepo := &sqlstore.EventRepo{DB: db}

	modelClient := anthropic.New(apiKey)

	artifactStore := &artifact.Store{
		BaseDir: "./artifacts",
		Meta:    artifactRepo,
	}
	if err := os.MkdirAll("./artifacts", 0755); err != nil {
		log.Fatal(err)
	}

	reducerChain := reducers.NewChain(
		&reducers.GitLogReducer{},
	)

	toolBroker := tools.NewBroker(
		&local.GitLogTool{},
		&local.ReadFileTool{},
		&local.GrepRepoTool{},
	)

	ctxMgr := &contextmgr.Manager{
		Store: &ContextStoreAdapter{
			Sessions: sessionRepo,
			Messages: messageRepo,
			Memory:   memoryRepo,
		},
		Budget: contextmgr.DefaultBudget(),
		Tools: func(s *runtime.Session) []runtime.ToolDef {
			return []runtime.ToolDef{
				{
					Name:        "git_log",
					Description: "Read git history and stats for recent commits",
					InputSchema: map[string]any{
						"type": "object",
						"properties": map[string]any{
							"since": map[string]any{"type": "string"},
						},
					},
				},
				{
					Name:        "read_file",
					Description: "Read a file from the repo workspace",
					InputSchema: map[string]any{
						"type": "object",
						"properties": map[string]any{
							"path": map[string]any{"type": "string"},
						},
						"required": []string{"path"},
					},
				},
				{
					Name:        "grep_repo",
					Description: "Search for text in the repo workspace",
					InputSchema: map[string]any{
						"type": "object",
						"properties": map[string]any{
							"query": map[string]any{"type": "string"},
							"path":  map[string]any{"type": "string"},
						},
						"required": []string{"query"},
					},
				},
			}
		},
	}

	policyEngine := AllowAllReadOnlyPolicy{}

	childRunner := &scheduler.ChildRunner{
		Sessions: &ChildSessionFactory{
			Repo: sessionRepo,
		},
		Runner: &runtime.Runner{
			Model:     modelClient,
			Context:   ctxMgr,
			Broker:    toolBroker,
			Artifacts: artifactStore,
			Policy:    policyEngine,
			EventBus:  eventRepo,
			Reducer:   reducerChain,
			// Scheduler 会在下面注入
			MaxTurnsPerRun: 8,
		},
		Reader: &LatestAssistantReader{DB: db},
	}

	sched := &scheduler.Scheduler{
		Config: scheduler.Config{
			MaxConcurrentChildren: 1,
			MaxDepth:              1,
			MaxChildTurns:         6,
		},
		Child: childRunner,
	}

	runner := &runtime.Runner{
		Model:      modelClient,
		Context:    ctxMgr,
		Broker:     toolBroker,
		Artifacts:  artifactStore,
		Scheduler:  sched,
		Policy:     policyEngine,
		EventBus:   eventRepo,
		Reducer:    reducerChain,
		MaxTurnsPerRun: 8,
	}

	sessionService := &runtime.SessionService{
		Repo:   sessionRepo,
		Runner: runner,
	}

	server := &api.Server{
		SessionService: sessionService,
		SessionRepo:    sessionRepo,
		MessageRepo:    messageRepo,
		EventRepo:      eventRepo,
		Runner:         runner,
	}

	log.Println("listening on :8080")
	if err := http.ListenAndServe(":8080", server.Routes(ctx)); err != nil {
		log.Fatal(err)
	}
}

func runSchema(db *sql.DB) error {
	sqlBytes, err := os.ReadFile("internal/store/sqlite/schema.sql")
	if err != nil {
		return err
	}
	_, err = db.Exec(string(sqlBytes))
	return err
}

// --- adapters / simple implementations ---

type ContextStoreAdapter struct {
	Sessions *sqlstore.SessionRepo
	Messages *sqlstore.MessageRepo
	Memory   *sqlstore.MemoryRepo
}

func (a *ContextStoreAdapter) LoadSessionTask(ctx context.Context, sessionID string) (*runtime.Task, error) {
	return a.Sessions.GetRootTask(ctx, sessionID)
}

func (a *ContextStoreAdapter) LoadRecentMessages(ctx context.Context, sessionID string, limit int) ([]runtime.Message, error) {
	return a.Messages.LoadRecentMessages(ctx, sessionID, limit)
}

func (a *ContextStoreAdapter) LoadMemoryEntries(ctx context.Context, sessionID string, kinds []string, limit int) ([]runtime.Message, error) {
	return a.Memory.LoadMemoryEntriesAsMessages(ctx, sessionID, kinds, limit)
}

func (a *ContextStoreAdapter) LoadRecentEvidence(ctx context.Context, sessionID string, limit int) ([]runtime.Message, error) {
	// P0 先返回空；P1 再接 tool envelopes / recall snippets
	return nil, nil
}

func (a *ContextStoreAdapter) SaveAssistantMessage(ctx context.Context, sessionID string, text string) error {
	return a.Messages.SaveAssistantMessage(ctx, sessionID, text)
}

func (a *ContextStoreAdapter) SaveToolEnvelope(ctx context.Context, sessionID string, env runtime.ToolEnvelope) error {
	return a.Messages.SaveEnvelopeMessage(ctx, sessionID, env)
}

func (a *ContextStoreAdapter) SaveSubagentReport(ctx context.Context, sessionID string, rep runtime.SubagentReport) error {
	return a.Messages.SaveAssistantMessage(ctx, sessionID, "Subagent "+rep.TaskName+": "+rep.Summary)
}

type AllowAllReadOnlyPolicy struct{}

func (p AllowAllReadOnlyPolicy) AllowTool(ctx context.Context, call runtime.ToolCall) error {
	switch call.Name {
	case "git_log", "read_file", "grep_repo":
		return nil
	default:
		return os.ErrPermission
	}
}

type ChildSessionFactory struct {
	Repo *sqlstore.SessionRepo
}

func (f *ChildSessionFactory) NewChildSession(ctx context.Context, parent *runtime.Session, task runtime.TaskPacket) (*runtime.Session, error) {
	now := runtimeNow()
	s := &runtime.Session{
		ID:         newID(),
		UserID:     parent.UserID,
		RootTaskID: newID(),
		Model:      parent.Model,
		Status:     "new",
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	root := &runtime.Task{
		ID:              s.RootTaskID,
		SessionID:       s.ID,
		Role:            "child",
		Goal:            task.Goal,
		SuccessCriteria: task.SuccessCriteria,
		AllowedTools:    task.AllowedTools,
		Status:          "queued",
		BudgetTokens:    task.BudgetTokens,
		BudgetUSD:       task.BudgetUSD,
		MaxTurns:        task.MaxTurns,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	return s, f.Repo.CreateAndReturn(ctx, s, root)
}

type LatestAssistantReader struct {
	DB *sql.DB
}

func (r *LatestAssistantReader) GetLatestAssistantText(ctx context.Context, sessionID string) (string, error) {
	row := r.DB.QueryRowContext(ctx, `
		SELECT content_json
		FROM messages
		WHERE session_id = ? AND role = 'assistant'
		ORDER BY created_at DESC
		LIMIT 1
	`, sessionID)
	var contentJSON string
	if err := row.Scan(&contentJSON); err != nil {
		return "", err
	}
	return extractTextFromJSON(contentJSON), nil
}

// 你可以把这几个 util 挪到统一 util 包
func runtimeNow() time.Time { return time.Now() }
func newID() string         { return fmt.Sprintf("%d", time.Now().UnixNano()) }
```

这段会有几个你需要补的 import 和 util，但整体 wiring 已经定了。

---

## 2. `internal/api/server.go`

```go
package api

import (
	"context"
	"net/http"

	"yourmod/internal/runtime"
	sqlstore "yourmod/internal/store/sqlite"
)

type Server struct {
	SessionService *runtime.SessionService
	SessionRepo    *sqlstore.SessionRepo
	MessageRepo    *sqlstore.MessageRepo
	EventRepo      *sqlstore.EventRepo
	Runner         *runtime.Runner
}

func (s *Server) Routes(ctx context.Context) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("POST /sessions", s.CreateSession)
	mux.HandleFunc("POST /sessions/", s.PostMessageToSession)
	mux.HandleFunc("GET /session/", s.GetSession)

	return mux
}
```

---

## 3. `internal/api/handlers.go`

这里先做最小可用：

* 创建 session
* 发送一条用户消息并立刻驱动 agent loop
* 获取 session 状态

```go
package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"

	"yourmod/internal/runtime"
)

type CreateSessionRequest struct {
	UserID string `json:"user_id"`
	Model  string `json:"model"`
	Goal   string `json:"goal"`
}

type CreateSessionResponse struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

type PostMessageRequest struct {
	Content string `json:"content"`
}

func (s *Server) CreateSession(w http.ResponseWriter, r *http.Request) {
	var req CreateSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	id, err := s.SessionService.Create(r.Context(), req.UserID, req.Model, req.Goal)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, CreateSessionResponse{
		ID:     id,
		Status: "created",
	})
}

func (s *Server) PostMessageToSession(w http.ResponseWriter, r *http.Request) {
	// 路径格式: /sessions/{id}/messages
	path := strings.TrimPrefix(r.URL.Path, "/sessions/")
	parts := strings.Split(path, "/")
	if len(parts) != 2 || parts[1] != "messages" {
		http.NotFound(w, r)
		return
	}
	sessionID := parts[0]

	var req PostMessageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := s.insertUserMessage(r.Context(), sessionID, req.Content); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	session, err := s.SessionRepo.GetSession(r.Context(), sessionID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	if err := s.Runner.RunUntilIdle(r.Context(), session); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, map[string]any{
		"session_id": sessionID,
		"status":     "processed",
	})
}

func (s *Server) GetSession(w http.ResponseWriter, r *http.Request) {
	// 路径格式: /session/{id}
	sessionID := strings.TrimPrefix(r.URL.Path, "/session/")
	session, err := s.SessionRepo.GetSession(r.Context(), sessionID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	writeJSON(w, session)
}

func (s *Server) insertUserMessage(ctx context.Context, sessionID, text string) error {
	return s.MessageRepo.InsertUserText(ctx, sessionID, text)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("content-type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
```

---

## 4. `internal/model/anthropic/client.go`

上次给了部分，这次补完整一点。

```go
package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"yourmod/internal/runtime"
)

type Client struct {
	APIKey  string
	BaseURL string
	Version string
	HTTP    *http.Client
}

func New(apiKey string) *Client {
	return &Client{
		APIKey:  apiKey,
		BaseURL: "https://api.anthropic.com",
		Version: "2023-06-01",
		HTTP: &http.Client{
			Timeout: 90 * time.Second,
		},
	}
}

func (c *Client) doJSON(ctx context.Context, path string, in any, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("x-api-key", c.APIKey)
	req.Header.Set("anthropic-version", c.Version)
	req.Header.Set("content-type", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		var raw map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&raw)
		return fmt.Errorf("anthropic status=%d body=%v", resp.StatusCode, raw)
	}

	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) CountTokens(ctx context.Context, req runtime.MessageRequest) (int, error) {
	payload := MapToAnthropicCountTokens(req)
	var out CountTokensResponse
	if err := c.doJSON(ctx, "/v1/messages/count_tokens", payload, &out); err != nil {
		return 0, err
	}
	return out.InputTokens, nil
}

func (c *Client) CreateMessage(ctx context.Context, req runtime.MessageRequest) (*runtime.MessageResponse, error) {
	payload := MapToAnthropicMessages(req)
	var out MessagesResponse
	if err := c.doJSON(ctx, "/v1/messages", payload, &out); err != nil {
		return nil, err
	}
	resp := MapFromAnthropicMessage(out)
	return &resp, nil
}
```

---

## 5. `internal/tools/local/read_file.go`

这个工具一定要有路径边界。

```go
package local

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"yourmod/internal/runtime"
)

type ReadFileTool struct {
	WorkspaceRoot string
}

func (t *ReadFileTool) Name() string { return "read_file" }

func (t *ReadFileTool) Run(ctx context.Context, input map[string]any) (runtime.ToolRawResult, error) {
	path, ok := input["path"].(string)
	if !ok || path == "" {
		return runtime.ToolRawResult{}, fmt.Errorf("missing path")
	}

	start := time.Now()
	fullPath, err := t.safePath(path)
	if err != nil {
		return runtime.ToolRawResult{}, err
	}

	data, err := os.ReadFile(fullPath)
	end := time.Now()

	res := runtime.ToolRawResult{
		ToolName:   "read_file",
		Files:      []string{fullPath},
		Stdout:     data,
		ExitCode:   0,
		Metadata:   map[string]any{"path": fullPath},
		StartedAt:  start,
		FinishedAt: end,
	}
	if err != nil {
		res.ExitCode = 1
		res.Stderr = []byte(err.Error())
	}
	return res, nil
}

func (t *ReadFileTool) safePath(p string) (string, error) {
	root := t.WorkspaceRoot
	if root == "" {
		root = "."
	}
	full := filepath.Clean(filepath.Join(root, p))
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	absFull, err := filepath.Abs(full)
	if err != nil {
		return "", err
	}
	if len(absFull) < len(absRoot) || absFull[:len(absRoot)] != absRoot {
		return "", fmt.Errorf("path escapes workspace root")
	}
	return absFull, nil
}
```

---

## 6. `internal/tools/local/grep_repo.go`

第一版可以直接用 Go 遍历文件，不必依赖外部 `grep`。

```go
package local

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"yourmod/internal/runtime"
)

type GrepRepoTool struct {
	WorkspaceRoot string
	MaxFiles      int
	MaxMatches    int
}

func (t *GrepRepoTool) Name() string { return "grep_repo" }

func (t *GrepRepoTool) Run(ctx context.Context, input map[string]any) (runtime.ToolRawResult, error) {
	query, ok := input["query"].(string)
	if !ok || query == "" {
		return runtime.ToolRawResult{}, fmt.Errorf("missing query")
	}

	root := t.WorkspaceRoot
	if root == "" {
		root = "."
	}
	maxFiles := t.MaxFiles
	if maxFiles <= 0 {
		maxFiles = 200
	}
	maxMatches := t.MaxMatches
	if maxMatches <= 0 {
		maxMatches = 100
	}

	start := time.Now()
	var buf bytes.Buffer
	filesSeen := 0
	matches := 0

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil {
			return nil
		}
		if info.IsDir() {
			name := info.Name()
			if name == ".git" || name == "node_modules" || name == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if filesSeen >= maxFiles || matches >= maxMatches {
			return filepath.SkipAll
		}

		filesSeen++
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}

		lines := strings.Split(string(data), "\n")
		for i, line := range lines {
			if strings.Contains(line, query) {
				matches++
				fmt.Fprintf(&buf, "%s:%d:%s\n", path, i+1, line)
				if matches >= maxMatches {
					return filepath.SkipAll
				}
			}
		}
		return nil
	})
	end := time.Now()

	res := runtime.ToolRawResult{
		ToolName:   "grep_repo",
		Stdout:     buf.Bytes(),
		ExitCode:   0,
		Metadata:   map[string]any{"query": query, "files_seen": filesSeen, "matches": matches},
		StartedAt:  start,
		FinishedAt: end,
	}
	if err != nil && err != filepath.SkipAll {
		res.ExitCode = 1
		res.Stderr = []byte(err.Error())
	}
	return res, nil
}
```

---

## 7. 给 `MessageRepo` 补两个必要方法

你前面 API 和 SessionService 已经要用到它们。

```go
package sqlite

import (
	"context"
	"encoding/json"
	"time"

	"yourmod/internal/runtime"
)

func (r *MessageRepo) InsertUserText(ctx context.Context, sessionID, text string) error {
	return r.insertMessage(ctx, sessionID, "user", []runtime.ContentBlock{
		{Type: "text", Text: text},
	})
}

func (r *MessageRepo) GetLatestAssistantText(ctx context.Context, sessionID string) (string, error) {
	row := r.DB.QueryRowContext(ctx, `
		SELECT content_json
		FROM messages
		WHERE session_id = ? AND role = 'assistant'
		ORDER BY created_at DESC
		LIMIT 1
	`, sessionID)

	var contentJSON string
	if err := row.Scan(&contentJSON); err != nil {
		return "", err
	}

	var blocks []runtime.ContentBlock
	if err := json.Unmarshal([]byte(contentJSON), &blocks); err != nil {
		return "", err
	}
	for _, b := range blocks {
		if b.Type == "text" {
			return b.Text, nil
		}
	}
	return "", nil
}
```

---

## 8. 给 `SessionRepo` 补一个 `CreateAndReturn`

前面 `ChildSessionFactory` 用到了。

```go
package sqlite

import (
	"context"

	"yourmod/internal/runtime"
)

func (r *SessionRepo) CreateAndReturn(ctx context.Context, s *runtime.Session, rootTask *runtime.Task) (*runtime.Session, error) {
	if err := r.CreateSession(ctx, s, rootTask); err != nil {
		return nil, err
	}
	return s, nil
}
```

---

## 9. 当前最小运行链路

把前面几批文件拼起来，P0 的主链就是：

```text
POST /sessions
 -> 创建 session + root task

POST /sessions/{id}/messages
 -> 插入 user message
 -> Runner.RunUntilIdle()
    -> ContextManager.BuildRequest()
    -> CountTokens()
    -> CreateMessage()
    -> 若 tool_use:
         ToolBroker.Exec()
         ArtifactStore.PutRaw()
         Reducer.Reduce()
         ContextManager.AdmitToolEnvelope()
         再下一轮
    -> 若 final text:
         保存 assistant 消息
```

---

## 10. 现在可以直接测的场景

### 场景 A：纯问答

请求：

```json
POST /sessions
{"user_id":"u1","model":"claude-sonnet-4-5","goal":"Answer questions about this repo"}

POST /sessions/{id}/messages
{"content":"先告诉我你会如何分析最近一周 auth 模块的改动"}
```

验收：

* 能创建 session
* 能返回 assistant 文本
* 消息落库

### 场景 B：触发 `git_log`

在 system/task 里暗示工具可用，要求模型先检查最近 7 天提交。

验收：

* 模型发 `tool_use`
* `git_log` 执行
* artifact 入库
* reducer 生效
* assistant 最终给出摘要

### 场景 C：触发 `grep_repo`

请求：
“帮我搜索 auth token 校验逻辑在哪里”

验收：

* `grep_repo` 执行
* 匹配结果不直接全进上下文
* 由 envelope 进入上下文

---

## 11. P0 剩余缺口

跑起来前你还差这些小块：

### 1) `extractTextFromJSON`

给 `LatestAssistantReader` 用，直接复用 `MessageRepo.GetLatestAssistantText()` 更简单，建议删掉独立函数。

### 2) `AllowAllReadOnlyPolicy`

现在返回 `os.ErrPermission` 太粗糙，建议改成明确错误：

```go
return fmt.Errorf("tool %s is not allowed", call.Name)
```

### 3) `ReadFileTool` 和 `GrepRepoTool` 的 `WorkspaceRoot`

在 `main.go` 初始化时传入项目根目录。

### 4) `runSchema`

建议支持幂等执行，`schema.sql` 已经基本满足。

---

## 12. P1 下一步最值得补的 4 个文件

等这批跑通后，下一阶段最值得写的是：

1. `contextmgr/compact.go`
2. `tools/reducers/go_test_json.go`
3. `tools/reducers/playwright_snapshot.go`
4. `tools/mcp/client.go`

因为这 4 个会真正把你的系统从“能跑”推进到“能长时间稳定跑”。

---

## 13. 你现在最该做的事

按顺序：

1. 把我前后几条里的代码骨架整理成真实文件
2. 先把 `main.go + schema.sql + client.go + runner.go` 编译通过
3. 打通 `POST /sessions` 和 `POST /sessions/{id}/messages`
4. 先只测 `git_log`
5. 确认 artifact 文件和 SQLite 数据都在落盘
6. 再加 `grep_repo` 和 `read_file`

下一条我可以继续直接给你：
**`compact.go`、`go_test_json_reducer.go`、`playwright_snapshot_reducer.go`、`tools/mcp/client.go`、`tool_catalog` 检索实现**。
