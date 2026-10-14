package commands

// /bots 副屏的单测：ID 生成、Schema 扫码动作探测、列表行序、凭据回填
// （含 owner 自动入白名单）、后台轮询确认路径与终端二维码渲染。
// 副屏框架自身的生命周期/降级契约由 chat_screen_*_test.go 覆盖，这里不重复。

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
	"github.com/wwsheng009/ai-agent-runtime/internal/connectors/api"
)

var botsIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// fakeBotsProvider 是 /bots 副屏链路回归测试的最小通道替身：Schema 声明一个
// kind=qr 动作；RunAction 固定失败，便于断言「扫码流确实被触发」。
type fakeBotsProvider struct{ actionErr error }

func (fakeBotsProvider) Name() string { return "wechat" }

func (fakeBotsProvider) Schema() api.ProviderSchema {
	return api.ProviderSchema{
		Provider: "wechat", Label: "微信", Modes: []string{"polling"},
		Fields:  []api.FieldSchema{{Key: "bot_token", Label: "Bot Token", Type: api.FieldSecret}},
		Actions: []api.ActionSpec{{Key: "start_login", Label: "扫码登录", Kind: "qr"}},
	}
}

func (fakeBotsProvider) Activate(context.Context, connectors.Config) (connectors.Activation, error) {
	return connectors.Activation{}, nil
}

func (fakeBotsProvider) Deactivate(context.Context, connectors.Config) error { return nil }

func (fakeBotsProvider) Send(context.Context, connectors.Config, string, []connectors.OutboundMessage) error {
	return nil
}

func (f fakeBotsProvider) RunAction(context.Context, connectors.Config, string, map[string]any) (map[string]any, error) {
	return nil, f.actionErr
}

// fakeBotsScanProvider 驱动扫码状态机：login_status 依次返回 wait → scanned →
// confirmed(带凭据合并补丁)。
type fakeBotsScanProvider struct {
	mu          sync.Mutex
	startCalls  int
	statusCalls int
}

func (f *fakeBotsScanProvider) Name() string { return "wechat" }

func (f *fakeBotsScanProvider) Schema() api.ProviderSchema {
	return api.ProviderSchema{
		Provider: "wechat", Label: "微信", Modes: []string{"polling"},
		Actions: []api.ActionSpec{{Key: "start_login", Label: "扫码登录", Kind: "qr"}},
	}
}

func (f *fakeBotsScanProvider) Activate(context.Context, connectors.Config) (connectors.Activation, error) {
	return connectors.Activation{}, nil
}

func (f *fakeBotsScanProvider) Deactivate(context.Context, connectors.Config) error { return nil }

func (f *fakeBotsScanProvider) Send(context.Context, connectors.Config, string, []connectors.OutboundMessage) error {
	return nil
}

