package restapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
	"github.com/wwsheng009/ai-agent-runtime/internal/connectors/api"
)

// ── 保存前凭据校验（CredentialVerifier）测试替身 ─────────────────────────────

// verifyProvider 是可注入校验结果的 CredentialVerifier 测试替身：
// app_id（required 非 secret）+ bot_token（required secret）+ label（普通），
// 用于区分「凭据相关字段变化」与「普通配置微调」。
type verifyProvider struct {
	verifyErr   error
	verifyCalls int
}

func (p *verifyProvider) Name() string { return "fakeverify" }

func (p *verifyProvider) Schema() api.ProviderSchema {
	return api.ProviderSchema{
		Provider: "fakeverify",
		Label:    "Fake Verify",
		Modes:    []string{"polling"},
		Fields: []api.FieldSchema{
			{Key: "app_id", Label: "AppID", Type: api.FieldText, Required: true},
			{Key: "bot_token", Label: "Bot Token", Type: api.FieldSecret, Required: true},
			{Key: "label", Label: "备注", Type: api.FieldText},
		},
	}
}

func (p *verifyProvider) Activate(context.Context, connectors.Config) (connectors.Activation, error) {
	return connectors.Activation{}, nil
}

func (p *verifyProvider) Deactivate(context.Context, connectors.Config) error { return nil }

func (p *verifyProvider) Send(context.Context, connectors.Config, string, []connectors.OutboundMessage) error {
	return nil
}

func (p *verifyProvider) VerifyCredentials(context.Context, connectors.Config) error {
	p.verifyCalls++
	return p.verifyErr
}

func newVerifyFixture(t *testing.T, verifyErr error, configs ...connectors.Config) (*connectors.MemoryStore, *verifyProvider, http.Handler) {
	t.Helper()
	store := connectors.NewMemoryStore(configs...)
	provider := &verifyProvider{verifyErr: verifyErr}
	service := connectors.New(connectors.Deps{Store: store, Backend: &fakeBackend{}, Logf: func(string, ...any) {}})
	if err := service.Register(provider); err != nil {
		t.Fatal(err)
	}
	deps := Deps{Service: service, Store: store, Authorize: allowAll, Logf: func(string, ...any) {}}
	return store, provider, NewRouter(deps)
}

func verifySeedConfig() connectors.Config {
	return connectors.Config{
		ID: "c1", Provider: "fakeverify", Name: "c1", Enabled: true, Mode: "polling",
		Settings: map[string]string{"app_id": "app-1"},
		Secrets:  map[string]string{"bot_token": "secret-token"},
	}
}

const verifyFailureText = "qqbot: token request failed: invalid appid or secret"

func TestCreateRejectsInvalidCredentialsAndDoesNotPersist(t *testing.T) {
	store, provider, router := newVerifyFixture(t, errors.New(verifyFailureText))
	recorder := do(t, router, http.MethodPost, "/connectors",
		`{"id":"c2","provider":"fakeverify","settings":{"app_id":"app-1"},"secrets":{"bot_token":"bad-token"}}`)
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "凭据校验失败") {
		t.Fatalf("无效凭据应 400 且带明确文案，got %d %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "skip_credential_check=true") {
		t.Fatalf("失败文案应提示跳过开关：%s", recorder.Body.String())
	}
	if _, ok := store.GetConfig("c2"); ok {
		t.Fatal("校验失败不得落盘")
	}
	if provider.verifyCalls != 1 {
		t.Fatalf("verify calls = %d, want 1", provider.verifyCalls)
	}
}

func TestCreateSkipCredentialCheckBypassesVerification(t *testing.T) {
	store, provider, router := newVerifyFixture(t, errors.New(verifyFailureText))
	recorder := do(t, router, http.MethodPost, "/connectors",
		`{"id":"c2","provider":"fakeverify","skip_credential_check":true,"settings":{"app_id":"app-1"},"secrets":{"bot_token":"bad-token"}}`)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("显式跳过应可创建，got %d %s", recorder.Code, recorder.Body.String())
	}
	if _, ok := store.GetConfig("c2"); !ok {
		t.Fatal("跳过校验时应落盘")
	}
	if provider.verifyCalls != 0 {
		t.Fatalf("跳过时不得调用校验，calls = %d", provider.verifyCalls)
	}
}

