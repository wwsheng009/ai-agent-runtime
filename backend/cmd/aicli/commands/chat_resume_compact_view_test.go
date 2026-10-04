package commands

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// newResumeCompactViewFixture 构造一个「已 compact 的长会话」：
//   - 先写入 150 条 canonical 消息（超过单页 100 与热投影上限 128）；
//   - 再按 actor 的持久化方式追加 2 轮（用户 + 助手），让 canonical 继续增长；
//   - 最后模拟一次 compact：用 compaction 摘要 + 最近 6 条保留消息替换热投影。
//
// 返回值保留摘要替换时实际保留的消息，供断言默认视图内容。
func newResumeCompactViewFixture(t *testing.T) (*runtimechat.SessionManager, string, []runtimetypes.Message) {
	t.Helper()
	storage, err := runtimechat.NewSQLiteSessionStorage(runtimechat.DefaultPersistentSessionStorageConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("new sqlite storage: %v", err)
	}
	manager := runtimechat.NewSessionManager(storage, &runtimechat.SessionManagerConfig{
		TTL:             24 * time.Hour,
		MaxHistory:      0,
		CleanupInterval: 0,
		AutoArchive:     false,
	})
	t.Cleanup(manager.Stop)

	ctx := context.Background()
	session, err := manager.Create(ctx, "tester")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	const seedCount = 150
	seed := make([]runtimetypes.Message, 0, seedCount)
	for index := 0; index < seedCount; index++ {
		if index%2 == 0 {
			seed = append(seed, *runtimetypes.NewUserMessage(fmt.Sprintf("seed user %d", index)))
		} else {
			seed = append(seed, *runtimetypes.NewAssistantMessage(fmt.Sprintf("seed assistant %d", index)))
		}
	}
	session.ReplaceHistory(seed)
	if err := storage.Save(ctx, session); err != nil {
		t.Fatalf("seed save: %v", err)
	}

	for turn := 0; turn < 2; turn++ {
		actor, err := manager.Get(ctx, session.ID)
		if err != nil {
			t.Fatalf("actor load: %v", err)
		}
		actor.AddMessage(*runtimetypes.NewUserMessage(fmt.Sprintf("live user %d", turn)))
		actor.AddMessage(*runtimetypes.NewAssistantMessage(fmt.Sprintf("live assistant %d", turn)))
		if err := manager.Update(ctx, actor); err != nil {
			t.Fatalf("actor persist turn %d: %v", turn, err)
		}
	}

	compactor, err := manager.Get(ctx, session.ID)
	if err != nil {
		t.Fatalf("compaction load: %v", err)
	}
	history := compactor.GetMessages()
	if len(history) < 6 {
		t.Fatalf("projection too short for compaction fixture: %d", len(history))
	}
	retained := make([]runtimetypes.Message, 6)
	copy(retained, history[len(history)-6:])

	summary := *runtimetypes.NewUserMessage("Compacted context from earlier turns: handoff summary")
	summary.Metadata["context_stage"] = "compaction"
	compactor.ReplaceHistory(append([]runtimetypes.Message{summary}, retained...))
	if err := manager.Update(ctx, compactor); err != nil {
		t.Fatalf("persist compaction: %v", err)
	}

	reloaded, err := manager.Get(ctx, session.ID)
	if err != nil {
		t.Fatalf("reload compacted session: %v", err)
	}
	if !resumeHistoryHasCompactionCheckpoint(reloaded.History) {
		t.Fatalf("fixture lost the compaction checkpoint in the restored projection: %+v", reloaded.History)
	}
	return manager, session.ID, retained
}

