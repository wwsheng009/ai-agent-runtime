package agent

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	runtimeerrors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/output"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestToolRequestedEventPayloadIncludesArgPreview(t *testing.T) {
	payload := toolRequestedEventPayload(types.ToolCall{
		ID:   "call-1",
		Name: "bash",
		Args: map[string]interface{}{
			"command": "Get-ChildItem -Force",
		},
	}, 3, "trace-1", nil)

	if got := payload["arg_preview"]; got != "command=Get-ChildItem -Force" {
		t.Fatalf("expected command preview, got %#v", got)
	}
	if got := payload["command_text"]; got != "Get-ChildItem -Force" {
		t.Fatalf("expected command text, got %#v", got)
	}
}

// 回归（2026-09-16）：shell 的真实入参是批量 `commands` 列表（toolargs 的 shell
// 参数表），只认 `command` 会让实时行没有 command_text，arg_preview 也退化成被截断
// 的 JSON。命令文本必须从批量列表里还原，并保持单行可读。
func TestToolRequestedEventPayloadIncludesBatchShellCommands(t *testing.T) {
	payload := toolRequestedEventPayload(types.ToolCall{
		ID:   "call-batch",
		Name: "shell",
		Args: map[string]interface{}{
			"commands": []interface{}{
				map[string]interface{}{"command": "go test ./...", "workdir": "E:/repo"},
				map[string]interface{}{"cmd": "git status --short"},
				map[string]interface{}{"workdir": "E:/repo"},
			},
		},
	}, 2, "trace-batch", nil)

	want := "go test ./... ; git status --short"
	if got := payload["command_text"]; got != want {
		t.Fatalf("expected joined batch command text %q, got %#v", want, got)
	}
	if got := payload["arg_preview"]; got != "command="+want {
		t.Fatalf("expected command preview %q, got %#v", "command="+want, got)
	}
}

func TestToolRequestedEventPayloadPrefersExplicitCommandOverBatch(t *testing.T) {
	payload := toolRequestedEventPayload(types.ToolCall{
		ID:   "call-mixed",
		Name: "bash",
		Args: map[string]interface{}{
			"command":  "git diff --stat",
			"commands": []string{"git status --short"},
		},
	}, 1, "trace-mixed", nil)

	if got := payload["command_text"]; got != "git diff --stat" {
		t.Fatalf("explicit command must win over batch list, got %#v", got)
	}
}

func TestToolRequestedEventPayloadIncludesMultipleGenericArgs(t *testing.T) {
	payload := toolRequestedEventPayload(types.ToolCall{
		ID:   "call-view",
		Name: "view",
		Args: map[string]interface{}{
			"file_path":            "main.go",
			"offset":               40,
			"limit":                20,
			"include_line_numbers": false,
			"workdir":              "E:/repo",
		},
	}, 1, "trace-view", nil)

	want := "file_path=main.go include_line_numbers=false limit=20 offset=40"
	if got := payload["arg_preview"]; got != want {
		t.Fatalf("expected complete generic preview %q, got %#v", want, got)
	}
	if got := payload["workdir"]; got != "E:/repo" {
		t.Fatalf("expected workdir context, got %#v", got)
	}
	if strings.Contains(payload["arg_preview"].(string), "workdir") {
		t.Fatalf("workdir must not be duplicated in arg preview: %#v", payload["arg_preview"])
	}
}

func TestToolRequestedEventPayloadPreservesLongFilename(t *testing.T) {
	base := t.TempDir()
	filename := strings.Repeat("very-long-file-name-", 4) + "component.generated.tsx"
	absPath := filepath.Join(base, "apps", "portal-modern", "src", filename)
	wantPath := filepath.Join("apps", "portal-modern", "src", filename)

	payload := toolRequestedEventPayload(types.ToolCall{
		ID:   "call-long-view",
		Name: "view",
		Args: map[string]interface{}{
			"file_path": absPath,
			"workdir":   base,
			"limit":     20,
		},
	}, 1, "trace-long-view", nil)

	if got := payload["arg_preview"]; got != "limit=20" {
		t.Fatalf("long file path must leave the compact preview, got %#v", got)
	}
	if got := payload["display_file_path"]; got != wantPath {
		t.Fatalf("expected complete relative file path %q, got %#v", wantPath, got)
	}
	if !strings.HasSuffix(payload["display_file_path"].(string), filename) {
		t.Fatalf("filename was not preserved: %#v", payload["display_file_path"])
	}
}

func TestToolCompletedEventPayloadRedactsSensitiveArgs(t *testing.T) {
	payload := toolCompletedEventPayload(toolExecutionResult{
		Call: types.ToolCall{
			ID:   "call-fetch",
			Name: "fetch",
			Args: map[string]interface{}{
				"url":          "https://example.test",
				"api_key":      "should-not-leak",
				"timeout_ms":   3000,
				"token_budget": 4096,
			},
		},
	}, 1, "trace-fetch", nil)

	want := "url=https://example.test api_key=<redacted> timeout_ms=3000 token_budget=4096"
	if got := payload["arg_preview"]; got != want {
		t.Fatalf("expected redacted generic preview %q, got %#v", want, got)
	}
	if strings.Contains(payload["arg_preview"].(string), "should-not-leak") {
		t.Fatalf("sensitive value leaked in arg preview: %#v", payload["arg_preview"])
	}
}

func TestToolRequestedEventPayloadIncludesCompleteGlobPreview(t *testing.T) {
	payload := toolRequestedEventPayload(types.ToolCall{
		ID:   "call-glob",
		Name: "glob",
		Args: map[string]interface{}{
			"pattern":          "**/*.tsx",
			"path":             "apps/portal-modern/src",
			"case_insensitive": false,
			"limit":            50,
		},
	}, 1, "trace-glob", nil)

	if got := payload["arg_preview"]; got != "pattern=**/*.tsx path=apps/portal-modern/src case_insensitive=false limit=50" {
		t.Fatalf("expected complete glob preview, got %#v", got)
	}
	if got := payload["logical_tool"]; got != "glob" {
		t.Fatalf("expected logical_tool=glob, got %#v", got)
	}
}

func TestToolRequestedEventPayloadIncludesCompleteGrepPreview(t *testing.T) {
	payload := toolRequestedEventPayload(types.ToolCall{
		ID:   "call-grep",
		Name: "grep",
		Args: map[string]interface{}{
			"patterns": []interface{}{"Popover", "DialogTrigger"},
			"paths":    []interface{}{"apps/portal-modern/src"},
			"glob":     "*.tsx",
			"context":  2,
		},
	}, 1, "trace-grep", nil)

	// 列表值按 " | " 连接（与前端折叠摘要同一口径）：不把 JSON 标点带进 UI，
	// 也不让 `[` `"` `,` 吃掉 200 字预算。
	want := "patterns=Popover | DialogTrigger paths=apps/portal-modern/src glob=*.tsx context=2"
	if got := payload["arg_preview"]; got != want {
		t.Fatalf("expected complete grep preview %q, got %#v", want, got)
	}
}

func TestToolRequestedEventPayloadRendersTypedStringListArgs(t *testing.T) {
	payload := toolRequestedEventPayload(types.ToolCall{
		ID:   "call-grep-typed",
		Name: "grep",
		Args: map[string]interface{}{
			// JSON 解码给 []interface{}，工具层直接构造给 []string：两条都要能渲染。
			"patterns": []string{"TODO", "FIXME"},
			"paths":    []string{"frontend/src"},
		},
	}, 1, "trace-grep", nil)

	want := "patterns=TODO | FIXME paths=frontend/src"
	if got := payload["arg_preview"]; got != want {
		t.Fatalf("expected readable typed list preview %q, got %#v", want, got)
	}
}

