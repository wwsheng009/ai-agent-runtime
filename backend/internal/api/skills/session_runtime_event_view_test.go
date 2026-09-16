package skills

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// TestBuildSessionRuntimeEventViewOmitsProvenanceWhenNotBearing 验证 Batch 1（P1-1）：
// 与 provenance 无关的事件不再无条件下发 8 键 provenance 字段（此前约 215B/帧）。
func TestBuildSessionRuntimeEventViewOmitsProvenanceWhenNotBearing(t *testing.T) {
	view := buildSessionRuntimeEventView(runtimeevents.Event{
		Type:      "job_output",
		SessionID: "sess-view",
		Payload:   map[string]interface{}{"content": "hello"},
	})
	_, ok := view["provenance"]
	assert.False(t, ok, "non-bearing event must not carry provenance")
	assert.Equal(t, "job_output", view["type"])
	assert.Equal(t, map[string]interface{}{"content": "hello"}, view["payload"])
}

// TestBuildSessionRuntimeEventViewKeepsBearingProvenance 验证承载事件仍下发
// provenance，且零值字段被省略（只保留有信息的键）。
func TestBuildSessionRuntimeEventViewKeepsBearingProvenance(t *testing.T) {
	view := buildSessionRuntimeEventView(runtimeevents.Event{
		Type:      "context.profile.injected",
		SessionID: "sess-view",
		Payload: map[string]interface{}{
			"source_refs": []interface{}{
				"profile-resource:memory:E:/profiles/dev/agents/tester/memory/memory.json",
			},
		},
	})
	provenance, ok := view["provenance"].(map[string]interface{})
	require.True(t, ok, "bearing event must carry provenance")
	assert.Equal(t, 1, provenance["profile_context_injected"])
	assert.Equal(t, 1, provenance["profile_memory_count"])
	assert.Equal(t, 1, provenance["profile_resource_count"])
	assert.Contains(t, provenance["profile_resource_labels"], "memory:memory.json")
	// 零值键省略：本事件没有 recall 信号。
	assert.NotContains(t, provenance, "recall_with_source_refs")
	assert.NotContains(t, provenance, "profile_notes_count")
}

// TestBuildSessionRuntimeEventViewOmitsEmptyProvenance 验证：类型在白名单内但
// 信号为零（如无来源引用的 recall）时仍不下发 provenance，避免空壳字段占字节。
func TestBuildSessionRuntimeEventViewOmitsEmptyProvenance(t *testing.T) {
	view := buildSessionRuntimeEventView(runtimeevents.Event{
		Type:      "recall.performed",
		SessionID: "sess-view",
		Payload:   map[string]interface{}{"query": "q"},
	})
	_, ok := view["provenance"]
	assert.False(t, ok, "empty provenance summary must be omitted")
}

// TestBuildSessionRuntimeEventViewKeepsCheckpointProvenance 验证 checkpoint_created
// 这类「无白名单类型但载荷带 source_refs」的事件仍计算 provenance（判据与
// runtimeevents.applyProvenanceEvent 的读取口径对齐）。
func TestBuildSessionRuntimeEventViewKeepsCheckpointProvenance(t *testing.T) {
	view := buildSessionRuntimeEventView(runtimeevents.Event{
		Type:      "checkpoint_created",
		SessionID: "sess-view",
		Payload: map[string]interface{}{
			"checkpoint_id": "chk_1",
			"source_refs": []interface{}{
				"profile-resource:notes:E:/profiles/dev/agents/tester/notes/notes.md",
			},
		},
	})
	provenance, ok := view["provenance"].(map[string]interface{})
	require.True(t, ok, "checkpoint event with source refs must carry provenance")
	assert.Equal(t, 1, provenance["profile_notes_count"])
	assert.Equal(t, 1, provenance["profile_resource_count"])
}
