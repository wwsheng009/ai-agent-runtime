//go:build windows

// L3 ConPTY 化：真实 chat 主循环在真终端语义（isatty）下运行，无头 CI 可跑。
//
// docs/plan/aicli-terminal-e2e-methodology.md §4 曾记载 ConPTY 路线废弃
// （0xC0000142），根因已在 internal/pty 修复（UTF-16LE 环境块 + attribute
// payload 为句柄值本身），e537b361 的可行性测试证明了前置条件成立。本文件
// 把可行性落地为 L3 交互 E2E：
//
//   - 被测程序 = 本测试二进制自身（go test -c 产物，零额外构建步骤），经
//     internal/pty 挂到伪控制台运行（helper 子进程），因此 stdin/stdout 均
//     为真实终端设备：ui.IsInteractiveTerminal() 自然为真、真实渲染决策路径
//     全开——不替换 os.Stdin/os.Stdout，无测试猴补丁。
//   - 同包测试二进制内直接使用 fakeChatExecutor / chatInteractionCoordinator
//     等真实结构（普通子进程无 *_test.go，test-binary-as-helper 是唯一复用
//     路径）。
//   - 渲染断言双通道：① 原始 VT 字节流（含 conhost 注入的 ?9001h/?25l 等
//     序列，断言前按模式归一化剔除）；② vt.Screen 屏幕重建（渲染决策回归
//     哨兵：相邻重复行）。
//   - 节拍：不等固定毫秒，而等"上一轮 assistant 回复出现在渲染流"——真实
//     用户可见信号，turn 慢于/快于预期都不会错拍。
//   - 能力门禁：pty.Supported()==false（Win7/老 CI）自动 skip，os.Pipe 层
//     （chat_tty_live_loop_test.go）测试矩阵不受影响。
//
// 已知注意点：helper 子进程显式 AICLI_CHAT_INPUT_LINE_MODE=1 强制按行队列
// （win32-input-mode/?9001 下按键经 conhost 编码，注入原始字符的逐键编辑器
// 无法成行——这正是此前 helper 卡死的根因）；主循环其余部分（slash 分发/
// executor/渲染管线）不受影响。脚本必须以 "/exit" 结束（helper 经真实退出
// 路径结束进程，EOF 语义在 ConPTY 管道上不可靠）。
package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/formatter"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render/encoding"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/vt"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/pty"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

const (
	// 环境块继手：父测试进程 → pty（环境继承）→ helper 子进程。
	chatTTYConPTYSpecEnv = "AICLI_CHAT_TTY_CONPTY_SPEC"
	// helper 把渲染字节流落盘，父进程断言（helper 侧 os.Stdout 的完整原始
	// 序列；父进程同时留有 PTY 管道回读流作对照/兜底）。
	chatTTYConPTYCaptureEnv = "AICLI_CHAT_TTY_CONPTY_CAPTURE"
	// T8b-6：residency 实机证据（helper 侧写 JSON，父进程补充流断言）。
	chatTTYConPTYResidencyEnv = "AICLI_CHAT_TTY_CONPTY_RESIDENCY_OUT"
)

// chatTTYConPTYScriptStep 一步注入：WaitSignal 非空则等渲染流出现该片段
// （上一轮回复可见 = 真实节拍），否则固定等待 FixedWait 毫秒；随后写入
// Line（行结束符由驱动端规范成 \r/Enter——bracketed paste 下 \n 不提交）。
type chatTTYConPTYScriptStep struct {
	WaitSignal string `json:"wait_signal,omitempty"`
	FixedWait  int    `json:"fixed_wait_ms,omitempty"`
	Line       string `json:"line"`
	// RawBytes 非空时按"原样字节"注入（不做行结束符规范化），用于控制字符
	// （例如 Ctrl+T = 0x14）。Line 仍用于日志/断言可读性。
	RawBytes string `json:"raw_bytes,omitempty"`
	// ResizeCols/ResizeRows 非零时，在本步注入按键之前调用
	// ResizePseudoConsole，模拟用户在真实会话中缩放窗口。Resize 走的是与
	// 真实宿主相同的 ConPTY 尺寸事件（conhost 会同步 reflow 已交付行）。
	ResizeCols int `json:"resize_cols,omitempty"`
	ResizeRows int `json:"resize_rows,omitempty"`
}

// chatTTYConPTYScript 既是父进程侧的注入脚本+断言清单，也是经环境变量下发
// 给 helper 子进程的启动规格（pty 环境块继承父进程 os.Environ，无需命令行
// 转义）。
type chatTTYConPTYScript struct {
	Reply string                    `json:"reply"`
	Steps []chatTTYConPTYScriptStep `json:"steps"`
	Wants []string                  `json:"wants"`
	// History / ResidencyKeep 非零时，helper 走 residency 场景（T8b-6）：
	// 先按 bridge 事件装 History 个 marker cell 并交付到原生 scrollback，
	// 再把场景窗口收紧到 ResidencyKeep（逐出已交付前缀），最后跑真实 TUI。
	History       int `json:"history,omitempty"`
	ResidencyKeep int `json:"residency_keep,omitempty"`
	// TrimLoadMessages / TrimProbeOut 非零时，helper 走 T10-7 裁剪场景：
	// 用真实 SQLite 存储 + SessionManager 恢复一个有 TrimLoadMessages 条消息的
	// 会话，走真实的分页装载 → 交付 → 裁剪链路，然后把裁剪前后的规模写进
	// TrimProbeOut 指向的 JSON（父进程据此断言实机行为与进程内一致）。
	TrimLoadMessages int    `json:"trim_load_messages,omitempty"`
	TrimProbeOut     string `json:"trim_probe_out,omitempty"`
}

func encodeChatTTYConPTYScript(script chatTTYConPTYScript) string {
	b, err := json.Marshal(script)
	if err != nil {
		return ""
	}
	return string(b)
}

func decodeChatTTYConPTYScript(t *testing.T, raw string) chatTTYConPTYScript {
	t.Helper()
	var script chatTTYConPTYScript
	if err := json.Unmarshal([]byte(raw), &script); err != nil {
		t.Fatalf("helper: decode script spec: %v", err)
	}
	if script.Reply == "" {
		t.Fatal("helper: script.reply is required")
	}
	return script
}

