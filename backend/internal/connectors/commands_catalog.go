// agentCommands 是 IM 命令的单一事实来源：命令解析、帮助文本与通道侧
// 指令面板（如 QQ /v2/panels）都从这张表生成，避免多处维护漂移。
//
// 边界：这里只描述「核心闭环命令」（不进入 agent turn）；agent 自己的
// slash 命令仍由 Backend 解释，不在此表。
package connectors

// AgentCommand 是一条 IM 核心命令的元数据。
type AgentCommand struct {
	// Name 是不带前缀的命令名（如 "new"）。
	Name string
	// Label 是给用户看的短名称（面板/按钮，≤14 字符）。
	Label string
	// Description 是给用户看的一句话说明（面板/帮助，≤30 字符）。
	Description string
	// Usage 是参数用法（帮助文本用，可空）。
	Usage string
}

// agentCommands 是核心命令表（顺序即面板/帮助展示顺序）。
//
// 新增命令时必须同步 handleCommand 与 isCoreCommand，避免面板出现但不可用。
var agentCommands = []AgentCommand{
	{Name: "help", Label: "帮助", Description: "查看可用命令"},
	{Name: "new", Label: "新会话", Description: "清空上下文重新开始"},
	{Name: "status", Label: "状态", Description: "查看当前绑定的会话"},
	{Name: "approve", Label: "批准", Description: "批准挂起的审批", Usage: "[request_id]"},
	{Name: "deny", Label: "拒绝", Description: "拒绝挂起的审批", Usage: "[request_id]"},
	{Name: "answer", Label: "回答", Description: "回答挂起的提问", Usage: "<序号|id> <内容>"},
	{Name: "invite", Label: "邀请", Description: "生成机器人邀请链接", Usage: "[callback_data]"},
	{Name: "undo", Label: "回退", Description: "回退最近一轮并撤回消息", Usage: "[轮数]"},
}

// AgentCommands 返回核心命令表的副本（供通道侧生成面板/菜单）。
func AgentCommands() []AgentCommand {
	return append([]AgentCommand(nil), agentCommands...)
}
