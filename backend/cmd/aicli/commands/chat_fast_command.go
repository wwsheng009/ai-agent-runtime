package commands

import (
	"fmt"
	"os"
	"strings"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

// codexServiceTierPriority is the Codex Responses API value for Fast mode.
// Codex config may persist service_tier as "fast", but the request body uses "priority".
const codexServiceTierPriority = "priority"

type fastCommandAction int

const (
	fastCommandToggle fastCommandAction = iota
	fastCommandSet
	fastCommandStatus
)

type fastCommandRequest struct {
	Action fastCommandAction
	Value  bool
}

func chatSessionSupportsFastMode(session *ChatSession) bool {
	if session == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(session.Provider.GetProtocol()), "codex")
}

func parseFastCommandRequest(command string) (fastCommandRequest, error) {
	arg := strings.ToLower(strings.TrimSpace(extractCommandArgument(command)))
	if arg == "" {
		return fastCommandRequest{Action: fastCommandToggle}, nil
	}
	switch arg {
	case "toggle", "switch", "flip":
		return fastCommandRequest{Action: fastCommandToggle}, nil
	case "status", "show":
		return fastCommandRequest{Action: fastCommandStatus}, nil
	case "on", "true", "1", "yes", "y", "fast", "priority":
		return fastCommandRequest{Action: fastCommandSet, Value: true}, nil
	case "off", "false", "0", "no", "n", "default", "normal":
		return fastCommandRequest{Action: fastCommandSet, Value: false}, nil
	}
	return fastCommandRequest{}, fmt.Errorf("无法识别的 /fast 参数: %s", arg)
}

// applyFastCommand toggles or sets Codex Fast mode (service_tier=priority).
// Fast is only valid when the current session protocol is codex.
func applyFastCommand(session *ChatSession, command string) bool {
	if session == nil {
		fmt.Println("错误: 当前没有活动会话")
		return false
	}
	req, err := parseFastCommandRequest(command)
	if err != nil {
		fmt.Printf("错误: %v\n", err)
		fmt.Println("用法: /fast [on|off|toggle|status]")
		return false
	}

	if req.Action == fastCommandStatus {
		printFastCommandStatus(session)
		return false
	}

	if !chatSessionSupportsFastMode(session) {
		protocol := strings.TrimSpace(session.Provider.GetProtocol())
		if protocol == "" {
			protocol = "(unknown)"
		}
		fmt.Printf("错误: Fast 模式仅支持 codex 协议（当前: %s）\n", protocol)
		return false
	}

	previous := session.FastMode
	switch req.Action {
	case fastCommandToggle:
		session.FastMode = !session.FastMode
	case fastCommandSet:
		session.FastMode = req.Value
	}

	warnIfChatSessionSyncFails(session, "toggle fast", syncRuntimeSessionFromChat(session))
	if session.Interaction != nil {
		session.Interaction.RefreshStatus("")
	}

	if session.FastMode {
		fmt.Println("提示: 已开启 Fast 模式（service_tier=priority）")
	} else {
		fmt.Println("提示: 已关闭 Fast 模式")
	}

	if previous != session.FastMode {
		persistFastCommandPreference(session)
	}
	return false
}

func printFastCommandStatus(session *ChatSession) {
	if !chatSessionSupportsFastMode(session) {
		protocol := strings.TrimSpace(session.Provider.GetProtocol())
		if protocol == "" {
			protocol = "(unknown)"
		}
		fmt.Printf("当前协议: %s（Fast 仅对 codex 生效）\n", protocol)
	}
	if session.FastMode {
		fmt.Println("当前 Fast 模式: on (priority)")
	} else {
		fmt.Println("当前 Fast 模式: off")
	}
	if session.Config != nil && session.Config.AICLI != nil && session.Config.AICLI.Chat != nil && session.Config.AICLI.Chat.FastMode != nil {
		if *session.Config.AICLI.Chat.FastMode {
			fmt.Println("配置默认: on")
		} else {
			fmt.Println("配置默认: off")
		}
	} else {
		fmt.Println("配置默认: (未设置)")
	}
}

func persistFastCommandPreference(session *ChatSession) {
	if session == nil || session.Config == nil {
		return
	}
	configPath, err := ensureWritableAICLIConfigPath(session.Config, session.Config.ConfigFilePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: 保存 /fast 偏好失败: %v\n", err)
		return
	}
	value := session.FastMode
	innerPtr := &value
	if _, err := config.UpdateAICLIChatPreferences(configPath, config.AICLIChatPreferenceUpdate{
		FastMode: &innerPtr,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: 保存 /fast 偏好失败: %v\n", err)
		return
	}
	if session.Config.AICLI == nil {
		session.Config.AICLI = &config.AICLIConfig{}
	}
	if session.Config.AICLI.Chat == nil {
		session.Config.AICLI.Chat = &config.AICLIChatConfig{}
	}
	fastCopy := value
	session.Config.AICLI.Chat.FastMode = &fastCopy
}