// TestChatTTYConPTYHelperChild 是 helper 子进程的入口测试：在伪控制台内
// 构造与 runTTYLiveLoop 同构的真实会话并驱动真实 runChatLoop，把渲染字节
// 流回传父进程（文件，路径经环境变量下发）。作为普通 `go test` 运行时
// （无规格环境变量）立即跳过。
func TestChatTTYConPTYHelperChild(t *testing.T) {
	specJSON := strings.TrimSpace(os.Getenv(chatTTYConPTYSpecEnv))
	if specJSON == "" {
		t.Skip("helper entry: not running under the ConPTY driver (no spec env)")
	}

	// 会话隔离（与 usage_analytics_testmain 的隔离思路一致）：helper 子进程
	// 不继承父进程已设置的隔离函数指针，显式把 HOME/USERPROFILE 钉到临时
	// 目录，避免 NewChatLogger/工作区解析触碰真实用户目录。
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	script := decodeChatTTYConPTYScript(t, specJSON)

	// isatty 证据行：写进真实终端（conhost → PTY 管道 → 父进程流与捕获
	// 文件都可见）。os.Pipe 层的等价场景永远是 INTERACTIVE=False。
	fmt.Fprintf(os.Stdout, "INTERACTIVE=%v\n", ui.IsInteractiveTerminal())

	raw := driveChatTTYConPTYChildLoop(t, script)
	if strings.TrimSpace(raw) == "" {
		t.Fatal("helper: real chat loop produced no render output")
	}
	writeChatTTYConPTYCapture(t, raw)
	t.Logf("helper: captured %d render bytes under pseudoconsole", len(raw))
}

// driveChatTTYConPTYChildLoop 在 helper 子进程内跑真实 chat 主循环：构造与
// runTTYLiveLoop 同构的会话（fake executor / coordinator / unified renderer），
// 脚本注入由父进程经 ConPTY 管道完成，主循环直到 "/exit" 退出。
//
// 渲染直连伪控制台 stdout：helper 的 os.Stdout 就是 ConPTY 的 conhost 端，
// unified renderer 经 io.MultiWriter(console, sink) 直写终端（os.Stdout 的
// fd 不变，isatty 语义不受影响）同时 tee 进内存 sink 回传父进程。
func driveChatTTYConPTYChildLoop(t *testing.T, script chatTTYConPTYScript) string {
	t.Helper()
	executor := &fakeChatExecutor{output: script.Reply}
	session := &ChatSession{
		Provider:     testChatProvider(),
		cancelCtx:    newTestChatContext(),
		ChatExecutor: executor,
		// HOME 已隔离：NewChatLogger 的目录解析只落临时目录。
		Logger:    NewChatLogger("codex_ee", "codex", "gpt-5.4", false, "https://example.com"),
		Formatter: formatter.NewMarkdownFormatter(false),
		InputBox:  ui.NewInputBox(nil),
	}
	session.Interaction = newTestChatInteractionCoordinator(t, session)
	session.Interaction.session = session

	var renderSink strings.Builder
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(80, 24)
	session.Interaction.SetSurface(surface)
	session.Interaction.SetWriter(io.MultiWriter(os.Stdout, &renderSink))
	if !session.Interaction.enableUnifiedRendererWithWriter(io.MultiWriter(os.Stdout, &renderSink)) {
		t.Fatal("helper: unified renderer did not attach")
	}

	// T8b-6：residency 冷前缀场景必须在真实终端交付路径挂载之后运行（否则
	// uiActor 没有物理写端，无法证明"已交付前缀不重铸"）。
	if script.History > 0 && script.ResidencyKeep > 0 {
		driveChatTTYConPTYResidencyPrefix(t, session, script)
	}
	// T10-7：展示历史裁剪场景。必须在真实终端交付路径挂载之后运行，
	// 因为裁剪的前提正是「交付已完成」。
	if script.TrimLoadMessages > 0 {
		driveChatTTYConPTYTrimAfterDelivery(t, session, script)
	}

	runChatLoop(session, false, "")

	// 退出路径最后一帧经 unified 管线异步提交：排空 actor/presenter 后再
	// 落捕获文件（与 driveTTYLiveLoop 的收尾顺序一致）。
	session.Interaction.waitUIActorIdle()
	session.Interaction.mu.Lock()
	presenter := session.Interaction.primaryPresenter
	session.Interaction.mu.Unlock()
	if presenter != nil {
		presenter.WaitIdle()
	}
	session.Interaction.Shutdown()

	if executor.called {
		t.Logf("helper: executor saw %d prompt(s)", len(executor.prompts))
	}
	return renderSink.String()
}

// driveChatTTYConPTYTrimAfterDelivery 在真实终端交付路径已挂载的前提下，
// 走完「真实 SQLite 存储 → 分页装载 → 交付 → 裁剪 → /history 恢复」全链路，
// 把每一步的实际规模写进证据 JSON。
//
// 为什么要放进 ConPTY 真机：裁剪的触发点是「交付完成」，而交付只有在真实
// 终端写端存在时才发生（进程内测试的 surface 不会真的把行交给终端）。
// 本场景证明：在真机路径上，裁剪确实发生、且发生在交付之后。
func driveChatTTYConPTYTrimAfterDelivery(t *testing.T, session *ChatSession, script chatTTYConPTYScript) {
	t.Helper()
	ctx := context.Background()

	inner, err := runtimechat.NewSQLiteSessionStorage(runtimechat.DefaultPersistentSessionStorageConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("trim probe: open storage: %v", err)
	}
	t.Cleanup(func() { _ = inner.CloseStorage() })
	manager := runtimechat.NewSessionManager(inner, &runtimechat.SessionManagerConfig{
		TTL:             24 * time.Hour,
		CleanupInterval: 0,
		AutoArchive:     false,
	})
	t.Cleanup(manager.Stop)

	total := script.TrimLoadMessages
	seedMessages := make([]runtimetypes.Message, 0, total)
	for index := 0; index < total; index++ {
		if index%2 == 0 {
			seedMessages = append(seedMessages, *runtimetypes.NewUserMessage(fmt.Sprintf("T10-7-MSG-%04d", index)))
		} else {
			seedMessages = append(seedMessages, *runtimetypes.NewAssistantMessage(fmt.Sprintf("T10-7-MSG-%04d", index)))
		}
	}
	runtimeSession, err := manager.Create(ctx, "probe")
	if err != nil {
		t.Fatalf("trim probe: create session: %v", err)
	}
	runtimeSession.ReplaceHistory(seedMessages)
	if err := inner.Save(ctx, runtimeSession); err != nil {
		t.Fatalf("trim probe: save session: %v", err)
	}

	// 把会话挂到真实 ChatSession 上，复用生产恢复链路。
	session.SessionManager = manager
	session.SessionUserID = "probe"
	session.RuntimeSession = runtimeSession
	session.ResumeFullHistory = false

	// 1) 真实分页装载（最新页 + 更早页）。
	first, ok := loadNewestResumeHistoryPage(session, runtimeSession.ID)
	if !ok {
		t.Fatalf("trim probe: newest page load failed")
	}
	loadedAfterFirst := len(session.resumeHistorySnapshot())
	if first.HasMore {
		pages := fetchOlderResumeHistoryPages(ctx, manager, runtimeSession.ID, first.NextBeforeSeq)
		if len(pages) > 0 {
			session.prependResumeHistoryPages(session.resumeHistoryGenerationValue(), pages)
		}
	}
	loadedFull := len(session.resumeHistorySnapshot())

	// 2) 真实交付：把展示历史重放进终端（走生产 printVisibleSessionLoadHistory）。
	delivered := printVisibleSessionLoadHistory(session, "")
	if delivered == 0 {
		t.Fatalf("trim probe: delivery produced no rows")
	}

	// 3) 交付完成后裁剪。
	trimResumeHistoryAfterDelivery(session)
	afterTrim := len(session.resumeHistorySnapshot())
	trimCount, trimmedOff := resumeHistoryTrimStats(session)

	// 4) /history 语义：从存储重新分页读回完整转录。
	restored := refreshResumeHistoryForReplay(session)
	afterRestore := len(session.resumeHistorySnapshot())

	probe := map[string]interface{}{
		"seed_messages":        total,
		"loaded_after_first":   loadedAfterFirst,
		"loaded_full":          loadedFull,
		"delivered_rows":       delivered,
		"after_trim":           afterTrim,
		"trim_count":           trimCount,
		"trimmed_off":          trimmedOff,
		"restore_ok":           restored,
		"after_restore":        afterRestore,
		"retained_limit":       resumeHistoryRetainedAfterDelivery,
		"interactive_terminal": ui.IsInteractiveTerminal(),
	}
	encoded, err := json.MarshalIndent(probe, "", "  ")
	if err != nil {
		t.Fatalf("trim probe: marshal: %v", err)
	}
	if path := strings.TrimSpace(script.TrimProbeOut); path != "" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err == nil {
			if writeErr := os.WriteFile(path, encoded, 0o644); writeErr != nil {
				t.Fatalf("trim probe: write evidence: %v", writeErr)
			}
		}
	}
	t.Logf("trim probe: %s", string(encoded))
}

