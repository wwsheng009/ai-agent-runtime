package commands

import (
	"fmt"
	"os"
	"strings"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

type streamCommandAction int

const (
	streamCommandToggle streamCommandAction = iota
	streamCommandSet
	streamCommandStatus
)

type streamCommandRequest struct {
	Action streamCommandAction
	Value  bool
}

func parseStreamCommandRequest(command string) (streamCommandRequest, error) {
	arg := strings.ToLower(strings.TrimSpace(extractCommandArgument(command)))
	if arg == "" {
		return streamCommandRequest{Action: streamCommandToggle}, nil
	}
	switch arg {
	case "toggle", "switch", "flip":
		return streamCommandRequest{Action: streamCommandToggle}, nil
	case "status", "show":
		return streamCommandRequest{Action: streamCommandStatus}, nil
	case "on", "true", "1", "yes", "y", "stream", "streaming":
		return streamCommandRequest{Action: streamCommandSet, Value: true}, nil
	case "off", "false", "0", "no", "n", "normal", "buffered":
		return streamCommandRequest{Action: streamCommandSet, Value: false}, nil
	}
	return streamCommandRequest{}, fmt.Errorf("无法识别的 /stream 参数: %s", arg)
}

// applyStreamCommand toggles or sets session.Stream and persists the choice
// to aicli.chat.stream when a config file is available.
func applyStreamCommand(session *ChatSession, command string) bool {
	if session == nil {
		fmt.Println("错误: 当前没有活动会话")
		return false
	}
	req, err := parseStreamCommandRequest(command)
	if err != nil {
		fmt.Printf("错误: %v\n", err)
		fmt.Println("用法: /stream [on|off|toggle|status]")
		return false
	}

	if req.Action == streamCommandStatus {
		printStreamCommandStatus(session)
		return false
	}

	previous := session.Stream
	switch req.Action {
	case streamCommandToggle:
		session.Stream = !session.Stream
	case streamCommandSet:
		session.Stream = req.Value
	}

	warnIfChatSessionSyncFails(session, "toggle stream", syncRuntimeSessionFromChat(session))
	if session.Interaction != nil {
		session.Interaction.RefreshStatus("")
	}

	if session.Stream {
		fmt.Println("提示: 已切换到流式模式")
	} else {
		fmt.Println("提示: 已切换到普通模式")
	}

	if previous != session.Stream {
		persistStreamCommandPreference(session)
	}
	return false
}

// applyStreamShortcut is the entry point for the legacy aliases /s and /n
// where the desired value is fixed. It mirrors applyStreamCommand's
// persistence behavior.
func applyStreamShortcut(session *ChatSession, value bool) bool {
	if session == nil {
		fmt.Println("错误: 当前没有活动会话")
		return false
	}
	previous := session.Stream
	session.Stream = value
	warnIfChatSessionSyncFails(session, "toggle stream", syncRuntimeSessionFromChat(session))
	if session.Interaction != nil {
		session.Interaction.RefreshStatus("")
	}
	if value {
		fmt.Println("提示: 已切换到流式模式")
	} else {
		fmt.Println("提示: 已切换到普通模式")
	}
	if previous != value {
		persistStreamCommandPreference(session)
	}
	return false
}

func printStreamCommandStatus(session *ChatSession) {
	if session.Stream {
		fmt.Println("当前输出模式: 流式 (stream)")
	} else {
		fmt.Println("当前输出模式: 普通 (normal)")
	}
	if session.Config != nil && session.Config.AICLI != nil && session.Config.AICLI.Chat != nil && session.Config.AICLI.Chat.Stream != nil {
		if *session.Config.AICLI.Chat.Stream {
			fmt.Println("配置默认: stream")
		} else {
			fmt.Println("配置默认: normal")
		}
	} else {
		fmt.Println("配置默认: (未设置)")
	}
}

func persistStreamCommandPreference(session *ChatSession) {
	if session == nil || session.Config == nil {
		return
	}
	configPath, err := ensureWritableAICLIConfigPath(session.Config, session.Config.ConfigFilePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: 保存 /stream 偏好失败: %v\n", err)
		return
	}
	value := session.Stream
	innerPtr := &value
	if _, err := config.UpdateAICLIChatPreferences(configPath, config.AICLIChatPreferenceUpdate{
		Stream: &innerPtr,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: 保存 /stream 偏好失败: %v\n", err)
		return
	}
	if session.Config.AICLI == nil {
		session.Config.AICLI = &config.AICLIConfig{}
	}
	if session.Config.AICLI.Chat == nil {
		session.Config.AICLI.Chat = &config.AICLIChatConfig{}
	}
	streamCopy := value
	session.Config.AICLI.Chat.Stream = &streamCopy
}
