package commands

// 复现验证：/debug display 文档超过一屏时，surface 帧（用户实际看到的
// 终端画面）中是否保留完整的中间节（会话文件与目录 / 运行时调试）。
// 用户实测 aicli-2x.exe（含 structured 迁移）在 UI 上只看到
// SessionInfo + Routing + pprof + AgentControl + Graph + Mailbox，
// 中间 "会话文件与目录:"、"运行时调试:" 等整节缺失。

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

// debugDisplayViewportMarkers 是文档从头部到尾部必须全部出现的锚点行。
// 任何一个缺失都代表该节内容在渲染链中被裁剪/移交/覆盖。
var debugDisplayViewportMarkers = []string{
	// 头部
	"Runtime Core:",
	// 中间节（用户实测缺失的部分）
	"会话文件与目录:",
	"Session Store:",
	"Session File:",
	"Chat Log File:",
	"HTTP Artifact Dir:",
	"运行时调试:",
	"AICLI Config Path:",
	"Resolved Skill Dirs:",
	"Agent Target:",
	// 尾部（用户实测可见的部分）
	"Subagent Routing:",
	"pprof 诊断:",
	"AgentControl Registry:",
	"Mailbox Pending:",
}

func TestDebugDisplayViewportKeepsAllSectionsTallTerminal(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	// 120 行高的终端：文档（约 100 行）应完整落在可见输出区内。
	const width, height = 100, 120
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(width, height)
	session := &ChatSession{
		ProviderName: "openai",
		Model:        "gpt-test",
		Surface:      surface,
	}
	coord := newChatInteractionCoordinator(session)
	t.Cleanup(coord.Shutdown)
	session.Interaction = coord
	coord.SetSurface(surface)
	screen := newScreenVT(width, height)
	feed := func(paint func()) string {
		t.Helper()
		captured := captureSurfaceStdout(t, func() {
			coord.SetWriter(os.Stdout)
			paint()
		})
		screen.feed(captured)
		return captured
	}

	feed(func() {
		coord.PrintPrompt()
	})
	raw := feed(func() {
		if dispatchChatCommand(session, "/debug display", false) {
			t.Fatal("/debug display unexpectedly requested chat exit")
		}
	})

	frame := commandResultFrameText(surface)
	history := surface.HistoryWindowForTest()
	handedOff := surface.HistoryHandedOffForTest()
	t.Logf("tall terminal: historyWindow=%d lines handedOff=%d frameRows=%d", len(history), handedOff, strings.Count(frame, "\n"))
	t.Logf("tall terminal: history head=%q", firstNonEmptyHistoryLine(history))
	t.Logf("tall terminal: history first 6=%q", history[:minInt(6, len(history))])
	t.Logf("tall terminal: history last 6=%q", history[maxInt(0, len(history)-6):])
	t.Logf("tall terminal: FULL FRAME:\n%s", frame)
	// 可见帧必须保留文档尾部 + 溢出提示（提示位于文档末尾，留在可见区内）。
	for _, marker := range []string{"Mailbox Pending:", "请向上滚动查看"} {
		if !strings.Contains(frame, marker) {
			t.Errorf("tall terminal visible frame missing %q", marker)
		}
	}
	// 中间节（用户实测缺失的部分）必须完整出现在移交输出（原生 scrollback）
	// 中，证明 viewport 渲染没有吞内容，只是把超屏行滚出了可见区。
	plain := ui.RenderDocumentPlain(buildChatDebugDisplayDocument(session))
	for _, marker := range debugDisplayViewportMarkers {
		if !strings.Contains(plain, marker) {
			continue // 该节在本次会话条件下不输出（如 Runtime Core 需 ChatExecutor）
		}
		if !strings.Contains(raw, marker) {
			t.Errorf("tall terminal scrollback handoff missing %q", marker)
		}
	}
}

func firstNonEmptyHistoryLine(lines []string) string {
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			return line
		}
	}
	return "<all empty>"
}

