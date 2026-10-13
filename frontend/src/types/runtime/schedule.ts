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

/**
 * 执行上下文（J0/J1）：session 投递进绑定会话（默认）；workspace 绑工作区；
 * headless 不绑会话（结果回投创建者）。
 */
export type RuntimeScheduleTargetKind = "session" | "workspace" | "headless";

/** 一条提醒的规则与已提交目标（`scheduledAt` 为 UTC RFC3339）。 */
export type RuntimeScheduleRecord = {
  id: string;
  kind: RuntimeScheduleKind;
  title: string;
  prompt: string;
  /** 作业目标（J0；空表示沿用 prompt 语义）。 */
  goal?: string;
  /** 声明式执行步骤（J0）。 */
  plan?: string[];
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
  /** 作业目标（J0；空表示沿用 prompt 语义）。 */
  goal?: string;
  /** 声明式执行步骤（J0）。 */
  plan?: string[];
  scheduledAt: string;
  afterSeconds?: number;
  everySeconds?: number;
  time?: string;
  timeZone?: string;
  weekdays?: number[];
  expression?: string;
  sessionId: string;
  status: RuntimeScheduleStatus;
  /** 创建来源：model=模型工具；manual=人工管理端（旧数据回落 model）。 */
  origin?: RuntimeScheduleOrigin;
  /** 执行上下文（J1；缺省回落 session）。 */
  targetKind?: RuntimeScheduleTargetKind;
  targetWorkspace?: string;
  targetProfile?: string;
  /** 创建者会话（J3；非 session 目标也保留，用于结果回投）。 */
  createdBySessionId?: string;
  lastDelivery?: {
    scheduledAt: string;
    deliveredAt: string;
    messageId: string;
  } | null;
};

/** 创建来源（与后端 `schedule.Origin` 同形）。 */
export type RuntimeScheduleOrigin = "model" | "manual";

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

/** 一次作业运行的结果状态（J3）。 */
export type RuntimeScheduleJobRunStatus = "done" | "failed";

/** 一次作业运行记录（J3：headless/workspace 作业的执行历史）。 */
export type RuntimeScheduleJobRun = {
  id: number;
  scheduleId: string;
  status: RuntimeScheduleJobRunStatus;
  output?: string;
  error?: string;
  /** 本次运行所用的临时会话（headless/workspace；可空）。 */
  sessionId?: string;
  startedAt: string;
  finishedAt: string;
};

export type RuntimeScheduleJobRunsResponse = {
  id: string;
  runs: RuntimeScheduleJobRun[];
  count: number;
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

/** 人工创建请求（恰好一个选择器；与后端 `scheduleCreateRequest` 同形）。 */
export type RuntimeScheduleCreateRequest = {
  sessionId: string;
  title: string;
  prompt: string;
  after_seconds?: number;
  at?: string;
  every_seconds?: number;
  daily?: { time: string; time_zone: string };
  weekly?: { time: string; time_zone: string; weekdays: number[] };
  cron?: { expression: string; time_zone: string };
  /** J0/J4：作业目标与计划（可选）。 */
  goal?: string;
  plan?: string[];
  /** J1：执行上下文（可选；非 session 目标不绑会话且必须有 goal）。 */
  target_kind?: RuntimeScheduleTargetKind;
  target_workspace?: string;
  target_profile?: string;
};

export type RuntimeScheduleCreateResponse = {
  record: RuntimeScheduleRecord;
  origin: RuntimeScheduleOrigin;
};