func writeChatTTYConPTYCapture(t *testing.T, raw string) {
	t.Helper()
	path := strings.TrimSpace(os.Getenv(chatTTYConPTYCaptureEnv))
	if path == "" {
		t.Fatal("helper: capture path env missing")
	}
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatalf("helper: write capture: %v", err)
	}
}

func readChatTTYConPTYCapture(t *testing.T) string {
	t.Helper()
	path := strings.TrimSpace(os.Getenv(chatTTYConPTYCaptureEnv))
	if path == "" {
		return ""
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(raw)
}

// driveChatTTYConPTYResidencyPrefix 是 T8b-6 的实机冷前缀场景（在伪控制台内执行）：
//
//  1. 经 bridge 真实事件装 History 个 committed assistant cell（每 cell 一行
//     marker），等价于 resume 装载的 canonical 历史；
//  2. 把这份快照投递给 UIController 并交付到原生 scrollback（真实写终端）；
//  3. 安装 bridge 的写穿裁剪策略（keep = ResidencyKeep，store = 真实 sqlite），
//     再触发一轮 applyChangeSet，令已交付前缀被逐出（先写穿后裁剪）；
//  4. 逐 cell 从 store 读回并校验指纹（证明冷区内容完整、可重建）。
//
// 断言材料写入 chatTTYConPTYResidencyEnv 指向的 JSON，供父进程与证据归档。
// 这里不做"是否重复"的判断：渲染流层面的 exactly-once 由父进程用 vt.Screen
// 重建后断言（同一份真实字节流）。
func driveChatTTYConPTYResidencyPrefix(t *testing.T, session *ChatSession, script chatTTYConPTYScript) {
	t.Helper()
	sessionID := "conpty-t8b6-" + time.Now().Format("150405.000000")
	session.RuntimeSession = &runtimechat.Session{ID: sessionID}
	bridge := newChatRuntimeEventBridge(session)
	session.RuntimeEventBridge = bridge

	markers := make([]string, 0, script.History)
	for index := 1; index <= script.History; index++ {
		marker := fmt.Sprintf("AICLI-T8B6-HISTORY-%03d", index)
		markers = append(markers, marker)
		turnID := fmt.Sprintf("t8b6-turn-%03d", index)
		streamID := fmt.Sprintf("t8b6-stream-%03d", index)
		for _, event := range []runtimeevents.Event{
			{Type: runtimechat.EventSessionStart, SessionID: sessionID, Payload: map[string]interface{}{"turn_id": turnID}},
			{Type: runtimechat.EventLLMRequestStarted, SessionID: sessionID, Payload: map[string]interface{}{
				"turn_id": turnID, "stream_id": streamID, "step": 1,
			}},
			{Type: runtimechat.EventAssistantMessage, SessionID: sessionID, Payload: map[string]interface{}{
				"turn_id": turnID, "stream_id": streamID, "content": marker,
			}},
			{Type: runtimechat.EventSessionEnd, SessionID: sessionID, Payload: map[string]interface{}{"turn_id": turnID, "success": true}},
		} {
			bridge.applyChangeSet(bridge.renderEncoder.Encode(event))
		}
	}

	snapshotBefore := bridge.sceneSnapshot()
	if snapshotBefore == nil || len(snapshotBefore.Cells) != script.History {
		t.Fatalf("T8b-6 fixture: scene cells=%d, want %d", len(snapshotBefore.Cells), script.History)
	}

	// 真实终端交付：把历史快照投给 UI actor 并等待 executor 把已提交行写进
	// 原生 scrollback（与 resume 装载后的首帧路径一致）。
	deliverChatTTYConPTYSnapshot(t, session, snapshotBefore, false)
	deliveredBefore := session.Interaction.currentUIActor().AppState()
	if got := len(deliveredBefore.Transcript.Cells); got != script.History {
		t.Fatalf("T8b-6 delivery: uiActor cells=%d, want %d", got, script.History)
	}
	deliveredEntries := len(deliveredBefore.HistoryEffects.Entries())
	if deliveredEntries == 0 {
		t.Fatal("T8b-6 delivery: 已提交历史未产生交付记录")
	}

	// 灰度顺序（与 T10 的 canary 一致）：历史已装载并交付后，residency 才启用。
	// 生产装配路径（T8b-1/2）：配置 → ui 选项 → 进程级运行时 + sqlite store；
	// keepLast 取配置里的 MaxResidentCells，裁剪策略由 bridge 的
	// ensureSceneTrimPolicy 在下一轮 applyChangeSet 时自动安装（不是测试手装）。
	dir := t.TempDir() // 必须先建目录，再注册"关闭 store"的清理
	resetResidencyRuntimeForTest(t)
	cfg := &runtimecfg.RuntimeConfig{}
	cfg.UI.Residency.Enabled = boolPtr(true)
	cfg.UI.Residency.MaxResidentCells = script.ResidencyKeep
	// §15.1 保守档：只有显式开启 evictPrefix 才会安装写穿裁剪策略。
	cfg.UI.Residency.EvictPrefix = boolPtr(true)
	applyTranscriptResidencyConfig(cfg, dir)
	runtime := currentTranscriptResidencyRuntime()
	if runtime == nil || runtime.Store == nil {
		t.Fatalf("T8b-6: residency 运行时未就绪：%+v", runtime)
	}

	// 写穿裁剪：keep = ResidencyKeep → 已交付前缀被逐出，冷区可读回。
	// 策略来自 ensureSceneTrimPolicy（运行时装配）；TrimBatch = keep/2（≥32），
	// 因此可能需要多轮才能收敛到 keep。
	for rounds := 0; rounds < 32 && len(bridge.sceneSnapshot().Cells) > script.ResidencyKeep; rounds++ {
		bridge.applyChangeSet(&encoding.ChangeSet{})
		if bridge.sceneTrimPolicy == nil {
			t.Fatal("T8b-6: bridge 未自动安装 scene trim 策略（residency 运行时未生效）")
		}
		if got := bridge.sceneTrimPolicy.KeepLastCells; got != script.ResidencyKeep {
			t.Fatalf("T8b-6: 策略 keepLast=%d，期望 %d", got, script.ResidencyKeep)
		}
	}

	snapshotAfter := bridge.sceneSnapshot()
	if snapshotAfter == nil || len(snapshotAfter.Cells) != script.ResidencyKeep {
		t.Fatalf("T8b-6 eviction: scene cells=%d, want %d", len(snapshotAfter.Cells), script.ResidencyKeep)
	}

	// 冷区读回校验：每个被逐出 cell 必须能重建且指纹一致。
	evicted := script.History - script.ResidencyKeep
	validated := 0
	for _, marker := range markers[:evicted] {
		cellID := scene.CellID(0)
		for _, cell := range snapshotBefore.Cells {
			if cell != nil && cell.Source == marker {
				cellID = cell.ID
				break
			}
		}
		if cellID == 0 {
			t.Fatalf("T8b-6 readback: marker %q 在装载快照中缺失", marker)
		}
		body, ok, err := runtime.Store.Get(context.Background(), sessionID, cellID, 1)
		if err != nil || !ok {
			t.Fatalf("T8b-6 readback: cell %d (%s) ok=%v err=%v", cellID, marker, ok, err)
		}
		if _, err := ui.ValidateTranscriptCellBody(body); err != nil {
			t.Fatalf("T8b-6 readback: cell %d (%s) 校验失败：%v", cellID, marker, err)
		}
		validated++
	}

	// 逐出后再次交付：只允许新窗口内容进入 uiActor。**不得** arm scrollback
	// replay —— 那会清空并重写已交付的 scrollback，正是本用例要排除的重铸路径。
	deliverChatTTYConPTYSnapshot(t, session, snapshotAfter, false)
	deliveredAfter := session.Interaction.currentUIActor().AppState()
	if got := len(deliveredAfter.Transcript.Cells); got != script.ResidencyKeep {
		t.Fatalf("T8b-6 post-evict delivery: uiActor cells=%d, want %d", got, script.ResidencyKeep)
	}
	if got := len(deliveredAfter.HistoryEffects.Entries()); got < deliveredEntries {
		t.Fatalf("T8b-6 post-evict delivery dropped ledger entries: before=%d after=%d", deliveredEntries, got)
	}

	stats := chatResidencyStatsForSession(session, nil, nil)
	if stats.Cold == nil || stats.Cold.Cells != evicted {
		t.Fatalf("T8b-6: cold 分区=%+v，期望 %d cells", stats.Cold, evicted)
	}
	if stats.BodyStore == nil || stats.BodyStore.Puts != uint64(evicted) || stats.BodyStore.PutErrors != 0 {
		t.Fatalf("T8b-6: body_store 自报=%+v，期望 puts=%d errors=0", stats.BodyStore, evicted)
	}

	// T10（A）：冷区账本 + pager 读回。逐出必须记入账本，且 pager 视图能取回
	// 全部被逐出内容（§14.9：开启逐出后 Ctrl+T 仍能翻到全部历史）。
	if bridge.sceneTrimPolicy == nil || bridge.sceneTrimPolicy.Residency == nil {
		t.Fatal("T10: 裁剪策略必须持有冷区账本")
	}
	if got := bridge.sceneTrimPolicy.Residency.Cold.Len(); got != evicted {
		t.Fatalf("T10: 冷区账本 cell 数=%d，期望 %d", got, evicted)
	}
	restored, expectedCold, restoreOK := restoreColdCellsHeader(session)
	if !restoreOK || expectedCold != evicted || len(restored) != evicted {
		t.Fatalf("T10: pager 冷区读回不符：ok=%v expected=%d restored=%d", restoreOK, expectedCold, len(restored))
	}
	// 顺序与内容：前 evicted 个 marker 依次读回。
	for index := range restored {
		want := fmt.Sprintf("AICLI-T8B6-HISTORY-%03d", index+1)
		if restored[index].Source != want {
			t.Fatalf("T10: 冷区第 %d 个 cell 正文=%q，期望 %q", index, restored[index].Source, want)
		}
	}

	// T10-1c：冷区内容必须能经**pager 渲染路径**输出到真实终端。
	//
	// 这里不模拟按键：行模式（AICLI_CHAT_INPUT_LINE_MODE=1）下 Ctrl+T 不由逐键
	// 编辑器认领，无法用原样字节注入触发。改为复用 pager 的同一对函数——
	// NewTranscriptPagerModel + renderTranscriptPagerFrame（openChatTranscriptPager
	// 最终就是它们），把帧直接写进伪控制台；父进程在 PTY 字节流里断言冷区 marker
	// 出现，从而证明"逐出后的内容能经 pager 显示在真实终端上"。
	pagerFrame := coldFrameProbe(t, restored)

	evidence := map[string]any{
		"task":            "T8b-6 ConPTY residency cold-prefix gate",
		"session_id":      sessionID,
		"history_cells":   script.History,
		"keep_cells":      script.ResidencyKeep,
		"evicted_cells":   evicted,
		"readback_ok":     validated,
		"resident_cells":  len(snapshotAfter.Cells),
		"store_path":      runtime.StorePath,
		"pager_frame":     pagerFrame,
		"residency":       stats,
		"residency_state": "conpty-helper",
	}
	if path := strings.TrimSpace(os.Getenv(chatTTYConPTYResidencyEnv)); path != "" {
		raw, err := json.MarshalIndent(evidence, "", "  ")
		if err != nil {
			t.Fatalf("T8b-6 evidence marshal: %v", err)
		}
		if err := os.WriteFile(path, raw, 0o644); err != nil {
			t.Fatalf("T8b-6 evidence write: %v", err)
		}
	}
	t.Logf("T8b-6 residency: history=%d evicted=%d readback_ok=%d resident=%d",
		script.History, evicted, validated, len(snapshotAfter.Cells))
}

// coldFrameProbe 把读回的冷区经 pager 的渲染函数生成一帧，写到独立文件。
//
// 为什么不写进主字节流：pager 是**副屏**（alternate screen），它的内容会在
// 屏幕上重画同一批 marker；若写进主 PTY 流，会把"终端投影里每个 marker 恰好
// 一次"的断言污染成两次（实测）。副屏帧本来就该独立于 scrollback 统计。
//
// 断言（父进程读该文件）：
//   - 帧里有 pager 标题行（证明走的是 pager 渲染路径）；
//   - 冷区首/中/末 marker 各出现一次（证明逐出内容可显示）。
//
// 这是 R1「冷前缀已逐出后的翻页」在当前行模式下的可执行等价物：pager 的
// 内容来源（chatTranscriptPagerView → 冷区读回）与渲染函数都是生产代码。
func coldFrameProbe(t *testing.T, cold []scene.TranscriptCell) string {
	t.Helper()
	if len(cold) == 0 {
		return ""
	}
	// 与 chatTranscriptPagerView 的产物同形：TranscriptState 承载已读回的冷区 cell。
	transcript := ui.NewTranscriptState(&scene.Snapshot{Revision: 1, Cells: pagerProbeCellPointers(cold)})
	model := ui.NewTranscriptPagerModel(ui.TranscriptPagerSnapshot{Transcript: transcript})
	if len(model.Cells) != len(cold) {
		t.Fatalf("T10-1c: pager model cells=%d，期望 %d", len(model.Cells), len(cold))
	}
	// pager 默认 FollowBottom：先渲染一次让锚点落到尾部，再跳到顶部渲染第二帧，
	// 这样才能看到**冷区**（最早的内容）。
	state := ui.NewTranscriptPagerState()
	_ = ui.RenderTranscriptPagerFrameForProbe(model, state, 80, 24)
	// Home 键语义 = 向上滚动整份行数（applyTranscriptPagerKey 的 editorKeyHome）。
	state.Scroll(model, 80, transcriptPagerProbeViewport(24), -len(model.Rows(80)))
	frame := ui.RenderTranscriptPagerFrameForProbe(model, state, 80, 24)
	if strings.TrimSpace(frame) == "" {
		t.Fatal("T10-1c: pager 帧为空（渲染路径未产出）")
	}
	return frame
}

// pagerProbeCellPointers 把值切片转成 Snapshot 需要的指针切片（只读借用）。
func pagerProbeCellPointers(cells []scene.TranscriptCell) []*scene.TranscriptCell {
	out := make([]*scene.TranscriptCell, 0, len(cells))
	for index := range cells {
		out = append(out, &cells[index])
	}
	return out
}

// transcriptPagerProbeViewport 复刻 pager 的视口行数口径（高度 - 标题 - 底栏）。
func transcriptPagerProbeViewport(height int) int {
	if height <= 3 {
		return 1
	}
	return height - 2
}

// deliverChatTTYConPTYSnapshot 把快照投递给 UI actor 并驱动 executor 落盘。
func deliverChatTTYConPTYSnapshot(t *testing.T, session *ChatSession, snapshot *scene.Snapshot, armReplay bool) {
	t.Helper()
	actor := session.Interaction.currentUIActor()
	if actor == nil {
		t.Fatal("T8b-6: UI actor 未挂载")
	}
	if !actor.Post(ui.ReplaceTranscriptAction{Snapshot: snapshot, ArmScrollbackReplay: armReplay}) {
		t.Fatal("T8b-6: post transcript snapshot")
	}
	actor.WaitIdle()
	// 物理交付由 presenter 持有的 executor 完成（统一渲染器路径）。
	session.Interaction.mu.Lock()
	presenter := session.Interaction.primaryPresenter
	session.Interaction.mu.Unlock()
	if presenter != nil {
		if executor := presenter.Executor(); executor != nil {
			executor.Request()
			executor.WaitIdle()
		}
		presenter.WaitIdle()
	}
	actor.WaitIdle()
}

// runChatTTYConPTYLiveLoop 在父测试进程内：把本测试二进制经 ConPTY 拉起为
// helper 子进程，按脚本节拍向 conhost 行规程写键，返回 helper 捕获的原始
// VT 字节流（为空时回落 PTY 管道回读流）。helper 崩溃/卡死由超时兜底
// （Wait ctx → Kill）。
func runChatTTYConPTYLiveLoop(t *testing.T, script chatTTYConPTYScript, timeout time.Duration) string {
	return runChatTTYConPTYLiveLoopWithEnv(t, script, timeout, nil)
}

// runChatTTYConPTYLiveLoopWithEnv 与 runChatTTYConPTYLiveLoop 相同，额外注入
// 调用方环境（T8b-6 用它下发 residency 证据路径）。
func runChatTTYConPTYLiveLoopWithEnv(t *testing.T, script chatTTYConPTYScript, timeout time.Duration, extraEnv []string) string {
	t.Helper()
	if len(script.Steps) == 0 {
		t.Fatalf("ConPTY script must not be empty")
	}
	last := script.Steps[len(script.Steps)-1]
	if !strings.EqualFold(strings.TrimSpace(last.Line), "/exit") && last.RawBytes != "/exit\r" {
		t.Fatalf("ConPTY script must end with /exit (helper exits via the real quit path); steps=%+v", script.Steps)
	}
	testBinary, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}

	capturePath := filepath.Join(t.TempDir(), "chat-tty-conpty-capture.vt")
	env := append(os.Environ(),
		chatTTYConPTYSpecEnv+"="+encodeChatTTYConPTYScript(script),
		chatTTYConPTYCaptureEnv+"="+capturePath,
		// 强制按行队列：伪控制台下 IsInteractiveTerminal()=true 会选逐键编辑
		// 器，而 win32-input-mode（?9001）下原始字符注入无法成行（helper 卡死
		// 的根因，2026-10-11）。生产不设置该变量。
		"AICLI_CHAT_INPUT_LINE_MODE=1",
	)
	env = append(env, extraEnv...)

	p, err := pty.Start(testBinary, []string{
		"-test.run", "^TestChatTTYConPTYHelperChild$",
		"-test.timeout", (timeout + 30*time.Second).String(),
		"-test.v",
	}, pty.WithSize(80, 24), pty.WithEnv(env))
	if err != nil {
		if errors.Is(err, pty.ErrUnsupported) {
			t.Skip("ConPTY not supported on this host")
		}
		t.Fatalf("start helper under pseudoconsole: %v", err)
	}
	return driveChatTTYConPTYStream(t, p, script, capturePath, timeout)
}

