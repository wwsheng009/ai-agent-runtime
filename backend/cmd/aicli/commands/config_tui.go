package commands

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

type configTUI struct {
	reader *bufio.Reader
	writer io.Writer
	cfg    *config.Config
}

func runConfigTUI(reader io.Reader, writer io.Writer, cfg *config.Config) error {
	if reader == nil {
		reader = os.Stdin
	}
	if writer == nil {
		writer = os.Stdout
	}
	if cfg == nil {
		return fmt.Errorf("config is not loaded")
	}
	if strings.TrimSpace(cfg.ConfigFilePath) == "" {
		return fmt.Errorf("config file path is not available")
	}
	tui := &configTUI{
		reader: bufio.NewReader(reader),
		writer: writer,
		cfg:    cfg,
	}
	return tui.run()
}

func (t *configTUI) run() error {
	for {
		t.printHeader("AICLI Config TUI")
		fmt.Fprintf(t.writer, "Config: %s\n\n", emptyIfBlank(t.cfg.ConfigFilePath))
		fmt.Fprintln(t.writer, "  1. 概览")
		fmt.Fprintln(t.writer, "  2. Provider 管理")
		fmt.Fprintln(t.writer, "  3. AICLI Chat 偏好")
		fmt.Fprintln(t.writer, "  4. Provider Groups")
		fmt.Fprintln(t.writer, "  q. 退出")
		input, err := t.prompt("config> ")
		if err != nil {
			return err
		}
		switch strings.ToLower(input) {
		case "1", "overview", "o":
			t.renderOverview()
			t.pause()
		case "2", "providers", "provider", "p":
			if err := t.runProviders(); err != nil {
				return err
			}
		case "3", "chat", "c":
			if err := t.runChatPreferences(); err != nil {
				return err
			}
		case "4", "groups", "g":
			t.renderProviderGroups()
			t.pause()
		case "q", "quit", "exit":
			fmt.Fprintln(t.writer, "已退出配置管理。")
			return nil
		default:
			t.notice("未知选项: " + input)
		}
	}
}

func (t *configTUI) runProviders() error {
	for {
		providers := config.ListProviderSummaries(t.cfg, config.ProviderListFilter{})
		t.printHeader("Provider 管理")
		t.renderProviderList(providers)
		fmt.Fprintln(t.writer)
		fmt.Fprintln(t.writer, "输入编号/名称查看详情；a 登录/新增 provider；r 多选删除；b 返回。")
		input, err := t.prompt("providers> ")
		if err != nil {
			return err
		}
		switch strings.ToLower(input) {
		case "b", "back":
			return nil
		case "a", "add", "login", "new":
			if err := t.loginProvider(); err != nil {
				t.notice(err.Error())
			}
		case "r", "remove", "delete":
			if err := t.removeProvidersInteractive(providers); err != nil {
				t.notice(err.Error())
			}
		default:
			name, ok := providerNameFromSelection(input, providers)
			if !ok {
				t.notice("找不到 provider: " + input)
				continue
			}
			if err := t.runProviderDetail(name); err != nil {
				return err
			}
		}
	}
}

