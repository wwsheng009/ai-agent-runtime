package commands

import (
	"fmt"
	"os"
	"reflect"
	"strings"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimellm "github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm/adapter"
	httpclient "github.com/wwsheng009/ai-agent-runtime/internal/pkg/httpclient"
)

// 本文件集中处理「运行中的会话如何感知磁盘配置变化」，以及配置重载后如何让
// 底部状态栏立即反映新的上下文窗口。
//
// 背景：session.ContextWindowTokenCount 会随会话持久化，并且在状态栏解析
// （resolveChatStatusContextWindowTokens）里优先于按配置能力计算的窗口。一旦
// 配置文件里的 max_context_tokens 被调大，旧会话仍会显示持久化的旧窗口，仅靠
// /resume 无法纠正（会话元数据里的旧值会被恢复并继续沿用）。因此这里补齐三件事：
//
//  1. refreshChatConfigIfChanged：turn 入口、/status 等时机按指纹检查磁盘配置，
//     发现变化就按与启动相同的加载顺序（ReloadGlobalConfig 的分层规则）重载；
//  2. reconcileChatSessionAfterConfigReload：重载后把会话视图（provider 能力表、
//     缓存上下文窗口）与新配置对齐，并刷新状态栏；
//  3. applyChatExecutionContext 在 provider/model 实际切换时作废窗口缓存，避免
//     旧目标的窗口泄漏到新目标。

// chatConfigFingerprintForPath 返回配置文件的内容指纹（路径 + mtime + size），
// 用于在不重启进程的前提下判断磁盘配置是否变化。
//
// 分层加载开启（AICLI_CONFIG_MERGE=on）且 path 是候选层之一时，指纹覆盖所有存在
// 的层：只改了低优先级层也必须能触发重载，与 ReloadGlobalConfig 的合并语义一致。
// 其他路径按单文件处理。文件缺失返回 "missing" 而不是错误：内存/测试会话的
// ConfigFilePath 可能只是描述性的，重载函数会保留内存配置。
func chatConfigFingerprintForPath(configPath string) (string, error) {
	trimmed := strings.TrimSpace(configPath)
	if trimmed == "" {
		return "", nil
	}
	if config.MergeModeFromEnv() == config.MergeModeOn && chatConfigPathIsLayer(trimmed) {
		parts := make([]string, 0, 4)
		for _, layer := range config.ConfigLayerStack() {
			if !layer.Present {
				continue
			}
			part, err := chatConfigFileFingerprint(layer.Path)
			if err != nil {
				return "", err
			}
			parts = append(parts, part)
		}
		if len(parts) == 0 {
			return "layers:missing", nil
		}
		return "layers:" + strings.Join(parts, ";"), nil
	}
	return chatConfigFileFingerprint(trimmed)
}

func chatConfigFileFingerprint(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return normalizeChatPathForCompare(path) + "|missing", nil
		}
		return "", err
	}
	return fmt.Sprintf("%s|%d|%d", normalizeChatPathForCompare(path), info.ModTime().UnixNano(), info.Size()), nil
}

// chatConfigPathIsLayer reports whether path denotes one of the bootstrap config
// candidates, mirroring agentconfig.isConfigLayerPath from the commands side.
func chatConfigPathIsLayer(path string) bool {
	target := normalizeChatPathForCompare(path)
	if target == "" {
		return false
	}
	for _, layer := range config.ConfigLayerStack() {
		if candidate := normalizeChatPathForCompare(layer.Path); candidate != "" && candidate == target {
			return true
		}
	}
	return false
}

// rememberChatConfigFingerprint records the fingerprint of the config source that
// session.Config was just loaded from. Call it after every session.Config (re)load
// so a later refresh only reacts to real on-disk changes.
func rememberChatConfigFingerprint(session *ChatSession, configPath string) {
	if session == nil {
		return
	}
	fingerprint, err := chatConfigFingerprintForPath(configPath)
	if err != nil || fingerprint == "" {
		return
	}
	session.configFingerprint = fingerprint
}

// refreshChatConfigIfChanged reloads session.Config from disk when the bound
// config file (all layers when merge mode is on) changed since it was last
// loaded, then reconciles the session view with the new config.
//
// It returns true when a reload happened. A missing file or an in-memory/test
// sentinel path is a no-op: reloadChatConfigForModelCommand keeps the existing
// in-memory config in that case, matching /model behaviour.
func refreshChatConfigIfChanged(session *ChatSession) (bool, error) {
	if session == nil || session.Config == nil {
		return false, nil
	}
	configPath := strings.TrimSpace(session.Config.ConfigFilePath)
	if configPath == "" {
		return false, nil
	}
	fingerprint, err := chatConfigFingerprintForPath(configPath)
	if err != nil {
		return false, fmt.Errorf("检查本地配置文件 %s 失败: %w", configPath, err)
	}
	if fingerprint == "" {
		return false, nil
	}
	// An empty stored fingerprint means the session never recorded a baseline
	// (older sessions / directly constructed test sessions). Reload once so the
	// baseline is trustworthy instead of silently marking a changed file as seen.
	if session.configFingerprint != "" && fingerprint == session.configFingerprint {
		return false, nil
	}
	previousConfig := session.Config
	if err := reloadChatConfigForModelCommand(session); err != nil {
		return false, err
	}
	rememberChatConfigFingerprint(session, configPath)
	// A missing/sentinel path keeps the in-memory config by contract; report no
	// reload so callers never announce a change that did not happen.
	if session.Config == previousConfig {
		return false, nil
	}
	// Config 已被整体替换：把 provider 能力表与缓存窗口同新配置对账，并在
	// 窗口变化时立即刷新底部状态栏。
	reconcileChatSessionAfterConfigReload(session)
	return true, nil
}