func (f *fakeBotsScanProvider) RunAction(_ context.Context, _ connectors.Config, action string, _ map[string]any) (map[string]any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if action == "start_login" {
		f.startCalls++
		return map[string]any{
			"login_id":        fmt.Sprintf("login-%d", f.startCalls),
			"qr_code_content": "https://ilink.example/login",
		}, nil
	}
	f.statusCalls++
	switch f.statusCalls {
	case 1:
		return map[string]any{"status": "wait"}, nil
	case 2:
		return map[string]any{"status": "scanned"}, nil
	default:
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
}

// TestChatBotsScanScreenAutoClosesAndBackfills 锁定扫码实况副屏的状态机：
// 副屏内轮询（wait → scanned → confirmed）→ 凭据回填 → ctx 取消自动关屏 →
// AfterClose 在主屏只打印一行结果（而不是把二维码留在主屏）。
func TestChatBotsScanScreenAutoClosesAndBackfills(t *testing.T) {
	chatScreenTestSeamsInstall(t, true)
	session := newUnifiedChatScreenTestSession(t)

	store := connectors.NewMemoryStore(connectors.Config{ID: "wx-scan", Provider: "wechat", Name: "微信", Enabled: true})
	service := connectors.New(connectors.Deps{Store: store, Logf: func(string, ...any) {}})
	provider := &fakeBotsScanProvider{}
	require.NoError(t, service.Register(provider))
	host := &LocalConnectorHost{store: store, service: service}

	prevInterval, prevDeadline := botsScanPollInterval, botsScanDeadline
	botsScanPollInterval = time.Millisecond
	botsScanDeadline = 2 * time.Second
	defer func() { botsScanPollInterval, botsScanDeadline = prevInterval, prevDeadline }()

	// 无 TTY 驱动桩：反复驱动 Refresh 直至 ctx 取消（成功自动关屏）。
	prevRunner := botsScanScreenRunner
	botsScanScreenRunner = func(_ *ChatSession, _ ui.ScreenLease, ctx context.Context, options ui.DebugOverlayOptions) error {
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) && ctx.Err() == nil {
			options.Refresh(ui.DebugOverlayRefreshTick)
			time.Sleep(time.Millisecond)
		}
		return ctx.Err()
	}
	t.Cleanup(func() { botsScanScreenRunner = prevRunner })

	var outputs []string
	prevObserver := chatCommandOutputObserver
	chatCommandOutputObserver = func(_ *ChatSession, text string) { outputs = append(outputs, text) }
	t.Cleanup(func() { chatCommandOutputObserver = prevObserver })

	cfg, ok := store.GetConfig("wx-scan")
	require.True(t, ok)
	runner := botsActionProvider(host, "wechat")
	require.NotNil(t, runner)
	payload, err := runner.RunAction(context.Background(), cfg, "start_login", nil)
	require.NoError(t, err)

	state := newBotsScanScreenState(host, runner, cfg, provider.Schema(), "start_login", payload)
	chatScreenOpenAndApply(session, chatScreenBotsScanSpec(state))

	joined := strings.Join(outputs, "\n")
	require.Contains(t, joined, "扫码登录成功")
	require.NotContains(t, joined, "等待扫码", "主屏不应再出现二维码/等待文案（它在副屏里）")
	got, _ := store.GetConfig("wx-scan")
	require.Equal(t, "tk-9", got.Secrets["bot_token"])
	require.Equal(t, []string{"owner-9"}, got.Policy.AllowedUsers)
}

// TestChatBotsNestedScreenAppliesAfterClose 锁定嵌套副屏必须经
// chatScreenOpenAndApply 打开（AfterClose 才会生效）：
// 列表选中连接器 → 动作副屏选中「扫码登录」→ 扫码流必须被触发。
// 回归背景：曾用裸 openChatScreen 打开嵌套副屏，回车关屏后什么都不发生。
func TestChatBotsNestedScreenAppliesAfterClose(t *testing.T) {
	chatScreenTestSeamsInstall(t, true)
	session := newUnifiedChatScreenTestSession(t)

	store := connectors.NewMemoryStore(connectors.Config{ID: "wx-1", Provider: "wechat", Name: "微信", Enabled: true})
	service := connectors.New(connectors.Deps{Store: store, Logf: func(string, ...any) {}})
	require.NoError(t, service.Register(fakeBotsProvider{actionErr: errors.New("network down")}))
	host := &LocalConnectorHost{store: store, service: service}

	var outputs []string
	prevObserver := chatCommandOutputObserver
	chatCommandOutputObserver = func(_ *ChatSession, text string) { outputs = append(outputs, text) }
	t.Cleanup(func() { chatCommandOutputObserver = prevObserver })

	picks := 0
	chatScreenListRunner = func(_ *ChatSession, _ ui.ScreenLease, _ ui.FullScreenListOptions) (ui.FullScreenListResult, error) {
		picks++
		if picks == 1 {
			return ui.FullScreenListResult{Index: 1}, nil // 列表行 1 = 第一个连接器
		}
		return ui.FullScreenListResult{Index: 0}, nil // 动作行 0 = 扫码登录
	}

	chatScreenOpenAndApply(session, chatScreenBotsListSpec(host))

	require.Equal(t, 2, picks, "嵌套副屏必须被打开并接受选择")
	require.Contains(t, strings.Join(outputs, "\n"), "获取二维码失败",
		"选中扫码登录后必须触发扫码流（而不是静默关屏）")
}

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