func firstNFrameRows(frame string, n int) string {
	rows := strings.Split(strings.TrimSuffix(frame, "\n"), "\n")
	if len(rows) > n {
		rows = rows[:n]
	}
	return strings.Join(rows, "\n")
}

func TestDebugDisplayViewportKeepsAllSectionsShortTerminal(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	// 常见终端高度：40 行。文档约 100 行，必然超过一屏。
	const width, height = 100, 40
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(width, height)
	session := &ChatSession{
		ProviderName: "openai",
		Model:        "gpt-test",
		Surface:      surface,
	}
	coord := newChatInteractionCoordinator(session)
	t.Cleanup(coord.Shutdown)
	session.Interaction = coord
	coord.SetSurface(surface)
	screen := newScreenVT(width, height)
	feed := func(paint func()) string {
		t.Helper()
		captured := captureSurfaceStdout(t, func() {
			coord.SetWriter(os.Stdout)
			paint()
		})
		screen.feed(captured)
		return captured
	}

	feed(func() {
		coord.PrintPrompt()
	})
	raw := feed(func() {
		if dispatchChatCommand(session, "/debug display", false) {
			t.Fatal("/debug display unexpectedly requested chat exit")
		}
	})

	frame := commandResultFrameText(surface)
	t.Logf("short terminal frame (%d rows):\n%s", height, frame)

	// 可见帧：尾部 + 溢出提示。
	for _, marker := range []string{"Mailbox Pending:", "请向上滚动查看"} {
		if !strings.Contains(frame, marker) {
			t.Errorf("short terminal visible frame missing %q", marker)
		}
	}
	// 移交输出：中间节完整进入原生 scrollback。
	plain := ui.RenderDocumentPlain(buildChatDebugDisplayDocument(session))
	for _, marker := range []string{"会话文件与目录:", "Session Store:", "Session File:", "运行时调试:", "AICLI Config Path:", "Agent Target:"} {
		if !strings.Contains(plain, marker) {
			continue
		}
		if !strings.Contains(raw, marker) {
			t.Errorf("short terminal scrollback handoff missing %q", marker)
		}
	}
}

func TestDebugDisplayDocumentPlainIsComplete(t *testing.T) {
	// 对照实验：文档在提交前（plain 投影）必须完整包含中间节。
	// 若 plain 完整而帧缺失，则可定位为 viewport 渲染裁剪；
	// 若 plain 也不完整，则是文档构建层问题。
	session := &ChatSession{
		ProviderName: "openai",
		Model:        "gpt-test",
	}
	doc := buildChatDebugDisplayDocument(session)
	plain := ui.RenderDocumentPlain(doc)
	for _, marker := range []string{"会话文件与目录:", "Session Store:", "运行时调试:", "Mailbox Pending:"} {
		if !strings.Contains(plain, marker) {
			t.Errorf("plain document missing %q", marker)
		}
	}
}

// TestAppendAgentRunSupervisionParts verifies P6-3 CLI rendering: the agent
// graph line gains run status, attempt, deadlines and heartbeat/progress so
// operators can spot stalled/canceling children at a glance.
func TestAppendAgentRunSupervisionParts(t *testing.T) {
	now := time.Now().UTC()
	future := now.Add(45 * time.Second)
	past := now.Add(-2 * time.Minute)

	parts := appendAgentRunSupervisionParts(nil, toolbroker.AgentStatusResult{
		RunID:               "run_abc",
		RunStatus:           "running",
		Attempt:             2,
		MaxAttempts:         3,
		ExecutionDeadlineAt: future.Format(time.RFC3339Nano),
		ProgressDeadlineAt:  future.Format(time.RFC3339Nano),
		LastHeartbeatAt:     now.Add(-5 * time.Second).Format(time.RFC3339Nano),
		LastProgressAt:      past.Format(time.RFC3339Nano),
	})
	joined := strings.Join(parts, " ")
	for _, want := range []string{"run=run_abc", "run_status=running", "attempt=2/3", "exec_deadline=in", "progress_deadline=in", "heartbeat=", "progress="} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected %q in rendered parts: %q", want, joined)
		}
	}
	if !strings.Contains(joined, "5s ago") {
		t.Fatalf("expected recent heartbeat to render as 5s ago: %q", joined)
	}
	if !strings.Contains(joined, "2m ago") {
		t.Fatalf("expected stale progress to render as 2m ago: %q", joined)
	}
}

