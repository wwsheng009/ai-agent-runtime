import {
  type RuntimePathClaimConflict,
  type RuntimePathClaimRecord,
  type RuntimeTaskGraphResponse,
  type RuntimeTeamEventRecord,
  type RuntimeTeamMailboxMessage,
  type RuntimeTeamSummaryEntry,
  type RuntimeTeamTask,
  type RuntimeTeammateRecord,
} from "@/lib/runtime-api";

export type TeamDetailsState = {
  events: RuntimeTeamEventRecord[];
  finalSummary: string;
  graph: RuntimeTaskGraphResponse | null;
  mailbox: RuntimeTeamMailboxMessage[];
  pathClaims: RuntimePathClaimRecord[];
  tasks: RuntimeTeamTask[];
  teammates: RuntimeTeammateRecord[];
};

export type ClaimCheckState = {
  conflicts: RuntimePathClaimConflict[];
  ok: boolean;
} | null;

export type MultiTeamDispatchResult = {
  error?: string;
  status: "created" | "failed";
  taskId?: string;
  teamId: string;
};

export type DispatchTeamReadiness = {
  executable: boolean;
  reason: string;
  runnableTeammates: number;
  totalTeammates: number;
};

export type DispatchMonitorEntry = {
  assignee?: string | null;
  error?: string;
  lastEventType?: string;
  mailboxPreview: string[];
  status: string;
  summary?: string;
  taskId: string;
  teamId: string;
  updatedAt?: string;
};

export type DispatchComparisonRow = {
  assignee?: string | null;
  created: boolean;
  detailLabel: string;
  detailText: string;
  error?: string;
  hasMonitor: boolean;
  isTerminal: boolean;
  lastEventType?: string;
  mailboxPreview: string[];
  outcomeKey: string;
  outcomeLabel: string;
  summaryLabel?: string;
  status: string;
  summary?: string;
  taskId?: string;
  teamId: string;
  updatedAt?: string;
};

export type DispatchBatchSummary = {
  activeCount: number;
  attemptedCount: number;
  completedCount: number;
  createdCount: number;
  failedCount: number;
  finalSummaryCount: number;
  latestUpdatedAt?: string;
  monitorCoverageCount: number;
  pendingCount: number;
  summaryEligibleTerminalCount: number;
  statusCounts: Record<string, number>;
  terminalCount: number;
  terminalWithoutSummaryCount: number;
  terminalRows: DispatchComparisonRow[];
};

export type DispatchOutcomeNarrative = {
  label: string;
  text: string;
};

export type DispatchTemplateMode = "mirror" | "review_implement_verify";

export type DispatchRolePlan = {
  deliverables: string[];
  goalInstruction?: string;
  inputHints: string[];
  key: string;
  label: string;
  strategySuffix?: string;
  teammateProfileSuffix?: string;
};

export function getSummaryCount(
  summary: RuntimeTeamSummaryEntry | undefined,
  group: "tasks" | "teammates",
  key: string,
) {
  if (!summary) {
    return 0;
  }
  return summary[group].counts?.[key] ?? 0;
}

export function truncateIdentifier(value: string | undefined, length = 14) {
  if (!value) {
    return "";
  }
  if (value.length <= length) {
    return value;
  }
  return `${value.slice(0, length)}...`;
}

export function statusTone(value: string | undefined) {
  switch ((value || "").toLowerCase()) {
    case "running":
    case "busy":
      return "border-[#8fd0c6]/24 bg-[#8fd0c6]/10 text-[#8fd0c6]";
    case "done":
    case "idle":
      return "border-[#f0c77b]/24 bg-[#f0c77b]/10 text-[#f0c77b]";
    case "failed":
    case "blocked":
    case "offline":
      return "border-[#f59e7d]/24 bg-[#f59e7d]/10 text-[#f59e7d]";
    default:
      return "border-white/10 bg-white/6 text-[var(--muted-foreground)]";
  }
}

export function sortTasks(tasks: RuntimeTeamTask[]) {
  return [...tasks].sort((left, right) => {
    const leftPriority = left.priority ?? 0;
    const rightPriority = right.priority ?? 0;
    if (leftPriority !== rightPriority) {
      return rightPriority - leftPriority;
    }
    return (left.title || left.id).localeCompare(right.title || right.id);
  });
}

export function sortTeammates(teammates: RuntimeTeammateRecord[]) {
  return [...teammates].sort((left, right) =>
    (left.name || left.id).localeCompare(right.name || right.id),
  );
}

