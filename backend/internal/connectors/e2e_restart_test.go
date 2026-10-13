// Package connectors_test 是连接器核心的包外端到端测试：Telegram Provider（真实 HTTP，
// 指向 httptest 假 Bot API）+ Service（去重/队列/流式回执）+ storefile（config.yaml 与
// state.json 文件态）。覆盖 docs/bots/04-migration-roadmap.md P0 验收中可自动化的部分：
// 「Telegram（polling）↔ 宿主端到端；重启进程后 offset 续传不重复」——
// 进程内重启 = 重建 Store/Service/Provider，复用同一份 state.json，并让假 Bot API
// 重投同一条 update（模拟平台未 ack / at-least-once 投递）。
package connectors_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
	"github.com/wwsheng009/ai-agent-runtime/internal/connectors/providers/telegram"
	"github.com/wwsheng009/ai-agent-runtime/internal/connectors/storefile"
)

// ── 假 Bot API ─────────────────────────────────────────────────────────────

// fakeBotAPI 总是重投同一条 update：模拟平台 at-least-once 投递（宿主未 ack 游标
// 或平台重试时，重启后仍会收到同一 update）。所有回复（sendMessage/editMessageText）
// 记录在案，供断言"只回复一次"。
type fakeBotAPI struct {
	mu sync.Mutex

	update          map[string]any
	getUpdatesCalls int
	offsets         []int64
	sent            []string
	edits           []string
}

func (f *fakeBotAPI) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
		if len(parts) != 2 || !strings.HasPrefix(parts[0], "bot") {
			http.NotFound(w, r)
			return
		}
		method := parts[1]
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)

		switch method {
		case "getMe":
			writeFakeJSON(w, map[string]any{"ok": true, "result": map[string]any{
				"id": 1, "username": "e2e_bot", "first_name": "E2E",
			}})
		case "getUpdates":
			f.mu.Lock()
			f.getUpdatesCalls++
			if offset, ok := body["offset"].(float64); ok {
				f.offsets = append(f.offsets, int64(offset))
			}
			update := f.update
			f.mu.Unlock()
			// 模拟一次长轮询的往返延迟，避免测试期空转。
			time.Sleep(5 * time.Millisecond)
			if update == nil {
				writeFakeJSON(w, map[string]any{"ok": true, "result": []map[string]any{}})
				return
			}
			writeFakeJSON(w, map[string]any{"ok": true, "result": []map[string]any{update}})
		case "sendMessage":
			f.mu.Lock()
			text, _ := body["text"].(string)
			f.sent = append(f.sent, text)
			nextID := int64(len(f.sent))
			f.mu.Unlock()
			writeFakeJSON(w, map[string]any{"ok": true, "result": map[string]any{"message_id": nextID}})
		case "editMessageText":
			f.mu.Lock()
			text, _ := body["text"].(string)
			f.edits = append(f.edits, text)
			f.mu.Unlock()
			writeFakeJSON(w, map[string]any{"ok": true, "result": map[string]any{}})
		case "sendChatAction":
			writeFakeJSON(w, map[string]any{"ok": true, "result": true})
		default:
			writeFakeJSON(w, map[string]any{"ok": false, "error_code": 404, "description": "Not Found"})
		}
	})
}

func (f *fakeBotAPI) getUpdatesCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.getUpdatesCalls
}

// offsetAt 返回第 index 次 getUpdates 携带的 offset（不存在时为 -1）。
func (f *fakeBotAPI) offsetAt(index int) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	if index < 0 || index >= len(f.offsets) {
		return -1
	}
	return f.offsets[index]
}

func (f *fakeBotAPI) replies() (sent, edits []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.sent...), append([]string(nil), f.edits...)
}

// replyCount 统计某条回复文本在 sendMessage/editMessageText 里出现的次数。
func (f *fakeBotAPI) replyCount(text string) int {
	sent, edits := f.replies()
	count := 0
	for _, value := range append(sent, edits...) {
		if strings.TrimSpace(value) == text {
			count++
		}
	}
	return count
}

func writeFakeJSON(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}

// ── 计数后端 ───────────────────────────────────────────────────────────────

// countingBackend 模拟宿主 agent 后端（runtime-server Backend 的最小替身）：
// 记录每次被处理的文本，并回一条固定格式的回复。
type countingBackend struct {
	mu    sync.Mutex
	texts []string
}

func (b *countingBackend) Name() string { return "e2e-backend" }