func (t *configTUI) runProviderDetail(name string) error {
	for {
		provider, ok := t.cfg.Providers.Items[name]
		if !ok {
			t.notice("provider 已不存在: " + name)
			return nil
		}
		t.printHeader("Provider: " + name)
		t.renderProviderDetail(name, provider)
		fmt.Fprintln(t.writer)
		fmt.Fprintln(t.writer, "  e. 启用/禁用")
		fmt.Fprintln(t.writer, "  d. 设为默认 provider")
		fmt.Fprintln(t.writer, "  m. 修改常用字段")
		fmt.Fprintln(t.writer, "  a. 高级编辑")
		fmt.Fprintln(t.writer, "  x. 删除该 provider")
		fmt.Fprintln(t.writer, "  b. 返回")
		input, err := t.prompt("provider> ")
		if err != nil {
			return err
		}
		switch strings.ToLower(input) {
		case "b", "back":
			return nil
		case "e", "enable", "toggle":
			result, err := config.SetProvidersEnabledConfig(t.cfg.ConfigFilePath, []string{name}, !provider.Enabled)
			if err != nil {
				t.notice(err.Error())
				continue
			}
			t.notice(fmt.Sprintf("已更新: %s", strings.Join(result.Updated, ", ")))
			if err := t.reload(); err != nil {
				return err
			}
		case "d", "default":
			result, err := config.SetDefaultProviderConfig(t.cfg.ConfigFilePath, name)
			if err != nil {
				t.notice(err.Error())
				continue
			}
			t.notice("默认 provider: " + result.DefaultProvider)
			if err := t.reload(); err != nil {
				return err
			}
		case "m", "modify", "edit":
			if err := t.editProvider(name); err != nil {
				t.notice(err.Error())
				continue
			}
		case "a", "advanced":
			if err := t.advancedEditProvider(name); err != nil {
				t.notice(err.Error())
				continue
			}
		case "x", "delete", "remove":
			if err := t.removeSingleProvider(name); err != nil {
				t.notice(err.Error())
				continue
			}
			return nil
		default:
			t.notice("未知选项: " + input)
		}
	}
}

func (t *configTUI) editProvider(name string) error {
	if strings.TrimSpace(name) == "" {
		input, err := t.prompt("Provider 名称: ")
		if err != nil {
			return err
		}
		name = strings.TrimSpace(input)
	}
	if name == "" {
		return fmt.Errorf("provider 名称不能为空")
	}
	current := t.cfg.Providers.Items[name]
	update := config.ProviderConfigUpdate{Name: name}
	changed := false
	if value, authMode, ok, err := t.promptProviderProtocolUpdate(current.GetProtocol(), current.AuthMode); err != nil {
		return err
	} else if ok {
		update.Protocol = &value
		update.AuthMode = &authMode
		changed = true
	}
	if value, ok, err := t.promptStringUpdate("Base URL", current.BaseURL); err != nil {
		return err
	} else if ok {
		update.BaseURL = &value
		changed = true
	}
	if value, ok, err := t.promptStringUpdate("API Path", current.APIPath); err != nil {
		return err
	} else if ok {
		update.APIPath = &value
		changed = true
	}
	if value, ok, err := t.promptStringUpdate("Models Path", current.ModelsPath); err != nil {
		return err
	} else if ok {
		update.ModelsPath = &value
		changed = true
	}
	if value, ok, err := t.promptStringUpdate("Default Model", current.DefaultModel); err != nil {
		return err
	} else if ok {
		update.DefaultModel = &value
		changed = true
	}
	if value, ok, err := t.promptStringUpdate("API key ref", current.APIKeyRef); err != nil {
		return err
	} else if ok {
		update.APIKeyRef = &value
		changed = true
	}
	if value, ok, err := t.promptIntUpdate("Max tokens limit", current.GetMaxTokensLimit()); err != nil {
		return err
	} else if ok {
		update.MaxTokensLimit = &value
		changed = true
	}
	if value, ok, err := t.promptBoolUpdate("Enabled", current.Enabled); err != nil {
		return err
	} else if ok {
		update.Enabled = &value
		changed = true
	}
	if value, ok, err := t.promptBoolUpdate("Set as default provider", strings.EqualFold(t.cfg.Providers.DefaultProvider, name)); err != nil {
		return err
	} else if ok {
		update.SetDefaultProvider = value
		changed = true
	}
	if !changed {
		t.notice("未修改 provider: " + name)
		return nil
	}
	if _, err := config.UpdateProviderConfig(t.cfg.ConfigFilePath, update); err != nil {
		return err
	}
	t.notice("已保存 provider: " + name)
	return t.reload()
}

