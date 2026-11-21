// P3：自动化任务表单的本地校验与错误码到字段的映射（纯函数，无 React 依赖）。

import type {
  CreateFormState,
  ScheduleFormErrors,
  ScheduleFormField,
} from "./form-state-types";

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