func (b *countingBackend) Handle(_ context.Context, req connectors.BackendRequest, _ connectors.ReplySink) (connectors.BackendResult, error) {
	b.mu.Lock()
	b.texts = append(b.texts, req.Text)
	b.mu.Unlock()
	return connectors.BackendResult{
		Session: connectors.SessionRef{SessionID: "sess-e2e"},
		Reply:   []connectors.OutboundMessage{{Text: "回复:" + req.Text}},
	}, nil
}

func (b *countingBackend) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.texts)
}

// ── 用例 ───────────────────────────────────────────────────────────────────

func TestTelegramPollingRestartResumesWithoutReplay(t *testing.T) {
	api := &fakeBotAPI{update: textUpdate(5, 100, 200, 7, "你好")}
	server := httptest.NewServer(api.handler())
	defer server.Close()

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	statePath := filepath.Join(dir, "data", "connectors", "state.json")

	store := storefile.NewConfigStore(configPath, statePath)
	if err := store.PutConfig(connectors.Config{
		ID:       "tg-main",
		Provider: telegram.Name,
		Name:     "Telegram e2e",
		Enabled:  true,
		Mode:     "polling",
		Settings: map[string]string{"api_base": server.URL, "poll_timeout_seconds": "1"},
		Secrets:  map[string]string{"bot_token": "tok"},
	}); err != nil {
		t.Fatalf("PutConfig: %v", err)
	}

	// 第一次运行：处理一条消息，并经流式占位 + editMessageText 回执。
	backend := &countingBackend{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	service := connectors.New(connectors.Deps{Store: store, Backend: backend, Logf: func(string, ...any) {}})
	if err := service.Register(telegram.New(telegram.WithRetryDelay(10 * time.Millisecond))); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := service.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	waitFor(t, 3*time.Second, "首条消息被后端处理", func() bool { return backend.count() == 1 })
	waitFor(t, 3*time.Second, "回复送达假 Bot API", func() bool { return api.replyCount("回复:你好") == 1 })
	waitFor(t, 3*time.Second, "轮询游标落盘（update_id 5 → offset 6）", func() bool {
		value, ok := store.PollingState("tg-main").Get("update_offset")
		return ok && value == "6"
	})
	baselinePolls := api.getUpdatesCount()
	service.Stop(context.Background())
	cancel()

	// 重启：重建 Store/Service/Provider，复用同一份 config.yaml + state.json。
	reopened := storefile.NewConfigStore(configPath, statePath)
	if value, ok := reopened.PollingState("tg-main").Get("update_offset"); !ok || value != "6" {
		t.Fatalf("重启后游标应续传为 6, got %q (ok=%v)", value, ok)
	}
	restartedBackend := &countingBackend{}
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	service2 := connectors.New(connectors.Deps{Store: reopened, Backend: restartedBackend, Logf: func(string, ...any) {}})
	if err := service2.Register(telegram.New(telegram.WithRetryDelay(10 * time.Millisecond))); err != nil {
		t.Fatalf("Register(restart): %v", err)
	}
	if err := service2.Start(ctx2); err != nil {
		t.Fatalf("Start(restart): %v", err)
	}
	defer service2.Stop(context.Background())

	// 假 Bot API 重投同一条 update：等待至少一次重投，再确认没有第二次处理与第二次回复。
	waitFor(t, 3*time.Second, "重启后收到同一 update 的重投", func() bool {
		return api.getUpdatesCount() > baselinePolls
	})
	if got := api.offsetAt(baselinePolls); got != 6 {
		t.Fatalf("重启后第一次 getUpdates 应续传 offset=6, got %d", got)
	}
	time.Sleep(300 * time.Millisecond)

	if got := restartedBackend.count(); got != 0 {
		t.Fatalf("重启后同一 update 不应被再次处理, got %d 次", got)
	}
	if got := backend.count(); got != 1 {
		t.Fatalf("首次运行处理次数 = %d, want 1", got)
	}
	if got := api.replyCount("回复:你好"); got != 1 {
		sent, edits := api.replies()
		t.Fatalf("回复应恰好一次, got %d（sent=%v edits=%v）", got, sent, edits)
	}
}

// ── 辅助 ───────────────────────────────────────────────────────────────────

func textUpdate(updateID, messageID, chatID, userID int64, text string) map[string]any {
	return map[string]any{
		"update_id": updateID,
		"message": map[string]any{
			"message_id": messageID,
			"from":       map[string]any{"id": userID, "username": "alice"},
			"chat":       map[string]any{"id": chatID, "type": "private"},
			"text":       text,
		},
	}
}

func waitFor(t *testing.T, timeout time.Duration, label string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("等待超时：%s", label)
}
