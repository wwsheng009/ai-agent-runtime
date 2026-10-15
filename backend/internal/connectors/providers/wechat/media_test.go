package wechat

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
)

// ── AES-ECB ────────────────────────────────────────────────────────────────

func TestAESECBRoundTripAndValidation(t *testing.T) {
	key := []byte("0123456789abcdef")
	for _, plaintext := range [][]byte{
		nil,
		[]byte("hello"),
		bytes.Repeat([]byte{0x11}, 16),
		bytes.Repeat([]byte{0x22}, 17),
	} {
		ciphertext, err := encryptAESECB(plaintext, key)
		if err != nil {
			t.Fatalf("encryptAESECB(%d bytes): %v", len(plaintext), err)
		}
		if len(ciphertext) == 0 || len(ciphertext)%16 != 0 || len(ciphertext) <= len(plaintext) {
			t.Fatalf("ciphertext size = %d for plaintext %d", len(ciphertext), len(plaintext))
		}
		got, err := decryptAESECB(ciphertext, key)
		if err != nil {
			t.Fatalf("decryptAESECB: %v", err)
		}
		if !bytes.Equal(got, plaintext) {
			t.Fatalf("round trip mismatch: got %q want %q", got, plaintext)
		}
	}
	if _, err := decryptAESECB(nil, key); err == nil {
		t.Fatalf("empty ciphertext should fail")
	}
	if _, err := decryptAESECB([]byte("not-a-block-multiple"), key); err == nil {
		t.Fatalf("non block multiple ciphertext should fail")
	}
}

// ── 入站媒体 ───────────────────────────────────────────────────────────────

func TestInboundImageMediaDownloadedAndDecrypted(t *testing.T) {
	fake := newFakeILink(t)
	plaintext := []byte("\x89PNG\r\n\x1a\nfake-image-bytes")
	aesKey := []byte("0123456789abcdef")
	ciphertext, err := encryptAESECB(plaintext, aesKey)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	fake.on("/cdn/download", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(ciphertext)
	})

	provider := New(WithLogger(discardLogger))
	cfg := testConfig("c1", "", map[string]string{"cdn_base_url": fake.URL() + "/cdn"})
	message := wechatMessage{
		FromUserID: "u1", SessionID: "s1", ContextToken: "ctx-1", MessageType: messageTypeUser,
		ItemList: []wechatMessageItem{{
			Type: itemTypeImage,
			ImageItem: &wechatImageItem{
				Media:     &wechatCDNMedia{EncryptQueryParam: "enc-1"},
				AESKeyHex: hex.EncodeToString(aesKey),
				MidSize:   int64(len(ciphertext)),
			},
		}},
	}

	inbound, ok := provider.inboundMessage(context.Background(), cfg, message)
	if !ok {
		t.Fatalf("message should be accepted")
	}
	if len(inbound.Media) != 1 {
		t.Fatalf("media = %+v, want 1 entry", inbound.Media)
	}
	entry := inbound.Media[0]
	if entry.Kind != mediaKindImage || entry.Path == "" || entry.FileName == "" {
		t.Fatalf("image media not downloaded: %+v", entry)
	}
	if entry.ContentType != "image/png" {
		t.Fatalf("content type = %q", entry.ContentType)
	}
	if entry.Size != int64(len(ciphertext)) {
		t.Fatalf("size = %d, want %d", entry.Size, len(ciphertext))
	}
	saved, err := os.ReadFile(entry.Path)
	if err != nil {
		t.Fatalf("read saved media: %v", err)
	}
	if !bytes.Equal(saved, plaintext) {
		t.Fatalf("saved media mismatch: got %q", saved)
	}
	if !strings.Contains(inbound.Text, "[Image attachment]") ||
		!strings.Contains(inbound.Text, "local_path: "+entry.Path) ||
		!strings.Contains(inbound.Text, outboundMediaNote) {
		t.Fatalf("inbound text missing media summary:\n%s", inbound.Text)
	}
	if fake.callCount("/cdn/download") != 1 {
		t.Fatalf("download calls = %d, want 1", fake.callCount("/cdn/download"))
	}
}

func TestInboundVoiceMediaTranscodedWithDecoder(t *testing.T) {
	fake := newFakeILink(t)
	silkBytes := []byte("silk-payload-bytes")
	fake.on("/cdn/download", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		w.Header().Set("Content-Type", "audio/silk")
		_, _ = w.Write(silkBytes)
	})

	originalLook, originalRun := lookPathVoiceCommand, runVoiceTranscode
	t.Cleanup(func() {
		lookPathVoiceCommand = originalLook
		runVoiceTranscode = originalRun
	})
	lookPathVoiceCommand = func(string) (string, error) { return "fake-decoder", nil }
	wavBytes := []byte("RIFF....wav-payload")
	var transcodedInput string
	runVoiceTranscode = func(_ context.Context, _ string, args []string, outputPath string) error {
		transcodedInput = args[0]
		return os.WriteFile(outputPath, wavBytes, 0o600)
	}

	provider := New(WithLogger(discardLogger))
	cfg := testConfig("c1", "", map[string]string{"cdn_base_url": fake.URL() + "/cdn"})
	message := wechatMessage{
		FromUserID: "u1", MessageType: messageTypeUser,
		ItemList: []wechatMessageItem{{
			Type:      itemTypeVoice,
			VoiceItem: &wechatVoiceItem{Media: &wechatCDNMedia{EncryptQueryParam: "enc-voice"}},
		}},
	}

	inbound, ok := provider.inboundMessage(context.Background(), cfg, message)
	if !ok {
		t.Fatalf("message should be accepted")
	}
	entry := inbound.Media[0]
	if !strings.HasSuffix(entry.Path, ".wav") || entry.ContentType != "audio/wav" {
		t.Fatalf("voice not transcoded: %+v", entry)
	}
	saved, err := os.ReadFile(entry.Path)
	if err != nil {
		t.Fatalf("read wav: %v", err)
	}
	if !bytes.Equal(saved, wavBytes) {
		t.Fatalf("wav mismatch: got %q", saved)
	}
	if transcodedInput == "" {
		t.Fatalf("decoder did not receive input path")
	}
	if _, err := os.Stat(transcodedInput); !os.IsNotExist(err) {
		t.Fatalf("silk intermediate should be removed after transcode, stat err = %v", err)
	}
	if !strings.Contains(inbound.Text, "local_path: "+entry.Path) {
		t.Fatalf("inbound text missing wav path:\n%s", inbound.Text)
	}
}

