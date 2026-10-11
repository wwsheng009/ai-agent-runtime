package schedule

// EventPublisher 是 schedule.changed 事件的发布接缝（方案 §9.1）：
// 任务变更（create/update/delete/投递推进）提交后发布一次无载荷通知，
// 前端据此刷新目录。nil / 发布失败都不影响管理写本身（通知不是事务参与者）。
type EventPublisher func()

// nopPublisher 是未注入发布器时的空实现。
func nopPublisher() {}
