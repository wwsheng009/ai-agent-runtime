package commands

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

// 锁序回归（2026-10-07 会话死锁）：控制序列适配器运行在
// renderengine.WriteTerminalText 持有的终端写锁之内；该路径不得反向获取
// coordinator.mu（paint 路径锁序为 c.mu -> surface.mu -> terminalWriteMu）。
// 修复前 UnifiedRendererActive/WriteTerminalTitle 会在终端锁内等待 c.mu，
// 与持 c.mu 等终端锁的 paint 路径构成 AB-BA 死锁，整会话挂死。
func TestChatControlSequenceWriterDoesNotWaitForCoordinatorMutex(t *testing.T) {
	session := &ChatSession{}
	coordinator := newTestChatInteractionCoordinator(t, session)
	t.Cleanup(coordinator.Shutdown)
	session.Interaction = coordinator

	var raw bytes.Buffer
	writer := chatControlSequenceWriter{
		session: session,
		raw:     &raw,
		submit: func(c *chatInteractionCoordinator, s string) bool {
			return c.WriteTerminalTitle(s)
		},
	}
	const title = "\x1b]0;lock-order\x07"

	// 模拟 paint 路径：持 c.mu 的同时，控制序列写路径（持终端写锁）必须
	// 能够完成，而不是等待 c.mu。
	coordinator.mu.Lock()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = ui.WriteTerminalText(writer, title)
	}()
	select {
	case <-done:
		coordinator.mu.Unlock()
	case <-time.After(2 * time.Second):
		coordinator.mu.Unlock()
		// 解锁后写路径必须能完成（旧实现正是阻塞在 c.mu 上）；等待它退出，
		// 避免把终端写锁泄漏给后续用例。
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("control-sequence write path still stuck after coordinator.mu release")
		}
		t.Fatal("control-sequence write path waits for coordinator.mu (lock-order inversion)")
	}
	if raw.String() != title {
		t.Fatalf("legacy raw fallback = %q, want %q", raw.String(), title)
	}
}

// unified 路由同样必须无 c.mu：终端锁内不得等待 coordinator.mu，且字节必须
// 经 session writer 落盘（不得回退 raw）。
func TestChatControlSequenceWriterUnifiedRouteDoesNotWaitForCoordinatorMutex(t *testing.T) {
	session := &ChatSession{}
	coordinator := newTestChatInteractionCoordinator(t, session)
	t.Cleanup(coordinator.Shutdown)
	session.Interaction = coordinator

	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(72, 18)
	surface.SetPhysicalWritesEnabled(false)
	coordinator.SetSurface(surface)
	var terminal bytes.Buffer
	if !coordinator.enableUnifiedRendererWithWriter(&terminal) {
		t.Fatal("unified renderer did not attach")
	}
	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)
	terminal.Reset()

	var raw bytes.Buffer
	writer := chatControlSequenceWriter{
		session: session,
		raw:     &raw,
		submit: func(c *chatInteractionCoordinator, s string) bool {
			return c.WriteTerminalTitle(s)
		},
	}
	const title = "\x1b]0;lock-order-unified\x07"

	// 直接调用 writer（不经 ui.WriteTerminalText 的外层终端锁）：test 直写
	// presenter 在 flush 时会重新进入终端写锁，外层再持锁会与本次修复无关地
	// 自锁；生产 gateway 路由不走 presenter flush。
	coordinator.mu.Lock()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = writer.Write([]byte(title))
	}()
	select {
	case <-done:
		coordinator.mu.Unlock()
	case <-time.After(2 * time.Second):
		coordinator.mu.Unlock()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("unified control-sequence write path still stuck after coordinator.mu release")
		}
		t.Fatal("unified control-sequence path waits for coordinator.mu (lock-order inversion)")
	}
	if raw.Len() != 0 {
		t.Fatalf("unified route must not touch the raw writer, got %q", raw.String())
	}
	if !strings.Contains(terminal.String(), title) {
		t.Fatalf("title bytes did not reach the unified writer: %q", terminal.String())
	}
}

// unified 会话下 submit 失败：必须 fail-closed——不得回退 raw writer，
// 否则 OSC/BEL 字节会绕过 session writer 与帧交错（G2/A1-3）。
func TestChatControlSequenceWriterUnifiedSubmitFailureFailsClosed(t *testing.T) {
	session := &ChatSession{}
	coordinator := newTestChatInteractionCoordinator(t, session)
	t.Cleanup(coordinator.Shutdown)
	session.Interaction = coordinator

	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(72, 18)
	surface.SetPhysicalWritesEnabled(false)
	coordinator.SetSurface(surface)
	var terminal bytes.Buffer
	if !coordinator.enableUnifiedRendererWithWriter(&terminal) {
		t.Fatal("unified renderer did not attach")
	}
	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)

	var raw bytes.Buffer
	writer := chatControlSequenceWriter{
		session: session,
		raw:     &raw,
		submit:  func(*chatInteractionCoordinator, string) bool { return false },
	}
	const title = "\x1b]0;unified-title\x07"
	if _, err := writer.Write([]byte(title)); err != nil {
		t.Fatalf("write title: %v", err)
	}
	if raw.Len() != 0 {
		t.Fatalf("unified submit failure must not fall back to the raw writer, got %q", raw.String())
	}
}

// 非 unified 会话：控制序列写回 legacy raw writer（字节不变）。
func TestChatControlSequenceWriterFallsBackToRawWriter(t *testing.T) {
	session := &ChatSession{}
	coordinator := newTestChatInteractionCoordinator(t, session)
	t.Cleanup(coordinator.Shutdown)
	session.Interaction = coordinator

	var raw bytes.Buffer
	writer := chatControlSequenceWriter{
		session: session,
		raw:     &raw,
		submit: func(coordinator *chatInteractionCoordinator, sequence string) bool {
			return coordinator.WriteTerminalBell(sequence)
		},
	}
	if _, err := writer.Write([]byte("\a")); err != nil {
		t.Fatalf("write bell: %v", err)
	}
	if raw.String() != "\a" {
		t.Fatalf("raw fallback = %q, want bell", raw.String())
	}
}

// unified 会话：标题序列经 TerminalSession 提交（不再触碰 raw writer）。
func TestChatControlSequenceWriterRoutesThroughUnifiedSession(t *testing.T) {
	session := &ChatSession{}
	coordinator := newTestChatInteractionCoordinator(t, session)
	t.Cleanup(coordinator.Shutdown)
	session.Interaction = coordinator

	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(72, 18)
	surface.SetPhysicalWritesEnabled(false)
	coordinator.SetSurface(surface)
	var terminal bytes.Buffer
	if !coordinator.enableUnifiedRendererWithWriter(&terminal) {
		t.Fatal("unified renderer did not attach")
	}
	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)
	terminal.Reset()

	var raw bytes.Buffer
	writer := chatControlSequenceWriter{
		session: session,
		raw:     &raw,
		submit: func(coordinator *chatInteractionCoordinator, sequence string) bool {
			return coordinator.WriteTerminalTitle(sequence)
		},
	}
	const title = "\x1b]0;unified-title\x07"
	if _, err := writer.Write([]byte(title)); err != nil {
		t.Fatalf("write title: %v", err)
	}
	if raw.Len() != 0 {
		t.Fatalf("raw fallback must stay empty when the unified session claims, got %q", raw.String())
	}
	if !strings.Contains(terminal.String(), title) {
		t.Fatalf("title bytes did not reach the unified writer: %q", terminal.String())
	}
}
