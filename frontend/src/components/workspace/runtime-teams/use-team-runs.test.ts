import { describe, expect, it } from "vitest";

import type { RuntimeTeamRun } from "@/lib/runtime-api";

import {
  mergeRuntimeTeamRunList,
  mergeRuntimeTeamRunSnapshot,
} from "./use-team-runs";

const currentRun: RuntimeTeamRun = {
  id: "run-1",
  owner_id: "owner-1",
  workspace_id: "workspace-1",
  goal: "current",
  status: "active",
  phase: "running",
  compatibility_mode: "legacy_team_view",
  snapshot_schema_version: 1,
  config_snapshot: {},
  snapshot_checksum: "checksum",
  version: 4,
  event_seq: 8,
  summary_state: "pending",
  created_at: "2026-11-23T00:00:00Z",
};

describe("mergeRuntimeTeamRunSnapshot", () => {
  it("does not let a stale list update move the selected Run backwards", () => {
    const olderRun = {
      ...currentRun,
      goal: "stale",
      version: 3,
      event_seq: 7,
    };

    const current = [currentRun];
    const result = mergeRuntimeTeamRunSnapshot(current, olderRun);

    expect(result).toBe(current);
    expect(result[0]).toBe(currentRun);
  });

  it("accepts forward progress and prepends a newly observed Run", () => {
    const progressedRun = {
      ...currentRun,
      goal: "progressed",
      version: 5,
      event_seq: 9,
    };
    const progressed = mergeRuntimeTeamRunSnapshot([currentRun], progressedRun);

    expect(progressed[0]).toBe(progressedRun);

    const newRun = { ...currentRun, id: "run-2", goal: "new" };
    const withNewRun = mergeRuntimeTeamRunSnapshot(progressed, newRun);
    expect(withNewRun[0]).toBe(newRun);
    expect(withNewRun[1]).toBe(progressedRun);
  });

  it("keeps a newer cached snapshot when a refresh list is older", () => {
    const cached = { ...currentRun, goal: "cached", version: 6, event_seq: 10 };
    const result = mergeRuntimeTeamRunList(
      [cached],
      [{ ...currentRun, goal: "refresh response", version: 5, event_seq: 9 }],
    );

    expect(result).toHaveLength(1);
    expect(result[0]).toBe(cached);
  });
});