func TestInboundVoiceTranscodeFailureKeepsSilk(t *testing.T) {
	fake := newFakeILink(t)
	silkBytes := []byte("silk-payload-bytes")
	fake.on("/cdn/download", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		w.Header().Set("Content-Type", "audio/silk")
		_, _ = w.Write(silkBytes)
	})

	originalLook, originalRun := lookPathVoiceCommand, runVoiceTranscode
	t.Cleanup(func() {
		lookPathVoiceCommand = originalLook
		runVoiceTranscode = originalRun
	})
	lookPathVoiceCommand = func(command string) (string, error) {
		return "", os.ErrNotExist
	}
	runVoiceTranscode = originalRun

	provider := New(WithLogger(discardLogger))
	cfg := testConfig("c1", "", map[string]string{"cdn_base_url": fake.URL() + "/cdn"})
	message := wechatMessage{
		FromUserID: "u1", MessageType: messageTypeUser,
		ItemList: []wechatMessageItem{{
			Type:      itemTypeVoice,
			VoiceItem: &wechatVoiceItem{Media: &wechatCDNMedia{EncryptQueryParam: "enc-voice"}},
		}},
	}

	inbound, ok := provider.inboundMessage(context.Background(), cfg, message)
	if !ok {
		t.Fatalf("message should be accepted")
	}
	entry := inbound.Media[0]
	if !strings.HasSuffix(entry.Path, ".silk") || entry.ContentType != "audio/silk" {
		t.Fatalf("silk should be kept when decoder missing: %+v", entry)
	}
	if saved, err := os.ReadFile(entry.Path); err != nil || !bytes.Equal(saved, silkBytes) {
		t.Fatalf("silk content mismatch: err=%v", err)
	}
}

func TestInboundMediaDownloadFailureKeepsMetadata(t *testing.T) {
	fake := newFakeILink(t)
	fake.on("/cdn/download", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	provider := New(WithLogger(discardLogger))
	cfg := testConfig("c1", "", map[string]string{"cdn_base_url": fake.URL() + "/cdn"})
	message := wechatMessage{
		FromUserID: "u1", MessageType: messageTypeUser,
		ItemList: []wechatMessageItem{{
			Type:     itemTypeFile,
			FileItem: &wechatFileItem{Media: &wechatCDNMedia{EncryptQueryParam: "enc-file"}, FileName: "报告.pdf", Len: "321"},
		}},
	}

	inbound, ok := provider.inboundMessage(context.Background(), cfg, message)
	if !ok {
		t.Fatalf("message should be accepted")
	}
	entry := inbound.Media[0]
	if entry.Path != "" || entry.FileName != "报告.pdf" || entry.Size != 321 {
		t.Fatalf("failed download should keep metadata: %+v", entry)
	}
	if !strings.Contains(inbound.Text, "[File attachment]") || !strings.Contains(inbound.Text, "file_name: 报告.pdf") {
		t.Fatalf("inbound text missing fallback block:\n%s", inbound.Text)
	}
}

// ── attachment 协议解析 ────────────────────────────────────────────────────

func TestParseAttachmentProtocolVariants(t *testing.T) {
	cases := []struct {
		name      string
		text      string
		wantText  string
		wantMedia []connectors.Media
	}{
		{
			name:     "fenced block",
			text:     "看图\n```wechat-attachments\nimage /tmp/a.png\nfile /tmp/b.pdf\n```",
			wantText: "看图",
			wantMedia: []connectors.Media{
				{Kind: mediaKindImage, Path: "/tmp/a.png", FileName: "a.png"},
				{Kind: mediaKindFile, Path: "/tmp/b.pdf", FileName: "b.pdf"},
			},
		},
		{
			name:      "directive line",
			text:      "MEDIA:/tmp/c.jpg\n正文",
			wantText:  "正文",
			wantMedia: []connectors.Media{{Kind: mediaKindImage, Path: "/tmp/c.jpg", FileName: "c.jpg"}},
		},
		{
			name:      "voice downgrades to file",
			text:      "VOICE: /tmp/v.silk",
			wantText:  "",
			wantMedia: []connectors.Media{{Kind: mediaKindFile, Path: "/tmp/v.silk", FileName: "v.silk"}},
		},
		{
			name:      "remote url video",
			text:      "```wechat-attachments\nvideo https://example.com/v.mp4\n```",
			wantText:  "",
			wantMedia: []connectors.Media{{Kind: mediaKindVideo, URL: "https://example.com/v.mp4"}},
		},
		{
			name:      "heading with fence",
			text:      "说明\nwechat-attachments:\n```\nimage /tmp/d.png\n```",
			wantText:  "说明",
			wantMedia: []connectors.Media{{Kind: mediaKindImage, Path: "/tmp/d.png", FileName: "d.png"}},
		},
		{
			name:      "heading with bare lines",
			text:      "wechat-attachments:\nimage /tmp/e.png\n\n后续文字",
			wantText:  "后续文字",
			wantMedia: []connectors.Media{{Kind: mediaKindImage, Path: "/tmp/e.png", FileName: "e.png"}},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			visible, media := parseAttachmentProtocol(testCase.text)
			if visible != testCase.wantText {
				t.Fatalf("visible text = %q, want %q", visible, testCase.wantText)
			}
			if len(media) != len(testCase.wantMedia) {
				t.Fatalf("media = %+v, want %+v", media, testCase.wantMedia)
			}
			for index := range media {
				if media[index] != testCase.wantMedia[index] {
					t.Fatalf("media[%d] = %+v, want %+v", index, media[index], testCase.wantMedia[index])
				}
			}
		})
	}
}

// ── 出站媒体上传 ───────────────────────────────────────────────────────────

type cdnRecorder struct {
	server *httptest.Server

	mu          sync.Mutex
	uploadRaw   []byte
	uploadQuery url.Values
	uploadHits  int
	failStatus  int
}