// TestStartLocalConnectorsOnDemandCreatesStarterConfig 锁定 /bots 的自举语义：
// 配置文件不存在时按需创建 starter 配置并装配宿主（不再要求用户先手工写
// config.yaml——/bots 本身即配置入口）。
func TestStartLocalConnectorsOnDemandCreatesStarterConfig(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	session := &ChatSession{
		Config:           &config.Config{ConfigFilePath: configPath},
		LocalRuntimeHost: &localChatRuntimeHost{},
	}
	host, err := startLocalConnectorsOnDemand(session)
	if err != nil {
		t.Fatalf("on-demand start: %v", err)
	}
	t.Cleanup(func() { host.Close() })
	if host.Service() == nil || host.Store() == nil {
		t.Fatal("on-demand host missing service/store")
	}
	if currentLocalConnectorHost() == nil {
		t.Fatal("host ref not published after on-demand start")
	}
	if _, statErr := os.Stat(configPath); statErr != nil {
		t.Fatalf("starter config not created: %v", statErr)
	}
	if note := localConnectorsInitNote(); note != "" {
		t.Fatalf("init note should be cleared after success, got %q", note)
	}
}

func TestBotsLocalHostUnavailableReasonRuntimeServerMode(t *testing.T) {
	session := &ChatSession{ChatExecutor: newAICLIRuntimeServerChatExecutor("http://127.0.0.1:8101")}
	reason := botsLocalHostUnavailableReason(session)
	for _, want := range []string{"runtime-server", "127.0.0.1:8101", "--runtime-mode local"} {
		if !strings.Contains(reason, want) {
			t.Fatalf("reason %q missing %q", reason, want)
		}
	}
	if got := botsLocalHostUnavailableReason(&ChatSession{}); !strings.Contains(got, "本机 runtime 未就绪") {
		t.Fatalf("generic reason = %q", got)
	}
	if got := botsLocalHostUnavailableReason(&ChatSession{CapabilitiesInitError: "技能注册失败: boom"}); !strings.Contains(got, "技能注册失败: boom") {
		t.Fatalf("init error reason = %q", got)
	}
}

// TestStartLocalConnectorsOnDemandWaitsForCapabilities 锁定 web/斜杠命令路径
// 的自愈语义：能力面（LocalRuntimeHost）仍在后台装载时按需自举必须**等待**
// 装载完成再装配，而不是立即失败——「配置为空/装载未完成」不是用户可见错误。
func TestStartLocalConnectorsOnDemandWaitsForCapabilities(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	session := &ChatSession{
		Config: &config.Config{ConfigFilePath: configPath},
	}
	// 安装门控但延迟 200ms 才启动装载：模拟「web 请求先于装载到达」。
	// attach 闭包负责挂载 LocalRuntimeHost（与生产 attachChatCapabilities 同责）。
	installed := make(chan struct{})
	installChatCapabilitiesGate(session,
		func() (*chatCapabilityDiscovery, error) {
			<-installed
			return &chatCapabilityDiscovery{}, nil
		},
		func(*chatCapabilityDiscovery) (func(), error) {
			session.LocalRuntimeHost = &localChatRuntimeHost{}
			return func() {}, nil
		},
	)
	require.Nil(t, session.LocalRuntimeHost, "前置：装载未完成前 LocalRuntimeHost 必须为 nil")
	go func() {
		time.Sleep(200 * time.Millisecond)
		close(installed)
		if load := currentChatCapabilityLoad(session); load != nil && load.discover != nil {
			discovery, discErr := load.discover()
			load.completeDiscovery(discovery, discErr)
		}
	}()

	host, err := startLocalConnectorsOnDemand(session)
	require.NoError(t, err, "自举应等待能力面装载完成，而不是立即失败")
	t.Cleanup(func() { host.Close() })
	require.NotNil(t, session.LocalRuntimeHost, "await 后 LocalRuntimeHost 必须已挂载")
	require.NotNil(t, host.Service())
}

