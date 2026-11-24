// 由 components/workspace/runtime-teams/shared.ts 机械拆分而来（P0-2），仅搬迁不改语义。
// 团队详情状态辅助（计数 / 空态 / 租约活跃判断 / 路径与标题归一）

import {
  type RuntimeTeamEventRecord,
  type RuntimeTeamFinalSummaryResponse,
  type RuntimeTeamSummaryEntry,
  type RuntimeTeamSummaryState,
} from "@/lib/runtime-api";
import { type TeamDetailsState } from "@/components/workspace/runtime-teams/shared/types";

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

export function createEmptyDetails(): TeamDetailsState {
  return {
    events: [],
    eventCursor: 0,
    latestEventSeq: 0,
    liveTeamStatus: "",
    liveSummary: undefined,
    finalSummary: "",
    summaryState: "unknown",
    graph: null,
    mailbox: [],
    pathClaims: [],
    tasks: [],
    teammates: [],
    preflight: null,
    isStale: false,
  };
}

export function mergeTeamFinalSummary(
  current: Pick<TeamDetailsState, "finalSummary" | "summaryState">,
  response?: RuntimeTeamFinalSummaryResponse,
) {
  if (!response) {
    return {
      finalSummary: current.finalSummary,
      summaryState: current.summaryState,
    };
  }
  const finalSummary = response.summary?.trim() || "";
  const summaryState =
    response.summary_state ??
    (finalSummary ? "ready" : "unknown");
  const keepLastKnownGood =
    !finalSummary && summaryState !== "ready" && summaryState !== "not_applicable";
  return {
    finalSummary:
      finalSummary || (keepLastKnownGood ? current.finalSummary : ""),
    // An older Runtime may omit summary_state. An empty/unknown response is
    // not authoritative enough to erase a known-good summary or its state.
    summaryState:
      summaryState === "unknown" && current.finalSummary
        ? current.summaryState
        : summaryState,
  };
}

export function mergeTeamEvents(
  existing: RuntimeTeamEventRecord[],
  incoming: RuntimeTeamEventRecord[],
) {
  const bySeq = new Map<number, RuntimeTeamEventRecord>();
  for (const event of existing) {
    bySeq.set(event.seq, event);
  }
  for (const event of incoming) {
    if (!bySeq.has(event.seq)) {
      bySeq.set(event.seq, event);
    }
  }
  return [...bySeq.values()].sort((left, right) => right.seq - left.seq);
}

const TERMINAL_TEAM_STATUSES = new Set([
  "done",
  "failed",
  "partially_completed",
  "canceled",
  "cancelled",
  "completed",
  "terminal",
]);

const OBSERVABLE_TEAM_STATUSES = new Set([
  "active",
  "pending",
  "ready",
  "running",
  "starting",
  "preflight",
  "reconciling",
  "paused",
]);

export function isTerminalTeamStatus(teamStatus: string | undefined) {
  return TERMINAL_TEAM_STATUSES.has(teamStatus?.trim().toLowerCase() ?? "");
}

/**
 * The details surface is a read-only observation window.  It may poll a
 * running Team, and it may make a short number of summary reads after a
 * terminal Team.  It never infers or changes the Team status locally.
 */
export function shouldPollTeamDetails(
  teamStatus: string | undefined,
  summaryState: RuntimeTeamSummaryState,
  terminalPollCount = 0,
) {
  const status = teamStatus?.trim().toLowerCase() ?? "";
  if (OBSERVABLE_TEAM_STATUSES.has(status)) {
    return true;
  }
  if (isTerminalTeamStatus(status)) {
    return (
      (summaryState === "pending" || summaryState === "unknown") &&
      terminalPollCount < 8
    );
  }
  return false;
}

/**
 * Keep active polling bounded and give a terminal summary a small, faster
 * observation window.  A caller can reset consecutivePolls when a new event
 * arrives or a request succeeds after an error.
 */
export function getTeamDetailsPollDelay(
  teamStatus: string | undefined,
  summaryState: RuntimeTeamSummaryState,
  consecutivePolls: number,
) {
  const status = teamStatus?.trim().toLowerCase() ?? "";
  if (isTerminalTeamStatus(status)) {
    return 2_000;
  }
  if (summaryState === "pending") {
    return 1_000;
  }
  const exponent = Math.min(Math.max(consecutivePolls, 0), 3);
  return 1_000 * 2 ** exponent;
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
