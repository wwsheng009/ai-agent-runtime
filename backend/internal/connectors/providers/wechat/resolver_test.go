package wechat

import (
	"bytes"
	"context"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
)

// ── 候选 URL 归一 ──────────────────────────────────────────────────────────

func TestResolveRemoteMediaURL(t *testing.T) {
	cases := []struct {
		base string
		raw  string
		want string
	}{
		{"https://a.example/page", "/img/x.png", "https://a.example/img/x.png"},
		{"https://a.example/dir/page", "img/x.png", "https://a.example/dir/img/x.png"},
		{"http://a.example/p", "//cdn.example/x.png", "http://cdn.example/x.png"},
		{"https://a.example/p", "https://b.example/x.png", "https://b.example/x.png"},
		{"https://a.example/p", "", ""},
	}
	for _, testCase := range cases {
		if got := resolveRemoteMediaURL(testCase.base, testCase.raw); got != testCase.want {
			t.Fatalf("resolveRemoteMediaURL(%q, %q) = %q, want %q", testCase.base, testCase.raw, got, testCase.want)
		}
	}
}

// ── 页面解析 ───────────────────────────────────────────────────────────────

func TestResolveRemoteMediaFromHTMLPage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/share":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(w, `<html><head><meta property="og:image" content="/img/cover.png"></head><body>分享页</body></html>`)
		case "/img/cover.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(pngTestBytes)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	provider := New(WithLogger(discardLogger), WithMediaHostPolicy(allowAllMediaHosts))
	filePath, _, contentType, cleanup, err := provider.downloadRemoteOutboundMedia(
		context.Background(), testConfig("c1", "", nil), server.URL+"/share", "", mediaKindImage)
	if err != nil {
		t.Fatalf("resolve page media: %v", err)
	}
	defer cleanup()
	if contentType != "image/png" {
		t.Fatalf("content type = %q, want image/png", contentType)
	}
	data, err := os.ReadFile(filePath)
	if err != nil || !bytes.Equal(data, pngTestBytes) {
		t.Fatalf("resolved media content mismatch: err=%v", err)
	}
}

func TestResolveRemoteMediaFromJSONPage(t *testing.T) {
	videoBytes := []byte("\x00\x00\x00\x18ftypmp42json-video")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/watch":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(w, `<html><body><script>var config = {"contentUrl": "/media/clip.mp4"};</script></body></html>`)
		case "/media/clip.mp4":
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write(videoBytes)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	provider := New(WithLogger(discardLogger), WithMediaHostPolicy(allowAllMediaHosts))
	filePath, _, contentType, cleanup, err := provider.downloadRemoteOutboundMedia(
		context.Background(), testConfig("c1", "", nil), server.URL+"/watch", "clip", mediaKindVideo)
	if err != nil {
		t.Fatalf("resolve json page: %v", err)
	}
	defer cleanup()
	if contentType != "video/mp4" {
		t.Fatalf("content type = %q, want video/mp4", contentType)
	}
	if data, err := os.ReadFile(filePath); err != nil || !bytes.Equal(data, videoBytes) {
		t.Fatalf("resolved video mismatch: err=%v", err)
	}
}

func TestResolveRemoteMediaPageWithoutCandidatesFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, `<html><body>no media here</body></html>`)
	}))
	t.Cleanup(server.Close)

	provider := New(WithLogger(discardLogger), WithMediaHostPolicy(allowAllMediaHosts))
	_, _, _, _, err := provider.downloadRemoteOutboundMedia(context.Background(), testConfig("c1", "", nil), server.URL+"/empty", "", mediaKindImage)
	if err == nil || !strings.Contains(err.Error(), "no downloadable") {
		t.Fatalf("error = %v, want no-downloadable-candidates error", err)
	}
}

func TestResolveRemoteMediaRecursiveReferenceFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, `<html><head><meta property="og:image" content="/page"></head></html>`)
	}))
	t.Cleanup(server.Close)

	provider := New(WithLogger(discardLogger), WithMediaHostPolicy(allowAllMediaHosts))
	_, _, _, _, err := provider.downloadRemoteOutboundMedia(context.Background(), testConfig("c1", "", nil), server.URL+"/page", "", mediaKindImage)
	if err == nil || !strings.Contains(err.Error(), "recursive") {
		t.Fatalf("error = %v, want recursive-url error", err)
	}
}

