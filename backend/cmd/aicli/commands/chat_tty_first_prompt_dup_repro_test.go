package commands

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/vt"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// TestTTY_LiveLoop_FirstPromptRendersOnce 复现 2026-11-02 TUI 现场：
// 全新会话在真实主循环里发送首条用户 prompt；live echo 提交后才挂上 runtime
// host（能力面异步发现晚于用户输入），actor 首次 start 不得把事件日志里刚
// 写入的当前 prompt 再铸造一次。最终终端投影（scrollback + 视口）里该 prompt
// 只能出现一次。
func TestTTY_LiveLoop_FirstPromptRendersOnce(t *testing.T) {
	const (
		marker = "FIRST-TUI-PROMPT-DUP-20261102"
		reply  = "TUI-FIRST-PROMPT-REPLY"
	)
	run := runTTYLiveLoop(t, reply, []ttyLiveScriptStep{
		{wait: 700 * time.Millisecond, line: marker + "\n"},
		{waitReady: true, wait: 100 * time.Millisecond, line: "/exit\n"},
	}, func(session *ChatSession, executor *fakeChatExecutor) {
		// sendMessage 的调用顺序是 renderSubmittedUserInputEcho → executor。
		// 在 executor 里才挂 host，等价于真机“首条 prompt 提交后才完成能力面
		// 装载”，也正是旧 start() 把 replay 推迟到首个 turn 的条件。
		executor.onCall = func(_ context.Context, session *ChatSession, _ string) (string, error) {
			if session.LocalRuntimeHost == nil {
				session.LocalRuntimeHost = &localChatRuntimeHost{EventBus: runtimeevents.NewBus()}
			}
			ensureChatRuntimeEventBridge(session)
			return reply, nil
		}
	})
	if !run.executor.called {
		t.Fatal("fakeChatExecutor 未被调用（首条 prompt 未进入真实循环）")
	}

	// 最终终端投影 = 原生 scrollback + 当前视口。用户看到的重复行来自
	// 这两者中的任一侧；只断言 raw 字节会把 composer 的输入帧也算进来。
	screen := vt.NewScreen(80, 24)
	screen.Feed(run.raw)
	projected := strings.Join(append(screen.ScrollbackLines(), screen.Lines(0, 24)...), "\n")
	if count := strings.Count(projected, marker); count != 1 {
		t.Fatalf("首条 TUI prompt 在最终终端投影中出现 %d 次，want 1\nprojection:\n%s",
			count, projected)
	}
	// 回复也必须正常出现，排除“渲染被整体压掉”的假绿。
	if !strings.Contains(projected, reply) {
		t.Fatalf("最终终端投影缺少 assistant 回复 %q\nprojection:\n%s", reply, projected)
	}
	if bridge := run.session.RuntimeEventBridge; bridge != nil {
		if _, _, replayed, _ := bridge.eventLogStats(); replayed != 0 {
			t.Fatalf("首条 TUI prompt 提交后仍重放了事件日志: replayed=%d, want 0", replayed)
		}
	}
}