// driveChatTTYConPTYStream 读 ConPTY 输出、按节拍注入按键、收尾校验：
// helper 进程必须正常退出且捕获文件完整。
//
// 清理顺序是正确性关键：ClosePseudoConsole 在客户端进程未退出时会阻塞
// （API 语义），任何 t.Fatal 退出路径都必须先 Kill helper —— 统一经
// t.Cleanup(Kill→Close) 兜底（cleanup 先于函数 defer 运行）。
func driveChatTTYConPTYStream(t *testing.T, p *pty.PTY, script chatTTYConPTYScript, capturePath string, timeout time.Duration) string {
	t.Helper()
	t.Cleanup(func() {
		_ = p.Kill()
		<-time.After(200 * time.Millisecond) // TerminateProcess 异步生效窗口
		_ = p.Close()
	})

	var (
		mu     sync.Mutex
		stream strings.Builder
	)
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 16*1024)
		for {
			n, readErr := p.Read(buf)
			if n > 0 {
				mu.Lock()
				stream.Write(buf[:n])
				mu.Unlock()
			}
			if readErr != nil {
				return
			}
		}
	}()

	waitCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	failure := make(chan string, 1)
	go func() {
		code, waitErr := p.Wait(waitCtx)
		if waitErr != nil {
			if errors.Is(waitErr, context.Canceled) {
				failure <- fmt.Sprintf("helper did not exit within %s (killed)", timeout)
			} else {
				failure <- fmt.Sprintf("helper wait: %v", waitErr)
			}
			return
		}
		if code != 0 {
			failure <- fmt.Sprintf("helper exit=%d", code)
			return
		}
		failure <- ""
	}()

	for i, step := range script.Steps {
		if step.ResizeCols > 0 || step.ResizeRows > 0 {
			cols, rows := step.ResizeCols, step.ResizeRows
			if cols <= 0 {
				cols = 80
			}
			if rows <= 0 {
				rows = 24
			}
			if err := p.Resize(cols, rows); err != nil {
				t.Fatalf("resize pseudoconsole to %dx%d at step %d: %v", cols, rows, i, err)
			}
			// The resize itself is asynchronous on the host side: give the
			// session a beat to observe the geometry and repaint before the
			// next keystroke.
			time.Sleep(300 * time.Millisecond)
		}
		if step.WaitSignal != "" {
			waitForChatTTYConPTYSignal(t, &mu, &stream, step.WaitSignal, timeout)
		} else {
			time.Sleep(time.Duration(step.FixedWait) * time.Millisecond)
		}
		// RawBytes 原样注入（控制字符，例如 Ctrl+T=0x14）；否则按行注入：
		// 行结束符必须是 \r（Enter）——composer 开启 bracketed paste（?2004h）
		// 后 \n 是多行草稿换行，不会提交（诊断实测结论）。
		payload := step.RawBytes
		if payload == "" {
			line := strings.TrimSuffix(step.Line, "\n")
			payload = strings.ReplaceAll(line, "\n", "\r") + "\r"
		}
		if _, err := p.Write([]byte(payload)); err != nil {
			mu.Lock()
			tail := tailBytes(stream.String(), 400)
			mu.Unlock()
			t.Fatalf("write step %d to pseudoconsole: %v; tail=%q", i, err, tail)
		}
	}
	// 脚本以 /exit 结束：helper 走真实退出路径结束进程，随后关闭句柄收尾。

	select {
	case msg := <-failure:
		if msg != "" {
			raw := readChatTTYConPTYCapture(t)
			t.Fatalf("%s; captured=%d bytes tail=%q", msg, len(raw), tailBytes(raw, 400))
		}
	case <-time.After(timeout):
		cancel() // Wait 的 ctx 取消 → 兜底 goroutine 报超时
		_ = p.Kill()
		t.Fatalf("helper did not exit within %s (killed)", timeout)
	}
	// helper 已退出：先关 PTY 解除读 goroutine 的 ReadFile 阻塞，再等 done。
	// （与 pty_test.go round-trip 收尾一致——ConPTY 输出管道在 ClosePseudoConsole
	// 前不会 EOF，直接 <-done 会永久死锁，2026-10-11。）
	_ = p.Close()
	<-done

	raw := readChatTTYConPTYCapture(t)
	if strings.TrimSpace(raw) == "" {
		mu.Lock()
		raw = stream.String()
		mu.Unlock()
	}
	return raw
}

