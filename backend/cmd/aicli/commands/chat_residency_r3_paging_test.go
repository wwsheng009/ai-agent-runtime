package commands

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render/encoding"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// chat_residency_r3_paging_test.go —— T10-2 / R3：**开启 residency 时**的多页
// 装载页序门禁（真实 SQLite 分页 + 真实逐出）。
//
// 设计约束（§14.6）：装载期间按页 mint/deliver，顺序必须是 append-only；
// 逐出只能在**已交付**前缀上发生，且不得让页序倒置或重复。
//
// 本测试用 250 条历史（HistoryPageMessages=100 → 至少 3 页）走真实分页 API，
// 逐页前插并统计顺序，然后在装载完成后开启 residency 触发真实逐出，
// 最后断言：转录仍完整升序、无重复，且冷区读回与窗口内容拼接后与原文一致。
func TestResidencyMultiPageLoadKeepsOrderAndEvictsDeliveredPrefix(t *testing.T) {
	// 目录先建，再注册"关 store"清理（TempDir 清理必须最后发生）。
	dir := t.TempDir()
	resetResidencyRuntimeForTest(t)

	storage, err := runtimechat.NewSQLiteSessionStorage(runtimechat.DefaultPersistentSessionStorageConfig(dir))
	if err != nil {
		t.Fatalf("new sqlite storage: %v", err)
	}
	t.Cleanup(func() { _ = storage.CloseStorage() })
	manager := runtimechat.NewSessionManager(storage, &runtimechat.SessionManagerConfig{
		TTL:             24 * time.Hour,
		CleanupInterval: 0,
		AutoArchive:     false,
	})
	t.Cleanup(manager.Stop)

	ctx := context.Background()
	session, err := manager.Create(ctx, "tester")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	const messageCount = 250 // > 2 页（每页 100）
	messages := make([]runtimetypes.Message, 0, messageCount)
	for index := 0; index < messageCount; index++ {
		marker := fmt.Sprintf("R3-MSG-%03d", index)
		if index%2 == 0 {
			messages = append(messages, *runtimetypes.NewUserMessage(marker))
		} else {
			messages = append(messages, *runtimetypes.NewAssistantMessage(marker))
		}
	}
	session.ReplaceHistory(messages)
	if err := storage.Save(ctx, session); err != nil {
		t.Fatalf("save session: %v", err)
	}

	chatSession := &ChatSession{SessionManager: manager, SessionUserID: "tester", NoInteractive: true}
	// 多页装载：最新一页先就位，随后逐页向前补齐。
	first, ok := loadNewestResumeHistoryPage(chatSession, session.ID)
	if !ok {
		t.Fatal("最新一页未装载")
	}
	if !first.HasMore {
		t.Fatalf("%d 条消息应超过单页", messageCount)
	}
	firstPage := append([]runtimetypes.Message(nil), chatSession.resumeHistorySnapshot()...)
	if len(firstPage) >= messageCount {
		t.Fatalf("首帧前只应装载最新一页：loaded=%d", len(firstPage))
	}
	chatSession.deferResumeHistoryCompletion(session.ID, first.NextBeforeSeq)
	startDeferredResumeHistoryLoad(chatSession)
	deadline := time.Now().Add(10 * time.Second)
	for len(chatSession.resumeHistorySnapshot()) < messageCount && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	// 页序断言：完整、升序、无重复（append-only）。
	history := chatSession.resumeHistorySnapshot()
	if len(history) != messageCount {
		t.Fatalf("分页装载未补齐：%d/%d", len(history), messageCount)
	}
	seen := make(map[string]struct{}, messageCount)
	for index := range history {
		want := fmt.Sprintf("R3-MSG-%03d", index)
		if history[index].Content != want {
			t.Fatalf("页序错位：history[%d]=%q，期望 %q", index, history[index].Content, want)
		}
		if _, duplicated := seen[history[index].Content]; duplicated {
			t.Fatalf("分页装载出现重复：%q", history[index].Content)
		}
		seen[history[index].Content] = struct{}{}
	}

	// 开启 residency（逐出）→ 真实写穿裁剪。
	cfg := &runtimecfg.RuntimeConfig{}
	cfg.UI.Residency.Enabled = boolPtr(true)
	cfg.UI.Residency.EvictPrefix = boolPtr(true)
	cfg.UI.Residency.MaxResidentCells = 16
	applyTranscriptResidencyConfig(cfg, dir)
	runtime := currentTranscriptResidencyRuntime()
	if runtime == nil || runtime.Store == nil {
		t.Fatalf("residency 运行时未就绪：%+v", runtime)
	}

	bridge := newChatRuntimeEventBridge(chatSession)
	chatSession.RuntimeEventBridge = bridge
	// ensureSceneTrimPolicy 需要可解析的 sessionID 才写穿/裁剪（无 ID 属刻意
	// fail-closed）；本用例注入 loaded 会话作为运行时会话。
	chatSession.RuntimeSession = session
	// 首轮 apply 安装裁剪策略（ensureSceneTrimPolicy），随后才能注入交付事实。
	bridge.applyChangeSet(&encoding.ChangeSet{})
	if bridge.sceneTrimPolicy == nil {
		t.Fatal("residency 启用 + evictPrefix 时 bridge 应自动安装裁剪策略")
	}
	// 把完整历史作为已交付内容装载进场景（等价于 resume 后的 canonical seed）。
	seedSceneFromMessages(t, bridge, chatSession.resumeHistorySnapshot())

	// 本用例没有 uiActor（不是交互渲染进程），交付事实无法从账本判定。
	// 生产里"多页装载完成"的等价事实是：所有页都已交付 → 全部 cell 已交付。
	// 直接注入该事实，让逐出守卫按真实语义工作（而不是因不可判定而放行）。
	bridge.sceneTrimPolicy.trimDeliveryFactsOverride = &deliveryFacts{Known: true, Delivered: map[scene.CellID]bool{}}
	for _, cell := range bridge.renderScene.Cells() {
		if cell != nil {
			bridge.sceneTrimPolicy.trimDeliveryFactsOverride.Delivered[cell.ID] = true
		}
	}

	for rounds := 0; rounds < 64 && sceneTrimLen(bridge) > 16; rounds++ {
		bridge.applyChangeSet(&encoding.ChangeSet{})
	}
	if size := sceneTrimLen(bridge); size != 16 {
		t.Fatalf("逐出未收敛到窗口：scene cells=%d（期望 16）", size)
	}
	policy := bridge.sceneTrimPolicy
	if policy == nil || policy.Residency == nil {
		t.Fatal("逐出后必须持有冷区账本")
	}
	evicted := policy.Residency.Cold.Len()
	if evicted == 0 || evicted != len(chatSession.resumeHistorySnapshot())-16 {
		t.Fatalf("冷区记账不符：cold=%d history=%d keep=16", evicted, len(chatSession.resumeHistorySnapshot()))
	}
	// 冷区读回：顺序必须与装载顺序一致（append-only）。
	restored, expected, okRestore := restoreColdCellsHeader(chatSession)
	if !okRestore || expected != evicted || len(restored) != evicted {
		t.Fatalf("冷区读回不完整：ok=%v expected=%d restored=%d", okRestore, expected, len(restored))
	}
	for index := range restored {
		want := fmt.Sprintf("R3-MSG-%03d", index)
		if restored[index].Source != want {
			t.Fatalf("冷区读回顺序错位：restored[%d]=%q，期望 %q", index, restored[index].Source, want)
		}
	}
}

// seedSceneFromMessages 把历史消息以 Scene 快照形式装进 bridge 的场景层
// （等价于 resume 后的 canonical seed；保持消息顺序）。
func seedSceneFromMessages(t *testing.T, bridge *chatRuntimeEventBridge, messages []runtimetypes.Message) {
	t.Helper()
	bridge.sceneMu.Lock()
	defer bridge.sceneMu.Unlock()
	if bridge.renderScene == nil {
		t.Fatal("renderScene 未初始化")
	}
	for index := range messages {
		cell := scene.TranscriptCell{
			ID:       scene.CellID(index + 1),
			Sequence: uint64(index + 1),
			Revision: 1,
			Kind:     scene.KindAssistant,
			Source:   messages[index].Content,
			Phase:    scene.CellCommitted,
		}
		if messages[index].Role == "user" {
			cell.Kind = scene.KindUser
		}
		if _, err := bridge.renderScene.ApplyCellMutation(&scene.AppendCell{Cell: cell}); err != nil {
			t.Fatalf("seed cell %d: %v", index+1, err)
		}
	}
}