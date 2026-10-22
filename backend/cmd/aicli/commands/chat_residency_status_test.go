package commands

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render/encoding"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
)

// chat_residency_status_test.go —— T8b-5 契约：
// 真实计数（bridge 写穿裁剪 / sqlite store 自报）、§15.2 分区、§15.3 内存口径、
// 不可归因分区必须显式登记（绝不静默零值）。

func TestChatResidencyStatusReportsBridgeAndStoreCounters(t *testing.T) {
	dir := t.TempDir()
	resetResidencyRuntimeForTest(t)
	cfg := &runtimecfg.RuntimeConfig{}
	cfg.UI.Residency.Enabled = boolPtr(true)
	// 写穿 store 只在逐出开启时惰性打开（T10-4）。
	cfg.UI.Residency.EvictPrefix = boolPtr(true)
	applyTranscriptResidencyConfig(cfg, dir)

	runtime := currentTranscriptResidencyRuntime()
	if runtime == nil || runtime.Store == nil {
		t.Fatalf("residency 运行时/store 未就绪：%+v", runtime)
	}

	bridge := newSceneTrimTestBridge(t, 100)
	bridge.SetSceneTrimPolicy(&bridgeSceneTrimPolicy{
		Enabled: true, KeepLastCells: 20, TrimBatch: 256,
		RowsPerGen: 1, Generation: 3, SessionID: "t8b5", Store: runtime.Store,
	})

	before := ui.TranscriptResidencyCountersSnapshot()
	bridge.applyChangeSet(&encoding.ChangeSet{})
	stats := chatResidencyStatsForSession(&ChatSession{
		RuntimeEventBridge: bridge,
		RuntimeSession:     &runtimechat.Session{ID: "t8b5"},
	}, nil, nil)

	if !stats.Enabled {
		t.Fatal("residency 已启用，stats.Enabled 应为 true")
	}
	if stats.Resident == nil || stats.Resident.Cells != 20 || stats.Resident.Rows <= 0 {
		t.Fatalf("resident 分区不符：%+v", stats.Resident)
	}
	if stats.Resident.Bytes <= 0 || stats.Resident.WindowStartSeq != 81 {
		t.Fatalf("resident 字节/起点不符：%+v（期望首格 seq=81）", stats.Resident)
	}
	if stats.Cold == nil || stats.Cold.Cells != 80 || stats.Cold.DeliveredRows <= 0 || stats.Cold.Bytes <= 0 {
		t.Fatalf("cold 分区不符：%+v", stats.Cold)
	}
	if stats.Evictions == nil || stats.Evictions.Total != 80 || stats.Evictions.LastReason != "prefix_window_exceeded" {
		t.Fatalf("evictions 分区不符：%+v", stats.Evictions)
	}
	// body_store 走 sqlite store 自报口径（非 ui 计数器）。
	if stats.BodyStore == nil || stats.BodyStore.Puts != 80 || stats.BodyStore.PutErrors != 0 {
		t.Fatalf("body_store 分区不符：%+v", stats.BodyStore)
	}
	if stats.Memory == nil {
		t.Fatal("§15.3 内存分区必须输出")
	}
	if stats.Memory.SceneCellsBytes <= 0 {
		t.Fatalf("scene_cells_bytes 应可归因：%+v", stats.Memory)
	}
	if stats.Memory.RenderCacheBytes < 0 {
		t.Fatalf("render_cache_bytes 恒可归因：%+v", stats.Memory)
	}
	if stats.Memory.AppStateCellsBytes != -1 || stats.Memory.LedgerBytes != -1 {
		t.Fatalf("无 uiActor/账本时可归因分区必须为 -1：%+v", stats.Memory)
	}
	// bridge → ui 计数器的真实累计（生产逐出路径）。
	after := ui.TranscriptResidencyCountersSnapshot()
	if after.EvictedCells-before.EvictedCells != 80 {
		t.Fatalf("ui 逐出计数未累加：before=%d after=%d", before.EvictedCells, after.EvictedCells)
	}
	sources := strings.Join(stats.Sources, ",")
	if !strings.Contains(sources, "bridge.scene_trim") || !strings.Contains(sources, "store.sqlite") {
		t.Fatalf("sources 未标注数据来源：%v", stats.Sources)
	}
	if !unavailableHas(stats, "index:") {
		t.Fatalf("L2 索引未接线时必须登记 unavailable：%v", stats.Unavailable)
	}
}

