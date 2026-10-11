// P3：自动化任务面板共享纯函数单测。
import { describe, expect, it } from "vitest";

import {
  formatScheduleRelative,
  isLiveScheduleStatus,
  isScheduleRuntimeEvent,
  scheduleFrequencySummary,
  scheduleKindLabelKey,
  splitScheduleTasks,
} from "./schedule-panel-shared";
import type { RuntimeScheduleTask } from "@/types/runtime";

function task(overrides: Partial<RuntimeScheduleTask>): RuntimeScheduleTask {
  return {
    id: "schedule-1",
    kind: "after",
    title: "t",
    prompt: "p",
    scheduledAt: "2026-10-11T12:00:00.000Z",
    sessionId: "session-1",
    status: "active",
    ...overrides,
  };
}

describe("splitScheduleTasks", () => {
  it("按状态分区且不重排", () => {
    const input = [
      task({ id: "a", status: "active" }),
      task({ id: "b", status: "inactive" }),
      task({ id: "c", status: "active" }),
    ];
    const { active, inactive } = splitScheduleTasks(input);
    expect(active.map((item) => item.id)).toEqual(["a", "c"]);
    expect(inactive.map((item) => item.id)).toEqual(["b"]);
  });
});

describe("isLiveScheduleStatus", () => {
  it("仅 active 视为 live", () => {
    expect(isLiveScheduleStatus("active")).toBe(true);
    expect(isLiveScheduleStatus("inactive")).toBe(false);
  });
});

describe("scheduleFrequencySummary", () => {
  it("按 kind 输出摘要", () => {
    expect(scheduleFrequencySummary(task({ kind: "after", afterSeconds: 600 }))).toBe("600s");
    expect(scheduleFrequencySummary(task({ kind: "every", everySeconds: 300 }))).toBe("300s");
    expect(
      scheduleFrequencySummary(
        task({ kind: "weekly", time: "09:00:00.000", weekdays: [1, 3] }),
      ),
    ).toBe("09:00:00.000 (1,3)");
    expect(scheduleFrequencySummary(task({ kind: "cron", expression: "*/15 * * * *" }))).toBe(
      "*/15 * * * *",
    );
  });
});

describe("scheduleKindLabelKey", () => {
  it("返回 panels.schedule.kind.* 前缀", () => {
    expect(scheduleKindLabelKey("daily")).toBe("panels.schedule.kind.daily");
  });
});

describe("formatScheduleRelative", () => {
  it("未来为正、过去为负", () => {
    const now = new Date("2026-10-11T12:00:00Z").getTime();
    expect(formatScheduleRelative("2026-10-11T12:00:30Z", now)).toBe("30s");
    expect(formatScheduleRelative("2026-10-11T11:59:30Z", now)).toBe("-30s");
    expect(formatScheduleRelative("2026-10-11T13:00:00Z", now)).toBe("1h");
  });
});

describe("isScheduleRuntimeEvent", () => {
  it("识别 schedule 前缀事件", () => {
    expect(isScheduleRuntimeEvent("schedule.changed")).toBe(true);
    expect(isScheduleRuntimeEvent("session.created")).toBe(false);
    expect(isScheduleRuntimeEvent(undefined)).toBe(false);
  });
});
