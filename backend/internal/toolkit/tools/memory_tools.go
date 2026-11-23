package tools

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

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

type memoryHistoryRecordView struct {
	ID         string             `json:"id"`
	Kind       string             `json:"kind"`
	Status     memorystore.Status `json:"status"`
	Text       string             `json:"text"`
	Title      string             `json:"title,omitempty"`
	Summary    string             `json:"summary,omitempty"`
	Revision   int64              `json:"revision"`
	ValidFrom  time.Time          `json:"valid_from,omitempty"`
	ValidUntil time.Time          `json:"valid_until,omitempty"`
	Archived   bool               `json:"archived"`
}

type memoryHistoryVersionView struct {
	Version      int64                   `json:"version"`
	ChangedBy    string                  `json:"changed_by"`
	ChangeReason string                  `json:"change_reason"`
	ChangeType   string                  `json:"change_type"`
	ValidFrom    time.Time               `json:"valid_from,omitempty"`
	ValidUntil   time.Time               `json:"valid_until,omitempty"`
	CreatedAt    time.Time               `json:"created_at"`
	Record       memoryHistoryRecordView `json:"record"`
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
	if t.operation == "rollback" && !memoryRollbackToolEnabled(t.cfg) {
		return memoryToolError(fmt.Errorf("memory_rollback is disabled; enable memory.mode=on, memory.tools.enabled, and memory.tools.allowRollback")), nil
	}
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
	case "core_get":
		return t.coreGet(ctx, store)
	case "add":
		return t.add(ctx, store, params)
	case "pin":
		return t.pin(ctx, store, params)
	case "correct":
		return t.correct(ctx, store, params)
	case "forget":
		return t.forget(ctx, store, params)
	case "why":
		return t.why(ctx, store, params)
	case "why_not":
		return t.whyNot(ctx, store, params)
	case "history":
		return t.history(ctx, store, params)
	case "rollback":
		return t.rollback(ctx, store, params)
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
	staleBehavior := "exclude"
	if t.cfg.Memory.Freshness.Enabled &&
		strings.EqualFold(strings.TrimSpace(t.cfg.Memory.Freshness.StaleBehavior), "warn") {
		staleBehavior = "warn"
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
		StaleBehavior:         staleBehavior,
		DisableSecretFilter:   !t.cfg.Memory.Redaction.Enabled,
		TurnDigestBatchSize:   t.cfg.Memory.Generation.TurnDigest.MaxTurnsPerBatch,
	})
}

