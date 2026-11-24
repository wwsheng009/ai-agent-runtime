import { describe, expect, it } from "vitest";

import {
  buildDispatchComparisonRows,
  formatDispatchOutcomeLabel,
  getDispatchOutcomeNarrative,
  getTeamDetailsPollDelay,
  isTerminalDispatchStatus,
  mergeTeamFinalSummary,
  mergeTeamEvents,
  normalizeDispatchOutcomeKey,
  normalizeTaskTitle,
  resolveDispatchRolePlan,
  shouldExpectDispatchOutcomeSummary,
  sortDispatchComparisonRows,
  summarizeDispatchBatch,
  sortDispatchMonitor,
  shouldPollTeamDetails,
} from "@/components/workspace/runtime-teams/shared";

describe("runtime teams shared helpers", () => {
  it("builds role-aware defaults for review/implement/verify fan-out", () => {
    expect(resolveDispatchRolePlan("review_implement_verify", 0).key).toBe("review");
    expect(resolveDispatchRolePlan("review_implement_verify", 1).key).toBe("implement");
    expect(resolveDispatchRolePlan("review_implement_verify", 2).key).toBe("verify");
    expect(resolveDispatchRolePlan("mirror", 3)).toMatchObject({
      key: "mirror",
      label: "Mirror",
    });
  });

  it("normalizes task titles from explicit text or goal fallback", () => {
    expect(normalizeTaskTitle("  Ship it  ", "ignored")).toBe("Ship it");
    expect(normalizeTaskTitle("", "short goal")).toBe("short goal");
    expect(
      normalizeTaskTitle("", "a".repeat(80)),
    ).toBe(`${"a".repeat(69)}...`);
  });

  it("keeps the last known summary while preserving explicit summary state", () => {
    expect(
      mergeTeamFinalSummary(
        { finalSummary: "previous summary", summaryState: "ready" },
        undefined,
      ),
    ).toEqual({
      finalSummary: "previous summary",
      summaryState: "ready",
    });

    expect(
      mergeTeamFinalSummary(
        { finalSummary: "previous summary", summaryState: "ready" },
        { team_id: "team-1", summary: "  still generating  ", summary_state: "pending" },
      ),
    ).toEqual({
      finalSummary: "still generating",
      summaryState: "pending",
    });

    expect(
      mergeTeamFinalSummary(
        { finalSummary: "last completed summary", summaryState: "ready" },
        { team_id: "team-1", summary: "", summary_state: "pending" },
      ),
    ).toEqual({
      finalSummary: "last completed summary",
      summaryState: "pending",
    });

    expect(
      mergeTeamFinalSummary(
        { finalSummary: "last completed summary", summaryState: "ready" },
        { team_id: "team-1", summary: "" },
      ),
    ).toEqual({
      finalSummary: "last completed summary",
      summaryState: "ready",
    });

    expect(
      mergeTeamFinalSummary(
        { finalSummary: "last completed summary", summaryState: "ready" },
        { team_id: "team-1", summary: "", summary_state: "unknown" },
      ),
    ).toEqual({
      finalSummary: "last completed summary",
      summaryState: "ready",
    });

    expect(
      mergeTeamFinalSummary(
        { finalSummary: "last completed summary", summaryState: "ready" },
        { team_id: "team-1", summary: "", summary_state: "ready" },
      ),
    ).toEqual({
      finalSummary: "",
      summaryState: "ready",
    });
  });

  it("merges team events by team-local sequence without duplicating prior events", () => {
    const existing = [
      {
        seq: 3,
        type: "team.completed",
        team_id: "team-1",
        timestamp: "2026-11-22T00:00:03Z",
      },
      {
        seq: 1,
        type: "team.started",
        team_id: "team-1",
        timestamp: "2026-11-22T00:00:01Z",
      },
    ];
    const incoming = [
      {
        seq: 3,
        type: "team.completed",
        team_id: "team-1",
        timestamp: "2026-11-22T00:00:03Z",
      },
      {
        seq: 2,
        type: "task.completed",
        team_id: "team-1",
        timestamp: "2026-11-22T00:00:02Z",
      },
    ];

    expect(mergeTeamEvents(existing, incoming).map((event) => event.seq)).toEqual([
      3, 2, 1,
    ]);
  });

  it("keeps the details monitor read-only and polls terminal summaries only briefly", () => {
    expect(shouldPollTeamDetails("active", "unknown")).toBe(true);
    expect(shouldPollTeamDetails("running", "ready")).toBe(true);
    expect(shouldPollTeamDetails("done", "pending")).toBe(true);
    expect(shouldPollTeamDetails("done", "ready")).toBe(false);
    expect(shouldPollTeamDetails("failed", "failed")).toBe(false);
    expect(shouldPollTeamDetails("done", "unknown", 8)).toBe(false);
    expect(shouldPollTeamDetails("unknown", "pending")).toBe(false);

    expect(getTeamDetailsPollDelay("active", "unknown", 0)).toBe(1_000);
    expect(getTeamDetailsPollDelay("active", "unknown", 3)).toBe(8_000);
    expect(getTeamDetailsPollDelay("active", "unknown", 99)).toBe(8_000);
    expect(getTeamDetailsPollDelay("done", "pending", 0)).toBe(2_000);
  });

  it("sorts dispatch monitor entries by latest update and identifies terminal states", () => {
    const sorted = sortDispatchMonitor([
      {
        mailboxPreview: [],
        status: "running",
        taskId: "task-1",
        teamId: "team-1",
        updatedAt: "2026-03-31T08:00:00Z",
      },
      {
        mailboxPreview: [],
        status: "done",
        taskId: "task-2",
        teamId: "team-2",
        updatedAt: "2026-03-31T09:00:00Z",
      },
    ]);

    expect(sorted.map((entry) => entry.taskId)).toEqual(["task-2", "task-1"]);
    expect(isTerminalDispatchStatus("done")).toBe(true);
    expect(isTerminalDispatchStatus("FAILED")).toBe(true);
    expect(isTerminalDispatchStatus("running")).toBe(false);
  });

  it("summarizes dispatch batches and builds compare rows from results + monitor", () => {
    const rows = buildDispatchComparisonRows(
      [
        { status: "created", teamId: "team-1", taskId: "task-1" },
        { status: "created", teamId: "team-2", taskId: "task-2" },
        { status: "created", teamId: "team-4", taskId: "task-4" },
        { status: "failed", teamId: "team-3", error: "boom" },
      ],
      [
        {
          mailboxPreview: ["mail-1"],
          status: "running",
          taskId: "task-1",
          teamId: "team-1",
          updatedAt: "2026-03-31T09:00:00Z",
        },
        {
          mailboxPreview: [],
          status: "done",
          taskId: "task-2",
          teamId: "team-2",
          updatedAt: "2026-03-31T09:30:00Z",
        },
      ],
    );

    expect(rows).toHaveLength(4);
    expect(rows[0].hasMonitor).toBe(true);
    expect(rows[1].status).toBe("done");
    expect(rows[2].outcomeKey).toBe("pending");
    expect(rows[3].created).toBe(false);
    expect(rows[1].outcomeLabel).toBe("Completed");
    expect(rows[3].outcomeKey).toBe("dispatch_failed");

    const summary = summarizeDispatchBatch(rows);
    expect(summary.activeCount).toBe(1);
    expect(summary.attemptedCount).toBe(4);
    expect(summary.completedCount).toBe(1);
    expect(summary.createdCount).toBe(3);
    expect(summary.failedCount).toBe(1);
    expect(summary.pendingCount).toBe(1);
    expect(summary.finalSummaryCount).toBe(0);
    expect(summary.monitorCoverageCount).toBe(2);
    expect(summary.summaryEligibleTerminalCount).toBe(1);
    expect(summary.terminalCount).toBe(2);
    expect(summary.terminalWithoutSummaryCount).toBe(1);
    expect(summary.latestUpdatedAt).toBe("2026-03-31T09:30:00Z");
    expect(summary.statusCounts.done).toBe(1);
    expect(summary.statusCounts.running).toBe(1);
    expect(summary.statusCounts["dispatch failed"]).toBe(1);
  });

  it("normalizes and sorts outcome-oriented compare rows", () => {
    expect(normalizeDispatchOutcomeKey("FAILED")).toBe("failed");
    expect(formatDispatchOutcomeLabel("blocked", true)).toBe("Blocked");

    const sorted = sortDispatchComparisonRows([
      {
        created: true,
        detailLabel: "task",
        detailText: "task t-1",
        hasMonitor: true,
        isTerminal: false,
        mailboxPreview: [],
        outcomeKey: "running",
        outcomeLabel: "In progress",
        status: "running",
        taskId: "task-1",
        teamId: "team-1",
        updatedAt: "2026-03-31T08:00:00Z",
      },
      {
        created: true,
        detailLabel: "task",
        detailText: "task t-2",
        hasMonitor: true,
        isTerminal: true,
        mailboxPreview: [],
        outcomeKey: "done",
        outcomeLabel: "Completed",
        status: "done",
        summary: "finished",
        summaryLabel: "final summary",
        taskId: "task-2",
        teamId: "team-2",
        updatedAt: "2026-03-31T07:00:00Z",
      },
    ]);

    expect(sorted.map((row) => row.teamId)).toEqual(["team-2", "team-1"]);
  });

  it("builds readable outcome narrative for summary, errors, mailbox preview, and gaps", () => {
    expect(
      getDispatchOutcomeNarrative({
        created: true,
        detailLabel: "task",
        detailText: "task-1",
        hasMonitor: true,
        isTerminal: true,
        mailboxPreview: [],
        outcomeKey: "done",
        outcomeLabel: "Completed",
        status: "done",
        summary: "finished cleanly",
        summaryLabel: "final summary",
        taskId: "task-1",
        teamId: "team-1",
      }),
    ).toEqual({
      label: "final summary",
      text: "finished cleanly",
    });

    expect(
      getDispatchOutcomeNarrative({
        created: false,
        detailLabel: "dispatch",
        detailText: "dispatch failed",
        error: "boom",
        hasMonitor: false,
        isTerminal: true,
        mailboxPreview: [],
        outcomeKey: "dispatch_failed",
        outcomeLabel: "Dispatch failed",
        status: "dispatch failed",
        teamId: "team-2",
      }),
    ).toEqual({
      label: "error",
      text: "boom",
    });

    expect(
      getDispatchOutcomeNarrative({
        created: true,
        detailLabel: "event",
        detailText: "team / mailbox",
        hasMonitor: true,
        isTerminal: false,
        mailboxPreview: ["worker pinged lead"],
        outcomeKey: "running",
        outcomeLabel: "In progress",
        status: "running",
        taskId: "task-3",
        teamId: "team-3",
      }),
    ).toEqual({
      label: "mailbox preview",
      text: "worker pinged lead",
    });

    expect(
      getDispatchOutcomeNarrative({
        created: true,
        detailLabel: "task",
        detailText: "task task-4",
        hasMonitor: true,
        isTerminal: true,
        mailboxPreview: [],
        outcomeKey: "done",
        outcomeLabel: "Completed",
        status: "done",
        taskId: "task-4",
        teamId: "team-4",
      }),
    ).toEqual({
      label: "final summary",
      text: "No final summary reported yet.",
    });
  });

  it("only expects final summaries for created terminal task outcomes", () => {
    expect(
      shouldExpectDispatchOutcomeSummary({
        created: true,
        detailLabel: "task",
        detailText: "task task-1",
        hasMonitor: true,
        isTerminal: true,
        mailboxPreview: [],
        outcomeKey: "done",
        outcomeLabel: "Completed",
        status: "done",
        taskId: "task-1",
        teamId: "team-1",
      }),
    ).toBe(true);

    expect(
      shouldExpectDispatchOutcomeSummary({
        created: false,
        detailLabel: "dispatch",
        detailText: "dispatch failed",
        hasMonitor: false,
        isTerminal: true,
        mailboxPreview: [],
        outcomeKey: "dispatch_failed",
        outcomeLabel: "Dispatch failed",
        status: "dispatch failed",
        teamId: "team-2",
      }),
    ).toBe(false);
  });
});
