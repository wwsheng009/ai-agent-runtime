package commands

import (
	"bytes"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
)

// L3 进程内 e2e：双行状态栏的 session ID 行。
//
// 回归场景：绑定持久化 runtime session 后，mount surface 必须把 session ID
// 发布到第二行状态栏（statusRow-1），并让整条状态栏占两行；未绑定 session
// 时保持单行。
//
// 断言走 surface 的 ComposedFrameForTest（与应用侧同一权威投影路径），
// 覆盖「coordinator → facade → UI actor → surface 状态 → 合成帧」全链路。
//
// popup/composer 覆盖层的 session 行隐藏已在 app_layout_test.go 的
// TestLayoutTwoRowStatus 中覆盖，此处不再重复。
func TestChatSessionIDLineE2E_PublishedOnSurfaceMount(t *testing.T) {
	session := &ChatSession{
		Stream:         true,
		RuntimeSession: &runtimechat.Session{ID: "lead-session"},
	}
	interaction := newChatInteractionCoordinator(session)
	t.Cleanup(interaction.Shutdown)
	interaction.SetWriter(&bytes.Buffer{})
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(160, 12)
	interaction.SetSurface(surface)
	session.Interaction = interaction
	interaction.waitUIActorIdle()

	frame := commandResultFrameText(surface)
	if !strings.Contains(frame, "会话 lead-session") {
		t.Fatalf("session ID line missing after surface mount:\n%s", frame)
	}
	// 双行状态栏：session 行必须在 status 行上方（12 行终端 → index 11 是
	// status，index 10 是 session）。
	rows := strings.Split(strings.TrimRight(frame, "\n"), "\n")
	if len(rows) < 12 {
		t.Fatalf("composed frame rows=%d, want >= 12", len(rows))
	}
	if !strings.Contains(rows[10], "会话 lead-session") {
		t.Fatalf("session line not on row 11 (statusRow-1):\n%q", rows[10])
	}
	if rows[11] == "" {
		t.Fatalf("status line on row 12 is empty; want a non-empty status line:\n%s", frame)
	}
}

// 未绑定 runtime session 时，状态栏必须保持单行且不渲染 "会话 " 行。
func TestChatSessionIDLineE2E_HiddenWithoutRuntimeSession(t *testing.T) {
	session := &ChatSession{Stream: true}
	interaction := newChatInteractionCoordinator(session)
	t.Cleanup(interaction.Shutdown)
	interaction.SetWriter(&bytes.Buffer{})
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(160, 12)
	interaction.SetSurface(surface)
	session.Interaction = interaction
	interaction.waitUIActorIdle()

	frame := commandResultFrameText(surface)
	if strings.Contains(frame, "会话 ") {
		t.Fatalf("session ID line rendered without a bound runtime session:\n%s", frame)
	}
	rows := strings.Split(strings.TrimRight(frame, "\n"), "\n")
	if len(rows) < 12 {
		t.Fatalf("composed frame rows=%d, want >= 12", len(rows))
	}
	if rows[11] == "" {
		t.Fatalf("status line on row 12 is empty:\n%s", frame)
	}
}