// reconcileChatSessionAfterConfigReload aligns the in-memory session view with a
// freshly loaded config: refresh the provider entry (capability table, base URL,
// HTTP client) for the still-active provider/model, then drop a stale cached
// context window. Returns true when anything changed.
func reconcileChatSessionAfterConfigReload(session *ChatSession) bool {
	if session == nil {
		return false
	}
	changed := reclaimProfileConfigOverlayAfterReload(session)
	if applyReloadedProviderConfig(session) {
		changed = true
	}
	if reconcileChatContextWindowWithConfig(session, false) {
		changed = true
	}
	if changed && session.Interaction != nil {
		session.Interaction.RefreshStatus("")
	}
	return changed
}

// reclaimProfileConfigOverlayAfterReload rebuilds the bound profile overlay on
// top of the freshly loaded config. Replacing session.Config wholesale (what both
// /model reload and the turn-start freshness check do) would otherwise silently
// drop runtime.overrides until the next profile re-apply. Without an active
// overlay, a retained baseline pointer is re-pointed at the new config so a later
// profile switch cannot merge onto a stale base.
func reclaimProfileConfigOverlayAfterReload(session *ChatSession) bool {
	if session == nil {
		return false
	}
	overlay := session.ProfileConfigOverlay
	if overlay == nil || !overlay.Active() {
		if session.ProfileConfigBase != nil {
			session.ProfileConfigBase = session.Config
			return true
		}
		return false
	}
	session.ProfileConfigBase = session.Config
	session.ProfileConfigOverlayApplied = nil
	if err := applyProfileConfigOverlay(session, overlay); err != nil {
		// The overlay failed to replay on the new base: keep the base config and
		// drop the overlay references so the session never claims overrides that
		// are not actually applied.
		session.ProfileConfigOverlay = nil
		session.ProfileConfigOverlayApplied = nil
		emitProfileConfigOverlayWarning(err)
		return true
	}
	return true
}

// applyReloadedProviderConfig re-resolves the session's current provider from the
// reloaded config and updates the fields the next turn and the status bar read
// from (ModelCapabilities in particular). The model selection itself is kept: a
// config reload must not silently switch the user's model. A provider that is no
// longer present keeps the previous in-memory view; /model and /provider still
// report the explicit error at switch time.
func applyReloadedProviderConfig(session *ChatSession) bool {
	if session == nil || session.Config == nil {
		return false
	}
	providerName := strings.TrimSpace(session.ProviderName)
	if providerName == "" {
		return false
	}
	canonical, provider, ok := findProviderByName(session.Config, providerName)
	if !ok {
		return false
	}
	if canonical == strings.TrimSpace(session.ProviderName) && reflect.DeepEqual(session.Provider, provider) {
		return false
	}
	session.ProviderName = canonical
	session.Provider = provider
	updateChatAccountBalanceProvider(session, canonical, provider)
	session.Adapter = adapter.GetAdapterOrDefault(provider.GetProtocol())
	apiPath := ""
	if session.Adapter != nil {
		apiPath = session.Adapter.GetAPIPath()
	}
	session.BaseURL = buildProviderURL(provider, apiPath, session.Model)
	session.HTTPClient = httpclient.GetHTTPClientWithProvider(session.Config, &session.Provider)
	session.EffectiveProvider = canonical
	syncChatLoggerModelState(session)
	return true
}

// reconcileChatContextWindowWithConfig drops the cached context window when the
// current config declares an explicit capability that differs from it. Only an
// explicit capability (max_context_tokens > 0) can invalidate the cache: without
// one the previous behaviour is kept so an unknown window source is never
// misclassified as stale.
//
// refreshStatus lets callers defer the status-bar redraw (for example while the
// interaction coordinator is not ready yet during session restore).
func reconcileChatContextWindowWithConfig(session *ChatSession, refreshStatus bool) bool {
	if session == nil {
		return false
	}
	capability := resolveChatConfiguredMaxContextTokens(session)
	if capability <= 0 || session.ContextWindowTokenCount <= 0 || session.ContextWindowTokenCount == capability {
		return false
	}
	session.ContextWindowTokenCount = 0
	session.providerContextTokenCount = 0
	session.providerContextWindowTokenCount = 0
	if refreshStatus && session.Interaction != nil {
		session.Interaction.RefreshStatus("")
	}
	return true
}

// resolveChatConfiguredMaxContextTokens returns the max_context_tokens declared
// for the session's current model, or 0 when the provider config declares none.
func resolveChatConfiguredMaxContextTokens(session *ChatSession) int {
	if session == nil {
		return 0
	}
	capability, ok := runtimellm.ResolveModelCapabilitySpec(strings.TrimSpace(session.Model), session.Provider.ModelCapabilities)
	if !ok {
		return 0
	}
	return capability.MaxContextTokens
}

// warnChatConfigRefreshFailure reports a config freshness check failure without
// failing the turn: a broken edit must not take down a running session. The next
// boundary retries; /model and /provider still surface parse errors explicitly.
func warnChatConfigRefreshFailure(session *ChatSession, err error) {
	if err == nil {
		return
	}
	writeSessionDebugInfo(session, fmt.Sprintf("[config] reload skipped: %v", err), true)
}
