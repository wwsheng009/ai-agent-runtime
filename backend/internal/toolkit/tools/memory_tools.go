package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/memoryredact"
	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolctx"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

type memoryTool struct {
	*toolkit.BaseTool
	cfg       *runtimecfg.RuntimeConfig
	operation string
	write     bool
}

func newMemoryTool(cfg *runtimecfg.RuntimeConfig, operation, name, description string, parameters map[string]interface{}, write bool) *memoryTool {
	return &memoryTool{
		BaseTool: toolkit.NewBaseTool(name, description, "1.0.0", parameters, true),
		cfg:      cfg, operation: operation, write: write,
	}
}

func (t *memoryTool) DefinitionMetadata() map[string]interface{} {
	kind := runtimetypes.ToolKindRead
	if t.operation == "search" || t.operation == "why_not" {
		kind = runtimetypes.ToolKindSearch
	}
	if t.write {
		kind = runtimetypes.ToolKindEdit
	}
	return map[string]interface{}{
		runtimetypes.ToolMetadataKindKey:        kind,
		runtimetypes.ToolMetadataReadOnlyKey:    !t.write,
		runtimetypes.ToolMetadataMutatesFSKey:   false,
		runtimetypes.ToolMetadataRequiresNetKey: false,
		// memory_read records a best-effort usage row (SQLite write), so keep
		// it out of parallel read waves even though it is a read operation.
		runtimetypes.ToolMetadataSupportsParallelKey: !t.write && t.operation != "read",
	}
}

func (t *memoryTool) Execute(ctx context.Context, params map[string]interface{}) (*toolkit.ToolResult, error) {
	store, err := t.openStore()
	if err != nil {
		return memoryToolError(err), nil
	}
	defer store.Close()
	switch t.operation {
	case "search":
		return t.search(ctx, store, params)
	case "read":
		return t.read(ctx, store, params)
	case "session_context":
		return t.sessionContext(ctx, store, params)
	case "recent":
		return t.recent(ctx, store, params)
	case "stats":
		if t.cfg != nil && strings.EqualFold(strings.TrimSpace(t.cfg.Memory.Mode), "off") {
			return memoryToolError(fmt.Errorf("memory mode is off")), nil
		}
		report, err := store.Status(ctx)
		if err != nil && !report.Fallback {
			return memoryToolError(err), nil
		}
		return memoryToolJSON(report), nil
	case "add":
		return t.add(ctx, store, params)
	case "correct":
		return t.correct(ctx, store, params)
	case "forget":
		return t.forget(ctx, store, params)
	case "why":
		return t.why(ctx, store, params)
	case "why_not":
		return t.whyNot(ctx, store, params)
	default:
		return memoryToolError(fmt.Errorf("unsupported memory operation %q", t.operation)), nil
	}
}

func (t *memoryTool) openStore() (*memorystore.Store, error) {
	if t.cfg == nil {
		return nil, fmt.Errorf("memory tools: runtime config is nil")
	}
	workspace := strings.TrimSpace(t.cfg.Workspace.Root)
	if workspace == "" {
		if cwd, err := os.Getwd(); err == nil {
			workspace = cwd
		}
	}
	if workspace == "" {
		return nil, fmt.Errorf("memory tools: workspace is not configured")
	}
	mode := strings.ToLower(strings.TrimSpace(t.cfg.Memory.Mode))
	if mode == "" {
		mode = "off"
	}
	return memorystore.New(memorystore.Config{
		ProjectRoot: workspace, Mode: mode, StorePath: t.cfg.Memory.StorePath,
		InjectionMode:         t.cfg.Memory.Injection.Mode,
		PinnedBudgetRatio:     t.cfg.Memory.Injection.PinnedBudgetRatio,
		DisableStageSplit:     !t.cfg.Memory.Injection.StageSplit,
		DisableInjection:      !t.cfg.Memory.Injection.Enabled,
		DisableManual:         !t.cfg.Memory.Manual.Enabled,
		RecallTraceEnabled:    t.cfg.Memory.Injection.RecallTrace.Enabled,
		RecallTraceSampleRate: t.cfg.Memory.Injection.RecallTrace.SampleRate,
	})
}

func (t *memoryTool) search(ctx context.Context, store *memorystore.Store, params map[string]interface{}) (*toolkit.ToolResult, error) {
	limit := memoryBoundedIntParam(params, "limit", 20, 100)
	status := strings.ToLower(strings.TrimSpace(memoryStringParam(params, "status")))
	if status == "" {
		status = string(memorystore.StatusActive)
	}
	if status != string(memorystore.StatusActive) && status != string(memorystore.StatusCandidate) {
		return memoryToolError(fmt.Errorf("agent memory_search only exposes active or candidate records")), nil
	}
	page, err := store.ListRecords(ctx, memorystore.RecordFilter{
		Status: memorystore.Status(status),
		Kind:   strings.TrimSpace(memoryStringParam(params, "kind")),
		Tag:    strings.TrimSpace(memoryStringParam(params, "tag")),
		Query:  strings.TrimSpace(memoryStringParam(params, "query")),
		Limit:  limit,
	})
	if err != nil {
		return memoryToolError(err), nil
	}
	page.Items = agentVisibleMemoryRecords(page.Items)
	page.Count = len(page.Items)
	return memoryToolJSON(map[string]interface{}{
		"items": page.Items, "count": page.Count, "next_cursor": page.NextCursor,
		"diagnostics": map[string]interface{}{"query": memoryStringParam(params, "query"), "limit": limit},
	}), nil
}

