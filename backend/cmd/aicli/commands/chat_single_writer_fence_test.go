package commands

import (
	"bytes"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

// singleWriterProbe 是注入的物理 writer：统计写次数并保留字节。
type singleWriterProbe struct {
	mu    sync.Mutex
	calls int
	buf   bytes.Buffer
}

func (p *singleWriterProbe) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	return p.buf.Write(b)
}

func (p *singleWriterProbe) Reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = 0
	p.buf.Reset()
}

func (p *singleWriterProbe) Snapshot() (int, string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls, p.buf.String()
}

// TestUnifiedSessionSinglePhysicalWriterFence 是 P0 单写端运行时栅栏：统一会话
// 存活期间，标题/铃/编辑器模式序列/动态诊断/直写输出/命令输出全部必须落在注入
// 的同一物理 writer 上；进程 stdout/stderr（第二写端候选）必须零字节。
func TestUnifiedSessionSinglePhysicalWriterFence(t *testing.T) {
	previousSink := chatDiagnosticSink.Swap(nil)
	t.Cleanup(func() { chatDiagnosticSink.Store(previousSink) })

	session := &ChatSession{}
	bridge := newChatRuntimeEventBridge(session)
	session.RuntimeEventBridge = bridge
	coordinator := newTestChatInteractionCoordinator(t, session)
	session.Interaction = coordinator
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(120, 30)
	surface.SetPhysicalWritesEnabled(false)
	coordinator.SetSurface(surface)
	session.Surface = surface

	var probe singleWriterProbe
	if !coordinator.enableUnifiedRendererWithWriter(&probe) {
		t.Fatal("unified renderer did not attach")
	}
	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)
	probe.Reset()

	const title = "\x1b]0;single-writer\x07"
	stdout, stderr := captureStdoutStderr(t, func() {
		// 适配器在捕获内构造：raw 兜底绑定到被捕获的 os.Stdout，任何回退
		// 泄漏都会被下面的断言抓住。
		titleWriter := chatControlSequenceWriter{
			session: session,
			raw:     os.Stdout,
			submit: func(c *chatInteractionCoordinator, s string) bool {
				return c.WriteTerminalTitle(s)
			},
		}
		bellWriter := chatControlSequenceWriter{
			session: session,
			raw:     os.Stdout,
			submit: func(c *chatInteractionCoordinator, s string) bool {
				return c.WriteTerminalBell(s)
			},
		}
		if _, err := titleWriter.Write([]byte(title)); err != nil {
			t.Fatalf("write title: %v", err)
		}
		if _, err := bellWriter.Write([]byte("\a")); err != nil {
			t.Fatalf("write bell: %v", err)
		}
		if !coordinator.WritePromptEditorControl("\x1b[?2004h") {
			t.Fatal("prompt editor control was not claimed by the unified session")
		}
		NotifyChatDiagnostic("Warning: single-writer probe")
		printDirectInteractiveOutput(session, "single-writer direct output\n")
		printChatCommandOutput(session, "single-writer command output\n")
		coordinator.waitUIActorIdle()
	})
	awaitUnifiedPresenterIdle(t, coordinator)

	if strings.TrimSpace(stdout) != "" || strings.TrimSpace(stderr) != "" {
		t.Fatalf("second writer leaked during unified session: stdout=%q stderr=%q", stdout, stderr)
	}
	calls, out := probe.Snapshot()
	if calls == 0 {
		t.Fatal("unified physical writer received no bytes")
	}
	for _, want := range []string{
		title,
		"\a",
		"\x1b[?2004h",
		"single-writer direct output",
		"single-writer command output",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("unified physical writer missing %q", want)
		}
	}
}
