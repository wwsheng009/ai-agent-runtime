package schedule

import "context"

// SessionExecutor 是内嵌执行（embedded，方案 E0）的最小依赖面：把一条提醒文本
// 作为 user 消息提交进指定会话并启动一次模型 turn。daemon 用它替代 HTTP
// delegate——在 runtime-server 离线时自己执行（embedded-execution-design.md）。
//
// 实现方（cmd/scheduled/embedded.go）负责：
//   - 构造 bootstrap.Manager + runtimeapi.Handler（不启 HTTP 监听）；
//   - hub.GetOrCreate(sessionID) → actor.SubmitPromptAsync(ctx, prompt)。
type SessionExecutor interface {
	// ExecuteTurn 提交一次 turn；返回 nil 表示 prompt 被 actor 接受。
	ExecuteTurn(ctx context.Context, sessionID, prompt string) error
}

// LeaseArbiter 是 E1 的会话租约仲裁接缝：内嵌执行前必须拿到该会话的排他租约，
// 保证 daemon 不与交互宿主（aicli / runtime-server）并发跑同一会话。
//
// 返回 ok=false 表示租约被其他宿主持有（用户可能正在聊天）——调用方必须放弃
// 内嵌执行并退回兜底路径（持久 mailbox），绝不抢占。
type LeaseArbiter interface {
	// AcquireSession 尝试获取 session 排他租约；成功返回释放函数。
	AcquireSession(ctx context.Context, sessionID string) (release func(), ok bool)
}
