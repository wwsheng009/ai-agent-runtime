import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  getRuntimeTeam: vi.fn(),
  getRuntimeTeamFinalSummary: vi.fn(),
  listRuntimeTeamEvents: vi.fn(),
  listRuntimeTeamSummaries: vi.fn(),
  listRuntimeTeamTasks: vi.fn(),
}));

vi.mock("@/lib/runtime-api", () => mocks);

import { createEmptyDetails } from "@/components/workspace/runtime-teams/shared";
import { useTeamDetailsPolling } from "@/components/workspace/runtime-teams/use-team-details-polling";
import type { RuntimeTeamTask } from "@/types/runtime";

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

const existingTask: RuntimeTeamTask = {
  id: "task-existing",
  status: "running",
  title: "Last-known-good task",
};

function makeTeam(status: string) {
  return { team: { id: "team-1", status } };
}

function makeSummary(summaryState = "pending") {
  return {
    team_id: "team-1",
    summary: "",
    summary_state: summaryState,
  };
}

function makeEvents(seq: number, nextCursor = seq) {
  return {
    team_id: "team-1",
    events:
      seq > 0
        ? [
            {
              seq,
              type: "task.progress",
              team_id: "team-1",
              timestamp: "2026-11-23T00:00:00Z",
            },
          ]
        : [],
    latest_seq: nextCursor,
    next_cursor: nextCursor,
  };
}

function Harness({
  initialStatus = "active",
  initialSummaryState = "unknown",
  initialCursor = 4,
}: {
  initialStatus?: string;
  initialSummaryState?: "pending" | "ready" | "failed" | "not_applicable" | "unknown";
  initialCursor?: number;
}) {
  const [details, setDetails] = useState(() => ({
    ...createEmptyDetails(),
    events: [
      {
        seq: initialCursor,
        type: "team.started",
        team_id: "team-1",
        timestamp: "2026-11-23T00:00:00Z",
      },
    ],
    eventCursor: initialCursor,
    latestEventSeq: initialCursor,
    finalSummary: "",
    summaryState: initialSummaryState,
    tasks: [existingTask],
    isStale: false,
  }));
  const [error, setError] = useState<string | null>(null);

  useTeamDetailsPolling({
    eventCursor: details.eventCursor,
    isLoading: false,
    selectedTeamId: "team-1",
    setDetails,
    setDetailsError: setError,
    summaryState: details.summaryState,
    teamStatus: initialStatus,
  });

  return (
    <div
      data-error={error ?? ""}
      data-status={details.liveTeamStatus ?? ""}
      data-stale={String(details.isStale)}
      data-task={details.tasks[0]?.id ?? ""}
      data-cursor={String(details.eventCursor)}
      data-event-sequences={details.events.map((event) => event.seq).join(",")}
      data-summary-state={details.summaryState}
    />
  );
}