func newCDNRecorder(t *testing.T) *cdnRecorder {
	t.Helper()
	recorder := &cdnRecorder{}
	recorder.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/upload":
			data, _ := io.ReadAll(r.Body)
			recorder.mu.Lock()
			recorder.uploadRaw = data
			recorder.uploadQuery = r.URL.Query()
			recorder.uploadHits++
			status := recorder.failStatus
			recorder.mu.Unlock()
			if status >= 400 {
				w.WriteHeader(status)
				_, _ = w.Write([]byte("upload rejected"))
				return
			}
			w.Header().Set("x-encrypted-param", "dl-1")
			w.WriteHeader(http.StatusOK)
		case "/asset.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(pngTestBytes)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(recorder.server.Close)
	return recorder
}

func (r *cdnRecorder) uploaded() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]byte(nil), r.uploadRaw...)
}

func (r *cdnRecorder) query() url.Values {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.uploadQuery
}

func (r *cdnRecorder) setFailure(status int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failStatus = status
}

var pngTestBytes = []byte("\x89PNG\r\n\x1a\nsend-image-bytes")

func sendMessageRequests(fake *fakeILink) []recordedRequest {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	sends := make([]recordedRequest, 0, 2)
	for _, request := range fake.requests {
		if request.path == "/ilink/bot/sendmessage" {
			sends = append(sends, request)
		}
	}
	return sends
}

func firstMessageItem(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	msg, ok := body["msg"].(map[string]any)
	if !ok {
		t.Fatalf("body missing msg: %+v", body)
	}
	items, ok := msg["item_list"].([]any)
	if !ok || len(items) == 0 {
		t.Fatalf("msg missing item_list: %+v", msg)
	}
	item, ok := items[0].(map[string]any)
	if !ok {
		t.Fatalf("item = %+v", items[0])
	}
	return item
}

func TestSendUploadsAttachmentProtocolImage(t *testing.T) {
	fake := newFakeILink(t)
	cdn := newCDNRecorder(t)
	fake.on("/ilink/bot/getuploadurl", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		writeJSON(t, w, map[string]any{
			"ret": 0, "errcode": 0,
			"upload_param":    "up-1",
			"upload_full_url": cdn.server.URL + "/upload",
		})
	})
	fake.on("/ilink/bot/sendmessage", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		writeRet0(w)
	})

	provider := New(WithAPIBase(fake.URL()), WithLogger(discardLogger))
	provider.rememberContextToken("c1", "u1", "ctx-1")
	cfg := testConfig("c1", fake.URL(), nil)

	imagePath := filepath.Join(t.TempDir(), "pic.png")
	if err := os.WriteFile(imagePath, pngTestBytes, 0o600); err != nil {
		t.Fatalf("write image: %v", err)
	}

	text := "配图\n```wechat-attachments\nimage " + imagePath + "\n```"
	if err := provider.Send(context.Background(), cfg, "u1", []connectors.OutboundMessage{{Text: text}}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	uploadRequest, ok := fake.lastRequest("/ilink/bot/getuploadurl")
	if !ok {
		t.Fatalf("getuploadurl not called")
	}
	if got := uploadRequest.body["media_type"]; got != float64(uploadMediaTypeImage) {
		t.Fatalf("media_type = %v, want %d", got, uploadMediaTypeImage)
	}
	if got := uploadRequest.body["rawsize"]; got != float64(len(pngTestBytes)) {
		t.Fatalf("rawsize = %v, want %d", got, len(pngTestBytes))
	}
	if got := uploadRequest.body["no_need_thumb"]; got != true {
		t.Fatalf("no_need_thumb = %v", got)
	}
	aesKeyHex, _ := uploadRequest.body["aeskey"].(string)
	if len(aesKeyHex) != 32 {
		t.Fatalf("aeskey = %q, want 32 hex chars", aesKeyHex)
	}
	sum := md5.Sum(pngTestBytes)
	if got := uploadRequest.body["rawfilemd5"]; got != hex.EncodeToString(sum[:]) {
		t.Fatalf("rawfilemd5 = %v", got)
	}

	ciphertext := cdn.uploaded()
	if len(ciphertext) == 0 || len(ciphertext)%16 != 0 {
		t.Fatalf("uploaded ciphertext invalid: %d bytes", len(ciphertext))
	}
	if got := uploadRequest.body["filesize"]; got != float64(len(ciphertext)) {
		t.Fatalf("filesize = %v, want %d", got, len(ciphertext))
	}
	aesKey, err := hex.DecodeString(aesKeyHex)
	if err != nil {
		t.Fatalf("decode aeskey: %v", err)
	}
	decrypted, err := decryptAESECB(ciphertext, aesKey)
	if err != nil {
		t.Fatalf("decrypt uploaded ciphertext: %v", err)
	}
	if !bytes.Equal(decrypted, pngTestBytes) {
		t.Fatalf("uploaded plaintext mismatch")
	}

	sends := sendMessageRequests(fake)
	if len(sends) != 2 {
		t.Fatalf("sendmessage calls = %d, want 2 (caption + media)", len(sends))
	}
	if got := sentText(t, sends[0].body); got != "配图" {
		t.Fatalf("caption = %q", got)
	}
	item := firstMessageItem(t, sends[1].body)
	if got := item["type"]; got != float64(itemTypeImage) {
		t.Fatalf("item type = %v, want image", got)
	}
	imageItem, ok := item["image_item"].(map[string]any)
	if !ok {
		t.Fatalf("missing image_item: %+v", item)
	}
	if got := imageItem["mid_size"]; got != float64(len(ciphertext)) {
		t.Fatalf("mid_size = %v, want %d", got, len(ciphertext))
	}
	media, ok := imageItem["media"].(map[string]any)
	if !ok {
		t.Fatalf("missing media: %+v", imageItem)
	}
	if got := media["encrypt_query_param"]; got != "dl-1" {
		t.Fatalf("encrypt_query_param = %v", got)
	}
	if got := media["encrypt_type"]; got != float64(1) {
		t.Fatalf("encrypt_type = %v", got)
	}
	wantKey := base64.StdEncoding.EncodeToString([]byte(aesKeyHex))
	if got := media["aes_key"]; got != wantKey {
		t.Fatalf("aes_key = %v, want %q", got, wantKey)
	}
}

