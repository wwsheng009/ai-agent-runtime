package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
)

// lastMultipart 解析最近一次 multipart 请求（字段 + 文件名 + 内容；跳过后续的
// JSON 消息发送请求）。
func lastMultipart(t *testing.T, fake *fakeFeishu) (map[string]string, string, []byte) {
	t.Helper()
	fake.mu.Lock()
	var request recordedRequest
	found := false
	for index := len(fake.requests) - 1; index >= 0; index-- {
		if strings.HasPrefix(fake.requests[index].headers.Get("Content-Type"), "multipart/form-data") {
			request = fake.requests[index]
			found = true
			break
		}
	}
	fake.mu.Unlock()
	if !found {
		t.Fatalf("no multipart request recorded")
	}

	contentType := request.headers.Get("Content-Type")
	if !strings.HasPrefix(contentType, "multipart/form-data") {
		t.Fatalf("request is not multipart: %q", contentType)
	}
	boundary := ""
	for _, part := range strings.Split(contentType, ";") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, "boundary=") {
			boundary = strings.TrimPrefix(part, "boundary=")
		}
	}
	form, err := multipart.NewReader(bytes.NewReader(request.raw), boundary).ReadForm(32 << 20)
	if err != nil {
		t.Fatalf("read multipart form: %v", err)
	}
	fields := make(map[string]string, len(form.Value))
	for key, values := range form.Value {
		if len(values) > 0 {
			fields[key] = values[0]
		}
	}
	for _, headers := range form.File {
		if len(headers) == 0 {
			continue
		}
		file, err := headers[0].Open()
		if err != nil {
			t.Fatalf("open multipart file: %v", err)
		}
		data, _ := io.ReadAll(file)
		file.Close()
		return fields, headers[0].Filename, data
	}
	return fields, "", nil
}

// TestInboundImageCarriesCompositeMediaRef 锁定入站媒体引用格式
// （messageID|fileKey|type；飞书下载资源三者缺一不可）。
func TestInboundImageCarriesCompositeMediaRef(t *testing.T) {
	fake := newFakeFeishu(t)
	fake.defaultTokenHandler("t-1", 7200)
	provider := newTestProvider(t, fake)

	result, err := provider.HandleWebhook(context.Background(), testConfig(),
		webhookRequest(t, messageEventPayload("ev-1", "image", `{"image_key":"img_1"}`)))
	if err != nil {
		t.Fatalf("HandleWebhook: %v", err)
	}
	if result.Message == nil {
		t.Fatalf("expected inbound message")
	}
	if len(result.Message.Media) != 1 || result.Message.Media[0].FileID != "om_msg_1|img_1|image" {
		t.Fatalf("media = %+v", result.Message.Media)
	}
	if !strings.Contains(result.Message.Text, "[图片]") {
		t.Fatalf("text = %q", result.Message.Text)
	}
}

// TestFetchInboundMediaDownloadsResource 锁定消息资源下载链路（GET + bearer + type）。
func TestFetchInboundMediaDownloadsResource(t *testing.T) {
	fake := newFakeFeishu(t)
	fake.defaultTokenHandler("t-1", 7200)
	fake.on("/open-apis/im/v1/messages/om_msg_1/resources/img_1",
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte("image-bytes"))
		})
	provider := newTestProvider(t, fake)

	path, err := provider.FetchInboundMedia(context.Background(), testConfig(),
		connectors.Media{Kind: "image", FileID: "om_msg_1|img_1|image", FileName: "pic.png"})
	if err != nil {
		t.Fatalf("FetchInboundMedia: %v", err)
	}
	if filepath.Base(path) != "pic.png" {
		t.Fatalf("file name = %q", filepath.Base(path))
	}
	if data, err := os.ReadFile(path); err != nil || !bytes.Equal(data, []byte("image-bytes")) {
		t.Fatalf("downloaded content mismatch: err=%v", err)
	}
	request := fake.lastRequest()
	if request.query.Get("type") != "image" {
		t.Fatalf("query type = %q", request.query.Get("type"))
	}
	if got := request.headers.Get("Authorization"); got != "Bearer t-1" {
		t.Fatalf("Authorization = %q", got)
	}
}

func TestFetchInboundMediaRejectsOversize(t *testing.T) {
	fake := newFakeFeishu(t)
	fake.defaultTokenHandler("t-1", 7200)
	fake.on("/open-apis/im/v1/messages/om_msg_1/resources/big_1",
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			_, _ = w.Write([]byte("0123456789"))
		})
	provider := newTestProvider(t, fake, WithInboundMediaLimit(4))

	_, err := provider.FetchInboundMedia(context.Background(), testConfig(),
		connectors.Media{Kind: "file", FileID: "om_msg_1|big_1|file"})
	if err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("err = %v, want too-large", err)
	}
}

