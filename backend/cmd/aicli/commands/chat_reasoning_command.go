package commands

import (
	"fmt"
	"os"
	"strings"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

type reasoningCommandAction int

const (
	reasoningCommandStatus reasoningCommandAction = iota
	reasoningCommandSet
)

type reasoningCommandRequest struct {
	Action reasoningCommandAction
	Value  bool
}

func parseReasoningCommandRequest(command string) (reasoningCommandRequest, error) {
	arg := strings.ToLower(strings.TrimSpace(extractCommandArgument(command)))
	if arg == "" {
		return reasoningCommandRequest{Action: reasoningCommandStatus}, nil
	}
	switch arg {
	case "status", "show":
		return reasoningCommandRequest{Action: reasoningCommandStatus}, nil
	case "on", "true", "1", "yes", "y":
		return reasoningCommandRequest{Action: reasoningCommandSet, Value: true}, nil
	case "off", "false", "0", "no", "n":
		return reasoningCommandRequest{Action: reasoningCommandSet, Value: false}, nil
	}
	return reasoningCommandRequest{}, fmt.Errorf("无法识别的 /reasoning 参数: %s", arg)
}

func applyReasoningCommand(session *ChatSession, command string) bool {
	if session == nil {
		fmt.Println("错误: 当前没有活动会话")
		return false
	}
	req, err := parseReasoningCommandRequest(command)
	if err != nil {
		fmt.Printf("错误: %v\n", err)
		fmt.Println("用法: /reasoning [on|off|status]")
		return false
	}
	if req.Action == reasoningCommandSet {
		session.SuppressReasoningOutput = !req.Value
		if session.Interaction != nil {
			session.Interaction.RefreshStatus("")
		}
	}
	printReasoningCommandStatus(session)
	return false
}

func printReasoningCommandStatus(session *ChatSession) {
	if chatReasoningOutputEnabled(session) {
		fmt.Println("当前 reasoning: on")
		return
	}
	fmt.Println("当前 reasoning: off")
}

func chatReasoningOutputEnabled(session *ChatSession) bool {
	return session == nil || !session.SuppressReasoningOutput
}

func shouldRenderChatReasoning(session *ChatSession) bool {
	return shouldRenderInteractiveOutput(session) && chatReasoningOutputEnabled(session)
}

type reasoningEffortCommandAction int

const (
	reasoningEffortCommandSelect reasoningEffortCommandAction = iota
	reasoningEffortCommandStatus
	reasoningEffortCommandSet
	reasoningEffortCommandClear
)

type reasoningEffortCommandRequest struct {
	Action reasoningEffortCommandAction
	Value  string
}

func parseReasoningEffortCommandRequest(command string) (reasoningEffortCommandRequest, error) {
	arg := strings.TrimSpace(extractCommandArgument(command))
	if arg == "" {
		return reasoningEffortCommandRequest{Action: reasoningEffortCommandSelect}, nil
	}
	fields := strings.Fields(arg)
	if len(fields) == 0 {
		return reasoningEffortCommandRequest{Action: reasoningEffortCommandSelect}, nil
	}
	if len(fields) > 2 || (len(fields) == 2 && !strings.EqualFold(fields[0], "set")) {
		return reasoningEffortCommandRequest{}, fmt.Errorf("无法解析 /reasoning_effort 参数: %s", arg)
	}
	token := fields[0]
	if len(fields) == 2 {
		token = fields[1]
	}
	switch strings.ToLower(strings.TrimSpace(token)) {
	case "status", "show":
		return reasoningEffortCommandRequest{Action: reasoningEffortCommandStatus}, nil
	case "select", "choose", "switch":
		return reasoningEffortCommandRequest{Action: reasoningEffortCommandSelect}, nil
	case "set":
		return reasoningEffortCommandRequest{}, fmt.Errorf("set 需要指定 reasoning_effort 值")
	case "clear", "unset", "reset", "0", "清空":
		return reasoningEffortCommandRequest{Action: reasoningEffortCommandClear}, nil
	default:
		value := runtimetypes.NormalizeReasoningEffort(token)
		if value == "" {
			return reasoningEffortCommandRequest{}, fmt.Errorf("reasoning_effort 不能为空")
		}
		return reasoningEffortCommandRequest{Action: reasoningEffortCommandSet, Value: value}, nil
	}
}

func handleReasoningEffortCommand(session *ChatSession, command string, noInteractive bool) bool {
	if session == nil {
		fmt.Println("错误: 当前没有活动会话")
		return false
	}
	req, err := parseReasoningEffortCommandRequest(command)
	if err != nil {
		fmt.Printf("错误: %v\n", err)
		fmt.Println("用法: /reasoning_effort [status|select|clear|<value>]")
		return false
	}

	switch req.Action {
	case reasoningEffortCommandStatus:
		printReasoningEffortCommandStatus(session)
		return false
	case reasoningEffortCommandSelect:
		if noInteractive {
			printReasoningEffortCommandStatus(session)
			return false
		}
		selected, err := selectReasoningEffortForCurrentSession(session)
		if err != nil {
			fmt.Printf("错误: %v\n", err)
			return false
		}
		if err := applyReasoningEffortCommandSelection(session, selected, false); err != nil {
			fmt.Printf("错误: %v\n", err)
			return false
		}
	case reasoningEffortCommandClear:
		if err := applyReasoningEffortCommandSelection(session, "", false); err != nil {
			fmt.Printf("错误: %v\n", err)
			return false
		}
	case reasoningEffortCommandSet:
		if err := applyReasoningEffortCommandSelection(session, req.Value, true); err != nil {
			fmt.Printf("错误: %v\n", err)
			return false
		}
	}
	printReasoningEffortCommandStatus(session)
	return false
}

func selectReasoningEffortForCurrentSession(session *ChatSession) (string, error) {
	if session == nil {
		return "", fmt.Errorf("当前没有活动会话")
	}
	catalog := reasoningEffortCatalogForModel(session.Provider, effectiveRuntimeModel(session))
	selected, _, err := selectRuntimeReasoningEffort(session, session.ReasoningEffort, catalog.options)
	if err != nil {
		return "", err
	}
	return selected, nil
}

func applyReasoningEffortCommandSelection(session *ChatSession, raw string, explicit bool) error {
	if session == nil {
		return fmt.Errorf("当前没有活动会话")
	}
	reasoning := runtimetypes.NormalizeReasoningEffort(raw)
	if reasoning != "" {
		resolved, warning, err := resolveChatReasoningEffort(session.Provider, effectiveRuntimeModel(session), reasoning, explicit)
		if err != nil {
			return err
		}
		if warning != "" {
			fmt.Fprintln(os.Stderr, warning)
		}
		reasoning = resolved
	}

	session.ReasoningEffort = reasoning
	warnIfChatSessionSyncFails(session, "toggle reasoning_effort", syncRuntimeSessionFromChat(session))
	if err := refreshLocalRuntimeAfterModelSelection(session); err != nil {
		warnIfChatSessionSyncFails(session, "refresh local runtime after reasoning_effort switch", err)
	}
	if session.Interaction != nil {
		session.Interaction.RefreshStatus("")
	}
	persistReasoningEffortCommandPreference(session)
	return nil
}

func printReasoningEffortCommandStatus(session *ChatSession) {
	if session == nil {
		fmt.Println("错误: 当前没有活动会话")
		return
	}
	beginDirectInteractiveOutput(session)
	reasoning := runtimetypes.NormalizeReasoningEffort(session.ReasoningEffort)
	if reasoning == "" {
		reasoning = "(无)"
	}
	fmt.Printf("当前 reasoning_effort: %s\n", reasoning)
	catalog := reasoningEffortCatalogForModel(session.Provider, effectiveRuntimeModel(session))
	if len(catalog.options) == 0 {
		fmt.Println("可选 reasoning_effort: (未声明)")
		return
	}
	fmt.Printf("可选 reasoning_effort: %s\n", strings.Join(catalog.options, ", "))
}

func persistReasoningEffortCommandPreference(session *ChatSession) {
	if session == nil || session.Config == nil {
		return
	}
	configPath, err := ensureWritableAICLIConfigPath(session.Config, session.Config.ConfigFilePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: 保存 /reasoning_effort 偏好失败: %v\n", err)
		return
	}
	value := runtimetypes.NormalizeReasoningEffort(session.ReasoningEffort)
	if _, err := config.UpdateAICLIChatPreferences(configPath, config.AICLIChatPreferenceUpdate{
		ReasoningEffort: stringValuePtr(value),
	}); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: 保存 /reasoning_effort 偏好失败: %v\n", err)
		return
	}
	if session.Config.AICLI == nil {
		session.Config.AICLI = &config.AICLIConfig{}
	}
	if session.Config.AICLI.Chat == nil {
		session.Config.AICLI.Chat = &config.AICLIChatConfig{}
	}
	session.Config.AICLI.Chat.ReasoningEffort = value
}