// TestResumeDefaultsToCompactViewWhenCheckpointPresent 校验新的默认口径：
// 恢复带 compact 检查点的会话时，不再加载 canonical 全量转录，展示历史就是
// 恢复出的热上下文（compact 摘要被可见性过滤隐藏），并且恢复标题带截断提示。
func TestResumeDefaultsToCompactViewWhenCheckpointPresent(t *testing.T) {
	manager, sessionID, retained := newResumeCompactViewFixture(t)
	ctx := context.Background()

	loaded, err := manager.Get(ctx, sessionID)
	if err != nil {
		t.Fatalf("resume load: %v", err)
	}
	resumed := &ChatSession{SessionManager: manager, SessionUserID: "tester"}
	if err := restoreChatStateFromRuntimeSession(resumed, loaded); err != nil {
		t.Fatalf("resume restore: %v", err)
	}

	loadResumeCanonicalHistoryForStartup(resumed, sessionID)
	if got := len(resumed.resumeHistorySnapshot()); got != 0 {
		t.Fatalf("default resume must not load canonical transcript, got %d messages", got)
	}
	if !resumed.resumeHistoryCompactViewActive() {
		t.Fatal("compact view state should be recorded for the resume header hint")
	}

	visible := collectVisibleChatHistory(resumed)
	if len(visible) != len(retained) {
		t.Fatalf("visible history length = %d, want %d (compact-after view)", len(visible), len(retained))
	}
	for index := range retained {
		if visible[index].Content != retained[index].Content {
			t.Fatalf("visible[%d] = %q, want retained %q", index, visible[index].Content, retained[index].Content)
		}
	}
	for _, message := range visible {
		if strings.Contains(message.Content, "seed user 0") {
			t.Fatalf("pre-compaction bulk leaked into the default view: %q", message.Content)
		}
		if stage := message.Metadata.GetString("context_stage", ""); strings.EqualFold(stage, "compaction") {
			t.Fatalf("compaction summary must stay hidden: %q", message.Content)
		}
	}

	header := resumeHistoryLoadHeader(resumed, "已加载历史会话")
	if !strings.Contains(header, "compact") || !strings.Contains(header, "--full") {
		t.Fatalf("resume header = %q, want compact truncation hint with --full", header)
	}
}

// TestResumeFullFlagLoadsCanonicalTranscript 校验 --full 保持原有行为：
// 忽略 compact 检查点，回放 canonical 全量转录。
func TestResumeFullFlagLoadsCanonicalTranscript(t *testing.T) {
	manager, sessionID, _ := newResumeCompactViewFixture(t)
	ctx := context.Background()

	loaded, err := manager.Get(ctx, sessionID)
	if err != nil {
		t.Fatalf("resume load: %v", err)
	}
	total := loaded.MessageCount()
	resumed := &ChatSession{
		SessionManager:    manager,
		SessionUserID:     "tester",
		ResumeFullHistory: true,
	}
	if err := restoreChatStateFromRuntimeSession(resumed, loaded); err != nil {
		t.Fatalf("resume restore: %v", err)
	}

	loadResumeCanonicalHistoryForStartup(resumed, sessionID)
	history := resumed.resumeHistorySnapshot()
	if len(history) != total {
		t.Fatalf("full resume history length = %d, want canonical total %d", len(history), total)
	}
	if history[0].Content != "seed user 0" {
		t.Fatalf("full resume first message = %q, want the oldest canonical message", history[0].Content)
	}
	if resumed.resumeHistoryCompactViewActive() {
		t.Fatal("--full must not report the compact-after view")
	}
	header := resumeHistoryLoadHeader(resumed, "已加载历史会话")
	if strings.Contains(header, "--full") {
		t.Fatalf("--full header must not carry the truncation hint: %q", header)
	}
}

