// P3：自动化任务（Schedule）面板：创建/编辑表单的类型、常量与纯函数（无 React 依赖）。
// 由 components/workspace/schedule-panel.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import type {
  RuntimeScheduleCreateRequest,
  RuntimeScheduleKind,
  RuntimeScheduleRecord,
  RuntimeScheduleTask,
  RuntimeScheduleTargetKind,
  RuntimeScheduleTimingChange,
  RuntimeScheduleUpdateRequest,
} from "@/types/runtime";

/** 人工创建表单的 kind 选项：覆盖全部六种规则（after 用分钟数表达）。 */
export const createKinds: RuntimeScheduleKind[] = [
  "after",
  "at",
  "every",
  "daily",
  "weekly",
  "cron",
];

export type CreateFormState = {
  sessionId: string;
  title: string;
  prompt: string;
  kind: RuntimeScheduleKind;
  /** after 分钟数 / every 分钟数。 */
  amount: string;
  /** `datetime-local` 的本地墙上时间；仅 at 使用。 */
  atTime: string;
  /** daily/weekly 的 HH:mm[:ss]；与 atTime 隔离，避免切换规则残留 ISO 值。 */
  clockTime: string;
  timeZone: string;
  weekdays: number[];
  cron: string;
  /** J0/J4：作业目标（非 session 目标必填）。 */
  goal: string;
  /** J0：执行步骤（每行一步，可选）。 */
  plan: string;
  /** J1：执行上下文（session 投递进会话；headless/workspace 不绑会话）。 */
  targetKind: RuntimeScheduleTargetKind;
  /** J1：workspace 目标必须显式指定工作区路径。 */
  targetWorkspace: string;
  /** J1：workspace/headless 可选执行 profile。 */
  targetProfile: string;
};

export function emptyCreateForm(sessionId = ""): CreateFormState {
  return {
    sessionId,
    title: "",
    prompt: "",
    kind: "after",
    amount: "30",
    atTime: "",
    clockTime: "",
    timeZone: Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC",
    weekdays: [],
    cron: "",
    goal: "",
    plan: "",
    targetKind: "session",
    targetWorkspace: "",
    targetProfile: "",
  };
}

export type ScheduleFormField =
  | "title"
  | "prompt"
  | "sessionId"
  | "goal"
  | "targetWorkspace"
  | "amount"
  | "atTime"
  | "clockTime"
  | "timeZone"
  | "weekdays"
  | "cron";

export type ScheduleFormErrors = Partial<Record<ScheduleFormField, string>>;

const CLOCK_TIME_PATTERN = /^(?:[01]\d|2[0-3]):[0-5]\d(?::[0-5]\d)?$/;

/** 返回可直接绑定到输入控件的字段错误码；不负责本地化。 */
export function validateCreateForm(form: CreateFormState): ScheduleFormErrors {
  const errors: ScheduleFormErrors = {};
  if (!form.title.trim()) errors.title = "required";
  if (!form.prompt.trim()) errors.prompt = "required";
  if (form.targetKind === "session" && !form.sessionId.trim()) {
    errors.sessionId = "required";
  }
  if (form.targetKind !== "session" && !form.goal.trim()) {
    errors.goal = "required";
  }
  if (form.targetKind === "workspace" && !form.targetWorkspace.trim()) {
    errors.targetWorkspace = "required";
  }

  switch (form.kind) {
    case "after": {
      const minutes = Number(form.amount);
      if (!Number.isFinite(minutes) || minutes <= 0) errors.amount = "positive";
      break;
    }
    case "every": {
      const minutes = Number(form.amount);
      if (!Number.isFinite(minutes) || minutes < 1) errors.amount = "positive";
      break;
    }
    case "at": {
      const parsed = new Date(form.atTime);
      if (!form.atTime || Number.isNaN(parsed.getTime())) errors.atTime = "required";
      break;
    }
    case "daily":
      if (!CLOCK_TIME_PATTERN.test(form.clockTime.trim())) errors.clockTime = "clock";
      if (!form.timeZone.trim()) errors.timeZone = "required";
      break;
    case "weekly":
      if (!CLOCK_TIME_PATTERN.test(form.clockTime.trim())) errors.clockTime = "clock";
      if (!form.timeZone.trim()) errors.timeZone = "required";
      if (form.weekdays.length === 0) errors.weekdays = "required";
      break;
    case "cron":
      if (!form.cron.trim()) errors.cron = "required";
      if (!form.timeZone.trim()) errors.timeZone = "required";
      break;
  }
  return errors;
}

function timingFieldFor(form: CreateFormState): ScheduleFormField {
  switch (form.kind) {
    case "after":
    case "every":
      return "amount";
    case "at":
      return "atTime";
    case "daily":
    case "weekly":
      return "clockTime";
    case "cron":
      return "cron";
  }
}

