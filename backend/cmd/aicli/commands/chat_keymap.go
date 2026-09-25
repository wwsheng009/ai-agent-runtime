package commands

import (
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/keymap"
)

var (
	chatKeymapMu            sync.Mutex
	chatKeymapRegistryCache *keymap.Registry
)

// chatKeybindingsPath 返回用户按键配置文件路径：优先
// $AICLI_HOME/keybindings.json，否则 ~/.aicli/keybindings.json。
func chatKeybindingsPath() string {
	if home := strings.TrimSpace(os.Getenv("AICLI_HOME")); home != "" {
		return filepath.Join(home, "keybindings.json")
	}
	return filepath.Join(chatUserAICLIDir(), "keybindings.json")
}

// chatUserAICLIDir 解析 ~/.aicli；主目录不可用时回退 ./.aicli（与其它
// 用户级路径的降级方向一致），保证按键配置读取不会 panic 或返回空路径。
func chatUserAICLIDir() string {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return ".aicli"
	}
	return filepath.Join(home, ".aicli")
}

// chatKeymapRegistry 懒加载进程级按键注册表。文件缺失或损坏时回退默认键位，
// 并把解析问题记录在 Registry.Warnings 中供 /hotkeys 展示。
func chatKeymapRegistry() *keymap.Registry {
	chatKeymapMu.Lock()
	defer chatKeymapMu.Unlock()
	if chatKeymapRegistryCache == nil {
		chatKeymapRegistryCache = keymap.Load(chatKeybindingsPath())
	}
	return chatKeymapRegistryCache
}

// reloadChatKeymapRegistry 丢弃缓存并在下次读取时重新加载用户配置。
func reloadChatKeymapRegistry() *keymap.Registry {
	chatKeymapMu.Lock()
	chatKeymapRegistryCache = nil
	chatKeymapMu.Unlock()
	return chatKeymapRegistry()
}

// chatComposerActionForChord 把编辑器上报的规范化 chord 解析为动作 id。
func chatComposerActionForChord(chord string) (string, bool) {
	action, ok := chatKeymapRegistry().Resolve(chord)
	if !ok {
		return "", false
	}
	return string(action), true
}

// chatComposerCollapsePastedText 读取 aicli.chat.collapse_pasted_text 偏好；
// nil 表示使用默认行为（折叠）。
func chatComposerCollapsePastedText(session *ChatSession) *bool {
	if session == nil || session.Config == nil || session.Config.AICLI == nil || session.Config.AICLI.Chat == nil {
		return nil
	}
	return session.Config.AICLI.Chat.CollapsePastedText
}