func TestSendFileMediaFromStructField(t *testing.T) {
	fake := newFakeILink(t)
	cdn := newCDNRecorder(t)
	fake.on("/ilink/bot/getuploadurl", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		writeJSON(t, w, map[string]any{
			"ret": 0, "errcode": 0,
			"upload_param":    "up-2",
			"upload_full_url": cdn.server.URL + "/upload",
		})
	})
	fake.on("/ilink/bot/sendmessage", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		writeRet0(w)
	})

	provider := New(WithAPIBase(fake.URL()), WithLogger(discardLogger))
	provider.rememberContextToken("c1", "u1", "ctx-1")
	cfg := testConfig("c1", fake.URL(), nil)

	filePath := filepath.Join(t.TempDir(), "报告.pdf")
	fileBytes := []byte("%PDF-1.7 test")
	if err := os.WriteFile(filePath, fileBytes, 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	err := provider.Send(context.Background(), cfg, "u1", []connectors.OutboundMessage{{
		Media: []connectors.Media{{Kind: mediaKindFile, Path: filePath}},
	}})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	uploadRequest, _ := fake.lastRequest("/ilink/bot/getuploadurl")
	if got := uploadRequest.body["media_type"]; got != float64(uploadMediaTypeFile) {
		t.Fatalf("media_type = %v, want file", got)
	}
	aesKeyHex, _ := uploadRequest.body["aeskey"].(string)
	aesKey, _ := hex.DecodeString(aesKeyHex)
	if decrypted, err := decryptAESECB(cdn.uploaded(), aesKey); err != nil || !bytes.Equal(decrypted, fileBytes) {
		t.Fatalf("uploaded file mismatch: err=%v", err)
	}

	sends := sendMessageRequests(fake)
	if len(sends) != 1 {
		t.Fatalf("sendmessage calls = %d, want 1 (media only, no caption)", len(sends))
	}
	item := firstMessageItem(t, sends[0].body)
	if got := item["type"]; got != float64(itemTypeFile) {
		t.Fatalf("item type = %v, want file", got)
	}
	fileItem, ok := item["file_item"].(map[string]any)
	if !ok {
		t.Fatalf("missing file_item: %+v", item)
	}
	if got := fileItem["file_name"]; got != "报告.pdf" {
		t.Fatalf("file_name = %v", got)
	}
	if got := fileItem["len"]; got != strconv.FormatInt(int64(len(fileBytes)), 10) {
		t.Fatalf("len = %v, want %d", got, len(fileBytes))
	}
}

func TestSendRemoteMediaURLDownloadsAndUploads(t *testing.T) {
	fake := newFakeILink(t)
	cdn := newCDNRecorder(t)
	fake.on("/ilink/bot/getuploadurl", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		writeJSON(t, w, map[string]any{
			"ret": 0, "errcode": 0,
			"upload_param":    "up-3",
			"upload_full_url": cdn.server.URL + "/upload",
		})
	})
	fake.on("/ilink/bot/sendmessage", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		writeRet0(w)
	})

	provider := New(WithAPIBase(fake.URL()), WithLogger(discardLogger), WithMediaHostPolicy(allowAllMediaHosts))
	provider.rememberContextToken("c1", "u1", "ctx-1")
	cfg := testConfig("c1", fake.URL(), nil)

	err := provider.Send(context.Background(), cfg, "u1", []connectors.OutboundMessage{{
		Media: []connectors.Media{{Kind: mediaKindImage, URL: cdn.server.URL + "/asset.png"}},
	}})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	uploadRequest, _ := fake.lastRequest("/ilink/bot/getuploadurl")
	aesKeyHex, _ := uploadRequest.body["aeskey"].(string)
	aesKey, _ := hex.DecodeString(aesKeyHex)
	decrypted, err := decryptAESECB(cdn.uploaded(), aesKey)
	if err != nil || !bytes.Equal(decrypted, pngTestBytes) {
		t.Fatalf("remote media round trip mismatch: err=%v", err)
	}
	if len(sendMessageRequests(fake)) != 1 {
		t.Fatalf("expected a single media message")
	}
}

func TestSendMediaUploadFailureDegradesToNotice(t *testing.T) {
	fake := newFakeILink(t)
	cdn := newCDNRecorder(t)
	cdn.setFailure(http.StatusBadRequest)
	fake.on("/ilink/bot/getuploadurl", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		writeJSON(t, w, map[string]any{
			"ret": 0, "errcode": 0,
			"upload_param":    "up-4",
			"upload_full_url": cdn.server.URL + "/upload",
		})
	})
	fake.on("/ilink/bot/sendmessage", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		writeRet0(w)
	})

	provider := New(WithAPIBase(fake.URL()), WithLogger(discardLogger))
	provider.rememberContextToken("c1", "u1", "ctx-1")
	cfg := testConfig("c1", fake.URL(), nil)

	imagePath := filepath.Join(t.TempDir(), "pic.png")
	if err := os.WriteFile(imagePath, pngTestBytes, 0o600); err != nil {
		t.Fatalf("write image: %v", err)
	}

	err := provider.Send(context.Background(), cfg, "u1", []connectors.OutboundMessage{{
		Text: "IMAGE:" + imagePath,
	}})
	if err != nil {
		t.Fatalf("Send should degrade instead of failing: %v", err)
	}

	sends := sendMessageRequests(fake)
	if len(sends) != 1 {
		t.Fatalf("sendmessage calls = %d, want 1 (fallback notice)", len(sends))
	}
	if got := sentText(t, sends[0].body); !strings.Contains(got, "[附件发送失败") {
		t.Fatalf("fallback text = %q", got)
	}
}

// ── Schema ─────────────────────────────────────────────────────────────────

func TestSchemaDeclaresCDNBaseURLAndDefault(t *testing.T) {
	schema := New().Schema()
	found := false
	for _, field := range schema.Fields {
		if field.Key == cdnBaseURLKey {
			found = true
			if field.Default != defaultCDNBaseURL {
				t.Fatalf("cdn field default = %q, want %q", field.Default, defaultCDNBaseURL)
			}
		}
	}
	if !found {
		t.Fatalf("schema missing %s field", cdnBaseURLKey)
	}

	if got := cdnBaseFor(connectors.Config{}); got != defaultCDNBaseURL {
		t.Fatalf("cdnBaseFor default = %q", got)
	}
	custom := connectors.Config{Settings: map[string]string{cdnBaseURLKey: "https://cdn.example/base/"}}
	if got := cdnBaseFor(custom); got != "https://cdn.example/base" {
		t.Fatalf("cdnBaseFor override = %q", got)
	}
}