func TestToolCompletedEventPayloadPromotesSearchBackend(t *testing.T) {
	payload := toolCompletedEventPayload(toolExecutionResult{
		Call: types.ToolCall{
			ID:   "call-grep-backend",
			Name: "grep",
			Args: map[string]interface{}{"pattern": "Popover", "path": "src"},
		},
		Envelope: &output.Envelope{
			Metadata: map[string]interface{}{
				"tool_metadata": map[string]interface{}{
					"engine":          "rg",
					"backend_command": "rg",
					"backend_path":    `C:\tools\rg.exe`,
				},
			},
		},
	}, 1, "trace-grep-backend", nil)

	if got := payload["execution_backend"]; got != "rg" {
		t.Fatalf("expected execution_backend=rg, got %#v", got)
	}
	if got := payload["backend_command"]; got != "rg" {
		t.Fatalf("expected backend command, got %#v", got)
	}
	if got := payload["backend_path"]; got != `C:\tools\rg.exe` {
		t.Fatalf("expected backend path, got %#v", got)
	}
}

func TestToolRequestedEventPayloadIncludesWorkdir(t *testing.T) {
	payload := toolRequestedEventPayload(types.ToolCall{
		ID:   "call-workdir",
		Name: "execute_shell_command",
		Args: map[string]interface{}{
			"command": "git status",
			"workdir": "E:/projects/ai/ai-agent-runtime",
		},
	}, 1, "trace-workdir", nil)

	if got := payload["workdir"]; got != "E:/projects/ai/ai-agent-runtime" {
		t.Fatalf("expected workdir, got %#v", got)
	}
}

func TestToolRequestedEventPayloadIncludesBackgroundCwd(t *testing.T) {
	payload := toolRequestedEventPayload(types.ToolCall{
		ID:   "call-cwd",
		Name: "background_task",
		Args: map[string]interface{}{
			"command": "git status",
			"cwd":     "E:/projects/ai/ai-agent-runtime",
		},
	}, 1, "trace-cwd", nil)

	if got := payload["cwd"]; got != "E:/projects/ai/ai-agent-runtime" {
		t.Fatalf("expected cwd, got %#v", got)
	}
}

func TestToolCompletedEventPayloadPrefersStructuredSummary(t *testing.T) {
	payload := toolCompletedEventPayload(toolExecutionResult{
		Call: types.ToolCall{
			ID:   "call-2",
			Name: "ls",
			Args: map[string]interface{}{"path": "."},
		},
		Output: "目录: .\n\n📁 a/\n📁 b/\n📄 main.go\n\n统计: 1 个文件, 2 个目录",
		Envelope: &output.Envelope{
			Summary: "directory listing summary that should not win",
			Metadata: map[string]interface{}{
				"tool_metadata": map[string]interface{}{
					"file_count": 2,
					"dir_count":  1,
				},
			},
		},
	}, 2, "trace-2", nil)

	summaryLines, ok := payload["summary_lines"].([]string)
	if !ok {
		t.Fatalf("expected summary lines, got %#v", payload["summary_lines"])
	}
	expected := []string{
		"目录: .",
		"📁 a/ · 📁 b/ · 📄 main.go",
		"统计: 1 个文件, 2 个目录",
	}
	if len(summaryLines) != len(expected) {
		t.Fatalf("expected %d summary lines, got %#v", len(expected), summaryLines)
	}
	for i, line := range expected {
		if summaryLines[i] != line {
			t.Fatalf("expected summary line %d to be %q, got %q", i, line, summaryLines[i])
		}
	}
	if got := payload["arg_preview"]; got != "path=." {
		t.Fatalf("expected path preview, got %#v", got)
	}
}

func TestToolCompletedEventPayloadIncludesProtocolResult(t *testing.T) {
	payload := toolCompletedEventPayload(toolExecutionResult{
		Call: types.ToolCall{
			ID:   "call-protocol",
			Name: "view",
			Args: map[string]interface{}{"file_path": "a.go"},
		},
		Output: "file body line one\nfile body line two",
		Envelope: &output.Envelope{
			Metadata: map[string]interface{}{
				toolresult.MetadataOKKey:      true,
				toolresult.MetadataOutcomeKey: toolresult.OutcomeSuccess,
				toolresult.MetadataKey:        toolresult.KindText,
				toolresult.SourceKey:          toolresult.SourceToolkit,
			},
		},
	}, 1, "trace-protocol", nil)

	raw, ok := payload["protocol_result"].(map[string]interface{})
	if !ok || raw == nil {
		t.Fatalf("expected protocol_result map, got %#v", payload["protocol_result"])
	}
	if raw["ok"] != true {
		t.Fatalf("protocol_result.ok=%#v", raw["ok"])
	}
	if raw["tool_id"] != "view" || raw["call_id"] != "call-protocol" {
		t.Fatalf("protocol_result ids=%#v", raw)
	}
	if raw["outcome"] != toolresult.OutcomeSuccess {
		t.Fatalf("protocol_result.outcome=%#v", raw["outcome"])
	}
	if _, hasContent := raw["content"]; hasContent {
		t.Fatalf("protocol_result must stay compact without content: %#v", raw)
	}
	// Flat disposition fields remain for offline analyzers.
	if payload[toolresult.MetadataOKKey] != true || payload[toolresult.MetadataOutcomeKey] != toolresult.OutcomeSuccess {
		t.Fatalf("flat disposition missing: %#v", payload)
	}
}

func TestToolCompletedEventPayloadPreservesTodosListLines(t *testing.T) {
	output := strings.Join([]string{
		"任务列表已更新: 2 待处理, 1 进行中, 0 已完成",
		"任务列表更新状态: 新增 3, 状态变更 0, 保持 0, 移除 0",
		"当前任务列表:",
		"1. [待处理] 分析需求 (新增)",
		"2. [进行中] 修改实现 (新增)",
		"3. [待处理] 运行测试 (新增)",
	}, "\n")

	payload := toolCompletedEventPayload(toolExecutionResult{
		Call: types.ToolCall{
			ID:   "call-todos",
			Name: "todos",
			Args: map[string]interface{}{
				"todos": []interface{}{1, 2, 3},
			},
		},
		Output: output,
	}, 1, "trace-todos", nil)

	summaryLines, ok := payload["summary_lines"].([]string)
	if !ok {
		t.Fatalf("expected summary lines, got %#v", payload["summary_lines"])
	}
	expected := []string{
		"任务列表已更新: 2 待处理, 1 进行中, 0 已完成",
		"任务列表更新状态: 新增 3, 状态变更 0, 保持 0, 移除 0",
		"当前任务列表:",
		"1. [待处理] 分析需求 (新增)",
		"2. [进行中] 修改实现 (新增)",
		"3. [待处理] 运行测试 (新增)",
	}
	if len(summaryLines) != len(expected) {
		t.Fatalf("expected %d summary lines, got %#v", len(expected), summaryLines)
	}
	for i, line := range expected {
		if summaryLines[i] != line {
			t.Fatalf("expected summary line %d to be %q, got %q", i, line, summaryLines[i])
		}
	}
	if got := payload["arg_preview"]; got != "todos=[3]" {
		t.Fatalf("expected todos arg preview, got %#v", got)
	}
}

