package commands

import (
	"context"
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	mcpadmin "github.com/wwsheng009/ai-agent-runtime/internal/mcp/admin"
)

// chat_mcp_picker.go 实现 §4.8 的 /mcp 交互菜单：
//
//	Stage 1  选择 MCP Server（可搜索；状态标记与来源直接来自 /mcp list 的同一份投影）
//	Stage 2  选择动作（查看状态 / 启用|停用 / 移除 / 热重载）
//	确认      动作复用既有 /mcp 文本通道执行（单一写路径，不在选择器内另起一套）
//
// 与其它 picker 一致：备用屏租约先释放、主渲染恢复之后才执行动作；非交互 /
// 非 TTY / JSON 输出降级为现有纯文本面板（/mcp list），行为不退化。

const (
	chatMCPPickerActionStatus = "查看状态"
	chatMCPPickerActionReload = "热重载全部 MCP"
	chatMCPPickerActionRemove = "移除"

	chatMCPPickerCancelLabel = "返回上一层"
)

// canOpenChatMCPPicker 与其它 lease-bound picker 的门禁一致：主呈现器空闲、
// 独占 viewport、没有竞争中的弹窗 / 备用屏、没有进行中的回合。
func canOpenChatMCPPicker(session *ChatSession) bool {
	if session == nil || session.NoInteractive || session.JSONOutput ||
		session.Interaction == nil || session.Surface == nil {
		return false
	}
	if !session.Surface.Enabled() || !session.Surface.OwnedViewport() ||
		session.Surface.LeaseActive() || session.Surface.HasActivePopup() {
		return false
	}
	if session.RuntimeEventBridge != nil && session.RuntimeEventBridge.isRunActive() {
		return false
	}
	return ui.CanUseFullScreenList(resumeFullScreenTerminal(session))
}

// mcpPickerAction 是一个可执行动作：标签 + 要转交给 /mcp 文本通道的命令。
type mcpPickerAction struct {
	Label    string
	Command  string
	Confirm  bool // 破坏性动作需要二次确认
	ReopenAt int  // 执行后回到哪一层：0=回到 server 列表，-1=结束
}

// openChatMCPPicker 是 /mcp 交互菜单入口（命令层 effect 的消费者）。
func openChatMCPPicker(session *ChatSession, _ MCPPickerRequest) {
	if !canOpenChatMCPPicker(session) {
		return
	}
	openChatMCPPickerWithService(session, newChatMCPService())
}

// openChatMCPPickerWithService 便于单测注入替身服务。
func openChatMCPPickerWithService(session *ChatSession, service chatMCPService) {
	if service == nil {
		_ = renderChatCommandResult(session, commandTextResult("错误: MCP 管理服务不可用"), false)
		return
	}
	for {
		items, err := chatMCPPickerItems(service)
		if err != nil {
			_ = renderChatCommandResult(session, commandErrorResult(err), false)
			return
		}
		if len(items) == 0 {
			_ = renderChatCommandResult(session, commandTextResult(chatMCPListText(service)), false)
			return
		}

		picked, err := selectChatMCPPickerList(session, "选择 MCP Server",
			"Enter 打开操作 · Esc 取消", buildChatMCPPickerServerItems(items))
		if err != nil {
			_ = renderChatCommandResult(session, commandErrorResult(err), false)
			return
		}
		if picked.Cancelled || picked.Index < 0 || picked.Index >= len(items) {
			_ = renderChatCommandResult(session, commandTextResult("已取消选择 MCP"), false)
			return
		}

		selected := items[picked.Index]
		name := strings.TrimSpace(selected.Config.Name)
		actions := chatMCPPickerActions(name, selected.Config.IsEnabled())

		actionPicked, err := selectChatMCPPickerList(session, "MCP: "+name,
			"Enter 执行 · Esc 返回", buildChatMCPPickerActionItems(actions))
		if err != nil {
			_ = renderChatCommandResult(session, commandErrorResult(err), false)
			return
		}
		if actionPicked.Cancelled || actionPicked.Index < 0 || actionPicked.Index >= len(actions) {
			continue // 回到 server 列表
		}

		action := actions[actionPicked.Index]
		if strings.TrimSpace(action.Command) == "" {
			continue // "返回上一层"：不执行任何命令，回到 server 列表
		}
		if action.Confirm {
			confirmed, err := confirmChatMCPPickerAction(session, name, action.Label)
			if err != nil {
				_ = renderChatCommandResult(session, commandErrorResult(err), false)
				return
			}
			if !confirmed {
				continue
			}
		}

		text := chatMCPCommandTextWithService(action.Command, service, func() {
			refreshChatMCPTools(session)
		})
		_ = renderChatCommandResult(session, commandTextResult(text), false)
		if action.ReopenAt < 0 {
			return
		}
	}
}

// chatMCPPickerItems 读取一次 MCP 列表（与 /mcp list 同源）。
func chatMCPPickerItems(service chatMCPService) ([]mcpadmin.Item, error) {
	ctx, cancel := context.WithTimeout(context.Background(), chatMCPCommandTimeout)
	defer cancel()
	items, err := service.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("读取 MCP 列表失败: %w", err)
	}
	return items, nil
}