func (t *memoryTool) read(ctx context.Context, store *memorystore.Store, params map[string]interface{}) (*toolkit.ToolResult, error) {
	id := strings.TrimSpace(memoryStringParam(params, "id"))
	if id == "" {
		return memoryToolError(fmt.Errorf("id is required")), nil
	}
	record, err := store.GetRecord(ctx, id)
	if err != nil {
		return memoryToolError(err), nil
	}
	if record.Sensitivity == "private" {
		return memoryToolError(fmt.Errorf("private memory requires admin authorization")), nil
	}
	if memoryredact.DetectSecret(record.Text) != "" {
		return memoryToolError(fmt.Errorf("secret-shaped memory is not readable through agent tools")), nil
	}
	if record.Status == memorystore.StatusDeleted {
		return memoryToolError(fmt.Errorf("deleted memory is not readable through agent tools")), nil
	}
	usage, _ := store.ListUsage(ctx, memorystore.UsageFilter{MemoryID: id, Limit: 20})
	if sessionID := strings.TrimSpace(toolctx.SessionID(ctx)); sessionID != "" {
		_, _ = store.RecordExposure(ctx, memorystore.Exposure{
			MemoryIDs: []string{id}, SessionID: sessionID, UseKind: memorystore.UsageRead,
		})
	}
	return memoryToolJSON(map[string]interface{}{"record": record, "usage": usage}), nil
}

func (t *memoryTool) sessionContext(ctx context.Context, store *memorystore.Store, params map[string]interface{}) (*toolkit.ToolResult, error) {
	sessionID := strings.TrimSpace(memoryStringParam(params, "session_id"))
	if sessionID == "" {
		return memoryToolError(fmt.Errorf("session_id is required")), nil
	}
	currentSessionID := strings.TrimSpace(toolctx.SessionID(ctx))
	if currentSessionID == "" {
		return memoryToolError(fmt.Errorf("session context requires an active session")), nil
	}
	if sessionID != currentSessionID {
		return memoryToolError(fmt.Errorf("cross-session memory context requires admin authorization")), nil
	}
	limit := memoryBoundedIntParam(params, "limit", 20, 100)
	page, err := store.ListRecords(ctx, memorystore.RecordFilter{SourceSessionID: sessionID, Limit: limit})
	if err != nil {
		return memoryToolError(err), nil
	}
	page.Items = agentVisibleMemoryRecords(page.Items)
	page.Count = len(page.Items)
	return memoryToolJSON(map[string]interface{}{"session_id": sessionID, "items": page.Items, "count": page.Count}), nil
}

func (t *memoryTool) recent(ctx context.Context, store *memorystore.Store, params map[string]interface{}) (*toolkit.ToolResult, error) {
	limit := memoryBoundedIntParam(params, "limit", 20, 100)
	page, err := store.ListRecords(ctx, memorystore.RecordFilter{
		Status: memorystore.StatusActive, Kind: strings.TrimSpace(memoryStringParam(params, "kind")), Limit: limit,
	})
	if err != nil {
		return memoryToolError(err), nil
	}
	page.Items = agentVisibleMemoryRecords(page.Items)
	page.Count = len(page.Items)
	return memoryToolJSON(map[string]interface{}{"items": page.Items, "count": page.Count}), nil
}

func (t *memoryTool) add(ctx context.Context, store *memorystore.Store, params map[string]interface{}) (*toolkit.ToolResult, error) {
	text := strings.TrimSpace(memoryStringParam(params, "text"))
	if text == "" {
		return memoryToolError(fmt.Errorf("text is required")), nil
	}
	var pinned *bool
	if value, ok := params["pinned"].(bool); ok {
		pinned = &value
	}
	note, err := store.Append(memorystore.AppendNoteOptions{
		Text: text, Kind: memoryStringParam(params, "kind"), Tags: memoryStringSliceParam(params, "tags"),
		Pinned: pinned, Source: "manual", SessionID: strings.TrimSpace(toolctx.SessionID(ctx)),
	})
	if err != nil {
		return memoryToolError(err), nil
	}
	return memoryToolJSON(note), nil
}

func (t *memoryTool) correct(ctx context.Context, store *memorystore.Store, params map[string]interface{}) (*toolkit.ToolResult, error) {
	if t.cfg != nil && !t.cfg.Memory.Tools.AllowCorrect {
		return memoryToolError(fmt.Errorf("memory_correct is disabled by memory.tools.allowCorrect")), nil
	}
	id := strings.TrimSpace(memoryStringParam(params, "id"))
	text := strings.TrimSpace(memoryStringParam(params, "text"))
	if id == "" || text == "" {
		return memoryToolError(fmt.Errorf("id and text are required")), nil
	}
	record, err := store.CorrectRecord(ctx, id, text)
	if err != nil {
		return memoryToolError(err), nil
	}
	return memoryToolJSON(record), nil
}