// todoSnapshotFixtureItem mirrors toolkit/tools.TodoItem's host-relevant fields
// plus producer-only extras, so the agent tests never import the tool package.
type todoSnapshotFixtureItem struct {
	Content     string `json:"content"`
	Status      string `json:"status"`
	ActiveForm  string `json:"active_form"`
	CreatedAt   int64  `json:"created_at"`
	UpdatedAt   int64  `json:"updated_at"`
	CompletedAt int64  `json:"completed_at,omitempty"`
}

func TestToolCompletedEventPayloadAttachesTodoSnapshot(t *testing.T) {
	producerMetadata := map[string]interface{}{
		"total":        4,
		"pending":      1,
		"in_progress":  1,
		"completed":    1,
		"storage_mode": "memory",
		"session_id":   "sess-1",
		"goal_id":      "goal-1",
		"todos": []todoSnapshotFixtureItem{
			{Content: "  分析需求  ", Status: " in_progress ", ActiveForm: "  分析需求中  ", CreatedAt: 1, UpdatedAt: 2},
			{Content: "修改实现", Status: "completed", ActiveForm: "修改实现中", CreatedAt: 1, UpdatedAt: 2, CompletedAt: 3},
			{Content: "   ", Status: "pending", ActiveForm: "空内容应被丢弃"},
			{Content: "状态非法应被丢弃", Status: "blocked", ActiveForm: "状态非法"},
		},
	}

	payload := toolCompletedEventPayload(toolExecutionResult{
		Call:     types.ToolCall{ID: "call-todos-snapshot", Name: "todos"},
		Output:   "任务列表已更新: 1 待处理, 1 进行中, 1 已完成",
		Envelope: &output.Envelope{Metadata: producerMetadata},
	}, 2, "trace-todos-snapshot", nil)

	proto, _ := payload["protocol_result"].(map[string]interface{})
	if proto == nil {
		t.Fatalf("expected protocol_result, payload=%#v", payload)
	}
	meta, _ := proto["metadata"].(map[string]interface{})
	if meta == nil {
		t.Fatalf("expected protocol_result.metadata, got %#v", proto)
	}
	rawSnapshot, ok := meta["todo_snapshot"]
	if !ok {
		t.Fatalf("expected protocol_result.metadata.todo_snapshot, got %#v", meta)
	}
	snapshot, ok := rawSnapshot.(map[string]interface{})
	if !ok {
		t.Fatalf("todo_snapshot type=%T", rawSnapshot)
	}
	if snapshot["session_id"] != "sess-1" || snapshot["goal_id"] != "goal-1" {
		t.Fatalf("todo_snapshot owner ids=%#v", snapshot)
	}
	items, ok := snapshot["items"].([]map[string]interface{})
	if !ok {
		t.Fatalf("todo_snapshot items type=%T", snapshot["items"])
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 valid items (blank content / bad status dropped), got %#v", items)
	}
	wantContent := []string{"分析需求", "修改实现"}
	wantStatus := []string{"in_progress", "completed"}
	wantActiveForm := []string{"分析需求中", "修改实现中"}
	for i, item := range items {
		if len(item) != 3 {
			t.Fatalf("item %d must carry exactly content/status/active_form, got %#v", i, item)
		}
		if item["content"] != wantContent[i] || item["status"] != wantStatus[i] || item["active_form"] != wantActiveForm[i] {
			t.Fatalf("item %d=%#v", i, item)
		}
	}
	// The producer map stays untouched: the snapshot is an added, wire-only key.
	if _, mutated := producerMetadata["todo_snapshot"]; mutated {
		t.Fatalf("producer metadata must not carry todo_snapshot: %#v", producerMetadata)
	}
	if todos, ok := producerMetadata["todos"].([]todoSnapshotFixtureItem); !ok || len(todos) != 4 {
		t.Fatalf("producer todos slice mutated: %#v", producerMetadata["todos"])
	}
}

// 生产形状回归（2026-09-16）：真实执行路径不把工具结果元数据平铺进 envelope，
// 而是由 recordToolExecutionOutcome 整体嵌到 metadata["tool_metadata"]（todos 数组
// 只存在该包内）。若快照只从顶层 metadata["todos"] 取值，
// protocol_result.metadata.todo_snapshot 永远不会出现，前端任务面板（通道 A）就只剩
// 文本摘要，组件不渲染。
func TestToolCompletedEventPayloadAttachesTodoSnapshotFromNestedToolMetadata(t *testing.T) {
	nested := map[string]interface{}{
		"total":        2,
		"pending":      1,
		"in_progress":  1,
		"completed":    0,
		"storage_mode": "memory",
		"session_id":   "session_20260916133052_DktyYFb3",
		"goal_id":      "goal-nested",
		"todos": []todoSnapshotFixtureItem{
			{Content: "编写 index.html 页面骨架", Status: "in_progress", ActiveForm: "编写 index.html 页面骨架中", CreatedAt: 1, UpdatedAt: 2},
			{Content: "编写 assets/css/style.css 报表样式", Status: "pending", ActiveForm: "编写 assets/css/style.css 报表样式中"},
		},
	}
	envelopeMetadata := map[string]interface{}{
		"step":          3,
		"trace_id":      "trace-todos-nested",
		"tool_source":   "toolkit",
		"tool_metadata": nested,
	}

	payload := toolCompletedEventPayload(toolExecutionResult{
		Call:     types.ToolCall{ID: "call-todos-nested", Name: "todos"},
		Output:   "任务列表已更新: 1 待处理, 1 进行中, 0 已完成",
		Envelope: &output.Envelope{Metadata: envelopeMetadata},
	}, 3, "trace-todos-nested", nil)

	proto, _ := payload["protocol_result"].(map[string]interface{})
	if proto == nil {
		t.Fatalf("expected protocol_result, payload=%#v", payload)
	}
	meta, _ := proto["metadata"].(map[string]interface{})
	if meta == nil {
		t.Fatalf("expected protocol_result.metadata, got %#v", proto)
	}
	snapshot, ok := meta["todo_snapshot"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected todo_snapshot from nested tool_metadata, got %#v", meta)
	}
	if snapshot["session_id"] != "session_20260916133052_DktyYFb3" || snapshot["goal_id"] != "goal-nested" {
		t.Fatalf("todo_snapshot owner ids=%#v", snapshot)
	}
	items, ok := snapshot["items"].([]map[string]interface{})
	if !ok || len(items) != 2 {
		t.Fatalf("todo_snapshot items=%#v", snapshot["items"])
	}
	if items[0]["status"] != "in_progress" || items[1]["content"] != "编写 assets/css/style.css 报表样式" {
		t.Fatalf("todo_snapshot items=%#v", items)
	}
	// 原始 todos 仍不得进入线上薄白名单，嵌套包本身也不该被写入。
	if _, leaked := meta["todos"]; leaked {
		t.Fatalf("raw todos must not leak into wire metadata: %#v", meta)
	}
	if _, mutated := nested["todo_snapshot"]; mutated {
		t.Fatalf("producer tool_metadata must not carry todo_snapshot: %#v", nested)
	}
}

