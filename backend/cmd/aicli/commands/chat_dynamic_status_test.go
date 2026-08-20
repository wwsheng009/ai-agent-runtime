package commands

import (
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
)

func TestBuildChatDynamicStatusModelUsesCodexActivityFormat(t *testing.T) {
	model := buildChatDynamicStatusModelForWidthAndInputMode(
		chatSurfaceStatus{kind: chatSurfaceStatusThinking},
		160,
		chatInputModeChat,
		2*time.Minute+20*time.Second,
	)
	if model == nil {
		t.Fatal("expected an active dynamic status model")
	}
	plain := style.StatusLineDocument(*model, 160).PlainText()
	if plain != "◦ Analyzing (2m 20s • esc to interrupt)" {
		t.Fatalf("unexpected dynamic status: %q", plain)
	}
}

func TestBuildChatDynamicStatusModelRendersRetryDetail(t *testing.T) {
	model := buildChatDynamicStatusModelForWidthAndInputMode(
		chatSurfaceStatus{kind: chatSurfaceStatusRetrying, detail: "step=1 attempt=2/3 reason=rate_limit delay=500ms"},
		160,
		chatInputModeChat,
		time.Second,
	)
	if model == nil {
		t.Fatal("expected an active retry dynamic status model")
	}
	plain := style.StatusLineDocument(*model, 160).PlainText()
	if !strings.Contains(plain, "◦ Retrying step=1 attempt=2/3 reason=rate_limit delay=500ms") {
		t.Fatalf("unexpected retry dynamic status: %q", plain)
	}
}

func TestSurfaceStatusKindRunningClassification(t *testing.T) {
	// llm.retry 通过 SetRetrying(parts) 发布结构化状态，detail 只作展示数据。
	// 语义（是否 running / 是否可中断）完全由 kind 决定——不再有字符串前缀
	// 匹配，因此旧 "retrying <detail> 卡 0s" 这类"某处忘记前缀"的 bug 从结构
	// 上不可能再发生。
	detailed := chatSurfaceStatus{
		kind:   chatSurfaceStatusRetrying,
		detail: "step=18 ttai / codex / gpt-5.6-sol attempt=1/3 reason=http_5xx_retry delay=1000ms source=prov...",
	}
	if !chatSurfaceStateIsRunning(detailed) {
		t.Fatalf("detailed retry status must be treated as running: %+v", detailed)
	}
	for _, s := range []chatSurfaceStatus{
		{kind: chatSurfaceStatusRetrying},
		{kind: chatSurfaceStatusTool, detail: "shell"},
		{kind: chatSurfaceStatusTool, detail: "git status"},
		{kind: chatSurfaceStatusStreaming},
		{kind: chatSurfaceStatusThinking},
		{kind: chatSurfaceStatusWaiting},
	} {
		if !chatSurfaceStateIsRunning(s) {
			t.Fatalf("status %+v must be treated as running", s)
		}
	}
	for _, s := range []chatSurfaceStatus{
		{kind: chatSurfaceStatusIdle},
		{kind: chatSurfaceStatusNotice, detail: "Agent Panel"},
		{kind: chatSurfaceStatusNotice, detail: "Paste draft 3 lines"},
	} {
		if chatSurfaceStateIsRunning(s) {
			t.Fatalf("status %+v must not be treated as running", s)
		}
	}
}

func TestRetryDetailStateKeepsDynamicStatusClockRunning(t *testing.T) {
	coord := newChatInteractionCoordinator(&ChatSession{})
	t.Cleanup(coord.Shutdown)

	// A live activity clock is already running mid-turn.
	start := time.Now().Add(-5 * time.Second)
	coord.mu.Lock()
	coord.dynamicStatusStarted = start
	coord.mu.Unlock()

	// The llm.retry bridge path: SetRetrying(parts) with structured status.
	coord.SetRetrying("step=18 ttai / codex / gpt-5.6-sol attempt=1/3 reason=http_5xx_retry delay=1000ms source=prov...")

	coord.mu.Lock()
	defer coord.mu.Unlock()
	if coord.dynamicStatusStarted.IsZero() {
		t.Fatal("retry detail must not reset the dynamic-status clock, otherwise elapsed freezes at 0s")
	}
	if !coord.dynamicStatusStarted.Equal(start) {
		t.Fatalf("retry detail must keep the running clock; got start=%v want %v", coord.dynamicStatusStarted, start)
	}
}

func TestBuildChatDynamicStatusModelKeepsCompletedTurnSummary(t *testing.T) {
	model := buildChatDynamicStatusModelForWidthInputModeAndCompletion(
		chatSurfaceStatus{kind: chatSurfaceStatusIdle},
		160,
		chatInputModeChat,
		time.Minute+21*time.Second,
		true,
	)
	if model == nil {
		t.Fatal("expected a completed dynamic status model")
	}
	if plain := style.StatusLineDocument(*model, 160).PlainText(); plain != "Worked for 1m 21s" {
		t.Fatalf("unexpected completed dynamic status: %q", plain)
	}
}

func TestBuildChatDynamicStatusModelOmitsSummaryForInitialReady(t *testing.T) {
	if model := buildChatDynamicStatusModelForWidthInputModeAndCompletion(
		chatSurfaceStatus{kind: chatSurfaceStatusIdle},
		160,
		chatInputModeChat,
		0,
		false,
	); model != nil {
		t.Fatalf("initial Ready must not invent a completion summary: %#v", model)
	}
}

func TestBuildChatPersistentStatusModelOmitsTransientState(t *testing.T) {
	model := buildChatPersistentStatusModelForWidth(&ChatSession{Model: "gpt-5.6-sol"}, 160)
	plain := strings.ToLower(style.StatusLineDocument(model, 160).PlainText())
	for _, transient := range []string{"thinking", "streaming", "analyzing", "等待", "思考", "输出中"} {
		if strings.Contains(plain, transient) {
			t.Fatalf("persistent status contains transient state %q: %q", transient, plain)
		}
	}
}

func TestFormatChatDynamicStatusElapsed(t *testing.T) {
	tests := []struct {
		input time.Duration
		want  string
	}{
		{0, "0s"},
		{59 * time.Second, "59s"},
		{2*time.Minute + 20*time.Second, "2m 20s"},
		{time.Hour + 2*time.Minute + 3*time.Second, "1h 2m 3s"},
	}
	for _, tt := range tests {
		if got := formatChatDynamicStatusElapsed(tt.input); got != tt.want {
			t.Fatalf("elapsed %s = %q, want %q", tt.input, got, tt.want)
		}
	}
}
