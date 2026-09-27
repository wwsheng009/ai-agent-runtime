package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

// writeChatConfigWithCapability writes a minimal valid aicli config declaring one
// provider/model whose max_context_tokens is configurable per test step.
func writeChatConfigWithCapability(t *testing.T, path string, maxContextTokens int) {
	t.Helper()
	raw := fmt.Sprintf(`providers:
  default_provider: alpha
  items:
    alpha:
      enabled: true
      protocol: openai
      base_url: https://alpha.example.com
      default_model: alpha-model
      supported_models:
        - alpha-model
      model_capabilities:
        alpha-model:
          max_context_tokens: %d
`, maxContextTokens)
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatalf("write config %s: %v", path, err)
	}
}

// touchChatConfigFuture forces a distinguishable mtime so fingerprint checks do
// not depend on filesystem timestamp granularity.
func touchChatConfigFuture(t *testing.T, path string) {
	t.Helper()
	future := time.Now().Add(3 * time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatalf("chtimes %s: %v", path, err)
	}
}

func TestRefreshChatConfigIfChangedReloadsEditedCapabilityAndResetsStaleWindow(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	writeChatConfigWithCapability(t, cfgPath, 128000)

	cfg, err := agentconfig.InitGlobalConfig(cfgPath)
	if err != nil {
		t.Fatalf("InitGlobalConfig: %v", err)
	}
	session := &ChatSession{
		Config:                  cfg,
		ProviderName:            "alpha",
		Provider:                cfg.Providers.Items["alpha"],
		Model:                   "alpha-model",
		ContextWindowTokenCount: 128000,
	}
	rememberChatConfigFingerprint(session, cfgPath)

	// Unchanged file: no reload, cached window left alone (provider-reported
	// windows must survive ordinary turns).
	changed, err := refreshChatConfigIfChanged(session)
	if err != nil {
		t.Fatalf("refreshChatConfigIfChanged (unchanged): %v", err)
	}
	if changed {
		t.Fatal("unchanged config must not be reloaded")
	}
	if session.ContextWindowTokenCount != 128000 {
		t.Fatalf("unchanged config must keep the cached window, got %d", session.ContextWindowTokenCount)
	}

	// Simulate the reported scenario: max_context_tokens raised from 128K to 1M
	// while the session kept the old persisted window.
	writeChatConfigWithCapability(t, cfgPath, 1000000)
	touchChatConfigFuture(t, cfgPath)

	changed, err = refreshChatConfigIfChanged(session)
	if err != nil {
		t.Fatalf("refreshChatConfigIfChanged (edited): %v", err)
	}
	if !changed {
		t.Fatal("edited config must be reloaded")
	}
	if session.ContextWindowTokenCount != 0 {
		t.Fatalf("stale cached window must be invalidated, got %d", session.ContextWindowTokenCount)
	}
	if got := session.Provider.ModelCapabilities["alpha-model"].MaxContextTokens; got != 1000000 {
		t.Fatalf("reloaded provider capability = %d, want 1000000", got)
	}
	if got := resolveChatStatusContextWindowTokens(session); got != 1000000 {
		t.Fatalf("status window after reload = %d, want 1000000", got)
	}

	// Second call is a no-op again.
	changed, err = refreshChatConfigIfChanged(session)
	if err != nil || changed {
		t.Fatalf("second refresh changed=%v err=%v, want false/nil", changed, err)
	}
}

func TestRefreshChatConfigIfChangedKeepsInMemoryConfigWhenPathMissing(t *testing.T) {
	cfg := &agentconfig.Config{ConfigFilePath: filepath.Join(t.TempDir(), "missing.yaml")}
	session := &ChatSession{Config: cfg}

	changed, err := refreshChatConfigIfChanged(session)
	if err != nil {
		t.Fatalf("refreshChatConfigIfChanged: %v", err)
	}
	if changed {
		t.Fatal("missing config path must not report a reload")
	}
	if session.Config != cfg {
		t.Fatal("missing config path must keep the in-memory config")
	}
}

func TestModelReselectionReconcilesStaleWindowAfterConfigEdit(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	writeChatConfigWithCapability(t, cfgPath, 128000)

	cfg, err := agentconfig.InitGlobalConfig(cfgPath)
	if err != nil {
		t.Fatalf("InitGlobalConfig: %v", err)
	}
	session := &ChatSession{
		Config:                  cfg,
		ProviderName:            "alpha",
		Provider:                cfg.Providers.Items["alpha"],
		Model:                   "alpha-model",
		ContextWindowTokenCount: 128000,
	}

	writeChatConfigWithCapability(t, cfgPath, 1000000)
	touchChatConfigFuture(t, cfgPath)

	if err := reloadChatConfigForModelCommand(session); err != nil {
		t.Fatalf("reloadChatConfigForModelCommand: %v", err)
	}
	// /model 重选同一模型：先按最新配置解析执行上下文，再走落地路径。
	providerCtx, _, err := resolveModelCommandExecutionContext(session, "alpha", "alpha-model")
	if err != nil {
		t.Fatalf("resolveModelCommandExecutionContext: %v", err)
	}
	if err := applyModelCommandSelection(session, providerCtx, "alpha-model", ""); err != nil {
		t.Fatalf("applyModelCommandSelection: %v", err)
	}
	if session.ContextWindowTokenCount != 0 {
		t.Fatalf("re-selecting the same model must drop the stale window, got %d", session.ContextWindowTokenCount)
	}
	if got := resolveChatStatusContextWindowTokens(session); got != 1000000 {
		t.Fatalf("status window after /model re-selection = %d, want 1000000", got)
	}
}

