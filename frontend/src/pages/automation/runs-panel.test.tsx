// @vitest-environment jsdom

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type {
  RuntimeScheduleJobRun,
  RuntimeScheduleTask,
} from "@/types/runtime";

import { AutomationRunsPanel } from "./runs-panel";

const scheduleApi = vi.hoisted(() => ({
  fetchScheduleJobRuns: vi.fn(),
}));

vi.mock("@/api/runtime/schedule", () => ({
  fetchScheduleJobRuns: scheduleApi.fetchScheduleJobRuns,
}));

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

function task(
  id: string,
  title: string,
  overrides: Partial<RuntimeScheduleTask> = {},
): RuntimeScheduleTask {
  return {
    id,
    kind: "every",
    title,
    prompt: "Inspect the project",
    scheduledAt: "2026-11-20T00:00:00Z",
    sessionId: `session-${id}`,
    createdBySessionId: `creator-${id}`,
    status: "active",
    targetKind: "headless",
    ...overrides,
  };
}

function run(
  id: number,
  scheduleId: string,
  overrides: Partial<RuntimeScheduleJobRun> = {},
): RuntimeScheduleJobRun {
  return {
    id,
    scheduleId,
    status: "done",
    output: `output-${id}`,
    startedAt: "2026-11-20T00:00:00Z",
    finishedAt: "2026-11-20T00:00:01Z",
    ...overrides,
  };
}

async function flush(): Promise<void> {
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 0));
  });
}

function click(element: Element) {
  act(() => {
    (element as HTMLElement).click();
  });
}

function chooseOption(label: string) {
  const option = Array.from(
    document.body.querySelectorAll<HTMLElement>('[role="option"]'),
  ).find((item) => item.textContent?.includes(label));
  if (!option) {
    throw new Error(`missing select option: ${label}`);
  }
  click(option);
}

describe("AutomationRunsPanel", () => {
  let container: HTMLDivElement;
  let root: Root | null;
  let originalScrollIntoView: typeof HTMLElement.prototype.scrollIntoView;

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
    originalScrollIntoView = HTMLElement.prototype.scrollIntoView;
    HTMLElement.prototype.scrollIntoView = vi.fn();
    vi.stubGlobal("requestAnimationFrame", (callback: FrameRequestCallback) => {
      callback(0);
      return 0;
    });
  });

  afterEach(() => {
    if (root) {
      act(() => root?.unmount());
      root = null;
    }
    document.body.innerHTML = "";
    HTMLElement.prototype.scrollIntoView = originalScrollIntoView;
    vi.unstubAllGlobals();
    scheduleApi.fetchScheduleJobRuns.mockReset();
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
  });

  function render(tasks: RuntimeScheduleTask[]) {
    act(() => {
      root?.render(<AutomationRunsPanel tasks={tasks} />);
    });
  }

  it("defaults to all tasks and merges their runs newest-first", async () => {
    scheduleApi.fetchScheduleJobRuns.mockImplementation(
      async (_sessionId: string | undefined, id: string) => ({
        id,
        runs:
          id === "task-a"
            ? [run(1, id), run(3, id)]
            : [run(2, id)],
        count: id === "task-a" ? 2 : 1,
      }),
    );
    render([task("task-a", "Task A"), task("task-b", "Task B")]);
    await flush();

    expect(scheduleApi.fetchScheduleJobRuns).toHaveBeenCalledTimes(2);
    expect(
      document.body.querySelector('[data-testid="automation-runs-task-filter"]')
        ?.textContent,
    ).toContain("全部任务");
    const rows = Array.from(document.body.querySelectorAll("ol > li")).map(
      (item) => item.textContent ?? "",
    );
    expect(rows).toHaveLength(3);
    expect(rows[0]).toContain("#3");
    expect(rows[1]).toContain("#2");
    expect(rows[2]).toContain("#1");
    expect(rows[0]).toContain("Task A");
    expect(rows[1]).toContain("Task B");
  });

  it("keeps the task filter optional and requests one task after selection", async () => {
    scheduleApi.fetchScheduleJobRuns.mockImplementation(
      async (_sessionId: string | undefined, id: string) => ({
        id,
        runs: [run(id === "task-a" ? 1 : 2, id)],
        count: 1,
      }),
    );
    render([task("task-a", "Task A"), task("task-b", "Task B")]);
    await flush();
    scheduleApi.fetchScheduleJobRuns.mockClear();

    click(
      document.body.querySelector<HTMLButtonElement>(
        '[data-testid="automation-runs-task-filter"]',
      ) as HTMLButtonElement,
    );
    chooseOption("Task B");
    await flush();

    expect(scheduleApi.fetchScheduleJobRuns).toHaveBeenCalledTimes(1);
    expect(scheduleApi.fetchScheduleJobRuns).toHaveBeenCalledWith(
      "session-task-b",
      "task-b",
      50,
    );
    expect(document.body.textContent).toContain("#2");
    expect(document.body.textContent).not.toContain("#1");
  });

  it("keeps successfully loaded runs when one task request fails", async () => {
    scheduleApi.fetchScheduleJobRuns.mockImplementation(
      async (_sessionId: string | undefined, id: string) => {
        if (id === "task-b") {
          throw new Error("offline");
        }
        return {
          id,
          runs: [run(1, id)],
          count: 1,
        };
      },
    );
    render([task("task-a", "Task A"), task("task-b", "Task B")]);
    await flush();

    expect(document.body.textContent).toContain(
      "部分任务的运行日志加载失败，当前展示已成功加载的记录。",
    );
    expect(document.body.textContent).toContain("#1");
    expect(document.body.textContent).toContain("Task A");
  });
});