// TestResumeWithoutCompactionKeepsCanonicalTranscript 校验无 compact 时默认
// 行为不变：仍然回放 canonical 全量转录。
func TestResumeWithoutCompactionKeepsCanonicalTranscript(t *testing.T) {
	storage, err := runtimechat.NewSQLiteSessionStorage(runtimechat.DefaultPersistentSessionStorageConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("new sqlite storage: %v", err)
	}
	manager := runtimechat.NewSessionManager(storage, &runtimechat.SessionManagerConfig{
		TTL:             24 * time.Hour,
		MaxHistory:      0,
		CleanupInterval: 0,
		AutoArchive:     false,
	})
	defer manager.Stop()

	ctx := context.Background()
	session, err := manager.Create(ctx, "tester")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	messages := make([]runtimetypes.Message, 0, 20)
	for index := 0; index < 20; index++ {
		if index%2 == 0 {
			messages = append(messages, *runtimetypes.NewUserMessage(fmt.Sprintf("plain user %d", index)))
		} else {
			messages = append(messages, *runtimetypes.NewAssistantMessage(fmt.Sprintf("plain assistant %d", index)))
		}
	}
	session.ReplaceHistory(messages)
	if err := storage.Save(ctx, session); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded, err := manager.Get(ctx, session.ID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	resumed := &ChatSession{SessionManager: manager, SessionUserID: "tester"}
	if err := restoreChatStateFromRuntimeSession(resumed, loaded); err != nil {
		t.Fatalf("restore: %v", err)
	}
	loadResumeCanonicalHistoryForStartup(resumed, session.ID)
	history := resumed.resumeHistorySnapshot()
	if len(history) != len(messages) {
		t.Fatalf("history length = %d, want %d without a compaction checkpoint", len(history), len(messages))
	}
	if history[0].Content != "plain user 0" {
		t.Fatalf("history first = %q, want plain user 0", history[0].Content)
	}
	if resumed.resumeHistoryCompactViewActive() {
		t.Fatal("compact view must stay inactive without a checkpoint")
	}
}

// TestResumeHeadlessKeepsCanonicalTranscript 校验非交互路径不受 compact 后视图
// 默认值影响：ACP session/load、脚本与导出仍看到 canonical 全量历史。
func TestResumeHeadlessKeepsCanonicalTranscript(t *testing.T) {
	manager, sessionID, _ := newResumeCompactViewFixture(t)
	ctx := context.Background()

	loaded, err := manager.Get(ctx, sessionID)
	if err != nil {
		t.Fatalf("resume load: %v", err)
	}
	total := loaded.MessageCount()
	resumed := &ChatSession{SessionManager: manager, SessionUserID: "tester", NoInteractive: true}
	if err := restoreChatStateFromRuntimeSession(resumed, loaded); err != nil {
		t.Fatalf("resume restore: %v", err)
	}

	loadResumeCanonicalHistoryForStartup(resumed, sessionID)
	history := resumed.resumeHistorySnapshot()
	if len(history) != total {
		t.Fatalf("headless resume history length = %d, want canonical total %d", len(history), total)
	}
	if resumed.resumeHistoryCompactViewActive() {
		t.Fatal("headless resume must not switch to the compact view")
	}
}

// TestParseChatCommandOptionsFullHistoryFlag 校验 --full 的解析与默认值。
func TestParseChatCommandOptionsFullHistoryFlag(t *testing.T) {
	fullCmd := NewResumeCommand(func() *config.Config { return nil })
	if err := fullCmd.ParseFlags([]string{"--full"}); err != nil {
		t.Fatalf("ParseFlags --full: %v", err)
	}
	opts, err := parseChatCommandOptions(fullCmd, &config.Config{})
	if err != nil {
		t.Fatalf("parseChatCommandOptions: %v", err)
	}
	if !opts.FullHistoryFlag {
		t.Fatal("FullHistoryFlag = false, want true with --full")
	}

	plainCmd := NewChatCommand(func() *config.Config { return nil })
	plainOpts, err := parseChatCommandOptions(plainCmd, &config.Config{})
	if err != nil {
		t.Fatalf("parseChatCommandOptions(default): %v", err)
	}
	if plainOpts.FullHistoryFlag {
		t.Fatal("FullHistoryFlag = true by default, want false")
	}
}

// TestResumeCompactViewReplacesReplayedScene 校验 unified 渲染路径：事件日志
// 重放已经把 compact 前历史装进 Scene 时，compact 后视图必须整体替换（而不是
// 增量 reconcile），否则旧的 cell 仍留在语义 transcript 上，回放成本只是被推迟。
func TestResumeCompactViewReplacesReplayedScene(t *testing.T) {
	oldInteractive := chatIsInteractiveTerminal
	chatIsInteractiveTerminal = func() bool { return true }
	t.Cleanup(func() { chatIsInteractiveTerminal = oldInteractive })
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	session := &ChatSession{}
	bridge := newChatRuntimeEventBridge(session)
	session.RuntimeEventBridge = bridge
	coordinator := newTestChatInteractionCoordinator(t, session)
	session.Interaction = coordinator

	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(72, 20)
	surface.SetPhysicalWritesEnabled(false)
	coordinator.SetSurface(surface)
	var terminal bytes.Buffer
	if !coordinator.enableUnifiedRendererWithWriter(&terminal) {
		t.Fatal("unified renderer did not attach")
	}

	full := []runtimetypes.Message{
		*runtimetypes.NewUserMessage("old user turn"),
		*runtimetypes.NewAssistantMessage("old assistant answer"),
		*runtimetypes.NewUserMessage("new user turn"),
		*runtimetypes.NewAssistantMessage("new assistant answer"),
	}
	// 模拟启动时的事件日志重放：全量历史已经进入 Scene。
	bridge.seedPersistedHistory(full, "")

	// 恢复出的热上下文只保留 compact 之后的两条。
	if err := replaceRuntimeMessages(session, full[2:]); err != nil {
		t.Fatalf("restore compact projection: %v", err)
	}
	session.markResumeHistoryCompactView(true)
	if got := printVisibleSessionLoadHistory(session, "已加载历史会话"); got != 2 {
		t.Fatalf("visible history = %d, want 2", got)
	}
	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)

	snapshot := bridge.sceneSnapshot()
	if snapshot == nil {
		t.Fatal("scene snapshot missing after compact-view load")
	}
	sources := make([]string, 0, len(snapshot.Cells))
	for _, cell := range snapshot.Cells {
		sources = append(sources, cell.Source)
		if strings.Contains(cell.Source, "old ") {
			t.Fatalf("pre-compaction cell leaked into the compact view: %+v", cell)
		}
	}
	joined := strings.Join(sources, "\n")
	if !strings.Contains(joined, "new user turn") || !strings.Contains(joined, "new assistant answer") {
		t.Fatalf("compact view lost post-compaction cells: %v", sources)
	}
}

