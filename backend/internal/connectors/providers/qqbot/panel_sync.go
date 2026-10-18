// 把核心 IM 命令同步到 QQ 指令面板（/v2/panels）。
//
// 目的：用户不必记住 slash 命令——打开输入框旁的「指令面板」即可看到
// 「帮助 / 新会话 / 状态 / 批准 / 拒绝 / 回答 / 邀请 / 回退」。
// 2026-10-17 真机确认：type=command 元素点击后客户端直接**发送元素名称
// （中文 Label）**（早前注释"自动填入输入框"与实测不符）；闭环依赖命令层
// 的中文标签别名归一（service.go parseLabelCommand），不再落进 agent 回合。
//
// 策略（幂等 + 最小写放大）：
//   - 只在 c2c 场景维护一个「agent 命令」面板；
//   - 通过 remark 标记归属（panelRemark），避免误改用户手工创建的面板；
//   - 已有面板 → 内容未变则跳过 PUT；变化则 PUT；
//   - 无面板 → POST 创建；
//   - 失败只记日志（面板是体验增强，不能影响消息收发）。
package qqbot

import (
	"context"
	"strconv"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
)

// panelRemark 是自动面板的归属标记（官方：remark 不对用户展示）。
const panelRemark = "ai-agent-runtime:agent-commands"

// commandPanelSyncEnabled 读取 Settings["command_panel_sync"]（缺省 true）。
// 设为 false 可关闭自动面板同步（例如无面板权限或想手工维护）。
func (p *Provider) commandPanelSyncEnabled(cfg connectors.Config) bool {
	raw := strings.TrimSpace(cfg.Setting("command_panel_sync", ""))
	if raw == "" {
		return true
	}
	enabled, err := strconv.ParseBool(raw)
	if err != nil {
		return true
	}
	return enabled
}

// SyncCommandPanel 把核心命令同步为 QQ 指令面板（c2c 全局）。
// 返回是否发生了写操作。
func (p *Provider) SyncCommandPanel(ctx context.Context, cfg connectors.Config) (bool, error) {
	commands := connectors.AgentCommands()
	if len(commands) == 0 {
		return false, nil
	}
	items := make([]QQPanelItem, 0, len(commands))
	for _, command := range commands {
		items = append(items, QQPanelItem{
			Name: command.Label,
			Desc: command.Description,
			Type: "command",
		})
	}

	panel, found, err := p.findAgentCommandPanel(ctx, cfg)
	if err != nil {
		return false, err
	}
	if !found {
		_, err := p.CreatePanel(ctx, cfg, QQPanelCreateRequest{
			Scope:      "c2c",
			TargetType: "all",
			Panel:      QQPanel{Items: items, Remark: panelRemark},
		})
		return err == nil, err
	}
	if panelItemsEqual(panel.Panel.Items, items) {
		return false, nil
	}
	_, err = p.UpdatePanel(ctx, cfg, panel.PanelID, QQPanel{Items: items, Remark: panelRemark})
	return err == nil, err
}

// findAgentCommandPanel 在 c2c 面板里按 remark 找自动维护的那一个。
func (p *Provider) findAgentCommandPanel(ctx context.Context, cfg connectors.Config) (QQPanelRecord, bool, error) {
	cursor := ""
	for page := 0; page < 10; page++ { // 防御：最多翻 10 页
		result, err := p.ListPanels(ctx, cfg, "c2c", cursor, 50)
		if err != nil {
			return QQPanelRecord{}, false, err
		}
		for _, record := range result.Records {
			if strings.TrimSpace(record.Panel.Remark) == panelRemark {
				return record, true, nil
			}
		}
		if result.IsEnd || strings.TrimSpace(result.NextCursor) == "" {
			break
		}
		cursor = result.NextCursor
	}
	return QQPanelRecord{}, false, nil
}

// panelItemsEqual 比较面板元素（名称/描述/类型/链接/管理员标记）。
func panelItemsEqual(current, want []QQPanelItem) bool {
	if len(current) != len(want) {
		return false
	}
	for i := range current {
		if current[i].Name != want[i].Name ||
			current[i].Desc != want[i].Desc ||
			current[i].Type != want[i].Type ||
			current[i].Link != want[i].Link ||
			current[i].OnlyAdmin != want[i].OnlyAdmin {
			return false
		}
	}
	return true
}
