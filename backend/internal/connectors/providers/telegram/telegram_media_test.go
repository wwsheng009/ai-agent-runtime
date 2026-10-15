package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
)

func newMediaTestProvider(t *testing.T, api *fakeAPI, opts ...Option) (*Provider, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(api.handler())
	t.Cleanup(server.Close)
	return New(append([]Option{WithAPIBase(server.URL)}, opts...)...), server
}

// TestParseUpdateExtractsInboundMedia 锁定入站媒体解析：photo 取最大档，
// document/voice/audio/video 映射为核心类别，caption 作为正文。
func TestParseUpdateExtractsInboundMedia(t *testing.T) {
	raw := `{
		"update_id": 1,
		"message": {
			"message_id": 11,
			"from": {"id": 7, "username": "u"},
			"chat": {"id": 100, "type": "private"},
			"caption": "说明文字",
			"photo": [
				{"file_id": "p-small", "file_size": 10, "width": 90, "height": 90},
				{"file_id": "p-large", "file_size": 2048, "width": 1280, "height": 720}
			],
			"document": {"file_id": "d1", "file_name": "report.pdf", "mime_type": "application/pdf", "file_size": 1234},
			"voice": {"file_id": "v1", "mime_type": "audio/ogg", "file_size": 55},
			"audio": {"file_id": "a1", "file_name": "song.mp3", "mime_type": "audio/mpeg", "file_size": 66},
			"video": {"file_id": "vid1", "file_name": "clip.mp4", "mime_type": "video/mp4", "file_size": 999}
		}
	}`
	var upd update
	if err := json.Unmarshal([]byte(raw), &upd); err != nil {
		t.Fatalf("unmarshal update: %v", err)
	}
	msg, ok := parseUpdate("tg-1", upd)
	if !ok {
		t.Fatalf("update should be accepted")
	}
	if msg.Text != "说明文字" {
		t.Fatalf("text = %q, want caption", msg.Text)
	}
	if len(msg.Media) != 5 {
		t.Fatalf("media = %+v, want 5 entries", msg.Media)
	}
	if msg.Media[0].Kind != "image" || msg.Media[0].FileID != "p-large" || msg.Media[0].Size != 2048 {
		t.Fatalf("photo media = %+v", msg.Media[0])
	}
	if msg.Media[1].Kind != "file" || msg.Media[1].FileName != "report.pdf" || msg.Media[1].ContentType != "application/pdf" {
		t.Fatalf("document media = %+v", msg.Media[1])
	}
	if msg.Media[2].Kind != "voice" || msg.Media[2].FileID != "v1" {
		t.Fatalf("voice media = %+v", msg.Media[2])
	}
	if msg.Media[3].Kind != "audio" || msg.Media[3].FileName != "song.mp3" {
		t.Fatalf("audio media = %+v", msg.Media[3])
	}
	if msg.Media[4].Kind != "video" || msg.Media[4].FileID != "vid1" {
		t.Fatalf("video media = %+v", msg.Media[4])
	}
}

// TestParseUpdateMediaOnlyPlaceholders 锁定媒体-only 消息不被丢弃并生成占位文本。
func TestParseUpdateMediaOnlyPlaceholders(t *testing.T) {
	raw := `{"update_id":2,"message":{"message_id":12,"chat":{"id":100},"photo":[{"file_id":"p1","file_size":10}]}}`
	var upd update
	if err := json.Unmarshal([]byte(raw), &upd); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	msg, ok := parseUpdate("tg-1", upd)
	if !ok || len(msg.Media) != 1 {
		t.Fatalf("media-only message = %+v ok=%v", msg, ok)
	}
	if !strings.Contains(msg.Text, "[Photo]") {
		t.Fatalf("text = %q, want placeholder", msg.Text)
	}
}

// TestFetchInboundMediaDownloads 锁定 getFile + 文件下载链路。
func TestFetchInboundMediaDownloads(t *testing.T) {
	api := newFakeAPI("tok")
	api.queueFile("f1", "docs/file_1.bin", []byte("payload-bytes"))
	provider, _ := newMediaTestProvider(t, api)

	path, err := provider.FetchInboundMedia(context.Background(), testConfig("tok"),
		connectors.Media{Kind: "file", FileID: "f1", FileName: "a.bin"})
	if err != nil {
		t.Fatalf("FetchInboundMedia: %v", err)
	}
	if filepath.Base(path) != "a.bin" {
		t.Fatalf("file name = %q, want a.bin", filepath.Base(path))
	}
	data, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(data, []byte("payload-bytes")) {
		t.Fatalf("downloaded content mismatch: err=%v", err)
	}
}

func TestFetchInboundMediaRejectsOversize(t *testing.T) {
	api := newFakeAPI("tok")
	api.queueFile("f1", "docs/big.bin", []byte("0123456789"))
	provider, _ := newMediaTestProvider(t, api, WithInboundMediaLimit(4))

	_, err := provider.FetchInboundMedia(context.Background(), testConfig("tok"),
		connectors.Media{Kind: "file", FileID: "f1"})
	if err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("err = %v, want too-large", err)
	}
}