// TestMediaLimitPolicyConfigurable 锁定媒体上限策略化：构造级覆盖与运行时
// `media_limit_mb` 均生效（超出即拒绝），缺省/非法值回退默认 100MB。
func TestMediaLimitPolicyConfigurable(t *testing.T) {
	// ① 构造级覆盖（WithInboundMediaLimit）：uploadOutboundMedia 在读文件后、
	// 任何 HTTP 前检查上限——9 字节文件 > 8 字节上限应直接拒绝。
	limited := New(WithLogger(discardLogger), WithInboundMediaLimit(8))
	filePath := filepath.Join(t.TempDir(), "big.png")
	if err := os.WriteFile(filePath, []byte("123456789"), 0o600); err != nil { // 9 字节 > 8
		t.Fatalf("write file: %v", err)
	}
	if _, err := limited.uploadOutboundMedia(context.Background(), testConfig("c1", "", nil),
		"", "", "", "", "", filePath, "big.png", "image/png", mediaKindImage); err == nil ||
		!strings.Contains(err.Error(), "exceeds 8 bytes") {
		t.Fatalf("constructor limit should reject 9-byte file, err = %v", err)
	}

	// ② 运行时配置（media_limit_mb=1）：2MB 文件 > 1MB 上限应直接拒绝。
	setting := New(WithLogger(discardLogger))
	if err := os.WriteFile(filePath, make([]byte, 2<<20), 0o600); err != nil { // 2MB > 1MB
		t.Fatalf("write file: %v", err)
	}
	cfg := testConfig("c1", "", map[string]string{mediaLimitSetting: "1"})
	if _, err := setting.uploadOutboundMedia(context.Background(), cfg,
		"", "", "", "", "", filePath, "big.bin", "application/octet-stream", mediaKindFile); err == nil ||
		!strings.Contains(err.Error(), "exceeds 1048576") {
		t.Fatalf("runtime setting should reject 2MB file with 1MB limit, err = %v", err)
	}

	// ③ 非法值回退默认（未配置 → mediaLimitFor = maxMediaTransferBytes）。
	if got := New().mediaLimitFor(connectors.Config{}); got != maxMediaTransferBytes {
		t.Fatalf("default limit = %d, want %d", got, maxMediaTransferBytes)
	}
	if got := New().mediaLimitFor(connectors.Config{Settings: map[string]string{mediaLimitSetting: "abc"}}); got != maxMediaTransferBytes {
		t.Fatalf("invalid setting limit = %d, want default", got)
	}
}

// ── 补充场景（上游对齐 + 边界路径） ─────────────────────────────────────────

// TestInboundQuotedMediaFallback 锁定引用回退：用户用文本回复一条纯媒体消息
// （text_item.ref_msg.message_item 指向图片）时，媒体仍应被下载并入站。
func TestInboundQuotedMediaFallback(t *testing.T) {
	fake := newFakeILink(t)
	plaintext := []byte("quoted-image-bytes")
	aesKey := []byte("0123456789abcdef")
	ciphertext, err := encryptAESECB(plaintext, aesKey)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	fake.on("/cdn/download", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(ciphertext)
	})

	provider := New(WithLogger(discardLogger))
	cfg := testConfig("c1", "", map[string]string{"cdn_base_url": fake.URL() + "/cdn"})
	message := wechatMessage{
		FromUserID: "u1", MessageType: messageTypeUser,
		ItemList: []wechatMessageItem{{
			Type: itemTypeText,
			TextItem: &wechatTextItem{
				RefMsg: &wechatReferenceMsg{
					MessageItem: &wechatMessageItem{
						Type: itemTypeImage,
						ImageItem: &wechatImageItem{
							Media:     &wechatCDNMedia{EncryptQueryParam: "enc-quoted"},
							AESKeyHex: hex.EncodeToString(aesKey),
						},
					},
				},
			},
		}},
	}

	inbound, ok := provider.inboundMessage(context.Background(), cfg, message)
	if !ok {
		t.Fatalf("quoted media message should be accepted")
	}
	if len(inbound.Media) != 1 || inbound.Media[0].Kind != mediaKindImage || inbound.Media[0].Path == "" {
		t.Fatalf("quoted media not recovered: %+v", inbound.Media)
	}
	saved, err := os.ReadFile(inbound.Media[0].Path)
	if err != nil || !bytes.Equal(saved, plaintext) {
		t.Fatalf("quoted media content mismatch: err=%v", err)
	}
	if !strings.Contains(inbound.Text, "[Image attachment]") {
		t.Fatalf("missing quoted media block:\n%s", inbound.Text)
	}
}

// TestInboundImageAESKeyBase64Fallback 覆盖 media.aes_key 的两种线上形态：
// base64(16 字节原始密钥) 与 base64(32 字节 hex 文本)。
func TestInboundImageAESKeyBase64Fallback(t *testing.T) {
	rawKey := []byte("0123456789abcdef")
	variants := map[string][]byte{
		"raw-16-bytes": rawKey,
		"hex-32-text":  []byte(hex.EncodeToString(rawKey)),
	}
	for name, keyBytes := range variants {
		t.Run(name, func(t *testing.T) {
			fake := newFakeILink(t)
			plaintext := []byte("aes-key-fallback-bytes")
			ciphertext, err := encryptAESECB(plaintext, rawKey)
			if err != nil {
				t.Fatalf("encrypt: %v", err)
			}
			fake.on("/cdn/download", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
				w.Header().Set("Content-Type", "image/png")
				_, _ = w.Write(ciphertext)
			})

			provider := New(WithLogger(discardLogger))
			cfg := testConfig("c1", "", map[string]string{"cdn_base_url": fake.URL() + "/cdn"})
			message := wechatMessage{
				FromUserID: "u1", MessageType: messageTypeUser,
				ItemList: []wechatMessageItem{{
					Type: itemTypeImage,
					ImageItem: &wechatImageItem{
						Media: &wechatCDNMedia{
							EncryptQueryParam: "enc-1",
							AESKey:            base64.StdEncoding.EncodeToString(keyBytes),
						},
					},
				}},
			}

			inbound, ok := provider.inboundMessage(context.Background(), cfg, message)
			if !ok || len(inbound.Media) != 1 || inbound.Media[0].Path == "" {
				t.Fatalf("media not downloaded: ok=%v media=%+v", ok, inbound.Media)
			}
			saved, err := os.ReadFile(inbound.Media[0].Path)
			if err != nil || !bytes.Equal(saved, plaintext) {
				t.Fatalf("decrypt via media.aes_key failed: err=%v", err)
			}
		})
	}
}

