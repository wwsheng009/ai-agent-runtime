package commands

// /bots 副屏的单测：ID 生成、Schema 扫码动作探测、列表行序、凭据回填
// （含 owner 自动入白名单）、后台轮询确认路径与终端二维码渲染。
// 副屏框架自身的生命周期/降级契约由 chat_screen_*_test.go 覆盖，这里不重复。

import (
	"context"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
	"github.com/wwsheng009/ai-agent-runtime/internal/connectors/api"
)

var botsIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

func TestBotsGenerateConnectorID(t *testing.T) {
	id := botsGenerateConnectorID("WeChat")
	if !botsIDPattern.MatchString(id) {
		t.Fatalf("id %q does not match restapi id constraint", id)
	}
	if !strings.HasPrefix(id, "wechat-") {
		t.Fatalf("id %q missing provider prefix", id)
	}
	other := botsGenerateConnectorID("wechat")
	if id == other {
		t.Fatalf("ids should not collide: %q == %q", id, other)
	}
	if got := botsGenerateConnectorID("!!!"); !strings.HasPrefix(got, "conn-") {
		t.Fatalf("non-alnum provider should fall back to conn- prefix, got %q", got)
	}
}

func TestBotsQRActionKey(t *testing.T) {
	withQR := api.ProviderSchema{
		Provider: "wechat",
		Actions: []api.ActionSpec{
			{Key: "start_login", Kind: "qr"},
			{Key: "login_status", Kind: "button"},
		},
	}
	if got := botsQRActionKey(withQR); got != "start_login" {
		t.Fatalf("qr action = %q, want start_login", got)
	}
	noQR := api.ProviderSchema{Provider: "telegram", Actions: []api.ActionSpec{{Key: "refresh", Kind: "button"}}}
	if got := botsQRActionKey(noQR); got != "" {
		t.Fatalf("qr action = %q, want empty", got)
	}
}

func TestChatBotsListSpecRowsOrder(t *testing.T) {
	store := connectors.NewMemoryStore(
		connectors.Config{ID: "wx-b", Provider: "wechat", Name: "微信 B", Enabled: true},
		connectors.Config{ID: "wx-a", Provider: "wechat", Name: "微信 A", Enabled: false},
	)
	host := &LocalConnectorHost{store: store}
	spec := chatScreenBotsListSpec(host)
	if spec.Kind != chatScreenList || spec.ID != chatBotsScreenListID {
		t.Fatalf("unexpected spec: %+v", spec)
	}
	if len(spec.Rows) != 3 {
		t.Fatalf("rows = %d, want 3 (新增 + 2 连接器)", len(spec.Rows))
	}
	if !strings.Contains(spec.Rows[0].Title, "新增") {
		t.Fatalf("first row should be 新增, got %q", spec.Rows[0].Title)
	}
	// 连接器按 ID 升序：wx-a 在 wx-b 前。
	if spec.Rows[1].Title != "微信 A" || spec.Rows[2].Title != "微信 B" {
		t.Fatalf("rows order = [%q, %q]", spec.Rows[1].Title, spec.Rows[2].Title)
	}
	if !strings.Contains(spec.Rows[1].Detail, "wx-a") || !strings.Contains(spec.Rows[1].Detail, "已停用") {
		t.Fatalf("row detail = %q", spec.Rows[1].Detail)
	}
}

func TestBotsApplyCredentialPatchMergesAndWhitelists(t *testing.T) {
	store := connectors.NewMemoryStore(connectors.Config{
		ID: "wx-1", Provider: "wechat", Name: "微信", Enabled: true,
		Settings: map[string]string{"route_tag": "keep-me"},
		Secrets:  map[string]string{"old": "keep"},
	})
	host := &LocalConnectorHost{store: store}
	cfg, ok := store.GetConfig("wx-1")
	if !ok {
		t.Fatal("seed config missing")
	}
	payload := map[string]any{
		"status":           "confirmed",
		"credential_ready": true,
		"settings": map[string]any{
			"base_url":      "https://ilink.example",
			"account_id":    "acct-1",
			"owner_user_id": "owner-9",
		},
		"secrets": map[string]any{"bot_token": "tk-9"},
	}
	if err := botsApplyCredentialPatch(host, cfg, payload); err != nil {
		t.Fatalf("patch: %v", err)
	}
	got, _ := store.GetConfig("wx-1")
	if got.Settings["base_url"] != "https://ilink.example" || got.Settings["route_tag"] != "keep-me" {
		t.Fatalf("settings merge = %+v", got.Settings)
	}
	if got.Secrets["bot_token"] != "tk-9" || got.Secrets["old"] != "keep" {
		t.Fatalf("secrets merge = %+v", got.Secrets)
	}
	if len(got.Policy.AllowedUsers) != 1 || got.Policy.AllowedUsers[0] != "owner-9" {
		t.Fatalf("allowed_users = %+v, want [owner-9]", got.Policy.AllowedUsers)
	}
	if !got.Enabled {
		t.Fatal("connector should stay enabled after credential patch")
	}
}

