// 自动化任务编辑表单 ↔ 任务记录的派生与时间规则比较（纯函数）。

import type {
  RuntimeScheduleRecord,
  RuntimeScheduleTask,
  RuntimeScheduleTimingChange,
  RuntimeScheduleUpdateRequest,
} from "@/types/runtime";

import type { CreateFormState } from "./form-state-types";
import {
  localDateTimeToRfc3339,
  validateCreateForm,
} from "./form-state-validation";

function defaultTimeZone(): string {
  return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
}

function minutesFromSeconds(seconds: number | undefined): string {
  if (!seconds || seconds <= 0) {
    return "";
  }
  const minutes = seconds / 60;
  return Number.isInteger(minutes)
    ? String(minutes)
    : String(Number(minutes.toFixed(2)));
}

function clockTime(value: string | undefined): string {
  return (value ?? "").slice(0, 8);
}

/** 把后端 UTC 时间转成 `<input type="datetime-local">` 的本地值。 */
function localDateTimeInput(value: string): string {
  const parsed = new Date(value);
  if (Number.isNaN(parsed.getTime())) {
    return "";
  }
  const local = new Date(
    parsed.getTime() - parsed.getTimezoneOffset() * 60_000,
  ).toISOString();
  const withSeconds = local.slice(0, 19);
  return withSeconds.endsWith(":00") ? local.slice(0, 16) : withSeconds;
}

/** 由已有任务派生编辑表单；目标会话在缺失时回落创建者会话。 */
export function editFormFromTask(
  task: RuntimeScheduleTask,
  fallbackSessionId = "",
): CreateFormState {
  return {
    sessionId: task.sessionId || task.createdBySessionId || fallbackSessionId,
    title: task.title,
    prompt: task.prompt,
    kind: task.kind,
    amount:
      task.kind === "after"
        ? minutesFromSeconds(task.afterSeconds)
        : task.kind === "every"
          ? minutesFromSeconds(task.everySeconds)
          : "",
    atTime:
      task.kind === "at"
        ? localDateTimeInput(task.scheduledAt)
        : "",
    clockTime:
      task.kind === "daily" || task.kind === "weekly"
        ? clockTime(task.time)
        : "",
    timeZone: task.timeZone?.trim() || defaultTimeZone(),
    weekdays: [...(task.weekdays ?? [])],
    cron: task.expression ?? "",
    goal: task.goal ?? "",
    plan: (task.plan ?? []).join("\n"),
    targetKind: task.targetKind ?? "session",
    targetWorkspace: task.targetWorkspace ?? "",
    targetProfile: task.targetProfile ?? "",
    provider: task.provider ?? "",
    model: task.model ?? "",
    reasoningEffort: task.reasoningEffort ?? "",
  };
}

/** 构造编辑保存时的完整时间替换负载；非法输入返回 null。 */
export function timingChangeFromForm(
  form: CreateFormState,
): RuntimeScheduleTimingChange | null {
  switch (form.kind) {
    case "after": {
      const minutes = Number(form.amount);
      if (!Number.isFinite(minutes) || minutes <= 0) {
        return null;
      }
      return { kind: "after", after_seconds: Math.round(minutes * 60) };
    }
    case "every": {
      const minutes = Number(form.amount);
      if (!Number.isFinite(minutes) || minutes < 1) {
        return null;
      }
      return { kind: "every", every_seconds: Math.round(minutes * 60) };
    }
    case "at": {
      const at = localDateTimeToRfc3339(form.atTime);
      if (!at) {
        return null;
      }
      return { kind: "at", at };
    }
    case "daily": {
      return {
        kind: "daily",
        daily: { time: form.clockTime.trim(), time_zone: form.timeZone.trim() },
      };
    }
    case "weekly": {
      if (
        !form.clockTime.trim() ||
        !form.timeZone.trim() ||
        form.weekdays.length === 0
      ) {
        return null;
      }
      return {
        kind: "weekly",
        weekly: {
          time: form.clockTime.trim(),
          time_zone: form.timeZone.trim(),
          weekdays: [...form.weekdays].sort((a, b) => a - b),
        },
      };
    }
    case "cron": {
      if (!form.cron.trim() || !form.timeZone.trim()) {
        return null;
      }
      return {
        kind: "cron",
        cron: {
          expression: form.cron.trim(),
          time_zone: form.timeZone.trim(),
        },
      };
    }
    default:
      return null;
  }
}