// 端到端形状回归：不走手搭 map，而是按真实三步装配——recordToolExecutionOutcome
// 嵌套工具元数据 → gateway.Process 生成 envelope → toolCompletedEventPayload 生成
// 事件载荷。装配形状一旦回退到只平铺，这里的断言会先红。
func TestToolCompletedEventPayloadTodoSnapshotThroughRealAssembly(t *testing.T) {
	tc := types.ToolCall{ID: "call-todos-e2e", Name: "todos"}
	metadata := map[string]interface{}{
		"step":        3,
		"trace_id":    "trace-todos-e2e",
		"tool_source": "toolkit",
	}
	rawMeta := map[string]interface{}{
		"total":        2,
		"pending":      1,
		"in_progress":  1,
		"completed":    0,
		"storage_mode": "memory",
		"session_id":   "session-e2e",
		"goal_id":      "goal-e2e",
		"todos": []todoSnapshotFixtureItem{
			{Content: "编写 index.html 页面骨架", Status: "in_progress", ActiveForm: "编写 index.html 页面骨架中"},
			{Content: "编写 assets/js/data.js 数据层", Status: "pending", ActiveForm: "编写 assets/js/data.js 数据层中"},
		},
	}
	result := toolExecutionResult{Call: tc}
	recordToolExecutionOutcome(&result, metadata, "任务列表已更新: 1 待处理, 1 进行中, 0 已完成", rawMeta, nil)

	envelope, err := output.NewGateway(nil).Process(
		context.Background(),
		newRawToolResult("session-e2e", tc, 3, result.Output, result.Error, metadata),
	)
	if err != nil || envelope == nil {
		t.Fatalf("gateway.Process envelope=%#v err=%v", envelope, err)
	}
	result.Envelope = envelope

	payload := toolCompletedEventPayload(result, 3, "trace-todos-e2e", nil)
	proto, _ := payload["protocol_result"].(map[string]interface{})
	meta, _ := proto["metadata"].(map[string]interface{})
	snapshot, ok := meta["todo_snapshot"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected todo_snapshot through the real assembly, got %#v", meta)
	}
	items, ok := snapshot["items"].([]map[string]interface{})
	if !ok || len(items) != 2 {
		t.Fatalf("todo_snapshot items=%#v", snapshot["items"])
	}
	if snapshot["session_id"] != "session-e2e" || snapshot["goal_id"] != "goal-e2e" {
		t.Fatalf("todo_snapshot owner ids=%#v", snapshot)
	}
}

func TestToolCompletedEventPayloadSkipsTodoSnapshotForOtherTools(t *testing.T) {
	payload := toolCompletedEventPayload(toolExecutionResult{
		Call:   types.ToolCall{ID: "call-read-file", Name: "read_file"},
		Output: "file body",
		Envelope: &output.Envelope{Metadata: map[string]interface{}{
			"todos": []map[string]interface{}{
				{"content": "非 todos 工具不得携带", "status": "pending", "active_form": "不得携带"},
			},
			"session_id": "sess-2",
			"goal_id":    "goal-2",
		}},
	}, 1, "trace-read-file", nil)

	proto, _ := payload["protocol_result"].(map[string]interface{})
	if proto == nil {
		t.Fatalf("expected protocol_result, payload=%#v", payload)
	}
	meta, _ := proto["metadata"].(map[string]interface{})
	if _, ok := meta["todo_snapshot"]; ok {
		t.Fatalf("todo_snapshot must stay scoped to the todos tool: %#v", meta)
	}
	// The scoping is per tool, so the shared allowlist must not have been widened
	// with the raw producer key either.
	if _, ok := meta["todos"]; ok {
		t.Fatalf("raw todos metadata leaked into the shared allowlist: %#v", meta)
	}
}

func TestToolCompletedEventPayloadOmitsEmptyTodoSnapshot(t *testing.T) {
	// No valid item survives filtering, so the key stays absent entirely.
	payload := toolCompletedEventPayload(toolExecutionResult{
		Call:   types.ToolCall{ID: "call-todos-invalid", Name: "todos"},
		Output: "任务列表已更新",
		Envelope: &output.Envelope{Metadata: map[string]interface{}{
			"todos": []map[string]interface{}{
				{"content": "   ", "status": "pending"},
				{"content": "状态非法", "status": "blocked"},
			},
		}},
	}, 1, "trace-todos-invalid", nil)
	proto, _ := payload["protocol_result"].(map[string]interface{})
	meta, _ := proto["metadata"].(map[string]interface{})
	if _, ok := meta["todo_snapshot"]; ok {
		t.Fatalf("empty snapshot must stay absent: %#v", meta)
	}

	// Blank owner ids are omitted rather than emitted as empty strings.
	payload = toolCompletedEventPayload(toolExecutionResult{
		Call:   types.ToolCall{ID: "call-todos-noids", Name: "todos"},
		Output: "任务列表已更新",
		Envelope: &output.Envelope{Metadata: map[string]interface{}{
			"todos": []map[string]interface{}{
				{"content": "运行测试", "status": "PENDING", "active_form": ""},
			},
			"session_id": "   ",
			"goal_id":    0,
		}},
	}, 1, "trace-todos-noids", nil)
	proto, _ = payload["protocol_result"].(map[string]interface{})
	meta, _ = proto["metadata"].(map[string]interface{})
	snapshot, ok := meta["todo_snapshot"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected todo_snapshot, got %#v", meta)
	}
	if _, ok := snapshot["session_id"]; ok {
		t.Fatalf("blank session_id must be omitted: %#v", snapshot)
	}
	if _, ok := snapshot["goal_id"]; ok {
		t.Fatalf("non-string goal_id must be omitted: %#v", snapshot)
	}
	items, _ := snapshot["items"].([]map[string]interface{})
	if len(items) != 1 || items[0]["status"] != "pending" || items[0]["active_form"] != "" {
		t.Fatalf("items=%#v", items)
	}
}

func TestToolCompletedEventPayloadFallsBackToEnvelopeSummary(t *testing.T) {
	payload := toolCompletedEventPayload(toolExecutionResult{
		Call: types.ToolCall{
			ID:   "call-3",
			Name: "bash",
			Args: map[string]interface{}{"command": "git status"},
		},
		Envelope: &output.Envelope{
			Summary: "On branch main\nnothing to commit, working tree clean",
		},
	}, 1, "trace-3", nil)

	summaryLines, ok := payload["summary_lines"].([]string)
	if !ok {
		t.Fatalf("expected summary lines, got %#v", payload["summary_lines"])
	}
	expected := []string{
		"On branch main",
		"nothing to commit, working tree clean",
	}
	if len(summaryLines) != len(expected) {
		t.Fatalf("expected %d summary lines, got %#v", len(expected), summaryLines)
	}
	for i, line := range expected {
		if summaryLines[i] != line {
			t.Fatalf("expected summary line %d to be %q, got %q", i, line, summaryLines[i])
		}
	}
	if got := payload["command_text"]; got != "git status" {
		t.Fatalf("expected command text, got %#v", got)
	}
}

func TestToolCompletedEventPayloadCarriesCompleteGitDiffForRendering(t *testing.T) {
	diff := "--- a/app.go\n+++ b/app.go\n@@ -1 +1 @@\n-old\n+new\n"
	payload := toolCompletedEventPayload(toolExecutionResult{
		Call: types.ToolCall{
			ID:   "call-diff",
			Name: "bash",
			Args: map[string]interface{}{"command": "git -C repo diff -- app.go"},
		},
		Output: diff,
		Envelope: &output.Envelope{Metadata: map[string]interface{}{
			"output_capture_complete": true,
		}},
	}, 1, "trace-diff", nil)

	if got := payload["render_output_format"]; got != "diff" {
		t.Fatalf("render_output_format=%#v, want diff", got)
	}
	if got := payload["render_output"]; got != strings.TrimSpace(diff) {
		t.Fatalf("unexpected diff render output: %#v", got)
	}
	if got := payload["render_output_untruncated"]; got != true {
		t.Fatalf("render_output_untruncated=%#v, want true", got)
	}
}