func TestApplyChatExecutionContextInvalidatesWindowOnlyOnTargetChange(t *testing.T) {
	provider := agentconfig.Provider{
		Enabled:      true,
		Protocol:     "openai",
		BaseURL:      "https://alpha.example.com",
		DefaultModel: "alpha-model",
		ModelCapabilities: map[string]agentconfig.ModelCapabilitySpec{
			"alpha-model": {MaxContextTokens: 128000},
			"beta-model":  {MaxContextTokens: 200000},
		},
	}
	session := &ChatSession{
		ProviderName:                    "alpha",
		Provider:                        provider,
		Model:                           "alpha-model",
		ContextWindowTokenCount:         128000,
		providerContextTokenCount:       4096,
		providerContextWindowTokenCount: 128000,
	}

	switched := &providerExecutionContext{
		ProviderName:   "alpha",
		Provider:       provider,
		Model:          "beta-model",
		RequestedModel: "beta-model",
	}
	if err := applyChatExecutionContext(session, switched, ""); err != nil {
		t.Fatalf("applyChatExecutionContext(switch): %v", err)
	}
	if session.ContextWindowTokenCount != 0 ||
		session.providerContextTokenCount != 0 ||
		session.providerContextWindowTokenCount != 0 {
		t.Fatalf("target change must clear the cached window snapshot, got window=%d providerContext=%d providerWindow=%d",
			session.ContextWindowTokenCount, session.providerContextTokenCount, session.providerContextWindowTokenCount)
	}

	// Re-selecting the same provider/model must not wipe a legitimate window
	// value (for example one reported by the provider).
	session.ContextWindowTokenCount = 200000
	same := &providerExecutionContext{
		ProviderName:   "alpha",
		Provider:       provider,
		Model:          "beta-model",
		RequestedModel: "beta-model",
	}
	if err := applyChatExecutionContext(session, same, ""); err != nil {
		t.Fatalf("applyChatExecutionContext(same): %v", err)
	}
	if session.ContextWindowTokenCount != 200000 {
		t.Fatalf("same-target re-selection must keep the cached window, got %d", session.ContextWindowTokenCount)
	}
}

func TestReconcileChatContextWindowWithConfigKeepsWindowWithoutCapability(t *testing.T) {
	session := &ChatSession{
		ProviderName:            "alpha",
		Provider:                agentconfig.Provider{Protocol: "openai"},
		Model:                   "alpha-model",
		ContextWindowTokenCount: 64000,
	}
	if reconcileChatContextWindowWithConfig(session, false) {
		t.Fatal("missing capability declaration must not invalidate the window")
	}
	if session.ContextWindowTokenCount != 64000 {
		t.Fatalf("window without capability = %d, want 64000", session.ContextWindowTokenCount)
	}
}

func TestRefreshChatConfigIfChangedKeepsProfileOverlayOnNewBase(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	writeChatConfigWithCapability(t, cfgPath, 128000)

	cfg, err := agentconfig.InitGlobalConfig(cfgPath)
	if err != nil {
		t.Fatalf("InitGlobalConfig: %v", err)
	}
	session := &ChatSession{
		Config:                  cfg,
		ProviderName:            "alpha",
		Provider:                cfg.Providers.Items["alpha"],
		Model:                   "alpha-model",
		ContextWindowTokenCount: 128000,
	}
	if err := applyProfileConfigOverlay(session, overlayTestOverlay(t, "overlay-provider")); err != nil {
		t.Fatalf("applyProfileConfigOverlay: %v", err)
	}
	rememberChatConfigFingerprint(session, cfgPath)

	writeChatConfigWithCapability(t, cfgPath, 1000000)
	touchChatConfigFuture(t, cfgPath)

	changed, err := refreshChatConfigIfChanged(session)
	if err != nil {
		t.Fatalf("refreshChatConfigIfChanged: %v", err)
	}
	if !changed {
		t.Fatal("edited config must be reloaded")
	}
	if got := session.Config.Providers.DefaultProvider; got != "overlay-provider" {
		t.Fatalf("profile overlay must survive a config reload, default provider = %q", got)
	}
	if session.ProfileConfigBase == nil || session.ProfileConfigBase.Providers.DefaultProvider != "alpha" {
		t.Fatalf("reloaded config must become the new overlay baseline, base = %#v", session.ProfileConfigBase)
	}
	if session.ProfileConfigOverlayApplied != session.Config {
		t.Fatal("overlay view must stay the applied session config")
	}
	if session.ContextWindowTokenCount != 0 {
		t.Fatalf("stale window must be invalidated, got %d", session.ContextWindowTokenCount)
	}
	if got := resolveChatStatusContextWindowTokens(session); got != 1000000 {
		t.Fatalf("status window after reload with overlay = %d, want 1000000", got)
	}
}
