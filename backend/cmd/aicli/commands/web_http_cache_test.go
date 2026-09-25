package commands

import (
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// resetWebHTTPCaches 清空包级响应缓存：测试之间不得互相污染（TTL 窗口内
// 相同指纹会命中缓存，跨测试断言会看到别的用例的构建结果）。
func resetWebHTTPCaches() {
	chatWebSessionsCache.invalidate()
}

func TestWebHTTPGzipNegotiation(t *testing.T) {
	payload := []byte(strings.Repeat("会话内容 ", 400)) // > webHTTPGzipMinBytes

	t.Run("accepted", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/web/api/sessions", nil)
		req.Header.Set("Accept-Encoding", "gzip, deflate")
		webHTTPWriteBody(rec, req, webAPIJSONContentType, payload, "")

		if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
			t.Fatalf("Content-Encoding = %q, want gzip", got)
		}
		if rec.Body.Len() >= len(payload) {
			t.Fatalf("compressed body %d bytes not smaller than payload %d", rec.Body.Len(), len(payload))
		}
		reader, err := gzip.NewReader(rec.Body)
		if err != nil {
			t.Fatalf("gzip.NewReader: %v", err)
		}
		decoded, err := io.ReadAll(reader)
		if err != nil {
			t.Fatalf("read gzip body: %v", err)
		}
		if string(decoded) != string(payload) {
			t.Fatalf("decoded body mismatch: %d bytes", len(decoded))
		}
	})

	t.Run("not accepted", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/web/api/sessions", nil)
		webHTTPWriteBody(rec, req, webAPIJSONContentType, payload, "")
		if got := rec.Header().Get("Content-Encoding"); got != "" {
			t.Fatalf("Content-Encoding = %q, want empty", got)
		}
		if rec.Body.String() != string(payload) {
			t.Fatal("plain body must be written byte-for-byte")
		}
	})

	t.Run("q=0 rejected", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/web/api/sessions", nil)
		req.Header.Set("Accept-Encoding", "gzip;q=0")
		webHTTPWriteBody(rec, req, webAPIJSONContentType, payload, "")
		if got := rec.Header().Get("Content-Encoding"); got != "" {
			t.Fatalf("Content-Encoding = %q, want empty for q=0", got)
		}
	})
}

func TestWebHTTPETagConditionalRequest(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/web/api/screen", nil)
	body := []byte("payload")
	etag := webHTTPBodyETag(body, "screen")
	webHTTPWriteBody(rec, req, "text/plain", body, etag)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("ETag"); got != etag {
		t.Fatalf("ETag = %q, want %q", got, etag)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/web/api/screen", nil)
	req.Header.Set("If-None-Match", "W/\"deadbeef\", "+etag)
	if !webHTTPETagMatches(req, etag) {
		t.Fatal("If-None-Match list with W/ prefix must match")
	}
	webHTTPWriteNotModified(rec, etag)
	if rec.Code != http.StatusNotModified {
		t.Fatalf("status = %d, want 304", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("304 body = %q, want empty", rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("Cache-Control = %q, want no-cache", got)
	}
}

func TestWebHTTPResponseCacheSingleFlightAndTTL(t *testing.T) {
	cache := newWebHTTPResponseCache(50 * time.Millisecond)
	var builds int32
	build := func() ([]byte, error) {
		atomic.AddInt32(&builds, 1)
		time.Sleep(20 * time.Millisecond)
		return []byte("body"), nil
	}

	var wait sync.WaitGroup
	for i := 0; i < 8; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if _, _, err := cache.get("k", build); err != nil {
				t.Errorf("cache.get: %v", err)
			}
		}()
	}
	wait.Wait()
	if got := atomic.LoadInt32(&builds); got != 1 {
		t.Fatalf("builds = %d, want 1 (single flight)", got)
	}

	if _, cached, _ := cache.get("k", build); !cached {
		t.Fatal("expected TTL hit")
	}
	time.Sleep(80 * time.Millisecond)
	if _, cached, _ := cache.get("k", build); cached {
		t.Fatal("expected TTL expiry to rebuild")
	}
	if got := atomic.LoadInt32(&builds); got != 2 {
		t.Fatalf("builds = %d, want 2 after expiry", got)
	}
}

// 条件请求命中时不得再触发任何完整加载：304 路径必须早于预览回退加载。
func TestChatWebSessionsConditionalRequestSkipsFallbackLoads(t *testing.T) {
	resetWebHTTPCaches()
	t.Cleanup(resetWebHTTPCaches)
	storage, manager, session := newCountingWebSessionFixture(t)
	ctx := context.Background()

	// 候选带齐标题与摘要 → 预览可纯元数据渲染 → 参与指纹（可 304）。
	candidate, err := manager.Create(ctx, "test-user")
	if err != nil {
		t.Fatalf("manager.Create(candidate): %v", err)
	}
	candidate.Metadata.Title = "候选会话"
	candidate.Metadata.Summary = "候选摘要"
	candidate.AddMessage(*runtimetypes.NewUserMessage("候选会话的第一条消息"))
	if err := storage.Save(ctx, candidate); err != nil {
		t.Fatalf("save candidate: %v", err)
	}
	loadsBefore := storage.fullLoads()

	first := httptest.NewRecorder()
	withWebTestSession(t, session)
	HandleChatWebAPISessions(first, httptest.NewRequest(http.MethodGet, ChatWebAPISessionsPath+"?scope=all", nil))
	if first.Code != http.StatusOK {
		t.Fatalf("first status = %d body=%s", first.Code, first.Body.String())
	}
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("first response must carry an ETag")
	}
	if first.Header().Get(webHTTPServerTimingHeader) == "" {
		t.Fatal("first response must carry Server-Timing")
	}

	second := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, ChatWebAPISessionsPath+"?scope=all", nil)
	req.Header.Set("If-None-Match", etag)
	withWebTestSession(t, session)
	HandleChatWebAPISessions(second, req)
	if second.Code != http.StatusNotModified {
		t.Fatalf("second status = %d, want 304 (body=%s)", second.Code, second.Body.String())
	}
	if second.Body.Len() != 0 {
		t.Fatalf("304 must not carry a body, got %q", second.Body.String())
	}
	if got := storage.fullLoads(); got != loadsBefore {
		t.Fatalf("full loads during conditional request = %d, want %d", got, loadsBefore)
	}
}
