继续。下面我直接补你下一批最关键的实现骨架：

* `tools/local/run_command_readonly.go`
* `tools/mcp/gateway.go`
* `store/sqlite/tool_catalog.go`
* `contextmgr/compact.go` 增强版
* `contextmgr/manager.go` 如何接 recall / compact / tool search
* 最后一版 P1/P2 落地顺序

这批补完后，你的系统就基本具备：

* 受限命令执行
* MCP host/gateway 雏形
* 本地 tool catalog 检索
* 真正可工作的 compaction
* BuildRequest 中的动态召回与按需工具加载

---

## 1. `tools/local/run_command_readonly.go`

这个工具要非常克制。第一版不要做通用 shell，直接做**白名单命令**。

```go id="8ttj3h"
package local

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"yourmod/internal/runtime"
)

type RunCommandReadonlyTool struct {
	WorkspaceRoot string
	Allowed       map[string]bool
	Timeout       time.Duration
}

func (t *RunCommandReadonlyTool) Name() string { return "run_command_readonly" }

func (t *RunCommandReadonlyTool) Run(ctx context.Context, input map[string]any) (runtime.ToolRawResult, error) {
	cmdName, ok := input["cmd"].(string)
	if !ok || cmdName == "" {
		return runtime.ToolRawResult{}, fmt.Errorf("missing cmd")
	}

	if !t.Allowed[cmdName] {
		return runtime.ToolRawResult{}, fmt.Errorf("command not allowed: %s", cmdName)
	}

	var args []string
	if rawArgs, ok := input["args"].([]any); ok {
		for _, a := range rawArgs {
			if s, ok := a.(string); ok {
				args = append(args, s)
			}
		}
	}

	runCtx := ctx
	var cancel context.CancelFunc
	if t.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, t.Timeout)
		defer cancel()
	}

	start := time.Now()
	cmd := exec.CommandContext(runCtx, cmdName, args...)
	if t.WorkspaceRoot != "" {
		cmd.Dir = t.WorkspaceRoot
	}

	out, err := cmd.CombinedOutput()
	end := time.Now()

	res := runtime.ToolRawResult{
		ToolName:  "run_command_readonly",
		Stdout:    out,
		ExitCode:  0,
		Metadata: map[string]any{
			"cmd":  cmdName,
			"args": args,
			"dir":  safeDir(t.WorkspaceRoot),
		},
		StartedAt:  start,
		FinishedAt: end,
	}

	if err != nil {
		res.ExitCode = 1
		res.Stderr = []byte(err.Error())
	}
	return res, nil
}

func safeDir(dir string) string {
	if dir == "" {
		return "."
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return dir
	}
	return abs
}

// 推荐初始化：
// Allowed: map[string]bool{
//   "go": true,
//   "git": true,
//   "npm": true,
//   "pnpm": true,
//   "pytest": true,
// }
func DefaultReadonlyCommands() map[string]bool {
	return map[string]bool{
		"go":     true,
		"git":    true,
		"pytest": true,
		"npm":    true,
		"pnpm":   true,
		"python": true,
	}
}

func sanitizeArgs(args []string) []string {
	out := make([]string, 0, len(args))
	for _, a := range args {
		a = strings.TrimSpace(a)
		if a != "" {
			out = append(out, a)
		}
	}
	return out
}
```

### 第一版限制建议

只允许：

* `go test`
* `git log`
* `git show`
* `pytest`
* `npm test`

不要允许：

* `sh`
* `bash`
* `zsh`
* `rm`
* `mv`
* `curl`
* `wget`

---

## 2. `tools/mcp/gateway.go`

这个文件把 MCP client 和 runtime 之间隔离开。
目标：runtime 只知道“列工具 / 调工具 / 列资源”，不知道 transport 和 JSON-RPC 细节。

