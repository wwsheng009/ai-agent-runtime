package connectors

// 入站媒体物化（MediaFetcher + Service 编排）的端到端单测：
//   - 实现 MediaFetcher 的 Provider：下载成功 → 摘要块（含 local_path）注入文本；
//   - 下载失败 → 保留元数据摘要（无 local_path），媒体不静默消失；
//   - 未实现 MediaFetcher 的 Provider 行为不变（既有用例覆盖）。

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// mediaFetchProvider 在 fakeProvider 基础上实现 MediaFetcher：写入临时文件
// 并返回路径；failWith 非空时模拟下载失败。
type mediaFetchProvider struct {
	fakeProvider
	mu       sync.Mutex
	calls    int
	failWith error
}

func (p *mediaFetchProvider) FetchInboundMedia(_ context.Context, _ Config, _ Media) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	if p.failWith != nil {
		return "", p.failWith
	}
	dir, err := os.MkdirTemp("", "connector-media-test-*")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "payload.bin")
	if err := os.WriteFile(path, []byte("media-payload"), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func (p *mediaFetchProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func TestServiceMaterializesInboundMedia(t *testing.T) {
	provider := &mediaFetchProvider{}
	backend := &fakeBackend{session: SessionRef{SessionID: "sess-1"}}
	store := NewMemoryStore(testConfig("c1", "fake"))
	service := newTestService(t, store, backend, provider)

	msg := inbound("c1", "chat-1", "m1", "看看这张图")
	msg.Media = []Media{{Kind: "image", FileName: "photo.jpg", ContentType: "image/jpeg", URL: "https://cdn.example/photo.jpg"}}
	if err := service.HandleInbound(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "backend handled", func() bool { return len(backend.requests()) > 0 })

	req := backend.requests()[0]
	for _, want := range []string{
		"看看这张图",
		"[Image attachment]",
		"file_name: photo.jpg",
		"local_path: ",
		"source_url: https://cdn.example/photo.jpg",
	} {
		if !strings.Contains(req.Text, want) {
			t.Fatalf("text = %q, want contains %q", req.Text, want)
		}
	}
	if got := provider.callCount(); got != 1 {
		t.Fatalf("fetch calls = %d, want 1", got)
	}
}

func TestServiceMaterializeMediaFailureKeepsSummary(t *testing.T) {
	provider := &mediaFetchProvider{failWith: errors.New("boom")}
	backend := &fakeBackend{session: SessionRef{SessionID: "sess-1"}}
	store := NewMemoryStore(testConfig("c1", "fake"))
	service := newTestService(t, store, backend, provider)

	msg := inbound("c1", "chat-1", "m1", "附件在这里")
	msg.Media = []Media{{Kind: "file", FileName: "doc.pdf", URL: "https://cdn.example/doc.pdf"}}
	if err := service.HandleInbound(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "backend handled", func() bool { return len(backend.requests()) > 0 })

	req := backend.requests()[0]
	if strings.Contains(req.Text, "local_path: ") {
		t.Fatalf("failed fetch must not report local_path: %q", req.Text)
	}
	if !strings.Contains(req.Text, "file_name: doc.pdf") ||
		!strings.Contains(req.Text, "source_url: https://cdn.example/doc.pdf") {
		t.Fatalf("metadata summary lost on failure: %q", req.Text)
	}
}