func (t *configTUI) loginProvider() error {
	setDefaultDefault := len(t.cfg.Providers.Items) == 0 || strings.TrimSpace(t.cfg.Providers.DefaultProvider) == ""
	setDefault, err := t.promptBoolDefault("设为默认 provider", setDefaultDefault)
	if err != nil {
		return err
	}
	result, err := runProviderLogin(providerLoginRequest{
		Context:     context.Background(),
		Config:      t.cfg,
		ConfigPath:  t.cfg.ConfigFilePath,
		SetDefault:  setDefault,
		Interactive: true,
		Prompter:    configTUILoginPrompter{tui: t},
	})
	if err != nil {
		return err
	}
	t.notice(fmt.Sprintf("已保存 provider: %s protocol=%s model=%s", result.ProviderName, result.Protocol, result.DefaultModel))
	return t.reload()
}

type configTUILoginPrompter struct {
	tui *configTUI
}

func (p configTUILoginPrompter) PromptText(label, current string, required bool) (string, error) {
	for {
		suffix := ""
		if strings.TrimSpace(current) != "" {
			suffix = fmt.Sprintf(" [%s]", current)
		}
		value, err := p.tui.prompt(label + suffix + ": ")
		if err != nil {
			return "", err
		}
		if value == "" && strings.TrimSpace(current) != "" {
			return strings.TrimSpace(current), nil
		}
		if !required || strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value), nil
		}
		p.PrintLine(label + " 不能为空")
	}
}

func (p configTUILoginPrompter) PromptSecret(label, currentMasked string, required bool) (string, error) {
	for {
		suffix := ""
		if strings.TrimSpace(currentMasked) != "" {
			suffix = fmt.Sprintf(" [%s]", currentMasked)
		}
		value, err := p.tui.prompt(label + suffix + ": ")
		if err != nil {
			return "", err
		}
		if !required || strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value), nil
		}
		p.PrintLine(label + " 不能为空")
	}
}

func (p configTUILoginPrompter) PrintLine(line string) {
	fmt.Fprintln(p.tui.writer, line)
}