// TestInboundFullURLPreferred 锁定 full_url 优先于 CDN 基址拼接。
func TestInboundFullURLPreferred(t *testing.T) {
	fake := newFakeILink(t)
	plaintext := []byte("absolute-url-bytes")
	fake.on("/cdn/absolute", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(plaintext)
	})
	fake.on("/cdn/download", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	provider := New(WithLogger(discardLogger))
	cfg := testConfig("c1", "", map[string]string{"cdn_base_url": fake.URL() + "/cdn"})
	message := wechatMessage{
		FromUserID: "u1", MessageType: messageTypeUser,
		ItemList: []wechatMessageItem{{
			Type: itemTypeImage,
			ImageItem: &wechatImageItem{
				Media: &wechatCDNMedia{FullURL: fake.URL() + "/cdn/absolute", EncryptQueryParam: "enc-ignored"},
			},
		}},
	}

	inbound, ok := provider.inboundMessage(context.Background(), cfg, message)
	if !ok || len(inbound.Media) != 1 || inbound.Media[0].Path == "" {
		t.Fatalf("full_url download failed: ok=%v media=%+v", ok, inbound.Media)
	}
	if fake.callCount("/cdn/absolute") != 1 {
		t.Fatalf("absolute url calls = %d, want 1", fake.callCount("/cdn/absolute"))
	}
	if fake.callCount("/cdn/download") != 0 {
		t.Fatalf("cdn base url should not be used when full_url is present")
	}
}

func TestSendVideoProtocolUsesVideoUploadType(t *testing.T) {
	fake := newFakeILink(t)
	cdn := newCDNRecorder(t)
	fake.on("/ilink/bot/getuploadurl", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		writeJSON(t, w, map[string]any{
			"ret": 0, "errcode": 0,
			"upload_param":    "up-video",
			"upload_full_url": cdn.server.URL + "/upload",
		})
	})
	fake.on("/ilink/bot/sendmessage", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		writeRet0(w)
	})

	provider := New(WithAPIBase(fake.URL()), WithLogger(discardLogger))
	provider.rememberContextToken("c1", "u1", "ctx-1")
	cfg := testConfig("c1", fake.URL(), nil)

	videoPath := filepath.Join(t.TempDir(), "clip.mp4")
	videoBytes := []byte("\x00\x00\x00\x18ftypmp42video-bytes")
	if err := os.WriteFile(videoPath, videoBytes, 0o600); err != nil {
		t.Fatalf("write video: %v", err)
	}

	text := "```wechat-attachments\nvideo " + videoPath + "\n```"
	if err := provider.Send(context.Background(), cfg, "u1", []connectors.OutboundMessage{{Text: text}}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	uploadRequest, _ := fake.lastRequest("/ilink/bot/getuploadurl")
	if got := uploadRequest.body["media_type"]; got != float64(uploadMediaTypeVideo) {
		t.Fatalf("media_type = %v, want video", got)
	}
	aesKeyHex, _ := uploadRequest.body["aeskey"].(string)
	aesKey, _ := hex.DecodeString(aesKeyHex)
	ciphertext := cdn.uploaded()
	if decrypted, err := decryptAESECB(ciphertext, aesKey); err != nil || !bytes.Equal(decrypted, videoBytes) {
		t.Fatalf("uploaded video mismatch: err=%v", err)
	}

	sends := sendMessageRequests(fake)
	if len(sends) != 1 {
		t.Fatalf("sendmessage calls = %d, want 1", len(sends))
	}
	item := firstMessageItem(t, sends[0].body)
	if got := item["type"]; got != float64(itemTypeVideo) {
		t.Fatalf("item type = %v, want video", got)
	}
	videoItem, ok := item["video_item"].(map[string]any)
	if !ok {
		t.Fatalf("missing video_item: %+v", item)
	}
	if got := videoItem["video_size"]; got != float64(len(ciphertext)) {
		t.Fatalf("video_size = %v, want %d", got, len(ciphertext))
	}
}

// TestSendUploadParamFallbackPath 覆盖无 upload_full_url 时按 CDN 基址 +
// upload_param + filekey 拼接上传地址的路径。
func TestSendUploadParamFallbackPath(t *testing.T) {
	fake := newFakeILink(t)
	cdn := newCDNRecorder(t)
	fake.on("/ilink/bot/getuploadurl", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		writeJSON(t, w, map[string]any{"ret": 0, "errcode": 0, "upload_param": "up-param"})
	})
	fake.on("/ilink/bot/sendmessage", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		writeRet0(w)
	})

	provider := New(WithAPIBase(fake.URL()), WithLogger(discardLogger))
	provider.rememberContextToken("c1", "u1", "ctx-1")
	cfg := testConfig("c1", fake.URL(), map[string]string{"cdn_base_url": cdn.server.URL})

	imagePath := filepath.Join(t.TempDir(), "pic.png")
	if err := os.WriteFile(imagePath, pngTestBytes, 0o600); err != nil {
		t.Fatalf("write image: %v", err)
	}
	if err := provider.Send(context.Background(), cfg, "u1", []connectors.OutboundMessage{{Text: "IMAGE:" + imagePath}}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	query := cdn.query()
	if got := query.Get("encrypted_query_param"); got != "up-param" {
		t.Fatalf("encrypted_query_param = %q, want up-param", got)
	}
	if got := query.Get("filekey"); len(got) != 32 {
		t.Fatalf("filekey = %q, want 32 hex chars", got)
	}
	if len(sendMessageRequests(fake)) != 1 {
		t.Fatalf("expected one media message")
	}
}

