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

// HeadlessTurnRequest 是一次不绑会话的作业执行请求（J2：workspace/headless 目标）。
//
// 与 SessionExecutor 的区别：这里**没有**既有会话——执行方需按 target 自行创建
// 临时会话（workspace 目标绑定工作区；headless 目标不绑）。
type HeadlessTurnRequest struct {
	// JobID 是作业 id（用于日志/结果归属）。
	JobID string
	// Goal / Plan 是创建时确定的目标与执行步骤。
	Goal string
	Plan []string
	// Prompt 是作业正文（无 goal/plan 时即提醒文本）。
	Prompt string
	// Profile 限定工具面（可空）。
	Profile string
	// Workspace 是 workspace 目标绑定的工作区（headless 为空）。
	Workspace string
	// TargetKind 是执行上下文类型。
	TargetKind TargetKind
}

// HeadlessExecutor 是 J2 的可选执行接缝：执行不绑会话的作业。
//
// Runtime 通过类型断言发现它；未实现时非 session 目标的到期任务会被**延后**
// （不空转、不计入死信），等待具备 headless 执行能力的宿主接管。
type HeadlessExecutor interface {
	ExecuteHeadlessTurn(ctx context.Context, req HeadlessTurnRequest) (HeadlessTurnResult, error)
}

// HeadlessTurnResult 是一次 headless 运行的结果（J3：用于运行历史与结果回投）。
type HeadlessTurnResult struct {
	// SessionID 是本次运行所用的临时会话（可空）。
	SessionID string
	// Output 是运行产出的文本（可空）。
	Output string
}
