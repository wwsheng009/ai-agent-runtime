package commands

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// TestExitThenResumeKeepsLastTurn 复现：长会话（150 条消息）中
// 完成最后一轮后退出，再 resume，最后 turn 的 user/assistant 消息不应丢失。
//
// 完整链路：
//  1. actor 侧：每次 turn 从 store 加载会话、追加消息、persistSession(Update)
//  2. CLI 侧：turn 结束后 syncRuntimeSessionBackIntoCLI（Get + restore + Update）
//  3. 退出：finalizeChatSessionWithError -> syncRuntimeSessionFromChat(Update)
//  4. resume：Get + restore + loadResumeCanonicalHistory（canonical 全量回放）
func TestExitThenResumeKeepsLastTurn(t *testing.T) {
	storage, err := runtimechat.NewSQLiteSessionStorage(runtimechat.DefaultPersistentSessionStorageConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("new sqlite storage: %v", err)
	}
	manager := runtimechat.NewSessionManager(storage, &runtimechat.SessionManagerConfig{
		TTL:             24 * time.Hour,
		CleanupInterval: 0,
		AutoArchive:     false,
	})
	defer manager.Stop()

	ctx := context.Background()
	session, err := manager.Create(ctx, "tester")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	sessionID := session.ID

	// 阶段 0：先写满 150 条历史（长会话），模拟已进行多轮。
	seed := make([]runtimetypes.Message, 0, 150)
	for index := 0; index < 150; index++ {
		if index%2 == 0 {
			seed = append(seed, *runtimetypes.NewUserMessage(fmt.Sprintf("seed user %d", index)))
		} else {
			seed = append(seed, *runtimetypes.NewAssistantMessage(fmt.Sprintf("seed assistant %d", index)))
		}
	}
	session.ReplaceHistory(seed)
	if err := storage.Save(ctx, session); err != nil {
		t.Fatalf("seed save: %v", err)
	}

	// 阶段 1：模拟最后一轮 turn（用户提问 + 助手回答），按 actor 的持久化方式：
	// 从 store 重新加载（拿到投影 History + CanonicalMessageCount），追加消息后 Update。
	lastUser := "final turn user question"
	lastAssistant := "final turn assistant answer"
	for turn := 0; turn < 2; turn++ {
		actorSession, err := manager.Get(ctx, sessionID)
		if err != nil {
			t.Fatalf("actor load: %v", err)
		}
		actorSession.AddMessage(*runtimetypes.NewUserMessage(lastUser))
		actorSession.AddMessage(*runtimetypes.NewAssistantMessage(lastAssistant))
		if err := manager.Update(ctx, actorSession); err != nil {
			t.Fatalf("actor persist turn %d: %v", turn, err)
		}
	}

	// 阶段 2：CLI 退出路径 —— Get + restoreChatStateFromRuntimeSession + syncRuntimeSessionFromChat。
	cli := &ChatSession{
		SessionManager: manager,
		SessionUserID:  "tester",
	}
	loaded, err := manager.Get(ctx, sessionID)
	if err != nil {
		t.Fatalf("cli load: %v", err)
	}
	if err := restoreChatStateFromRuntimeSession(cli, loaded); err != nil {
		t.Fatalf("cli restore: %v", err)
	}
	if err := syncRuntimeSessionFromChat(cli); err != nil {
		t.Fatalf("cli sync (exit): %v", err)
	}

	// 阶段 3：resume —— Get + restore + loadResumeCanonicalHistory。
	resumed := &ChatSession{
		SessionManager: manager,
		SessionUserID:  "tester",
	}
	loadedResume, err := manager.Get(ctx, sessionID)
	if err != nil {
		t.Fatalf("resume load: %v", err)
	}
	if err := restoreChatStateFromRuntimeSession(resumed, loadedResume); err != nil {
		t.Fatalf("resume restore: %v", err)
	}
	loadResumeCanonicalHistory(resumed, sessionID)
	if len(resumed.ResumeHistory) == 0 {
		t.Fatalf("resume history empty")
	}

	visible := collectVisibleChatHistory(resumed)
	if len(visible) == 0 {
		t.Fatalf("visible history empty")
	}
	last := visible[len(visible)-1]
	if last.Content != lastAssistant {
		t.Fatalf("last visible message = %q, want %q (last turn lost?)\nvisible tail: %s",
			last.Content, lastAssistant, formatResumeHistoryForTest(tailMessages(visible, 6)))
	}
	// 最后 turn 的用户提问也必须还在。
	if visible[len(visible)-2].Content != lastUser {
		t.Fatalf("second-to-last visible message = %q, want %q", visible[len(visible)-2].Content, lastUser)
	}
}

func tailMessages(messages []runtimetypes.Message, count int) []runtimetypes.Message {
	if len(messages) <= count {
		return messages
	}
	return messages[len(messages)-count:]
}