// TestSendRemotePageMediaResolvesAndUploads 覆盖端到端：Send 收到页面型 URL →
// resolver 提取 og:image → 下载 → 上传 → 发送媒体 item。
func TestSendRemotePageMediaResolvesAndUploads(t *testing.T) {
	fake := newFakeILink(t)
	cdn := newCDNRecorder(t)
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/share" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, `<html><head><meta property="og:image" content="`+cdn.server.URL+`/asset.png"></head></html>`)
	}))
	t.Cleanup(page.Close)

	fake.on("/ilink/bot/getuploadurl", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		writeJSON(t, w, map[string]any{
			"ret": 0, "errcode": 0,
			"upload_param":    "up-page",
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
		Media: []connectors.Media{{Kind: mediaKindImage, URL: page.URL + "/share"}},
	}})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	uploadRequest, _ := fake.lastRequest("/ilink/bot/getuploadurl")
	aesKeyHex, _ := uploadRequest.body["aeskey"].(string)
	aesKey, _ := hex.DecodeString(aesKeyHex)
	decrypted, err := decryptAESECB(cdn.uploaded(), aesKey)
	if err != nil || !bytes.Equal(decrypted, pngTestBytes) {
		t.Fatalf("uploaded page media mismatch: err=%v", err)
	}
	if len(sendMessageRequests(fake)) != 1 {
		t.Fatalf("expected one media message")
	}
}

// ── 流媒体清单 → 转码 ──────────────────────────────────────────────────────

func TestResolveStreamingPlaylistWithoutTranscoderFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		_, _ = io.WriteString(w, "#EXTM3U\n#EXT-X-VERSION:3\n")
	}))
	t.Cleanup(server.Close)

	originalLook := lookPathRemoteVideoCommand
	t.Cleanup(func() { lookPathRemoteVideoCommand = originalLook })
	lookPathRemoteVideoCommand = func(string) (string, error) { return "", os.ErrNotExist }

	provider := New(WithLogger(discardLogger), WithMediaHostPolicy(allowAllMediaHosts))
	_, _, _, _, err := provider.downloadRemoteOutboundMedia(context.Background(), testConfig("c1", "", nil), server.URL+"/stream.m3u8", "clip", mediaKindVideo)
	if err == nil || !strings.Contains(err.Error(), "transcod") {
		t.Fatalf("error = %v, want transcoding-required error", err)
	}
}

func TestResolveStreamingPlaylistTranscodesWithStub(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		_, _ = io.WriteString(w, "#EXTM3U\n#EXT-X-VERSION:3\n")
	}))
	t.Cleanup(server.Close)

	transcodedBytes := []byte("\x00\x00\x00\x18ftypmp42transcoded-stub")
	originalLook, originalRun := lookPathRemoteVideoCommand, runRemoteVideoTranscode
	t.Cleanup(func() {
		lookPathRemoteVideoCommand = originalLook
		runRemoteVideoTranscode = originalRun
	})
	lookPathRemoteVideoCommand = func(string) (string, error) { return "fake-ffmpeg", nil }
	transcodedInputFound := false
	runRemoteVideoTranscode = func(_ context.Context, _ string, args []string, outputPath string) error {
		for _, arg := range args {
			if arg == server.URL+"/stream.m3u8" {
				transcodedInputFound = true
			}
		}
		return os.WriteFile(outputPath, transcodedBytes, 0o600)
	}

	provider := New(WithLogger(discardLogger), WithMediaHostPolicy(allowAllMediaHosts))
	filePath, _, contentType, cleanup, err := provider.downloadRemoteOutboundMedia(
		context.Background(), testConfig("c1", "", nil), server.URL+"/stream.m3u8", "", mediaKindVideo)
	if err != nil {
		t.Fatalf("resolve playlist: %v", err)
	}
	if contentType != "video/mp4" {
		t.Fatalf("content type = %q, want video/mp4", contentType)
	}
	if base := filepath.Base(filePath); !strings.HasPrefix(base, "stream-") {
		t.Fatalf("transcoded file name = %q, want stream-<rand>.mp4（URL 派生名回退链）", base)
	}
	if !transcodedInputFound {
		t.Fatalf("transcoder did not receive the playlist url")
	}
	data, err := os.ReadFile(filePath)
	if err != nil || !bytes.Equal(data, transcodedBytes) {
		t.Fatalf("transcoded content mismatch: err=%v", err)
	}
	cleanup()
	if _, err := os.Stat(filePath); !os.IsNotExist(err) {
		t.Fatalf("cleanup should remove transcoded file, stat err = %v", err)
	}
}
