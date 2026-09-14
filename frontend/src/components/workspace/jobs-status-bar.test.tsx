// @vitest-environment jsdom

// P2-9：顶栏常驻状态条 —— 与弹层共用同一控制器（同一 `liveCount`），
// 因此「状态条计数 == 弹层 live 计数」不是巧合而是结构保证；0 个 live 时不渲染状态条。

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { type Thread } from "@/data/mock";
import { useBackgroundJobs } from "@/hooks/workspace/use-background-jobs";
import type { RuntimeJob } from "@/types/runtime";

import { JobsPanel } from "./jobs-panel";
import { WorkspaceShellTopbar } from "./workspace-shell-topbar";

const { listRuntimeJobsMock } = vi.hoisted(() => ({
  listRuntimeJobsMock: vi.fn(),
}));

vi.mock("@/lib/runtime-api", () => ({
  cancelRuntimeJob: vi.fn(),
  getRuntimeJobOutput: vi.fn(),
  listRuntimeJobs: listRuntimeJobsMock,
}));

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

const thread: Thread = {
  id: "thread-1",
  title: "Review runtime changes",
  summary: "",
  updatedAt: "2026-07-27T00:00:00Z",
  status: "active",
  tags: [],
  prompts: [],
  messages: [],
  artifacts: [],
  sessionId: "session-jobs-1",
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

describe("后台任务状态条与弹层计数一致性", () => {
  let container: HTMLDivElement;
  let root: Root;
  const onOpenJobs = vi.fn();

  beforeEach(() => {
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    onOpenJobs.mockReset();
    listRuntimeJobsMock.mockReset();
    listRuntimeJobsMock.mockResolvedValue({ jobs: [], count: 0 });
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    document.body.innerHTML = "";
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
  });

  async function render() {
    await act(async () => {
      root.render(
        <MemoryRouter>
          <Harness onOpenJobs={onOpenJobs} />
        </MemoryRouter>,
      );
    });
    await act(async () => {
      await Promise.resolve();
    });
  }

  function Harness({ onOpenJobs: open }: { onOpenJobs: () => void }) {
    const jobs = useBackgroundJobs({
      enabled: true,
      sessionId: "session-jobs-1",
    });
    return (
      <>
        <WorkspaceShellTopbar
          density="comfortable"
          liveJobsCount={jobs.liveCount}
          liveTeamCount={0}
          onOpenJobs={open}
          onOpenSettings={() => {}}
          onOpenSidebar={() => {}}
          onToggleRightRail={() => {}}
          rightRailOpen={false}
          selectedThread={thread}
          threadStatusLabel="进行中"
          threadSubtitle="session-jobs-1"
          transportLabel="SSE"
        />
        <JobsPanel controller={jobs} onClose={() => {}} open />
      </>
    );
  }

  it("状态条计数与弹层 live 分区一致，并可打开弹层", async () => {
    listRuntimeJobsMock.mockResolvedValue({
      jobs: [
        makeJob({ id: "live-1", command: "sleep 30", status: "running" }),
        makeJob({ id: "live-2", command: "go build", status: "pending" }),
        makeJob({ id: "settled-1", status: "completed", exitCode: 0 }),
      ],
      count: 3,
    });

    await render();

    const chip = document.body.querySelector<HTMLButtonElement>(
      '[data-testid="topbar-jobs-running"]',
    );
    expect(chip).not.toBeNull();
    expect(chip?.textContent).toBe("2 个后台任务运行中");

    const panel = document.body.querySelector('[data-testid="jobs-panel"]');
    expect(panel?.textContent).toContain("进行中（2）");
    expect(panel?.textContent).toContain("已结束（1）");

    await act(async () => {
      chip?.click();
    });
    expect(onOpenJobs).toHaveBeenCalledTimes(1);
  });

  it("没有 live 任务时不渲染状态条（不制造 0 计数噪声）", async () => {
    listRuntimeJobsMock.mockResolvedValue({
      jobs: [makeJob({ id: "settled-1", status: "completed" })],
      count: 1,
    });

    await render();

    expect(
      document.body.querySelector('[data-testid="topbar-jobs-running"]'),
    ).toBeNull();
  });
});