```go id="5vuzc8"
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"yourmod/internal/runtime"
)

type ClientFactory interface {
	GetClient(ctx context.Context, serverName string) (*Client, error)
}

type CatalogWriter interface {
	UpsertTool(ctx context.Context, source, serverName string, t runtime.ToolDef, tags []string) error
	Search(ctx context.Context, query string, limit int) ([]runtime.ToolDef, error)
}

type Gateway struct {
	Clients ClientFactory
	Catalog CatalogWriter
}

func (g *Gateway) Initialize(ctx context.Context, serverName string) error {
	client, err := g.Clients.GetClient(ctx, serverName)
	if err != nil {
		return err
	}

	if _, err := client.Initialize(ctx); err != nil {
		return err
	}

	tools, err := client.ListTools(ctx)
	if err != nil {
		return err
	}

	for _, t := range tools {
		err := g.Catalog.UpsertTool(ctx, "mcp", serverName, runtime.ToolDef{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.InputSchema,
		}, []string{"mcp", serverName})
		if err != nil {
			return err
		}
	}

	return nil
}

func (g *Gateway) ListTools(ctx context.Context, serverName string) ([]runtime.ToolDef, error) {
	client, err := g.Clients.GetClient(ctx, serverName)
	if err != nil {
		return nil, err
	}

	tools, err := client.ListTools(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]runtime.ToolDef, 0, len(tools))
	for _, t := range tools {
		out = append(out, runtime.ToolDef{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.InputSchema,
		})
	}
	return out, nil
}

func (g *Gateway) CallTool(ctx context.Context, serverName, toolName string, input map[string]any) (runtime.ToolRawResult, error) {
	client, err := g.Clients.GetClient(ctx, serverName)
	if err != nil {
		return runtime.ToolRawResult{}, err
	}

	res, err := client.CallTool(ctx, toolName, input)
	if err != nil {
		return runtime.ToolRawResult{}, err
	}

	var parts []string
	for _, item := range res.Content {
		switch item["type"] {
		case "text":
			if txt, ok := item["text"].(string); ok {
				parts = append(parts, txt)
			}
		default:
			b, _ := json.Marshal(item)
			parts = append(parts, string(b))
		}
	}

	return runtime.ToolRawResult{
		ToolName: "mcp:" + serverName + ":" + toolName,
		Stdout:   []byte(strings.Join(parts, "\n")),
		ExitCode: boolToExitCode(res.IsError),
		Metadata: map[string]any{
			"server": serverName,
			"tool":   toolName,
		},
	}, nil
}

func (g *Gateway) ListResources(ctx context.Context, serverName string) ([]string, error) {
	// P1 先留空；P2 再补 resources/list
	return nil, nil
}

func (g *Gateway) SearchTools(ctx context.Context, query string, limit int) ([]runtime.ToolDef, error) {
	if g.Catalog == nil {
		return nil, fmt.Errorf("catalog not configured")
	}
	return g.Catalog.Search(ctx, query, limit)
}

func boolToExitCode(isError bool) int {
	if isError {
		return 1
	}
	return 0
}
```

### 这里最重要的点

MCP 工具结果依然返回 `ToolRawResult`，所以后面仍然会走：

`artifact -> reducer -> envelope -> context`

这保证了 MCP 不会绕过你的 Context Mode。

---

## 3. `store/sqlite/tool_catalog.go`

先给 catalog 做一个真正的存取层。P1 先用 `LIKE` 检索，够用了。

