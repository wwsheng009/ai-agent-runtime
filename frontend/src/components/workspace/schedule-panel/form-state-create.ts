// 自动化任务创建表单 → 创建请求负载（纯函数）。

import type { RuntimeScheduleCreateRequest } from "@/types/runtime";

import type { CreateFormState } from "./form-state-types";
import {
  localDateTimeToRfc3339,
  validateCreateForm,
} from "./form-state-validation";

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
    ...(form.targetKind !== "session" && form.provider.trim()
      ? { provider: form.provider.trim() }
      : {}),
    ...(form.targetKind !== "session" && form.model.trim()
      ? { model: form.model.trim() }
      : {}),
    ...(form.targetKind !== "session" && form.reasoningEffort.trim()
      ? { reasoning_effort: form.reasoningEffort.trim() }
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