func (t *memoryTool) forget(ctx context.Context, store *memorystore.Store, params map[string]interface{}) (*toolkit.ToolResult, error) {
	if t.cfg != nil && !t.cfg.Memory.Tools.AllowForget {
		return memoryToolError(fmt.Errorf("memory_forget is disabled by memory.tools.allowForget")), nil
	}
	confirm, _ := params["confirm"].(bool)
	if !confirm {
		return memoryToolError(fmt.Errorf("memory_forget requires confirm=true")), nil
	}
	id := strings.TrimSpace(memoryStringParam(params, "id"))
	if id == "" {
		return memoryToolError(fmt.Errorf("id is required")), nil
	}
	if err := store.DeleteRecord(ctx, id); err != nil {
		return memoryToolError(err), nil
	}
	return memoryToolJSON(map[string]interface{}{"ok": true, "id": id}), nil
}

func (t *memoryTool) why(ctx context.Context, store *memorystore.Store, params map[string]interface{}) (*toolkit.ToolResult, error) {
	id := strings.TrimSpace(memoryStringParam(params, "id"))
	if id == "" {
		return memoryToolError(fmt.Errorf("id is required")), nil
	}
	record, err := store.GetRecord(ctx, id)
	if err != nil {
		return memoryToolError(err), nil
	}
	if record.Sensitivity == "private" {
		return memoryToolError(fmt.Errorf("private memory requires admin authorization")), nil
	}
	if memoryredact.DetectSecret(record.Text) != "" {
		return memoryToolError(fmt.Errorf("secret-shaped memory is not readable through agent tools")), nil
	}
	if record.Status == memorystore.StatusDeleted {
		return memoryToolError(fmt.Errorf("deleted memory is not readable through agent tools")), nil
	}
	usage, _ := store.ListUsage(ctx, memorystore.UsageFilter{MemoryID: id, Limit: 50})
	return memoryToolJSON(map[string]interface{}{"record": record, "usage": usage}), nil
}

func (t *memoryTool) whyNot(ctx context.Context, store *memorystore.Store, params map[string]interface{}) (*toolkit.ToolResult, error) {
	selection, err := store.SelectForInjectV2(memorystore.InjectOptions{
		Query: strings.TrimSpace(memoryStringParam(params, "query")), Limit: memoryBoundedIntParam(params, "limit", 5, 20),
		TokenBudget: memoryBoundedIntParam(params, "token_budget", 0, 4000),
		SessionID:   strings.TrimSpace(toolctx.SessionID(ctx)),
		SkipTrace:   true,
	})
	if err != nil {
		return memoryToolError(err), nil
	}
	return memoryToolJSON(map[string]interface{}{
		"selected_ids": selection.IDs, "recall_count": len(selection.Recall),
		"summary_revision": selection.Revision, "token_estimate": selection.TokenEstimate,
		"dropped": selection.Dropped,
	}), nil
}

func memoryToolJSON(value interface{}) *toolkit.ToolResult {
	body, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return memoryToolError(err)
	}
	return &toolkit.ToolResult{Success: true, OutputKind: toolresult.KindText, Content: string(body)}
}

func agentVisibleMemoryRecords(records []memorystore.Record) []memorystore.Record {
	out := make([]memorystore.Record, 0, len(records))
	for _, record := range records {
		if record.Sensitivity == "private" || memoryredact.DetectSecret(record.Text) != "" {
			continue
		}
		out = append(out, record)
	}
	return out
}

func memoryToolError(err error) *toolkit.ToolResult {
	if err == nil {
		err = fmt.Errorf("memory tool failed")
	}
	return &toolkit.ToolResult{
		Success: false, OutputKind: toolresult.KindText,
		Error: err, Content: `{"error":` + strconvQuote(err.Error()) + `}`,
	}
}

func strconvQuote(value string) string {
	body, _ := json.Marshal(value)
	return string(body)
}

func memoryStringParam(params map[string]interface{}, key string) string {
	value, _ := params[key].(string)
	return value
}

func memoryIntParam(params map[string]interface{}, key string) int {
	switch value := params[key].(type) {
	case float64:
		return int(value)
	case int:
		return value
	case int64:
		return int(value)
	case json.Number:
		parsed, _ := value.Int64()
		return int(parsed)
	case string:
		var parsed int
		_, _ = fmt.Sscanf(strings.TrimSpace(value), "%d", &parsed)
		return parsed
	default:
		return 0
	}
}

func memoryBoundedIntParam(params map[string]interface{}, key string, fallback, max int) int {
	value := memoryIntParam(params, key)
	if value <= 0 {
		value = fallback
	}
	if max > 0 && value > max {
		value = max
	}
	return value
}

func memoryStringSliceParam(params map[string]interface{}, key string) []string {
	raw, ok := params[key].([]interface{})
	if !ok {
		if values, ok := params[key].([]string); ok {
			return append([]string(nil), values...)
		}
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, value := range raw {
		if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
			out = append(out, strings.TrimSpace(text))
		}
	}
	return out
}
