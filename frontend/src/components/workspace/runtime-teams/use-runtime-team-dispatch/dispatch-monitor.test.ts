import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@/lib/runtime-api", () => ({
  getRuntimeTeamReadiness: vi.fn(),
  listRuntimeTeamEvents: vi.fn(),
  listRuntimeTeamMailbox: vi.fn(),
  listRuntimeTeamTasks: vi.fn(),
  listRuntimeTeamTeammates: vi.fn(),
}));

import {
  getRuntimeTeamReadiness,
  listRuntimeTeamTeammates,
} from "@/lib/runtime-api";

import { loadDispatchTeamReadiness } from "./dispatch-monitor";

const getReadinessMock = vi.mocked(getRuntimeTeamReadiness);
const listTeammatesMock = vi.mocked(listRuntimeTeamTeammates);

describe("loadDispatchTeamReadiness", () => {
  beforeEach(() => {
    getReadinessMock.mockReset();
    listTeammatesMock.mockReset();
  });

  it("uses Runtime preflight findings instead of deriving readiness from roster fields", async () => {
    getReadinessMock
      .mockResolvedValueOnce({
        team_id: "team-blocked",
        readiness: false,
        preflight: {
          team_id: "team-blocked",
          team_exists: true,
          ready: false,
          blocker_count: 1,
          warning_count: 0,
          info_count: 0,
          findings: [
            {
              severity: "blocker",
              code: "dependency_cycle",
              message: "The task graph contains a cycle.",
            },
          ],
        },
      })
      .mockResolvedValueOnce({
        team_id: "team-ready",
        readiness: true,
        preflight: {
          team_id: "team-ready",
          team_exists: true,
          ready: true,
          blocker_count: 0,
          warning_count: 1,
          info_count: 0,
          findings: [],
        },
      });
    listTeammatesMock.mockResolvedValue({
      teammates: [
        {
          id: "mate-1",
          team_id: "team-1",
          session_id: "session-1",
          state: "idle",
        },
      ],
      count: 1,
    });
    const controller = new AbortController();

    const result = await loadDispatchTeamReadiness(
      [
        { id: "team-blocked", status: "active" },
        { id: "team-ready", status: "active" },
      ],
      controller.signal,
    );

    expect(getReadinessMock).toHaveBeenNthCalledWith(1, "team-blocked", {
      signal: controller.signal,
    });
    expect(listTeammatesMock).toHaveBeenNthCalledWith(1, "team-blocked", {
      limit: 24,
      signal: controller.signal,
    });
    expect(result).toEqual({
      "team-blocked": {
        executable: false,
        reason: "The task graph contains a cycle.",
        runnableTeammates: 1,
        totalTeammates: 1,
      },
      "team-ready": {
        executable: true,
        reason: "Runtime preflight permits task dispatch",
        runnableTeammates: 1,
        totalTeammates: 1,
      },
    });
  });

  it("keeps an inactive Team non-executable even if its last preflight passed", async () => {
    getReadinessMock.mockResolvedValue({
      team_id: "team-paused",
      readiness: true,
      preflight: {
        team_id: "team-paused",
        team_exists: true,
        ready: true,
        blocker_count: 0,
        warning_count: 0,
        info_count: 0,
      },
    });
    listTeammatesMock.mockResolvedValue({
      teammates: [],
      count: 0,
    });

    const result = await loadDispatchTeamReadiness([
      { id: "team-paused", status: "paused" },
    ]);

    expect(result["team-paused"]).toEqual({
      executable: false,
      reason: "team is not active",
      runnableTeammates: 0,
      totalTeammates: 0,
    });
  });

  it("uses Runtime team_status as the authoritative active-state check", async () => {
    getReadinessMock.mockResolvedValue({
      team_id: "team-runtime-paused",
      readiness: true,
      team_status: "paused",
      preflight: {
        team_id: "team-runtime-paused",
        team_exists: true,
        ready: true,
        blocker_count: 0,
        warning_count: 0,
        info_count: 0,
      },
    });
    listTeammatesMock.mockResolvedValue({
      teammates: [
        {
          id: "mate-1",
          team_id: "team-runtime-paused",
          session_id: "session-1",
          state: "idle",
        },
      ],
      count: 1,
    });

    const result = await loadDispatchTeamReadiness([
      { id: "team-runtime-paused", status: "active" },
    ]);

    expect(result["team-runtime-paused"]).toEqual({
      executable: false,
      reason: "team is not active",
      runnableTeammates: 1,
      totalTeammates: 1,
    });
  });

  it("allows initial task dispatch when the only preflight blocker is an empty task graph", async () => {
    getReadinessMock.mockResolvedValue({
      team_id: "team-empty-graph",
      readiness: false,
      preflight: {
        team_id: "team-empty-graph",
        team_exists: true,
        ready: false,
        blocker_count: 1,
        warning_count: 0,
        info_count: 0,
        findings: [
          {
            severity: "blocker",
            code: "team_tasks_empty",
            message: "The Team has no tasks to execute.",
          },
        ],
      },
    });
    listTeammatesMock.mockResolvedValue({
      teammates: [
        {
          id: "mate-1",
          team_id: "team-empty-graph",
          session_id: "session-1",
          state: "idle",
        },
      ],
      count: 1,
    });

    const result = await loadDispatchTeamReadiness([
      { id: "team-empty-graph", status: "active" },
    ]);

    expect(result["team-empty-graph"].executable).toBe(true);
    expect(result["team-empty-graph"].reason).toBe(
      "Runtime preflight permits task dispatch",
    );
  });

  it("does not let an empty-task blocker bypass contradictory blocker metadata", async () => {
    getReadinessMock.mockResolvedValue({
      team_id: "team-inconsistent-empty-graph",
      readiness: false,
      preflight: {
        team_id: "team-inconsistent-empty-graph",
        team_exists: true,
        ready: false,
        blocker_count: 2,
        warning_count: 0,
        info_count: 0,
        findings: [
          {
            severity: "blocker",
            code: "team_tasks_empty",
            message: "The Team has no tasks to execute.",
          },
        ],
      },
    });
    listTeammatesMock.mockResolvedValue({
      teammates: [
        {
          id: "mate-1",
          team_id: "team-inconsistent-empty-graph",
          session_id: "session-1",
          state: "idle",
        },
      ],
      count: 1,
    });

    const result = await loadDispatchTeamReadiness([
      { id: "team-inconsistent-empty-graph", status: "active" },
    ]);

    expect(result["team-inconsistent-empty-graph"]).toEqual({
      executable: false,
      reason: "Runtime preflight did not confirm task dispatch readiness",
      runnableTeammates: 1,
      totalTeammates: 1,
    });
  });

  it("does not infer readiness from a contradictory ready flag and blocker count", async () => {
    getReadinessMock.mockResolvedValue({
      team_id: "team-inconsistent-ready",
      readiness: true,
      preflight: {
        team_id: "team-inconsistent-ready",
        team_exists: true,
        ready: true,
        blocker_count: 1,
        warning_count: 0,
        info_count: 0,
        findings: [],
      },
    });
    listTeammatesMock.mockResolvedValue({
      teammates: [
        {
          id: "mate-1",
          team_id: "team-inconsistent-ready",
          session_id: "session-1",
          state: "idle",
        },
      ],
      count: 1,
    });

    const result = await loadDispatchTeamReadiness([
      { id: "team-inconsistent-ready", status: "active" },
    ]);

    expect(result["team-inconsistent-ready"]).toEqual({
      executable: false,
      reason: "Runtime preflight did not confirm task dispatch readiness",
      runnableTeammates: 1,
      totalTeammates: 1,
    });
  });

  it("does not ignore a blocker list that contradicts zero blocker_count", async () => {
    getReadinessMock.mockResolvedValue({
      team_id: "team-inconsistent-empty-count",
      readiness: true,
      preflight: {
        team_id: "team-inconsistent-empty-count",
        team_exists: true,
        ready: true,
        blocker_count: 0,
        warning_count: 0,
        info_count: 0,
        blockers: [
          {
            severity: "blocker",
            code: "team_tasks_empty",
            message: "The Team has no tasks to execute.",
          },
        ],
      },
    });
    listTeammatesMock.mockResolvedValue({
      teammates: [
        {
          id: "mate-1",
          team_id: "team-inconsistent-empty-count",
          session_id: "session-1",
          state: "idle",
        },
      ],
      count: 1,
    });

    const result = await loadDispatchTeamReadiness([
      { id: "team-inconsistent-empty-count", status: "active" },
    ]);

    expect(result["team-inconsistent-empty-count"]).toEqual({
      executable: false,
      reason: "Runtime preflight did not confirm task dispatch readiness",
      runnableTeammates: 1,
      totalTeammates: 1,
    });
  });
});
