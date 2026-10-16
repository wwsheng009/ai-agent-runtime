package commands

import (
	"sync"
	"testing"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
)

// TestChatRuntimeContextMap_LiveReadersDoNotRaceWriters 守护本次修复的核心不变量：
// 只要 ChatSession 持有 live runtime session，任何展示/恢复读路径都不许绕过
// runtimeContextMapMu 直接读 Metadata.Context。
//
// 旧实现（restoreChatRuntimeContext / attachChatCompactLineage /
// appendChatLoadSessionMeta / appendChatLoadCompactLineage）直接读
// session.RuntimeSession.Metadata.Context，与 TUI 侧的加锁写入构成
// "concurrent map read and map write"——Go 运行时对 map 的这类竞争是不可 recover
// 的硬致命错误（见回归现场：sessionmeta.Value ← runtimeSessionContextString ←
// chatSessionRoutingOverrideRaw），只能在锁纪律层面预防。
//
// 本用例的判定力来自 -race：无竞争时它只是把读写路径跑热；一旦某个读路径退回
// 裸读共享 map，-race 会立刻报 concurrent map read/write。
func TestChatRuntimeContextMap_LiveReadersDoNotRaceWriters(t *testing.T) {
	runtimeSession := runtimechat.NewSession("tester")
	session := &ChatSession{}
	setChatRuntimeSessionPointer(session, runtimeSession)

	// 写入方：TUI 侧在 runtimeContextMapMu 下更新共享 context map。
	writeCtx := func() {
		withRuntimeContextWrite(session, func(ctx map[string]interface{}) {
			ctx[chatRuntimeContextApprovalReuse] = "always"
			ctx[chatRuntimeContextPermissionMode] = "default"
			ctx[chatRuntimeContextDebugMode] = true
			ctx[chatRuntimeContextSelectedAgent] = "/root/agent-1"
			ctx[chatRuntimeContextActiveTeamID] = "team-1"
			ctx[chatRuntimeContextActiveAgentID] = "agent-1"
			ctx[chatRuntimeContextActiveTaskID] = "task-1"
			ctx[runtimechat.ContextCompactGeneration] = 3
			ctx[runtimechat.ContextCompactRootTitle] = "root-title"
			ctx[runtimechat.ContextCompactRootSessionID] = "root-session"
		})
	}
	// 先同步写一次，让读方断言有确定初值（后台写入是持续并发噪声）。
	writeCtx()

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			writeCtx()
		}
	}()
	defer wg.Wait()
	defer close(stop)

	// 读取方：展示 / 恢复路径必须全程只在叶子读锁内触碰该 map。
	for i := 0; i < 200; i++ {
		restoreChatRuntimeContext(session)
		if session.SelectedAgentTarget != "/root/agent-1" {
			t.Fatalf("selected agent target 未被恢复: got %q", session.SelectedAgentTarget)
		}
		if session.ActiveTeam == nil || session.ActiveTeam.TeamID != "team-1" {
			t.Fatalf("active team 未被恢复: got %+v", session.ActiveTeam)
		}

		report := &chatCompactReport{}
		attachChatCompactLineage(report, session)
		if report.Generation != 3 {
			t.Fatalf("compact generation 未被读出: got %d", report.Generation)
		}
		if report.RootTitle != "root-title" {
			t.Fatalf("compact root title 未被读出: got %q", report.RootTitle)
		}

		var lineageBuilder chatDebugDocumentBuilder
		appendChatLoadCompactLineage(&lineageBuilder, session)

		var metaBuilder chatDebugDocumentBuilder
		appendChatLoadSessionMeta(&metaBuilder, session)
	}
}