func TestSendUploadRetriesServerErrorThenSucceeds(t *testing.T) {
	fake := newFakeILink(t)
	var attempts int32
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&attempts, 1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("x-encrypted-param", "dl-retry")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(cdn.Close)

	fake.on("/ilink/bot/getuploadurl", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		writeJSON(t, w, map[string]any{
			"ret": 0, "errcode": 0,
			"upload_param":    "up-retry",
			"upload_full_url": cdn.URL + "/upload",
		})
	})
	fake.on("/ilink/bot/sendmessage", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		writeRet0(w)
	})

	provider := New(WithAPIBase(fake.URL()), WithLogger(discardLogger))
	provider.rememberContextToken("c1", "u1", "ctx-1")
	cfg := testConfig("c1", fake.URL(), nil)

	imagePath := filepath.Join(t.TempDir(), "pic.png")
	if err := os.WriteFile(imagePath, pngTestBytes, 0o600); err != nil {
		t.Fatalf("write image: %v", err)
	}
	if err := provider.Send(context.Background(), cfg, "u1", []connectors.OutboundMessage{{Text: "IMAGE:" + imagePath}}); err != nil {
		t.Fatalf("Send should succeed after retry: %v", err)
	}
	if got := atomic.LoadInt32(&attempts); got != 2 {
		t.Fatalf("upload attempts = %d, want 2", got)
	}
	sends := sendMessageRequests(fake)
	if len(sends) != 1 {
		t.Fatalf("sendmessage calls = %d, want 1", len(sends))
	}
	item := firstMessageItem(t, sends[0].body)
	imageItem, _ := item["image_item"].(map[string]any)
	media, _ := imageItem["media"].(map[string]any)
	if got := media["encrypt_query_param"]; got != "dl-retry" {
		t.Fatalf("encrypt_query_param = %v, want dl-retry", got)
	}
}

// ── context_token 跨重启持久化 ──────────────────────────────────────────────

// allowAllMediaHosts 是测试用的宿主校验逃生口（httptest 服务监听 127.0.0.1）。
func allowAllMediaHosts(string) error { return nil }

func TestRememberContextTokenChangeDetection(t *testing.T) {
	provider := New(WithLogger(discardLogger))
	if !provider.rememberContextToken("c1", "u1", "ctx-1") {
		t.Fatalf("first remember should report change")
	}
	if provider.rememberContextToken("c1", "u1", "ctx-1") {
		t.Fatalf("same token should not report change")
	}
	if !provider.rememberContextToken("c1", "u1", "ctx-2") {
		t.Fatalf("new token should report change")
	}
	if got := provider.contextToken("c1", "u1"); got != "ctx-2" {
		t.Fatalf("context token = %q, want ctx-2", got)
	}
}

func TestContextTokenPersistsAndRestoresAcrossRestart(t *testing.T) {
	store := connectors.NewMemoryStore()
	first := New(WithLogger(discardLogger))
	first.AttachState("c1", store.PollingState("c1"))
	first.rememberContextToken("c1", "u1", "ctx-1")
	first.persistContextTokens("c1")

	raw, ok := store.PollingState("c1").Get(contextTokensStateKey)
	if !ok || !strings.Contains(raw, "ctx-1") {
		t.Fatalf("context snapshot missing: ok=%v raw=%q", ok, raw)
	}

	restarted := New(WithLogger(discardLogger))
	restarted.AttachState("c1", store.PollingState("c1"))
	if got := restarted.contextToken("c1", "u1"); got != "ctx-1" {
		t.Fatalf("restored token = %q, want ctx-1", got)
	}
	if got := New(WithLogger(discardLogger)).contextToken("c1", "u1"); got != "" {
		t.Fatalf("token without attached state = %q, want empty", got)
	}
}

// TestContextTokenWrittenByPollingAndUsableAfterRestart 覆盖端到端链路：
// RunPolling 收信 → 快照落盘 → 模拟重启（新 provider + 同一存储）→ Send 直接
// 使用恢复出的 context_token（不需要该会话再发一条消息）。
func TestContextTokenWrittenByPollingAndUsableAfterRestart(t *testing.T) {
	fake := newFakeILink(t)
	fake.on("/ilink/bot/getupdates", func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		fake.mu.Lock()
		served := fake.calls["/ilink/bot/getupdates"]
		fake.mu.Unlock()
		if served == 1 {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ret":0,"errcode":0,"get_updates_buf":"buf-1","msgs":[{"from_user_id":"u1","message_type":1,"context_token":"ctx-1","item_list":[{"type":1,"text_item":{"text":"你好"}}]}]}`))
			return
		}
		<-r.Context().Done()
	})
	fake.on("/ilink/bot/sendmessage", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		writeRet0(w)
	})

	store := connectors.NewMemoryStore()
	cfg := testConfig("c1", fake.URL(), nil)
	state := store.PollingState("c1")

	first := New(WithAPIBase(fake.URL()), WithLogger(discardLogger))
	first.AttachState("c1", state)
	ctx, cancel := context.WithCancel(context.Background())
	received := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() {
		done <- first.RunPolling(ctx, cfg, func(context.Context, connectors.InboundMessage) error {
			received <- struct{}{}
			return nil
		}, state)
	}()
	select {
	case <-received:
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for inbound message")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunPolling returned %v, want nil on ctx cancel", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("RunPolling did not stop after cancel")
	}

	raw, ok := store.PollingState("c1").Get(contextTokensStateKey)
	if !ok || !strings.Contains(raw, "ctx-1") {
		t.Fatalf("polling did not persist context token: ok=%v raw=%q", ok, raw)
	}

	restarted := New(WithAPIBase(fake.URL()), WithLogger(discardLogger))
	restarted.AttachState("c1", store.PollingState("c1"))
	if err := restarted.Send(context.Background(), cfg, "u1", []connectors.OutboundMessage{{Text: "hi"}}); err != nil {
		t.Fatalf("Send after restart: %v", err)
	}
	request, ok := fake.lastRequest("/ilink/bot/sendmessage")
	if !ok {
		t.Fatalf("sendmessage not called")
	}
	msg, _ := request.body["msg"].(map[string]any)
	if got := msg["context_token"]; got != "ctx-1" {
		t.Fatalf("context_token = %v, want ctx-1", got)
	}
}

// ── 出站远端媒体安全（SSRF / 类型校验） ─────────────────────────────────────

