// 把核心 IM 命令同步为 QQ 全局自定义菜单（/v2/menu）。
//
// 目的：在 QQ 单聊输入框旁的「菜单」里提供可直接点击的命令入口。与指令面板
// 不同，菜单项点击即「发送消息」（send_message），发送文本由本程序指定
// （"/help" 等 slash 命令）——不依赖客户端对面板元素的填写行为，触发路径确定，
// 点击后必然命中 connectors 的核心命令层。
//
// 策略（保守幂等；官方菜单没有 remark 归属字段，无法标记所有权）：
//   - 菜单为空 → 写入内置命令；内容与内置完全一致 → 跳过（不写放大）；
//   - 可识别为本程序写的旧版菜单（逐项 name+send_message 与核心命令目录配对，
//     如命令目录演进后的子集/旧序）→ 覆盖更新为最新；
//   - 已有他人自定义菜单（非空且与内置不一致）→ **不覆盖**，只记一条日志
//     （避免误改运营配置；想托管先清空菜单，或把 command_menu_sync 设为 false）；
//   - 失败只记日志（菜单是体验增强，不能影响消息收发）。
package qqbot

import (
	"context"
	"strconv"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
)

// commandMenuSyncEnabled 读取 Settings["command_menu_sync"]（缺省 true）。
// 设为 false 可关闭自动菜单同步（例如想自行用开放平台配置菜单）。
func (p *Provider) commandMenuSyncEnabled(cfg connectors.Config) bool {
	raw := strings.TrimSpace(cfg.Setting("command_menu_sync", ""))
	if raw == "" {
		return true
	}
	enabled, err := strconv.ParseBool(raw)
	if err != nil {
		return true
	}
	return enabled
}

// SyncCommandMenu 把核心命令同步为全局自定义菜单（c2c，设置后对所有用户生效）。
// 返回是否发生了写操作。已有非空且非内置的菜单时跳过（不覆盖他人配置）。
func (p *Provider) SyncCommandMenu(ctx context.Context, cfg connectors.Config) (bool, error) {
	want := commandMenuItems()
	if len(want) == 0 {
		return false, nil
	}
	state, err := p.GetMenu(ctx, cfg)
	if err != nil {
		return false, err
	}
	var current []QQMenuItem
	if state.Menu != nil {
		current = state.Menu.Items
	}
	switch {
	case len(current) == 0:
		_, err := p.SetMenu(ctx, cfg, QQMenu{Items: want})
		return err == nil, err
	case menuItemsEqual(current, want):
		return false, nil
	case menuLooksManaged(current):
		// 本程序写出的旧版菜单（命令目录演进/子集/旧序）：更新为最新内置。
		_, err := p.SetMenu(ctx, cfg, QQMenu{Items: want})
		return err == nil, err
	default:
		p.logf("qqbot: 已存在自定义菜单（非本程序维护，与内置命令不一致），跳过自动同步以免覆盖；如需托管请先清空菜单，或设 command_menu_sync=false 关闭本同步")
		return false, nil
	}
}

// commandMenuItems 从核心命令目录生成菜单项：name=中文短名（展示），
// send_message="/<命令名>"（点击直发，命中核心命令层）。
// 防御：目录超过官方 10 项上限时截断，而不是让 PUT 注定失败。
func commandMenuItems() []QQMenuItem {
	commands := connectors.AgentCommands()
	if len(commands) > menuMaxItems {
		commands = commands[:menuMaxItems]
	}
	items := make([]QQMenuItem, 0, len(commands))
	for _, command := range commands {
		items = append(items, QQMenuItem{
			Name:        command.Label,
			Type:        "send_message",
			SendMessage: "/" + command.Name,
		})
	}
	return items
}

// menuItemsEqual 比较两组菜单项（名称/类型/发送文本/链接/开关/子菜单全字段），
// 供幂等判断：一致的菜单不做任何写操作。
func menuItemsEqual(current, want []QQMenuItem) bool {
	if len(current) != len(want) {
		return false
	}
	for i := range current {
		if current[i].Name != want[i].Name ||
			!strings.EqualFold(current[i].Type, want[i].Type) ||
			current[i].SendMessage != want[i].SendMessage ||
			current[i].Link != want[i].Link ||
			!menuSwitchEqual(current[i].Switch, want[i].Switch) ||
			!menuSubItemsEqual(current[i].SubMenuItems, want[i].SubMenuItems) {
			return false
		}
	}
	return true
}

// menuLooksManaged 识别「本程序维护的菜单」（含历史版本）：每一项都是
// send_message 且 name+send_message 与当前核心命令目录的（Label, "/Name"）
// 配对完全吻合。命令目录演进后旧版菜单据此被升级；运营自建的 link/switch/
// 自定义文本/自定义名称菜单不满足条件，不会被覆盖。
func menuLooksManaged(items []QQMenuItem) bool {
	if len(items) == 0 {
		return false
	}
	type commandPair struct{ label, send string }
	known := make(map[commandPair]bool)
	for _, command := range connectors.AgentCommands() {
		known[commandPair{command.Label, "/" + command.Name}] = true
	}
	for _, item := range items {
		if !strings.EqualFold(strings.TrimSpace(item.Type), "send_message") {
			return false
		}
		if !known[commandPair{item.Name, strings.TrimSpace(item.SendMessage)}] {
			return false
		}
	}
	return true
}

// menuSwitchEqual nil 安全的开关比较。
func menuSwitchEqual(a, b *QQMenuSwitch) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.SwitchID == b.SwitchID && a.Default == b.Default
}

// menuSubItemsEqual 子菜单逐项比较（顺序敏感，与官方覆盖式配置一致）。
func menuSubItemsEqual(a, b []QQSubMenuItem) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Name != b[i].Name ||
			!strings.EqualFold(a[i].Type, b[i].Type) ||
			a[i].SendMessage != b[i].SendMessage ||
			a[i].Link != b[i].Link {
			return false
		}
	}
	return true
}