// TestAppendAgentRunSupervisionPartsEmpty verifies the helper is a no-op for
// an agent without a supervision run record.
func TestAppendAgentRunSupervisionPartsEmpty(t *testing.T) {
	parts := appendAgentRunSupervisionParts(nil, toolbroker.AgentStatusResult{})
	if len(parts) != 0 {
		t.Fatalf("expected no parts for unmonitored agent, got %q", parts)
	}
}

// TestChatSupervisionTimeShort verifies compact relative timestamp rendering.
func TestChatSupervisionTimeShort(t *testing.T) {
	if got := chatSupervisionTimeShort(""); got != "" {
		t.Fatalf("empty input must render empty, got %q", got)
	}
	if got := chatSupervisionTimeShort("not-a-time"); got != "not-a-time" {
		t.Fatalf("unparseable input must pass through, got %q", got)
	}
	if got := chatSupervisionTimeShort(time.Now().Add(30 * time.Second).Format(time.RFC3339Nano)); !strings.HasPrefix(got, "in ") || !strings.HasSuffix(got, "s") {
		t.Fatalf("expected future time to render as in <Ns>, got %q", got)
	}
	if got := chatSupervisionTimeShort(time.Now().Add(-90 * time.Second).Format(time.RFC3339Nano)); got != "1m ago" {
		t.Fatalf("expected 1m ago, got %q", got)
	}
}

// TestChatAgentPanelSummaryAgentLineSupervision verifies the TUI agent panel
// summary line carries compact P6-3 supervision fields (run status, attempt,
// deadlines, progress, heartbeat).
func TestChatAgentPanelSummaryAgentLineSupervision(t *testing.T) {
	now := time.Now().UTC()
	line := chatAgentPanelSummaryAgentLine(toolbroker.AgentStatusResult{
		Path:                "parent/child",
		Status:              "running",
		RunID:               "run_x",
		RunStatus:           "running",
		Attempt:             2,
		MaxAttempts:         3,
		ExecutionDeadlineAt: now.Add(2 * time.Minute).Format(time.RFC3339Nano),
		CancelDeadlineAt:    now.Add(-30 * time.Second).Format(time.RFC3339Nano),
		LastProgressAt:      now.Add(-10 * time.Minute).Format(time.RFC3339Nano),
		LastHeartbeatAt:     now.Add(-3 * time.Second).Format(time.RFC3339Nano),
	}, "parent/child")
	for _, want := range []string{"parent/child", "status=running", "run_status=running", "attempt=2/3", "exec_deadline=in", "cancel_deadline=30s ago", "progress=10m ago", "heartbeat=3s ago"} {
		if !strings.Contains(line, want) {
			t.Fatalf("expected %q in panel summary line: %q", want, line)
		}
	}
}

// TestChatAgentPanelSummaryAgentLineNoSupervision verifies unmonitored agents
// keep the previous compact rendering.
func TestChatAgentPanelSummaryAgentLineNoSupervision(t *testing.T) {
	line := chatAgentPanelSummaryAgentLine(toolbroker.AgentStatusResult{
		Path:            "parent/child",
		Status:          "running",
		PendingApproval: true,
	}, "parent/child")
	if line != "  * parent/child status=running waiting=approval" {
		t.Fatalf("unexpected panel summary line: %q", line)
	}
	unselected := chatAgentPanelSummaryAgentLine(toolbroker.AgentStatusResult{
		Path:            "parent/child",
		Status:          "running",
		PendingApproval: true,
	}, "")
	if unselected != "    parent/child status=running waiting=approval" {
		t.Fatalf("unexpected unselected panel summary line: %q", unselected)
	}
}
