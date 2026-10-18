package commands

import (
	"context"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

// /stop（2026-10-18 入口审计 R3）：可键入的中断入口，与 Esc 共用唯一决策点。
// 本组用例锁定：注册表/catalog 覆盖、忙时策略为 immediate、运行中执行中断、
// 空闲不产生中断。

func TestStopCommandRegisteredAndBusyImmediate(t *testing.T) {
	spec, ok := resolveRuntimeCommandSpec("/stop")
	if !ok {
		t.Fatal("/stop 未在运行时注册表登记")
	}
	if spec.Mode != runtimeModeInline {
		t.Fatalf("/stop 忙时必须是 inline 档，got %s", spec.Mode)
	}
	if got := chatInputCommandBusyPolicy(&ChatSession{}, "/stop"); got != chatBusyPolicyImmediate {
		t.Fatalf("/stop 忙时策略 = %v，期望 immediate", got)
	}

	found := false
	for _, spec := range chatSlashCommandCatalog() {
		if spec.Name == "/stop" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("/stop 未登记进 slash 命令目录（补全/帮助不可见）")
	}
}

func TestStopCommandInterruptsActiveTurn(t *testing.T) {
	kh := ui.NewKeyHandler()
	kh.Start()
	defer kh.Stop()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	session := &ChatSession{KeyHandler: kh, cancelCtx: ctx, cancelFunc: cancel}
	coord := newTestChatInteractionCoordinator(t, session)
	session.Interaction = coord
	stopConsumer := startChatEscapeInterruptWatcher(session)
	defer stopConsumer()

	result := executeStructuredStopCommand(session)
	if !session.IsInterrupted() {
		t.Fatal("/stop 必须中断在途回合")
	}
	select {
	case <-ctx.Done():
	default:
		t.Fatal("/stop 必须取消在途回合的 context")
	}
	if !strings.Contains(renderCommandResultText(result), "已请求停止") {
		t.Fatalf("/stop 回执缺少停止说明: %q", renderCommandResultText(result))
	}
}

func TestStopCommandIdleDoesNotInterrupt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	session := &ChatSession{cancelCtx: ctx, cancelFunc: cancel}
	coord := newTestChatInteractionCoordinator(t, session)
	session.Interaction = coord

	result := executeStructuredStopCommand(session)
	if session.IsInterrupted() {
		t.Fatal("空闲 /stop 不得置中断标记")
	}
	select {
	case <-ctx.Done():
		t.Fatal("空闲 /stop 不得取消 context")
	default:
	}
	if !strings.Contains(renderCommandResultText(result), "没有在途回合") {
		t.Fatalf("空闲 /stop 回执必须说明无目标: %q", renderCommandResultText(result))
	}
}

// 2026-10-18 真机会话 session_20261018233755_ZxzOaDFH：Web 注入一次输入会把
// webCaptureActive 粘滞置真（直到下一次控制台前台回合释放）。它是"外部客户端
// 接管输入面"的标记，不是"有在途回合"，/stop 与 ESC 可用性都不得据此误报。
func TestStopCommandIgnoresStaleWebCaptureLatch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	queue := newChatInputQueue(nil)
	queue.setWebInputCaptureActive(true) // Web 注入后的粘滞标记
	session := &ChatSession{InputQueue: queue, cancelCtx: ctx, cancelFunc: cancel}
	coord := newTestChatInteractionCoordinator(t, session)
	session.Interaction = coord

	if chatStopCommandHasTarget(session) {
		t.Fatal("Web 粘滞标记不得被判为在途回合")
	}
	if chatEscapeInterruptAvailable(session) {
		t.Fatal("Web 粘滞标记不得承诺 esc to interrupt（它不消费 Esc）")
	}
	result := executeStructuredStopCommand(session)
	if session.IsInterrupted() {
		t.Fatal("空闲 /stop 不得置中断标记（Web 粘滞标记场景）")
	}
	if !strings.Contains(renderCommandResultText(result), "没有在途回合") {
		t.Fatalf("回执必须说明无目标: %q", renderCommandResultText(result))
	}

	// 控制台 busy capture 仍然必须被认作“有目标”（它是真实的 Esc 消费者）。
	queue.setExternalInputCaptureActive(true)
	if !chatStopCommandHasTarget(session) {
		t.Fatal("控制台 busy capture 活跃时必须判为有在途回合")
	}
	if !chatEscapeInterruptAvailable(session) {
		t.Fatal("控制台 busy capture 活跃时必须承诺可中断")
	}
}

// renderCommandResultText 提取命令结果的首个文本块，用于断言回执内容。
func renderCommandResultText(result CommandResult) string {
	if len(result.Blocks) == 0 {
		return ""
	}
	doc := result.Blocks[0].Document
	var sb strings.Builder
	for _, block := range doc.Blocks {
		for _, line := range block.Lines {
			for _, span := range line.Spans {
				sb.WriteString(span.Text)
			}
			sb.WriteByte('\n')
		}
	}
	return sb.String()
}
