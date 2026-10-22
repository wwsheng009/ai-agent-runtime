package commands

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render/encoding"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
)

// chat_runtime_scene_trim_test.go —— T6 契约测试：
// 默认关闭 = 行为不变；开启时场景受 keepLast 约束且被裁 cell 全部可写穿读回；
// 写穿失败 fail-closed（场景不变）。

func newSceneTrimTestBridge(t *testing.T, cells int) *chatRuntimeEventBridge {
	t.Helper()
	bridge := newChatRuntimeEventBridge(&ChatSession{})
	return seedSceneTrimTestBridge(t, bridge, cells)
}

// newSceneTrimTestBridgeWithSession 与 newSceneTrimTestBridge 相同，但带上
// RuntimeSession —— ensureSceneTrimPolicy 需要 sessionID 才能写穿/裁剪
// （无 ID 时 fail-closed 不裁剪）。
func newSceneTrimTestBridgeWithSession(t *testing.T, cells int, sessionID string) *chatRuntimeEventBridge {
	t.Helper()
	bridge := newChatRuntimeEventBridge(&ChatSession{RuntimeSession: &runtimechat.Session{ID: sessionID}})
	return seedSceneTrimTestBridge(t, bridge, cells)
}

func seedSceneTrimTestBridge(t *testing.T, bridge *chatRuntimeEventBridge, cells int) *chatRuntimeEventBridge {
	t.Helper()
	// 走真实 applyChangeSet 初始化 renderScene/renderMapper（空 changeset 无副作用）。
	bridge.applyChangeSet(&encoding.ChangeSet{})
	if bridge.renderScene == nil {
		t.Fatal("renderScene 未初始化")
	}
	appendTrimTestCells(t, bridge, 1, cells)
	return bridge
}

func appendTrimTestCells(t *testing.T, bridge *chatRuntimeEventBridge, from, to int) {
	t.Helper()
	bridge.sceneMu.Lock()
	defer bridge.sceneMu.Unlock()
	for id := from; id <= to; id++ {
		if _, err := bridge.renderScene.ApplyCellMutation(&scene.AppendCell{Cell: scene.TranscriptCell{
			ID:       scene.CellID(id),
			Kind:     scene.KindAssistant,
			Source:   fmt.Sprintf("cell %d\n", id),
			Revision: 1,
			Phase:    scene.CellCommitted,
		}}); err != nil {
			t.Fatalf("append %d: %v", id, err)
		}
	}
}

func sceneTrimLen(bridge *chatRuntimeEventBridge) int {
	bridge.sceneMu.Lock()
	defer bridge.sceneMu.Unlock()
	return bridge.renderScene.Len()
}

type trimFailingStore struct{}

func (trimFailingStore) Put(context.Context, string, ui.CellBodyPayload) error {
	return errors.New("disk full")
}

func (trimFailingStore) Get(context.Context, string, scene.CellID, uint64) (ui.CellBodyPayload, bool, error) {
	return ui.CellBodyPayload{}, false, nil
}

func TestSceneTrimDisabledByDefault(t *testing.T) {
	bridge := newSceneTrimTestBridge(t, 100)
	bridge.applyChangeSet(&encoding.ChangeSet{})
	if size := sceneTrimLen(bridge); size != 100 {
		t.Fatalf("默认关闭不得裁剪：size=%d", size)
	}
	trimmed, putErrors, restoreErrors, _ := bridge.SceneTrimStats()
	if trimmed != 0 || putErrors != 0 || restoreErrors != 0 {
		t.Fatalf("默认关闭计数应全 0：%d/%d/%d", trimmed, putErrors, restoreErrors)
	}
}

