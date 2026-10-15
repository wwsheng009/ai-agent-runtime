package qqbot

// 流式路径（C2C stream_messages）× 出站媒体指令回归：
//   - Final 里的 ```qq-attachments``` 围栏块必须解析为富媒体上传（file_type=1
//     图片）+ msg_type=7 发送，可见文本不再含指令原文；
//   - Update 的流式中间态不得把未闭合围栏原文泄漏给用户（截断 + 剥离）。

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

// TestStreamFinalParsesMediaFence 覆盖 C2C 流式回复携带 qq-attachments 围栏块：
// 图片经 files 上传后以 msg_type=7 发送，流式收尾文本不含指令原文。
func TestStreamFinalParsesMediaFence(t *testing.T) {
	fake := newFakeQQ("secret-1", "token-1")
	server := newFakeQQServer(t, fake)
	provider := New(WithAPIBase(server.URL))
	cfg := testConfig("qq-1", "app-1", "secret-1")
	provider.rememberInboundMsgID("user:user-openid-1", "evt-1")

	path := filepath.Join(t.TempDir(), "pic.png")
	if err := os.WriteFile(path, fakePNG, 0o600); err != nil {
		t.Fatalf("write media: %v", err)
	}

	session, err := provider.StartStreaming(context.Background(), cfg, "user:user-openid-1")
	if err != nil {
		t.Fatalf("start streaming: %v", err)
	}
	// 中间态：围栏未闭合，用户不应看到指令原文。
	if err := session.Update(context.Background(), "回复正文\n```qq-attachments\nimage " + path); err != nil {
		t.Fatal(err)
	}
	if err := session.Final(context.Background(), []connectors.OutboundMessage{{
		Text: "回复正文\n```qq-attachments\nimage " + path + "\n```",
	}}); err != nil {
		t.Fatal(err)
	}

	fragments := fake.streamFragmentPayloads()
	if len(fragments) == 0 {
		t.Fatal("want stream fragments")
	}
	for i, fragment := range fragments {
		content, _ := fragment["content_raw"].(string)
		if strings.Contains(content, "qq-attachments") || strings.Contains(content, path) {
			t.Fatalf("fragment %d leaked directives: %q", i, content)
		}
	}
	// 中间态截断：未闭合围栏前的正文应已下发。
	if got, _ := fragments[0]["content_raw"].(string); got != "回复正文" {
		t.Fatalf("open-fence fragment content = %q, want 回复正文", got)
	}

	sent := fake.sentMessages()
	var upload, mediaMessage *sentMessage
	for index := range sent {
		switch {
		case strings.HasSuffix(sent[index].path, "/files"):
			upload = &sent[index]
		case sent[index].payload["msg_type"] == float64(7):
			mediaMessage = &sent[index]
		}
	}
	if upload == nil || mediaMessage == nil {
		t.Fatalf("sent = %+v, want upload + media message", sent)
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
	if media == nil || media["file_info"] != "file-info-1" {
		t.Fatalf("media payload = %+v", mediaMessage.payload)
	}
}

// TestStreamUpdateDoesNotLeakOpenFence 单独锁定 Update 中间态：未闭合围栏被
// 截断、已闭合指令块被剥离，且不会发出空/重复分片。
func TestStreamUpdateDoesNotLeakOpenFence(t *testing.T) {
	fake := newFakeQQ("secret-1", "token-1")
	server := newFakeQQServer(t, fake)
	provider := New(WithAPIBase(server.URL))
	cfg := testConfig("qq-1", "app-1", "secret-1")
	provider.rememberInboundMsgID("user:user-openid-1", "evt-1")

	session, err := provider.StartStreaming(context.Background(), cfg, "user:user-openid-1")
	if err != nil {
		t.Fatal(err)
	}
	// 1) 未闭合围栏：只发正文前缀。
	if err := session.Update(context.Background(), "正文一\n```qq-attachments\nimage /tmp/a.png"); err != nil {
		t.Fatal(err)
	}
	// 2) 围栏闭合：中间态同样剥离整块，不发指令原文。
	if err := session.Update(context.Background(), "正文一\n```qq-attachments\nimage /tmp/a.png\n```\n正文二"); err != nil {
		t.Fatal(err)
	}
	fragments := fake.streamFragmentPayloads()
	if len(fragments) != 2 {
		t.Fatalf("fragments = %d, want 2: %+v", len(fragments), fragments)
	}
	if got, _ := fragments[0]["content_raw"].(string); got != "正文一" {
		t.Fatalf("fragment 0 = %q, want 正文一（未闭合围栏截断）", got)
	}
	if got, _ := fragments[1]["content_raw"].(string); got != "正文一\n\n正文二" {
		t.Fatalf("fragment 1 = %q, want 正文一\\n\\n正文二（闭合块剥离）", got)
	}
	for i, fragment := range fragments {
		content, _ := fragment["content_raw"].(string)
		if strings.Contains(content, "qq-attachments") || strings.Contains(content, "/tmp/a.png") {
			t.Fatalf("fragment %d leaked directives: %q", i, content)
		}
	}
}

// TestTruncateOpenAttachmentBlocks 覆盖 connectors 侧截断助手的边界。
func TestTruncateOpenAttachmentBlocks(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"无块原样", "普通正文", "普通正文"},
		{"未闭合围栏截断", "正文\n```qq-attachments\nimage /a.png", "正文"},
		{"已闭合围栏保留", "正文\n```qq-attachments\nimage /a.png\n```\n尾段",
			"正文\n```qq-attachments\nimage /a.png\n```\n尾段"},
		{"未闭合标题行截断", "正文\nqq-attachments:\nimage /a.png", "正文"},
		{"标题行后跟正文不截断", "正文\nqq-attachments:\nimage /a.png\n\n说明文字",
			"正文\nqq-attachments:\nimage /a.png\n\n说明文字"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := connectors.TruncateOpenAttachmentBlocks(testCase.in, qqAttachmentFenceNames...)
			if got != testCase.want {
				t.Fatalf("got %q, want %q", got, testCase.want)
			}
		})
	}
}