```go id="5kgkkz"
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"yourmod/internal/runtime"
)

type ToolCatalogRepo struct {
	DB *sql.DB
}

func (r *ToolCatalogRepo) UpsertTool(ctx context.Context, source, serverName string, t runtime.ToolDef, tags []string) error {
	schemaJSON, _ := json.Marshal(t.InputSchema)
	argNamesJSON, _ := json.Marshal(extractArgNames(t.InputSchema))
	tagsJSON, _ := json.Marshal(tags)

	_, err := r.DB.ExecContext(ctx, `
		INSERT INTO tool_catalog (id, source, server_name, tool_name, description, input_schema_json, arg_names_json, tags_json, enabled, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1, ?)
		ON CONFLICT(id) DO UPDATE SET
			description=excluded.description,
			input_schema_json=excluded.input_schema_json,
			arg_names_json=excluded.arg_names_json,
			tags_json=excluded.tags_json,
			enabled=1,
			updated_at=excluded.updated_at
	`, toolCatalogID(source, serverName, t.Name), source, serverName, t.Name, t.Description, string(schemaJSON), string(argNamesJSON), string(tagsJSON), time.Now().Format(time.RFC3339))
	return err
}

func (r *ToolCatalogRepo) Search(ctx context.Context, query string, limit int) ([]runtime.ToolDef, error) {
	if limit <= 0 {
		limit = 5
	}

	rows, err := r.DB.QueryContext(ctx, `
		SELECT tool_name, description, input_schema_json
		FROM tool_catalog
		WHERE enabled = 1
		  AND (
		    tool_name LIKE ?
		    OR description LIKE ?
		    OR arg_names_json LIKE ?
		    OR tags_json LIKE ?
		  )
		ORDER BY updated_at DESC
		LIMIT ?
	`, "%"+query+"%", "%"+query+"%", "%"+query+"%", "%"+query+"%", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []runtime.ToolDef
	for rows.Next() {
		var name, desc, schemaJSON string
		if err := rows.Scan(&name, &desc, &schemaJSON); err != nil {
			return nil, err
		}
		var schema map[string]any
		_ = json.Unmarshal([]byte(schemaJSON), &schema)

		out = append(out, runtime.ToolDef{
			Name:        name,
			Description: desc,
			InputSchema: schema,
		})
	}
	return out, rows.Err()
}

func extractArgNames(schema map[string]any) []string {
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(props))
	for k := range props {
		out = append(out, k)
	}
	return out
}

func toolCatalogID(source, serverName, name string) string {
	return source + "::" + serverName + "::" + name
}
```

---

## 4. `contextmgr/compact.go` 增强版

上一个版本已经能压旧消息，这一版再加两个点：

* 保留最近消息不压
* 把 old envelope / assistant text 更细地拆成 ledger entry

```go id="shsz3v"
package contextmgr

import (
	"context"
	"strings"

	"yourmod/internal/runtime"
)

type CompactingStore interface {
	LoadAllMessages(ctx context.Context, sessionID string) ([]runtime.Message, error)
	LoadSessionTask(ctx context.Context, sessionID string) (*runtime.Task, error)
	InsertMemoryEntry(
		ctx context.Context,
		sessionID string,
		taskID string,
		kind string,
		priority int,
		content map[string]any,
		sourceRefs []string,
	) error
}

type CompactService struct {
	Store CompactingStore
	KeepRecent int
}

func (c *CompactService) Compact(ctx context.Context, s *runtime.Session, reason string) error {
	task, err := c.Store.LoadSessionTask(ctx, s.ID)
	if err != nil {
		return err
	}

	msgs, err := c.Store.LoadAllMessages(ctx, s.ID)
	if err != nil {
		return err
	}

	keep := c.KeepRecent
	if keep <= 0 {
		keep = 4
	}
	if len(msgs) <= keep {
		return nil
	}

	old := msgs[:len(msgs)-keep]

	for _, msg := range old {
		text := flattenMessage(msg)
		if text == "" {
			continue
		}

		entries := deriveEntries(text, reason)
		for _, e := range entries {
			_ = c.Store.InsertMemoryEntry(ctx, s.ID, task.ID, e.Kind, e.Priority, e.Content, nil)
		}
	}

	return nil
}

type derivedEntry struct {
	Kind     string
	Priority int
	Content  map[string]any
}

func deriveEntries(text, reason string) []derivedEntry {
	var out []derivedEntry
	lower := strings.ToLower(text)

	if looksLikeDecision(lower) {
		out = append(out, derivedEntry{
			Kind:     "decision",
			Priority: 90,
			Content: map[string]any{
				"summary": trimText(text, 400),
				"reason":  reason,
			},
		})
	}
	if looksLikePlan(lower) {
		out = append(out, derivedEntry{
			Kind:     "plan",
			Priority: 80,
			Content: map[string]any{
				"summary": trimText(text, 400),
				"reason":  reason,
			},
		})
	}
	if looksLikeOpenQuestion(lower) {
		out = append(out, derivedEntry{
			Kind:     "open_question",
			Priority: 70,
			Content: map[string]any{
				"summary": trimText(text, 300),
				"reason":  reason,
			},
		})
	}
	if looksLikeFailure(lower) {
		out = append(out, derivedEntry{
			Kind:     "failure",
			Priority: 85,
			Content: map[string]any{
				"summary": trimText(text, 400),
				"reason":  reason,
			},
		})
	}
	if len(out) == 0 {
		out = append(out, derivedEntry{
			Kind:     "fact",
			Priority: 60,
			Content: map[string]any{
				"summary": trimText(text, 300),
				"reason":  reason,
			},
		})
	}
	return out
}
```

