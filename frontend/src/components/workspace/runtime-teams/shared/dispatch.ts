// 由 components/workspace/runtime-teams/shared.ts 机械拆分而来（P0-2），仅搬迁不改语义。
// 派发监控对比与结论叙事（对比行构建 / 批次汇总 / 终态与轮询判定 / 角色计划）

import {
  type DispatchBatchSummary,
  type DispatchComparisonRow,
  type DispatchMonitorEntry,
  type DispatchOutcomeNarrative,
  type DispatchRolePlan,
  type DispatchTemplateMode,
  type MultiTeamDispatchResult,
} from "@/components/workspace/runtime-teams/shared/types";
import { prettyEventType, truncateIdentifier } from "@/components/workspace/runtime-teams/shared/format";

export function countDispatchMonitorStatuses(entries: DispatchMonitorEntry[]) {
  const counts: Record<string, number> = {};
  for (const entry of entries) {
    const key = (entry.status || "unknown").trim().toLowerCase() || "unknown";
    counts[key] = (counts[key] ?? 0) + 1;
  }
  return counts;
}

export function buildDispatchComparisonRows(
  results: MultiTeamDispatchResult[],
  monitorEntries: DispatchMonitorEntry[],
) {
  const monitorMap = new Map(
    monitorEntries.map((entry) => [`${entry.teamId}:${entry.taskId}`, entry]),
  );

  return results.map((result) => {
    if (result.status === "failed") {
      return {
        created: false,
        detailLabel: "dispatch",
        detailText: result.error || "dispatch failed",
        error: result.error,
        hasMonitor: false,
        isTerminal: true,
        mailboxPreview: [],
        outcomeKey: "dispatch_failed",
        outcomeLabel: "Dispatch failed",
        status: "dispatch failed",
        summaryLabel: "failure",
        teamId: result.teamId,
      } satisfies DispatchComparisonRow;
    }

    const monitorEntry = result.taskId
      ? monitorMap.get(`${result.teamId}:${result.taskId}`)
      : undefined;

    if (!monitorEntry) {
      return {
        created: true,
        detailLabel: "task",
        detailText: result.taskId
          ? `task ${truncateIdentifier(result.taskId, 16)} created`
          : "task pending",
        hasMonitor: false,
        isTerminal: false,
        mailboxPreview: [],
        outcomeKey: "pending",
        outcomeLabel: "Pending outcome",
        status: "pending",
        taskId: result.taskId,
        teamId: result.teamId,
      } satisfies DispatchComparisonRow;
    }

    const outcomeKey = normalizeDispatchOutcomeKey(monitorEntry.status);
    const isTerminal = isTerminalDispatchStatus(monitorEntry.status);

    return {
      assignee: monitorEntry.assignee,
      created: true,
      detailLabel: monitorEntry.lastEventType ? "event" : "task",
      detailText: monitorEntry.lastEventType
        ? prettyEventType(monitorEntry.lastEventType)
        : monitorEntry.taskId
          ? `task ${truncateIdentifier(monitorEntry.taskId, 16)}`
          : "monitor attached",
      error: monitorEntry.error,
      hasMonitor: true,
      isTerminal,
      lastEventType: monitorEntry.lastEventType,
      mailboxPreview: monitorEntry.mailboxPreview,
      outcomeKey,
      outcomeLabel: formatDispatchOutcomeLabel(outcomeKey, isTerminal),
      status: monitorEntry.status,
      summary: monitorEntry.summary,
      summaryLabel: isTerminal ? "final summary" : "latest summary",
      taskId: monitorEntry.taskId,
      teamId: monitorEntry.teamId,
      updatedAt: monitorEntry.updatedAt,
    } satisfies DispatchComparisonRow;
  });
}

export function summarizeDispatchBatch(rows: DispatchComparisonRow[]) {
  const statusCounts: Record<string, number> = {};
  let latestUpdatedAt: string | undefined;

  for (const row of rows) {
    const key = (row.status || "unknown").trim().toLowerCase() || "unknown";
    statusCounts[key] = (statusCounts[key] ?? 0) + 1;

    if (!row.updatedAt) {
      continue;
    }

    const nextTime = new Date(row.updatedAt).getTime();
    const currentTime = latestUpdatedAt ? new Date(latestUpdatedAt).getTime() : 0;
    if (Number.isNaN(nextTime)) {
      continue;
    }
    if (!latestUpdatedAt || nextTime > currentTime) {
      latestUpdatedAt = row.updatedAt;
    }
  }

  const createdRows = rows.filter((row) => row.created);
  const failedRows = rows.filter((row) => !row.created);
  const monitorCoverageCount = createdRows.filter((row) => row.hasMonitor).length;
  const terminalRows = rows.filter((row) => row.isTerminal);
  const terminalCount = terminalRows.length;
  const completedCount = rows.filter((row) => row.outcomeKey === "done").length;
  const activeCount = rows.filter((row) => row.outcomeKey === "running").length;
  const pendingCount = rows.filter((row) => row.outcomeKey === "pending").length;
  const summaryEligibleTerminalRows = terminalRows.filter(
    shouldExpectDispatchOutcomeSummary,
  );
  const finalSummaryCount = summaryEligibleTerminalRows.filter(
    (row) => Boolean(row.summary?.trim()),
  ).length;
  const terminalWithoutSummaryCount = summaryEligibleTerminalRows.filter(
    (row) => !row.summary?.trim(),
  ).length;

  return {
    activeCount,
    attemptedCount: rows.length,
    completedCount,
    createdCount: createdRows.length,
    failedCount: failedRows.length,
    finalSummaryCount,
    latestUpdatedAt,
    monitorCoverageCount,
    pendingCount,
    summaryEligibleTerminalCount: summaryEligibleTerminalRows.length,
    statusCounts,
    terminalCount,
    terminalWithoutSummaryCount,
    terminalRows,
  } satisfies DispatchBatchSummary;
}

