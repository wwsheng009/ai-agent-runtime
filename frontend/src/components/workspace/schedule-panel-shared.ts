// P3：自动化任务面板共享的纯函数（格式化/分组/频次文案 key）。
// 保持与 jobs-panel-shared 同风格：无 React 依赖，便于单测。

import type {
  RuntimeScheduleKind,
  RuntimeScheduleTask,
} from "@/types/runtime";

/** live = 仍会驱动投递的任务。 */
export function isLiveScheduleStatus(status: string): boolean {
  return status === "active";
}

/** 分区：活动在前（按 scheduledAt 升序），未运行在后。 */
export function splitScheduleTasks(tasks: RuntimeScheduleTask[]): {
  active: RuntimeScheduleTask[];
  inactive: RuntimeScheduleTask[];
} {
  const active: RuntimeScheduleTask[] = [];
  const inactive: RuntimeScheduleTask[] = [];
  for (const task of tasks) {
    if (isLiveScheduleStatus(task.status)) {
      active.push(task);
    } else {
      inactive.push(task);
    }
  }
  return { active, inactive };
}

/** kind → i18n key（`panels.schedule.kind.*`）；模板字面量类型让 t() 通过。 */
export function scheduleKindLabelKey(
  kind: RuntimeScheduleKind,
): `panels.schedule.kind.${RuntimeScheduleKind}` {
  return `panels.schedule.kind.${kind}` as const;
}

/** 频次摘要（不含时区/日期细节；详细规则在详情里展示）。 */
export function scheduleFrequencySummary(task: RuntimeScheduleTask): string {
  switch (task.kind) {
    case "after":
      return `${task.afterSeconds ?? 0}s`;
    case "at":
      return task.scheduledAt;
    case "every":
      return `${task.everySeconds ?? 0}s`;
    case "daily":
      return task.time ?? "";
    case "weekly":
      return `${task.time ?? ""} (${(task.weekdays ?? []).join(",")})`;
    case "cron":
      return task.expression ?? "";
    default:
      return "";
  }
}

/** UTC 目标 → 本地展示（后端一律 UTC，展示在浏览器时区；方案 F17）。 */
export function formatScheduleTimestamp(value: string): string {
  if (!value) {
    return "";
  }
  const parsed = new Date(value);
  if (Number.isNaN(parsed.getTime())) {
    return value;
  }
  return parsed.toLocaleString();
}

/** 下次运行的相对时长（到点前为正）。 */
export function formatScheduleRelative(value: string, nowMs: number): string {
  const parsed = new Date(value).getTime();
  if (Number.isNaN(parsed)) {
    return "";
  }
  const deltaMs = parsed - nowMs;
  const absSeconds = Math.abs(Math.round(deltaMs / 1000));
  const sign = deltaMs >= 0 ? "" : "-";
  if (absSeconds < 60) {
    return `${sign}${absSeconds}s`;
  }
  const minutes = Math.floor(absSeconds / 60);
  if (minutes < 60) {
    return `${sign}${minutes}m`;
  }
  const hours = Math.floor(minutes / 60);
  if (hours < 24) {
    return `${sign}${hours}h${minutes % 60 ? ` ${minutes % 60}m` : ""}`;
  }
  const days = Math.floor(hours / 24);
  return `${sign}${days}d`;
}

/** schedule_* 运行时事件是否应触发刷新（事件名前缀判定）。 */
export function isScheduleRuntimeEvent(eventType?: string): boolean {
  return typeof eventType === "string" && eventType.startsWith("schedule");
}
