// P3：自动化任务面板共享的纯函数（格式化/分组/频次文案 key）。
// 保持与 jobs-panel-shared 同风格：无 React 依赖，便于单测。

import type {
  RuntimeScheduleKind,
  RuntimeScheduleStatus,
  RuntimeScheduleTask,
} from "@/types/runtime";

/** live = 仍会驱动投递的任务。 */
export function isLiveScheduleStatus(status: string): boolean {
  return status === "active";
}

/** paused 是非终态但不会驱动投递；恢复前保持专用分区。 */
export function isPausedScheduleStatus(status: string): boolean {
  return status === "paused";
}

/** i18n 键：`panels.schedule.status<Active|Paused|Inactive|Blocked|Failed>`。 */
export function scheduleStatusLabelKey(
  status: RuntimeScheduleStatus,
): `panels.schedule.${
  | "statusActive"
  | "statusPaused"
  | "statusInactive"
  | "statusBlocked"
  | "statusFailed"}` {
  if (status === "active") {
    return "panels.schedule.statusActive";
  }
  if (status === "paused") {
    return "panels.schedule.statusPaused";
  }
  if (status === "blocked") {
    return "panels.schedule.statusBlocked";
  }
  if (status === "failed") {
    return "panels.schedule.statusFailed";
  }
  return "panels.schedule.statusInactive";
}

/** 分区：活动在前，暂停居中，已结束在后。 */
export function splitScheduleTasks(tasks: RuntimeScheduleTask[]): {
  active: RuntimeScheduleTask[];
  paused: RuntimeScheduleTask[];
  inactive: RuntimeScheduleTask[];
  blocked: RuntimeScheduleTask[];
  failed: RuntimeScheduleTask[];
} {
  const active: RuntimeScheduleTask[] = [];
  const paused: RuntimeScheduleTask[] = [];
  const inactive: RuntimeScheduleTask[] = [];
  const blocked: RuntimeScheduleTask[] = [];
  const failed: RuntimeScheduleTask[] = [];
  for (const task of tasks) {
    if (isLiveScheduleStatus(task.status)) {
      active.push(task);
    } else if (isPausedScheduleStatus(task.status)) {
      paused.push(task);
    } else if (task.status === "blocked") {
      blocked.push(task);
    } else if (task.status === "failed") {
      failed.push(task);
    } else {
      inactive.push(task);
    }
  }
  return { active, paused, inactive, blocked, failed };
}

/** kind → i18n key（`panels.schedule.kind.*`）；模板字面量类型让 t() 通过。 */
export function scheduleKindLabelKey(
  kind: RuntimeScheduleKind,
): `panels.schedule.kind.${RuntimeScheduleKind}` {
  return `panels.schedule.kind.${kind}` as const;
}

function formatDuration(
  seconds: number,
  mode: "after" | "every",
  language = "zh-CN",
): string {
  const safeSeconds = Math.max(0, Math.round(seconds));
  const zh = language.toLowerCase().startsWith("zh");
  let value: number;
  let unit: string;
  if (safeSeconds >= 86_400 && safeSeconds % 86_400 === 0) {
    value = safeSeconds / 86_400;
    unit = zh ? "天" : "day";
  } else if (safeSeconds >= 3_600 && safeSeconds % 3_600 === 0) {
    value = safeSeconds / 3_600;
    unit = zh ? "小时" : "hour";
  } else if (safeSeconds >= 60 && safeSeconds % 60 === 0) {
    value = safeSeconds / 60;
    unit = zh ? "分钟" : "minute";
  } else {
    value = safeSeconds;
    unit = zh ? "秒" : "second";
  }
  const number = String(value);
  if (zh) {
    return mode === "after" ? `${number} ${unit}后` : `每 ${number} ${unit}`;
  }
  const plural = value === 1 ? unit : `${unit}s`;
  return mode === "after" ? `in ${number} ${plural}` : `every ${number} ${plural}`;
}

/** 频次摘要（不含时区/日期细节；详细规则在详情里展示）。 */
export function scheduleFrequencySummary(
  task: RuntimeScheduleTask,
  language = "zh-CN",
  nowMs = Date.now(),
): string {
  switch (task.kind) {
    case "after": {
      // "N 分钟后" is a creation-time rule, not a live countdown. Once the
      // one-shot target has passed, showing it again is misleading—especially
      // for paused/blocked rows where the target line already says overdue.
      const target = new Date(task.scheduledAt).getTime();
      if (Number.isFinite(target) && target <= nowMs) {
        return language.toLowerCase().startsWith("zh") ? "已到期" : "Due";
      }
      return formatDuration(task.afterSeconds ?? 0, "after", language);
    }
    case "at":
      return task.scheduledAt;
    case "every":
      return formatDuration(task.everySeconds ?? 0, "every", language);
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

/** 下次运行的相对时长；过去的目标明确显示为逾期而不是负数。 */
export function formatScheduleRelative(
  value: string,
  nowMs: number,
  language = "zh-CN",
): string {
  const parsed = new Date(value).getTime();
  if (Number.isNaN(parsed)) {
    return "";
  }
  const deltaMs = parsed - nowMs;
  const absSeconds = Math.abs(Math.round(deltaMs / 1000));
  const zh = language.toLowerCase().startsWith("zh");
  const format = (amount: string) =>
    deltaMs < 0 ? (zh ? `已逾期 ${amount}` : `Overdue by ${amount}`) : amount;
  if (absSeconds < 60) {
    return format(`${absSeconds}s`);
  }
  const minutes = Math.floor(absSeconds / 60);
  if (minutes < 60) {
    return format(`${minutes}m`);
  }
  const hours = Math.floor(minutes / 60);
  if (hours < 24) {
    return format(`${hours}h${minutes % 60 ? ` ${minutes % 60}m` : ""}`);
  }
  const days = Math.floor(hours / 24);
  return format(`${days}d`);
}

/** schedule_* 运行时事件是否应触发刷新（事件名前缀判定）。 */
export function isScheduleRuntimeEvent(eventType?: string): boolean {
  return typeof eventType === "string" && eventType.startsWith("schedule");
}