func TestToolCompletedEventPayloadDoesNotRenderTruncatedGitDiff(t *testing.T) {
	payload := toolCompletedEventPayload(toolExecutionResult{
		Call: types.ToolCall{
			ID:   "call-diff-truncated",
			Name: "bash",
			Args: map[string]interface{}{"command": "git diff"},
		},
		Output: "--- a/app.go\n+++ b/app.go\n@@ -1 +1 @@\n-old",
		Envelope: &output.Envelope{Metadata: map[string]interface{}{
			"output_capture_complete": false,
			"capture_limit_reached":   true,
		}},
	}, 1, "trace-diff-truncated", nil)

	if _, ok := payload["render_output"]; ok {
		t.Fatalf("truncated diff must not be marked renderable: %#v", payload)
	}
}

func TestToolCompletedEventPayloadSynthesizesEmptyMutationSummary(t *testing.T) {
	payload := toolCompletedEventPayload(toolExecutionResult{
		Call: types.ToolCall{
			ID:   "call-empty-patch",
			Name: "apply_patch",
		},
		Envelope: &output.Envelope{
			ToolName: "apply_patch",
			Metadata: map[string]interface{}{
				"tool_metadata": map[string]interface{}{
					"mutated_paths": []string{"changed.go"},
				},
			},
		},
	}, 1, "trace-empty-patch", nil)

	want := "Tool completed successfully; changed 1 file: changed.go."
	summaryLines, ok := payload["summary_lines"].([]string)
	if !ok || len(summaryLines) != 1 || summaryLines[0] != want {
		t.Fatalf("expected mutation summary line %q, got %#v", want, payload["summary_lines"])
	}
	if got := payload["render_output"]; got != want {
		t.Fatalf("expected mutation render fallback %q, got %#v", want, got)
	}
}

func TestToolCompletedEventPayloadPrefersErrorOverGenericFallbackSummary(t *testing.T) {
	payload := toolCompletedEventPayload(toolExecutionResult{
		Call: types.ToolCall{
			ID:   "call-3b",
			Name: "execute_shell_command",
			Args: map[string]interface{}{"command": "git status"},
		},
		Error: "exit status 128",
		Envelope: &output.Envelope{
			Summary: "Tool execute_shell_command failed before producing output.",
			Error:   "exit status 128",
		},
	}, 1, "trace-3b", nil)

	summaryLines, ok := payload["summary_lines"].([]string)
	if !ok {
		t.Fatalf("expected summary lines, got %#v", payload["summary_lines"])
	}
	expected := []string{"failed: exit status 128"}
	if len(summaryLines) != len(expected) {
		t.Fatalf("expected %d summary lines, got %#v", len(expected), summaryLines)
	}
	for i, line := range expected {
		if summaryLines[i] != line {
			t.Fatalf("expected summary line %d to be %q, got %q", i, line, summaryLines[i])
		}
	}
}

func TestToolCompletedEventPayloadSkipsToolMetadataAppendix(t *testing.T) {
	payload := toolCompletedEventPayload(toolExecutionResult{
		Call: types.ToolCall{
			ID:   "call-4",
			Name: "view",
			Args: map[string]interface{}{"file_path": "README.md"},
		},
		Output: "line 1\nline 2\nline 3\n\nMetadata:\n{\"file_path\":\"README.md\"}",
	}, 1, "trace-4", nil)

	summaryLines, ok := payload["summary_lines"].([]string)
	if !ok {
		t.Fatalf("expected summary lines, got %#v", payload["summary_lines"])
	}
	expected := []string{"line 1", "line 2", "line 3"}
	if len(summaryLines) != len(expected) {
		t.Fatalf("expected %d summary lines, got %#v", len(expected), summaryLines)
	}
	for i, line := range expected {
		if summaryLines[i] != line {
			t.Fatalf("expected summary line %d to be %q, got %q", i, line, summaryLines[i])
		}
	}
}

func TestToolCompletedEventPayloadMergesAwaitingModelHint(t *testing.T) {
	payload := toolCompletedEventPayload(toolExecutionResult{
		Call: types.ToolCall{
			ID:   "call-5",
			Name: "web_search",
			Args: map[string]interface{}{"query": "weather"},
		},
		Output: "result 1\nresult 2",
	}, 1, "trace-5", map[string]interface{}{
		"awaiting_model": true,
	})

	if got := payload["awaiting_model"]; got != true {
		t.Fatalf("expected awaiting_model=true, got %#v", got)
	}
}

func TestToolCompletedEventPayloadIncludesToolSource(t *testing.T) {
	payload := toolCompletedEventPayload(toolExecutionResult{
		Call: types.ToolCall{
			ID:   "call-source",
			Name: "view",
			Args: map[string]interface{}{"file_path": "README.md"},
		},
		Output: "line 1",
		Envelope: &output.Envelope{
			Metadata: map[string]interface{}{
				toolresult.SourceKey: toolresult.SourceToolkit,
			},
		},
	}, 1, "trace-source", nil)

	if got := payload[toolresult.SourceKey]; got != toolresult.SourceToolkit {
		t.Fatalf("expected %s=%q, got %#v", toolresult.SourceKey, toolresult.SourceToolkit, got)
	}
}

func TestToolCompletedEventPayloadIncludesShellMetadata(t *testing.T) {
	payload := toolCompletedEventPayload(toolExecutionResult{
		Call: types.ToolCall{
			ID:   "call-shell",
			Name: "execute_shell_command",
			Args: map[string]interface{}{"command": "git status"},
		},
		Output: "On branch main",
		Envelope: &output.Envelope{
			Metadata: map[string]interface{}{
				toolresult.SourceKey:   toolresult.SourceToolkit,
				toolresult.MetadataKey: toolresult.KindText,
				"shell_type":           "pwsh",
				"shell_path":           `C:\Program Files\PowerShell\7\pwsh.exe`,
				"shell_display":        `pwsh (C:\Program Files\PowerShell\7\pwsh.exe)`,
			},
		},
	}, 1, "trace-shell", nil)

	if got := payload[toolresult.MetadataKey]; got != toolresult.KindText {
		t.Fatalf("expected %s=%q, got %#v", toolresult.MetadataKey, toolresult.KindText, got)
	}
	if got := payload["shell_type"]; got != "pwsh" {
		t.Fatalf("expected shell_type=pwsh, got %#v", got)
	}
	if got := payload["shell_path"]; got != `C:\Program Files\PowerShell\7\pwsh.exe` {
		t.Fatalf("expected shell_path to be preserved, got %#v", got)
	}
	if got := payload["shell_display"]; got != `pwsh (C:\Program Files\PowerShell\7\pwsh.exe)` {
		t.Fatalf("expected shell_display to be preserved, got %#v", got)
	}
}

