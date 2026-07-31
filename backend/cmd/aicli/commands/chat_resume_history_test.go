package commands

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// TestLoadResumeCanonicalHistoryRestoresFullTranscript 验证 Phase 1 核心行为：
// 压缩/截断后的热上下文投影（session_prompt_messages，≤128 条）不应成为
// /resume 回放的唯一来源——恢复后应展示 canonical 完整转录（session_messages）。
func TestLoadResumeCanonicalHistoryRestoresFullTranscript(t *testing.T) {
	storage, err := runtimechat.NewSQLiteSessionStorage(runtimechat.DefaultPersistentSessionStorageConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("new sqlite storage: %v", err)
	}
	manager := runtimechat.NewSessionManager(storage, &runtimechat.SessionManagerConfig{
		TTL:             24 * time.Hour,
		MaxHistory:      0,
		CleanupInterval: 0,
		AutoArchive:     false,
	})
	defer manager.Stop()

	ctx := context.Background()
	session, err := manager.Create(ctx, "tester")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	const messageCount = 150 // 超过 HotHistoryMessages=128
	messages := make([]runtimetypes.Message, 0, messageCount)
	for index := 0; index < messageCount; index++ {
		if index%2 == 0 {
			messages = append(messages, *runtimetypes.NewUserMessage(fmt.Sprintf("user %d", index)))
		} else {
			messages = append(messages, *runtimetypes.NewAssistantMessage(fmt.Sprintf("assistant %d", index)))
		}
	}
	session.ReplaceHistory(messages)
	if err := storage.Save(ctx, session); err != nil {
		t.Fatalf("save session: %v", err)
	}

	// 重载后：模型热上下文投影应被截断到 HotHistoryMessages 以内。
	loaded, err := manager.Get(ctx, session.ID)
	if err != nil {
		t.Fatalf("reload session: %v", err)
	}
	if len(loaded.History) >= messageCount {
		t.Fatalf("expected prompt projection truncated below %d, got %d", messageCount, len(loaded.History))
	}
	if len(loaded.History) == 0 {
		t.Fatalf("expected non-empty prompt projection after save")
	}

	// commands 层恢复路径：投影写入 Messages，再加载 canonical 完整历史。
	chatSession := &ChatSession{
		SessionManager: manager,
		SessionUserID:  "tester",
	}
	if err := replaceRuntimeMessages(chatSession, loaded.History); err != nil {
		t.Fatalf("replace runtime messages: %v", err)
	}
	loadResumeCanonicalHistory(chatSession, session.ID)
	if len(chatSession.ResumeHistory) != messageCount {
		t.Fatalf("expected resume history to restore full transcript (%d), got %d", messageCount, len(chatSession.ResumeHistory))
	}
	if chatSession.ResumeHistory[0].Content != "user 0" {
		t.Fatalf("expected resume history to start at the first message, got %q", chatSession.ResumeHistory[0].Content)
	}
	if chatSession.ResumeHistory[messageCount-1].Content != "assistant 149" {
		t.Fatalf("expected resume history to end at the last message, got %q", chatSession.ResumeHistory[messageCount-1].Content)
	}

	// 展示层回放完整历史（不依赖截断投影）。
	visible := collectVisibleChatHistory(chatSession)
	if len(visible) != messageCount {
		t.Fatalf("expected %d visible history messages, got %d", messageCount, len(visible))
	}

	// 继续对话：新消息同步追加到完整历史回放。
	appendRuntimeMessage(chatSession, *runtimetypes.NewAssistantMessage("post-resume reply"))
	if after := collectVisibleChatHistory(chatSession); len(after) != messageCount+1 {
		t.Fatalf("expected %d visible messages after resume append, got %d", messageCount+1, len(after))
	}
	if last := chatSession.ResumeHistory[len(chatSession.ResumeHistory)-1].Content; last != "post-resume reply" {
		t.Fatalf("expected appended message in resume history, got %q", last)
	}

	// 上下文整体替换后展示快照失效：回退到投影展示。
	if err := replaceRuntimeMessages(chatSession, loaded.History); err != nil {
		t.Fatalf("replace runtime messages after append: %v", err)
	}
	if chatSession.ResumeHistory != nil {
		t.Fatalf("expected resume history cleared on context replacement")
	}
	if after := collectVisibleChatHistory(chatSession); len(after) != len(loaded.History) {
		t.Fatalf("expected fallback to prompt projection after replacement, got %d", len(after))
	}
}

// TestLoadResumeCanonicalHistoryFallbackNoPager 验证无分页后端（文件/内存）
// 优雅降级：ResumeHistory 保持为空，展示回退到投影历史。
func TestLoadResumeCanonicalHistoryFallbackNoPager(t *testing.T) {
	storage, err := runtimechat.NewFileStorage(t.TempDir())
	if err != nil {
		t.Fatalf("new file storage: %v", err)
	}
	manager := runtimechat.NewSessionManager(storage, &runtimechat.SessionManagerConfig{
		TTL:             24 * time.Hour,
		MaxHistory:      0,
		CleanupInterval: 0,
		AutoArchive:     false,
	})
	defer manager.Stop()

	ctx := context.Background()
	session, err := manager.Create(ctx, "tester")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	session.ReplaceHistory([]runtimetypes.Message{
		*runtimetypes.NewUserMessage("hello"),
		*runtimetypes.NewAssistantMessage("world"),
	})
	if err := manager.Update(ctx, session); err != nil {
		t.Fatalf("update session: %v", err)
	}

	chatSession := &ChatSession{
		SessionManager: manager,
		SessionUserID:  "tester",
	}
	loadResumeCanonicalHistory(chatSession, session.ID)
	if len(chatSession.ResumeHistory) != 0 {
		t.Fatalf("expected no resume history for non-pager backend, got %d", len(chatSession.ResumeHistory))
	}
	if err := replaceRuntimeMessages(chatSession, session.History); err != nil {
		t.Fatalf("replace runtime messages: %v", err)
	}
	visible := collectVisibleChatHistory(chatSession)
	if len(visible) != 2 {
		t.Fatalf("expected fallback projection visible history of 2, got %d: %s", len(visible), formatResumeHistoryForTest(visible))
	}
}

func formatResumeHistoryForTest(messages []runtimetypes.Message) string {
	parts := make([]string, 0, len(messages))
	for _, message := range messages {
		parts = append(parts, fmt.Sprintf("%s:%s", strings.TrimSpace(message.Role), strings.TrimSpace(message.Content)))
	}
	return strings.Join(parts, " | ")
}