/** 把后端稳定错误码映射到最可能需要修正的表单字段。 */
export function scheduleErrorFields(
  code: string | undefined,
  form: CreateFormState,
): ScheduleFormErrors {
  switch (code) {
    case "invalid_prompt":
      return { title: "invalid", prompt: "invalid" };
    case "invalid_selector":
      return form.targetKind === "session"
        ? { sessionId: "invalid" }
        : { [timingFieldFor(form)]: "invalid" };
    case "invalid_rule":
      return {
        [timingFieldFor(form)]: "invalid",
        ...(form.targetKind === "workspace" && !form.targetWorkspace.trim()
          ? { targetWorkspace: "required" }
          : {}),
        ...(form.targetKind !== "session" && !form.goal.trim()
          ? { goal: "required" }
          : {}),
      };
    case "invalid_time_zone":
      return { timeZone: "invalid" };
    case "not_future":
      return { atTime: "invalid" };
    case "time_out_of_range":
      return { [timingFieldFor(form)]: "invalid" };
    case "frequency_too_high":
      return { amount: "invalid" };
    case "subagent_session":
      return { sessionId: "invalid" };
    default:
      return {};
  }
}

/** 把本地 datetime-local 值格式化成带当前本地 UTC offset 的 RFC3339。 */
export function localDateTimeToRfc3339(value: string): string | null {
  const parsed = new Date(value);
  if (!value || Number.isNaN(parsed.getTime())) return null;
  const offsetMinutes = -parsed.getTimezoneOffset();
  const sign = offsetMinutes >= 0 ? "+" : "-";
  const absoluteOffset = Math.abs(offsetMinutes);
  const offsetHours = String(Math.floor(absoluteOffset / 60)).padStart(2, "0");
  const offsetRemainder = String(absoluteOffset % 60).padStart(2, "0");
  const parts = [
    parsed.getFullYear(),
    String(parsed.getMonth() + 1).padStart(2, "0"),
    String(parsed.getDate()).padStart(2, "0"),
  ];
  const clock = [
    String(parsed.getHours()).padStart(2, "0"),
    String(parsed.getMinutes()).padStart(2, "0"),
    String(parsed.getSeconds()).padStart(2, "0"),
  ].join(":");
  return `${parts[0]}-${parts[1]}-${parts[2]}T${clock}${sign}${offsetHours}:${offsetRemainder}`;
}

/** 由表单状态派生创建负载（恰好一个选择器；后端服务层做最终校验）。 */
export function createRequestFrom(
  form: CreateFormState,
): RuntimeScheduleCreateRequest | null {
  const sessionId = form.sessionId.trim();
  const title = form.title.trim();
  const prompt = form.prompt.trim();
  if (Object.keys(validateCreateForm(form)).length > 0) {
    return null;
  }
  const plan = form.plan
    .split("\n")
    .map((line) => line.trim())
    .filter((line) => line.length > 0);
  const base = {
    sessionId,
    title,
    prompt,
    ...(form.goal.trim() ? { goal: form.goal.trim() } : {}),
    ...(plan.length > 0 ? { plan } : {}),
    ...(form.targetKind !== "session" ? { target_kind: form.targetKind } : {}),
    ...(form.targetKind === "workspace" && form.targetWorkspace.trim()
      ? { target_workspace: form.targetWorkspace.trim() }
      : {}),
    ...(form.targetKind !== "session" && form.targetProfile.trim()
      ? { target_profile: form.targetProfile.trim() }
      : {}),
  };
  switch (form.kind) {
    case "after": {
      const minutes = Number(form.amount);
      if (!Number.isFinite(minutes) || minutes <= 0) {
        return null;
      }
      return {
        ...base,
        after_seconds: Math.round(minutes * 60),
      };
    }
    case "every": {
      const minutes = Number(form.amount);
      if (!Number.isFinite(minutes) || minutes < 1) {
        return null;
      }
      return {
        ...base,
        every_seconds: Math.round(minutes * 60),
      };
    }
    case "at": {
      const at = localDateTimeToRfc3339(form.atTime);
      if (!at) {
        return null;
      }
      return { ...base, at };
    }
    case "daily": {
      return {
        ...base,
        daily: { time: form.clockTime.trim(), time_zone: form.timeZone.trim() },
      };
    }
    case "weekly": {
      return {
        ...base,
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
        ...base,
        cron: { expression: form.cron.trim(), time_zone: form.timeZone.trim() },
      };
    }
    default:
      return null;
  }
}

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
    target_session_id:
      form.targetKind === "session" ? form.sessionId.trim() : undefined,
    ...(change ? { change } : {}),
  };
}