func (t *configTUI) advancedEditProvider(name string) error {
	for {
		provider, ok := t.cfg.Providers.Items[name]
		if !ok {
			return fmt.Errorf("provider 已不存在: %s", name)
		}
		t.printHeader("高级编辑: " + name)
		fmt.Fprintf(t.writer, "  1. protocol         %s\n", emptyIfBlank(provider.GetProtocol()))
		fmt.Fprintf(t.writer, "  2. base_url         %s\n", emptyIfBlank(provider.BaseURL))
		fmt.Fprintf(t.writer, "  3. api_path         %s\n", emptyIfBlank(provider.APIPath))
		fmt.Fprintf(t.writer, "  4. forward_url      %s\n", emptyIfBlank(provider.ForwardURL))
		fmt.Fprintf(t.writer, "  5. models_path      %s\n", emptyIfBlank(provider.ModelsPath))
		fmt.Fprintf(t.writer, "  6. default_model    %s\n", emptyIfBlank(provider.DefaultModel))
		fmt.Fprintf(t.writer, "  7. api_key_ref      %s\n", emptyIfBlank(provider.APIKeyRef))
		fmt.Fprintf(t.writer, "  8. auth_mode        %s\n", emptyIfBlank(provider.AuthMode))
		fmt.Fprintf(t.writer, "  9. auth_ref         %s\n", emptyIfBlank(provider.AuthRef))
		fmt.Fprintf(t.writer, "  10. max_tokens_limit %d\n", provider.GetMaxTokensLimit())
		fmt.Fprintf(t.writer, "  11. enabled         %t\n", provider.Enabled)
		fmt.Fprintln(t.writer, "  12. 设为默认 provider")
		fmt.Fprintln(t.writer, "  b. 返回")
		input, err := t.prompt("advanced> ")
		if err != nil {
			return err
		}
		if strings.EqualFold(input, "b") || strings.EqualFold(input, "back") {
			return nil
		}
		update := config.ProviderConfigUpdate{Name: name}
		changed := true
		switch strings.ToLower(input) {
		case "1", "protocol":
			value, authMode, ok, err := t.promptProviderProtocolUpdate(provider.GetProtocol(), provider.AuthMode)
			if err != nil {
				return err
			}
			if ok {
				update.Protocol = &value
				update.AuthMode = &authMode
			} else {
				changed = false
			}
		case "2", "base_url", "base-url":
			value, ok, err := t.promptStringUpdate("Base URL", provider.BaseURL)
			if err != nil {
				return err
			}
			if ok {
				update.BaseURL = &value
			} else {
				changed = false
			}
		case "3", "api_path", "api-path":
			value, ok, err := t.promptStringUpdate("API Path", provider.APIPath)
			if err != nil {
				return err
			}
			if ok {
				update.APIPath = &value
			} else {
				changed = false
			}
		case "4", "forward_url", "forward-url":
			value, ok, err := t.promptStringUpdate("Forward URL", provider.ForwardURL)
			if err != nil {
				return err
			}
			if ok {
				update.ForwardURL = &value
			} else {
				changed = false
			}
		case "5", "models_path", "models-path":
			value, ok, err := t.promptStringUpdate("Models Path", provider.ModelsPath)
			if err != nil {
				return err
			}
			if ok {
				update.ModelsPath = &value
			} else {
				changed = false
			}
		case "6", "default_model", "default-model":
			value, ok, err := t.promptStringUpdate("Default Model", provider.DefaultModel)
			if err != nil {
				return err
			}
			if ok {
				update.DefaultModel = &value
			} else {
				changed = false
			}
		case "7", "api_key_ref", "api-key-ref":
			value, ok, err := t.promptStringUpdate("API key ref", provider.APIKeyRef)
			if err != nil {
				return err
			}
			if ok {
				update.APIKeyRef = &value
			} else {
				changed = false
			}
		case "8", "auth_mode", "auth-mode":
			value, ok, err := t.promptAuthModeUpdate(provider.AuthMode)
			if err != nil {
				return err
			}
			if ok {
				update.AuthMode = &value
			} else {
				changed = false
			}
		case "9", "auth_ref", "auth-ref":
			value, ok, err := t.promptStringUpdate("Auth ref", provider.AuthRef)
			if err != nil {
				return err
			}
			if ok {
				update.AuthRef = &value
			} else {
				changed = false
			}
		case "10", "max_tokens_limit", "max-tokens-limit":
			value, ok, err := t.promptIntUpdate("Max tokens limit", provider.GetMaxTokensLimit())
			if err != nil {
				return err
			}
			if ok {
				update.MaxTokensLimit = &value
			} else {
				changed = false
			}
		case "11", "enabled":
			value, ok, err := t.promptBoolUpdate("Enabled", provider.Enabled)
			if err != nil {
				return err
			}
			if ok {
				update.Enabled = &value
			} else {
				changed = false
			}
		case "12", "default":
			update.SetDefaultProvider = true
		default:
			t.notice("未知选项: " + input)
			changed = false
		}
		if !changed {
			continue
		}
		if _, err := config.UpdateProviderConfig(t.cfg.ConfigFilePath, update); err != nil {
			return err
		}
		t.notice("已保存: " + name)
		if err := t.reload(); err != nil {
			return err
		}
	}
}

func (t *configTUI) removeProvidersInteractive(providers []config.ProviderSummary) error {
	selected, err := promptProviderRemoveSelection(t.reader, t.writer, providers)
	if err != nil {
		return err
	}
	if len(selected) == 0 {
		return fmt.Errorf("未选择要删除的 provider")
	}
	confirmed, err := confirmProviderRemoveSelection(t.reader, t.writer, selected)
	if err != nil {
		return err
	}
	if !confirmed {
		return fmt.Errorf("已取消删除 provider")
	}
	result, err := runProviderRemoveCommand(t.cfg, config.ProviderDeleteRequest{
		Names:        selected,
		Cascade:      true,
		ClearDefault: true,
		PruneAuth:    true,
	})
	if err != nil {
		return err
	}
	t.notice("已删除: " + strings.Join(result.Deleted, ", "))
	return t.reload()
}