func TestChatResidencyStatusMarksFailClosedRestore(t *testing.T) {
	resetResidencyRuntimeForTest(t)
	bridge := newSceneTrimTestBridge(t, 40)
	bridge.SetSceneTrimPolicy(&bridgeSceneTrimPolicy{
		Enabled: true, KeepLastCells: 10, TrimBatch: 64,
		RowsPerGen: 1, Generation: 1, SessionID: "t8b5-fail", Store: trimFailingStore{},
	})

	before := ui.TranscriptResidencyCountersSnapshot()
	bridge.applyChangeSet(&encoding.ChangeSet{})
	stats := chatResidencyStatsForSession(&ChatSession{RuntimeEventBridge: bridge}, nil, nil)
	if stats.Cold == nil || stats.Cold.Cells != 0 {
		t.Fatalf("写穿全失败不得计入冷区：%+v", stats.Cold)
	}
	if stats.Evictions == nil || stats.Evictions.LastReason != "write_through_failed_restored" {
		t.Fatalf("last_reason 应标注 fail-closed 恢复：%+v", stats.Evictions)
	}
	if !unavailableHas(stats, "fail-closed restore: 30 cells") {
		t.Fatalf("恢复必须显式登记：%v", stats.Unavailable)
	}
	if size := sceneTrimLen(bridge); size != 40 {
		t.Fatalf("fail-closed 后场景必须完整：size=%d", size)
	}
	after := ui.TranscriptResidencyCountersSnapshot()
	if after.BodyStorePuts-before.BodyStorePuts != 1 || after.BodyStorePutErrors-before.BodyStorePutErrors != 1 {
		t.Fatalf("写穿尝试/失败计数不符：before=%+v after=%+v", before, after)
	}
}

func TestChatResidencyStatusDisabledDefaults(t *testing.T) {
	resetResidencyRuntimeForTest(t)
	// T10-4 后默认不再"关闭"；要覆盖关闭态必须显式关闭。
	off := &runtimecfg.RuntimeConfig{}
	off.UI.Residency.Enabled = boolPtr(false)
	applyTranscriptResidencyConfig(off, t.TempDir())
	stats := chatResidencyStatsForSession(&ChatSession{}, nil, nil)
	if stats.Enabled || stats.EvictPrefix {
		t.Fatalf("显式关闭后应关闭：%+v", stats)
	}
	if stats.Resident != nil || stats.Cold != nil {
		t.Fatalf("无 bridge 时 resident/cold 必须缺席：%+v %+v", stats.Resident, stats.Cold)
	}
	if !unavailableHas(stats, "body_store: residency runtime not installed") ||
		!unavailableHas(stats, "resident/cold: no runtime event bridge") ||
		!unavailableHas(stats, "index:") {
		t.Fatalf("不可归因分区必须登记：%v", stats.Unavailable)
	}
	if stats.Memory == nil || stats.Memory.RenderCacheBytes < 0 {
		t.Fatalf("布局缓存字节恒可归因：%+v", stats.Memory)
	}
	if stats.Memory.SceneCellsBytes != -1 || stats.Memory.SessionMessagesBytes != -1 {
		t.Fatalf("无数据源时分区应为 -1：%+v", stats.Memory)
	}
}

func unavailableHas(stats ui.TranscriptResidencyStats, substring string) bool {
	for _, entry := range stats.Unavailable {
		if strings.Contains(entry, substring) {
			return true
		}
	}
	return false
}

// TestChatResidencyStatusJSONShape 钉住 §15.2 的线上字段名（消费方契约）。
func TestChatResidencyStatusJSONShape(t *testing.T) {
	resetResidencyRuntimeForTest(t)
	bridge := newSceneTrimTestBridge(t, 30)
	bridge.SetSceneTrimPolicy(&bridgeSceneTrimPolicy{
		Enabled: true, KeepLastCells: 10, TrimBatch: 64,
		RowsPerGen: 1, Generation: 1, SessionID: "t8b5-json", Store: ui.NewMemoryCellBodyStore(),
	})
	bridge.applyChangeSet(&encoding.ChangeSet{})
	stats := chatResidencyStatsForSession(&ChatSession{
		RuntimeEventBridge: bridge,
		RuntimeSession:     &runtimechat.Session{ID: "t8b5-json"},
	}, nil, nil)
	raw, err := json.Marshal(stats)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	text := string(raw)
	for _, key := range []string{
		`"enabled"`, `"evict_prefix"`, `"options"`, `"counters"`, `"note"`,
		`"resident"`, `"cold"`, `"evictions"`, `"rematerialize"`, `"body_store"`,
		`"memory"`, `"sources"`, `"unavailable"`,
		`"scene_cells_bytes"`, `"appstate_cells_bytes"`, `"ledger_bytes"`,
		`"seed_units_bytes"`, `"session_messages_bytes"`, `"render_cache_bytes"`,
	} {
		if !strings.Contains(text, key) {
			t.Fatalf("§15.2/§15.3 JSON 缺少 %s：%s", key, text)
		}
	}
	if strings.Contains(text, `"index"`) {
		t.Fatalf("L2 索引未接线时不得输出零值 index 分区：%s", text)
	}
}
