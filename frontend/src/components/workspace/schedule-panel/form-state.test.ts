import { describe, expect, it } from "vitest";

import type { RuntimeScheduleTask } from "@/types/runtime";

import {
  createRequestFrom,
  editFormFromTask,
  emptyCreateForm,
  localDateTimeToRfc3339,
  scheduleErrorFields,
  updateRequestFrom,
  validateCreateForm,
} from "./form-state";

function task(overrides: Partial<RuntimeScheduleTask> = {}): RuntimeScheduleTask {
  return {
    id: "schedule-1",
    kind: "every",
    title: "Ping",
    prompt: "ping",
    scheduledAt: "2026-10-11T12:05:00Z",
    everySeconds: 300,
    sessionId: "session-1",
    status: "active",
    ...overrides,
  };
}

describe("createRequestFrom targets", () => {
  it("session target requires a session id", () => {
    const form = {
      ...emptyCreateForm(),
      title: "t",
      prompt: "p",
      targetKind: "session" as const,
    };
    expect(createRequestFrom(form)).toBeNull();
  });

  it("headless target creates without session id and carries goal/plan", () => {
    const request = createRequestFrom({
      ...emptyCreateForm(),
      title: "nightly",
      prompt: "run checks",
      targetKind: "headless",
      goal: "keep the system healthy",
      plan: "check disk\ncheck memory",
    });
    expect(request).toMatchObject({
      sessionId: "",
      target_kind: "headless",
      goal: "keep the system healthy",
      plan: ["check disk", "check memory"],
      after_seconds: 1800,
    });
  });

  it("workspace target requires an absolute workspace path", () => {
    const base = {
      ...emptyCreateForm(),
      title: "workspace",
      prompt: "run checks",
      targetKind: "workspace" as const,
      goal: "keep the workspace healthy",
    };
    expect(createRequestFrom(base)).toBeNull();
    expect(
      createRequestFrom({
        ...base,
        targetWorkspace: "E:/projects/demo",
        targetProfile: "scheduled",
      }),
    ).toMatchObject({
      target_kind: "workspace",
      target_workspace: "E:/projects/demo",
      target_profile: "scheduled",
    });
  });
});

describe("schedule form validation and local timing", () => {
  it("returns field-level errors for the default incomplete session form", () => {
    expect(validateCreateForm(emptyCreateForm("session-1"))).toMatchObject({
      title: "required",
      prompt: "required",
    });
  });

  it("keeps daily clock time independent from stale at input", () => {
    const request = createRequestFrom({
      ...emptyCreateForm("session-1"),
      title: "Daily",
      prompt: "check",
      kind: "daily",
      atTime: "2030-05-06T07:08",
      clockTime: "09:30",
    });
    expect(request).toMatchObject({
      daily: { time: "09:30" },
    });
    expect(request).not.toHaveProperty("at");
  });

  it("formats datetime-local with a numeric local offset, not a UTC Z suffix", () => {
    const value = localDateTimeToRfc3339("2030-05-06T07:08");
    expect(value).toMatch(/^2030-05-06T07:08:00[+-]\d{2}:\d{2}$/);
  });
});

describe("scheduleErrorFields", () => {
  it("maps prompt, time zone, and frequency errors to their fields", () => {
    const form = {
      ...emptyCreateForm("session-1"),
      kind: "daily" as const,
    };
    expect(scheduleErrorFields("invalid_prompt", form)).toEqual({
      title: "invalid",
      prompt: "invalid",
    });
    expect(scheduleErrorFields("invalid_time_zone", form)).toEqual({
      timeZone: "invalid",
    });
    expect(scheduleErrorFields("frequency_too_high", form)).toEqual({
      amount: "invalid",
    });
  });

  it("maps selector and rule errors using the selected target and timing rule", () => {
    const sessionForm = {
      ...emptyCreateForm(),
      kind: "at" as const,
    };
    expect(scheduleErrorFields("invalid_selector", sessionForm)).toEqual({
      sessionId: "invalid",
    });

    const jobForm = {
      ...emptyCreateForm(),
      targetKind: "headless" as const,
      kind: "weekly" as const,
      goal: "",
    };
    expect(scheduleErrorFields("invalid_rule", jobForm)).toEqual({
      clockTime: "invalid",
      goal: "required",
    });
  });
});

describe("editFormFromTask", () => {
  it("回填内容、执行上下文与规则字段", () => {
    const form = editFormFromTask(
      task({
        goal: "health check",
        plan: ["disk", "memory"],
        targetKind: "workspace",
        targetWorkspace: "E:/projects/demo",
        targetProfile: "scheduled",
      }),
    );

    expect(form).toMatchObject({
      sessionId: "session-1",
      title: "Ping",
      prompt: "ping",
      kind: "every",
      amount: "5",
      goal: "health check",
      plan: "disk\nmemory",
      targetKind: "workspace",
      targetWorkspace: "E:/projects/demo",
      targetProfile: "scheduled",
    });
  });
});

describe("updateRequestFrom", () => {
  it("时间未变化时不发送 change，并完整提交内容与执行上下文", () => {
    const base = task({ goal: "old goal" });
    const form = {
      ...editFormFromTask(base),
      title: "  New title  ",
      prompt: "  new prompt  ",
      goal: "  new goal  ",
      plan: " step one \n\n step two ",
      targetKind: "headless" as const,
      targetProfile: " scheduled ",
    };

    expect(updateRequestFrom(form, base)).toMatchObject({
      id: "schedule-1",
      expected: {
        id: "schedule-1",
        kind: "every",
        title: "Ping",
        prompt: "ping",
        scheduledAt: "2026-10-11T12:05:00Z",
      },
      title: "New title",
      prompt: "new prompt",
      goal: "new goal",
      plan: ["step one", "step two"],
      target_kind: "headless",
      target_profile: "scheduled",
      target_workspace: "",
      target_session_id: undefined,
    });
    expect(updateRequestFrom(form, base)?.change).toBeUndefined();
  });

  it("修改延迟规则时发送 after_seconds", () => {
    const base = task();
    const form = {
      ...editFormFromTask(base),
      kind: "after" as const,
      amount: "2",
    };

    expect(updateRequestFrom(form, base)?.change).toEqual({
      kind: "after",
      after_seconds: 120,
    });
  });

  it("无会话的 headless 任务仍可编辑保存", () => {
    const base = task({
      sessionId: "",
      createdBySessionId: undefined,
      targetKind: "headless",
      goal: "keep healthy",
    });
    const form = {
      ...editFormFromTask(base),
      title: "Nightly edited",
    };

    expect(updateRequestFrom(form, base)).toMatchObject({
      id: "schedule-1",
      title: "Nightly edited",
      target_kind: "headless",
      target_session_id: undefined,
    });
  });

  it("非 session 目标缺少 goal 时拒绝保存", () => {
    const base = task();
    expect(
      updateRequestFrom(
        {
          ...editFormFromTask(base),
          targetKind: "headless",
          goal: " ",
        },
        base,
      ),
    ).toBeNull();
  });
});