// TestSendMessagePersistsUserPromptAtInputTime 验证修复：用户输入 prompt 时
// （appendChatTurnUserMessage，在 ensureChatExecutor 之前调用）就立即把该 turn
// 的用户消息追加到 session.Messages 并持久化到存储。即使 executor 尚未运行
// （或后续失败/进程被中断），最后 turn 的用户消息也不会在退出时丢失。
func TestSendMessagePersistsUserPromptAtInputTime(t *testing.T) {
	storage, err := runtimechat.NewSQLiteSessionStorage(runtimechat.DefaultPersistentSessionStorageConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("new sqlite storage: %v", err)
	}
	manager := runtimechat.NewSessionManager(storage, &runtimechat.SessionManagerConfig{
		TTL:             24 * time.Hour,
		CleanupInterval: 0,
		AutoArchive:     false,
	})
	defer manager.Stop()

	ctx := context.Background()
	created, err := manager.Create(ctx, "tester")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	sessionID := created.ID

	// CLI 侧会话：从 store 恢复，建立 RuntimeSession 以便 syncRuntimeSessionFromChat。
	cli := &ChatSession{
		SessionManager: manager,
		SessionUserID:  "tester",
	}
	loaded, err := manager.Get(ctx, sessionID)
	if err != nil {
		t.Fatalf("cli load: %v", err)
	}
	if err := restoreChatStateFromRuntimeSession(cli, loaded); err != nil {
		t.Fatalf("cli restore: %v", err)
	}

	prompt := "last turn prompt entered by user"
	// 模拟 actor executor：先请求一个 turn identity，之后必须带同一 ID
	// 进入 actor，确保 capture 门禁能在 canonical user 行上命中它。
	turnID := "turn_" + uuid.NewString()
	appendChatTurnUserMessageWithTurnID(cli, prompt, turnID)

	if count := len(cli.Messages); count == 0 {
		t.Fatalf("session.Messages empty after appendChatTurnUserMessage")
	} else if last := cli.Messages[count-1]; last.Role != "user" || last.Content != prompt {
		t.Fatalf("last in-memory message = %q/%q, want user/%q", last.Role, last.Content, prompt)
	}

	// 从 store 重新读取，确认该 turn 的用户消息已被持久化（退出时不再丢失）。
	reloaded, err := manager.Get(ctx, sessionID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	history := reloaded.History
	if len(history) == 0 {
		t.Fatalf("stored history empty after appendChatTurnUserMessage")
	}
	lastStored := history[len(history)-1]
	if lastStored.Role != "user" || lastStored.Content != prompt {
		t.Fatalf("stored last message = %q/%q, want user/%q (last turn lost on exit?)",
			lastStored.Role, lastStored.Content, prompt)
	}
	if got := runtimetypes.TurnID(lastStored); got != turnID {
		t.Fatalf("pre-persisted user turn_id = %q, want actor request %q", got, turnID)
	}

	// durable suspension may override the provisional ID. Re-key the same
	// trailing user row rather than persisting a duplicate prompt.
	selectedTurnID := "turn_parked"
	before := len(cli.Messages)
	appendChatTurnUserMessageWithTurnID(cli, prompt, selectedTurnID)
	if len(cli.Messages) != before {
		t.Fatalf("actor identity change duplicated prompt: before=%d after=%d", before, len(cli.Messages))
	}
	if got := runtimetypes.TurnID(cli.Messages[len(cli.Messages)-1]); got != selectedTurnID {
		t.Fatalf("re-keyed in-memory user turn_id = %q, want actor-selected %q", got, selectedTurnID)
	}
	rekeyed, err := manager.Get(ctx, sessionID)
	if err != nil {
		t.Fatalf("reload re-keyed session: %v", err)
	}
	if got := runtimetypes.TurnID(rekeyed.History[len(rekeyed.History)-1]); got != selectedTurnID {
		t.Fatalf("re-keyed stored user turn_id = %q, want actor-selected %q", got, selectedTurnID)
	}

	// 重复应用同一 actor 选择应幂等。
	appendChatTurnUserMessageWithTurnID(cli, prompt, selectedTurnID)
	if len(cli.Messages) != before {
		t.Fatalf("same turn prompt appended again: before=%d after=%d", before, len(cli.Messages))
	}
	if count := len(cli.Messages); count == 0 || cli.Messages[count-1].Content != prompt {
		t.Fatalf("dedup failed: last in-memory message no longer the prompt")
	}
	if strings.Count(lastStoredContent(cli), prompt) > 1 {
		t.Fatalf("dedup failed: prompt appears more than once in memory")
	}
}

func lastStoredContent(cli *ChatSession) string {
	if cli == nil || len(cli.Messages) == 0 {
		return ""
	}
	return cli.Messages[len(cli.Messages)-1].Content
}