// waitForChatTTYConPTYSignal 等待渲染流出现信号片段（上一轮回复可见 =
// 真实节拍），超时 fatal（附当前流尾，便于定位）。
func waitForChatTTYConPTYSignal(t *testing.T, mu *sync.Mutex, stream *strings.Builder, signal string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		mu.Lock()
		seen := strings.Contains(stream.String(), signal)
		mu.Unlock()
		if seen {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	mu.Lock()
	tail := tailBytes(stream.String(), 400)
	mu.Unlock()
	t.Fatalf("渲染流 %s 内未出现节拍信号 %q; tail=%q", timeout, signal, tail)
}

func tailBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// normalizeChatTTYConPTYStream 剔除 conhost 注入的控制序列（?9001 win32
// input mode、?1004 focus reporting、光标显隐等），保留内容与 SGR，使
// grep 断言与 os.Pipe 层的可读性对齐。
func normalizeChatTTYConPTYStream(raw string) string {
	patterns := []string{
		"\x1b[?9001h", "\x1b[?9001l",
		"\x1b[?1004h", "\x1b[?1004l",
		"\x1b[?25l", "\x1b[?25h",
		"\x1b[6n",
	}
	for _, pat := range patterns {
		raw = strings.ReplaceAll(raw, pat, "")
	}
	return raw
}

// TestChatTTY_LiveLoop_ConPTY_RendersRealScreen L3 ConPTY 层主用例：本测试
// 二进制经伪控制台运行真实 chat 主循环（stdin/stdout 是真终端设备，
// IsInteractiveTerminal 自然为真），注入 "hi"（fake executor 回复）与
// "/exit"，断言双通道：① 渲染流（归一化后）含回复、再见、isatty 证据；
// ② vt.Screen 重建屏幕健康（相邻重复行哨兵）。
func TestChatTTY_LiveLoop_ConPTY_RendersRealScreen(t *testing.T) {
	const reply = "这是ConPTY真终端语义下渲染的回复。"
	script := chatTTYConPTYScript{
		Reply: reply,
		Steps: []chatTTYConPTYScriptStep{
			{FixedWait: 1500, Line: "hi"},
			{WaitSignal: reply, Line: "/exit"},
		},
		Wants: []string{reply, "再见"},
	}
	raw := runChatTTYConPTYLiveLoop(t, script, 90*time.Second)
	normalized := normalizeChatTTYConPTYStream(raw)

	// isatty 证据：helper 在伪控制台下观察到真实终端语义（os.Pipe 层永远
	// 是 INTERACTIVE=False）。大小写不敏感匹配（PowerShell bool 渲染为
	// "True"，Go fmt %v 渲染为 "true"——两种证据来源都接受）。
	if !strings.Contains(strings.ToLower(normalized), "interactive=true") {
		t.Errorf("伪控制台下未观察到 isatty 语义（INTERACTIVE=True）; normalized tail=%q", tailBytes(normalized, 600))
	}
	for _, want := range script.Wants {
		if !strings.Contains(normalized, want) {
			t.Errorf("渲染流中未找到 %q; normalized tail=%q", want, tailBytes(normalized, 600))
		}
	}

	screen := vt.NewScreen(80, 24)
	screen.Feed(normalized)
	lines := screen.Lines(0, 24)
	t.Logf("--- ConPTY rendered screen (%d raw bytes, %d lines) ---\n%s", len(raw), len(lines), screen.Dump())
	assertNoAdjacentDuplicateLines(t, lines)

	// 屏幕健康哨兵：至少一行非空内容（回复/状态行/输入回显）。
	nonEmpty := 0
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			nonEmpty++
		}
	}
	if nonEmpty == 0 {
		t.Fatalf("屏幕全空：渲染未呈现任何内容; raw=%d bytes", len(raw))
	}
}