export function sortDispatchComparisonRows(rows: DispatchComparisonRow[]) {
  return [...rows].sort((left, right) => {
    if (left.isTerminal !== right.isTerminal) {
      return left.isTerminal ? -1 : 1;
    }

    const leftUpdated = left.updatedAt ? new Date(left.updatedAt).getTime() : 0;
    const rightUpdated = right.updatedAt ? new Date(right.updatedAt).getTime() : 0;
    if (leftUpdated !== rightUpdated) {
      return rightUpdated - leftUpdated;
    }

    return left.teamId.localeCompare(right.teamId);
  });
}

export function isTerminalDispatchStatus(status: string | undefined) {
  switch ((status || "").trim().toLowerCase()) {
    case "done":
    case "failed":
    case "blocked":
    case "cancelled":
      return true;
    default:
      return false;
  }
}

export function normalizeDispatchOutcomeKey(status: string | undefined) {
  switch ((status || "").trim().toLowerCase()) {
    case "done":
      return "done";
    case "failed":
      return "failed";
    case "blocked":
      return "blocked";
    case "cancelled":
      return "cancelled";
    case "running":
    case "busy":
      return "running";
    case "pending":
      return "pending";
    default:
      return "unknown";
  }
}

export function formatDispatchOutcomeLabel(
  outcomeKey: string,
  isTerminal: boolean,
) {
  switch (outcomeKey) {
    case "done":
      return "Completed";
    case "failed":
      return "Failed outcome";
    case "blocked":
      return "Blocked";
    case "cancelled":
      return "Cancelled";
    case "running":
      return "In progress";
    case "pending":
      return "Pending outcome";
    case "dispatch_failed":
      return "Dispatch failed";
    default:
      return isTerminal ? "Terminal outcome" : "Outcome pending";
  }
}

export function getDispatchOutcomeNarrative(
  row: DispatchComparisonRow,
): DispatchOutcomeNarrative {
  if (row.summary?.trim()) {
    return {
      label: row.summaryLabel || (row.isTerminal ? "final summary" : "latest summary"),
      text: row.summary.trim(),
    };
  }

  if (row.error?.trim()) {
    return {
      label: "error",
      text: row.error.trim(),
    };
  }

  if (row.mailboxPreview.length > 0) {
    return {
      label: "mailbox preview",
      text: row.mailboxPreview[0],
    };
  }

  if (row.isTerminal) {
    return {
      label: "final summary",
      text: shouldExpectDispatchOutcomeSummary(row)
        ? "No final summary reported yet."
        : row.detailText || "Terminal outcome recorded.",
    };
  }

  return {
    label: row.detailLabel || "status",
    text: row.detailText || "Outcome still pending.",
  };
}

export function shouldExpectDispatchOutcomeSummary(
  row: DispatchComparisonRow,
) {
  return row.created && row.isTerminal;
}

export function hasCreatedDispatchResults(results: MultiTeamDispatchResult[]) {
  return results.some((item) => item.status === "created" && Boolean(item.taskId));
}

export function shouldPollDispatchMonitor(
  entries: DispatchMonitorEntry[],
  results: MultiTeamDispatchResult[],
  isLoading: boolean,
) {
  if (isLoading || !hasCreatedDispatchResults(results)) {
    return false;
  }
  return entries.length === 0 || entries.some((entry) => !isTerminalDispatchStatus(entry.status));
}

export function resolveDispatchRolePlan(
  mode: DispatchTemplateMode,
  index: number,
): DispatchRolePlan {
  if (mode === "mirror") {
    return {
      deliverables: [],
      inputHints: [],
      key: "mirror",
      label: "Mirror",
    };
  }

  const roles: DispatchRolePlan[] = [
    {
      deliverables: ["review-notes.md"],
      goalInstruction:
        "Primary lens: review the task for architecture risks, regressions, missing constraints, and edge cases before proposing changes.",
      inputHints: ["focus:risk-review", "focus:architecture", "focus:regressions"],
      key: "review",
      label: "Review",
      strategySuffix: "review",
      teammateProfileSuffix: "risk reviewer and architecture critic",
    },
    {
      deliverables: ["implementation-plan.md", "patch-summary.md"],
      goalInstruction:
        "Primary lens: turn the task into the most direct implementation path, including concrete change shape and execution order.",
      inputHints: ["focus:implementation", "focus:patch-shape", "focus:delivery"],
      key: "implement",
      label: "Implement",
      strategySuffix: "implement",
      teammateProfileSuffix: "direct implementation worker",
    },
    {
      deliverables: ["verification-report.md", "test-matrix.md"],
      goalInstruction:
        "Primary lens: validate correctness with tests, failure modes, rollout checks, and runtime verification criteria.",
      inputHints: ["focus:verification", "focus:test-plan", "focus:failure-modes"],
      key: "verify",
      label: "Verify",
      strategySuffix: "verify",
      teammateProfileSuffix: "verification and QA specialist",
    },
  ];

  return roles[index % roles.length];
}
