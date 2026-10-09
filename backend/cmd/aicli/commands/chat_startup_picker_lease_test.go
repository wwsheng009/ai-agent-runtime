package commands

import (
	"errors"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

// L5-1 Batch B 回归契约：三个启动选择器统一经 ui.RunStartupFullScreenList（D2）。
// 无 TTY（测试环境/管道）时必须 fail-closed 回落 numbered——不打开副屏、不写 raw
// 序列、返回可判定的「不可用」信号，调用方继续走既有 numbered 路径。
func TestStartupPickersFailClosedWithoutTTY(t *testing.T) {
	if ui.IsInteractiveTerminal() {
		t.Skip("requires a non-TTY test environment")
	}
	cfg := &config.Config{Providers: config.ProvidersConfig{Items: map[string]config.Provider{
		"alpha": {Enabled: true, SupportedModels: []string{"m1"}},
	}}}
	if name, ok := selectProviderFullScreen(cfg); ok || name != "" {
		t.Fatalf("selectProviderFullScreen must fall back without a TTY, got (%q, %v)", name, ok)
	}
	if model, ok := selectModelFullScreen(config.Provider{SupportedModels: []string{"m1"}}); ok || model != "" {
		t.Fatalf("selectModelFullScreen must fall back without a TTY, got (%q, %v)", model, ok)
	}
	var prompter cliLoginPrompter
	if _, _, err := prompter.PromptSelect("Provider", "provider", []string{"alpha"}, "", true); !errors.Is(err, ui.ErrFullScreenUnavailable) {
		t.Fatalf("cliLoginPrompter.PromptSelect must fail closed without a TTY, got %v", err)
	}
}
