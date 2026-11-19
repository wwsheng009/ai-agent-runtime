// @vitest-environment jsdom

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { RuntimeScheduleJobRun } from "@/types/runtime";

import { ScheduleRunHistory } from "./run-history";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({
    t: (key: string) => {
      const messages: Record<string, string> = {
        "panels.schedule.runHistory": "运行历史",
        "panels.schedule.runStatus.failed": "失败",
        "panels.schedule.runStatus.done": "成功",
        "panels.schedule.runError": "错误",
        "panels.schedule.runFailure": "任务运行失败，请检查运行时配置后重试。",
        "panels.schedule.errorCode.headless_executor_unavailable":
          "当前运行时未配置无人值守执行器。配置执行器后点击“重试”。",
      };
      return messages[key] ?? key;
    },
  }),
}));

function run(overrides: Partial<RuntimeScheduleJobRun> = {}): RuntimeScheduleJobRun {
  return {
    id: 1,
    scheduleId: "task-1",
    status: "failed",
    error: "no headless executor configured",
    errorCode: "headless_executor_unavailable",
    startedAt: "2026-10-11T12:05:00Z",
    finishedAt: "2026-10-11T12:05:01Z",
    ...overrides,
  };
}

describe("ScheduleRunHistory", () => {
  let container: HTMLDivElement;
  let root: Root | null;

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(() => {
    if (root) {
      act(() => root?.unmount());
      root = null;
    }
    document.body.innerHTML = "";
  });

  it("localizes a stable run error code instead of rendering the raw internal error", () => {
    act(() => {
      root?.render(
        <ScheduleRunHistory
          expanded
          onToggle={vi.fn()}
          runs={[run()]}
        />,
      );
    });

    expect(container.textContent).toContain(
      "当前运行时未配置无人值守执行器。配置执行器后点击“重试”。",
    );
    expect(container.textContent).not.toContain("no headless executor configured");
  });

  it("uses a generic localized failure when only an unknown error code is available", () => {
    act(() => {
      root?.render(
        <ScheduleRunHistory
          expanded
          onToggle={vi.fn()}
          runs={[
            run({
              error: "provider secret missing",
              errorCode: "provider_internal_failure",
            }),
          ]}
        />,
      );
    });

    expect(container.textContent).toContain(
      "任务运行失败，请检查运行时配置后重试。",
    );
    expect(container.textContent).not.toContain("provider secret missing");
  });
});
