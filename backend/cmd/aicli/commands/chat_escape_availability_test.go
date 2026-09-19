package commands

import (
	"bufio"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
)

func TestChatEscapeInterruptAvailable(t *testing.T) {
	t.Run("nil session", func(t *testing.T) {
		if chatEscapeInterruptAvailable(nil) {
			t.Fatal("nil session must not advertise an ESC consumer")
		}
	})

	t.Run("disabled key handler", func(t *testing.T) {
		session := &ChatSession{KeyHandler: ui.NewKeyHandler()}
		if chatEscapeInterruptAvailable(session) {
			t.Fatal("unstarted key handler must not advertise an ESC consumer")
		}
	})

	t.Run("armed key handler", func(t *testing.T) {
		kh := ui.NewKeyHandler()
		kh.Start()
		t.Cleanup(kh.Stop)
		kh.Arm()
		session := &ChatSession{KeyHandler: kh}
		if !chatEscapeInterruptAvailable(session) {
			t.Fatal("armed key handler must advertise an ESC consumer")
		}
		if !kh.Armed() || kh.Suspended() {
			t.Fatalf("unexpected key handler state armed=%v suspended=%v", kh.Armed(), kh.Suspended())
		}
	})

	t.Run("suspended key handler", func(t *testing.T) {
		kh := ui.NewKeyHandler()
		kh.Start()
		t.Cleanup(kh.Stop)
		kh.Arm()
		kh.Suspend()
		session := &ChatSession{KeyHandler: kh}
		if chatEscapeInterruptAvailable(session) {
			t.Fatal("suspended key handler must not advertise an ESC consumer")
		}
	})

	t.Run("busy input capture", func(t *testing.T) {
		queue := newChatInputQueue(bufio.NewReader(strings.NewReader("")))
		queue.setExternalInputCaptureActive(true)
		session := &ChatSession{InputQueue: queue}
		if !chatEscapeInterruptAvailable(session) {
			t.Fatal("active busy capture must advertise an ESC consumer")
		}
	})
}

func TestBuildChatDynamicStatusModelEscHintMatchesConsumer(t *testing.T) {
	status := chatSurfaceStatus{kind: chatSurfaceStatusRetrying, detail: "step=1 attempt=2/3"}

	withEsc := buildChatDynamicStatusModelForWidthInputModeCompletionAndEsc(
		status, 160, chatInputModeChat, 5*time.Second, false, true,
	)
	if withEsc == nil {
		t.Fatal("retrying status must produce a dynamic model")
	}
	wantWithEsc := "◦ Retrying step=1 attempt=2/3 (5s • esc to interrupt)"
	if plain := style.StatusLineDocument(*withEsc, 160).PlainText(); plain != wantWithEsc {
		t.Fatalf("esc-available hint = %q, want %q", plain, wantWithEsc)
	}

	withoutEsc := buildChatDynamicStatusModelForWidthInputModeCompletionAndEsc(
		status, 160, chatInputModeChat, 5*time.Second, false, false,
	)
	if withoutEsc == nil {
		t.Fatal("retrying status must produce a dynamic model")
	}
	wantWithoutEsc := "◦ Retrying step=1 attempt=2/3 (5s • ctrl+c to stop)"
	if plain := style.StatusLineDocument(*withoutEsc, 160).PlainText(); plain != wantWithoutEsc {
		t.Fatalf("esc-unavailable hint = %q, want %q", plain, wantWithoutEsc)
	}
}