// TestSceneTrimRefusesUndeliveredPrefix 锁定 T9 在真实 provider 下抓到的缺陷：
// residency 在**装载前**启用且窗口极小时（实测 keep=1），尚未交付到 scrollback
// 的 cell 曾被写穿并裁掉，终端里 40 个 marker 全部消失（数据在 store，用户看到
// "历史没了"）。守卫语义：账本可判定时，只逐出**连续已交付前缀**。
func TestSceneTrimRefusesUndeliveredPrefix(t *testing.T) {
	bridge := newSceneTrimTestBridge(t, 40)
	bridge.SetSceneTrimPolicy(&bridgeSceneTrimPolicy{
		Enabled: true, KeepLastCells: 1, TrimBatch: 256,
		RowsPerGen: 1, Generation: 1, SessionID: "t9-guard", Store: ui.NewMemoryCellBodyStore(),
	})
	// 只标记前 10 个 cell 已交付（其余未铸提交）。
	bridge.sceneTrimPolicy.trimDeliveryFactsOverride = &deliveryFacts{Known: true, Delivered: map[scene.CellID]bool{}}
	for id := 1; id <= 10; id++ {
		bridge.sceneTrimPolicy.trimDeliveryFactsOverride.Delivered[scene.CellID(id)] = true
	}
	bridge.applyChangeSet(&encoding.ChangeSet{})

	if size := sceneTrimLen(bridge); size != 30 {
		t.Fatalf("只应逐出已交付的 10 个前缀 cell：size=%d（期望 30）", size)
	}
	trimmed, putErrors, _, lastError := bridge.SceneTrimStats()
	if trimmed != 10 || putErrors != 0 {
		t.Fatalf("逐出计数不符：trimmed=%d putErrors=%d err=%s", trimmed, putErrors, lastError)
	}
	// 一个都不可逐出时保持场景不变且给出原因码。
	bridge2 := newSceneTrimTestBridge(t, 20)
	bridge2.SetSceneTrimPolicy(&bridgeSceneTrimPolicy{
		Enabled: true, KeepLastCells: 1, TrimBatch: 256,
		RowsPerGen: 1, Generation: 1, SessionID: "t9-guard2", Store: ui.NewMemoryCellBodyStore(),
	})
	bridge2.sceneTrimPolicy.trimDeliveryFactsOverride = &deliveryFacts{Known: true, Delivered: map[scene.CellID]bool{scene.CellID(1): true}}
	bridge2.applyChangeSet(&encoding.ChangeSet{})
	// cell 1 已交付 → 可逐出 1 个，随后 cell 2 未交付 → 停止。
	if size := sceneTrimLen(bridge2); size != 19 {
		t.Fatalf("应在首个未交付 cell 处停止：size=%d（期望 19）", size)
	}
	if accounting, ok := bridge2.sceneTrimAccounting(); !ok || accounting.TrimmedCells != 1 {
		t.Fatalf("会计口径不符：%+v ok=%v", accounting, ok)
	}
}

func TestSceneTrimWriteThroughKeepsSceneBounded(t *testing.T) {
	ctx := context.Background()
	store := ui.NewMemoryCellBodyStore()
	bridge := newSceneTrimTestBridge(t, 100)
	bridge.SetSceneTrimPolicy(&bridgeSceneTrimPolicy{
		Enabled: true, KeepLastCells: 20, TrimBatch: 256,
		RowsPerGen: 2, Generation: 3, SessionID: "t6", Store: store,
	})
	bridge.applyChangeSet(&encoding.ChangeSet{})
	if size := sceneTrimLen(bridge); size != 20 {
		t.Fatalf("裁剪后场景应为 20，实际 %d", size)
	}
	trimmed, putErrors, restoreErrors, lastError := bridge.SceneTrimStats()
	if trimmed != 80 || putErrors != 0 || restoreErrors != 0 {
		t.Fatalf("stats 不符：trimmed=%d putErrors=%d restore=%d err=%s", trimmed, putErrors, restoreErrors, lastError)
	}
	for id := 1; id <= 80; id++ {
		body, ok, err := store.Get(ctx, "t6", scene.CellID(id), 1)
		if err != nil || !ok {
			t.Fatalf("cell %d 未写穿：ok=%v err=%v", id, ok, err)
		}
		if _, err := ui.ValidateTranscriptCellBody(body); err != nil {
			t.Fatalf("cell %d 写穿内容校验失败：%v", id, err)
		}
	}
	// 持续增长：再追加 50 个并触发策略，场景仍受 keepLast 约束。
	appendTrimTestCells(t, bridge, 101, 150)
	bridge.applyChangeSet(&encoding.ChangeSet{})
	if size := sceneTrimLen(bridge); size != 20 {
		t.Fatalf("持续增长后场景应为 20，实际 %d", size)
	}
	if trimmed, _, _, _ = bridge.SceneTrimStats(); trimmed != 130 {
		t.Fatalf("累计裁剪应为 130，实际 %d", trimmed)
	}
}

func TestSceneTrimFailClosedOnStoreError(t *testing.T) {
	bridge := newSceneTrimTestBridge(t, 100)
	bridge.SetSceneTrimPolicy(&bridgeSceneTrimPolicy{
		Enabled: true, KeepLastCells: 20, TrimBatch: 256,
		RowsPerGen: 1, Generation: 1, SessionID: "t6", Store: trimFailingStore{},
	})
	bridge.applyChangeSet(&encoding.ChangeSet{})
	if size := sceneTrimLen(bridge); size != 100 {
		t.Fatalf("写穿失败必须保持场景不变：size=%d", size)
	}
	trimmed, putErrors, _, lastError := bridge.SceneTrimStats()
	if trimmed != 0 || putErrors == 0 || lastError == "" {
		t.Fatalf("fail-closed 统计不符：trimmed=%d putErrors=%d err=%q", trimmed, putErrors, lastError)
	}
}
