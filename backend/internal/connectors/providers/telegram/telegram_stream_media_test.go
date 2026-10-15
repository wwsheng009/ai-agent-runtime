package telegram

// 富媒体透传（docs/bots/13 §7.1 content/stream 形态）：流式会话（Update/Final）
// 与普通 Send 同口径解析出站媒体指令——中间态不泄漏未闭合指令块，收尾时
// 可见正文 + 附件（文本指令与结构化 Media）齐发；媒体失败降级为文本提示。

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
)

// 中间态：未闭合围栏截断、已闭合围栏/指令行剥离（指令原文不得进卡片编辑）。
func TestStreamingUpdateHidesAttachmentDirectives(t *testing.T) {
	api := newFakeAPI("tok")
	provider, _ := newMediaTestProvider(t, api)

	session, err := provider.StartStreaming(context.Background(), testConfig("tok"), "-100123")
	if err != nil {
		t.Fatalf("start streaming: %v", err)
	}

	if err := session.Update(context.Background(), "正文前半\n```telegram-attachments\nimage /tmp/a.png"); err != nil {
		t.Fatalf("update (open fence): %v", err)
	}
	if err := session.Update(context.Background(), "正文前半\n尾段\n```telegram-attachments\nimage /tmp/a.png\n```"); err != nil {
		t.Fatalf("update (closed fence): %v", err)
	}
	if err := session.Update(context.Background(), "正文前半\n尾段\n第二段\nIMAGE:/tmp/a.png"); err != nil {
		t.Fatalf("update (directive line): %v", err)
	}

	edits := api.editPayloads()
	want := []string{"正文前半", "正文前半\n尾段", "正文前半\n尾段\n第二段"}
	if len(edits) != len(want) {
		t.Fatalf("edits = %d, want %d（%+v）", len(edits), len(want), edits)
	}
	for index, edit := range edits {
		text, _ := edit["text"].(string)
		if text != want[index] {
			t.Fatalf("edit[%d].text = %q, want %q", index, text, want[index])
		}
		if strings.Contains(text, "telegram-attachments") || strings.Contains(text, "IMAGE:") {
			t.Fatalf("directive leaked to stream edit: %q", text)
		}
	}
}

// 收尾：文本指令解析出的附件（multipart 上传）与结构化 Media（URL 直传）
// 都随 Final 发出；可见正文做终态编辑。
func TestStreamingFinalParsesMediaDirectivesAndMediaField(t *testing.T) {
	api := newFakeAPI("tok")
	provider, _ := newMediaTestProvider(t, api)

	png := []byte("\x89PNG\r\n\x1a\nstream-image")
	imagePath := filepath.Join(t.TempDir(), "pic.png")
	if err := os.WriteFile(imagePath, png, 0o600); err != nil {
		t.Fatalf("write image: %v", err)
	}

	session, err := provider.StartStreaming(context.Background(), testConfig("tok"), "-100123")
	if err != nil {
		t.Fatalf("start streaming: %v", err)
	}
	if err := session.Final(context.Background(), []connectors.OutboundMessage{
		{Text: "配图\n```telegram-attachments\nimage " + imagePath + "\n```"},
		{Media: []connectors.Media{{Kind: "file", URL: "https://cdn.example/doc.pdf"}}},
	}); err != nil {
		t.Fatalf("final: %v", err)
	}

	edits := api.editPayloads()
	if len(edits) != 1 {
		t.Fatalf("edits = %+v, want 1", edits)
	}
	if text, _ := edits[0]["text"].(string); text != "配图" {
		t.Fatalf("final edit text = %q, want 配图", text)
	}
	uploads := api.mediaUploadList()
	if len(uploads) != 1 || uploads[0].method != "sendPhoto" || uploads[0].fileName != "pic.png" || !bytes.Equal(uploads[0].data, png) {
		t.Fatalf("uploads = %+v", uploads)
	}
	sends := api.mediaSendList()
	if len(sends) != 1 || sends[0]["_method"] != "sendDocument" || sends[0]["document"] != "https://cdn.example/doc.pdf" {
		t.Fatalf("media sends = %+v", sends)
	}
}

// 媒体失败：降级为文本提示（与 Send 同款），Final 不因单条附件失败而丢正文。
func TestStreamingFinalMediaFailureDegradesToNotice(t *testing.T) {
	api := newFakeAPI("tok")
	api.mu.Lock()
	api.mediaError = true
	api.mu.Unlock()
	provider, _ := newMediaTestProvider(t, api)

	session, err := provider.StartStreaming(context.Background(), testConfig("tok"), "-100123")
	if err != nil {
		t.Fatalf("start streaming: %v", err)
	}
	if err := session.Final(context.Background(), []connectors.OutboundMessage{
		{Text: "看这个", Media: []connectors.Media{{Kind: "file", URL: "https://cdn.example/doc.pdf"}}},
	}); err != nil {
		t.Fatalf("final: %v", err)
	}

	sent := api.sentPayloads()
	found := false
	for _, payload := range sent {
		if text, _ := payload["text"].(string); strings.Contains(text, "附件发送失败") {
			found = true
		}
	}
	if !found {
		t.Fatalf("media failure notice missing: %+v", sent)
	}
	if edits := api.editPayloads(); len(edits) != 1 {
		t.Fatalf("edits = %+v, want 1（正文不受媒体失败影响）", edits)
	}
}