---

## 5. 把 recall / compact / tool search 接进 `contextmgr/manager.go`

现在把 `BuildRequest()` 从“静态装配”变成“动态装配”。

### 新增依赖

* `RecallManager`
* `ToolSearch`
* `CompactNeeded` 判断

```go id="mjlwmn"
package contextmgr

import (
	"context"
	"strings"

	"yourmod/internal/runtime"
)

type ToolSearcher interface {
	Search(ctx context.Context, query string, limit int) ([]runtime.ToolDef, error)
}

type Manager struct {
	Store   Store
	Budget  Budget
	Tools   func(*runtime.Session) []runtime.ToolDef
	Recall  *RecallManager
	Search  ToolSearcher
}

func (m *Manager) BuildRequest(ctx context.Context, s *runtime.Session) (runtime.MessageRequest, error) {
	task, err := m.Store.LoadSessionTask(ctx, s.ID)
	if err != nil {
		return runtime.MessageRequest{}, err
	}

	req := runtime.MessageRequest{
		Model:     s.Model,
		System:    buildSystemPrompt(task),
		MaxTokens: 4096,
		Metadata:  map[string]string{"session_id": s.ID},
	}

	req.Messages = append(req.Messages, buildTaskMessage(task))

	recent, err := m.Store.LoadRecentMessages(ctx, s.ID, 4)
	if err != nil {
		return runtime.MessageRequest{}, err
	}
	req.Messages = append(req.Messages, recent...)

	ledger, err := m.Store.LoadMemoryEntries(ctx, s.ID, []string{"decision", "fact", "open_question", "plan", "failure"}, 12)
	if err != nil {
		return runtime.MessageRequest{}, err
	}
	req.Messages = append(req.Messages, ledger...)

	evidence, err := m.Store.LoadRecentEvidence(ctx, s.ID, 6)
	if err != nil {
		return runtime.MessageRequest{}, err
	}
	req.Messages = append(req.Messages, evidence...)

	// 根据 task goal 做按需 recall
	if m.Recall != nil && shouldRecall(task.Goal) {
		recallMsgs, err := m.Recall.RecallAsMessages(ctx, runtime.RecallQuery{
			SessionID: s.ID,
			Query:     task.Goal,
			K:         3,
		})
		if err == nil {
			req.Messages = append(req.Messages, recallMsgs...)
		}
	}

	// 工具加载策略：
	// 1. 如果配置了 Search，则优先按 task goal 搜索少量工具
	// 2. 否则回退到固定 tools
	if m.Search != nil {
		tools, err := m.Search.Search(ctx, task.Goal, 5)
		if err == nil && len(tools) > 0 {
			req.Tools = tools
		}
	}
	if len(req.Tools) == 0 && m.Tools != nil {
		req.Tools = m.Tools(s)
	}

	return req, nil
}

func shouldRecall(goal string) bool {
	g := strings.ToLower(goal)
	return strings.Contains(g, "evidence") ||
		strings.Contains(g, "stack trace") ||
		strings.Contains(g, "log") ||
		strings.Contains(g, "error") ||
		strings.Contains(g, "commit")
}
```