// TestChatTTY_LiveLoop_ConPTY_UnknownCommandRendersError 真实未知命令路径：
// "/bogus" 走 slash 分发渲染错误行，且不得触发 executor（渲染流中不得出现
// fake executor 的回复）。
func TestChatTTY_LiveLoop_ConPTY_UnknownCommandRendersError(t *testing.T) {
	const reply = "不应被调用的回复"
	script := chatTTYConPTYScript{
		Reply: reply,
		Steps: []chatTTYConPTYScriptStep{
			{FixedWait: 1500, Line: "/bogus"},
			{FixedWait: 800, Line: "/exit"},
		},
		Wants: []string{"未知命令: /bogus"},
	}
	raw := runChatTTYConPTYLiveLoop(t, script, 90*time.Second)
	normalized := normalizeChatTTYConPTYStream(raw)
	for _, want := range script.Wants {
		if !strings.Contains(normalized, want) {
			t.Errorf("渲染流中未找到 %q; normalized tail=%q", want, tailBytes(normalized, 600))
		}
	}
	if strings.Contains(normalized, reply) {
		t.Errorf("未知命令不应触发 executor 回复 %q", reply)
	}
	screen := vt.NewScreen(80, 24)
	screen.Feed(normalized)
	lines := screen.Lines(0, 24)
	t.Logf("--- ConPTY unknown-command screen (%d raw bytes) ---\n%s", len(raw), screen.Dump())
	assertNoAdjacentDuplicateLines(t, lines)
}