func TestToolCompletedEventPayloadIncludesStructuredTimeoutMetadata(t *testing.T) {
	metadata := map[string]interface{}{}
	result := toolExecutionResult{
		Call: types.ToolCall{ID: "call-timeout", Name: "bash"},
	}
	recordToolExecutionOutcome(&result, metadata, "partial output", map[string]interface{}{
		"shell_type": "pwsh",
	}, runtimeerrors.WrapWithContext(
		runtimeerrors.ErrTurnDeadlineExceeded,
		"turn deadline exceeded",
		context.DeadlineExceeded,
		map[string]interface{}{
			"timeout_requested_ms": int64(600000),
			"timeout_effective_ms": int64(30000),
			"timeout_source":       "chat_turn_deadline",
		},
	))
	result.Envelope = &output.Envelope{Metadata: metadata}

	payload := toolCompletedEventPayload(result, 1, "trace-timeout", nil)
	if got := payload["error_code"]; got != string(runtimeerrors.ErrTurnDeadlineExceeded) {
		t.Fatalf("expected structured error code, got %#v", got)
	}
	if got := payload["timeout_requested_ms"]; got != int64(600000) {
		t.Fatalf("expected requested timeout, got %#v", got)
	}
	if got := payload["timeout_effective_ms"]; got != int64(30000) {
		t.Fatalf("expected effective timeout, got %#v", got)
	}
	if got := payload["timeout_source"]; got != "chat_turn_deadline" {
		t.Fatalf("expected timeout source, got %#v", got)
	}
}

func TestToolCompletedEventPayloadPromotesStaleContextDisposition(t *testing.T) {
	// End-to-end: tool-authored STALE_CONTEXT lives in raw toolkit metadata,
	// recordToolExecutionOutcome must promote codes + recovery hints, and
	// tool.completed payload / Diagnose must surface them for chat-log export.
	metadata := map[string]interface{}{"step": 2, "trace_id": "trace-stale-event"}
	result := toolExecutionResult{
		Call: types.ToolCall{ID: "call-edit-stale", Name: "edit"},
	}
	recordToolExecutionOutcome(&result, metadata, "partial", map[string]interface{}{
		"error_code":                 string(runtimeerrors.ErrToolStaleContext),
		"retryable":                  false,
		"failure_class":              "stale_context",
		"file_path":                  `E:\projects\demo\file.go`,
		"suggested_view_offset":      12,
		"suggested_view_limit":       40,
		"current_snippet":            "func Hello() {}\n",
		"current_snippet_start_line": 13,
		"next_action":                "STALE_CONTEXT: copy current_snippet then rebuild; do not retry the same stale old_string unchanged.",
	}, fmt.Errorf("old_string 未在文件中找到；edit 只执行精确匹配"))
	result.Envelope = &output.Envelope{Metadata: metadata}

	payload := toolCompletedEventPayload(result, 2, "trace-stale-event", nil)
	if payload["error_code"] != string(runtimeerrors.ErrToolStaleContext) {
		t.Fatalf("payload error_code=%v want STALE_CONTEXT meta=%#v", payload["error_code"], metadata)
	}
	if payload["ok"] != false {
		t.Fatalf("expected ok=false, got %#v", payload)
	}
	if payload["retryable"] != false {
		t.Fatalf("expected retryable=false, got %#v", payload)
	}
	next, _ := payload["next_action"].(string)
	if !strings.Contains(next, "STALE_CONTEXT") || !strings.Contains(next, "current_snippet") {
		t.Fatalf("expected tool-authored STALE_CONTEXT next_action, got %q", next)
	}
	if payload["failure_class"] != "stale_context" {
		t.Fatalf("expected failure_class=stale_context, got %#v", payload["failure_class"])
	}
	if payload["file_path"] != `E:\projects\demo\file.go` {
		t.Fatalf("expected file_path promoted, got %#v", payload["file_path"])
	}
	if offset, _ := payload["suggested_view_offset"].(int); offset != 12 {
		t.Fatalf("expected suggested_view_offset=12, got %#v", payload["suggested_view_offset"])
	}
	if limit, _ := payload["suggested_view_limit"].(int); limit != 40 {
		t.Fatalf("expected suggested_view_limit=40, got %#v", payload["suggested_view_limit"])
	}
	if snippet, _ := payload["current_snippet"].(string); !strings.Contains(snippet, "func Hello") {
		t.Fatalf("expected current_snippet on payload, got %#v", payload["current_snippet"])
	}
	if start, _ := payload["current_snippet_start_line"].(int); start != 13 {
		t.Fatalf("expected current_snippet_start_line=13, got %#v", payload["current_snippet_start_line"])
	}
	if payload[toolresult.MetadataOutcomeKey] != toolresult.OutcomeFailed {
		t.Fatalf("expected outcome=failed, got %#v", payload[toolresult.MetadataOutcomeKey])
	}
}

func TestToolCompletedEventPayloadRefinesGenericToolExecutionEditMiss(t *testing.T) {
	// Mirrors live chat-log shape: TOOL_EXECUTION + generic next_action stamped
	// on envelope metadata while the error body is an edit old_string miss.
	// Diagnose must refine so chat-log export stops counting bare TOOL_EXECUTION.
	result := toolExecutionResult{
		Call:  types.ToolCall{ID: "call-edit-generic", Name: "edit"},
		Error: "old_string 未在文件中找到；edit 只执行精确匹配（包括空格、缩进和换行），不会自动模糊定位。",
		Envelope: &output.Envelope{Metadata: map[string]interface{}{
			"error_code":  string(runtimeerrors.ErrToolExecution),
			"retryable":   true,
			"next_action": toolresult.DefaultToolExecutionNextAction,
		}},
	}
	payload := toolCompletedEventPayload(result, 3, "trace-edit-refine", nil)
	if payload["error_code"] != string(runtimeerrors.ErrToolStaleContext) {
		t.Fatalf("payload error_code=%v want STALE_CONTEXT payload=%#v", payload["error_code"], payload)
	}
	if payload["ok"] != false {
		t.Fatalf("expected ok=false, got %#v", payload)
	}
	if payload["retryable"] != false {
		t.Fatalf("refined STALE_CONTEXT must not be retryable, got %#v", payload["retryable"])
	}
	next, _ := payload["next_action"].(string)
	if next == toolresult.DefaultToolExecutionNextAction || strings.HasPrefix(strings.ToLower(next), "inspect the error details") {
		t.Fatalf("generic next_action must yield to stale recovery, got %q", next)
	}
	if payload[toolresult.MetadataOutcomeKey] != toolresult.OutcomeFailed {
		t.Fatalf("expected outcome=failed, got %#v", payload[toolresult.MetadataOutcomeKey])
	}
}

