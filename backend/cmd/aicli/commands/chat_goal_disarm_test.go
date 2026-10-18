package commands

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	runtimegoal "github.com/wwsheng009/ai-agent-runtime/internal/goal"
)

// G4 rearm 闸（DSH goal 借鉴，docs/analysis/dsh-goal-domain-vs-aar-goal-20261017.md
// §2.3/G4）：恢复含 active goal 的会话后，用户任意消息不得触发自动续跑；
// /goal resume（或显式继续意图）重新武装。
func TestGoalDisarmGateBlocksContinuationAfterResume(t *testing.T) {
	session, cleanup := newGoalAutoContinueTestSession(t, runtimegoal.StatusActive)
	defer cleanup()
	session.ChatExecutor = &aicliSharedChatExecutor{}
	session.goalContinuationDisarmed.Store(true)

	decision, err := shouldAutoContinueActiveGoalDecision(session)
	require.NoError(t, err)
	require.False(t, decision.Continue, "disarmed session must not auto-continue")
	require.Equal(t, "goal_disarmed", decision.Reason)

	// rearm 后同一 active goal 恢复可续跑（其余判定路径不变）。
	session.goalContinuationDisarmed.Store(false)
	decision, err = shouldAutoContinueActiveGoalDecision(session)
	require.NoError(t, err)
	require.True(t, decision.Continue, "re-armed session with active goal must auto-continue")
	require.Equal(t, "goal_active", decision.Reason)
}

func TestDisarmGoalContinuationAfterRestore(t *testing.T) {
	session, cleanup := newGoalAutoContinueTestSession(t, runtimegoal.StatusActive)
	defer cleanup()
	session.goalContinuationDisarmed.Store(true)

	// active goal：restore 后保持 disarmed。
	disarmGoalContinuationAfterRestore(session)
	require.True(t, session.goalContinuationDisarmed.Load(), "restore with an active goal keeps the gate set")

	// 非 active goal（如 complete）：restore 不置闸。
	session.goalContinuationDisarmed.Store(false)
	goal, ok, err := currentSessionGoal(session)
	require.NoError(t, err)
	require.True(t, ok)
	goal.Status = runtimegoal.StatusComplete
	goal.CompletedBy = "user"
	goal.UpdatedAt = time.Now()
	if err := runtimegoal.NewMetadataStore().Put(session.RuntimeSession, *goal); err != nil {
		t.Fatalf("mark goal complete: %v", err)
	}
	disarmGoalContinuationAfterRestore(session)
	require.False(t, session.goalContinuationDisarmed.Load(), "restore without an active goal must not disarm")
}

func TestGoalResumeCommandRearmsContinuation(t *testing.T) {
	session, cleanup := newGoalAutoContinueTestSession(t, runtimegoal.StatusPaused)
	defer cleanup()
	session.goalContinuationDisarmed.Store(true)

	output := captureStdout(t, func() {
		handleGoalCommand(session, "/goal resume")
	})
	require.Contains(t, output, "Goal 已恢复")
	require.False(t, session.goalContinuationDisarmed.Load(), "/goal resume must re-arm the continuation gate")
}

func TestGoalStatusShowsDisarmedHint(t *testing.T) {
	session, cleanup := newGoalAutoContinueTestSession(t, runtimegoal.StatusActive)
	defer cleanup()
	session.goalContinuationDisarmed.Store(true)

	output := captureStdout(t, func() {
		printGoalStatus(session, false)
	})
	require.Contains(t, output, goalDisarmedStatusHint, "disarmed active goal must surface the hint")

	// armed（默认）时不出现提示。
	session.goalContinuationDisarmed.Store(false)
	output = captureStdout(t, func() {
		printGoalStatus(session, false)
	})
	require.NotContains(t, output, goalDisarmedStatusHint)
}

