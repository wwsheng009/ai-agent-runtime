// Package runtimehost 定义「会话执行核」的稳定接口：谁执行会话 turn，与
// 「谁提供 HTTP 路由」解耦。目标架构与迁移方案见
// docs/automation-tasks/runtimehost-executor-extraction-plan.md。
//
// E3.0（本批次）只引入接口与适配器，**行为零变化**：现有执行栈仍在
// runtimeapi.Handler 内，Handler 通过 handlerExecutor 适配器对外暴露本接口。
// 后续 E3.1/E3.2 再把 actor 工厂与 hub 所有权真正搬进本包。
package runtimehost

import (
	"context"

	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
)

// Executor 是唯一的会话执行核心：拥有 hub、actor 工厂、会话租约与事件落盘。
// 它不感知 HTTP；调用方（runtimeapi / aicli / scheduled）负责传输与控制面。
type Executor interface {
	// SubmitTurn 同步执行一次 turn。completed=false 表示提交被 actor 接受但尚未结束
	// （等待审批/输入，或超过提交等待预算），此时 Output 为空、State 非空。
	SubmitTurn(ctx context.Context, req TurnRequest) (TurnResult, error)
	// SubmitAsync 提交后立即返回（"被 actor 接受"语义），用于投递回执。
	SubmitAsync(ctx context.Context, req TurnRequest) error
	// Hub 暴露底层 hub 供既有 API 读取（过渡期；E3.2 后应收敛为窄方法）。
	Hub() *chat.SessionHub
	// Close 停止 hub、flush 事件缓冲、释放执行核资源。
	Close(ctx context.Context) error
}

// TurnRequest 是一次会话 turn 请求。
type TurnRequest struct {
	SessionID string
	Prompt    string
	// RunMeta 携带 source / permission mode 等运行元数据（可空）。
	RunMeta *team.RunMeta
	// Images 是本次消息附带的图片路径（可选）。
	Images []string
	// Profile 限定本次执行的工具面（可选；E3.1+ 生效，E3.0 适配器忽略）。
	Profile string
}

// TurnResult 是一次 turn 的终态（或"已接受未完成"的中间态）。
type TurnResult struct {
	SessionID string
	Output    string
	Success   bool
	Error     string
	// Completed 表示 turn 已结束；false 表示已接受但未完成（见 State）。
	Completed bool
	// State 是未完成时的运行态快照（可空）。
	State *chat.RuntimeState
}
