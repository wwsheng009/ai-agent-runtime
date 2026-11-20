import {
  ActivityIcon,
  BookOpenCheckIcon,
  CalendarCheck2Icon,
  ClipboardListIcon,
  SparklesIcon,
  type LucideIcon,
} from "lucide-react";
import type { TFunction } from "i18next";

import {
  localDateTimeToRfc3339,
  type CreateFormState,
} from "@/components/workspace/schedule-panel/form-state";
import type {
  RuntimeScheduleKind,
  RuntimeScheduleTargetKind,
  RuntimeScheduleTemplate,
  RuntimeScheduleTemplateRequest,
} from "@/types/runtime";

export type AutomationTemplate = {
  id: string;
  title: string;
  description: string;
  category: string;
  frequency: string;
  icon: LucideIcon;
  form: Partial<CreateFormState>;
  source: RuntimeScheduleTemplate;
};

export type AutomationTemplateForm = RuntimeScheduleTemplateRequest;

export type AutomationTemplateFormErrors = Partial<
  Record<
    | "title"
    | "taskTitle"
    | "prompt"
    | "goal"
    | "targetWorkspace"
    | "amount"
    | "atTime"
    | "clockTime"
    | "timeZone"
    | "weekdays"
    | "cron",
    string
  >
>;

const ICON_BY_CATEGORY: Record<string, LucideIcon> = {
  productivity: CalendarCheck2Icon,
  reporting: ClipboardListIcon,
  maintenance: ActivityIcon,
  knowledge: BookOpenCheckIcon,
};

export function localTimeZone(): string {
  return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
}

function localDateTimeInput(value: string): string {
  if (!value) {
    return "";
  }
  const parsed = new Date(value);
  if (Number.isNaN(parsed.getTime())) {
    return value.slice(0, 16);
  }
  const local = new Date(
    parsed.getTime() - parsed.getTimezoneOffset() * 60_000,
  ).toISOString();
  const withSeconds = local.slice(0, 19);
  return withSeconds.endsWith(":00") ? local.slice(0, 16) : withSeconds;
}

function frequencyFor(
  template: RuntimeScheduleTemplate,
  t: TFunction<"workspace">,
): string {
  const kind = t(`panels.schedule.kind.${template.kind}` as never);
  switch (template.kind) {
    case "after":
    case "every":
      return `${kind} · ${template.amount}`;
    case "at":
      return `${kind} · ${localDateTimeInput(template.atTime) || template.atTime}`;
    case "daily":
    case "weekly":
      return `${kind} · ${template.clockTime}`;
    case "cron":
      return `${kind} · ${template.cron}`;
    default:
      return kind;
  }
}

function categoryFor(category: string, t: TFunction<"workspace">): string {
  const key = category.trim().toLowerCase();
  const labels: Record<string, string> = {
    productivity: t("panels.schedule.templates.categories.productivity"),
    reporting: t("panels.schedule.templates.categories.reporting"),
    maintenance: t("panels.schedule.templates.categories.maintenance"),
    knowledge: t("panels.schedule.templates.categories.knowledge"),
  };
  return (
    labels[key] ??
    (category.trim() || t("panels.schedule.templates.customCategory"))
  );
}

function formFromTemplate(
  template: RuntimeScheduleTemplate,
): Partial<CreateFormState> {
  return {
    title: template.taskTitle || template.title,
    prompt: template.prompt,
    goal: template.goal,
    plan: template.plan,
    kind: template.kind,
    amount: template.amount,
    atTime: template.kind === "at" ? localDateTimeInput(template.atTime) : "",
    clockTime: template.clockTime,
    timeZone: template.timeZone || localTimeZone(),
    weekdays: [...template.weekdays],
    cron: template.cron,
    targetKind: template.targetKind,
    targetWorkspace: template.targetWorkspace,
    targetProfile: template.targetProfile,
    provider: template.provider,
    model: template.model,
    reasoningEffort: template.reasoningEffort,
  };
}

/** Build cards and creation presets from persisted records only. */
export function buildAutomationTemplates(
  records: RuntimeScheduleTemplate[],
  t: TFunction<"workspace">,
): AutomationTemplate[] {
  return records.map((template) => {
    const category = categoryFor(template.category, t);
    return {
      id: template.id,
      title: template.title,
      description: template.description || template.prompt,
      category,
      frequency: frequencyFor(template, t),
      icon:
        ICON_BY_CATEGORY[template.category.trim().toLowerCase()] ??
        SparklesIcon,
      form: formFromTemplate(template),
      source: template,
    };
  });
}