// TestSendUploadsImageAndSendsImageMessage 锁定出站指令 + 图片上传 + 图片消息。
func TestSendUploadsImageAndSendsImageMessage(t *testing.T) {
	fake := newFakeFeishu(t)
	fake.defaultTokenHandler("t-1", 7200)
	fake.onJSON(imageUploadPath, http.StatusOK, map[string]any{
		"code": 0, "msg": "ok", "data": map[string]any{"image_key": "img_out_1"},
	})

	var mu sync.Mutex
	var messageBodies []map[string]any
	fake.on(messageCreatePath, func(w http.ResponseWriter, _ *http.Request, body map[string]any) {
		mu.Lock()
		messageBodies = append(messageBodies, body)
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "msg": "ok"})
	})

	provider := newTestProvider(t, fake)
	png := []byte("\x89PNG\r\n\x1a\nfeishu-image")
	imagePath := filepath.Join(t.TempDir(), "pic.png")
	if err := os.WriteFile(imagePath, png, 0o600); err != nil {
		t.Fatalf("write image: %v", err)
	}

	text := "配图\n```feishu-attachments\nimage " + imagePath + "\n```"
	if err := provider.Send(context.Background(), testConfig(), "oc_chat_1",
		[]connectors.OutboundMessage{{Text: text}}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	fields, fileName, data := lastMultipart(t, fake)
	if fields["image_type"] != "message" || fileName != "pic.png" || !bytes.Equal(data, png) {
		t.Fatalf("upload fields=%+v fileName=%q data=%q", fields, fileName, data)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(messageBodies) != 2 {
		t.Fatalf("messages = %+v, want text + image", messageBodies)
	}
	if messageBodies[0]["msg_type"] != "text" {
		t.Fatalf("first message = %+v", messageBodies[0])
	}
	if messageBodies[1]["msg_type"] != "image" || messageBodies[1]["content"] != `{"image_key":"img_out_1"}` {
		t.Fatalf("image message = %+v", messageBodies[1])
	}
}

// TestSendUploadsFileAndVoiceUseExpectedFileTypes 锁定文件/语音的上传参数与消息类型。
func TestSendUploadsFileAndVoiceUseExpectedFileTypes(t *testing.T) {
	fake := newFakeFeishu(t)
	fake.defaultTokenHandler("t-1", 7200)
	uploadIndex := 0
	fake.on(fileUploadPath, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		uploadIndex++
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0, "msg": "ok", "data": map[string]any{"file_key": "file_key_" + string(rune('0'+uploadIndex))},
		})
	})
	var mu sync.Mutex
	var messageTypes []string
	fake.on(messageCreatePath, func(w http.ResponseWriter, _ *http.Request, body map[string]any) {
		mu.Lock()
		messageTypes = append(messageTypes, stringOf(body["msg_type"]))
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "msg": "ok"})
	})

	provider := newTestProvider(t, fake)
	dir := t.TempDir()
	docPath := filepath.Join(dir, "report.pdf")
	voicePath := filepath.Join(dir, "voice.opus")
	if err := os.WriteFile(docPath, []byte("%PDF-1.7"), 0o600); err != nil {
		t.Fatalf("write doc: %v", err)
	}
	if err := os.WriteFile(voicePath, []byte("opus-bytes"), 0o600); err != nil {
		t.Fatalf("write voice: %v", err)
	}

	if err := provider.Send(context.Background(), testConfig(), "oc_chat_1", []connectors.OutboundMessage{{
		Media: []connectors.Media{
			{Kind: "file", Path: docPath},
			{Kind: "voice", Path: voicePath},
		},
	}}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	fields, _, _ := lastMultipart(t, fake)
	if fields["file_type"] != "opus" || fields["file_name"] != "voice.opus" {
		t.Fatalf("voice upload fields = %+v", fields)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(messageTypes) != 2 || messageTypes[0] != "file" || messageTypes[1] != "audio" {
		t.Fatalf("message types = %v, want [file audio]", messageTypes)
	}
}

// TestSendMediaByURLDownloadsThenUploads 锁定远端 URL 先下载再上传。
func TestSendMediaByURLDownloadsThenUploads(t *testing.T) {
	fake := newFakeFeishu(t)
	fake.defaultTokenHandler("t-1", 7200)
	fake.onJSON(imageUploadPath, http.StatusOK, map[string]any{
		"code": 0, "msg": "ok", "data": map[string]any{"image_key": "img_out_2"},
	})
	fake.on(messageCreatePath, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "msg": "ok"})
	})
	asset := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("remote-image"))
	}))
	defer asset.Close()

	// allowAllMediaHosts 是测试逃生口：httptest 服务监听 127.0.0.1，默认
	// 公网宿主校验会拒绝（SSRF 用例单独锁定默认拒绝行为）。
	provider := newTestProvider(t, fake, WithMediaHostPolicy(func(string) error { return nil }))
	if err := provider.Send(context.Background(), testConfig(), "oc_chat_1", []connectors.OutboundMessage{{
		Media: []connectors.Media{{Kind: "image", URL: asset.URL + "/pic.png"}},
	}}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	_, fileName, data := lastMultipart(t, fake)
	if fileName != "pic.png" || !bytes.Equal(data, []byte("remote-image")) {
		t.Fatalf("uploaded fileName=%q data=%q", fileName, data)
	}
}

