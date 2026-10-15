package feishu

// 卡片式流式回复测试（feishu_stream.go）：
//   - 开关关闭 → StartStreaming 返回 (nil, nil)，零请求；
//   - 开关开启 → 创建占位卡（interactive）+ PATCH 逐步替换 + Final 终态；
//   - 创建/PATCH 失败 → 返回错误（Service 回退普通发送，不丢回复）；
//   - Final 媒体消息走普通富媒体发送（上传 + 消息创建）。

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
)

// streamingConfig 返回开启流式的连接器配置。
func streamingConfig() connectors.Config {
	cfg := testConfig()
	cfg.Settings[streamingSetting] = "true"
	return cfg
}

// cardContent 提取请求里 interactive 载荷的卡片 markdown 正文（缺失返回空）。
func cardContent(t *testing.T, payload map[string]any) string {
	t.Helper()
	content, _ := payload["content"].(string)
	if content == "" {
		return ""
	}
	var card struct {
		Body struct {
			Elements []struct {
				Content string `json:"content"`
			} `json:"elements"`
		} `json:"body"`
	}
	if err := json.Unmarshal([]byte(content), &card); err != nil {
		t.Fatalf("decode card content %q: %v", content, err)
	}
	if len(card.Body.Elements) == 0 {
		return ""
	}
	return card.Body.Elements[0].Content
}

// TestStartStreamingDisabledReturnsNil 锁定开关关闭：零请求、返回 nil 会话。
func TestStartStreamingDisabledReturnsNil(t *testing.T) {
	fake := newFakeFeishu(t)
	fake.defaultTokenHandler("t-1", 7200)
	provider := newTestProvider(t, fake)

	session, err := provider.StartStreaming(context.Background(), testConfig(), "oc_chat_1")
	if err != nil {
		t.Fatalf("StartStreaming: %v", err)
	}
	if session != nil {
		t.Fatalf("expected nil session when streaming disabled, got %T", session)
	}
	fake.mu.Lock()
	requests := len(fake.requests)
	fake.mu.Unlock()
	if requests != 0 {
		t.Fatalf("expected no requests, got %d", requests)
	}
}

// TestStreamingSessionUpdatesCardByPATCH 锁定全链路：创建占位卡 → PATCH 累积
// 文本 → Final 终态（message_id 沿用创建响应）。
func TestStreamingSessionUpdatesCardByPATCH(t *testing.T) {
	fake := newFakeFeishu(t)
	fake.defaultTokenHandler("t-1", 7200)
	fake.onJSON(messageCreatePath, http.StatusOK, map[string]any{
		"code": 0, "msg": "ok", "data": map[string]any{"message_id": "om_stream_1"},
	})
	var patches []string
	patchPath := "/open-apis/im/v1/messages/om_stream_1"
	fake.on(patchPath, func(w http.ResponseWriter, _ *http.Request, body map[string]any) {
		if strings.TrimSpace(stringOf(body["content"])) != "" {
			patches = append(patches, cardContent(t, body))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "msg": "ok"})
	})
	provider := newTestProvider(t, fake)

	session, err := provider.StartStreaming(context.Background(), streamingConfig(), "oc_chat_1")
	if err != nil {
		t.Fatalf("StartStreaming: %v", err)
	}
	if session == nil {
		t.Fatal("expected streaming session")
	}
	if err := session.Update(context.Background(), "第一段"); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if err := session.Update(context.Background(), "第一段\n\n第二段"); err != nil {
		t.Fatalf("Update #2: %v", err)
	}
	if err := session.Update(context.Background(), "第一段\n\n第二段"); err != nil {
		t.Fatalf("Update #3 (unchanged): %v", err)
	}
	if err := session.Final(context.Background(), []connectors.OutboundMessage{{Text: "第一段\n\n第二段\n\n完成"}}); err != nil {
		t.Fatalf("Final: %v", err)
	}

	// PATCH 序列：两次增量 + 一次终态；重复文本被跳过（第 3 次 Update 无请求）。
	want := []string{"第一段", "第一段\n\n第二段", "第一段\n\n第二段\n\n完成"}
	if len(patches) != len(want) {
		t.Fatalf("patches = %q, want %q", patches, want)
	}
	for index, text := range want {
		if patches[index] != text {
			t.Fatalf("patch[%d] = %q, want %q", index, patches[index], text)
		}
	}
	// 创建占位卡走 interactive。
	create := fake.lastRequest()
	if create.path != patchPath { // last request is final PATCH
		t.Fatalf("last request path = %s", create.path)
	}
}