// TestChatTTY_LiveLoop_ConPTY_ResidencyColdPrefixExactlyOnce 是 T8b-6 的实机
// 门禁（真实 ConPTY + 真实终端字节流）：
//
//	72 个历史 marker 先经真实交付路径写进原生 scrollback → 已交付前缀被写穿
//	逐出（keep=24）→ 冷区逐 cell 读回校验 → 再交付新窗口 → 用户继续一轮对话。
//
// 断言：72 个 marker 在最终终端投影（scrollback + 可见屏）里**各恰好一次**且
// 严格递增；被逐出 cell 的冷区读回 100% 通过；`3J`（scrollback 清屏）为 0；
// 无相邻重复行。
func TestChatTTY_LiveLoop_ConPTY_ResidencyColdPrefixExactlyOnce(t *testing.T) {
	const (
		history = 72
		keep    = 24
		reply   = "T8B6-CONPTY-ANSWER"
	)
	script := chatTTYConPTYScript{
		Reply:         reply,
		History:       history,
		ResidencyKeep: keep,
		Steps: []chatTTYConPTYScriptStep{
			{FixedWait: 1500, Line: "hi"},
			{WaitSignal: reply, Line: "/exit"},
		},
		Wants: []string{reply},
	}
	evidenceDir := strings.TrimSpace(os.Getenv("AICLI_T8B6_OUT"))
	evidencePath := ""
	if evidenceDir != "" {
		if err := os.MkdirAll(evidenceDir, 0o755); err != nil {
			t.Fatalf("mkdir evidence: %v", err)
		}
		evidencePath = filepath.Join(evidenceDir, "conpty-residency.json")
	}
	var extraEnv []string
	if evidencePath != "" {
		extraEnv = append(extraEnv, chatTTYConPTYResidencyEnv+"="+evidencePath)
	}

	raw := runChatTTYConPTYLiveLoopWithEnv(t, script, 180*time.Second, extraEnv)
	normalized := normalizeChatTTYConPTYStream(raw)
	if !strings.Contains(strings.ToLower(normalized), "interactive=true") {
		t.Fatalf("伪控制台下未观察到 isatty 语义（INTERACTIVE=True）; tail=%q", tailBytes(normalized, 600))
	}
	for _, want := range script.Wants {
		if !strings.Contains(normalized, want) {
			t.Fatalf("渲染流中未找到 %q; tail=%q", want, tailBytes(normalized, 800))
		}
	}

	screen := vt.NewScreen(80, 24)
	screen.Feed(normalized)
	projected := strings.Join(append(screen.ScrollbackLines(), screen.Lines(1, 24)...), "\n")
	previous := -1
	for index := 1; index <= history; index++ {
		marker := fmt.Sprintf("AICLI-T8B6-HISTORY-%03d", index)
		count := strings.Count(projected, marker)
		if count != 1 {
			t.Fatalf("T8b-6 marker %q 在终端投影中出现 %d 次（要求恰好 1 次）\nprojection tail:\n%s",
				marker, count, tailString(projected, 4000))
		}
		position := strings.Index(projected, marker)
		if position <= previous {
			t.Fatalf("T8b-6 marker 顺序倒退：%q at %d（上一个 %d）", marker, position, previous)
		}
		previous = position
	}
	if strings.Contains(normalized, "\x1b[3J") {
		t.Fatalf("T8b-6 residency 路径不得清空 scrollback（CSI 3J）")
	}
	assertNoAdjacentDuplicateLines(t, screen.Lines(0, 24))

	if evidencePath == "" {
		t.Log("T8b-6: AICLI_T8B6_OUT 未设置，跳过证据归档")
		return
	}
	rawEvidence, err := os.ReadFile(evidencePath)
	if err != nil {
		t.Fatalf("T8b-6 helper 未写出证据：%v", err)
	}
	var evidence map[string]any
	if err := json.Unmarshal(rawEvidence, &evidence); err != nil {
		t.Fatalf("T8b-6 证据解析失败：%v", err)
	}
	if got, _ := evidence["evicted_cells"].(float64); int(got) != history-keep {
		t.Fatalf("T8b-6 证据逐出数=%v，期望 %d", evidence["evicted_cells"], history-keep)
	}
	if got, _ := evidence["readback_ok"].(float64); int(got) != history-keep {
		t.Fatalf("T8b-6 冷区读回校验数=%v，期望 %d", evidence["readback_ok"], history-keep)
	}
	if got, _ := evidence["resident_cells"].(float64); int(got) != keep {
		t.Fatalf("T8b-6 驻留窗口=%v，期望 %d", evidence["resident_cells"], keep)
	}
	// T10-1c：冷区必须能经 pager 渲染路径显示（副屏帧独立于 scrollback 统计）。
	pagerFrame, _ := evidence["pager_frame"].(string)
	if !strings.Contains(pagerFrame, "Transcript") {
		t.Fatalf("T10-1c: pager 标题行缺失（冷区帧未渲染）：%q", tailString(pagerFrame, 400))
	}
	// 跳到顶部后帧内应包含**最早**的冷区 cell。视口只有 22 行，因此只断言首个
	// marker；其余冷区 cell 的完整性与顺序由 readback_ok 与上面的顺序断言覆盖。
	firstMarker := fmt.Sprintf("AICLI-T8B6-HISTORY-%03d", 1)
	if count := strings.Count(pagerFrame, firstMarker); count != 1 {
		t.Fatalf("T10-1c: pager 帧中冷区首 marker %q 出现 %d 次（期望 1 次）\nframe tail:\n%s",
			firstMarker, count, tailString(pagerFrame, 1200))
	}
	// 顶部对齐：首 marker 必须出现在帧的前部（证明 Home 语义生效）。
	if index := strings.Index(pagerFrame, firstMarker); index < 0 || index > 200 {
		t.Fatalf("T10-1c: 冷区首 marker 未落在帧顶部（index=%d）\nframe head:\n%s",
			index, tailString(pagerFrame, 600))
	}
	t.Logf("T8b-6 ConPTY PASS: history=%d evicted=%d readback=%v resident=%v raw_bytes=%d",
		history, history-keep, evidence["readback_ok"], evidence["resident_cells"], len(raw))
}