// TestSendMediaByURLBlocksPrivateHost 锁定 SSRF 防护：出站远端媒体 URL 指向
// 回环/私网/CGNAT 时拒绝下载——Send 降级为文本提示（不向聊天外抛错），且
// 不触发任何 multipart 上传（无文件离开本机，也无内网请求）。
func TestSendMediaByURLBlocksPrivateHost(t *testing.T) {
	fake := newFakeFeishu(t)
	fake.defaultTokenHandler("t-1", 7200)
	// 降级文本可成功投递（messageCreate 返回 code=0）。
	fake.onJSON(messageCreatePath, http.StatusOK, map[string]any{"code": 0, "msg": "ok"})
	provider := newTestProvider(t, fake)

	blocked := []string{
		"http://127.0.0.1:9/pic.png",
		"http://10.0.0.5/pic.png",
		"http://192.168.1.9/pic.png",
		"http://172.16.5.5/pic.png",
		"http://169.254.169.254/latest/meta-data",
		"http://100.100.100.100/pic.png",
		"file:///etc/passwd",
		"ftp://example.com/pic.png",
	}
	for _, raw := range blocked {
		if err := provider.Send(context.Background(), testConfig(), "oc_chat_1",
			[]connectors.OutboundMessage{{Media: []connectors.Media{{Kind: "image", URL: raw}}}}); err != nil {
			t.Fatalf("Send with %s should degrade instead of failing: %v", raw, err)
		}
	}
	// 每个被拒 URL 只产生一条降级文本（messageCreate），零 multipart 上传。
	if got := fake.callCount(messageCreatePath); got != len(blocked) {
		t.Fatalf("degrade notices = %d, want %d", got, len(blocked))
	}
	fake.mu.Lock()
	uploads := 0
	for _, request := range fake.requests {
		if strings.HasPrefix(request.headers.Get("Content-Type"), "multipart/form-data") {
			uploads++
		}
	}
	fake.mu.Unlock()
	if uploads != 0 {
		t.Fatalf("blocked sends must not upload, got %d multipart requests", uploads)
	}
}

// TestSendMediaFailureDegrades 锁定上传失败降级为文本提示。
func TestSendMediaFailureDegrades(t *testing.T) {
	fake := newFakeFeishu(t)
	fake.defaultTokenHandler("t-1", 7200)
	fake.onJSON(imageUploadPath, http.StatusOK, map[string]any{"code": 99991663, "msg": "upload rejected"})
	var mu sync.Mutex
	var notice string
	fake.on(messageCreatePath, func(w http.ResponseWriter, _ *http.Request, body map[string]any) {
		mu.Lock()
		notice = stringOf(body["content"])
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "msg": "ok"})
	})

	provider := newTestProvider(t, fake)
	imagePath := filepath.Join(t.TempDir(), "pic.png")
	if err := os.WriteFile(imagePath, []byte("img"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := provider.Send(context.Background(), testConfig(), "oc_chat_1",
		[]connectors.OutboundMessage{{Text: "IMAGE:" + imagePath}}); err != nil {
		t.Fatalf("Send should degrade instead of failing: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(notice, "附件发送失败") {
		t.Fatalf("fallback notice = %q", notice)
	}
}

func TestOutboundMediaNoteDeclaresFence(t *testing.T) {
	note := New().OutboundMediaNote()
	if !strings.Contains(note, feishuAttachmentFence) {
		t.Fatalf("note = %q, want fence %q", note, feishuAttachmentFence)
	}
}

func TestMediaTempSweep(t *testing.T) {
	provider := New()
	fixedNow := time.Now()
	provider.now = func() time.Time { return fixedNow }

	tempRoot := os.TempDir()
	staleDir, err := os.MkdirTemp(tempRoot, mediaTempDirPrefix)
	if err != nil {
		t.Fatalf("mkdir stale: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(staleDir) })
	staleTime := fixedNow.Add(-mediaTempRetention - time.Hour)
	if err := os.Chtimes(staleDir, staleTime, staleTime); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	mediaSweepCounter.Store(mediaSweepEvery)
	provider.maybeSweepMediaTempDirs()
	if _, err := os.Stat(staleDir); !os.IsNotExist(err) {
		t.Fatalf("stale dir should be swept, err=%v", err)
	}
}

func stringOf(value any) string {
	text, _ := value.(string)
	return text
}

var _ connectors.MediaFetcher = (*Provider)(nil)
var _ connectors.OutboundMediaNoteProvider = (*Provider)(nil)
