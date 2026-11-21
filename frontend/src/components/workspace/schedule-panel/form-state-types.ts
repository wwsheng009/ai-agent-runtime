// P3：自动化任务（Schedule）面板：创建/编辑表单的类型、常量与空表单工厂。
// 由 components/workspace/schedule-panel.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import type {
  RuntimeScheduleKind,
  RuntimeScheduleTargetKind,
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
  provider: string;
  model: string;
  reasoningEffort: string;
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
    provider: "",
    model: "",
    reasoningEffort: "",
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