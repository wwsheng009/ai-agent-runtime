import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { MemoryRouter, useSearchParams } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  getRuntimeTeamRun: vi.fn(),
  getRuntimeTeamRunPreflight: vi.fn(),
  startRuntimeTeamRun: vi.fn(),
}));

vi.mock("@/lib/runtime-api", () => ({
  ...mocks,
  isRuntimeTeamProductCapabilitySupported: (
    capabilities: Record<string, string> | undefined,
    capability: string,
  ) => capabilities?.[capability] === "supported",
}));

vi.mock("react-i18next", () => ({
  useTranslation: () => ({
    t: (key: string, values?: Record<string, unknown>) =>
      values ? `${key} ${Object.values(values).join(" ")}` : key,
  }),
}));

import type { RuntimeTeamRun } from "@/types/runtime";
import { RuntimeApiError } from "@/api/runtime/shared";

import { RuntimeTeamRunsView } from "./runtime-team-runs-view";

const draftRun: RuntimeTeamRun = {
  id: "run-draft-1",
  owner_id: "owner-1",
  workspace_id: "workspace-1",
  goal: "Review the change",
  status: "draft",
  phase: "draft",
  compatibility_mode: "none",
  snapshot_schema_version: 1,
  config_snapshot: { roles: ["reviewer"] },
  snapshot_checksum: "snapshot-checksum",
  version: 7,
  event_seq: 0,
  summary_state: "not_applicable",
  created_at: "2026-11-22T12:00:00Z",
};

const readyPreflight = {
  run_id: draftRun.id,
  version: draftRun.version,
  status: "draft",
  phase: "draft",
  snapshot_schema_version: 1,
  ready: true,
  blocker_count: 0,
  warning_count: 0,
  info_count: 0,
  findings: [],
  blockers: [],
  warnings: [],
  infos: [],
  route_preview_capability: "disabled",
};

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

function Harness({
  initialEntry,
  runStartCapability,
  runPreflightCapability,
}: {
  initialEntry: string;
  runStartCapability: string;
  runPreflightCapability: string;
}) {
  const [runs, setRuns] = useState([draftRun]);

  return (
    <MemoryRouter initialEntries={[initialEntry]}>
      <RuntimeTeamRunsView
        capabilities={{
          run_draft_create: "unsupported",
          run_start: runStartCapability,
          run_preflight: runPreflightCapability,
          worker_session_liveness: "unsupported",
        }}
        error={null}
        isLoading={false}
        onOpenLegacyTeam={() => undefined}
        onRetry={() => undefined}
        onRunUpdated={(run) =>
          setRuns((current) => {
            const existing = current.findIndex((item) => item.id === run.id);
            if (existing < 0) {
              return [run, ...current];
            }
            return current.map((item) => (item.id === run.id ? run : item));
          })
        }
        runs={runs}
      />
      <SearchParamsProbe />
    </MemoryRouter>
  );
}

function SearchParamsProbe() {
  const [searchParams] = useSearchParams();
  return (
    <output data-testid="team-run-search">
      {searchParams.toString()}
    </output>
  );
}