func TestBotsUnavailableTextSurfacesInitNote(t *testing.T) {
	prev := localConnectorsInitNote()
	t.Cleanup(func() { setLocalConnectorInitNote(prev) })

	setLocalConnectorInitNote("未找到共享 config.yaml（解析路径：C:\\demo\\config.yaml）")
	text := botsUnavailableText("")
	if !strings.Contains(text, "config.yaml") || !strings.Contains(text, "C:\\demo\\config.yaml") {
		t.Fatalf("unavailable text should surface the init note, got %q", text)
	}

	setLocalConnectorInitNote("")
	fallback := botsUnavailableText("会话缺少配置句柄")
	if !strings.Contains(fallback, "会话缺少配置句柄") {
		t.Fatalf("fallback text = %q", fallback)
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
	adopted, err := botsApplyCredentialPatch(host, cfg, payload)
	if err != nil {
		t.Fatalf("patch: %v", err)
	}
	if adopted != "wx-1" {
		t.Fatalf("adopted = %q, want wx-1 (no duplicate account)", adopted)
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

// TestBotsApplyCredentialPatchAdoptsExistingConnector 锁定重复扫码识别：
// 同一用户（同 provider + 同 owner_user_id=ilink_user_id，平台唯一）重扫码
// 时（微信会新建 bot：新 account_id，并解除旧连接），凭据合并进既有连接器、
// 自动壳连接器被删除——而不是新建第二个机器人配置。
func TestBotsApplyCredentialPatchAdoptsExistingConnector(t *testing.T) {
	store := connectors.NewMemoryStore(
		connectors.Config{
			ID: "wx-existing", Provider: "wechat", Name: "我的微信", Enabled: true,
			// 旧连接：旧 bot 实例 acct-old（每次扫码都会变）+ 稳定 owner。
			Settings: map[string]string{"account_id": "acct-old", "owner_user_id": "owner-9"},
			Policy:   connectors.Policy{AllowedUsers: []string{"vip-user"}},
		},
	)
	host := &LocalConnectorHost{store: store}
	shell := connectors.Config{ID: "wx-shell", Provider: "wechat", Name: "微信", Enabled: true}
	payload := map[string]any{
		"status":           "confirmed",
		"credential_ready": true,
		"settings": map[string]any{
			"base_url":      "https://ilink.example",
			"account_id":    "acct-new",
			"owner_user_id": "owner-9",
		},
		"secrets": map[string]any{"bot_token": "tk-new"},
	}
	adopted, err := botsApplyCredentialPatch(host, shell, payload)
	if err != nil {
		t.Fatalf("patch: %v", err)
	}
	if adopted != "wx-existing" {
		t.Fatalf("adopted = %q, want wx-existing", adopted)
	}
	if _, exists := store.GetConfig("wx-shell"); exists {
		t.Fatal("shell connector should be deleted after adoption")
	}
	got, _ := store.GetConfig("wx-existing")
	// 新 bot 实例标识（account_id）与凭据都已更新；owner 不变。
	if got.Settings["account_id"] != "acct-new" {
		t.Fatalf("account_id = %q, want acct-new (superseded acct-old)", got.Settings["account_id"])
	}
	if got.Secrets["bot_token"] != "tk-new" {
		t.Fatalf("existing connector not updated: %+v", got.Secrets)
	}
	if got.Settings["owner_user_id"] != "owner-9" {
		t.Fatalf("owner_user_id = %q, want unchanged owner-9", got.Settings["owner_user_id"])
	}
	// 既有连接器保留自己的白名单（不被壳的自动 owner 覆盖——它已有白名单）。
	if len(got.Policy.AllowedUsers) != 1 || got.Policy.AllowedUsers[0] != "vip-user" {
		t.Fatalf("allowed_users = %+v, want kept [vip-user]", got.Policy.AllowedUsers)
	}
	if got.UpdatedAt.IsZero() {
		t.Fatal("UpdatedAt should be stamped on adoption")
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

func TestBotsRenderQRText(t *testing.T) {
	blocks, ok := botsRenderQRText("https://ilink.example/login?token=abc123")
	if !ok {
		t.Fatal("expected QR render for login link")
	}
	if !strings.ContainsAny(blocks, "█▀▄") {
		t.Fatalf("no block chars in QR:\n%s", blocks)
	}
	if lines := strings.Count(blocks, "\n"); lines < 12 {
		t.Fatalf("QR too small: %d lines", lines)
	}
	if _, ok := botsRenderQRText(""); ok {
		t.Fatal("empty content must not render as QR")
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
