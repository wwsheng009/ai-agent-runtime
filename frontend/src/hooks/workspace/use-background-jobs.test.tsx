// @vitest-environment jsdom

// P2-9：后台任务控制器单测（拉取口径 / 事件延迟刷新 / live 兜底轮询 / 取消 / 计数派生）。
//
// 弹层与顶栏常驻状态条共用这一份数据：`liveCount` 与面板 `splitRuntimeJobs` 的 live
// 分区同源同判据（都用 `isLiveJobStatus`），因此「状态条计数 == 弹层 live 计数」由结构保证。

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { RuntimeJob } from "@/types/runtime";

const { cancelRuntimeJobMock, listRuntimeJobsMock } = vi.hoisted(() => ({
  cancelRuntimeJobMock: vi.fn(),
  listRuntimeJobsMock: vi.fn(),
}));

vi.mock("@/lib/runtime-api", () => ({
  cancelRuntimeJob: cancelRuntimeJobMock,
  listRuntimeJobs: listRuntimeJobsMock,
}));

import {
  useBackgroundJobs,
  type BackgroundJobsController,
  type UseBackgroundJobsOptions,
} from "./use-background-jobs";

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

function Harness({
  onSnapshot,
  options,
}: {
  onSnapshot: (controller: BackgroundJobsController) => void;
  options: UseBackgroundJobsOptions;
}) {
  const controller = useBackgroundJobs(options);
  onSnapshot(controller);
  return null;
}

describe("useBackgroundJobs", () => {
  let container: HTMLDivElement;
  let root: Root;
  let latest: BackgroundJobsController | null;

  beforeEach(() => {
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    latest = null;
    cancelRuntimeJobMock.mockReset();
    listRuntimeJobsMock.mockReset();
    listRuntimeJobsMock.mockResolvedValue({ jobs: [], count: 0 });
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
    vi.useRealTimers();
  });

  function snapshot(controller: BackgroundJobsController) {
    latest = controller;
  }

  async function render(options: UseBackgroundJobsOptions) {
    await act(async () => {
      root.render(<Harness onSnapshot={snapshot} options={options} />);
    });
    await act(async () => {
      await Promise.resolve();
    });
    return latest as unknown as BackgroundJobsController;
  }

  const baseOptions: UseBackgroundJobsOptions = {
    enabled: true,
    sessionId: "session-jobs-1",
  };

  it("未启用或缺会话 id 时不拉取", async () => {
    await render({ ...baseOptions, enabled: false });
    expect(listRuntimeJobsMock).not.toHaveBeenCalled();

    await render({ ...baseOptions, sessionId: "" });
    expect(listRuntimeJobsMock).not.toHaveBeenCalled();
  });

  it("按会话拉取一次并派生 liveCount / hasLiveJobs", async () => {
    listRuntimeJobsMock.mockResolvedValue({
      jobs: [
        makeJob({ id: "live-1", status: "running" }),
        makeJob({ id: "live-2", status: "pending" }),
        makeJob({ id: "settled-1", status: "completed" }),
      ],
      count: 3,
    });

    const controller = await render(baseOptions);

    expect(listRuntimeJobsMock).toHaveBeenCalledWith({
      sessionId: "session-jobs-1",
      limit: 50,
    });
    expect(controller.jobs).toHaveLength(3);
    expect(controller.liveCount).toBe(2);
    expect(controller.hasLiveJobs).toBe(true);
  });

  it("job_* 运行时事件到达后延迟一拍（800ms）合并刷新", async () => {
    vi.useFakeTimers();
    const controller = await render(baseOptions);
    expect(controller.jobs).toEqual([]);
    const callsAfterMount = listRuntimeJobsMock.mock.calls.length;

    await act(async () => {
      root.render(
        <Harness
          onSnapshot={snapshot}
          options={{
            ...baseOptions,
            lastRuntimeEventType: "job_started",
            runtimeEventCount: 1,
          }}
        />,
      );
    });

    await act(async () => {
      await vi.advanceTimersByTimeAsync(700);
    });
    expect(listRuntimeJobsMock.mock.calls.length).toBe(callsAfterMount);

    await act(async () => {
      await vi.advanceTimersByTimeAsync(200);
    });
    expect(listRuntimeJobsMock.mock.calls.length).toBeGreaterThan(callsAfterMount);
  });

  it("存在 live 任务时按 5s 兜底轮询，全部终态后停止", async () => {
    vi.useFakeTimers();
    listRuntimeJobsMock.mockResolvedValue({
      jobs: [makeJob({ id: "live-1", status: "running" })],
      count: 1,
    });

    const controller = await render(baseOptions);
    expect(controller.liveCount).toBe(1);
    const callsWithLive = listRuntimeJobsMock.mock.calls.length;

    await act(async () => {
      await vi.advanceTimersByTimeAsync(5_000);
    });
    expect(listRuntimeJobsMock.mock.calls.length).toBeGreaterThan(callsWithLive);

    // 任务转入终态后不再轮询。
    listRuntimeJobsMock.mockResolvedValue({
      jobs: [makeJob({ id: "live-1", status: "completed" })],
      count: 1,
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5_000);
    });
    await act(async () => {
      await Promise.resolve();
    });
    const callsAfterSettled = listRuntimeJobsMock.mock.calls.length;

    await act(async () => {
      await vi.advanceTimersByTimeAsync(5_000);
    });
    expect(listRuntimeJobsMock.mock.calls.length).toBe(callsAfterSettled);
  });

  it("取消成功后立即刷新", async () => {
    const controller = await render(baseOptions);
    const callsBefore = listRuntimeJobsMock.mock.calls.length;
    cancelRuntimeJobMock.mockResolvedValue(makeJob({ status: "cancelled" }));

    await act(async () => {
      await controller.cancel("job-1");
    });
    await act(async () => {
      await Promise.resolve();
    });

    expect(cancelRuntimeJobMock).toHaveBeenCalledWith("job-1");
    expect(listRuntimeJobsMock.mock.calls.length).toBeGreaterThan(callsBefore);
    expect((latest as unknown as BackgroundJobsController).cancellingId).toBe("");
  });

  it("取消失败暴露错误且不触发刷新", async () => {
    const controller = await render(baseOptions);
    const callsBefore = listRuntimeJobsMock.mock.calls.length;
    cancelRuntimeJobMock.mockRejectedValue(new Error("cancel denied"));

    await act(async () => {
      await controller.cancel("job-1");
    });
    await act(async () => {
      await Promise.resolve();
    });

    const after = latest as unknown as BackgroundJobsController;
    expect(after.error).toBe("cancel denied");
    expect(listRuntimeJobsMock.mock.calls.length).toBe(callsBefore);
    expect(after.cancellingId).toBe("");
  });
});
