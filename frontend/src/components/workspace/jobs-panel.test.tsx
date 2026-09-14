// @vitest-environment jsdom

// P2-9：面板只渲染 + 转发动作；数据由 shell owner 的控制器注入（与顶栏状态条同源）。
// 拉取 / 事件刷新 / 兜底轮询 / 取消后刷新等数据语义见
// `hooks/workspace/use-background-jobs.test.tsx`。

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { BackgroundJobsController } from "@/hooks/workspace/use-background-jobs";
import type { RuntimeJob } from "@/types/runtime";

import { JobsPanel } from "./jobs-panel";

const { getRuntimeJobOutputMock } = vi.hoisted(() => ({
  getRuntimeJobOutputMock: vi.fn(),
}));

vi.mock("@/lib/runtime-api", () => ({
  getRuntimeJobOutput: getRuntimeJobOutputMock,
}));

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

function makeJob(overrides: Partial<RuntimeJob> = {}): RuntimeJob {
  return {
    id: "job-1",
    sessionId: "session-jobs-1",
    kind: "shell",
    command: "pnpm test",
    cwd: "E:/repo",
    priority: 0,
    restartPolicy: "never",
    status: "running",
    message: "",
    createdAt: "2026-09-13T10:00:00.000Z",
    startedAt: "2026-09-13T10:00:01.000Z",
    finishedAt: "",
    exitCode: null,
    logPath: "",
    ...overrides,
  };
}

function makeController(
  overrides: Partial<BackgroundJobsController> = {},
): BackgroundJobsController {
  return {
    cancel: vi.fn(async () => true),
    cancellingId: "",
    error: null,
    hasLiveJobs: false,
    jobs: [],
    liveCount: 0,
    loading: false,
    refresh: vi.fn(),
    ...overrides,
  };
}

function flush() {
  return Promise.resolve().then(() => Promise.resolve());
}

describe("JobsPanel", () => {
  let container: HTMLDivElement;
  let root: Root | null;
  const onClose = vi.fn();

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
    onClose.mockReset();
    getRuntimeJobOutputMock.mockReset();
  });

  afterEach(() => {
    if (root) {
      act(() => root?.unmount());
    }
    container.remove();
    document.body.innerHTML = "";
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
  });

  async function renderPanel(
    controllerOverrides: Partial<BackgroundJobsController> = {},
    panelOverrides: { open?: boolean } = {},
  ) {
    const controller = makeController(controllerOverrides);
    await act(async () => {
      root?.render(
        <JobsPanel
          controller={controller}
          onClose={onClose}
          open={panelOverrides.open ?? true}
        />,
      );
    });
    await act(flush);
    return controller;
  }

  it("按 live/settled 分区渲染任务与退出码", async () => {
    await renderPanel({
      hasLiveJobs: true,
      liveCount: 1,
      jobs: [
        makeJob({ id: "live-1", command: "sleep 30", status: "running" }),
        makeJob({
          id: "settled-1",
          command: "go test ./...",
          status: "failed",
          finishedAt: "2026-09-13T10:00:09.000Z",
          exitCode: 2,
        }),
      ],
    });

    const panel = document.body.querySelector('[data-testid="jobs-panel"]');
    expect(panel).not.toBeNull();

    expect(panel?.textContent).toContain("进行中（1）");
    expect(panel?.textContent).toContain("已结束（1）");
    expect(panel?.textContent).toContain("sleep 30");
    expect(panel?.textContent).toContain("go test ./...");
    expect(panel?.textContent).toContain("退出码 2");
    expect(panel?.textContent).toContain("失败");
  });

  it("空列表显示空态", async () => {
    await renderPanel();
    expect(document.body.textContent).toContain("当前会话没有后台任务");
  });

  it("控制器错误如实呈现（不伪造空态）", async () => {
    await renderPanel({ error: "jobs backend down" });
    expect(document.body.textContent).toContain("后台任务加载失败");
    expect(document.body.textContent).toContain("jobs backend down");
  });

  it("展开行时按 offset=0 读取输出，再次点击收起", async () => {
    getRuntimeJobOutputMock.mockResolvedValue({
      jobId: "live-1",
      status: "running",
      output: "line-1\n",
      nextOffset: 7,
      exitCode: null,
      message: "",
      errorCode: "",
    });

    await renderPanel({
      hasLiveJobs: true,
      liveCount: 1,
      jobs: [makeJob({ id: "live-1", command: "sleep 30" })],
    });

    const toggle = document.body.querySelector<HTMLButtonElement>(
      'button[aria-label="查看 sleep 30 的输出"]',
    );
    expect(toggle).not.toBeNull();

    await act(async () => {
      toggle?.click();
    });
    await act(flush);

    expect(getRuntimeJobOutputMock).toHaveBeenCalledWith("live-1", {
      offset: 0,
      limit: 8192,
    });
    expect(document.body.textContent).toContain("line-1");
    expect(toggle?.getAttribute("aria-expanded")).toBe("true");
  });

  it("输出可增量加载更多", async () => {
    getRuntimeJobOutputMock
      .mockResolvedValueOnce({
        jobId: "settled-1",
        status: "completed",
        output: "first\n",
        nextOffset: 6,
        exitCode: 0,
        message: "",
        errorCode: "",
      })
      .mockResolvedValueOnce({
        jobId: "settled-1",
        status: "completed",
        output: "second\n",
        nextOffset: 13,
        exitCode: 0,
        message: "",
        errorCode: "",
      });

    await renderPanel({
      jobs: [makeJob({ id: "settled-1", status: "completed" })],
    });

    await act(async () => {
      document.body
        .querySelector<HTMLButtonElement>('button[aria-label="查看 pnpm test 的输出"]')
        ?.click();
    });
    await act(flush);

    const loadMore = Array.from(document.body.querySelectorAll("button")).find(
      (button) => button.textContent?.includes("加载更多"),
    );
    expect(loadMore).toBeDefined();

    await act(async () => {
      loadMore?.click();
    });
    await act(flush);

    expect(getRuntimeJobOutputMock).toHaveBeenLastCalledWith("settled-1", {
      offset: 6,
      limit: 8192,
    });
    expect(document.body.textContent).toContain("first");
    expect(document.body.textContent).toContain("second");
  });

  it("取消动作转发给控制器（刷新语义在 hook 内）", async () => {
    const controller = await renderPanel({
      hasLiveJobs: true,
      liveCount: 1,
      jobs: [makeJob({ id: "live-1", command: "sleep 30" })],
    });

    const cancelButton = Array.from(document.body.querySelectorAll("button")).find(
      (button) => button.textContent?.trim() === "取消",
    );
    expect(cancelButton).toBeDefined();

    await act(async () => {
      cancelButton?.click();
    });
    await act(flush);

    expect(controller.cancel).toHaveBeenCalledWith("live-1");
  });

  it("关闭按钮与 Esc 都触发 onClose", async () => {
    await renderPanel();

    const closeButton = document.body.querySelector<HTMLButtonElement>(
      'button[aria-label="关闭后台任务面板"]',
    );
    expect(closeButton).not.toBeNull();

    await act(async () => {
      closeButton?.click();
    });
    expect(onClose).toHaveBeenCalledTimes(1);

    await act(async () => {
      window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" }));
    });
    expect(onClose.mock.calls.length).toBeGreaterThanOrEqual(2);
  });

  it("open=false 时不渲染面板", async () => {
    await renderPanel({}, { open: false });
    expect(document.body.querySelector('[data-testid="jobs-panel"]')).toBeNull();
  });
});