// TestChatTTY_LiveLoop_ConPTY_ResizeKeepsContentExactlyOnce 是本次缩放修复的
// 真机门禁（真实 ConPTY + 真实 ConPTY 尺寸事件）：
//
//	首轮对话可见 → ResizePseudoConsole 缩小/放大（conhost 会 reflow 已交付行）
//	→ 再跑一轮对话 → /exit。
//
// 断言：两轮回复与用户输入在最终终端投影（scrollback + 可见屏）中各恰好
// 一次；缩放后不出现相邻重复行；缩放期间不得清屏（?3J）。
func TestChatTTY_LiveLoop_ConPTY_ResizeKeepsContentExactlyOnce(t *testing.T) {
	const (
		firstReply  = "CONPTY-RESIZE-FIRST-REPLY"
		firstAsk    = "CONPTY-RESIZE-ASK-ONE"
		postResize  = "/bogus"
		errorMarker = "未知命令: /bogus"
	)
	script := chatTTYConPTYScript{
		Reply: firstReply,
		Steps: []chatTTYConPTYScriptStep{
			{FixedWait: 1500, Line: firstAsk},
			{WaitSignal: firstReply, Line: ""},
			// ConPTY 尺寸事件：conhost 在自己的屏幕模型里 reflow 已交付行。
			{ResizeCols: 48, ResizeRows: 18, FixedWait: 400, Line: ""},
			{ResizeCols: 96, ResizeRows: 30, FixedWait: 400, Line: ""},
			{ResizeCols: 64, ResizeRows: 20, FixedWait: 600, Line: postResize},
			{WaitSignal: errorMarker, FixedWait: 200, Line: "/exit"},
		},
		Wants: []string{firstReply, firstAsk},
	}
	raw := runChatTTYConPTYLiveLoop(t, script, 120*time.Second)
	if strings.Contains(raw, "\x1b[3J") {
		t.Fatalf("resize purged native scrollback (CSI 3J) during a live ConPTY session")
	}
	normalized := normalizeChatTTYConPTYStream(raw)

	// 判据取原始字节流：任一内容被重发（历史重放/旧宽度重折）都会让它出现
	// 多次。这比“重放后的屏幕文本”更准确——屏幕重放只能用一个固定宽度。
	if count := strings.Count(normalized, firstReply); count != 1 {
		t.Fatalf("first reply crossed the writer %d times after three resizes, want exactly 1\n%s",
			count, tailBytes(normalized, 2000))
	}
	if count := strings.Count(normalized, errorMarker); count != 1 {
		t.Fatalf("post-resize output %q crossed the writer %d times, want exactly 1\n%s",
			errorMarker, count, tailBytes(normalized, 2000))
	}
	if strings.Contains(normalized, firstAsk) == false {
		t.Fatalf("first user turn was never rendered")
	}
	// 缩放不得把同一个内容重复写进可见区/滚动区。
	screen := vt.NewScreen(64, 20)
	screen.Feed(normalized)
	assertNoAdjacentDuplicateLines(t, screen.Lines(0, 20))
	t.Logf("ConPTY resize scenario: %d raw bytes, %d scrollback rows, reply=%d error=%d",
		len(raw), len(screen.ScrollbackLines()),
		strings.Count(normalized, firstReply), strings.Count(normalized, errorMarker))
}
