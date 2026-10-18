package connectors

// /invite 命令：核心命令闭环（不进入 agent turn），走 ShareLinkProvider 可选能力。

import (
	"context"
	"strings"
	"testing"
)

// shareProvider 在 basicProvider 基础上实现 ShareLinkProvider。
type shareProvider struct {
	basicProvider
	link      string
	err       error
	callbacks []string
}

func (p *shareProvider) GenerateShareLink(_ context.Context, _ Config, callbackData string) (string, error) {
	p.callbacks = append(p.callbacks, callbackData)
	if p.err != nil {
		return "", p.err
	}
	return p.link, nil
}

func TestInviteCommandUsesShareLinkProvider(t *testing.T) {
	provider := &shareProvider{link: "https://qun.qq.com/robot?data=abc"}
	backend := &fakeBackend{session: SessionRef{SessionID: "s1"}}
	store := NewMemoryStore(testConfig("c1", "basic"))
	service := newTestService(t, store, backend, provider)

	if err := service.HandleInbound(context.Background(),
		inbound("c1", "user:u1", "m1", "/invite abc")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "invite reply", func() bool { return len(provider.sentTexts()) > 0 })

	texts := provider.sentTexts()
	if len(texts) != 1 || !strings.Contains(texts[0], "https://qun.qq.com/robot?data=abc") {
		t.Fatalf("reply = %v", texts)
	}
	if len(provider.callbacks) != 1 || provider.callbacks[0] != "abc" {
		t.Fatalf("callback = %v", provider.callbacks)
	}
	// 核心命令不进入 agent turn。
	if got := len(backend.requests()); got != 0 {
		t.Fatalf("invite must not reach backend, got %d requests", got)
	}
}

func TestInviteCommandUnsupportedChannel(t *testing.T) {
	provider := &basicProvider{}
	backend := &fakeBackend{session: SessionRef{SessionID: "s1"}}
	store := NewMemoryStore(testConfig("c1", "basic"))
	service := newTestService(t, store, backend, provider)

	if err := service.HandleInbound(context.Background(),
		inbound("c1", "user:u1", "m1", "/invite")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "unsupported reply", func() bool { return len(provider.sentTexts()) > 0 })
	texts := provider.sentTexts()
	if len(texts) != 1 || !strings.Contains(texts[0], "不支持") {
		t.Fatalf("reply = %v", texts)
	}
}

func TestInviteCommandFailureIsReported(t *testing.T) {
	provider := &shareProvider{err: context.DeadlineExceeded}
	backend := &fakeBackend{session: SessionRef{SessionID: "s1"}}
	store := NewMemoryStore(testConfig("c1", "basic"))
	service := newTestService(t, store, backend, provider)

	if err := service.HandleInbound(context.Background(),
		inbound("c1", "user:u1", "m1", "/invite")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "failure reply", func() bool { return len(provider.sentTexts()) > 0 })
	if texts := provider.sentTexts(); !strings.Contains(texts[0], "失败") {
		t.Fatalf("reply = %v", texts)
	}
}

// 帮助文本由命令表生成，必须包含所有核心命令。
func TestHelpTextCoversCatalog(t *testing.T) {
	cfg := testConfig("c1", "fake")
	text := helpText(cfg)
	for _, command := range AgentCommands() {
		if !strings.Contains(text, "/"+command.Name) {
			t.Fatalf("help text missing /%s:\n%s", command.Name, text)
		}
	}
}