export function emptyAutomationTemplateForm(): AutomationTemplateForm {
  return {
    id: "",
    title: "",
    description: "",
    taskTitle: "",
    category: "",
    prompt: "",
    kind: "after",
    amount: "30",
    atTime: "",
    clockTime: "",
    timeZone: localTimeZone(),
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

export function templateFormFrom(
  template: RuntimeScheduleTemplate,
): AutomationTemplateForm {
  const editable = { ...template };
  delete editable.createdAt;
  delete editable.updatedAt;
  return {
    ...editable,
    atTime:
      template.kind === "at"
        ? localDateTimeInput(template.atTime)
        : template.atTime,
    timeZone: template.timeZone.trim() || localTimeZone(),
    weekdays: [...template.weekdays],
  };
}

function normalizedWeekdays(days: number[]): number[] {
  return Array.from(
    new Set(
      days.filter((day) => Number.isInteger(day) && day >= 1 && day <= 7),
    ),
  ).sort((left, right) => left - right);
}

function validTimeZone(value: string): boolean {
  if (!value.trim()) return false;
  try {
    Intl.DateTimeFormat(undefined, { timeZone: value.trim() });
    return true;
  } catch {
    return false;
  }
}

/** Trim editable fields before crossing the API boundary. */
export function templateRequestFromForm(
  form: AutomationTemplateForm,
): RuntimeScheduleTemplateRequest {
  const atTime =
    form.kind === "at"
      ? (localDateTimeToRfc3339(form.atTime) ?? form.atTime.trim())
      : form.atTime.trim();
  return {
    ...form,
    id: form.id.trim(),
    title: form.title.trim(),
    description: form.description.trim(),
    taskTitle: form.taskTitle.trim(),
    category: form.category.trim(),
    prompt: form.prompt.trim(),
    amount: form.amount.trim(),
    atTime,
    clockTime: form.clockTime.trim(),
    timeZone: form.timeZone.trim(),
    weekdays: normalizedWeekdays(form.weekdays),
    cron: form.cron.trim(),
    goal: form.goal.trim(),
    plan: form.plan.trim(),
    targetKind: form.targetKind,
    targetWorkspace: form.targetWorkspace.trim(),
    targetProfile: form.targetProfile.trim(),
    provider: form.provider.trim(),
    model: form.model.trim(),
    reasoningEffort: form.reasoningEffort.trim(),
  };
}

export function validateAutomationTemplateForm(
  form: AutomationTemplateForm,
): AutomationTemplateFormErrors {
  const errors: AutomationTemplateFormErrors = {};
  if (!form.title.trim()) errors.title = "required";
  if (!form.taskTitle.trim()) errors.taskTitle = "required";
  if (!form.prompt.trim()) errors.prompt = "required";
  if (form.targetKind !== "session" && !form.goal.trim())
    errors.goal = "required";
  if (form.targetKind === "workspace" && !form.targetWorkspace.trim()) {
    errors.targetWorkspace = "required";
  }

  switch (form.kind) {
    case "after":
    case "every": {
      const amount = Number(form.amount);
      if (!Number.isInteger(amount) || amount <= 0) errors.amount = "positive";
      break;
    }
    case "at":
      if (
        !form.atTime.trim() ||
        Number.isNaN(new Date(form.atTime).getTime())
      ) {
        errors.atTime = "required";
      }
      break;
    case "daily":
      if (!/^(?:[01]\d|2[0-3]):[0-5]\d(?::[0-5]\d)?$/.test(form.clockTime))
        errors.clockTime = "clock";
      if (!form.timeZone.trim()) errors.timeZone = "required";
      else if (!validTimeZone(form.timeZone)) errors.timeZone = "invalid";
      break;
    case "weekly":
      if (!/^(?:[01]\d|2[0-3]):[0-5]\d(?::[0-5]\d)?$/.test(form.clockTime))
        errors.clockTime = "clock";
      if (!form.timeZone.trim()) errors.timeZone = "required";
      else if (!validTimeZone(form.timeZone)) errors.timeZone = "invalid";
      if (form.weekdays.length === 0) errors.weekdays = "required";
      else if (
        form.weekdays.some(
          (day) => !Number.isInteger(day) || day < 1 || day > 7,
        ) ||
        new Set(form.weekdays).size !== form.weekdays.length
      ) {
        errors.weekdays = "invalid";
      }
      break;
    case "cron":
      if (!form.cron.trim()) errors.cron = "required";
      if (!form.timeZone.trim()) errors.timeZone = "required";
      else if (!validTimeZone(form.timeZone)) errors.timeZone = "invalid";
      break;
  }
  return errors;
}

export const templateKinds: RuntimeScheduleKind[] = [
  "after",
  "at",
  "every",
  "daily",
  "weekly",
  "cron",
];

export const templateTargetKinds: RuntimeScheduleTargetKind[] = [
  "session",
  "workspace",
  "headless",
];
