package commands

import (
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render/encoding"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
)

// chat_pager_cold_restore_test.go —— T10（A）契约：逐出 → 冷区账本 → pager 读回。
//
// 设计 §14.9 的核心承诺：开启逐出后，Ctrl+T 仍然能翻到全部历史（冷区按需从
// 写穿 store 读回）。本文件锁定：
//  ① 逐出后账本记录了被逐出 cell（供 status index / pager 使用）；
//  ② pager 视图前置了冷区内容，且顺序正确（冷区在前、窗口在后、不重复）；
//  ③ store 不可用时降级为"仅窗口"且 PagerTruncated=true（绝不静默少显示）；
//  ④ status 的 index/cold 分区变为真实值。
func TestPagerRestoresEvictedCellsFromStore(t *testing.T) {
	dir := t.TempDir()
	resetResidencyRuntimeForTest(t)
	cfg := &runtimecfg.RuntimeConfig{}
	cfg.UI.Residency.Enabled = boolPtr(true)
	cfg.UI.Residency.EvictPrefix = boolPtr(true)
	cfg.UI.Residency.MaxResidentCells = 8
	applyTranscriptResidencyConfig(cfg, dir)

	bridge := newSceneTrimTestBridgeWithSession(t, 40, "t10-restore")
	bridge.applyChangeSet(&encoding.ChangeSet{})

	// 收敛到窗口（TrimBatch=keep/2，需多轮）。
	for rounds := 0; rounds < 32 && sceneTrimLen(bridge) > 8; rounds++ {
		bridge.applyChangeSet(&encoding.ChangeSet{})
	}
	if size := sceneTrimLen(bridge); size != 8 {
		t.Fatalf("逐出未收敛：scene cells=%d（期望 8）", size)
	}

	// ① 账本记录了被逐出的 cell（32 个）。
	policy := bridge.sceneTrimPolicy
	if policy == nil || policy.Residency == nil {
		t.Fatal("裁剪策略必须持有冷区账本")
	}
	if got := policy.Residency.Cold.Len(); got != 32 {
		t.Fatalf("冷区账本 cell 数=%d，期望 32", got)
	}

	// ② pager 视图读回冷区并前置。
	session := &ChatSession{RuntimeSession: &runtimechat.Session{ID: "t10-restore"}, RuntimeEventBridge: bridge}
	restored, expected, ok := restoreColdCellsHeader(session)
	if !ok {
		t.Fatal("有冷区账本时应可读回")
	}
	if expected != 32 || len(restored) != 32 {
		t.Fatalf("冷区读回数量不符：expected=%d restored=%d", expected, len(restored))
	}
	// 内容与顺序：前 32 个 marker（cell 1..32），严格递增。
	for index := range restored {
		wantID := scene.CellID(index + 1)
		if restored[index].ID != wantID {
			t.Fatalf("冷区顺序不符：第 %d 个 cell ID=%d，期望 %d", index, restored[index].ID, wantID)
		}
		if restored[index].Source == "" {
			t.Fatalf("冷区 cell %d 正文为空（读回失败）", restored[index].ID)
		}
	}
	// 冷区 cell 必须都能通过指纹校验（ValidateTranscriptCellBody 已在读回内做）。
	if restored[0].Revision != 1 {
		t.Fatalf("冷区 cell revision 丢失：%+v", restored[0])
	}

	// ③ status 的 index / cold 分区变为真实值。
	stats := chatResidencyStatsForSession(session, nil, nil)
	if stats.Index == nil {
		t.Fatalf("冷区存在时 index 分区必须为真实值，unavailable=%v", stats.Unavailable)
	}
	if stats.Index.PrefixFence == 0 {
		t.Fatal("index.prefix_fence 不得为 0（前缀链已建立）")
	}
	if stats.Cold == nil || stats.Cold.Cells != 32 {
		t.Fatalf("cold 分区不符：%+v", stats.Cold)
	}
	if stats.Cold.Bytes <= 0 || stats.Cold.DeliveredRows <= 0 {
		t.Fatalf("cold 字节/行数应为真实值：%+v", stats.Cold)
	}
	sources := ""
	for _, source := range stats.Sources {
		sources += source + ","
	}
	if !containsSubstring(sources, "bridge.cold_ledger") {
		t.Fatalf("sources 应标注冷区账本：%v", stats.Sources)
	}
}

// TestPagerColdRestoreDegradesExplicitly store 不可用时必须降级并显式标注。
func TestPagerColdRestoreDegradesExplicitly(t *testing.T) {
	bridge := newSceneTrimTestBridge(t, 20)
	bridge.SetSceneTrimPolicy(&bridgeSceneTrimPolicy{
		Enabled: true, KeepLastCells: 4, TrimBatch: 64,
		RowsPerGen: 1, Generation: 1, SessionID: "t10-degrade",
		Store:     ui.NewMemoryCellBodyStore(),
		Residency: ui.NewTranscriptResidency(ui.ResidencyPolicy{EvictPrefix: true}, nil, "t10-degrade"),
	})
	for rounds := 0; rounds < 32 && sceneTrimLen(bridge) > 4; rounds++ {
		bridge.applyChangeSet(&encoding.ChangeSet{})
	}
	// 账本记录了逐出（store 为 nil 时 AppendEvicted 仍记账），但读回必然失败。
	policy := bridge.sceneTrimPolicy
	if policy.Residency == nil || policy.Residency.Cold.Len() == 0 {
		t.Fatalf("账本应记录逐出的 cell：%+v", policy.Residency)
	}
	session := &ChatSession{RuntimeEventBridge: bridge}
	restored, expected, ok := restoreColdCellsHeader(session)
	if !ok {
		t.Fatal("有账本时应返回 ok（由 truncated 表达降级）")
	}
	if len(restored) != 0 {
		t.Fatalf("无 store 时不得读回任何 cell：%d", len(restored))
	}
	if expected == 0 {
		t.Fatal("expected 必须反映账本规模（用于判定 truncated）")
	}
	// truncated 判定：restored(0) < expected(>0)。
	if len(restored) >= expected {
		t.Fatalf("降级判定失效：restored=%d expected=%d", len(restored), expected)
	}
}

// TestPagerColdRestoreNoLedgerKeepsLegacy 无账本（residency 关闭）时不得改动行为。
func TestPagerColdRestoreNoLedgerKeepsLegacy(t *testing.T) {
	bridge := newSceneTrimTestBridge(t, 10)
	session := &ChatSession{RuntimeEventBridge: bridge}
	if _, _, ok := restoreColdCellsHeader(session); ok {
		t.Fatal("无裁剪策略时 ok 必须为 false（调用方保持既有行为）")
	}
	// 无 bridge 时同样为 false。
	if _, _, ok := restoreColdCellsHeader(&ChatSession{}); ok {
		t.Fatal("无 bridge 时 ok 必须为 false")
	}
}

func containsSubstring(haystack, needle string) bool {
	if len(needle) == 0 {
		return true
	}
	for index := 0; index+len(needle) <= len(haystack); index++ {
		if haystack[index:index+len(needle)] == needle {
			return true
		}
	}
	return false
}