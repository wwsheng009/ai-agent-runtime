// Package schedule 实现"自动化任务"（Schedule）的领域层、存储与运行时。
//
// 设计依据：docs/automation-tasks/analysis-and-implementation-plan.md（P0）。
// 命名：本包只服务 schedule_* 工具族；后台作业用 job_*，团队任务用 team_*。
package schedule

import "time"

// Kind 是提醒规则的判别式。
type Kind string

const (
	// KindAfter 是从正延迟创建的一次性提醒。
	KindAfter Kind = "after"
	// KindAt 是从绝对时点创建的一次性提醒。
	KindAt Kind = "at"
	// KindEvery 是按固定经过时间重复的提醒（>= 1 分钟）。
	KindEvery Kind = "every"
	// KindDaily 是每日本地钟表时间提醒（P1）。
	KindDaily Kind = "daily"
	// KindWeekly 是每周本地钟表时间提醒（P1）。
	KindWeekly Kind = "weekly"
	// KindCron 是五字段 Vixie cron 提醒（P1）。
	KindCron Kind = "cron"
)

// Status 是任务的持久生命周期状态。
type Status string

const (
	// StatusActive 表示任务仍会驱动下一次投递。
	StatusActive Status = "active"
	// StatusInactive 表示任务已结束（一次性已投递，或无未来目标）。
	StatusInactive Status = "inactive"
)

// ScheduleState 是从记录与墙钟派生的时间态（与 Status 是两个概念）。
type ScheduleState string

const (
	// StateScheduled 表示目标仍在未来。
	StateScheduled ScheduleState = "scheduled"
	// StateOverdue 表示目标已到点。
	StateOverdue ScheduleState = "overdue"
)

const (
	// MaxTitleLength 是任务标题的字符上限（trim 后）。
	MaxTitleLength = 120
	// MinEveryIntervalSeconds 是固定速率间隔的下限。
	MinEveryIntervalSeconds int64 = 60
	// DeliveryModeHost 是唯一的投递模式：由宿主投递到原会话。
	DeliveryModeHost = "host"
)

// Record 是一条提醒的规则与已提交目标；ScheduledAt 一律为 UTC。
type Record struct {
	ID           string    `json:"id"`
	Kind         Kind      `json:"kind"`
	Title        string    `json:"title"`
	Prompt       string    `json:"prompt"`
	ScheduledAt  time.Time `json:"scheduledAt"`
	AfterSeconds int64     `json:"afterSeconds,omitempty"`
	EverySeconds int64     `json:"everySeconds,omitempty"`
	Time         string    `json:"time,omitempty"`
	TimeZone     string    `json:"timeZone,omitempty"`
	Weekdays     []int     `json:"weekdays,omitempty"`
	Expression   string    `json:"expression,omitempty"`
}

// DeliveryReceipt 是一次收件箱投递回执；它不代表模型执行完成。
type DeliveryReceipt struct {
	ScheduledAt time.Time `json:"scheduledAt"`
	DeliveredAt time.Time `json:"deliveredAt"`
	MessageID   string    `json:"messageId"`
}

// DeliveryRecord 是保存的投递记录，含发送时的提示快照。
type DeliveryRecord struct {
	DeliveryReceipt
	Prompt string `json:"prompt,omitempty"`
}

// DeliveryHistory 是任务的已保存投递记录（旧到新）。
type DeliveryHistory struct {
	Records                   []DeliveryRecord `json:"records"`
	EarlierRecordsUnavailable bool             `json:"earlierRecordsUnavailable"`
	EarlierRecordsPruned      bool             `json:"earlierRecordsPruned,omitempty"`
}

// Task 是持久任务：记录 + 原会话绑定 + 生命周期 + 回执 + CAS 版本。
type Task struct {
	SessionID       string           `json:"sessionId"`
	Record          Record           `json:"record"`
	Status          Status           `json:"status"`
	LastDelivery    *DeliveryReceipt `json:"lastDelivery,omitempty"`
	DeliveryHistory *DeliveryHistory `json:"deliveryHistory,omitempty"`
	StateVersion    int64            `json:"stateVersion"`
}

// View 是模型可见的提醒视图。
type View struct {
	Record
	State        ScheduleState `json:"state"`
	DeliveryMode string        `json:"deliveryMode"`
}

// Occurrence 是一次发生点解析结果。
type Occurrence struct {
	// OccurrenceAt 是最近一次到期（已提交目标或对齐后的发生时点）。
	OccurrenceAt time.Time
	// NextScheduledAt 是下一个未来目标；nil 表示没有可表示的未来目标（转 inactive）。
	NextScheduledAt *time.Time
}

// CreateRequest 是创建提醒的选择器与内容（P0 支持 after/at/every；daily/weekly/cron 归 P1）。
type CreateRequest struct {
	Prompt       string
	Title        string
	AfterSeconds *int64
	At           *string
	EverySeconds *int64
	Daily        *DailyInput
	Weekly       *WeeklyInput
	Cron         *CronInput
}

// DailyInput 是每日本地钟表时间选择器。
type DailyInput struct {
	// Time 是 HH:mm:ss 及可选 1-3 位小数秒。
	Time string `json:"time"`
	// TimeZone 是显式 IANA/UTC 时区。
	TimeZone string `json:"time_zone"`
}

// WeeklyInput 是每周本地钟表时间 + ISO 星期集合选择器。
type WeeklyInput struct {
	Time     string `json:"time"`
	TimeZone string `json:"time_zone"`
	Weekdays []int  `json:"weekdays"`
}

// CronInput 是五字段 Vixie 表达式 + 显式时区选择器。
type CronInput struct {
	Expression string `json:"expression"`
	TimeZone   string `json:"time_zone"`
}

// UpdateContent 是可选的名称/指令替换。
type UpdateContent struct {
	Title  *string
	Prompt *string
}

// TimingChange 是时间替换（P0 支持 at/every；after 仅创建时可用）。
type TimingChange struct {
	Kind         Kind
	At           *string
	EverySeconds *int64
	Daily        *DailyInput
	Weekly       *WeeklyInput
	Cron         *CronInput
}