export function sortEvents(events: RuntimeTeamEventRecord[]) {
  return [...events].sort((left, right) => right.seq - left.seq);
}

export function sortMailbox(messages: RuntimeTeamMailboxMessage[]) {
  return [...messages].sort((left, right) => {
    const leftTime = left.created_at ? new Date(left.created_at).getTime() : 0;
    const rightTime = right.created_at ? new Date(right.created_at).getTime() : 0;
    return rightTime - leftTime;
  });
}

export function sortPathClaims(claims: RuntimePathClaimRecord[]) {
  return [...claims].sort((left, right) => {
    const leftLease = left.lease_until
      ? new Date(left.lease_until).getTime()
      : Number.POSITIVE_INFINITY;
    const rightLease = right.lease_until
      ? new Date(right.lease_until).getTime()
      : Number.POSITIVE_INFINITY;
    if (leftLease !== rightLease) {
      return rightLease - leftLease;
    }
    return left.path.localeCompare(right.path);
  });
}

export function prettyEventType(value: string) {
  return value.replaceAll(".", " / ");
}

export function createEmptyDetails(): TeamDetailsState {
  return {
    events: [],
    finalSummary: "",
    graph: null,
    mailbox: [],
    pathClaims: [],
    tasks: [],
    teammates: [],
  };
}

export function isClaimActive(leaseUntil: string | undefined) {
  if (!leaseUntil) {
    return true;
  }
  const value = new Date(leaseUntil).getTime();
  if (Number.isNaN(value)) {
    return true;
  }
  return value >= Date.now();
}

export function describeMailboxRoute(message: RuntimeTeamMailboxMessage) {
  const from = message.from_agent?.trim() || "system";
  const rawTo = message.to_agent?.trim();
  const to = !rawTo || rawTo === "*" ? "broadcast" : rawTo;
  const parts = [`from ${from}`, `to ${to}`];
  if (message.task_id) {
    parts.push(`task ${truncateIdentifier(message.task_id, 12)}`);
  }
  const metadataCount = Object.keys(message.metadata ?? {}).length;
  if (metadataCount > 0) {
    parts.push(`${metadataCount} meta`);
  }
  return parts.join(" · ");
}

export function parsePathLines(value: string) {
  return value
    .split(/\r?\n|,/)
    .map((entry) => entry.trim())
    .filter(Boolean);
}

export function uniqueStrings(values: string[]) {
  return [...new Set(values.map((value) => value.trim()).filter(Boolean))];
}

export function normalizeTaskTitle(title: string, goal: string) {
  const trimmedTitle = title.trim();
  if (trimmedTitle) {
    return trimmedTitle;
  }
  const trimmedGoal = goal.trim();
  if (!trimmedGoal) {
    return "";
  }
  return trimmedGoal.length > 72 ? `${trimmedGoal.slice(0, 69)}...` : trimmedGoal;
}

export function summarizeConflict(conflict: RuntimePathClaimConflict) {
  return [
    `request ${conflict.path}`,
    `existing ${conflict.existing_path}`,
    `owner ${truncateIdentifier(conflict.existing_owner, 14)}`,
    `task ${truncateIdentifier(conflict.existing_task_id, 14)}`,
    `mode ${conflict.existing_mode}`,
  ].join(" · ");
}

export function sortDispatchMonitor(entries: DispatchMonitorEntry[]) {
  return [...entries].sort((left, right) => {
    const leftUpdated = left.updatedAt ? new Date(left.updatedAt).getTime() : 0;
    const rightUpdated = right.updatedAt ? new Date(right.updatedAt).getTime() : 0;
    return rightUpdated - leftUpdated;
  });
}

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

export function describeEventPayload(event: RuntimeTeamEventRecord) {
  const payload = event.payload ?? {};
  const stringKeys = [
    "summary",
    "title",
    "task_id",
    "teammate_id",
    "assignee",
    "from_agent",
    "to_agent",
    "message_id",
    "workspace_id",
  ] as const;

  for (const key of stringKeys) {
    const raw = payload[key];
    if (typeof raw === "string" && raw.trim()) {
      return `${key}: ${raw.trim()}`;
    }
  }

  if (typeof payload.count === "number") {
    return `count: ${payload.count}`;
  }

  const keys = Object.keys(payload);
  if (keys.length === 0) {
    return "no payload";
  }

  return keys.slice(0, 3).join(", ");
}