func TestCreateWithValidCredentialsPersists(t *testing.T) {
	store, provider, router := newVerifyFixture(t, nil)
	recorder := do(t, router, http.MethodPost, "/connectors",
		`{"id":"c2","provider":"fakeverify","settings":{"app_id":"app-1"},"secrets":{"bot_token":"good-token"}}`)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("有效凭据应 201，got %d %s", recorder.Code, recorder.Body.String())
	}
	if _, ok := store.GetConfig("c2"); !ok {
		t.Fatal("有效凭据应落盘")
	}
	if provider.verifyCalls != 1 {
		t.Fatalf("verify calls = %d, want 1", provider.verifyCalls)
	}
}

func TestUpdateVerifiesCredentialChangesAndRejectsInvalid(t *testing.T) {
	store, provider, router := newVerifyFixture(t, errors.New(verifyFailureText), verifySeedConfig())

	// secret 变化 → 校验（失败 → 400 且不落盘）。
	recorder := do(t, router, http.MethodPatch, "/connectors/c1", `{"secrets":{"bot_token":"bad-token"}}`)
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "凭据校验失败") {
		t.Fatalf("secret 变化 + 无效凭据应 400，got %d %s", recorder.Code, recorder.Body.String())
	}
	if cfg, _ := store.GetConfig("c1"); cfg.Secret("bot_token") != "secret-token" {
		t.Fatalf("校验失败不得覆盖旧密钥：%+v", cfg.Secrets)
	}
	if provider.verifyCalls != 1 {
		t.Fatalf("verify calls = %d, want 1", provider.verifyCalls)
	}

	// required 非 secret 字段（app_id）变化同样触发校验。
	recorder = do(t, router, http.MethodPatch, "/connectors/c1", `{"settings":{"app_id":"app-2"}}`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("required 字段变化应触发校验并 400，got %d %s", recorder.Code, recorder.Body.String())
	}
	if cfg, _ := store.GetConfig("c1"); cfg.Setting("app_id", "") != "app-1" {
		t.Fatalf("校验失败不得覆盖 app_id：%+v", cfg.Settings)
	}
	if provider.verifyCalls != 2 {
		t.Fatalf("verify calls = %d, want 2", provider.verifyCalls)
	}
}

func TestUpdateSkipsVerificationForNonCredentialChanges(t *testing.T) {
	store, provider, router := newVerifyFixture(t, errors.New(verifyFailureText), verifySeedConfig())

	// 名称/备注（非 required、非 secret）变化 → 不发起在线校验，不被平台可用性阻断。
	renamed := do(t, router, http.MethodPatch, "/connectors/c1", `{"name":"改个名","settings":{"label":"备注"}}`)
	if renamed.Code != http.StatusOK {
		t.Fatalf("普通字段更新应 200，got %d %s", renamed.Code, renamed.Body.String())
	}
	if cfg, _ := store.GetConfig("c1"); cfg.Name != "改个名" || cfg.Setting("label", "") != "备注" {
		t.Fatalf("普通字段未落盘：%+v", cfg)
	}
	if provider.verifyCalls != 0 {
		t.Fatalf("普通字段变化不得触发校验，calls = %d", provider.verifyCalls)
	}
}

func TestUpdateSkipsVerificationWhenSecretCleared(t *testing.T) {
	store, provider, router := newVerifyFixture(t, errors.New(verifyFailureText), verifySeedConfig())

	// 显式清空 = 有意失效，不做事后在线校验（与既有「更新允许清空密钥」一致）。
	recorder := do(t, router, http.MethodPatch, "/connectors/c1", `{"secrets":{"bot_token":"__clear__"}}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("清空密钥应 200，got %d %s", recorder.Code, recorder.Body.String())
	}
	if cfg, _ := store.GetConfig("c1"); cfg.Secret("bot_token") != "" {
		t.Fatalf("__clear__ 应清空密钥：%+v", cfg.Secrets)
	}
	if provider.verifyCalls != 0 {
		t.Fatalf("清空密钥不得触发在线校验，calls = %d", provider.verifyCalls)
	}
}

func TestUpdateWithValidCredentialsPersistsSecretChange(t *testing.T) {
	store, provider, router := newVerifyFixture(t, nil, verifySeedConfig())
	recorder := do(t, router, http.MethodPatch, "/connectors/c1", `{"secrets":{"bot_token":"tok-2"}}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("有效凭据应 200，got %d %s", recorder.Code, recorder.Body.String())
	}
	if cfg, _ := store.GetConfig("c1"); cfg.Secret("bot_token") != "tok-2" {
		t.Fatalf("新密钥未落盘：%+v", cfg.Secrets)
	}
	if provider.verifyCalls != 1 {
		t.Fatalf("verify calls = %d, want 1", provider.verifyCalls)
	}
	if strings.Contains(recorder.Body.String(), "tok-2") {
		t.Fatalf("响应不得回显密钥明文：%s", recorder.Body.String())
	}
}