func TestToolCompletedEventPayloadRehydratesSnippetFromErrorBody(t *testing.T) {
	// Live residual: toolkit metadata lost current_snippet; only error body has
	// multi-line closest block. tool.completed / chat-log must still export it.
	errBody := "old_string 未在文件中找到；已尝试 CRLF/LF。\n" +
		"建议从第 10 行附近用 view 重读（suggested_view_offset=9）。\n" +
		"最接近的当前内容（第 10 行附近，可直接据此重建 old_string）:\n" +
		"    10|\tfunc Hello() {\n" +
		"    11|\t\treturn\n" +
		"    12|\t}\n" +
		"next_action: 优先用上方“最接近的当前内容”"
	result := toolExecutionResult{
		Call:  types.ToolCall{ID: "call-edit-rehydrate", Name: "edit"},
		Error: errBody,
		Envelope: &output.Envelope{Metadata: map[string]interface{}{
			"error_code":  string(runtimeerrors.ErrToolExecution),
			"next_action": toolresult.DefaultToolExecutionNextAction,
		}},
	}
	payload := toolCompletedEventPayload(result, 4, "trace-rehydrate", nil)
	if payload["error_code"] != string(runtimeerrors.ErrToolStaleContext) {
		t.Fatalf("error_code=%v want STALE_CONTEXT", payload["error_code"])
	}
	snip, _ := payload["current_snippet"].(string)
	if !strings.Contains(snip, "func Hello()") {
		t.Fatalf("expected rehydrated current_snippet, got %#v", payload["current_snippet"])
	}
	if !strings.Contains(snip, "\tfunc Hello") {
		t.Fatalf("expected indent preserved in snippet, got %q", snip)
	}
	if offset, _ := payload["suggested_view_offset"].(int); offset != 9 {
		t.Fatalf("suggested_view_offset=%#v want 9", payload["suggested_view_offset"])
	}
	// Nested protocol_result thin metadata should also carry snippet.
	proto, _ := payload["protocol_result"].(map[string]interface{})
	if proto == nil {
		t.Fatalf("expected protocol_result, payload=%#v", payload)
	}
	meta, _ := proto["metadata"].(map[string]interface{})
	if meta == nil {
		t.Fatalf("expected protocol_result.metadata, got %#v", proto)
	}
	if psnip, _ := meta["current_snippet"].(string); !strings.Contains(psnip, "func Hello") {
		t.Fatalf("protocol_result.metadata.current_snippet missing: %#v", meta)
	}
}

func TestToolCompletedEventPayloadIncludesActionableDiagnostic(t *testing.T) {
	result := toolExecutionResult{
		Call:  types.ToolCall{ID: "call-missing", Name: "task_output"},
		Error: "background job not found",
		Envelope: &output.Envelope{Metadata: map[string]interface{}{
			"ok":          false,
			"error_code":  "JOB_NOT_FOUND",
			"retryable":   false,
			"next_action": "Use the exact job_id returned by background_task.",
		}},
	}

	payload := toolCompletedEventPayload(result, 1, "trace-diagnostic", nil)
	if payload["ok"] != false || payload["error_code"] != "JOB_NOT_FOUND" || payload["retryable"] != false {
		t.Fatalf("expected structured failure diagnostic, got %#v", payload)
	}
	if payload["next_action"] != "Use the exact job_id returned by background_task." {
		t.Fatalf("expected next action, got %#v", payload)
	}
	if payload[toolresult.MetadataOutcomeKey] != toolresult.OutcomeFailed {
		t.Fatalf("expected outcome=failed on hard failure, got %#v", payload[toolresult.MetadataOutcomeKey])
	}
}

func TestToolCompletedEventPayloadExportsEmptySuccessDisposition(t *testing.T) {
	payload := toolCompletedEventPayload(toolExecutionResult{
		Call: types.ToolCall{
			ID:   "call-empty-grep",
			Name: "grep",
			Args: map[string]interface{}{"pattern": "no-such-token"},
		},
		Output: "No matches found.",
		Envelope: &output.Envelope{
			Metadata: map[string]interface{}{
				toolresult.MetadataOKKey:          true,
				toolresult.MetadataEmptyResultKey: true,
				toolresult.MetadataOutcomeKey:     toolresult.OutcomeEmpty,
				toolresult.MetadataNextActionKey:  "Broaden the query or confirm the search scope; empty success is valid evidence.",
				"match_count":                     0,
			},
		},
	}, 1, "trace-empty", nil)

	if payload[toolresult.MetadataOKKey] != true {
		t.Fatalf("expected ok=true for empty success, got %#v", payload)
	}
	if payload[toolresult.MetadataOutcomeKey] != toolresult.OutcomeEmpty {
		t.Fatalf("expected outcome=empty, got %#v", payload[toolresult.MetadataOutcomeKey])
	}
	if payload[toolresult.MetadataEmptyResultKey] != true {
		t.Fatalf("expected empty_result=true, got %#v", payload[toolresult.MetadataEmptyResultKey])
	}
	if got := strings.TrimSpace(fmt.Sprint(payload[toolresult.MetadataNextActionKey])); got == "" || got == "<nil>" {
		t.Fatalf("expected empty-success next_action, got %#v", payload[toolresult.MetadataNextActionKey])
	}
}

func TestToolCompletedEventPayloadExportsPartialBatchDisposition(t *testing.T) {
	payload := toolCompletedEventPayload(toolExecutionResult{
		Call: types.ToolCall{
			ID:   "call-partial-view",
			Name: "view",
			Args: map[string]interface{}{
				"files": []interface{}{
					map[string]interface{}{"file_path": "a.go"},
					map[string]interface{}{"file_path": "missing.go"},
				},
			},
		},
		Output: "partial batch body",
		Envelope: &output.Envelope{
			Metadata: map[string]interface{}{
				toolresult.MetadataOKKey:             true,
				toolresult.MetadataOutcomeKey:        toolresult.OutcomePartial,
				toolresult.MetadataPartialFailureKey: true,
				toolresult.MetadataRequestedCountKey: 2,
				toolresult.MetadataFailedCountKey:    1,
				toolresult.MetadataSucceededCountKey: 1,
				toolresult.MetadataFailedItemsKey: []map[string]interface{}{
					toolresult.FailedItemMap(toolresult.IntPtr(1), "missing.go", "", "path does not exist"),
				},
			},
		},
	}, 2, "trace-partial", nil)

	if payload[toolresult.MetadataOKKey] != true {
		t.Fatalf("expected ok=true for partial batch, got %#v", payload)
	}
	if payload[toolresult.MetadataOutcomeKey] != toolresult.OutcomePartial {
		t.Fatalf("expected outcome=partial, got %#v", payload[toolresult.MetadataOutcomeKey])
	}
	if payload[toolresult.MetadataPartialFailureKey] != true {
		t.Fatalf("expected partial_failure=true, got %#v", payload[toolresult.MetadataPartialFailureKey])
	}
	if payload[toolresult.MetadataRequestedCountKey] != 2 {
		t.Fatalf("expected requested_count=2, got %#v", payload[toolresult.MetadataRequestedCountKey])
	}
	if payload[toolresult.MetadataFailedCountKey] != 1 {
		t.Fatalf("expected failed_count=1, got %#v", payload[toolresult.MetadataFailedCountKey])
	}
	if payload[toolresult.MetadataSucceededCountKey] != 1 {
		t.Fatalf("expected succeeded_count=1, got %#v", payload[toolresult.MetadataSucceededCountKey])
	}
	rawItems, ok := payload[toolresult.MetadataFailedItemsKey].([]map[string]interface{})
	if !ok || len(rawItems) == 0 {
		t.Fatalf("expected failed_items on payload, got %#v", payload[toolresult.MetadataFailedItemsKey])
	}
	if got := fmt.Sprint(rawItems[0]["path"]); got != "missing.go" {
		t.Fatalf("expected failed path missing.go, got %#v", rawItems[0])
	}
	if got := strings.TrimSpace(fmt.Sprint(payload[toolresult.MetadataNextActionKey])); got == "" || got == "<nil>" {
		t.Fatalf("expected partial next_action, got %#v", payload[toolresult.MetadataNextActionKey])
	}
}