func TestValidatePublicMediaHostBlocksLocalTargets(t *testing.T) {
	blocked := []string{
		"127.0.0.1", "10.0.0.1", "192.168.1.9", "172.16.5.5",
		"169.254.169.254", "100.64.0.1", "0.0.0.0", "::1", "fc00::1", "fe80::1",
	}
	for _, host := range blocked {
		if err := validatePublicMediaHost(host); err == nil {
			t.Fatalf("host %s should be disallowed", host)
		}
	}
	if err := validatePublicMediaHost("8.8.8.8"); err != nil {
		t.Fatalf("public address should be allowed: %v", err)
	}
	if err := validatePublicMediaHost(""); err == nil {
		t.Fatalf("empty host should be rejected")
	}
}

func TestRemoteMediaFetchBlocksPrivateHostByDefault(t *testing.T) {
	provider := New(WithLogger(discardLogger))
	_, _, _, _, err := provider.downloadRemoteOutboundMedia(
		context.Background(), testConfig("c1", "", nil), "http://127.0.0.1:9/asset.png", "asset.png", mediaKindImage)
	if err == nil || !strings.Contains(err.Error(), "disallowed") {
		t.Fatalf("private host fetch error = %v, want disallowed-address error", err)
	}
}

func TestRemoteMediaRedirectToDisallowedHostIsBlocked(t *testing.T) {
	final := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngTestBytes)
	}))
	t.Cleanup(final.Close)
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target := strings.Replace(final.URL, "127.0.0.1", "localhost", 1) + "/asset.png"
		http.Redirect(w, r, target, http.StatusFound)
	}))
	t.Cleanup(redirector.Close)

	policy := func(host string) error {
		if host == "localhost" {
			return fmt.Errorf("wechat: test policy blocks host %q", host)
		}
		return nil
	}
	provider := New(WithLogger(discardLogger), WithMediaHostPolicy(policy))
	_, _, _, _, err := provider.downloadRemoteOutboundMedia(
		context.Background(), testConfig("c1", "", nil), redirector.URL+"/asset.png", "asset.png", mediaKindImage)
	if err == nil || !strings.Contains(err.Error(), "blocks host") {
		t.Fatalf("redirect target should be blocked, err = %v", err)
	}
}

func TestValidateOutboundRemoteMediaType(t *testing.T) {
	cases := []struct {
		name        string
		url         string
		contentType string
		kind        string
		wantOK      bool
	}{
		{"image by content type", "https://cdn.example/img.png", "image/png", "image", true},
		{"image by extension", "https://cdn.example/img.png", "application/octet-stream", "image", true},
		{"image without type or extension", "https://cdn.example/asset", "application/octet-stream", "image", false},
		{"html rejected", "https://cdn.example/asset", "text/html; charset=utf-8", "image", false},
		{"html with image extension still rejected", "https://cdn.example/img.png", "text/html", "image", false},
		{"video by content type", "https://cdn.example/clip.mp4", "video/mp4", "video", true},
		{"video m4v by extension", "https://cdn.example/clip.m4v", "application/octet-stream", "video", true},
		{"streaming playlist rejected", "https://cdn.example/stream.m3u8", "application/vnd.apple.mpegurl", "video", false},
		{"file unconstrained", "https://cdn.example/whatever", "text/html", "file", true},
		{"empty kind unconstrained", "https://cdn.example/whatever", "text/html", "", true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			err := validateOutboundRemoteMediaType(testCase.url, testCase.contentType, testCase.kind)
			if testCase.wantOK && err != nil {
				t.Fatalf("want allowed, got %v", err)
			}
			if !testCase.wantOK && err == nil {
				t.Fatalf("want rejected, got nil")
			}
		})
	}
}

func TestSendRemoteMediaHTMLRejectedAndDegraded(t *testing.T) {
	fake := newFakeILink(t)
	htmlServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<html><body>not an image</body></html>"))
	}))
	t.Cleanup(htmlServer.Close)
	fake.on("/ilink/bot/sendmessage", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		writeRet0(w)
	})

	provider := New(WithAPIBase(fake.URL()), WithLogger(discardLogger), WithMediaHostPolicy(allowAllMediaHosts))
	provider.rememberContextToken("c1", "u1", "ctx-1")
	cfg := testConfig("c1", fake.URL(), nil)

	err := provider.Send(context.Background(), cfg, "u1", []connectors.OutboundMessage{{
		Media: []connectors.Media{{Kind: mediaKindImage, URL: htmlServer.URL + "/asset"}},
	}})
	if err != nil {
		t.Fatalf("Send should degrade instead of failing: %v", err)
	}
	sends := sendMessageRequests(fake)
	if len(sends) != 1 {
		t.Fatalf("sendmessage calls = %d, want 1 (fallback notice)", len(sends))
	}
	if got := sentText(t, sends[0].body); !strings.Contains(got, "[附件发送失败") {
		t.Fatalf("fallback text = %q", got)
	}
}

// ── 临时媒体清理 ────────────────────────────────────────────────────────────

func TestPersistTempMediaSweepsExpiredFiles(t *testing.T) {
	fixedNow := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	provider := New(WithLogger(discardLogger))
	provider.now = func() time.Time { return fixedNow }

	dir := filepath.Join(os.TempDir(), "aicli", "wechat", "media", "inbound")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir temp dir: %v", err)
	}
	stale := filepath.Join(dir, "sweep-test-stale.bin")
	fresh := filepath.Join(dir, "sweep-test-fresh.bin")
	if err := os.WriteFile(stale, []byte("old"), 0o600); err != nil {
		t.Fatalf("write stale file: %v", err)
	}
	if err := os.WriteFile(fresh, []byte("new"), 0o600); err != nil {
		t.Fatalf("write fresh file: %v", err)
	}
	staleTime := fixedNow.Add(-mediaTempRetention - time.Hour)
	freshTime := fixedNow.Add(-time.Minute)
	if err := os.Chtimes(stale, staleTime, staleTime); err != nil {
		t.Fatalf("chtimes stale: %v", err)
	}
	if err := os.Chtimes(fresh, freshTime, freshTime); err != nil {
		t.Fatalf("chtimes fresh: %v", err)
	}

	// 计数推进到下一次落盘即触发清扫（counter%every == 1）。
	mediaTempSweepCounter.Store(mediaTempSweepEvery)
	if _, err := provider.persistTempMedia([]byte("probe"), "application/octet-stream", "sweep-probe.bin", "inbound"); err != nil {
		t.Fatalf("persistTempMedia: %v", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale temp file should be swept, stat err = %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("fresh temp file should be kept: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(fresh) })
}
