// @vitest-environment jsdom

// P2-9：面板只渲染 + 转发动作；数据由 shell owner 的控制器注入（与顶栏状态条同源）。
// 拉取 / 事件刷新 / 兜底轮询 / 取消后刷新等数据语义见
// `hooks/workspace/use-background-jobs.test.tsx`。

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { BackgroundJobsController } from "@/hooks/workspace/use-background-jobs";
import { ingestJobOutputEvent, resetJobOutputStreams } from "@/lib/job-output-stream/store";
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
    cleanTerminals: vi.fn(async () => 0),
    error: null,
    hasLiveJobs: false,
    jobs: [],
    liveCount: 0,
    loading: false,
    pendingAction: null,
    refresh: vi.fn(),
    resizeTerminal: vi.fn(async () => true),
    runAction: vi.fn(async () => true),
    writeInput: vi.fn(async () => null),
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
    resetJobOutputStreams();
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

  /** 行内动作按钮文案（排除无文本的输出展开按钮）。 */
  function actionLabels(command: string): string[] {
    const row = Array.from(document.body.querySelectorAll("li")).find((item) =>
      item.textContent?.includes(command),
    );
    return Array.from(row?.querySelectorAll("button") ?? [])
      .map((button) => button.textContent?.trim() ?? "")
      .filter((text) => text !== "");
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

  it("展开的 live 任务跟随 job_output 事件增量（事件驱动）", async () => {
    getRuntimeJobOutputMock
      .mockResolvedValueOnce({
        jobId: "live-1",
        status: "running",
        output: "first\n",
        nextOffset: 6,
        exitCode: null,
        message: "",
        errorCode: "",
      })
      .mockResolvedValueOnce({
        jobId: "live-1",
        status: "running",
        output: "second\n",
        nextOffset: 13,
        exitCode: null,
        message: "",
        errorCode: "",
      });

    await renderPanel({
      hasLiveJobs: true,
      liveCount: 1,
      jobs: [makeJob({ id: "live-1", command: "sleep 30" })],
    });

    await act(async () => {
      document.body
        .querySelector<HTMLButtonElement>('button[aria-label="查看 sleep 30 的输出"]')
        ?.click();
    });
    await act(flush);
    expect(getRuntimeJobOutputMock).toHaveBeenCalledTimes(1);

    // 事件驱动：注入一条 job_output 信号 → 面板去抖后按 nextOffset 续读。
    await act(async () => {
      ingestJobOutputEvent({
        type: "job_output",
        payload: { job_id: "live-1", offset: 6, next_offset: 13, size: 7 },
      });
      await new Promise((resolve) => setTimeout(resolve, 320));
    });
    await act(flush);

    expect(getRuntimeJobOutputMock).toHaveBeenLastCalledWith("live-1", {
      offset: 6,
      limit: 8192,
    });
    expect(document.body.textContent).toContain("second");
  });

  it("动作按钮按状态矩阵渲染（pending/paused/终态）", async () => {
    await renderPanel({
      hasLiveJobs: true,
      liveCount: 2,
      jobs: [
        makeJob({ id: "pending-1", command: "queued task", status: "pending" }),
        makeJob({ id: "paused-1", command: "paused task", status: "paused" }),
        makeJob({ id: "settled-1", command: "done task", status: "completed" }),
      ],
    });

    expect(actionLabels("queued task")).toEqual(["暂停", "取消", "放弃"]);
    expect(actionLabels("paused task")).toEqual(["恢复", "取消", "放弃"]);
    expect(actionLabels("done task")).toEqual(["重新排队"]);
  });

  it("交互会话的 live 行渲染输入框，非交互/终态不渲染", async () => {
    await renderPanel({
      hasLiveJobs: true,
      liveCount: 2,
      jobs: [
        makeJob({ id: "live-interactive", command: "more", status: "running", interactive: true }),
        makeJob({ id: "live-plain", command: "sleep 30", status: "running" }),
      ],
    });

    const interactiveInput = document.body.querySelector<HTMLInputElement>(
      'input[data-testid="job-input"]',
    );
    expect(interactiveInput).not.toBeNull();
    // Only the interactive live row carries an input; the plain live row and
    // (no) settled rows render none.
    expect(document.body.querySelectorAll('input[data-testid="job-input"]').length).toBe(1);
    expect(interactiveInput?.disabled).toBe(false);
  });

  it("提交输入把 chars 转发给 writeInput 并把回显追加到输出窗口", async () => {
    // 控制器契约：writeInput 返回解包后的 RuntimeJobExchange（或 null）。
    const writeInput = vi.fn(async () => ({
      jobId: "live-interactive",
      status: "running",
      output: "echo:hi\r\n",
      offset: 7,
      nextOffset: 16,
      waitCondition: "output",
      waitedMs: 40,
      exited: false,
      exitCode: null,
      message: "",
      osPid: null,
      cpuPercent: null,
      rssKb: null,
    }));
    getRuntimeJobOutputMock.mockResolvedValue({
      jobId: "live-interactive",
      status: "running",
      output: "",
      nextOffset: 7,
      exitCode: null,
      message: "",
      errorCode: "",
      waitCondition: "",
      waitedMs: 0,
      osPid: null,
      cpuPercent: null,
      rssKb: null,
    });
    await renderPanel({
      hasLiveJobs: true,
      liveCount: 1,
      jobs: [makeJob({ id: "live-interactive", command: "more", status: "running", interactive: true })],
      writeInput,
    });

    await act(async () => {
      document.body
        .querySelector<HTMLButtonElement>('button[aria-label="查看 more 的输出"]')
        ?.click();
    });
    await act(flush);

    const input = document.body.querySelector<HTMLInputElement>('input[data-testid="job-input"]');
    expect(input).not.toBeNull();
    // React-controlled input: route through the native value setter so the
    // synthetic onChange fires (direct .value assignment is ignored by React).
    const nativeSetter = Object.getOwnPropertyDescriptor(
      window.HTMLInputElement.prototype,
      "value",
    )?.set;
    await act(async () => {
      nativeSetter?.call(input!, "hi");
      input!.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await act(async () => {
      input!.form!.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
    });
    await act(flush);

    expect(writeInput).toHaveBeenCalledWith("live-interactive", { chars: "hi\r" });
    expect(document.body.textContent).toContain("echo:hi");
  });

  it("点击动作按钮把 (jobId, action) 转发给控制器", async () => {
    const controller = await renderPanel({
      hasLiveJobs: true,
      liveCount: 1,
      jobs: [
        makeJob({ id: "pending-1", command: "queued task", status: "pending" }),
        makeJob({ id: "settled-1", command: "done task", status: "completed" }),
      ],
    });

    const clickAction = async (label: string) => {
      const button = Array.from(document.body.querySelectorAll("button")).find(
        (candidate) => candidate.textContent?.trim() === label,
      );
      expect(button).toBeDefined();
      await act(async () => {
        button?.click();
      });
      await act(flush);
    };

    await clickAction("暂停");
    await clickAction("放弃");
    await clickAction("重新排队");

    expect(controller.runAction).toHaveBeenNthCalledWith(1, "pending-1", "pause");
    expect(controller.runAction).toHaveBeenNthCalledWith(2, "pending-1", "abandon");
    expect(controller.runAction).toHaveBeenNthCalledWith(3, "settled-1", "requeue");
  });

  it("在途动作显示进行中文案并禁用该行按钮", async () => {
    await renderPanel({
      hasLiveJobs: true,
      liveCount: 1,
      pendingAction: { jobId: "pending-1", action: "pause" },
      jobs: [makeJob({ id: "pending-1", command: "queued task", status: "pending" })],
    });

    const row = Array.from(document.body.querySelectorAll("li")).find((item) =>
      item.textContent?.includes("queued task"),
    );
    const buttons = Array.from(row?.querySelectorAll("button") ?? []).filter(
      (button) => (button.textContent?.trim() ?? "") !== "",
    );

    expect(buttons.map((button) => button.textContent?.trim())).toEqual([
      "暂停中…",
      "取消",
      "放弃",
    ]);
    expect(buttons.every((button) => button.disabled)).toBe(true);
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
