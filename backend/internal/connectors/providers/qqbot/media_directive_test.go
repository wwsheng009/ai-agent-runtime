package qqbot

import (
	"bytes"
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
)

// TestSendParsesOutboundMediaFence 覆盖端到端出站媒体指令：回复文本里的
// qq-attachments 围栏块 → 富媒体上传（file_type=1 图片）+ msg_type=7 发送，
// 可见文本单独发送且不残留指令。
func TestSendParsesOutboundMediaFence(t *testing.T) {
	fake := newFakeQQ("secret-1", "token-1")
	server := newFakeQQServer(t, fake)
	provider := New(WithAPIBase(server.URL))
	cfg := testConfig("qq-1", "app-1", "secret-1")

	path := filepath.Join(t.TempDir(), "pic.png")
	if err := os.WriteFile(path, fakePNG, 0o600); err != nil {
		t.Fatalf("write media: %v", err)
	}

	text := "配图\n```qq-attachments\nimage " + path + "\n```"
	if err := provider.Send(context.Background(), cfg, "group:group-openid-1",
		[]connectors.OutboundMessage{{Text: text}}); err != nil {
		t.Fatalf("send: %v", err)
	}

	sent := fake.sentMessages()
	var upload, mediaMessage, textMessage *sentMessage
	for index := range sent {
		switch {
		case strings.HasSuffix(sent[index].path, "/files"):
			upload = &sent[index]
		case sent[index].payload["msg_type"] == float64(7):
			mediaMessage = &sent[index]
		case strings.HasSuffix(sent[index].path, "/messages"):
			textMessage = &sent[index]
		}
	}
	if upload == nil || mediaMessage == nil || textMessage == nil {
		t.Fatalf("sent = %+v, want text + upload + media message", sent)
	}
	if upload.payload["file_type"] != float64(1) {
		t.Fatalf("upload file_type = %v, want 1 (image)", upload.payload["file_type"])
	}
	encoded, _ := upload.payload["file_data"].(string)
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || !bytes.Equal(decoded, fakePNG) {
		t.Fatalf("file_data mismatch: %v", err)
	}
	media, _ := mediaMessage.payload["media"].(map[string]any)
	if media["file_info"] != "file-info-1" {
		t.Fatalf("media payload = %+v", mediaMessage.payload)
	}
	content, _ := textMessage.payload["content"].(string)
	if strings.Contains(content, "qq-attachments") || strings.Contains(content, path) {
		t.Fatalf("visible text leaked directives: %q", content)
	}
	if !strings.Contains(content, "配图") {
		t.Fatalf("visible text = %q, want 配图", content)
	}
}

// TestSendParsesMediaDirectiveLine 覆盖 `FILE:<路径>` 指令行（文件类型 4）。
func TestSendParsesMediaDirectiveLine(t *testing.T) {
	fake := newFakeQQ("secret-1", "token-1")
	server := newFakeQQServer(t, fake)
	provider := New(WithAPIBase(server.URL))
	cfg := testConfig("qq-1", "app-1", "secret-1")

	path := filepath.Join(t.TempDir(), "note.pdf")
	if err := os.WriteFile(path, []byte("%PDF-1.7 demo"), 0o600); err != nil {
		t.Fatalf("write media: %v", err)
	}
	if err := provider.Send(context.Background(), cfg, "user:user-openid-1",
		[]connectors.OutboundMessage{{Text: "文件在此\nFILE:" + path}}); err != nil {
		t.Fatalf("send: %v", err)
	}

	sent := fake.sentMessages()
	var upload, mediaMessage, textMessage *sentMessage
	for index := range sent {
		switch {
		case strings.HasSuffix(sent[index].path, "/files"):
			upload = &sent[index]
		case sent[index].payload["msg_type"] == float64(7):
			mediaMessage = &sent[index]
		case strings.HasSuffix(sent[index].path, "/messages"):
			textMessage = &sent[index]
		}
	}
	if upload == nil || mediaMessage == nil || textMessage == nil {
		t.Fatalf("sent = %+v, want text + upload + media message", sent)
	}
	if upload.payload["file_type"] != float64(4) {
		t.Fatalf("upload file_type = %v, want 4 (file)", upload.payload["file_type"])
	}
	if content, _ := textMessage.payload["content"].(string); strings.Contains(content, "FILE:") {
		t.Fatalf("visible text leaked directive: %q", content)
	}
}

// TestOutboundMediaNoteDeclaresFence 锁定通道提示与接受的围栏名一致（仅
// 通道专属名，通用 `attachments` 不参与解析以避免与普通代码块冲突）。
func TestOutboundMediaNoteDeclaresFence(t *testing.T) {
	provider := New()
	note := provider.OutboundMediaNote()
	if !strings.Contains(note, qqAttachmentFence) {
		t.Fatalf("note = %q, want fence %q", note, qqAttachmentFence)
	}
	if len(qqAttachmentFenceNames) != 1 || qqAttachmentFenceNames[0] != qqAttachmentFence {
		t.Fatalf("fence names = %v, want only %q", qqAttachmentFenceNames, qqAttachmentFence)
	}
	if visible, media := connectors.ParseOutboundMediaDirectives(
		"```attachments\nfile /tmp/a.pdf\n```", qqAttachmentFenceNames...); len(media) != 0 || !strings.Contains(visible, "attachments") {
		t.Fatalf("generic attachments fence should be ignored: visible=%q media=%+v", visible, media)
	}
}
