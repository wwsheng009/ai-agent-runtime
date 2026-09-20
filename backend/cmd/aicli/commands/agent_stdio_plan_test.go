package commands

import (
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/acp"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// todosToolFinishedEvent builds the live runtime event shape published when the
// todos tool finishes: payload.protocol_result.metadata.todo_snapshot.
func todosToolFinishedEvent(items []map[string]interface{}) runtimeevents.Event {
	return runtimeevents.Event{
		Type:    runtimechat.EventToolFinished,
		TraceID: "trace-todos",
		Payload: map[string]interface{}{
			"tool_name":    "todos",
			"tool_call_id": "tc_todos",
			"protocol_result": map[string]interface{}{
				"metadata": map[string]interface{}{
					"todo_snapshot": map[string]interface{}{
						"items":      items,
						"session_id": "sess_plan",
					},
				},
			},
		},
	}
}

func planUpdates(updates []acp.SessionUpdate) []acp.SessionUpdate {
	out := make([]acp.SessionUpdate, 0, len(updates))
	for _, update := range updates {
		if update.SessionUpdate == acp.SessionUpdatePlan {
			out = append(out, update)
		}
	}
	return out
}

func TestACPEventBridge_TodosToolEmitsPlan(t *testing.T) {
	t.Parallel()

	bridge := newACPEventBridge("sess_plan")
	emit := &recordingACPEmitter{}
	bridge.BeginPrompt("sess_plan", emit)
	defer bridge.EndPrompt()

	bridge.HandleRuntimeEvent(todosToolFinishedEvent([]map[string]interface{}{
		{"content": "分析需求", "status": "completed", "active_form": "正在分析需求"},
		{"content": "修改实现", "status": "in_progress", "active_form": "正在修改实现"},
		{"content": "运行测试", "status": "pending", "active_form": ""},
	}))

	updates := emit.snapshot()
	plans := planUpdates(updates)
	if len(plans) != 1 {
		t.Fatalf("expected exactly one plan update, got %d: %+v", len(plans), updates)
	}
	if updates[len(updates)-1].SessionUpdate != acp.SessionUpdatePlan {
		t.Fatalf("plan must be the last update, got %q", updates[len(updates)-1].SessionUpdate)
	}
	plan := plans[0]
	want := []acp.PlanEntry{
		{Content: "分析需求", Status: acp.PlanStatusCompleted, Priority: acp.PlanPriorityMedium},
		{Content: "修改实现", Status: acp.PlanStatusInProgress, Priority: acp.PlanPriorityMedium},
		{Content: "运行测试", Status: acp.PlanStatusPending, Priority: acp.PlanPriorityMedium},
	}
	if len(plan.Entries) != len(want) {
		t.Fatalf("entries = %+v, want %+v", plan.Entries, want)
	}
	for i := range want {
		if plan.Entries[i] != want[i] {
			t.Fatalf("entry %d = %+v, want %+v", i, plan.Entries[i], want[i])
		}
	}
}

// A repeated identical snapshot must not re-render the client-side list.
func TestACPEventBridge_TodosPlanDeduplicatesUnchangedSnapshot(t *testing.T) {
	t.Parallel()

	bridge := newACPEventBridge("sess_plan")
	emit := &recordingACPEmitter{}
	bridge.BeginPrompt("sess_plan", emit)
	defer bridge.EndPrompt()

	event := todosToolFinishedEvent([]map[string]interface{}{
		{"content": "分析需求", "status": "in_progress", "active_form": "正在分析需求"},
	})
	bridge.HandleRuntimeEvent(event)
	bridge.HandleRuntimeEvent(event)

	if plans := planUpdates(emit.snapshot()); len(plans) != 1 {
		t.Fatalf("expected 1 plan after duplicate snapshots, got %d", len(plans))
	}
}

func TestACPEventBridge_TodosPlanReemitsOnChange(t *testing.T) {
	t.Parallel()

	bridge := newACPEventBridge("sess_plan")
	emit := &recordingACPEmitter{}
	bridge.BeginPrompt("sess_plan", emit)
	defer bridge.EndPrompt()

	bridge.HandleRuntimeEvent(todosToolFinishedEvent([]map[string]interface{}{
		{"content": "分析需求", "status": "in_progress", "active_form": ""},
	}))
	bridge.HandleRuntimeEvent(todosToolFinishedEvent([]map[string]interface{}{
		{"content": "分析需求", "status": "completed", "active_form": ""},
		{"content": "实施适配", "status": "in_progress", "active_form": ""},
	}))

	plans := planUpdates(emit.snapshot())
	if len(plans) != 2 {
		t.Fatalf("expected 2 plan updates, got %d: %+v", len(plans), plans)
	}
	last := plans[1]
	if len(last.Entries) != 2 || last.Entries[1].Status != acp.PlanStatusInProgress {
		t.Fatalf("last plan = %+v", last.Entries)
	}
}

func TestACPEventBridge_TodosPlanSkipsEmptyOrForeignPayload(t *testing.T) {
	t.Parallel()

	bridge := newACPEventBridge("sess_plan")
	emit := &recordingACPEmitter{}
	bridge.BeginPrompt("sess_plan", emit)
	defer bridge.EndPrompt()

	// Empty snapshot (todos cleared) and a missing snapshot must stay silent.
	bridge.HandleRuntimeEvent(todosToolFinishedEvent(nil))
	// Non-todos tools never carry a task list.
	bridge.HandleRuntimeEvent(runtimeevents.Event{
		Type:    runtimechat.EventToolFinished,
		TraceID: "trace-view",
		Payload: map[string]interface{}{
			"tool_name":    "view",
			"tool_call_id": "tc_view",
			"protocol_result": map[string]interface{}{
				"metadata": map[string]interface{}{
					"todo_snapshot": map[string]interface{}{
						"items": []map[string]interface{}{
							{"content": "误报", "status": "pending"},
						},
					},
				},
			},
		},
	})

	if plans := planUpdates(emit.snapshot()); len(plans) != 0 {
		t.Fatalf("expected no plan updates, got %+v", plans)
	}
}

func TestReplayACPSessionHistory_RestoresLatestTodosPlan(t *testing.T) {
	t.Parallel()

	hostSess := &acpHostSession{
		id: "sess_plan_hist",
		chat: &ChatSession{
			Messages: []runtimetypes.Message{
				{Role: "user", Content: "开始"},
				{Role: "tool", ToolCallID: "call_todos_1", Content: "任务列表更新", Metadata: runtimetypes.Metadata{
					"todos": []map[string]interface{}{
						{"content": "旧任务", "status": "completed"},
					},
				}},
				{Role: "tool", ToolCallID: "call_todos_2", Content: "任务列表更新", Metadata: runtimetypes.Metadata{
					"tool_metadata": map[string]interface{}{
						"todos": []map[string]interface{}{
							{"content": "分析需求", "status": "completed", "active_form": ""},
							{"content": "实施适配", "status": "in_progress", "active_form": "正在实施适配"},
						},
					},
				}},
			},
		},
	}
	emit := &recordingACPEmitter{}
	if err := replayACPSessionHistory("sess_plan_hist", hostSess, emit); err != nil {
		t.Fatalf("replay: %v", err)
	}

	updates := emit.snapshot()
	// user + two tool finishes + the rebuilt plan
	if len(updates) != 4 {
		t.Fatalf("expected 4 updates, got %d: %+v", len(updates), updates)
	}
	plan := updates[len(updates)-1]
	if plan.SessionUpdate != acp.SessionUpdatePlan {
		t.Fatalf("last update = %q, want plan", plan.SessionUpdate)
	}
	// The newest snapshot wins; the older completed-only list must be gone.
	if len(plan.Entries) != 2 || plan.Entries[0].Content != "分析需求" || plan.Entries[1].Status != acp.PlanStatusInProgress {
		t.Fatalf("plan entries = %+v", plan.Entries)
	}
}

func TestReplayACPSessionHistory_FlatTodosMetadataFallback(t *testing.T) {
	t.Parallel()

	hostSess := &acpHostSession{
		id: "sess_plan_flat",
		chat: &ChatSession{
			Messages: []runtimetypes.Message{
				{Role: "user", Content: "继续"},
				{Role: "tool", ToolCallID: "call_todos_flat", Content: "任务列表更新", Metadata: runtimetypes.Metadata{
					"todos": []map[string]interface{}{
						{"content": "收尾", "status": "pending"},
					},
				}},
			},
		},
	}
	emit := &recordingACPEmitter{}
	if err := replayACPSessionHistory("sess_plan_flat", hostSess, emit); err != nil {
		t.Fatalf("replay: %v", err)
	}

	updates := emit.snapshot()
	if len(updates) != 3 {
		t.Fatalf("expected 3 updates, got %d: %+v", len(updates), updates)
	}
	if updates[2].SessionUpdate != acp.SessionUpdatePlan || len(updates[2].Entries) != 1 {
		t.Fatalf("flat metadata plan = %+v", updates[2])
	}
	if updates[2].Entries[0].Content != "收尾" || updates[2].Entries[0].Priority != acp.PlanPriorityMedium {
		t.Fatalf("flat metadata entry = %+v", updates[2].Entries[0])
	}
}