function sameWeekdays(left: number[] | undefined, right: number[]): boolean {
  const a = [...(left ?? [])].sort((x, y) => x - y);
  const b = [...right].sort((x, y) => x - y);
  return a.length === b.length && a.every((value, index) => value === b[index]);
}

/** 仅在时间规则确实变化时请求后端重算，避免无改动保存重置倒计时。 */
export function timingRuleChanged(
  task: RuntimeScheduleTask,
  form: CreateFormState,
): boolean {
  if (task.kind !== form.kind) {
    return true;
  }
  switch (form.kind) {
    case "after": {
      const minutes = Number(form.amount);
      return (
        !Number.isFinite(minutes) ||
        Math.round(minutes * 60) !== (task.afterSeconds ?? 0)
      );
    }
    case "every": {
      const minutes = Number(form.amount);
      return (
        !Number.isFinite(minutes) ||
        Math.round(minutes * 60) !== (task.everySeconds ?? 0)
      );
    }
    case "at": {
      const next = new Date(form.atTime).getTime();
      const current = new Date(task.scheduledAt).getTime();
      if (Number.isNaN(next) || Number.isNaN(current)) {
        return true;
      }
      return Math.floor(next / 1000) !== Math.floor(current / 1000);
    }
    case "daily":
      return (
        clockTime(form.clockTime) !== clockTime(task.time) ||
        form.timeZone.trim() !== (task.timeZone ?? "").trim()
      );
    case "weekly":
      return (
        clockTime(form.clockTime) !== clockTime(task.time) ||
        form.timeZone.trim() !== (task.timeZone ?? "").trim() ||
        !sameWeekdays(task.weekdays, form.weekdays)
      );
    case "cron":
      return (
        form.cron.trim() !== (task.expression ?? "").trim() ||
        form.timeZone.trim() !== (task.timeZone ?? "").trim()
      );
    default:
      return true;
  }
}

export function recordFromTask(
  task: RuntimeScheduleTask,
): RuntimeScheduleRecord {
  return {
    id: task.id,
    kind: task.kind,
    title: task.title,
    prompt: task.prompt,
    goal: task.goal || undefined,
    plan: task.plan && task.plan.length > 0 ? [...task.plan] : undefined,
    scheduledAt: task.scheduledAt,
    afterSeconds: task.afterSeconds,
    everySeconds: task.everySeconds,
    time: task.time,
    timeZone: task.timeZone,
    weekdays: task.weekdays,
    expression: task.expression,
  };
}

/**
 * 由编辑表单构造 update 负载。返回类型不含 sessionId：控制器会按任务归属
 * 选择实际的管理会话（非会话目标允许空，按任务 id 管理），避免前端伪造 owner。
 */
export function updateRequestFrom(
  form: CreateFormState,
  task: RuntimeScheduleTask,
): Omit<RuntimeScheduleUpdateRequest, "sessionId"> | null {
  const title = form.title.trim();
  const prompt = form.prompt.trim();
  if (!title || !prompt) return null;
  if (Object.keys(validateCreateForm(form)).length > 0) {
    return null;
  }

  const ruleChanged = timingRuleChanged(task, form);
  const change = ruleChanged ? timingChangeFromForm(form) : null;
  if (ruleChanged && !change) {
    return null;
  }
  const plan = form.plan
    .split("\n")
    .map((line) => line.trim())
    .filter((line) => line.length > 0);

  return {
    id: task.id,
    expected: recordFromTask(task),
    title,
    prompt,
    goal: form.goal.trim(),
    plan,
    target_kind: form.targetKind,
    target_workspace:
      form.targetKind === "workspace" ? form.targetWorkspace.trim() : "",
    target_profile:
      form.targetKind === "session" ? "" : form.targetProfile.trim(),
    provider: form.targetKind === "session" ? "" : form.provider.trim(),
    model: form.targetKind === "session" ? "" : form.model.trim(),
    reasoning_effort:
      form.targetKind === "session" ? "" : form.reasoningEffort.trim(),
    target_session_id:
      form.targetKind === "session" ? form.sessionId.trim() : undefined,
    ...(change ? { change } : {}),
  };
}