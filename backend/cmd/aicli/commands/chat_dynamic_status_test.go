package commands

import (
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
)

func TestBuildChatDynamicStatusModelUsesCodexActivityFormat(t *testing.T) {
	model := buildChatDynamicStatusModelForWidthAndInputMode(
		"Thinking",
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

func TestBuildChatDynamicStatusModelKeepsCompletedTurnSummary(t *testing.T) {
	model := buildChatDynamicStatusModelForWidthInputModeAndCompletion(
		"Ready",
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
		"Ready",
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