// fakeBotsActionProvider 是 ActionProvider 测试替身：login_status 首次返回
// scanned，第二次返回 confirmed + 凭据合并补丁。
type fakeBotsActionProvider struct {
	mu          sync.Mutex
	statusCalls int
}

func (f *fakeBotsActionProvider) RunAction(_ context.Context, _ connectors.Config, action string, _ map[string]any) (map[string]any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if action != botsLoginStatusAction {
		return map[string]any{"login_id": "login-1"}, nil
	}
	f.statusCalls++
	if f.statusCalls == 1 {
		return map[string]any{"status": "scanned"}, nil
	}
	return map[string]any{
		"status":           "confirmed",
		"credential_ready": true,
		"settings": map[string]any{
			"base_url":      "https://ilink.example",
			"account_id":    "acct-1",
			"owner_user_id": "owner-9",
		},
		"secrets": map[string]any{"bot_token": "tk-9"},
	}, nil
}

func TestBotsPollLoginConfirmedBackfills(t *testing.T) {
	prevInterval, prevDeadline := botsScanPollInterval, botsScanDeadline
	botsScanPollInterval = time.Millisecond
	botsScanDeadline = 2 * time.Second
	defer func() {
		botsScanPollInterval, botsScanDeadline = prevInterval, prevDeadline
	}()

	store := connectors.NewMemoryStore(connectors.Config{
		ID: "wx-poll", Provider: "wechat", Name: "微信", Enabled: true,
	})
	host := &LocalConnectorHost{store: store}
	cfg, _ := store.GetConfig("wx-poll")
	// session 为 nil：supplement 走 no-op 守卫，不 panic。
	botsPollLogin(nil, host, &fakeBotsActionProvider{}, cfg, api.ProviderSchema{}, "login-1")

	got, _ := store.GetConfig("wx-poll")
	if got.Secrets["bot_token"] != "tk-9" {
		t.Fatalf("poll did not backfill token: %+v", got.Secrets)
	}
	if len(got.Policy.AllowedUsers) != 1 || got.Policy.AllowedUsers[0] != "owner-9" {
		t.Fatalf("poll did not whitelist owner: %+v", got.Policy.AllowedUsers)
	}
}

func TestBotsRenderQRBlocks(t *testing.T) {
	// 构造 21x21 黑白棋盘 PNG（模拟二维码），转 data URI 后应能渲染出块字符。
	img := image.NewRGBA(image.Rect(0, 0, 42, 42))
	for y := 0; y < 42; y++ {
		for x := 0; x < 42; x++ {
			if (x/2+y/2)%2 == 0 {
				img.Set(x, y, color.RGBA{A: 255})
			} else {
				img.Set(x, y, color.RGBA{R: 255, G: 255, B: 255, A: 255})
			}
		}
	}
	var buf strings.Builder
	if err := png.Encode(&bufWriter{&buf}, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	uri := "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte(buf.String()))
	blocks, ok := botsRenderQRBlocks(uri)
	if !ok {
		t.Fatal("expected QR blocks to render")
	}
	if !strings.ContainsAny(blocks, "█▀▄") {
		t.Fatalf("blocks missing block chars:\n%s", blocks)
	}
	if _, ok := botsRenderQRBlocks("https://ilink.example/login"); ok {
		t.Fatal("non-image content should not render as blocks")
	}
}

// bufWriter 是 strings.Builder 的最小 io.Writer 适配（避免引入 bytes 依赖面）。
type bufWriter struct{ b *strings.Builder }

func (w *bufWriter) Write(p []byte) (int, error) { return w.b.Write(p) }