// TestGoalContinueIntentMatcher 锁定「明示继续」的保守匹配面：只认短促显式
// 继续指令，查询类消息与长句一律不命中（误判的代价是幽灵自动续跑）。
func TestGoalContinueIntentMatcher(t *testing.T) {
	positive := []string{
		"继续", "继续。", "继续！", " 继续 ", "继续吧", "继续执行", "请继续",
		"接着来", "往下做", "恢复", "干吧",
		"continue", "Continue", "continue.", "please continue", "go on",
		"keep going", "carry on", "proceed", "resume", "keep working",
	}
	for _, message := range positive {
		if !isGoalContinueIntentMessage(message) {
			t.Fatalf("expected %q to be recognized as an explicit continue intent", message)
		}
	}

	negative := []string{
		"", "   ", "看一下当前状态", "现在进度如何？", "继续看看还有什么问题",
		"概括一下", "暂停", "停", "继续之前先看一下状态", "你好", "continue 后面还有别的要求",
	}
	for _, message := range negative {
		if isGoalContinueIntentMessage(message) {
			t.Fatalf("expected %q NOT to be recognized as a continue intent", message)
		}
	}
}

// TestContinueIntentMessageRearmsAndResumesGoal 验证 G4 入口 2（2026-10-18 修复）：
// 恢复态（disarmed）下用户发「继续」消息——turn 入口 rearm，turn 结束后既有
// 续跑决策立即接上自动续跑，无需再执行 /goal resume。
func TestContinueIntentMessageRearmsAndResumesGoal(t *testing.T) {
	session, cleanup := newGoalAutoContinueTestSession(t, runtimegoal.StatusActive)
	defer cleanup()
	session.goalContinuationDisarmed.Store(true)
	session.goalMaxContinuations = 1

	executor := &fakeChatExecutor{output: "resumed by continue intent"}
	session.ChatExecutor = executor

	if _, err := sendMessage(session, "继续"); err != nil {
		t.Fatalf("sendMessage failed: %v", err)
	}
	require.False(t, session.goalContinuationDisarmed.Load(), "explicit continue intent must re-arm the gate")
	require.Equal(t, 1, executor.continuations, "re-armed session must resume auto-continuation after the user turn")
}

// TestQueryMessageDoesNotRearmDisarmedGoal 锁定恢复态闸门的本意：查询类普通
// 消息不得 rearm（rearm 即意味着 turn 结束后开启自主续跑）。
func TestQueryMessageDoesNotRearmDisarmedGoal(t *testing.T) {
	session, cleanup := newGoalAutoContinueTestSession(t, runtimegoal.StatusActive)
	defer cleanup()
	session.goalContinuationDisarmed.Store(true)

	executor := &fakeChatExecutor{output: "status report"}
	session.ChatExecutor = executor

	if _, err := sendMessage(session, "看一下当前状态"); err != nil {
		t.Fatalf("sendMessage failed: %v", err)
	}
	require.True(t, session.goalContinuationDisarmed.Load(), "ordinary query must keep the gate disarmed")
	require.Equal(t, 0, executor.continuations, "disarmed session must not auto-continue after a query")
}

// TestGoalStatusBarShowsIdleWhenDisarmed 锁定状态栏可见性修复：disarmed 的
// active goal 必须显示 "Goal idle (/goal resume)" 而不是 "Pursuing goal"
// （后者会让用户误以为目标正在推进）。
func TestGoalStatusBarShowsIdleWhenDisarmed(t *testing.T) {
	session, cleanup := newGoalAutoContinueTestSession(t, runtimegoal.StatusActive)
	defer cleanup()
	session.goalContinuationDisarmed.Store(true)

	status := buildChatSurfaceStatusLineForWidth(session, "Ready", 160)
	require.Contains(t, status, "Goal idle (/goal resume)", "disarmed active goal must render the idle indicator")
	require.NotContains(t, status, "Pursuing goal", "disarmed active goal must not claim to be pursuing")

	session.goalContinuationDisarmed.Store(false)
	status = buildChatSurfaceStatusLineForWidth(session, "Ready", 160)
	require.Contains(t, status, "Pursuing goal", "armed active goal keeps the original indicator")
}

