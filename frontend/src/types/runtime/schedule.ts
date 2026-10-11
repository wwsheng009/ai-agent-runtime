// P3：自动化任务（Schedule）运行时域类型。
//
// 数据源：`/api/runtime/schedule/*`（backend/internal/schedule）。
// 后端 `schedule.Record`/`Task`/`CatalogEntry` 均有 json tag（camelCase），
// 前端仍经 `api/runtime/schedule.ts` 归一化后使用，不在组件里读原始键。

export type RuntimeScheduleKind =
  | "after"
  | "at"
  | "every"
  | "daily"
  | "weekly"
  | "cron";

export type RuntimeScheduleStatus = "active" | "inactive";

export type RuntimeScheduleState = "scheduled" | "overdue";

/** 一条提醒的规则与已提交目标（`scheduledAt` 为 UTC RFC3339）。 */
export type RuntimeScheduleRecord = {
  id: string;
  kind: RuntimeScheduleKind;
  title: string;
  prompt: string;
  scheduledAt: string;
  /** kind=after */
  afterSeconds?: number;
  /** kind=every */
  everySeconds?: number;
  /** kind=daily|weekly（HH:mm:ss.SSS） */
  time?: string;
  /** kind=daily|weekly|cron（显式 IANA） */
  timeZone?: string;
  /** kind=weekly（ISO 1..7，升序去重） */
  weekdays?: number[];
  /** kind=cron（规范化五字段） */
  expression?: string;
};

/** 目录条目：记录 + 原会话绑定 + 生命周期 + 最近回执。 */
export type RuntimeScheduleTask = {
  id: string;
  kind: RuntimeScheduleKind;
  title: string;
  prompt: string;
  scheduledAt: string;
  afterSeconds?: number;
  everySeconds?: number;
  time?: string;
  timeZone?: string;
  weekdays?: number[];
  expression?: string;
  sessionId: string;
  status: RuntimeScheduleStatus;
  lastDelivery?: {
    scheduledAt: string;
    deliveredAt: string;
    messageId: string;
  } | null;
};

export type RuntimeScheduleCatalogResponse = {
  tasks: RuntimeScheduleTask[];
  count: number;
};

export type RuntimeScheduleListResponse = {
  tasks: RuntimeScheduleRecord[];
  count: number;
};

/** 已保存投递记录（`deliveredAt` 不代表模型执行完成）。 */
export type RuntimeScheduleDeliveryRecord = {
  scheduledAt: string;
  deliveredAt: string;
  messageId: string;
  prompt?: string;
};

export type RuntimeScheduleHistoryResponse = {
  id: string;
  records: RuntimeScheduleDeliveryRecord[];
  nextBefore?: string;
  earlierRecordsUnavailable: boolean;
  earlierRecordsPruned: boolean;
  retentionDays: number;
  retentionRecords: number;
};

export type RuntimeScheduleDeleteResponse = {
  id: string;
  deleted: boolean;
  code?: string;
};

export type RuntimeScheduleUpdateResponse = {
  id: string;
  updated: boolean;
  record?: RuntimeScheduleRecord;
  code?: string;
  message?: string;
};

/** 时间替换负载（与后端 `TimingChange` 同形）。 */
export type RuntimeScheduleTimingChange = {
  kind: RuntimeScheduleKind;
  at?: string;
  every_seconds?: number;
  daily?: { time: string; time_zone: string };
  weekly?: { time: string; time_zone: string; weekdays: number[] };
  cron?: { expression: string; time_zone: string };
};

export type RuntimeScheduleUpdateRequest = {
  sessionId: string;
  id: string;
  expected: RuntimeScheduleRecord;
  title?: string;
  prompt?: string;
  change?: RuntimeScheduleTimingChange;
};