func (t *memoryTool) search(ctx context.Context, store *memorystore.Store, params map[string]interface{}) (*toolkit.ToolResult, error) {
	limit := memoryBoundedIntParam(params, "limit", 20, 100)
	statuses := memoryStringSliceParam(params, "statuses")
	if len(statuses) == 0 {
		status := strings.ToLower(strings.TrimSpace(memoryStringParam(params, "status")))
		if status == "" {
			status = string(memorystore.StatusActive)
		}
		statuses = []string{status}
	}
	normalizedStatuses := make([]memorystore.Status, 0, len(statuses))
	for _, status := range statuses {
		status = strings.ToLower(strings.TrimSpace(status))
		if status != string(memorystore.StatusActive) && status != string(memorystore.StatusCandidate) {
			return memoryToolError(fmt.Errorf("agent memory_search only exposes active or candidate records")), nil
		}
		normalizedStatuses = append(normalizedStatuses, memorystore.Status(status))
	}
	kinds := memoryStringSliceParam(params, "kinds")
	if len(kinds) == 0 {
		if kind := strings.TrimSpace(memoryStringParam(params, "kind")); kind != "" {
			kinds = []string{kind}
		}
	}
	tags := memoryStringSliceParam(params, "tags")
	if len(tags) == 0 {
		if tag := strings.TrimSpace(memoryStringParam(params, "tag")); tag != "" {
			tags = []string{tag}
		}
	}
	result, err := store.SearchRecords(ctx, memorystore.RecordSearchQuery{
		Statuses:   normalizedStatuses,
		Kinds:      kinds,
		Tags:       tags,
		Text:       strings.TrimSpace(memoryStringParam(params, "query")),
		SourceKind: strings.TrimSpace(memoryStringParam(params, "source_kind")),
		Limit:      limit,
	})
	if err != nil {
		return memoryToolError(err), nil
	}
	records := make([]memorystore.Record, 0, len(result.Items))
	for _, hit := range result.Items {
		records = append(records, hit.Record)
	}
	records = agentVisibleMemoryRecords(records)
	return memoryToolJSON(map[string]interface{}{
		"items": records, "count": len(records), "next_cursor": result.NextCursor,
		"diagnostics": map[string]interface{}{
			"query": memoryStringParam(params, "query"), "limit": limit,
			"search_backend": result.Backend, "statuses": statuses,
			"kinds": kinds, "tags": tags,
		},
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
	if visibilityErr := agentMemoryVisibilityError(record); visibilityErr != nil {
		return memoryToolError(visibilityErr), nil
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

func (t *memoryTool) coreGet(ctx context.Context, store *memorystore.Store) (*toolkit.ToolResult, error) {
	core, err := store.GetCore(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return memoryToolJSON(map[string]interface{}{"core": nil, "status": "not_found"}), nil
	}
	if err != nil {
		return memoryToolError(err), nil
	}
	if sessionID := strings.TrimSpace(toolctx.SessionID(ctx)); sessionID != "" {
		// Usage is telemetry only. A transient write failure must not turn a
		// successful read of reviewed Core into a failed tool call.
		_, _ = store.RecordCoreExposure(ctx, memorystore.CoreExposure{
			CoreIDs: []string{core.ID}, SessionID: sessionID,
			TurnID: toolctx.TurnID(ctx), MessageID: toolctx.ToolCallID(ctx),
			UseKind: memorystore.UsageRead,
		})
	}
	return memoryToolJSON(map[string]interface{}{"core": core, "status": memorystore.CoreStatusActive}), nil
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
		Text: text, Title: memoryStringParam(params, "title"), Summary: memoryStringParam(params, "summary"),
		CanonicalID: memoryStringParam(params, "canonical_id"),
		Kind:        memoryStringParam(params, "kind"), Tags: memoryStringSliceParam(params, "tags"),
		Pinned: pinned, Source: "manual", SessionID: strings.TrimSpace(toolctx.SessionID(ctx)),
	})
	if err != nil {
		return memoryToolError(err), nil
	}
	return memoryToolJSON(note), nil
}

func (t *memoryTool) pin(ctx context.Context, store *memorystore.Store, params map[string]interface{}) (*toolkit.ToolResult, error) {
	id := strings.TrimSpace(memoryStringParam(params, "id"))
	if id == "" {
		return memoryToolError(fmt.Errorf("id is required")), nil
	}
	record, err := store.GetRecord(ctx, id)
	if err != nil {
		return memoryToolError(err), nil
	}
	if visibilityErr := agentMemoryVisibilityError(record); visibilityErr != nil {
		return memoryToolError(visibilityErr), nil
	}
	pinned := true
	if value, ok := params["pinned"].(bool); ok {
		pinned = value
	}
	record, err = store.UpdateRecord(ctx, id, memorystore.RecordPatch{Pinned: &pinned})
	if err != nil {
		return memoryToolError(err), nil
	}
	return memoryToolJSON(record), nil
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
	if visibilityErr := agentMemoryVisibilityError(record); visibilityErr != nil {
		return memoryToolError(visibilityErr), nil
	}
	usage, _ := store.ListUsage(ctx, memorystore.UsageFilter{MemoryID: id, Limit: 50})
	return memoryToolJSON(map[string]interface{}{"record": record, "usage": usage}), nil
}

func (t *memoryTool) whyNot(ctx context.Context, store *memorystore.Store, params map[string]interface{}) (*toolkit.ToolResult, error) {
	selection, err := store.DiagnoseInjection(memorystore.InjectOptions{
		Query: strings.TrimSpace(memoryStringParam(params, "query")), Limit: memoryBoundedIntParam(params, "limit", 5, 20),
		TokenBudget: memoryBoundedIntParam(params, "token_budget", 0, 4000),
		SessionID:   strings.TrimSpace(toolctx.SessionID(ctx)),
		SkipTrace:   true,
	})
	if err != nil {
		return memoryToolError(err), nil
	}
	dropped := make([]memorystore.DroppedRecord, 0, len(selection.Dropped))
	for _, item := range selection.Dropped {
		if item.Reason == "archived" {
			continue
		}
		dropped = append(dropped, item)
	}
	return memoryToolJSON(map[string]interface{}{
		"selected_ids": selection.IDs, "recall_count": len(selection.Recall),
		"summary_revision": selection.Revision, "token_estimate": selection.TokenEstimate,
		"dropped": dropped,
	}), nil
}

func (t *memoryTool) history(ctx context.Context, store *memorystore.Store, params map[string]interface{}) (*toolkit.ToolResult, error) {
	id := strings.TrimSpace(memoryStringParam(params, "id"))
	if id == "" {
		return memoryToolError(fmt.Errorf("id is required")), nil
	}
	current, err := store.GetRecord(ctx, id)
	if err != nil {
		return memoryToolError(err), nil
	}
	if visibilityErr := agentMemoryVisibilityError(current); visibilityErr != nil {
		return memoryToolError(visibilityErr), nil
	}
	sessionID := strings.TrimSpace(toolctx.SessionID(ctx))
	if sessionID == "" {
		return memoryToolError(fmt.Errorf("memory history requires an active session")), nil
	}
	if sourceSessionID := strings.TrimSpace(current.SourceSessionID); sourceSessionID != "" && sourceSessionID != sessionID {
		return memoryToolError(fmt.Errorf("cross-session memory history requires admin authorization")), nil
	}
	limit := memoryBoundedIntParam(params, "limit", 50, 100)
	versions, hasMore, err := store.ListRecordVersionsPage(ctx, id, limit)
	if err != nil {
		return memoryToolError(err), nil
	}
	views := make([]memoryHistoryVersionView, 0, len(versions))
	for _, version := range versions {
		record := version.Record
		if version.MemoryID != id || version.ProjectKey != store.ProjectKey() ||
			version.ScopeKind != memorystore.ScopeProject ||
			record.ID != id || record.Revision != version.Version ||
			record.ProjectKey != store.ProjectKey() || record.Scope != memorystore.ScopeProject {
			return memoryToolError(fmt.Errorf("inconsistent record history")), nil
		}
		if sourceSessionID := strings.TrimSpace(record.SourceSessionID); sourceSessionID != "" && sourceSessionID != sessionID {
			return memoryToolError(fmt.Errorf("cross-session memory history requires admin authorization")), nil
		}
		if record.Sensitivity == "private" || record.Archived ||
			record.Status == memorystore.StatusDeleted ||
			memoryredact.DetectSecret(record.Text) != "" {
			return memoryToolError(fmt.Errorf("historical memory contains restricted content")), nil
		}
		views = append(views, memoryHistoryVersionView{
			Version: version.Version, ChangedBy: memoryHistorySafeText(version.ChangedBy, 120),
			ChangeReason: memoryHistorySafeText(version.ChangeReason, 500),
			ChangeType:   memoryHistorySafeText(version.ChangeType, 80),
			ValidFrom:    version.ValidFrom, ValidUntil: version.ValidUntil, CreatedAt: version.CreatedAt,
			Record: memoryHistoryRecordView{
				ID: memoryHistorySafeText(record.ID, 120), Kind: memoryHistorySafeText(record.Kind, 80), Status: record.Status,
				Text: memoryHistorySafeText(record.Text, 2000), Title: memoryHistorySafeText(record.Title, 200),
				Summary: memoryHistorySafeText(record.Summary, 500), Revision: record.Revision,
				ValidFrom: record.ValidFrom, ValidUntil: record.ValidUntil, Archived: record.Archived,
			},
		})
	}
	return memoryToolJSON(map[string]interface{}{
		"id": memoryHistorySafeText(id, 120), "versions": views, "count": len(views), "has_more": hasMore,
	}), nil
}

func (t *memoryTool) rollback(ctx context.Context, store *memorystore.Store, params map[string]interface{}) (*toolkit.ToolResult, error) {
	if !memoryRollbackToolEnabled(t.cfg) {
		return memoryToolError(fmt.Errorf("memory_rollback is disabled; enable memory.mode=on, memory.tools.enabled, and memory.tools.allowRollback")), nil
	}
	confirm, _ := params["confirm"].(bool)
	if !confirm {
		return memoryToolError(fmt.Errorf("memory_rollback requires confirm=true")), nil
	}
	id := strings.TrimSpace(memoryStringParam(params, "id"))
	if id == "" {
		return memoryToolError(fmt.Errorf("id is required")), nil
	}
	targetVersion, err := memoryRequiredInt64Param(params, "target_version")
	if err != nil {
		return memoryToolError(err), nil
	}
	expectedRevision, err := memoryRequiredInt64Param(params, "expected_revision")
	if err != nil {
		return memoryToolError(err), nil
	}
	reason := strings.TrimSpace(memoryStringParam(params, "reason"))
	if reason == "" {
		return memoryToolError(fmt.Errorf("reason is required")), nil
	}
	revertSupersede, _ := params["revert_supersede"].(bool)
	sessionID := strings.TrimSpace(toolctx.SessionID(ctx))
	if sessionID == "" {
		return memoryToolError(fmt.Errorf("memory rollback requires an active session")), nil
	}

	current, err := store.GetRecord(ctx, id)
	if err != nil {
		return memoryToolError(err), nil
	}
	if visibilityErr := agentMemoryVisibilityError(current); visibilityErr != nil {
		return memoryToolError(visibilityErr), nil
	}
	if memoryRecordContainsSecret(current) {
		return memoryToolError(fmt.Errorf("memory rollback is not allowed for secret-shaped content")), nil
	}
	if current.ProjectKey != store.ProjectKey() || current.Scope != memorystore.ScopeProject {
		return memoryToolError(fmt.Errorf("memory rollback is limited to the current project")), nil
	}
	if sourceSessionID := strings.TrimSpace(current.SourceSessionID); sourceSessionID != sessionID {
		return memoryToolError(fmt.Errorf("memory rollback requires a record owned by the current session")), nil
	}

	versions, err := store.ListRecordVersions(ctx, id)
	if err != nil {
		return memoryToolError(err), nil
	}
	var target *memorystore.RecordVersion
	for index := range versions {
		if versions[index].Version == targetVersion {
			target = &versions[index]
			break
		}
	}
	if target == nil {
		return memoryToolError(fmt.Errorf("memory rollback target version %d was not found", targetVersion)), nil
	}
	if target.Record.ID != id || target.MemoryID != id ||
		target.Record.Revision != target.Version || target.Record.ProjectKey != store.ProjectKey() ||
		target.Record.Scope != memorystore.ScopeProject ||
		target.ProjectKey != store.ProjectKey() || target.ScopeKind != memorystore.ScopeProject {
		return memoryToolError(fmt.Errorf("inconsistent record history")), nil
	}
	if visibilityErr := agentMemoryVisibilityError(target.Record); visibilityErr != nil {
		return memoryToolError(fmt.Errorf("memory rollback target is not agent-visible: %w", visibilityErr)), nil
	}
	if memoryRecordContainsSecret(target.Record) {
		return memoryToolError(fmt.Errorf("memory rollback target contains secret-shaped content")), nil
	}
	if sourceSessionID := strings.TrimSpace(target.Record.SourceSessionID); sourceSessionID != sessionID {
		return memoryToolError(fmt.Errorf("memory rollback target requires current-session ownership")), nil
	}
	if revertSupersede && current.Status == memorystore.StatusSuperseded &&
		target.Record.Status == memorystore.StatusActive {
		successorID := strings.TrimSpace(current.SupersededBy)
		if successorID == "" {
			return memoryToolError(fmt.Errorf("memory rollback cannot revert supersession without a successor")), nil
		}
		successor, err := store.GetRecord(ctx, successorID)
		if err != nil {
			return memoryToolError(fmt.Errorf("memory rollback successor is unavailable: %w", err)), nil
		}
		if successor.ProjectKey != store.ProjectKey() || successor.Scope != memorystore.ScopeProject ||
			successor.SupersedesID != current.ID || successor.Status != memorystore.StatusActive {
			return memoryToolError(fmt.Errorf("memory rollback successor changed; rollback it separately first")), nil
		}
		if visibilityErr := agentMemoryVisibilityError(successor); visibilityErr != nil {
			return memoryToolError(fmt.Errorf("memory rollback successor is not agent-visible: %w", visibilityErr)), nil
		}
		if memoryRecordContainsSecret(successor) {
			return memoryToolError(fmt.Errorf("memory rollback successor contains secret-shaped content")), nil
		}
		if sourceSessionID := strings.TrimSpace(successor.SourceSessionID); sourceSessionID != sessionID {
			return memoryToolError(fmt.Errorf("memory rollback successor requires current-session ownership")), nil
		}
	}

	restored, err := store.RollbackRecord(ctx, id, memorystore.RecordRollbackOptions{
		TargetVersion:    targetVersion,
		ExpectedRevision: expectedRevision,
		Reason:           reason,
		ChangedBy:        "agent_tool",
		RevertSupersede:  revertSupersede,
	})
	if err != nil {
		return memoryToolError(err), nil
	}
	return memoryToolJSON(map[string]interface{}{
		"ok": true, "id": restored.ID, "revision": restored.Revision, "status": restored.Status,
	}), nil
}

func memoryRollbackToolEnabled(cfg *runtimecfg.RuntimeConfig) bool {
	return cfg != nil && cfg.Memory.Tools.Enabled && cfg.Memory.Tools.AllowRollback &&
		strings.EqualFold(strings.TrimSpace(cfg.Memory.Mode), "on")
}

func memoryRecordContainsSecret(record memorystore.Record) bool {
	fields := []string{record.Text, record.Title, record.Summary, strings.Join(record.Tags, " ")}
	if len(record.Metadata) > 0 {
		metadata, err := json.Marshal(record.Metadata)
		if err != nil {
			return true
		}
		fields = append(fields, string(metadata))
	}
	for _, field := range fields {
		if memoryredact.DetectSecret(field) != "" {
			return true
		}
	}
	return false
}

func memoryToolJSON(value interface{}) *toolkit.ToolResult {
	body, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return memoryToolError(err)
	}
	return &toolkit.ToolResult{Success: true, OutputKind: toolresult.KindText, Content: string(body)}
}

func memoryHistorySafeText(value string, maxRunes int) string {
	value, _ = memoryredact.Redact(strings.TrimSpace(value))
	value = strings.Join(strings.Fields(value), " ")
	if maxRunes > 0 {
		runes := []rune(value)
		if len(runes) > maxRunes {
			value = string(runes[:maxRunes-1]) + "…"
		}
	}
	return value
}

func agentVisibleMemoryRecords(records []memorystore.Record) []memorystore.Record {
	out := make([]memorystore.Record, 0, len(records))
	for _, record := range records {
		if record.Sensitivity == "private" || record.Archived || memoryredact.DetectSecret(record.Text) != "" {
			continue
		}
		out = append(out, record)
	}
	return out
}

func agentMemoryVisibilityError(record memorystore.Record) error {
	if record.Sensitivity == "private" {
		return fmt.Errorf("private memory requires admin authorization")
	}
	if memoryredact.DetectSecret(record.Text) != "" {
		return fmt.Errorf("secret-shaped memory is not readable through agent tools")
	}
	if record.Status == memorystore.StatusDeleted {
		return fmt.Errorf("deleted memory is not readable through agent tools")
	}
	if record.Archived {
		return fmt.Errorf("archived memory is not readable through agent tools")
	}
	return nil
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

func memoryRequiredInt64Param(params map[string]interface{}, key string) (int64, error) {
	value, ok := params[key]
	if !ok || value == nil {
		return 0, fmt.Errorf("%s is required", key)
	}
	switch value := value.(type) {
	case int:
		if value <= 0 {
			return 0, fmt.Errorf("%s must be positive", key)
		}
		return int64(value), nil
	case int8:
		if value <= 0 {
			return 0, fmt.Errorf("%s must be positive", key)
		}
		return int64(value), nil
	case int16:
		if value <= 0 {
			return 0, fmt.Errorf("%s must be positive", key)
		}
		return int64(value), nil
	case int32:
		if value <= 0 {
			return 0, fmt.Errorf("%s must be positive", key)
		}
		return int64(value), nil
	case int64:
		if value <= 0 {
			return 0, fmt.Errorf("%s must be positive", key)
		}
		return value, nil
	case uint:
		if value == 0 || uint64(value) > math.MaxInt64 {
			return 0, fmt.Errorf("%s must be a positive int64", key)
		}
		return int64(value), nil
	case uint8:
		if value == 0 {
			return 0, fmt.Errorf("%s must be positive", key)
		}
		return int64(value), nil
	case uint16:
		if value == 0 {
			return 0, fmt.Errorf("%s must be positive", key)
		}
		return int64(value), nil
	case uint32:
		if value == 0 {
			return 0, fmt.Errorf("%s must be positive", key)
		}
		return int64(value), nil
	case uint64:
		if value == 0 || value > math.MaxInt64 {
			return 0, fmt.Errorf("%s must be a positive int64", key)
		}
		return int64(value), nil
	case float32:
		return memoryFloat64ToInt64(key, float64(value))
	case float64:
		return memoryFloat64ToInt64(key, value)
	case json.Number:
		parsed, err := strconv.ParseInt(string(value), 10, 64)
		if err != nil || parsed <= 0 {
			return 0, fmt.Errorf("%s must be a positive integer", key)
		}
		return parsed, nil
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if err != nil || parsed <= 0 {
			return 0, fmt.Errorf("%s must be a positive integer", key)
		}
		return parsed, nil
	default:
		return 0, fmt.Errorf("%s must be a positive integer", key)
	}
}

func memoryFloat64ToInt64(key string, value float64) (int64, error) {
	if value <= 0 || math.IsNaN(value) || math.IsInf(value, 0) ||
		math.Trunc(value) != value || value > 9007199254740991 {
		return 0, fmt.Errorf("%s must be a positive integer", key)
	}
	return int64(value), nil
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
	if text, ok := params[key].(string); ok {
		out := make([]string, 0, 4)
		for _, value := range strings.Split(text, ",") {
			if value = strings.TrimSpace(value); value != "" {
				out = append(out, value)
			}
		}
		return out
	}
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