// buildChatMCPPickerServerItems 把 /mcp list 的行投影复用到选择器：第一行做标题，
// 其余行（endpoint / 来源 / 覆盖链）做详情，保证菜单与列表口径完全一致。
func buildChatMCPPickerServerItems(items []mcpadmin.Item) []ui.FullScreenListItem {
	rows := make([]ui.FullScreenListItem, 0, len(items))
	for _, item := range items {
		lines := chatMCPItemLines(item)
		title := item.Config.Name
		detail := ""
		if len(lines) > 0 {
			title = lines[0]
			detail = strings.TrimSpace(strings.Join(lines[1:], " · "))
		}
		rows = append(rows, ui.FullScreenListItem{
			Title:      title,
			Detail:     detail,
			SearchText: item.Config.Name,
		})
	}
	return rows
}

// buildChatMCPPickerActionItems 渲染动作列表（含"返回上一层"）。
func buildChatMCPPickerActionItems(actions []mcpPickerAction) []ui.FullScreenListItem {
	rows := make([]ui.FullScreenListItem, 0, len(actions))
	for _, action := range actions {
		rows = append(rows, ui.FullScreenListItem{
			Title:      action.Label,
			SearchText: action.Label,
		})
	}
	return rows
}

// chatMCPPickerActions 按 server 当前状态给出动作集合。命令字符串全部落在既有
// /mcp 子命令上，选择器不新增写路径。
func chatMCPPickerActions(name string, enabled bool) []mcpPickerAction {
	name = strings.TrimSpace(name)
	toggleLabel := "停用"
	toggleSub := "disable"
	if !enabled {
		toggleLabel = "启用"
		toggleSub = "enable"
	}
	return []mcpPickerAction{
		{Label: chatMCPPickerActionStatus, Command: "/mcp status " + name, ReopenAt: 0},
		{Label: toggleLabel, Command: "/mcp " + toggleSub + " " + name, ReopenAt: 0},
		{Label: chatMCPPickerActionReload, Command: "/mcp reload", ReopenAt: -1},
		{Label: chatMCPPickerActionRemove, Command: "/mcp remove " + name, Confirm: true, ReopenAt: 0},
		{Label: chatMCPPickerCancelLabel, Command: "", ReopenAt: -1},
	}
}

// confirmChatMCPPickerAction 是破坏性动作的二次确认（同一套全屏列表交互）。
func confirmChatMCPPickerAction(session *ChatSession, name, label string) (bool, error) {
	picked, err := selectChatMCPPickerList(session, label+" "+name,
		"Enter 确认 · Esc 取消", []ui.FullScreenListItem{
			{Title: "取消"},
			{Title: "确认" + label + " " + name},
		})
	if err != nil {
		return false, err
	}
	if picked.Cancelled || picked.Index <= 0 {
		return false, nil
	}
	return true, nil
}

// selectChatMCPPickerList 执行一次「接管备用屏 → 全屏列表 → 释放备用屏」的生命
// 周期：返回前必须等 actor 观察到 LeaseReleased，调用方才能继续碰终端或再开列表。
func selectChatMCPPickerList(session *ChatSession, title, subtitle string, items []ui.FullScreenListItem) (ui.FullScreenListResult, error) {
	lease, err := session.Surface.AcquireAlternateScreen(context.Background(), ui.FullscreenRequest{Title: title})
	if err != nil {
		return ui.FullScreenListResult{}, fmt.Errorf("打开 MCP 选择器失败: %w", err)
	}
	if !session.Interaction.postUIAction(ui.OpenMCPPicker{LeaseID: lease.ID()}) {
		_ = lease.Release(context.Background())
		return ui.FullScreenListResult{}, errChatPickerStateUncommitted
	}
	if !session.Interaction.waitUIActorIdleBounded("open mcp picker") {
		_ = lease.Release(context.Background())
		return ui.FullScreenListResult{}, errChatPickerRenderNotReady
	}

	picked, pickErr := ui.SelectFullScreenListWithLease(context.Background(), resumeFullScreenTerminal(session), ui.FullScreenListOptions{
		Title:        title,
		Subtitle:     subtitle,
		EmptyMessage: "没有可选项",
		ConfirmLabel: "确认",
		Items:        items,
	}, lease)

	_ = session.Interaction.postUIAction(ui.CloseMCPPicker{LeaseID: lease.ID()})
	releaseErr := lease.Release(context.Background())
	if !session.Interaction.waitUIActorIdleBounded("close mcp picker") {
		return ui.FullScreenListResult{}, errChatPickerActorNotIdle
	}
	if releaseErr != nil {
		return ui.FullScreenListResult{}, fmt.Errorf("关闭 MCP 选择器失败: %w", releaseErr)
	}
	if pickErr != nil {
		return ui.FullScreenListResult{}, fmt.Errorf("MCP 选择器失败: %w", pickErr)
	}
	return picked, nil
}
