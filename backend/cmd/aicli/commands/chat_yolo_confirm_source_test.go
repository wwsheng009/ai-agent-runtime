package commands

import (
	"bufio"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

// §13 F3：bypass 的二次确认语义是「终端前的人确认」。终端的 stdin 与 Web 注入
// 共用同一条优先级队列，只有 chatQueuedInput.Source 能区分两者，这里就验证门
// 真的按来源放行/拒绝，并且拒绝时尽量不丢用户输入。
//
// 下面的用例走 `/permission-mode bypass_permissions`：它与 `/yolo` 共用
// evalBypassPermissionModeConfirmation 这一个门（/yolo 的独立分支只是统一渲染
// 通道不同），因此对确认语义的验证等价。

type bypassConfirmOutcome struct {
	result  CommandResult
	handled bool
	err     error
}

func newBypassConfirmSession() *ChatSession {
	session := &ChatSession{
		PermissionMode:    "default",
		ApprovalReuseMode: chatApprovalReuseSessionReadOnlyShell,
	}
	session.InputQueue = newChatInputQueue(bufio.NewReader(strings.NewReader("")))
	// 与既有用例一致：外部捕获开着时优先级读取不再启动 stdin pump，测试只喂队列。
	session.InputQueue.setExternalInputCaptureActive(true)
	return session
}

func startBypassConfirm(t *testing.T, session *ChatSession, command string) <-chan bypassConfirmOutcome {
	t.Helper()
	done := make(chan bypassConfirmOutcome, 1)
	go func() {
		result, handled, err := tryExecuteStructuredChatCommand(session, command)
		done <- bypassConfirmOutcome{result: result, handled: handled, err: err}
	}()
	requireEventuallyPriorityMode(t, session.InputQueue)
	return done
}

func TestBypassConfirmRejectsWebInjectedLine(t *testing.T) {
	session := newBypassConfirmSession()
	done := startBypassConfirm(t, session, "/permission-mode bypass_permissions")

	session.InputQueue.routeLine(chatQueuedInput{
		Text:       "bypass_permissions",
		Source:     chatInputSourceWeb,
		EnqueuedAt: time.Now().UTC(),
	})
	outcome := <-done

	if outcome.err != nil || !outcome.handled {
		t.Fatalf("bypass confirm match=(%t, %v), want handled", outcome.handled, outcome.err)
	}
	plain := ui.RenderDocumentPlain(outcome.result.Document())
	if !strings.Contains(plain, "必须来自本机终端输入") || !strings.Contains(plain, chatInputSourceWeb) {
		t.Fatalf("web-injected confirm must be refused with source reason:\n%s", plain)
	}
	if session.PermissionMode == "bypass_permissions" {
		t.Fatal("web-injected line must not switch to bypass_permissions")
	}
	// 无损回填：该行按原文本/来源回到队列最前，不会被当成确认。
	item, ok := session.InputQueue.takeQueuedFront()
	if !ok {
		t.Fatal("refused line should be requeued, not dropped")
	}
	if item.Text != "bypass_permissions" || item.Source != chatInputSourceWeb {
		t.Fatalf("requeued item=(%q,%q), want original text/source", item.Text, item.Source)
	}
}

func TestBypassConfirmAcceptsLocalTerminalLine(t *testing.T) {
	session := newBypassConfirmSession()
	done := startBypassConfirm(t, session, "/permission-mode bypass_permissions")

	session.InputQueue.routeLine(chatQueuedInput{
		Text:       "bypass_permissions",
		Source:     chatInputSourceStdin,
		EnqueuedAt: time.Now().UTC(),
	})
	outcome := <-done

	if outcome.err != nil || !outcome.handled {
		t.Fatalf("bypass confirm match=(%t, %v), want handled", outcome.handled, outcome.err)
	}
	if session.PermissionMode != "bypass_permissions" {
		t.Fatalf("permission mode=%q want bypass_permissions", session.PermissionMode)
	}
}

func TestBypassConfirmDropsWebInjectedSlashCommand(t *testing.T) {
	session := newBypassConfirmSession()
	done := startBypassConfirm(t, session, "/permission-mode bypass_permissions")

	session.InputQueue.routeLine(chatQueuedInput{
		Text:       "/permission-mode bypass_permissions",
		Source:     chatInputSourceWeb,
		EnqueuedAt: time.Now().UTC(),
	})
	outcome := <-done

	plain := ui.RenderDocumentPlain(outcome.result.Document())
	if !strings.Contains(plain, "已忽略") {
		t.Fatalf("slash command from web should be reported as ignored:\n%s", plain)
	}
	if _, ok := session.InputQueue.takeQueuedFront(); ok {
		t.Fatal("slash command from web must not be requeued (prompt-loop guard)")
	}
	if session.PermissionMode == "bypass_permissions" {
		t.Fatal("web-injected slash command must not switch mode")
	}
}

func TestReadPriorityItemPreservesSource(t *testing.T) {
	queue := newChatInputQueue(bufio.NewReader(strings.NewReader("")))
	queue.setExternalInputCaptureActive(true)

	items := make(chan chatQueuedInput, 1)
	errs := make(chan error, 1)
	go func() {
		item, err := queue.readPriorityItemWithPrompt(context.Background(), "")
		if err != nil {
			errs <- err
			return
		}
		items <- item
	}()
	requireEventuallyPriorityMode(t, queue)
	queue.routeLine(chatQueuedInput{Text: "answer", Source: chatInputSourceWeb, EnqueuedAt: time.Now().UTC()})

	select {
	case item := <-items:
		if item.Text != "answer" || item.Source != chatInputSourceWeb {
			t.Fatalf("priority item=(%q,%q), want text/source preserved", item.Text, item.Source)
		}
	case err := <-errs:
		t.Fatalf("readPriorityItemWithPrompt failed: %v", err)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for priority item")
	}
}