func TestToolCompletedEventPayloadDerivesPartialFromNestedToolMetadata(t *testing.T) {
	// Regression for live smoke: nested toolkit batch stats were copied onto the
	// chat-log payload (partial_failure/counts) while outcome stayed success
	// because Diagnose could not read nested integer counts.
	payload := toolCompletedEventPayload(toolExecutionResult{
		Call: types.ToolCall{
			ID:   "call-nested-partial-view",
			Name: "view",
			Args: map[string]interface{}{
				"files": []interface{}{
					map[string]interface{}{"file_path": "a.go"},
					map[string]interface{}{"file_path": "missing.go"},
				},
			},
		},
		Output: "===== a.go =====\nok\n\n===== errors =====\nmissing.go: missing",
		Envelope: &output.Envelope{
			Metadata: map[string]interface{}{
				"tool_metadata": map[string]interface{}{
					"batch":           true,
					"request_count":   2,
					"succeeded_count": 1,
					"failed_count":    1,
					"partial_failure": true,
					toolresult.MetadataFailedItemsKey: []map[string]interface{}{
						toolresult.FailedItemMap(toolresult.IntPtr(1), "missing.go", "missing.go", "path does not exist"),
					},
				},
			},
		},
	}, 1, "trace-nested-partial", nil)

	if payload[toolresult.MetadataOKKey] != true {
		t.Fatalf("expected ok=true, got %#v", payload)
	}
	if payload[toolresult.MetadataOutcomeKey] != toolresult.OutcomePartial {
		t.Fatalf("expected outcome=partial from nested tool_metadata, got %#v", payload[toolresult.MetadataOutcomeKey])
	}
	if payload[toolresult.MetadataPartialFailureKey] != true {
		t.Fatalf("expected partial_failure=true, got %#v", payload[toolresult.MetadataPartialFailureKey])
	}
	if payload[toolresult.MetadataRequestedCountKey] != 2 ||
		payload[toolresult.MetadataFailedCountKey] != 1 ||
		payload[toolresult.MetadataSucceededCountKey] != 1 {
		t.Fatalf("expected nested counts on payload, got %#v", payload)
	}
	if got := strings.TrimSpace(fmt.Sprint(payload[toolresult.MetadataNextActionKey])); got == "" || got == "<nil>" {
		t.Fatalf("expected partial next_action, got %#v", payload[toolresult.MetadataNextActionKey])
	}
}

func TestToolCompletedEventPayloadExportsOrdinarySuccessOutcome(t *testing.T) {
	payload := toolCompletedEventPayload(toolExecutionResult{
		Call: types.ToolCall{
			ID:   "call-success",
			Name: "ls",
			Args: map[string]interface{}{"path": "."},
		},
		Output: "main.go",
		Envelope: &output.Envelope{
			Metadata: map[string]interface{}{
				toolresult.MetadataOKKey: true,
				"file_count":             1,
			},
		},
	}, 1, "trace-success", nil)

	if payload[toolresult.MetadataOKKey] != true {
		t.Fatalf("expected ok=true, got %#v", payload)
	}
	if payload[toolresult.MetadataOutcomeKey] != toolresult.OutcomeSuccess {
		t.Fatalf("expected outcome=success, got %#v", payload[toolresult.MetadataOutcomeKey])
	}
	if payload[toolresult.MetadataEmptyResultKey] == true {
		t.Fatalf("ordinary success must not set empty_result: %#v", payload)
	}
	if _, exists := payload[toolresult.MetadataFailedItemsKey]; exists {
		t.Fatalf("ordinary success must not set failed_items: %#v", payload)
	}
}

func TestFinalizeDeniedToolResultEmitsCompletedWithFailedOutcome(t *testing.T) {
	agent := &Agent{config: &Config{Name: "test-agent", Model: "test-model"}}
	bus := runtimeevents.NewBus()
	var completed []runtimeevents.Event
	bus.Subscribe("tool.completed", func(event runtimeevents.Event) {
		completed = append(completed, event)
	})
	var reduced []runtimeevents.Event
	bus.Subscribe("tool.reduced", func(event runtimeevents.Event) {
		reduced = append(reduced, event)
	})
	agent.SetEventBus(bus)

	loop := NewReActLoop(agent, nil, &LoopReActConfig{MaxSteps: 1, EnableToolCalls: true})
	gateway := output.NewGateway(nil)
	tc := types.ToolCall{ID: "call-denied", Name: "write_file", Args: map[string]interface{}{"path": "x.txt"}}
	result := toolExecutionResult{Call: tc, Error: "read-only policy blocks write-like tool"}
	metadata := map[string]interface{}{}

	got := loop.finalizeDeniedToolResult(context.Background(), gateway, "session-denied", tc, 1, "trace-denied", result, metadata, nil)
	if got.Envelope == nil {
		t.Fatal("expected envelope after denied finalize")
	}
	if len(completed) != 1 {
		t.Fatalf("expected 1 tool.completed, got %d", len(completed))
	}
	payload := completed[0].Payload
	if payload["denied"] != true {
		t.Fatalf("expected denied=true, got %#v", payload)
	}
	if payload[toolresult.MetadataOutcomeKey] != toolresult.OutcomeFailed {
		t.Fatalf("expected outcome=failed, got %#v", payload[toolresult.MetadataOutcomeKey])
	}
	if payload[toolresult.MetadataOKKey] != false {
		t.Fatalf("expected ok=false, got %#v", payload[toolresult.MetadataOKKey])
	}
	if len(reduced) != 1 {
		t.Fatalf("expected 1 tool.reduced, got %d", len(reduced))
	}
}

// TestToolCompletedEventPayloadPromotesEnvelopeDuration 锁定耗时同源：事件载荷
// 缺失 duration_ms 时提升 tool_metadata.duration_ms。实时标题（bridge 编码）
// 与事件日志/重放投影共用该字段，bridge 对已有值不再按墙钟覆盖，因此 live 与
// replay 会显示同一个可复现的耗时。
func TestToolCompletedEventPayloadPromotesEnvelopeDuration(t *testing.T) {
	payload := toolCompletedEventPayload(toolExecutionResult{
		Call: types.ToolCall{
			ID:   "call-shell-duration",
			Name: "shell",
			Args: map[string]interface{}{"command": "echo hi"},
		},
		Envelope: &output.Envelope{
			Metadata: map[string]interface{}{
				"tool_metadata": map[string]interface{}{
					"command":     "echo hi",
					"duration_ms": 602,
				},
			},
		},
	}, 1, "trace-duration", nil)

	if got := payload["duration_ms"]; got != 602 {
		t.Fatalf("duration_ms = %#v, want promoted envelope duration 602", got)
	}
}

// TestToolCompletedEventPayloadKeepsExplicitDuration 显式传入的 duration_ms
// （最具体的调用方口径）优先于信封提升值。
func TestToolCompletedEventPayloadKeepsExplicitDuration(t *testing.T) {
	payload := toolCompletedEventPayload(toolExecutionResult{
		Call: types.ToolCall{ID: "call-shell-explicit", Name: "shell"},
		Envelope: &output.Envelope{
			Metadata: map[string]interface{}{
				"tool_metadata": map[string]interface{}{"duration_ms": 602},
			},
		},
	}, 1, "trace-duration", map[string]interface{}{"duration_ms": 900})

	if got := payload["duration_ms"]; got != 900 {
		t.Fatalf("duration_ms = %#v, want explicit 900", got)
	}
}