func (t *configTUI) removeSingleProvider(name string) error {
	confirmed, err := confirmProviderRemoveSelection(t.reader, t.writer, []string{name})
	if err != nil {
		return err
	}
	if !confirmed {
		return fmt.Errorf("已取消删除 provider")
	}
	result, err := runProviderRemoveCommand(t.cfg, config.ProviderDeleteRequest{
		Names:        []string{name},
		Cascade:      true,
		ClearDefault: true,
		PruneAuth:    true,
	})
	if err != nil {
		return err
	}
	t.notice("已删除: " + strings.Join(result.Deleted, ", "))
	return t.reload()
}

func (t *configTUI) runChatPreferences() error {
	for {
		t.printHeader("AICLI Chat 偏好")
		chat := &config.AICLIChatConfig{}
		if t.cfg.AICLI != nil && t.cfg.AICLI.Chat != nil {
			*chat = *t.cfg.AICLI.Chat
		}
		fmt.Fprintf(t.writer, "  Default provider: %s\n", emptyIfBlank(chat.DefaultProvider))
		fmt.Fprintf(t.writer, "  Default model:    %s\n", emptyIfBlank(chat.DefaultModel))
		fmt.Fprintf(t.writer, "  Reasoning effort: %s\n", emptyIfBlank(chat.ReasoningEffort))
		fmt.Fprintf(t.writer, "  Stream:           %s\n", streamPreferenceText(chat.Stream))
		fmt.Fprintln(t.writer)
		fmt.Fprintln(t.writer, "  p. 设置默认 provider")
		fmt.Fprintln(t.writer, "  m. 设置默认 model")
		fmt.Fprintln(t.writer, "  r. 设置 reasoning effort")
		fmt.Fprintln(t.writer, "  s. 设置 stream")
		fmt.Fprintln(t.writer, "  b. 返回")
		input, err := t.prompt("chat> ")
		if err != nil {
			return err
		}
		update := config.AICLIChatPreferenceUpdate{}
		changed := true
		switch strings.ToLower(input) {
		case "b", "back":
			return nil
		case "p", "provider":
			value, err := t.promptProviderName("默认 provider")
			if err != nil {
				return err
			}
			update.DefaultProvider = &value
		case "m", "model":
			value, err := t.prompt("默认 model（留空清除）: ")
			if err != nil {
				return err
			}
			update.DefaultModel = &value
		case "r", "reasoning":
			value, err := t.prompt("Reasoning effort（low|medium|high|xhigh，留空清除）: ")
			if err != nil {
				return err
			}
			update.ReasoningEffort = &value
		case "s", "stream":
			stream, err := t.promptOptionalBool("Stream（true/false/clear）")
			if err != nil {
				return err
			}
			update.Stream = &stream
		default:
			t.notice("未知选项: " + input)
			changed = false
		}
		if !changed {
			continue
		}
		if _, err := config.UpdateAICLIChatPreferences(t.cfg.ConfigFilePath, update); err != nil {
			t.notice(err.Error())
			continue
		}
		t.notice("已保存 AICLI Chat 偏好")
		if err := t.reload(); err != nil {
			return err
		}
	}
}