// TestInChatResumeAndLoadFullFlagReplayCanonicalTranscript 覆盖 TUI 内
// /resume、/load 的 --full 接线：默认保持 compact 后视图；带 --full 时作用域内
// 强制 canonical 全量回放，命令结束后不把该取值泄漏进会话状态。
func TestInChatResumeAndLoadFullFlagReplayCanonicalTranscript(t *testing.T) {
	manager, sessionID, _ := newResumeCompactViewFixture(t)
	ctx := context.Background()
	loaded, err := manager.Get(ctx, sessionID)
	if err != nil {
		t.Fatalf("resume load: %v", err)
	}
	total := loaded.MessageCount()

	// /resume <id>（默认）：只回放 compact 之后的上下文，不装载 canonical。
	defaultSession := &ChatSession{SessionManager: manager, SessionUserID: "tester"}
	if _, handled := executeStructuredResumeCommand(defaultSession, "/resume "+sessionID); !handled {
		t.Fatal("/resume <id> was not structured-handled")
	}
	if !defaultSession.resumeHistoryCompactViewActive() {
		t.Fatal("default /resume lost the compact-after view")
	}
	if got := len(defaultSession.resumeHistorySnapshot()); got != 0 {
		t.Fatalf("default /resume loaded %d canonical messages, want projection-only", got)
	}

	// /resume <id> --full：作用域内强制 canonical 全量，结束后复位标记。
	fullSession := &ChatSession{SessionManager: manager, SessionUserID: "tester"}
	if _, handled := executeStructuredResumeCommand(fullSession, "/resume "+sessionID+" --full"); !handled {
		t.Fatal("/resume <id> --full was not structured-handled")
	}
	if fullSession.resumeHistoryCompactViewActive() {
		t.Fatal("--full /resume must not stay on the compact view")
	}
	if got := len(fullSession.resumeHistorySnapshot()); got != total {
		t.Fatalf("--full /resume replay = %d messages, want canonical total %d", got, total)
	}
	if fullSession.ResumeFullHistory {
		t.Fatal("scoped /resume --full leaked into the session state")
	}

	// /load <id> --full：同一条装载路径，同样只在该次命令内生效。
	loadSession := &ChatSession{SessionManager: manager, SessionUserID: "tester"}
	if _, handled, err := tryExecuteStructuredChatCommand(loadSession, "/load "+sessionID+" --full"); err != nil || !handled {
		t.Fatalf("/load --full structured match=(%t, %v), want handled", handled, err)
	}
	if got := len(loadSession.resumeHistorySnapshot()); got != total {
		t.Fatalf("--full /load replay = %d messages, want canonical total %d", got, total)
	}
	if loadSession.ResumeFullHistory {
		t.Fatal("scoped /load --full leaked into the session state")
	}
}
