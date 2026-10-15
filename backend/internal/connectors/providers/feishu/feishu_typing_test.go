package feishu

// 打字表情测试（feishu_typing.go）：
//   - 有回复目标 → POST 加 Typing 表情（emoji_type 正确）→ Stop DELETE 摘除；
//   - 无回复目标 → (nil, nil)，零请求；
//   - 加表情失败 → (nil, nil)（无 typing 能力）；
//   - Stop 幂等（第二次无请求）且摘除失败不抛错。

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
)

// TestStartTypingAddsAndStopsReaction 锁定全链路：入站记忆回复目标 → StartTyping
// 加表情 → Stop 摘除（DELETE 到 reaction_id 路径）；重复 Stop 幂等。
func TestStartTypingAddsAndStopsReaction(t *testing.T) {
	fake := newFakeFeishu(t)
	fake.defaultTokenHandler("t-1", 7200)
	fake.onJSON(messageCreatePath, http.StatusOK, map[string]any{"code": 0, "msg": "ok"})

	var mu sync.Mutex
	createCount, deleteCount := 0, 0
	createEmoji := ""
	reactionPath := "/open-apis/im/v1/messages/om_msg_1/reactions"
	fake.on(reactionPath, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		mu.Lock()
		defer mu.Unlock()
		switch r.Method {
		case http.MethodPost:
			createCount++
			if reactionType, ok := body["reaction_type"].(map[string]any); ok {
				createEmoji, _ = reactionType["emoji_type"].(string)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0, "msg": "ok", "data": map[string]any{"reaction_id": "reaction_1"},
			})
		case http.MethodDelete:
			deleteCount++
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "msg": "ok"})
		default:
			http.Error(w, "unexpected method", http.StatusMethodNotAllowed)
		}
	})
	fake.on(reactionPath+"/reaction_1", func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		mu.Lock()
		defer mu.Unlock()
		if r.Method != http.MethodDelete {
			http.Error(w, "unexpected method", http.StatusMethodNotAllowed)
			return
		}
		deleteCount++
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "msg": "ok"})
	})
	provider := newTestProvider(t, fake)

	// 入站一条消息（建立回复目标记忆 om_in_1）。
	if _, err := provider.HandleWebhook(context.Background(), testConfig(),
		webhookRequest(t, messageEventPayload("evt-typing-1", "text", `{"text":"你好"}`))); err != nil {
		t.Fatalf("HandleWebhook: %v", err)
	}

	session, err := provider.StartTyping(context.Background(), testConfig(), "oc_chat_1")
	if err != nil {
		t.Fatalf("StartTyping: %v", err)
	}
	if session == nil {
		t.Fatal("expected typing session after inbound message")
	}
	mu.Lock()
	if createCount != 1 || createEmoji != typingReactionEmoji {
		t.Fatalf("reaction create = %d emoji %q, want 1 %q", createCount, createEmoji, typingReactionEmoji)
	}
	mu.Unlock()

	if err := session.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := session.Stop(context.Background()); err != nil { // 幂等
		t.Fatalf("second Stop: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if deleteCount != 1 {
		t.Fatalf("reaction delete count = %d, want 1 (idempotent)", deleteCount)
	}
}

// TestStartTypingWithoutReplyTargetReturnsNil 锁定无回复目标：零请求、nil 会话。
func TestStartTypingWithoutReplyTargetReturnsNil(t *testing.T) {
	fake := newFakeFeishu(t)
	fake.defaultTokenHandler("t-1", 7200)
	provider := newTestProvider(t, fake)

	session, err := provider.StartTyping(context.Background(), testConfig(), "oc_chat_none")
	if err != nil {
		t.Fatalf("StartTyping: %v", err)
	}
	if session != nil {
		t.Fatalf("expected nil session without reply target, got %T", session)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.requests) != 0 {
		t.Fatalf("expected zero requests, got %d", len(fake.requests))
	}
}

// TestStartTypingReactionFailureDegrades 锁定加表情失败：返回 (nil, nil)。
func TestStartTypingReactionFailureDegrades(t *testing.T) {
	fake := newFakeFeishu(t)
	fake.defaultTokenHandler("t-1", 7200)
	fake.onJSON(messageCreatePath, http.StatusOK, map[string]any{"code": 0, "msg": "ok"})
	fake.onJSON("/open-apis/im/v1/messages/om_msg_1/reactions", http.StatusOK,
		map[string]any{"code": 230020, "msg": "reaction rejected"})
	provider := newTestProvider(t, fake)

	if _, err := provider.HandleWebhook(context.Background(), testConfig(),
		webhookRequest(t, messageEventPayload("evt-typing-2", "text", `{"text":"你好"}`))); err != nil {
		t.Fatalf("HandleWebhook: %v", err)
	}
	session, err := provider.StartTyping(context.Background(), testConfig(), "oc_chat_1")
	if err != nil {
		t.Fatalf("StartTyping should degrade on reaction failure: %v", err)
	}
	if session != nil {
		t.Fatalf("expected nil session on reaction failure, got %T", session)
	}
}

// TestTypingStopFailureIsSwallowed 锁定摘除失败不抛错（体验项 best-effort）。
func TestTypingStopFailureIsSwallowed(t *testing.T) {
	fake := newFakeFeishu(t)
	fake.defaultTokenHandler("t-1", 7200)
	fake.onJSON(messageCreatePath, http.StatusOK, map[string]any{"code": 0, "msg": "ok"})
	fake.on("/open-apis/im/v1/messages/om_msg_1/reactions", func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		if r.Method == http.MethodPost {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0, "msg": "ok", "data": map[string]any{"reaction_id": "reaction_3"},
			})
			return
		}
		// DELETE：返回 5xx 触发摘除失败。
		http.Error(w, "server error", http.StatusInternalServerError)
	})
	fake.on("/open-apis/im/v1/messages/om_msg_1/reactions/reaction_3", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		// DELETE 子路径：返回 5xx 触发摘除失败（Stop 应 best-effort 吞掉）。
		http.Error(w, "server error", http.StatusInternalServerError)
	})
	provider := newTestProvider(t, fake)

	if _, err := provider.HandleWebhook(context.Background(), testConfig(),
		webhookRequest(t, messageEventPayload("evt-typing-3", "text", `{"text":"你好"}`))); err != nil {
		t.Fatalf("HandleWebhook: %v", err)
	}
	session, err := provider.StartTyping(context.Background(), testConfig(), "oc_chat_1")
	if err != nil || session == nil {
		t.Fatalf("StartTyping = (%v, %v)", session, err)
	}
	if err := session.Stop(context.Background()); err != nil {
		t.Fatalf("Stop should be best-effort, got %v", err)
	}
}