func (t *configTUI) renderOverview() {
	t.printHeader("配置概览")
	fmt.Fprintf(t.writer, "Config file:      %s\n", emptyIfBlank(t.cfg.ConfigFilePath))
	fmt.Fprintf(t.writer, "Server:           %s %s:%d\n", emptyIfBlank(t.cfg.Server.Name), emptyIfBlank(t.cfg.Server.Host), t.cfg.Server.Port)
	fmt.Fprintf(t.writer, "Providers:        %d\n", len(t.cfg.Providers.Items))
	fmt.Fprintf(t.writer, "Default provider: %s\n", emptyIfBlank(t.cfg.Providers.DefaultProvider))
	fmt.Fprintf(t.writer, "Provider groups:  %d\n", len(t.cfg.ProviderGroups))
	if t.cfg.AICLI != nil && t.cfg.AICLI.Theme != nil {
		fmt.Fprintf(t.writer, "Theme:            %s\n", emptyIfBlank(t.cfg.AICLI.Theme.Name))
	}
	if t.cfg.AICLI != nil && t.cfg.AICLI.Runtime != nil {
		fmt.Fprintf(t.writer, "Runtime mode:     %s\n", emptyIfBlank(t.cfg.AICLI.Runtime.Mode))
		fmt.Fprintf(t.writer, "Runtime server:   %s\n", emptyIfBlank(t.cfg.AICLI.Runtime.ServerURL))
	}
}

func (t *configTUI) renderProviderGroups() {
	t.printHeader("Provider Groups")
	if len(t.cfg.ProviderGroups) == 0 {
		fmt.Fprintln(t.writer, "No provider groups configured")
		return
	}
	for _, group := range t.cfg.ProviderGroups {
		fmt.Fprintf(t.writer, "Group: %s strategy=%s providers=%d\n", group.Name, emptyIfBlank(group.Strategy), len(group.Providers))
		for _, provider := range group.Providers {
			fmt.Fprintf(t.writer, "  - %s weight=%d role=%s enabled=%t\n", provider.Name, provider.Weight, emptyIfBlank(provider.Role), provider.Enabled)
		}
	}
}

func (t *configTUI) renderProviderList(providers []config.ProviderSummary) {
	if len(providers) == 0 {
		fmt.Fprintln(t.writer, "No providers configured")
		return
	}
	fmt.Fprintf(t.writer, "%-4s %-22s %-8s %-8s %-14s %s\n", "#", "NAME", "ENABLED", "DEFAULT", "PROTOCOL", "BASE_URL")
	for i, provider := range providers {
		fmt.Fprintf(t.writer, "%-4d %-22s %-8t %-8t %-14s %s\n", i+1, provider.Name, provider.Enabled, provider.Default, emptyIfBlank(provider.Protocol), emptyIfBlank(provider.BaseURL))
	}
}

func (t *configTUI) renderProviderDetail(name string, provider config.Provider) {
	fmt.Fprintf(t.writer, "Enabled:              %t\n", provider.Enabled)
	fmt.Fprintf(t.writer, "Default:              %t\n", strings.EqualFold(t.cfg.Providers.DefaultProvider, name))
	fmt.Fprintf(t.writer, "Protocol:             %s\n", emptyIfBlank(provider.GetProtocol()))
	fmt.Fprintf(t.writer, "Base URL:             %s\n", emptyIfBlank(provider.BaseURL))
	fmt.Fprintf(t.writer, "API Path:             %s\n", emptyIfBlank(provider.APIPath))
	fmt.Fprintf(t.writer, "Forward URL:          %s\n", emptyIfBlank(provider.ForwardURL))
	fmt.Fprintf(t.writer, "Models Path:          %s\n", emptyIfBlank(provider.ModelsPath))
	fmt.Fprintf(t.writer, "Default Model:        %s\n", emptyIfBlank(provider.DefaultModel))
	fmt.Fprintf(t.writer, "API key ref:          %s\n", emptyIfBlank(provider.APIKeyRef))
	fmt.Fprintf(t.writer, "Auth mode/ref:        %s / %s\n", emptyIfBlank(provider.AuthMode), emptyIfBlank(provider.AuthRef))
	fmt.Fprintf(t.writer, "Supported models:     %d\n", len(provider.SupportedModels))
	fmt.Fprintf(t.writer, "Model mappings:       %d\n", len(provider.ModelMappings))
	fmt.Fprintf(t.writer, "Model capabilities:   %d\n", len(provider.ModelCapabilities))
	fmt.Fprintf(t.writer, "Max tokens limit:     %d\n", provider.GetMaxTokensLimit())
}