// TestStructuredGoalResumeCommandRearmsContinuation 锁定 2026-10-18 真机根因：
// web/TUI 实际命中结构化统一命令面（executeStructuredGoalStatusChange），其
// resume 分支必须与 legacy handleGoalCommand 一样清 disarmed 标记；否则只改
// durable 状态、kick 被消费后仍按 goal_disarmed 跳过——表现为"执行 /goal
// resume 没有作用"（真机 07:42 复现：无 rearm 日志 + goal_disarmed skip）。
func TestStructuredGoalResumeCommandRearmsContinuation(t *testing.T) {
	session, cleanup := newGoalAutoContinueTestSession(t, runtimegoal.StatusActive)
	defer cleanup()
	session.goalContinuationDisarmed.Store(true)

	_, handled, err := executeStructuredGoalStatusChange(session, "resume")
	require.NoError(t, err)
	require.True(t, handled)
	require.False(t, session.goalContinuationDisarmed.Load(),
		"structured /goal resume must re-arm the continuation gate")
}

// TestGoalCommandEntryParity_ResumeRearmsAndKicks 是入口收敛后的防漂移门禁：
// legacy handleGoalCommand 与结构化统一分派两条入口，对 disarmed 的 active goal
// 执行 resume 都必须 (a) 清 disarmed 闸、(b) 请求续跑 kick——语义单点
// applyGoalStatusChange 的契约，任何一侧回退都会在这里红。
func TestGoalCommandEntryParity_ResumeRearmsAndKicks(t *testing.T) {
	entries := []struct {
		name string
		run  func(t *testing.T, session *ChatSession) error
	}{
		{"legacy", func(t *testing.T, session *ChatSession) error {
			captureStdout(t, func() {
				handleGoalCommand(session, "/goal resume")
			})
			return nil
		}},
		{"structured", func(t *testing.T, session *ChatSession) error {
			_, handled, err := tryExecuteStructuredChatCommand(session, "/goal resume")
			if err != nil {
				return err
			}
			if !handled {
				return fmt.Errorf("/goal resume not handled by structured dispatch")
			}
			return nil
		}},
	}

	for _, entry := range entries {
		session, cleanup := newGoalAutoContinueTestSession(t, runtimegoal.StatusActive)
		session.goalContinuationDisarmed.Store(true)
		err := entry.run(t, session)
		require.NoError(t, err, entry.name)
		require.False(t, session.goalContinuationDisarmed.Load(), entry.name+": /goal resume must re-arm the gate")
		require.True(t, session.goalKickPending.Load(), entry.name+": /goal resume must request a continuation kick")
		cleanup()
	}
}

// TestGoalCommandEntryParity_StatusShowsDisarmedHint 防漂移门禁：两条命令入口的
// /goal status 都必须输出 disarmed 提示（goalStatusTrailingHints 单点契约）。
func TestGoalCommandEntryParity_StatusShowsDisarmedHint(t *testing.T) {
	session, cleanup := newGoalAutoContinueTestSession(t, runtimegoal.StatusActive)
	defer cleanup()
	session.goalContinuationDisarmed.Store(true)

	legacyOut := captureStdout(t, func() {
		handleGoalCommand(session, "/goal status")
	})
	require.Contains(t, legacyOut, goalDisarmedStatusHint, "legacy /goal status must show the disarmed hint")

	result, handled, err := tryExecuteStructuredChatCommand(session, "/goal status")
	require.NoError(t, err)
	require.True(t, handled)
	plain := ui.RenderDocumentPlain(result.Document())
	require.Contains(t, plain, goalDisarmedStatusHint, "structured /goal status must show the disarmed hint")
}
