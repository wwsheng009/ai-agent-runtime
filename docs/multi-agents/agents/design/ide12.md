下面继续补 **P1 最关键的 5 个实现**，它们会把你的系统从“能跑”推进到“能稳定处理长任务”。

这次补的是：

* `contextmgr/compact.go`
* `tools/reducers/go_test_json.go`
* `tools/reducers/playwright_snapshot.go`
* `tools/mcp/client.go`
* `tools/mcp/catalog.go`

最后我会再给你一版 **P1 联调顺序**。

---

## 1. `contextmgr/compact.go`

这一版先做**确定性 compaction**，不额外调用 LLM。目标不是“总结得很文艺”，而是把旧上下文压成结构化 ledger。

### 设计原则

Anthropic 对长任务的公开建议很一致：旧历史、旧工具调用、旧工具结果应该优先被清掉；关键信息应该沉淀成更紧凑的结构化状态，比如计划、已确认事实、开放问题和失败路径。([anthropic.com](https://www.anthropic.com/engineering/effective-context-engineering-for-ai-agents))

### 做法

* 读取旧消息，但保留最近 2~4 轮不动
* 从旧 assistant 消息里抽 `decision` / `plan`
* 从旧 envelope 或日志摘要里抽 `fact`
* 从错误和失败里抽 `failure`
* 从“不确定/待验证”句式里抽 `open_question`
* 写入 `memory_entries`
* 更新 `checkpoints`

```go id="m7gmo8"
package contextmgr

import (
	"context"
	"strings"
	"yourmod/internal/runtime"
)

type CompactStore interface {
	LoadAllMessages(ctx context.Context, sessionID string) ([]runtime.Message, error)
	InsertMemoryEntry(
		ctx context.Context,
		sessionID string,
		taskID string,
		kind string,
		priority int,
		content map[string]any,
		sourceRefs []string,
	) error
	LoadSessionTask(ctx context.Context, sessionID string) (*runtime.Task, error)
}

type CompactManager struct {
	Store CompactStore
}

func (c *CompactManager) Compact(ctx context.Context, s *runtime.Session, reason string) error {
	task, err := c.Store.LoadSessionTask(ctx, s.ID)
	if err != nil {
		return err
	}

	msgs, err := c.Store.LoadAllMessages(ctx, s.ID)
	if err != nil {
		return err
	}
	if len(msgs) <= 4 {
		return nil
	}

	// 保留最近 4 条不做摘要
	candidates := msgs[:max(0, len(msgs)-4)]

	for _, msg := range candidates {
		text := flattenMessage(msg)
		if text == "" {
			continue
		}

		lower := strings.ToLower(text)

		switch {
		case looksLikeDecision(lower):
			_ = c.Store.InsertMemoryEntry(ctx, s.ID, task.ID, "decision", 90, map[string]any{
				"summary": trimText(text, 400),
				"reason":  reason,
			}, nil)

		case looksLikePlan(lower):
			_ = c.Store.InsertMemoryEntry(ctx, s.ID, task.ID, "plan", 80, map[string]any{
				"summary": trimText(text, 400),
				"reason":  reason,
			}, nil)

		case looksLikeOpenQuestion(lower):
			_ = c.Store.InsertMemoryEntry(ctx, s.ID, task.ID, "open_question", 70, map[string]any{
				"summary": trimText(text, 300),
				"reason":  reason,
			}, nil)

		case looksLikeFailure(lower):
			_ = c.Store.InsertMemoryEntry(ctx, s.ID, task.ID, "failure", 85, map[string]any{
				"summary": trimText(text, 400),
				"reason":  reason,
			}, nil)

		default:
			_ = c.Store.InsertMemoryEntry(ctx, s.ID, task.ID, "fact", 60, map[string]any{
				"summary": trimText(text, 300),
				"reason":  reason,
			}, nil)
		}
	}

	return nil
}

func flattenMessage(msg runtime.Message) string {
	var parts []string
	for _, b := range msg.Content {
		if b.Type == "text" && b.Text != "" {
			parts = append(parts, b.Text)
		}
		if b.Type == "tool_result" {
			if s, ok := b.Result.(string); ok && s != "" {
				parts = append(parts, s)
			}
		}
	}
	return strings.Join(parts, "\n")
}

func looksLikeDecision(s string) bool {
	return strings.Contains(s, "decision:") ||
		strings.Contains(s, "conclusion:") ||
		strings.Contains(s, "most likely") ||
		strings.Contains(s, "we should")
}

func looksLikePlan(s string) bool {
	return strings.Contains(s, "plan:") ||
		strings.Contains(s, "next steps") ||
		strings.Contains(s, "step 1") ||
		strings.Contains(s, "i will")
}

func looksLikeOpenQuestion(s string) bool {
	return strings.Contains(s, "unknown") ||
		strings.Contains(s, "unclear") ||
		strings.Contains(s, "need to verify") ||
		strings.Contains(s, "not sure")
}

func looksLikeFailure(s string) bool {
	return strings.Contains(s, "failed") ||
		strings.Contains(s, "error") ||
		strings.Contains(s, "denied") ||
		strings.Contains(s, "panic")
}

func trimText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
```

### 这个版本的价值

* 先让 compaction 流程存在
* 先让旧历史从 transcript 迁移到 ledger
* 后面可以再把规则升级成更细的结构抽取

---

## 2. `tools/reducers/go_test_json.go`

`go test -json` 非常容易产生大量输出，这个 reducer 很值钱。

### 目标抽取

* 失败 package
* 失败测试名
* panic / stack 入口
* 首个高价值错误
* 重现命令

```go id="9f5unc"
package reducers

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"yourmod/internal/runtime"
)

type GoTestJSONReducer struct{}

func (r *GoTestJSONReducer) Name() string { return "go_test_json_reducer" }
func (r *GoTestJSONReducer) Match(raw runtime.ToolRawResult) bool {
	return raw.ToolName == "run_command_readonly" && looksLikeGoTestJSON(raw)
}

func looksLikeGoTestJSON(raw runtime.ToolRawResult) bool {
	s := string(raw.Stdout)
	return strings.Contains(s, `"Action":"fail"`) || strings.Contains(s, `"Action":"output"`)
}

type goTestEvent struct {
	Time    string `json:"Time"`
	Action  string `json:"Action"`
	Package string `json:"Package"`
	Test    string `json:"Test"`
	Output  string `json:"Output"`
}

func (r *GoTestJSONReducer) Reduce(ctx context.Context, raw runtime.ToolRawResult, refs []runtime.ArtifactRef) (runtime.ToolEnvelope, error) {
	sc := bufio.NewScanner(strings.NewReader(string(raw.Stdout)))

	failPkgs := map[string]bool{}
	failTests := map[string]bool{}
	var firstError string
	var firstStack string

	for sc.Scan() {
		line := sc.Text()
		var ev goTestEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}

		switch ev.Action {
		case "fail":
			if ev.Package != "" {
				failPkgs[ev.Package] = true
			}
			if ev.Test != "" {
				failTests[ev.Test] = true
			}
		case "output":
			if firstError == "" && looksLikeErrorLine(ev.Output) {
				firstError = strings.TrimSpace(ev.Output)
			}
			if firstStack == "" && looksLikeStackLine(ev.Output) {
				firstStack = strings.TrimSpace(ev.Output)
			}
		}
	}

	keyFacts := []string{}
	for p := range failPkgs {
		keyFacts = append(keyFacts, "failed_package="+p)
	}
	for t := range failTests {
		keyFacts = append(keyFacts, "failed_test="+t)
	}

	if firstError != "" {
		keyFacts = append(keyFacts, "first_error="+truncateInline(firstError, 200))
	}
	if firstStack != "" {
		keyFacts = append(keyFacts, "stack_hint="+truncateInline(firstStack, 200))
	}

	summary := fmt.Sprintf("Parsed go test JSON: %d failed packages, %d failed tests", len(failPkgs), len(failTests))

	return runtime.ToolEnvelope{
		Summary:      summary,
		KeyFacts:     keyFacts,
		ArtifactRefs: refs,
		RecallHints: []string{
			"first failing package",
			"first stack trace",
			"panic output",
			"test failure lines",
		},
		SuggestedNext: []string{
			"Inspect first failing package",
			"Retrieve first stack trace snippet",
		},
		IsError: raw.ExitCode != 0 || len(failPkgs) > 0,
	}, nil
}

func looksLikeErrorLine(s string) bool {
	l := strings.ToLower(s)
	return strings.Contains(l, "error") || strings.Contains(l, "panic") || strings.Contains(l, "fail")
}

func looksLikeStackLine(s string) bool {
	return strings.Contains(s, ".go:") || strings.Contains(s, "goroutine")
}

func truncateInline(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n]
}
```

---

## 3. `tools/reducers/playwright_snapshot.go`

Playwright 快照和调试输出通常是上下文杀手，必须 reducer。

### 目标抽取

* URL
* title
* visible text top-N
* console error
* failed requests
* screenshot ref

```go id="8yhsz3"
package reducers

import (
	"context"
	"fmt"
	"strings"
	"yourmod/internal/runtime"
)

type PlaywrightSnapshotReducer struct{}

func (r *PlaywrightSnapshotReducer) Name() string { return "playwright_snapshot_reducer" }
func (r *PlaywrightSnapshotReducer) Match(raw runtime.ToolRawResult) bool {
	if raw.ToolName != "run_command_readonly" {
		return false
	}
	s := strings.ToLower(string(raw.Stdout) + "\n" + string(raw.Stderr))
	return strings.Contains(s, "playwright") || strings.Contains(s, "console error") || strings.Contains(s, "failed request")
}

func (r *PlaywrightSnapshotReducer) Reduce(ctx context.Context, raw runtime.ToolRawResult, refs []runtime.ArtifactRef) (runtime.ToolEnvelope, error) {
	text := string(raw.Stdout) + "\n" + string(raw.Stderr)
	lines := strings.Split(text, "\n")

	var url, title string
	var consoleErrors []string
	var failedRequests []string
	var visibleText []string

	for _, ln := range lines {
		l := strings.TrimSpace(ln)
		ll := strings.ToLower(l)

		switch {
		case strings.HasPrefix(ll, "url:"):
			if url == "" {
				url = strings.TrimSpace(l[4:])
			}
		case strings.HasPrefix(ll, "title:"):
			if title == "" {
				title = strings.TrimSpace(l[6:])
			}
		case strings.Contains(ll, "console error"):
			if len(consoleErrors) < 5 {
				consoleErrors = append(consoleErrors, l)
			}
		case strings.Contains(ll, "failed request"):
			if len(failedRequests) < 5 {
				failedRequests = append(failedRequests, l)
			}
		case strings.Contains(ll, "visible text") || strings.Contains(ll, "text:"):
			if len(visibleText) < 5 {
				visibleText = append(visibleText, l)
			}
		}
	}

	keyFacts := []string{}
	if url != "" {
		keyFacts = append(keyFacts, "url="+url)
	}
	if title != "" {
		keyFacts = append(keyFacts, "title="+title)
	}
	for _, e := range consoleErrors {
		keyFacts = append(keyFacts, "console_error="+truncateInline(e, 180))
	}
	for _, r := range failedRequests {
		keyFacts = append(keyFacts, "failed_request="+truncateInline(r, 180))
	}

	summary := fmt.Sprintf("Parsed Playwright output: %d console errors, %d failed requests", len(consoleErrors), len(failedRequests))

	return runtime.ToolEnvelope{
		Summary:      summary,
		KeyFacts:     keyFacts,
		ArtifactRefs: refs,
		RecallHints: []string{
			"console errors",
			"failed requests",
			"visible text",
			"screenshot reference",
		},
		SuggestedNext: []string{
			"Inspect first console error",
			"Inspect failed request details",
		},
		IsError: raw.ExitCode != 0 || len(consoleErrors) > 0 || len(failedRequests) > 0,
	}, nil
}
```

---

## 4. `tools/mcp/client.go`

先做一个**最小 MCP client 骨架**，重点不是立刻跑通所有 server，而是把 host/gateway 的边界固定下来。

MCP 官方架构里，host 负责管理多个 client，每个 client 连接一个 server；协议是有状态 session，初始化必须是第一步。([modelcontextprotocol.io](https://modelcontextprotocol.io/docs/learn/architecture))

### 先定义最小 JSON-RPC 结构

```go id="nkv9e1"
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
)

type JSONRPCRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      any    `json:"id,omitempty"`
	Method  string `json:"method"`
	Params   any   `json:"params,omitempty"`
}

type JSONRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *JSONRPCError   `json:"error,omitempty"`
}

type JSONRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type Transport interface {
	Send(ctx context.Context, req JSONRPCRequest) error
	Recv(ctx context.Context) (JSONRPCResponse, error)
	Close() error
}

type Client struct {
	mu         sync.Mutex
	serverName string
	transport  Transport
	nextID     int64
}

func NewClient(serverName string, transport Transport) *Client {
	return &Client{
		serverName: serverName,
		transport:  transport,
		nextID:     1,
	}
}

func (c *Client) next() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	id := c.nextID
	c.nextID++
	return id
}

func (c *Client) call(ctx context.Context, method string, params any, out any) error {
	id := c.next()

	req := JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  params,
	}
	if err := c.transport.Send(ctx, req); err != nil {
		return err
	}

	resp, err := c.transport.Recv(ctx)
	if err != nil {
		return err
	}
	if resp.Error != nil {
		return fmt.Errorf("mcp error code=%d message=%s", resp.Error.Code, resp.Error.Message)
	}
	if out != nil {
		return json.Unmarshal(resp.Result, out)
	}
	return nil
}

func (c *Client) Close() error {
	if c.transport == nil {
		return nil
	}
	return c.transport.Close()
}

// ---- minimal protocol methods ----

type InitializeParams struct {
	ProtocolVersion string         `json:"protocolVersion"`
	ClientInfo      map[string]any `json:"clientInfo"`
	Capabilities    map[string]any `json:"capabilities"`
}

type InitializeResult struct {
	ProtocolVersion string         `json:"protocolVersion"`
	ServerInfo      map[string]any `json:"serverInfo"`
	Capabilities    map[string]any `json:"capabilities"`
}

func (c *Client) Initialize(ctx context.Context) (*InitializeResult, error) {
	var out InitializeResult
	err := c.call(ctx, "initialize", InitializeParams{
		ProtocolVersion: "2025-03-26",
		ClientInfo: map[string]any{
			"name":    "go-agent-runtime",
			"version": "0.1.0",
		},
		Capabilities: map[string]any{
			"roots": map[string]any{"listChanged": false},
		},
	}, &out)
	if err != nil {
		return nil, err
	}

	// initialized notification
	if err := c.transport.Send(ctx, JSONRPCRequest{
		JSONRPC: "2.0",
		Method:  "notifications/initialized",
	}); err != nil {
		return nil, err
	}

	return &out, nil
}

type ListToolsResult struct {
	Tools []ToolInfo `json:"tools"`
}

type ToolInfo struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

func (c *Client) ListTools(ctx context.Context) ([]ToolInfo, error) {
	var out ListToolsResult
	if err := c.call(ctx, "tools/list", map[string]any{}, &out); err != nil {
		return nil, err
	}
	return out.Tools, nil
}

type CallToolResult struct {
	Content []map[string]any `json:"content"`
	IsError bool             `json:"isError"`
}

func (c *Client) CallTool(ctx context.Context, toolName string, input map[string]any) (*CallToolResult, error) {
	var out CallToolResult
	if err := c.call(ctx, "tools/call", map[string]any{
		"name":      toolName,
		"arguments": input,
	}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// 一个最小的 stdio transport 可以后面再补；这里先留接口
var _ io.Closer
```

### 这里的重点

* Host 层以后只碰 `Client.Initialize/ListTools/CallTool`
* runtime 永远不直接碰 JSON-RPC
* 后续接 stdio 或 Streamable HTTP，只需要实现 `Transport`

---

## 5. `tools/mcp/catalog.go`

这个是 client-side tool search 的基础。

Anthropic 官方文档已经明确把 tool search 定位为：在工具很多时，只把 3–5 个相关工具注入上下文，避免几十上百个工具 schema 先吃光 context。([platform.claude.com](https://platform.claude.com/docs/en/agents-and-tools/tool-use/tool-search-tool))

### Catalog 接口和 FTS5 实现骨架

```go id="2n9aqv"
package mcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
	"yourmod/internal/runtime"
)

type Catalog struct {
	DB *sql.DB
}

func (c *Catalog) UpsertTool(ctx context.Context, source, serverName string, t runtime.ToolDef, tags []string) error {
	schemaJSON, _ := json.Marshal(t.InputSchema)
	argsJSON, _ := json.Marshal(extractArgNames(t.InputSchema))
	tagsJSON, _ := json.Marshal(tags)

	_, err := c.DB.ExecContext(ctx, `
		INSERT INTO tool_catalog (id, source, server_name, tool_name, description, input_schema_json, arg_names_json, tags_json, enabled, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1, ?)
		ON CONFLICT(id) DO UPDATE SET
			description=excluded.description,
			input_schema_json=excluded.input_schema_json,
			arg_names_json=excluded.arg_names_json,
			tags_json=excluded.tags_json,
			updated_at=excluded.updated_at
	`, toolID(source, serverName, t.Name), source, serverName, t.Name, t.Description, string(schemaJSON), string(argsJSON), string(tagsJSON), time.Now().Format(time.RFC3339))
	return err
}

func (c *Catalog) Search(ctx context.Context, query string, limit int) ([]runtime.ToolDef, error) {
	if limit <= 0 {
		limit = 5
	}

	rows, err := c.DB.QueryContext(ctx, `
		SELECT tool_name, description, input_schema_json
		FROM tool_catalog
		WHERE enabled = 1
		  AND (tool_name LIKE ? OR description LIKE ? OR arg_names_json LIKE ? OR tags_json LIKE ?)
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

func toolID(source, serverName, name string) string {
	return source + "::" + serverName + "::" + name
}
```

### 下一步建议

P1 先用 `LIKE` 够了；P2 再给 `tool_catalog` 做 FTS5。

---

## 6. 给 `tool_catalog` 增加 FTS5（可选但推荐）

如果你现在就想一步到位，可以加这两张表：

```sql id="4x3nys"
CREATE VIRTUAL TABLE tool_catalog_fts USING fts5(
  tool_name,
  description,
  arg_names,
  tags
);
```

然后在 `UpsertTool()` 时同步插入。
不过第一阶段其实 `LIKE` 就够用，尤其工具数还不多时。

---

## 7. P1 联调顺序

下面这个顺序最稳，不要跳：

### 第一步：接真正的 `Compact()`

目标：

* 会话超过阈值时，不是崩掉，而是把旧消息转成 ledger
* `memory_entries` 开始有内容

验收：

* 20+ 轮后仍能继续
* `decision/fact/open_question/plan/failure` 表里有数据

### 第二步：接 `go_test_json_reducer`

目标：

* 测试输出不再直冲上下文

验收：

* `go test -json` 原始输出落 artifact
* 上下文里只有失败包/失败测试/stack hint

### 第三步：接 `playwright_snapshot_reducer`

目标：

* DOM / snapshot / console error 不再污染上下文

验收：

* artifact 落盘
* envelope 提供 URL/title/error summary/screenshot refs

### 第四步：把 `RecallAsMessages()` 接进 `BuildRequest()`

目标：

* 当 task 或 plan 明确要求取证据时，可以 page-in

验收：

* 用户追问“给我首个 stack trace”时，能从 artifact 索引召回

### 第五步：接 MCP `Initialize/ListTools/CallTool`

目标：

* host/gateway 形态跑起来
* 能把 MCP 工具同步进 `tool_catalog`

验收：

* 至少一个 MCP server 能 initialize
* tools 可以被 catalog 搜到
* `CallTool` 的原始结果仍走 artifact -> reducer -> envelope

---

## 8. P1 的首个集成测试场景

### 场景

“跑一轮测试，定位第一个失败包，并给出可能相关的最近提交。”

### 链路

1. `run_command_readonly` 执行 `go test -json ./...`
2. `GoTestJSONReducer` 产出失败包 / stack hint
3. `Recall` 可拉回首个相关 stack 片段
4. 模型再调用 `git_log`
5. `GitLogReducer` 产出最近提交摘要
6. assistant 综合回答

### 验收

* 原始测试输出不进上下文
* 原始 git log 不进上下文
* 最终回答引用 artifact ref
* 任务能连续多轮跑完

---

## 9. 你现在最值得继续写的文件

接下来优先级最高的是：

1. `contextmgr/compact.go`
2. `tools/reducers/go_test_json.go`
3. `tools/reducers/playwright_snapshot.go`
4. `tools/mcp/client.go`
5. `tools/mcp/catalog.go`

这 5 个补完，你的系统就从 **P0: 可运行** 进入 **P1: 可持续运行**。

下一条我可以继续直接给你：
**`run_command_readonly.go`、`compact.go` 的增强版、`tool_catalog` 的 FTS5 版、以及 `tools/mcp/gateway.go` 的完整骨架**。
