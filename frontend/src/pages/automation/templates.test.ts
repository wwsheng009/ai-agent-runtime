import type { TFunction } from "i18next";
import { describe, expect, it } from "vitest";

import type { RuntimeScheduleTemplate } from "@/types/runtime";

import {
  buildAutomationTemplates,
  emptyAutomationTemplateForm,
  localTimeZone,
  templateFormFrom,
  templateRequestFromForm,
  validateAutomationTemplateForm,
} from "./templates";

const translateKey = ((key: string) =>
  key) as unknown as TFunction<"workspace">;

function template(
  overrides: Partial<RuntimeScheduleTemplate> = {},
): RuntimeScheduleTemplate {
  return {
    id: "weekday-digest",
    title: "Weekday digest",
    description: "A saved reusable workflow",
    taskTitle: "Morning summary",
    category: "productivity",
    prompt: "Summarize recent work",
    kind: "weekly",
    amount: "",
    atTime: "",
    clockTime: "09:00",
    timeZone: "Asia/Shanghai",
    weekdays: [1, 2, 3, 4, 5],
    cron: "",
    goal: "Produce an actionable digest",
    plan: "Scan recent work\nList priorities",
    targetKind: "headless",
    targetWorkspace: "",
    targetProfile: "",
    provider: "",
    model: "",
    reasoningEffort: "",
    createdAt: "2026-11-20T00:00:00Z",
    updatedAt: "2026-11-20T00:00:00Z",
    ...overrides,
  };
}

describe("buildAutomationTemplates", () => {
  it("returns no hard-coded templates when the store has no records", () => {
    expect(buildAutomationTemplates([], translateKey)).toEqual([]);
  });

  it("builds cards and task presets from persisted records", () => {
    const record = template();
    const [card] = buildAutomationTemplates([record], translateKey);

    expect(card).toMatchObject({
      id: record.id,
      title: record.title,
      description: record.description,
      category: "panels.schedule.templates.categories.productivity",
      form: {
        title: record.taskTitle,
        prompt: record.prompt,
        goal: record.goal,
        plan: record.plan,
        kind: "weekly",
        clockTime: record.clockTime,
        weekdays: record.weekdays,
        targetKind: "headless",
      },
      source: record,
    });
    expect(card?.frequency).toContain("09:00");
  });

  it("uses a custom category as-is and falls back to the task prompt for an empty description", () => {
    const [card] = buildAutomationTemplates(
      [template({ category: "Team workflows", description: "" })],
      translateKey,
    );

    expect(card?.category).toBe("Team workflows");
    expect(card?.description).toBe("Summarize recent work");
  });
});

describe("automation template form conversion", () => {
  it("defaults new templates and templates without a zone to the user's current timezone", () => {
    const currentZone =
      Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";

    expect(emptyAutomationTemplateForm().timeZone).toBe(currentZone);
    expect(templateFormFrom(template({ timeZone: "" })).timeZone).toBe(
      currentZone,
    );
    expect(localTimeZone()).toBe(currentZone);
  });

  it("preserves an explicit timezone from a persisted template", () => {
    expect(
      templateFormFrom(template({ timeZone: "Europe/Berlin" })).timeZone,
    ).toBe("Europe/Berlin");
  });

  it("copies editable values without persisted timestamps", () => {
    const form = templateFormFrom(template());

    expect(form).toMatchObject({
      id: "weekday-digest",
      title: "Weekday digest",
      taskTitle: "Morning summary",
      weekdays: [1, 2, 3, 4, 5],
    });
    expect(form).not.toHaveProperty("createdAt");
    expect(form).not.toHaveProperty("updatedAt");
  });

  it("trims values and sorts/deduplicates weekdays before saving", () => {
    const form = {
      ...emptyAutomationTemplateForm(),
      id: "  custom-id  ",
      title: "  Template name  ",
      taskTitle: "  Task name  ",
      description: "  Description  ",
      category: "  reporting  ",
      prompt: "  Do the work  ",
      kind: "weekly" as const,
      clockTime: " 09:30 ",
      timeZone: " Asia/Shanghai ",
      weekdays: [5, 1, 5, 0, 8],
      goal: "  Goal  ",
      plan: " First step \n Second step ",
    };

    expect(templateRequestFromForm(form)).toMatchObject({
      id: "custom-id",
      title: "Template name",
      taskTitle: "Task name",
      description: "Description",
      category: "reporting",
      prompt: "Do the work",
      clockTime: "09:30",
      timeZone: "Asia/Shanghai",
      weekdays: [1, 5],
      goal: "Goal",
      plan: "First step \n Second step",
    });
  });

  it("converts a datetime-local value to RFC3339 for the API", () => {
    const form = {
      ...emptyAutomationTemplateForm(),
      title: "One time",
      taskTitle: "One time task",
      prompt: "Run once",
      kind: "at" as const,
      atTime: "2027-01-02T03:04",
    };

    expect(templateRequestFromForm(form).atTime).toMatch(
      /^2027-01-02T03:04:00[+-]\d{2}:\d{2}$/,
    );
  });
});

describe("validateAutomationTemplateForm", () => {
  it("reports missing required fields and invalid schedule values", () => {
    expect(
      validateAutomationTemplateForm(emptyAutomationTemplateForm()),
    ).toMatchObject({
      title: "required",
      taskTitle: "required",
      prompt: "required",
    });

    const daily = {
      ...emptyAutomationTemplateForm(),
      title: "Daily",
      taskTitle: "Daily task",
      prompt: "Run daily",
      kind: "daily" as const,
      clockTime: "25:61",
      timeZone: "Not/AZone",
    };
    expect(validateAutomationTemplateForm(daily)).toMatchObject({
      clockTime: "clock",
      timeZone: "invalid",
    });
  });

  it("requires valid weekdays for weekly schedules and a goal for unattended execution", () => {
    const weekly = {
      ...emptyAutomationTemplateForm(),
      title: "Weekly",
      taskTitle: "Weekly task",
      prompt: "Review the week",
      targetKind: "headless" as const,
      kind: "weekly" as const,
      clockTime: "17:00",
      timeZone: "UTC",
      weekdays: [1, 1],
    };

    expect(validateAutomationTemplateForm(weekly)).toMatchObject({
      goal: "required",
      weekdays: "invalid",
    });
  });
});
