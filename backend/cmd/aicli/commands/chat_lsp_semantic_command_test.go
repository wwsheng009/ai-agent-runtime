package commands

import (
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/knowledge"
)

// chat_lsp_semantic_command_test.go 覆盖 /lsp semantic 的渲染：命令存在的意义是
// 把「code_* 为什么没有语义精度」从不可见变成可读。

func TestChatLSPSemanticHintCoversEveryStableReason(t *testing.T) {
	// 每个稳定 reason token 都必须有可行动提示——token 只保证机器可读，
	// 用户要的是"那我改什么"。漏一个就等于该降级原因仍然不可排查。
	for _, reason := range []string{
		knowledge.SemanticReasonDisabled,
		knowledge.SemanticReasonModeNotSelf,
		knowledge.SemanticReasonNoLanguage,
		knowledge.SemanticReasonWorkspace,
		knowledge.SemanticReasonServerSpecMiss,
	} {
		hint := chatLSPSemanticHint(knowledge.SemanticChannelStatus{State: "degraded", Reason: reason})
		if hint == "" {
			t.Fatalf("reason %q 没有提示", reason)
		}
		if !strings.HasPrefix(hint, "提示:") {
			t.Fatalf("reason %q 的提示格式不对: %q", reason, hint)
		}
	}
}

func TestChatLSPSemanticHintExplainsLockAndStates(t *testing.T) {
	lock := chatLSPSemanticHint(knowledge.SemanticChannelStatus{
		State: "degraded", Reason: "knowledge/lsp: lock held by a live process: pid=123",
	})
	if !strings.Contains(lock, "锁") {
		t.Fatalf("锁降级提示未提到锁: %q", lock)
	}
	idle := chatLSPSemanticHint(knowledge.SemanticChannelStatus{
		State: "idle", Reason: "not started (lazy; starts on first semantic query)",
	})
	if !strings.Contains(idle, "惰性") {
		t.Fatalf("idle 提示未说明惰性启动: %q", idle)
	}
	crashed := chatLSPSemanticHint(knowledge.SemanticChannelStatus{
		State: "crashed", Reason: "language server exited",
	})
	if !strings.Contains(crashed, "source=index") {
		t.Fatalf("crashed 提示未说明降级后果: %q", crashed)
	}
}

func TestChatLSPSemanticHintEmptyWhenHealthy(t *testing.T) {
	// 全健康时不产生噪音：状态行已经说清楚了。
	if hint := chatLSPSemanticHint(knowledge.SemanticChannelStatus{State: "ready", PID: 9}); hint != "" {
		t.Fatalf("ready 不应有提示: %q", hint)
	}
}

func TestChatLSPSemanticStatusLine(t *testing.T) {
	line := chatLSPSemanticStatusLine(knowledge.SemanticChannelStatus{
		State: "ready", PID: 11, Root: "/w/backend",
	})
	for _, want := range []string{"gopls", "ready", "pid=11", "module_root=/w/backend"} {
		if !strings.Contains(line, want) {
			t.Fatalf("line %q 缺少 %q", line, want)
		}
	}
	degraded := chatLSPSemanticStatusLine(knowledge.SemanticChannelStatus{
		State: "degraded", Reason: "lsp_disabled",
		LockPath: "/w/backend/.aicli/knowledge/lsp/gopls-x.lock",
	})
	if !strings.Contains(degraded, "lock=") {
		t.Fatalf("降级行必须显示锁路径（排障需要）: %q", degraded)
	}
	if !strings.Contains(degraded, "reason=lsp_disabled") {
		t.Fatalf("降级行必须显示原因: %q", degraded)
	}
	// 正常态不显示锁路径：那是噪声路径。
	ready := chatLSPSemanticStatusLine(knowledge.SemanticChannelStatus{
		State: "ready", LockPath: "/w/backend/.aicli/knowledge/lsp/gopls-x.lock",
	})
	if strings.Contains(ready, "lock=") {
		t.Fatalf("正常态不应显示锁路径: %q", ready)
	}
}

func TestChatLSPUsageMentionsSemanticSubcommand(t *testing.T) {
	if !strings.Contains(chatLSPCommandUsage, "/lsp semantic") {
		t.Fatalf("用法里缺少 semantic 子命令:\n%s", chatLSPCommandUsage)
	}
}

func TestChatLSPSemanticHintExplainsSharedInstance(t *testing.T) {
	// 共享进程退出时的降级原因必须指向宿主重启，而不是让用户以为语义通道崩了。
	hint := chatLSPSemanticHint(knowledge.SemanticChannelStatus{
		State: "degraded", Shared: true, Reason: "shared language server exited (host restarting?)",
	})
	if !strings.Contains(hint, "restart") && !strings.Contains(hint, "重启") {
		t.Fatalf("共享态降级提示应指向 /lsp restart: %q", hint)
	}
}