func (t *configTUI) reload() error {
	loaded, err := config.InitGlobalConfig(t.cfg.ConfigFilePath)
	if err != nil {
		return err
	}
	t.cfg = loaded
	return nil
}

func (t *configTUI) prompt(label string) (string, error) {
	fmt.Fprint(t.writer, label)
	line, err := t.reader.ReadString('\n')
	if err != nil {
		if err == io.EOF && strings.TrimSpace(line) != "" {
			return strings.TrimSpace(line), nil
		}
		return "", err
	}
	return strings.TrimSpace(line), nil
}

func (t *configTUI) promptStringUpdate(label, current string) (string, bool, error) {
	value, err := t.prompt(fmt.Sprintf("%s [%s]（回车保留，clear 清除）: ", label, emptyIfBlank(current)))
	if err != nil {
		return "", false, err
	}
	if value == "" {
		return "", false, nil
	}
	if strings.EqualFold(value, "clear") {
		return "", true, nil
	}
	return value, true, nil
}

func (t *configTUI) promptProviderProtocolUpdate(currentProtocol, currentAuthMode string) (string, string, bool, error) {
	options := loginProtocolOptions()
	currentLoginProtocol := loginProtocolFromProvider(config.Provider{
		Protocol: currentProtocol,
		AuthMode: currentAuthMode,
	}, currentAuthMode)
	fmt.Fprintln(t.writer, "请选择登录协议:")
	for i, option := range options {
		currentMarker := ""
		if strings.EqualFold(option, currentLoginProtocol) {
			currentMarker = " *"
		}
		fmt.Fprintf(t.writer, "  [%d] %s%s\n", i+1, option, currentMarker)
	}
	for {
		value, err := t.prompt(fmt.Sprintf("协议编号或名称 [%s]（回车保留）: ", emptyIfBlank(currentLoginProtocol)))
		if err != nil {
			return "", "", false, err
		}
		if value == "" {
			return "", "", false, nil
		}
		for i, option := range options {
			if value == fmt.Sprintf("%d", i+1) || strings.EqualFold(value, option) {
				runtimeProtocol := runtimeProtocolForLoginProtocol(option)
				authMode := authModeForLoginProtocol(option)
				return runtimeProtocol, authMode, true, nil
			}
		}
		fmt.Fprintln(t.writer, "无效协议，请重新输入")
	}
}

func authModeForLoginProtocol(protocol string) string {
	if normalizeLoginProtocol(protocol, "") == "codex-oauth" {
		return providerAuthModeOAuth
	}
	return providerAuthModeAPIKey
}

func (t *configTUI) promptBoolDefault(label string, current bool) (bool, error) {
	for {
		value, err := t.prompt(fmt.Sprintf("%s [%t]: ", label, current))
		if err != nil {
			return false, err
		}
		if value == "" {
			return current, nil
		}
		parsed, ok := parseConfigTUIBool(value)
		if ok {
			return parsed, nil
		}
		fmt.Fprintf(t.writer, "%s 必须是 true 或 false\n", label)
	}
}

func (t *configTUI) promptAuthModeUpdate(current string) (string, bool, error) {
	options := []string{providerAuthModeAPIKey, providerAuthModeOAuth}
	current = normalizeProviderAuthMode(current)
	fmt.Fprintln(t.writer, "请选择认证模式:")
	for i, option := range options {
		currentMarker := ""
		if strings.EqualFold(option, current) {
			currentMarker = " *"
		}
		fmt.Fprintf(t.writer, "  [%d] %s%s\n", i+1, option, currentMarker)
	}
	for {
		value, err := t.prompt(fmt.Sprintf("认证模式编号或名称 [%s]（回车保留，clear 清除）: ", emptyIfBlank(current)))
		if err != nil {
			return "", false, err
		}
		if value == "" {
			return "", false, nil
		}
		if strings.EqualFold(value, "clear") {
			return "", true, nil
		}
		for i, option := range options {
			if value == fmt.Sprintf("%d", i+1) || strings.EqualFold(value, option) {
				return option, true, nil
			}
		}
		fmt.Fprintln(t.writer, "无效认证模式，请重新输入")
	}
}