### 这个版本的提升

* 不是所有轮次都 recall
* 不是所有工具都注入
* `task goal` 成了 recall 和 tool search 的驱动输入

---

## 6. `run_command_readonly` 怎么接两个新 reducer

在 `main.go` 初始化 reducer 时，把这两个新 reducer 接进去：

```go id="xq6qeo"
reducerChain := reducers.NewChain(
	&reducers.GitLogReducer{},
	&reducers.GoTestJSONReducer{},
	&reducers.PlaywrightSnapshotReducer{},
)
```

然后在 `main.go` 初始化工具时加入：

```go id="5ffwd3"
&local.RunCommandReadonlyTool{
	WorkspaceRoot: ".",
	Allowed:       local.DefaultReadonlyCommands(),
	Timeout:       30 * time.Second,
},
```

同时给 tool defs 加上：

```go id="fnejbt"
{
	Name:        "run_command_readonly",
	Description: "Run approved readonly commands like go test, git, pytest",
	InputSchema: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"cmd": map[string]any{"type": "string"},
			"args": map[string]any{
				"type": "array",
				"items": map[string]any{"type": "string"},
			},
		},
		"required": []string{"cmd"},
	},
},
```

---

## 7. MCP 的下一步真正要补什么

你现在有了：

* `mcp/client.go`
* `mcp/gateway.go`
* `tool_catalog.go`

下一步只差一个 transport 实现。
P1 最稳的是先做 **stdio transport**，因为本地 MCP server 最容易验证。

建议下一个文件：

* `tools/mcp/transport_stdio.go`

它要做的事情只有：

* 启动本地 server 进程
* 向 stdin 写 JSON-RPC request
* 从 stdout 读 JSON-RPC response
* 逐条 dispatch 给 `Client`

这一步做好，你就能连本地 MCP server 了。

---

## 8. P1 最小联调清单

现在最稳的顺序是：

### A. 打通 readonly command

测：

* `run_command_readonly` 跑 `go test -json ./...`
* raw output 落 artifact
* `GoTestJSONReducer` 生效

### B. 接 Playwright reducer

测：

* 用模拟 Playwright 输出文本
* reducer 提取 console errors / failed requests / url / title

### C. 开启真正 compact

测：

* 手工塞 10+ 轮消息
* `Compact()` 后 `memory_entries` 增加
* 新的 `BuildRequest()` 能读到 ledger

### D. 把 recall 接到 BuildRequest

测：

* 先落一条长日志 artifact
* 再发 “请给我 stack trace”
* 能从 FTS5 把 snippet 拉回

### E. 接 `tool_catalog` 搜索

测：

* 插几条本地工具和 mcp 工具
* 用 task goal 搜 top 3~5 个工具
* `BuildRequest()` 动态只注入少量工具

---

## 9. 到这里系统已经具备的能力

补完这一批后，你的系统已经不是“聊天机器人 + 几个工具”了，而是有了这些真正的 agent runtime 能力：

* **工具输出外置**
* **Context compaction**
* **Artifact recall**
* **按需工具加载**
* **MCP host/gateway 形态**
* **子代理协议化**

也就是你一开始想要的那条主线：

**主 agent 指挥 subagent，context window 不再靠人工控 token，而是靠系统层的 Context OS 管理。**

---

## 10. 现在最值得继续写的文件

按优先级：

1. `tools/local/run_command_readonly.go`
2. `tools/reducers/go_test_json.go`
3. `contextmgr/compact.go`
4. `store/sqlite/tool_catalog.go`
5. `tools/mcp/gateway.go`

这 5 个补完，P1 就基本能落地。

下一条我可以继续直接给你：
**`tools/mcp/transport_stdio.go`、`tools/mcp/client_factory.go`、`store/sqlite/checkpoints.go`、`contextmgr/checkpoint.go`、以及 `tool_catalog` 的 FTS5 版实现**。