describe("useTeamDetailsPolling", () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
    vi.useFakeTimers();
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    mocks.getRuntimeTeam.mockReset().mockResolvedValue(makeTeam("active"));
    mocks.getRuntimeTeamFinalSummary
      .mockReset()
      .mockResolvedValue(makeSummary("pending"));
    mocks.listRuntimeTeamEvents.mockReset().mockResolvedValue(makeEvents(0, 4));
    mocks.listRuntimeTeamSummaries
      .mockReset()
      .mockResolvedValue({ teams: [] });
    mocks.listRuntimeTeamTasks
      .mockReset()
      .mockResolvedValue({ tasks: [{ id: "task-refreshed", status: "done" }] });
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    document.body.innerHTML = "";
    Object.defineProperty(document, "visibilityState", {
      configurable: true,
      value: "visible",
    });
    vi.useRealTimers();
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
  });

  async function render(props: Parameters<typeof Harness>[0] = {}) {
    await act(async () => {
      root.render(<Harness {...props} />);
    });
  }

  it("continues from the Team-local event cursor and exposes only Runtime status", async () => {
    mocks.listRuntimeTeamEvents.mockResolvedValueOnce(makeEvents(5, 6));
    await render({ initialCursor: 4 });

    await act(async () => {
      await vi.advanceTimersByTimeAsync(1_000);
    });

    expect(mocks.listRuntimeTeamEvents).toHaveBeenCalledWith(
      "team-1",
      expect.objectContaining({ afterSeq: 4, limit: 12 }),
    );
    expect(container.firstElementChild?.getAttribute("data-cursor")).toBe("6");
    expect(container.firstElementChild?.getAttribute("data-event-sequences")).toBe(
      "5,4",
    );
    expect(container.firstElementChild?.getAttribute("data-status")).toBe("active");
  });

  it("retains last-known-good data on polling error and clears stale after recovery", async () => {
    mocks.listRuntimeTeamTasks
      .mockResolvedValue({
        tasks: [{ id: "task-recovered", status: "done" }],
      })
      .mockRejectedValueOnce(new Error("temporarily unavailable"));
    await render();

    await act(async () => {
      await vi.advanceTimersByTimeAsync(1_000);
    });
    expect(container.firstElementChild?.getAttribute("data-stale")).toBe("true");
    expect(container.firstElementChild?.getAttribute("data-task")).toBe(
      "task-existing",
    );
    expect(container.firstElementChild?.getAttribute("data-status")).toBe("active");
    expect(container.firstElementChild?.getAttribute("data-error")).toContain(
      "tasks",
    );

    await act(async () => {
      await vi.advanceTimersByTimeAsync(2_000);
    });
    expect(container.firstElementChild?.getAttribute("data-stale")).toBe("false");
    expect(container.firstElementChild?.getAttribute("data-task")).toBe(
      "task-recovered",
    );
    expect(container.firstElementChild?.getAttribute("data-error")).toBe("");
    expect(container.firstElementChild?.getAttribute("data-status")).toBe("active");
  });

  it("stops terminal polling once Runtime reports summary ready", async () => {
    mocks.getRuntimeTeam.mockResolvedValue(makeTeam("done"));
    mocks.getRuntimeTeamFinalSummary.mockResolvedValue({
      team_id: "team-1",
      summary: "The run is complete.",
      summary_state: "ready",
    });
    await render({ initialStatus: "done", initialSummaryState: "pending" });

    await act(async () => {
      await vi.advanceTimersByTimeAsync(2_000);
    });
    expect(container.firstElementChild?.getAttribute("data-summary-state")).toBe(
      "ready",
    );
    const callsAfterReady = mocks.getRuntimeTeam.mock.calls.length;

    await act(async () => {
      await vi.advanceTimersByTimeAsync(20_000);
    });
    expect(mocks.getRuntimeTeam).toHaveBeenCalledTimes(callsAfterReady);
  });

  it("aborts an in-flight observation when the selected Team unmounts", async () => {
    let requestSignal: AbortSignal | undefined;
    mocks.getRuntimeTeam.mockImplementation(
      (_teamId: string, options: { signal: AbortSignal }) => {
        requestSignal = options.signal;
        return new Promise(() => {});
      },
    );
    await render();

    await act(async () => {
      await vi.advanceTimersByTimeAsync(1_000);
    });
    expect(requestSignal).toBeDefined();
    act(() => root.unmount());
    expect(requestSignal?.aborted).toBe(true);
  });

  it("pauses polling while the browser page is hidden and resumes from its cursor", async () => {
    Object.defineProperty(document, "visibilityState", {
      configurable: true,
      value: "hidden",
    });
    await render();

    await act(async () => {
      await vi.advanceTimersByTimeAsync(10_000);
    });
    expect(mocks.getRuntimeTeam).not.toHaveBeenCalled();

    Object.defineProperty(document, "visibilityState", {
      configurable: true,
      value: "visible",
    });
    await act(async () => {
      document.dispatchEvent(new Event("visibilitychange"));
      await vi.advanceTimersByTimeAsync(1_000);
    });
    expect(mocks.getRuntimeTeam).toHaveBeenCalledTimes(1);
    expect(mocks.listRuntimeTeamEvents).toHaveBeenCalledWith(
      "team-1",
      expect.objectContaining({ afterSeq: 4 }),
    );
  });
});