func (t *configTUI) promptIntUpdate(label string, current int) (int, bool, error) {
	value, err := t.prompt(fmt.Sprintf("%s [%d]（回车保留，0 清除）: ", label, current))
	if err != nil {
		return 0, false, err
	}
	if value == "" {
		return 0, false, nil
	}
	number, err := strconv.Atoi(value)
	if err != nil || number < 0 {
		return 0, false, fmt.Errorf("%s 必须是非负整数", label)
	}
	return number, true, nil
}

func (t *configTUI) promptBoolUpdate(label string, current bool) (bool, bool, error) {
	value, err := t.prompt(fmt.Sprintf("%s [%t]（true/false，回车保留）: ", label, current))
	if err != nil {
		return false, false, err
	}
	if value == "" {
		return false, false, nil
	}
	parsed, ok := parseConfigTUIBool(value)
	if !ok {
		return false, false, fmt.Errorf("%s 必须是 true 或 false", label)
	}
	return parsed, true, nil
}

func (t *configTUI) promptOptionalBool(label string) (*bool, error) {
	value, err := t.prompt(label + ": ")
	if err != nil {
		return nil, err
	}
	if strings.EqualFold(value, "clear") || value == "" {
		return nil, nil
	}
	parsed, ok := parseConfigTUIBool(value)
	if !ok {
		return nil, fmt.Errorf("%s 必须是 true、false 或 clear", label)
	}
	return &parsed, nil
}

func (t *configTUI) promptProviderName(label string) (string, error) {
	providers := config.ListProviderSummaries(t.cfg, config.ProviderListFilter{})
	t.renderProviderList(providers)
	value, err := t.prompt(label + "（编号/名称，留空清除）: ")
	if err != nil {
		return "", err
	}
	if value == "" {
		return "", nil
	}
	if name, ok := providerNameFromSelection(value, providers); ok {
		return name, nil
	}
	return value, nil
}

func (t *configTUI) pause() {
	_, _ = t.prompt("\n按 Enter 返回...")
}

func (t *configTUI) notice(message string) {
	if strings.TrimSpace(message) == "" {
		return
	}
	fmt.Fprintf(t.writer, "\n%s\n\n", message)
}

func (t *configTUI) printHeader(title string) {
	fmt.Fprintln(t.writer)
	fmt.Fprintln(t.writer, strings.Repeat("=", 72))
	fmt.Fprintf(t.writer, "%s\n", title)
	fmt.Fprintln(t.writer, strings.Repeat("=", 72))
}

func providerNameFromSelection(input string, providers []config.ProviderSummary) (string, bool) {
	input = strings.TrimSpace(input)
	if input == "" {
		return "", false
	}
	if number, err := strconv.Atoi(input); err == nil {
		if number > 0 && number <= len(providers) {
			return providers[number-1].Name, true
		}
		return "", false
	}
	for _, provider := range providers {
		if strings.EqualFold(provider.Name, input) {
			return provider.Name, true
		}
	}
	return "", false
}

func parseConfigTUIBool(value string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true", "t", "yes", "y", "1", "on", "enable", "enabled":
		return true, true
	case "false", "f", "no", "n", "0", "off", "disable", "disabled":
		return false, true
	default:
		return false, false
	}
}

func streamPreferenceText(value *bool) string {
	if value == nil {
		return "-"
	}
	return strconv.FormatBool(*value)
}