// TestSendUploadsLocalFileAsPhoto 锁定出站指令协议 + 本地文件 multipart 上传。
func TestSendUploadsLocalFileAsPhoto(t *testing.T) {
	api := newFakeAPI("tok")
	provider, _ := newMediaTestProvider(t, api)

	png := []byte("\x89PNG\r\n\x1a\nlocal-image")
	imagePath := filepath.Join(t.TempDir(), "pic.png")
	if err := os.WriteFile(imagePath, png, 0o600); err != nil {
		t.Fatalf("write image: %v", err)
	}

	text := "配图\n```telegram-attachments\nimage " + imagePath + "\n```"
	if err := provider.Send(context.Background(), testConfig("tok"), "-100123",
		[]connectors.OutboundMessage{{Text: text}}); err != nil {
		t.Fatalf("send: %v", err)
	}

	sent := api.sentPayloads()
	if len(sent) != 1 || sent[0]["text"] != "配图" {
		t.Fatalf("visible text payloads = %+v", sent)
	}
	uploads := api.mediaUploadList()
	if len(uploads) != 1 {
		t.Fatalf("uploads = %+v, want 1", uploads)
	}
	if uploads[0].method != "sendPhoto" || uploads[0].fileName != "pic.png" ||
		uploads[0].fields["chat_id"] != "-100123" || !bytes.Equal(uploads[0].data, png) {
		t.Fatalf("upload = %+v", uploads[0])
	}
}

// TestSendMediaByURLAndFileID 锁定远端 URL 直传与 file_id 复用（JSON 调用）。
func TestSendMediaByURLAndFileID(t *testing.T) {
	api := newFakeAPI("tok")
	provider, _ := newMediaTestProvider(t, api)

	err := provider.Send(context.Background(), testConfig("tok"), "-100123", []connectors.OutboundMessage{{
		Media: []connectors.Media{
			{Kind: "file", URL: "https://cdn.example/doc.pdf"},
			{Kind: "video", FileID: "vid-1"},
		},
	}})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	sends := api.mediaSendList()
	if len(sends) != 2 {
		t.Fatalf("media sends = %+v, want 2", sends)
	}
	if sends[0]["_method"] != "sendDocument" || sends[0]["document"] != "https://cdn.example/doc.pdf" {
		t.Fatalf("url media = %+v", sends[0])
	}
	if sends[1]["_method"] != "sendVideo" || sends[1]["video"] != "vid-1" {
		t.Fatalf("file_id media = %+v", sends[1])
	}
}

// TestSendVoiceFallsBackToDocumentForNonOgg 锁定非 OGG/OPUS 音频降级为文件。
func TestSendVoiceFallsBackToDocumentForNonOgg(t *testing.T) {
	api := newFakeAPI("tok")
	provider, _ := newMediaTestProvider(t, api)

	mp3Path := filepath.Join(t.TempDir(), "voice.mp3")
	if err := os.WriteFile(mp3Path, []byte("mp3-bytes"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := provider.Send(context.Background(), testConfig("tok"), "42",
		[]connectors.OutboundMessage{{Media: []connectors.Media{{Kind: "voice", Path: mp3Path}}}}); err != nil {
		t.Fatalf("send: %v", err)
	}
	uploads := api.mediaUploadList()
	if len(uploads) != 1 || uploads[0].method != "sendDocument" {
		t.Fatalf("uploads = %+v, want sendDocument", uploads)
	}
}

// TestSendMediaFailureDegrades 锁定单条媒体失败降级为文本提示。
func TestSendMediaFailureDegrades(t *testing.T) {
	api := newFakeAPI("tok")
	api.mu.Lock()
	api.mediaError = true
	api.mu.Unlock()
	provider, _ := newMediaTestProvider(t, api)

	imagePath := filepath.Join(t.TempDir(), "pic.png")
	if err := os.WriteFile(imagePath, []byte("img"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := provider.Send(context.Background(), testConfig("tok"), "42",
		[]connectors.OutboundMessage{{Text: "IMAGE:" + imagePath}}); err != nil {
		t.Fatalf("send should degrade instead of failing: %v", err)
	}
	sent := api.sentPayloads()
	if len(sent) != 1 {
		t.Fatalf("sent = %+v, want fallback notice", sent)
	}
	text, _ := sent[0]["text"].(string)
	if !strings.Contains(text, "[附件发送失败") {
		t.Fatalf("fallback text = %q", text)
	}
}

func TestOutboundMediaNoteDeclaresFence(t *testing.T) {
	note := New().OutboundMediaNote()
	if !strings.Contains(note, telegramAttachmentFence) {
		t.Fatalf("note = %q, want fence %q", note, telegramAttachmentFence)
	}
}

// TestMediaTempSweep 锁定入站临时目录 24h 低频清扫。
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
	freshDir, err := os.MkdirTemp(tempRoot, mediaTempDirPrefix)
	if err != nil {
		t.Fatalf("mkdir fresh: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(freshDir) })
	freshTime := fixedNow.Add(-time.Minute)
	if err := os.Chtimes(freshDir, freshTime, freshTime); err != nil {
		t.Fatalf("chtimes fresh: %v", err)
	}

	mediaSweepCounter.Store(mediaSweepEvery)
	provider.maybeSweepMediaTempDirs()

	if _, err := os.Stat(staleDir); !os.IsNotExist(err) {
		t.Fatalf("stale dir should be swept, err=%v", err)
	}
	if _, err := os.Stat(freshDir); err != nil {
		t.Fatalf("fresh dir should be kept: %v", err)
	}
}

var _ connectors.MediaFetcher = (*Provider)(nil)
var _ connectors.OutboundMediaNoteProvider = (*Provider)(nil)