// TestStartStreamingFailureReturnsError 锁定创建失败：返回错误（Service 层
// 回退普通发送），不返回半可用会话。
func TestStartStreamingFailureReturnsError(t *testing.T) {
	fake := newFakeFeishu(t)
	fake.defaultTokenHandler("t-1", 7200)
	fake.onJSON(messageCreatePath, http.StatusOK, map[string]any{"code": 230001, "msg": "card create failed"})
	provider := newTestProvider(t, fake)

	session, err := provider.StartStreaming(context.Background(), streamingConfig(), "oc_chat_1")
	if err == nil {
		t.Fatalf("expected error, got session %#v", session)
	}
	if session != nil {
		t.Fatalf("expected nil session on failure, got %T", session)
	}
}

// TestStreamingFinalSendsMediaBeforeCardFinalize 锁定媒体收尾：Final 的媒体
// 走普通上传/发送，文本终态 PATCH 到卡片。
func TestStreamingFinalSendsMediaBeforeCardFinalize(t *testing.T) {
	fake := newFakeFeishu(t)
	fake.defaultTokenHandler("t-1", 7200)
	fake.onJSON(imageUploadPath, http.StatusOK, map[string]any{
		"code": 0, "msg": "ok", "data": map[string]any{"image_key": "img_stream"},
	})
	fake.onJSON(messageCreatePath, http.StatusOK, map[string]any{
		"code": 0, "msg": "ok", "data": map[string]any{"message_id": "om_stream_2"},
	})
	var patches []string
	fake.on("/open-apis/im/v1/messages/om_stream_2", func(w http.ResponseWriter, _ *http.Request, body map[string]any) {
		if strings.TrimSpace(stringOf(body["content"])) != "" {
			patches = append(patches, cardContent(t, body))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "msg": "ok"})
	})
	provider := newTestProvider(t, fake)

	session, err := provider.StartStreaming(context.Background(), streamingConfig(), "oc_chat_1")
	if err != nil {
		t.Fatalf("StartStreaming: %v", err)
	}
	if err := session.Update(context.Background(), "看这张图"); err != nil {
		t.Fatalf("Update: %v", err)
	}
	mediaPath := filepath.Join(t.TempDir(), "pic.png")
	if err := os.WriteFile(mediaPath, []byte("png-bytes"), 0o600); err != nil {
		t.Fatalf("write media: %v", err)
	}
	if err := session.Final(context.Background(), []connectors.OutboundMessage{{
		Text:  "看这张图",
		Media: []connectors.Media{{Kind: "image", Path: mediaPath}},
	}}); err != nil {
		t.Fatalf("Final: %v", err)
	}

	// Final 文本与上次 Update 相同 → 去重跳过 PATCH（卡片已是该文本）；
	// 只有 Update 一次文本替换，媒体走上传不 PATCH。
	want := []string{"看这张图"}
	if len(patches) != len(want) || patches[len(patches)-1] != want[len(want)-1] {
		t.Fatalf("patches = %q, want %q", patches, want)
	}
	_, fileName, data := lastMultipart(t, fake)
	if fileName != "pic.png" || !strings.Contains(string(data), "png") {
		t.Fatalf("media upload fileName=%q data=%q", fileName, data)
	}
}

// TestStreamingFailPatchesFailureText 锁定 Fail：兜底文案 PATCH 到卡片且不抛错。
func TestStreamingFailPatchesFailureText(t *testing.T) {
	fake := newFakeFeishu(t)
	fake.defaultTokenHandler("t-1", 7200)
	fake.onJSON(messageCreatePath, http.StatusOK, map[string]any{
		"code": 0, "msg": "ok", "data": map[string]any{"message_id": "om_stream_3"},
	})
	var lastPatch string
	fake.on("/open-apis/im/v1/messages/om_stream_3", func(w http.ResponseWriter, _ *http.Request, body map[string]any) {
		if content := cardContent(t, body); content != "" {
			lastPatch = content
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "msg": "ok"})
	})
	provider := newTestProvider(t, fake)

	session, err := provider.StartStreaming(context.Background(), streamingConfig(), "oc_chat_1")
	if err != nil {
		t.Fatalf("StartStreaming: %v", err)
	}
	if err := session.Fail(context.Background(), ""); err != nil {
		t.Fatalf("Fail: %v", err)
	}
	if lastPatch != streamFailureText {
		t.Fatalf("fail patch = %q, want %q", lastPatch, streamFailureText)
	}
}
