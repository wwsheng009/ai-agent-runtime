package commands

import (
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render/encoding"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
)

// chat_residency_rollback_test.go —— T10-3 回退演练（进程内可复现部分）。
//
// 演练目标（§T10 交付物②）：把 `ui.residency.enabled` 关掉后，行为必须
// **完全回到旧形态**——不裁剪、不写穿、不读回、pager 不含冷区。
//
// 真实回退动作是改配置 + 重启进程；进程内等价动作是重新应用配置
// （`applyTranscriptResidencyConfig` 会用 enabled=false 清空运行时）。
func TestResidencyRollbackReturnsToLegacyShape(t *testing.T) {
	dir := t.TempDir()
	resetResidencyRuntimeForTest(t)

	// ① 先开启（灰度实例）：逐出 + 冷区账本。
	on := &runtimecfg.RuntimeConfig{}
	on.UI.Residency.Enabled = boolPtr(true)
	on.UI.Residency.EvictPrefix = boolPtr(true)
	on.UI.Residency.MaxResidentCells = 4
	applyTranscriptResidencyConfig(on, dir)
	if runtime := currentTranscriptResidencyRuntime(); runtime == nil || runtime.Store == nil {
		t.Fatalf("开启后运行时未就绪：%+v", runtime)
	}
	bridge := newSceneTrimTestBridgeWithSession(t, 40, "t10-rollback")
	for rounds := 0; rounds < 32 && sceneTrimLen(bridge) > 4; rounds++ {
		bridge.applyChangeSet(&encoding.ChangeSet{})
	}
	if size := sceneTrimLen(bridge); size != 4 {
		t.Fatalf("开启时应收敛到窗口：scene=%d", size)
	}
	evicted := 0
	if policy := bridge.sceneTrimPolicy; policy != nil && policy.Residency != nil {
		evicted = policy.Residency.Cold.Len()
	}
	if evicted == 0 {
		t.Fatal("开启时应有逐出记录")
	}

	// ② 回退：enabled=false（等价于改配置并重启）。
	off := &runtimecfg.RuntimeConfig{}
	off.UI.Residency.Enabled = boolPtr(false)
	applyTranscriptResidencyConfig(off, dir)
	if runtime := currentTranscriptResidencyRuntime(); runtime != nil {
		t.Fatalf("回退后运行时必须清空：%+v", runtime)
	}
	if enabled, _, _ := transcriptResidencyRuntimeStatus(); enabled {
		t.Fatal("回退后 status.enabled 必须为 false")
	}
	if budgets := ui.LayoutCacheBudgetsSnapshot(); budgets != (ui.LayoutCacheBudgets{}) {
		t.Fatalf("回退后不得保留 residency 默认缓存预算：%+v", budgets)
	}

	// ③ 新会话（重启后的等价物）：不裁剪、不产生冷区、pager 不注入冷区。
	fresh := newSceneTrimTestBridgeWithSession(t, 40, "t10-rollback-fresh")
	fresh.applyChangeSet(&encoding.ChangeSet{})
	if size := sceneTrimLen(fresh); size != 40 {
		t.Fatalf("回退后不得裁剪：scene=%d（期望 40）", size)
	}
	if fresh.sceneTrimPolicy != nil {
		t.Fatalf("回退后不得安装裁剪策略：%+v", fresh.sceneTrimPolicy)
	}
	session := &ChatSession{RuntimeEventBridge: fresh, RuntimeSession: &runtimechat.Session{ID: "t10-rollback-fresh"}}
	if _, _, ok := restoreColdCellsHeader(session); ok {
		t.Fatal("回退后 pager 不得注入冷区")
	}
	stats := chatResidencyStatsForSession(session, nil, nil)
	if stats.Enabled {
		t.Fatal("回退后状态必须报告 enabled=false")
	}
	if stats.Index != nil {
		t.Fatalf("回退后不得输出 index 分区：%+v", stats.Index)
	}

	// ④ 旧实例的已逐出内容不受影响（数据仍在 store），但不再被读回——
	//    这是回退的预期形态：回到"只有驻留窗口"的旧行为。
	if _, _, ok := restoreColdCellsHeader(&ChatSession{RuntimeEventBridge: bridge}); !ok {
		// 旧 bridge 仍持有自己的策略与账本；这只影响已存在的实例，
		// 重启后不会再创建。此处只断言新实例干净。
		t.Log("旧实例仍保留其账本（重启后不再存在）")
	}
}
