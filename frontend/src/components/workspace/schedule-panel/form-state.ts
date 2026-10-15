// P3：自动化任务（Schedule）面板：创建/编辑表单的类型、常量与纯函数（无 React 依赖）。
// 由 components/workspace/schedule-panel.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import type {
  RuntimeScheduleCreateRequest,
  RuntimeScheduleKind,
  RuntimeScheduleTask,
  RuntimeScheduleTargetKind,
  RuntimeScheduleTimingChange,
} from "@/types/runtime";

export type DraftState = {
  title: string;
  prompt: string;
  time: string;
  timeZone: string;
};

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
  /** after 分钟数 / every 分钟数 / at 本地时间输入（转 RFC3339）。 */
  amount: string;
  time: string;
  timeZone: string;
  weekdays: number[];
  cron: string;
  /** J0/J4：作业目标（非 session 目标必填）。 */
  goal: string;
  /** J0：执行步骤（每行一步，可选）。 */
  plan: string;
  /** J1：执行上下文（session 投递进会话；headless/workspace 不绑会话）。 */
  targetKind: RuntimeScheduleTargetKind;
};

export function emptyCreateForm(): CreateFormState {
  return {
    sessionId: "",
    title: "",
    prompt: "",
    kind: "after",
    amount: "30",
    time: "",
    timeZone: Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC",
    weekdays: [],
    cron: "",
    goal: "",
    plan: "",
    targetKind: "session",
  };
}

/** 由表单状态派生创建负载（恰好一个选择器；后端服务层做最终校验）。 */
export function createRequestFrom(
  form: CreateFormState,
): RuntimeScheduleCreateRequest | null {
  const sessionId = form.sessionId.trim();
  const title = form.title.trim();
  const prompt = form.prompt.trim();
  if (!title || !prompt) {
    return null;
  }
  // J1：仅 session 目标要求 session_id；非 session 目标必须有 goal（后端同样校验）。
  if (form.targetKind === "session" && !sessionId) {
    return null;
  }
  if (form.targetKind !== "session" && !form.goal.trim()) {
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
      // 本地时间输入（datetime-local 语义）转带偏移的 RFC3339。
      const parsed = new Date(form.time);
      if (Number.isNaN(parsed.getTime())) {
        return null;
      }
      return { ...base, at: parsed.toISOString() };
    }
    case "daily": {
      if (!form.time.trim() || !form.timeZone.trim()) {
        return null;
      }
      return {
        ...base,
        daily: { time: form.time.trim(), time_zone: form.timeZone.trim() },
      };
    }
    case "weekly": {
      if (!form.time.trim() || !form.timeZone.trim() || form.weekdays.length === 0) {
        return null;
      }
      return {
        ...base,
        weekly: {
          time: form.time.trim(),
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

export function draftFromTask(task: RuntimeScheduleTask): DraftState {
  return {
    title: task.title,
    prompt: task.prompt,
    time: (task.time ?? "").slice(0, 8),
    timeZone: task.timeZone ?? "",
  };
}

/** 从记录派生时间替换负载（仅钟表规则可编辑时间；after/at/every 保持原 kind）。 */
export function timingChangeFrom(
  task: RuntimeScheduleTask,
  draft: DraftState,
): RuntimeScheduleTimingChange | undefined {
  if (task.kind !== "daily" && task.kind !== "weekly") {
    return undefined;
  }
  if (!draft.time.trim() || !draft.timeZone.trim()) {
    return undefined;
  }
  if (task.kind === "daily") {
    return {
      kind: "daily",
      daily: { time: draft.time.trim(), time_zone: draft.timeZone.trim() },
    };
  }
  return {
    kind: "weekly",
    weekly: {
      time: draft.time.trim(),
      time_zone: draft.timeZone.trim(),
      weekdays: task.weekdays ?? [],
    },
  };
}