describe("RuntimeTeamRunsView", () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    mocks.getRuntimeTeamRun.mockReset();
    mocks.getRuntimeTeamRunPreflight.mockReset().mockResolvedValue({
      run: draftRun,
      run_id: draftRun.id,
      preflight: readyPreflight,
      readiness: true,
      capabilities: {
        run_start: "supported",
        run_preflight: "supported",
        worker_session_liveness: "unsupported",
      },
    });
    mocks.startRuntimeTeamRun.mockReset().mockResolvedValue({
      run: {
        ...draftRun,
        status: "active",
        phase: "running",
        started_at: "2026-11-22T12:01:00Z",
      },
      capabilities: { run_start: "supported" },
    });
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    document.body.innerHTML = "";
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
  });

  async function render(
    runStartCapability: string,
    runPreflightCapability = "supported",
    initialEntry = "/workspace?team_view=runs",
  ) {
    await act(async () => {
      root.render(
        <Harness
          initialEntry={initialEntry}
          runStartCapability={runStartCapability}
          runPreflightCapability={runPreflightCapability}
        />,
      );
    });
  }

  it("renders Start only when the server advertises run_start", async () => {
    await render("unsupported");

    expect(container.textContent).toContain(
      "panels.teamsPanels.runs.startUnsupported",
    );
    expect(
      Array.from(container.querySelectorAll("button")).some(
        (button) => button.textContent === "panels.teamsPanels.runs.start",
      ),
    ).toBe(false);
  });

  it("starts a draft with the current version and an idempotency key", async () => {
    await render("supported");

    expect(mocks.getRuntimeTeamRunPreflight).toHaveBeenCalledWith(
      "run-draft-1",
      { signal: expect.any(AbortSignal) },
    );
    expect(container.textContent).toContain(
      "panels.teamsPanels.runs.workerSessionLivenessUnsupported",
    );
    const startButton = Array.from(container.querySelectorAll("button")).find(
      (button) => button.textContent === "panels.teamsPanels.runs.start",
    );
    expect(startButton).toBeDefined();
    expect(startButton?.disabled).toBe(false);

    await act(async () => {
      startButton?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
      await Promise.resolve();
    });

    expect(mocks.startRuntimeTeamRun).toHaveBeenCalledWith(
      "run-draft-1",
      { expected_version: 7 },
      { idempotencyKey: expect.stringMatching(/^web-team-run-start-run-draft-1-/) },
    );
    expect(container.textContent).not.toContain(
      "panels.teamsPanels.runs.startUnsupported",
    );
  });

  it("shows server blockers and keeps Start disabled", async () => {
    mocks.getRuntimeTeamRunPreflight.mockResolvedValueOnce({
      run: draftRun,
      run_id: draftRun.id,
      preflight: {
        ...readyPreflight,
        ready: false,
        blocker_count: 1,
        findings: [
          {
            severity: "blocker",
            code: "task_assignee_offline",
            message: "The assigned teammate is offline.",
            remediation: "Bind an available teammate.",
            task_id: "task-1",
          },
        ],
        blockers: [
          {
            severity: "blocker",
            code: "task_assignee_offline",
            message: "The assigned teammate is offline.",
            remediation: "Bind an available teammate.",
            task_id: "task-1",
          },
        ],
      },
      readiness: false,
      capabilities: { run_start: "supported", run_preflight: "supported" },
    });
    await render("supported");

    const startButton = Array.from(container.querySelectorAll("button")).find(
      (button) => button.textContent === "panels.teamsPanels.runs.start",
    );
    expect(startButton?.disabled).toBe(true);
    expect(container.textContent).toContain("task_assignee_offline");
    expect(container.textContent).toContain(
      "The assigned teammate is offline.",
    );
    expect(mocks.startRuntimeTeamRun).not.toHaveBeenCalled();
  });

  it("keeps Start disabled when preflight is unsupported or unavailable", async () => {
    await render("supported", "unsupported");

    expect(container.textContent).toContain(
      "panels.teamsPanels.runs.preflightUnsupported",
    );
    const unsupportedStart = Array.from(
      container.querySelectorAll("button"),
    ).find((button) => button.textContent === "panels.teamsPanels.runs.start");
    expect(unsupportedStart?.disabled).toBe(true);

    mocks.getRuntimeTeamRunPreflight.mockRejectedValueOnce(
      new Error("preflight service unavailable"),
    );
    await act(async () => {
      root.render(
        <Harness
          initialEntry="/workspace?team_view=runs"
          runStartCapability="supported"
          runPreflightCapability="supported"
        />,
      );
      await Promise.resolve();
    });
    expect(container.textContent).toContain(
      "panels.teamsPanels.runs.preflightError preflight service unavailable",
    );
    const retryStart = Array.from(container.querySelectorAll("button")).find(
      (button) => button.textContent === "panels.teamsPanels.runs.start",
    );
    expect(retryStart?.disabled).toBe(true);

    const retryPreflight = Array.from(
      container.querySelectorAll("button"),
    ).find(
      (button) =>
        button.textContent === "panels.teamsPanels.runs.retryPreflight",
    );
    expect(retryPreflight).toBeDefined();
    await act(async () => {
      retryPreflight?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
      await Promise.resolve();
    });
    const recoveredStart = Array.from(
      container.querySelectorAll("button"),
    ).find((button) => button.textContent === "panels.teamsPanels.runs.start");
    expect(recoveredStart?.disabled).toBe(false);
  });

  it("refreshes the Runtime version and preflight after a Start failure", async () => {
    const refreshedRun: RuntimeTeamRun = {
      ...draftRun,
      version: draftRun.version + 1,
    };
    const activeRun: RuntimeTeamRun = {
      ...refreshedRun,
      status: "active",
      phase: "running",
      version: refreshedRun.version + 1,
    };
    let preflightCalls = 0;
    mocks.getRuntimeTeamRun.mockResolvedValueOnce({ run: refreshedRun });
    mocks.getRuntimeTeamRunPreflight.mockImplementation(async () => {
      preflightCalls += 1;
      const run = preflightCalls === 1 ? draftRun : refreshedRun;
      return {
        run,
        run_id: run.id,
        preflight: {
          ...readyPreflight,
          version: run.version,
        },
        readiness: true,
        capabilities: {
          run_start: "supported",
          run_preflight: "supported",
        },
      };
    });
    mocks.startRuntimeTeamRun
      .mockRejectedValueOnce(new Error("Team Run version conflict"))
      .mockResolvedValueOnce({ run: activeRun });

    await render("supported");
    const startButton = Array.from(container.querySelectorAll("button")).find(
      (button) => button.textContent === "panels.teamsPanels.runs.start",
    );
    expect(startButton?.disabled).toBe(false);

    await act(async () => {
      startButton?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(mocks.getRuntimeTeamRun).toHaveBeenCalledWith(
      refreshedRun.id,
      { signal: expect.any(AbortSignal) },
    );
    expect(mocks.getRuntimeTeamRunPreflight).toHaveBeenCalled();
    expect(preflightCalls).toBeGreaterThanOrEqual(2);
    expect(container.textContent).toContain(
      `panels.teamsPanels.runs.version${refreshedRun.version}`,
    );
    const recoveredStart = Array.from(
      container.querySelectorAll("button"),
    ).find((button) => button.textContent === "panels.teamsPanels.runs.start");
    expect(recoveredStart?.disabled).toBe(false);

    await act(async () => {
      recoveredStart?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(mocks.startRuntimeTeamRun).toHaveBeenCalledTimes(2);
    expect(mocks.startRuntimeTeamRun).toHaveBeenLastCalledWith(
      refreshedRun.id,
      { expected_version: refreshedRun.version },
      {
        idempotencyKey: expect.stringMatching(
          /^web-team-run-start-run-draft-1-/,
        ),
      },
    );
  });

  it("disables Start when Runtime refresh fails after a Start failure", async () => {
    mocks.getRuntimeTeamRun.mockRejectedValueOnce(
      new Error("Runtime refresh unavailable"),
    );
    mocks.startRuntimeTeamRun.mockRejectedValueOnce(
      new Error("Team Run version conflict"),
    );

    await render("supported");
    const startButton = Array.from(container.querySelectorAll("button")).find(
      (button) => button.textContent === "panels.teamsPanels.runs.start",
    );
    expect(startButton?.disabled).toBe(false);

    await act(async () => {
      startButton?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(container.textContent).toContain(
      "panels.teamsPanels.runs.stale Runtime refresh unavailable",
    );
    const recoveredStart = Array.from(
      container.querySelectorAll("button"),
    ).find((button) => button.textContent === "panels.teamsPanels.runs.start");
    expect(recoveredStart?.disabled).toBe(true);
    expect(mocks.getRuntimeTeamRunPreflight).toHaveBeenCalledTimes(1);
  });

  it("restores a linked Run outside the recent list from the URL", async () => {
    const linkedRun = {
      ...draftRun,
      id: "run-outside-recent-list",
      goal: "Restore this exact Run",
    };
    mocks.getRuntimeTeamRun.mockResolvedValueOnce({ run: linkedRun });
    await render(
      "unsupported",
      "supported",
      `/workspace?team_view=runs&team_run_id=${linkedRun.id}`,
    );

    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(mocks.getRuntimeTeamRun).toHaveBeenCalledWith(
      linkedRun.id,
      { signal: expect.any(AbortSignal) },
    );
    expect(container.textContent).toContain(linkedRun.goal);
  });

  it("backs off active Run polling from one to eight seconds", async () => {
    vi.useFakeTimers();
    const activeRun: RuntimeTeamRun = {
      ...draftRun,
      id: "run-active-backoff",
      status: "active",
      phase: "running",
    };
    mocks.getRuntimeTeamRun.mockResolvedValue({ run: activeRun });

    const advance = async (milliseconds: number) => {
      await act(async () => {
        vi.advanceTimersByTime(milliseconds);
        await Promise.resolve();
        await Promise.resolve();
      });
    };

    try {
      await act(async () => {
        root.render(
          <MemoryRouter
            initialEntries={[
              `/workspace?team_view=runs&team_run_id=${activeRun.id}`,
            ]}
          >
            <RuntimeTeamRunsView
              capabilities={{
                run_start: "unsupported",
                run_preflight: "unsupported",
              }}
              error={null}
              isLoading={false}
              onOpenLegacyTeam={() => undefined}
              onRetry={() => undefined}
              onRunUpdated={() => undefined}
              runs={[activeRun]}
            />
          </MemoryRouter>,
        );
        await Promise.resolve();
        await Promise.resolve();
      });

      expect(mocks.getRuntimeTeamRun).toHaveBeenCalledTimes(1);
      await advance(999);
      expect(mocks.getRuntimeTeamRun).toHaveBeenCalledTimes(1);
      await advance(1);
      expect(mocks.getRuntimeTeamRun).toHaveBeenCalledTimes(2);
      await advance(1_999);
      expect(mocks.getRuntimeTeamRun).toHaveBeenCalledTimes(2);
      await advance(1);
      expect(mocks.getRuntimeTeamRun).toHaveBeenCalledTimes(3);
      await advance(3_999);
      expect(mocks.getRuntimeTeamRun).toHaveBeenCalledTimes(3);
      await advance(1);
      expect(mocks.getRuntimeTeamRun).toHaveBeenCalledTimes(4);
      await advance(7_999);
      expect(mocks.getRuntimeTeamRun).toHaveBeenCalledTimes(4);
      await advance(1);
      expect(mocks.getRuntimeTeamRun).toHaveBeenCalledTimes(5);
    } finally {
      vi.useRealTimers();
    }
  });

  it("resets the active Run polling delay when the snapshot cursor advances", async () => {
    vi.useFakeTimers();
    const activeRun: RuntimeTeamRun = {
      ...draftRun,
      id: "run-active-progress",
      status: "active",
      phase: "running",
    };
    const progressedRun: RuntimeTeamRun = {
      ...activeRun,
      event_seq: 1,
      version: activeRun.version + 1,
    };
    mocks.getRuntimeTeamRun
      .mockResolvedValueOnce({ run: activeRun })
      .mockResolvedValueOnce({ run: progressedRun })
      .mockResolvedValue({ run: progressedRun });

    try {
      await act(async () => {
        root.render(
          <MemoryRouter
            initialEntries={[
              `/workspace?team_view=runs&team_run_id=${activeRun.id}`,
            ]}
          >
            <RuntimeTeamRunsView
              capabilities={{
                run_start: "unsupported",
                run_preflight: "unsupported",
              }}
              error={null}
              isLoading={false}
              onOpenLegacyTeam={() => undefined}
              onRetry={() => undefined}
              onRunUpdated={() => undefined}
              runs={[activeRun]}
            />
          </MemoryRouter>,
        );
        await Promise.resolve();
        await Promise.resolve();
      });
      expect(mocks.getRuntimeTeamRun).toHaveBeenCalledTimes(1);

      await act(async () => {
        vi.advanceTimersByTime(1_000);
        await Promise.resolve();
        await Promise.resolve();
      });
      expect(mocks.getRuntimeTeamRun).toHaveBeenCalledTimes(2);

      await act(async () => {
        vi.advanceTimersByTime(999);
        await Promise.resolve();
      });
      expect(mocks.getRuntimeTeamRun).toHaveBeenCalledTimes(2);
      await act(async () => {
        vi.advanceTimersByTime(1);
        await Promise.resolve();
        await Promise.resolve();
      });
      expect(mocks.getRuntimeTeamRun).toHaveBeenCalledTimes(3);
    } finally {
      vi.useRealTimers();
    }
  });

  it("does not let a late older Run snapshot overwrite newer observed state", async () => {
    vi.useFakeTimers();
    const activeRun: RuntimeTeamRun = {
      ...draftRun,
      id: "run-out-of-order-read",
      status: "active",
      phase: "running",
    };
    const newerRun: RuntimeTeamRun = {
      ...activeRun,
      version: activeRun.version + 1,
      event_seq: activeRun.event_seq + 1,
    };
    const onRunUpdated = vi.fn();
    mocks.getRuntimeTeamRun
      .mockResolvedValueOnce({ run: newerRun })
      .mockResolvedValueOnce({ run: activeRun })
      .mockResolvedValue({ run: newerRun });

    try {
      await act(async () => {
        root.render(
          <MemoryRouter
            initialEntries={[
              `/workspace?team_view=runs&team_run_id=${activeRun.id}`,
            ]}
          >
            <RuntimeTeamRunsView
              capabilities={{
                run_start: "unsupported",
                run_preflight: "unsupported",
              }}
              error={null}
              isLoading={false}
              onOpenLegacyTeam={() => undefined}
              onRetry={() => undefined}
              onRunUpdated={onRunUpdated}
              runs={[activeRun]}
            />
          </MemoryRouter>,
        );
        await Promise.resolve();
        await Promise.resolve();
      });

      expect(mocks.getRuntimeTeamRun).toHaveBeenCalledTimes(1);
      expect(onRunUpdated).toHaveBeenCalledTimes(1);
      expect(onRunUpdated).toHaveBeenLastCalledWith(newerRun);

      const versionLabel = Array.from(container.querySelectorAll("dt")).find(
        (label) => label.textContent === "panels.teamsPanels.runs.version",
      );
      expect(versionLabel?.nextElementSibling?.textContent).toBe(
        String(newerRun.version),
      );

      await act(async () => {
        vi.advanceTimersByTime(1_000);
        await Promise.resolve();
        await Promise.resolve();
      });

      expect(mocks.getRuntimeTeamRun).toHaveBeenCalledTimes(2);
      expect(onRunUpdated).toHaveBeenCalledTimes(1);
      expect(versionLabel?.nextElementSibling?.textContent).toBe(
        String(newerRun.version),
      );
      expect(container.textContent).toContain(String(newerRun.event_seq));
      expect(container.textContent).toContain(
        "panels.teamsPanels.runs.compatibilityTeamEventSequence",
      );
      expect(container.textContent).not.toContain(
        "panels.teamsPanels.runs.eventCursor",
      );
    } finally {
      vi.useRealTimers();
    }
  });

  it("honors Retry-After on 503 and keeps the last Run status", async () => {
    vi.useFakeTimers();
    const activeRun: RuntimeTeamRun = {
      ...draftRun,
      id: "run-temporarily-unavailable",
      status: "active",
      phase: "running",
    };
    mocks.getRuntimeTeamRun
      .mockRejectedValueOnce(
        new RuntimeApiError(
          503,
          { error: "temporarily unavailable" },
          new Headers({ "Retry-After": "3" }),
        ),
      )
      .mockResolvedValue({ run: activeRun });

    try {
      await act(async () => {
        root.render(
          <MemoryRouter
            initialEntries={[
              `/workspace?team_view=runs&team_run_id=${activeRun.id}`,
            ]}
          >
            <RuntimeTeamRunsView
              capabilities={{
                run_start: "unsupported",
                run_preflight: "unsupported",
              }}
              error={null}
              isLoading={false}
              onOpenLegacyTeam={() => undefined}
              onRetry={() => undefined}
              onRunUpdated={() => undefined}
              runs={[activeRun]}
            />
          </MemoryRouter>,
        );
        await Promise.resolve();
        await Promise.resolve();
      });

      expect(mocks.getRuntimeTeamRun).toHaveBeenCalledTimes(1);
      expect(container.textContent).toContain(
        "panels.teamsPanels.runs.temporarilyUnavailable temporarily unavailable",
      );
      expect(container.textContent).toContain("active");

      await act(async () => {
        vi.advanceTimersByTime(2_999);
        await Promise.resolve();
      });
      expect(mocks.getRuntimeTeamRun).toHaveBeenCalledTimes(1);
      await act(async () => {
        vi.advanceTimersByTime(1);
        await Promise.resolve();
        await Promise.resolve();
      });
      expect(mocks.getRuntimeTeamRun).toHaveBeenCalledTimes(2);
      expect(container.textContent).toContain("active");
      expect(container.textContent).not.toContain("failed");
    } finally {
      vi.useRealTimers();
    }
  });

  it("cancels Run polling while hidden and resumes on visibility", async () => {
    vi.useFakeTimers();
    const activeRun: RuntimeTeamRun = {
      ...draftRun,
      id: "run-hidden-poll",
      status: "active",
      phase: "running",
    };
    const visibility = vi
      .spyOn(document, "visibilityState", "get")
      .mockReturnValue("hidden");
    let resolveRead: ((value: { run: RuntimeTeamRun }) => void) | undefined;
    mocks.getRuntimeTeamRun.mockImplementation(
      () =>
        new Promise((resolve) => {
          resolveRead = resolve;
        }),
    );

    try {
      await act(async () => {
        root.render(
          <MemoryRouter
            initialEntries={[
              `/workspace?team_view=runs&team_run_id=${activeRun.id}`,
            ]}
          >
            <RuntimeTeamRunsView
              capabilities={{
                run_start: "unsupported",
                run_preflight: "unsupported",
              }}
              error={null}
              isLoading={false}
              onOpenLegacyTeam={() => undefined}
              onRetry={() => undefined}
              onRunUpdated={() => undefined}
              runs={[activeRun]}
            />
          </MemoryRouter>,
        );
        await Promise.resolve();
      });
      expect(mocks.getRuntimeTeamRun).not.toHaveBeenCalled();

      await act(async () => {
        vi.advanceTimersByTime(60_000);
        await Promise.resolve();
      });
      expect(mocks.getRuntimeTeamRun).not.toHaveBeenCalled();

      visibility.mockReturnValue("visible");
      await act(async () => {
        document.dispatchEvent(new Event("visibilitychange"));
        await Promise.resolve();
      });
      expect(mocks.getRuntimeTeamRun).toHaveBeenCalledTimes(1);

      const signal = mocks.getRuntimeTeamRun.mock.calls[0][1]?.signal;
      visibility.mockReturnValue("hidden");
      await act(async () => {
        document.dispatchEvent(new Event("visibilitychange"));
      });
      expect(signal?.aborted).toBe(true);

      await act(async () => {
        resolveRead?.({ run: activeRun });
        await Promise.resolve();
        await Promise.resolve();
      });
      expect(mocks.getRuntimeTeamRun).toHaveBeenCalledTimes(1);
      expect(vi.getTimerCount()).toBe(0);
    } finally {
      visibility.mockRestore();
      vi.useRealTimers();
    }
  });

  it("bounds terminal summary polling without changing the pending state", async () => {
    vi.useFakeTimers();
    const terminalPendingRun: RuntimeTeamRun = {
      ...draftRun,
      id: "run-terminal-pending",
      status: "done",
      phase: "terminal",
      summary_state: "pending",
    };
    mocks.getRuntimeTeamRun.mockResolvedValue({
      run: terminalPendingRun,
    });

    try {
      await act(async () => {
        root.render(
          <MemoryRouter
            initialEntries={[
              `/workspace?team_view=runs&team_run_id=${terminalPendingRun.id}`,
            ]}
          >
            <RuntimeTeamRunsView
              capabilities={{
                run_start: "unsupported",
                run_preflight: "unsupported",
              }}
              error={null}
              isLoading={false}
              onOpenLegacyTeam={() => undefined}
              onRetry={() => undefined}
              onRunUpdated={() => undefined}
              runs={[terminalPendingRun]}
            />
          </MemoryRouter>,
        );
        await Promise.resolve();
      });

      expect(mocks.getRuntimeTeamRun).toHaveBeenCalledTimes(1);
      for (let observation = 1; observation < 8; observation += 1) {
        await act(async () => {
          vi.advanceTimersByTime(5_000);
          await Promise.resolve();
          await Promise.resolve();
        });
      }
      expect(mocks.getRuntimeTeamRun).toHaveBeenCalledTimes(8);
      expect(container.textContent).toContain(
        "panels.teamsPanels.runs.summaryStates.pending",
      );

      await act(async () => {
        vi.advanceTimersByTime(30_000);
        await Promise.resolve();
      });
      expect(mocks.getRuntimeTeamRun).toHaveBeenCalledTimes(8);

      const refreshButton = Array.from(
        container.querySelectorAll("button"),
      ).find(
        (button) => button.textContent === "panels.teamsPanels.runs.refresh",
      );
      expect(refreshButton).toBeDefined();
      await act(async () => {
        refreshButton?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
        await Promise.resolve();
      });
      expect(mocks.getRuntimeTeamRun).toHaveBeenCalledTimes(9);
    } finally {
      vi.useRealTimers();
    }
  });

  it("does not reset terminal summary observation after a transient read failure", async () => {
    vi.useFakeTimers();
    const activeRun: RuntimeTeamRun = {
      ...draftRun,
      id: "run-terminal-after-progress",
      status: "active",
      phase: "running",
    };
    const terminalPendingRun: RuntimeTeamRun = {
      ...activeRun,
      status: "done",
      phase: "terminal",
      event_seq: activeRun.event_seq + 1,
      version: activeRun.version + 1,
      summary_state: "pending",
    };
    mocks.getRuntimeTeamRun
      .mockResolvedValueOnce({ run: terminalPendingRun })
      .mockRejectedValue(new Error("summary service temporarily unavailable"));

    const flushObservation = async (milliseconds: number) => {
      await act(async () => {
        vi.advanceTimersByTime(milliseconds);
        await Promise.resolve();
        await Promise.resolve();
      });
    };

    try {
      await act(async () => {
        root.render(
          <MemoryRouter
            initialEntries={[
              `/workspace?team_view=runs&team_run_id=${activeRun.id}`,
            ]}
          >
            <RuntimeTeamRunsView
              capabilities={{
                run_start: "unsupported",
                run_preflight: "unsupported",
              }}
              error={null}
              isLoading={false}
              onOpenLegacyTeam={() => undefined}
              onRetry={() => undefined}
              onRunUpdated={() => undefined}
              runs={[activeRun]}
            />
          </MemoryRouter>,
        );
        await Promise.resolve();
        await Promise.resolve();
      });

      expect(mocks.getRuntimeTeamRun).toHaveBeenCalledTimes(1);
      await flushObservation(5_000);
      expect(mocks.getRuntimeTeamRun).toHaveBeenCalledTimes(2);

      for (const delay of [1_000, 2_000, 4_000, 8_000, 8_000, 8_000]) {
        await flushObservation(delay);
      }
      expect(mocks.getRuntimeTeamRun).toHaveBeenCalledTimes(8);

      await flushObservation(30_000);
      expect(mocks.getRuntimeTeamRun).toHaveBeenCalledTimes(8);
      expect(container.textContent).toContain(
        "panels.teamsPanels.runs.summaryStates.pending",
      );
    } finally {
      vi.useRealTimers();
    }
  });

  it("writes the selected Run ID to the URL", async () => {
    const secondRun = {
      ...draftRun,
      id: "run-draft-2",
      goal: "Second Run",
    };
    await act(async () => {
      root.render(
        <MemoryRouter initialEntries={["/workspace?team_view=runs"]}>
          <RuntimeTeamRunsView
            capabilities={{
              run_start: "unsupported",
              run_preflight: "unsupported",
            }}
            error={null}
            isLoading={false}
            onOpenLegacyTeam={() => undefined}
            onRetry={() => undefined}
            onRunUpdated={() => undefined}
            runs={[draftRun, secondRun]}
          />
          <SearchParamsProbe />
        </MemoryRouter>,
      );
    });

    const secondRunButton = Array.from(
      container.querySelectorAll("button"),
    ).find((button) => button.textContent?.includes(secondRun.id));
    expect(secondRunButton).toBeDefined();
    await act(async () => {
      secondRunButton?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });

    expect(container.querySelector('[data-testid="team-run-search"]')?.textContent)
      .toContain(`team_run_id=${secondRun.id}`);
  });
});
